package hgmLibre2

// spanresolve_test.go — 锚定解析 (ResolveSpan) 的【语义正确性】: 与暴力参考实现逐位对拍。
//
// 被测的是这条断言: 给定一个端点, ResolveSpan 返回的是【最长】那个匹配的另一端。
// 参考实现照定义写 —— 枚举另一端的每一个候选位置, 用 stdlib 判 text[s:e] 是不是这条 pattern
// 在【原文上下文里】的一个完整匹配, 取最远的那个。它与被测实现没有任何共享逻辑。
//
// 判定必须带上下文: ^ $ \b 这些零宽断言看的是它在整篇正文里的位置, 拿 text[s:e] 单独去匹配
// 会把子串的两头当成正文的两头 (spanscan_test.go 的第一版就栽在这)。所以参考实现把前后缀
// 按字面量钉死, 拿【整篇正文】去匹配。

import (
	"regexp"
	"strings"
	"testing"
)

var spanResolvePatterns = []string{
	"ab|c",
	"abc",
	"abcd",
	"[a-z]{3,}",
	"[a-z]{2,4}",
	"a*",
	"[0-9]+",
	`\bkey[0-9]+`,
	"^the",
	"end$",
	"x(?:yz)+",
	"[A-Za-z0-9]{4,}",
}

var spanResolveInputs = []string{
	"",
	"a",
	"abc",
	"abcd",
	"abcabc",
	"the key123 end",
	"xyzyzyz ab12",
	"aaaaaaaa",
	"zz abcdef zz",
}

// matchesInContext 判定: text[s:e] 是不是 pat 在整篇 text 里的一个完整匹配。
func matchesInContext(t *testing.T, pat, text string, s, e int) bool {
	t.Helper()
	re := regexp.MustCompile(`\A` + regexp.QuoteMeta(text[:s]) + `(?:` + pat + `)` +
		regexp.QuoteMeta(text[e:]) + `\z`)
	return re.MatchString(text)
}

// bruteLongestEnd: 从 s 出发最远能匹配到哪 (返回右端, 不含); -1 = 从 s 出发根本不匹配。
func bruteLongestEnd(t *testing.T, pat, text string, s int) int {
	t.Helper()
	// 先便宜地判一下"从 s 出发有没有匹配", 绝大多数位置在这里就被剪掉了。
	any := regexp.MustCompile(`\A` + regexp.QuoteMeta(text[:s]) + `(?:` + pat + `)(?s).*\z`)
	if !any.MatchString(text) {
		return -1
	}
	for e := len(text); e >= s; e-- {
		if matchesInContext(t, pat, text, s, e) {
			return e
		}
	}
	return -1
}

// bruteLongestStart: 到 e 为止最远能往回匹配到哪 (返回左端, 含); -1 = 在 e 结束的匹配不存在。
func bruteLongestStart(t *testing.T, pat, text string, e int) int {
	t.Helper()
	any := regexp.MustCompile(`\A(?s).*(?:` + pat + `)` + regexp.QuoteMeta(text[e:]) + `\z`)
	if !any.MatchString(text) {
		return -1
	}
	for s := 0; s <= e; s++ {
		if matchesInContext(t, pat, text, s, e) {
			return s
		}
	}
	return -1
}

func TestSpanResolve_ForwardMatchesBrute(t *testing.T) {
	set, err := NewRegexpSet(spanResolvePatterns)
	if err != nil {
		t.Fatalf("建正向 set: %v", err)
	}
	for id, pat := range spanResolvePatterns {
		for _, text := range spanResolveInputs {
			for s := 0; s <= len(text); s++ {
				want := bruteLongestEnd(t, pat, text, s)
				got, ok, err := set.ResolveSpan(text, int32(s), int32(id))
				if err != nil {
					t.Fatalf("pat=%q text=%q s=%d: %v", pat, text, s, err)
				}
				if want < 0 {
					if ok {
						t.Fatalf("pat=%q text=%q s=%d: 从这里出发没有匹配, 却返回了右端 %d",
							pat, text, s, got)
					}
					continue
				}
				if !ok {
					t.Fatalf("pat=%q text=%q s=%d: 应该匹配到 %d, 却说不匹配", pat, text, s, want)
				}
				if int(got) != want {
					t.Fatalf("pat=%q text=%q s=%d: 右端 got=%d want=%d (段 got=%q want=%q)",
						pat, text, s, got, want, text[s:got], text[s:want])
				}
			}
		}
	}
}

func TestSpanResolve_ReverseMatchesBrute(t *testing.T) {
	set, err := NewRegexpSetReverseMaxMem(spanResolvePatterns, 0)
	if err != nil {
		t.Fatalf("建反向 set: %v", err)
	}
	for id, pat := range spanResolvePatterns {
		for _, text := range spanResolveInputs {
			for e := 0; e <= len(text); e++ {
				want := bruteLongestStart(t, pat, text, e)
				got, ok, err := set.ResolveSpan(text, int32(e), int32(id))
				if err != nil {
					t.Fatalf("pat=%q text=%q e=%d: %v", pat, text, e, err)
				}
				if want < 0 {
					if ok {
						t.Fatalf("pat=%q text=%q e=%d: 在这里结束的匹配不存在, 却返回了左端 %d",
							pat, text, e, got)
					}
					continue
				}
				if !ok {
					t.Fatalf("pat=%q text=%q e=%d: 应该匹配到 %d, 却说不匹配", pat, text, e, want)
				}
				if int(got) != want {
					t.Fatalf("pat=%q text=%q e=%d: 左端 got=%d want=%d (段 got=%q want=%q)",
						pat, text, e, got, want, text[got:e], text[want:e])
				}
			}
		}
	}
}

// TestSpanResolve_LongestNotFirst 把"返回最长而不是碰到的第一个"单独钉死。
//
// 这不是风格问题: 变长 pattern 在同一个端点上通常有一串长度都成立, DFA 反着走时【先】撞上
// 的是最短的那个。要是实现写成"碰到第一个 match 状态就收工", 下面这几条会全部返回短的那个,
// 而调用方拿到的就是一段被截断的命中 —— 下游做定长校验时会把真命中判成假命中。
func TestSpanResolve_LongestNotFirst(t *testing.T) {
	pats := []string{`[A-Za-z0-9]{4,}`, `AAA-[A-Za-z0-9]{8,16}`, `ab|abc|abcd`}
	text := "  ABCDEFGH AAA-abcdefgh1234 abcd  "

	fwd, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatalf("建正向 set: %v", err)
	}
	rev, err := NewRegexpSetReverseMaxMem(pats, 0)
	if err != nil {
		t.Fatalf("建反向 set: %v", err)
	}

	cases := []struct {
		id           int32
		start, end   int
		shortestEnd  int // 从 start 出发"第一个"匹配会停在哪 (= 错误答案)
		shortestStrt int // 从 end 往回"第一个"匹配会停在哪 (= 错误答案)
		seg          string
	}{
		{id: 0, start: 2, end: 10, shortestEnd: 6, shortestStrt: 6, seg: "ABCDEFGH"},
		{id: 1, start: 11, end: 27, shortestEnd: 23, shortestStrt: 11, seg: "AAA-abcdefgh1234"},
		{id: 2, start: 28, end: 32, shortestEnd: 30, shortestStrt: 28, seg: "abcd"},
	}
	for _, c := range cases {
		if got := text[c.start:c.end]; got != c.seg {
			t.Fatalf("语料坐标写错了: text[%d:%d]=%q, 期望 %q", c.start, c.end, got, c.seg)
		}
		// 正向: 给左端求右端, 必须是最远的那个。
		got, ok, err := fwd.ResolveSpan(text, int32(c.start), c.id)
		if err != nil || !ok {
			t.Fatalf("正向 id=%d from=%d: ok=%v err=%v", c.id, c.start, ok, err)
		}
		if int(got) == c.shortestEnd {
			t.Fatalf("正向 id=%d 返回了【最短】匹配的右端 %d (段 %q), 应该是最长的 %d (段 %q)",
				c.id, got, text[c.start:got], c.end, c.seg)
		}
		if int(got) != c.end {
			t.Fatalf("正向 id=%d from=%d: 右端 got=%d want=%d", c.id, c.start, got, c.end)
		}
		// 反向: 给右端求左端, 必须是最远的那个。
		got, ok, err = rev.ResolveSpan(text, int32(c.end), c.id)
		if err != nil || !ok {
			t.Fatalf("反向 id=%d from=%d: ok=%v err=%v", c.id, c.end, ok, err)
		}
		if int(got) == c.shortestStrt && c.shortestStrt != c.start {
			t.Fatalf("反向 id=%d 返回了【最短】匹配的左端 %d (段 %q), 应该是最长的 %d (段 %q)",
				c.id, got, text[got:c.end], c.start, c.seg)
		}
		if int(got) != c.start {
			t.Fatalf("反向 id=%d from=%d: 左端 got=%d want=%d", c.id, c.end, got, c.start)
		}
	}
}

// TestSpanResolve_PerPatternNotPerSet: 同一个端点上多条 pattern 同时成立时, 每条各自算各自的。
// 要是实现漏掉了"这个 match 状态里有没有第 id 条"这一步 (只看"是不是 match 状态"),
// 下面这两条会互相污染 —— 短的那条会被长的那条拖长。
func TestSpanResolve_PerPatternNotPerSet(t *testing.T) {
	pats := []string{`[a-z]{2}`, `[a-z]{6}`}
	text := "abcdef"
	rev, err := NewRegexpSetReverseMaxMem(pats, 0)
	if err != nil {
		t.Fatalf("建反向 set: %v", err)
	}
	for id, want := range []int{4, 0} { // 到 6 结束: 两字母的从 4 起, 六字母的从 0 起
		got, ok, err := rev.ResolveSpan(text, 6, int32(id))
		if err != nil || !ok {
			t.Fatalf("id=%d: ok=%v err=%v", id, ok, err)
		}
		if int(got) != want {
			t.Fatalf("id=%d: 左端 got=%d want=%d (段 %q)", id, got, want, text[got:6])
		}
	}
	fwd, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatalf("建正向 set: %v", err)
	}
	for id, want := range []int{2, 6} { // 从 0 出发: 两字母的到 2, 六字母的到 6
		got, ok, err := fwd.ResolveSpan(text, 0, int32(id))
		if err != nil || !ok {
			t.Fatalf("id=%d: ok=%v err=%v", id, ok, err)
		}
		if int(got) != want {
			t.Fatalf("id=%d: 右端 got=%d want=%d (段 %q)", id, got, want, text[0:got])
		}
	}
}

// TestSpanResolve_Bound: bound 掐住"最远看到哪"。
// 掐 bound 只能让答案变短, 不能让它变错 —— 上下文永远是整篇正文, \b 看到的还是真实邻居。
func TestSpanResolve_Bound(t *testing.T) {
	pats := []string{`(?s)A.*Z`, `\bxy[a-z]*\b`}
	text := "  A0123456789Z  xyzzz "
	aPos := strings.IndexByte(text, 'A')
	zPos := strings.IndexByte(text, 'Z') + 1 // 'Z' 的下一个位置 = 右端(不含)
	xy := strings.Index(text, "xy")
	xyEnd := len(text) - 1                   // "xyzzz" 的右端 (后面还有一个空格)
	if aPos < 0 || zPos <= 0 || xy < 0 || text[xy:xyEnd] != "xyzzz" {
		t.Fatalf("语料坐标写错了: %q", text)
	}

	fwd, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatalf("建正向 set: %v", err)
	}
	// 不限: 一路走到 Z。
	got, ok, err := fwd.ResolveSpan(text, int32(aPos), 0)
	if err != nil || !ok || int(got) != zPos {
		t.Fatalf("不限 bound: got=%d ok=%v err=%v want=%d", got, ok, err, zPos)
	}
	// 掐在 Z 之前: 这条 pattern 在窗口里走不完, 应该报"没有"。
	got, ok, err = fwd.ResolveSpanWithin(text, int32(aPos), int32(zPos-1), 0)
	if err != nil {
		t.Fatalf("掐 bound: %v", err)
	}
	if ok {
		t.Fatalf("掐在 Z 之前不该有答案, 却返回了 %d", got)
	}
	// 掐得正好: 还是能找到。
	got, ok, err = fwd.ResolveSpanWithin(text, int32(aPos), int32(zPos), 0)
	if err != nil || !ok || int(got) != zPos {
		t.Fatalf("掐在 Z 上: got=%d ok=%v err=%v want=%d", got, ok, err, zPos)
	}

	// \b 那条: bound 切出来的边界【不是】词边界的依据, 上下文永远是整篇正文。
	got, ok, err = fwd.ResolveSpan(text, int32(xy), 1)
	if err != nil || !ok {
		t.Fatalf("\\b 那条: ok=%v err=%v", ok, err)
	}
	if int(got) != xyEnd {
		t.Fatalf("\\b 那条: 右端 got=%d want=%d (段 %q)", got, xyEnd, text[xy:got])
	}
	// 掐在词中间: 那里不是词边界, 所以没有答案 (而不是"就当它是边界"给个短的)。
	if _, ok, err = fwd.ResolveSpanWithin(text, int32(xy), int32(xy+2), 1); err != nil {
		t.Fatalf("\\b 掐 bound: %v", err)
	} else if ok {
		t.Fatalf("掐在词中间不该有答案 —— bound 不是词边界")
	}
}

// TestSpanResolve_BadArgs: 参数守卫。
func TestSpanResolve_BadArgs(t *testing.T) {
	set, err := NewRegexpSet([]string{"abc"})
	if err != nil {
		t.Fatalf("建 set: %v", err)
	}
	text := "abc"
	if _, _, err := set.ResolveSpan(text, 0, 1); err == nil {
		t.Fatalf("id 越界应该报错")
	}
	if _, _, err := set.ResolveSpan(text, 0, -1); err == nil {
		t.Fatalf("id 为负应该报错")
	}
	if _, _, err := set.ResolveSpan(text, 4, 0); err == nil {
		t.Fatalf("from 超出正文应该报错")
	}
	if _, _, err := set.ResolveSpan(text, -1, 0); err == nil {
		t.Fatalf("from 为负应该报错")
	}
	// 空正文 + 空匹配 pattern: 合法, 且应该答"匹配, 右端 0"。
	e, err := NewRegexpSet([]string{"a*"})
	if err != nil {
		t.Fatalf("建 set: %v", err)
	}
	pos, ok, err := e.ResolveSpan("", 0, 0)
	if err != nil || !ok || pos != 0 {
		t.Fatalf("空正文空匹配: pos=%d ok=%v err=%v", pos, ok, err)
	}
	// ResolveSpanBytes 与 ResolveSpan 同解。
	pos, ok, err = set.ResolveSpanBytes([]byte(text), 0, 0)
	if err != nil || !ok || pos != 3 {
		t.Fatalf("ResolveSpanBytes: pos=%d ok=%v err=%v", pos, ok, err)
	}
}
