// cre2_internal.h — cre2 各 .cpp 之间共用的【内部】句柄定义。
// 不对外: cre2.h 里这些类型只留不完整声明 (typedef struct cre2_set cre2_set;),
// 调用方只拿指针。放这里是为了多个 .cpp 能共用同一份定义而不必互相 #include .cpp。
#ifndef CRE2_INTERNAL_H
#define CRE2_INTERNAL_H

#include "re2_re2.h"
#include "re2_set.h"

#include <atomic>
#include <mutex>
#include <string>
#include <vector>

struct cre2_re; // 定义在 cre2.cpp

struct cre2_set {
	// ── 引用计数 ────────────────────────────────────────────────────────────
	// 建出来是 1; cre2_set_free 是【减一】, 减到 0 才真拆。持有方一律 cre2_set_ref /
	// cre2_set_free 配对 (目前唯一的持有方是 cre2_re2set)。
	//
	// 🔴 为什么要它: 策略换表的时候旧表得能【确定地】释放, 而那一刻可能还有别人攥着它
	//    (一个 re2set 对象, 或者一遍正在跑的扫描)。靠调用方那侧存引用只解决"不早死",
	//    解决不了"该死的时候死得掉" —— 那是 GC 的时间表, 不是策略的时间表。
	std::atomic<int> ref{1};

	re2::RE2::Set *set;
	int64_t max_mem;               // 建这张表时的预算, 派生的单条对象跟着它走
	std::vector<std::string> pats; // 逐条源串: 惰性建单条对象要它

	// 🔴 补端点用的【单条正向/反向对象】不在这里 —— 它们挂在 cre2_re2set 上
	//    (定义在 cre2_re2set.cpp)。表只管这一张 set 的 DFA。
};

#endif // CRE2_INTERNAL_H
