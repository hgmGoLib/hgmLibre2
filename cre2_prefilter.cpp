// cre2_prefilter.cpp — 把 vendored 的 FilteredRE2 (re2_filtered_re2.cc + re2_prefilter*.cc) 接出 C ABI。
//
// ── 这是什么 ──────────────────────────────────────────────────────────────────
//
// RE2 的 prefilter 回答一个问题: 「这条 pattern 想命中, 正文里【必须】先出现哪些字面量?」
// 它从 pattern 的 AST 上推出一棵 AND-OR 树, 叶子是小写化、去重之后的原子串。调用方拿这些原子
// 去正文里找 (用自己喜欢的字符串匹配器 —— 本仓有 litscan 那台 AC), 找到哪几个, 再回来问
// 「那么现在有哪几条 pattern 还【可能】命中?」。没通过筛的 pattern 【保证】不命中, 可以整条跳过。
//
// 关键的第三个问题, 也是接这套出来的直接动机: **哪几条 pattern 是筛不掉的?**
// 有些 pattern 根本没有必需字面量 (纯字符类驱动, 形如 `[A-Za-z0-9+/=_-]{20,}` 或
// `(?-i:\([A-Z]{2,5}\))`), 它们无论正文长什么样都得跑。这批就是 PrefilterTree 的 unfiltered_,
// 而它决定了任何「前置粗筛」方案的天花板 —— 筛得掉的那部分再便宜, 也省不掉这批的钱。
// 这个数只有 RE2 自己的 prefilter 算得准: 手写的字面量抽取器会在 `(?:foo|[A-Z]{5})` 上答错
// (它含字面量 foo, 但整条【不可过滤】, 因为另一支不需要 foo)。
//
// ── 为什么单开一个文件 ────────────────────────────────────────────────────────
//
// cre2.cpp 已经装了 Regexp + Set + 反向扫三套东西。prefilter 与它们没有共享状态
// (FilteredRE2 自己持有一批独立的 RE2 对象), 塞进去只会让那个文件更难读。
#include "cre2.h"
#include "re2/filtered_re2.h"
#include "re2/re2.h"
#include "re2/stringpiece.h"
#include <new>
#include <string>
#include <vector>

struct cre2_prefilter {
	re2::FilteredRE2 *f;
	std::vector<std::string> atoms; // Compile 出来的原子表; 生命周期跟着 handle
	bool compiled;
	int64_t max_mem;
};

extern "C" {

cre2_prefilter *cre2_prefilter_new(int min_atom_len, int64_t max_mem) {
	cre2_prefilter *h = new (std::nothrow) cre2_prefilter();
	if (h == nullptr) {
		return nullptr;
	}
	// min_atom_len <= 0 走 FilteredRE2 的默认构造 (它内部的默认最小原子长度)。
	h->f = (min_atom_len > 0) ? new (std::nothrow) re2::FilteredRE2(min_atom_len)
	                          : new (std::nothrow) re2::FilteredRE2();
	if (h->f == nullptr) {
		delete h;
		return nullptr;
	}
	h->compiled = false;
	h->max_mem = max_mem;
	return h;
}

int cre2_prefilter_add(cre2_prefilter *h, const char *pat, int patlen) {
	if (h->compiled) {
		return -1; // Compile 之后不许再加 —— FilteredRE2 的契约
	}
	re2::StringPiece sp(pat, patlen);
	RE2::Options opt;
	opt.set_log_errors(false);
	if (h->max_mem > 0) {
		opt.set_max_mem(h->max_mem);
	}
	int id = -1;
	if (h->f->Add(sp, opt, &id) != RE2::NoError) {
		return -1;
	}
	return id;
}

int cre2_prefilter_compile(cre2_prefilter *h) {
	if (h->compiled) {
		return -1;
	}
	h->f->Compile(&h->atoms);
	h->compiled = true;
	return (int)h->atoms.size();
}

int cre2_prefilter_atom(const cre2_prefilter *h, int i, const char **p) {
	if (i < 0 || (size_t)i >= h->atoms.size()) {
		*p = "";
		return 0;
	}
	*p = h->atoms[i].data();
	return (int)h->atoms[i].size();
}

int cre2_prefilter_num_regexps(const cre2_prefilter *h) { return h->f->NumRegexps(); }

/* cre2_prefilter_potentials — 给定"正文里找到了哪几个原子"(atoms 是原子下标数组),
 * 回填"还可能命中的 pattern 下标"(升序)。
 *
 * 🔴 natoms==0 时返回的正是【不可过滤集】: PrefilterTree::RegexpsGivenStrings 在传空原子集时,
 * 除了 unfiltered_ 之外一条都不会加进来 (见 re2_prefilter_tree.cc:309 那行无条件 insert)。
 * 这就是"前置粗筛的天花板"那个数。
 *
 * 返回值是【真实条数】, 可能大于 outcap —— 此时 out 只填了前 outcap 个, 调用方按返回值扩容重来
 * (同 cre2_set_match 的约定)。 */
int cre2_prefilter_potentials(const cre2_prefilter *h, const int *atoms, int natoms, int *out, int outcap) {
	std::vector<int> a;
	if (natoms > 0 && atoms != nullptr) {
		a.assign(atoms, atoms + natoms);
	}
	std::vector<int> v;
	h->f->AllPotentials(a, &v);
	int n = (int)v.size();
	int m = n < outcap ? n : outcap;
	for (int i = 0; i < m; i++) {
		out[i] = v[i];
	}
	return n;
}

void cre2_prefilter_free(cre2_prefilter *h) {
	if (h == nullptr) {
		return;
	}
	delete h->f;
	delete h;
}

} // extern "C"
