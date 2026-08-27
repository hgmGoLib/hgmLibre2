package hgmLibre2

import (
	"fmt"
	"regexp"
	"testing"
)

// scan2ByPat 把一遍 MatchScanner2 的分批输出按 pattern 归拢成扁平 (lo,hi) 表。
// 与 scanByPat 逐字同解, 只是对象换成 MatchScanner2。
func scan2ByPat(t *testing.T, ms *MatchScanner2, text string) map[int32][]int32 {
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

// ms2Pats 是对拍用的 pattern 表。选的时候两件事都要:
//   · 前七条是【路 A 已知会岔开】的形状 (matchscan.go 文件头列的那几个 + 最小反例);
//   · 后面几条是"变长但无歧义"和"定长", 用来证明 D2 没有把简单情形做坏。
var ms2Pats = []string{
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

// TestMatchScan2IsLongest —— MatchScanner2 (路 D2) 那条【无条件保证】:
// 逐字节等于 stdlib 的 re.Longest().FindAllStringIndex。
//
// 语料是 matchscan.go 文件头列的那几个已知反例 —— 正是路 A 岔开的地方。测试顺手把同一批
// 用 spanFast (路 A) 再跑一遍数出岔开几条: 这一数【不是】为了钉住 A 的答案 (那是"第三种
// 口径", 本来就不该被钉), 是为了证明这几条语料真的有牙齿 —— 要是哪天 A 也全对了,
// 说明反例失效了, 该换一批。
func TestMatchScan2IsLongest(t *testing.T) {
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
		ms2, _, err := set.NewMatchScanner2()
		if err != nil {
			t.Fatal(err)
		}
		got := scan2ByPat(t, ms2, c.text)[0]
		ms2.Close()
		if fmt.Sprint(got) != fmt.Sprint(flat) {
			t.Errorf("%q 撞 %q: D2 给 %v, Longest 给 %v", c.pat, c.text, got, flat)
		}

		ms, _, err := set.NewMatchScanner()
		if err != nil {
			t.Fatal(err)
		}
		if err := ms.SetModes([]MatchScanMode_t{MatchScanMode_spanFast}); err != nil {
			t.Fatal(err)
		}
		gotA := scanByPat(t, ms, c.text)[0]
		ms.Close()
		if fmt.Sprint(gotA) != fmt.Sprint(flat) {
			divergedA++
			t.Logf("(预期内) %q 撞 %q: 路 A 给 %v, Longest 给 %v", c.pat, c.text, gotA, flat)
		}
	}
	if divergedA == 0 {
		t.Errorf("一条都没岔开 —— 这批反例已经失效, 换一批, 否则这个测试是空的")
	}
}

// TestMatchScan2VsLongestFuzz —— 整张表 × 随机正文, 每条 pattern 都要与
// re.Longest().FindAllStringIndex 【逐字节】相同。
//
// 🔴 oracle 必须是 Longest() 那个。stdlib 默认的 FindAll 是 leftmost-first (贪心),
//    两者在"同一起点上贪心先撞到的比最长的短"时给不同的右端, 拿默认那个对是【假红】。
func TestMatchScan2VsLongestFuzz(t *testing.T) {
	set, err := NewRegexpSet(ms2Pats)
	if err != nil {
		t.Fatal(err)
	}
	std := make([]*regexp.Regexp, len(ms2Pats))
	for i, p := range ms2Pats {
		std[i] = regexp.MustCompile(p)
		std[i].Longest()
	}
	ms2, unsup, err := set.NewMatchScanner2()
	if err != nil {
		t.Fatal(err)
	}
	defer ms2.Close()
	if len(unsup) != 0 {
		t.Fatalf("这批 pattern 不该有走不了区间的: %v", unsup)
	}
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
		byPat := scan2ByPat(t, ms2, text)
		for i := range ms2Pats {
			var flat []int32
			for _, loc := range std[i].FindAllStringIndex(text, -1) {
				flat = append(flat, int32(loc[0]), int32(loc[1]))
			}
			cmp++
			total += len(flat) / 2
			if fmt.Sprint(byPat[int32(i)]) != fmt.Sprint(flat) {
				t.Fatalf("round=%d pattern %d %q 撞 %q:\n  D2      %v\n  Longest %v",
					round, i, ms2Pats[i], text, byPat[int32(i)], flat)
			}
		}
	}
	if total == 0 {
		t.Fatalf("对账了 %d 条 pattern-正文, 一处命中都没有 —— 语料没牙齿, 这个测试是空的", cmp)
	}
	t.Logf("对账 %d 条 pattern-正文 · 共 %d 处命中, 岔开 0", cmp, total)
}

// TestMatchScan2SameAsPathB —— 同一张表 (benchPats) 同一份正文上, D2 与 MatchScanner 的
// 默认档 (路 B) 必须【逐字节相同】。两条路都自称严格 leftmost-longest, 那它们就没有
// 任何可以互相岔开的余地; 岔开就是其中一条错了。
func TestMatchScan2SameAsPathB(t *testing.T) {
	set, err := NewRegexpSet(benchPats)
	if err != nil {
		t.Fatal(err)
	}
	ms, _, err := set.NewMatchScanner()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	ms2, _, err := set.NewMatchScanner2()
	if err != nil {
		t.Fatal(err)
	}
	defer ms2.Close()
	spans := 0
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		b := scanByPat(t, ms, text)
		d := scan2ByPat(t, ms2, text)
		for i := range benchPats {
			if fmt.Sprint(b[int32(i)]) != fmt.Sprint(d[int32(i)]) {
				t.Fatalf("语料 %s · pattern %d %q 岔开:\n  路B %v\n  D2  %v",
					kind, i, benchPats[i], b[int32(i)], d[int32(i)])
			}
			spans += len(d[int32(i)]) / 2
		}
		t.Logf("语料 %-4s · D2 账: walks=%d cands=%d tries=%d emits=%d (tries/emit=%.2f)",
			kind, ms2.Stats().Walks, ms2.Stats().Cands, ms2.Stats().Tries, ms2.Stats().Emits,
			float64(ms2.Stats().Tries)/float64(max1(ms2.Stats().Emits)))
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

// TestMatchScan2EmptyCapable —— 能匹配空串的那几条: 建工作区那一刻就报出名单,
// 想配要区间的档 SetModes 当场报错, 配 boolOnly 放行 (与 MatchScanner 同解)。
func TestMatchScan2EmptyCapable(t *testing.T) {
	set, err := NewRegexpSet([]string{`a+`, `[a-f]*`})
	if err != nil {
		t.Fatal(err)
	}
	ms2, unsup, err := set.NewMatchScanner2()
	if err != nil {
		t.Fatal(err)
	}
	defer ms2.Close()
	if fmt.Sprint(unsup) != "[1]" {
		t.Fatalf("unsupported 该是 [1], 给的是 %v", unsup)
	}
	if err := ms2.SetModes([]MatchScanMode_t{MatchScanMode_span, MatchScanMode_span}); err == nil {
		t.Fatal("能匹配空串的那条配了要区间的档, SetModes 该当场报错")
	}
	if err := ms2.SetModes([]MatchScanMode_t{MatchScanMode_span, MatchScanMode_boolOnly}); err != nil {
		t.Fatal(err)
	}
	byPat := scan2ByPat(t, ms2, "xxaaayy")
	if fmt.Sprint(byPat[0]) != "[2 5]" {
		t.Fatalf("a+ 该给 [2 5], 给的是 %v", byPat[0])
	}
	if len(byPat[1]) != 0 {
		t.Fatalf("boolOnly 那条不该有区间, 给的是 %v", byPat[1])
	}
	if !ms2.Hit(1) {
		t.Fatal("boolOnly 那条照样要进命中表")
	}
}

// TestViableStartsBasics —— 底下那个原语 (RegexpSetReverse.ViableStarts) 的逐例钉子。
// 每一行都是手算得出来的: 候选起点 s ⟺ text[s:from) 是这条 pattern 【某个匹配的前缀】,
// 且 s < from (空前缀不算)。给出来的顺序是【降序】。
//
// 🔴 第一行就是路 A 那条第三种口径的病根: 门给的最小右端是 5 ("cd" 那处), 只种 accept
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
		n, err := rs.ViableStarts(c.text, c.from, 0, 0, buf)
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
	n, err := rs.ViableStarts(text, int32(len(text)), 0, 0, small)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(text) {
		t.Fatalf("[a-z]{1,} 撞 %d 个小写字母, 候选该是 %d 条, 给的是 %d", len(text), len(text), n)
	}
	big := make([]int32, n)
	n2, err := rs.ViableStarts(text, int32(len(text)), 0, 0, big)
	if err != nil || n2 != n {
		t.Fatalf("换大缓冲重来一次该给同样的 %d 条, 给的是 %d (err=%v)", n, n2, err)
	}
	if big[0] != int32(len(text)-1) || big[n-1] != 0 {
		t.Fatalf("该是降序 %d..0, 给的是 %v", len(text)-1, big)
	}
}

// TestMatchScan2NoAlloc —— 稳态零分配。
//
// 🔴 这不是洁癖: ViableStarts 是"每个右端问一次"的调用形态, 那里漏一笔 4 字节就按右端数
//    放大 (第一版在函数里开了个局部数组当"len(out)==0 时的落点", 地址交给 C 之后逃逸分析
//    每次调用把它搬上堆 —— benchPats/命中稀疏 上 33 次回推正好 33 笔)。同一类账见
//    spanresolve.go 走 _r 孪生那一段, 和 TestSpanPerf_NoAlloc。
func TestMatchScan2NoAlloc(t *testing.T) {
	set, err := NewRegexpSet(benchPats)
	if err != nil {
		t.Fatal(err)
	}
	ms2, _, err := set.NewMatchScanner2()
	if err != nil {
		t.Fatal(err)
	}
	defer ms2.Close()
	text := benchCorpus("few")
	sink := 0
	run := func() {
		if err := ms2.Scan(text, func(mm []SetMatch) { sink += len(mm) }); err != nil {
			t.Fatal(err)
		}
	}
	run() // 热身: 单条对象 (fwd1 / vp1) 惰性建 + 候选缓冲定型, 都只发生一次
	run()
	got := testing.AllocsPerRun(20, run)
	if sink == 0 {
		t.Fatal("一处命中都没有 —— 这个测试是空的")
	}
	// 2 笔是那个 Scan 闭包本身的固定开销 (三条路一样), 回推那一步该是 0 笔。
	if got > 2 {
		t.Errorf("稳态 %.1f 笔/遍 —— 该是 2 笔 (闭包) 才对, 多出来的是回推那一步漏的", got)
	}
}
