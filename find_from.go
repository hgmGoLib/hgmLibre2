// find_from.go —— 「从 pos 起找下一处」: 非锚定, 但起点不许早于 pos。
//
// ── 为什么单独有这么一组入口 ────────────────────────────────────────────────
// 内核早就有这个能力: C 层 cre2_match_at(h, text, textlen, startpos, ...) 里 text 传的是
// 【整串】, 所以 \b / ^ / $ 在 startpos 处看到的是真实邻字节。Go 层 findFrom(s, pos) 也一直
// 包着, 只是所有导出入口都写死 pos=0 —— 要"从某处接着找"的调用方只能自己 s[pos:] 切一刀,
// 而切完 \b / ^ / $ 看到的就是【假邻居】(切口两边的字节没了), 答案会错。
//
// 🔴 所以这组入口的存在理由只有一条: 让"从 pos 接着找"这件事不必切片。pos 是【原串上的
//    偏移】, 整串照样喂给 RE2。
//
// 谁在用: MatchScanner 的 B 路 (matchscan.go 的 MatchScanMode_t) —— 它要"起点 >= 游标的
// 最左匹配", 正是这个形状。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import "runtime"

// FindStringIndexFrom 返回【起点 >= pos】的最左匹配 [start,end), 无匹配返回 nil。
// pos 越界 (<0 或 >len(s)) 当无匹配。
//
// 非锚定, leftmost-first —— 与 leftmost-longest 选【同一个起点】, 只在终点上分歧, 所以拿它
// 定起点、再用 ResolveSpan 取最长终点, 得到的就是 leftmost-longest。
func (re *Regexp) FindStringIndexFrom(s string, pos int) []int {
	if pos < 0 || pos > len(s) {
		return nil
	}
	m := re.findFrom(s, pos)
	if m == nil {
		return nil
	}
	return []int{m[0], m[1]}
}

// FindStringIndexFrom 同上, 但复用 ctx 的 scratch (单线程顺序反复调稳态零分配)。
// 返回的切片切自 ctx.ret, 仅在【下次用同一 ctx 调用前】有效。
//
// 与 ctx.FindStringIndex 的差别只有 startpos 一个参数 —— nmatch=1 同样只回填 group0,
// 不进 findFrom 那条"按子组个数现 make 两块"的路。
func (ctx *FindStringIndex_ctx_t) FindStringIndexFrom(re *Regexp, s string, pos int) []int {
	if len(s) > maxCInt || pos < 0 || pos > len(s) {
		return nil
	}
	if cap(ctx.cbuf) < 2 {
		ctx.cbuf = make([]C.int, 2)
	}
	cbuf := ctx.cbuf[:2]
	tp := strBytePtr(s)
	ok := C.cre2_match_at(re.h, tp, C.int(len(s)), C.int(pos), &cbuf[0], 1) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if !ok {
		return nil
	}
	ctx.ret[0] = int(cbuf[0])
	ctx.ret[1] = int(cbuf[1])
	return ctx.ret[:]
}
