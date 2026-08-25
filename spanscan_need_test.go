package hgmLibre2

// spanscan_need_test.go — 按【真实调用形态】走一遍完整流水线, 验证这套 API 撑得住需求。
//
// 需求形态 (脱产品谈, 就是一类很常见的活):
//   有一张 N 条正则的表, 每条描述一种"敏感片段"的写法。要在一篇正文里把它们全找出来、
//   按【规则优先级】取舍掉重叠的候选, 然后一次性替换成占位串。要点四条:
//     ① 一遍扫就要知道"哪条命中"【和】"命中在哪" —— 不能命中一条就把整篇正文重扫一遍;
//     ② 同一片文本常被多条规则同时盖住 (同一个值的两种边界写法, 必然相交),
//        要的是"按优先级贪心, 相交即丢", 不是整批退回, 也不是随便选一个;
//     ③ 边界必须【精确】: 下游会拿这一段去做定长校验 (长度差一个字符就判成假命中),
//        所以多吃一个字符和少吃一个字符都是错的;
//     ④ 命中条数本身是有意义的 (下游有"出现 N 次以上才算数"这类判断), 不能多也不能少。
//
// 第二遍取另一端有两种写法, 两个测试各测一种:
//   TestSpanScan_RedactPipeline           —— 调用方自己编一条锚定正则 \A(?:pat) 在 text[start:]
//                                            上跑。锚定是关键 (末尾 AnchoredSecondPassDiesEarly
//                                            那条断言专门钉这个机制), 但每条 pattern 得多一个
//                                            Regexp 对象、一份独立 DFA 缓存, 语义靠人工对齐。
//   TestSpanScan_RedactPipelineBothDirections —— 用库里的 ResolveSpan, 走 set 自己那份程序里
//                                            那个不带 .*? 前缀的入口。正反两条路各走一遍对账。
//
// 语料和正则都是现编的通用形状, 不对应任何具体产品的规则表。

import (
	"sort"
	"strings"
	"testing"
)

// needRule 是一条规则: 正则 + 优先级 (在切片里的下标就是优先级, 越靠前越高)。
type needRule struct {
	pat  string
	desc string
}

var needRules = []needRule{
	// #0 最高优先级: 定长写法。下游要拿这一段做定长校验, 边界必须一个字节都不差。
	{pat: `AAA-[A-Za-z0-9]{12}`, desc: "定长"},
	// #1 同一个值的变长写法 —— 与 #0 【必然相交】, 是"相交即丢"这条规则的主考点。
	{pat: `AAA-[A-Za-z0-9]{8,16}`, desc: "变长(与定长必相交)"},
	// #2 变长尾巴, 一条匹配会连出一串右端 (游程收敛的主战场)。
	{pat: `BBB_[a-z]{6,}`, desc: "变长尾"},
	// #3 另一种形态, 与上面都不相交。
	{pat: `[0-9]{4}-[0-9]{4}`, desc: "分段数字"},
}

// needText 是正文: 三个该被替换的片段, 埋在一堆什么都不命中的填充里。
// 填充要足够长 —— "第二遍不必重扫整篇正文"这件事在短正文上看不出来。
const needText = "" +
	"line 1: nothing interesting here at all, just prose and more prose\n" +
	"line 2: the value is AAA-AbCdEf123456 and that is the whole of it\n" +
	"line 3: filler filler filler filler filler filler filler filler\n" +
	"line 4: another one BBB_abcdefghij sits right here in the middle\n" +
	"line 5: filler filler filler filler filler filler filler filler\n" +
	"line 6: and finally 1234-5678 closes the list of things to hide\n" +
	"line 7: trailing prose that matches nothing whatsoever, the end\n"

// needSpan 是一个已经定好边界的候选 (原文坐标, [Lo,Hi))。
type needSpan struct {
	rule   int
	lo, hi int
}

func TestSpanScan_RedactPipeline(t *testing.T) {
	pats := make([]string, len(needRules))
	for i, r := range needRules {
		pats[i] = r.pat
	}

	// ── ① 一遍扫: 反向 set 直接吐每条 pattern 的【左端】游程 ──────────────────
	set, err := NewRegexpSetReverseMaxMem(pats, 0)
	if err != nil {
		t.Fatalf("建反向 set: %v", err)
	}
	var starts []needSpan // 先只填 rule + lo
	if err := set.FindAllIndex(needText, nil, func(reIndex, startLo, startHi int32) {
		// 游程要展开: startLo..startHi 里【每一个】值都是一个真实的匹配左端。
		for p := startLo; p <= startHi; p++ {
			starts = append(starts, needSpan{rule: int(reIndex), lo: int(p)})
		}
	}); err != nil {
		t.Fatalf("FindAllIndex: %v", err)
	}
	if len(starts) == 0 {
		t.Fatalf("一个左端都没扫到")
	}

	// ── ② 第二遍: 只在【命中那一段】上跑锚定正则取右端 ────────────────────────
	// 锚定 (\A(?:pat)) 是这条路的全部意义: 起点只剩一个, 走不通立刻 DeadState 收工。
	// 对照今天的做法 —— 拿原正则在【整篇正文】上 FindAllStringIndex, 每条命中规则一遍。
	anchored := make([]*Regexp, len(needRules))
	for i, r := range needRules {
		anchored[i], err = Compile(`\A(?:` + r.pat + `)`)
		if err != nil {
			t.Fatalf("编锚定正则 #%d: %v", i, err)
		}
	}
	secondPassCalls := 0
	cands := make([]needSpan, 0, len(starts))
	for _, st := range starts {
		secondPassCalls++
		loc := anchored[st.rule].FindStringIndex(needText[st.lo:])
		if loc == nil {
			t.Fatalf("规则 #%d 说它的匹配从 %d 开始, 锚定正则却在那儿找不到东西", st.rule, st.lo)
		}
		if loc[0] != 0 {
			t.Fatalf("锚定正则居然从偏移 %d 才开始匹配 (锚定失效了)", loc[0])
		}
		cands = append(cands, needSpan{rule: st.rule, lo: st.lo, hi: st.lo + loc[1]})
	}
	// 第二遍的调用次数 = 扫出来的左端数, 而不是"命中规则数 × 一遍全文"。
	// 这就是这套 API 要换掉的东西: 结构上不再有"拿命中正则过一遍整篇正文"这一步。
	if secondPassCalls != len(starts) {
		t.Fatalf("第二遍跑了 %d 次, 左端只有 %d 个", secondPassCalls, len(starts))
	}

	// ── ③ 按优先级贪心: 规则序在前的先要, 与已收下的相交就丢 ──────────────────
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].rule != cands[b].rule {
			return cands[a].rule < cands[b].rule
		}
		return cands[a].lo < cands[b].lo
	})
	var acc []needSpan
	for _, c := range cands {
		hit := false
		for _, a := range acc {
			if c.lo < a.hi && a.lo < c.hi { // 半开区间相交
				hit = true
				break
			}
		}
		if !hit {
			acc = append(acc, c)
		}
	}
	sort.Slice(acc, func(a, b int) bool { return acc[a].lo < acc[b].lo })

	// ── ④ 一次升序替换 ──────────────────────────────────────────────────────
	var sb strings.Builder
	prev := 0
	for _, a := range acc {
		sb.WriteString(needText[prev:a.lo])
		sb.WriteString("<hidden>")
		prev = a.hi
	}
	sb.WriteString(needText[prev:])
	got := sb.String()

	want := "" +
		"line 1: nothing interesting here at all, just prose and more prose\n" +
		"line 2: the value is <hidden> and that is the whole of it\n" +
		"line 3: filler filler filler filler filler filler filler filler\n" +
		"line 4: another one <hidden> sits right here in the middle\n" +
		"line 5: filler filler filler filler filler filler filler filler\n" +
		"line 6: and finally <hidden> closes the list of things to hide\n" +
		"line 7: trailing prose that matches nothing whatsoever, the end\n"
	if got != want {
		t.Fatalf("替换结果不对\n got=%q\nwant=%q", got, want)
	}

	// 需求 ④: 条数本身有意义 —— 不能多也不能少。
	if len(acc) != 3 {
		t.Fatalf("收下 %d 段, 应该正好 3 段: %+v", len(acc), acc)
	}

	// 需求 ③: 边界必须精确。定长那条规则拿到的必须正好是那 19 个字节,
	// 多一个字符或少一个字符, 下游的定长校验就会把真命中判成假命中。
	var fixed *needSpan
	for i := range acc {
		if acc[i].rule == 0 {
			fixed = &acc[i]
		}
	}
	if fixed == nil {
		t.Fatalf("定长那条规则没被收下, 说明优先级贪心让变长那条抢先了: %+v", acc)
	}
	if seg := needText[fixed.lo:fixed.hi]; seg != "AAA-AbCdEf123456" {
		t.Fatalf("定长段边界不精确: %q (长度 %d)", seg, len(seg))
	}

	// 需求 ②: 变长那条 (#1) 确实产生了候选, 但因为与 #0 相交被丢掉了 ——
	// 要是它根本没产生候选, 上面那条"相交即丢"就是空跑, 测了个寂寞。
	overlapped := 0
	for _, c := range cands {
		if c.rule == 1 {
			overlapped++
		}
	}
	if overlapped == 0 {
		t.Fatalf("变长规则一个候选都没有, 相交取舍这条路没被走到: %+v", cands)
	}
	for _, a := range acc {
		if a.rule == 1 {
			t.Fatalf("变长规则的候选跟定长的相交, 不该被收下: %+v", a)
		}
	}
}

// TestSpanScan_AnchoredSecondPassDiesEarly 把"第二遍为什么必须锚定"钉死。
//
// 拿到左端之后, 如果还用【原来那条非锚定正则】去扫 text[start:], 它的 .*? 前缀让每个位置
// 都能当起点 —— 走不通就往后挪一格接着试, 一路扫到正文末尾都不会停。省下来的只有 start
// 之前那一截, 后面那一大片无关正文照扫不误, 状态也照建不误。
//
// 换成锚定的 \A(?:pat), 起点只剩一个: 走不通就是 DeadState, 当场收工。
// 下面用一个可判定的现象把这个差别摆出来 —— 在【错误的偏移】上:
//   非锚定的: 照样能找到那个匹配 (证明它一直在往后扫)
//   锚定的  : 什么都找不到 (证明它当场就死了)
func TestSpanScan_AnchoredSecondPassDiesEarly(t *testing.T) {
	pat := needRules[0].pat
	plain, err := Compile(pat)
	if err != nil {
		t.Fatalf("编非锚定正则: %v", err)
	}
	anch, err := Compile(`\A(?:` + pat + `)`)
	if err != nil {
		t.Fatalf("编锚定正则: %v", err)
	}

	start := strings.Index(needText, "AAA-AbCdEf123456")
	if start < 0 {
		t.Fatalf("语料里没有那个片段")
	}

	// 正确的起点上, 两者结果一致 —— 锚定没有改变语义。
	if a, b := anch.FindStringIndex(needText[start:]), plain.FindStringIndex(needText[start:]); a == nil ||
		b == nil || a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("正确起点上锚定与非锚定结果不一致: %v vs %v", a, b)
	}

	// 错开一格 (= 从匹配内部开始)。
	off := start + 1
	if plain.FindStringIndex(needText[off:]) != nil {
		// 非锚定的会往后扫到别处 —— 本语料里 off 之后没有第二个同形片段, 所以它应该也找不到。
		// 换个更直白的现象: 让它从匹配【之前】一大截开始, 看它是不是一路扫过来找到了。
		t.Fatalf("语料假设不成立: 错位之后不该还有同形片段")
	}
	if anch.FindStringIndex(needText[off:]) != nil {
		t.Fatalf("锚定正则在错误偏移上不该有任何匹配")
	}

	// 正面证据: 从正文开头喂给非锚定正则, 它能找到 —— 也就是说它把 start 之前那 100 多字节
	// 全扫了一遍才走到匹配。这正是"命中一条就重扫整篇正文"要付的账。
	loc := plain.FindStringIndex(needText)
	if loc == nil || loc[0] != start {
		t.Fatalf("非锚定正则从头扫应该扫到 %d, 拿到 %v", start, loc)
	}
	// 同一条正则锚定之后, 从正文开头什么都找不到 —— 它在第 0 个字节就死了。
	if anch.FindStringIndex(needText) != nil {
		t.Fatalf("锚定正则从正文开头不该有匹配 (它应该在第一个字节就进 DeadState)")
	}
}

// TestSpanScan_RedactPipelineBothDirections — 同一个需求, 两条路各走一遍, 结果必须一致。
//
// 上面那个 TestSpanScan_RedactPipeline 走的是"反向 set 拿左端 + 【自己另编一条锚定正则】拿
// 右端"。自己编那一条是有代价的: 每条 pattern 一个 Regexp 对象、一份独立的 DFA 缓存, 而且
// 它和 set 里那条是不是同一个语义, 全靠人工保证。ResolveSpan 把这一步收回库里 —— 用的是
// set 自己那份程序、那份缓存, 走的是程序里那个【不带 .*? 前缀】的入口 (从外面够不着的那个)。
//
// 于是这个需求有了对称的两条路, 这里两条都走一遍并互相对账:
//   正向路: 正向 set 扫出【右端】 → 反向 set 的 ResolveSpan 求左端
//   反向路: 反向 set 扫出【左端】 → 正向 set 的 ResolveSpan 求右端
// 两条路要给出【逐字节相同】的候选取舍结果和替换结果 —— 一条路上的坐标错一格, 对不上账。
//
// 顺带钉死一件事: 变长规则在同一个端点上有一串长度都成立 (rule#2 的右端就是一段游程),
// ResolveSpan 必须给【最长】的那个。给最短的话下面 want 里的 BBB_ 那段会少几个字母。
func TestSpanScan_RedactPipelineBothDirections(t *testing.T) {
	pats := make([]string, len(needRules))
	for i, r := range needRules {
		pats[i] = r.pat
	}
	fwd, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatalf("建正向 set: %v", err)
	}
	rev, err := NewRegexpSetReverseMaxMem(pats, 0)
	if err != nil {
		t.Fatalf("建反向 set: %v", err)
	}

	// scanEnds 把一遍扫描吐的游程展开成 (规则, 端点) 列表。
	// 正反是两个类型, 所以这里收的是 FindAllIndex 那个方法本身。
	scanEnds := func(find func(string, *RegexpSet_FindAllIndex_Alloc_t, func(int32, int32, int32)) error) []needSpan {
		var out []needSpan
		if err := find(needText, nil, func(reIndex, lo, hi int32) {
			for p := lo; p <= hi; p++ {
				out = append(out, needSpan{rule: int(reIndex), lo: int(p)})
			}
		}); err != nil {
			t.Fatalf("FindAllIndex: %v", err)
		}
		return out
	}

	// resolve 把"一个端点"补成"一整段"。other 是【另一个方向】的那个 set:
	// 正向 set 吐的右端要拿反向 set 求左端, 反过来也一样。
	resolve := func(other func(string, int32, int32) (int32, bool, error), ends []needSpan, endIsRight bool) []needSpan {
		out := make([]needSpan, 0, len(ends))
		for _, e := range ends {
			pos, ok, err := other(needText, int32(e.lo), int32(e.rule))
			if err != nil {
				t.Fatalf("ResolveSpan(规则 #%d, 端点 %d): %v", e.rule, e.lo, err)
			}
			if !ok {
				t.Fatalf("规则 #%d 说它有个端点在 %d, ResolveSpan 却说那儿不匹配", e.rule, e.lo)
			}
			if endIsRight {
				out = append(out, needSpan{rule: e.rule, lo: int(pos), hi: e.lo})
			} else {
				out = append(out, needSpan{rule: e.rule, lo: e.lo, hi: int(pos)})
			}
		}
		return out
	}

	// greedy: 按规则优先级 + 同一起点先要长的, 相交即丢。
	greedy := func(cands []needSpan) []needSpan {
		c := append([]needSpan(nil), cands...)
		sort.SliceStable(c, func(a, b int) bool {
			if c[a].rule != c[b].rule {
				return c[a].rule < c[b].rule
			}
			if c[a].lo != c[b].lo {
				return c[a].lo < c[b].lo
			}
			return c[a].hi > c[b].hi // 同一起点上先要最长的那个
		})
		var acc []needSpan
		for _, x := range c {
			hit := false
			for _, a := range acc {
				if x.lo < a.hi && a.lo < x.hi {
					hit = true
					break
				}
			}
			if !hit {
				acc = append(acc, x)
			}
		}
		sort.Slice(acc, func(a, b int) bool { return acc[a].lo < acc[b].lo })
		return acc
	}

	splice := func(acc []needSpan) string {
		var sb strings.Builder
		prev := 0
		for _, a := range acc {
			sb.WriteString(needText[prev:a.lo])
			sb.WriteString("<hidden>")
			prev = a.hi
		}
		sb.WriteString(needText[prev:])
		return sb.String()
	}

	// 正向路: 正向 set 的端点是【右端】, 拿反向 set 求左端。
	fwdCands := resolve(rev.ResolveSpan, scanEnds(fwd.FindAllIndex), true)
	// 反向路: 反向 set 的端点是【左端】, 拿正向 set 求右端。
	revCands := resolve(fwd.ResolveSpan, scanEnds(rev.FindAllIndex), false)
	if len(fwdCands) == 0 || len(revCands) == 0 {
		t.Fatalf("有一条路一个候选都没出: 正向 %d 条, 反向 %d 条", len(fwdCands), len(revCands))
	}

	fwdAcc, revAcc := greedy(fwdCands), greedy(revCands)
	if len(fwdAcc) != len(revAcc) {
		t.Fatalf("两条路收下的段数不同: 正向 %+v, 反向 %+v", fwdAcc, revAcc)
	}
	for i := range fwdAcc {
		if fwdAcc[i] != revAcc[i] {
			t.Fatalf("两条路第 %d 段对不上: 正向 %+v (%q), 反向 %+v (%q)", i,
				fwdAcc[i], needText[fwdAcc[i].lo:fwdAcc[i].hi],
				revAcc[i], needText[revAcc[i].lo:revAcc[i].hi])
		}
	}

	want := "" +
		"line 1: nothing interesting here at all, just prose and more prose\n" +
		"line 2: the value is <hidden> and that is the whole of it\n" +
		"line 3: filler filler filler filler filler filler filler filler\n" +
		"line 4: another one <hidden> sits right here in the middle\n" +
		"line 5: filler filler filler filler filler filler filler filler\n" +
		"line 6: and finally <hidden> closes the list of things to hide\n" +
		"line 7: trailing prose that matches nothing whatsoever, the end\n"
	if got := splice(fwdAcc); got != want {
		t.Fatalf("正向路替换结果不对\n got=%q\nwant=%q", got, want)
	}
	if len(fwdAcc) != 3 {
		t.Fatalf("收下 %d 段, 应该正好 3 段: %+v", len(fwdAcc), fwdAcc)
	}

	// 需求 ③: 定长那条的边界必须一个字节都不差。
	var fixed *needSpan
	for i := range fwdAcc {
		if fwdAcc[i].rule == 0 {
			fixed = &fwdAcc[i]
		}
	}
	if fixed == nil {
		t.Fatalf("定长那条规则没被收下: %+v", fwdAcc)
	}
	if seg := needText[fixed.lo:fixed.hi]; seg != "AAA-AbCdEf123456" {
		t.Fatalf("定长段边界不精确: %q (长度 %d)", seg, len(seg))
	}

	// 需求 ②: 变长那条 (#1) 确实产生了候选, 但全被"相交即丢"筛掉了。
	n1 := 0
	for _, c := range fwdCands {
		if c.rule == 1 {
			n1++
		}
	}
	if n1 == 0 {
		t.Fatalf("变长规则一个候选都没有, 相交取舍这条路没被走到")
	}
	for _, a := range fwdAcc {
		if a.rule == 1 {
			t.Fatalf("变长规则的候选跟定长的相交, 不该被收下: %+v", a)
		}
	}

	// 变长尾 (#2) 在同一段文本上会连出一串右端 —— 每一个右端都必须解析回【同一个】左端,
	// 而且被收下的那个必须是最长的那一段 (给最短的话 want 里那段会少几个字母)。
	starts := map[int]bool{}
	maxHi := -1
	for _, c := range fwdCands {
		if c.rule == 2 {
			starts[c.lo] = true
			if c.hi > maxHi {
				maxHi = c.hi
			}
		}
	}
	if len(starts) != 1 {
		t.Fatalf("变长尾的一串右端解析出了 %d 个不同左端, 应该只有 1 个: %v", len(starts), starts)
	}
	if maxHi < 0 {
		t.Fatalf("变长尾一个候选都没有")
	}
	for _, a := range fwdAcc {
		if a.rule == 2 && a.hi != maxHi {
			t.Fatalf("变长尾收下的不是最长的那段: 收下 %q, 最长能到 %q",
				needText[a.lo:a.hi], needText[a.lo:maxHi])
		}
	}
}
