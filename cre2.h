/* cre2.h — 极小 C 包装, 把 C++ 的 RE2 暴露成 C ABI 给 cgo 用.
 * 只暴露 Go 侧真正需要的: 编译 + 非锚定(unanchored)匹配 + 捕获组.
 * 不依赖 abseil; 配套的 RE2 源码是 abseil 之前的 2023-03-01 版 (纯 C++11), vendored 在本目录. */
#ifndef RE2NATIVE_CRE2_H
#define RE2NATIVE_CRE2_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct cre2_re cre2_re;

/* 编译 pattern (pat,patlen). 永不返回 NULL(分配失败才 NULL); 编译错误用 cre2_ok 检测.
 * 内存预算 = RE2 默认 kDefaultMaxMem (8MB). */
cre2_re *cre2_new(const char *pat, int patlen);
/* 同 cre2_new, 但显式给 RE2::Options::max_mem (字节; <=0 = 用 RE2 默认 8MB).
 * 一个旋钮同时抬两条天花板: 编译期程序指令上限 + 运行期 DFA 状态缓存额度.
 * 单条 pattern 撞上状态爆炸时, 默认 8MB 会让 DFA 反复整表清空 (结果仍对, 吞吐掉两个数量级),
 * 而调用方在 cre2_dfa_stats 之外拿不到任何按对象的读数 —— 所以先得能把预算调大. */
cre2_re *cre2_new_max_mem(const char *pat, int patlen, int64_t max_mem);
/* 同 cre2_new_max_mem, 但打开 RE2::Options::set_longest_match —— 这个 handle 的匹配口径从
 * leftmost-first (贪心, RE2/PCRE 默认) 换成 leftmost-longest (POSIX):【同一个起点】上取最长的
 * 那个匹配, 而不是 NFA 指令优先序先撞上的那个. 起点的选法两者完全相同 (都是最靠左的那个).
 *
 * 🔴 存在理由: 调用方要"从这一点起最长的那个匹配"时, 有了它就是【一次】搜索的事 —— 不必再
 *    先用贪心搜一次定起点、再另找一条路把终点重取成最长. 而且这一次走的是 RE2::Match 那条
 *    完整的路 (DFA → OnePass/BitState/NFA 逐级回退), DFA 放弃了还有下家; set 那侧的锚定解析
 *    是 DFA 独一条, 没有下家.
 * 🔴 longest 是【编译期】的事 (kind 由 options 决定), 所以它是另一个 handle, 不是调用上的开关. */
cre2_re *cre2_new_longest_max_mem(const char *pat, int patlen, int64_t max_mem);
/* 这个 handle 编译时用的 max_mem (字节). */
int64_t cre2_max_mem(const cre2_re *h);
/* 1=编译成功 0=失败. */
int cre2_ok(const cre2_re *h);
/* 失败原因, NUL 结尾, 有效期直到 cre2_free. */
const char *cre2_error(const cre2_re *h);
/* 非锚定匹配 (等价 go-re2 MatchString): text 任意位置命中返回 1. */
int cre2_partial_match(const cre2_re *h, const char *text, int textlen);

/* 捕获组个数 (不含整体匹配 group0) = RE2::NumberOfCapturingGroups. */
int cre2_num_groups(const cre2_re *h);
/* 取第 idx 组的命名 (无名/越界返回 0), 把名字写进 buf, 返回名字真实长度 (可能 > buflen). */
int cre2_group_name(const cre2_re *h, int idx, char *buf, int buflen);
/* 在 [startpos, endpos) 这一段里找【非锚定】的下一处匹配, 把 group0..groupN 的字节区间写进
 * match (长度须 = 2*nmatch, 每组 [start,end); 未参与的组写 -1,-1). 1=有匹配 0=无.
 *
 * 🔴 text/textlen 传的始终是【整串】, startpos/endpos 只圈定"在哪一段里找" ——
 *    ^ / $ / \b 看到的仍是整串的真实邻字节 (RE2::Match 把 text 当 context, 只在
 *    text.substr(startpos, endpos-startpos) 上搜). 这正是这组入口存在的理由:
 *    调用方不必自己 text[a:b] 切一刀, 切完两侧就是假邻居了.
 * 要"从 startpos 一直找到底"就传 endpos = textlen. */
int cre2_match_at(const cre2_re *h, const char *text, int textlen, int startpos, int endpos, int *match, int nmatch);

/* 同 cre2_match_at, 但匹配必须【从 startpos 起头】(RE2::ANCHOR_START). 其余逐字相同:
 * text/textlen 仍是整串 (context), 只有 [startpos,endpos) 参与搜索, ^ / $ / \b 看到真实邻字节.
 *
 * 配 cre2_new_longest_max_mem 编出来的 handle 用, 这一句给的就是"从 startpos 起、不越过 endpos
 * 的【最长】匹配"—— 也就是 cre2_set_resolve_span 在 set 那侧干的事, 只不过走的是 RE2::Match
 * 那条带 NFA 回退的路, 且不需要一个 set. 配普通 handle 用给的是贪心那个终点. */
int cre2_match_at_anchored(const cre2_re *h, const char *text, int textlen, int startpos, int endpos, int *match, int nmatch);

/* cre2_match_step_result: cre2_match_all_step 的返回值。
 *   rc       : 1=正常; 0=参数不合法(当无匹配处理)。
 *   nmatches : 本批填进 outBuf 的匹配【处】数 (写入 int 数 = nmatches * 2 * nmatch)。
 *   pos      : 游标, 下次 step 原样传回来。
 *   prevEnd  : 空匹配去重游标, 下次 step 原样传回来。
 *   done     : 1=已扫到底(或已满 maxn), 不必再 step。 */
typedef struct {
	int rc;
	int nmatches;
	int pos;
	int prevEnd;
	int done;
} cre2_match_step_result;

/* cre2_match_all_step: cre2_match_all 的【分批】孪生 —— 逐处匹配的循环仍整段留在 C 内,
 * 一直填到 outBuf 装满 (outCapMatches 处) 或扫到底才返回, 所以 cgo 过境次数是
 * ceil(匹配数/outCapMatches)+1 而不是每处一次; 无匹配时就 1 次, 与 cre2_match_all 相同。
 *
 * 与 cre2_match_all 的差别只有"结果落在哪": 匹配集合 / 顺序 / 空匹配去重推进逐字相同
 * (同一段循环)。结果【直接写进调用方给的 outBuf】—— 没有 std::vector 累积, 没有 malloc,
 * 没有拷贝, 因此 C 侧不再出现"整张命中表的两份"那个峰值。
 *
 * 参数:
 *   maxn_left     : 本次还允许再填几处; <0 = 不限。跨批的剩余额度由调用方递减。
 *   pos, prevEnd  : 游标。首次调用传 pos=0, prevEnd=-1。
 *   outBuf        : 调用方内存 (Go 侧就是一块 []int 的首元素地址); 本函数返回即不再引用,
 *                   不留存 —— 满足 cgo "C 不得保存 Go 指针" 的规则。
 *   outCapMatches : outBuf 能装几【处】匹配 (即 outBuf 至少有 outCapMatches*2*nmatch 个 int)。
 *
 * 🔴 native 侧不保存正文指针: text/textlen 每次 step 重新传进来。 */
cre2_match_step_result cre2_match_all_step(const cre2_re *h, const char *text, int textlen, int nmatch,
                                           int maxn_left, int pos, int prevEnd,
                                           int *outBuf, int outCapMatches);

/* ── 实验中 (2026-08-26): 缓冲改由 C 侧持有 ────────────────────────────────────────
 * cre2_match_all_step_alloc 与上面那个逐字同一段循环, 唯一差别是【缓冲谁分配】:
 *   inBuf==NULL 时不预先分配, 直到真的要写第一处命中才 malloc(capMatches*2*nmatch 个 int),
 *   并把指针放进 res.buf 交回调用方; 之后每次 step 原样传回来。扫完由调用方 cre2_step_buf_free。
 * 目的是回答"能不能干掉调用方持有的工作区(Go 侧 MatchStep_t)、让 miss 路径一分钱不花"。
 * 结论见 match_step_alloc_bench_test.go 顶部。 */
typedef struct {
	int rc;
	int nmatches;
	int pos;
	int prevEnd;
	int done;
	int *buf; /* C 侧缓冲 (可能是本次刚 malloc 的); 调用方负责最后 cre2_step_buf_free */
} cre2_match_step_alloc_result;

cre2_match_step_alloc_result cre2_match_all_step_alloc(const cre2_re *h, const char *text, int textlen, int nmatch,
                                                       int maxn_left, int pos, int prevEnd,
                                                       int *inBuf, int capMatches);
void cre2_step_buf_free(int *p);
/* cre2_nop: 量一次纯 cgo 过境要多少 ns (给上面那个方案的"额外一次 free 过境"定价)。 */
void cre2_nop(void);
/* cre2_malloc_free_roundtrip: 在 C 内做 n 轮 malloc(nbytes)+free, 量 glibc 分配器本身的价
 * (不含 cgo 过境)。用来验证"C 的 malloc/free 是不是一个高效的 sync.Pool"。 */
void cre2_malloc_free_roundtrip(int n, int nbytes);

/* cre2_match_all vs cre2_match_all_step 的分工 (2026-08-26 实测定的, 别再改回去):
 *   · 调用方【不需要全部物化】(顺序遍历一遍就完事) ⇒ step。零 Go 分配, C 侧也不攒表。
 *   · 调用方【就是要一次吐完的数组】(FindAll* 的契约) ⇒ 本函数。因为"先在 C 里数好个数,
 *     再一次精确 malloc"本来就是这个契约下的最优解: Go 侧拿到 count 后一次精确 make。
 *     用 step 物化反而更贵 —— Go append 阶梯累计收敛到 5N, 实测 20000 处命中
 *     1.45MB/4 笔 → 5.70MB/26 笔, CPU +17%。
 * 所以两个都留着, 各管各的。中间那种"半物化"形态 (AppendAllStringIndexFlat) 才是两头不靠的
 * 那个 —— 不物化就用 step, 要物化就用 FindAll*, 它没有自己的地盘, 所以它被删。 */

/* 批量全匹配: 在 C 内一次循环跑完整个 text 的所有(最多 maxn 个; maxn<0=不限)非锚定匹配,
 * 复刻调用方 allMatches 的空匹配去重 + UTF-8 rune 推进语义. 每处匹配顺序写 2*nmatch 个 int
 * (group0..groupN-1 的 [start,end); 未参与的组 -1,-1). 用途: 把原本「每处匹配一次 cgo」的
 * Go 循环压成单次 cgo 调用. 成功(有匹配)时 *out 指向 malloc 的 int 数组(调用方负责 free),
 * *nmatches = 匹配数, 返回 1; 无匹配返回 0(*out=NULL,*nmatches=0); malloc 失败返回 -1. */
int cre2_match_all(const cre2_re *h, const char *text, int textlen, int nmatch, int maxn, int **out, int *nmatches);

/* cre2_match_all_result: cre2_match_all_r 的返回值 (字段含义同 cre2_match_all 的三个出参)。
 *   rc       : 1=有匹配; 0=无匹配; -1=malloc 失败。
 *   out      : rc=1 时 malloc 的 int 数组 (调用方 free); 否则 NULL。
 *   nmatches : 匹配数。
 * 为什么要这个【按值返回】的孪生: Go 侧用出参版必须把 &out/&cnt 两个 Go 指针交给 C,
 * 逃逸分析据此把这两个局部变量搬上堆 —— 每次调用两笔小分配, 正好把 AppendAllStringIndexFlat
 * 「稳态零分配」那条唯一的卖点抵消掉。按值返回后 Go 侧一个指针都不用交出去。*/
typedef struct {
	int rc;
	int *out;
	int nmatches;
} cre2_match_all_result;

/* cre2_match_all_r: cre2_match_all 的按值返回孪生 (同一份实现, 语义逐字相同)。 */
cre2_match_all_result cre2_match_all_r(const cre2_re *h, const char *text, int textlen, int nmatch, int maxn);

/* cre2_replace_result: cre2_find_replace_within 的返回值。
 *   rc      : 1=正常完成; -1=malloc 失败 (调用方退回原串)。
 *   changed : 1=结果与输入【有字节差异】(out 指向 malloc 缓冲, 调用方 free); 0=无任何改动。
 *   outlen  : changed=1 时的结果字节数。
 *   out     : changed=1 时 malloc 的结果缓冲 (调用方 free); changed=0 时为 NULL。
 * 关键: changed=0 (含完全无匹配 / 命中但替换后逐字节不变) 时【不分配、不拷贝】, 调用方直接用原串。 */
typedef struct {
	int rc;
	int changed;
	int outlen;
	char *out;
} cre2_replace_result;

/* cre2_find_replace_within: 复刻 Go 的
 *   find.ReplaceAllStringFunc(text, func(m){ return strip.ReplaceAllString(m, repl) })
 * 把【外层 find 逐处匹配循环 + 每处匹配内层 strip 替换】整体压进一次 cgo 调用:
 *   - find 在 text 上做非锚定全匹配 (推进/空匹配去重语义同 cre2_match_all);
 *   - 对每处匹配的【整体文本 group0】用 strip 做 RE2::GlobalReplace → repl (repl 是 RE2 重写串,
 *     捕获组引用用 \1..\9, 非 $1 语法; 字面 repl 如 "" 无差别);
 *   - 匹配之外的部分原样拼接。
 * 算法与两正则嵌套版完全一致 (find 仍可零捕获组走最快 DFA, strip 仍只删字符类), 仅省 cgo 次数
 * 与 Go 侧 per-match 分配。典型用途: 去混淆还原 (find=被分隔符拆开的关键词骨架, strip=分隔符字符类,
 * repl="")。
 * 结果缓冲【惰性物化】: 扫描中遇到【第一处真正改变字节的替换】才开始建结果串; 在那之前 (含全程
 * 无匹配的最常见主路径) 完全不分配 → 返回 changed=0, 调用方用原串。详见 cre2_replace_result。 */
cre2_replace_result cre2_find_replace_within(const cre2_re *find, const cre2_re *strip, const char *text,
                                             int textlen, const char *repl, int replen);

/* cre2_replace_all_literal: 复刻 Go 的 re.ReplaceAllString(text, repl), 但限 repl 为【字面串】
 * (调用方已保证 repl 不含 '$', 故无 $1/${name} 展开). 把「逐处匹配 + 字面拼接」整循环压进一次 cgo:
 *   - re 在 text 上做非锚定全匹配 (推进/空匹配去重语义同 cre2_find_replace_within);
 *   - 每处命中段 [m0,m1) 整体换成 repl[0,replen) 的【原始字节】(不解释 \1/$1);
 *   - 匹配之外原样拼接。
 * 结果惰性物化 (同 cre2_find_replace_within): 全程无字节改动(无匹配/命中但 repl==命中段)→ changed=0,
 * 不分配, 调用方用原串。返回值含义见 cre2_replace_result。 */
cre2_replace_result cre2_replace_all_literal(const cre2_re *re, const char *text, int textlen,
                                             const char *repl, int replen);

void cre2_free(cre2_re *h);

/* ── RE2::Set: 多正则【一次扫描·返回哪几条命中】(litscan 的正则版) ──────────────
 * 把 N 条正则编进一个 DFA, 一遍扫 text 得到命中的 pattern index 集合 (不锚定/partial)。
 * 不返回位置 —— 只回答"哪些 pattern 命中"(需位置的调用方再对命中条单独取)。 */
typedef struct cre2_set cre2_set;
/* 建一个空 set (UNANCHORED · log_errors off)。OOM 返回 NULL。
 * max_mem = RE2::Options::max_mem (字节): RE2 用它同时算【编译期程序指令上限】和【运行期 DFA
 * 状态缓存预算】(Prog::CompileSet(re, anchor, max_mem))。<=0 表示用 RE2 默认 8MB(kDefaultMaxMem)。
 * 条数多到编译期预算装不下时 cre2_set_compile 返回 0 —— 调大它即可把整表塞进一个 set。 */
cre2_set *cre2_set_new(int64_t max_mem);
/* 加一条 pattern, 返回它的 index (从 0 顺序递增); 解析失败返回 -1 (不占 index)。 */
int cre2_set_add(cre2_set *h, const char *pat, int patlen);
/* 编译整个 set (Match 前必须调一次)。1=成功 0=失败(OOM)。 */
int cre2_set_compile(cre2_set *h);
/* 扫 text 一遍, 把命中的 pattern index 写进 out (容量 outcap, 调用方给 = pattern 数即够),
 * 返回命中条数 (index 不重复 · 顺序不保证)。无命中返回 0。out 写入个数 = min(命中数, outcap)。 */
int cre2_set_match(const cre2_set *h, const char *text, int textlen, int *out, int outcap);
/* 只问"有没有任一条命中", 不取 index。1=命中 0=没命中(或 DFA 放弃)。
 * 走的是同一份 kManyMatch DFA 缓存, 但不传 matches 集合 —— RE2 的 Prog::SearchDFA 见 matches==NULL
 * 就把 want_earliest_match 打开, DFA 扫到【第一个】命中位置立刻收工, 不再把正文剩下的部分扫完。 */
int cre2_set_match_any(const cre2_set *h, const char *text, int textlen);
void cre2_set_free(cre2_set *h);

/* ── 按对象归因的 DFA 计数 (per-Set / per-scan) ────────────────────────────────
 * 下面这套跟 cre2_dfa_stats_* 那套【进程级】计数是两回事:
 *   进程级: 装在 re2 的全局钩子上, 回调不带上下文, 只能回答"有人在 thrash";
 *   这一套: 计数挂在 Set 自己的 DFA 上, 外加一个调用方在栈上开的一次性对象,
 *           能回答"是哪个 Set"和"这一次调用里发生了几次"。学的是 Rust regex-automata
 *           的 per-Cache 计数 (clear_count / 已扫字节数), 不用 thread_local (mingw 没有)。 */
typedef struct {
	int64_t Flushes;      /* 本次扫描里状态缓存被整表清空几次。>0 = 这次调用踩在悬崖上。 */
	int64_t Grows;        /* 本次扫描里 arena 扩容几次 (不丢状态, 不是 thrash)。 */
	int64_t StatesBuilt;  /* 本次扫描新建了几个状态 (并发时会把别人建的算进来)。 */
	int64_t Bytes;        /* 本次扫描的正文字节数。 */
	int64_t StatesEnd;    /* 扫完时缓存里的状态数。 */
	int64_t StateBudget;  /* 这个 DFA 的状态缓存额度 (字节)。 */
	int64_t MemLeft;      /* 扫完时剩余额度; 已用 = StateBudget - MemLeft。 */
} cre2_scan_stats;

/* 同 cre2_set_match, 外加把这一次扫描的计数写进 *st (st 可为 NULL)。
 * st 不必预先清零; 每次调用被完整覆盖。零开销来自"不传就完全不统计"。 */
int cre2_set_match_stats(const cre2_set *h, const char *text, int textlen,
                         int *out, int outcap, cre2_scan_stats *st);

typedef struct {
	int32_t Built;             /* 0 = 这个 Set 还没扫过 (DFA 都没建), 其余字段无意义。 */
	int64_t StateBudget;       /* 状态缓存额度 (字节)。 */
	int64_t MemLeft;           /* 剩余额度; 已用 = StateBudget - MemLeft。 */
	int64_t States;            /* 当前缓存里的状态数。 */
	int64_t ArenaCap;          /* 实际向系统要到的状态区字节数 (arena 版才有意义)。 */
	int64_t FlushesTotal;      /* 这个 Set 生涯里整表清空了几次。 */
	int64_t StatesBuiltTotal;  /* 这个 Set 生涯里建过几个状态。 */
} cre2_set_mem;

/* 查这个 Set 当前吃掉了多少额度 / 装了多少状态 / 生涯清空过几次。
 * 【不会】因为查询而把 DFA 建出来 (没扫过就返回 Built=0)。可与扫描并发调。 */
void cre2_set_mem_info(const cre2_set *h, cre2_set_mem *out);

/* ── 建状态的归因 (要 -DRE2_DFA_ATTRIB=1 编译, 否则 Enabled=0) ────────────────
 * 回答三件事: 这些状态【是哪几条 pattern 造的】、【单个有多贵】、【在正文哪一段造的】。
 * 之前定位病灶只能"摘掉一条重编 + 跑二分找 0-flush 门槛", 一轮几十分钟; 这个是扫一遍出榜。 */
typedef struct {
	int32_t Enabled;          /* 0 = 编译时没开 RE2_DFA_ATTRIB, 其余字段无意义。 */
	int32_t Built;            /* 0 = 这个 Set 还没扫过 (DFA 都没建)。 */
	int32_t NPat;             /* pattern 条数。 */
	int64_t StatesTotal;      /* 生涯建过的状态数。 */
	int64_t SharedInsts;      /* 落在"多条 pattern 共用"指令上的次数 (起始 .* 循环等)。 */
	int64_t NInstSum;         /* 新建状态的 ninst 之和; /StatesTotal = 平均状态宽度。 */
	int64_t NInstMax;
	int64_t NInstHist[16];    /* ninst 落在 [2^i, 2^(i+1)) 的状态数 = "变胖"的分布。 */
	int64_t BirthHist[64];    /* 建状态时读到正文的第几个 1/64 = "在哪造的"。 */
} cre2_set_attrib;

/* agg 必填; pat_states / pat_insts 可为 NULL, 否则各按 pattern 下标回填 min(NPat, cap) 个:
 *   pat_states[i] = 有多少个新建状态里出现了第 i 条 pattern 独占的指令
 *   pat_insts[i]  = 这些独占指令一共出现了多少次 (加权, 反映它对"状态变胖"的贡献) */
void cre2_set_attrib_info(const cre2_set *h, cre2_set_attrib *agg,
                          int64_t *pat_states, int64_t *pat_insts, int cap);

/* ── 反着扫 (reverse DFA): 让 DFA 从正文末尾往前走【原始 buffer】 ──────────────
 * 为什么要它: `S B{m,n}L` 这种【起始类窄于重复类】的计数重复 (如 [A-Za-z][A-Za-z0-9]{2,19}key),
 * 正向 DFA 的活跃起点集会退化成任意子集 → 状态数对 n 指数; 同一条语言反过来读只要线性
 * (Myhill-Nerode 层面的不对称, 不是实现问题, 任何等价改写都消不掉)。
 * 反向由 RE2 编译器自己做: 所有 concat 反序、^ 与 $ 对调、\b 不变、多字节 UTF-8 的字节序列
 * 也一并反过来编 —— 所以【调用方不需要反转正文】, 也不会把 UTF-8 拆散。
 * 语义: 只回答"命中/哪几条命中", 不回答"在哪"。要位置的调用方拿到结果后再正向取一次。
 */

/* cre2_rev_match_result: cre2_partial_match_reverse 的按值返回值 (按值是为了不把 Go 指针交出去)。
 *   Matched  : 1=命中 0=不命中。
 *   FellBack : 1 = 反向 DFA 这次没跑成 (反向程序编译失败 / DFA 中途放弃), 结果是退回
 *              正向 RE2::PartialMatch 得到的 —— 结果仍然正确, 只是这次没省到状态。
 *   Stats    : want_stats!=0 时填这一次扫描的计数 (FellBack=1 时全 0)。 */
typedef struct {
	int32_t Matched;
	int32_t FellBack;
	cre2_scan_stats Stats;
} cre2_rev_match_result;

/* 反向非锚定匹配: 语义与 cre2_partial_match 逐字相同 (正文任意位置命中即 1), 但 DFA 反着跑。
 * 反向程序在首次调用时惰性编出来 (线程安全), 预算用 handle 的 max_mem。 */
cre2_rev_match_result cre2_partial_match_reverse(const cre2_re *h, const char *text, int textlen, int want_stats);

/* ── 流式游程扫描: 一遍扫正文, 边扫边说"哪条 pattern 命中在【哪】" ──────────────
 * cre2_set_match 只回答"哪几条命中"; 要位置的调用方只能拿到 id 之后, 再用那几条各自
 * 跑一遍【整篇正文】。位置本来就在 DFA 热循环里算出来了 (只是被丢掉), 这套 API 把它收下来。
 *
 * 吐出来的是【游程】(id, lo, hi), 不是逐个位置 —— kManyMatch 的 DFA 在每一个能结束匹配的
 * 位置都会报一次, 可变长匹配上会连出一串 (`[a-z]{3,}` 撞 "abcdef" 会在 3/4/5/6 各报一次)。
 * 按 pattern 把连号的收敛成一段再吐, 且【两端都给】所以无信息损失 (展开 [lo,hi] 即可还原)。
 *   正向 set : 位置 = 匹配【右端】偏移 (不含)
 *   反向 set : 位置 = 匹配【左端】偏移 (含)
 * 恒 lo <= hi (原文坐标)。顺序【不保证】全局按位置升序 —— 游程要等"这条再次命中且不连号"
 * 或"整篇扫完"才收口, 所以不同 pattern 的游程会交错。要有序自己排 (条数是游程数, 很少)。
 *
 * 语义细节和"为什么是轮询不是回调/不是一次吐完"见 internal_include/re2/span_scan.h。 */
typedef struct cre2_spanscan cre2_spanscan;

/* 开一个流式扫描工作区 (set 必须已 cre2_set_compile)。OOM / 没编译返回 NULL。
 * 工作区可以反复用于多次扫描 (游程表不重新分配), 但【不是并发安全的】: 一个线程一个。 */
cre2_spanscan *cre2_set_spanscan_new(const cre2_set *h);
void cre2_spanscan_free(cre2_spanscan *ss);

/* 开始一次新扫描 (清游程表, 绑定正文长度)。1=成功 0=失败(textlen<0)。 */
int cre2_spanscan_begin(cre2_spanscan *ss, int textlen);

/* 推进一步, 最多吐 outcap/3 条游程进 out (每条 3 个 int32: index, lo, hi)。
 * 返回本批的【游程条数】(>=0); <0 = 出错 (DFA 放弃 / 状态恢复失败, 整次扫描作废)。
 * *more: 1 = 还没扫完, 取走这批后再 step 一次; 0 = 扫完了。
 * outcap 必须 >= 3*pattern条数 (保证"再来一个命中字节也一定塞得下"), 否则返回 -1。
 *
 * 🔴 text/textlen 每次 step 都要重新传, 且必须是同一份正文、同一个长度:
 *    native 侧【只存偏移不存指针】—— 挂起期间不持有调用方的任何指针 (cgo 的硬要求),
 *    也不持有 DFA 的任何锁 (持锁跨回调会把想 flush 的线程全堵住)。 */
int cre2_spanscan_step(cre2_spanscan *ss, const char *text, int textlen,
                       int32_t *out, int outcap, int *more);

/* 给定 spanscan 吐出来的一个端点, 求第 id 条 pattern 在这个端点上的【另一端】(最长的那个)。
 *   正向 set: from = 匹配左端(含), 写回右端(不含) —— text[from, *out) 是一个匹配;
 *   反向 set: from = 匹配右端(不含), 写回左端(含) —— text[*out, from) 是一个匹配。
 * bound = 最远看到哪 (正向是上界, 反向是下界), 负数 = 不限; 判定用的上下文恒是整篇正文,
 * 所以 \b / ^ / $ 看到的永远是真实邻居, 不是 bound 切出来的假边界。
 * 返回 1 = 找到, 0 = 这条 pattern 在这个端点上不匹配, -1 = 参数错 / DFA 放弃。
 * 无状态、只读, 可以与扫描并发调。为什么这一步得在库里做见 internal_include/re2/span_scan.h。 */
int cre2_set_resolve_span(const cre2_set *h, const char *text, int textlen,
                          int from, int bound, int id, int32_t *out);

/* cre2_span_resolve_result: cre2_set_resolve_span_r 的返回值。
 *   rc  同 cre2_set_resolve_span 的返回值 (1 找到 / 0 不匹配 / -1 参数错或 DFA 放弃);
 *   pos 就是那个出参 (rc!=1 时无意义)。 */
typedef struct {
	int32_t rc;
	int32_t pos;
} cre2_span_resolve_result;

/* cre2_set_resolve_span_r: 上面那个的【按值返回】孪生 (同一份实现, 语义逐字相同)。
 * 🔴 Go 侧一律走这个: 出参版要把 &out 这个 Go 指针交给 C, 逃逸分析据此每次调用把那个
 * 局部变量搬上堆 —— 一次解析一笔 4 字节堆分配, 在"每个端点解析一次"的用法上直接按端点数
 * 放大 (实测 6.5 万个端点 = 6.5 万笔)。同一招见 cre2_match_all_r。 */
cre2_span_resolve_result cre2_set_resolve_span_r(const cre2_set *h, const char *text,
                                                 int textlen, int from, int bound, int id);

/* cre2_set_viable_starts: 【反向 set 专用】给一个匹配右端 from, 把 [bound, from) 里全部
 * 【候选起点】收下来 —— 即那些位置 s 使 text[s, from) 是第 id 条 pattern 的一个
 * 【可行前缀】(还能被某个后缀补成真匹配, 不一定当场就是匹配).
 *
 * 与 cre2_set_resolve_span_r 的差别只有一处, 但是决定性的: 那个的反向机器只种 accept,
 * 所以只认"正好是匹配"的左端; 这个种【全部指令】, 连"路过这个右端的更长匹配"的起点也认.
 * 前者是后者的子集, 而"leftmost 到底在哪"只有问后者才问得对 ——
 *   (?:ab cd ef|cd) 撞 "ab cd ef": 门给的最小右端是 "cd" 那一处的右端, 只种 accept
 *   只能回推到 "cd" 的左端, 种全部状态才能回推到 "ab" 那个真正的最左起点.
 *
 * out 里是【降序】(机器从右往左走). 返回【找到的总条数】, 可能 > outcap —— 此时只写了
 * 前 outcap 个 (也就是最大的那几个, 恰好最没用), 调用方该换个更大的缓冲重来一次.
 * -1 = 参数错 / 不是反向 set / DFA 放弃.
 * 代价 = 这处命中能往回够多远 (机器走到死就收工), 与正文长度无关. */
int cre2_set_viable_starts(const cre2_set *h, const char *text, int textlen,
                           int from, int bound, int id, int32_t *out, int outcap);

/* cre2_resolve_span_reverse_r: 【单条】正则的反向锚定解析 —— 不经过 set.
 * from = 匹配右端 (不含), 返回【最靠左】的那个左端 (含), text[pos,from) 就是这条 pattern 的一个匹配.
 * bound = 回看不越过它 (左下界), 负数 = 不限. 判定上下文恒是整篇正文, 所以 \b / ^ / $ 看到的
 * 是真实邻居, 不是 bound 切出来的假边界.
 *
 * 实现就是 RE2::Match 自己求匹配【左端】时走的那一句: 反向程序 + kAnchored + kLongestMatch.
 * 惰性编反向程序 (与 cre2_partial_match_reverse 共用同一份, 见 cre2_rev_prog).
 * 返回 rc: 1 = 找到, 0 = 这个右端上伸不出匹配, -1 = 参数错 / 反向程序编不出来 / DFA 放弃.
 *
 * 🔴 为什么不能拿 cre2_set_resolve_span_r 套一条 pattern 的 set 凑: 那是 kManyMatch 的 DFA
 *    (每个状态多背一张 id 表), 而且 set 那侧的 ^ / $ 是留在程序里的指令, 单条 Compile 却把
 *    它们摘成 anchor_start_/anchor_end_ 两个标志 —— 只有 SearchDFA 会去检查那两个标志.
 *    绕开它自己走 DFA 的话, `^foo` 会在正文中间也认. */
cre2_span_resolve_result cre2_resolve_span_reverse_r(const cre2_re *h, const char *text,
                                                     int textlen, int from, int bound);

/* 查这条 pattern 的【反向程序】当前的 DFA 缓存水位 (字段同 cre2_set_mem).
 * 没走过反向 (程序都没编 / DFA 没建) 就返回 Built=0, 【不会】因为查询而把它建出来. */
void cre2_rev_mem_info(const cre2_re *h, cre2_set_mem *out);

/* 建一个空 set, reversed!=0 时整个 set 反向编译 (Match 从末尾往前扫原始 buffer)。
 * cre2_set_new(mm) == cre2_set_new_ex(mm, 0)。其余 API (add/compile/match/stats/mem_info/attrib)
 * 对反向 set 一律照常可用, 命中集与正向逐位相同。 */
cre2_set *cre2_set_new_ex(int64_t max_mem, int reversed);
/* 这个 set 是不是反向编译的。 */
int cre2_set_reversed(const cre2_set *h);

/* ── DFA 状态缓存计数 (可观测 · 进程级) ────────────────────────────────────────
 * RE2 的 DFA 状态缓存满了不是 LRU 淘汰, 而是【整表清空】重建 (DFA::ResetCache):
 * 结果仍正确, 所以调用方看不见, 但吞吐是几十倍的悬崖。这几个计数把它变成可测的量。
 * 实现与口径见 cre2_dfastats.cpp; 钩子由该文件在动态初始化阶段自动装好, 无需初始化调用。 */
typedef struct {
	uint64_t Resets;         /* DFA::ResetCache 次数 (状态缓存整表清空). 每扫必涨 = 正在 thrash。 */
	uint64_t SearchFailures; /* DFA 放弃搜索次数 (单条 Regexp 会退回 NFA; RE2::Set 不会, 见 README)。 */
	int64_t LastStateBudget; /* 最近一次 Resets 时那个 DFA 的状态预算 (字节)。 */
	int64_t LastCacheStates; /* 最近一次 Resets 时缓存里的状态数 (清掉的就是这么多)。 */
} cre2_dfa_stats;
/* 取一份快照 (无锁, 四个字段各自 relaxed 读, 不保证互相一致)。 */
cre2_dfa_stats cre2_dfa_stats_get(void);
/* 四个计数归零 (分段测量用)。 */
void cre2_dfa_stats_zero(void);

/* ── prefilter (FilteredRE2 的 C ABI · 实现在 cre2_prefilter.cpp) ──────────────
 * 回答「这条 pattern 想命中, 正文里必须先出现哪些字面量」, 以及最要紧的那个反问:
 * 「哪几条根本没有必需字面量, 因而任何前置粗筛都筛不掉」。用途与判据见 cre2_prefilter.cpp 头注。*/
typedef struct cre2_prefilter cre2_prefilter;

/* 新建. min_atom_len<=0 用 FilteredRE2 默认最小原子长度; max_mem<=0 用 RE2 默认 8MB. */
cre2_prefilter *cre2_prefilter_new(int min_atom_len, int64_t max_mem);
/* 加一条 pattern (必须在 compile 之前). 返回 id (0 起, 与加入顺序一致) 或 -1 (解析失败/已 compile). */
int cre2_prefilter_add(cre2_prefilter *h, const char *pat, int patlen);
/* 编译, 返回原子个数 (>=0); 重复调返回 -1. */
int cre2_prefilter_compile(cre2_prefilter *h);
/* 取第 i 个原子 (已小写化、去重). 返回字节长度, 指针写进 *p (有效期到 cre2_prefilter_free). */
int cre2_prefilter_atom(const cre2_prefilter *h, int i, const char **p);
/* 加进去的 pattern 条数. */
int cre2_prefilter_num_regexps(const cre2_prefilter *h);
/* 给定"正文里找到的原子下标集合", 回填"还可能命中的 pattern 下标"(升序).
 * natoms==0 ⟹ 返回【不可过滤集】= 无论正文长什么样都得跑的那批.
 * 返回值是真实条数, 可能 > outcap (此时只填了前 outcap 个). */
int cre2_prefilter_potentials(const cre2_prefilter *h, const int *atoms, int natoms, int *out, int outcap);
void cre2_prefilter_free(cre2_prefilter *h);

#ifdef __cplusplus
}
#endif

#endif
