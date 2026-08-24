package hgmLibre2

// empty_width_bench_test.go — 【能匹配空串】的 (?m) 行模式是本库的反模式。
//
// 这是本库 vs 标准库里唯一一处"同一份正文、同一个意图, 换个写法方向就反转"的形状:
// 让 pattern 不再能匹配空串(`*` -> `+` 之类), 本库从慢 5 倍变成快 6~10 倍。
// 现象与数字写在 doc/与标准库regexp怎么选.md; 本文件是那些数字的来源。
//
//	go test -run TestEmptyWidthMultiline -v .

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func ewNs(f func()) float64 {
	f()
	n := 1
	for {
		t0 := time.Now()
		for i := 0; i < n; i++ {
			f()
		}
		d := time.Since(t0)
		if d > 200*time.Millisecond {
			return float64(d.Nanoseconds()) / float64(n)
		}
		n *= 4
	}
}

func TestEmptyWidthMultiline(t *testing.T) {
	// 行式正文: 空行 / 缩进行 / 注释行 各占一部分 —— 逐行扫描类调用方的典型输入。
	body := strings.Repeat("package foo\n\n// 一行注释\nfunc Bar() int {\n\treturn 1\n}\n\n", 2000)

	cases := []struct{ Name, Pat string }{
		{"空行 · 可空匹配", `(?m)^[ \t]*$`},
		{"空行 · 同意图但不可空", `(?m)^[ \t]+$`},
		{"注释或空行 · 可空匹配", `(?m)^\s*(?://.*)?$`},
		{"注释行 · 同意图但不可空", `(?m)^\s*//.*$`},
	}
	t.Logf("正文 %d KB · %d 行", len(body)/1024, strings.Count(body, "\n"))
	t.Logf("%-26s %-8s %8s %11s %11s %8s", "pattern", "可空", "matches", "stdlib", "本库", "本库倍率")
	for _, c := range cases {
		std, re2 := regexp.MustCompile(c.Pat), MustCompile(c.Pat)
		if a, b := std.MatchString(""), re2.MatchString(""); a != b {
			t.Fatalf("%s: 空串语义两边不一致 std=%v re2=%v", c.Pat, a, b)
		}
		n := len(std.FindAllStringIndex(body, -1))
		if n2 := len(re2.FindAllStringIndex(body, -1)); n2 != n {
			t.Fatalf("%s: 匹配数不一致 std=%d re2=%d", c.Pat, n, n2)
		}
		a := ewNs(func() { std.FindAllStringIndex(body, -1) })
		b := ewNs(func() { re2.FindAllStringIndex(body, -1) })
		t.Logf("%-26s %-8v %8d %9.0f us %9.0f us %7.2fx", c.Name, re2.MatchString(""), n, a/1000, b/1000, a/b)
	}
	t.Logf("判据: pattern 能匹配空串 + 要 FindAll 整篇 → 这一格留标准库, 或者先把 pattern 改成不可空匹配。")
	t.Logf("注意这【不是】\"哪个引擎更好\"的问题: 同一份正文, 只把 * 换成 +, 方向就反过来了。")
}
