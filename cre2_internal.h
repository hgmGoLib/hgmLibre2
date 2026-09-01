// cre2_internal.h — cre2 各 .cpp 之间共用的【内部】句柄定义。
// 不对外: cre2.h 里这些类型只留不完整声明 (typedef struct cre2_set cre2_set;),
// 调用方只拿指针。放这里是为了多个 .cpp 能共用同一份定义而不必互相 #include .cpp。
#ifndef CRE2_INTERNAL_H
#define CRE2_INTERNAL_H

#include "re2_re2.h"
#include "re2_set.h"

#include <mutex>
#include <string>
#include <vector>

struct cre2_re; // 定义在 cre2.cpp

struct cre2_set {
	re2::RE2::Set *set;
	int64_t max_mem;               // 建这张表时的预算, 派生的单条对象跟着它走
	std::vector<std::string> pats; // 逐条源串: 惰性建单条对象要它

	// ── 补端点用的【单条对象】缓存 (惰性 · 加锁 · 一张表一份) ──────────────────
	//
	// 🔴 它必须挂在【表】上, 不能挂进扫描工作区: 工作区是每 goroutine 一份 (生产上按
	//    GOMAXPROCS 池化 4~64 份), 挂进去就是把最大那张表 9.6MB 的反向单条缓存乘以份数。
	//    一张表一份则是所有工作区共用, 与 2026-08-28 那版 Go 侧 fwd1/vp1 同解。
	//
	// one_fwd[i]    第 i 条 pattern 自己那条【正向 · longest 口径】的 RE2 对象。
	//               fll/rrl 拿它锚定取最长右端; frel 拿它的【反向程序】做反向锚定
	//               (cre2_resolve_span_reverse_r) —— longest 只改搜索 kind 不改 ParseFlags,
	//               所以那条反向程序与默认口径编出来的逐字相同, 一份够三家用。
	// one_viable[i] 第 i 条 pattern 自己那条【反向 · 只装这一条】的 set, fll 收候选起点用。
	//               必须是 set 不是单条: 单条 Compile 会把 ^/$ 摘成标志, 自己驱动 DFA 查不到。
	//
	// *_no[i] = 试过且建不出来, 记下来免得每遍扫描重编一次 (失败是确定性的)。
	std::mutex one_mu;
	std::vector<cre2_re *> one_fwd;
	std::vector<unsigned char> one_fwd_no;
	std::vector<cre2_set *> one_viable;
	std::vector<unsigned char> one_viable_no;
};

#endif // CRE2_INTERNAL_H
