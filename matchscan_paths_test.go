package hgmLibre2

// matchscan_paths_test.go —— 2026-08-27 那次"补端点全部改走单条对象"的回归钉子 + 价钱对照。
//
// 改的是【谁来补端点】, 不是【补出什么】:
//
//	路 B  旧: 贪心单条 FindStringIndexFrom 定起点 → 整表 set.ResolveSpan 重取最长终点 (两趟)
//	      新: longest 单条 FindStringIndexFrom                                      (一趟)
//	路 A  旧: 一条一个的反向 set ResolveSpanWithin → 整表 set.ResolveSpan            (两趟, 都是 set)
//	      新: 单条 RegexpReverse.ResolveSpanWithin → 单条 FindStringIndexAtWithin    (两趟, 都不是 set)
//
// 所以这份文件问两句:
//	① 新路子和旧路子在同一份正文上给的东西一字不差吗 (TestMatchScanPathsSameAsSetRoute);
//	② 新路子有没有变慢 (BenchmarkMatchScanOldVsNew)。
//
// 参照实现 (msOldScanner) 故意用【旧那套 API】—— set.ResolveSpan · 一条一个的反向 set ·
// 贪心口径的单条 Regexp —— 那几个入口一个都没删, 所以这份钉子长期有效: 它钉的是"单条那条路
// 与 set 那条路同解"这件事本身, 不是某一次改动的快照。
//
// 🔴 游标推进那几行在这里是【第二份实现】。这是有意的: 它跟 feed 各走各的引擎入口,
//    两边同时算错同一处的概率远低于共用一份实现。
// 🔴 它也是【一遍扫全表】的 (与真实现同构), 不是一条 pattern 扫一遍 —— 否则量出来的差价里
//    混着"多扫了 9 遍正文", 那就不是在量补端点这一步了。

import (
	"math/rand"
	"regexp/syntax"
	"strings"
	"testing"
)

// msOldPat_t 是参照实现里每条 pattern 的状态 (对应 msPat_t)。
type msOldPat_t struct {
	spanable bool
	fixed    bool
	minL     int32
	maxL     int32
	greedy   *Regexp           // 旧路 B: 贪心口径的单条正则 (只用来定起点)
	revSet   *RegexpSetReverse // 旧路 A: 一条一个的反向 set
	cur      int32
	done     bool
}

// msOldScanner 是【旧实现】的一份等价复刻: 一遍 FindAllIndex, 每条 pattern 一把游标,
// 补端点两趟都走 set 那侧的入口。
type msOldScanner struct {
	set   *RegexpSet
	alloc *RegexpSet_FindAllIndex_Alloc_t
	pathA bool
	per   []msOldPat_t
	out   map[int32][]int32 // nil = 只计数不收集 (benchmark 用)
	n     int
	// findCtx: 旧实现那边也是走 ctx 版的 (稳态零分配)。这里必须照抄, 否则量出来的差价里
	// 混着"参照实现每处命中多一笔 []int 分配", 那不是被测的东西。
	findCtx *FindStringIndex_ctx_t
}

func newMsOld(tb testing.TB, set *RegexpSet, pathA bool) *msOldScanner {
	tb.Helper()
	alloc, err := set.NewFindAllIndexAlloc()
	if err != nil {
		tb.Fatal(err)
	}
	o := &msOldScanner{set: set, alloc: alloc, pathA: pathA,
		per: make([]msOldPat_t, set.GetPatternLen()), findCtx: NewFindStringIndex_ctx()}
	for i := range o.per {
		p := &o.per[i]
		minL, maxL := set.PatternLenRange(i)
		if minL <= 0 {
			continue
		}
		p.spanable, p.minL, p.maxL = true, int32(minL), int32(maxL)
		p.fixed = minL == maxL && maxL >= 0
		if p.fixed {
			continue
		}
		if pathA {
			r, err := NewRegexpSetReverseMaxMem([]string{set.pats[i]}, set.maxMem)
			if err != nil {
				tb.Fatalf("旧路 A 的一条一个反向 set 建不出来: %v", err)
			}
			p.revSet = r
		} else {
			r, err := CompileMaxMem(set.pats[i], set.maxMem)
			if err != nil {
				tb.Fatalf("旧路 B 的贪心单条建不出来: %v", err)
			}
			p.greedy = r
		}
	}
	return o
}

func (o *msOldScanner) Close() { o.alloc.Close() }

func (o *msOldScanner) emit(i int, lo, hi int32) {
	o.n++
	if o.out != nil {
		o.out[int32(i)] = append(o.out[int32(i)], lo, hi)
	}
}

// scan 跑一遍。collect=true 时把区间收进 o.out (对账用), 否则只计数 (量价钱用)。
func (o *msOldScanner) scan(tb testing.TB, text string, collect bool) int {
	tb.Helper()
	o.n = 0
	o.out = nil
	if collect {
		o.out = map[int32][]int32{}
	}
	for i := range o.per {
		o.per[i].cur, o.per[i].done = 0, false
	}
	err := o.set.FindAllIndex(text, o.alloc, func(runs []RegexpSet_FindAllIndex_Run_t) {
		for k := range runs {
			r := &runs[k]
			i := int(r.ReIndex)
			p := &o.per[i]
			if !p.spanable || p.done {
				continue
			}
			for e := r.Lo; e <= r.Hi; e++ {
				if e <= p.cur {
					continue
				}
				if p.fixed {
					start := e - p.minL
					if start < p.cur {
						continue
					}
					o.emit(i, start, e)
					p.cur = e
					continue
				}
				if !o.pathA {
					from := p.cur
					if p.maxL >= 0 {
						if w := e - p.maxL; w > from {
							from = w
						}
					}
					loc := o.findCtx.FindStringIndexFrom(p.greedy, text, int(from))
					if loc == nil {
						p.done = true
						break
					}
					start, end := int32(loc[0]), int32(loc[1])
					pos, ok, err := o.set.ResolveSpan(text, start, int32(i))
					if err != nil {
						tb.Fatal(err)
					}
					if ok && pos > end {
						end = pos
					}
					o.emit(i, start, end)
					p.cur = end
					continue
				}
				start, ok, err := p.revSet.ResolveSpanWithin(text, e, p.cur, 0)
				if err != nil {
					tb.Fatal(err)
				}
				if !ok {
					continue
				}
				end := e
				pos, ok, err := o.set.ResolveSpan(text, start, int32(i))
				if err != nil {
					tb.Fatal(err)
				}
				if ok && pos > end {
					end = pos
				}
				o.emit(i, start, end)
				p.cur = end
			}
		}
	})
	if err != nil {
		tb.Fatal(err)
	}
	return o.n
}

// msPathsPats 是两条路各自的形状都覆盖到的一张小表: 定长 · 有上界变长 · 无上界变长 ·
// 长度不齐的交替 · 两头带 \b 的 · 多字节的 · 起始类窄于重复类的那一族。
var msPathsPats = []string{
	`[A-Z]\d{3}`,
	`[a-f]{2,6}`,
	`q[0-9a-z]{3,}q`,
	`[\x{4e00}-\x{9fff}]{2,4}`,
	`w+x`,
	`ab|abcd`,
	`\b[A-F0-9]{8}\b`,
	`x[a-f]{1,4}y`,
	`[a-f]{2}-[a-f]{2,4}`,
	`\d{3}-\d{4}`,
	`[A-Za-z][A-Za-z0-9]{2,19}key`,
	`(?:ab)?[bc]{1,2}`,
	`x{1,3}[a-c]?(?:ab|cd)?`,
}

// TestMatchScanPathsSameAsSetRoute —— 两条路各 300 轮随机正文, 新的单条路子必须与旧的
// set 路子【逐字节相同】。任何一处不同都说明这次重构改了语义, 不是改了实现。
func TestMatchScanPathsSameAsSetRoute(t *testing.T) {
	set, err := NewRegexpSet(msPathsPats)
	if err != nil {
		t.Fatal(err)
	}
	// 语料从各条 pattern 自己的 AST 生成再拌上噪声 —— 纯随机字节撞不出几处真匹配, 那就是
	// 空转绿 (同 TestMatchScanReverse_VsBrute 的红字)。msrGen 在 matchscan_reverse_test.go。
	asts := make([]*syntax.Regexp, len(msPathsPats))
	for i, pat := range msPathsPats {
		a, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			t.Fatalf("pat=%q 解析失败: %v", pat, err)
		}
		asts[i] = a.Simplify()
	}
	noiseR := []rune(" ,;:-_/@.\n\tabcdefkqwxyz019ABFKZ张三李")

	for _, pathA := range []bool{false, true} {
		name := "路B(默认档)"
		mode := MatchScanMode_span
		if pathA {
			name = "路A(spanFast)"
			mode = MatchScanMode_spanFast
		}
		modes := make([]MatchScanMode_t, len(msPathsPats))
		for i := range modes {
			modes[i] = mode
		}
		ms, unsup, err := set.NewMatchScanner()
		if err != nil {
			t.Fatal(err)
		}
		if len(unsup) != 0 {
			t.Fatalf("%s: 不该有走不了区间的: %v", name, unsup)
		}
		if err := ms.SetModes(modes); err != nil {
			t.Fatal(err)
		}
		old := newMsOld(t, set, pathA)
		rng := rand.New(rand.NewSource(20260827))
		nSpan := 0
		for round := 0; round < 300; round++ {
			var sb strings.Builder
			target := 60 + rng.Intn(400)
			for sb.Len() < target {
				if rng.Intn(3) == 0 {
					sb.WriteRune(noiseR[rng.Intn(len(noiseR))])
					continue
				}
				msrGen(asts[rng.Intn(len(asts))], rng, &sb, 0)
				sb.WriteRune(noiseR[rng.Intn(len(noiseR))])
			}
			text := sb.String()
			got := scanByPat(t, ms, text)
			old.scan(t, text, true)
			for id := range msPathsPats {
				g, want := got[int32(id)], old.out[int32(id)]
				nSpan += len(g) / 2
				if len(g) != len(want) {
					t.Fatalf("%s 轮 %d: #%d %q 处数不同: 新 %d 处 %v · 旧 set 路 %d 处 %v\n正文 %q",
						name, round, id, msPathsPats[id], len(g)/2, g, len(want)/2, want, text)
				}
				for k := range want {
					if g[k] != want[k] {
						t.Fatalf("%s 轮 %d: #%d %q 第 %d 个数不同: 新 %v · 旧 set 路 %v\n正文 %q",
							name, round, id, msPathsPats[id], k, g, want, text)
					}
				}
			}
		}
		ms.Close()
		old.Close()
		if nSpan < 1000 {
			t.Fatalf("%s: 只对账了 %d 处区间 —— 语料没造对, 这是空转绿", name, nSpan)
		}
		t.Logf("%s: 300 轮 · 对账 %d 处区间, 与旧的 set 路子逐字节相同", name, nSpan)
	}
}

// ── 老路子 vs 新路子的价钱 ───────────────────────────────────────────────────
//
// 新旧两套都在这一个二进制里, 所以是同一台机器同一批语料同一次运行里比出来的 —— 不必靠
// "改之前跑一遍、改之后再跑一遍"那种隔着两次进程的对照。两边都是【一遍扫全表】, 差的只有
// 补端点那几趟走谁。语料/正则来自 spanscan_bench_test.go 的 benchPats / benchCorpus。
func BenchmarkMatchScanOldVsNew(b *testing.B) {
	set, err := NewRegexpSet(benchPats)
	if err != nil {
		b.Fatal(err)
	}
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		for _, pathA := range []bool{false, true} {
			tag := "pathB"
			mode := MatchScanMode_span
			if pathA {
				tag = "pathA"
				mode = MatchScanMode_spanFast
			}
			modes := make([]MatchScanMode_t, len(benchPats))
			for i := range modes {
				modes[i] = mode
			}

			b.Run("new-"+tag+"/"+kind, func(b *testing.B) {
				ms, unsup, err := set.NewMatchScanner()
				if err != nil {
					b.Fatal(err)
				}
				defer ms.Close()
				if len(unsup) != 0 {
					b.Fatalf("不该有走不了区间的: %v", unsup)
				}
				if err := ms.SetModes(modes); err != nil {
					b.Fatal(err)
				}
				// 🔴 计数器提到闭包外面: 写在 run 里面的话 n 每次调用都逃逸上堆, 量出来的
				//    2 allocs/op 是【这个 benchmark 自己的】, 不是被测那一层的。
				n := 0
				run := func() int {
					n = 0
					if err := ms.Scan(text, func(mm []SetMatch) { n += len(mm) }); err != nil {
						b.Fatal(err)
					}
					return n
				}
				want := run() // 热身: 把各条的单条对象和 DFA 状态建出来
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if run() != want {
						b.Fatal("同一份正文两遍结果数不同")
					}
				}
			})

			b.Run("old-"+tag+"/"+kind, func(b *testing.B) {
				old := newMsOld(b, set, pathA)
				defer old.Close()
				want := old.scan(b, text, false)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if old.scan(b, text, false) != want {
						b.Fatal("同一份正文两遍结果数不同")
					}
				}
			})
		}
	}
}
