/* cre2.h — 极小 C 包装, 把 C++ 的 RE2 暴露成 C ABI 给 cgo 用.
 * 只暴露 Go 侧真正需要的: 编译 + 非锚定(unanchored)匹配 + 捕获组.
 * 不依赖 abseil; 配套的 RE2 源码是 abseil 之前的 2023-03-01 版 (纯 C++11), vendored 在本目录. */
#ifndef RE2NATIVE_CRE2_H
#define RE2NATIVE_CRE2_H

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

void cre2_free(cre2_re *h);

/* ── RE2::Set: 多正则【一次扫描·返回哪几条命中】(litscan 的正则版) ──────────────
 * 把 N 条正则编进一个 DFA, 一遍扫 text 得到命中的 pattern index 集合 (不锚定/partial)。
 * 不返回位置 —— 只回答"哪些 pattern 命中"(需位置的调用方再对命中条单独取)。 */
typedef struct cre2_set cre2_set;
/* 建一个空 set (UNANCHORED · log_errors off)。OOM 返回 NULL。 */
cre2_set *cre2_set_new(void);
/* 加一条 pattern, 返回它的 index (从 0 顺序递增); 解析失败返回 -1 (不占 index)。 */
int cre2_set_add(cre2_set *h, const char *pat, int patlen);
/* 编译整个 set (Match 前必须调一次)。1=成功 0=失败(OOM)。 */
int cre2_set_compile(cre2_set *h);
/* 扫 text 一遍, 把命中的 pattern index 写进 out (容量 outcap, 调用方给 = pattern 数即够),
 * 返回命中条数 (index 不重复 · 顺序不保证)。无命中返回 0。out 写入个数 = min(命中数, outcap)。 */
int cre2_set_match(const cre2_set *h, const char *text, int textlen, int *out, int outcap);
void cre2_set_free(cre2_set *h);

#ifdef __cplusplus
}
#endif

#endif
