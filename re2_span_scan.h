// span_scan.h — ── hgmLibre2 追加 (非上游 re2) ──
// RE2::Set 的【流式游程扫描】: 一遍扫正文, 边扫边把"哪条 pattern 命中在哪"吐出来。
//
// 与 RE2::Set::Match 的分工:
//   Match      只回答"哪几条命中" (把命中 id 塞进 SparseSet 就完事), 不回答"在哪";
//              要位置的调用方只能拿到 id 之后, 再用那几条各自跑一遍【整篇正文】。
//   SpanScan   回答"命中在哪"。DFA 热循环里那个位置本来就算出来了 (lastmatch), 只是
//              被丢掉了 —— 这里把它收下来, 按 pattern 收敛成【游程】吐给调用方。
//
// ── 吐出来的是什么 (定死的语义) ──────────────────────────────────────────────
// kManyMatch 的 DFA 在【每一个】能结束匹配的位置都会进入 match 状态, 所以同一条 pattern
// 在一段可变长匹配上会连续产生一串位置 (例: `[a-z]{3,}` 撞上 "abcdef" 会在 3/4/5/6 各报一次)。
// 逐个吐出去既费带宽又没意义, 所以【按 pattern 把连号的位置收敛成一段游程】再吐:
//
//   正向 (run_forward): 位置 = 匹配【右端】的偏移 (不含, 即 text[..pos) 是一个匹配)。
//                       吐 (id, lo, hi) 表示: 这条 pattern 的右端落在 [lo, hi] 里的每一个值上。
//   反向 (!run_forward): 位置 = 匹配【左端】的偏移 (含, 即匹配从 text[pos] 开始)。
//                        吐 (id, lo, hi) 表示: 这条 pattern 的左端落在 [lo, hi] 里的每一个值上。
//
// 🔴 收敛【必须可逆】: 只吐游程的一端 (比如只留最右那个 end) 会把两个真实独立的匹配
//    悄悄并成一个 —— `ab|c` 撞上 "abc" 的两个 end 是 2 和 3, 连号, 只留 3 就把 [0,2) 弄丢了。
//    吐 (lo,hi) 两端就没有信息损失: 调用方要还原逐个位置, 展开 [lo,hi] 即可。
//
// 🔴 吐出的顺序【不保证全局按位置升序】。游程只有在"这条 pattern 再次命中且与上次不连号"
//    或"整篇扫完"时才收口, 所以不同 pattern 的游程会交错, 最后一批还会在扫完时集中吐出来。
//    需要有序的调用方自己排 (条数是游程数, 不是位置数, 排序成本可以忽略)。
//
// ── 为什么是"轮询 (step)"而不是"一次吐完"或"回调进 Go" ──────────────────────
//   一次吐完 : 游程条数没有上界, native 侧要么无界攒内存, 要么"buf 不够就扩容重跑"——
//              重跑要付的正是最贵的那一遍 (新正文现造 DFA 状态), 是性能炸弹。
//   回调进 Go: 回调期间还攥着 DFA 的 cache 读锁, 谁想 flush 谁就得等 Go 跑完, 拖垮并发。
//   轮询     : 攒满一批就【挂起】—— 用 StateSaver 按内容存下当前状态, 放掉读锁, 返回;
//              调用方取走这批再 Step 一次, 重新拿锁、按内容把状态查回来接着扫。
//              挂起期间一把锁都不持有, 也不保存任何调用方指针 (正文每次 Step 重新传)。
//
// 🔴 native 侧【不保存】正文指针: DFASpanScanStep 每次都要把同一份正文重新传进来
//    (只存偏移)。这是为了不违反 cgo 的"C 不得在调用返回后持有 Go 指针"。

#ifndef RE2_SPAN_SCAN_H_
#define RE2_SPAN_SCAN_H_

#include <stdint.h>

namespace re2 {

class Prog;

// DFASpanScan 是一次流式扫描的工作区 (含游程表 + 挂起点)。定义在 re2_dfa.cc 里 (要 DFA 内部)。
// 不是并发安全的: 一个工作区同时只能被一个线程用; 可以反复 Begin 复用 (游程表不重新分配)。
class DFASpanScan;

// 开/关工作区见 Prog::NewSpanScan (prog.h) —— 要拿 Prog 的 kManyMatch DFA, 所以挂在 Prog 上。
void DFASpanScanFree(DFASpanScan* ss);

// 开始一次新扫描 (清游程表, 绑定正文长度)。textlen < 0 返回 false。
bool DFASpanScanBegin(DFASpanScan* ss, int textlen);

// ── g2 档: 存活位切分量 + 游程留 native, 分量整块交付 (Re2Set_frel_t / Re2Set_fll_t 走的就是这条) ──
// 开了之后每个字节额外读一次状态的 per-pattern 存活位: 某条 pattern 由活转死, 说明
// "没有任何匹配能跨过这个位置", 于是它左右两侧的命中互不影响 —— 当场把它挂着的那一段
// 收口成一个【分量】。分量内部再按各自的口径结算 (那一步在 cre2_re2set.cpp)。
//
// 命中【不逐条过桥】: 每条 pattern 当前分量的结束位置游程攒在 native 侧 (每条一块,
// 8 个 int32 起二倍扩, 收口后进按大小分档的回收池), 分量收口时把整块挂进待取列表。
// 调用方每次 Step 返回后调 DFASpanScanG2Closed 取走这一批, 下一次 Step 会回收这些缓冲
// ⇒ 指针只在"本次 Step 返回 到 下次 Step 调用"之间有效。
// 🔴 g2 档下 Step 一个字节都不往 out 写 (out/outcap 完全没用上)。
bool DFASpanScanBeginG2(DFASpanScan* ss, int textlen);

// 这条 pattern 只要"有没有命中" —— 不攒游程、不盯存活位、不收口。Begin 之前调, 跨 scan 保留
// (所以 on=0 要能关: 调用方每遍传的名单不一样, 不关就把上一遍的名单粘到这一遍)。
void DFASpanScanG2BoolOnly(DFASpanScan* ss, int id, int on);
// nid 个字节, 第 i 个非零 = 第 i 条这一遍命中过 (每次 BeginG2 清零)。
const uint8_t* DFASpanScanG2Hits(DFASpanScan* ss);

struct DFASpanScanG2Rec {
  int32_t id;             // 哪条 pattern
  int32_t lo;             // 本分量左界: 上一次这条 pattern 断气的位置 (反向锚定的 bound)
  int32_t nrun;           // 游程条数
  int32_t pad_;
  const int32_t* runs;    // runs[2k], runs[2k+1] = 第 k 条游程的 lo,hi (升序, 互不相接)
};
int DFASpanScanG2Closed(DFASpanScan* ss, const DFASpanScanG2Rec** recs);

// g2 的内存账 (字节; nseg 是条数)。工作区就活一遍扫描, 所以这几个数就是这一遍的全部。
void DFASpanScanG2Stats(DFASpanScan* ss, long long* usedpeak, long long* heappeak,
                        long long* nseg);

// 推进一步。text/textlen 必须与 Begin 时的长度一致, 且每次传同一份正文。
// out 写入 (id, lo, hi) 三元组, outcap 是 int32 个数, 必须 >= 3*nid。
// 返回本批写进去的【游程条数】(>=0); <0 = 出错 (DFA 放弃 / 状态恢复失败, 整次扫描作废)。
// *more: 1 = 还没扫完, 取走这批之后要再 Step; 0 = 扫完了。
int DFASpanScanStep(DFASpanScan* ss, const char* text, int textlen,
                    int32_t* out, int outcap, int* more);

// ── 另一半: 锚定解析 (Prog::SpanResolve / RE2::Set::ResolveSpan) ─────────────
//
// SpanScan 吐的是【一端】: 正向 set 吐右端, 反向 set 吐左端。调用方要知道命中到底覆盖了
// 哪一段, 就得再跑一次正则求另一端 —— 而这一次【必须是锚定的】: 非锚定的 .*? 前缀让每个
// 位置都能当起点, 状态数对计数上界指数增长 (doc/状态数为什么会相乘.md 里同一条 pattern
// 加个 \b 就是 967 倍), 等于把刚省下来的又赔回去。
//
// 🔴 这一步【调用方自己补不出来】(补出来的也是另一样东西): set 程序里那截 .*? 前缀是编进
//    程序的, 从外面进不去"不带前缀的入口" (Prog::start_setanchored 就是为这个留的)。
//    调用方能做的只有另编一条 \A(?:pat) —— 每条 pattern 一个 RE2 对象、一份独立 DFA 缓存,
//    还得手工保证它与 set 里那条语义一致。SpanResolve 用的是 set 自己那份程序和那份缓存。
//
// 语义 (方向跟着 set 的编译方向, 与 SpanScan 同一个口径):
//   正向 set: from = 匹配左端 (含), 返回右端 (不含) —— text[from, *out) 是该 pattern 的匹配;
//   反向 set: from = 匹配右端 (不含), 返回左端 (含) —— text[*out, from) 是该 pattern 的匹配。
//
// 🔴 返回的是【最长】的那个匹配的另一端, 不是碰到的第一个。"碰到第一个 match 状态就收工"
//    给的是最短匹配, 对变长 pattern 等于把命中截断 —— 不是任何调用方要的东西。
//    走到死状态的成本 = 这条命中实际能延伸到多远, 与正文长度无关; 真能无限延伸的 pattern
//    ((?s).*KEY 那种) 用 bound 掐住。

}  // namespace re2

#endif  // RE2_SPAN_SCAN_H_
