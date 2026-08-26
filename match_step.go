// match_step.go — 全匹配的【sqlite3_step 式】原语: C 侧一次填一批命中进调用方自己的缓冲,
// Go 侧取走这批、再 step 下一批, 直到扫完。内存里【从来没有全部命中信息】, 只有一批。
//
// 为什么要它 (2026-08-26 · 接 doc/plan12/20260826_213re2.txt):
// 老路 FindAll* / AppendAllStringIndexFlat 一次调用要在三层各付一笔 ∝ 命中数的账 ——
//   ① C 侧 cre2_match_all 的 std::vector<int> acc 逐处 push_back, 扫完再 malloc 一整块拷过去,
//      峰值是整张命中表的【两份】(纯 RSS, Go profile 上根本看不见);
//   ② Go 侧 matchAllFlat 的 flat = make([]int, count*2*nmatch), 把 ① 整块再拷进 Go 堆;
//   ③ 外壳 make([][]int, count) / make([]string, count) / 每处一个 []string。
// Append*Flat 形态只干掉了 ②③, 而且是拿【live-max】换的: dst 这块复用缓冲会涨到历史最大命中数
// 就再也不缩, 挂在 plan/pool 上 × 并发度常驻 —— 累计分配好看了, 常驻反而变成常态。① 一分没省。
//
// step 形态把三层全部干掉: C 直接写进 Go 缓冲 (无 vector · 无 malloc · 全程零次全量拷贝),
// 缓冲大小固定为一批, 与命中数和正文长度都无关。live-max 从 O(命中数 × per × 并发)
// 降到 O(一批 × per × 并发)。
//
// 挂起为什么不需要 native 对象: 单条 Regexp 的每处匹配本来就是一次独立的
// RE2::Match(full, pos, …) —— DFA 状态与 cache 锁都在那一次 Match 内部生灭。所以"挂起"就是把
// pos/prevEnd 两个 int 交回 Go, 下次原样传回来。没有 C 对象、没有 finalizer、没有 Close、
// 没有 cgo handle 生命周期、没有"这个工作区是不是这条 re 的"检查。
// (对比 RegexpSet 的 findallindex.go: 那边挂起的是一个扫到一半的【连续 DFA】, 三个 int 描述不了,
//  才不得不有 native 挂起点 + finalizer + Close —— 那套复杂度是 set 扫描逼出来的, 不是 step 固有的。)
//
// cgo 过境次数 = ceil(命中数/批容量) + 1; 无匹配就 1 次, 与老路完全相同 (逐处匹配的循环整段留在 C 内)。
//
// 🔴 边界: step【不取代】FindAll* —— 那一族的契约就是"一次吐完个数组", 而"先在 C 里数好个数再
// 一次精确 malloc"正是该契约下的最优解。拿 step 去物化 FindAll* 实测是净亏 (Go append 阶梯累计
// 收敛到 5N: 20000 处命中 1.45MB/4 笔 → 5.70MB/26 笔, CPU +17%, 见
// BenchmarkFindAllSub_matAll_vs_step)。所以分工是: 不需要全部物化 ⇒ step; 就是要数组 ⇒ FindAll*。
// 被这条边界挤掉的是中间那种【半物化】形态 (AppendAllStringIndexFlat): 它既没省下物化,
// 又要背一块 ∝ 命中数的常驻 ratchet 缓冲, 两头不靠 —— 所以它被删, 而 FindAll* 不动。
//
// 同题实测 (5900X · (\w+)=(\w+) · per=6 · 64 字节一处命中):
//   1MB 正文 · 子组版: FindAllStringSubmatchIndex 7.55ms / 1,179,668 B / 4 笔
//                   → StepAllStringSubmatchIndex 7.37ms / 0 B / 0 笔   (CPU -2.4%)
//   1MB 正文 · group0: AppendAllStringIndexFlat  4.44ms /       873 B / 0 笔
//                   → StepAllStringIndex          4.35ms / 0 B / 0 笔   (CPU -2.0%)
//   miss 路径:        FindAllStringSubmatchIndex  598ns /  12 B / 2 笔
//                   → StepAllStringSubmatchIndex  557ns /   0 B / 0 笔  (CPU -6%)
// (这里量的只是 Go 堆; C 侧那份 vector+malloc 的峰值消除不进 Go profile, 要 RSS/massif 才看得见。)
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// C.int 必须是 4 字节: 本文件把 Go 的 []int32 缓冲直接交给 C 当 int* 写, 零拷贝的前提就是这个。
// 两条方向相反的静态断言 —— sizeof 不等于 4 时其中一条会算出负数, 转 uint 直接编译失败。
const (
	_ = uint(unsafe.Sizeof(C.int(0)) - 4)
	_ = uint(4 - unsafe.Sizeof(C.int(0)))
)

// 批容量按【处】数算 (缓冲的 int32 数 = 批容量 × per)。
//
// 为什么首批这么小: 扫描型负载里绝大多数调用是 miss 或个位数命中 (一张规则表 × 一堆正文),
// 为它们预付一块大缓冲是纯浪费。所以首批只开 stepBatchFirst 处; 只有真的被填满过
// (说明这条路确实是多命中) 才一次性长到 stepBatchMatches 处, 之后不再变。
//
// 为什么大批停在 128: 过桥约 50~100ns, 128 处摊下来 <1ns/处, 早已淹没在每处 RE2::Match 的
// 百纳秒量级里, 再往大调不再有收益, 只是白白抬高 live-max。128 × per(典型 4~8) × 4B = 2~4KB。
const (
	stepBatchFirst   = 8
	stepBatchMatches = 128
)

// MatchStep_t 是调用方持有并复用的工作区 —— 它【只是一块 []int32】。
//
// 零值即可用; 非线程安全, 并发各持一个。构造费为零 (缓冲惰性长出, 且首批只有几百字节),
// 所以放不放 sync.Pool 都无所谓 —— 这一点是刻意的: findallindex.go 那种带 native 挂起点 +
// finalizer 的工作区一旦被误当成"随手 new 一个"就会很贵 (真出过事, 见 asc 的
// sd_body_gate_span_pool.go 头注), 本类型从根上没有那个成本。
//
// 同一个 MatchStep_t 可以跨不同 re 复用: 容量按 int32 数持有, 每次按本条 re 的 per 换算能装几处。
type MatchStep_t struct {
	buf []int32
	// fixedCap > 0 时强制批容量为这么多【处】, 且不再自动长大。只给对拍门用 ——
	// 批一大就一次装完, 跨批携带 pos/prevEnd 的那条路径永远测不到, 所以测试要能把批边界
	// 切在任意一处命中上 (batch=1/2/3)。构造入口 newMatchStepFixed, 见 match_step_test.go。
	// (同一招见 findallindex.go 的 newFindAllIndexAlloc(s, batch) —— 树内既有先例。)
	fixedCap int
}

// newMatchStepFixed 造一个批容量写死为 batchMatches 处的工作区。仅对拍门用, 不导出。
func newMatchStepFixed(batchMatches int) *MatchStep_t {
	return &MatchStep_t{fixedCap: batchMatches}
}

// StepAllStringSubmatchIndex 把 re 在 s 上前 n 处匹配 (n<0 = 全部) 分批交给 batchFn。
//
// flat 的布局与 FindAllStringSubmatchIndex 的单行【逐字相同】:
//
//	per = 2*(re.NumSubexp()+1); 本批第 k 处 = flat[k*per : (k+1)*per]; 未参与的组是 -1,-1。
//
// per 由调用方拿 re.NumSubexp()+1 现算 —— 不在回调里带 count/per (那就又是要传的结构),
// 调用方本来就知道自己那条正则。
//
// 🔴 flat 只在本次回调内有效: 下一批就地覆写同一块内存。要留存请自己 copy。
// batchFn 返回 false = 提前停, 剩下的正文不再扫 (这是一次性 API 做不到的事)。
// 无匹配: batchFn 一次都不调。
//
// 匹配集合 / 顺序 / 空匹配去重推进与 FindAllStringSubmatchIndex 逐处相同 —— 同一段 C 循环,
// 差别只有结果落在哪、以及是不是一次吐完。对拍门见 match_step_test.go。
func (re *Regexp) StepAllStringSubmatchIndex(st *MatchStep_t, s string, n int, batchFn func(flat []int32) bool) {
	re.stepAll(st, s, n, re.numSubexp+1, batchFn)
}

// StepAllStringIndex 同 StepAllStringSubmatchIndex, 但只回填 group0 (per = 2,
// 本批第 k 处 = flat[2k], flat[2k+1])。
//
// 不是"取子组版的前两个"那么简单: nmatch=1 让 C 侧的 vector<StringPiece> 也从 numSubexp+1
// 缩到 1, 每处匹配少填 numSubexp 组区间。只要子组 (AppendAllStringIndexFlat 原来的场景)
// 就用这个。
func (re *Regexp) StepAllStringIndex(st *MatchStep_t, s string, n int, batchFn func(flat []int32) bool) {
	re.stepAll(st, s, n, 1, batchFn)
}

// stepAll 是上面两个的共同内核。nmatch = 要回填几组 (1 = 只 group0)。
func (re *Regexp) stepAll(st *MatchStep_t, s string, n int, nmatch int, batchFn func(flat []int32) bool) {
	if len(s) > maxCInt { // 超 C.int 的输入直接当无匹配 (同 matchAllFlat 守卫)
		return
	}
	if n < 0 {
		n = len(s) + 1 // 与 FindAll* 一致: 含逐字节空匹配的最大可能匹配数
	}
	if n == 0 {
		return
	}
	per := 2 * nmatch
	capM := st.fixedCap      // 对拍门: 批容量写死, 且下面不自动长大
	if capM <= 0 {
		capM = cap(st.buf) / per // 现有缓冲按本条 re 的 per 能装几处
		if capM < stepBatchFirst {
			capM = stepBatchFirst
		}
	}
	if cap(st.buf) < capM*per {
		st.buf = make([]int32, capM*per)
	}
	tp := strBytePtr(s)
	pos := C.int(0)
	prevEnd := C.int(-1)
	left := n
	for {
		buf := st.buf[:capM*per]
		r := C.cre2_match_all_step(re.h, tp, C.int(len(s)), C.int(nmatch), C.int(left),
			pos, prevEnd, (*C.int)(unsafe.Pointer(&buf[0])), C.int(capM))
		runtime.KeepAlive(s)
		runtime.KeepAlive(re)
		if r.rc <= 0 {
			return // 参数不合法: 当无匹配 (同 matchAllFlat 对 rc<=0 的处理)
		}
		cnt := int(r.nmatches)
		if cnt > 0 {
			if !batchFn(buf[:cnt*per]) {
				return // 调用方要求提前停
			}
			left -= cnt
		}
		if r.done != 0 || left <= 0 {
			return
		}
		pos, prevEnd = r.pos, r.prevEnd
		// 这一批被填满 ⇒ 这条路确实是多命中, 一次性长到大批, 之后不再变。
		if st.fixedCap <= 0 && cnt == capM && capM < stepBatchMatches {
			st.buf = make([]int32, stepBatchMatches*per)
			capM = stepBatchMatches
		}
	}
}
