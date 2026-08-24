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

// 推进一步。text/textlen 必须与 Begin 时的长度一致, 且每次传同一份正文。
// out 写入 (id, lo, hi) 三元组, outcap 是 int32 个数, 必须 >= 3*nid。
// 返回本批写进去的【游程条数】(>=0); <0 = 出错 (DFA 放弃 / 状态恢复失败, 整次扫描作废)。
// *more: 1 = 还没扫完, 取走这批之后要再 Step; 0 = 扫完了。
int DFASpanScanStep(DFASpanScan* ss, const char* text, int textlen,
                    int32_t* out, int outcap, int* more);

}  // namespace re2

#endif  // RE2_SPAN_SCAN_H_
