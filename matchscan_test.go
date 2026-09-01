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
		lo, hi := GetPatternLenRange(c.pat)
		if lo != c.min || hi != c.max {
			t.Errorf("%q: 得到 (%d,%d), 要 (%d,%d)", c.pat, lo, hi, c.min, c.max)
		}
	}
}

// scanByPat 把一遍 Scan 的【分批】输出按 pattern 归拢成扁平 (lo,hi) 表。
// 库这边【不归拢】—— 归拢就得攒, 正是分批接口要躲开的 (见 matchscan.go 文件头); 归拢是
// 调用方一句 append 的事, 这个函数就是那一句。
func scanByPat(t *testing.T, ms *MatchScanner, text string) map[int32][]int32 {
	t.Helper()
	out := map[int32][]int32{}
	if err := ms.Scan(text, func(mm []SetMatch) {
		for _, m := range mm {
			out[m.Index] = append(out[m.Index], m.Lo, m.Hi)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// scanAll 把一遍 MatchScanner 的输出按 pattern 收成扁平 (lo,hi) 表。
func scanAll(t *testing.T, set *RegexpSet, text string) map[int32][]int32 {
	t.Helper()
	ms, unsup, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	if len(unsup) != 0 {
		t.Errorf("有 %d 条走不了区间: %v", len(unsup), unsup)
	}
	defer ms.Close()
	var all []SetMatch
	if err := ms.Scan(text, func(batch []SetMatch) { all = append(all, batch...) }); err != nil {
		t.Fatal(err)
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

// TestMatchScanStrictVsFindAll —— 随机语料上的差分对账。两档两个口径, 按 GetPatternLenRange 分:
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
		lo, hi := GetPatternLenRange(p)
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
	ms, unsup, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	// 🔴 "这几条走不了区间"是【建工作区那一刻】给的名单, 与正文无关 —— 下面 400 轮随机
	//    正文一遍都不该改变它。这正是这一层没有"扫到一半反悔"的地方。
	gotUnsup := map[int32]bool{}
	for _, id := range unsup {
		gotUnsup[id] = true
	}
	if len(gotUnsup) != len(wantUnres) {
		t.Fatalf("unsupported 名单对不上: 得到 %v, 要 %v", unsup, wantUnres)
	}
	for id := range wantUnres {
		if !gotUnsup[id] {
			t.Fatalf("#%d %q 能匹配空串, 该在 unsupported 名单里: %v", id, pats[id], unsup)
		}
	}

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
		byPat := scanByPat(t, ms, text)
		for id := range pats {
			f := byPat[int32(id)]
			if wantUnres[int32(id)] {
				// 能匹配空串的那几条在建工作区时就报进 unsupported 了 (见下面那处断言),
				// 这一遍它们一处区间都不该交出来。
				if len(f) > 0 {
					t.Fatalf("轮 %d: #%d %q 能匹配空串, 走不了区间, 却给了 %v",
						round, id, pats[id], f)
				}
				continue
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

// TestMatchScanEmptyCapableFallback —— 能匹配空串的 pattern 必须在【建工作区】那一刻就被
// 报进 unsupported (交给调用方走老路), 而不是在每个位置吐一处零长命中, 也不是扫到一半才说。
func TestMatchScanEmptyCapableFallback(t *testing.T) {
	set, err := NewRegexpSet([]string{`a*`, `b+`})
	if err != nil {
		t.Fatal(err)
	}
	ms, unsup, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if len(unsup) != 1 || unsup[0] != 0 {
		t.Fatalf("要 unsupported=[0] (a* 能匹配空串), 得到 %v", unsup)
	}
	// 名单是静态的: 扫多少遍都不变, 而且 Scan 本身不因为它报错。
	for round := 0; round < 3; round++ {
		if err := ms.Scan("--aa--bb--", func([]SetMatch) {}); err != nil {
			t.Fatalf("轮 %d: Scan 不该因为 unsupported 报错: %v", round, err)
		}
	}
}

// TestMatchScanBoolOnly —— 配了 boolOnly 的那几条: 位照样亮 (IsHit/GetHitIDs), 但一处区间都不收口、
// 一次端点都不补, 一处都不交出来 (也【不】进 unresolved —— 那是调用方自己关掉的)。
func TestMatchScanBoolOnly(t *testing.T) {
	set, err := NewRegexpSet([]string{`[A-Z]\d{3}`, `[a-f]{2,6}`})
	if err != nil {
		t.Fatal(err)
	}
	ms, _, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_boolOnly, MatchScanMode_span}); err != nil {
		t.Fatal(err)
	}
	byPat := scanByPat(t, ms, "A123 beef")
	if !ms.IsHit(0) || !ms.IsHit(1) {
		t.Fatalf("两条都该命中: %v", ms.GetHitIDs())
	}
	if len(byPat[0]) != 0 {
		t.Errorf("第 0 条配的是 boolOnly, 不该交出任何区间, 却给了 %v", byPat[0])
	}
	got := byPat[1]
	if fmt.Sprint(got) != fmt.Sprint([]int32{5, 9}) {
		t.Fatalf("得到 %v, 要 [5 9] (%q)", got, "beef")
	}
}

// 🔴 这里曾经有一个 TestMatchScanSpanIsLongest: 同一批已知反例上, 默认档对 Longest()、
//    spanFast 那一档数岔开几条。2026-08-28 spanFast 整档删了, 而它对默认档那一半连语料
//    带判据都被 matchscan_viable_test.go 的 TestMatchScanViableIsLongest 整个盖住 (那边
//    的例子还多两条), 所以这里不再留一份。

// TestMatchScanSetModesRejectsEmptyCapable —— 能匹配空串的 pattern 想要区间, SetModes 当场
// 报错 (不是等到扫描时静默退回老路); 配成 boolOnly 则放行。
func TestMatchScanSetModesRejectsEmptyCapable(t *testing.T) {
	set, err := NewRegexpSet([]string{`a*`, `b+`})
	if err != nil {
		t.Fatal(err)
	}
	ms, unsup, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if len(unsup) != 1 || unsup[0] != 0 {
		t.Fatalf("a* 该在建工作区时就报进 unsupported: %v", unsup)
	}
	if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_span, MatchScanMode_span}); err == nil {
		t.Fatalf("a* 能匹配空串却要区间, SetModes 必须报错")
	}
	if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_boolOnly, MatchScanMode_span}); err != nil {
		t.Fatalf("配成 boolOnly 应当放行: %v", err)
	}
}

// TestMatchScanUnsupportedNoCollateral —— "这一条走不了区间"绝不许连坐同一遍里【别的】
// pattern: unsupported 是一条 pattern 自己的属性, 不是这一遍扫描的事故。
//
// 🔴 顺带钉住这一层【唯一】的两处交代: 建工作区给一张静态名单, Scan 只给一个整遍的 err。
//    从前那个"Scan 返回 unresolved 名单、每条带 ResumeFrom 断点"的中间态已经拆掉了 ——
//    调用方造不出来的错误码不该出现在返回值里 (原委见 matchscan.go 文件头)。
func TestMatchScanUnsupportedNoCollateral(t *testing.T) {
	set, err := NewRegexpSet([]string{`a*`, `\d{3}`})
	if err != nil {
		t.Fatal(err)
	}
	ms, unsup, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if len(unsup) != 1 || unsup[0] != 0 {
		t.Fatalf("要 unsupported=[0] (a* 能匹配空串), 得到 %v", unsup)
	}

	text := "aa 123 aaa 456"
	var got []SetMatch
	if err := ms.Scan(text, func(b []SetMatch) { got = append(got, b...) }); err != nil {
		t.Fatal(err)
	}
	// 第 1 条 (\d{3}) 与它无关, 必须一处不少。
	want := []SetMatch{{Index: 1, Lo: 3, Hi: 6}, {Index: 1, Lo: 11, Hi: 14}}
	var only1 []SetMatch
	for _, m := range got {
		if m.Index == 1 {
			only1 = append(only1, m)
		}
	}
	if len(only1) != len(want) {
		t.Fatalf("\\d{3} 要 %v, 得到 %v (unsupported 不该连坐别的 pattern)", want, only1)
	}
	for k := range want {
		if only1[k] != want[k] {
			t.Fatalf("\\d{3} 要 %v, 得到 %v", want, only1)
		}
	}
	// 名单上那一条一处都不该交出来。
	for _, m := range got {
		if m.Index == 0 {
			t.Fatalf("unsupported 那一条不该交出任何区间, 却给了 %+v", m)
		}
	}
}
