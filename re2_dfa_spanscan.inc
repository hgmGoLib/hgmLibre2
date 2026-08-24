// re2_dfa_spanscan.inc — ── hgmLibre2 追加 (非上游 re2) ──
// RE2::Set "命中在哪"的实现, 两件事:
//   · DFASpanScan  : 流式游程扫描 —— 一遍扫正文, 吐每条 pattern 的命中【端点】;
//   · SpanDFA::Resolve : 锚定解析 —— 给定一个端点, 求同一条 pattern 的【另一端】。
// 两者的语义/为什么这么设计见 re2/span_scan.h。
//
// 这份文件被 re2_dfa.cc 在文件末尾 #include 进来 (不是独立编译单元): class DFA 整个定义
// 就在 re2_dfa.cc 里, 外面看不见 State / RWLocker / StateSaver / RunStateOnByteUnlocked,
// 所以只能同编译单元。拆成单独文件是为了不让 re2_dfa.cc 再长 400 行。
//
// 🔴 热循环是【另写的一份】, 不往 InlinedSearchLoop 里塞 if。两者要回答的问题不同:
//    老的一路只管把 id 塞进 SparseSet (每个 id 塞一次就够, 位置无所谓, 塞完还能 early-out);
//    这一份每个命中字节都要维护"这条 pattern 的游程长到哪了", 且永远不能 early-out。
//    混在一起会给老循环 (全库最热的那个) 白加分支。能共用的是【状态推进那几行】,
//    但那几行本来就短, 复制一份比拿模板/lambda 绕回去可读得多。
//
// 与老循环相比【去掉】的东西 (都不是省事, 是这条路上用不到):
//   · can_prefix_accel : Set 恒 anchored (AnalyzeSearch 里 anchored ⇒ 不开 prefix accel);
//   · want_earliest_match : 要吐全部位置, 不可能提前收工;
//   · FullMatchState   : WorkqToCachedState 对 kManyMatch 明确不返回它 (见该函数 kInstAltMatch 分支);
//   · "造状态太慢就退回 NFA" : 那条启发式本来就写着 kind_ != kManyMatch, Set 走不到;
//   · start 状态       : 老循环留着它只为 prefix accel, 这里没有 ⇒ 挂起时也不用存它。

namespace re2 {

// DeadState / SpecialStateMax 那几个宏是拿裸的 State* 写的, 只有在 DFA 的成员函数里才解析得开。
// DFASpanScan 不是 DFA 的成员, 所以这里各留一份带限定名的。
static inline DFA::State* SpanDeadState() { return reinterpret_cast<DFA::State*>(1); }
static inline DFA::State* SpanSpecialMax() { return reinterpret_cast<DFA::State*>(2); }

// SpanDFA 是"游程扫描"和"锚定解析"共用的那几行 DFA 私有细节 (DFA 里给它开了 friend)。
// 只有两件事: 把状态推过一个字节, 和问一个 match 状态里有没有某条 pattern。
struct SpanDFA {
  // Advance 把 *sp 推过一个字节类 c (c 可以是 DFA::kByteEndText)。
  // 状态没造过就现造; 造不出来是"arena 该扩了"或"缓存满了", 分别处理 —— 两条路都要用
  // StateSaver 把状态按内容存下再按内容查回来 (搬家/清表之后裸 State* 就废了)。
  // off/textlen 只给 RE2_DFA_ATTRIB 的归因用, 不影响结果。
  static bool Advance(DFA* dfa, DFA::RWLocker* lock, DFA::State** sp, int c,
                      int off, int textlen) {
    DFA::State* s = *sp;
    DFA::State* ns = dfa->NextOf(s, dfa->ByteMap(c));
    if (ns != NULL) {
      *sp = ns;
      return true;
    }
#if RE2_DFA_ATTRIB
    dfa->atr_off_ = static_cast<size_t>(off >= 0 ? off : -off);
    dfa->atr_len_ = static_cast<size_t>(textlen);
#endif
    ns = dfa->RunStateOnByteUnlocked(s, c);
    if (ns != NULL) {
      *sp = ns;
      return true;
    }
    {
      DFA::StateSaver save_s(dfa, s);
      dfa->ResetCache(lock, NULL);   // arena 该扩就只扩不清 (见 ResetCache 里的 GrowPending)
      if ((s = save_s.Restore()) == NULL)
        return false;
    }
    ns = dfa->RunStateOnByteUnlocked(s, c);
    if (ns == NULL)
      return false;
    *sp = ns;
    return true;
  }

  // HasId 问: 这个 match 状态里有没有第 id 条 pattern。
  // inst_ 的布局是 [inst ids...] MatchSep [match ids...], 从尾巴往回走到 MatchSep 为止
  // (与 InlinedSearchLoop 里那段逐字一致)。
  static bool HasId(DFA::State* s, int id) {
    for (int i = s->ninst_ - 1; i >= 0; i--) {
      int t = s->inst_[i];
      if (t == MatchSep)
        break;
      if (t == id)
        return true;
    }
    return false;
  }

  static int Resolve(DFA* dfa, bool run_forward, const char* text, int textlen,
                     int from, int bound, int id, int32_t* out);
};

class DFASpanScan {
 public:
  DFASpanScan(DFA* dfa, int nid, bool run_forward)
      : dfa_(dfa),
        nid_(nid),
        run_forward_(run_forward),
        phase_(kPhaseDone),
        textlen_(0),
        off_(0),
        s_(NULL),
        saved_(NULL),
        flushi_(0),
        out_(NULL),
        n_(0),
        limit_(0),
        lock_(NULL) {
    int n = nid > 0 ? nid : 1;
    runlo_.resize(n);
    runhi_.resize(n);
    pend_.assign(n, 0);
    pendlist_.reserve(n);
  }

  ~DFASpanScan() { delete saved_; }

  bool Begin(int textlen) {
    if (textlen < 0)
      return false;
    // 上一次扫描要是没扫完就被丢下 (调用方提前 return false), 挂着的状态副本要清掉。
    delete saved_;
    saved_ = NULL;
    for (size_t i = 0; i < pendlist_.size(); i++)
      pend_[pendlist_[i]] = 0;
    pendlist_.clear();
    flushi_ = 0;
    s_ = NULL;
    textlen_ = textlen;
    off_ = 0;
    phase_ = (nid_ > 0) ? kPhaseInit : kPhaseDone;
    return true;
  }

  int Step(const char* text, int textlen, int32_t* out, int outcap, int* more);

 private:
  enum Phase {
    kPhaseInit,   // 还没选起点 (要有正文才能选, 所以推迟到第一次 Step)
    kPhaseLoop,   // 主循环: p 还在正文里
    kPhaseEtx,    // 主循环走完, 还差最后那个 kByteEndText
    kPhaseFlush,  // 正文扫完, 把还挂着的游程收口吐出去
    kPhaseDone,
  };

  // Emit 把一条游程写进本批输出。反向扫描时游程是【递减】长出来的, 这里正过来,
  // 对外恒 lo <= hi (原文坐标), 免得调用方每次都要判方向。
  inline void Emit(int32_t id, int32_t a, int32_t b) {
    out_[n_] = id;
    if (a <= b) {
      out_[n_ + 1] = a;
      out_[n_ + 2] = b;
    } else {
      out_[n_ + 1] = b;
      out_[n_ + 2] = a;
    }
    n_ += 3;
  }

  // Note 记一次命中: pos 是这条 pattern 这次的位置 (正向=右端不含, 反向=左端含)。
  // 与上次连号就把游程接长, 不连号就把上一段收口吐掉、从 pos 重开一段。
  //
  // 🔴 成本 = O(1)/次, 整体 O(该状态 match id 表长)/命中字节 —— 与老循环那个
  //    params->matches->insert(id) 的循环【同阶】, 一分不多。
  //    反过来说: 千万别写成"每个命中字节扫一遍挂着的表, 找这次没出现的 id 收口",
  //    那是 O(nid)/字节 (150 条 pattern 就是 150 次/字节), 比想省的那部分还贵。
  //    这里的收口是【惰性】的 —— 等这条 pattern 下次出现、或者整篇扫完才发现它掉出去了。
  template <bool run_forward>
  inline void Note(int32_t id, int32_t pos) {
    if (!pend_[id]) {
      pend_[id] = 1;
      pendlist_.push_back(id);
      runlo_[id] = pos;
      runhi_[id] = pos;
      return;
    }
    if (pos == (run_forward ? runhi_[id] + 1 : runhi_[id] - 1)) {
      runhi_[id] = pos;
      return;
    }
    Emit(id, runlo_[id], runhi_[id]);
    runlo_[id] = pos;
    runhi_[id] = pos;
  }

  // NoteState 把一个 match 状态里的所有 pattern id 都记一遍 (布局见 SpanDFA::HasId)。
  template <bool run_forward>
  inline void NoteState(DFA::State* s, int32_t pos) {
    for (int i = s->ninst_ - 1; i >= 0; i--) {
      int id = s->inst_[i];
      if (id == MatchSep)
        break;
      Note<run_forward>(static_cast<int32_t>(id), pos);
    }
  }

  template <bool run_forward>
  int RunLoop(const uint8_t* bp, int32_t* dummy_unused);

  bool Analyze(const char* text, int textlen);
  bool AdvanceState(int c);   // s_ = NextOf(s_, c), 必要时造状态/扩 arena/flush 缓存
  void Suspend();
  bool Resume();

  DFA* dfa_;
  int nid_;
  bool run_forward_;

  Phase phase_;
  int textlen_;
  int off_;                  // 挂起点: p - bp (正反向都是这个口径)
  DFA::State* s_;            // 当前状态 (只在持锁期间有效)
  DFA::StateSaver* saved_;   // 挂起期间按内容存下的 s_ (不持锁也不会失效)
  size_t flushi_;            // kPhaseFlush 收口到 pendlist_ 的第几个了

  // 每条 pattern 当前挂着的游程 (原文坐标)。pend_[id]!=0 才有效。
  std::vector<int32_t> runlo_;
  std::vector<int32_t> runhi_;
  std::vector<uint8_t> pend_;
  std::vector<int32_t> pendlist_;   // 挂着的 id 列表 (无重复), 扫完时按它收口

  // 本批输出 (只在一次 Step 里有效)
  int32_t* out_;
  int n_;        // 已写进 out_ 的 int32 个数
  int limit_;    // n_ 超过它就挂起; = outcap - 3*nid, 保证下一个命中字节一定塞得下
  DFA::RWLocker* lock_;

  DFASpanScan(const DFASpanScan&) = delete;
  DFASpanScan& operator=(const DFASpanScan&) = delete;
};

// Analyze 选起点。照 Prog::SearchDFA 对 kManyMatch 那条路走: anchored=false
// (走 start_unanchored_, 也就是带 .*? 前缀的那个入口 —— 与 RE2::Set::Match 同一个起点),
// want_earliest_match=false, context == text。
bool DFASpanScan::Analyze(const char* text, int textlen) {
  StringPiece t(text, static_cast<size_t>(textlen));
  DFA::SearchParams params(t, t, lock_);
  params.anchored = false;
  params.want_earliest_match = false;
  params.run_forward = run_forward_;
  if (!dfa_->AnalyzeSearch(&params) || params.failed)
    return false;
  if (params.start == SpanDeadState()) {
    // 起点就死了 = 整篇不可能有任何命中。
    phase_ = kPhaseDone;
    s_ = NULL;
    return true;
  }
  s_ = params.start;
  return true;
}

// AdvanceState 把 s_ 推过一个字节类 c (c 可以是 kByteEndText)。见 SpanDFA::Advance。
bool DFASpanScan::AdvanceState(int c) {
  return SpanDFA::Advance(dfa_, lock_, &s_, c, off_, textlen_);
}

// Suspend 放掉状态指针 (按内容存起来), 让调用方可以在【不持任何锁】的情况下把这批取走。
void DFASpanScan::Suspend() {
  delete saved_;
  saved_ = new DFA::StateSaver(dfa_, s_);
  s_ = NULL;
}

bool DFASpanScan::Resume() {
  // 收口阶段只是把游程表倒出去, 一个 DFA 状态都用不上 —— 也【不能】用: 挂起期间锁是放掉的,
  // 手里那个 State* 早就可能被别的线程 flush 掉了。这里显式把它清成 NULL, 免得后面误用。
  if (phase_ == kPhaseFlush) {
    delete saved_;
    saved_ = NULL;
    s_ = NULL;
    return true;
  }
  if (saved_ == NULL)
    return false;   // kPhaseLoop 的挂起点一定存过状态, 没存 = 内部状态坏了
  DFA::State* s = saved_->Restore();
  if (s == NULL) {
    // 极少数情况: 挂起期间别的线程把缓存填满了, 查不回来。清一次再查。
    dfa_->ResetCache(lock_, NULL);
    s = saved_->Restore();
    if (s == NULL)
      return false;
  }
  delete saved_;
  saved_ = NULL;
  s_ = s;
  return true;
}

// RunLoop 是新的热循环。返回 0 = 本批攒满了要挂起, 1 = 正文走完了 (进入 etx), -1 = 出错。
template <bool run_forward>
int DFASpanScan::RunLoop(const uint8_t* bp, int32_t* /*unused*/) {
  // 当前状态和 dfa_ 都走【本地变量】, 与上游 InlinedSearchLoop 写法一致 (它的 s 也是局部的)。
  // ⚠ 实测这一条在这份循环上【量不出差别】(零命中 1MiB 上 ±1%, 落在噪声里) —— 留着只是
  //    为了和上游那份对得上, 不要拿它当优化项。
  DFA* dfa = dfa_;
  const uint8_t* bytemap = dfa->prog_->bytemap();
  const uint8_t* p = bp + off_;
  const uint8_t* ep = run_forward ? (bp + textlen_) : bp;
  DFA::State* s = s_;

  while (p != ep) {
    int c;
    if (run_forward)
      c = *p++;
    else
      c = *--p;

    DFA::State* ns = dfa->NextOf(s, bytemap[c]);
    if (ns == NULL) {
      off_ = static_cast<int>(p - bp);
      s_ = s;                      // AdvanceState 是拿 s_ 进出的 (要存/查状态内容)
      if (!AdvanceState(c)) {
        return -1;
      }
      s = s_;
    } else {
      s = ns;
    }

    if (s <= SpanSpecialMax()) {
      // kManyMatch 不会出 FullMatchState (见文件头), 所以这里只可能是 DeadState:
      // 从这里往后不可能再有任何命中 ⇒ 正文部分到此为止, 直接去收口。
      off_ = static_cast<int>(p - bp);
      s_ = NULL;          // 收口用不上状态; 清掉免得挂起之后有人误用这个失效指针
      phase_ = kPhaseFlush;
      return 1;
    }

    if (s->IsMatch()) {
      // 匹配晚一个字节才被看见 (见 InlinedSearchLoop 头上的说明), 所以位置要退一格:
      // 正向 p-1 = 匹配右端 (不含); 反向 p+1 = 匹配左端 (含)。
      const uint8_t* m = run_forward ? p - 1 : p + 1;
      s_ = s;                      // 挂起要把它按内容存下来, 所以先写回
      NoteState<run_forward>(s, static_cast<int32_t>(m - bp));
      if (n_ > limit_) {
        off_ = static_cast<int>(p - bp);
        Suspend();
        return 0;
      }
    }
  }
  off_ = static_cast<int>(p - bp);
  s_ = s;
  phase_ = kPhaseEtx;
  return 1;
}

int DFASpanScan::Step(const char* text, int textlen, int32_t* out, int outcap, int* more) {
  *more = 0;
  if (phase_ == kPhaseDone)
    return 0;
  if (textlen != textlen_ || (textlen > 0 && text == NULL))
    return -1;
  if (outcap < 3 * nid_)
    return -1;

  out_ = out;
  n_ = 0;
  // limit_ 保证"再来一个命中字节也一定塞得下": 一个状态最多带 nid 条 pattern,
  // 最坏每条都收口一次 = 3*nid 个 int32。所以本批写到 limit_ 就该收手。
  limit_ = outcap - 3 * nid_;

  DFA::RWLocker l(&dfa_->cache_mutex_);
  lock_ = &l;

  const uint8_t* bp = reinterpret_cast<const uint8_t*>(text);

  if (phase_ == kPhaseInit) {
    if (!Analyze(text, textlen)) {
      phase_ = kPhaseDone;
      lock_ = NULL;
      return -1;
    }
    if (phase_ == kPhaseDone) {   // Analyze 判了起点是 DeadState
      lock_ = NULL;
      return 0;
    }
    // 起点本身就可能是 match 状态 (空匹配 / 能匹配空串的 pattern)。
    off_ = run_forward_ ? 0 : textlen_;
    if (s_->IsMatch()) {
      if (run_forward_)
        NoteState<true>(s_, static_cast<int32_t>(off_));
      else
        NoteState<false>(s_, static_cast<int32_t>(off_));
    }
    phase_ = kPhaseLoop;
  } else if (!Resume()) {
    phase_ = kPhaseDone;
    lock_ = NULL;
    return -1;
  }

  if (phase_ == kPhaseLoop) {
    int r = run_forward_ ? RunLoop<true>(bp, NULL) : RunLoop<false>(bp, NULL);
    if (r < 0) {
      phase_ = kPhaseDone;
      lock_ = NULL;
      return -1;
    }
    if (r == 0) {   // 本批满了, 挂起
      *more = 1;
      lock_ = NULL;
      return n_ / 3;
    }
  }

  if (phase_ == kPhaseEtx) {
    // 再喂一个"正文结束"符号看看是不是又触发一次匹配 (匹配晚一个字节)。
    // context == text, 所以这个字节恒为 kByteEndText (不必像老循环那样判 context 边界)。
    if (!AdvanceState(DFA::kByteEndText)) {
      phase_ = kPhaseDone;
      lock_ = NULL;
      return -1;
    }
    if (s_ > SpanSpecialMax() && s_->IsMatch()) {
      int32_t pos = static_cast<int32_t>(off_);
      if (run_forward_)
        NoteState<true>(s_, pos);
      else
        NoteState<false>(s_, pos);
    }
    s_ = NULL;
    phase_ = kPhaseFlush;
  }

  // 收口: 把还挂着的游程吐出去。一条 pattern 最多挂一段, 所以最多 nid 条,
  // 但本批不一定装得下 (前面已经写了东西), 装不下就挂起 —— 这一段不需要 DFA 状态,
  // 挂起时也就没什么要存的。
  while (flushi_ < pendlist_.size()) {
    if (n_ > limit_) {
      s_ = NULL;   // 见 Resume: 挂起期间不持锁, 留着的 State* 随时可能失效
      *more = 1;
      lock_ = NULL;
      return n_ / 3;
    }
    int32_t id = pendlist_[flushi_++];
    Emit(id, runlo_[id], runhi_[id]);
    pend_[id] = 0;
  }
  pendlist_.clear();
  flushi_ = 0;
  phase_ = kPhaseDone;
  lock_ = NULL;
  return n_ / 3;
}

// Prog::NewSpanScan 在这个 Prog 的 kManyMatch DFA 上开一个工作区。
// run_forward 跟 SearchDFA 一个口径: 反向编译的 prog 就反着扫原始 buffer。
DFASpanScan* Prog::NewSpanScan(int nid) {
  if (nid < 0)
    return NULL;
  DFA* dfa = GetDFA(kManyMatch);
  if (dfa == NULL || !dfa->ok())
    return NULL;
  return new DFASpanScan(dfa, nid, !reversed_);
}

// ── 锚定解析 (SpanDFA::Resolve) ────────────────────────────────────────────
//
// 给定匹配的一端, 求同一条 pattern 在这个端点上能达到的【另一端】。方向跟着 set 的编译方向:
//   正向 set: from = 匹配左端 (含), 返回右端 (不含) —— text[from, *out) 是 id 的一个匹配;
//   反向 set: from = 匹配右端 (不含), 返回左端 (含) —— text[*out, from) 是 id 的一个匹配。
//
// 🔴 从 start_setanchored 进 —— 这是它必须待在库里的唯一原因。set 程序里那截 .*? 前缀是
//    【编进程序】的, 从外面进不去"不带前缀的入口": 调用方想在 Go 那侧补这一步, 只能自己
//    另编一条 \A(?:pat) 的锚定正则 —— 那是每条 pattern 一个 RE2 对象、一份独立 DFA 缓存,
//    而且得跟 set 里那条的语义手工对齐。从这里进去, 用的还是 set 自己那份 DFA。
//
// 🔴 返回【最长】的那个, 不是碰到的第一个。DFA 是一路走到死状态才知道还能不能更长;
//    "碰到第一个 match 状态就收工"给的是【最短】匹配 —— `AAA-[A-Za-z0-9]{8,16}` 会只认
//    前 8 个字符, 把命中截断。代价并不对称: 走到死状态的成本 = 这条命中实际能延伸到多远,
//    与正文长度无关。除非 pattern 本身就能无限延伸 ((?s).*KEY 那种), 那用 bound 掐住。
//
// bound = 最远看到哪 (正向是上界, 反向是下界); 负数 = 不限。context 恒为【整篇正文】,
// 所以 \b / ^ / $ 看到的永远是真实邻居, 而不是被 bound 切出来的人为边界。
int SpanDFA::Resolve(DFA* dfa, bool run_forward, const char* text, int textlen,
                     int from, int bound, int id, int32_t* out) {
  if (textlen < 0 || from < 0 || from > textlen || id < 0)
    return -1;
  if (text == NULL)
    return -1;
  if (run_forward) {
    if (bound < 0 || bound > textlen)
      bound = textlen;
    if (bound < from)
      bound = from;
  } else {
    if (bound < 0)
      bound = 0;
    if (bound > from)
      bound = from;
  }
  const char* rlo = run_forward ? text + from : text + bound;   // 搜索区间左端
  const char* rhi = run_forward ? text + bound : text + from;   // 搜索区间右端

  DFA::RWLocker l(&dfa->cache_mutex_);
  StringPiece region(rlo, static_cast<size_t>(rhi - rlo));
  StringPiece context(text, static_cast<size_t>(textlen));
  DFA::SearchParams params(region, context, &l);
  params.anchored = true;   // ← 与 DFASpanScan 唯一的实质差别就是这一行: 走真锚定入口
  params.want_earliest_match = false;
  params.run_forward = run_forward;
  if (!dfa->AnalyzeSearch(&params) || params.failed)
    return -1;
  DFA::State* s = params.start;
  if (s == SpanDeadState())
    return 0;

  const uint8_t* bp = reinterpret_cast<const uint8_t*>(text);
  const uint8_t* p = reinterpret_cast<const uint8_t*>(run_forward ? rlo : rhi);
  const uint8_t* ep = reinterpret_cast<const uint8_t*>(run_forward ? rhi : rlo);
  int32_t best = -1;

  // 起点本身就可能是 match 状态 (这条 pattern 能匹配空串)。
  if (s > SpanSpecialMax() && s->IsMatch() && HasId(s, id))
    best = static_cast<int32_t>(p - bp);

  const uint8_t* bytemap = dfa->prog_->bytemap();
  while (p != ep) {
    int c = run_forward ? *p++ : *--p;
    DFA::State* ns = dfa->NextOf(s, bytemap[c]);
    if (ns == NULL) {
      if (!Advance(dfa, &l, &s, c, static_cast<int>(p - bp), textlen))
        return -1;
    } else {
      s = ns;
    }
    if (s <= SpanSpecialMax()) {
      // kManyMatch 不会出 FullMatchState (见文件头) ⇒ 只可能是 DeadState:
      // 再往前走不可能有任何匹配, 更不可能有更长的。收工。
      s = NULL;
      break;
    }
    if (s->IsMatch()) {
      // 匹配晚一个字节才被看见 —— 正向 p-1 = 右端 (不含), 反向 p+1 = 左端 (含)。
      const uint8_t* m = run_forward ? p - 1 : p + 1;
      if (HasId(s, id))
        best = static_cast<int32_t>(m - bp);
    }
  }

  if (s != NULL) {
    // 最后再喂一个"边界符号": 区间端点正好是整篇正文的两头才是 kByteEndText, 否则喂
    // 【真实的邻居字节】(与 InlinedSearchLoop 那段 lastbyte 一个口径) —— 这样 \b / $
    // 判的是它在整篇正文里的真实处境, 而不是 bound 切出来的假边界。
    int lastbyte;
    if (run_forward)
      lastbyte = (rhi == text + textlen) ? DFA::kByteEndText : (rhi[0] & 0xFF);
    else
      lastbyte = (rlo == text) ? DFA::kByteEndText : (rlo[-1] & 0xFF);
    if (!Advance(dfa, &l, &s, lastbyte, static_cast<int>(p - bp), textlen))
      return -1;
    if (s > SpanSpecialMax() && s->IsMatch() && HasId(s, id))
      best = static_cast<int32_t>(p - bp);
  }

  if (best < 0)
    return 0;
  *out = best;
  return 1;
}

int Prog::SpanResolve(int nid, const char* text, int textlen,
                      int from, int bound, int id, int32_t* out) {
  if (out == NULL || id < 0 || id >= nid)
    return -1;
  DFA* dfa = GetDFA(kManyMatch);
  if (dfa == NULL || !dfa->ok())
    return -1;
  return SpanDFA::Resolve(dfa, !reversed_, text, textlen, from, bound, id, out);
}

void DFASpanScanFree(DFASpanScan* ss) { delete ss; }

bool DFASpanScanBegin(DFASpanScan* ss, int textlen) {
  if (ss == NULL)
    return false;
  return ss->Begin(textlen);
}

int DFASpanScanStep(DFASpanScan* ss, const char* text, int textlen,
                    int32_t* out, int outcap, int* more) {
  if (ss == NULL || more == NULL || out == NULL) {
    if (more != NULL)
      *more = 0;
    return -1;
  }
  return ss->Step(text, textlen, out, outcap, more);
}

}  // namespace re2
