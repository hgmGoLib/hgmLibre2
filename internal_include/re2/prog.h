// Copyright 2007 The RE2 Authors.  All Rights Reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#ifndef RE2_PROG_H_
#define RE2_PROG_H_

// Compiled representation of regular expressions.
// See regexp.h for the Regexp class, which represents a regular
// expression symbolically.

#include <stdint.h>
#include <atomic>
#include <functional>
#include <mutex>
#include <string>
#include <vector>
#include <type_traits>

#include "util/util.h"
#include "util/logging.h"
#include "re2/dfa_stats.h"
#include "re2/pod_array.h"
#include "re2/re2.h"
#include "re2/sparse_array.h"
#include "re2/sparse_set.h"

namespace re2 {

// Opcodes for Inst
enum InstOp {
  kInstAlt = 0,      // choose between out_ and out1_
  kInstAltMatch,     // Alt: out_ is [00-FF] and back, out1_ is match; or vice versa.
  kInstByteRange,    // next (possible case-folded) byte must be in [lo_, hi_]
  kInstCapture,      // capturing parenthesis number cap_
  kInstEmptyWidth,   // empty-width special (^ $ ...); bit(s) set in empty_
  kInstMatch,        // found a match!
  kInstNop,          // no-op; occasionally unavoidable
  kInstFail,         // never match; occasionally unavoidable
  kNumInst,
};

// Bit flags for empty-width specials
enum EmptyOp {
  kEmptyBeginLine        = 1<<0,      // ^ - beginning of line
  kEmptyEndLine          = 1<<1,      // $ - end of line
  kEmptyBeginText        = 1<<2,      // \A - beginning of text
  kEmptyEndText          = 1<<3,      // \z - end of text
  kEmptyWordBoundary     = 1<<4,      // \b - word boundary
  kEmptyNonWordBoundary  = 1<<5,      // \B - not \b
  kEmptyAllFlags         = (1<<6)-1,
};

class DFA;
class DFASpanScan;   // ── hgmLibre2 追加 ── 见 re2/span_scan.h
class Regexp;

// Compiled form of regexp program.
class Prog {
 public:
  Prog();
  ~Prog();

  // Single instruction in regexp program.
  class Inst {
   public:
    // See the assertion below for why this is so.
    Inst() = default;

    // Copyable.
    Inst(const Inst&) = default;
    Inst& operator=(const Inst&) = default;

    // Constructors per opcode
    void InitAlt(uint32_t out, uint32_t out1);
    void InitByteRange(int lo, int hi, int foldcase, uint32_t out);
    void InitCapture(int cap, uint32_t out);
    void InitEmptyWidth(EmptyOp empty, uint32_t out);
    void InitMatch(int id);
    void InitNop(uint32_t out);
    void InitFail();

    // Getters
    int id(Prog* p) { return static_cast<int>(this - p->inst_.data()); }
    InstOp opcode() { return static_cast<InstOp>(out_opcode_&7); }
    int last()      { return (out_opcode_>>3)&1; }
    int out()       { return out_opcode_>>4; }
    int out1()      { DCHECK(opcode() == kInstAlt || opcode() == kInstAltMatch); return out1_; }
    int cap()       { DCHECK_EQ(opcode(), kInstCapture); return cap_; }
    int lo()        { DCHECK_EQ(opcode(), kInstByteRange); return lo_; }
    int hi()        { DCHECK_EQ(opcode(), kInstByteRange); return hi_; }
    int foldcase()  { DCHECK_EQ(opcode(), kInstByteRange); return hint_foldcase_&1; }
    int hint()      { DCHECK_EQ(opcode(), kInstByteRange); return hint_foldcase_>>1; }
    int match_id()  { DCHECK_EQ(opcode(), kInstMatch); return match_id_; }
    EmptyOp empty() { DCHECK_EQ(opcode(), kInstEmptyWidth); return empty_; }

    bool greedy(Prog* p) {
      DCHECK_EQ(opcode(), kInstAltMatch);
      return p->inst(out())->opcode() == kInstByteRange ||
             (p->inst(out())->opcode() == kInstNop &&
              p->inst(p->inst(out())->out())->opcode() == kInstByteRange);
    }

    // Does this inst (an kInstByteRange) match c?
    inline bool Matches(int c) {
      DCHECK_EQ(opcode(), kInstByteRange);
      if (foldcase() && 'A' <= c && c <= 'Z')
        c += 'a' - 'A';
      return lo_ <= c && c <= hi_;
    }

    // Returns string representation for debugging.
    std::string Dump();

    // Maximum instruction id.
    // (Must fit in out_opcode_. PatchList/last steal another bit.)
    static const int kMaxInst = (1<<28) - 1;

   private:
    void set_opcode(InstOp opcode) {
      out_opcode_ = (out()<<4) | (last()<<3) | opcode;
    }

    void set_last() {
      out_opcode_ = (out()<<4) | (1<<3) | opcode();
    }

    void set_out(int out) {
      out_opcode_ = (out<<4) | (last()<<3) | opcode();
    }

    void set_out_opcode(int out, InstOp opcode) {
      out_opcode_ = (out<<4) | (last()<<3) | opcode;
    }

    uint32_t out_opcode_;  // 28 bits: out, 1 bit: last, 3 (low) bits: opcode
    union {                // additional instruction arguments:
      uint32_t out1_;      // opcode == kInstAlt
                           //   alternate next instruction

      int32_t cap_;        // opcode == kInstCapture
                           //   Index of capture register (holds text
                           //   position recorded by capturing parentheses).
                           //   For \n (the submatch for the nth parentheses),
                           //   the left parenthesis captures into register 2*n
                           //   and the right one captures into register 2*n+1.

      int32_t match_id_;   // opcode == kInstMatch
                           //   Match ID to identify this match (for re2::Set).

      struct {             // opcode == kInstByteRange
        uint8_t lo_;       //   byte range is lo_-hi_ inclusive
        uint8_t hi_;       //
        uint16_t hint_foldcase_;  // 15 bits: hint, 1 (low) bit: foldcase
                           //   hint to execution engines: the delta to the
                           //   next instruction (in the current list) worth
                           //   exploring iff this instruction matched; 0
                           //   means there are no remaining possibilities,
                           //   which is most likely for character classes.
                           //   foldcase: A-Z -> a-z before checking range.
      };

      EmptyOp empty_;       // opcode == kInstEmptyWidth
                            //   empty_ is bitwise OR of kEmpty* flags above.
    };

    friend class Compiler;
    friend struct PatchList;
    friend class Prog;
  };

  // Inst must be trivial so that we can freely clear it with memset(3).
  // Arrays of Inst are initialised by copying the initial elements with
  // memmove(3) and then clearing any remaining elements with memset(3).
  static_assert(std::is_trivial<Inst>::value, "Inst must be trivial");

  // Whether to anchor the search.
  enum Anchor {
    kUnanchored,  // match anywhere
    kAnchored,    // match only starting at beginning of text
  };

  // Kind of match to look for (for anchor != kFullMatch)
  //
  // kLongestMatch mode finds the overall longest
  // match but still makes its submatch choices the way
  // Perl would, not in the way prescribed by POSIX.
  // The POSIX rules are much more expensive to implement,
  // and no one has needed them.
  //
  // kFullMatch is not strictly necessary -- we could use
  // kLongestMatch and then check the length of the match -- but
  // the matching code can run faster if it knows to consider only
  // full matches.
  enum MatchKind {
    kFirstMatch,     // like Perl, PCRE
    kLongestMatch,   // like egrep or POSIX
    kFullMatch,      // match only entire text; implies anchor==kAnchored
    kManyMatch       // for SearchDFA, records set of matches
  };

  Inst *inst(int id) { return &inst_[id]; }
  int start() { return start_; }
  void set_start(int start) { start_ = start; }
  int start_unanchored() { return start_unanchored_; }
  void set_start_unanchored(int start) { start_unanchored_ = start; }
  int size() { return size_; }
  bool reversed() { return reversed_; }
  void set_reversed(bool reversed) { reversed_ = reversed; }
  int list_count() { return list_count_; }
  int inst_count(InstOp op) { return inst_count_[op]; }
  uint16_t* list_heads() { return list_heads_.data(); }
  size_t bit_state_text_max_size() { return bit_state_text_max_size_; }
  int64_t dfa_mem() { return dfa_mem_; }
  void set_dfa_mem(int64_t dfa_mem) { dfa_mem_ = dfa_mem; }
  bool anchor_start() { return anchor_start_; }
  void set_anchor_start(bool b) { anchor_start_ = b; }
  bool anchor_end() { return anchor_end_; }
  void set_anchor_end(bool b) { anchor_end_ = b; }
  int bytemap_range() { return bytemap_range_; }
  const uint8_t* bytemap() { return bytemap_; }
  bool can_prefix_accel() { return prefix_size_ != 0; }

  // Accelerates to the first likely occurrence of the prefix.
  // Returns a pointer to the first byte or NULL if not found.
  const void* PrefixAccel(const void* data, size_t size) {
    DCHECK(can_prefix_accel());
    if (prefix_foldcase_) {
      return PrefixAccel_ShiftDFA(data, size);
    } else if (prefix_size_ != 1) {
      return PrefixAccel_FrontAndBack(data, size);
    } else {
      return memchr(data, prefix_front_, size);
    }
  }

  // Configures prefix accel using the analysis performed during compilation.
  void ConfigurePrefixAccel(const std::string& prefix, bool prefix_foldcase);

  // An implementation of prefix accel that uses prefix_dfa_ to perform
  // case-insensitive search.
  const void* PrefixAccel_ShiftDFA(const void* data, size_t size);

  // An implementation of prefix accel that looks for prefix_front_ and
  // prefix_back_ to return fewer false positives than memchr(3) alone.
  const void* PrefixAccel_FrontAndBack(const void* data, size_t size);

  // Returns string representation of program for debugging.
  std::string Dump();
  std::string DumpUnanchored();
  std::string DumpByteMap();

  // Returns the set of kEmpty flags that are in effect at
  // position p within context.
  static uint32_t EmptyFlags(const StringPiece& context, const char* p);

  // Returns whether byte c is a word character: ASCII only.
  // Used by the implementation of \b and \B.
  // This is not right for Unicode, but:
  //   - it's hard to get right in a byte-at-a-time matching world
  //     (the DFA has only one-byte lookahead).
  //   - even if the lookahead were possible, the Progs would be huge.
  // This crude approximation is the same one PCRE uses.
  static bool IsWordChar(uint8_t c) {
    return ('A' <= c && c <= 'Z') ||
           ('a' <= c && c <= 'z') ||
           ('0' <= c && c <= '9') ||
           c == '_';
  }

  // Execution engines.  They all search for the regexp (run the prog)
  // in text, which is in the larger context (used for ^ $ \b etc).
  // Anchor and kind control the kind of search.
  // Returns true if match found, false if not.
  // If match found, fills match[0..nmatch-1] with submatch info.
  // match[0] is overall match, match[1] is first set of parens, etc.
  // If a particular submatch is not matched during the regexp match,
  // it is set to NULL.
  //
  // Matching text == StringPiece(NULL, 0) is treated as any other empty
  // string, but note that on return, it will not be possible to distinguish
  // submatches that matched that empty string from submatches that didn't
  // match anything.  Either way, match[i] == NULL.

  // Search using NFA: can find submatches but kind of slow.
  bool SearchNFA(const StringPiece& text, const StringPiece& context,
                 Anchor anchor, MatchKind kind,
                 StringPiece* match, int nmatch);

  // Search using DFA: much faster than NFA but only finds
  // end of match and can use a lot more memory.
  // Returns whether a match was found.
  // If the DFA runs out of memory, sets *failed to true and returns false.
  // If matches != NULL and kind == kManyMatch and there is a match,
  // SearchDFA fills matches with the match IDs of the final matching state.
  // stats != NULL 时, 顺便把这一次扫描的 DFA 计数填进去 (见 re2/dfa_stats.h)。
  // 不传 = 与上游行为逐字节一致, 零开销。
  bool SearchDFA(const StringPiece& text, const StringPiece& context,
                 Anchor anchor, MatchKind kind, StringPiece* match0,
                 bool* failed, SparseSet* matches,
                 DFAScanStats* stats = NULL);

  // ── hgmLibre2 追加 ── 在这个 Prog 的 kManyMatch DFA 上开一个【流式游程扫描】工作区
  // (语义/用法见 re2/span_scan.h; 用完 DFASpanScanFree)。nid = Set 里的 pattern 条数。
  DFASpanScan* NewSpanScan(int nid);

  // ── hgmLibre2 追加 (非上游 re2) ──
  // 给定匹配的一端, 求同一条 pattern 在这个端点上能达到的【另一端】(最长的那个)。
  // 语义 / 为什么这件事只能在库里做, 见 re2/span_scan.h 末尾那段。
  // 返回 1 = 找到并写 *out, 0 = 这条 pattern 在这个端点上根本不匹配, -1 = 参数错 / DFA 放弃。
  int SpanResolve(int nid, const char* text, int textlen,
                  int from, int bound, int id, int32_t* out);

  // 查这个 Prog 上某一 kind 的 DFA 缓存水位 (没建出来则 out->built=false)。
  // 只读, 会短暂拿 DFA 的读锁; 可以在扫描并发进行时调。
  void GetDFAMemInfo(MatchKind kind, DFAMemInfo* out);

  // 查这个 kind 的 DFA 的建状态归因 (见 re2/dfa_stats.h 的 DFAAttribInfo)。
  // 与 GetDFAMemInfo 一样: 【不会】替你把 DFA 建出来, 没建就返回 built=false。
  // out->pat_states / pat_insts / pat_cap 由调用方填好再传进来。
  void GetDFAAttribInfo(MatchKind kind, DFAAttribInfo* out);

  // The callback issued after building each DFA state with BuildEntireDFA().
  // If next is null, then the memory budget has been exhausted and building
  // will halt. Otherwise, the state has been built and next points to an array
  // of bytemap_range()+1 slots holding the next states as per the bytemap and
  // kByteEndText. The number of the state is implied by the callback sequence:
  // the first callback is for state 0, the second callback is for state 1, ...
  // match indicates whether the state is a matching state.
  using DFAStateCallback = std::function<void(const int* next, bool match)>;

  // Build the entire DFA for the given match kind.
  // Usually the DFA is built out incrementally, as needed, which
  // avoids lots of unnecessary work.
  // If cb is not empty, it receives one callback per state built.
  // Returns the number of states built.
  // FOR TESTING OR EXPERIMENTAL PURPOSES ONLY.
  int BuildEntireDFA(MatchKind kind, const DFAStateCallback& cb);

  // Compute bytemap.
  void ComputeByteMap();

  // Run peep-hole optimizer on program.
  void Optimize();

  // One-pass NFA: only correct if IsOnePass() is true,
  // but much faster than NFA (competitive with PCRE)
  // for those expressions.
  bool IsOnePass();
  bool SearchOnePass(const StringPiece& text, const StringPiece& context,
                     Anchor anchor, MatchKind kind,
                     StringPiece* match, int nmatch);

  // Bit-state backtracking.  Fast on small cases but uses memory
  // proportional to the product of the list count and the text size.
  bool CanBitState() { return list_heads_.data() != NULL; }
  bool SearchBitState(const StringPiece& text, const StringPiece& context,
                      Anchor anchor, MatchKind kind,
                      StringPiece* match, int nmatch);

  static const int kMaxOnePassCapture = 5;  // $0 through $4

  // Backtracking search: the gold standard against which the other
  // implementations are checked.  FOR TESTING ONLY.
  // It allocates a ton of memory to avoid running forever.
  // It is also recursive, so can't use in production (will overflow stacks).
  // The name "Unsafe" here is supposed to be a flag that
  // you should not be using this function.
  bool UnsafeSearchBacktrack(const StringPiece& text,
                             const StringPiece& context,
                             Anchor anchor, MatchKind kind,
                             StringPiece* match, int nmatch);

  // Computes range for any strings matching regexp. The min and max can in
  // some cases be arbitrarily precise, so the caller gets to specify the
  // maximum desired length of string returned.
  //
  // Assuming PossibleMatchRange(&min, &max, N) returns successfully, any
  // string s that is an anchored match for this regexp satisfies
  //   min <= s && s <= max.
  //
  // Note that PossibleMatchRange() will only consider the first copy of an
  // infinitely repeated element (i.e., any regexp element followed by a '*' or
  // '+' operator). Regexps with "{N}" constructions are not affected, as those
  // do not compile down to infinite repetitions.
  //
  // Returns true on success, false on error.
  bool PossibleMatchRange(std::string* min, std::string* max, int maxlen);

  // Outputs the program fanout into the given sparse array.
  void Fanout(SparseArray<int>* fanout);

  // Compiles a collection of regexps to Prog.  Each regexp will have
  // its own Match instruction recording the index in the output vector.
  //
  // ── hgmLibre2 追加 (非上游 re2): reversed ──
  // reversed=true 编出【反着跑】的 set 程序 (所有 concat 反序, ^/$ 与 \b 由编译器自行对调),
  // SearchDFA 会据此把 run_forward 置 false, 从 text 末尾往前逐字节走【原始 buffer】——
  // 调用方不需要自己反转正文, 也不会踩到多字节 UTF-8 被拆散的坑。
  // 用途: 起始类窄于重复类的计数重复 (如 [A-Za-z][A-Za-z0-9]{2,19}key) 正向 DFA 状态数指数,
  // 反向只要线性; 命中集 (哪几条 pattern 命中) 与正向逐位相同。
  // 限制: reversed=true 时只支持 anchor==RE2::UNANCHORED, 其余 anchor 返回 NULL。
  // 配套: RE2::Set 在 reversed 模式下必须把 HaveMatch 节点【前置】而不是后置 (见 set.cc),
  // 否则反序之后 Match 指令会跑到 pattern 前面, 一进门就命中。
  static Prog* CompileSet(Regexp* re, RE2::Anchor anchor, int64_t max_mem,
                          bool reversed = false);

  // Flattens the Prog from "tree" form to "list" form. This is an in-place
  // operation in the sense that the old instructions are lost.
  void Flatten();

  // Walks the Prog; the "successor roots" or predecessors of the reachable
  // instructions are marked in rootmap or predmap/predvec, respectively.
  // reachable and stk are preallocated scratch structures.
  void MarkSuccessors(SparseArray<int>* rootmap,
                      SparseArray<int>* predmap,
                      std::vector<std::vector<int>>* predvec,
                      SparseSet* reachable, std::vector<int>* stk);

  // Walks the Prog from the given "root" instruction; the "dominator root"
  // of the reachable instructions (if such exists) is marked in rootmap.
  // reachable and stk are preallocated scratch structures.
  void MarkDominator(int root, SparseArray<int>* rootmap,
                     SparseArray<int>* predmap,
                     std::vector<std::vector<int>>* predvec,
                     SparseSet* reachable, std::vector<int>* stk);

  // Walks the Prog from the given "root" instruction; the reachable
  // instructions are emitted in "list" form and appended to flat.
  // reachable and stk are preallocated scratch structures.
  void EmitList(int root, SparseArray<int>* rootmap,
                std::vector<Inst>* flat,
                SparseSet* reachable, std::vector<int>* stk);

  // Computes hints for ByteRange instructions in [begin, end).
  void ComputeHints(std::vector<Inst>* flat, int begin, int end);

  // Controls whether the DFA should bail out early if the NFA would be faster.
  // FOR TESTING ONLY.
  static void TESTING_ONLY_set_dfa_should_bail_when_slow(bool b);

 private:
  friend class Compiler;

  DFA* GetDFA(MatchKind kind);
  void DeleteDFA(DFA* dfa);

  bool anchor_start_;       // regexp has explicit start anchor
  bool anchor_end_;         // regexp has explicit end anchor
  bool reversed_;           // whether program runs backward over input
  bool did_flatten_;        // has Flatten been called?
  bool did_onepass_;        // has IsOnePass been called?

  int start_;               // entry point for program
  int start_unanchored_;    // unanchored entry point for program
  int size_;                // number of instructions
  int bytemap_range_;       // bytemap_[x] < bytemap_range_

  bool prefix_foldcase_;    // whether prefix is case-insensitive
  size_t prefix_size_;      // size of prefix (0 if no prefix)
  union {
    uint64_t* prefix_dfa_;  // "Shift DFA" for prefix
    struct {
      int prefix_front_;    // first byte of prefix
      int prefix_back_;     // last byte of prefix
    };
  };

  int list_count_;                  // count of lists (see above)
  int inst_count_[kNumInst];        // count of instructions by opcode
  PODArray<uint16_t> list_heads_;   // sparse array enumerating list heads
                                    // not populated if size_ is overly large
  size_t bit_state_text_max_size_;  // upper bound (inclusive) on text.size()

  PODArray<Inst> inst_;              // pointer to instruction array
  PODArray<uint8_t> onepass_nodes_;  // data for OnePass nodes

  int64_t dfa_mem_;         // Maximum memory for DFAs.
  DFA* dfa_first_;          // DFA cached for kFirstMatch/kManyMatch
  DFA* dfa_longest_;        // DFA cached for kLongestMatch/kFullMatch

  uint8_t bytemap_[256];    // map from input bytes to byte classes

  std::once_flag dfa_first_once_;
  std::once_flag dfa_longest_once_;
  // GetDFAMemInfo 用: 只想【看一眼】缓存水位, 不想因为查询而把 DFA 建出来。
  // call_once 的 flag 没有"问一下建了没"的接口, 所以另立一个 release/acquire 的标志。
  std::atomic<bool> dfa_first_ready_{false};
  std::atomic<bool> dfa_longest_ready_{false};

  Prog(const Prog&) = delete;
  Prog& operator=(const Prog&) = delete;
};

// std::string_view in MSVC has iterators that aren't just pointers and
// that don't allow comparisons between different objects - not even if
// those objects are views into the same string! Thus, we provide these
// conversion functions for convenience.
static inline const char* BeginPtr(const StringPiece& s) {
  return s.data();
}
static inline const char* EndPtr(const StringPiece& s) {
  return s.data() + s.size();
}

}  // namespace re2

#endif  // RE2_PROG_H_
