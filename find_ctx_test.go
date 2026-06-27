package hgmLibre2

// find_ctx_test.go — FindStringIndex_ctx_t 与普通 (*Regexp).FindStringIndex / stdlib 逐条对拍,
// 并验证「同一 ctx 顺序复用」不串味 (前一次结果不污染后一次)。复用 hgmLibre2_test.go 的语料。

import (
	"regexp"
	"testing"
)

func TestFindStringIndexCtx_EquivPlain(t *testing.T) {
	ctx := NewFindStringIndex_ctx() // 单个 ctx 跨所有 pattern × input 顺序复用 (模拟热路径)
	for _, pat := range testPatterns {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		for _, in := range testInputs {
			want := std.FindStringIndex(in)
			plain := mine.FindStringIndex(in)
			got := ctx.FindStringIndex(mine, in)
			// got 切自 ctx.ret · 仅当下用 · 这里立刻比对不留存。
			if !sameIntSlice(got, want) {
				t.Errorf("ctx vs stdlib 不一致 pat=%q in=%q ctx=%v std=%v", pat, in, got, want)
			}
			if !sameIntSlice(got, plain) {
				t.Errorf("ctx vs plain 不一致 pat=%q in=%q ctx=%v plain=%v", pat, in, got, plain)
			}
		}
	}
}

// TestFindStringIndexCtx_ReuseNoBleed: 命中后紧跟一次无匹配, 必须返回 nil (不回放上一次的 ret)。
func TestFindStringIndexCtx_ReuseNoBleed(t *testing.T) {
	ctx := NewFindStringIndex_ctx()
	re := MustCompile(`[0-9]+`)
	if loc := ctx.FindStringIndex(re, "abc123"); !sameIntSlice(loc, []int{3, 6}) {
		t.Fatalf("首次命中应为 [3 6], got %v", loc)
	}
	if loc := ctx.FindStringIndex(re, "no digits here"); loc != nil {
		t.Fatalf("随后无匹配应返回 nil, got %v", loc)
	}
	if loc := ctx.FindStringIndex(re, "x9"); !sameIntSlice(loc, []int{1, 2}) {
		t.Fatalf("再次命中应为 [1 2], got %v", loc)
	}
}

// TestFindStringIndexCtx_ZeroValue: 零值 ctx (未经 New) 首次调用应惰性分配并正常工作。
func TestFindStringIndexCtx_ZeroValue(t *testing.T) {
	var ctx FindStringIndex_ctx_t
	re := MustCompile(`[a-z]+`)
	if loc := ctx.FindStringIndex(re, "  abc  "); !sameIntSlice(loc, []int{2, 5}) {
		t.Fatalf("零值 ctx 应为 [2 5], got %v", loc)
	}
}

func sameIntSlice(a, b []int) bool {
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
