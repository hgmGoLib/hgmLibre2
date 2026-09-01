package hgmLibre2

import (
	"fmt"
	"math/rand"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// frelScan 是"开一个 Re2SetFrel, 扫一遍, 把区间收成一个数组"的测试用简写。
// 🔴 生产路径别这么写 (那块 dst 是 ∝ 命中数的 ratchet 缓冲, 正是分批接口要躲开的东西),
// 这里是判据代码, 语料才一百多字节。
func frelScan(t *testing.T, pats []Re2SetFrelPattern_t, text string, batch int) []SetMatch {
	t.Helper()
	s, err := NewRe2SetFrelMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建 Re2SetFrel 失败 pats=%v: %v", pats, err)
	}
	defer s.Close()
	var got []SetMatch
	err = s.Scan(text, make([]Re2SetFrel_result_t, batch), func(rs []Re2SetFrel_result_t) bool {
		for _, r := range rs {
			got = append(got, SetMatch{Index: r.Index, Lo: r.Start, Hi: r.End})
		}
		return true
	})
	if err != nil {
		t.Fatalf("Scan 失败 pats=%v: %v", pats, err)
	}
	return got
}

// frelBrute 是穷举出来的【最右终点最长】不重叠序列 —— 与本库无关, 只用 stdlib。
// 上界从正文末尾起, 反复取"终点最靠右且 <= 上界"的匹配, 同终点取最长 (= 起点最靠左),
// 收下之后把上界压到它的起点。返回升序。
//
// 🔴 这不是 msrBrute (反向 MatchScanner 那个)。那个先挑【起点】最靠右的, 这个先挑
//    【终点】最靠右的 —— aa|a 撞 "aaa" 两者给出完全不同的答案, 见 TestRe2SetFrel_Shape。
func frelBrute(pat, text string) []SetMatch {
	re := regexp.MustCompile(`\A(?:` + pat + `)\z`)
	var out []SetMatch
	bound := len(text)
	for {
		bs, be := -1, -1
		for e := bound; e > 0 && be < 0; e-- {
			for s := 0; s < e; s++ { // 同终点取最长 = 起点最靠左
				if re.MatchString(text[s:e]) {
					bs, be = s, e
					break
				}
			}
		}
		if be < 0 {
			break
		}
		out = append(out, SetMatch{Lo: int32(bs), Hi: int32(be)})
		bound = bs
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// TestRe2SetFrel_Shape 钉住对外说过的口径, 并且证明它与另外两个口径【真的不一样】——
// 否则下面那些对拍全是空转。
func TestRe2SetFrel_Shape(t *testing.T) {
	// aa|a 撞 "aaa": 三个口径三个答案。这一格就是整套判据的自检。
	got := frelScan(t, []Re2SetFrelPattern_t{{Pattern: `aa|a`}}, "aaa", 64)
	if fmt.Sprint(got) != "[{0 0 1} {0 1 3}]" {
		t.Fatalf("最右终点最长不对: 要 [{0 0 1} {0 1 3}] 得到 %v", got)
	}
	if s := fmt.Sprint(msrBrute(`aa|a`, "aaa")); s != "[{0 2 3} {0 1 2} {0 0 1}]" {
		t.Fatalf("判据自身失效: 最右【起点】最长该是 [{0 2 3} {0 1 2} {0 0 1}], 得到 %v", s)
	}
	ll := regexp.MustCompile(`aa|a`)
	ll.Longest()
	if loc := ll.FindAllStringIndex("aaa", -1); fmt.Sprint(loc) != "[[0 2] [2 3]]" {
		t.Fatalf("判据自身失效: stdlib 的最左最长该是 [[0 2] [2 3]], 得到 %v", loc)
	}

	// b|abc 撞 "abc": 最右【终点】那一侧给整条 "abc", 最右【起点】那一侧只给中间那个 "b"。
	// 这就是为什么这一层选终点不选起点 —— 截短的左端拿去过校验位会把真命中自己毙掉。
	got = frelScan(t, []Re2SetFrelPattern_t{{Pattern: `b|abc`}}, "abc", 64)
	if fmt.Sprint(got) != "[{0 0 3}]" {
		t.Fatalf("要 [{0 0 3}] 得到 %v", got)
	}

	// 定长: "12 345 6789" 里 \d{3} 的终点是 6 和 11, 从右往左各取一次。
	got = frelScan(t, []Re2SetFrelPattern_t{{Pattern: `\d{3}`}}, "12 345 6789", 64)
	if fmt.Sprint(got) != "[{0 3 6} {0 8 11}]" {
		t.Fatalf("定长档不对: 要 [{0 3 6} {0 8 11}] 得到 %v", got)
	}
}

// frelPats 是下面几个对拍共用的名单。前五条是 matchscan.go 头注里那几个"会把猜起点的路
// 搞岔"的反例; 第六条是正向状态数爆炸那一族; 剩下几条是真门表里的形状。
// 🔴 一条 \b / ^ / $ 都不许有: 判据是拿 text[s:e] 切片跑 stdlib 的, 上下文断言在切片上
//    看到的邻居是假的 —— 那会变成判据的伪影, 不是被测对象的。
var frelPats = []string{
	`abc|b`,
	`a|ab`,
	`x{1,3}[a-c]?(?:ab|cd)?`,
	`(?:ab)?[bc]{1,2}`,
	`(?:ab)*b{1,3}`,
	`[A-Za-z][A-Za-z0-9]{2,19}key`,
	`[A-Za-z]{1,8}[-_]?\d[A-Za-z0-9\-_]{0,15}`,
	`(?i)(?:routing|aba|rtn)\D{0,24}\d{9}`,
	`\d{3}-\d{2}-\d{4}`,
}

// frelCorpus 按 pattern 自己的 AST 造一篇【真的撞得上】的语料 (随机字节撞不出真匹配 = 空转绿)。
// msrGen 在 matchscan_reverse_test.go 里, 两边共用。
func frelCorpus(ast *syntax.Regexp, rng *rand.Rand) string {
	const noise = " ,;:\"'\n\tabcXYZ019-_/@." // 全 ASCII, 理由同 msrGen 那段红字
	var sb strings.Builder
	for sb.Len() < 60+rng.Intn(80) {
		if rng.Intn(3) == 0 {
			sb.WriteByte(noise[rng.Intn(len(noise))])
			continue
		}
		msrGen(ast, rng, &sb, 0)
		sb.WriteByte(noise[rng.Intn(len(noise))])
	}
	return sb.String()
}

// TestRe2SetFrel_VsBrute 是这条路正确性的【全部】依靠。
// 它一并证明了"存活位切分量不改变答案"—— 判据 frelBrute 是【不分量】的全局穷举。
func TestRe2SetFrel_VsBrute(t *testing.T) {
	rng := rand.New(rand.NewSource(20260831))
	for _, pat := range frelPats {
		ast, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			t.Fatalf("pat=%q 解析失败: %v", pat, err)
		}
		ast = ast.Simplify()
		hits := 0
		for round := 0; round < 200; round++ {
			text := frelCorpus(ast, rng)
			got := frelScan(t, []Re2SetFrelPattern_t{{Pattern: pat}}, text, 64)
			want := frelBrute(pat, text)
			hits += len(want)
			if len(got) != len(want) {
				t.Fatalf("pat=%q text=%q\n 得到 %v\n 判据 %v", pat, text, got, want)
			}
			for i := range got {
				if got[i].Lo != want[i].Lo || got[i].Hi != want[i].Hi {
					t.Fatalf("pat=%q text=%q 第 %d 处\n 得到 %v\n 判据 %v", pat, text, i, got, want)
				}
			}
		}
		if hits < 50 {
			t.Fatalf("pat=%q 只撞出 %d 处命中 —— 语料没生效, 这一格是空转", pat, hits)
		}
	}
}

// TestRe2SetFrel_MultiPattern: 整表一起扫, 逐条与"这一条自己扫"的判据对。
// 跨 pattern 一概不合并 —— 两条 pattern 在同一片正文上照样各给各的答案。
func TestRe2SetFrel_MultiPattern(t *testing.T) {
	pats := make([]Re2SetFrelPattern_t, len(frelPats))
	asts := make([]*syntax.Regexp, len(frelPats))
	for i, p := range frelPats {
		pats[i] = Re2SetFrelPattern_t{Pattern: p}
		ast, err := syntax.Parse(p, syntax.Perl)
		if err != nil {
			t.Fatalf("pat=%q 解析失败: %v", p, err)
		}
		asts[i] = ast.Simplify()
	}
	rng := rand.New(rand.NewSource(20260831))
	for round := 0; round < 40; round++ {
		var sb strings.Builder
		for i := range asts { // 每条 pattern 都往里塞一点, 保证整表都撞得上
			sb.WriteString(frelCorpus(asts[i], rng))
		}
		text := sb.String()
		got := frelScan(t, pats, text, 64)
		byID := map[int32][]SetMatch{}
		for _, m := range got {
			byID[m.Index] = append(byID[m.Index], m)
		}
		for i, p := range frelPats {
			want := frelBrute(p, text)
			have := byID[int32(i)]
			if len(have) != len(want) {
				t.Fatalf("round=%d pat=%q 条数 %d != 判据 %d\n 得到 %v\n 判据 %v",
					round, p, len(have), len(want), have, want)
			}
			for k := range have {
				if have[k].Lo != want[k].Lo || have[k].Hi != want[k].Hi {
					t.Fatalf("round=%d pat=%q 第 %d 处 得到 %v 判据 %v", round, p, k, have[k], want[k])
				}
			}
		}
	}
}

// TestRe2SetFrel_Batch: 批缓冲大小只决定过桥次数, 不许影响结果 —— 一次一条和一次四千条
// 必须逐处相同 (分量跨批续上的那段逻辑靠这个钉住)。
func TestRe2SetFrel_Batch(t *testing.T) {
	pats := make([]Re2SetFrelPattern_t, len(frelPats))
	asts := make([]*syntax.Regexp, len(frelPats))
	for i, p := range frelPats {
		pats[i] = Re2SetFrelPattern_t{Pattern: p}
		ast, _ := syntax.Parse(p, syntax.Perl)
		asts[i] = ast.Simplify()
	}
	rng := rand.New(rand.NewSource(20260901))
	for round := 0; round < 40; round++ {
		var sb strings.Builder
		for i := range asts {
			sb.WriteString(frelCorpus(asts[i], rng))
		}
		text := sb.String()
		one := frelScan(t, pats, text, 1)
		big := frelScan(t, pats, text, 4096)
		if fmt.Sprint(one) != fmt.Sprint(big) {
			t.Fatalf("round=%d 批大小改变了结果\n 一条一批 %v\n 一次装完 %v", round, one, big)
		}
		if len(big) == 0 {
			t.Fatalf("round=%d 一处都没命中 —— 这一格是空转", round)
		}
	}
}

// TestRe2SetFrel_ExistOnly: 配了 ExistOnly 的那几条只上命中表, 不出区间;
// 命中表本身要与 RegexpSet.Match 逐条相同。
func TestRe2SetFrel_ExistOnly(t *testing.T) {
	pats := []Re2SetFrelPattern_t{
		{Pattern: `\d{3}-\d{2}-\d{4}`},
		{Pattern: `[a-z]{4,}`, ExistOnly: true},
		{Pattern: `ZZZQQQ`, ExistOnly: true},
		{Pattern: `[A-Za-z][A-Za-z0-9]{2,19}key`},
	}
	raw := make([]string, len(pats))
	for i := range pats {
		raw[i] = pats[i].Pattern
	}
	set, err := NewRegexpSetMaxMem(raw, 64<<20)
	if err != nil {
		t.Fatalf("建对照 set 失败: %v", err)
	}
	s, err := NewRe2SetFrelMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建 Re2SetFrel 失败: %v", err)
	}
	defer s.Close()
	buf := make([]Re2SetFrel_result_t, 16)
	texts := []string{
		"ab 123-45-6789 cd", "nothing here at all", "abcdkey and 111-22-3333 xyzw", "",
	}
	mbuf := make([]int32, len(pats))
	for _, text := range texts {
		var got []SetMatch
		if err := s.Scan(text, buf, func(rs []Re2SetFrel_result_t) bool {
			for _, r := range rs {
				got = append(got, SetMatch{Index: r.Index, Lo: r.Start, Hi: r.End})
			}
			return true
		}); err != nil {
			t.Fatalf("Scan 失败 text=%q: %v", text, err)
		}
		for _, m := range got {
			if pats[m.Index].ExistOnly {
				t.Fatalf("text=%q: ExistOnly 的第 %d 条不该出区间: %v", text, m.Index, m)
			}
			if text[m.Lo:m.Hi] == "" || !regexp.MustCompile(`\A(?:`+pats[m.Index].Pattern+`)\z`).
				MatchString(text[m.Lo:m.Hi]) {
				t.Fatalf("text=%q: 第 %d 条给的 %q 不是真匹配", text, m.Index, text[m.Lo:m.Hi])
			}
		}
		// 命中表与 RegexpSet.Match 对
		want := map[int32]bool{}
		for _, id := range set.Match(text, mbuf) {
			want[id] = true
		}
		for i := range pats {
			if s.Hit(i) != want[int32(i)] {
				t.Fatalf("text=%q 第 %d 条命中表不对: Frel=%v set.Match=%v",
					text, i, s.Hit(i), want[int32(i)])
			}
		}
	}
}

// TestRe2SetFrel_EmptyMatchRejected: 能匹配空串的条配区间档【建的时候】就报错, 与正文无关。
func TestRe2SetFrel_EmptyMatchRejected(t *testing.T) {
	if _, err := NewRe2SetFrel([]Re2SetFrelPattern_t{{Pattern: `a*`}}); err == nil {
		t.Fatal("a* 配区间档应当当场报错")
	}
	// 同一条配 ExistOnly 就行 —— 它只上命中表, 不进游程。
	s, err := NewRe2SetFrel([]Re2SetFrelPattern_t{{Pattern: `a*`, ExistOnly: true}})
	if err != nil {
		t.Fatalf("a* 配 ExistOnly 应当能建: %v", err)
	}
	defer s.Close()
	if err := s.Scan("bbb", make([]Re2SetFrel_result_t, 8), func(rs []Re2SetFrel_result_t) bool {
		t.Fatalf("ExistOnly 不该出区间: %v", rs)
		return false
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !s.Hit(0) {
		t.Fatal("a* 在任何正文上都命中")
	}
}

// TestRe2SetFrel_EarlyStopAndReuse: 回调返 false 当场收工 (不算错), 而且同一个工作区
// 反复 Scan 不串味。
func TestRe2SetFrel_EarlyStopAndReuse(t *testing.T) {
	s, err := NewRe2SetFrelMaxMem([]Re2SetFrelPattern_t{{Pattern: `\d{3}`}}, 64<<20)
	if err != nil {
		t.Fatalf("建 Re2SetFrel 失败: %v", err)
	}
	defer s.Close()
	text := "111 222 333 444 555"
	buf := make([]Re2SetFrel_result_t, 1)
	n := 0
	if err := s.Scan(text, buf, func(rs []Re2SetFrel_result_t) bool { n += len(rs); return n < 2 }); err != nil {
		t.Fatalf("提前收工不该报错: %v", err)
	}
	if n != 2 {
		t.Fatalf("提前收工没生效: 收了 %d 处", n)
	}
	for round := 0; round < 3; round++ {
		got := frelScan(t, []Re2SetFrelPattern_t{{Pattern: `\d{3}`}}, text, 64)
		all := 0
		if err := s.Scan(text, make([]Re2SetFrel_result_t, 64), func(rs []Re2SetFrel_result_t) bool {
			all += len(rs)
			return true
		}); err != nil {
			t.Fatalf("重复 Scan 失败: %v", err)
		}
		if all != len(got) {
			t.Fatalf("第 %d 次重复 Scan 给了 %d 处, 头一次是 %d 处", round, all, len(got))
		}
	}
}

// TestRe2SetFrel_Closed: Close 之后再用只报错, 不 panic。
func TestRe2SetFrel_Closed(t *testing.T) {
	s, err := NewRe2SetFrel([]Re2SetFrelPattern_t{{Pattern: `abc`}})
	if err != nil {
		t.Fatalf("建 Re2SetFrel 失败: %v", err)
	}
	s.Close()
	s.Close() // 幂等
	if err := s.Scan("abc", make([]Re2SetFrel_result_t, 4), nil); err == nil {
		t.Fatal("Close 之后 Scan 应当报错")
	}
	if s.Hit(0) {
		t.Fatal("Close 之后 Hit 应当是 false")
	}
}
