package hgmLibre2

// matchscan_test.go —— MatchScanner 的三条保证 (见 matchscan.go 文件头) 各钉一遍:
//   ① 吐出去的 text[Lo:Hi] 一定是那条 pattern 的真匹配
//   ② 正文里有匹配的地方一定被覆盖到 (不丢召回)
//   ③ 同一条 pattern 吐的区间互不相交、按 Lo 升序
// 外加长度区间 (patlen.go) 本身的表驱动。语料与 pattern 全是合成的。

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func TestPatternLenRangeTable(t *testing.T) {
	cases := []struct {
		pat      string
		min, max int
	}{
		{`abc`, 3, 3},
		{`[A-Z]\d{3}`, 4, 4},
		{`x{2,4}`, 2, 4},
		{`[\x{4e00}-\x{9fff}]{2,4}`, 6, 12}, // 三字节字符 ×2..4
		{`ab|abcd`, 2, 4},
		{`a?b`, 1, 2},
		{`a+`, 1, PatLenUnbounded},
		{`a*`, 0, PatLenUnbounded},
		{`[a-z]{4,}`, 4, PatLenUnbounded},
		{`\babc\b`, 3, 3},          // 零宽断言不占字节
		{`(?i)k`, 1, 3},            // K 的折叠轨道里有 3 字节的 U+212A
		{`(?:aa|bbb)(?:c|dd)`, 3, 5},
		{`.`, 1, 4},
	}
	for _, c := range cases {
		lo, hi := PatternLenRange(c.pat)
		if lo != c.min || hi != c.max {
			t.Errorf("%q: 得到 (%d,%d), 要 (%d,%d)", c.pat, lo, hi, c.min, c.max)
		}
	}
}

// scanByPat 把一遍 Scan 的【分批】输出按 pattern 归拢成扁平 (lo,hi) 表, 并把 unresolved
// 那几条已经收到的剔掉 (调用方对它们要走老路)。库这边【不归拢】—— 归拢就得攒, 正是分批接口
// 要躲开的 (见 matchscan.go 文件头); 归拢是调用方一句 append 的事, 这个函数就是那一句。
func scanByPat(t *testing.T, ms *MatchScanner, text string) (map[int32][]int32, map[int32]bool) {
	t.Helper()
	out := map[int32][]int32{}
	unres, err := ms.Scan(text, func(mm []SetMatch) {
		for _, m := range mm {
			out[m.Index] = append(out[m.Index], m.Lo, m.Hi)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := map[int32]bool{}
	for _, id := range unres {
		bad[id] = true
		delete(out, id)
	}
	return out, bad
}

// scanAll 把一遍 MatchScanner 的输出按 pattern 收成扁平 (lo,hi) 表。
func scanAll(t *testing.T, set *RegexpSet, text string) map[int32][]int32 {
	t.Helper()
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	var all []SetMatch
	unres, err := ms.Scan(text, func(batch []SetMatch) { all = append(all, batch...) })
	if err != nil {
		t.Fatal(err)
	}
	if len(unres) != 0 {
		t.Errorf("有 %d 条补不出左端: %v", len(unres), unres)
	}
	out := map[int32][]int32{}
	for _, m := range all {
		out[m.Index] = append(out[m.Index], m.Lo, m.Hi)
	}
	return out
}

// TestMatchScanRunCollapse —— 变长 pattern 在一片连续正文上会报一串右端, 必须收敛成
// 【最长的那几处】而不是每个右端一处; 而且不能因此把后面那处独立的命中弄丢。
// "张三李四王五" 六个汉字: 右端 6/9/12/15/18, 正确答案是 [0,12) 与 [12,18) 两处。
func TestMatchScanRunCollapse(t *testing.T) {
	set, err := NewRegexpSet([]string{`[\x{4e00}-\x{9fff}]{2,4}`})
	if err != nil {
		t.Fatal(err)
	}
	text := "张三李四王五"
	got := scanAll(t, set, text)[0]
	want := []int32{0, 12, 12, 18}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("得到 %v, 要 %v (%q/%q)", got, want, text[0:12], text[12:18])
	}
}

// TestMatchScanFixedAdjacent —— 定长 pattern 的相邻两处必须都在 (走的是减法那一档)。
func TestMatchScanFixedAdjacent(t *testing.T) {
	set, err := NewRegexpSet([]string{`[A-Z]\d{3}`})
	if err != nil {
		t.Fatal(err)
	}
	text := "..A123B456.."
	got := scanAll(t, set, text)[0]
	if fmt.Sprint(got) != fmt.Sprint([]int32{2, 6, 6, 10}) {
		t.Fatalf("得到 %v", got)
	}
}

// TestMatchScanCrossPatternNotMerged —— 两条 pattern 撞在同一片正文上【不许】互相吞掉:
// 下游正是靠"命中的是哪一条"分流的。
func TestMatchScanCrossPatternNotMerged(t *testing.T) {
	set, err := NewRegexpSet([]string{`[A-Z]{3}\d{3}`, `[A-Z]{3} \d{3}`})
	if err != nil {
		t.Fatal(err)
	}
	got := scanAll(t, set, "|ABC123| |XYZ 456|")
	if fmt.Sprint(got[0]) != fmt.Sprint([]int32{1, 7}) {
		t.Errorf("第 0 条: %v", got[0])
	}
	if fmt.Sprint(got[1]) != fmt.Sprint([]int32{10, 17}) {
		t.Errorf("第 1 条: %v", got[1])
	}
}

// TestMatchScanUnboundedTail —— 没有长度上限的 pattern 也走同一条路 (单条反向锚定回推),
// 不该落进 Unresolved。
func TestMatchScanUnboundedTail(t *testing.T) {
	set, err := NewRegexpSet([]string{`z[a-y]{3,}z`})
	if err != nil {
		t.Fatal(err)
	}
	got := scanAll(t, set, "--zabcdez--zqqqz--")
	if fmt.Sprint(got[0]) != fmt.Sprint([]int32{2, 9, 11, 16}) {
		t.Fatalf("得到 %v", got)
	}
}

// TestMatchScanStrictVsFindAll —— 随机语料上的差分对账。两档两个口径, 按 PatternLenRange 分:
//
//	定长 (min == max)  【逐字节等于 FindAll】。这一档是可以论证的 (右端定了起点就唯一),
//	                   也是唯一能拿去切片过校验位 (身份证 / IBAN mod-97 / Luhn) 的一档。
//	变长 (min < max)   只钉三条: ① 每段都是真匹配 ② 互不相交且升序 ③ FindAll 命中的每一处
//	                   都被盖到 (不整段静默)。边界【允许】和 FindAll 不同 —— 这一档给的既不是
//	                   贪心也不是最长, 见 matchscan.go 文件头"变长档"。
//
// 2026-08-25 之前这里是"变长条要么逐字节相同、要么自认 ok=false", 靠 PatternLeftmostLongestSafe
// 那道静态闸兜。闸删掉了 (它兑现不了这个承诺: ? * + {m,n} 同样是长度不齐的交替, 堵严就退化成
// min == max), 于是口径改成上面这样 —— 承诺缩到真做得到的范围里。
func TestMatchScanStrictVsFindAll(t *testing.T) {
	pats := []string{
		`[A-Z]\d{3}`,
		`[a-f]{2,6}`,
		`q[0-9a-z]{3,}q`,
		`[\x{4e00}-\x{9fff}]{2,4}`,
		`w+x`,
		`ab|abcd`,
		`\b[A-F0-9]{8}\b`,
		`x[a-f]{1,4}y`,
		`[a-f]{2}-[a-f]{2,4}`,
		`[a-f]{4}`,
		`\d{3}-\d{4}`,
		`[A-Z]{2}\d{2}[a-f]{2}`,
		`[a-f]*`, // 🔴 能匹配空串: 必须自认 ok=false 交回老路, 不许硬给答案
	}
	// 分档就按长度区间算, 别再手写下标 —— 加一条 pattern 不用回来改这里。
	fixed := make([]bool, len(pats))
	wantUnres := map[int32]bool{}
	for i, p := range pats {
		lo, hi := PatternLenRange(p)
		fixed[i] = lo == hi && hi >= 0
		if lo <= 0 {
			wantUnres[int32(i)] = true
		}
	}

	set, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatal(err)
	}
	std := make([]*regexp.Regexp, len(pats))
	anch := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		// 🔴 定长档对 FindAllStringIndex (leftmost-first), 变长档对 Longest() ——
		//    默认档走的是路 B, 它的口径是 leftmost-longest, 拿默认那个去对是【假红】。
		std[i] = regexp.MustCompile(p)
		if !fixed[i] {
			std[i].Longest()
		}
		anch[i] = regexp.MustCompile(`\A(?:` + p + `)\z`)
	}
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	alphabet := []string{"a", "b", "c", "d", "e", "f", "q", "w", "x", "y", "z",
		"0", "1", "9", "A", "B", "F", "Z", " ", "-", "张", "三", "李"}
	rng := rand.New(rand.NewSource(20260824))
	nStrict, nLoose := 0, 0
	for round := 0; round < 400; round++ {
		var sb strings.Builder
		n := 40 + rng.Intn(400)
		for i := 0; i < n; i++ {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		text := sb.String()
		byPat, bad := scanByPat(t, ms, text)
		for id := range pats {
			f := byPat[int32(id)]
			if wantUnres[int32(id)] {
				if !bad[int32(id)] && len(f) > 0 {
					t.Fatalf("轮 %d: #%d %q 能匹配空串, 必须进 unresolved 交回老路, 却给了 %v",
						round, id, pats[id], f)
				}
				continue
			}
			if bad[int32(id)] {
				t.Fatalf("轮 %d: #%d %q 意外补不出左端", round, id, pats[id])
			}
			// ① 每一段都是真匹配 · ② 互不相交且升序
			prev := int32(-1)
			for k := 0; k+1 < len(f); k += 2 {
				if !anch[id].MatchString(text[f[k]:f[k+1]]) {
					t.Fatalf("轮 %d: #%d [%d,%d)=%q 不是 %q 的匹配",
						round, id, f[k], f[k+1], text[f[k]:f[k+1]], pats[id])
				}
				if f[k] < prev {
					t.Fatalf("轮 %d: #%d 自相重叠或乱序 %v", round, id, f)
				}
				prev = f[k+1]
			}
			old := std[id].FindAllStringIndex(text, -1)
			// 两档都是【逐字节相同】, 只是对的那个 oracle 不同 (见上面建 std 那段)。
			if !fixed[id] {
				nLoose += len(old)
			} else {
				nStrict += len(old)
			}
			if len(f) != 2*len(old) {
				t.Fatalf("轮 %d: #%d %q 处数不同: 新 %d 处 %v · FindAll %d 处 %v\n正文 %q",
					round, id, pats[id], len(f)/2, f, len(old), old, text)
			}
			for k := range old {
				if int(f[2*k]) != old[k][0] || int(f[2*k+1]) != old[k][1] {
					t.Fatalf("轮 %d: #%d %q 第 %d 处不同: 新 [%d,%d)=%q · FindAll [%d,%d)=%q\n正文 %q",
						round, id, pats[id], k, f[2*k], f[2*k+1], text[f[2*k]:f[2*k+1]],
						old[k][0], old[k][1], text[old[k][0]:old[k][1]], text)
				}
			}
		}
	}
	t.Logf("400 轮: 定长档对 FindAll %d 处 · 变长档对 Longest %d 处", nStrict, nLoose)
}

// TestMatchScanEmptyCapableFallback —— 能匹配空串的 pattern 必须落进"补不出左端"那一档,
// 交给调用方走老路, 而不是在每个位置吐一处零长命中。
func TestMatchScanEmptyCapableFallback(t *testing.T) {
	set, err := NewRegexpSet([]string{`a*`, `b+`})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if unres, err := ms.Scan("--aa--bb--", func([]SetMatch) {}); err != nil {
		t.Fatal(err)
	} else if len(unres) != 1 || unres[0] != 0 {
		t.Fatalf("要 [0] (a* 走老路), 得到 %v", unres)
	}
}

// TestMatchScanBoolOnly —— 配了 boolOnly 的那几条: 位照样亮 (Hit/HitIDs), 但一处区间都不收口、
// 一次端点都不补, 一处都不交出来 (也【不】进 unresolved —— 那是调用方自己关掉的)。
func TestMatchScanBoolOnly(t *testing.T) {
	set, err := NewRegexpSet([]string{`[A-Z]\d{3}`, `[a-f]{2,6}`})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_boolOnly, MatchScanMode_span}); err != nil {
		t.Fatal(err)
	}
	byPat, bad := scanByPat(t, ms, "A123 beef")
	if !ms.Hit(0) || !ms.Hit(1) {
		t.Fatalf("两条都该命中: %v", ms.HitIDs())
	}
	if len(byPat[0]) != 0 {
		t.Errorf("第 0 条配的是 boolOnly, 不该交出任何区间, 却给了 %v", byPat[0])
	}
	if bad[0] || bad[1] {
		t.Errorf("配 boolOnly 不等于补不出来, 不该进 unresolved: %v", bad)
	}
	got := byPat[1]
	if fmt.Sprint(got) != fmt.Sprint([]int32{5, 9}) {
		t.Fatalf("得到 %v, 要 [5 9] (%q)", got, "beef")
	}
}

// TestMatchScanSpanIsLongest —— 默认档 (MatchScanMode_span, 走路 B) 的那条【无条件保证】:
// 逐字节等于 stdlib 的 re.Longest().FindAllStringIndex。
//
// 语料就是 matchscan.go 文件头列的那几个已知反例 —— 它们正是路 A 岔开的地方。测试同时把
// 同一批 pattern 用 spanFast (路 A) 再跑一遍并数出岔开了几条: 这一数不是为了钉住
// A 的答案 (那是"第三种口径", 本来就不该被钉), 是为了证明【这几条语料真的有牙齿】——
// 要是哪天 A 也全对了, 说明选的反例失效了, 该换一批。
func TestMatchScanSpanIsLongest(t *testing.T) {
	cases := []struct{ pat, text string }{
		{`abc|b`, "abc"},                       // 文件头那个最小反例
		{`x{1,3}[a-c]?(?:ab|cd)?`, "xab"},      // 12 万处对账里差出来的三条
		{`(?:ab)?[bc]{1,2}`, "axbabbyxx"},      //
		{`(?:ab)*b{1,3}`, "yaxyabbbb"},         //
		{`a|ab`, "abab"},                       // 贪心 ≠ 最长 的最小例
		{`[a-f]{2,6}`, "beefcafebabe"},         // 变长但无歧义: 两条路都该对
		{`\b[A-Z][12]\d{8}\b`, "x A123456780"}, // 定长: 档位对它不生效
	}
	divergedA := 0
	for _, c := range cases {
		set, err := NewRegexpSet([]string{c.pat})
		if err != nil {
			t.Fatalf("%q: %v", c.pat, err)
		}
		want := regexp.MustCompile(c.pat)
		want.Longest()
		var flat []int32
		for _, loc := range want.FindAllStringIndex(c.text, -1) {
			flat = append(flat, int32(loc[0]), int32(loc[1]))
		}
		for _, mode := range []MatchScanMode_t{MatchScanMode_span, MatchScanMode_spanFast} {
			ms, err := set.NewMatchScanner()
			if err != nil {
				t.Fatal(err)
			}
			if err := ms.SetModes([]MatchScanMode_t{mode}); err != nil {
				t.Fatal(err)
			}
			byPat, bad := scanByPat(t, ms, c.text)
			ms.Close()
			if bad[0] {
				t.Fatalf("%q 档 %q: 意外进了 unresolved", c.pat, mode)
			}
			got := byPat[0]
			same := fmt.Sprint(got) == fmt.Sprint(flat)
			if mode == MatchScanMode_span {
				if !same {
					t.Errorf("%q 撞 %q: 默认档给 %v, Longest 给 %v", c.pat, c.text, got, flat)
				}
				continue
			}
			if !same {
				divergedA++
				t.Logf("(预期内) %q 撞 %q: 路 A 给 %v, Longest 给 %v", c.pat, c.text, got, flat)
			}
		}
	}
	if divergedA == 0 {
		t.Errorf("一条都没岔开 —— 这批反例已经失效, 换一批, 否则这个测试是空的")
	}
}

// TestMatchScanSetModesRejectsEmptyCapable —— 能匹配空串的 pattern 想要区间, SetModes 当场
// 报错 (不是等到扫描时静默退回老路); 配成 boolOnly 则放行。
func TestMatchScanSetModesRejectsEmptyCapable(t *testing.T) {
	set, err := NewRegexpSet([]string{`a*`, `b+`})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_span, MatchScanMode_span}); err == nil {
		t.Fatalf("a* 能匹配空串却要区间, SetModes 必须报错")
	}
	if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_boolOnly, MatchScanMode_span}); err != nil {
		t.Fatalf("配成 boolOnly 应当放行: %v", err)
	}
}
