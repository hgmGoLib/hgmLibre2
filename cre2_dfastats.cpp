// cre2_dfastats.cpp — DFA 状态缓存计数 (cre2.h 的 cre2_dfa_stats_* 实现)。
//
// 【为什么要这个】RE2 的 DFA 状态缓存装不下当前走出来的状态集时, 不是 LRU 淘汰,
// 而是 DFA::ResetCache() 把缓存【整表清空】后从零重建 (re2_dfa.cc)。结果仍然正确,
// 所以在调用方眼里这件事【一点信号都没有】—— 但代价不是缓慢劣化而是几十倍的悬崖:
// working set 比预算大 1%, 吞吐掉 50×, 而不是掉 1%。而且 ResetCache 要拿
// cache_mutex_ 的【写锁】, 之后那段搜索也一直持写锁跑完 (re2_dfa.cc 里那段注释:
// "the rest of the search runs holding cache_mutex_ for writing"), 并发扫同一个
// Set 的其它线程全部停摆 —— 生产上的实际损失比单线程量到的还大。
//
// RE2 自己留了钩子 (re2/re2.h 的 hooks::SetDFAStateCacheResetHook), 本文件把它接成
// 进程级计数, 让"正在 thrash"变成可测的量: maxMem 到底够不够, 不必再靠猜。
// 用法与口径见 cre2.h 的声明与 Go 侧 dfastats.go。
#include "cre2.h"
#include "re2_re2.h"

#include <atomic>

namespace {

// 全是 relaxed: 计数器之间不需要互相定序, 也不用它去同步别的数据。
std::atomic<uint64_t> gResets(0);
std::atomic<uint64_t> gSearchFailures(0);
std::atomic<int64_t> gLastStateBudget(0);
std::atomic<int64_t> gLastCacheStates(0);

void onStateCacheReset(const re2::hooks::DFAStateCacheReset &r) {
	gResets.fetch_add(1, std::memory_order_relaxed);
	gLastStateBudget.store(r.state_budget, std::memory_order_relaxed);
	gLastCacheStates.store((int64_t)r.state_cache_size, std::memory_order_relaxed);
}

void onSearchFailure(const re2::hooks::DFASearchFailure &) {
	gSearchFailures.fetch_add(1, std::memory_order_relaxed);
}

// 装钩子: 命名空间作用域对象的构造函数在动态初始化阶段跑完 (早于 Go runtime 起来),
// 之后这两个 hook 再不改动, 故无需任何锁或 once。
// RE2 那两个 hook 变量本身是【常量初始化】的 (re2.cc 里用 union 做那个 hack 正是为了
// 这个), 所以在别的翻译单元的动态初始化里 Store 它们不存在静态初始化顺序问题。
struct installer_t {
	installer_t() {
		re2::hooks::SetDFAStateCacheResetHook(&onStateCacheReset);
		re2::hooks::SetDFASearchFailureHook(&onSearchFailure);
	}
};
installer_t gInstaller;

} // namespace

extern "C" {

cre2_dfa_stats cre2_dfa_stats_get(void) {
	cre2_dfa_stats s;
	s.Resets = gResets.load(std::memory_order_relaxed);
	s.SearchFailures = gSearchFailures.load(std::memory_order_relaxed);
	s.LastStateBudget = gLastStateBudget.load(std::memory_order_relaxed);
	s.LastCacheStates = gLastCacheStates.load(std::memory_order_relaxed);
	return s;
}

void cre2_dfa_stats_zero(void) {
	gResets.store(0, std::memory_order_relaxed);
	gSearchFailures.store(0, std::memory_order_relaxed);
	gLastStateBudget.store(0, std::memory_order_relaxed);
	gLastCacheStates.store(0, std::memory_order_relaxed);
}

} // extern "C"
