// cre2.cpp — cre2.h 的实现, 直接调 vendored RE2 (2023-03-01, 无 abseil).
#include "cre2.h"
#include "cre2_internal.h"
#include "re2/re2.h"
#include "re2/set.h"
// 反着扫要直接用 re2 的内部件: Regexp::Parse + Regexp::CompileToReverseProg + Prog::SearchDFA。
// 走 RE2 对象本身不行 —— 它的 rprog_ 是从【剥掉必需前缀之后】的 suffix_regexp_ 编的,
// 只在"已经知道匹配右端在哪"的场景下用, 拿来当整篇非锚定搜索会漏。
#include "re2/prog.h"
#include "re2/regexp.h"
#include "re2/stringpiece.h"
#include <cstdlib>
#include <cstring>
#include <map>
#include <mutex>
#include <new>
#include <string>
#include <vector>

struct cre2_re {
	RE2 *re;
	int64_t max_mem;         // 这个 handle 编译时用的 RE2::Options::max_mem
	// 反向程序惰性建: 大多数 handle 一辈子不走反向, 不该白付一次编译。
	re2::Regexp *rre;        // pattern 的独立解析 (只给 rprog 用)
	re2::Prog *rprog;        // 反向程序; 建失败留 NULL → 退回正向
	std::once_flag ronce;
};

extern "C" {

cre2_re *cre2_new_max_mem(const char *pat, int patlen, int64_t max_mem) {
	re2::StringPiece sp(pat, patlen);
	RE2::Options opt;
	opt.set_log_errors(false); // 别往 stderr 喷, 错误走 cre2_error 取
	if (max_mem > 0) {
		opt.set_max_mem(max_mem); // <=0 保持 RE2 默认 kDefaultMaxMem=8MB
	}
	cre2_re *h = new (std::nothrow) cre2_re();
	if (h == nullptr) {
		return nullptr;
	}
	h->re = new RE2(sp, opt);
	h->max_mem = h->re->options().max_mem(); // 回读: <=0 时是 RE2 填的默认值
	h->rre = nullptr;
	h->rprog = nullptr;
	return h;
}

cre2_re *cre2_new(const char *pat, int patlen) { return cre2_new_max_mem(pat, patlen, 0); }

int64_t cre2_max_mem(const cre2_re *h) { return h->max_mem; }

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

/* cre2_match_all_step: 见 cre2.h 的契约。
 *
 * 循环体与 cre2_match_all 【逐字相同】(pos 推进 · 空匹配去重 · utf8WidthGo · 未参与组 -1,-1),
 * 唯二的差别:
 *   ① 结果写进调用方的 outBuf, 不进 std::vector, 收尾也不 malloc/拷贝;
 *   ② 填满 outCapMatches 处就【挂起】返回 (done=0), 把 pos/prevEnd 交回给调用方下次传进来。
 * 挂起不需要保存任何 native 状态 —— 每处匹配本来就是一次独立的 h->re->Match(full, pos, …),
 * DFA 状态与 cache 锁都在那一次 Match 内部生灭, 循环切在哪一处都不影响结果。
 *
 * 🔴 这两份实现【没有】共用同一段代码, 而且两个都会长期留着 (分工见 cre2.h 里那段)。
 *    让 cre2_match_all 绕道本函数会给它凭空加一次拷贝, 拿本函数绕道它则要先攒完整张表 ——
 *    两个契约不同, 硬合成一份只会两头都变慢。防止语义漂移靠的是对拍门:
 *    match_step_test.go 拿 batch=1/2/3 强制切在批边界上 (跨批携带的 pos/prevEnd 是唯一会
 *    静默出错的地方), 与 FindAllStringSubmatchIndex 以及 stdlib regexp 三方逐处逐组对拍。 */
cre2_match_step_result cre2_match_all_step(const cre2_re *h, const char *text, int textlen, int nmatch,
                                           int maxn_left, int pos, int prevEnd,
                                           int *outBuf, int outCapMatches) {
	cre2_match_step_result res;
	res.rc = 1;
	res.nmatches = 0;
	res.pos = pos;
	res.prevEnd = prevEnd;
	res.done = 1; /* 只有"缓冲填满而不是扫完"那一条出口才会改成 0 */
	if (h == NULL || nmatch < 1 || outBuf == NULL || outCapMatches < 1 || textlen < 0 || pos < 0) {
		res.rc = 0;
		return res;
	}
	if (maxn_left == 0) {
		return res; /* 额度已用尽: 一处不填, 且已经结束 */
	}
	const char *base = text ? text : "";
	re2::StringPiece full(base, textlen);
	std::vector<re2::StringPiece> sub(nmatch);
	int end = textlen;
	int count = 0; /* 本批已【接受】的匹配处数 */
	int w = 0;     /* outBuf 写游标 (int 数) */
	while (pos <= end) {
		if (count >= outCapMatches) {
			res.done = 0; /* 缓冲满 —— 正文还没扫完, 调用方要再 step 一次 */
			break;
		}
		if (maxn_left > 0 && count >= maxn_left) {
			break; /* 本次额度用尽; done 保持 1 */
		}
		bool ok = h->re->Match(full, (size_t)pos, (size_t)textlen, RE2::UNANCHORED, sub.data(), nmatch);
		if (!ok) {
			break;
		}
		/* group0 在成功匹配时必参与, data() 非 NULL. */
		int m0 = (int)(sub[0].data() - base);
		int m1 = m0 + (int)sub[0].size();
		bool accept = true;
		if (m1 == m0) {
			/* 空匹配: 紧贴上一处匹配末尾的空匹配丢弃, 避免重复; 按 rune 宽度推进 pos. */
			if (m0 == prevEnd) {
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
		prevEnd = m1;
		if (accept) {
			for (int i = 0; i < nmatch; i++) {
				if (sub[i].data() == nullptr) {
					outBuf[w++] = -1;
					outBuf[w++] = -1;
				} else {
					int b = (int)(sub[i].data() - base);
					outBuf[w++] = b;
					outBuf[w++] = b + (int)sub[i].size();
				}
			}
			count++;
		}
	}
	res.nmatches = count;
	res.pos = pos;
	res.prevEnd = prevEnd;
	return res;
}

/* ── 实验中 (2026-08-26): 缓冲改由 C 侧持有, 见 cre2.h 的契约。 ──────────────────────
 * 循环体与 cre2_match_all_step 逐字相同, 只在"第一次要写命中"处插了一句惰性 malloc。 */
cre2_match_step_alloc_result cre2_match_all_step_alloc(const cre2_re *h, const char *text, int textlen, int nmatch,
                                                       int maxn_left, int pos, int prevEnd,
                                                       int *inBuf, int capMatches) {
	cre2_match_step_alloc_result res;
	res.rc = 1;
	res.nmatches = 0;
	res.pos = pos;
	res.prevEnd = prevEnd;
	res.done = 1;
	res.buf = inBuf;
	if (h == NULL || nmatch < 1 || capMatches < 1 || textlen < 0 || pos < 0) {
		res.rc = 0;
		return res;
	}
	if (maxn_left == 0) {
		return res;
	}
	const char *base = text ? text : "";
	re2::StringPiece full(base, textlen);
	std::vector<re2::StringPiece> sub(nmatch);
	int *out = inBuf;
	int end = textlen;
	int count = 0;
	int w = 0;
	while (pos <= end) {
		if (count >= capMatches) {
			res.done = 0;
			break;
		}
		if (maxn_left > 0 && count >= maxn_left) {
			break;
		}
		bool ok = h->re->Match(full, (size_t)pos, (size_t)textlen, RE2::UNANCHORED, sub.data(), nmatch);
		if (!ok) {
			break;
		}
		int m0 = (int)(sub[0].data() - base);
		int m1 = m0 + (int)sub[0].size();
		bool accept = true;
		if (m1 == m0) {
			if (m0 == prevEnd) {
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
		prevEnd = m1;
		if (accept) {
			if (out == NULL) {
				/* 惰性: 到这里才知道真的有命中。miss 路径一分钱不花。 */
				out = (int *)malloc(sizeof(int) * (size_t)capMatches * 2 * (size_t)nmatch);
				if (out == NULL) {
					res.rc = 0;
					return res;
				}
				res.buf = out;
			}
			for (int i = 0; i < nmatch; i++) {
				if (sub[i].data() == nullptr) {
					out[w++] = -1;
					out[w++] = -1;
				} else {
					int b = (int)(sub[i].data() - base);
					out[w++] = b;
					out[w++] = b + (int)sub[i].size();
				}
			}
			count++;
		}
	}
	res.nmatches = count;
	res.pos = pos;
	res.prevEnd = prevEnd;
	return res;
}

void cre2_step_buf_free(int *p) {
	free(p);
}

void cre2_nop(void) {
}

void cre2_malloc_free_roundtrip(int n, int nbytes) {
	volatile int sink = 0;
	for (int i = 0; i < n; i++) {
		void *p = malloc((size_t)nbytes);
		if (p == NULL) {
			return;
		}
		((char *)p)[0] = (char)i; /* 防止编译器把 malloc/free 对消掉 */
		sink += ((char *)p)[0];
		free(p);
	}
	(void)sink;
}

/* 保留 (不是待删除): 本函数服务 FindAll* 那个"一次吐完数组"的契约 —— 在 C 里数好个数再一次
 * 精确 malloc, Go 侧据此一次精确 make, 是该契约下的最优解。用 step 物化反而 +17% CPU / 分配翻 4 倍
 * (数字见 cre2.h 上面那段与 match_step_bench_test.go 的 BenchmarkFindAllSub_matAll_vs_step)。
 * 不需要全部物化的调用方走 cre2_match_all_step。 */
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

cre2_match_all_result cre2_match_all_r(const cre2_re *h, const char *text, int textlen, int nmatch, int maxn) {
	cre2_match_all_result res;
	res.out = NULL;
	res.nmatches = 0;
	res.rc = cre2_match_all(h, text, textlen, nmatch, maxn, &res.out, &res.nmatches);
	return res;
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

cre2_replace_result cre2_replace_all_literal(const cre2_re *re, const char *text, int textlen,
                                             const char *repl, int replen) {
	cre2_replace_result res;
	res.rc = 1;
	res.changed = 0;
	res.outlen = 0;
	res.out = NULL;
	const char *base = text ? text : "";
	re2::StringPiece full(base, textlen);
	const char *rp = repl ? repl : "";
	int end = textlen;
	int lastMatchEnd = 0;
	bool dirty = false; // 是否已遇到第一处改变字节的替换 (未脏前不建 result, 不分配)
	std::string result;
	re2::StringPiece g0[1]; // 只取 group0; repl 是字面串(不解释 \1/$1), 无需跟踪子组
	// 推进/写入条件与 Go replaceAllString(= stdlib regexp.replaceAll) 逐字一致, 整循环留在 C 内.
	for (int searchPos = 0; searchPos <= end;) {
		bool ok = re->re->Match(full, (size_t)searchPos, (size_t)textlen, RE2::UNANCHORED, g0, 1);
		if (!ok) {
			break;
		}
		int m0 = (int)(g0[0].data() - base);
		int m1 = m0 + (int)g0[0].size();
		bool applied = (m1 > lastMatchEnd || m0 == 0); // 同 Go 写入条件 (空匹配去重)
		if (applied) {
			// 本处替换是否真的改变了字节 (repl 与命中段 [m0,m1) 逐字节相同 → 不变).
			bool segChanged = (m1 - m0) != replen || memcmp(base + m0, rp, (size_t)replen) != 0;
			if (!dirty) {
				if (segChanged) {
					// 首次改动: 物化 result, 补 [0:m0] 前缀 (此前内容逐字节 = 原串, 直接拷原串).
					dirty = true;
					result.reserve((size_t)textlen);
					result.append(base, (size_t)m0);
					result.append(rp, (size_t)replen);
				} // else: 仍无改动, 不建 result, 继续扫
			} else {
				result.append(base + lastMatchEnd, (size_t)(m0 - lastMatchEnd));
				result.append(rp, (size_t)replen);
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
	delete h->rprog;
	if (h->rre != nullptr) {
		h->rre->Decref();
	}
	delete h->re;
	delete h;
}

// ── 反着扫 (单条) ────────────────────────────────────────────────────────────
// 惰性把 pattern 再解析一遍并编成【反向程序】。用完整 pattern 而不是 RE2 的 suffix_regexp_,
// 理由见文件头的 include 注释。编不出来就留 NULL, 调用方退回正向 —— 反向只是加速手段, 不是语义。
static re2::Prog *cre2_rev_prog(const cre2_re *h) {
	cre2_re *m = const_cast<cre2_re *>(h);
	std::call_once(m->ronce, [m]() {
		if (!m->re->ok()) {
			return;
		}
		re2::Regexp::ParseFlags pf = static_cast<re2::Regexp::ParseFlags>(m->re->options().ParseFlags());
		re2::RegexpStatus st;
		re2::StringPiece pat(m->re->pattern());
		re2::Regexp *rre = re2::Regexp::Parse(pat, pf, &st);
		if (rre == nullptr) {
			return; // RE2 自己已经编过一遍了, 走到这里说明 pattern 本来就坏 —— re->ok() 会是 false
		}
		re2::Prog *prog = rre->CompileToReverseProg(m->max_mem);
		if (prog == nullptr) {
			rre->Decref();
			return;
		}
		m->rre = rre;
		m->rprog = prog;
	});
	return m->rprog;
}

cre2_rev_match_result cre2_partial_match_reverse(const cre2_re *h, const char *text, int textlen, int want_stats) {
	cre2_rev_match_result r;
	memset(&r, 0, sizeof r);
	const char *base = text ? text : ""; // 空串也喂合法指针 (同 cre2_match_at)
	re2::StringPiece sp(base, textlen);
	re2::Prog *prog = cre2_rev_prog(h);
	if (prog != nullptr) {
		bool failed = false;
		re2::DFAScanStats st;
		// text==context: 让 ^ / $ / \b 看到的是整篇正文的边界。
		// SearchDFA 内部对 reversed_ 程序会把 caret/dollar 换回来, 并在 pattern 带 ^ 时
		// 走 endmatch 检查 (要求反向搜索的落点正好是 text 起点) —— 锚定语义不用我们自己补。
		bool ok = prog->SearchDFA(sp, sp, re2::Prog::kUnanchored, re2::Prog::kFirstMatch, NULL, &failed, NULL,
		                          want_stats ? &st : NULL);
		if (!failed) {
			r.Matched = ok ? 1 : 0;
			if (want_stats) {
				r.Stats.Flushes = st.flushes;
				r.Stats.Grows = st.grows;
				r.Stats.StatesBuilt = st.states_built;
				r.Stats.Bytes = st.bytes;
				r.Stats.StatesEnd = st.states_end;
				r.Stats.StateBudget = st.state_budget;
				r.Stats.MemLeft = st.mem_left;
			}
			return r;
		}
		// failed = 反向 DFA 中途放弃 (预算不够, RE2 对 kFirstMatch 有"造状态太慢就 bail"的启发式)。
	}
	r.FellBack = 1;
	r.Matched = RE2::PartialMatch(sp, *h->re) ? 1 : 0;
	return r;
}

// ── RE2::Set 包装 ────────────────────────────────────────────────────────────
// struct cre2_set 的定义在 cre2_internal.h (cre2_spanscan.cpp 也要用)。

cre2_set *cre2_set_new_ex(int64_t max_mem, int reversed) {
	RE2::Options opt;
	opt.set_log_errors(false);
	if (max_mem > 0) {
		opt.set_max_mem(max_mem); // <=0 保持 RE2 默认 kDefaultMaxMem=8MB
	}
	cre2_set *h = new (std::nothrow) cre2_set;
	if (h == nullptr) {
		return nullptr;
	}
	h->set = new (std::nothrow) RE2::Set(opt, RE2::UNANCHORED, reversed != 0);
	if (h->set == nullptr) {
		delete h;
		return nullptr;
	}
	return h;
}

cre2_set *cre2_set_new(int64_t max_mem) { return cre2_set_new_ex(max_mem, 0); }

int cre2_set_reversed(const cre2_set *h) { return h->set->reversed() ? 1 : 0; }

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

int cre2_set_match_any(const cre2_set *h, const char *text, int textlen) {
	const char *base = text ? text : "";
	re2::StringPiece sp(base, textlen);
	// v=NULL: Set::Match 不建 SparseSet → Prog::SearchDFA 里 matches==NULL 把 want_earliest_match
	// 打开 → DFA 命中第一个位置就返回, 正文剩下的字节根本不看。DFA 缓存与 cre2_set_match 共用
	// (kManyMatch 那一份), 所以这条快路径不会额外占一份状态缓存。
	return h->set->Match(sp, NULL) ? 1 : 0;
}

int cre2_set_match_stats(const cre2_set *h, const char *text, int textlen,
                         int *out, int outcap, cre2_scan_stats *st) {
	const char *base = text ? text : "";
	re2::StringPiece sp(base, textlen);
	std::vector<int> v;
	re2::DFAScanStats s;
	bool ok = h->set->Match(sp, &v, NULL, st ? &s : NULL);
	if (st != nullptr) {
		st->Flushes = s.flushes;
		st->Grows = s.grows;
		st->StatesBuilt = s.states_built;
		st->Bytes = s.bytes;
		st->StatesEnd = s.states_end;
		st->StateBudget = s.state_budget;
		st->MemLeft = s.mem_left;
	}
	if (!ok) {
		return 0;
	}
	int n = (int)v.size();
	int m = n < outcap ? n : outcap;
	for (int i = 0; i < m; i++) {
		out[i] = v[i];
	}
	return n;
}

void cre2_set_mem_info(const cre2_set *h, cre2_set_mem *out) {
	re2::DFAMemInfo mi;
	h->set->MemInfo(&mi);
	out->Built = mi.built ? 1 : 0;
	out->StateBudget = mi.state_budget;
	out->MemLeft = mi.mem_left;
	out->States = mi.states;
	out->ArenaCap = mi.arena_cap;
	out->FlushesTotal = mi.flushes_total;
	out->StatesBuiltTotal = mi.states_built_total;
}

void cre2_set_attrib_info(const cre2_set *h, cre2_set_attrib *agg,
                          int64_t *pat_states, int64_t *pat_insts, int cap) {
	re2::DFAAttribInfo ai;
	ai.pat_states = pat_states;
	ai.pat_insts = pat_insts;
	ai.pat_cap = (pat_states != nullptr || pat_insts != nullptr) ? cap : 0;
	h->set->AttribInfo(&ai);
	agg->Enabled = ai.enabled ? 1 : 0;
	agg->Built = ai.built ? 1 : 0;
	agg->NPat = ai.npat;
	agg->StatesTotal = ai.states_total;
	agg->SharedInsts = ai.shared_insts;
	agg->NInstSum = ai.ninst_sum;
	agg->NInstMax = ai.ninst_max;
	memcpy(agg->NInstHist, ai.ninst_hist, sizeof agg->NInstHist);
	memcpy(agg->BirthHist, ai.birth_hist, sizeof agg->BirthHist);
}

void cre2_set_free(cre2_set *h) {
	if (h == nullptr) {
		return;
	}
	delete h->set;
	delete h;
}

} // extern "C"
