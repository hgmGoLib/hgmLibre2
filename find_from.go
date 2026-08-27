// find_from.go —— 「从 pos 起找下一处」: 非锚定, 但起点不许早于 pos。
//
// ── 为什么单独有这么一组入口 ────────────────────────────────────────────────
// 内核早就有这个能力: C 层 cre2_match_at(h, text, textlen, startpos, endpos, ...) 里 text 传的
// 是【整串】, 只有 [startpos,endpos) 这一段参与搜索, 所以 \b / ^ / $ 在切口处看到的是真实邻
// 字节。Go 层 findWithin(s, from, bound) 也一直包着, 只是所有导出入口都写死 0/len(s) ——
// 要"从某处接着找"的调用方只能自己 s[pos:] 切一刀, 而切完 \b / ^ / $ 看到的就是【假邻居】
// (切口两边的字节没了), 答案会错。
//
// 🔴 所以这组入口的存在理由只有一条: 让"只在某一段里找"这件事不必切片。参数都是【原串上的
//    偏移】, 整串照样喂给 RE2。
//
// 谁在用: ① MatchScanner 的 B 路 (matchscan.go 的 MatchScanMode_t) —— 它要"起点 >= 游标的
// 最左匹配", 正是这个形状。② 拿到整段区间之后回头补捕获组偏移的调用方 (下面那个 Within 版)。
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
	m := re.findWithin(s, pos, len(s))
	if m == nil {
		return nil
	}
	return []int{m[0], m[1]}
}

// FindStringIndexFrom 同上, 但复用 ctx 的 scratch (单线程顺序反复调稳态零分配)。
// 返回的切片切自 ctx.ret, 仅在【下次用同一 ctx 调用前】有效。
//
// 与 ctx.FindStringIndex 的差别只有 startpos 一个参数 —— nmatch=1 同样只回填 group0,
// 不进 findWithin 那条"按子组个数现 make 两块"的路。
func (ctx *FindStringIndex_ctx_t) FindStringIndexFrom(re *Regexp, s string, pos int) []int {
	if len(s) > maxCInt || pos < 0 || pos > len(s) {
		return nil
	}
	if cap(ctx.cbuf) < 2 {
		ctx.cbuf = make([]C.int, 2)
	}
	cbuf := ctx.cbuf[:2]
	tp := strBytePtr(s)
	ok := C.cre2_match_at(re.h, tp, C.int(len(s)), C.int(pos), C.int(len(s)), &cbuf[0], 1) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if !ok {
		return nil
	}
	ctx.ret[0] = int(cbuf[0])
	ctx.ret[1] = int(cbuf[1])
	return ctx.ret[:]
}

// FindStringSubmatchIndexWithin 在 s 的 [from,bound) 这一段里找最左匹配, 连子组偏移一起回填
// (布局同 FindStringSubmatchIndex: 2*(numSubexp+1) 个, 没参与匹配的组是 -1)。无匹配返回 nil。
// 偏移都是【原串 s 上的】。越界 (from<0 / bound>len(s) / from>bound) 当无匹配。
//
// 🔴 它存在的理由与本文件头那段一字不差, 只是换个消费点: 调用方已经从别处 (比如
//    MatchScanner) 拿到了某一处匹配的【整段区间】 [from,bound), 现在还想要这一段里某个捕获组
//    的位置。没有这个入口的话只能 re.FindStringSubmatchIndex(s[from:bound]) —— 那两刀切完,
//    ^ / $ / \b 在两端看到的是假邻居, 捕获组偏移会偏; 而捕获组偏移的下游往往是脱敏切片。
//
// 🔴 两端都收是因为调用方【本来就两端都知道】(区间是它给的), 白送的信息不用白不用:
//    RE2 只在这一段里搜, 越过 bound 的那部分正文一个字节都不碰。而且这道边界顺手把
//    "从 from 起头没匹配, 于是往后找到了另一处"这种答非所问挡在外面 —— 但挡不干净
//    (另一处也可能整个落在段内), 所以下面那句仍然作数。
//
// 非锚定: 调用方拿到 m 之后【仍须自己核对 m[0]/m[1] 就是那个 from/bound】—— 想问的是
// "这一处的子组", 不是"这一段里随便哪一处的子组"。
func (re *Regexp) FindStringSubmatchIndexWithin(s string, from, bound int) []int {
	return re.findWithin(s, from, bound)
}
