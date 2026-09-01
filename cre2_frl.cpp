// cre2_frl.cpp — Re2SetFrl 的引擎: 【F】irst-pass-forward + 【R】ightmost-【L】ongest。
//
// 一遍正向 set 扫描, 直接交出【不重叠的命中区间】(index, start, end), 口径是
// rightmost-longest。与 MatchScanner 的区别只在一件事上: 补起点那一步不再逐个右端
// 各问一次, 而是靠 DFA 状态里的【per-pattern 存活位】把一条 pattern 的命中切成
// 若干【分量】, 分量内部一次结算 —— 一处命中定下来之后, 下一处的搜索上界立刻被压到
// 它的左端, 所以一个分量里问几次反向锚定 = 这个分量里真的有几处不重叠的匹配。
//
// 三段分工 (每一段都在它最便宜的地方):
//   ① 扫正文       整表正向 set 的 kManyMatch DFA, 一遍           (re2_dfa_spanscan_inl.h)
//   ② 攒 + 切分量  存活位由活转死就收口, 游程留 native 不过桥      (同上, g2 档)
//   ③ 补起点       这一条 pattern 自己的【单条】对象, 反向锚定     (本文件 FrlCloseSeg)
// 🔴 ③ 走单条不走 set 是本库的一条总规矩 (2026-08-27 定的, 理由见 readme.txt):
//    单条走 RE2::Match 那条完整的路 (DFA 放弃还有 OnePass/BitState/NFA 下家), set 那侧的
//    锚定解析是 kManyMatch 的 DFA 独一条; 而且单条不必背每个状态那张 id 表, 状态更小。
//
// 只要"有没有命中"的那几条 (boolonly) 走的是另一条路: 命中时只置一个字节, 不攒游程、
// 不盯存活位、不收口、不补起点 —— 门上只当短路 bool 用的位一律该配这个。
//
// Go 侧门面在 re2setfrl.go。

#include "cre2.h"
#include "cre2_internal.h"
#include "re2_span_scan.h"

#include <new>
#include <string>
#include <vector>

struct cre2_frl {
	cre2_set *set;                 // 正向整表, 只用来扫
	re2::DFASpanScan *ss;
	int n;                         // pattern 条数
	int64_t maxmem;
	std::vector<std::string> pats; // 原文, 留着惰性编单条对象
	std::vector<unsigned char> boolonly;
	std::vector<cre2_re *> re;     // 逐条单条对象, 惰性建 (一辈子没命中的那条不编)

	// 本分量结算出来的区间, 3 个 int32 一条 (id, start, end), 升序
	std::vector<int32_t> seg;
	size_t segi;

	// 上一次 spanscan step 交出来、还没结算的分量
	const re2::DFASpanScanG2Rec *recs;
	int nrec;
	int reci;

	int scandone;                  // spanscan 那侧已经走完
	int failed;                    // 这一遍作废了
	long long nresolve;            // 统计: 反向锚定问了几次
	std::string err;
	int badidx;
};

// FrlReOf: 惰性把第 id 条编成【单条】对象。补起点全走它。
static cre2_re *FrlReOf(cre2_frl *f, int id) {
	if (id < 0 || id >= f->n) {
		return NULL;
	}
	if (f->re[id] != NULL) {
		return f->re[id];
	}
	cre2_re *re = cre2_new_max_mem(f->pats[id].data(), (int)f->pats[id].size(), f->maxmem);
	if (re == NULL) {
		f->err = "re2 frl: 单条对象建不出来 (OOM)";
		f->badidx = id;
		return NULL;
	}
	const char *e = cre2_error(re);   // 没错时是空串, 不是 NULL
	if (e != NULL && *e != '\0') {
		f->err = std::string("re2 frl: 单条编译失败: ") + e;
		f->badidx = id;
		cre2_free(re);
		return NULL;
	}
	f->re[id] = re;
	return re;
}

// FrlCloseSeg: 把一个分量按 rightmost-longest 结算掉, 结果 (升序) 落进 f->seg。
//
// r.runs 是这一分量里【匹配右端】的游程 (升序, 互不相接), r.lo 是分量左界 ——
// 上一次这条 pattern 断气的位置, 分量内任何匹配的左端都 >= r.lo, 所以它就是反向锚定的
// bound, 不必一路回看到正文开头。这是存活位除了切分量之外白送的第二笔。
//
// 从最右的右端起: 反向锚定拿【最靠左】的左端 (= 这个右端上最长的那个匹配), 收下,
// 然后把上界 limit 压到刚拿到的左端 —— 下一处必须整个落在它左边。倒着找出来的,
// 最后翻成升序。
static bool FrlCloseSeg(cre2_frl *f, const char *text, int textlen,
                        const re2::DFASpanScanG2Rec &r) {
	int k = r.nrun - 1;
	if (k < 0) {
		return true;
	}
	cre2_re *re = FrlReOf(f, r.id);
	if (re == NULL) {
		return false;
	}
	const int32_t *rs = r.runs;
	const size_t base = f->seg.size();
	int32_t limit = rs[2 * k + 1];
	while (k >= 0) {
		if (rs[2 * k] > limit) { // 整条游程都在 limit 右边, 整条跳过
			k--;
			if (k >= 0 && rs[2 * k + 1] < limit) {
				limit = rs[2 * k + 1];
			}
			continue;
		}
		int32_t e = rs[2 * k + 1];
		if (e > limit) {
			e = limit;
		}
		cre2_span_resolve_result rr = cre2_resolve_span_reverse_r(re, text, textlen, e, r.lo);
		f->nresolve++;
		if (rr.rc < 0) {
			f->err = "re2 frl: 反向锚定解析失败 (反向程序编不出来, 或 DFA 放弃); "
			         "用更大的 maxMem 重建";
			f->badidx = r.id;
			return false;
		}
		// rc==0: 这个右端上伸不出匹配。按分量的定义走不到这里 (set 报出来的右端上一定有
		// 匹配, 而且它的左端一定 >= 分量左界), 真走到了就退一格接着找, 不整遍作废。
		// rr.pos >= e 是零长匹配 —— 同样退一格, 否则 limit 不动会原地打转。
		if (rr.rc == 0 || rr.pos >= e) {
			if (e > rs[2 * k]) {
				limit = e - 1;
				continue;
			}
			k--;
			if (k >= 0) {
				limit = rs[2 * k + 1];
			}
			continue;
		}
		f->seg.push_back(r.id);
		f->seg.push_back(rr.pos);
		f->seg.push_back(e);
		limit = rr.pos; // 下一处必须整个落在这一处左边 ⇒ 它的右端 <= 这一处的左端
	}
	// 倒着找出来的, 翻成升序 (同一条 pattern 交出来的区间按 start 升序)
	size_t i = base;
	size_t j = f->seg.size();
	while (j - i >= 6) {
		j -= 3;
		for (int t = 0; t < 3; t++) {
			int32_t tmp = f->seg[i + t];
			f->seg[i + t] = f->seg[j + t];
			f->seg[j + t] = tmp;
		}
		i += 3;
	}
	return true;
}

extern "C" {

cre2_frl *cre2_frl_new(const char **pats, const int *patlens, const unsigned char *boolonly,
                       int n, int64_t max_mem) {
	cre2_frl *f = new (std::nothrow) cre2_frl;
	if (f == NULL) {
		return NULL;
	}
	f->set = NULL;
	f->ss = NULL;
	f->n = n;
	f->maxmem = max_mem;
	f->segi = 0;
	f->recs = NULL;
	f->nrec = 0;
	f->reci = 0;
	f->scandone = 1;
	f->failed = 1; // 还没 begin
	f->nresolve = 0;
	f->badidx = -1;
	if (n < 0 || (n > 0 && (pats == NULL || patlens == NULL))) {
		f->err = "re2 frl: 参数不对";
		return f;
	}
	f->re.assign(n > 0 ? n : 0, NULL);
	f->boolonly.assign(n > 0 ? n : 0, 0);
	f->set = cre2_set_new(max_mem);
	if (f->set == NULL) {
		f->err = "re2 frl: set 建不出来 (OOM)";
		return f;
	}
	for (int i = 0; i < n; i++) {
		f->pats.push_back(std::string(pats[i], (size_t)patlens[i]));
		if (boolonly != NULL) {
			f->boolonly[i] = boolonly[i] ? 1 : 0;
		}
		if (cre2_set_add(f->set, pats[i], patlens[i]) != i) {
			f->err = "re2 frl: pattern 解析失败";
			f->badidx = i;
			return f;
		}
	}
	if (n > 0 && cre2_set_compile(f->set) == 0) {
		f->err = "re2 frl: set 编译失败 (maxMem 不够, 调大它)";
		return f;
	}
	if (n > 0) {
		f->ss = f->set->set->NewSpanScan();
		if (f->ss == NULL) {
			f->err = "re2 frl: 扫描工作区建不出来 (DFA 建不出来 / OOM)";
			return f;
		}
		for (int i = 0; i < n; i++) {
			if (f->boolonly[i]) {
				re2::DFASpanScanG2BoolOnly(f->ss, i);
			}
		}
	}
	f->err.clear();
	return f;
}

void cre2_frl_free(cre2_frl *f) {
	if (f == NULL) {
		return;
	}
	if (f->ss != NULL) {
		re2::DFASpanScanFree(f->ss);
	}
	for (size_t i = 0; i < f->re.size(); i++) {
		if (f->re[i] != NULL) {
			cre2_free(f->re[i]);
		}
	}
	if (f->set != NULL) {
		cre2_set_free(f->set);
	}
	delete f;
}

const char *cre2_frl_error(const cre2_frl *f, int *badidx) {
	if (f == NULL) {
		return "re2 frl: 空句柄";
	}
	if (badidx != NULL) {
		*badidx = f->badidx;
	}
	return f->err.empty() ? NULL : f->err.c_str();
}

int cre2_frl_begin(cre2_frl *f, int textlen) {
	if (f == NULL || f->ss == NULL || textlen < 0) {
		return 0;
	}
	f->seg.clear();
	f->segi = 0;
	f->recs = NULL;
	f->nrec = 0;
	f->reci = 0;
	f->scandone = 0;
	f->failed = 0;
	f->nresolve = 0;
	f->badidx = -1;
	f->err.clear();
	if (!re2::DFASpanScanBeginG2(f->ss, textlen)) {
		f->err = "re2 frl: begin 失败";
		f->failed = 1;
		return 0;
	}
	return 1;
}

int cre2_frl_step(cre2_frl *f, const char *text, int textlen,
                  int32_t *index, int32_t *start, int32_t *end, int cap, int *more) {
	if (f == NULL || more == NULL || index == NULL || start == NULL || end == NULL || cap <= 0) {
		return -1;
	}
	*more = 0;
	if (f->failed || f->ss == NULL) {
		return -1;
	}
	int32_t dummy = 0;
	int n = 0;
	for (;;) {
		// ① 先把上一个分量结算出来的区间倒进调用方的数组
		while (f->segi < f->seg.size() && n < cap) {
			index[n] = f->seg[f->segi];
			start[n] = f->seg[f->segi + 1];
			end[n] = f->seg[f->segi + 2];
			f->segi += 3;
			n++;
		}
		if (f->segi >= f->seg.size()) {
			f->seg.clear();
			f->segi = 0;
		}
		if (n >= cap) {
			*more = 1;
			return n;
		}
		// ② 结算下一个已收口的分量 (一次一个: 内部缓冲最多只装一个分量的量)
		if (f->reci < f->nrec) {
			const re2::DFASpanScanG2Rec &r = f->recs[f->reci++];
			if (!FrlCloseSeg(f, text, textlen, r)) {
				f->failed = 1;
				return -1;
			}
			continue;
		}
		// ③ 这一批分量吃完了, 再向扫描那侧要一批
		if (f->scandone) {
			*more = 0;
			return n;
		}
		int mo = 0;
		// g2 档一个字节都不往 out 写, 所以这里给个占位就够 (见 span_scan.h)。
		if (re2::DFASpanScanStep(f->ss, text, textlen, &dummy, 1, &mo) < 0) {
			f->err = "re2 frl: 扫描失败 (DFA 放弃), 整遍作废; 用更大的 maxMem 重建";
			f->failed = 1;
			return -1;
		}
		f->nrec = re2::DFASpanScanG2Closed(f->ss, &f->recs);
		f->reci = 0;
		if (!mo) {
			f->scandone = 1;
		}
	}
}

const unsigned char *cre2_frl_hits(const cre2_frl *f) {
	if (f == NULL || f->ss == NULL) {
		return NULL;
	}
	return re2::DFASpanScanG2Hits(f->ss);
}

void cre2_frl_stats(const cre2_frl *f, long long *usedpeak, long long *heappeak,
                    long long *poolbytes, long long *nseg, long long *nresolve) {
	if (f == NULL) {
		return;
	}
	if (nresolve != NULL) {
		*nresolve = f->nresolve;
	}
	if (f->ss != NULL) {
		re2::DFASpanScanG2Stats(f->ss, usedpeak, heappeak, poolbytes, nseg);
	}
}

} // extern "C"
