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

// scanAll 把一遍 MatchScanner 的输出按 pattern 收成扁平 (lo,hi) 表。
func scanAll(t *testing.T, set *RegexpSet, text string) map[int32][]int32 {
	t.Helper()
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	var all []SetMatch
	all, unres, err := ms.AppendAllMatches(nil, text)
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

// TestMatchScanVsFindAllRandom —— 随机语料上对三条保证做批量对账 (合成 pattern + 合成正文)。
// 口径不是"与 FindAll 逐字节相同" (交替式上本来就不同), 而是:
// 真匹配 · 不丢召回 (FindAll 的每一处都被覆盖) · 同条不自相重叠。
func TestMatchScanVsFindAllRandom(t *testing.T) {
	pats := []string{
		`[A-Z]\d{3}`,
		`[a-f]{2,6}`,
		`q[0-9a-z]{3,}q`,
		`[\x{4e00}-\x{9fff}]{2,4}`,
		`w+x`,
		`ab|abcd`,
		`\b[A-F0-9]{8}\b`,
	}
	set, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatal(err)
	}
	anch := make([]*regexp.Regexp, len(pats))
	std := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		anch[i] = regexp.MustCompile(`\A(?:` + p + `)\z`)
		std[i] = regexp.MustCompile(p)
	}
	alphabet := []string{"a", "b", "c", "d", "e", "f", "q", "w", "x", "z",
		"0", "1", "9", "A", "B", "F", "Z", " ", "-", "张", "三", "李"}
	rng := rand.New(rand.NewSource(20260824))
	for round := 0; round < 40; round++ {
		var sb strings.Builder
		for i := 0; i < 400; i++ {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		text := sb.String()
		got := scanAll(t, set, text)
		for id, f := range got {
			prev := int32(-1)
			for k := 0; k+1 < len(f); k += 2 {
				if !anch[id].MatchString(text[f[k]:f[k+1]]) {
					t.Fatalf("轮 %d: #%d [%d,%d)=%q 不是 %q 的匹配",
						round, id, f[k], f[k+1], text[f[k]:f[k+1]], pats[id])
				}
				if f[k] < prev {
					t.Fatalf("轮 %d: #%d 区间自相重叠或乱序 %v", round, id, f)
				}
				prev = f[k+1]
			}
		}
		for id := range pats {
			f := got[int32(id)]
			for _, loc := range std[id].FindAllStringIndex(text, -1) {
				hit := false
				for k := 0; k+1 < len(f); k += 2 {
					if int(f[k]) < loc[1] && loc[0] < int(f[k+1]) {
						hit = true
						break
					}
				}
				if !hit {
					t.Fatalf("轮 %d: #%d FindAll 的 [%d,%d)=%q 没被覆盖到; 新腿给的是 %v",
						round, id, loc[0], loc[1], text[loc[0]:loc[1]], f)
				}
			}
		}
	}
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
	if _, unres, err := ms.AppendAllMatches(nil, "--aa--bb--"); err != nil {
		t.Fatal(err)
	} else if len(unres) != 1 || unres[0] != 0 {
		t.Fatalf("要 [0] (a* 走老路), 得到 %v", unres)
	}
}

// TestMatchScanWanted —— 没进 SetWanted 的那几条: 位照样亮 (Hit/HitIDs), 但一个游程不攒、
// 一次左端不补, AppendMatches 返回 ok=false 让调用方走老路。
func TestMatchScanWanted(t *testing.T) {
	set, err := NewRegexpSet([]string{`[A-Z]\d{3}`, `[a-f]{2,6}`})
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	ms.SetWanted([]bool{false, true})
	if err := ms.Scan("A123 beef"); err != nil {
		t.Fatal(err)
	}
	if !ms.Hit(0) || !ms.Hit(1) {
		t.Fatalf("两条都该命中: %v", ms.HitIDs())
	}
	if _, ok, _ := ms.AppendMatches(nil, 0); ok {
		t.Errorf("第 0 条没在 wanted 里, 该返回 ok=false")
	}
	got, ok, err := ms.AppendMatches(nil, 1)
	if err != nil || !ok {
		t.Fatalf("第 1 条: ok=%v err=%v", ok, err)
	}
	if fmt.Sprint(got) != fmt.Sprint([]int32{5, 9}) {
		t.Fatalf("得到 %v, 要 [5 9] (%q)", got, "beef")
	}
}
