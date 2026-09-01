package hgmLibre2

// re2set_fll_test.go —— Re2Set_fll_t 的三条保证 (见 re2set_fll.go 文件头) 各钉一遍:
//   ① 吐出去的 body[Start:End] 一定是那条 pattern 的真匹配
//   ② 正文里有匹配的地方一定被覆盖到 (不丢召回)
//   ③ 同一条 pattern 吐的区间互不相交、按 Start 升序
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
		{`a*`, 0, PatLenUnbounded}, // 这一条编译入口会拒 (见 emptymatch.go), 但长度区间本身照算
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

// fllScanAll 把一遍 fll 的输出按 pattern 收成扁平 (start,end) 表。
func fllScanAll(t *testing.T, pats []string, text string) map[int32][]int32 {
	t.Helper()
	byPat, _, _ := scanFlat(t, newFll(t, pats).Scan, text)
	return byPat
}

// TestRe2SetFllRunCollapse —— 变长 pattern 在一片连续正文上会报一串右端, 必须收敛成
// 【最长的那几处】而不是每个右端一处; 而且不能因此把后面那处独立的命中弄丢。
// "张三李四王五" 六个汉字: 右端 6/9/12/15/18, 正确答案是 [0,12) 与 [12,18) 两处。
func TestRe2SetFllRunCollapse(t *testing.T) {
	text := "张三李四王五"
	got := fllScanAll(t, []string{`[\x{4e00}-\x{9fff}]{2,4}`}, text)[0]
	want := []int32{0, 12, 12, 18}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("得到 %v, 要 %v (%q/%q)", got, want, text[0:12], text[12:18])
	}
}

// TestRe2SetFllFixedAdjacent —— 定长 pattern 的相邻两处必须都在 (走的是减法那一档)。
func TestRe2SetFllFixedAdjacent(t *testing.T) {
	got := fllScanAll(t, []string{`[A-Z]\d{3}`}, "..A123B456..")[0]
	if fmt.Sprint(got) != fmt.Sprint([]int32{2, 6, 6, 10}) {
		t.Fatalf("得到 %v", got)
	}
}

// TestRe2SetFllCrossPatternNotMerged —— 两条 pattern 撞在同一片正文上【不许】互相吞掉:
// 下游正是靠"命中的是哪一条"分流的。
func TestRe2SetFllCrossPatternNotMerged(t *testing.T) {
	got := fllScanAll(t, []string{`[A-Z]{3}\d{3}`, `[A-Z]{3} \d{3}`}, "|ABC123| |XYZ 456|")
	if fmt.Sprint(got[0]) != fmt.Sprint([]int32{1, 7}) {
		t.Errorf("第 0 条: %v", got[0])
	}
	if fmt.Sprint(got[1]) != fmt.Sprint([]int32{10, 17}) {
		t.Errorf("第 1 条: %v", got[1])
	}
}

// TestRe2SetFllUnboundedTail —— 没有长度上限的 pattern 也走同一条路 (单条反向锚定回推)。
func TestRe2SetFllUnboundedTail(t *testing.T) {
	got := fllScanAll(t, []string{`z[a-y]{3,}z`}, "--zabcdez--zqqqz--")
	if fmt.Sprint(got[0]) != fmt.Sprint([]int32{2, 9, 11, 16}) {
		t.Fatalf("得到 %v", got)
	}
}

// TestRe2SetFllStrictVsFindAll —— 随机语料上的差分对账。两档两个口径, 按 GetPatternLenRange 分:
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
func TestRe2SetFllStrictVsFindAll(t *testing.T) {
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
		// 🔴 这里曾经还有一条 `[a-f]*` (能匹配空串), 用来钉"它必须落进 unsupported 名单"。
		//    2026-09-01 起【全库编译入口一律拒空串】(见 emptymatch.go), 名单和整条逃生通道
		//    一起没了, 这一条也就进不来了 —— 被拒这件事在 emptymatch_test.go 里逐入口钉。
	}
	// 分档就按长度区间算, 别再手写下标 —— 加一条 pattern 不用回来改这里。
	fixed := make([]bool, len(pats))
	for i, p := range pats {
		lo, hi := GetPatternLenRange(p)
		fixed[i] = lo == hi && hi >= 0
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
	ms, err := set.NewRe2Set_fll()
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
		byPat, _, _ := scanFlat(t, ms.Scan, text)
		for id := range pats {
			f := byPat[int32(id)]
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

// 🔴 这里曾经有 TestMatchScanEmptyCapableFallback / TestMatchScanSetModesRejectsEmptyCapable /
//    TestMatchScanUnsupportedNoCollateral 三格, 钉的都是"能匹配空串的 pattern 走不了区间"
//    这条逃生通道 (建工作区给一张 unsupported 静态名单 · 名单上的只许配 boolOnly · 名单不
//    连坐别的 pattern)。2026-09-01 起【全库编译入口一律拒空串】, 通道整条拆掉, 这三格随之
//    作废 —— 换成 emptymatch_test.go 里"每个编译入口扔一条 a* 进去必须当场报错"逐入口一格。

// TestRe2SetFllExistOnly —— 进了 ExistOnlyIndexList 的那几条: 位照样亮 (进 HitIndexResultFn
// 的全表命中位), 但一处区间都不收口、一次端点都不补, 一处都不交给 StartEndResultFn。
func TestRe2SetFllExistOnly(t *testing.T) {
	s := newFll(t, []string{`[A-Z]\d{3}`, `[a-f]{2,6}`})
	byPat := map[int32][]int32{}
	var hits []int32
	err := s.Scan(Re2Set_req_t{
		Body:               "A123 beef",
		ExistOnlyIndexList: []int32{0},
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool {
			for _, r := range rs {
				byPat[r.Index] = append(byPat[r.Index], r.Start, r.End)
			}
			return true
		},
		HitIndexResultFn: func(h []int32) { hits = append(hits, h...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(hits) != fmt.Sprint([]int32{0, 1}) {
		t.Fatalf("两条都该命中, 命中位表 = %v", hits)
	}
	if len(byPat[0]) != 0 {
		t.Errorf("第 0 条进了 ExistOnly, 不该交出任何区间, 却给了 %v", byPat[0])
	}
	got := byPat[1]
	if fmt.Sprint(got) != fmt.Sprint([]int32{5, 9}) {
		t.Fatalf("得到 %v, 要 [5 9] (%q)", got, "beef")
	}
}

// 🔴 这里曾经有一个 TestMatchScanSpanIsLongest: 同一批已知反例上, 默认档对 Longest()、
//    spanFast 那一档数岔开几条。2026-08-28 spanFast 整档删了, 而它对默认档那一半连语料
//    带判据都被 re2set_fll_viable_test.go 的 TestRe2SetFllViableIsLongest 整个盖住 (那边
//    的例子还多两条), 所以这里不再留一份。
