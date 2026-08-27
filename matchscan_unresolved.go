// matchscan_unresolved.go —— "这一条本遍没能全给你"的那份交代, 正反两个 MatchScanner 共用。
//
// ── 这里修掉的是一个说得太狠的旧契约 ────────────────────────────────────────
// 旧版 Scan 返回 []int32 (光秃秃的下标), 文档上配的话是:"作废可能发生在已经交出去几批之后,
// 所以调用方要把这一条本遍收到的东西【全丢掉】再走老路"。
//
// 那句话对四种作废原因里的【一种】才成立, 剩下三种是被它连坐的:
//
//	emptyMatch  能匹配空串 —— 在这一条的【第一条游程】上就判掉了, 一处都还没交出去。
//	compile     单条对象编不出来 —— 同上, 也是初始化那一下, 一处都还没交出去。
//	            这两类都只跟 pattern 有关、跟正文无关 ⟹ 出现一次就是每一遍都出现,
//	            调用方该在建集那一步挑掉它 (SetModes 已经把 emptyMatch 那类挡在建工作区)。
//	dfaBudget   锚定解析时 DFA 放弃 (arena 撞到 maxMem 上限) —— 只有【这一种】能发生在
//	            已经交出去几处之后。
//	runOrder    游程乱序 —— native 侧的不变量被破坏, 属于"本不该发生"。
//
// 而就算是 dfaBudget, "全丢掉"也是白丢的: 出事那一刻, 这一条已经交出去的区间每一处都是
// 被锚定解析验过的真匹配, 且【完整覆盖到游标为止】—— 缺的只有游标之后那一截。所以正确的
// 交代不是"全废", 是"给你一个断点":
//
//	ResumeFrom  已交付部分与未交付部分的分界 (就是那把游标)。
//	            正向: 已交付的覆盖 [0, ResumeFrom), 请从 ResumeFrom 往后补。
//	            反向: 已交付的覆盖 [ResumeFrom, len(text)), 请从 ResumeFrom 往前补。
//
// 🔴 补的时候【不要切片】: text[ResumeFrom:] 会让 \b / ^ / $ 看到假邻居, 答案会错。用
//    (*Regexp).FindStringIndexFrom(text, ResumeFrom) 反复推进 —— 参数是原串上的偏移, 整串
//    照样喂给 RE2 (见 find_from.go 文件头)。反向那侧对应 ResolveSpanWithin 的 bound。
//
// 于是 emptyMatch / compile / runOrder 三类的 ResumeFrom 就是"整篇"(正向 0 · 反向 len(text)),
// 与旧的"全丢掉"逐字同解; 只有 dfaBudget 这一类真的省下了前面那一截。调用方那边少掉的是
// "先把这一条已收到的挑出来删掉"这一整段 —— 那段代码本来就只为了迁就这句过狠的契约而存在。
package hgmLibre2

// MatchScanUnresolved 是 Scan 返回的一条交代: 第 Index 条 pattern 本遍没能全给你, 原因是
// Reason, 已交付与未交付的分界在 ResumeFrom。
type MatchScanUnresolved struct {
	Index      int32
	ResumeFrom int32
	Reason     MatchScanUnresolvedReason_t
}

// MatchScanUnresolvedReason_t 是作废的原因, 四取一。前两类只跟 pattern 有关 (每遍都会出现,
// 该在建集时挑掉), 后两类跟这一遍的正文/运行时有关。
type MatchScanUnresolvedReason_t string

// MatchScanUnresolvedReason_emptyMatch: 这条 pattern 能匹配空串 (PatternLenRange 的 min <= 0)。
// 每个位置都是一处零长命中, 游标压不住。一处都没交出去, ResumeFrom = 整篇。
// 🔴 配了 SetModes 的调用方碰不到它 —— SetModes 在建工作区那一步就报错了。
const MatchScanUnresolvedReason_emptyMatch MatchScanUnresolvedReason_t = "emptyMatch"

// MatchScanUnresolvedReason_compile: 补端点要的那个"只有这一条"的单条对象编不出来
// (maxMem 太小 / 这条 pattern 反向或正向编译失败)。一处都没交出去, ResumeFrom = 整篇。
const MatchScanUnresolvedReason_compile MatchScanUnresolvedReason_t = "compile"

// MatchScanUnresolvedReason_dfaBudget: 锚定解析 (ResolveSpan / ResolveSpanWithin) 时 DFA 放弃
// —— arena 撞到 maxMem 上限, 清一次缓存重来仍然建不出下一个状态。
// 🔴 这是【唯一】一种会发生在已经交出去几处之后的: 已交付的那些是完整且正确的, 从
//    ResumeFrom 接着补即可。想根治就 NewRegexpSetMaxMem 把 maxMem 调大。
const MatchScanUnresolvedReason_dfaBudget MatchScanUnresolvedReason_t = "dfaBudget"

// MatchScanUnresolvedReason_runOrder: 游程不是按扫描方向单调来的 (正向该升序 · 反向该降序)。
// native 侧的不变量被破坏, 本不该发生; 一旦发生, 已交付的每一处仍是真匹配, 但"有没有漏"就
// 没法保证了 ⟹ ResumeFrom = 整篇, 照旧全部重来。
const MatchScanUnresolvedReason_runOrder MatchScanUnresolvedReason_t = "runOrder"
