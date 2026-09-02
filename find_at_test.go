package hgmLibre2

import (
	"reflect"
	"regexp"
	"testing"
)

// TestCompileLongestVsGreedy 钉住"longest 与贪心选同一个起点, 只在终点上分歧"这句话 ——
// 上层 (Re2Set_fll_t 在 2026-08-28 之前的"路 B") 把两趟压成一趟, 靠的就是它。
func TestCompileLongestVsGreedy(t *testing.T) {
	cases := []struct {
		pat  string
		text string
	}{
		{`a|ab`, "ab"},
		{`abc|b`, "abc"},
		{`x{1,3}[a-c]?(?:ab|cd)?`, "xab"},
		{`(?:ab)?[bc]{1,2}`, "axbabbyxx"},
		{`[A-Za-z][A-Za-z0-9]{2,19}key`, "zz ab1keykey zz"},
	}
	for _, c := range cases {
		re, err := CompileLongest(c.pat)
		if err != nil {
			t.Fatalf("CompileLongest(%q): %v", c.pat, err)
		}
		got := re.FindStringIndex(c.text)
		// oracle = stdlib 的 Longest()
		want := regexp.MustCompile(c.pat).Copy()
		want.Longest()
		wantLoc := want.FindStringIndex(c.text)
		if !reflect.DeepEqual(got, wantLoc) {
			t.Errorf("pat=%q text=%q longest=%v want=%v", c.pat, c.text, got, wantLoc)
		}
		re.FreeC()
	}
}

// TestFindStringIndexAtWithin 钉住锚定版: 起点必须就是 from, 而且 bound 只让答案变短。
func TestFindStringIndexAtWithin(t *testing.T) {
	re, err := CompileLongest(`[a-z]{2,5}`)
	if err != nil {
		t.Fatal(err)
	}
	defer re.FreeC()
	const text = "abcdefg"
	if got := re.FindStringIndexAtWithin(text, 0, len(text)); !reflect.DeepEqual(got, []int{0, 5}) {
		t.Errorf("from=0 bound=len: %v, want [0 5]", got)
	}
	if got := re.FindStringIndexAtWithin(text, 0, 3); !reflect.DeepEqual(got, []int{0, 3}) {
		t.Errorf("bound=3: %v, want [0 3] (掐 bound 只让答案变短)", got)
	}
	if got := re.FindStringIndexAtWithin(text, 6, len(text)); got != nil {
		t.Errorf("from=6 只剩一个字节, 起不了头: %v, want nil", got)
	}
	// 锚定 = 起点就是 from, 不许往后挪。
	re2, err := CompileLongest(`bcd`)
	if err != nil {
		t.Fatal(err)
	}
	defer re2.FreeC()
	if got := re2.FindStringIndexAtWithin(text, 0, len(text)); got != nil {
		t.Errorf("锚定在 0 不该找到 [1,4): %v", got)
	}
	if got := re2.FindStringIndexAtWithin(text, 1, len(text)); !reflect.DeepEqual(got, []int{1, 4}) {
		t.Errorf("锚定在 1: %v, want [1 4]", got)
	}
	// ctx 版与非 ctx 版必须逐字相同。
	ctx := NewFindStringIndex_ctx()
	for from := 0; from <= len(text); from++ {
		a := re.FindStringIndexAtWithin(text, from, len(text))
		b := ctx.FindStringIndexAtWithin(re, text, from, len(text))
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("from=%d: 非 ctx %v != ctx %v", from, a, b)
		}
	}
}

// TestFindStringIndexAtWithinRealNeighbors 钉住"不切片": \b 看到的必须是整串里的真邻居。
func TestFindStringIndexAtWithinRealNeighbors(t *testing.T) {
	re, err := CompileLongest(`\bcd\b`)
	if err != nil {
		t.Fatal(err)
	}
	defer re.FreeC()
	const text = "abcd ef"
	// 从 2 起头的 "cd" 左边是 'b' —— 整串里它不是词边界, 所以不该命中。
	// 自己切成 text[2:4] 再搜的话会【假绿】。
	if got := re.FindStringIndexAtWithin(text, 2, 4); got != nil {
		t.Errorf("切口两侧必须是真邻居: %v, want nil", got)
	}
}

// TestReverseResolveSpanWithin 钉住单条反向锚定解析: 给右端, 求最靠左的左端。
func TestReverseResolveSpanWithin(t *testing.T) {
	rr, err := CompileReverse(`[a-z]{2,5}`)
	if err != nil {
		t.Fatal(err)
	}
	defer rr.FreeC()
	const text = "abcdefg"
	pos, ok, err := rr.ResolveSpanWithin(text, 6, -1)
	if err != nil || !ok || pos != 1 {
		t.Errorf("from=6 不限 bound: pos=%d ok=%v err=%v, want pos=1 (最长 5 个字节)", pos, ok, err)
	}
	pos, ok, err = rr.ResolveSpanWithin(text, 6, 3)
	if err != nil || !ok || pos != 3 {
		t.Errorf("from=6 bound=3: pos=%d ok=%v err=%v, want pos=3 (掐住只让答案变短)", pos, ok, err)
	}
	pos, ok, err = rr.ResolveSpanWithin(text, 1, -1)
	if err != nil || ok {
		t.Errorf("from=1 只剩一个字节, 伸不出匹配: pos=%d ok=%v err=%v", pos, ok, err)
	}
}

// TestReverseResolveSpanWithinAnchorPattern 🔴 这条是本次改动的关键回归: 单条 Compile 会把
// ^ / $ 从程序里摘成两个标志, 只有 SearchDFA 会去检查它们 —— 所以 `^ab` 绝不能在正文中间认。
// (set 那侧的 ResolveSpan 没这个问题, 它的 ^ / $ 是留在程序里的指令。)
func TestReverseResolveSpanWithinAnchorPattern(t *testing.T) {
	rr, err := CompileReverse(`^ab`)
	if err != nil {
		t.Fatal(err)
	}
	defer rr.FreeC()
	const text = "xxabab"
	if _, ok, err := rr.ResolveSpanWithin(text, 4, -1); err != nil || ok {
		t.Errorf("^ab 不该在正文中间认 (from=4): ok=%v err=%v", ok, err)
	}
	rr2, err := CompileReverse(`^ab`)
	if err != nil {
		t.Fatal(err)
	}
	defer rr2.FreeC()
	if pos, ok, err := rr2.ResolveSpanWithin("abxx", 2, -1); err != nil || !ok || pos != 0 {
		t.Errorf("^ab 在开头该认: pos=%d ok=%v err=%v", pos, ok, err)
	}
}

// TestReverseResolveSpanWithinVsOracle 拿 stdlib 的 Longest() 对拍: 对每个右端 e, 最靠左的
// 那个起点 s 必须满足 text[s:e] 匹配整段, 且 s 之前没有更靠左的合法起点。
func TestReverseResolveSpanWithinVsOracle(t *testing.T) {
	pats := []string{`[a-z]{2,5}`, `a[bc]{1,4}d?`, `\d{2,4}-\d{1,3}`, `(?:ab)+`, `x{1,3}[a-c]?`}
	texts := []string{"abcdefg", "abcbcbcd", "12-345 6789-1", "ababab", "xxxabc", "aaa123-45xxab"}
	for _, pat := range pats {
		rr, err := CompileReverse(pat)
		if err != nil {
			t.Fatal(err)
		}
		full := regexp.MustCompile(`\A(?:` + pat + `)\z`)
		for _, text := range texts {
			for e := 0; e <= len(text); e++ {
				pos, ok, err := rr.ResolveSpanWithin(text, int32(e), -1)
				if err != nil {
					t.Fatalf("pat=%q text=%q e=%d: %v", pat, text, e, err)
				}
				// oracle: 从左往右第一个 s 使 text[s:e] 整段匹配。
				wantS, wantOK := -1, false
				for s := 0; s < e; s++ {
					if full.MatchString(text[s:e]) {
						wantS, wantOK = s, true
						break
					}
				}
				if ok != wantOK || (ok && int(pos) != wantS) {
					t.Errorf("pat=%q text=%q e=%d: got (%d,%v) want (%d,%v)",
						pat, text, e, pos, ok, wantS, wantOK)
				}
			}
		}
		rr.FreeC()
	}
}
