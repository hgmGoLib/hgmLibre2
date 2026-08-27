// cre2_spanscan.cpp — RE2::Set "命中在哪"的 C 门面: 流式游程扫描 (sqlite3_step 式轮询)
// + 锚定解析 (cre2_set_resolve_span)。
//
// 语义 (吐什么 / 为什么是游程 / 为什么是轮询) 全在 internal_include/re2/span_scan.h,
// 这里只做句柄包装和参数守卫。用法:
//
//   cre2_spanscan *ss = cre2_set_spanscan_new(set);
//   cre2_spanscan_begin(ss, textlen);
//   for (;;) {
//       int more = 0;
//       int n = cre2_spanscan_step(ss, text, textlen, out, outcap, &more);
//       if (n < 0) { /* 出错, 整次扫描作废 */ break; }
//       /* out 里有 n 条 (id, lo, hi) */
//       if (!more) break;
//   }
//   cre2_spanscan_free(ss);      // 工作区可以反复 begin, 不必每次扫描重开

#include "cre2.h"
#include "cre2_internal.h"
#include "re2/span_scan.h"

#include <new>

struct cre2_spanscan {
	re2::DFASpanScan *ss;
};

cre2_spanscan *cre2_set_spanscan_new(const cre2_set *h) {
	if (h == nullptr || h->set == nullptr) {
		return nullptr;
	}
	re2::DFASpanScan *inner = h->set->NewSpanScan();
	if (inner == nullptr) {
		return nullptr; // 没 Compile 过 / DFA 建不出来
	}
	cre2_spanscan *w = new (std::nothrow) cre2_spanscan;
	if (w == nullptr) {
		re2::DFASpanScanFree(inner);
		return nullptr;
	}
	w->ss = inner;
	return w;
}

void cre2_spanscan_free(cre2_spanscan *ss) {
	if (ss == nullptr) {
		return;
	}
	re2::DFASpanScanFree(ss->ss);
	delete ss;
}

int cre2_spanscan_begin(cre2_spanscan *ss, int textlen) {
	if (ss == nullptr) {
		return 0;
	}
	return re2::DFASpanScanBegin(ss->ss, textlen) ? 1 : 0;
}

int cre2_spanscan_step(cre2_spanscan *ss, const char *text, int textlen,
                       int32_t *out, int outcap, int *more) {
	if (more != nullptr) {
		*more = 0;
	}
	if (ss == nullptr || more == nullptr || out == nullptr) {
		return -1;
	}
	// 空串也喂合法指针 (同 cre2_set_match): native 侧只做指针算术, 不解引用长度 0 的缓冲。
	const char *base = text ? text : "";
	return re2::DFASpanScanStep(ss->ss, base, textlen, out, outcap, more);
}

int cre2_set_resolve_span(const cre2_set *h, const char *text, int textlen,
                          int from, int bound, int id, int32_t *out) {
	if (h == nullptr || h->set == nullptr || out == nullptr) {
		return -1;
	}
	const char *base = text ? text : "";
	return h->set->ResolveSpan(base, textlen, from, bound, id, out);
}

int cre2_set_viable_starts(const cre2_set *h, const char *text, int textlen,
                           int from, int bound, int id, int32_t *out, int outcap) {
	if (h == nullptr || h->set == nullptr || out == nullptr) {
		return -1;
	}
	const char *base = text ? text : "";
	return h->set->ViableStarts(base, textlen, from, bound, id, out, outcap);
}

cre2_span_resolve_result cre2_set_resolve_span_r(const cre2_set *h, const char *text,
                                                 int textlen, int from, int bound, int id) {
	cre2_span_resolve_result r;
	r.rc = -1;
	r.pos = 0;
	if (h == nullptr || h->set == nullptr) {
		return r;
	}
	const char *base = text ? text : "";
	int32_t pos = 0;
	r.rc = (int32_t)h->set->ResolveSpan(base, textlen, from, bound, id, &pos);
	r.pos = pos;
	return r;
}
