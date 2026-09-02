package hgmLibre2

import (
	"fmt"
	"math/rand"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// rrlScan 是"开一个 Re2Set_rrl_t, 扫一遍, 把区间收成一个数组"的测试用简写。
// 🔴 生产路径别这么写 (那块数组是 ∝ 命中数的 ratchet 缓冲, 正是分批接口要躲开的东西),
// 这里是判据代码, 语料才一百多字节。
func rrlScan(t *testing.T, pat, text string) []Re2Set_startEnd_t {
	t.Helper()
	rs, err := NewRegexpSetReverseMaxMem([]string{pat}, 64<<20)
	if err != nil {
		t.Fatalf("建反向 set 失败 pat=%q: %v", pat, err)
	}
	ms, err := rs.NewRe2Set_rrl()
	if err != nil {
		t.Fatalf("开 Re2Set_rrl_t 失败: %v", err)
	}
	defer ms.Close()
	return scanList(t, ms.Scan, text)
}

// rrlBrute 是穷举出来的 rightmost-longest 不重叠序列 —— 与本库无关, 只用 stdlib。
// 在还没被占掉的 [0,bound) 里反复取【起点最靠右】的匹配, 同起点取【最长】, 吐出去再往左。
func rrlBrute(pat, text string) []Re2Set_startEnd_t {
	re := regexp.MustCompile(`\A(?:` + pat + `)\z`)
	var out []Re2Set_startEnd_t
	bound := len(text)
	for {
		bs, be := -1, -1
		for s := bound - 1; s >= 0 && bs < 0; s-- {
			for e := bound; e > s; e-- {
				if re.MatchString(text[s:e]) {
					bs, be = s, e
					break
				}
			}
		}
		if bs < 0 {
			break
		}
		out = append(out, Re2Set_startEnd_t{Start: int32(bs), End: int32(be)})
		bound = bs
	}
	return out
}

// TestRe2SetRrl_Shape 钉住三件对外说过的事: 真匹配 · 同条不相交 · 按 Start【降序】,
// 外加 rightmost-longest 与 leftmost-longest 真的会给不同答案 (否则下面的对拍是空转)。
func TestRe2SetRrl_Shape(t *testing.T) {
	// ab|b 撞 "aab": 最左最长是 [1,3)="ab", 最右最长是 [2,3)="b" —— 方向定输赢。
	got := rrlScan(t, `ab|b`, "aab")
	if len(got) != 1 || got[0].Start != 2 || got[0].End != 3 {
		t.Fatalf("rightmost-longest 不对: 要 [[2,3)] 得到 %v", got)
	}
	ll := regexp.MustCompile(`ab|b`)
	ll.Longest()
	if loc := ll.FindAllStringIndex("aab", -1); len(loc) != 1 || loc[0][0] != 1 {
		t.Fatalf("判据自身失效: stdlib 的 leftmost-longest 该是 [[1 3]], 得到 %v", loc)
	}

	// a|ab 撞 "abab": 两边命中集相同, 只是顺序反过来 —— 降序这件事在这里最好看。
	got = rrlScan(t, `a|ab`, "abab")
	if fmt.Sprint(got) != "[{0 2 4} {0 0 2}]" {
		t.Fatalf("降序不对: 要 [{0 2 4} {0 0 2}] 得到 %v", got)
	}

	// 定长走的是那句加法, 不进正则引擎。"12 345 6789" 里最靠右的三位数起点是 8 ("789"),
	// 不是 7 ("678") —— 这一处正是 rightmost 与 leftmost 分家的地方。
	got = rrlScan(t, `\d{3}`, "12 345 6789")
	if fmt.Sprint(got) != "[{0 8 11} {0 3 6}]" {
		t.Fatalf("定长档不对: 要 [{0 8 11} {0 3 6}] 得到 %v", got)
	}
}

// astGen 按 pattern 自己的 AST 造一个真匹配。
// 🔴 只取 ASCII 可见字符: 判据是拿 text[s:e] 切片跑 stdlib 的, 切在多字节 UTF-8 中间会被当成
//    U+FFFD 从而多报一处起点 —— 那是判据的伪影, 不是被测对象的。
func astGen(re *syntax.Regexp, rng *rand.Rand, sb *strings.Builder, depth int) {
	if depth > 12 {
		return
	}
	switch re.Op {
	case syntax.OpLiteral:
		sb.WriteString(string(re.Rune))
	case syntax.OpCharClass:
		var cand []rune
		for k := 0; k+1 < len(re.Rune); k += 2 {
			lo, hi := re.Rune[k], re.Rune[k+1]
			if lo < 0x21 {
				lo = 0x21
			}
			if hi > 0x7e {
				hi = 0x7e
			}
			for c := lo; c <= hi; c++ {
				cand = append(cand, c)
			}
		}
		if len(cand) > 0 {
			sb.WriteRune(cand[rng.Intn(len(cand))])
		}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		sb.WriteByte(byte('a' + rng.Intn(26)))
	case syntax.OpCapture, syntax.OpPlus, syntax.OpConcat:
		for _, s := range re.Sub {
			astGen(s, rng, sb, depth+1)
		}
	case syntax.OpAlternate:
		astGen(re.Sub[rng.Intn(len(re.Sub))], rng, sb, depth+1)
	case syntax.OpQuest:
		if rng.Intn(2) == 0 {
			astGen(re.Sub[0], rng, sb, depth+1)
		}
	case syntax.OpStar:
		for i := rng.Intn(3); i > 0; i-- {
			astGen(re.Sub[0], rng, sb, depth+1)
		}
	case syntax.OpRepeat:
		hi := re.Max
		if hi < 0 || hi > re.Min+3 {
			hi = re.Min + 3
		}
		n := re.Min + rng.Intn(hi-re.Min+1)
		for i := 0; i < n; i++ {
			astGen(re.Sub[0], rng, sb, depth+1)
		}
	}
}

// TestRe2SetRrl_VsBrute 是这条路正确性的【全部】依靠: 语料从 pattern 自己的 AST 生成
// (随机字节撞不出真匹配 = 空转绿), 判据是上面那个与本库无关的穷举。
//
// 名单里前五条正是那几个会把"猜起点"的老路搞岔的反例 —— 反向这一侧没有"猜起点"这一步,
// 所以它们在这里应当一处都不岔。
func TestRe2SetRrl_VsBrute(t *testing.T) {
	pats := []string{
		`abc|b`,
		`a|ab`,
		`x{1,3}[a-c]?(?:ab|cd)?`,
		`(?:ab)?[bc]{1,2}`,
		`(?:ab)*b{1,3}`,
		`[A-Za-z][A-Za-z0-9]{2,19}key`, // 正向状态数爆炸那一族 (doc/状态数为什么会相乘.txt §3)
		`([^\s,;}\]"'{:\\][^,;}\]"'\n\\]{0,255})`,
		`[A-Za-z]{1,8}[-_]?\d[A-Za-z0-9\-_]{0,15}`,
		`(?i)(?:routing|aba|rtn)\D{0,24}\d{9}`,
		`\d{3}-\d{2}-\d{4}`, // 定长
	}
	noise := " ,;:\"'\n\tabcXYZ019-_/@." // 全 ASCII, 理由同 astGen 那段红字
	rng := rand.New(rand.NewSource(20260827))
	for _, pat := range pats {
		ast, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			t.Fatalf("pat=%q 解析失败: %v", pat, err)
		}
		ast = ast.Simplify()
		hits := 0
		for round := 0; round < 200; round++ {
			var sb strings.Builder
			for sb.Len() < 60+rng.Intn(80) {
				if rng.Intn(3) == 0 {
					sb.WriteByte(noise[rng.Intn(len(noise))])
					continue
				}
				astGen(ast, rng, &sb, 0)
				sb.WriteByte(noise[rng.Intn(len(noise))])
			}
			text := sb.String()
			want := rrlBrute(pat, text)
			hits += len(want)
			got := rrlScan(t, pat, text)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("pat=%q text=%q\n  本路 %v\n  判据 %v", pat, text, got, want)
			}
		}
		if hits == 0 {
			t.Fatalf("pat=%q 判据一处都没命中 —— 语料没造对, 这一条是空转绿", pat)
		}
		t.Logf("%-46s 200 份语料 · 判据命中 %d 处 · 零岔开", pat, hits)
	}
}

// TestRe2SetRrl_ExistOnly 钉住 ExistOnlyIndexList: 进名单的那几条照样上命中位表,
// 但一处区间都不收口。
//
// 🔴 这一格从前叫 TestMatchScanReverse_Modes, 里面一半是"x* 能匹配空串 ⟹ 建工作区时报进
//    unsupported / 配 span 当场报错"。2026-09-01 起【全库编译入口一律拒空串】, x* 连表都
//    进不来了, 那一半整个作废 —— 被拒这件事在 emptymatch_test.go 里逐入口钉。
func TestRe2SetRrl_ExistOnly(t *testing.T) {
	rs, err := NewRegexpSetReverseMaxMem([]string{`\d{3}`, `[a-z]+`}, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := rs.NewRe2Set_rrl()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	var got []Re2Set_startEnd_t
	var hits []int32
	if err := ms.Scan(Re2Set_req_t{
		Body:               "ab 123 cd",
		ExistOnlyIndexList: []int32{1},
		StartEndResultFn:   func(rs []Re2Set_startEnd_t) bool { got = append(got, rs...); return true },
		HitIndexResultFn:   func(h []int32) { hits = append(hits, h...) },
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(hits) != "[0 1]" {
		t.Fatalf("命中位表不对: %v", hits)
	}
	if fmt.Sprint(got) != "[{0 3 6}]" {
		t.Fatalf("ExistOnly 那一条不该出区间: %v", got)
	}
}
