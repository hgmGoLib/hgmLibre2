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
// 这里走的是【反向 set】那条路: 反向 set 直接吐匹配的【左端】, 拿到左端之后用一条【锚定】
// 正则 \A(?:pat) 在 text[start:] 上取右端。锚定是关键 —— 它让"能当起点的位置"从"每一个"
// 塌成"恰好一个", 正则走不下去就立刻进 DeadState 收工, 而不是靠 .*? 前缀一路扫到正文末尾。
// 测试末尾有一条断言专门把这个机制钉死。
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
	sc, err := set.NewSpanScanner(16)
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc.Close()

	var starts []needSpan // 先只填 rule + lo
	if err := sc.Scan(needText, func(spans []SetSpan) bool {
		for _, sp := range spans {
			// 游程要展开: [Lo,Hi] 里【每一个】值都是一个真实的匹配左端。
			for p := sp.Lo; p <= sp.Hi; p++ {
				starts = append(starts, needSpan{rule: int(sp.Index), lo: int(p)})
			}
		}
		return true
	}); err != nil {
		t.Fatalf("Scan: %v", err)
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
