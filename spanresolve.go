// spanresolve.go —— ResolveSpan: 给一个端点, 锚定推出【另一端】。单点、有界、不扫正文。
//
// ── 它和 FindAllIndex 是两件事, 别按对称的样子去用 ──────────────────────────
// FindAllIndex 是【扫一遍正文】: 代价跟正文一样长。
// ResolveSpan   是【在一个点上问一句】: "从这儿起(或到这儿止), 这条 pattern 能伸到哪?"
//               代价 = 这处命中实际能延伸多远, 与正文长度【无关】。1KB 的正文和 6.4MB 的
//               正文上问同一句话, 价钱一样。
//
// 🔴 所以"补一处命中的另一端"要用它, 不要用反向 FindAllIndex 去扫一遍。反向扫全表在 6.4MB
//    上是 65 秒 (正向 18ms); 拆成一条一条反着扫倒是便宜, 可命中 k 条就是 k 遍全文 ——
//    正好是 FindAllIndex 存在的意义 (把 1+k 遍压成 1 遍) 被原样赔回去。
//
// 🔴 也不要在 Go 这侧自己补。自己补只能另编一条 \A(?:pat) 的锚定正则 —— 每条 pattern 一个
//    Regexp 对象、一份独立的 DFA 缓存, 还得手工保证它和 set 里那条语义一致; 而【非锚定】的
//    那种补法 (拿原正则去扫 text[from:]) 更糟: `.*?` 前缀让每个位置都能当起点, 状态数对
//    计数上界指数增长 (doc/状态数为什么会相乘.md 里同形状差 967 倍), 等于把 FindAllIndex
//    刚省下来的又赔回去。这里走的是 set 自己那份程序、那份 DFA 缓存, 且真锚定。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
)

// ResolveSpan 求【另一端】: 给定 FindAllIndex 吐出来的一个端点, 返回第 id 条 pattern 在这个
// 端点上能达到的另一端。
//
//	from = 匹配左端(含), 返回右端(不含) —— text[from:pos] 就是这条 pattern 的匹配
//
// ok=false 表示这条 pattern 在这个端点上根本不匹配 (调用方给错端点了, 或者给错 id)。
//
// 🔴 返回的是【最长】的那个匹配, 不是最短的。变长 pattern 在同一个端点上通常有一串长度都
//    成立 (`AAA-[A-Za-z0-9]{8,16}` 在同一个右端上有 9 个合法左端), "碰到第一个就收工"给的是
//    最短那个 = 把命中截断, 下游拿去做定长校验就会把真命中判成假命中。
//
// 无状态、只读, 可以和别的 goroutine 的 FindAllIndex 并发调 (与 Match 同一个口径)。
func (s *RegexpSet) ResolveSpan(text string, from, id int32) (pos int32, ok bool, err error) {
	return resolveSpanWithin(s, text, from, -1, id)
}

// ResolveSpanWithin 同 ResolveSpan, 但限定【最远看到哪】: bound 是右上界, 负数 = 不限。
//
// 什么时候需要它: 走到死状态的成本 = 这条命中实际能延伸到多远 —— 除非 pattern 本身就能无限
// 延伸 ((?s).*KEY 那种), 那一条在整篇正文上解析一次就是 O(正文)。给这类 pattern 配一个
// "看到哪为止"就把它钉回常数。
//
// 判定用的上下文恒是【整篇正文】, 所以 \b / ^ / $ 看到的永远是真实邻居字节, 而不是 bound
// 切出来的假边界 —— 掐 bound 只会让答案变短, 不会让它变错。
func (s *RegexpSet) ResolveSpanWithin(text string, from, bound, id int32) (pos int32, ok bool, err error) {
	return resolveSpanWithin(s, text, from, bound, id)
}

// ResolveSpanBytes 同 ResolveSpan, 但正文是 []byte (零拷贝)。
func (s *RegexpSet) ResolveSpanBytes(text []byte, from, id int32) (pos int32, ok bool, err error) {
	pos, ok, err = resolveSpanWithin(s, bytesStr(text), from, -1, id)
	runtime.KeepAlive(text)
	return pos, ok, err
}

// ResolveSpan 求【另一端】: 方向跟着反向 set 走, 与正向那个正好相反 ——
//
//	from = 匹配右端(不含), 返回左端(含) —— text[pos:from] 就是这条 pattern 的匹配
//
// 这就是"补左端"该走的那条路: 单点、锚定、代价与正文长度无关。上面那层 (re2set_fll.go 的
// Re2Set_fll_t) 给每条 pattern 惰性建一个【只有这一条】的反向 set, 就是为了在这里问这一句。
func (r *RegexpSetReverse) ResolveSpan(text string, from, id int32) (pos int32, ok bool, err error) {
	return resolveSpanWithin(r.s, text, from, -1, id)
}

// ResolveSpanWithin 同 ResolveSpan, 但限定【最远看到哪】: 反向的 bound 是左下界 (回看不越过
// 它), 负数 = 不限。上面那层把 bound 掐在游标上 —— 那是【正确性】不是省钱, 见 re2set_fll.go。
func (r *RegexpSetReverse) ResolveSpanWithin(text string, from, bound, id int32) (pos int32, ok bool, err error) {
	return resolveSpanWithin(r.s, text, from, bound, id)
}

// ResolveSpanBytes 同 ResolveSpan, 但正文是 []byte (零拷贝)。
func (r *RegexpSetReverse) ResolveSpanBytes(text []byte, from, id int32) (pos int32, ok bool, err error) {
	pos, ok, err = resolveSpanWithin(r.s, bytesStr(text), from, -1, id)
	runtime.KeepAlive(text)
	return pos, ok, err
}

// resolveSpanWithin 是正反两个方向共用的实现 —— 方向早在建 set 的时候就编进程序里了。
func resolveSpanWithin(s *RegexpSet, text string, from, bound, id int32) (pos int32, ok bool, err error) {
	if s.size == 0 {
		return 0, false, errors.New("re2native: resolve span on empty set")
	}
	if len(text) > maxCInt {
		return 0, false, errors.New("re2native: resolve span text too large (>2GiB)")
	}
	if id < 0 || int(id) >= s.size {
		return 0, false, errors.New("re2native: resolve span bad pattern index " + strconv.Itoa(int(id)))
	}
	if from < 0 || int(from) > len(text) {
		return 0, false, errors.New("re2native: resolve span bad offset " + strconv.Itoa(int(from)))
	}
	// 🔴 走【按值返回】的 _r 孪生, 不走出参版: 出参版要把 &out 这个 Go 指针交给 C, 逃逸分析
	// 据此每次调用把它搬上堆 —— 一次解析一笔 4 字节分配。调用方按端点解析时这笔按端点数
	// 放大 (实测 6.5 万个端点 = 6.5 万笔 262KB), 而这个方法本该是零分配的。同一招见
	// find_all_flat.go 里 cre2_match_all_r 那段。
	r := C.cre2_set_resolve_span_r(s.h, strBytePtr(text), C.int(len(text)),
		C.int(from), C.int(bound), C.int(id))
	runtime.KeepAlive(text)
	runtime.KeepAlive(s)
	switch r.rc {
	case 1:
		return int32(r.pos), true, nil
	case 0:
		return 0, false, nil
	}
	return 0, false, errors.New("re2native: resolve span failed (DFA gave up); patterns=" +
		strconv.Itoa(s.size) + "; 用 NewRegexpSetMaxMem 把 maxMem 调大")
}

// ── 单条 (非 set) 的反向锚定解析 ─────────────────────────────────────────────

// ResolveSpanWithin 求【左端】: 给一个匹配右端 from (不含), 返回最靠左的那个左端 pos (含),
// text[pos:from] 就是这条 pattern 的一个匹配。bound 是回看的左下界 (负数 = 不限)。
// ok=false 表示这条 pattern 在这个右端上根本伸不出匹配 (或者伸不到 bound 以内)。
//
// 判定用的上下文恒是【整篇正文】, 所以 \b / ^ / $ 看到的永远是真实邻居; 掐 bound 只会让
// 答案变短, 不会让它变错。代价 = 实际回看了多远, 与正文长度无关。
//
// 🔴 给的是【最靠左】的那个左端, 不是碰到的第一个。反向走到死状态才知道还能不能更靠左;
//    "撞到第一个 match 状态就收工"给的是最短匹配 = 把命中截断。
//
// ── 它和 (*RegexpSetReverse).ResolveSpanWithin 的关系 ────────────────────────
// 语义逐字相同, 差别只在【对象是谁】: 那个是一张表 (要一个 id 说是哪条), 这个是一条 pattern。
// 一条 pattern 就该用这一个 —— 套一条 pattern 的 set 去凑要多背一张 id 表 (kManyMatch 的
// 状态更大), 而且 set 与单条对 ^ / $ 的处理方式不同, 走的根本不是同一条代码路。
//
// 无状态、只读; 反向程序首次调用时惰性编出来 (线程安全)。编不出来 / DFA 放弃都返回 err ——
// 这一层不猜、不静默退回, 因为"没有匹配"和"算不出来"对调用方是两件完全不同的事。
func (rr *RegexpReverse) ResolveSpanWithin(text string, from, bound int32) (pos int32, ok bool, err error) {
	re := rr.re
	if len(text) > maxCInt {
		return 0, false, errors.New("re2native: resolve span text too large (>2GiB)")
	}
	if from < 0 || int(from) > len(text) {
		return 0, false, errors.New("re2native: resolve span bad offset " + strconv.Itoa(int(from)))
	}
	// 走【按值返回】的 _r 孪生, 理由同上面那个: 出参版每次调用一笔 4 字节堆分配, 而这个
	// 方法在"每个端点问一次"的用法上会按端点数放大。
	r := C.cre2_resolve_span_reverse_r(re.h, strBytePtr(text), C.int(len(text)),
		C.int(from), C.int(bound))
	runtime.KeepAlive(text)
	runtime.KeepAlive(re)
	switch r.rc {
	case 1:
		return int32(r.pos), true, nil
	case 0:
		return 0, false, nil
	}
	return 0, false, errors.New("re2native: reverse resolve span failed (反向程序编不出来, 或 DFA 放弃)" +
		"; 用 CompileReverseMaxMem 把 maxMem 调大; pattern=" + re.expr)
}
