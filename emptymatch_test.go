package hgmLibre2

// emptymatch_test.go —— 全库那条规矩的回归: 【能匹配空串的 pattern, 每一个编译入口都当场拒】。
//
// 🔴 一个入口一格, 不许漏 —— 这条规矩的价值全在"没有例外"上: 只要还剩一个入口能编出
//    可空 pattern, 后面所有"每个匹配至少 1 字节"的无条件假设就都是空头支票。
//
// 规矩本身和它的来由见 emptymatch.go 文件头。

import (
	"math/rand"
	"regexp/syntax"
	"strconv"
	"strings"
	"testing"
)

// emptyCapablePats 是几种典型的可空写法。每一条都该被每一个入口拒掉。
var emptyCapablePats = []string{
	`a*`,           // 星号
	`x{0,3}`,       // 计数下界 0
	`(a|)`,         // 交替里有空支
	`a?`,           // 问号
	``,             // 空 pattern 本身
	`(?m)^[ \t]*$`, // README "该留标准库" 第③条那一族
	`\b`,           // 整条零宽
	`(?:^|$){500}`, // 零宽的计数重复

	// 🔴 下面这三条是 2026-09-01 那个 bug 的原形: PerlX 的 \C (匹配任意一个字节) RE2 认,
	//    Go 的 regexp/syntax 报 invalid escape sequence。老实现"Go 解析不了就放行", 于是
	//    它们整条绕过这道门, 编得出来还产零长匹配 (`\C*?` 在 "ab" 上给 [[0 0] [1 1] [2 2]])。
	//    判定挪到 RE2 自己的解析器上 (cre2_emptymatch.cpp) 之后, 这个口子没有了。
	`\C*`,
	`\C?`,
	`(?:\C\C)*`,
}

// wantEmptyErr 判一个 error 是不是"拒空串"那一条 (而不是别的编译错误)。
func wantEmptyErr(t *testing.T, entry, pat string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s(%q) 必须当场报错 —— 能匹配空串的 pattern 全库都不许编出来", entry, pat)
	}
	if !strings.Contains(err.Error(), "能匹配空串") {
		t.Fatalf("%s(%q) 报的不是【拒空串】那一条: %v", entry, pat, err)
	}
}

// mustPanic 跑 f, 要求它 panic (Must* 那一族入口)。
func mustPanic(t *testing.T, entry, pat string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s(%q) 必须 panic", entry, pat)
		}
		if s, ok := r.(string); ok && !strings.Contains(s, "能匹配空串") {
			t.Fatalf("%s(%q) panic 的不是【拒空串】那一条: %v", entry, pat, r)
		}
	}()
	f()
}

// TestEmptyMatchRejectedAtEveryEntry —— 逐个编译入口。
func TestEmptyMatchRejectedAtEveryEntry(t *testing.T) {
	for _, pat := range emptyCapablePats {
		p := pat
		// ── 单条 ────────────────────────────────────────────────────────────
		_, err := Compile(p)
		wantEmptyErr(t, "Compile", p, err)
		_, err = CompileMaxMem(p, 64<<20)
		wantEmptyErr(t, "CompileMaxMem", p, err)
		_, err = CompileLongest(p)
		wantEmptyErr(t, "CompileLongest", p, err)
		_, err = CompileLongestMaxMem(p, 64<<20)
		wantEmptyErr(t, "CompileLongestMaxMem", p, err)
		_, err = CompileReverse(p)
		wantEmptyErr(t, "CompileReverse", p, err)
		_, err = CompileReverseMaxMem(p, 64<<20)
		wantEmptyErr(t, "CompileReverseMaxMem", p, err)
		mustPanic(t, "MustCompile", p, func() { MustCompile(p) })
		mustPanic(t, "MustCompileLongest", p, func() { MustCompileLongest(p) })
		mustPanic(t, "MustCompileReverse", p, func() { MustCompileReverse(p) })

		// ── 表 ──────────────────────────────────────────────────────────────
		// 🔴 混在一张【好 pattern 的表】里也要拒, 而且错误文案要指出是第几条 ——
		//    静默丢掉一条会让下标错位, 那是最难查的一类错。
		mixed := []string{`\d{3}`, p, `[a-z]{2,4}`}
		_, err = NewRegexpSet(mixed)
		wantEmptyErr(t, "NewRegexpSet", p, err)
		if !strings.Contains(err.Error(), "index 1") {
			t.Fatalf("NewRegexpSet(%q) 的错误文案没说清是第几条: %v", p, err)
		}
		_, err = NewRegexpSetMaxMem(mixed, 64<<20)
		wantEmptyErr(t, "NewRegexpSetMaxMem", p, err)
		_, err = NewRegexpSetReverseMaxMem(mixed, 64<<20)
		wantEmptyErr(t, "NewRegexpSetReverseMaxMem", p, err)
		_, err = NewPrefilter(mixed, 3, 64<<20)
		wantEmptyErr(t, "NewPrefilter", p, err)
	}
}

// TestEmptyMatchGoodPatternsStillCompile —— 反面: 不可空的写法一条都不许被误伤。
// 🔴 这一格是上面那一格的配对: 一道只会说"不"的门是没有用的。
func TestEmptyMatchGoodPatternsStillCompile(t *testing.T) {
	good := []string{
		`a+`, `x{1,3}`, `(a|b)`, `a?b`, `(?m)^[ \t]+$`, `\bword\b`,
		`\b{1000}foo`, `(?:^){1000}foo`, `a(?:\b){0,900}b`, `[A-Za-z][A-Za-z0-9]{2,19}key`,

		// 🔴 \C 那一族里【不可空】的那些必须照编不误。这是"换成 RE2 的解析器"这件事的
		//    另一半代价: 门看得懂 \C 了, 就不许再拿"看不懂"当借口把它们一并拒掉。
		`\C`, `a\C`, `\C+`, `(?:\C\C)+`,
	}
	for _, p := range good {
		if _, err := Compile(p); err != nil {
			t.Fatalf("Compile(%q) 不该被拒: %v", p, err)
		}
		if _, err := NewRegexpSet([]string{p}); err != nil {
			t.Fatalf("NewRegexpSet(%q) 不该被拒: %v", p, err)
		}
	}
}

// TestEmptyMatchUnparsableStillCompiles —— 【RE2 自己都解析不了】的 pattern 一律放行,
// 由紧接着的 cre2_new / cre2_set_add 去报 RE2 自己那条更准的错, 不该被这道门截胡。
// 🔴 注意这已经【不是口子】了: 这道门用的就是 RE2 的解析器, "它解析不了"等价于
//    "这条 pattern 根本编不出来", 不存在"漏过去还能跑"的可能。
func TestEmptyMatchUnparsableStillCompiles(t *testing.T) {
	for _, p := range []string{`(unclosed`, `[z-a]`, `(`} {
		_, err := Compile(p)
		if err == nil {
			t.Fatalf("坏 pattern %q 该报错", p)
		}
		if strings.Contains(err.Error(), "能匹配空串") {
			t.Fatalf("坏 pattern %q 该由 RE2 自己报它本来的错, 不该被空串这道门截胡: %v", p, err)
		}
	}
}

// TestEmptyMatchUsesRe2Parser —— 这道门用的是【RE2 的解析器】不是 Go 的 regexp/syntax。
// 🔴 这一格盯的是 2026-09-01 修掉的那个 bug 本身, 不是它的某个症状: 只要判定还在 Go 侧,
//    两个解析器认的语言不一样, 就一定还有一族 pattern 从门底下钻过去。\C 是找得到的那一族,
//    但这一格真正要守的是"判定必须和编译共用一个解析器"这条结构约束。
func TestEmptyMatchUsesRe2Parser(t *testing.T) {
	// 前提: 这些写法 Go 的解析器确实不认 —— 前提不成立的话这一格就名存实亡了, 要当场知道。
	for _, p := range []string{`\C`, `\C*`} {
		if _, err := syntax.Parse(p, syntax.Perl); err == nil {
			t.Fatalf("前提变了: Go 的 regexp/syntax 现在认得 %q 了, 这一格该重挑写法", p)
		}
	}
	// 可空的那一支: 必须被拒, 且报的就是【拒空串】那一条。
	for _, p := range []string{`\C*`, `\C?`, `(?:\C\C)*`} {
		_, err := Compile(p)
		wantEmptyErr(t, "Compile", p, err)
	}
	// 不可空的那一支: 必须照编, 而且跑出来是 RE2 的语义 (\C = 一个字节)。
	re, err := Compile(`\C`)
	if err != nil {
		t.Fatalf(`Compile(\C) 不该被拒: %v`, err)
	}
	if got := re.FindAllStringIndex("ab", -1); len(got) != 2 ||
		got[0][0] != 0 || got[0][1] != 1 || got[1][0] != 1 || got[1][1] != 2 {
		t.Fatalf(`\C 在 "ab" 上该逐字节命中 [[0 1] [1 2]], 实得 %v`, got)
	}
}

// msEmptyGen 随机造一条【两个解析器都认】的 pattern (只用公共子集: 不碰 \C, 不碰
// Go 没有的写法)。depth 到顶就只出叶子, 保证收敛。
func msEmptyGen(rng *rand.Rand, depth int) string {
	leaf := []string{`a`, `b`, `.`, `[a-c]`, `\d`, `\w`, `\b`, `\B`, `^`, `$`, ``, `ab`}
	if depth >= 4 || rng.Intn(3) == 0 {
		return leaf[rng.Intn(len(leaf))]
	}
	x := msEmptyGen(rng, depth+1)
	switch rng.Intn(9) {
	case 0:
		return `(?:` + x + `)*`
	case 1:
		return `(?:` + x + `)+`
	case 2:
		return `(?:` + x + `)?`
	case 3:
		lo := rng.Intn(3)
		return `(?:` + x + `){` + strconv.Itoa(lo) + `,` + strconv.Itoa(lo+1+rng.Intn(2)) + `}`
	case 4:
		return `(` + x + `)`
	case 5:
		return `(?:` + x + `|` + msEmptyGen(rng, depth+1) + `)`
	default:
		return `(?:` + x + `)(?:` + msEmptyGen(rng, depth+1) + `)`
	}
}

// TestEmptyMatchAgreesWithGoSyntaxOnSharedLanguage —— 新判定 (RE2 的解析器 + 本库的可空
// 走树) 与旧判定 (Go 的 regexp/syntax + lenRangeOf) 在【两者都认的那部分语言】上必须逐条
// 一致。
//
// 🔴 这一格盯的是【换解析器有没有顺手改了语义】: 换掉的理由只是"Go 认得少", 不是
//    "旧的算错了"。两个解析器都认的写法上出现任何分歧, 都是新走树 (CanMatchEmptyWalker)
//    的 op 表写漏或写错了 —— 那是个静默的错, 只会表现为某条 pattern 莫名其妙编不出来
//    (或者更糟, 一条可空的混进来把"每个匹配至少 1 字节"那条假设废掉)。
func TestEmptyMatchAgreesWithGoSyntaxOnSharedLanguage(t *testing.T) {
	rng := rand.New(rand.NewSource(20260901))
	nEmpty, nNonEmpty := 0, 0
	for i := 0; i < 4000; i++ {
		pat := msEmptyGen(rng, 0)
		re, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			continue // Go 不认的不在对拍范围内 (那正是换解析器要救的那部分)
		}
		min, _ := lenRangeOf(re)
		wantEmpty := min <= 0
		gotEmpty := checkNoEmptyMatch("pattern", pat) != nil
		if wantEmpty != gotEmpty {
			t.Fatalf("第 %d 条 %q: Go 侧算 min=%d (可空=%v), RE2 侧这道门判可空=%v",
				i, pat, min, wantEmpty, gotEmpty)
		}
		if wantEmpty {
			nEmpty++
		} else {
			nNonEmpty++
		}
	}
	// 两边都得有量, 否则是"全判同一个答案"的空转绿。
	if nEmpty < 200 || nNonEmpty < 200 {
		t.Fatalf("语料没造匀: 可空 %d 条 / 不可空 %d 条", nEmpty, nNonEmpty)
	}
	t.Logf("4000 条随机 pattern 对拍一致 (可空 %d / 不可空 %d)", nEmpty, nNonEmpty)
}
