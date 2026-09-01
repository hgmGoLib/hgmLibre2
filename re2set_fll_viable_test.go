package hgmLibre2

import (
	"fmt"
	"regexp"
	"testing"
)

// re2set_fll_viable_test.go —— fll 补起点那条路 (反向种全部状态收候选 + 升序逐个
// 正向锚定验) 的回归。GetViableStarts 本身的单元测试也在这里。
//
// 🔴 2026-08-28 之前这条路叫"路 D2", 挂在一个独立类型 MatchScanner2 上, 与老的路 A
//    (spanFast) / 路 B (默认档) 并存比价。比完了, A/B 和那个类型一起删了, 这几个用例
//    跟着落到 Re2Set_fll_t 上 —— 现在它们钉的就是【唯一那条路】。

// viablePats 是对拍用的 pattern 表。选的时候两件事都要:
//   · 前七条是【老的路 A 已知会岔开】的形状 (最小反例 + 那个 \b(?:ab cd ef|cd)\b);
//     留着不是为了钉 A (它已经不存在了), 是因为这几条正是"只种 accept 看不见真起点"的
//     那种形状 —— 它们仍然是这条路最有牙齿的语料。
//   · 后面几条是"变长但无歧义"和"定长", 用来证明没把简单情形做坏。
var viablePats = []string{
	`abc|b`,
	`x{1,3}[a-c]?(?:ab|cd)?`,
	`(?:ab)?[bc]{1,2}`,
	`(?:ab)*b{1,3}`,
	`a|ab`,
	`\b(?:ab cd ef|cd)\b`,
	`ab|abcd`,
	`[a-f]{2,6}`,
	`q[0-9a-z]{3,}q`,
	`[\x{4e00}-\x{9fff}]{2,4}`,
	`\b[A-F0-9]{8}\b`,
	`x[a-f]{1,4}y`,
	`AAA-[A-Za-z0-9]{8,16}`,
	`\b[A-Z][12]\d{8}\b`,
}

// TestRe2SetFllViableIsLongest —— 那条【无条件保证】: 逐字节等于 stdlib 的
// re.Longest().FindAllStringIndex。
//
// 语料是老的路 A 岔开过的那几个最小反例 —— 这条路的分歧全出在"同一个右端上有好几个
// 可行起点"的形状里, 这批就是那种形状。
func TestRe2SetFllViableIsLongest(t *testing.T) {
	cases := []struct{ pat, text string }{
		{`abc|b`, "abc"},
		{`x{1,3}[a-c]?(?:ab|cd)?`, "xab"},
		{`(?:ab)?[bc]{1,2}`, "axbabbyxx"},
		{`(?:ab)*b{1,3}`, "yaxyabbbb"},
		{`a|ab`, "abab"},
		{`\b(?:ab cd ef|cd)\b`, "ab cd ef"},
		{`ab|abcd`, "xabcdy"},
		{`[a-f]{2,6}`, "beefcafebabe"},
		{`\b[A-Z][12]\d{8}\b`, "x A123456780"},
	}
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
		ms, err := set.NewRe2Set_fll()
		if err != nil {
			t.Fatal(err)
		}
		byPat, _, _ := scanFlat(t, ms.Scan, c.text)
		got := byPat[0]
		ms.Close()
		if fmt.Sprint(got) != fmt.Sprint(flat) {
			t.Errorf("%q 撞 %q: 给 %v, Longest 给 %v", c.pat, c.text, got, flat)
		}
	}
}

// TestRe2SetFllVsLongestFuzz —— 整张表 × 随机正文, 每条 pattern 都要与
// re.Longest().FindAllStringIndex 【逐字节】相同。
//
// 🔴 oracle 必须是 Longest() 那个。stdlib 默认的 FindAll 是 leftmost-first (贪心),
//    两者在"同一起点上贪心先撞到的比最长的短"时给不同的右端, 拿默认那个对是【假红】。
func TestRe2SetFllVsLongestFuzz(t *testing.T) {
	set, err := NewRegexpSet(viablePats)
	if err != nil {
		t.Fatal(err)
	}
	std := make([]*regexp.Regexp, len(viablePats))
	for i, p := range viablePats {
		std[i] = regexp.MustCompile(p)
		std[i].Longest()
	}
	ms, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	// 语料按【pattern 自己的字母表】撒, 否则随机正文上一条都命中不了, 测试是空转绿。
	const alpha = "abcdefqxyz ABCDEF12345678-张三李四王五"
	rnd := benchLCG(20260827)
	rs := []rune(alpha)
	total, cmp := 0, 0
	for round := 0; round < 400; round++ {
		n := 1 + rnd(120)
		buf := make([]rune, n)
		for i := range buf {
			buf[i] = rs[rnd(len(rs))]
		}
		text := string(buf)
		byPat, _, _ := scanFlat(t, ms.Scan, text)
		for i := range viablePats {
			var flat []int32
			for _, loc := range std[i].FindAllStringIndex(text, -1) {
				flat = append(flat, int32(loc[0]), int32(loc[1]))
			}
			cmp++
			total += len(flat) / 2
			if fmt.Sprint(byPat[int32(i)]) != fmt.Sprint(flat) {
				t.Fatalf("round=%d pattern %d %q 撞 %q:\n  给的    %v\n  Longest %v",
					round, i, viablePats[i], text, byPat[int32(i)], flat)
			}
		}
	}
	if total == 0 {
		t.Fatalf("对账了 %d 条 pattern-正文, 一处命中都没有 —— 语料没牙齿, 这个测试是空的", cmp)
	}
	t.Logf("对账 %d 条 pattern-正文 · 共 %d 处命中, 岔开 0", cmp, total)
}

// TestRe2SetFllBenchTableVsLongest —— 换成【整张真表 benchPats × 三档语料】再对一遍
// re.Longest().FindAllStringIndex。上面那个 fuzz 是"小表 × 随机短正文"(形状刁钻但正文小),
// 这个是"大表 × 长正文"(形状普通但游标要真的走很多步) —— 两头都要。
//
// 🔴 2026-08-28 之前这里是 TestMatchScan2SameAsPathB: D2 与路 B 两条自家实现互相对拍。
//    路 B 删了之后那个对拍没了对手, 而且拿自家实现当 oracle 本来就弱一档 —— 换成 stdlib。
func TestRe2SetFllBenchTableVsLongest(t *testing.T) {
	set, err := NewRegexpSet(benchPats)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	std := make([]*regexp.Regexp, len(benchPats))
	for i, p := range benchPats {
		std[i] = regexp.MustCompile(p)
		std[i].Longest()
	}
	spans := 0
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		got, _, st := scanFlat(t, ms.Scan, text)
		for i := range benchPats {
			var flat []int32
			for _, loc := range std[i].FindAllStringIndex(text, -1) {
				flat = append(flat, int32(loc[0]), int32(loc[1]))
			}
			if fmt.Sprint(got[int32(i)]) != fmt.Sprint(flat) {
				t.Fatalf("语料 %s · pattern %d %q 岔开:\n  给的    %v\n  Longest %v",
					kind, i, benchPats[i], got[int32(i)], flat)
			}
			spans += len(flat) / 2
		}
		t.Logf("语料 %-4s · 账: walks=%d cands=%d tries=%d emits=%d (试/看=%.2f)",
			kind, st.Walks, st.Cands, st.Tries, st.Emits,
			float64(st.Tries)/float64(max1(st.Walks)))
	}
	if spans == 0 {
		t.Fatal("三档语料一处区间都没有 —— 这个测试是空的")
	}
	t.Logf("三档语料共对账 %d 处区间, 岔开 0", spans)
}

func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}

// 每一行都是手算得出来的: 候选起点 s ⟺ text[s:from) 是这条 pattern 【某个匹配的前缀】,
// 且 s < from (空前缀不算)。给出来的顺序是【降序】。
//
// 🔴 第一行就是老的路 A 那条"第三种口径"的病根: 门给的最小右端是 5 ("cd" 那处), 只种 accept
//    只看得见 3, 而真正的 leftmost 起点 0 要靠"可行前缀"才看得见 —— text[0:5)="ab cd"
//    不是匹配, 但再补 " ef" 就是。
// 🔴 最后两行是那道"只种锚定入口的可达闭包, 不是 1..size 全塞"的闸: 全塞的话 set 程序里
//    那截 .*? 非锚定前缀也被种上, 机器永远死不掉 —— `b` 撞 "abc" 问 from=3 会多报一个 1
//    出来 (text[1:3)="bc" 根本不是 `b` 的前缀)。
func TestViableStartsBasics(t *testing.T) {
	cases := []struct {
		pat  string
		text string
		from int32
		want string // 降序
	}{
		{`\b(?:ab cd ef|cd)\b`, "ab cd ef", 5, "[3 0]"},
		{`abc|b`, "abc", 2, "[1 0]"},
		{`abc|b`, "abc", 3, "[0]"},
		{`a|a+b`, "aaaab", 1, "[0]"},
		{`AAA-[A-Za-z0-9]{8,16}`, "xxAAA-abcdefgh12345yy", 14, "[2]"},
		{`abc`, "abc", 3, "[0]"},
		{`abc`, "abc", 1, "[0]"},
		{`xyz`, "abc", 3, "[]"},
		{`b`, "abc", 2, "[1]"},
		{`b`, "abc", 3, "[]"},
	}
	for _, c := range cases {
		rs, err := NewRegexpSetReverseMaxMem([]string{c.pat}, 0)
		if err != nil {
			t.Fatalf("%q: %v", c.pat, err)
		}
		buf := make([]int32, 64)
		n, err := rs.GetViableStarts(c.text, c.from, 0, 0, buf)
		if err != nil {
			t.Fatalf("%q: %v", c.pat, err)
		}
		if n > len(buf) {
			t.Fatalf("%q: 候选 %d 条, 缓冲只有 %d —— 这几个例子不该撑爆", c.pat, n, len(buf))
		}
		if got := fmt.Sprint(buf[:n]); got != c.want {
			t.Errorf("%q 撞 %q from=%d: 给 %s, 该是 %s", c.pat, c.text, c.from, got, c.want)
		}
	}
}

// TestViableStartsBufTooSmall —— 缓冲不够时的交代: 返回值是【总条数】(> len(out)),
// 调用方按它换个更大的重来一次就对得上。
func TestViableStartsBufTooSmall(t *testing.T) {
	rs, err := NewRegexpSetReverseMaxMem([]string{`[a-z]{1,}`}, 0)
	if err != nil {
		t.Fatal(err)
	}
	text := "abcdefghijklmnopqrst"
	small := make([]int32, 3)
	n, err := rs.GetViableStarts(text, int32(len(text)), 0, 0, small)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(text) {
		t.Fatalf("[a-z]{1,} 撞 %d 个小写字母, 候选该是 %d 条, 给的是 %d", len(text), len(text), n)
	}
	big := make([]int32, n)
	n2, err := rs.GetViableStarts(text, int32(len(text)), 0, 0, big)
	if err != nil || n2 != n {
		t.Fatalf("换大缓冲重来一次该给同样的 %d 条, 给的是 %d (err=%v)", n, n2, err)
	}
	if big[0] != int32(len(text)-1) || big[n-1] != 0 {
		t.Fatalf("该是降序 %d..0, 给的是 %v", len(text)-1, big)
	}
}

// TestRe2SetFllViableNoAlloc —— 稳态零分配。
//
// 🔴 这不是洁癖: GetViableStarts 是"每个右端问一次"的调用形态, 那里漏一笔 4 字节就按右端数
//    放大 (第一版在函数里开了个局部数组当"len(out)==0 时的落点", 地址交给 C 之后逃逸分析
//    每次调用把它搬上堆 —— benchPats/命中稀疏 上 33 次回推正好 33 笔)。同一类账见
//    spanresolve.go 走 _r 孪生那一段, 和 TestSpanPerf_NoAlloc。
func TestRe2SetFllViableNoAlloc(t *testing.T) {
	set, err := NewRegexpSet(benchPats)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	text := benchCorpus("few")
	sink := 0
	// 🔴 req 和 alloc 都【建一次留着】—— 每遍现造一个就是每遍一笔分配, 那正是这一格要量的东西。
	req := Re2Set_req_t{
		Body:             text,
		Allocer:          NewRe2Set_alloc(),
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { sink += len(rs); return true },
	}
	run := func() {
		if err := ms.Scan(req); err != nil {
			t.Fatal(err)
		}
	}
	run() // 热身: 补端点的单条对象惰性建 + 各处缓冲定型, 都只发生一次
	run()
	got := testing.AllocsPerRun(20, run)
	if sink == 0 {
		t.Fatal("一处命中都没有 —— 这个测试是空的")
	}
	if got > 0 {
		t.Errorf("稳态 %.1f 笔/遍 —— 该是 0 笔: req/alloc 都是复用的, 回推那一步也不该漏", got)
	}
}
