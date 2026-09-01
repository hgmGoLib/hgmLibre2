package hgmLibre2

// emptymatch_test.go —— 全库那条规矩的回归: 【能匹配空串的 pattern, 每一个编译入口都当场拒】。
//
// 🔴 一个入口一格, 不许漏 —— 这条规矩的价值全在"没有例外"上: 只要还剩一个入口能编出
//    可空 pattern, 后面所有"每个匹配至少 1 字节"的无条件假设就都是空头支票。
//
// 规矩本身和它的来由见 emptymatch.go 文件头。

import (
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

// TestEmptyMatchUnparsableStillCompiles —— 那个【故意留的口子】: Go 的 regexp/syntax 解析不了
// 的写法一律【放行】, 由 RE2 自己去报它本来的错。宁可漏一条, 也不能把本来能用的拒掉。
// (走到这里的坏 pattern 仍然报错, 只是报的不是【拒空串】那一条。)
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
