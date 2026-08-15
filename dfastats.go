// dfastats.go — DFA 状态缓存计数: 把"预算够不够"从猜变成量。
//
// 【病灶】RE2 的 DFA 状态缓存装不下当前语料走出来的状态集时, 不是 LRU 淘汰而是【整表清空】
// 重建 (DFA::ResetCache)。结果仍然正确 —— 所以这件事在调用方眼里没有任何信号 —— 但它是
// 悬崖不是斜坡: working set 比预算大 1%, 吞吐掉几十倍。RegexpSet (kManyMatch) 更容易撞:
// 状态里带着"已命中哪几条"的位集, 状态数对 pattern 条数是超线性的。
//
// 【为什么单形状 benchmark 量不到】同一份 body 扫 N 遍, 第一遍之后缓存全热, 再不新建状态,
// 所以"换个预算吞吐没差别"是必然结论 —— 而生产是每个请求一份【互不相同】的 body, 每换一份
// 就把缓存冲垮一次。要量这条曲线, 语料必须多形状, 且要看 Resets 而不是只看吞吐。
//
// 【怎么用】
//   - 标定 maxMem: 单线程热身跑一批【互不相同】的真 body, 取 Resets 增量; >0 就把预算翻倍
//     重编重来, 直到增量归零 —— 那个预算才是"够", 编译过得去不算够。
//   - 产线: 定期采样算速率 (resets/秒 或 resets/次扫描)。稳定 >0 = 正站在悬崖底下。
//
// 口径: 【进程级】计数, 不区分是哪个 Regexp/RegexpSet 造成的 (RE2 的钩子不带上下文, Set 匹配
// 更是完全没有)。并发扫描时取差值只能得到"这段时间内全进程的次数", 要归因到某一次扫描请单线程量。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

// DFAStats_t 是一份 DFA 状态缓存计数快照 (字段名与 C 侧 cre2_dfa_stats 逐字一致)。
// 四个字段各自无锁读取, 相互之间不保证同一瞬间 (并发下 Resets 与 Last* 可能差一拍)。
type DFAStats_t struct {
	// Resets 是 DFA::ResetCache 的累计次数 —— 状态缓存被整表清空重建了几次。
	// 这是 thrash 的直接读数: 一批扫描期间它不涨 = 预算够; 每扫必涨 = 预算不够。
	Resets uint64
	// SearchFailures 是 DFA 放弃搜索的累计次数。单条 Regexp 撞上会退回 NFA (慢一个数量级,
	// 结果仍对); RegexpSet 不会走到这里 (RE2 对 kManyMatch 禁掉了 bail, 只 flush, 见 README)。
	SearchFailures uint64
	// LastStateBudget 是最近一次 Resets 发生时, 那个 DFA 的状态缓存预算 (字节)。
	// 它约等于 maxMem 扣掉编译期程序占用后剩下的那一半 —— 想知道"现在实际有多少额度给状态", 看它。
	LastStateBudget int64
	// LastCacheStates 是最近一次 Resets 发生时缓存里的状态数 (即这一次清掉了多少个状态)。
	// 它是 working set 的下界样本: 预算撑不住这么多状态, 才会有这次清空。
	LastCacheStates int64
}

// DFAStats 取一份当前快照。开销 = 四次原子读, 可以随便调。
func DFAStats() DFAStats_t {
	s := C.cre2_dfa_stats_get()
	return DFAStats_t{
		Resets:          uint64(s.Resets),
		SearchFailures:  uint64(s.SearchFailures),
		LastStateBudget: int64(s.LastStateBudget),
		LastCacheStates: int64(s.LastCacheStates),
	}
}

// DFAStatsZero 把四个计数归零, 便于分段测量 (标定循环每轮开头调一次)。
// 进程级共享: 并发跑多组测量时归零会互相踩, 那种场合请改用取差值。
func DFAStatsZero() { C.cre2_dfa_stats_zero() }
