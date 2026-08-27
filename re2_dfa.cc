// Copyright 2008 The RE2 Authors.  All Rights Reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// A DFA (deterministic finite automaton)-based regular expression search.
//
// The DFA search has two main parts: the construction of the automaton,
// which is represented by a graph of State structures, and the execution
// of the automaton over a given input string.
//
// The basic idea is that the State graph is constructed so that the
// execution can simply start with a state s, and then for each byte c in
// the input string, execute "s = s->next[c]", checking at each point whether
// the current s represents a matching state.
//
// The simple explanation just given does convey the essence of this code,
// but it omits the details of how the State graph gets constructed as well
// as some performance-driven optimizations to the execution of the automaton.
// All these details are explained in the comments for the code following
// the definition of class DFA.
//
// See http://swtch.com/~rsc/regexp/ for a very bare-bones equivalent.

#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>   // getenv (RE2_DFA_BIRTH_FILE)
#include <string.h>
#include <algorithm>
#include <atomic>
#include <deque>
#include <mutex>
#include <new>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <utility>
#include <vector>

#include "util/logging.h"
#include "util/mix.h"
#include "util/mutex.h"
#include "util/strutil.h"
#include "re2/pod_array.h"
#include "re2/prog.h"
#include "re2/span_scan.h"   // ── hgmLibre2 追加 ── 流式游程扫描 (实现见文件末尾的 .inc)
#include "re2/re2.h"
#include "re2/sparse_set.h"
#include "re2/stringpiece.h"

// Silence "zero-sized array in struct/union" warning for DFA::State::next_.
#ifdef _MSC_VER
#pragma warning(disable: 4200)
#endif

namespace re2 {

// Controls whether the DFA should bail out early if the NFA would be faster.
static bool dfa_should_bail_when_slow = true;

void Prog::TESTING_ONLY_set_dfa_should_bail_when_slow(bool b) {
  dfa_should_bail_when_slow = b;
}

// Changing this to true compiles in prints that trace execution of the DFA.
// Generates a lot of output -- only useful for debugging.
static const bool ExtraDebug = false;

// A DFA implementation of a regular expression program.
// Since this is entirely a forward declaration mandated by C++,
// some of the comments here are better understood after reading
// the comments in the sections that follow the DFA definition.
// ---- hgmLibre2 编译期开关: DFA 转移表槽位宽度 (非上游 re2) ------------------------------------
// RE2 原版每个 State 后面跟着一排 next_ = State* (8 字节/槽, 槽数 = bytemap_range()+1,
// 真 pattern 表上是 100~200 槽) ⇒ 单个 State 的钱几乎全在这排指针上, 上游文档说的
// "each DFA state costs ~1.1KiB" 就是它。所以"预算够不够"基本等价于"8 字节 × 槽数 × 状态数"。
//
// 本实验把槽位换成【状态表下标】: 4 字节 (RE2_DFA_NEXT_BITS=32) 或 2 字节 (=16)。
//   · 下标 0/1/2 是保留槽 (NULL / DeadState / FullMatchState) 且直接摆在表里 ⇒ 读路径无分支,
//     只多一次"读表基址"的间接;
//   · 状态表满了就整体复制到两倍大的新数组, 旧数组留到 ClearCache 再释放 —— 并发读者手里
//     可能还攥着旧基址在扫, 旧数组必须一直有效 (这就是"复制而不是原地扩")。
//   · 16 位版有硬上限 65533 个状态: 超了当作"预算用尽", 走原本的 ResetCache 那条路。
// 【当前默认】RE2_DFA_NEXT_BITS=32 + RE2_DFA_ARENA=1 + RE2_DFA_ARENA_GROW=1:
//   4 字节槽 + arena(下标=偏移/8) + arena 按需翻倍。4 字节下标 × 8 字节粒度 ⇒ 单个 DFA
//   的 arena 上限 32GB, 远超 RE2 自己的 max_mem 预算, 够用。
// RE2_DFA_NEXT_BITS=64 (要显式给, 且要同时 -DRE2_DFA_ARENA=0) 与原版逐字节一致, 只留作对照。
#ifndef RE2_DFA_NEXT_BITS
#define RE2_DFA_NEXT_BITS 32
#endif

// RE2_DFA_ARENA=1 (只在 32 位下标下有意义): 状态不再一个个 malloc, 而是从一整块 arena 里
// 顺序切出来, 下标 = 【8 字节为单位的偏移量】。于是"下标 → State*"是一次移位加法, 不再多读
// 一次状态表 —— 表版实测健康档掉 30%+, 全部来自那次多出来的依赖读 (load 下标 → load 表 → 用)。
// 保留槽 0/1/2 正好等于 NULL/DeadState/FullMatchState 这三个指针值本身, 所以特殊值那一支
// 连表都不用查; arena 的前 24 字节空着不用。
// 附带两个好处: ①flush 变成"把游标拨回 24", 不用逐个 free 上万个 State;
//              ②状态在内存里连续, 扫描时的空间局部性比一堆独立 malloc 好。
// 16 位下标不能用 arena: 65536 个槽 × 8 字节只够寻址 512KB, 放不下几个状态。
#ifndef RE2_DFA_ARENA
#define RE2_DFA_ARENA (RE2_DFA_NEXT_BITS == 32)   // 默认开; 只有显式退回 64 位槽时才是 0
#endif
#if RE2_DFA_ARENA && RE2_DFA_NEXT_BITS != 32 && \
    !(RE2_DFA_INST_OUT && RE2_DFA_NEXT_BITS == 16)
#error "RE2_DFA_ARENA 只支持 RE2_DFA_NEXT_BITS=32 (或 16+RE2_DFA_INST_OUT)"
#endif

// RE2_DFA_ARENA_GROW=1: arena 不再一上来就按预算开满, 而是从 kArenaInitCap 起步、不够就翻倍
// (realloc, 内容整体搬家), 上限仍是预算。目的: 实际用不到那么多状态时少占内存。
// 搬家会让所有 State* 失效, 所以【不在 CachedState 里扩】—— 那时只持 mutex_, 还有别的线程
// 正拿着 State* 在扫。做法是复用 re2 原本处理"缓存满"的那条路: CachedState 返回 NULL,
// 调用方用 StateSaver 存好手里的状态、把 cache_mutex_ 升成写锁进 ResetCache, 那里没有任何
// 读者, 才真正 realloc + 重定位。对上层来说是一次"没有丢缓存的 flush"。
// next_ 槽存的是偏移量, 搬家后不用动; 要重定位的只有 ①哈希表里的 State* ②每个 State 的
// inst_ ③start_[] 里的起始状态。
#ifndef RE2_DFA_ARENA_GROW
#define RE2_DFA_ARENA_GROW RE2_DFA_ARENA          // 默认跟着 arena 开
#endif
#if RE2_DFA_ARENA_GROW && !RE2_DFA_ARENA
#error "RE2_DFA_ARENA_GROW 需要 RE2_DFA_ARENA=1"
#endif

// RE2_DFA_INST_OUT=1: 把 inst_ (这个 state 对应的那组 NFA 指令编号) 从 State 后面挪到
// 【另一块 arena】。ninst 每个 state 都不一样, 是它把 State 变成了变长对象; 挪走之后
// State 就是定长的 (头 + next_ 转移表), 于是下标可以回到最自然的【第几个 state】,
// 取指针 = arena_ + k*stride_。
//   · 好处: 定长 ⇒ 2 字节下标能寻址 65536 个 state (而不是"偏移量/8"的 512KB),
//     于是 2 字节槽第一次能跟 arena 合体 —— 热循环仍然只有一次内存读, 但每槽只要 2 字节。
//   · 代价: 算一条没缓存过的转移时要多读一次 inst_ 那块 (冷路径, 热循环压根不碰 inst_);
//     还有 k*stride_ 是乘法而不是移位 (stride_ 是运行期常量, 由 pattern 表决定)。
//   · 依赖按需翻倍: 两块 arena 的比例是数据决定的, 没法静态切预算。
#ifndef RE2_DFA_INST_OUT
#define RE2_DFA_INST_OUT 0
#endif
#if RE2_DFA_INST_OUT && (!RE2_DFA_ARENA || !RE2_DFA_ARENA_GROW)
#error "RE2_DFA_INST_OUT 需要 RE2_DFA_ARENA=1 且 RE2_DFA_ARENA_GROW=1"
#endif

// RE2_DFA_STRIDE_POW2=1: 把定长 State 的 stride 补到 2 的幂, 于是"下标 → 地址"是移位
// 而不是乘法 (乘法在指针追逐的依赖链上多 2~3 个周期)。代价是补的那点空白:
// 这批表 368 → 512, 每个 state 多 39%。用来量"乘法到底值多少钱"。
#ifndef RE2_DFA_STRIDE_POW2
#define RE2_DFA_STRIDE_POW2 0
#endif

// RE2_DFA_ORDINAL=0 + RE2_DFA_INST_OUT=1: inst_ 已经挪出去了 (State 定长), 但下标仍按
// 老办法存"字节偏移/8"。纯对照组 —— 用来把"挪走 inst_"和"下标改成序号(要乘 stride)"
// 这两件事的代价拆开量。2 字节槽只能用序号 (偏移/8 只够 512KB), 所以对照组只有 4 字节版。
#ifndef RE2_DFA_ORDINAL
#define RE2_DFA_ORDINAL 1
#endif

// RE2_DFA_ARENA_SHIFT=n (>0): stride 固定成【编译期常量】1<<n, 于是"下标 → 地址"是
// shl 立即数 + add, 跟原来的 lea 只差一个周期; 而 RE2_DFA_STRIDE_POW2 的移位量是运行期
// 变量(要走 %cl, 还得先从 this 里读出来), 实测就是慢。代价: 定长部分补到 1<<n,
// 且 pattern 表大到 State 放不下 1<<n 字节时这个 DFA 直接不可用 (退回 NFA)。
// 172 槽 × 2 字节 + 24 字节头 = 368 ⇒ n=9 (512) 刚好。
#ifndef RE2_DFA_ARENA_SHIFT
#define RE2_DFA_ARENA_SHIFT 0
#endif
#if RE2_DFA_ARENA_SHIFT && !(RE2_DFA_INST_OUT && RE2_DFA_ORDINAL)
#error "RE2_DFA_ARENA_SHIFT 需要 RE2_DFA_INST_OUT=1 且 RE2_DFA_ORDINAL=1"
#endif

// RE2_DFA_HOTSTATS=1: 【只用于测量, 不要进生产】。为回答"缓存爆了能不能只换一部分"
// 而加的三组读数, 都只有真 flush 的那一刻才付代价 (除了 visits_ 那个每字节自增):
//   ① 每个 State 的进入次数 visits_ ⇒ flush 前的热度分布: 前 x% 的状态吃掉了多少 % 的访问。
//      分布越陡, "留住热的那一小撮"这条路的天花板越高。
//   ② 指纹集 seen_fp_ (跨 flush 不清): 新建的状态里有多少是【以前建过又被清掉的】。
//      这是任何"保留策略"能省下来的活儿的上界 —— 重建率低的话, 保留谁都没用。
//   ③ 起始状态可达的 BFS 前缀有多大: "留住起点附近那一圈"这个具体策略能覆盖多少访问。
// 打开后每次 flush 往 stderr 打一行 RE2_HOTSTATS ...; 关掉时零开销、零字段。
#ifndef RE2_DFA_HOTSTATS
#define RE2_DFA_HOTSTATS 0
#endif

// RE2_DFA_TRACE=1: 【只用于测量】把"这段正文依次走过哪些逻辑状态"整条序列 dump 出来。
// 关键性质: 这条序列【与缓存策略无关】—— DFA 是确定的, 同一段正文走的逻辑状态序列永远一样,
// 缓存只决定"这个状态是现成的还是要现造"。所以 dump 一次就够, 之后可以在离线模拟器里把
// 任意淘汰策略 (整表清空 / LRU / LFU / 保留最热一半 / 随机保留) 都跑一遍, 数各自要造几次状态。
// 状态的身份用 (flag, inst 集合) 的指纹映射成稠密 id, 跨 flush 保持不变。
// 用法: -DRE2_DFA_TRACE=1 编译, 运行时 RE2_DFA_TRACE_FILE=xxx.bin (uint32 小端裸流)。
#ifndef RE2_DFA_TRACE
#define RE2_DFA_TRACE 0
#endif
#if RE2_DFA_TRACE && !RE2_DFA_HOTSTATS
#error "RE2_DFA_TRACE 需要 RE2_DFA_HOTSTATS=1 (指纹函数在那边)"
#endif

// RE2_DFA_ATTRIB=1: 【只用于测量/排障, 默认关】把"建状态"这件事归因到三个维度:
//   ① 谁造的  —— 每条 pattern 独占的 NFA 指令在多少个新建状态里出现过 (见 dfa_stats.h)
//   ② 有多贵  —— 新建状态的 ninst (状态"宽度") 直方图。WorkqToCachedState 是 O(ninst),
//                 所以 ninst 就是单个状态的造价; 它回答"状态是变多了还是变胖了"
//   ③ 哪造的  —— 建这个状态时读到了正文的第几个 1/64
// 代价: 归因只在【真的新建一个状态】的时候付 (O(ninst), 与建状态本身同阶), 内层循环零改动;
//       构造 DFA 时多跑一次 O(prog) 的反向定点。关掉时零字段、零代码。
// 运行时 RE2_DFA_BIRTH_FILE=xxx.csv 还会把每个新建状态逐行落盘 (seq,off,len,ninst,owner),
// 用来把"造状态的位置"对回正文原文。
#ifndef RE2_DFA_ATTRIB
#define RE2_DFA_ATTRIB 0
#endif

class DFA {
 public:
  // ── hgmLibre2 追加 ── 流式游程扫描的工作区 (re2_dfa_spanscan.inc)。它要用 State /
  // RWLocker / StateSaver / RunStateOnByteUnlocked 这些只在本编译单元里可见的东西。
  friend class DFASpanScan;
  // 上面那个和"锚定解析"共用的几行 (推一个字节 / 查状态里有没有某条 pattern)。
  friend struct SpanDFA;

  DFA(Prog* prog, Prog::MatchKind kind, int64_t max_mem);
  ~DFA();
  bool ok() const { return !init_failed_; }
  Prog::MatchKind kind() { return kind_; }

  // Searches for the regular expression in text, which is considered
  // as a subsection of context for the purposes of interpreting flags
  // like ^ and $ and \A and \z.
  // Returns whether a match was found.
  // If a match is found, sets *ep to the end point of the best match in text.
  // If "anchored", the match must begin at the start of text.
  // If "want_earliest_match", the match that ends first is used, not
  //   necessarily the best one.
  // If "run_forward" is true, the DFA runs from text.begin() to text.end().
  //   If it is false, the DFA runs from text.end() to text.begin(),
  //   returning the leftmost end of the match instead of the rightmost one.
  // If the DFA cannot complete the search (for example, if it is out of
  //   memory), it sets *failed and returns false.
  bool Search(const StringPiece& text, const StringPiece& context,
              bool anchored, bool want_earliest_match, bool run_forward,
              bool* failed, const char** ep, SparseSet* matches,
              DFAScanStats* stats = NULL);

  // 这个 DFA 缓存的当前水位 + 生涯累计。只读, 短暂拿读锁。
  void MemInfo(DFAMemInfo* out);

  // 建状态的归因读数。只读, 短暂拿读锁。RE2_DFA_ATTRIB=0 时只填 enabled=false。
  void AttribInfo(DFAAttribInfo* out);

  // Builds out all states for the entire DFA.
  // If cb is not empty, it receives one callback per state built.
  // Returns the number of states built.
  // FOR TESTING OR EXPERIMENTAL PURPOSES ONLY.
  int BuildAllStates(const Prog::DFAStateCallback& cb);

  // Computes min and max for matching strings.  Won't return strings
  // bigger than maxlen.
  bool PossibleMatchRange(std::string* min, std::string* max, int maxlen);

  // These data structures are logically private, but C++ makes it too
  // difficult to mark them as such.
  class RWLocker;
  class StateSaver;
  class Workq;

#if RE2_DFA_NEXT_BITS == 32
  typedef uint32_t StateIdx;          // next_ 槽位 = 状态表下标 (4 字节)
#elif RE2_DFA_NEXT_BITS == 16
  typedef uint16_t StateIdx;          // next_ 槽位 = 状态表下标 (2 字节)
#endif
#if RE2_DFA_NEXT_BITS != 64
  // 状态表里前三个槽是保留的特殊值, 真状态从 3 开始。
  enum { kStblNull = 0, kStblDead = 1, kStblFullMatch = 2, kStblFirst = 3,
         kStblInitCap = 1024,     // 起始容量; 满了翻倍(整体复制)
         kArenaInitCap = 64 << 10 };  // RE2_DFA_ARENA_GROW 的 arena 起始容量
  // 16 位版的硬上限: 下标存不下就只能当"缓存满了"处理。
  static const uint32_t kStblMax =
      (RE2_DFA_NEXT_BITS == 16) ? 65536u : 0xFFFFFFFFu;
#endif

  // A single DFA state.  The DFA is represented as a graph of these
  // States, linked by the next_ pointers.  If in state s and reading
  // byte c, the next state should be s->next_[c].
  struct State {
    inline bool IsMatch() const { return (flag_ & kFlagMatch) != 0; }

    int* inst_;         // Instruction pointers in the state.
    int ninst_;         // # of inst_ pointers.
    uint32_t flag_;     // Empty string bitfield flags in effect on the way
                        // into this state, along with kFlagMatch if this
                        // is a matching state.

// Work around the bug affecting flexible array members in GCC 6.x (for x >= 1).
// (https://gcc.gnu.org/bugzilla/show_bug.cgi?id=70932)
#if RE2_DFA_NEXT_BITS != 64
    uint32_t idx_;      // 本 State 在 DFA::stbl_ 里的下标 (>= kStblFirst)
#endif
#if RE2_DFA_HOTSTATS
    uint32_t visits_;   // 测量用: 被进入过几次 (热度)。非原子 —— 并发下会丢更新, 量分布够用。
#endif
#if RE2_DFA_TRACE
    uint32_t tid_;      // 测量用: 逻辑状态 id (同一个 (flag,inst) 跨 flush 始终同一个 id)
#endif

#if RE2_DFA_NEXT_BITS == 64
#if !defined(__clang__) && defined(__GNUC__) && __GNUC__ == 6 && __GNUC_MINOR__ >= 1
    std::atomic<State*> next_[0];   // Outgoing arrows from State,
#else
    std::atomic<State*> next_[];    // Outgoing arrows from State,
#endif
#else
#if !defined(__clang__) && defined(__GNUC__) && __GNUC__ == 6 && __GNUC_MINOR__ >= 1
    std::atomic<StateIdx> next_[0]; // 同上, 但存的是状态表下标
#else
    std::atomic<StateIdx> next_[];  // 同上, 但存的是状态表下标
#endif
#endif

                        // one per input byte class
  };

  enum {
    kByteEndText = 256,         // imaginary byte at end of text

    kFlagEmptyMask = 0xFF,      // State.flag_: bits holding kEmptyXXX flags
    kFlagMatch = 0x0100,        // State.flag_: this is a matching state
    kFlagLastWord = 0x0200,     // State.flag_: last byte was a word char
    kFlagNeedShift = 16,        // needed kEmpty bits are or'ed in shifted left
  };

  struct StateHash {
    size_t operator()(const State* a) const {
      DCHECK(a != NULL);
      HashMix mix(a->flag_);
      for (int i = 0; i < a->ninst_; i++)
        mix.Mix(a->inst_[i]);
      mix.Mix(0);
      return mix.get();
    }
  };

  struct StateEqual {
    bool operator()(const State* a, const State* b) const {
      DCHECK(a != NULL);
      DCHECK(b != NULL);
      if (a == b)
        return true;
      if (a->flag_ != b->flag_)
        return false;
      if (a->ninst_ != b->ninst_)
        return false;
      for (int i = 0; i < a->ninst_; i++)
        if (a->inst_[i] != b->inst_[i])
          return false;
      return true;
    }
  };

  typedef std::unordered_set<State*, StateHash, StateEqual> StateSet;

 private:
  // Make it easier to swap in a scalable reader-writer mutex.
  using CacheMutex = Mutex;

  enum {
    // Indices into start_ for unanchored searches.
    // Add kStartAnchored for anchored searches.
    kStartBeginText = 0,          // text at beginning of context
    kStartBeginLine = 2,          // text at beginning of line
    kStartAfterWordChar = 4,      // text follows a word character
    kStartAfterNonWordChar = 6,   // text follows non-word character

    // ── hgmLibre2 追加 ── 种【全部指令】的那个起始状态 (可行前缀回推用, 见
    // re2_dfa_spanscan.inc 的 SpanDFA::ViableStarts)。与上面四个 base 或起来用
    // (8/10/12/14)。它与 kStartAnchored 不是一回事, 也不冲突: 那个选的是"从 start
    // 还是 start_unanchored 进", 这个是"锚定入口可达的每一条指令都是起点"。
    // 摆进 start_[] 里是为了白拿三样东西 —— arena 搬家时的重定位 · ResetCache 的清空 ·
    // 内存归因那趟 BFS 的起点, 三处都是 for (i < kMaxStart) 的循环。
    kStartViable = 8,
    kMaxStart = 16,

    kStartAnchored = 1,
  };

  // Resets the DFA State cache, flushing all saved State* information.
  // Releases and reacquires cache_mutex_ via cache_lock, so any
  // State* existing before the call are not valid after the call.
  // Use a StateSaver to preserve important states across the call.
  // cache_mutex_.r <= L < mutex_
  // After: cache_mutex_.w <= L < mutex_
  // stats != NULL 时把这次的动作记进去: 真清空 → flushes++, 只是扩 arena → grows++。
  void ResetCache(RWLocker* cache_lock, DFAScanStats* stats = NULL);

  // next_ 槽位的读写。64 位版就是原来的指针存取 (编译产物与原版一致);
  // 索引版多一次"读状态表基址"的间接, 换来的是每槽 8 → 4/2 字节。
  inline State* NextOf(State* s, int i) const;
  inline void SetNextOf(State* s, int i, State* ns);
  // 一个 State 的字节数。分配 (CachedState) 与释放 (ClearCache) 必须走同一个算式,
  // 否则 sized delete 会拿错尺寸。索引版把 inst_ 的对齐补齐算进 next_ 段里。
  static size_t NextArrayBytes(int nnext);
  static int StateMemSize(int nnext, int ninst);
#if RE2_DFA_NEXT_BITS != 64
  // 把新状态登记进状态表, 返回下标; 表满 (16 位版的 65536 上限) 返回 0。
  // L >= mutex_ (增长时会换基址, 与 CachedState 同一把锁)
  uint32_t StblAppend(State* s);
  void StblClear();   // 释放全部状态表 (新旧), 重新只留 3 个保留槽
#endif
#if RE2_DFA_ARENA_GROW
  // 把 arena 扩到至少放得下 need 字节并重定位全部状态。只能在 cache_mutex_ 写锁下调。
  // 返回 false 表示已经到顶、扩不动了 (调用方改走真 flush)。
  bool GrowArena(size_t need);
#if RE2_DFA_INST_OUT
  bool GrowIArena(size_t need);   // 同上, 扩 inst_ 那块
#endif
  bool GrowPending() const {      // 上一次失败是不是"arena 该扩了"
#if RE2_DFA_INST_OUT
    return arena_need_ > 0 || iarena_need_ > 0;
#else
    return arena_need_ > 0;
#endif
  }
#endif

  // Looks up and returns the State corresponding to a Workq.
  // L >= mutex_
  State* WorkqToCachedState(Workq* q, Workq* mq, uint32_t flag);

  // Looks up and returns a State matching the inst, ninst, and flag.
  // L >= mutex_
  State* CachedState(int* inst, int ninst, uint32_t flag);

  // Clear the cache entirely.
  // Must hold cache_mutex_.w or be in destructor.
  void ClearCache();

  // Converts a State into a Workq: the opposite of WorkqToCachedState.
  // L >= mutex_
  void StateToWorkq(State* s, Workq* q);

  // Runs a State on a given byte, returning the next state.
  State* RunStateOnByteUnlocked(State*, int);  // cache_mutex_.r <= L < mutex_
  State* RunStateOnByte(State*, int);          // L >= mutex_

  // Runs a Workq on a given byte followed by a set of empty-string flags,
  // producing a new Workq in nq.  If a match instruction is encountered,
  // sets *ismatch to true.
  // L >= mutex_
  void RunWorkqOnByte(Workq* q, Workq* nq,
                      int c, uint32_t flag, bool* ismatch);

  // Runs a Workq on a set of empty-string flags, producing a new Workq in nq.
  // L >= mutex_
  void RunWorkqOnEmptyString(Workq* q, Workq* nq, uint32_t flag);

  // Adds the instruction id to the Workq, following empty arrows
  // according to flag.
  // L >= mutex_
  void AddToQueue(Workq* q, int id, uint32_t flag);

  // For debugging, returns a text representation of State.
  static std::string DumpState(State* state);

  // For debugging, returns a text representation of a Workq.
  static std::string DumpWorkq(Workq* q);

  // Search parameters
  struct SearchParams {
    SearchParams(const StringPiece& text, const StringPiece& context,
                 RWLocker* cache_lock)
      : text(text),
        context(context),
        anchored(false),
        can_prefix_accel(false),
        want_earliest_match(false),
        run_forward(false),
        start(NULL),
        cache_lock(cache_lock),
        failed(false),
        ep(NULL),
        matches(NULL),
        stats(NULL) {}

    StringPiece text;
    StringPiece context;
    bool anchored;
    bool can_prefix_accel;
    bool want_earliest_match;
    bool run_forward;
    State* start;
    RWLocker* cache_lock;
    bool failed;     // "out" parameter: whether search gave up
    const char* ep;  // "out" parameter: end pointer for match
    SparseSet* matches;
    DFAScanStats* stats;  // "out" parameter: 本次扫描的 DFA 计数 (NULL = 不统计)

   private:
    SearchParams(const SearchParams&) = delete;
    SearchParams& operator=(const SearchParams&) = delete;
  };

  // Before each search, the parameters to Search are analyzed by
  // AnalyzeSearch to determine the state in which to start.
  struct StartInfo {
    StartInfo() : start(NULL) {}
    std::atomic<State*> start;
  };

  // Fills in params->start and params->can_prefix_accel using
  // the other search parameters.  Returns true on success,
  // false on failure.
  // cache_mutex_.r <= L < mutex_
  bool AnalyzeSearch(SearchParams* params);
  bool AnalyzeSearchHelper(SearchParams* params, StartInfo* info,
                           uint32_t flags);

  // The generic search loop, inlined to create specialized versions.
  // cache_mutex_.r <= L < mutex_
  // Might unlock and relock cache_mutex_ via params->cache_lock.
  template <bool can_prefix_accel,
            bool want_earliest_match,
            bool run_forward>
  inline bool InlinedSearchLoop(SearchParams* params);

  // The specialized versions of InlinedSearchLoop.  The three letters
  // at the ends of the name denote the true/false values used as the
  // last three parameters of InlinedSearchLoop.
  // cache_mutex_.r <= L < mutex_
  // Might unlock and relock cache_mutex_ via params->cache_lock.
  bool SearchFFF(SearchParams* params);
  bool SearchFFT(SearchParams* params);
  bool SearchFTF(SearchParams* params);
  bool SearchFTT(SearchParams* params);
  bool SearchTFF(SearchParams* params);
  bool SearchTFT(SearchParams* params);
  bool SearchTTF(SearchParams* params);
  bool SearchTTT(SearchParams* params);

  // The main search loop: calls an appropriate specialized version of
  // InlinedSearchLoop.
  // cache_mutex_.r <= L < mutex_
  // Might unlock and relock cache_mutex_ via params->cache_lock.
  bool FastSearchLoop(SearchParams* params);


  // Looks up bytes in bytemap_ but handles case c == kByteEndText too.
  int ByteMap(int c) {
    if (c == kByteEndText)
      return prog_->bytemap_range();
    return prog_->bytemap()[c];
  }

  // Constant after initialization.
  Prog* prog_;              // The regular expression program to run.
  Prog::MatchKind kind_;    // The kind of DFA.
  bool init_failed_;        // initialization failed (out of memory)

  Mutex mutex_;  // mutex_ >= cache_mutex_.r

  // Scratch areas, protected by mutex_.
  Workq* q0_;             // Two pre-allocated work queues.
  Workq* q1_;
  PODArray<int> stack_;   // Pre-allocated stack for AddToQueue

  // State* cache.  Many threads use and add to the cache simultaneously,
  // holding cache_mutex_ for reading and mutex_ (above) when adding.
  // If the cache fills and needs to be discarded, the discarding is done
  // while holding cache_mutex_ for writing, to avoid interrupting other
  // readers.  Any State* pointers are only valid while cache_mutex_
  // is held.
  CacheMutex cache_mutex_;
  // ── 按对象归因的生涯计数 (见 re2/dfa_stats.h) ──
  // 挂在 DFA 上而不是进程全局, 所以"是哪个 Set 在 thrash"能直接答。
  // flushes_total_ 在写锁下改; states_built_ 在 mutex_ 下改。读的时候都不持锁, 故用 atomic。
  std::atomic<int64_t> flushes_total_{0};
  std::atomic<int64_t> states_built_{0};
#if RE2_DFA_HOTSTATS
  std::unordered_set<uint64_t> seen_fp_;  // 跨 flush 不清: 见过的状态指纹
  int64_t built_first_ = 0;               // 头一次建
  int64_t built_again_ = 0;               // 以前建过、被清掉、又建一遍
  // ── "保留一部分"能省多少活儿: 上一代被清掉的状态按访问次数排名, 这一代重建时看排名 ──
  // prev_rank_ : 上一代每个状态的指纹 → 它在上一代里按 visits 降序的名次 (0 = 最热)
  // prev_n_    : 上一代的状态总数 (名次的分母)
  // rebuild_bucket_ : 这一代新建的状态, 按"它在上一代的名次落在哪个百分位段"计数
  //                   —— 落在前 x% 的那些, 正是"保留前 x% 热状态"这条策略能省掉的重建。
#if RE2_DFA_TRACE
  std::unordered_map<uint64_t, uint32_t> fp2id_;  // 指纹 → 稠密逻辑 id (跨 flush 不清)
  static void TraceVisit(uint32_t id);
  static void TraceFlushNow();   // 每次扫描结束落一次盘: Go 的退出不走 libc atexit
#endif
  std::unordered_map<uint64_t, uint32_t> prev_rank_;
  size_t prev_n_ = 0;
  int64_t rebuild_bucket_[7] = {0,0,0,0,0,0,0};  // <1% <5% <10% <25% <50% <100% / 上一代没有
  uint64_t StateFingerprint(uint32_t flag, const int* inst, int ninst) const;
  void HotStatsDump();                    // 真 flush 前把这一世代的账打出来
#endif
#if RE2_DFA_ATTRIB
  // ── 建状态归因 (RE2_DFA_ATTRIB) ──
  // inst_owner_[id] = 独占这条指令的 pattern 下标; -1 = 多条 pattern 共用, 或到不了任何 Match。
  // 构造时由 BuildInstOwner() 跑一次反向可达定点算出来, 之后只读。
  std::vector<int32_t> inst_owner_;
  int atr_npat_ = 0;
  std::vector<int64_t> pat_states_;   // 每条 pattern: 出现在多少个新建状态里
  std::vector<int64_t> pat_insts_;    // 每条 pattern: 独占指令在新建状态里出现的总次数
  std::vector<uint32_t> pat_seen_;    // 每状态去重用的世代戳 (免得清一遍数组)
  std::vector<uint32_t> pat_cnt_;     // 本状态里每条 pattern 各出现了几个零件 (跟 pat_seen_ 同世代)
  uint32_t atr_epoch_ = 0;
  int64_t atr_shared_insts_ = 0;
  int64_t atr_ninst_sum_ = 0, atr_ninst_max_ = 0;
  int64_t atr_ninst_hist_[16] = {0};
  int64_t atr_birth_hist_[64] = {0};
  // 当前扫描读到哪了。只在【缓存未命中】那一刻更新 (内层命中路径一行不动),
  // 所以是"最近一次未命中的位置", 对"新建状态在哪"来说正好。
  // ⚠ 并发扫同一个 DFA 时这两个值会互相覆盖 —— 位置归因要单线程量。
  size_t atr_off_ = 0, atr_len_ = 0;
  FILE* atr_birth_file_ = NULL;
  int64_t atr_birth_seq_ = 0;
  void BuildInstOwner();                          // 构造时跑一次
  void AttribNewState(const int* inst, int ninst);  // CachedState 里每建一个状态调一次
#endif
  int64_t mem_budget_;     // Total memory budget for all States.
  int64_t state_budget_;   // Amount of memory remaining for new States.
  StateSet state_cache_;   // All States computed so far.
  StartInfo start_[kMaxStart];

#if RE2_DFA_NEXT_BITS != 64
  // 状态表 (下标 → State*)。容量在 ClearCache 里【一次算死】= 预算最多装得下多少个 State,
  // 所以一个缓存世代内基址不动 —— 读路径可以把它留在寄存器里, 不用每个字节重读一次原子基址。
  // (第一版是"满了复制到两倍大的新数组", 那样基址会在读者脚下换掉, 只能每字节原子读一次基址,
  //  实测健康档因此掉 30%+。容量本来就有硬上界: 每个 State 至少 min_state 字节。)
#if RE2_DFA_ARENA
  char* arena_ = NULL;     // 状态区; 下标 = (指针 - arena_) / 8 · mutex_ 保护
  size_t arena_len_ = 0;   // 已切出去的字节数 (从 kStblFirst*8 起步, 前 24 字节留给保留槽)
  size_t arena_cap_ = 0;   // arena 当前容量
  size_t arena_max_ = 0;   // arena 容量上限 = state_budget_ + 一点富余
  size_t arena_need_ = 0;  // >0 表示"刚才是 arena 装不下才失败的, 还差这么多字节"
#if RE2_DFA_INST_OUT
  size_t stride_ = 0;      // 每个 State 的定长字节数 (头 + next_), 8 字节对齐
#if RE2_DFA_STRIDE_POW2
  int stride_shift_ = 0;   // stride_ == 1 << stride_shift_
#endif
  char* iarena_ = NULL;    // inst_ 专用 arena
  size_t iarena_len_ = 0;
  size_t iarena_cap_ = 0;
  size_t iarena_max_ = 0;
  size_t iarena_need_ = 0;
#endif
#else
  State** stbl_ = NULL;   // 基址; [0]=NULL [1]=DeadState [2]=FullMatchState
  uint32_t stbl_len_ = 0; // 已用槽数 (含 3 个保留槽) · mutex_ 保护
  uint32_t stbl_cap_ = 0; // 容量 · 一个缓存世代内固定
#endif
#endif

  DFA(const DFA&) = delete;
  DFA& operator=(const DFA&) = delete;
};

// Shorthand for casting to uint8_t*.
static inline const uint8_t* BytePtr(const void* v) {
  return reinterpret_cast<const uint8_t*>(v);
}

// Work queues

// Marks separate thread groups of different priority
// in the work queue when in leftmost-longest matching mode.
#define Mark (-1)

// Separates the match IDs from the instructions in inst_.
// Used only for "many match" DFA states.
#define MatchSep (-2)

// Internally, the DFA uses a sparse array of
// program instruction pointers as a work queue.
// In leftmost longest mode, marks separate sections
// of workq that started executing at different
// locations in the string (earlier locations first).
class DFA::Workq : public SparseSet {
 public:
  // Constructor: n is number of normal slots, maxmark number of mark slots.
  Workq(int n, int maxmark) :
    SparseSet(n+maxmark),
    n_(n),
    maxmark_(maxmark),
    nextmark_(n),
    last_was_mark_(true) {
  }

  bool is_mark(int i) { return i >= n_; }

  int maxmark() { return maxmark_; }

  void clear() {
    SparseSet::clear();
    nextmark_ = n_;
  }

  void mark() {
    if (last_was_mark_)
      return;
    last_was_mark_ = false;
    SparseSet::insert_new(nextmark_++);
  }

  int size() {
    return n_ + maxmark_;
  }

  void insert(int id) {
    if (contains(id))
      return;
    insert_new(id);
  }

  void insert_new(int id) {
    last_was_mark_ = false;
    SparseSet::insert_new(id);
  }

 private:
  int n_;                // size excluding marks
  int maxmark_;          // maximum number of marks
  int nextmark_;         // id of next mark
  bool last_was_mark_;   // last inserted was mark

  Workq(const Workq&) = delete;
  Workq& operator=(const Workq&) = delete;
};

DFA::DFA(Prog* prog, Prog::MatchKind kind, int64_t max_mem)
  : prog_(prog),
    kind_(kind),
    init_failed_(false),
    q0_(NULL),
    q1_(NULL),
    mem_budget_(max_mem) {
  if (ExtraDebug)
    fprintf(stderr, "\nkind %d\n%s\n", kind_, prog_->DumpUnanchored().c_str());
  int nmark = 0;
  if (kind_ == Prog::kLongestMatch)
    nmark = prog_->size();
  // See DFA::AddToQueue() for why this is so.
  int nstack = prog_->inst_count(kInstCapture) +
               prog_->inst_count(kInstEmptyWidth) +
               prog_->inst_count(kInstNop) +
               nmark + 1;  // + 1 for start inst

  // Account for space needed for DFA, q0, q1, stack.
  mem_budget_ -= sizeof(DFA);
  mem_budget_ -= (prog_->size() + nmark) *
                 (sizeof(int)+sizeof(int)) * 2;  // q0, q1
  mem_budget_ -= nstack * sizeof(int);  // stack
  if (mem_budget_ < 0) {
    init_failed_ = true;
    return;
  }

  state_budget_ = mem_budget_;

  // Make sure there is a reasonable amount of working room left.
  // At minimum, the search requires room for two states in order
  // to limp along, restarting frequently.  We'll get better performance
  // if there is room for a larger number of states, say 20.
  // Note that a state stores list heads only, so we use the program
  // list count for the upper bound, not the program size.
  int nnext = prog_->bytemap_range() + 1;  // + 1 for kByteEndText slot
#if RE2_DFA_INST_OUT
  // State 的定长部分 = 头 + next_ 转移表, 补到 8 字节对齐 (下标 × stride_ 要还是 8 对齐)。
  stride_ = (sizeof(State) + NextArrayBytes(nnext) + 7) & ~static_cast<size_t>(7);
#if RE2_DFA_ARENA_SHIFT
  if (stride_ > (static_cast<size_t>(1) << RE2_DFA_ARENA_SHIFT)) {
    init_failed_ = true;   // 这批 pattern 的 State 放不进固定格子
    return;
  }
  stride_ = static_cast<size_t>(1) << RE2_DFA_ARENA_SHIFT;
#endif
#if RE2_DFA_STRIDE_POW2
  stride_shift_ = 3;
  while ((static_cast<size_t>(1) << stride_shift_) < stride_)
    stride_shift_++;
  stride_ = static_cast<size_t>(1) << stride_shift_;
#endif
#endif
  int64_t one_state = sizeof(State) + NextArrayBytes(nnext) +
                      (prog_->list_count()+nmark)*sizeof(int);
#if RE2_DFA_NEXT_BITS != 64
  one_state += sizeof(State*);   // 状态表里的那一格
#endif
  if (state_budget_ < 20*one_state) {
    init_failed_ = true;
    return;
  }

#if RE2_DFA_NEXT_BITS != 64
  StblClear();   // 建出只有 3 个保留槽的初始状态表
#endif

  q0_ = new Workq(prog_->size(), nmark);
  q1_ = new Workq(prog_->size(), nmark);
  stack_ = PODArray<int>(nstack);
#if RE2_DFA_ATTRIB
  BuildInstOwner();
  if (const char* f = getenv("RE2_DFA_BIRTH_FILE"))
    atr_birth_file_ = fopen(f, "w");
  if (atr_birth_file_ != NULL)
    fprintf(atr_birth_file_, "seq,off,len,ninst,owner\n");
#endif
}

#if RE2_DFA_ATTRIB
// BuildInstOwner —— 算出每条 NFA 指令"专属于哪条 pattern"。
//
// 【为什么不能用正向可达】起点那条 .* 循环能走到所有 pattern, 而非锚定搜索的每个状态里
// 都躺着它 ⇒ 拿"这个状态能到哪些 pattern"当归因, 答案永远是"全部", 没有信息量。
// 所以这里算的是【反向】: 从每个 kInstMatch 出发倒着走, 看每条指令能到达哪几个 Match。
// 只能到一个 ⇒ 它是那条 pattern 的私有零件, 拿它当代表; 能到多个 ⇒ 公共零件, 记 shared。
//
// 边的口径与 DFA::AddToQueue / RunWorkqOnByte 保持一致: 同一个 list 里的下一条 (!last ⇒ id+1),
// 加上 out()/out1()。宁可多算 (归成 shared) 也不要漏算 (错归给某一条)。
void DFA::BuildInstOwner() {
  const int n = prog_->size();
  inst_owner_.assign(n, -1);
  if (n <= 0)
    return;

  // pattern 条数 = match_id 最大值 + 1。单条 RE2 的 match_id 恒为 0, 于是 npat=1,
  // 归因退化成"要么属于这条正则要么是公共部分", 无害。
  int npat = 0;
  for (int id = 0; id < n; id++) {
    Prog::Inst* ip = prog_->inst(id);
    if (ip->opcode() == kInstMatch && ip->match_id() + 1 > npat)
      npat = ip->match_id() + 1;
  }
  atr_npat_ = npat;
  if (npat <= 0)
    return;
  pat_states_.assign(npat, 0);
  pat_insts_.assign(npat, 0);
  pat_seen_.assign(npat, 0);
  pat_cnt_.assign(npat, 0);

  const int W = (npat + 63) / 64;          // 每条指令一个 npat 位的位集
  std::vector<uint64_t> reach(static_cast<size_t>(n) * W, 0);
  std::vector<std::vector<int>> preds(n);  // 反向边

  auto add_edge = [&](int from, int to) {
    if (to > 0 && to < n)
      preds[to].push_back(from);
  };
  for (int id = 0; id < n; id++) {
    Prog::Inst* ip = prog_->inst(id);
    switch (ip->opcode()) {
      case kInstMatch:
        reach[static_cast<size_t>(id) * W + ip->match_id() / 64] |=
            uint64_t{1} << (ip->match_id() % 64);
        break;
      case kInstAlt:
      case kInstAltMatch:
        add_edge(id, ip->out());
        add_edge(id, ip->out1());
        break;
      case kInstByteRange:
      case kInstCapture:
      case kInstNop:
      case kInstEmptyWidth:
        add_edge(id, ip->out());
        break;
      default:   // kInstFail: 走不出去
        break;
    }
    // list 里的下一条: AddToQueue 对 !last() 一律往 id+1 走。
    if (ip->opcode() != kInstFail && !ip->last())
      add_edge(id, id + 1);
  }

  // worklist 定点。图里有环 (正则的循环), 所以不能拓扑一遍了事;
  // 但每条指令的位集只增不减, 每次变化才把前驱重新入队, 会收敛。
  std::vector<int> work;
  std::vector<char> inq(n, 0);
  work.reserve(n);
  for (int id = 0; id < n; id++) {
    if (prog_->inst(id)->opcode() == kInstMatch) {
      work.push_back(id);
      inq[id] = 1;
    }
  }
  while (!work.empty()) {
    int id = work.back();
    work.pop_back();
    inq[id] = 0;
    const uint64_t* mine = &reach[static_cast<size_t>(id) * W];
    for (int pd : preds[id]) {
      uint64_t* theirs = &reach[static_cast<size_t>(pd) * W];
      bool changed = false;
      for (int w = 0; w < W; w++) {
        uint64_t nv = theirs[w] | mine[w];
        if (nv != theirs[w]) {
          theirs[w] = nv;
          changed = true;
        }
      }
      if (changed && !inq[pd]) {
        work.push_back(pd);
        inq[pd] = 1;
      }
    }
  }

  for (int id = 0; id < n; id++) {
    const uint64_t* r = &reach[static_cast<size_t>(id) * W];
    int owner = -1;
    int cnt = 0;
    for (int w = 0; w < W && cnt < 2; w++) {
      uint64_t v = r[w];
      while (v != 0 && cnt < 2) {
        int b = 0;
        while (((v >> b) & 1) == 0) b++;
        owner = w * 64 + b;
        cnt++;
        v &= ~(uint64_t{1} << b);
      }
    }
    inst_owner_[id] = (cnt == 1) ? owner : -1;   // 能到多条 ⇒ 公共零件
  }
}

// AttribNewState —— CachedState 里【真的新建了一个状态】时调一次。O(ninst), 与建状态同阶。
void DFA::AttribNewState(const int* inst, int ninst) {
  // ① 宽度 (= 单个状态的造价)
  atr_ninst_sum_ += ninst;
  if (ninst > atr_ninst_max_)
    atr_ninst_max_ = ninst;
  int b = 0;
  while (b < 15 && (1 << (b + 1)) <= ninst)
    b++;
  atr_ninst_hist_[b]++;

  // ② 归因到 pattern。inst_ 的布局是 [inst ids...] MatchSep [match ids...],
  //    MatchSep 之后那截是匹配到的 pattern 下标而不是指令号, 必须停在这里。
  atr_epoch_++;
  int top_owner = -1;
  uint32_t top_cnt = 0;
  for (int i = 0; i < ninst; i++) {
    int id = inst[i];
    if (id == MatchSep)
      break;
    if (id < 0 || id >= static_cast<int>(inst_owner_.size()))
      continue;                        // Mark 之类的哨兵
    int o = inst_owner_[id];
    if (o < 0) {
      atr_shared_insts_++;
      continue;
    }
    pat_insts_[o]++;
    if (pat_seen_[o] != atr_epoch_) {  // 世代戳: 每状态对每条 pattern 只记一次"出现过"
      pat_seen_[o] = atr_epoch_;
      pat_states_[o]++;
      pat_cnt_[o] = 0;
    }
    if (++pat_cnt_[o] > top_cnt) {     // 本状态里零件最多的那条 = 落盘用的"主责"
      top_cnt = pat_cnt_[o];
      top_owner = o;
    }
  }

  // ③ 位置: 建这个状态时读到正文的第几个 1/64
  if (atr_len_ > 0) {
    size_t bk = atr_off_ * 64 / atr_len_;
    if (bk > 63) bk = 63;
    atr_birth_hist_[bk]++;
  } else {
    atr_birth_hist_[0]++;
  }
  if (atr_birth_file_ != NULL) {
    fprintf(atr_birth_file_, "%lld,%zu,%zu,%d,%d\n",
            static_cast<long long>(atr_birth_seq_++), atr_off_, atr_len_,
            ninst, top_owner);
  }
}
#endif  // RE2_DFA_ATTRIB

DFA::~DFA() {
  delete q0_;
  delete q1_;
  ClearCache();
#if RE2_DFA_ATTRIB
  if (atr_birth_file_ != NULL) {
    fclose(atr_birth_file_);
    atr_birth_file_ = NULL;
  }
#endif
#if RE2_DFA_NEXT_BITS != 64
  // ClearCache 里的 StblClear 会重建一张空表 (它平时是给 flush 用的), 这里彻底还掉。
#if RE2_DFA_ARENA
  free(arena_);
  arena_ = NULL;
#if RE2_DFA_INST_OUT
  free(iarena_);
  iarena_ = NULL;
#endif
#else
  delete[] stbl_;
  stbl_ = NULL;
#endif
#endif
}

// In the DFA state graph, s->next[c] == NULL means that the
// state has not yet been computed and needs to be.  We need
// a different special value to signal that s->next[c] is a
// state that can never lead to a match (and thus the search
// can be called off).  Hence DeadState.
#define DeadState reinterpret_cast<State*>(1)

// Signals that the rest of the string matches no matter what it is.
#define FullMatchState reinterpret_cast<State*>(2)

#define SpecialStateMax FullMatchState

#if RE2_DFA_NEXT_BITS == 64

inline DFA::State* DFA::NextOf(State* s, int i) const {
  return s->next_[i].load(std::memory_order_acquire);
}

inline void DFA::SetNextOf(State* s, int i, State* ns) {
  s->next_[i].store(ns, std::memory_order_release);
}

#else

// 读: 一次 4/2 字节的原子读拿到下标, 再从状态表取指针。
// 特殊值 (NULL / DeadState / FullMatchState) 就摆在表的前三槽里, 所以这里【没有分支】。
inline DFA::State* DFA::NextOf(State* s, int i) const {
  StateIdx k = s->next_[i].load(std::memory_order_acquire);
#if RE2_DFA_ARENA
  // k <= 2 时 k 本身就是那三个特殊"指针值"(NULL=0 / DeadState=1 / FullMatchState=2),
  // 直接转回去即可; 否则是 arena 里的 8 字节偏移。全程不再碰第二块内存。
  if (k <= kStblFullMatch)
    return reinterpret_cast<State*>(static_cast<uintptr_t>(k));
#if RE2_DFA_INST_OUT && !RE2_DFA_ORDINAL
  return reinterpret_cast<State*>(arena_ + (static_cast<size_t>(k) << 3));
#elif RE2_DFA_ARENA_SHIFT
  return reinterpret_cast<State*>(arena_ + (static_cast<size_t>(k)
                                            << RE2_DFA_ARENA_SHIFT));
#elif RE2_DFA_INST_OUT
  // State 定长 ⇒ 下标就是"第几个 state", 一次乘加 (stride 补成 2 的幂时是移位)。
#if RE2_DFA_STRIDE_POW2
  return reinterpret_cast<State*>(arena_ + (static_cast<size_t>(k)
                                            << stride_shift_));
#else
  return reinterpret_cast<State*>(arena_ + static_cast<size_t>(k) * stride_);
#endif
#else
  return reinterpret_cast<State*>(arena_ + (static_cast<size_t>(k) << 3));
#endif
#else
  return stbl_[k];
#endif
}

// 写: 只在"这条转移第一次算出来"时走一次, 分支开销无所谓。
// release 存下标 —— 与读侧的 acquire 配对, 保证读者看见下标时, 状态表那一格早已写好。
inline void DFA::SetNextOf(State* s, int i, State* ns) {
  StateIdx k;
  if (ns == NULL)
    k = kStblNull;
  else if (ns == DeadState)
    k = kStblDead;
  else if (ns == FullMatchState)
    k = kStblFullMatch;
  else
    k = static_cast<StateIdx>(ns->idx_);
  s->next_[i].store(k, std::memory_order_release);
}

#endif

size_t DFA::NextArrayBytes(int nnext) {
#if RE2_DFA_NEXT_BITS == 64
  return nnext * sizeof(std::atomic<State*>);
#else
  size_t n = nnext * sizeof(std::atomic<StateIdx>);
  size_t pad = (alignof(int) - (sizeof(State) + n) % alignof(int)) % alignof(int);
  return n + pad;
#endif
}

int DFA::StateMemSize(int nnext, int ninst) {
  return static_cast<int>(sizeof(State) + NextArrayBytes(nnext) +
                          ninst * sizeof(int));
}

// Debugging printouts

// For debugging, returns a string representation of the work queue.
std::string DFA::DumpWorkq(Workq* q) {
  std::string s;
  const char* sep = "";
  for (Workq::iterator it = q->begin(); it != q->end(); ++it) {
    if (q->is_mark(*it)) {
      s += "|";
      sep = "";
    } else {
      s += StringPrintf("%s%d", sep, *it);
      sep = ",";
    }
  }
  return s;
}

// For debugging, returns a string representation of the state.
std::string DFA::DumpState(State* state) {
  if (state == NULL)
    return "_";
  if (state == DeadState)
    return "X";
  if (state == FullMatchState)
    return "*";
  std::string s;
  const char* sep = "";
  s += StringPrintf("(%p)", state);
  for (int i = 0; i < state->ninst_; i++) {
    if (state->inst_[i] == Mark) {
      s += "|";
      sep = "";
    } else if (state->inst_[i] == MatchSep) {
      s += "||";
      sep = "";
    } else {
      s += StringPrintf("%s%d", sep, state->inst_[i]);
      sep = ",";
    }
  }
  s += StringPrintf(" flag=%#x", state->flag_);
  return s;
}

//////////////////////////////////////////////////////////////////////
//
// DFA state graph construction.
//
// The DFA state graph is a heavily-linked collection of State* structures.
// The state_cache_ is a set of all the State structures ever allocated,
// so that if the same state is reached by two different paths,
// the same State structure can be used.  This reduces allocation
// requirements and also avoids duplication of effort across the two
// identical states.
//
// A State is defined by an ordered list of instruction ids and a flag word.
//
// The choice of an ordered list of instructions differs from a typical
// textbook DFA implementation, which would use an unordered set.
// Textbook descriptions, however, only care about whether
// the DFA matches, not where it matches in the text.  To decide where the
// DFA matches, we need to mimic the behavior of the dominant backtracking
// implementations like PCRE, which try one possible regular expression
// execution, then another, then another, stopping when one of them succeeds.
// The DFA execution tries these many executions in parallel, representing
// each by an instruction id.  These pointers are ordered in the State.inst_
// list in the same order that the executions would happen in a backtracking
// search: if a match is found during execution of inst_[2], inst_[i] for i>=3
// can be discarded.
//
// Textbooks also typically do not consider context-aware empty string operators
// like ^ or $.  These are handled by the flag word, which specifies the set
// of empty-string operators that should be matched when executing at the
// current text position.  These flag bits are defined in prog.h.
// The flag word also contains two DFA-specific bits: kFlagMatch if the state
// is a matching state (one that reached a kInstMatch in the program)
// and kFlagLastWord if the last processed byte was a word character, for the
// implementation of \B and \b.
//
// The flag word also contains, shifted up 16 bits, the bits looked for by
// any kInstEmptyWidth instructions in the state.  These provide a useful
// summary indicating when new flags might be useful.
//
// The permanent representation of a State's instruction ids is just an array,
// but while a state is being analyzed, these instruction ids are represented
// as a Workq, which is an array that allows iteration in insertion order.

// NOTE(rsc): The choice of State construction determines whether the DFA
// mimics backtracking implementations (so-called leftmost first matching) or
// traditional DFA implementations (so-called leftmost longest matching as
// prescribed by POSIX).  This implementation chooses to mimic the
// backtracking implementations, because we want to replace PCRE.  To get
// POSIX behavior, the states would need to be considered not as a simple
// ordered list of instruction ids, but as a list of unordered sets of instruction
// ids.  A match by a state in one set would inhibit the running of sets
// farther down the list but not other instruction ids in the same set.  Each
// set would correspond to matches beginning at a given point in the string.
// This is implemented by separating different sets with Mark pointers.

// Looks in the State cache for a State matching q, flag.
// If one is found, returns it.  If one is not found, allocates one,
// inserts it in the cache, and returns it.
// If mq is not null, MatchSep and the match IDs in mq will be appended
// to the State.
DFA::State* DFA::WorkqToCachedState(Workq* q, Workq* mq, uint32_t flag) {
  //mutex_.AssertHeld();

  // Construct array of instruction ids for the new state.
  // Only ByteRange, EmptyWidth, and Match instructions are useful to keep:
  // those are the only operators with any effect in
  // RunWorkqOnEmptyString or RunWorkqOnByte.
  PODArray<int> inst(q->size());
  int n = 0;
  uint32_t needflags = 0;  // flags needed by kInstEmptyWidth instructions
  bool sawmatch = false;   // whether queue contains guaranteed kInstMatch
  bool sawmark = false;    // whether queue contains a Mark
  if (ExtraDebug)
    fprintf(stderr, "WorkqToCachedState %s [%#x]", DumpWorkq(q).c_str(), flag);
  for (Workq::iterator it = q->begin(); it != q->end(); ++it) {
    int id = *it;
    if (sawmatch && (kind_ == Prog::kFirstMatch || q->is_mark(id)))
      break;
    if (q->is_mark(id)) {
      if (n > 0 && inst[n-1] != Mark) {
        sawmark = true;
        inst[n++] = Mark;
      }
      continue;
    }
    Prog::Inst* ip = prog_->inst(id);
    switch (ip->opcode()) {
      case kInstAltMatch:
        // This state will continue to a match no matter what
        // the rest of the input is.  If it is the highest priority match
        // being considered, return the special FullMatchState
        // to indicate that it's all matches from here out.
        if (kind_ != Prog::kManyMatch &&
            (kind_ != Prog::kFirstMatch ||
             (it == q->begin() && ip->greedy(prog_))) &&
            (kind_ != Prog::kLongestMatch || !sawmark) &&
            (flag & kFlagMatch)) {
          if (ExtraDebug)
            fprintf(stderr, " -> FullMatchState\n");
          return FullMatchState;
        }
        FALLTHROUGH_INTENDED;
      default:
        // Record iff id is the head of its list, which must
        // be the case if id-1 is the last of *its* list. :)
        if (prog_->inst(id-1)->last())
          inst[n++] = *it;
        if (ip->opcode() == kInstEmptyWidth)
          needflags |= ip->empty();
        if (ip->opcode() == kInstMatch && !prog_->anchor_end())
          sawmatch = true;
        break;
    }
  }
  DCHECK_LE(n, q->size());
  if (n > 0 && inst[n-1] == Mark)
    n--;

  // If there are no empty-width instructions waiting to execute,
  // then the extra flag bits will not be used, so there is no
  // point in saving them.  (Discarding them reduces the number
  // of distinct states.)
  if (needflags == 0)
    flag &= kFlagMatch;

  // NOTE(rsc): The code above cannot do flag &= needflags,
  // because if the right flags were present to pass the current
  // kInstEmptyWidth instructions, new kInstEmptyWidth instructions
  // might be reached that in turn need different flags.
  // The only sure thing is that if there are no kInstEmptyWidth
  // instructions at all, no flags will be needed.
  // We could do the extra work to figure out the full set of
  // possibly needed flags by exploring past the kInstEmptyWidth
  // instructions, but the check above -- are any flags needed
  // at all? -- handles the most common case.  More fine-grained
  // analysis can only be justified by measurements showing that
  // too many redundant states are being allocated.

  // If there are no Insts in the list, it's a dead state,
  // which is useful to signal with a special pointer so that
  // the execution loop can stop early.  This is only okay
  // if the state is *not* a matching state.
  if (n == 0 && flag == 0) {
    if (ExtraDebug)
      fprintf(stderr, " -> DeadState\n");
    return DeadState;
  }

  // If we're in longest match mode, the state is a sequence of
  // unordered state sets separated by Marks.  Sort each set
  // to canonicalize, to reduce the number of distinct sets stored.
  if (kind_ == Prog::kLongestMatch) {
    int* ip = inst.data();
    int* ep = ip + n;
    while (ip < ep) {
      int* markp = ip;
      while (markp < ep && *markp != Mark)
        markp++;
      std::sort(ip, markp);
      if (markp < ep)
        markp++;
      ip = markp;
    }
  }

  // If we're in many match mode, canonicalize for similar reasons:
  // we have an unordered set of states (i.e. we don't have Marks)
  // and sorting will reduce the number of distinct sets stored.
  if (kind_ == Prog::kManyMatch) {
    int* ip = inst.data();
    int* ep = ip + n;
    std::sort(ip, ep);
  }

  // Append MatchSep and the match IDs in mq if necessary.
  if (mq != NULL) {
    inst[n++] = MatchSep;
    for (Workq::iterator i = mq->begin(); i != mq->end(); ++i) {
      int id = *i;
      Prog::Inst* ip = prog_->inst(id);
      if (ip->opcode() == kInstMatch)
        inst[n++] = ip->match_id();
    }
  }

  // Save the needed empty-width flags in the top bits for use later.
  flag |= needflags << kFlagNeedShift;

  State* state = CachedState(inst.data(), n, flag);
  return state;
}

// Looks in the State cache for a State matching inst, ninst, flag.
// If one is found, returns it.  If one is not found, allocates one,
// inserts it in the cache, and returns it.
DFA::State* DFA::CachedState(int* inst, int ninst, uint32_t flag) {
  //mutex_.AssertHeld();

  // Look in the cache for a pre-existing state.
  // We have to initialise the struct like this because otherwise
  // MSVC will complain about the flexible array member. :(
  State state;
  state.inst_ = inst;
  state.ninst_ = ninst;
  state.flag_ = flag;
  StateSet::iterator it = state_cache_.find(&state);
  if (it != state_cache_.end()) {
    if (ExtraDebug)
      fprintf(stderr, " -cached-> %s\n", DumpState(*it).c_str());
    return *it;
  }

  // Must have enough memory for new state.
  // In addition to what we're going to allocate,
  // the state cache hash table seems to incur about 40 bytes per
  // State*, empirically.
  const int kStateCacheOverhead = 40;
  int nnext = prog_->bytemap_range() + 1;  // + 1 for kByteEndText slot
  int mem = StateMemSize(nnext, ninst);
#if RE2_DFA_NEXT_BITS != 64
  // 状态表自己也要钱: 每个状态在表里占一个 State* 槽。
  mem += sizeof(State*);
#if RE2_DFA_ARENA
#if RE2_DFA_INST_OUT
  // 两块分开记账: State 定长那块走 arena_, inst_ 走 iarena_。
  size_t imem = (ninst * sizeof(int) + 7) & ~static_cast<size_t>(7);
  mem = static_cast<int>(stride_ + imem + sizeof(State*));
  // 两块要【一起】记缺口: 调用方失败后只会 ResetCache 一次再重试一次,
  // 一次只扩一块的话第二次照样失败 (=> "Failed to analyze start state")。
  bool arena_short = false;
  if (iarena_len_ + imem > iarena_cap_) {
    iarena_need_ = imem;
    arena_short = true;
  }
  if (arena_len_ + stride_ > arena_cap_) {
    arena_need_ = stride_;
    arena_short = true;
  }
  if (arena_short)
    return NULL;
#else
  mem = (mem + 7) & ~7;          // 8 字节对齐: 下标是 8 字节为单位的偏移
  if (arena_len_ + mem > arena_cap_) {
#if RE2_DFA_ARENA_GROW
    // arena 该扩了。这里【不】把 mem_budget_ 拉成 -1: arena_len_ 只增不减, 这个条件本身
    // 就是个闩, 后续调用照样会失败, 而 ResetCache 扩完之后预算得原样接着用。
    arena_need_ = mem;
#else
    mem_budget_ = -1;   // 一次开满的版本理论上撞不到 (预算先耗尽), 兜底
#endif
    return NULL;
  }
#endif
#else
  if (stbl_len_ >= kStblMax) {   // 16 位版的下标用尽 = 缓存满了, 走原本的 flush 那条路
    mem_budget_ = -1;
    return NULL;
  }
#endif
#endif
  if (mem_budget_ < mem + kStateCacheOverhead) {
    mem_budget_ = -1;
    return NULL;
  }
  mem_budget_ -= mem + kStateCacheOverhead;

  // Allocate new state along with room for next_ and inst_.
#if RE2_DFA_INST_OUT
  char* space = arena_ + arena_len_;
  arena_len_ += stride_;
#elif RE2_DFA_ARENA
  char* space = arena_ + arena_len_;
  arena_len_ += mem;
#else
  char* space = std::allocator<char>().allocate(mem);
#endif
  State* s = new (space) State;
#if RE2_DFA_NEXT_BITS == 64
  (void) new (s->next_) std::atomic<State*>[nnext];
  // Work around a unfortunate bug in older versions of libstdc++.
  // (https://gcc.gnu.org/bugzilla/show_bug.cgi?id=64658)
  for (int i = 0; i < nnext; i++)
    (void) new (s->next_ + i) std::atomic<State*>(NULL);
  s->inst_ = new (s->next_ + nnext) int[ninst];
#else
  (void) new (s->next_) std::atomic<StateIdx>[nnext];
  for (int i = 0; i < nnext; i++)
    (void) new (s->next_ + i) std::atomic<StateIdx>(kStblNull);
#if RE2_DFA_INST_OUT
  // inst_ 住在另一块 arena 里 —— State 本体因此定长。
  s->inst_ = new (iarena_ + iarena_len_) int[ninst];
  iarena_len_ += imem;
#else
  // inst_ 接在 next_ 后面, 但 next_ 只有 2/4 字节对齐, int 要 4 字节 —— 补齐再放。
  s->inst_ = new (reinterpret_cast<char*>(s->next_) + NextArrayBytes(nnext)) int[ninst];
#endif
#endif
  memmove(s->inst_, inst, ninst*sizeof s->inst_[0]);
  s->ninst_ = ninst;
  s->flag_ = flag;
#if RE2_DFA_HOTSTATS
  s->visits_ = 0;
  {
    uint64_t fp = StateFingerprint(flag, inst, ninst);
    if (seen_fp_.insert(fp).second) built_first_++; else built_again_++;
#if RE2_DFA_TRACE
    {
      uint32_t next_id = static_cast<uint32_t>(fp2id_.size());
      s->tid_ = fp2id_.emplace(fp, next_id).first->second;
    }
#endif
    auto it = prev_rank_.find(fp);
    if (it == prev_rank_.end() || prev_n_ == 0) {
      rebuild_bucket_[6]++;             // 上一代压根没有它 —— 保留策略救不了
    } else {
      double q = static_cast<double>(it->second) / static_cast<double>(prev_n_);
      int b = q < 0.01 ? 0 : q < 0.05 ? 1 : q < 0.10 ? 2 : q < 0.25 ? 3 : q < 0.50 ? 4 : 5;
      rebuild_bucket_[b]++;
    }
  }
#endif
#if RE2_DFA_NEXT_BITS != 64
  s->idx_ = StblAppend(s);
  if (s->idx_ == 0) {   // 表满 (16 位版的下标上限) —— 当作缓存满
#if !RE2_DFA_ARENA
    std::allocator<char>().deallocate(space, mem);
#endif
    mem_budget_ = -1;
    return NULL;
  }
#endif
  if (ExtraDebug)
    fprintf(stderr, " -> %s\n", DumpState(s).c_str());

  // Put state in cache and return it.
  state_cache_.insert(s);
  states_built_.fetch_add(1, std::memory_order_relaxed);
#if RE2_DFA_ATTRIB
  AttribNewState(inst, ninst);   // 在 mutex_ 下, 与 states_built_ 同一处
#endif
  return s;
}

// Clear the cache.  Must hold cache_mutex_.w or be in destructor.
void DFA::ClearCache() {
#if RE2_DFA_ARENA
  // arena 版: 状态不是一个个 malloc 出来的, 没什么可 free 的, 清哈希表 + 拨游标就行。
  state_cache_.clear();
  StblClear();
  return;
#endif
  StateSet::iterator begin = state_cache_.begin();
  StateSet::iterator end = state_cache_.end();
  while (begin != end) {
    StateSet::iterator tmp = begin;
    ++begin;
    // Deallocate the blob of memory that we allocated in DFA::CachedState().
    // We recompute mem in order to benefit from sized delete where possible.
    int ninst = (*tmp)->ninst_;
    int nnext = prog_->bytemap_range() + 1;  // + 1 for kByteEndText slot
    int mem = StateMemSize(nnext, ninst);
#if RE2_DFA_NEXT_BITS != 64
    mem += sizeof(State*);   // 与 CachedState 里那笔一一对应 (sized delete 要求尺寸一致)
#endif
    std::allocator<char>().deallocate(reinterpret_cast<char*>(*tmp), mem);
  }
  state_cache_.clear();
#if RE2_DFA_NEXT_BITS != 64
  StblClear();
#endif
}

#if RE2_DFA_NEXT_BITS != 64

// StblAppend 把新状态登记进状态表, 返回它的下标 (>= kStblFirst)。表满返回 0。
// 容量是按"预算最多装得下多少个 State"算死的, 所以正常情况下永远满不了;
// 16 位版例外 —— 65536 这个下标上限可能先于内存预算撞上, 那时按"缓存满"处理。
// L >= mutex_
uint32_t DFA::StblAppend(State* s) {
#if RE2_DFA_INST_OUT && !RE2_DFA_ORDINAL
  return static_cast<uint32_t>((reinterpret_cast<char*>(s) - arena_) >> 3);
#elif RE2_DFA_ARENA_SHIFT
  return static_cast<uint32_t>((reinterpret_cast<char*>(s) - arena_)
                               >> RE2_DFA_ARENA_SHIFT);
#elif RE2_DFA_INST_OUT
  // 定长 ⇒ 下标就是"第几个 state"。
  return static_cast<uint32_t>((reinterpret_cast<char*>(s) - arena_) / stride_);
#elif RE2_DFA_ARENA
  // arena 版不需要登记: 地址本身就是下标 (State 已经是从 arena 里切出来的)。
  return static_cast<uint32_t>((reinterpret_cast<char*>(s) - arena_) >> 3);
#else
  if (stbl_len_ >= stbl_cap_)
    return 0;
  uint32_t k = stbl_len_++;
  // 先写好这一格, 调用方再 release 存下标 —— 读者 acquire 到下标时这一格必然已可见。
  stbl_[k] = s;
  return k;
#endif
}

// StblClear 重建状态表。只在 ClearCache 里调 —— 那时 cache_mutex_ 是写锁独占,
// 没有读者还攥着旧基址, 所以可以直接把整张表换掉。
void DFA::StblClear() {
#if RE2_DFA_ARENA
  // flush 就是把游标拨回去 —— 上万个 State 一个都不用 free (原版那个逐个 deallocate 的循环
  // 本身就是 flush 代价的一部分)。arena 本体留着复用。
#if RE2_DFA_INST_OUT
  if (arena_max_ == 0) {
    // 状态那块的上限还要再压一道: 下标只有 kStblMax 个 (2 字节版 = 65536)。
    arena_max_ = static_cast<size_t>(state_budget_) + kStblFirst * stride_ + 64;
#if RE2_DFA_ORDINAL
    size_t bykidx = static_cast<size_t>(kStblMax) * stride_;
    if (arena_max_ > bykidx)
      arena_max_ = bykidx;
#endif
    iarena_max_ = static_cast<size_t>(state_budget_) + 64;
  }
  arena_len_ = kStblFirst * stride_;   // 前 3 格空着: 下标 0/1/2 是保留值
  iarena_len_ = 0;
  return;
#endif
  if (arena_ == NULL) {
    arena_max_ = static_cast<size_t>(state_budget_) + (kStblFirst << 3) + 64;
#if RE2_DFA_ARENA_GROW
    // 一个字节都先不要: 第一个状态诞生时才由 GrowArena 从 kArenaInitCap 开起。
    // (DFA 是构造出来就 StblClear 的, 而一个 Prog 最多挂 4 个 DFA, 很多从头到尾没被用过。)
    arena_cap_ = 0;
#else
    arena_cap_ = arena_max_;
    arena_ = static_cast<char*>(malloc(arena_cap_));
    if (arena_ == NULL) {
      init_failed_ = true;
      arena_cap_ = 0;
    }
#endif
  }
  arena_len_ = kStblFirst << 3;   // 前 24 字节空着: 下标 0/1/2 是保留值
  return;
#else
  int nnext = prog_->bytemap_range() + 1;
  // 单个 State 的最小可能开销: ninst 至少 1, 再加状态表里那一格与哈希表每项约 40 字节
  // (与 CachedState 里那笔账逐项对应)。预算除以它 = 状态数的硬上界。
  int64_t min_state = StateMemSize(nnext, 1) + sizeof(State*) + 40;
  int64_t cap = state_budget_ / min_state + kStblFirst + 8;
  if (cap > static_cast<int64_t>(kStblMax))
    cap = kStblMax;
  if (cap < 64)
    cap = 64;
  if (stbl_ == NULL || static_cast<uint32_t>(cap) != stbl_cap_) {
    delete[] stbl_;
    stbl_cap_ = static_cast<uint32_t>(cap);
    stbl_ = new State*[stbl_cap_];
  }
  stbl_[kStblNull] = NULL;
  stbl_[kStblDead] = DeadState;
  stbl_[kStblFullMatch] = FullMatchState;
  stbl_len_ = kStblFirst;
#endif
}

#if RE2_DFA_ARENA_GROW

// 把 arena 扩到至少还能再放下 need 字节。必须在 cache_mutex_ 写锁下调用 (没有读者)。
// realloc: 大块内存在 glibc 下走 mremap, 通常不用"旧+新同时在世", 峰值不会翻倍;
// 真搬家了就按位移量把三处 State* 重定位 —— next_ 槽存的是偏移, 不用动。
bool DFA::GrowArena(size_t need) {
  if (arena_cap_ >= arena_max_)
    return false;
  size_t newcap = arena_cap_ > 0 ? arena_cap_ : kArenaInitCap;
  if (newcap > arena_max_)
    newcap = arena_max_;
  while (newcap < arena_len_ + need) {
    newcap *= 2;
    if (newcap >= arena_max_) { newcap = arena_max_; break; }
  }
  if (newcap <= arena_cap_)
    return false;
  char* old_arena = arena_;
  char* na = static_cast<char*>(realloc(arena_, newcap));
  if (na == NULL)
    return false;
  // 头一次分配 (old_arena == NULL): 还一个状态都没有, 没什么可重定位的。
  ptrdiff_t delta = old_arena == NULL ? 0 : na - old_arena;
  arena_ = na;
  arena_cap_ = newcap;
  if (arena_len_ + need > arena_cap_)
    return false;   // 到顶了还是塞不下 (need 比整块还大, 不可能, 兜底)
  if (delta == 0)
    return true;    // 原地扩上去了, 什么都不用重定位

  // 搬家了: 重定位 ①每个 State 的 inst_ ②哈希表里的键 ③start_[] 里的起始状态。
  int nnext = prog_->bytemap_range() + 1;
  StateSet ns;
  ns.reserve(state_cache_.size());
  for (StateSet::iterator it = state_cache_.begin(); it != state_cache_.end();
       ++it) {
    State* q = reinterpret_cast<State*>(reinterpret_cast<char*>(*it) + delta);
#if !RE2_DFA_INST_OUT
    // inst_ 就跟在 next_ 后面 (位置只跟 nnext 有关), 直接按新地址重算, 不用加位移。
    q->inst_ = reinterpret_cast<int*>(
        reinterpret_cast<char*>(q->next_) + NextArrayBytes(nnext));
#endif
    ns.insert(q);   // 哈希值只跟 flag_/inst_ 内容有关, 与地址无关 ⇒ 桶不变
  }
  state_cache_.swap(ns);
  for (int i = 0; i < kMaxStart; i++) {
    State* p = start_[i].start.load(std::memory_order_relaxed);
    if (p > SpecialStateMax)
      start_[i].start.store(
          reinterpret_cast<State*>(reinterpret_cast<char*>(p) + delta),
          std::memory_order_relaxed);
  }
  return true;
}

#if RE2_DFA_INST_OUT

// 扩 inst_ 那块。搬家后只有一处要重定位: 每个 State 的 inst_ 指针。
// (哈希值只跟 inst_ 的【内容】有关, 内容原样搬过去了, 所以桶不变、表不用重建。)
bool DFA::GrowIArena(size_t need) {
  if (iarena_cap_ >= iarena_max_)
    return false;
  size_t newcap = iarena_cap_ > 0 ? iarena_cap_ : kArenaInitCap;
  if (newcap > iarena_max_)
    newcap = iarena_max_;
  while (newcap < iarena_len_ + need) {
    newcap *= 2;
    if (newcap >= iarena_max_) { newcap = iarena_max_; break; }
  }
  if (newcap <= iarena_cap_)
    return false;
  char* old_iarena = iarena_;
  char* na = static_cast<char*>(realloc(iarena_, newcap));
  if (na == NULL)
    return false;
  ptrdiff_t delta = old_iarena == NULL ? 0 : na - old_iarena;
  iarena_ = na;
  iarena_cap_ = newcap;
  if (iarena_len_ + need > iarena_cap_)
    return false;
  if (delta == 0)
    return true;
  for (StateSet::iterator it = state_cache_.begin(); it != state_cache_.end();
       ++it)
    (*it)->inst_ = reinterpret_cast<int*>(
        reinterpret_cast<char*>((*it)->inst_) + delta);
  return true;
}

#endif  // RE2_DFA_INST_OUT

#endif  // RE2_DFA_ARENA_GROW

#endif

// Copies insts in state s to the work queue q.
void DFA::StateToWorkq(State* s, Workq* q) {
  q->clear();
  for (int i = 0; i < s->ninst_; i++) {
    if (s->inst_[i] == Mark) {
      q->mark();
    } else if (s->inst_[i] == MatchSep) {
      // Nothing after this is an instruction!
      break;
    } else {
      // Explore from the head of the list.
      AddToQueue(q, s->inst_[i], s->flag_ & kFlagEmptyMask);
    }
  }
}

// Adds ip to the work queue, following empty arrows according to flag.
void DFA::AddToQueue(Workq* q, int id, uint32_t flag) {

  // Use stack_ to hold our stack of instructions yet to process.
  // It was preallocated as follows:
  //   one entry per Capture;
  //   one entry per EmptyWidth; and
  //   one entry per Nop.
  // This reflects the maximum number of stack pushes that each can
  // perform. (Each instruction can be processed at most once.)
  // When using marks, we also added nmark == prog_->size().
  // (Otherwise, nmark == 0.)
  int* stk = stack_.data();
  int nstk = 0;

  stk[nstk++] = id;
  while (nstk > 0) {
    DCHECK_LE(nstk, stack_.size());
    id = stk[--nstk];

  Loop:
    if (id == Mark) {
      q->mark();
      continue;
    }

    if (id == 0)
      continue;

    // If ip is already on the queue, nothing to do.
    // Otherwise add it.  We don't actually keep all the
    // ones that get added, but adding all of them here
    // increases the likelihood of q->contains(id),
    // reducing the amount of duplicated work.
    if (q->contains(id))
      continue;
    q->insert_new(id);

    // Process instruction.
    Prog::Inst* ip = prog_->inst(id);
    switch (ip->opcode()) {
      default:
        LOG(DFATAL) << "unhandled opcode: " << ip->opcode();
        break;

      case kInstByteRange:  // just save these on the queue
      case kInstMatch:
        if (ip->last())
          break;
        id = id+1;
        goto Loop;

      case kInstCapture:    // DFA treats captures as no-ops.
      case kInstNop:
        if (!ip->last())
          stk[nstk++] = id+1;

        // If this instruction is the [00-FF]* loop at the beginning of
        // a leftmost-longest unanchored search, separate with a Mark so
        // that future threads (which will start farther to the right in
        // the input string) are lower priority than current threads.
        if (ip->opcode() == kInstNop && q->maxmark() > 0 &&
            id == prog_->start_unanchored() && id != prog_->start())
          stk[nstk++] = Mark;
        id = ip->out();
        goto Loop;

      case kInstAltMatch:
        DCHECK(!ip->last());
        id = id+1;
        goto Loop;

      case kInstEmptyWidth:
        if (!ip->last())
          stk[nstk++] = id+1;

        // Continue on if we have all the right flag bits.
        if (ip->empty() & ~flag)
          break;
        id = ip->out();
        goto Loop;
    }
  }
}

// Running of work queues.  In the work queue, order matters:
// the queue is sorted in priority order.  If instruction i comes before j,
// then the instructions that i produces during the run must come before
// the ones that j produces.  In order to keep this invariant, all the
// work queue runners have to take an old queue to process and then
// also a new queue to fill in.  It's not acceptable to add to the end of
// an existing queue, because new instructions will not end up in the
// correct position.

// Runs the work queue, processing the empty strings indicated by flag.
// For example, flag == kEmptyBeginLine|kEmptyEndLine means to match
// both ^ and $.  It is important that callers pass all flags at once:
// processing both ^ and $ is not the same as first processing only ^
// and then processing only $.  Doing the two-step sequence won't match
// ^$^$^$ but processing ^ and $ simultaneously will (and is the behavior
// exhibited by existing implementations).
void DFA::RunWorkqOnEmptyString(Workq* oldq, Workq* newq, uint32_t flag) {
  newq->clear();
  for (Workq::iterator i = oldq->begin(); i != oldq->end(); ++i) {
    if (oldq->is_mark(*i))
      AddToQueue(newq, Mark, flag);
    else
      AddToQueue(newq, *i, flag);
  }
}

// Runs the work queue, processing the single byte c followed by any empty
// strings indicated by flag.  For example, c == 'a' and flag == kEmptyEndLine,
// means to match c$.  Sets the bool *ismatch to true if the end of the
// regular expression program has been reached (the regexp has matched).
void DFA::RunWorkqOnByte(Workq* oldq, Workq* newq,
                         int c, uint32_t flag, bool* ismatch) {
  //mutex_.AssertHeld();

  newq->clear();
  for (Workq::iterator i = oldq->begin(); i != oldq->end(); ++i) {
    if (oldq->is_mark(*i)) {
      if (*ismatch)
        return;
      newq->mark();
      continue;
    }
    int id = *i;
    Prog::Inst* ip = prog_->inst(id);
    switch (ip->opcode()) {
      default:
        LOG(DFATAL) << "unhandled opcode: " << ip->opcode();
        break;

      case kInstFail:        // never succeeds
      case kInstCapture:     // already followed
      case kInstNop:         // already followed
      case kInstAltMatch:    // already followed
      case kInstEmptyWidth:  // already followed
        break;

      case kInstByteRange:   // can follow if c is in range
        if (!ip->Matches(c))
          break;
        AddToQueue(newq, ip->out(), flag);
        if (ip->hint() != 0) {
          // We have a hint, but we must cancel out the
          // increment that will occur after the break.
          i += ip->hint() - 1;
        } else {
          // We have no hint, so we must find the end
          // of the current list and then skip to it.
          Prog::Inst* ip0 = ip;
          while (!ip->last())
            ++ip;
          i += ip - ip0;
        }
        break;

      case kInstMatch:
        if (prog_->anchor_end() && c != kByteEndText &&
            kind_ != Prog::kManyMatch)
          break;
        *ismatch = true;
        if (kind_ == Prog::kFirstMatch) {
          // Can stop processing work queue since we found a match.
          return;
        }
        break;
    }
  }

  if (ExtraDebug)
    fprintf(stderr, "%s on %d[%#x] -> %s [%d]\n",
            DumpWorkq(oldq).c_str(), c, flag, DumpWorkq(newq).c_str(), *ismatch);
}

// Processes input byte c in state, returning new state.
// Caller does not hold mutex.
DFA::State* DFA::RunStateOnByteUnlocked(State* state, int c) {
  // Keep only one RunStateOnByte going
  // even if the DFA is being run by multiple threads.
  MutexLock l(&mutex_);
  return RunStateOnByte(state, c);
}

// Processes input byte c in state, returning new state.
DFA::State* DFA::RunStateOnByte(State* state, int c) {
  //mutex_.AssertHeld();

  if (state <= SpecialStateMax) {
    if (state == FullMatchState) {
      // It is convenient for routines like PossibleMatchRange
      // if we implement RunStateOnByte for FullMatchState:
      // once you get into this state you never get out,
      // so it's pretty easy.
      return FullMatchState;
    }
    if (state == DeadState) {
      LOG(DFATAL) << "DeadState in RunStateOnByte";
      return NULL;
    }
    if (state == NULL) {
      LOG(DFATAL) << "NULL state in RunStateOnByte";
      return NULL;
    }
    LOG(DFATAL) << "Unexpected special state in RunStateOnByte";
    return NULL;
  }

  // If someone else already computed this, return it.
  State* ns = NextOf(state, ByteMap(c));
  if (ns != NULL)
    return ns;

  // Convert state into Workq.
  StateToWorkq(state, q0_);

  // Flags marking the kinds of empty-width things (^ $ etc)
  // around this byte.  Before the byte we have the flags recorded
  // in the State structure itself.  After the byte we have
  // nothing yet (but that will change: read on).
  uint32_t needflag = state->flag_ >> kFlagNeedShift;
  uint32_t beforeflag = state->flag_ & kFlagEmptyMask;
  uint32_t oldbeforeflag = beforeflag;
  uint32_t afterflag = 0;

  if (c == '\n') {
    // Insert implicit $ and ^ around \n
    beforeflag |= kEmptyEndLine;
    afterflag |= kEmptyBeginLine;
  }

  if (c == kByteEndText) {
    // Insert implicit $ and \z before the fake "end text" byte.
    beforeflag |= kEmptyEndLine | kEmptyEndText;
  }

  // The state flag kFlagLastWord says whether the last
  // byte processed was a word character.  Use that info to
  // insert empty-width (non-)word boundaries.
  bool islastword = (state->flag_ & kFlagLastWord) != 0;
  bool isword = c != kByteEndText && Prog::IsWordChar(static_cast<uint8_t>(c));
  if (isword == islastword)
    beforeflag |= kEmptyNonWordBoundary;
  else
    beforeflag |= kEmptyWordBoundary;

  // Okay, finally ready to run.
  // Only useful to rerun on empty string if there are new, useful flags.
  if (beforeflag & ~oldbeforeflag & needflag) {
    RunWorkqOnEmptyString(q0_, q1_, beforeflag);
    using std::swap;
    swap(q0_, q1_);
  }
  bool ismatch = false;
  RunWorkqOnByte(q0_, q1_, c, afterflag, &ismatch);
  using std::swap;
  swap(q0_, q1_);

  // Save afterflag along with ismatch and isword in new state.
  uint32_t flag = afterflag;
  if (ismatch)
    flag |= kFlagMatch;
  if (isword)
    flag |= kFlagLastWord;

  if (ismatch && kind_ == Prog::kManyMatch)
    ns = WorkqToCachedState(q0_, q1_, flag);
  else
    ns = WorkqToCachedState(q0_, NULL, flag);

  // Flush ns before linking to it.
  // Write barrier before updating state->next_ so that the
  // main search loop can proceed without any locking, for speed.
  // (Otherwise it would need one mutex operation per input byte.)
  SetNextOf(state, ByteMap(c), ns);
  return ns;
}


//////////////////////////////////////////////////////////////////////
// DFA cache reset.

// Reader-writer lock helper.
//
// The DFA uses a reader-writer mutex to protect the state graph itself.
// Traversing the state graph requires holding the mutex for reading,
// and discarding the state graph and starting over requires holding the
// lock for writing.  If a search needs to expand the graph but is out
// of memory, it will need to drop its read lock and then acquire the
// write lock.  Since it cannot then atomically downgrade from write lock
// to read lock, it runs the rest of the search holding the write lock.
// (This probably helps avoid repeated contention, but really the decision
// is forced by the Mutex interface.)  It's a bit complicated to keep
// track of whether the lock is held for reading or writing and thread
// that through the search, so instead we encapsulate it in the RWLocker
// and pass that around.

class DFA::RWLocker {
 public:
  explicit RWLocker(CacheMutex* mu);
  ~RWLocker();

  // If the lock is only held for reading right now,
  // drop the read lock and re-acquire for writing.
  // Subsequent calls to LockForWriting are no-ops.
  // Notice that the lock is *released* temporarily.
  void LockForWriting();

 private:
  CacheMutex* mu_;
  bool writing_;

  RWLocker(const RWLocker&) = delete;
  RWLocker& operator=(const RWLocker&) = delete;
};

DFA::RWLocker::RWLocker(CacheMutex* mu) : mu_(mu), writing_(false) {
  mu_->ReaderLock();
}

// This function is marked as NO_THREAD_SAFETY_ANALYSIS because
// the annotations don't support lock upgrade.
void DFA::RWLocker::LockForWriting() NO_THREAD_SAFETY_ANALYSIS {
  if (!writing_) {
    mu_->ReaderUnlock();
    mu_->WriterLock();
    writing_ = true;
  }
}

DFA::RWLocker::~RWLocker() {
  if (!writing_)
    mu_->ReaderUnlock();
  else
    mu_->WriterUnlock();
}


// When the DFA's State cache fills, we discard all the states in the
// cache and start over.  Many threads can be using and adding to the
// cache at the same time, so we synchronize using the cache_mutex_
// to keep from stepping on other threads.  Specifically, all the
// threads using the current cache hold cache_mutex_ for reading.
// When a thread decides to flush the cache, it drops cache_mutex_
// and then re-acquires it for writing.  That ensures there are no
// other threads accessing the cache anymore.  The rest of the search
// runs holding cache_mutex_ for writing, avoiding any contention
// with or cache pollution caused by other threads.

#if RE2_DFA_HOTSTATS
// 64 位 FNV-1a 指纹: 状态的身份就是 (flag, inst 集合), 跟 StateHash 同一套口径, 但要 64 位
// —— 32 位在几十万状态上会撞出假的"重建"。
uint64_t DFA::StateFingerprint(uint32_t flag, const int* inst, int ninst) const {
  uint64_t fp = 1469598103934665603ULL;
  auto mix = [&fp](uint32_t v) {
    for (int b = 0; b < 4; b++) { fp ^= (v >> (8*b)) & 0xFF; fp *= 1099511628211ULL; }
  };
  mix(flag);
  for (int i = 0; i < ninst; i++) mix(static_cast<uint32_t>(inst[i]));
  return fp;
}

#if RE2_DFA_TRACE
// 落盘: 攒够 64K 个 id 写一次。文件名从环境变量拿, 没给就不 trace。
// 状态放在文件作用域而不是 DFA 上, 是为了能 atexit 收尾 —— DFA 的析构在 Go 这边由
// finalizer 决定, 进程退出时不一定跑到, 不收尾就会丢掉最后一段。
// (代价: 一个进程只 trace 得了一个 DFA。测量场景就是一个 Set, 够用。)
namespace {
FILE* gTraceFile = NULL;
std::vector<uint32_t>* gTraceBuf = NULL;
void TraceFlush() {
  if (gTraceFile != NULL && gTraceBuf != NULL && !gTraceBuf->empty()) {
    fwrite(gTraceBuf->data(), sizeof(uint32_t), gTraceBuf->size(), gTraceFile);
    gTraceBuf->clear();
  }
  if (gTraceFile != NULL) fflush(gTraceFile);
}
}  // namespace

void DFA::TraceFlushNow() { TraceFlush(); }

void DFA::TraceVisit(uint32_t id) {
  if (gTraceBuf == NULL) {
    const char* path = getenv("RE2_DFA_TRACE_FILE");
    if (path == NULL) { static std::vector<uint32_t> sink; gTraceBuf = &sink; return; }
    gTraceFile = fopen(path, "wb");
    gTraceBuf = new std::vector<uint32_t>();
    gTraceBuf->reserve(1 << 16);
    atexit(TraceFlush);
  }
  if (gTraceFile == NULL) return;
  gTraceBuf->push_back(id);
  if (gTraceBuf->size() >= (1 << 16)) TraceFlush();
}
#endif

// 真 flush 前把这一世代的账打出来。只在写锁下调, 可以随便遍历 state_cache_。
//   share@x%  = 访问次数最多的前 x% 状态吃掉了百分之多少的总访问
//   bfs@k     = 从 8 个起始状态出发按已算出的转移做 BFS, 前 k 个状态覆盖了多少 % 的访问
//               (这是"只留起点附近一圈"这个具体策略的上限)
void DFA::HotStatsDump() {
  std::vector<uint32_t> v;
  v.reserve(state_cache_.size());
  uint64_t total = 0;
  for (State* st : state_cache_) {
    v.push_back(st->visits_);
    total += st->visits_;
  }
  std::sort(v.begin(), v.end(), std::greater<uint32_t>());
  auto share = [&](double frac) {
    if (total == 0) return 0.0;
    size_t n = static_cast<size_t>(v.size() * frac);
    uint64_t acc = 0;
    for (size_t i = 0; i < n && i < v.size(); i++) acc += v[i];
    return 100.0 * static_cast<double>(acc) / static_cast<double>(total);
  };
  // 起点 BFS: 只走【已经算出来的】转移 (NULL 槽表示还没算, 本来就不占内存)。
  std::unordered_set<State*> seen;
  std::vector<State*> order;
  for (int i = 0; i < kMaxStart; i++) {
    State* st = start_[i].start.load(std::memory_order_relaxed);
    if (st > SpecialStateMax && seen.insert(st).second) order.push_back(st);
  }
  int nnext = prog_->bytemap_range() + 1;
  for (size_t i = 0; i < order.size(); i++) {
    State* st = order[i];
    for (int c = 0; c < nnext; c++) {
      State* ns = NextOf(st, c);
      if (ns > SpecialStateMax && seen.insert(ns).second) order.push_back(ns);
    }
  }
  auto bfsShare = [&](size_t k) {
    if (total == 0) return 0.0;
    uint64_t acc = 0;
    for (size_t i = 0; i < k && i < order.size(); i++) acc += order[i]->visits_;
    return 100.0 * static_cast<double>(acc) / static_cast<double>(total);
  };
  // 这一代的重建都落在上一代的哪个热度段 —— 直接就是"保留前 x% 最热状态"的收益上限。
  int64_t rb = 0;
  for (int i = 0; i < 7; i++) rb += rebuild_bucket_[i];
  auto cum = [&](int upto) {
    if (rb == 0) return 0.0;
    int64_t a = 0;
    for (int i = 0; i <= upto; i++) a += rebuild_bucket_[i];
    return 100.0 * static_cast<double>(a) / static_cast<double>(rb);
  };
  // 换代: 这一代的状态按 visits 排名存进 prev_rank_, 给下一代当参照。
  {
    std::vector<std::pair<uint32_t, uint64_t>> rank;
    rank.reserve(state_cache_.size());
    for (State* st : state_cache_)
      rank.push_back({st->visits_, StateFingerprint(st->flag_, st->inst_, st->ninst_)});
    std::sort(rank.begin(), rank.end(),
              [](const std::pair<uint32_t,uint64_t>& a, const std::pair<uint32_t,uint64_t>& b) {
                return a.first > b.first;
              });
    prev_rank_.clear();
    for (size_t i = 0; i < rank.size(); i++) prev_rank_[rank[i].second] = static_cast<uint32_t>(i);
    prev_n_ = rank.size();
  }

  size_t n = state_cache_.size();
  fprintf(stderr,
          "RE2_REBUILD builds=%lld top1=%.1f top5=%.1f top10=%.1f top25=%.1f top50=%.1f "
          "all=%.1f newborn=%.1f\n",
          (long long)rb, cum(0), cum(1), cum(2), cum(3), cum(4), cum(5),
          rb ? 100.0*(double)rebuild_bucket_[6]/(double)rb : 0.0);
  for (int i = 0; i < 7; i++) rebuild_bucket_[i] = 0;

  fprintf(stderr,
          "RE2_HOTSTATS states=%zu visits=%llu share1=%.1f share5=%.1f share10=%.1f "
          "share25=%.1f share50=%.1f zero=%zu bfsreach=%zu bfs10=%.1f bfs25=%.1f bfs50=%.1f "
          "first=%lld again=%lld\n",
          n, (unsigned long long)total, share(0.01), share(0.05), share(0.10),
          share(0.25), share(0.50),
          static_cast<size_t>(std::count(v.begin(), v.end(), 0u)),
          order.size(), bfsShare(n/10), bfsShare(n/4), bfsShare(n/2),
          (long long)built_first_, (long long)built_again_);
}
#endif

void DFA::ResetCache(RWLocker* cache_lock, DFAScanStats* stats) {
  // Re-acquire the cache_mutex_ for writing (exclusive use).
  cache_lock->LockForWriting();

#if RE2_DFA_ARENA_GROW
  // 这时候已经是写锁独占, 没有任何读者手里攥着 State* —— 唯一能安全搬 arena 的地方。
  if (GrowPending()) {
    size_t need = arena_need_;
    arena_need_ = 0;
    bool handled = true;            // 两块都安顿好了 = 一次"没丢缓存的 flush"
#if RE2_DFA_INST_OUT
    size_t ineed = iarena_need_;
    iarena_need_ = 0;
    if (ineed > 0 && iarena_len_ + ineed > iarena_cap_ && !GrowIArena(ineed))
      handled = false;
#endif
    if (need > 0 && arena_len_ + need > arena_cap_ && !GrowArena(need))
      handled = false;
    if (handled) {
      // 一次"没丢缓存的 flush": 只是 arena 变大了, 一个状态都没少。
      if (stats != NULL)
        stats->grows++;
      return;
    }
    // 到顶了, 往下走真 flush
  }
#endif

#if RE2_DFA_HOTSTATS
  HotStatsDump();
#endif
  // 按对象归因的计数 (进程全局那份 hook 照旧发, 两者口径不同, 互不替代)。
  flushes_total_.fetch_add(1, std::memory_order_relaxed);
  if (stats != NULL)
    stats->flushes++;

  hooks::GetDFAStateCacheResetHook()({
      state_budget_,
      state_cache_.size(),
  });

  // Clear the cache, reset the memory budget.
  for (int i = 0; i < kMaxStart; i++)
    start_[i].start.store(NULL, std::memory_order_relaxed);
  ClearCache();
  mem_budget_ = state_budget_;
}

// Typically, a couple States do need to be preserved across a cache
// reset, like the State at the current point in the search.
// The StateSaver class helps keep States across cache resets.
// It makes a copy of the state's guts outside the cache (before the reset)
// and then can be asked, after the reset, to recreate the State
// in the new cache.  For example, in a DFA method ("this" is a DFA):
//
//   StateSaver saver(this, s);
//   ResetCache(cache_lock);
//   s = saver.Restore();
//
// The saver should always have room in the cache to re-create the state,
// because resetting the cache locks out all other threads, and the cache
// is known to have room for at least a couple states (otherwise the DFA
// constructor fails).

class DFA::StateSaver {
 public:
  explicit StateSaver(DFA* dfa, State* state);
  ~StateSaver();

  // Recreates and returns a state equivalent to the
  // original state passed to the constructor.
  // Returns NULL if the cache has filled, but
  // since the DFA guarantees to have room in the cache
  // for a couple states, should never return NULL
  // if used right after ResetCache.
  State* Restore();

 private:
  DFA* dfa_;         // the DFA to use
  int* inst_;        // saved info from State
  int ninst_;
  uint32_t flag_;
  bool is_special_;  // whether original state was special
  State* special_;   // if is_special_, the original state

  StateSaver(const StateSaver&) = delete;
  StateSaver& operator=(const StateSaver&) = delete;
};

DFA::StateSaver::StateSaver(DFA* dfa, State* state) {
  dfa_ = dfa;
  if (state <= SpecialStateMax) {
    inst_ = NULL;
    ninst_ = 0;
    flag_ = 0;
    is_special_ = true;
    special_ = state;
    return;
  }
  is_special_ = false;
  special_ = NULL;
  flag_ = state->flag_;
  ninst_ = state->ninst_;
  inst_ = new int[ninst_];
  memmove(inst_, state->inst_, ninst_*sizeof inst_[0]);
}

DFA::StateSaver::~StateSaver() {
  if (!is_special_)
    delete[] inst_;
}

DFA::State* DFA::StateSaver::Restore() {
  if (is_special_)
    return special_;
  MutexLock l(&dfa_->mutex_);
  State* s = dfa_->CachedState(inst_, ninst_, flag_);
  if (s == NULL)
    LOG(DFATAL) << "StateSaver failed to restore state.";
  return s;
}


//////////////////////////////////////////////////////////////////////
//
// DFA execution.
//
// The basic search loop is easy: start in a state s and then for each
// byte c in the input, s = s->next[c].
//
// This simple description omits a few efficiency-driven complications.
//
// First, the State graph is constructed incrementally: it is possible
// that s->next[c] is null, indicating that that state has not been
// fully explored.  In this case, RunStateOnByte must be invoked to
// determine the next state, which is cached in s->next[c] to save
// future effort.  An alternative reason for s->next[c] to be null is
// that the DFA has reached a so-called "dead state", in which any match
// is no longer possible.  In this case RunStateOnByte will return NULL
// and the processing of the string can stop early.
//
// Second, a 256-element pointer array for s->next_ makes each State
// quite large (2kB on 64-bit machines).  Instead, dfa->bytemap_[]
// maps from bytes to "byte classes" and then next_ only needs to have
// as many pointers as there are byte classes.  A byte class is simply a
// range of bytes that the regexp never distinguishes between.
// A regexp looking for a[abc] would have four byte ranges -- 0 to 'a'-1,
// 'a', 'b' to 'c', and 'c' to 0xFF.  The bytemap slows us a little bit
// but in exchange we typically cut the size of a State (and thus our
// memory footprint) by about 5-10x.  The comments still refer to
// s->next[c] for simplicity, but code should refer to s->next_[bytemap_[c]].
//
// Third, it is common for a DFA for an unanchored match to begin in a
// state in which only one particular byte value can take the DFA to a
// different state.  That is, s->next[c] != s for only one c.  In this
// situation, the DFA can do better than executing the simple loop.
// Instead, it can call memchr to search very quickly for the byte c.
// Whether the start state has this property is determined during a
// pre-compilation pass and the "can_prefix_accel" argument is set.
//
// Fourth, the desired behavior is to search for the leftmost-best match
// (approximately, the same one that Perl would find), which is not
// necessarily the match ending earliest in the string.  Each time a
// match is found, it must be noted, but the DFA must continue on in
// hope of finding a higher-priority match.  In some cases, the caller only
// cares whether there is any match at all, not which one is found.
// The "want_earliest_match" flag causes the search to stop at the first
// match found.
//
// Fifth, one algorithm that uses the DFA needs it to run over the
// input string backward, beginning at the end and ending at the beginning.
// Passing false for the "run_forward" flag causes the DFA to run backward.
//
// The checks for these last three cases, which in a naive implementation
// would be performed once per input byte, slow the general loop enough
// to merit specialized versions of the search loop for each of the
// eight possible settings of the three booleans.  Rather than write
// eight different functions, we write one general implementation and then
// inline it to create the specialized ones.
//
// Note that matches are delayed by one byte, to make it easier to
// accomodate match conditions depending on the next input byte (like $ and \b).
// When s->next[c]->IsMatch(), it means that there is a match ending just
// *before* byte c.

// The generic search loop.  Searches text for a match, returning
// the pointer to the end of the chosen match, or NULL if no match.
// The bools are equal to the same-named variables in params, but
// making them function arguments lets the inliner specialize
// this function to each combination (see two paragraphs above).
template <bool can_prefix_accel,
          bool want_earliest_match,
          bool run_forward>
inline bool DFA::InlinedSearchLoop(SearchParams* params) {
  State* start = params->start;
  const uint8_t* bp = BytePtr(params->text.data());  // start of text
  const uint8_t* p = bp;                             // text scanning point
  const uint8_t* ep = BytePtr(params->text.data() +
                              params->text.size());  // end of text
  const uint8_t* resetp = NULL;                      // p at last cache reset
  if (!run_forward) {
    using std::swap;
    swap(p, ep);
  }

  const uint8_t* bytemap = prog_->bytemap();
  const uint8_t* lastmatch = NULL;   // most recent matching position in text
  bool matched = false;

  State* s = start;
  if (ExtraDebug)
    fprintf(stderr, "@stx: %s\n", DumpState(s).c_str());

  if (s->IsMatch()) {
    matched = true;
    lastmatch = p;
    if (ExtraDebug)
      fprintf(stderr, "match @stx! [%s]\n", DumpState(s).c_str());
    if (params->matches != NULL) {  // [backport re2 24d460a]
      for (int i = s->ninst_ - 1; i >= 0; i--) {
        int id = s->inst_[i];
        if (id == MatchSep)
          break;
        params->matches->insert(id);
      }
    }
    if (want_earliest_match) {
      params->ep = reinterpret_cast<const char*>(lastmatch);
      return true;
    }
  }

  while (p != ep) {
    if (ExtraDebug)
      fprintf(stderr, "@%td: %s\n", p - bp, DumpState(s).c_str());

    if (can_prefix_accel && s == start) {
      // In start state, only way out is to find the prefix,
      // so we use prefix accel (e.g. memchr) to skip ahead.
      // If not found, we can skip to the end of the string.
      p = BytePtr(prog_->PrefixAccel(p, ep - p));
      if (p == NULL) {
        p = ep;
        break;
      }
    }

    int c;
    if (run_forward)
      c = *p++;
    else
      c = *--p;

    // Note that multiple threads might be consulting
    // s->next_[bytemap[c]] simultaneously.
    // RunStateOnByte takes care of the appropriate locking,
    // including a memory barrier so that the unlocked access
    // (sometimes known as "double-checked locking") is safe.
    // The alternative would be either one DFA per thread
    // or one mutex operation per input byte.
    //
    // ns == DeadState means the state is known to be dead
    // (no more matches are possible).
    // ns == NULL means the state has not yet been computed
    // (need to call RunStateOnByteUnlocked).
    // RunStateOnByte returns ns == NULL if it is out of memory.
    // ns == FullMatchState means the rest of the string matches.
    //
    // Okay to use bytemap[] not ByteMap() here, because
    // c is known to be an actual byte and not kByteEndText.

    State* ns = NextOf(s, bytemap[c]);
    if (ns == NULL) {
#if RE2_DFA_ATTRIB
      // 只在未命中这条路上写 —— 命中路径 (绝大多数字节) 一个字都没多写。
      atr_off_ = static_cast<size_t>(p > bp ? p - bp : bp - p);
      atr_len_ = params->text.size();
#endif
      ns = RunStateOnByteUnlocked(s, c);
      if (ns == NULL) {
        // After we reset the cache, we hold cache_mutex exclusively,
        // so if resetp != NULL, it means we filled the DFA state
        // cache with this search alone (without any other threads).
        // Benchmarks show that doing a state computation on every
        // byte runs at about 0.2 MB/s, while the NFA (nfa.cc) can do the
        // same at about 2 MB/s.  Unless we're processing an average
        // of 10 bytes per state computation, fail so that RE2 can
        // fall back to the NFA.  However, RE2::Set cannot fall back,
        // so we just have to keep on keeping on in that case.
#if RE2_DFA_ARENA_GROW
        // arena 该扩了 —— 这不是"缓存满", 一个状态都不会丢, 所以既不该触发退回 NFA,
        // 也不该动 resetp (那是给"多久没 flush 了"这个启发式用的)。
        // 手里的 start/s 在搬家后会失效, 照样用 StateSaver 存内容再按内容查回来。
        if (GrowPending()) {
          StateSaver save_start(this, start);
          StateSaver save_s(this, s);
          ResetCache(params->cache_lock, params->stats);   // 里面只是 realloc + 重定位
          if ((start = save_start.Restore()) == NULL ||
              (s = save_s.Restore()) == NULL) {
            params->failed = true;
            return false;
          }
          ns = RunStateOnByteUnlocked(s, c);
          if (ns == NULL) {
            LOG(DFATAL) << "RunStateOnByteUnlocked failed after GrowArena";
            params->failed = true;
            return false;
          }
        } else
#endif
        {
        // [backport re2 PR#609] 反向扫描时 p 是【递减】的, p - resetp 是负数, 转成 size_t
        // 变成天文数字, 这个 < 判断永远为假 —— 于是"造状态太慢就退回 NFA"这条启发式
        // 【在反向扫描里从来没生效过】, 反向 DFA 只会一遍遍 flush 自己。run_forward 是模板
        // 参数(编译期常量), 三目在两个特化里都被折叠掉, 热循环不多一条指令。
        // 实测 (?s)a[a-d]{24}b[a-d]* 扫 1MB: 修前反向扫描 flush 43 次 234ms, 修后 flush 1 次
        // 退回 NFA 34ms —— 7 倍; 结果与 stdlib 逐位一致。
        if (dfa_should_bail_when_slow && resetp != NULL &&
            static_cast<size_t>(run_forward ? p - resetp : resetp - p) <
                10*state_cache_.size() &&
            kind_ != Prog::kManyMatch) {
          params->failed = true;
          return false;
        }
        resetp = p;

        // Prepare to save start and s across the reset.
        StateSaver save_start(this, start);
        StateSaver save_s(this, s);

        // Discard all the States in the cache.
        ResetCache(params->cache_lock, params->stats);

        // Restore start and s so we can continue.
        if ((start = save_start.Restore()) == NULL ||
            (s = save_s.Restore()) == NULL) {
          // Restore already did LOG(DFATAL).
          params->failed = true;
          return false;
        }
        ns = RunStateOnByteUnlocked(s, c);
        if (ns == NULL) {
          LOG(DFATAL) << "RunStateOnByteUnlocked failed after ResetCache";
          params->failed = true;
          return false;
        }
        }
      }
    }
    if (ns <= SpecialStateMax) {
      if (ns == DeadState) {
        params->ep = reinterpret_cast<const char*>(lastmatch);
        return matched;
      }
      // FullMatchState
      params->ep = reinterpret_cast<const char*>(ep);
      return true;
    }

    s = ns;
#if RE2_DFA_HOTSTATS
    s->visits_++;   // 测量build限定: 热循环里唯一的写, 打开时会掉速, 只用来量分布
#endif
#if RE2_DFA_TRACE
    // 每条记录 = (进入的状态 id << 8) | 走过来用的字节类。
    // 上一条记录的 id 就是这一步的源状态, 所以离线可以还原出每一步走的是哪条边 (源,字节类),
    // 而 re2 的转移就是存在源状态里的那一槽 —— 有了边才能模拟"哪些槽会被清掉"。
    TraceVisit((s->tid_ << 8) | static_cast<uint32_t>(bytemap[c]));
#endif
    if (s->IsMatch()) {
      matched = true;
      // The DFA notices the match one byte late,
      // so adjust p before using it in the match.
      if (run_forward)
        lastmatch = p - 1;
      else
        lastmatch = p + 1;
      if (ExtraDebug)
        fprintf(stderr, "match @%td! [%s]\n", lastmatch - bp, DumpState(s).c_str());
      if (params->matches != NULL) {  // [backport re2 24d460a]
        for (int i = s->ninst_ - 1; i >= 0; i--) {
          int id = s->inst_[i];
          if (id == MatchSep)
            break;
          params->matches->insert(id);
        }
      }
      if (want_earliest_match) {
        params->ep = reinterpret_cast<const char*>(lastmatch);
        return true;
      }
    }
  }

  // Process one more byte to see if it triggers a match.
  // (Remember, matches are delayed one byte.)
  if (ExtraDebug)
    fprintf(stderr, "@etx: %s\n", DumpState(s).c_str());

  int lastbyte;
  if (run_forward) {
    if (EndPtr(params->text) == EndPtr(params->context))
      lastbyte = kByteEndText;
    else
      lastbyte = EndPtr(params->text)[0] & 0xFF;
  } else {
    if (BeginPtr(params->text) == BeginPtr(params->context))
      lastbyte = kByteEndText;
    else
      lastbyte = BeginPtr(params->text)[-1] & 0xFF;
  }

  State* ns = NextOf(s, ByteMap(lastbyte));
  if (ns == NULL) {
    ns = RunStateOnByteUnlocked(s, lastbyte);
    if (ns == NULL) {
      StateSaver save_s(this, s);
      ResetCache(params->cache_lock, params->stats);
      if ((s = save_s.Restore()) == NULL) {
        params->failed = true;
        return false;
      }
      ns = RunStateOnByteUnlocked(s, lastbyte);
      if (ns == NULL) {
        LOG(DFATAL) << "RunStateOnByteUnlocked failed after Reset";
        params->failed = true;
        return false;
      }
    }
  }
  if (ns <= SpecialStateMax) {
    if (ns == DeadState) {
      params->ep = reinterpret_cast<const char*>(lastmatch);
      return matched;
    }
    // FullMatchState
    params->ep = reinterpret_cast<const char*>(ep);
    return true;
  }

  s = ns;
  if (s->IsMatch()) {
    matched = true;
    lastmatch = p;
    if (ExtraDebug)
      fprintf(stderr, "match @etx! [%s]\n", DumpState(s).c_str());
    if (params->matches != NULL) {  // [backport re2 24d460a]
      for (int i = s->ninst_ - 1; i >= 0; i--) {
        int id = s->inst_[i];
        if (id == MatchSep)
          break;
        params->matches->insert(id);
      }
    }
  }

  params->ep = reinterpret_cast<const char*>(lastmatch);
  return matched;
}

// Inline specializations of the general loop.
bool DFA::SearchFFF(SearchParams* params) {
  return InlinedSearchLoop<false, false, false>(params);
}
bool DFA::SearchFFT(SearchParams* params) {
  return InlinedSearchLoop<false, false, true>(params);
}
bool DFA::SearchFTF(SearchParams* params) {
  return InlinedSearchLoop<false, true, false>(params);
}
bool DFA::SearchFTT(SearchParams* params) {
  return InlinedSearchLoop<false, true, true>(params);
}
bool DFA::SearchTFF(SearchParams* params) {
  return InlinedSearchLoop<true, false, false>(params);
}
bool DFA::SearchTFT(SearchParams* params) {
  return InlinedSearchLoop<true, false, true>(params);
}
bool DFA::SearchTTF(SearchParams* params) {
  return InlinedSearchLoop<true, true, false>(params);
}
bool DFA::SearchTTT(SearchParams* params) {
  return InlinedSearchLoop<true, true, true>(params);
}

// For performance, calls the appropriate specialized version
// of InlinedSearchLoop.
bool DFA::FastSearchLoop(SearchParams* params) {
  // Because the methods are private, the Searches array
  // cannot be declared at top level.
  static bool (DFA::*Searches[])(SearchParams*) = {
    &DFA::SearchFFF,
    &DFA::SearchFFT,
    &DFA::SearchFTF,
    &DFA::SearchFTT,
    &DFA::SearchTFF,
    &DFA::SearchTFT,
    &DFA::SearchTTF,
    &DFA::SearchTTT,
  };

  int index = 4 * params->can_prefix_accel +
              2 * params->want_earliest_match +
              1 * params->run_forward;
  return (this->*Searches[index])(params);
}


// The discussion of DFA execution above ignored the question of how
// to determine the initial state for the search loop.  There are two
// factors that influence the choice of start state.
//
// The first factor is whether the search is anchored or not.
// The regexp program (Prog*) itself has
// two different entry points: one for anchored searches and one for
// unanchored searches.  (The unanchored version starts with a leading ".*?"
// and then jumps to the anchored one.)
//
// The second factor is where text appears in the larger context, which
// determines which empty-string operators can be matched at the beginning
// of execution.  If text is at the very beginning of context, \A and ^ match.
// Otherwise if text is at the beginning of a line, then ^ matches.
// Otherwise it matters whether the character before text is a word character
// or a non-word character.
//
// The two cases (unanchored vs not) and four cases (empty-string flags)
// combine to make the eight cases recorded in the DFA's begin_text_[2],
// begin_line_[2], after_wordchar_[2], and after_nonwordchar_[2] cached
// StartInfos.  The start state for each is filled in the first time it
// is used for an actual search.

// Examines text, context, and anchored to determine the right start
// state for the DFA search loop.  Fills in params and returns true on success.
// Returns false on failure.
bool DFA::AnalyzeSearch(SearchParams* params) {
  const StringPiece& text = params->text;
  const StringPiece& context = params->context;

  // Sanity check: make sure that text lies within context.
  if (BeginPtr(text) < BeginPtr(context) || EndPtr(text) > EndPtr(context)) {
    LOG(DFATAL) << "context does not contain text";
    params->start = DeadState;
    return true;
  }

  // Determine correct search type.
  int start;
  uint32_t flags;
  if (params->run_forward) {
    if (BeginPtr(text) == BeginPtr(context)) {
      start = kStartBeginText;
      flags = kEmptyBeginText|kEmptyBeginLine;
    } else if (BeginPtr(text)[-1] == '\n') {
      start = kStartBeginLine;
      flags = kEmptyBeginLine;
    } else if (Prog::IsWordChar(BeginPtr(text)[-1] & 0xFF)) {
      start = kStartAfterWordChar;
      flags = kFlagLastWord;
    } else {
      start = kStartAfterNonWordChar;
      flags = 0;
    }
  } else {
    if (EndPtr(text) == EndPtr(context)) {
      start = kStartBeginText;
      flags = kEmptyBeginText|kEmptyBeginLine;
    } else if (EndPtr(text)[0] == '\n') {
      start = kStartBeginLine;
      flags = kEmptyBeginLine;
    } else if (Prog::IsWordChar(EndPtr(text)[0] & 0xFF)) {
      start = kStartAfterWordChar;
      flags = kFlagLastWord;
    } else {
      start = kStartAfterNonWordChar;
      flags = 0;
    }
  }
  if (params->anchored)
    start |= kStartAnchored;
  StartInfo* info = &start_[start];

  // Try once without cache_lock for writing.
  // Try again after resetting the cache
  // (ResetCache will relock cache_lock for writing).
  if (!AnalyzeSearchHelper(params, info, flags)) {
    ResetCache(params->cache_lock, params->stats);
    if (!AnalyzeSearchHelper(params, info, flags)) {
      params->failed = true;
      LOG(DFATAL) << "Failed to analyze start state.";
      return false;
    }
  }

  params->start = info->start.load(std::memory_order_acquire);

  // Even if we could prefix accel, we cannot do so when anchored and,
  // less obviously, we cannot do so when we are going to need flags.
  // This trick works only when there is a single byte that leads to a
  // different state!
  if (prog_->can_prefix_accel() &&
      !params->anchored &&
      params->start > SpecialStateMax &&
      params->start->flag_ >> kFlagNeedShift == 0)
    params->can_prefix_accel = true;

  if (ExtraDebug)
    fprintf(stderr, "anchored=%d fwd=%d flags=%#x state=%s can_prefix_accel=%d\n",
            params->anchored, params->run_forward, flags,
            DumpState(params->start).c_str(), params->can_prefix_accel);

  return true;
}

// Fills in info if needed.  Returns true on success, false on failure.
bool DFA::AnalyzeSearchHelper(SearchParams* params, StartInfo* info,
                              uint32_t flags) {
  // Quick check.
  State* start = info->start.load(std::memory_order_acquire);
  if (start != NULL)
    return true;

  MutexLock l(&mutex_);
  start = info->start.load(std::memory_order_relaxed);
  if (start != NULL)
    return true;

  q0_->clear();
  AddToQueue(q0_,
             params->anchored ? prog_->start() : prog_->start_unanchored(),
             flags);
  start = WorkqToCachedState(q0_, NULL, flags);
  if (start == NULL)
    return false;

  // Synchronize with "quick check" above.
  info->start.store(start, std::memory_order_release);
  return true;
}

// The actual DFA search: calls AnalyzeSearch and then FastSearchLoop.
bool DFA::Search(const StringPiece& text,
                 const StringPiece& context,
                 bool anchored,
                 bool want_earliest_match,
                 bool run_forward,
                 bool* failed,
                 const char** epp,
                 SparseSet* matches,
                 DFAScanStats* stats) {
  *epp = NULL;
  // 计数对象由被调方清零 —— 调用方栈上开一个就能用, 不必记得 memset。
  int64_t built_before = 0;
  if (stats != NULL) {
    memset(stats, 0, sizeof *stats);
    stats->bytes = static_cast<int64_t>(text.size());
    built_before = states_built_.load(std::memory_order_relaxed);
  }
  if (!ok()) {
    *failed = true;
    return false;
  }
  *failed = false;

  if (ExtraDebug) {
    fprintf(stderr, "\nprogram:\n%s\n", prog_->DumpUnanchored().c_str());
    fprintf(stderr, "text %s anchored=%d earliest=%d fwd=%d kind %d\n",
            std::string(text).c_str(), anchored, want_earliest_match, run_forward, kind_);
  }

  RWLocker l(&cache_mutex_);
  SearchParams params(text, context, &l);
  params.anchored = anchored;
  params.want_earliest_match = want_earliest_match;
  params.run_forward = run_forward;
  params.matches = matches;
  params.stats = stats;

  // 收尾: 不管从哪条路返回, 都把水位记进 stats。
  // (RWLocker 还在作用域内, 所以 state_cache_ 这时候读是安全的。)
  struct Finish {
    DFA* dfa; DFAScanStats* st; int64_t built_before;
    ~Finish() {
#if RE2_DFA_ATTRIB
      // 与 TraceFlushNow 同一个理由: Go 直接 exit_group, libc 的 atexit / fclose 跑不到,
      // 不每次扫完落盘的话文件尾巴就是半行。
      if (dfa->atr_birth_file_ != NULL)
        fflush(dfa->atr_birth_file_);
#endif
#if RE2_DFA_TRACE
      TraceFlushNow();   // Go 直接 exit_group, libc 的 atexit 跑不到, 只能每次扫完就落盘
#endif
      if (st == NULL) return;
      st->states_built = dfa->states_built_.load(std::memory_order_relaxed) - built_before;
      st->states_end = static_cast<int64_t>(dfa->state_cache_.size());
      st->state_budget = dfa->state_budget_;
      st->mem_left = dfa->mem_budget_;
    }
  } finish{this, stats, built_before};

  if (!AnalyzeSearch(&params)) {
    *failed = true;
    return false;
  }
  if (params.start == DeadState)
    return false;
  if (params.start == FullMatchState) {
    if (run_forward == want_earliest_match)
      *epp = text.data();
    else
      *epp = text.data() + text.size();
    return true;
  }
  if (ExtraDebug)
    fprintf(stderr, "start %s\n", DumpState(params.start).c_str());
  bool ret = FastSearchLoop(&params);
  if (params.failed) {
    *failed = true;
    return false;
  }
  *epp = params.ep;
  return ret;
}

// 当前水位 + 生涯累计。拿读锁是为了 state_cache_/mem_budget_ 不在别人 flush 到一半时被读。
void DFA::MemInfo(DFAMemInfo* out) {
  memset(out, 0, sizeof *out);
  out->built = true;
  RWLocker l(&cache_mutex_);
  MutexLock lock(&mutex_);
  out->state_budget = state_budget_;
  out->mem_left = mem_budget_;
  out->states = static_cast<int64_t>(state_cache_.size());
#if RE2_DFA_ARENA
  out->arena_cap = static_cast<int64_t>(arena_cap_);
#if RE2_DFA_INST_OUT
  out->arena_cap += static_cast<int64_t>(iarena_cap_);
#endif
#endif
  out->flushes_total = flushes_total_.load(std::memory_order_relaxed);
  out->states_built_total = states_built_.load(std::memory_order_relaxed);
}

void DFA::AttribInfo(DFAAttribInfo* out) {
  int64_t* ps = out->pat_states;
  int64_t* pi = out->pat_insts;
  int cap = out->pat_cap;
  memset(out, 0, sizeof *out);
  out->pat_states = ps;
  out->pat_insts = pi;
  out->pat_cap = cap;
  out->built = true;
#if !RE2_DFA_ATTRIB
  out->enabled = false;
#else
  out->enabled = true;
  RWLocker l(&cache_mutex_);
  MutexLock lock(&mutex_);
  out->npat = atr_npat_;
  out->states_total = states_built_.load(std::memory_order_relaxed);
  out->shared_insts = atr_shared_insts_;
  out->ninst_sum = atr_ninst_sum_;
  out->ninst_max = atr_ninst_max_;
  memcpy(out->ninst_hist, atr_ninst_hist_, sizeof out->ninst_hist);
  memcpy(out->birth_hist, atr_birth_hist_, sizeof out->birth_hist);
  int n = atr_npat_ < cap ? atr_npat_ : cap;
  for (int i = 0; i < n; i++) {
    if (ps != NULL) ps[i] = pat_states_[i];
    if (pi != NULL) pi[i] = pat_insts_[i];
  }
#endif
}

DFA* Prog::GetDFA(MatchKind kind) {
  // For a forward DFA, half the memory goes to each DFA.
  // However, if it is a "many match" DFA, then there is
  // no counterpart with which the memory must be shared.
  //
  // For a reverse DFA, all the memory goes to the
  // "longest match" DFA, because RE2 never does reverse
  // "first match" searches.
  if (kind == kFirstMatch) {
    std::call_once(dfa_first_once_, [](Prog* prog) {
      prog->dfa_first_ = new DFA(prog, kFirstMatch, prog->dfa_mem_ / 2);
      prog->dfa_first_ready_.store(true, std::memory_order_release);
    }, this);
    return dfa_first_;
  } else if (kind == kManyMatch) {
    std::call_once(dfa_first_once_, [](Prog* prog) {
      prog->dfa_first_ = new DFA(prog, kManyMatch, prog->dfa_mem_);
      prog->dfa_first_ready_.store(true, std::memory_order_release);
    }, this);
    return dfa_first_;
  } else {
    std::call_once(dfa_longest_once_, [](Prog* prog) {
      if (!prog->reversed_)
        prog->dfa_longest_ = new DFA(prog, kLongestMatch, prog->dfa_mem_ / 2);
      else
        prog->dfa_longest_ = new DFA(prog, kLongestMatch, prog->dfa_mem_);
      prog->dfa_longest_ready_.store(true, std::memory_order_release);
    }, this);
    return dfa_longest_;
  }
}

void Prog::DeleteDFA(DFA* dfa) {
  delete dfa;
}

// Executes the regexp program to search in text,
// which itself is inside the larger context.  (As a convenience,
// passing a NULL context is equivalent to passing text.)
// Returns true if a match is found, false if not.
// If a match is found, fills in match0->end() to point at the end of the match
// and sets match0->begin() to text.begin(), since the DFA can't track
// where the match actually began.
//
// This is the only external interface (class DFA only exists in this file).
//
// 查缓存水位。【不会】把 DFA 建出来 —— 没建过就返回 built=false, 免得一次诊断调用
// 反过来分配几十 MB。
void Prog::GetDFAMemInfo(MatchKind kind, DFAMemInfo* out) {
  memset(out, 0, sizeof *out);
  bool longest = (kind == kLongestMatch || kind == kFullMatch);
  const std::atomic<bool>& ready = longest ? dfa_longest_ready_ : dfa_first_ready_;
  if (!ready.load(std::memory_order_acquire))
    return;
  DFA* dfa = longest ? dfa_longest_ : dfa_first_;
  if (dfa == NULL)
    return;
  dfa->MemInfo(out);
}

void Prog::GetDFAAttribInfo(MatchKind kind, DFAAttribInfo* out) {
  int64_t* ps = out->pat_states;
  int64_t* pi = out->pat_insts;
  int cap = out->pat_cap;
  memset(out, 0, sizeof *out);
  out->pat_states = ps;
  out->pat_insts = pi;
  out->pat_cap = cap;
  bool longest = (kind == kLongestMatch || kind == kFullMatch);
  const std::atomic<bool>& ready = longest ? dfa_longest_ready_ : dfa_first_ready_;
  if (!ready.load(std::memory_order_acquire))
    return;   // DFA 还没建 —— 与 GetDFAMemInfo 一样, 查询本身不替你建
  DFA* dfa = longest ? dfa_longest_ : dfa_first_;
  if (dfa == NULL)
    return;
  dfa->AttribInfo(out);
}

bool Prog::SearchDFA(const StringPiece& text, const StringPiece& const_context,
                     Anchor anchor, MatchKind kind, StringPiece* match0,
                     bool* failed, SparseSet* matches, DFAScanStats* stats) {
  *failed = false;
  if (stats != NULL)
    memset(stats, 0, sizeof *stats);   // 下面有几条早退路径不进 DFA

  StringPiece context = const_context;
  if (context.data() == NULL)
    context = text;
  bool caret = anchor_start();
  bool dollar = anchor_end();
  if (reversed_) {
    using std::swap;
    swap(caret, dollar);
  }
  if (caret && BeginPtr(context) != BeginPtr(text))
    return false;
  if (dollar && EndPtr(context) != EndPtr(text))
    return false;

  // Handle full match by running an anchored longest match
  // and then checking if it covers all of text.
  bool anchored = anchor == kAnchored || anchor_start() || kind == kFullMatch;
  bool endmatch = false;
  if (kind == kManyMatch) {
    // This is split out in order to avoid clobbering kind.
  } else if (kind == kFullMatch || anchor_end()) {
    endmatch = true;
    kind = kLongestMatch;
  }

  // If the caller doesn't care where the match is (just whether one exists),
  // then we can stop at the very first match we find, the so-called
  // "earliest match".
  bool want_earliest_match = false;
  if (kind == kManyMatch) {
    // This is split out in order to avoid clobbering kind.
    if (matches == NULL) {
      want_earliest_match = true;
    }
  } else if (match0 == NULL && !endmatch) {
    want_earliest_match = true;
    kind = kLongestMatch;
  }

  DFA* dfa = GetDFA(kind);
  const char* ep;
  bool matched = dfa->Search(text, context, anchored,
                             want_earliest_match, !reversed_,
                             failed, &ep, matches, stats);
  if (*failed) {
    hooks::GetDFASearchFailureHook()({
        // Nothing yet...
    });
    return false;
  }
  if (!matched)
    return false;
  if (endmatch && ep != (reversed_ ? text.data() : text.data() + text.size()))
    return false;

  // If caller cares, record the boundary of the match.
  // We only know where it ends, so use the boundary of text
  // as the beginning.
  if (match0) {
    if (reversed_)
      *match0 =
          StringPiece(ep, static_cast<size_t>(text.data() + text.size() - ep));
    else
      *match0 =
          StringPiece(text.data(), static_cast<size_t>(ep - text.data()));
  }
  return true;
}

// Build out all states in DFA.  Returns number of states.
int DFA::BuildAllStates(const Prog::DFAStateCallback& cb) {
  if (!ok())
    return 0;

  // Pick out start state for unanchored search
  // at beginning of text.
  RWLocker l(&cache_mutex_);
  SearchParams params(StringPiece(), StringPiece(), &l);
  params.anchored = false;
  if (!AnalyzeSearch(&params) ||
      params.start == NULL ||
      params.start == DeadState)
    return 0;

  // Add start state to work queue.
  // Note that any State* that we handle here must point into the cache,
  // so we can simply depend on pointer-as-a-number hashing and equality.
  std::unordered_map<State*, int> m;
  std::deque<State*> q;
  m.emplace(params.start, static_cast<int>(m.size()));
  q.push_back(params.start);

  // Compute the input bytes needed to cover all of the next pointers.
  int nnext = prog_->bytemap_range() + 1;  // + 1 for kByteEndText slot
  std::vector<int> input(nnext);
  for (int c = 0; c < 256; c++) {
    int b = prog_->bytemap()[c];
    while (c < 256-1 && prog_->bytemap()[c+1] == b)
      c++;
    input[b] = c;
  }
  input[prog_->bytemap_range()] = kByteEndText;

  // Scratch space for the output.
  std::vector<int> output(nnext);

  // Flood to expand every state.
  bool oom = false;
  while (!q.empty()) {
    State* s = q.front();
    q.pop_front();
    for (int c : input) {
      State* ns = RunStateOnByteUnlocked(s, c);
      if (ns == NULL) {
        oom = true;
        break;
      }
      if (ns == DeadState) {
        output[ByteMap(c)] = -1;
        continue;
      }
      if (m.find(ns) == m.end()) {
        m.emplace(ns, static_cast<int>(m.size()));
        q.push_back(ns);
      }
      output[ByteMap(c)] = m[ns];
    }
    if (cb)
      cb(oom ? NULL : output.data(),
         s == FullMatchState || s->IsMatch());
    if (oom)
      break;
  }

  return static_cast<int>(m.size());
}

// Build out all states in DFA for kind.  Returns number of states.
int Prog::BuildEntireDFA(MatchKind kind, const DFAStateCallback& cb) {
  return GetDFA(kind)->BuildAllStates(cb);
}

// Computes min and max for matching string.
// Won't return strings bigger than maxlen.
bool DFA::PossibleMatchRange(std::string* min, std::string* max, int maxlen) {
  if (!ok())
    return false;

  // NOTE: if future users of PossibleMatchRange want more precision when
  // presented with infinitely repeated elements, consider making this a
  // parameter to PossibleMatchRange.
  static int kMaxEltRepetitions = 0;

  // Keep track of the number of times we've visited states previously. We only
  // revisit a given state if it's part of a repeated group, so if the value
  // portion of the map tuple exceeds kMaxEltRepetitions we bail out and set
  // |*max| to |PrefixSuccessor(*max)|.
  //
  // Also note that previously_visited_states[UnseenStatePtr] will, in the STL
  // tradition, implicitly insert a '0' value at first use. We take advantage
  // of that property below.
  std::unordered_map<State*, int> previously_visited_states;

  // Pick out start state for anchored search at beginning of text.
  RWLocker l(&cache_mutex_);
  SearchParams params(StringPiece(), StringPiece(), &l);
  params.anchored = true;
  if (!AnalyzeSearch(&params))
    return false;
  if (params.start == DeadState) {  // No matching strings
    *min = "";
    *max = "";
    return true;
  }
  if (params.start == FullMatchState)  // Every string matches: no max
    return false;

  // The DFA is essentially a big graph rooted at params.start,
  // and paths in the graph correspond to accepted strings.
  // Each node in the graph has potentially 256+1 arrows
  // coming out, one for each byte plus the magic end of
  // text character kByteEndText.

  // To find the smallest possible prefix of an accepted
  // string, we just walk the graph preferring to follow
  // arrows with the lowest bytes possible.  To find the
  // largest possible prefix, we follow the largest bytes
  // possible.

  // The test for whether there is an arrow from s on byte j is
  //    ns = RunStateOnByteUnlocked(s, j);
  //    if (ns == NULL)
  //      return false;
  //    if (ns != DeadState && ns->ninst > 0)
  // The RunStateOnByteUnlocked call asks the DFA to build out the graph.
  // It returns NULL only if the DFA has run out of memory,
  // in which case we can't be sure of anything.
  // The second check sees whether there was graph built
  // and whether it is interesting graph.  Nodes might have
  // ns->ninst == 0 if they exist only to represent the fact
  // that a match was found on the previous byte.

  // Build minimum prefix.
  State* s = params.start;
  min->clear();
  MutexLock lock(&mutex_);
  for (int i = 0; i < maxlen; i++) {
    if (previously_visited_states[s] > kMaxEltRepetitions)
      break;
    previously_visited_states[s]++;

    // Stop if min is a match.
    State* ns = RunStateOnByte(s, kByteEndText);
    if (ns == NULL)  // DFA out of memory
      return false;
    if (ns != DeadState && (ns == FullMatchState || ns->IsMatch()))
      break;

    // Try to extend the string with low bytes.
    bool extended = false;
    for (int j = 0; j < 256; j++) {
      ns = RunStateOnByte(s, j);
      if (ns == NULL)  // DFA out of memory
        return false;
      if (ns == FullMatchState ||
          (ns > SpecialStateMax && ns->ninst_ > 0)) {
        extended = true;
        min->append(1, static_cast<char>(j));
        s = ns;
        break;
      }
    }
    if (!extended)
      break;
  }

  // Build maximum prefix.
  previously_visited_states.clear();
  s = params.start;
  max->clear();
  for (int i = 0; i < maxlen; i++) {
    if (previously_visited_states[s] > kMaxEltRepetitions)
      break;
    previously_visited_states[s] += 1;

    // Try to extend the string with high bytes.
    bool extended = false;
    for (int j = 255; j >= 0; j--) {
      State* ns = RunStateOnByte(s, j);
      if (ns == NULL)
        return false;
      if (ns == FullMatchState ||
          (ns > SpecialStateMax && ns->ninst_ > 0)) {
        extended = true;
        max->append(1, static_cast<char>(j));
        s = ns;
        break;
      }
    }
    if (!extended) {
      // Done, no need for PrefixSuccessor.
      return true;
    }
  }

  // Stopped while still adding to *max - round aaaaaaaaaa... to aaaa...b
  PrefixSuccessor(max);

  // If there are no bytes left, we have no way to say "there is no maximum
  // string".  We could make the interface more complicated and be able to
  // return "there is no maximum but here is a minimum", but that seems like
  // overkill -- the most common no-max case is all possible strings, so not
  // telling the caller that the empty string is the minimum match isn't a
  // great loss.
  if (max->empty())
    return false;

  return true;
}

// PossibleMatchRange for a Prog.
bool Prog::PossibleMatchRange(std::string* min, std::string* max, int maxlen) {
  // Have to use dfa_longest_ to get all strings for full matches.
  // For example, (a|aa) never matches aa in first-match mode.
  return GetDFA(kLongestMatch)->PossibleMatchRange(min, max, maxlen);
}

}  // namespace re2

// ── hgmLibre2 追加 ── 流式游程扫描 (自带 namespace re2)。放文件末尾是因为它要用到
// 上面所有 DFA 内部件, 而 class DFA 整个定义就在本文件里, 外面的编译单元看不见。
#include "re2_dfa_spanscan.inc"
