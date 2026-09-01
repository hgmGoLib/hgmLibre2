package hgmLibre2

import (
	"fmt"
	"math/rand"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"testing"
)

// frelOpen 开一张表 + 一个 frel 工作区 (测试结束自动 Close)。
func frelOpen(t *testing.T, pats []string) *Re2Set_frel_t {
	t.Helper()
	set, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建表失败 pats=%v: %v", pats, err)
	}
	s, err := set.NewRe2Set_frel()
	if err != nil {
		t.Fatalf("建 Re2Set_frel_t 失败 pats=%v: %v", pats, err)
	}
	t.Cleanup(s.Close)
	return s
}

// frelScan 是"开一个 Re2Set_frel_t, 扫一遍, 把区间收成一个数组"的测试用简写。
// 🔴 生产路径别这么写 (那块数组是 ∝ 命中数的 ratchet 缓冲, 正是分批接口要躲开的东西),
// 这里是判据代码, 语料才一百多字节。batch 只决定过桥次数, 不影响结果。
func frelScan(t *testing.T, pats []string, text string, batch int) []Re2Set_startEnd_t {
	t.Helper()
	return frelScanOn(t, frelOpen(t, pats), text, batch)
}

// frelScanOn 同上, 但用现成的工作区 (复用/不串味那几格要它)。
func frelScanOn(t *testing.T, s *Re2Set_frel_t, text string, batch int) []Re2Set_startEnd_t {
	t.Helper()
	var got []Re2Set_startEnd_t
	a := NewRe2Set_allocBatch(batch) // 只有对拍才去掐这个数, 生产走默认批
	err := s.Scan(text, &Re2Set_req_t{
		Allocer: a,
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool {
			if len(rs) > batch {
				t.Fatalf("一批交了 %d 处, 超过批缓冲 %d", len(rs), batch)
			}
			got = append(got, rs...)
			return true
		},
	})
	if err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	return got
}

// frelBrute 是穷举出来的【最右终点最长】不重叠序列 —— 与本库无关, 只用 stdlib。
// 上界从正文末尾起, 反复取"终点最靠右且 <= 上界"的匹配, 同终点取最长 (= 起点最靠左),
// 收下之后把上界压到它的起点。返回升序。
//
// 🔴 这不是 msrBrute (反向 MatchScanner 那个)。那个先挑【起点】最靠右的, 这个先挑
//    【终点】最靠右的 —— aa|a 撞 "aaa" 两者给出完全不同的答案, 见 TestRe2SetFrel_Shape。
func frelBrute(pat, text string) []Re2Set_startEnd_t {
	re := regexp.MustCompile(`\A(?:` + pat + `)\z`)
	var out []Re2Set_startEnd_t
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
		out = append(out, Re2Set_startEnd_t{Start: int32(bs), End: int32(be)})
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
	got := frelScan(t, []string{`aa|a`}, "aaa", 64)
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
	got = frelScan(t, []string{`b|abc`}, "abc", 64)
	if fmt.Sprint(got) != "[{0 0 3}]" {
		t.Fatalf("要 [{0 0 3}] 得到 %v", got)
	}

	// 定长: "12 345 6789" 里 \d{3} 的终点是 6 和 11, 从右往左各取一次。
	got = frelScan(t, []string{`\d{3}`}, "12 345 6789", 64)
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
// msrGen 在 re2set_rrl_test.go 里, 两边共用。
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
			got := frelScan(t, []string{pat}, text, 64)
			want := frelBrute(pat, text)
			hits += len(want)
			if len(got) != len(want) {
				t.Fatalf("pat=%q text=%q\n 得到 %v\n 判据 %v", pat, text, got, want)
			}
			for i := range got {
				if got[i].Start != want[i].Start || got[i].End != want[i].End {
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
	asts := make([]*syntax.Regexp, len(frelPats))
	for i, p := range frelPats {
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
		got := frelScan(t, frelPats, text, 64)
		byID := map[int32][]Re2Set_startEnd_t{}
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
				if have[k].Start != want[k].Start || have[k].End != want[k].End {
					t.Fatalf("round=%d pat=%q 第 %d 处 得到 %v 判据 %v", round, p, k, have[k], want[k])
				}
			}
		}
	}
}

// TestRe2SetFrel_Batch: 批缓冲大小只决定过桥次数, 不许影响结果 —— 一次一条和一次四千条
// 必须逐处相同 (分量跨批续上的那段逻辑靠这个钉住)。
func TestRe2SetFrel_Batch(t *testing.T) {
	asts := make([]*syntax.Regexp, len(frelPats))
	for i, p := range frelPats {
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
		one := frelScan(t, frelPats, text, 1)
		big := frelScan(t, frelPats, text, 4096)
		if fmt.Sprint(one) != fmt.Sprint(big) {
			t.Fatalf("round=%d 批大小改变了结果\n 一条一批 %v\n 一次装完 %v", round, one, big)
		}
		if len(big) == 0 {
			t.Fatalf("round=%d 一处都没命中 —— 这一格是空转", round)
		}
	}
}

// TestRe2SetFrel_ExistOnly: 进了 ExistOnlyIndexList 的那几条只上命中表, 不出区间;
// 命中表本身要与 RegexpSet.Match 逐条相同。
//
// 🔴 ExistOnly 是【每遍】的参数 (不是建对象时定死的属性), 所以这一格顺带钉住:
//    同一个工作区, 名单换来换去, 命中位表【逐位不变】—— 名单只该改成本, 不该改答案。
func TestRe2SetFrel_ExistOnly(t *testing.T) {
	pats := []string{
		`\d{3}-\d{2}-\d{4}`,
		`[a-z]{4,}`,
		`ZZZQQQ`,
		`[A-Za-z][A-Za-z0-9]{2,19}key`,
	}
	existOnly := []int32{1, 2}
	isExistOnly := map[int32]bool{1: true, 2: true}
	set, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建对照 set 失败: %v", err)
	}
	s, err := set.NewRe2Set_frel()
	if err != nil {
		t.Fatalf("建 Re2Set_frel_t 失败: %v", err)
	}
	defer s.Close()
	texts := []string{
		"ab 123-45-6789 cd", "nothing here at all", "abcdkey and 111-22-3333 xyzw", "",
	}
	mbuf := make([]int32, len(pats))
	a := NewRe2Set_alloc()
	for _, text := range texts {
		var got []Re2Set_startEnd_t
		var hits []int32
		if err := s.Scan(text, &Re2Set_req_t{
			Allocer:            a,
			ExistOnlyIndexList: existOnly,
			StartEndResultFn: func(rs []Re2Set_startEnd_t) bool {
				got = append(got, rs...)
				return true
			},
			HitIndexResultFn: func(h []int32) bool { hits = append(hits, h...); return true },
		}); err != nil {
			t.Fatalf("Scan 失败 text=%q: %v", text, err)
		}
		for _, m := range got {
			if isExistOnly[m.Index] {
				t.Fatalf("text=%q: ExistOnly 的第 %d 条不该出区间: %v", text, m.Index, m)
			}
			if text[m.Start:m.End] == "" || !regexp.MustCompile(`\A(?:`+pats[m.Index]+`)\z`).
				MatchString(text[m.Start:m.End]) {
				t.Fatalf("text=%q: 第 %d 条给的 %q 不是真匹配", text, m.Index, text[m.Start:m.End])
			}
		}
		// 命中表与 RegexpSet.Match 对
		var want []int32
		for _, id := range set.Match(text, mbuf) {
			want = append(want, id)
		}
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if fmt.Sprint(hits) != fmt.Sprint(want) {
			t.Fatalf("text=%q 命中表不对: frel=%v set.Match=%v", text, hits, want)
		}
		// 换一张名单 (全表都要区间 / 全表都只要位), 命中位表必须逐位相同。
		for _, list := range [][]int32{nil, {0, 1, 2, 3}, {3}} {
			var h2 []int32
			if err := s.Scan(text, &Re2Set_req_t{
				Allocer:            a,
				ExistOnlyIndexList: list,
				StartEndResultFn:   func([]Re2Set_startEnd_t) bool { return true },
				HitIndexResultFn:   func(h []int32) bool { h2 = append(h2, h...); return true },
			}); err != nil {
				t.Fatalf("Scan 失败 text=%q list=%v: %v", text, list, err)
			}
			if fmt.Sprint(h2) != fmt.Sprint(want) {
				t.Fatalf("text=%q list=%v: ExistOnly 名单改变了命中位表: %v != %v", text, list, h2, want)
			}
		}
	}
}

// TestRe2SetFrel_EarlyStopAndReuse: 回调返 false 当场收工 (不算错), 而且同一个工作区
// 反复 Scan 不串味。
func TestRe2SetFrel_EarlyStopAndReuse(t *testing.T) {
	s := frelOpen(t, []string{`\d{3}`})
	text := "111 222 333 444 555"
	n := 0
	a := NewRe2Set_allocBatch(1)
	if err := s.Scan(text, &Re2Set_req_t{
		Allocer:          a,
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { n += len(rs); return n < 2 },
	}); err != nil {
		t.Fatalf("提前收工不该报错: %v", err)
	}
	if n != 2 {
		t.Fatalf("提前收工没生效: 收了 %d 处", n)
	}
	want := frelScan(t, []string{`\d{3}`}, text, 64)
	for round := 0; round < 3; round++ {
		got := frelScanOn(t, s, text, 64)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("第 %d 次重复 Scan 给了 %v, 头一次是 %v", round, got, want)
		}
	}
}

// TestRe2SetFrel_Closed: Close 之后再用只报错, 不 panic。
func TestRe2SetFrel_Closed(t *testing.T) {
	set, err := NewRegexpSet([]string{`abc`})
	if err != nil {
		t.Fatal(err)
	}
	s, err := set.NewRe2Set_frel()
	if err != nil {
		t.Fatalf("建 Re2Set_frel_t 失败: %v", err)
	}
	s.Close()
	s.Close() // 幂等
	err = s.Scan("abc", &Re2Set_req_t{StartEndResultFn: func([]Re2Set_startEnd_t) bool { return true }})
	if err == nil {
		t.Fatal("Close 之后 Scan 应当报错")
	}
}
