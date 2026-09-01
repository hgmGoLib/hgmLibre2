// cre2_re2set.cpp — fll / rrl / frel 三个算法的【同一台机器】。
//
// 一遍扫正文, 直接交出各条 pattern 的【不重叠命中区间】(index, start, end)。三个口径各挑
// 一个坐标当锚点, 这是它们唯一的真区别:
//
//   fll   第一趟【正向】· leftmost-longest       起点最靠左 · 同起点最长   同条内 start 升序
//   rrl   第一趟【反向】· rightmost-longest      起点最靠右 · 同起点最长   同条内 start 降序
//   frel  第一趟【正向】· rightmost-END-longest  终点最靠右 · 同终点最长   同条内 start 升序
//
// 🔴 rrl 与 frel 都带个 "r", 但锚的坐标不一样: rrl 锚【起点】, frel 锚【终点】(名字里那个 e
//    就是这件事的标记)。b|abc 撞 "abc": frel 给 "abc", rrl 给中间那个 "b"。
//
// ── 一套底座, 三个收口策略 ──────────────────────────────────────────────────
//   ① 扫正文      整表 set 的 kManyMatch DFA, 一遍            (re2_dfa_spanscan_inl.h)
//   ② 切分量      per-pattern 存活位由活转死就收口一个分量    (同上, g2 档)
//   ③ 攒游程      留在 native 不过桥, 按分量整块交付          (同上)
//   ④ 反向锚定    这一条自己的单条对象, 回推左端              (cre2_resolve_span_reverse_r)
//   ⑤ 正向锚定    这一条自己的单条 longest 对象, 验候选取最长右端 (cre2_match_at_anchored)
//
//   frel  在分量里从【最右终点】起算 ⟹ ④ 取最靠左的左端, 上界压到它的左端, 继续往左
//   fll   在分量里从【最左起点】起算 ⟹ 定长走减法; 变长先收候选起点再升序拿 ⑤ 验
//   rrl   同 fll 的机器, 但跑在【反向 set】上: 第一趟交出来的就是左端, 一趟 ⑤ 就够
//
// 🔴 ② 只在【正向 set】上有 (DFASpanScan::Begin 里 `g_span_ = run_forward_ && gspan`),
//    所以 rrl 走的是不切分量的 g1 档。这不是偷懒: rrl 的回看上界本来就是它自己的游标,
//    分量左界压不出更紧的东西来 —— 它每处命中恒等于"一趟 ⑤", 没有 fll 那种"一个右端
//    问一次回看"的浪费可省。fll / frel 才真的靠 ② 把回看次数压到"分量里真有几处不重叠匹配"。
//
// 🔴 ④⑤ 一律走【这一条 pattern 自己的单条对象】, 扫完正文之后一趟都不再回 set
//    (2026-08-27 定的总规矩): 单条走 RE2::Match 那条完整的路 (DFA 放弃还有 OnePass/
//    BitState/NFA 下家), set 那侧的锚定解析是 kManyMatch 的 DFA 独一条没有下家;
//    而且单条不必背每个状态那张 id 表, 状态更小, 也不去冲刷整表那份大缓存。
//    这些单条对象缓存在【表】上 (cre2_set_one_fwd / cre2_set_one_viable), 不在工作区里 ——
//    工作区是每 goroutine 一份, 挂进去就是把 9.6MB 乘以池子份数。
//
// 🔴 本文件【无条件】假设"每个匹配至少 1 字节"(能匹配空串的 pattern 在编译入口就被拒了,
//    见 emptymatch.go)。所以这里没有任何一处"零长匹配"的兜底分支。
//
// existonly 是【每遍】的参数, 不是建对象时定死的属性: 名单上的那几条只报"命中没命中",
// 不攒游程 · 不盯存活位 · 不收口 · 不补端点 (g2 档), 或只是不补端点 (rrl 的 g1 档)。
// 门上只当短路 bool 用的位一律该进这张名单。
//
// Go 门面在 re2set_common.go / re2set_fll.go / re2set_rrl.go / re2set_frel.go。

#include "cre2.h"
#include "cre2_internal.h"
#include "re2_span_scan.h"

#include <new>
#include <string>
#include <vector>

// out 数组是 Go 那侧的切片直接指过来的 (Re2Set_startEnd_t), 两边必须是同一个布局:
// 三个 int32, 没有洞。Go 那侧也有一条对应的编译期断言。
static_assert(sizeof(cre2_re2set_result) == 12, "cre2_re2set_result 必须是 3 个紧挨着的 int32");

struct cre2_re2set {
	cre2_set *set; // 【借】的, 不持有: set 必须活得比工作区久 (Go 侧存引用保住)
	int mode;
	int n;
	re2::DFASpanScan *ss;

	std::vector<int32_t> minlen, maxlen;    // 每条的匹配字节长度区间 (Go 侧算好传下来)
	std::vector<unsigned char> existonly;   // 本遍的名单 (每次 begin 刷新)
	std::vector<unsigned char> hit;         // rrl 自己维护的命中表 (g1 档 native 不管这个)

	// 本单元 (一个分量 / 一条游程) 结算出来的区间, 3 个 int32 一条
	std::vector<int32_t> seg;
	size_t segi;

	// g2 档: 上一次 step 交出来、还没结算的分量
	const re2::DFASpanScanG2Rec *recs;
	int nrec, reci;

	// g1 档 (rrl): 上一次 step 收到的游程 (id, lo, hi)
	std::vector<int32_t> runbuf;
	int nrun, runi;

	// rrl 的逐条推进游标 (fll/frel 的游标是分量级的, 不跨分量, 所以不需要这一份)
	std::vector<int32_t> cur;
	std::vector<unsigned char> started;

	std::vector<int32_t> cands; // fll 收候选起点的缓冲, 不够翻倍, 翻上去就留着

	int scandone, failed;
	long long walks, ncand, tries, emits;
	std::string err;
	int badidx;
};

// Emit: 把一处收口出来的区间记进本单元的结算缓冲。
static inline void Emit(cre2_re2set *s, int32_t id, int32_t start, int32_t end) {
	s->seg.push_back(id);
	s->seg.push_back(start);
	s->seg.push_back(end);
	s->emits++;
}

// Fail: 记下整遍作废的原因。🔴 只记第一个, 后面的多半是它的连锁。
static bool Fail(cre2_re2set *s, const char *msg, int id) {
	if (s->err.empty()) {
		s->err = msg;
		s->badidx = id;
	}
	s->failed = 1;
	return false;
}

// IsFixed: 定长 (起点唯一, 一句加减法, 不进正则引擎)。
static inline bool IsFixed(const cre2_re2set *s, int id) {
	return s->maxlen[id] >= 0 && s->minlen[id] == s->maxlen[id];
}

// ── frel: 分量按【最右终点最长】结算 ────────────────────────────────────────
//
// r.runs 是这一分量里【匹配右端】的游程 (升序, 互不相接), r.lo 是分量左界 —— 上一次这条
// pattern 断气的位置, 分量内任何匹配的左端都 >= r.lo, 所以它就是反向锚定的 bound, 不必
// 一路回看到正文开头。这是存活位除了切分量之外白送的第二笔。
//
// 从最右的右端起: 反向锚定拿【最靠左】的左端 (= 这个右端上最长的那个匹配), 收下, 然后把
// 上界 limit 压到刚拿到的左端 —— 下一处必须整个落在它左边。倒着找出来的, 最后翻成升序。
static bool FrelCloseSeg(cre2_re2set *s, const char *text, int textlen,
                         const re2::DFASpanScanG2Rec &r) {
	int k = r.nrun - 1;
	if (k < 0) {
		return true;
	}
	const cre2_re *re = cre2_set_one_fwd(s->set, r.id);
	if (re == NULL) {
		return Fail(s, "re2 re2set: 补端点要的【单条对象】编不出来 (maxMem 配小了), 把它调大", r.id);
	}
	const int32_t *rs = r.runs;
	const size_t base = s->seg.size();
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
		s->walks++;
		s->tries++;
		if (rr.rc < 0) {
			return Fail(s, "re2 re2set: 反向锚定解析失败 (反向程序编不出来, 或 DFA 放弃); "
			               "用更大的 maxMem 重建", r.id);
		}
		// rc==0: 这个右端上伸不出匹配。按分量的定义走不到这里 (set 报出来的右端上一定有
		// 匹配, 而且它的左端一定 >= 分量左界), 真走到了就退一格接着找, 不整遍作废。
		if (rr.rc == 0) {
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
		Emit(s, r.id, rr.pos, e);
		limit = rr.pos; // 下一处必须整个落在这一处左边 ⇒ 它的右端 <= 这一处的左端
	}
	// 倒着找出来的, 翻成升序 (同一条 pattern 交出来的区间按 start 升序)
	size_t i = base;
	size_t j = s->seg.size();
	while (j - i >= 6) {
		j -= 3;
		for (int t = 0; t < 3; t++) {
			int32_t tmp = s->seg[i + t];
			s->seg[i + t] = s->seg[j + t];
			s->seg[j + t] = tmp;
		}
		i += 3;
	}
	return true;
}

// FllViableStarts: 把 [bound, from) 里全部候选起点收进 s->cands, 返回条数; <0 = 失败。
// 缓冲不够就翻倍重来一趟 —— 里面写下的是最大的那几个 (恰好最没用), 整批作废没有损失。
static int FllViableStarts(cre2_re2set *s, const cre2_set *vp, const char *text, int textlen,
                           int32_t from, int32_t bound, int id) {
	for (int round = 0; round < 2; round++) {
		int need = cre2_set_viable_starts(vp, text, textlen, from, bound, 0,
		                                  &s->cands[0], (int)s->cands.size());
		if (need < 0) {
			Fail(s, "re2 re2set: 可行前缀回推失败 (反向单条 set 的 DFA 放弃); "
			        "用更大的 maxMem 重建", id);
			return -1;
		}
		if (need <= (int)s->cands.size()) {
			return need;
		}
		s->cands.assign((size_t)need * 2, 0); // 翻倍留余量: 下一个右端多半也是这个量级
	}
	Fail(s, "re2 re2set: 候选缓冲扩容后仍然不够 —— 这是【本库的 bug】", id);
	return -1;
}

// ── fll: 分量按【最左起点最长】结算 ─────────────────────────────────────────
//
// 游标从分量左界 r.lo 起 (分量的定义就是"没有任何匹配能跨过这个位置", 所以这一分量里
// 任何匹配的左端都 >= r.lo)。逐个右端往右推:
//   ① 右端落在已吐出去那一处里面 (e <= cur) → 跳过;
//   ② 定长: 起点唯一 (e - minlen), 一句减法, 不进正则引擎;
//   ③ 变长: 反向【种全部状态】收齐 [cur, e) 里的全部候选起点, 【从小到大】逐个拿正向
//      longest 锚定去验, 第一个验过的就是答案 (leftmost), 而 longest 给的右端就是最长的
//      那个 ⟹ 严格 leftmost-longest;
//   ④ 一个都没验过 ⟹ [cur, e) 里根本没有起点, 游标直接推到 e。
//
// 🔴 ③ 的回看窗口下界掐在 cur 上是【正确性】不是省钱: 不掐的话推出来的起点会与刚吐出去
//    的那一处相交, 那一处就得整个丢掉 = 无声漏报。
static bool FllCloseSeg(cre2_re2set *s, const char *text, int textlen,
                        const re2::DFASpanScanG2Rec &r) {
	if (r.nrun <= 0) {
		return true;
	}
	const int id = r.id;
	const bool fixed = IsFixed(s, id);
	const cre2_re *fwd = NULL;
	const cre2_set *vp = NULL;
	if (!fixed) {
		fwd = cre2_set_one_fwd(s->set, id);
		if (fwd == NULL) {
			return Fail(s, "re2 re2set: 补端点要的【正向单条】编不出来 (maxMem 配小了), 把它调大", id);
		}
		vp = cre2_set_one_viable(s->set, id);
		if (vp == NULL) {
			return Fail(s, "re2 re2set: 补端点要的【反向单条 set】编不出来 (maxMem 配小了), 把它调大", id);
		}
	}
	int32_t cur = r.lo;
	for (int k = 0; k < r.nrun; k++) {
		const int32_t rlo = r.runs[2 * k];
		const int32_t rhi = r.runs[2 * k + 1];
		for (int32_t e = rlo; e <= rhi; e++) {
			if (e <= cur) {
				continue;
			}
			if (fixed) {
				int32_t st = e - s->minlen[id];
				if (st < cur) {
					continue; // 与已吐出去那一处相交
				}
				Emit(s, id, st, e);
				cur = e;
				continue;
			}
			int need = FllViableStarts(s, vp, text, textlen, e, cur, id);
			if (need < 0) {
				return false;
			}
			s->walks++;
			s->ncand += need;
			bool got = false;
			// 缓冲是降序的, 所以倒着走 = 候选从小到大。
			for (int t = need - 1; t >= 0; t--) {
				int32_t st = s->cands[t];
				s->tries++;
				int m[2];
				if (cre2_match_at_anchored(fwd, text, textlen, (int)st, textlen, m, 1) == 0) {
					continue; // 这条"可行前缀"只是张空头支票, 试下一个
				}
				Emit(s, id, st, (int32_t)m[1]);
				cur = (int32_t)m[1];
				got = true;
				break;
			}
			if (!got) {
				cur = e; // 一个都没验过 ⟹ [cur, e) 里根本没有起点
			}
		}
	}
	return true;
}

// ── rrl: 一条【左端】游程按【最右起点最长】结算 ─────────────────────────────
//
// 反向 set 第一趟交出来的就是左端 = 起点, 而 rightmost-longest 这个口径本来就定义在起点
// 上, 所以没有"收候选再逐个验"那一步 —— 每处命中恒等于一趟正向锚定。
//   ① 左端落在已吐出去那一处里面 (st >= cur) → 跳过;
//   ② 否则从 st 起锚定取最长右端, 上界【掐在 cur 上】—— 绝不越过游标 (正确性: 不掐就会
//      与刚吐出去的那一处相交, 而对外承诺的是"同一条内部互不相交");
//   ③ 取不到 (这个左端在游标底下伸不出完整匹配) → 跳过; 取到就吐, cur 退到 st。
static bool RrlCloseRun(cre2_re2set *s, const char *text, int textlen,
                        int32_t id, int32_t lo, int32_t hi) {
	if (!s->started[id]) {
		s->started[id] = 1;
		s->cur[id] = textlen; // 游标从正文【末尾】起, 往左退
	}
	const bool fixed = IsFixed(s, id);
	const cre2_re *fwd = NULL;
	if (!fixed) {
		fwd = cre2_set_one_fwd(s->set, id);
		if (fwd == NULL) {
			return Fail(s, "re2 re2set: 补端点要的【正向单条】编不出来 (maxMem 配小了), 把它调大", id);
		}
	}
	for (int32_t st = hi; st >= lo; st--) {
		if (st >= s->cur[id]) {
			continue;
		}
		if (fixed) {
			int32_t end = st + s->minlen[id];
			if (end > s->cur[id]) {
				continue; // 会与已吐出去那一处相交
			}
			Emit(s, id, st, end);
			s->cur[id] = st;
			continue;
		}
		int m[2];
		s->walks++;
		s->tries++;
		if (cre2_match_at_anchored(fwd, text, textlen, (int)st, (int)s->cur[id], m, 1) == 0) {
			continue;
		}
		Emit(s, id, st, (int32_t)m[1]);
		s->cur[id] = st;
	}
	return true;
}

extern "C" {

cre2_re2set *cre2_re2set_new(cre2_set *set, int mode, const int32_t *minlen,
                             const int32_t *maxlen, int n) {
	cre2_re2set *s = new (std::nothrow) cre2_re2set;
	if (s == NULL) {
		return NULL;
	}
	s->set = set;
	s->mode = mode;
	s->n = n;
	s->ss = NULL;
	s->segi = 0;
	s->recs = NULL;
	s->nrec = 0;
	s->reci = 0;
	s->nrun = 0;
	s->runi = 0;
	s->scandone = 1;
	s->failed = 1; // 还没 begin
	s->walks = 0;
	s->ncand = 0;
	s->tries = 0;
	s->emits = 0;
	s->badidx = -1;
	if (set == NULL || set->set == NULL || n < 0 ||
	    (n > 0 && (minlen == NULL || maxlen == NULL))) {
		s->err = "re2 re2set: 参数不对";
		return s;
	}
	if (mode != CRE2_RE2SET_fll && mode != CRE2_RE2SET_rrl && mode != CRE2_RE2SET_frel) {
		s->err = "re2 re2set: mode 不认识";
		return s;
	}
	if ((mode == CRE2_RE2SET_rrl) != (cre2_set_reversed(set) != 0)) {
		s->err = "re2 re2set: rrl 要反向 set, fll/frel 要正向 set";
		return s;
	}
	s->minlen.assign(minlen, minlen + (n > 0 ? n : 0));
	s->maxlen.assign(maxlen, maxlen + (n > 0 ? n : 0));
	s->existonly.assign(n > 0 ? n : 0, 0);
	s->hit.assign(n > 0 ? n : 1, 0);
	s->cur.assign(n > 0 ? n : 0, 0);
	s->started.assign(n > 0 ? n : 0, 0);
	s->cands.assign(64, 0); // 真表上一个右端的候选通常是个位数, 这个数只是让翻倍基本不发生
	if (n > 0) {
		s->ss = set->set->NewSpanScan();
		if (s->ss == NULL) {
			s->err = "re2 re2set: 扫描工作区建不出来 (没 Compile / DFA 建不出来 / OOM)";
			return s;
		}
		// g1 档 (rrl) 的 out 必须 >= 3*n, 见 re2_span_scan.h。
		s->runbuf.assign((size_t)n * 3, 0);
	}
	s->err.clear();
	return s;
}

void cre2_re2set_free(cre2_re2set *s) {
	if (s == NULL) {
		return;
	}
	if (s->ss != NULL) {
		re2::DFASpanScanFree(s->ss);
	}
	delete s; // set 是借的, 不释放
}

const char *cre2_re2set_error(const cre2_re2set *s, int *badidx) {
	if (s == NULL) {
		return "re2 re2set: 空句柄";
	}
	if (badidx != NULL) {
		*badidx = s->badidx;
	}
	return s->err.empty() ? NULL : s->err.c_str();
}

int cre2_re2set_begin(cre2_re2set *s, int textlen, const unsigned char *existonly) {
	if (s == NULL || textlen < 0) {
		return 0;
	}
	s->seg.clear();
	s->segi = 0;
	s->recs = NULL;
	s->nrec = 0;
	s->reci = 0;
	s->nrun = 0;
	s->runi = 0;
	s->scandone = 0;
	s->failed = 0;
	s->walks = 0;
	s->ncand = 0;
	s->tries = 0;
	s->emits = 0;
	s->badidx = -1;
	s->err.clear();
	for (int i = 0; i < s->n; i++) {
		s->existonly[i] = (existonly != NULL && existonly[i]) ? 1 : 0;
		s->hit[i] = 0;
		s->started[i] = 0;
		s->cur[i] = 0;
	}
	if (s->n == 0 || s->ss == NULL) {
		s->scandone = 1; // 空表: 合法的一遍, 什么都不给
		return s->ss == NULL && s->n > 0 ? 0 : 1;
	}
	bool ok;
	if (s->mode == CRE2_RE2SET_rrl) {
		ok = re2::DFASpanScanBegin(s->ss, textlen);
	} else {
		// existonly 是【每遍】的名单, 所以这里开也要关 —— 不关就把上一遍的名单粘过来。
		for (int i = 0; i < s->n; i++) {
			re2::DFASpanScanG2BoolOnly(s->ss, i, s->existonly[i]);
		}
		ok = re2::DFASpanScanBeginG2(s->ss, textlen);
	}
	if (!ok) {
		s->err = "re2 re2set: begin 失败";
		s->failed = 1;
		return 0;
	}
	return 1;
}

int cre2_re2set_step(cre2_re2set *s, const char *text, int textlen,
                     cre2_re2set_result *out, int cap, int *more) {
	if (s == NULL || more == NULL || out == NULL || cap <= 0) {
		return -1;
	}
	*more = 0;
	if (s->failed) {
		return -1;
	}
	if (s->n == 0 || s->ss == NULL) {
		return 0;
	}
	const char *base = text != NULL ? text : "";
	int k = 0;
	for (;;) {
		// ① 先把上一个单元结算出来的区间倒进调用方的数组
		while (s->segi < s->seg.size() && k < cap) {
			out[k].index = s->seg[s->segi];
			out[k].start = s->seg[s->segi + 1];
			out[k].end = s->seg[s->segi + 2];
			s->segi += 3;
			k++;
		}
		if (s->segi >= s->seg.size()) {
			s->seg.clear();
			s->segi = 0;
		}
		if (k >= cap) {
			*more = 1;
			return k;
		}
		// ② 结算下一个单元 (一次一个: 结算缓冲最多只装一个单元的量)
		if (s->mode == CRE2_RE2SET_rrl) {
			if (s->runi < s->nrun) {
				int b = s->runi * 3;
				s->runi++;
				int32_t id = s->runbuf[b];
				if (id < 0 || id >= s->n) {
					continue;
				}
				s->hit[id] = 1;
				if (s->existonly[id]) {
					continue; // 只要位: 一次端点都不补
				}
				if (!RrlCloseRun(s, base, textlen, id, s->runbuf[b + 1], s->runbuf[b + 2])) {
					return -1;
				}
				continue;
			}
		} else {
			if (s->reci < s->nrec) {
				const re2::DFASpanScanG2Rec &r = s->recs[s->reci++];
				if (r.id < 0 || r.id >= s->n) {
					continue;
				}
				bool ok = (s->mode == CRE2_RE2SET_frel) ? FrelCloseSeg(s, base, textlen, r)
				                                        : FllCloseSeg(s, base, textlen, r);
				if (!ok) {
					return -1;
				}
				continue;
			}
		}
		// ③ 这一批单元吃完了, 再向扫描那侧要一批
		if (s->scandone) {
			*more = 0;
			return k;
		}
		int mo = 0;
		int32_t dummy = 0;
		// g2 档一个字节都不往 out 写, 给个占位就够 (见 re2_span_scan.h)。
		int32_t *sout = s->mode == CRE2_RE2SET_rrl ? &s->runbuf[0] : &dummy;
		int soutcap = s->mode == CRE2_RE2SET_rrl ? (int)s->runbuf.size() : 1;
		int got = re2::DFASpanScanStep(s->ss, base, textlen, sout, soutcap, &mo);
		if (got < 0) {
			Fail(s, "re2 re2set: 扫描失败 (DFA 放弃), 整遍作废; 用更大的 maxMem 重建", -1);
			return -1;
		}
		if (s->mode == CRE2_RE2SET_rrl) {
			s->nrun = got;
			s->runi = 0;
		} else {
			s->nrec = re2::DFASpanScanG2Closed(s->ss, &s->recs);
			s->reci = 0;
		}
		if (!mo) {
			s->scandone = 1;
		}
	}
}

const unsigned char *cre2_re2set_hits(const cre2_re2set *s) {
	if (s == NULL || s->n == 0) {
		return NULL;
	}
	if (s->mode == CRE2_RE2SET_rrl) {
		return &s->hit[0]; // g1 档 native 不维护命中表, 这一份是本文件按游程记的
	}
	if (s->ss == NULL) {
		return NULL;
	}
	return re2::DFASpanScanG2Hits(s->ss);
}

void cre2_re2set_stats(const cre2_re2set *s, long long *walks, long long *cands,
                       long long *tries, long long *emits, long long *nseg,
                       long long *usedpeak, long long *heappeak, long long *poolbytes) {
	if (s == NULL) {
		return;
	}
	if (s->ss != NULL) {
		// g1 档 (rrl) 这四个数恒 0 —— 它不切分量, 游程也不留 native。
		re2::DFASpanScanG2Stats(s->ss, usedpeak, heappeak, poolbytes, nseg);
	}
	if (walks != NULL) {
		*walks = s->walks;
	}
	if (cands != NULL) {
		*cands = s->ncand;
	}
	if (tries != NULL) {
		*tries = s->tries;
	}
	if (emits != NULL) {
		*emits = s->emits;
	}
}

} // extern "C"
