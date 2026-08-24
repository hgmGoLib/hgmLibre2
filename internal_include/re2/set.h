// Copyright 2010 The RE2 Authors.  All Rights Reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#ifndef RE2_SET_H_
#define RE2_SET_H_

#include <stdint.h>

#include <memory>
#include <string>
#include <utility>
#include <vector>

#include "re2/dfa_stats.h"
#include "re2/re2.h"

namespace re2 {
class DFASpanScan;   // ── hgmLibre2 追加 ── 见 re2/span_scan.h
class Prog;
class Regexp;
}  // namespace re2

namespace re2 {

// An RE2::Set represents a collection of regexps that can
// be searched for simultaneously.
class RE2::Set {
 public:
  enum ErrorKind {
    kNoError = 0,
    kNotCompiled,   // The set is not compiled.
    kOutOfMemory,   // The DFA ran out of memory.
    kInconsistent,  // The result is inconsistent. This should never happen.
  };

  struct ErrorInfo {
    ErrorKind kind;
  };

  Set(const RE2::Options& options, RE2::Anchor anchor);
  // ── hgmLibre2 追加 (非上游 re2) ──
  // reversed=true: 把整个 set 编成【反向程序】, Match 从 text 末尾往前扫【原始 buffer】。
  // 命中集语义与正向完全相同 (仍然只回答"哪几条命中", 不回答"在哪"), 但对
  // "起始类窄于重复类的计数重复" 这类形状, 状态数从指数塌回线性。
  // 只支持 anchor==UNANCHORED; 其余 anchor 下 Compile() 返回 false。
  Set(const RE2::Options& options, RE2::Anchor anchor, bool reversed);
  ~Set();

  // 这个 Set 是不是反向编译的 (见上面的三参构造)。
  bool reversed() const { return reversed_; }

  // Not copyable.
  Set(const Set&) = delete;
  Set& operator=(const Set&) = delete;
  // Movable.
  Set(Set&& other);
  Set& operator=(Set&& other);

  // Adds pattern to the set using the options passed to the constructor.
  // Returns the index that will identify the regexp in the output of Match(),
  // or -1 if the regexp cannot be parsed.
  // Indices are assigned in sequential order starting from 0.
  // Errors do not increment the index; if error is not NULL, *error will hold
  // the error message from the parser.
  int Add(const StringPiece& pattern, std::string* error);

  // Compiles the set in preparation for matching.
  // Returns false if the compiler runs out of memory.
  // Add() must not be called again after Compile().
  // Compile() must be called before Match().
  bool Compile();

  // Returns true if text matches at least one of the regexps in the set.
  // Fills v (if not NULL) with the indices of the matching regexps.
  // Callers must not expect v to be sorted.
  bool Match(const StringPiece& text, std::vector<int>* v) const;

  // As above, but populates error_info (if not NULL) when none of the regexps
  // in the set matched. This can inform callers when DFA execution fails, for
  // example, because they might wish to handle that case differently.
  bool Match(const StringPiece& text, std::vector<int>* v,
             ErrorInfo* error_info) const;

  // ── hgmLibre2 追加 (非上游 re2) ──
  // 同上, 外加把【这一次扫描】的 DFA 计数填进 stats (见 re2/dfa_stats.h)。
  // stats 由调用方在栈上开一个即可, 不必清零; 传 NULL 等价于上面那个重载。
  bool Match(const StringPiece& text, std::vector<int>* v,
             ErrorInfo* error_info, DFAScanStats* stats) const;

  // ── hgmLibre2 追加 (非上游 re2) ──
  // 开一个【流式游程扫描】工作区: Match 只回答"哪几条命中", 这个回答"命中在哪"。
  // 语义 (吐什么 / 为什么是游程 / 为什么是轮询) 全在 re2/span_scan.h, 用完 DFASpanScanFree。
  // 没编译 / OOM 返回 NULL。工作区可以反复用于多次扫描, 但不是并发安全的。
  DFASpanScan* NewSpanScan() const;

  // ── hgmLibre2 追加 (非上游 re2) ──
  // 给定 NewSpanScan 吐出来的一个端点, 求同一条 pattern 在这个端点上的【另一端】(最长的那个)。
  // 语义 / 为什么这一步只能在库里做, 见 re2/span_scan.h 末尾那段。
  // 无状态、只读, 可以与扫描并发调 (自己拿 DFA 的缓存读锁)。
  // 返回 1 = 找到并写 *out, 0 = 这条 pattern 在这个端点上根本不匹配, -1 = 参数错 / DFA 放弃。
  int ResolveSpan(const char* text, int textlen, int from, int bound,
                  int id, int32_t* out) const;

  // 查这个 Set 的 DFA 缓存水位 + 生涯累计 (没扫过则 out->built=false)。
  // 只读, 短暂拿 DFA 的读锁, 可以和扫描并发调。
  void MemInfo(DFAMemInfo* out) const;

  // 查"这几万个状态是谁造的、有多贵、在正文哪一段造的" (见 re2/dfa_stats.h 的 DFAAttribInfo)。
  // 需要 -DRE2_DFA_ATTRIB=1 编译, 否则只返回 enabled=false。
  // out->pat_states / pat_insts / pat_cap 由调用方填好缓冲区再传进来。
  void AttribInfo(DFAAttribInfo* out) const;

 private:
  typedef std::pair<std::string, re2::Regexp*> Elem;

  RE2::Options options_;
  RE2::Anchor anchor_;
  bool reversed_;   // ── hgmLibre2 追加 ── 反向编译 (见三参构造)
  std::vector<Elem> elem_;
  bool compiled_;
  int size_;
  std::unique_ptr<re2::Prog> prog_;
};

}  // namespace re2

#endif  // RE2_SET_H_
