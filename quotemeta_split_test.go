package hgmLibre2

// quotemeta_split_test.go — QuoteMeta / Split 与 stdlib regexp 逐一对拍。

import (
	"regexp"
	"testing"
)

func TestQuoteMeta_EquivStdlib(t *testing.T) {
	cases := []string{
		"", "abc", `a.b*c+`, `(group)|[class]{2}`, `^$\d\w`, "no-meta_123",
		`C:\path\to\file`, "中文.txt", `price=$5.00 (50%)`, `a\nb`, "tab\tend",
	}
	for _, s := range cases {
		if got, want := QuoteMeta(s), regexp.QuoteMeta(s); got != want {
			t.Errorf("QuoteMeta(%q) = %q · stdlib = %q", s, got, want)
		}
		// 转义后必须能编译且精确匹配原串字面量。
		if s != "" {
			re := MustCompile(QuoteMeta(s))
			if !re.MatchString(s) {
				t.Errorf("QuoteMeta(%q) 编译后不匹配原串", s)
			}
		}
	}
}

func TestSplit_EquivStdlib(t *testing.T) {
	patterns := []string{`[\s.\-_]+`, `,`, `\d+`, `a*`, `\s*`, `(?i)x`}
	inputs := []string{
		"", "f-a-l-c-o-n", "f.a.l.c.o.n", "f a l c o n", "a,b,,c,", "x1y22z333",
		"aXbXc", "no separators here", "  lead and trail  ",
	}
	for _, pat := range patterns {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		for _, in := range inputs {
			for _, n := range []int{-1, 0, 1, 2, 3} {
				got := mine.Split(in, n)
				want := std.Split(in, n)
				if !sameStrSlice(got, want) {
					t.Errorf("Split pat=%q in=%q n=%d\n  got =%#v\n  want=%#v", pat, in, n, got, want)
				}
			}
		}
	}
}

func sameStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
