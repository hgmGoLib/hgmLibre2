// re2_dfa_spanscan_inl.h — ── hgmLibre2 追加 (非上游 re2) ──
// RE2::Set "命中在哪"的实现, 两件事:
//   · DFASpanScan  : 流式游程扫描 —— 一遍扫正文, 吐每条 pattern 的命中【端点】;
//   · SpanDFA::Resolve : 锚定解析 —— 给定一个端点, 求同一条 pattern 的【另一端】。
// 两者的语义/为什么这么设计见 re2_span_scan.h。
//
// 🔴 这不是普通头文件, 是"只给 re2_dfa.cc 末尾 #include 一次"的实现片段 (没有 include
// guard, 里面全是函数体, 被第二个编译单元 include 就是重复定义)。名字用 -inl.h 是跟着上游
// 的 re2_walker-inl.h 走的。为什么只能这么摆: class DFA 整个定义就在 re2_dfa.cc 里, 外面
// 看不见 State / RWLocker / StateSaver / RunStateOnByteUnlocked, 所以只能同编译单元;
// 拆成单独文件是为了不让 re2_dfa.cc 再长 400 行。
// 🔴 扩展名【必须是 .h】, 不能用 .inc: go build 的缓存只哈希包目录里它认得的扩展名
// (.go/.c/.cc/.cpp/.h/.s...), .inc 对 go 完全不存在 —— 只改这个文件的话 go build 会直接
// 复用旧目标文件, 编出来的是旧二进制, 而且一声不吭。也不能叫 .cc (那会被当成第二个编译单元)。
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

  // ── 可行前缀回推 (MatchScanner2 / 路 D2 用) ────────────────────────────────
  // ViableStart  : 拿"种全部指令"的那个起始状态 (按 flags 缓存在 start_[base|kStartViable])。
  // ViableStarts : 从 from 往左走一趟, 沿途把每一个【候选起点】收下来。
  static DFA::State* ViableStart(DFA* dfa, int base, uint32_t flags);
  static int ViableStarts(DFA* dfa, const char* text, int textlen,
                          int from, int bound, int id, int32_t* out, int outcap);
};

class DFASpanScan {
 public:
  typedef DFASpanScanG2Rec G2Rec;   // g2: 交给调用方的分量记录 (定义在 span_scan.h)

  // 逐条状态打在一起: 一次命中只碰一条 cache line (拆成几个 vector 会碰几条)。
  struct G2St {
    int32_t* p;    // 当前分量的结束位置【游程数组】(NULL = 没开段); p[2k],p[2k+1] = 第 k 条的 lo,hi
    int32_t cap;   // p 的容量, 单位 int32 (8 起 = 4 条游程, 二倍扩)
    int32_t n;     // 已用游程条数
    int32_t lo;    // 本分量左界 (上一次断气的位置)
  };

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
    gbool_.assign(n, 0);
    ghit_.assign(n, 0);
    G2St z;
    z.p = NULL;
    z.cap = 0;
    z.n = 0;
    z.lo = 0;
    g2_.assign(n, z);
    g2bucket_.resize(31);
    g2want_.assign(n, 8);
  }

  ~DFASpanScan() {
#ifdef G2_PEAKDUMP
    G2PeakDump();
#endif
    delete saved_;
    G2FreeAll();
  }

  // g 档: 开了之后每个字节读一次 state->live_, 某条 pattern 由活转死就把它当前挂着的
  // 那一段游程整块收口成一个【分量】, 挂进待取列表 (语义见 span_scan.h)。
  bool Begin(int textlen) { return Begin(textlen, false); }
  bool Begin(int textlen, bool gspan) {
    if (textlen < 0)
      return false;
#ifdef G2_PEAKDUMP
    G2PeakDump();
#endif
    // g 档 (存活位切分量 + 游程留 native) 只在正向 set 上有意义 —— 反向 set 一律关掉。
    g_span_ = run_forward_ && gspan;
    for (int i = 0; i < nid_; i++)
      ghit_[i] = 0;
    for (int w = 0; w < DFA::kGW; w++) {
      glive_[w] = 0;
      gpend_[w] = 0;
    }
    gpendw_ = 0;
    G2Recycle();
    for (int i = 0; i < nid_; i++) {
      if (g2_[i].p != NULL) {
        g2live_ -= g2_[i].cap * 4;
        g2open_ -= g2_[i].cap * 4;
        G2Heap(-static_cast<long long>(g2_[i].cap) * 4);
        free(g2_[i].p);
        g2_[i].p = NULL;
        g2_[i].cap = 0;
        g2_[i].n = 0;
      }
      g2_[i].lo = 0;
    }
    g2peak_ = 0;
    g2openpeak_ = 0;
    g2used_ = 0;
    g2usedpeak_ = 0;
    g2nopen_ = 0;
    g2nopenpeak_ = 0;
    g2heappeak_ = g2heap_;   // 水位不清零, 只把高水位拉回当前值
    g2ngrow_ = 0;
    g2alloc_ = 0;
    g2nseg_ = 0;
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
  template <bool run_forward, bool gspan>
  inline void Note(int32_t id, int32_t pos) {
    if (gspan) {
      G2Note(id, pos);
      return;
    }
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

  // GDeaths: died 里每一位对应一条(折叠后可能是几条) pattern 由活转死。
  // 把它当前挂着的游程收口, 再吐一条 (id, -1, pos) 告诉调用方"这一分量到此为止"。
  // 🔴 noinline: 断气是罕见事件, 让它被 inline 进热循环只会把循环体撑大、赶跑 I-cache。
  //    (实测: 不加这一条, 零命中的第一趟从 0.41ms 掉到 0.65ms —— 纯粹是代码膨胀的钱。)
  //    dmask 的第 w 位 = died[w] 非零 (热循环只填了这些字, 其余是脏的, 不许读)。
  __attribute__((noinline)) void GDeaths(const uint64_t* died, uint32_t dmask, int32_t pos) {
    while (dmask != 0) {
      int w = __builtin_ctz(dmask);
      dmask &= dmask - 1;
      uint64_t d = died[w];
      while (d != 0) {
        int b = __builtin_ctzll(d);
        d &= d - 1;
        // 折叠时 (kGW=1 且 npat>64) 一位对应好几条 pattern, 所以要按 kGBits 跨步遍历。
        for (int id = w * 64 + b; id < nid_; id += DFA::kGBits)
          G2Close(id, pos);
        gpend_[w] &= ~(uint64_t{1} << b);
      }
      if (gpend_[w] == 0)
        gpendw_ &= ~(uint32_t{1} << w);
    }
  }

  // ── g2 (游程档) ───────────────────────────────────────────────────────────
  // 与 g1 的差别 (语义完全一样, 都是 rightmost-longest 的分量切分):
  //   ① 一条 pattern 没命中过就【完全不管】存活位 (gpend_ == 0 时热循环里只判一次零);
  //   ② 结束位置的游程留在 native 侧, 每条 pattern 一块, 从 8 个 int32 (= 4 条) 起二倍扩;
  //   ③ 分量收口时把整块游程数组交给调用方 —— 命中【不逐条过桥】, 一个分量交一次。
  // (位图版试过了: 内存和 CPU 都更差, 因为结束位置天生连号, 游程本来就是它的最优压缩。
  //  换算与实测见 doc/plan12/20260831_219re2scanFast.txt。)
  inline void GMark(int32_t id) {
    int b = id % DFA::kGBits;
    int w = b >> 6;
    uint64_t bit = uint64_t{1} << (b & 63);
    gpend_[w] |= bit;
    gpendw_ |= uint32_t{1} << w;
    // 新挂上的位先当它"上一个字节还活着": 这个字没挂东西的期间 glive_[w] 是不维护的,
    // 不补这一下, 紧跟着的那个字节就判不出"这条已经死了", 分量会白白拖长。
    // (按字懒维护也靠这一行 —— gpendw_ 的第 w 位是 0 时 glive_[w] 一律是脏的。)
    glive_[w] |= bit;
  }

  // 内存账口径 (字节): g2open_ = 逐条【还开着】的分量占用 (与 g1 的 Go 侧峰值同口径);
  //                    g2live_ = 再加上"已收口但调用方还没取走"的那批; 都不含回收池。
  inline void G2Acct(int32_t ncap) {
    g2live_ += ncap * 4;
    g2open_ += ncap * 4;
    if (g2live_ > g2peak_)
      g2peak_ = g2live_;
    if (g2open_ > g2openpeak_)
      g2openpeak_ = g2open_;
  }

  // G2Take: 给 id 这条 pattern 的新分量要一块游程数组。
  //
  // 🔴 回收池必须【按大小分档】, 不能是一个全局 LIFO。三版都试过, 数字在 readme:
  //   ① 全局 LIFO: pop 出来的袋子是"历史最大分量"那个级别, 原样发给
  //      只装一条游程的分量 ⇒ 开着的容量收敛到 (同时开着的分量数 × 全局最大袋)。
  //      body 表实测: 真正装着 325.8KB, 真实堆 2128.2KB。
  //   ② 逐条 pattern 一个槽: 内存降到 588KB, 但一个 pattern 在同一批里能开关好几次分量,
  //      槽空了就得 malloc ⇒ 14.5 万次 malloc/free, 第一趟从 4.4ms 拖到 8.4ms。不要。
  //   ③ 按 cap 分档的空闲链 (本版): 拿的时候按【这条 pattern 的容量高水位】取对应档,
  //      所以小分量拿小袋子、大分量拿大袋子; 档内是 LIFO, 零 malloc。两头都拿到。
  __attribute__((noinline)) int32_t* G2Take(int32_t id, int32_t* cap) {
    int32_t want = g2want_[id];
    if (want < 8)
      want = 8;
    std::vector<G2Buf>& bk = g2bucket_[G2Bucket(want)];
    if (!bk.empty()) {
      G2Buf b = bk.back();
      bk.pop_back();
      g2pool_ -= b.cap;
      *cap = b.cap;
      G2Acct(b.cap);
      return b.p;
    }
    // 档里空着 —— 按这条 pattern 的容量高水位一次开够, 不从 8 起二倍扩。
    int32_t want2 = g2want_[id];
    if (want2 < 8)
      want2 = 8;
    int32_t* p = static_cast<int32_t*>(malloc(static_cast<size_t>(want2) * sizeof(int32_t)));
    *cap = want2;
    g2alloc_ += want2 * 4;
    G2Heap(static_cast<long long>(want2) * 4);
    G2Acct(want2);
    return p;
  }

  // G2Bucket: cap 恒为 8*2^k, 直接拿最低位的位置当档号。
  static inline int G2Bucket(int32_t cap) {
    int b = 0;
    while ((1 << b) < cap && b < 30)
      b++;
    return b;
  }

  // G2Note 与 g1 的 Note 同一套收敛规则: 与上一条游程连号就接长, 否则开一条新的。
  inline void G2Note(int32_t id, int32_t pos) {
    ghit_[id] = 1;
    if (gbool_[id])   // 这条只要"有没有命中" ⇒ 不攒游程, 也不去盯它的存活位
      return;
    G2St& g = g2_[id];
    GMark(id);
    if (g.p == NULL) {                       // 开新分量
      g.p = G2Take(id, &g.cap);
      if (g.p == NULL)
        return;
      g.p[0] = pos;
      g.p[1] = pos;
      g.n = 1;
      G2Used(8);
      g2nopen_++;
      if (g2nopen_ > g2nopenpeak_)
        g2nopenpeak_ = g2nopen_;
      if (!pend_[id]) {
        pend_[id] = 1;
        pendlist_.push_back(id);
      }
      return;
    }
    int32_t* last = g.p + 2 * g.n - 1;
    if (pos == *last + 1) {                  // 连号, 接长
      *last = pos;
      return;
    }
    if (2 * (g.n + 1) > g.cap)               // 二倍扩
      G2Grow(g);
    g.p[2 * g.n] = pos;
    g.p[2 * g.n + 1] = pos;
    g.n++;
    G2Used(8);
  }

  // G2Used: 只算【真正装着结束位置】的字节 (n 条游程 = 8n)。
  // 与 g2open_ 的差别就是"多申请没用上的那部分" —— 二倍扩的尾巴 + 回收池发下来的大袋子。
  // G2Heap: 这个 scanner 手上真实持有的堆字节 (malloc 加、free 减)。
  // 跨 scan 不清零 —— 回收池里的袋子是跨 scan 活着的, 清零就成了自欺欺人。
  inline void G2Heap(long long d) {
    g2heap_ += d;
    if (g2heap_ > g2heappeak_)
      g2heappeak_ = g2heap_;
  }

  inline void G2Used(int32_t d) {
    g2used_ += d;
    if (g2used_ > g2usedpeak_)
      g2usedpeak_ = g2used_;
#ifdef G2_PEAKDUMP
    // 峰值现场取样: 只在【历史最高水位】又被顶上去 4KB 时才扫一遍开着的分量,
    // 所以最后留下的快照离真峰值不超过 4KB。默认档不编这段。
    if (g2used_ > g2snapused_ + 4096) {
      g2snapused_ = g2used_;
      g2snap_.clear();
      for (int i = 0; i < nid_; i++) {
        G2St& g = g2_[i];
        if (g.p == NULL)
          continue;
        g2snap_.push_back(i);
        g2snap_.push_back(g.n);
        for (int k = 0; k < 2 * g.n; k++)
          g2snap_.push_back(g.p[k]);
      }
    }
#endif
  }

#ifdef G2_PEAKDUMP
  // 把峰值现场原样打到 stderr, 一行一个分量: id 游程数 首lo 末hi 然后是每条游程的
  // "长度:到下一条的空档"。给上层拿去统计到底能不能压。
  void G2PeakDump() {
    if (g2snap_.empty())
      return;
    fprintf(stderr, "#PEAKDUMP used=%lld\n", g2snapused_);
    size_t i = 0;
    while (i < g2snap_.size()) {
      int32_t id = g2snap_[i++];
      int32_t n = g2snap_[i++];
      fprintf(stderr, "C %d %d", id, n);
      for (int k = 0; k < n; k++) {
        int32_t lo = g2snap_[i + 2 * k];
        int32_t hi = g2snap_[i + 2 * k + 1];
        int32_t gap = (k + 1 < n) ? (g2snap_[i + 2 * k + 2] - hi - 1) : -1;
        fprintf(stderr, " %d:%d", hi - lo + 1, gap);
      }
      fprintf(stderr, "\n");
      i += 2 * n;
    }
    g2snap_.clear();   // 水位 g2snapused_ 【不】清 —— 只有更高的峰值才会再取样、再打一次
  }
#endif

  __attribute__((noinline)) void G2Grow(G2St& g) {
    int32_t nc = g.cap * 2;
    int32_t* np = static_cast<int32_t*>(realloc(g.p, static_cast<size_t>(nc) * sizeof(int32_t)));
    if (np == NULL)
      return;
    g2alloc_ += (nc - g.cap) * 4;
    G2Heap((nc - g.cap) * 4);
    G2Acct(nc - g.cap);
    g2ngrow_++;
    g.p = np;
    g.cap = nc;
  }

  // G2Close: 把这条 pattern 当前分量的游程数组整块交出去 (所有权转给 g2closed_),
  // 并把这次断气的位置记成【下一个分量的左界】。
  void G2Close(int32_t id, int32_t death) {
    G2St& g = g2_[id];
    if (g.p != NULL) {
      G2Rec r;
      r.id = id;
      r.lo = g.lo;
      r.nrun = g.n;
      r.runs = g.p;
      g2closed_.push_back(r);
      g2closedcap_.push_back(g.cap);   // 所有权转给 closed 列表, g2live_ 不变
      g2used_ -= static_cast<long long>(g.n) * 8;
      g2nopen_--;
      if (g.cap > g2want_[id])
        g2want_[id] = g.cap;   // 这条 pattern 的分量能有多大, 下次一次开够
      g2nseg_++;
      g2open_ -= g.cap * 4;
      g.p = NULL;
      g.cap = 0;
      g.n = 0;
    }
    if (death >= 0) {
      // 界就是 death 本身, 不能再 +1: 偏移 death 处没有存活线程, 说明【起点 < death】的
      // 匹配全断了 —— 但起点【等于】death 的匹配是这个字节新种下的 (fresh, 不算进存活位),
      // 它完全可以活下去。+1 会把这种匹配漏掉。用 -DGLO_PLUS1 编一份可以看到对拍变红。
#ifdef GLO_PLUS1
      g.lo = death + 1;
#else
      g.lo = death;
#endif
    }
  }

  // G2Recycle: 上一批交出去的游程数组, 调用方已经读完了, 收回回收池。
  // (位图版这里还得 memset 清零; 游程是按下标覆写的, 不用清 —— 白省一笔。)
  void G2Recycle() {
    for (size_t i = 0; i < g2closed_.size(); i++) {
      int32_t* p = const_cast<int32_t*>(g2closed_[i].runs);
      if (p == NULL)
        continue;
      G2Buf b;
      b.p = p;
      b.cap = g2closedcap_[i];
      g2live_ -= b.cap * 4;
      g2pool_ += b.cap;
      g2bucket_[G2Bucket(b.cap)].push_back(b);
    }
    g2closed_.clear();
    g2closedcap_.clear();
  }

  void G2FreeAll() {
    G2Recycle();
    for (size_t b = 0; b < g2bucket_.size(); b++) {
      for (size_t i = 0; i < g2bucket_[b].size(); i++) {
        G2Heap(-static_cast<long long>(g2bucket_[b][i].cap) * 4);
        free(g2bucket_[b][i].p);
      }
      g2bucket_[b].clear();
    }
    g2pool_ = 0;
    for (size_t i = 0; i < g2_.size(); i++) {
      if (g2_[i].p != NULL) {
        G2Heap(-static_cast<long long>(g2_[i].cap) * 4);
        free(g2_[i].p);
      }
      g2_[i].p = NULL;
    }
  }

  // NoteState 把一个 match 状态里的所有 pattern id 都记一遍 (布局见 SpanDFA::HasId)。
  template <bool run_forward, bool gspan>
  inline void NoteState(DFA::State* s, int32_t pos) {
    for (int i = s->ninst_ - 1; i >= 0; i--) {
      int id = s->inst_[i];
      if (id == MatchSep)
        break;
      Note<run_forward, gspan>(static_cast<int32_t>(id), pos);
    }
  }

  // gspan  = 开 g 档 (存活位切分量); gwatch = 当下【真的有 pattern 挂着】, 要逐字节读存活位。
  // 两个循环轮流跑: 空闲档 (gspan && !gwatch) 一个字节都不读存活位, 第一次命中时 return 2
  // 换到盯着档; 挂着的都断气之后再 return 2 换回来。Step 里那个 for(;;) 就是干这个的。
  template <bool run_forward, bool gspan, bool gwatch>
  int RunLoop(const uint8_t* bp, int32_t* dummy_unused);

  // GLiveStep: 读一个字节的存活位, 把由活转死的那几条 (可能是几条, 折叠时一位对好几条)
  // 收口。前提: gpendw_ != 0。返回 0=接着走 · 1=收口攒够了要挂起 · 2=没有挂着的了。
  inline int GLiveStep(DFA::State* s, int32_t pos) {
    // 状态的 inst_ 描述的是【吃完这个字节之后】的活线程, 也就是偏移 pos 处的存活位;
    // 而 IsMatch 说的是 pos-1 处结束的匹配 —— 所以死亡事件排在命中之后。
    // 🔴 不按 GW 收费: 只过"有挂着 pattern"的那几个字。挂着一两条 (常态) 就只读一个字
    //    —— 所以 GLIVE_WORDS 调到 4 (256 条不折叠) 与折叠版同价。
    uint64_t died[DFA::kGW];
    uint32_t dmask = 0;
    uint32_t wm = gpendw_;
    do {
      int w = __builtin_ctz(wm);
      wm &= wm - 1;
      uint64_t nl = s->live_[w];
      uint64_t d = glive_[w] & ~nl & gpend_[w];
      glive_[w] = nl;
      if (d != 0) {
        died[w] = d;
        dmask |= uint32_t{1} << w;
      }
    } while (wm != 0);
    if (dmask == 0)
      return 0;
    s_ = s;
    GDeaths(died, dmask, pos);
    if (static_cast<int>(g2closed_.size()) >= g2batch_)
      return 1;
    return gpendw_ == 0 ? 2 : 0;
  }

  // 起点/正文结束那两个"匹配晚一个字节"的特例, 三档共用的分派。
  inline void NoteStateDyn(DFA::State* s, int32_t pos) {
    if (!run_forward_)
      NoteState<false, false>(s, pos);
    else if (g_span_)
      NoteState<true, true>(s, pos);
    else
      NoteState<true, false>(s, pos);
  }

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

  // ── g 档 (存活位切分量) ──
  bool g_span_ = false;             // 开着 = 存活位切分量 + 游程留 native, 分量整块交付
  uint64_t glive_[DFA::kGW];        // 上一个字节的存活位 (只有 gpendw_ 置位的那些字有效)
  uint64_t gpend_[DFA::kGW];        // 当前"有未收口命中"的 pattern (npat>kGBits 才折叠)
  uint32_t gpendw_ = 0;             // gpend_ 里哪几个字非零 —— 热循环只碰这几个字
  static_assert(DFA::kGW <= 32, "gpendw_ 是 uint32, GLIVE_WORDS 不能超过 32 (= 2048 条)");
  std::vector<uint8_t> gbool_;      // 逐条: 1 = 只要"有没有命中", 不攒游程 (调用方声明的)
  std::vector<uint8_t> ghit_;       // 逐条: 1 = 这一遍命中过 (boolOnly 那几条唯一的产物)

  // ── g2 游程档 ──
  struct G2Buf { int32_t* p; int32_t cap; };   // cap 单位 int32
  std::vector<G2St> g2_;
  std::vector<G2Rec> g2closed_;     // 本批已经收口、等调用方取走的分量
  std::vector<int32_t> g2closedcap_;
  std::vector<std::vector<G2Buf> > g2bucket_;  // 按 cap 分档的空闲链 (默认走这个, 见 G2Take)
  std::vector<int32_t> g2want_;     // 逐条 pattern 的容量高水位: 槽空时按它一次开够
  int g2batch_ = 1024;               // 攒够这么多条收口就挂起, 让调用方消化
 public:
  int G2Closed(const G2Rec** recs) {
    *recs = g2closed_.empty() ? NULL : &g2closed_[0];
    return static_cast<int>(g2closed_.size());
  }

  // SetBoolOnly: 这条 pattern 只要"有没有命中"。建工作区之后 Begin 之前调, 一次就够
  // (跨 scan 保留)。挡掉的是这一层真花钱的那步 —— 攒游程 + 盯存活位 + 收口 + 补起点,
  // 不只是少交几处结果。门上只当短路 bool 用的位一律配这个。
  void SetBoolOnly(int id) {
    if (id >= 0 && id < nid_)
      gbool_[id] = 1;
  }
  // Hits: nid 个字节, 第 i 个非零 = 第 i 条这一遍命中过。Begin 时清零。
  const uint8_t* Hits() const { return ghit_.empty() ? NULL : &ghit_[0]; }

  long long g2alloc_ = 0;           // 统计: 二倍扩容累计申请的字节
  long long g2ngrow_ = 0;           // 统计: 二倍扩容次数
  long long g2live_ = 0;            // 统计: 当前正在用的游程数组字节
  long long g2peak_ = 0;            // 统计: 峰值 (这就是 g2 相对 g1 要比的那个数)
  long long g2pool_ = 0;            // 统计: 回收池里躺着的字节
  long long g2used_ = 0;            // 统计: 开着的分量里【真正装着数据】的字节 (8 * 游程条数)
  long long g2usedpeak_ = 0;        // 统计: 上面那个的峰值 —— 和 g1 的 maxPend*8 才是同一把尺
  long long g2nopen_ = 0;           // 统计: 当前同时开着几个分量 (最多 nid 个)
  long long g2nopenpeak_ = 0;       // 统计: 上面那个的峰值
  long long g2heap_ = 0;            // 统计: 真实持有的堆字节 (含回收池, 跨 scan 累计)
  long long g2heappeak_ = 0;        // 统计: 本遍的真实堆高水位
  long long g2nseg_ = 0;            // 统计: 收口的分量条数
  long long g2open_ = 0;            // 统计: 还开着的分量游程字节 (与 g1 的 Go 侧峰值同口径)
  long long g2openpeak_ = 0;
#ifdef G2_PEAKDUMP
  long long g2snapused_ = -1;       // 已取样快照对应的 used 水位 (跨 scan 不清零 = 全局最高那次)
  std::vector<int32_t> g2snap_;     // 峰值现场: [id, n, lo0,hi0, lo1,hi1, ...] 逐个分量接在一起
#endif
 private:

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
template <bool run_forward, bool gspan, bool gwatch>
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
      NoteState<run_forward, gspan>(s, static_cast<int32_t>(m - bp));
      if (gspan && !gwatch && gpendw_ != 0) {
        // 【第一条 pattern 挂上来了】。本字节的存活位要在这儿读掉 (语义与盯着那一档逐字
        // 相同 —— GMark 刚把这一位当成"上个字节还活着", 死亡就发生在本字节上), 读完换档。
        int gr = GLiveStep(s, static_cast<int32_t>(p - bp));
        off_ = static_cast<int>(p - bp);
        s_ = s;
        if (gr == 1) {
          Suspend();
          return 0;
        }
        return 2;   // 换循环: Step 会按 gpendw_ 重新选一次档
      }
    }
    if (gwatch) {
      // 盯着档: 每个字节读一次存活位。注意【空闲档一个字节都不读】—— 一条 pattern 都还
      // 没挂上的时候走的是另一份循环 (见上面那个 return 2), 那份与不开 g 档的循环逐条
      // 指令相同。这是"零命中的正文上等于没开 g 档"的真正做法: 换的是整个循环,
      // 不是每个字节判一次 gpendw_。
      // 🔴 为什么值得这么做: 每字节多一次 this->gpendw_ 的 load + 一个分支, 实测在这台机
      //    上能让零命中 64KiB 从 97us 变成 198us (整整两倍, 而且换个代码布局又会变回
      //    101us —— 这条循环对布局极其敏感, 详见 doc)。索性让它一个字节都不读。
      int gr = GLiveStep(s, static_cast<int32_t>(p - bp));
      if (gr != 0) {
        off_ = static_cast<int>(p - bp);
        s_ = s;
        if (gr == 1) {   // 收口攒够了, 交给调用方消化
          Suspend();
          return 0;
        }
        return 2;        // 挂着的 pattern 全断气了, 换回空闲档
      }
    }
    if (!gspan && n_ > limit_) {
      off_ = static_cast<int>(p - bp);
      s_ = s;
      Suspend();
      return 0;
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
  // g 档一个字节都不往 out 写 (游程留 native, 分量整块交), 所以只有非 g 档才要这个下界。
  if (!g_span_ && outcap < 3 * nid_)
    return -1;

  if (g_span_)
    G2Recycle();   // 上一批交出去的游程数组, 调用方已经读完了

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
    if (s_->IsMatch())
      NoteStateDyn(s_, static_cast<int32_t>(off_));
    // (起点这里不用给 glive_ 播种: gpendw_ 还是 0, 没有任何字在被盯着;
    //  第一次命中时 GMark 会把那一位连同 glive_ 一起补上。)
    phase_ = kPhaseLoop;
  } else if (!Resume()) {
    phase_ = kPhaseDone;
    lock_ = NULL;
    return -1;
  }

  if (phase_ == kPhaseLoop) {
    int r;
    for (;;) {   // r == 2 = 空闲档/盯着档互换, 换完接着跑同一遍正文
      if (!run_forward_)
        r = RunLoop<false, false, false>(bp, NULL);
      else if (!g_span_)
        r = RunLoop<true, false, false>(bp, NULL);
      else if (gpendw_ != 0)
        r = RunLoop<true, true, true>(bp, NULL);
      else
        r = RunLoop<true, true, false>(bp, NULL);
      if (r != 2)
        break;
    }
    if (r < 0) {
      phase_ = kPhaseDone;
      lock_ = NULL;
      return -1;
    }
    if (r == 0) {   // 本批满了 (g2: 收口攒够了), 挂起
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
      NoteStateDyn(s_, static_cast<int32_t>(off_));
    }
    s_ = NULL;
    phase_ = kPhaseFlush;
  }

  // 收口: 把还挂着的游程吐出去。一条 pattern 最多挂一段, 所以最多 nid 条,
  // 但本批不一定装得下 (前面已经写了东西), 装不下就挂起 —— 这一段不需要 DFA 状态,
  // 挂起时也就没什么要存的。
  if (g_span_) {
    // g 档收口: 正文扫完还开着的分量, 整块交出去 (death = -1 ⇒ 不动左界)。
    while (flushi_ < pendlist_.size()) {
      int32_t id = pendlist_[flushi_++];
      G2Close(id, -1);
      pend_[id] = 0;
      if (static_cast<int>(g2closed_.size()) >= g2batch_ && flushi_ < pendlist_.size()) {
        s_ = NULL;
        *more = 1;
        lock_ = NULL;
        return 0;
      }
    }
    pendlist_.clear();
    flushi_ = 0;
    phase_ = kPhaseDone;
    lock_ = NULL;
    return 0;
  }

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

// ── 可行前缀回推 (SpanDFA::ViableStart / ViableStarts) ─────────────────────
//
// 回答的问题: 给一个匹配【右端】e, 在 [bound, e) 里, 哪些位置 s 满足
//   "text[s, e) 是第 id 条 pattern 的一个【可行前缀】" —— 即存在某个 w 使
//   text[s, e) + w 能被这条 pattern 匹配。
//
// 🔴 与 Resolve 的差别只有一处, 但是决定性的: Resolve 的反向机器只种【accept】, 所以它
//    回答的是"哪些 s 使 text[s,e) 【正好】是一个匹配"; 这里种的是【全部指令】, 回答的是
//    "哪些 s 起头的匹配【路过】了 e"。后者是前者的超集, 而且正是"leftmost 到底在哪"
//    这个问题唯一问得对的形状 ——
//      \b(?:ab cd ef|cd)\b 撞 "ab cd ef": 门给的最小右端是 "cd" 那处的右端,
//      只种 accept 只能回推到 "cd" 的左端; 种全部状态才能回推到 "ab" 那个真正的最左起点
//      (text["ab cd ef" 的 0 : "cd" 右端) 是可行前缀, 但不是匹配)。
//
// 🔴 为什么"种全部指令"就等于可行前缀 (证明):
//    反向 set 的程序 R 认的是 reverse(L) —— 它不是"把正向程序的箭头掉个头", 而是从
//    倒过来的 pattern 树重新编的一台机器 (CompileSet 的 reversed_), 但认的语言就是 reverse(L)。
//    这一趟从 e 往左吃字节, 吃进去的串正好是 reverse(text[s,e)); 种子是【R 的全部活状态】,
//    终点是 R 的 accept。于是:
//      走到 accept ⟺ 存在某个活状态 q, 从 q 出发吃 reverse(text[s,e)) 能到 accept
//                  ⟺ reverse(text[s,e)) 是 reverse(L) 里某个词的【后缀】
//                  ⟺ text[s,e) 是 L 里某个词的【前缀】 = 可行前缀。∎
//    (两个方向都要活状态: 只有从 start 可达的状态才配当种子 —— 见下面那道"可达闭包"的闸;
//     而到不了 accept 的状态种上也白种, 它一个候选都变不出来。)
//
// 出参 out 里的候选起点是【降序】的 (机器从右往左走, 先看见的位置更大)。调用方要 leftmost
// 就倒着遍历。返回值是【找到的总条数】, 可能 > outcap —— 此时只写了前 outcap 个 (也就是
// 最大的那几个, 恰好是最没用的那几个), 调用方该换个更大的缓冲重来一次。
//
// 代价: 每步吃掉一个字节, 位置严格递减, 所以最多 (from - bound) 步; 而常态是机器自己先死
// (可行前缀集合空了) —— 与 Resolve 一样, 与正文长度无关, 只与"这处命中能往回够多远"有关。

DFA::State* SpanDFA::ViableStart(DFA* dfa, int base, uint32_t flags) {
  DFA::StartInfo* info = &dfa->start_[base | DFA::kStartViable];
  DFA::State* s = info->start.load(std::memory_order_acquire);
  if (s != NULL)
    return s;
  MutexLock l(&dfa->mutex_);
  s = info->start.load(std::memory_order_relaxed);
  if (s != NULL)
    return s;
  // 🔴 这里是与 AnalyzeSearchHelper 唯一的差别: 那边只 AddToQueue 一个入口指令
  // (start 或 start_unanchored), 这里把【锚定入口能到达的每一条指令】都塞进去。
  //
  // 🔴 是"锚定入口的可达闭包", 不是图省事的 "1..size 全塞"。set 程序里除了锚定入口
  //    还挂着一截 `.*?` 非锚定前缀 (start_unanchored 那条路), 那截也在 1..size 里 ——
  //    种上它机器就【永远死不掉】, 而且"走到 accept"回答的会变成另一个问题
  //    ("存在某个 s' >= s 使 text[s',from) 是可行前缀"), 那不是调用方要的东西。
  //    实测: `b` 撞 "abc" 问 from=3, 全塞会多报一个 1 出来 —— text[1:3)="bc" 根本
  //    不是 `b` 的前缀。只种锚定闭包就没有这一路。
  //
  // 可达闭包【就是】那个自动机的全部状态, 所以这一步不多不少: 走不到的指令连 Match
  // 都够不着, 种上它一个候选也多不出来。
  int n = dfa->prog_->size();
  std::vector<bool> seen(n, false);
  std::vector<int> stk;
  stk.push_back(dfa->prog_->start());
  while (!stk.empty()) {
    int id = stk.back();
    stk.pop_back();
    if (id <= 0 || id >= n || seen[id])
      continue;
    seen[id] = true;
    Prog::Inst* ip = dfa->prog_->inst(id);
    if (!ip->last())
      stk.push_back(id + 1);   // 同一条 list 里的下一支 (交替的另一条路)
    switch (ip->opcode()) {
      case kInstAlt:
      case kInstAltMatch:
        stk.push_back(ip->out1());
        stk.push_back(ip->out());
        break;
      case kInstByteRange:
      case kInstCapture:
      case kInstNop:
      case kInstEmptyWidth:
        stk.push_back(ip->out());
        break;
      default:   // kInstMatch / kInstFail: 到头了
        break;
    }
  }
  dfa->q0_->clear();
  for (int id = 1; id < n; id++) {
    if (seen[id])
      dfa->AddToQueue(dfa->q0_, id, flags);
  }
  s = dfa->WorkqToCachedState(dfa->q0_, NULL, flags);
  if (s == NULL)
    return NULL;   // 预算不够 —— 调用方 ResetCache 之后再试一次
  info->start.store(s, std::memory_order_release);
  return s;
}

int SpanDFA::ViableStarts(DFA* dfa, const char* text, int textlen,
                          int from, int bound, int id, int32_t* out, int outcap) {
  if (text == NULL || out == NULL || textlen < 0 || from < 0 || from > textlen ||
      id < 0 || outcap < 0)
    return -1;
  if (bound < 0)
    bound = 0;
  if (bound > from)
    bound = from;

  DFA::RWLocker l(&dfa->cache_mutex_);

  // 起始 flags 按【反向搜索】那一套取 —— 与 AnalyzeSearch 的 !run_forward 分支逐字一致:
  // 反着走的"起点"是区间的右端 text+from, 看的是它右边那个【真实邻居字节】,
  // 所以 \b / ^ / $ 判的是整篇正文里的真实处境, bound 只让答案变短不让它变错。
  int base;
  uint32_t flags;
  if (from == textlen) {
    base = DFA::kStartBeginText;
    flags = kEmptyBeginText | kEmptyBeginLine;
  } else if (text[from] == '\n') {
    base = DFA::kStartBeginLine;
    flags = kEmptyBeginLine;
  } else if (Prog::IsWordChar(text[from] & 0xFF)) {
    base = DFA::kStartAfterWordChar;
    flags = DFA::kFlagLastWord;
  } else {
    base = DFA::kStartAfterNonWordChar;
    flags = 0;
  }

  DFA::State* s = ViableStart(dfa, base, flags);
  if (s == NULL) {
    dfa->ResetCache(&l, NULL);
    s = ViableStart(dfa, base, flags);
    if (s == NULL)
      return -1;
  }
  if (s <= SpanSpecialMax())
    return 0;   // 只可能是 DeadState (kManyMatch 不出 FullMatchState, 见文件头)

  const uint8_t* bp = reinterpret_cast<const uint8_t*>(text);
  const uint8_t* p = bp + from;     // 反着走: p 从右往左
  const uint8_t* ep = bp + bound;
  const uint8_t* bytemap = dfa->prog_->bytemap();
  int n = 0;

  while (p != ep) {
    int c = *--p;
    DFA::State* ns = dfa->NextOf(s, bytemap[c]);
    if (ns == NULL) {
      if (!Advance(dfa, &l, &s, c, static_cast<int>(p - bp), textlen))
        return -1;
    } else {
      s = ns;
    }
    if (s <= SpanSpecialMax()) {
      // DeadState: 可行前缀集合空了 —— 再往左一个候选都不可能有。收工。
      s = NULL;
      break;
    }
    if (s->IsMatch()) {
      // 匹配晚一个字节才被看见 (与 Resolve 那一句同一个口径): 反向 p+1 = 候选起点。
      int32_t pos = static_cast<int32_t>(p + 1 - bp);
      if (pos < from && HasId(s, id)) {   // pos == from 是空的可行前缀, 没有意义
        if (n < outcap)
          out[n] = pos;
        n++;
      }
    }
  }

  if (s != NULL) {
    // 最后喂一个"边界符号", 把落在 bound 那一格上的候选冲出来。区间左端正好是正文开头
    // 才是 kByteEndText, 否则喂【真实的邻居字节】—— 同 Resolve 末尾那一段。
    int lastbyte = (ep == bp) ? DFA::kByteEndText : (ep[-1] & 0xFF);
    if (!Advance(dfa, &l, &s, lastbyte, static_cast<int>(p - bp), textlen))
      return -1;
    if (s > SpanSpecialMax() && s->IsMatch()) {
      int32_t pos = static_cast<int32_t>(p - bp);   // = bound
      if (pos < from && HasId(s, id)) {
        if (n < outcap)
          out[n] = pos;
        n++;
      }
    }
  }
  return n;
}

// ── hgmLibre2 追加 ── 见上面 ViableStarts 的头注。只对【反向】程序有意义:
// 正向程序上"种全部状态往右走"回答的是另一个问题 (后缀), 不是调用方要的东西。
int Prog::SpanViableStarts(int nid, const char* text, int textlen,
                           int from, int bound, int id, int32_t* out, int outcap) {
  if (out == NULL || id < 0 || id >= nid)
    return -1;
  if (!reversed_)
    return -1;
  DFA* dfa = GetDFA(kManyMatch);
  if (dfa == NULL || !dfa->ok())
    return -1;
  return SpanDFA::ViableStarts(dfa, text, textlen, from, bound, id, out, outcap);
}

void DFASpanScanFree(DFASpanScan* ss) { delete ss; }

bool DFASpanScanBegin(DFASpanScan* ss, int textlen) {
  if (ss == NULL)
    return false;
  return ss->Begin(textlen);
}

int DFASpanScanG2Closed(DFASpanScan* ss, const DFASpanScanG2Rec** recs) {
  if (ss == NULL || recs == NULL)
    return 0;
  return ss->G2Closed(recs);
}

// heappeak = 这一遍为游程数组真实 malloc 出来的高水位 (扫描期间只申请不释放, 收口的
// 袋子进回收池等着被再发出去); usedpeak = 其中【真正装着结束位置】的那部分 (8 * 游程条数)。
void DFASpanScanG2Stats(DFASpanScan* ss, long long* usedpeak, long long* heappeak,
                        long long* poolbytes, long long* nseg) {
  if (ss == NULL)
    return;
  if (usedpeak != NULL) *usedpeak = ss->g2usedpeak_;
  if (heappeak != NULL) *heappeak = ss->g2heappeak_;
  if (poolbytes != NULL) *poolbytes = ss->g2pool_ * 4;
  if (nseg != NULL) *nseg = ss->g2nseg_;
}

bool DFASpanScanBeginG2(DFASpanScan* ss, int textlen) {
  if (ss == NULL)
    return false;
  return ss->Begin(textlen, true);
}

void DFASpanScanG2BoolOnly(DFASpanScan* ss, int id) {
  if (ss != NULL)
    ss->SetBoolOnly(id);
}

const uint8_t* DFASpanScanG2Hits(DFASpanScan* ss) {
  return ss == NULL ? NULL : ss->Hits();
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
