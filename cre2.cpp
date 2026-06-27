// cre2.cpp — cre2.h 的实现, 直接调 vendored RE2 (2023-03-01, 无 abseil).
#include "cre2.h"
#include "re2/re2.h"
#include "re2/set.h"
#include <cstdlib>
#include <cstring>
#include <map>
#include <new>
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

// utf8WidthGo 复刻 Go utf8.DecodeRuneInString 返回的【宽度】, 仅供空匹配推进:
//   空串=0; 合法 rune=其字节数(1..4); 非法前导/截断/非法后续字节=1 (Go 对非法编码返回
//   RuneError 且宽度 1). 不另判 overlong/surrogate/超范围 —— 那些只在【非法 UTF-8】上与 Go
//   有别, 而本库对非法 UTF-8 的匹配语义本就按原生 RE2 (见 README caveats), 此处合法输入精确一致.
static int utf8WidthGo(const char *s, int n) {
	if (n <= 0) {
		return 0;
	}
	unsigned char b0 = (unsigned char)s[0];
	if (b0 < 0x80) {
		return 1;
	}
	int w;
	if ((b0 & 0xE0) == 0xC0) {
		w = 2;
	} else if ((b0 & 0xF0) == 0xE0) {
		w = 3;
	} else if ((b0 & 0xF8) == 0xF0) {
		w = 4;
	} else {
		return 1; // 非法前导字节
	}
	if (w > n) {
		return 1; // 截断
	}
	for (int k = 1; k < w; k++) {
		if (((unsigned char)s[k] & 0xC0) != 0x80) {
			return 1; // 非法后续字节
		}
	}
	return w;
}

int cre2_match_all(const cre2_re *h, const char *text, int textlen, int nmatch, int maxn, int **out, int *nmatches) {
	*out = NULL;
	*nmatches = 0;
	const char *base = text ? text : "";
	re2::StringPiece full(base, textlen);
	std::vector<re2::StringPiece> sub(nmatch);
	std::vector<int> acc; // flat: 每处匹配 2*nmatch 个 int
	int end = textlen;
	int count = 0;
	int prevMatchEnd = -1;
	// 逐处匹配的循环整体留在 C 内 (原 Go allMatches 每处一次 cgo, 此处零 cgo).
	// pos/i/prevMatchEnd 推进与 stdlib regexp.allMatches 逐字一致.
	for (int pos = 0; (maxn < 0 || count < maxn) && pos <= end;) {
		bool ok = h->re->Match(full, (size_t)pos, (size_t)textlen, RE2::UNANCHORED, sub.data(), nmatch);
		if (!ok) {
			break;
		}
		// group0 在成功匹配时必参与, data() 非 NULL.
		int m0 = (int)(sub[0].data() - base);
		int m1 = m0 + (int)sub[0].size();
		bool accept = true;
		if (m1 == m0) {
			// 空匹配: 紧贴上一处匹配末尾的空匹配丢弃, 避免重复; 按 rune 宽度推进 pos.
			if (m0 == prevMatchEnd) {
				accept = false;
			}
			int width = utf8WidthGo(base + pos, end - pos);
			if (width > 0) {
				pos += width;
			} else {
				pos = end + 1;
			}
		} else {
			pos = m1;
		}
		prevMatchEnd = m1;
		if (accept) {
			for (int i = 0; i < nmatch; i++) {
				if (sub[i].data() == nullptr) {
					acc.push_back(-1);
					acc.push_back(-1);
				} else {
					int b = (int)(sub[i].data() - base);
					acc.push_back(b);
					acc.push_back(b + (int)sub[i].size());
				}
			}
			count++;
		}
	}
	if (count == 0) {
		return 0;
	}
	int *buf = (int *)malloc(sizeof(int) * acc.size());
	if (buf == NULL) {
		return -1;
	}
	for (size_t i = 0; i < acc.size(); i++) {
		buf[i] = acc[i];
	}
	*out = buf;
	*nmatches = count;
	return 1;
}

cre2_replace_result cre2_find_replace_within(const cre2_re *find, const cre2_re *strip, const char *text,
                                             int textlen, const char *repl, int replen) {
	cre2_replace_result res;
	res.rc = 1;
	res.changed = 0;
	res.outlen = 0;
	res.out = NULL;
	const char *base = text ? text : "";
	re2::StringPiece full(base, textlen);
	re2::StringPiece rewrite(repl ? repl : "", replen);
	int end = textlen;
	int lastMatchEnd = 0;
	bool dirty = false; // 是否已遇到第一处改变字节的替换 (未脏前不建 result, 不分配)
	std::string result;
	re2::StringPiece g0[1]; // 只取 group0(整体匹配); find 是否有捕获组都不影响, 不退 submatch 跟踪
	// 推进/写入条件与 Go replaceAllString(= stdlib regexp.replaceAll) 逐字一致, 整循环留在 C 内.
	for (int searchPos = 0; searchPos <= end;) {
		bool ok = find->re->Match(full, (size_t)searchPos, (size_t)textlen, RE2::UNANCHORED, g0, 1);
		if (!ok) {
			break;
		}
		int m0 = (int)(g0[0].data() - base);
		int m1 = m0 + (int)g0[0].size();
		bool applied = (m1 > lastMatchEnd || m0 == 0); // 同 Go 写入条件 (空匹配去重)
		if (applied) {
			std::string seg(base + m0, (size_t)(m1 - m0));
			RE2::GlobalReplace(&seg, *strip->re, rewrite); // 内层替换在 C 内, 不回 Go
			// 本段替换后是否真的改变了字节 (明文动词 ignore 删 0 分隔符 → 不变).
			bool segChanged = seg.size() != (size_t)(m1 - m0) ||
			                  memcmp(seg.data(), base + m0, seg.size()) != 0;
			if (!dirty) {
				if (segChanged) {
					// 首次改动: 物化 result, 补 [0:m0] 前缀 (此前内容逐字节 = 原串, 直接拷原串).
					dirty = true;
					result.reserve((size_t)textlen);
					result.append(base, (size_t)m0);
					result.append(seg);
				} // else: 仍无改动, 不建 result, 继续扫
			} else {
				result.append(base + lastMatchEnd, (size_t)(m0 - lastMatchEnd));
				result.append(seg);
			}
		} else if (dirty) {
			// applied=false (空匹配跳过): 段不写, 只补 gap, 与 always-build 的推进一致.
			result.append(base + lastMatchEnd, (size_t)(m0 - lastMatchEnd));
		}
		lastMatchEnd = m1;
		int width = utf8WidthGo(base + searchPos, end - searchPos);
		if (searchPos + width > m1) {
			searchPos += width;
		} else if (searchPos + 1 > m1) {
			searchPos++;
		} else {
			searchPos = m1;
		}
	}
	if (!dirty) {
		return res; // 无任何字节改动: changed=0, out=NULL, 调用方用原串 (零分配)
	}
	result.append(base + lastMatchEnd, (size_t)(end - lastMatchEnd));
	size_t sz = result.size();
	char *buf = (char *)malloc(sz ? sz : 1); // 空结果也给 1 字节占位 (changed=1 但结果为空串)
	if (buf == NULL) {
		res.rc = -1;
		return res;
	}
	memcpy(buf, result.data(), sz);
	res.changed = 1;
	res.outlen = (int)sz;
	res.out = buf;
	return res;
}

void cre2_free(cre2_re *h) {
	if (h == nullptr) {
		return;
	}
	delete h->re;
	delete h;
}

// ── RE2::Set 包装 ────────────────────────────────────────────────────────────
struct cre2_set {
	RE2::Set *set;
};

cre2_set *cre2_set_new(void) {
	RE2::Options opt;
	opt.set_log_errors(false);
	cre2_set *h = new (std::nothrow) cre2_set;
	if (h == nullptr) {
		return nullptr;
	}
	h->set = new (std::nothrow) RE2::Set(opt, RE2::UNANCHORED);
	if (h->set == nullptr) {
		delete h;
		return nullptr;
	}
	return h;
}

int cre2_set_add(cre2_set *h, const char *pat, int patlen) {
	re2::StringPiece sp(pat, patlen);
	return h->set->Add(sp, NULL); // 返回 index 或 -1(解析失败)
}

int cre2_set_compile(cre2_set *h) { return h->set->Compile() ? 1 : 0; }

int cre2_set_match(const cre2_set *h, const char *text, int textlen, int *out, int outcap) {
	const char *base = text ? text : ""; // 空串也喂合法指针(同 cre2_match_at)
	re2::StringPiece sp(base, textlen);
	std::vector<int> v; // 无命中时 RE2 不填 → 空 vector 不分配
	if (!h->set->Match(sp, &v)) {
		return 0;
	}
	int n = (int)v.size();
	int m = n < outcap ? n : outcap;
	for (int i = 0; i < m; i++) {
		out[i] = v[i];
	}
	return n;
}

void cre2_set_free(cre2_set *h) {
	if (h == nullptr) {
		return;
	}
	delete h->set;
	delete h;
}

} // extern "C"
