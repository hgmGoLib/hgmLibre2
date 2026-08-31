package hgmLibre2

import (
	"testing"
)

// re2setfrl_bench_test.go — Re2SetFrl vs 今天两条老路。
//
// 语料 / pattern / 共用对象那一套全部沿用 spanscan_bench_test.go (benchPats · benchCorpus ·
// benchObjects), 三档语料的含义见那里:
//   zero 一条也不命中 (产线绝大多数正文长这样) · few 24 处稀疏命中 · most 全小写最坏输入。
//
// 三条路:
//   旧实现        set.Match 当门 + 命中的每条各对整篇正文跑一遍 FindAllStringIndex
//                 (命中 k 条 = 1+k 遍全文, 后面那 k 遍是最贵的非锚定扫描)
//   MatchScanner  一遍正向 set 扫 + 逐个右端回推起点, 口径 leftmost-longest
//   Re2SetFrl     一遍正向 set 扫 + 存活位切分量 + 分量内一次结算, 口径最右终点最长
//
// ⚠ 口径不同 (见 re2setfrl.go 头注), 这里比的是【同一个问题的三种答法要多少钱】, 不是
//    "谁给的答案对"。答案对不对由 re2setfrl_test.go 的穷举判据管。
// ⚠ Frl 自己建自己那份正向 set, 没法与另外两条共用对象 —— 按 readme.txt 的警告, 同一段
//    代码换个 set 对象能差 5~8%。
//
// 🔴🔴 怎么量: 【一个二进制里的数字不作数, 必须扫布局取最小值】。
// 这几条 DFA 热循环在这台机 (Ryzen 5900X) 上对代码布局极其敏感 —— 同一份 C++, Go 那侧
// 只多编进一个【没人调用】的函数, 数字就能翻一倍。2026-09-01 实测 8 个布局:
//     zero/Re2SetFrl      198 98 99 98 97 98 99 98      (us)
//     zero/MatchScanner    99 143 143 142 143 145 139 143
// 两个变体的【最小值】都是 ~98us, 但任何单个二进制都会给出一个 1.5~2 倍的假差别, 而且
// 谁快谁慢跟着布局翻面。所以量法是:
//     for k in 0..7: 往测试包里塞 k 个没用的函数各建一个二进制, 跑一遍
//     每个变体【各取最小值】再比
// (doc/set性能优化经验.txt 早写了这台机 ±20%~2倍的抖动, 这是目前见过最狠的一例。)
//
// ── 按上面那个法子量出来的 (64KiB · 10 条 pattern · 8 个布局取最小 · us) ──────
//     语料            旧实现   MatchScanner   Re2SetFrl
//     zero 0 命中      97.5       98.5          97.2      三条打平
//     few  39 处       430.9      104.8         104.3     对旧实现 4.13x
//     most 最坏输入    598.9      478.7         575.6     对 MatchScanner 0.83x
// 怎么读:
//  · zero —— 产线绝大多数正文长这样。Frl 与【完全不开 g 档的裸扫】逐 us 相同, 也就是说
//    "存活位切分量"这套在没有命中的正文上真的一分钱不花。做法见 re2_dfa_spanscan.inc 里
//    那两个循环 (空闲档一个字节都不读存活位, 第一次命中才换到盯着档)。
//  · few —— 4.13 倍。省掉的正是旧实现那 1+k 遍全文重扫。与 MatchScanner 打平, 但 Frl
//    问反向锚定的次数是【每个分量一次】而不是每个右端一次 (见 TestRe2SetFrl_Cost)。
//  · most —— 语料是全小写无空格, [a-z]{4,} 一口吃掉整篇 ⇒ 这条 pattern 从头到尾【不断气】,
//    整篇就是一个分量, 每个字节都在盯着档里读一次存活位, 而且每个字节都是好几条 pattern
//    的命中 (G2Note 要跑)。这一档 Frl 比 MatchScanner 慢 17%, 比旧实现快 4%。
//    真语料不长这样; 要在这种输入上也快, 只能让表少造端点 (那是表的问题, 不是这一层的)。

// frlBenchWork 是 Re2SetFrl 那条路的可复用工作区。
type frlBenchWork struct {
	s   *Re2SetFrl
	buf *Re2SetFrlBuf_t
	n   int
}

func newFrlBenchWork(tb testing.TB) *frlBenchWork {
	pats := make([]Re2SetFrlPattern_t, len(benchPats))
	for i, p := range benchPats {
		pats[i] = Re2SetFrlPattern_t{Pattern: p}
	}
	s, err := NewRe2SetFrl(pats)
	if err != nil {
		tb.Fatalf("建 Re2SetFrl: %v", err)
	}
	return &frlBenchWork{s: s, buf: NewRe2SetFrlBuf(1024)}
}

func (w *frlBenchWork) run(tb testing.TB, text string) int {
	w.n = 0
	if err := w.s.Scan(text, w.buf, func(idx, st, en []int32) bool {
		w.n += len(idx)
		return true
	}); err != nil {
		tb.Fatalf("Frl Scan: %v", err)
	}
	return w.n
}

// frlBenchMs 是 MatchScanner 那条路的可复用工作区。
type frlBenchMs struct {
	ms *MatchScanner
	n  int
}

func newFrlBenchMs(tb testing.TB) *frlBenchMs {
	fwd, _, _ := benchObjects(tb)
	ms, unsup, err := fwd.NewMatchScanner()
	if err != nil {
		tb.Fatalf("开 MatchScanner: %v", err)
	}
	if len(unsup) != 0 {
		tb.Fatalf("benchPats 里有走不了区间的: %v", unsup)
	}
	return &frlBenchMs{ms: ms}
}

func (w *frlBenchMs) run(tb testing.TB, text string) int {
	w.n = 0
	if err := w.ms.Scan(text, func(batch []SetMatch) { w.n += len(batch) }); err != nil {
		tb.Fatalf("MatchScanner Scan: %v", err)
	}
	return w.n
}

func BenchmarkRe2SetFrl(b *testing.B) {
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		b.Run(kind+"/旧实现", func(b *testing.B) {
			o := newBenchOld(b)
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(o.run(text))%3 != 0 {
					b.Fatal("三元组对不齐")
				}
			}
		})
		b.Run(kind+"/MatchScanner", func(b *testing.B) {
			w := newFrlBenchMs(b)
			defer w.ms.Close()
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w.run(b, text)
			}
		})
		b.Run(kind+"/Re2SetFrl", func(b *testing.B) {
			w := newFrlBenchWork(b)
			defer w.s.Close()
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w.run(b, text)
			}
		})
	}
}

// TestRe2SetFrl_Cost 把三条路在三档语料上的【产出与代价】并排打出来 —— 光看 ns/op
// 说不清"为什么", 这里报的是 Frl 那条路真正花钱的两个数:
//
//	NSeg     切出来几个分量 (存活位干的活)
//	NResolve 问了几次反向锚定 —— 这一层唯一按命中数增长的成本
//	NResolve/NSeg 就是"平均每个分量里有几处不重叠的匹配"; 它越接近 1, 说明存活位把
//	              分量切得越干净, 一个分量一趟锚定就结完。
func TestRe2SetFrl_Cost(t *testing.T) {
	w := newFrlBenchWork(t)
	defer w.s.Close()
	o := newBenchOld(t)
	ms := newFrlBenchMs(t)
	defer ms.ms.Close()
	t.Logf("%-6s %8s %10s %10s %10s %8s %8s %10s", "语料", "字节",
		"旧实现处数", "MatchScan处数", "Frl处数", "分量数", "锚定数", "峰值字节")
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		nold := len(o.run(text)) / 3
		nms := ms.run(t, text)
		nfrl := w.run(t, text)
		st := w.s.Stats()
		t.Logf("%-6s %8d %10d %10d %10d %8d %8d %10d",
			kind, len(text), nold, nms, nfrl, st.NSeg, st.NResolve, st.UsedPeak)
	}
}
