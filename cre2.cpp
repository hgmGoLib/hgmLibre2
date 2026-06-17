// cre2.cpp — cre2.h 的实现, 直接调 vendored RE2 (2023-03-01, 无 abseil).
#include "cre2.h"
#include "re2/re2.h"
#include <map>
#include <string>
#include <vector>

struct cre2_re {
	RE2 *re;
};

extern "C" {

cre2_re *cre2_new(const char *pat, int patlen) {
	re2::StringPiece sp(pat, patlen);
	RE2::Options opt;
	opt.set_log_errors(false); // 别往 stderr 喷, 错误走 cre2_error 取
	cre2_re *h = new (std::nothrow) cre2_re;
	if (h == nullptr) {
		return nullptr;
	}
	h->re = new RE2(sp, opt);
	return h;
}

int cre2_ok(const cre2_re *h) { return h->re->ok() ? 1 : 0; }

const char *cre2_error(const cre2_re *h) { return h->re->error().c_str(); }

int cre2_partial_match(const cre2_re *h, const char *text, int textlen) {
	re2::StringPiece sp(text, textlen);
	return RE2::PartialMatch(sp, *h->re) ? 1 : 0;
}

int cre2_num_groups(const cre2_re *h) { return h->re->NumberOfCapturingGroups(); }

int cre2_group_name(const cre2_re *h, int idx, char *buf, int buflen) {
	const std::map<int, std::string> &names = h->re->CapturingGroupNames();
	std::map<int, std::string>::const_iterator it = names.find(idx);
	if (it == names.end()) {
		return 0;
	}
	const std::string &nm = it->second;
	int n = (int)nm.size();
	for (int i = 0; i < n && i < buflen; i++) {
		buf[i] = nm[i];
	}
	return n;
}

int cre2_match_at(const cre2_re *h, const char *text, int textlen, int startpos, int *match, int nmatch) {
	// 用非空 base: RE2 文档规定 text==NULL 时连 group0 的 data() 都返回 NULL (无法算偏移),
	// 故空串也喂一个合法指针, 偏移一律相对 base 计算.
	const char *base = text ? text : "";
	re2::StringPiece full(base, textlen);
	std::vector<re2::StringPiece> sub(nmatch);
	bool ok = h->re->Match(full, (size_t)startpos, (size_t)textlen, RE2::UNANCHORED, sub.data(), nmatch);
	if (!ok) {
		return 0;
	}
	for (int i = 0; i < nmatch; i++) {
		if (sub[i].data() == nullptr) {
			match[2 * i] = -1;
			match[2 * i + 1] = -1;
		} else {
			int b = (int)(sub[i].data() - base);
			match[2 * i] = b;
			match[2 * i + 1] = b + (int)sub[i].size();
		}
	}
	return 1;
}

void cre2_free(cre2_re *h) {
	if (h == nullptr) {
		return;
	}
	delete h->re;
	delete h;
}

} // extern "C"
