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

void cre2_free(cre2_re *h);

#ifdef __cplusplus
}
#endif

#endif
