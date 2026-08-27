// match_step.go — 全匹配的【sqlite3_step 式】原语: C 侧一次填一批命中进一块批缓冲,
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
// 降到 O(一批 × 并发)。
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
// ── 批缓冲从哪来 (2026-08-26 第二版: 库内 sync.Pool, 调用方不再持有工作区) ──────────────
// 第一版的形状是 StepAll…(st *MatchStep_t, …) —— 调用方自己持有一块工作区跨调用复用。
// 它在"调用方本来就有个 per-scan 的壳可以挂"时是零开销, 但代价是【没有壳的调用方会写成
// 函数内 var st MatchStep_t】, 而那正好是最差的一种: 进 C 之前必须无条件先备好缓冲
// (Go 侧拿不到"这次有没有命中"的先验), 于是每次调用白付一笔 ~200B 的 make,【命不命中都付】。
// 而 FindAll* 在无命中那条路上几乎不花钱 (12B/2 笔) ⟹ 扫描型负载 (一张规则表挨个打同一份正文,
// 绝大多数调用是 miss) 换成 step 之后字节数反而涨。调用方产品实测: 8.2MB 档 920.4M → 922.7M,
// 对象数倒是降了 2 万 —— 典型的"对象少了、字节多了"两头不靠。
//
// 所以缓冲改成【库内一个 sync.Pool 持有, 每次调用借一块、返回前还回去】:
//   · 调用方一个工作区都不用持有, API 从三个参数变两个, 也就不存在"写成 var st"这种最差用法;
//   · 每块的尺寸【与 per / 命中数 / 正文长度全无关】, 恒是 stepBufInts 个 int32 (4KB),
//     常驻是 O(4KB × 并发度) —— 与被否掉的 Append*Flat (O(历史最大命中数 × 并发) 且只涨不缩)
//     完全不是一回事;
//   · 一个 Get/Put 来回 9.5ns · 零分配 (BenchmarkX_syncPoolRoundtrip);
//   · 顺带把嵌套调用变安全了 —— batchFn 里再起一条 step 扫描各借各的块, 老形状共用一个 st 会就地
//     互相覆写。
// 四方对拍与定案理由见 zexp_step_alloc_bench_test.go 顶部 (那里的变体 E 就是现在这条主线)。
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
	"sync"
	"unsafe"
)

// C.int 必须是 4 字节: 本文件把 Go 的 []int32 缓冲直接交给 C 当 int* 写, 零拷贝的前提就是这个。
// 两条方向相反的静态断言 —— sizeof 不等于 4 时其中一条会算出负数, 转 uint 直接编译失败。
const (
	_ = uint(unsafe.Sizeof(C.int(0)) - 4)
	_ = uint(4 - unsafe.Sizeof(C.int(0)))
)

// stepBufInts 是一块批缓冲的尺寸, 按【int32 个数】算 —— 不是按"几处匹配"算。
//
// 为什么按字节数定而不是按处数定: 池子里每块必须【尺寸一模一样】, 才谈得上"借还"。按处数定的话
// 一块的大小是 处数 × per × 4B, 而 per = 2*(子组数+1) 是跟着正则走的 (本树里 2 ~ 20 都有),
// 同一个池子里就会混进大小差十倍的块, 借到小的还得重开、还回去的又把大的钉住 —— 那就是
// Append*Flat 那种只涨不缩的 ratchet, 正是要躲开的东西。按 int32 数定死, 池子里块块同尺寸,
// 一块的一生就一次 make。
//
// 为什么是 1024 (=4KB): 一批装 1024/per 处 —— group0 版 (per=2) 512 处, 典型子组版 (per=6) 170 处。
// 🔴 这个数是【量出来的, 不是拍的】: 先试的 256 (=1KB), 调用方产品 16MB 档实测 CPU 9.01s → 9.13~9.33s
// (+2%), 三次全在基线之上; 换成 1024 就回到 9.00~9.12s = 与第一版两段式打平。原因是过一次
// cgo 桥约 20ns 而本树真实的 per 是 6~10, 1KB 一批只装得下三四十处 —— 命中几十上百处的调用点
// (窗口化凭据表 / 媒体跨度 / 客户数据值) 的过境次数直接乘三。4KB 这一档正好把典型 per 的一批
// 拉回一两百处, 摊到每处远低于一次 RE2::Match 的百纳秒。再往上没有收益, 只是白抬常驻。
// (常驻仍是 O(4KB × 并发度) —— 20 个 P 也就 80KB, 与命中数无关, 这是与 Append*Flat 的分界。)
// 🔴 第一版那套"首批 8 处、装满了再长到 128 处"的两段式已删: 有了池子, 一块的 make 一辈子只发生
// 一次(还是在池子里发生的), 首批开小省的那点东西不存在了, 而两段式要多一个 st.fixedCap 分支、
// 一条"换大批"的跨批路径和一个只测得到它的判据 —— 净是复杂度。
const stepBufInts = 1024

// stepBufPool 持有批缓冲。存 *[]int32 而不是 []int32: 切片头进 interface 要装箱 (每次 Put 一笔
// 分配), 指针进 interface 不用 —— 这个池子的全部意义就是零分配, 装箱会把它一笔勾销。
var stepBufPool = sync.Pool{New: func() any { b := make([]int32, stepBufInts); return &b }}

// StepAllStringSubmatchIndex 把 re 在 s 上前 n 处匹配 (n<0 = 全部) 分批交给 batchFn。
//
// flat 的布局与 FindAllStringSubmatchIndex 的单行【逐字相同】:
//
//	per = 2*(re.NumSubexp()+1); 本批第 k 处 = flat[k*per : (k+1)*per]; 未参与的组是 -1,-1。
//
// per 由调用方拿 re.NumSubexp()+1 现算 —— 不在回调里带 count/per (那就又是要传的结构),
// 调用方本来就知道自己那条正则。
//
// 🔴 flat 只在本次回调内有效: 下一批就地覆写同一块内存, 而且【本次调用一返回, 这块就还回池子了】,
// 别人下一次 step 会写它。要留存请自己 copy。
// batchFn 返回 false = 提前停, 剩下的正文不再扫 (这是一次性 API 做不到的事)。
// 无匹配: batchFn 一次都不调, 且【全程零 Go 堆分配】(缓冲是借的, 不是现开的)。
//
// 匹配集合 / 顺序 / 空匹配去重推进与 FindAllStringSubmatchIndex 逐处相同 —— 同一段 C 循环,
// 差别只有结果落在哪、以及是不是一次吐完。对拍门见 match_step_test.go。
func (re *Regexp) StepAllStringSubmatchIndex(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAll(s, n, re.numSubexp+1, 0, batchFn)
}

// StepAllStringIndex 同 StepAllStringSubmatchIndex, 但只回填 group0 (per = 2,
// 本批第 k 处 = flat[2k], flat[2k+1])。
//
// 不是"取子组版的前两个"那么简单: nmatch=1 让 C 侧的 vector<StringPiece> 也从 numSubexp+1
// 缩到 1, 每处匹配少填 numSubexp 组区间。只要子组 (AppendAllStringIndexFlat 原来的场景)
// 就用这个。
func (re *Regexp) StepAllStringIndex(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAll(s, n, 1, 0, batchFn)
}

// stepAll 是上面两个的共同内核。
//
//	nmatch   = 要回填几组 (1 = 只 group0)
//	fixedCap > 0 时把批容量写死为这么多【处】, 只给对拍门用 —— 批一大就一次装完, 跨批携带
//	         pos/prevEnd 的那条路径 (整件事里唯一会静默出错的地方) 就永远测不到, 所以判据要能把
//	         批边界切在任意一处命中上 (batch=1/2/3)。生产入口一律传 0。
//	         (同一招见 findallindex.go 的 newFindAllIndexAlloc(s, batch) —— 树内既有先例。)
func (re *Regexp) stepAll(s string, n int, nmatch int, fixedCap int, batchFn func(flat []int32) bool) {
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
	capM := fixedCap
	if capM <= 0 {
		capM = stepBufInts / per
		if capM < 1 {
			capM = 1 // per 比一整块还大 (子组多到 128 个以上): 一批就装一处
		}
	}
	var buf []int32
	if need := capM * per; need <= stepBufInts {
		p := stepBufPool.Get().(*[]int32)
		defer stepBufPool.Put(p)
		buf = (*p)[:need]
	} else {
		buf = make([]int32, need) // 装不进标准块 ⟹ 现开一块, 且【不进池】(见 stepBufInts 的红字)
	}
	tp := strBytePtr(s)
	pos := C.int(0)
	prevEnd := C.int(-1)
	left := n
	for {
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
	}
}
