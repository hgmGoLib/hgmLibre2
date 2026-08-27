// find_at.go —— 「必须从这一点起头」: 锚定在 from 的一次搜索。
//
// ── 它和 find_from.go 那一组差在哪 ──────────────────────────────────────────
// FindStringIndexFrom(s, pos): 【非】锚定 —— 起点不许早于 pos, 但可以比 pos 晚。
// FindStringIndexAtWithin(s, from, bound): 锚定 —— 起点【就是】from, 不是就没有匹配。
//
// 两者的 C 入口是同一个 RE2::Match, 只差 anchor 一个参数; 而 text/textlen 一律传【整串】,
// from/bound 只圈定"在哪一段里搜" —— 所以 \b / ^ / $ 看到的是真实邻字节, 调用方不必自己
// s[from:bound] 切一刀 (切完两侧就是假邻居, 答案会错)。
//
// ── 为什么需要这一组 ────────────────────────────────────────────────────────
// 这就是"给一个起点, 求这条 pattern 在这儿能伸到哪"—— set 那侧叫 ResolveSpan。
// 拿【单条】对象做同一件事有两个好处:
//   ① 走的是 RE2::Match 那条完整的路 (DFA → OnePass/BitState/NFA 逐级回退), DFA 放弃了
//      还有下家; set 那侧的锚定解析是 kManyMatch 的 DFA 独一条, 没有下家 ——
//      "DFA 放弃"在那边只能整遍失败, 在这边根本不发生。
//   ② 用的是这一条自己的程序和自己那份 DFA 缓存, 不去冲刷整表那份大的。
//
// 🔴 要"最长的那个终点"就得配 CompileLongestMaxMem 编出来的对象。普通对象给的是贪心那个
//    终点 —— 变长 pattern 上那是【把命中截断】, 下游过校验位会把真命中判成假 (见
//    CompileLongestMaxMem 的红字)。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import "runtime"

// FindStringIndexAtWithin 返回【起点就是 from】的匹配 [from,end), 且整个匹配不越过 bound。
// 无匹配 (这条 pattern 在 from 这一点上起不了头, 或者伸不到 bound 以内) 返回 nil。
// 越界 (from<0 / bound>len(s) / from>bound) 当无匹配。
//
// bound = 最远看到哪。判定用的上下文恒是【整篇 s】, 所以掐 bound 只会让答案变短, 不会让它
// 变错。不想掐就传 len(s)。
//
// 配 CompileLongest* 的对象用: 给的是"从 from 起、不越过 bound 的【最长】那个匹配"。
func (re *Regexp) FindStringIndexAtWithin(s string, from, bound int) []int {
	if len(s) > maxCInt || from < 0 || bound > len(s) || from > bound {
		return nil
	}
	var cbuf [2]C.int
	ok := C.cre2_match_at_anchored(re.h, strBytePtr(s), C.int(len(s)),
		C.int(from), C.int(bound), &cbuf[0], 1) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if !ok {
		return nil
	}
	return []int{int(cbuf[0]), int(cbuf[1])}
}

// FindStringIndexAtWithin 同上, 但复用 ctx 的 scratch (单线程顺序反复调稳态零分配)。
// 返回的切片切自 ctx.ret, 仅在【下次用同一 ctx 调用前】有效。
func (ctx *FindStringIndex_ctx_t) FindStringIndexAtWithin(re *Regexp, s string, from, bound int) []int {
	if len(s) > maxCInt || from < 0 || bound > len(s) || from > bound {
		return nil
	}
	if cap(ctx.cbuf) < 2 {
		ctx.cbuf = make([]C.int, 2)
	}
	cbuf := ctx.cbuf[:2]
	ok := C.cre2_match_at_anchored(re.h, strBytePtr(s), C.int(len(s)),
		C.int(from), C.int(bound), &cbuf[0], 1) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if !ok {
		return nil
	}
	ctx.ret[0] = int(cbuf[0])
	ctx.ret[1] = int(cbuf[1])
	return ctx.ret[:]
}
