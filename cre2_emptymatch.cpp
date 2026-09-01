// cre2_emptymatch.cpp — 【能不能匹配空串】这道门的判定, 在 RE2 自己的语法树上算。
//
// 为什么不在 Go 侧用 regexp/syntax 算 (2026-09-01 之前就是那么干的, 是个 bug):
// Go 的解析器和 RE2 的解析器【不是同一个语言】。最实在的一处是 PerlX 的 \C (匹配任意
// 一个字节) —— RE2 认, Go 的 regexp/syntax 报 "invalid escape sequence"。老实现的口子是
// "Go 解析不了就放行", 于是 `\C*` 整条绕过这道门, 照常产出零长匹配:
//
//     `\C*?`       "ab"    FindAllStringIndex = [[0 0] [1 1] [2 2]]
//     `(?:\C\C)*`  "abc"   FindAllStringIndex = [[0 2] [3 3]]
//
// 换成本文件之后【没有口子】: 用的就是 RE2::Init 里那一句 Regexp::Parse, ParseFlags 也是
// RE2::Options::ParseFlags() 算出来的同一份 (本库所有编译入口只动 max_mem / longest_match,
// 两个都不进 ParseFlags, 所以一份默认 options 就代表全库)。RE2 解析得了的, 这里一定解析
// 得了; RE2 解析不了的, 这里返回 -1, 让后面 RE2 自己去报它本来的错。
//
// 判据是【结构上能不能吃 0 个字节走完】, 不是"在空文本上能不能命中":
// \b 在空文本上不命中 (RE2::PartialMatch("", `\b`) = false), 可它在 "ab" 上照样产出零长
// 匹配。所以零宽断言 (^ $ \b \B \A \z) 一律算【可空】。这也是为什么不能拿运行期空串探针
// 当判据 —— 那条会漏掉一整族。
//
// 走树用 RE2 自己的 Regexp::Walker (re2_walker-inl.h): 它是显式栈不递归的, 深嵌套的
// pattern 不会把 C 栈压爆 —— RE2 自己的 Regexp::Destroy 也是因为这个才用它。
#include "cre2.h"

#include "re2_re2.h"
#include "re2_regexp.h"
#include "re2_stringpiece.h"
#include "re2_walker-inl.h"

namespace {

// CanMatchEmptyWalker: 自底向上算"这棵子树能不能吃 0 个字节走完"。
class CanMatchEmptyWalker : public re2::Regexp::Walker<bool> {
public:
	bool PostVisit(re2::Regexp *re, bool parent_arg, bool pre_arg,
	               bool *child_args, int nchild_args) override {
		switch (re->op()) {
		case re2::kRegexpNoMatch:
			return false; // 什么都匹配不上, 自然也产不出零长匹配
		case re2::kRegexpEmptyMatch:
			return true;
		case re2::kRegexpLiteral:
			return false; // 一个 rune
		case re2::kRegexpLiteralString:
			return re->nrunes() == 0;
		case re2::kRegexpConcat: {
			// 每一节都得能空, 整串才能空
			for (int i = 0; i < nchild_args; i++) {
				if (!child_args[i]) {
					return false;
				}
			}
			return true;
		}
		case re2::kRegexpAlternate: {
			// 有一支能空就能空
			for (int i = 0; i < nchild_args; i++) {
				if (child_args[i]) {
					return true;
				}
			}
			return false;
		}
		case re2::kRegexpStar:
		case re2::kRegexpQuest:
			return true; // 零次
		case re2::kRegexpPlus:
			return nchild_args > 0 && child_args[0]; // 至少一次, 那一次能空才能空
		case re2::kRegexpRepeat:
			// x{0,n} 直接能空; x{m,n} (m>=1) 看 x 自己能不能空
			return re->min() == 0 || (nchild_args > 0 && child_args[0]);
		case re2::kRegexpCapture:
			return nchild_args > 0 && child_args[0];
		case re2::kRegexpAnyChar:
		case re2::kRegexpAnyByte:
		case re2::kRegexpCharClass:
			return false; // 都要吃掉一个 rune / 一个字节
		case re2::kRegexpBeginLine:
		case re2::kRegexpEndLine:
		case re2::kRegexpWordBoundary:
		case re2::kRegexpNoWordBoundary:
		case re2::kRegexpBeginText:
		case re2::kRegexpEndText:
			return true; // 零宽断言: 见文件头, 断言【能不能成立】不在判据里
		case re2::kRegexpHaveMatch:
			return true; // set 里那个"第 i 条命中了"的零宽标记
		default:
			return true; // 不认识的 op: 宁可误拒也不放过, 见 ShortVisit
		}
	}

	// 只有 Walk 的 100 万节点预算用光才会走到 (本库的 pattern 到不了)。
	// 走到就说明这棵树大到算不完 —— 判"可空"把它拒掉, 比放一条可能产零长匹配的进来强。
	bool ShortVisit(re2::Regexp *re, bool parent_arg) override { return true; }
};

} // namespace

extern "C" {

int cre2_pattern_can_match_empty(const char *pat, int patlen) {
	// 🔴 这一份 options 必须与真正编译时用的那份【同源】: 本库所有编译入口 (cre2_new_common /
	//    cre2_set_new_ex) 都只动 max_mem 与 longest_match, 两者都不进 ParseFlags, 所以默认
	//    构造的 options 算出来的 flags 就是全库那一份。将来谁给编译入口加了会进 ParseFlags
	//    的 option (latin1 / posix_syntax / literal / never_capture / case_sensitive …),
	//    这里必须跟着改, 否则门和编译又对不上 —— 那正是这个 bug 当初的形状。
	RE2::Options opt;
	opt.set_log_errors(false);
	re2::RegexpStatus status;
	re2::Regexp *re = re2::Regexp::Parse(
	    re2::StringPiece(pat, patlen),
	    static_cast<re2::Regexp::ParseFlags>(opt.ParseFlags()),
	    &status);
	if (re == nullptr) {
		return -1; // RE2 自己都解析不了 —— 交给后面的编译去报它本来的错
	}
	CanMatchEmptyWalker w;
	bool empty = w.Walk(re, false);
	re->Decref();
	return empty ? 1 : 0;
}

} // extern "C"
