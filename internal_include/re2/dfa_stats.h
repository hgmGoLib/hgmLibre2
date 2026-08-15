// Copyright 2010 The RE2 Authors.  All Rights Reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// ── hgmLibre2 追加 (非上游 re2) ─────────────────────────────────────────────────────────────
// DFA 状态缓存的【按对象归因】计数。
//
// 【为什么要另开一套】re2 自带的 hooks::SetDFAStateCacheResetHook 是【进程全局】的:
// 钩子回调不带任何上下文, 既不知道是哪个 RE2/RE2::Set 造成的, 也不知道是哪一次扫描。
// 生产上一个进程里有几十个 Set, 全局计数只能告诉你"有人在 thrash", 归不了因。
// re2 上游留的另一条路是 hooks::context (thread_local), 但 mingw 那条工具链没有可靠的
// thread_local(RE2_HAVE_THREAD_LOCAL 关着), 而且 thread_local 也解决不了"一次调用内
// 发生了几次"的问题。
//
// 这里改成 Rust regex-automata 的做法: 计数挂在【缓存对象】上 (它那边是 hybrid::dfa::Cache
// 的 clear_count / search_total_len), 再加一个【调用方在栈上开的一次性对象】沿调用链传下去。
// 没有全局状态, 没有 thread_local, 并发安全, mingw 也能用。

#ifndef RE2_DFA_STATS_H_
#define RE2_DFA_STATS_H_

#include <stdint.h>

namespace re2 {

// 一次扫描 (一次 RE2::Set::Match / 一次 SearchDFA) 的 DFA 计数。
// 调用方在栈上开一个传进去即可; 被调方在开头清零, 返回时填满。传 NULL = 不统计。
//
// ⚠ 只统计 DFA 这一层。搜索失败退回 NFA/BitState 之后的事情不在这里。
struct DFAScanStats {
  // 本次扫描里状态缓存被【整表清空】了几次 (DFA::ResetCache 真清了的那种)。
  // 这是 thrash 的直接读数, 也是 Rust 那边 clear_count 的等价物 —— 区别是这里能精确到
  // 一次调用。>0 就意味着这次扫描里有一段是在"每个字节都重新造状态"的速度上跑的。
  int64_t flushes;
  // 本次扫描里 arena 扩容了几次 (RE2_DFA_ARENA_GROW 才可能 >0)。
  // 扩容【不丢任何状态】, 只是把 arena realloc 到更大再重定位, 所以它不是 thrash;
  // 单独列出来免得跟 flushes 混在一起吓人。
  int64_t grows;
  // 本次扫描新建了多少个状态 (缓存未命中才会建)。
  // 稳态下应该趋近 0; 每次扫描都建几千个 = 缓存对这批语料没起作用。
  // 口径: 取 DFA 上一个累计计数器的前后差值, 所以【并发扫同一个 Set 时会把别人建的算进来】。
  int64_t states_built;
  // 本次扫描的正文字节数 (bytes/flushes 就是 Rust 那边判"缓存没用"的那个比值)。
  int64_t bytes;
  // 扫完时缓存里的状态数。
  int64_t states_end;
  // 这个 DFA 分到的状态缓存额度 (字节) 与扫完时的剩余额度。
  // 已用 = state_budget - mem_left。mem_left 见底就是下一次 flush 的前夜。
  int64_t state_budget;
  int64_t mem_left;
};

// 一个 DFA 缓存的当前水位 + 生涯累计 (Rust 那边 Cache::memory_usage() + clear_count)。
// 按对象查询, 不是进程全局。
struct DFAMemInfo {
  // false = 这个 kind 的 DFA 还没被建出来 (从来没扫过), 其余字段全 0。
  bool built;
  int64_t state_budget;   // 状态缓存总额度 (字节)
  int64_t mem_left;       // 还剩多少额度; 已用 = state_budget - mem_left
  int64_t states;         // 当前缓存里的状态数
  int64_t arena_cap;      // 实际向系统要到的状态区字节数 (RE2_DFA_ARENA 才有意义, 否则 0)
  int64_t flushes_total;  // 这个 DFA 生涯里整表清空了几次
  int64_t states_built_total;  // 这个 DFA 生涯里建过几个状态
};

// ── 建状态的归因 (RE2_DFA_ATTRIB=1 才采集; 关掉时 enabled=false, 其余字段全 0) ──
//
// 【要回答的问题】"这几万个状态是谁造的、有多贵、在正文的哪一段造的"。
// 现有的读数只能告诉你【总共】建了多少状态 (DFAMemInfo::states_built_total),
// 但 RE2::Set 里几百条 pattern 合成一张 DFA, 到底是哪几条在造状态, 之前只能靠
// "摘掉一条重新编译、跑一遍二分找 0-flush 门槛"—— 一轮几十分钟。
//
// 【怎么归因的】Prog 里每个 kInstMatch 带着 match_id = 它属于 Set 里的第几条 pattern。
// 构造 DFA 时做一次【反向可达定点】: inst → 从它出发能走到哪些 pattern 的 Match。
//   · 只能走到一条  ⇒ 这条 inst 被那条 pattern【独占】, 归给它;
//   · 能走到多条    ⇒ 是公共部分 (最典型的是非锚定搜索开头那个 .* 循环), 归到 shared。
// 每建一个状态, 就把它 inst 集合里的独占指令按 pattern 记一笔。
// ⚠ 这是【归因】不是【因果】: "状态里有 #31 独占的指令"不等于"这个状态是 #31 一条造成的",
//   共现型 pattern 的状态本来就是多条 pattern 的位集乘出来的。它给的是排序, 不是分解。
struct DFAAttribInfo {
  bool enabled;           // 编译时没开 RE2_DFA_ATTRIB 就是 false
  bool built;             // DFA 还没建出来
  int npat;               // pattern 条数 (= Prog 里 match_id 的最大值 + 1)
  int64_t states_total;   // 生涯建过的状态数 (= DFAMemInfo::states_built_total)
  int64_t shared_insts;   // 落在"多条 pattern 共用"指令上的次数
  int64_t ninst_sum;      // 所有新建状态的 ninst 之和 ⇒ /states_total = 平均"状态宽度"
  int64_t ninst_max;
  // ninst 落在 [2^i, 2^(i+1)) 的状态数。状态宽度就是单个状态的【造价】:
  // WorkqToCachedState 是 O(ninst), 所以这条直方图回答的是"状态变多"还是"状态变胖"。
  int64_t ninst_hist[16];
  // 新建状态时读到了正文的第几个 1/64。回答"这份正文的哪一段在造状态":
  // 平坦 = 全篇都在造 (缓存对这种语料没用); 集中在几个桶 = 有特定文本形态在触发。
  int64_t birth_hist[64];
  // 下面两个由调用方给缓冲区, 按 pattern 下标回填 (给多少填多少):
  //   pat_states[i] = 有多少个新建状态里出现了第 i 条 pattern 独占的指令
  //   pat_insts[i]  = 这些状态里第 i 条独占指令一共出现了多少次 (加权, 反映它对"状态变胖"的贡献)
  int64_t* pat_states;
  int64_t* pat_insts;
  int pat_cap;            // 上面两个缓冲区能放几个 (调用方填)
};

}  // namespace re2

#endif  // RE2_DFA_STATS_H_
