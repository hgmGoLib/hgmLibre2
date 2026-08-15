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

/* 编译 pattern (pat,patlen). 永不返回 NULL(分配失败才 NULL); 编译错误用 cre2_ok 检测. */
cre2_re *cre2_new(const char *pat, int patlen);
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
/* 从 startpos 起的【非锚定】下一处匹配, 把 group0..groupN 的字节区间写进 match
 * (长度须 = 2*nmatch, 每组 [start,end); 未参与的组写 -1,-1). 1=有匹配 0=无. */
int cre2_match_at(const cre2_re *h, const char *text, int textlen, int startpos, int *match, int nmatch);

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

#ifdef __cplusplus
}
#endif

#endif
