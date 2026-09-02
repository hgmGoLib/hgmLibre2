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
//    这些单条对象缓存在 cre2_re2set 上 (OneFwd / OneViable), 惰性建 · 内部加锁 ——
//    cre2_re2set 是【进程级】的, 一个策略一份, 活多久缓存就活多久。
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

#include <atomic>
#include <mutex>
#include <new>
#include <string>
#include <vector>

// out 数组是 Go 那侧的切片直接指过来的 (Re2Set_startEnd_t), 两边必须是同一个布局:
// 三个 int32, 没有洞。Go 那侧也有一条对应的编译期断言。
static_assert(sizeof(cre2_re2set_result) == 12, "cre2_re2set_result 必须是 3 个紧挨着的 int32");

// ── 两层, 分得死死的 ────────────────────────────────────────────────────────
//
//   cre2_re2set       【进程级】· new 之后只读 (除了 one_mu 护着的惰性缓存) · 可并发用
//   cre2_re2set_scan  【一遍扫描一份】· malloc/free · 一遍完了就没了
//
// 🔴 这条线不许再模糊: cre2_re2set 上一个字节的"这一遍"状态都不能有, 否则并发扫同一个
//    策略就是互相踩。反过来, 每遍的暂存也不许上提 —— 那就成了池子, 拿常驻内存换一点
//    malloc, 而 glibc 的 tcache/smallbin 本来就是那个效果 (这里每遍二十来次 malloc,
//    最大一块 3.8KB, 离 128KB 的 mmap 门槛远得很, 一次都不进内核)。

struct cre2_re2set {
	// ref: 建出来是 1; cre2_re2set_free 是【减一】。每开一遍扫描加一, 那一遍结束减一 ——
	// 所以策略换表的时候 Close 掉旧的, 手上还在跑的那几遍照样能安全跑完, 最后一个走的人
	// 关灯 (语义同 cre2_set 的 ref, 见 cre2_internal.h)。
	std::atomic<int> ref{1};

	cre2_set *set; // 【持有一份引用】: new 时 cre2_set_ref, free 时 cre2_set_free
	int mode;
	int n;

	std::vector<int32_t> minlen, maxlen; // 每条的匹配字节长度区间 (Go 侧算好传下来)

	std::string err; // 建的时候定的, 之后不动
	int badidx;

	// ── 补端点用的【单条对象】缓存 (惰性 · 加锁 · 一个 cre2_re2set 一份) ──────────
	//
	// one_fwd[i]    第 i 条 pattern 自己那条【正向 · longest 口径】的 RE2 对象。
	//               fll/rrl 拿它锚定取最长右端; frel 拿它的【反向程序】做反向锚定
	//               (cre2_resolve_span_reverse_r) —— longest 只改搜索 kind 不改 ParseFlags,
	//               所以那条反向程序与默认口径编出来的逐字相同, 一份够三家用。
	// one_viable[i] 第 i 条 pattern 自己那条【反向 · 只装这一条】的 set, fll 收候选起点用。
	//               必须是 set 不是单条: 单条 Compile 会把 ^/$ 摘成标志, 自己驱动 DFA 查不到。
	//
	// *_no[i] = 试过且建不出来, 记下来免得每遍扫描重编一次 (失败是确定性的)。
	//
	// 🔴 它必须挂在这一层, 不能挂进【一遍扫描】那一层: 最大那张生产规则表的反向单条缓存实测
	//    9.6MB, 挂进去就是每扫一篇正文重编一遍 9.6MB 再扔掉。挂这里则是一个策略一份,
	//    所有并发扫描共用, 策略活多久它活多久。
	std::mutex one_mu;
	std::vector<cre2_re *> one_fwd;
	std::vector<unsigned char> one_fwd_no;
	std::vector<cre2_set *> one_viable;
	std::vector<unsigned char> one_viable_no;
};

struct cre2_re2set_scan {
	cre2_re2set *own; // 【持有一份引用】: scan_new 时 +1, scan_free 时 -1
	re2::DFASpanScan *ss;

	// 🔴 下面这些按 n 开的表【一律按 mode 只开自己那一档要的】: 这个结构一篇正文一开一关,
	//    多开一张就是每篇正文多一次 malloc + n 字节 memset。谁归谁在各自的注释里写死了。

	std::vector<unsigned char> existonly; // 〔rrl〕本遍的名单 (fll/frel 只在 scan_new 读参数)
	std::vector<unsigned char> hit;       // 〔rrl〕自己维护的命中表 (g1 档 native 不管这个)

	// 本单元 (一个分量 / 一条游程) 结算出来的区间, 3 个 int32 一条
	std::vector<int32_t> seg;
	size_t segi;

	// g2 档: 上一次 step 交出来、还没结算的分量
	const re2::DFASpanScanG2Rec *recs;
	int nrec, reci;

	// 〔rrl〕g1 档: 上一次 step 收到的游程 (id, lo, hi)
	std::vector<int32_t> runbuf;
	int nrun, runi;

	// 〔rrl〕逐条推进游标 (fll/frel 的游标是分量级的, 不跨分量, 所以不需要这一份)
	std::vector<int32_t> cur;
	std::vector<unsigned char> started;

	// 〔fll〕收候选起点的缓冲, 不够翻倍, 翻上去这一遍里就留着。
	// 惰性开 (见 FllViableStarts): 全是定长条 / 整篇没命中的正文一分不付。
	std::vector<int32_t> cands;

	int scandone, failed;
	long long walks, ncand, tries, emits;
	std::string err;
	int badidx;
};

// ── 单条对象: 惰性建 · 加锁 · 挂在 cre2_re2set 上 ────────────────────────────
// 建不出来返回 NULL 并记住, 不会每遍重编。返回的指针有效期到 cre2_re2set 被拆掉。

// OneFwd: 第 i 条 pattern 自己那条【正向 · longest】对象。
static const cre2_re *OneFwd(cre2_re2set *o, int i) {
	if (o == NULL || o->set == NULL || i < 0 || (size_t)i >= o->set->pats.size()) {
		return NULL;
	}
	std::lock_guard<std::mutex> lk(o->one_mu);
	if (o->one_fwd.empty()) {
		o->one_fwd.assign(o->set->pats.size(), NULL);
		o->one_fwd_no.assign(o->set->pats.size(), 0);
	}
	if (o->one_fwd[i] != NULL) {
		return o->one_fwd[i];
	}
	if (o->one_fwd_no[i]) {
		return NULL;
	}
	const std::string &p = o->set->pats[i];
	cre2_re *re = cre2_new_longest_max_mem(p.data(), (int)p.size(), o->set->max_mem);
	if (re == NULL) {
		o->one_fwd_no[i] = 1;
		return NULL;
	}
	const char *e = cre2_error(re); // 没错时是空串, 不是 NULL
	if (cre2_ok(re) == 0 || (e != NULL && *e != 0)) {
		cre2_free(re);
		o->one_fwd_no[i] = 1;
		return NULL;
	}
	o->one_fwd[i] = re;
	return re;
}

// OneViable: 第 i 条 pattern 自己那条【反向 · 只装这一条】的 set (给 cre2_set_viable_starts)。
static const cre2_set *OneViable(cre2_re2set *o, int i) {
	if (o == NULL || o->set == NULL || i < 0 || (size_t)i >= o->set->pats.size()) {
		return NULL;
	}
	std::lock_guard<std::mutex> lk(o->one_mu);
	if (o->one_viable.empty()) {
		o->one_viable.assign(o->set->pats.size(), NULL);
		o->one_viable_no.assign(o->set->pats.size(), 0);
	}
	if (o->one_viable[i] != NULL) {
		return o->one_viable[i];
	}
	if (o->one_viable_no[i]) {
		return NULL;
	}
	cre2_set *one = cre2_set_new_ex(o->set->max_mem, 1);
	if (one == NULL) {
		o->one_viable_no[i] = 1;
		return NULL;
	}
	const std::string &p = o->set->pats[i];
	if (cre2_set_add(one, p.data(), (int)p.size()) != 0 || cre2_set_compile(one) == 0) {
		cre2_set_free(one);
		o->one_viable_no[i] = 1;
		return NULL;
	}
	o->one_viable[i] = one;
	return one;
}

// Emit: 把一处收口出来的区间记进本单元的结算缓冲。
static inline void Emit(cre2_re2set_scan *s, int32_t id, int32_t start, int32_t end) {
	s->seg.push_back(id);
	s->seg.push_back(start);
	s->seg.push_back(end);
	s->emits++;
}

// Fail: 记下整遍作废的原因。🔴 只记第一个, 后面的多半是它的连锁。
static bool Fail(cre2_re2set_scan *s, const char *msg, int id) {
	if (s->err.empty()) {
		s->err = msg;
		s->badidx = id;
	}
	s->failed = 1;
	return false;
}

// IsFixed: 定长 (起点唯一, 一句加减法, 不进正则引擎)。
static inline bool IsFixed(const cre2_re2set_scan *s, int id) {
	return s->own->maxlen[id] >= 0 && s->own->minlen[id] == s->own->maxlen[id];
}

// ── frel: 分量按【最右终点最长】结算 ────────────────────────────────────────
//
// r.runs 是这一分量里【匹配右端】的游程 (升序, 互不相接), r.lo 是分量左界 —— 上一次这条
// pattern 断气的位置, 分量内任何匹配的左端都 >= r.lo, 所以它就是反向锚定的 bound, 不必
// 一路回看到正文开头。这是存活位除了切分量之外白送的第二笔。
//
// 从最右的右端起: 反向锚定拿【最靠左】的左端 (= 这个右端上最长的那个匹配), 收下, 然后把
// 上界 limit 压到刚拿到的左端 —— 下一处必须整个落在它左边。倒着找出来的, 最后翻成升序。
static bool FrelCloseSeg(cre2_re2set_scan *s, const char *text, int textlen,
                         const re2::DFASpanScanG2Rec &r) {
	int k = r.nrun - 1;
	if (k < 0) {
		return true;
	}
	const cre2_re *re = OneFwd(s->own, r.id);
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
static int FllViableStarts(cre2_re2set_scan *s, const cre2_set *vp, const char *text, int textlen,
                           int32_t from, int32_t bound, int id) {
	if (s->cands.empty()) {
		// 惰性开: 只有变长条才会走到这里, 定长条 (一句减法) 和整篇没命中的正文一分不付。
		// 64 是"真表上一个右端的候选通常是个位数"来的, 这个数只是让下面那次翻倍基本不发生。
		s->cands.assign(64, 0);
	}
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
static bool FllCloseSeg(cre2_re2set_scan *s, const char *text, int textlen,
                        const re2::DFASpanScanG2Rec &r) {
	if (r.nrun <= 0) {
		return true;
	}
	const int id = r.id;
	const bool fixed = IsFixed(s, id);
	const cre2_re *fwd = NULL;
	const cre2_set *vp = NULL;
	if (!fixed) {
		fwd = OneFwd(s->own, id);
		if (fwd == NULL) {
			return Fail(s, "re2 re2set: 补端点要的【正向单条】编不出来 (maxMem 配小了), 把它调大", id);
		}
		vp = OneViable(s->own, id);
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
				int32_t st = e - s->own->minlen[id];
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
static bool RrlCloseRun(cre2_re2set_scan *s, const char *text, int textlen,
                        int32_t id, int32_t lo, int32_t hi) {
	if (!s->started[id]) {
		s->started[id] = 1;
		s->cur[id] = textlen; // 游标从正文【末尾】起, 往左退
	}
	const bool fixed = IsFixed(s, id);
	const cre2_re *fwd = NULL;
	if (!fixed) {
		fwd = OneFwd(s->own, id);
		if (fwd == NULL) {
			return Fail(s, "re2 re2set: 补端点要的【正向单条】编不出来 (maxMem 配小了), 把它调大", id);
		}
	}
	for (int32_t st = hi; st >= lo; st--) {
		if (st >= s->cur[id]) {
			continue;
		}
		if (fixed) {
			int32_t end = st + s->own->minlen[id];
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
	cre2_re2set *o = new (std::nothrow) cre2_re2set;
	if (o == NULL) {
		return NULL;
	}
	o->set = NULL; // 参数不对就直接返回, 那时还没引用 set
	o->mode = mode;
	o->n = n;
	o->badidx = -1;
	if (set == NULL || set->set == NULL || n < 0 ||
	    (n > 0 && (minlen == NULL || maxlen == NULL))) {
		o->err = "re2 re2set: 参数不对";
		return o;
	}
	if (mode != CRE2_RE2SET_fll && mode != CRE2_RE2SET_rrl && mode != CRE2_RE2SET_frel) {
		o->err = "re2 re2set: mode 不认识";
		return o;
	}
	if ((mode == CRE2_RE2SET_rrl) != (cre2_set_reversed(set) != 0)) {
		o->err = "re2 re2set: rrl 要反向 set, fll/frel 要正向 set";
		return o;
	}
	cre2_set_ref(set); // 从这里起表活得比 o 久, 与 Go 那侧存不存引用无关
	o->set = set;
	o->minlen.assign(minlen, minlen + (n > 0 ? n : 0));
	o->maxlen.assign(maxlen, maxlen + (n > 0 ? n : 0));
	if (n > 0) {
		// 探一下就扔, 图的是【建不出来的表在建策略这里就报错】, 而不是等第一篇正文才发现。
		// 🔴 别指望它顺带热身: NewSpanScan 内部那句 GetDFA 只 new 出 DFA 对象和它的状态区
		//    (re2_dfa.cc: Prog::GetDFA), 状态是搜索的时候才一个个造的 —— 第一篇正文那笔
		//    状态构建照样要付, 挪不走。真扫描用的工作区也是每遍自己开的。
		re2::DFASpanScan *probe = set->set->NewSpanScan();
		if (probe == NULL) {
			o->err = "re2 re2set: 扫描工作区建不出来 (没 Compile / DFA 建不出来 / OOM)";
			return o;
		}
		re2::DFASpanScanFree(probe);
	}
	o->err.clear();
	return o;
}

// cre2_re2set_free: 引用【减一】, 减到 0 才真拆 (连同单条缓存和表的那份引用)。
// 可以对同一个句柄调多次 —— 每次配一份引用。手上还在跑的扫描各攥着一份, 所以换策略的时候
// 调用方尽管放手, 最后一个走的人关灯。
void cre2_re2set_free(cre2_re2set *o) {
	if (o == NULL) {
		return;
	}
	if (o->ref.fetch_sub(1, std::memory_order_acq_rel) != 1) {
		return;
	}
	for (size_t i = 0; i < o->one_fwd.size(); i++) {
		if (o->one_fwd[i] != NULL) {
			cre2_free(o->one_fwd[i]);
		}
	}
	for (size_t i = 0; i < o->one_viable.size(); i++) {
		if (o->one_viable[i] != NULL) {
			cre2_set_free(o->one_viable[i]);
		}
	}
	cre2_set_free(o->set); // 引用减一, 不一定真拆
	delete o;
}

const char *cre2_re2set_error(const cre2_re2set *o, int *badidx) {
	if (o == NULL) {
		return "re2 re2set: 空句柄";
	}
	if (badidx != NULL) {
		*badidx = o->badidx;
	}
	return o->err.empty() ? NULL : o->err.c_str();
}

// cre2_re2set_one_viable_stats: 【已经被建出来】的那些反向单条 set 的账 (条数 · 状态数 ·
// 状态区字节)。不制造状态 —— 量具不制造被量的东西。
void cre2_re2set_one_viable_stats(cre2_re2set *o, int *n, long long *states,
                                  long long *arenacap) {
	if (n != NULL) {
		*n = 0;
	}
	if (states != NULL) {
		*states = 0;
	}
	if (arenacap != NULL) {
		*arenacap = 0;
	}
	if (o == NULL) {
		return;
	}
	std::lock_guard<std::mutex> lk(o->one_mu);
	for (size_t k = 0; k < o->one_viable.size(); k++) {
		if (o->one_viable[k] == NULL) {
			continue;
		}
		cre2_set_mem mi;
		cre2_set_mem_info(o->one_viable[k], &mi);
		if (n != NULL) {
			(*n)++;
		}
		if (states != NULL) {
			*states += mi.States;
		}
		if (arenacap != NULL) {
			*arenacap += mi.ArenaCap;
		}
	}
}

// ── 一遍扫描 ────────────────────────────────────────────────────────────────

cre2_re2set_scan *cre2_re2set_scan_new(cre2_re2set *o, int textlen,
                                       const unsigned char *existonly) {
	if (o == NULL || textlen < 0) {
		return NULL;
	}
	cre2_re2set_scan *s = new (std::nothrow) cre2_re2set_scan;
	if (s == NULL) {
		return NULL;
	}
	o->ref.fetch_add(1, std::memory_order_relaxed);
	s->own = o;
	s->ss = NULL;
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
	const int n = o->n > 0 ? o->n : 0;
	const int mode = o->mode;
	// ── 这几张表【只有 rrl 用】────────────────────────────────────────────────
	// fll/frel 一张都不碰: 它们的命中表在 spanscan 里 (ghit_), 游标是分量级的不跨分量,
	// 游程也不过桥 (g2 档整块留 native)。而这几张是按 n 开的, 每篇正文一开一关 —— 白开
	// 4 次 malloc + 18n 字节 memset, 在短正文上占得见 (见 doc 里那张固定开销表)。
	//
	// 🔴 摆在【出错路之前】: 下面任何一条早退都会让调用方接着调 hits/stats, 而
	//    cre2_re2set_scan_hits 在 rrl 上是 &hit[0] 取地址, 表是空的就是越界。
	//    (mode 不认识那条早退走的是 else, 那时 hits 会因为 ss==NULL 返回 NULL, 不取地址。)
	if (mode == CRE2_RE2SET_rrl) {
		s->hit.assign(n > 0 ? n : 1, 0);
		s->cur.assign(n, 0);
		s->started.assign(n, 0);
		// existonly 只有 g1 档的 step 每条游程要查一次, 所以只有这一档留一份副本;
		// fll/frel 那侧只在下面开 boolOnly 的时候读一遍参数, 读完就不再问了。
		s->existonly.assign(n, 0);
		for (int i = 0; i < n; i++) {
			s->existonly[i] = (existonly != NULL && existonly[i]) ? 1 : 0;
		}
	}
	if (!o->err.empty()) {
		// 没建成的对象上开扫描: 把建的时候那句话原样带过来, 别让调用方去问另一个句柄。
		s->err = o->err;
		s->badidx = o->badidx;
		s->failed = 1;
		s->scandone = 1;
		return s;
	}
	if (n == 0) {
		s->scandone = 1; // 空表: 合法的一遍, 什么都不给
		return s;
	}
	s->ss = o->set->set->NewSpanScan();
	if (s->ss == NULL) {
		s->err = "re2 re2set: 扫描工作区建不出来 (没 Compile / DFA 建不出来 / OOM)";
		s->failed = 1;
		return s;
	}
	bool ok;
	if (mode == CRE2_RE2SET_rrl) {
		// g1 档的 out 必须 >= 3*n, 见 re2_span_scan.h。g2 档一个字节都不往那儿写, 不用开。
		s->runbuf.assign((size_t)n * 3, 0);
		ok = re2::DFASpanScanBegin(s->ss, textlen);
	} else {
		// 工作区是新开的, gbool_ 本来就是全 0, 只把名单上的开起来即可。
		// 名单直接读参数 —— 这是 fll/frel 唯一一次问它, 不必留副本。
		if (existonly != NULL) {
			for (int i = 0; i < n; i++) {
				if (existonly[i]) {
					re2::DFASpanScanG2BoolOnly(s->ss, i, 1);
				}
			}
		}
		ok = re2::DFASpanScanBeginG2(s->ss, textlen);
	}
	if (!ok) {
		s->err = "re2 re2set: begin 失败";
		s->failed = 1;
	}
	return s;
}

void cre2_re2set_scan_free(cre2_re2set_scan *s) {
	if (s == NULL) {
		return;
	}
	if (s->ss != NULL) {
		re2::DFASpanScanFree(s->ss);
	}
	cre2_re2set_free(s->own); // 引用减一
	delete s;
}

const char *cre2_re2set_scan_error(const cre2_re2set_scan *s, int *badidx) {
	if (s == NULL) {
		return "re2 re2set: 空句柄";
	}
	if (badidx != NULL) {
		*badidx = s->badidx;
	}
	return s->err.empty() ? NULL : s->err.c_str();
}

int cre2_re2set_scan_step(cre2_re2set_scan *s, const char *text, int textlen,
                          cre2_re2set_result *out, int cap, int *more) {
	if (s == NULL || more == NULL || out == NULL || cap <= 0) {
		return -1;
	}
	*more = 0;
	if (s->failed) {
		return -1;
	}
	const int n = s->own->n;
	const int mode = s->own->mode;
	if (n == 0 || s->ss == NULL) {
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
		if (mode == CRE2_RE2SET_rrl) {
			if (s->runi < s->nrun) {
				int b = s->runi * 3;
				s->runi++;
				int32_t id = s->runbuf[b];
				if (id < 0 || id >= n) {
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
				if (r.id < 0 || r.id >= n) {
					continue;
				}
				bool ok = (mode == CRE2_RE2SET_frel) ? FrelCloseSeg(s, base, textlen, r)
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
		int32_t *sout = mode == CRE2_RE2SET_rrl ? &s->runbuf[0] : &dummy;
		int soutcap = mode == CRE2_RE2SET_rrl ? (int)s->runbuf.size() : 1;
		int got = re2::DFASpanScanStep(s->ss, base, textlen, sout, soutcap, &mo);
		if (got < 0) {
			Fail(s, "re2 re2set: 扫描失败 (DFA 放弃), 整遍作废; 用更大的 maxMem 重建", -1);
			return -1;
		}
		if (mode == CRE2_RE2SET_rrl) {
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

const unsigned char *cre2_re2set_scan_hits(const cre2_re2set_scan *s) {
	if (s == NULL || s->own->n == 0) {
		return NULL;
	}
	if (s->own->mode == CRE2_RE2SET_rrl) {
		return &s->hit[0]; // g1 档 native 不维护命中表, 这一份是本文件按游程记的
	}
	if (s->ss == NULL) {
		return NULL;
	}
	return re2::DFASpanScanG2Hits(s->ss);
}

void cre2_re2set_scan_stats(const cre2_re2set_scan *s, long long *walks, long long *cands,
                            long long *tries, long long *emits, long long *nseg,
                            long long *usedpeak, long long *heappeak) {
	if (s == NULL) {
		return;
	}
	if (s->ss != NULL) {
		// g1 档 (rrl) 这三个数恒 0 —— 它不切分量, 游程也不留 native。
		re2::DFASpanScanG2Stats(s->ss, usedpeak, heappeak, nseg);
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
