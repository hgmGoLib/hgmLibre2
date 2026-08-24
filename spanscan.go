// spanscan.go — RegexpSet 的【流式游程扫描】: 一遍扫正文, 边扫边说"哪条 pattern 命中在哪"。
//
// 与 (*RegexpSet).Match 的分工:
//   Match     只回答"哪几条命中"。要位置的调用方拿到 index 之后, 只能对那几条各跑一次
//             FindStringIndex —— 每跑一次就是【又扫一遍整篇正文】, 而且用的是非锚定正则:
//             `.*?` 前缀让"哪个位置能当匹配起点"变成"每个位置都能", 状态数对计数上界指数增长
//             (见 doc/状态数为什么会相乘.txt: 同一条 pattern 加个 \b 就是 967 倍的差距)。
//   ScanSpans 直接把位置吐出来。调用方要再跑正则就只跑【命中那一段】, 而且可以写成锚定的
//             (\A(?:pat)) —— 起点只剩一个, 那套状态爆炸的机制从根上就不存在了。
//
// ── 吐出来的是什么 ──────────────────────────────────────────────────────────
// 吐的是【游程】(Index, Lo, Hi), 不是逐个位置。kManyMatch 的 DFA 在每一个能结束匹配的位置
// 都会报一次, 所以一条可变长 pattern 在一段正文上会连出一串位置 (`[a-z]{3,}` 撞上 "abcdef"
// 会在 3/4/5/6 各报一次)。按 pattern 把连号的收敛成一段再吐, 省带宽也省调用方的活。
//
//	正向 set (NewRegexpSet)        : 位置 = 匹配【右端】的偏移 (不含), 即 text[..pos) 是一个匹配
//	反向 set (NewRegexpSetReverse*) : 位置 = 匹配【左端】的偏移 (含), 即匹配从 text[pos] 开始
//
// 🔴 收敛【两端都给】所以无信息损失: 展开 [Lo,Hi] 就还原成逐个位置。这一点不能省 ——
//    只留一端 (比如只留最右那个 end) 会把两个真实独立的匹配悄悄并成一个: `ab|c` 撞上
//    "abc" 的两个 end 是 2 和 3, 连号, 只留 3 就把 [0,2) 这个匹配弄丢了, 而且【不报错】。
//
// 🔴 顺序【不保证】全局按位置升序。一段游程要等"这条 pattern 再次命中且与上次不连号"或者
//    "整篇扫完"才收口, 所以不同 pattern 的游程会交错, 最后一批还会在扫完时集中吐出来。
//    要有序的调用方自己排 —— 排的是游程条数 (通常个位数), 不是位置数。
//
// 🔴 语义不是 FindAllStringIndex。FindAll 给的是 leftmost-first 的【不重叠】匹配序列;
//    这里给的是"所有 pattern 的所有匹配端点", 重叠的也在里面 (`abcd|bc` 撞 "abcd" 两条都报)。
//    要 FindAll 语义的调用方拿位置之后自己做取舍 (优先级贪心 / 相交即丢 / …), 那本来就是
//    调用方的业务规则, 库这一层不替它决定。
//
// ── 为什么是"取一批再要一批"而不是一次吐完 ────────────────────────────────────
// 游程条数没有上界, native 侧要么无界攒内存, 要么"缓冲不够就扩容重跑"—— 重跑要付的正是
// 最贵的那一遍 (新正文现造 DFA 状态, 实测比命中缓存的重复扫慢 66 倍), 是性能炸弹。
// 所以攒满一批就【挂起】: 按内容存下当前 DFA 状态, 放掉 DFA 的缓存读锁, 返回给 Go;
// 取走之后再进去, 重新拿锁、按内容把状态查回来接着扫。挂起期间一把锁都不持有 ——
// 反过来说, 如果做成"C 直接回调进 Go", 回调期间还攥着读锁, 谁想 flush 谁就得等 Go 跑完。
//
// 生命周期: SpanScanner 是可复用工作区 (native 侧的游程表跟着它, 不是跟着每次扫描),
// 【不是并发安全的】—— 一个 goroutine 一个, 别几个 goroutine 共用一个 scanner。
// 同一个 RegexpSet 上开多个 scanner 并发扫是可以的 (Set 本身只读)。
package hgmLibre2

/*
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
	"unsafe"
)

// SetSpan 是一段游程: 第 Index 条 pattern 的命中位置落在 [Lo, Hi] 里的【每一个】值上
// (原文字节偏移, 两端都含, 恒 Lo <= Hi)。位置是"右端"还是"左端"取决于 set 的方向, 见文件头。
//
// ⚠ 字段布局必须是紧挨着的三个 int32 —— native 侧直接往这块内存写三元组, 不做逐条转换。
// 下面的 spanSizeAssert 把这条约束钉在编译期。
type SetSpan struct {
	Index int32
	Lo    int32
	Hi    int32
}

// spanSizeAssert: SetSpan 必须正好 3 个 int32 (12 字节无补位), 否则 native 写进来的三元组
// 会错位。数组长度为负 = 编译不过。
type spanSizeAssert [unsafe.Sizeof(SetSpan{}) - 12]byte

// SpanScanner 是流式扫描的可复用工作区 (native 侧游程表 + 挂起点 + 一批输出缓冲)。
// 用 (*RegexpSet).NewSpanScanner 开, 不用了调 Close (不调也有 finalizer 兜底)。
// 不是并发安全的: 一个 goroutine 一个。
type SpanScanner struct {
	h   *C.cre2_spanscan
	set *RegexpSet
	buf []SetSpan
}

// NewSpanScanner 开一个流式扫描工作区。batch = 一批最多吐几条游程 (一次 Scan 里回调被调几次
// 取决于游程总数 / batch)。batch 会被抬到至少 pattern 条数 —— native 侧要保证"再来一个命中
// 字节也一定塞得下这一批", 一个 DFA 状态最多带走全表那么多条 pattern。
//
// 工作区可以反复 Scan (不重新分配), 所以热路径上应该建一次长期留着, 别每次扫描新建。
func (s *RegexpSet) NewSpanScanner(batch int) (*SpanScanner, error) {
	if s.size == 0 {
		return nil, errors.New("re2native: span scanner on empty set")
	}
	if batch < s.size {
		batch = s.size
	}
	h := C.cre2_set_spanscan_new(s.h)
	runtime.KeepAlive(s)
	if h == nil {
		return nil, errors.New("re2native: span scanner alloc failed")
	}
	sc := &SpanScanner{h: h, set: s, buf: make([]SetSpan, batch)}
	runtime.SetFinalizer(sc, func(x *SpanScanner) { x.freeC() })
	return sc, nil
}

func (sc *SpanScanner) freeC() {
	if sc.h != nil {
		C.cre2_spanscan_free(sc.h)
		sc.h = nil
	}
}

// Close 释放 native 侧的工作区。可重复调; 调过之后 Scan 返回 error。
func (sc *SpanScanner) Close() {
	runtime.SetFinalizer(sc, nil)
	sc.freeC()
}

// Scan 扫 text 一遍, 把命中的游程【分批】交给 fn。fn 返回 false 就地停扫 (剩下的正文不再扫,
// 也不会再调 fn), Scan 返回 nil。fn 拿到的切片只在本次回调内有效 —— 下一批会覆写同一块内存,
// 要留着就自己 copy (或者在回调里当场处理完, 这也是分批的意义)。
//
// 没有任何命中时 fn 一次都不会被调。返回 error 只有两种情况: 工作区已 Close, 或者 native 侧
// DFA 中途放弃 (预算实在不够) —— 后者跟 Match 返回空是同一类事故, 调大 maxMem 即可。
func (sc *SpanScanner) Scan(text string, fn func(spans []SetSpan) bool) error {
	if sc.h == nil {
		return errors.New("re2native: span scanner closed")
	}
	if len(text) > maxCInt {
		return errors.New("re2native: span scan text too large (>2GiB)")
	}
	if sc.set.size == 0 {
		return nil
	}
	if C.cre2_spanscan_begin(sc.h, C.int(len(text))) == 0 {
		return errors.New("re2native: span scan begin failed")
	}
	// native 侧【不保存】正文指针 (只存偏移), 所以每次 step 都要重新传进去。
	tp := strBytePtr(text)
	tn := C.int(len(text))
	outp := (*C.int32_t)(unsafe.Pointer(&sc.buf[0]))
	outcap := C.int(len(sc.buf) * 3)
	for {
		var more C.int
		n := int(C.cre2_spanscan_step(sc.h, tp, tn, outp, outcap, &more))
		runtime.KeepAlive(text)
		runtime.KeepAlive(sc)
		if n < 0 {
			return errors.New("re2native: span scan failed (DFA gave up); patterns=" +
				strconv.Itoa(sc.set.size) + "; 用 NewRegexpSetMaxMem 把 maxMem 调大")
		}
		if n > len(sc.buf) {
			n = len(sc.buf) // 防御: C 侧最多写 outcap/3 条
		}
		if n > 0 && !fn(sc.buf[:n]) {
			return nil
		}
		if more == 0 {
			return nil
		}
	}
}

// ScanBytes 同 Scan, 但正文是 []byte (零拷贝)。
func (sc *SpanScanner) ScanBytes(text []byte, fn func(spans []SetSpan) bool) error {
	err := sc.Scan(bytesStr(text), fn)
	runtime.KeepAlive(text)
	return err
}

// AppendSpans 是 Scan 的"我就要全部, 一次拿走"版: 把所有游程 append 进 dst 并返回。
// 传 dst[:0] 复用切片即可零分配 (稳态下)。顺序同 Scan (不保证全局升序)。
func (sc *SpanScanner) AppendSpans(dst []SetSpan, text string) ([]SetSpan, error) {
	err := sc.Scan(text, func(spans []SetSpan) bool {
		dst = append(dst, spans...)
		return true
	})
	return dst, err
}
