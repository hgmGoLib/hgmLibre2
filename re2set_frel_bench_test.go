package hgmLibre2

import (
	"testing"
)

// re2set_frel_bench_test.go — frel vs 另外两条路。
//
// 语料 / pattern / 共用对象那一套全部沿用 spanscan_bench_test.go (benchPats · benchCorpus ·
// benchObjects), 三档语料的含义见那里:
//   zero 一条也不命中 (产线绝大多数正文长这样) · few 24 处稀疏命中 · most 全小写最坏输入。
//
// 三条路:
//   旧实现        set.Match 当门 + 命中的每条各对整篇正文跑一遍 FindAllStringIndex
//                 (命中 k 条 = 1+k 遍全文, 后面那 k 遍是最贵的非锚定扫描)
//   fll           一遍正向 set 扫 + 逐个右端回推起点, 口径 leftmost-longest
//   frel          一遍正向 set 扫 + 存活位切分量 + 分量内一次结算, 口径最右终点最长
//
// ⚠ 口径不同 (见 re2setfrel.go 头注), 这里比的是【同一个问题的三种答法要多少钱】, 不是
//    "谁给的答案对"。答案对不对由 re2setfrel_test.go 的穷举判据管。
// ⚠ Frel 自己建自己那份正向 set, 没法与另外两条共用对象 —— 按 readme.txt 的警告, 同一段
//    代码换个 set 对象能差 5~8%。
//
// 🔴🔴 怎么量: 【一个二进制里的数字不作数, 必须扫布局取最小值】。
// 这几条 DFA 热循环在这台机 (Ryzen 5900X) 上对代码布局极其敏感 —— 同一份 C++, Go 那侧
// 只多编进一个【没人调用】的函数, 数字就能翻一倍。2026-09-01 实测 8 个布局:
//     zero/Re2SetFrel      198 98 99 98 97 98 99 98      (us)
//     zero/MatchScanner    99 143 143 142 143 145 139 143
// 两个变体的【最小值】都是 ~98us, 但任何单个二进制都会给出一个 1.5~2 倍的假差别, 而且
// 谁快谁慢跟着布局翻面。所以量法是:
//     for k in 0..7: 往测试包里塞 k 个没用的函数各建一个二进制, 跑一遍
//     每个变体【各取最小值】再比
// (doc/set性能优化经验.txt 早写了这台机 ±20%~2倍的抖动, 这是目前见过最狠的一例。)
//
// ── 按上面那个法子量出来的 (64KiB · 10 条 pattern · 8 个布局取最小 · us) ──────
//     语料            旧实现   MatchScanner   Re2SetFrel
//     zero 0 命中      97.5       98.5          97.2      三条打平
//     few  39 处       430.9      104.8         104.3     对旧实现 4.13x
//     most 最坏输入    598.9      478.7         575.6     对 MatchScanner 0.83x
// 怎么读:
//  · zero —— 产线绝大多数正文长这样。Frel 与【完全不开 g 档的裸扫】逐 us 相同, 也就是说
//    "存活位切分量"这套在没有命中的正文上真的一分钱不花。做法见 re2_dfa_spanscan_inl.h 里
//    那两个循环 (空闲档一个字节都不读存活位, 第一次命中才换到盯着档)。
//  · few —— 4.13 倍。省掉的正是旧实现那 1+k 遍全文重扫。与 MatchScanner 打平, 但 Frel
//    问反向锚定的次数是【每个分量一次】而不是每个右端一次 (见 TestRe2SetFrel_Cost)。
//  · most —— 语料是全小写无空格, [a-z]{4,} 一口吃掉整篇 ⇒ 这条 pattern 从头到尾【不断气】,
//    整篇就是一个分量, 每个字节都在盯着档里读一次存活位, 而且每个字节都是好几条 pattern
//    的命中 (G2Note 要跑)。这一档 Frel 比 MatchScanner 慢 17%, 比旧实现快 4%。
//    真语料不长这样; 要在这种输入上也快, 只能让表少造端点 (那是表的问题, 不是这一层的)。
//
// ── 🔴 这三档语料只有 10 条手造 pattern, 【会低估】: 换成真门表是 1.4~2.2 倍 ──────
// 上面 few/most 那两行"与 MatchScanner 打平 / 慢 17%"是这份小语料的性质, 不是这一层的
// 性质。同样的两条路放到从 asc 源码里静态收出来的三张真门表上 (368 条字面量按形状分成
// cred 64 条 / prompt 31 条 / body 160 条, 256KiB 正文 × 四档命中密度, 8 个布局各取
// 最小), 差距就出来了 —— 尺子在 tmp/frlbench, 跑法见那儿的 readme.txt:
//
//     表/密度      地板    d2总   frel总  | d2补  frel补 | 总 d2/frel  补 d2/frel
//     cred   0%    0.38    0.38    0.36     0.00  -0.02     1.06x        —
//     cred   1%    0.40    0.45    0.42     0.05   0.02     1.07x     2.50x
//     cred  10%    0.48    0.97    0.70     0.49   0.22     1.39x     2.23x
//     cred  90%    1.00    5.42    3.00     4.42   2.00     1.81x     2.21x
//     prompt 0%    0.38    0.38    0.36     0.00  -0.02     1.06x        —
//     prompt 1%    0.40    0.52    0.59     0.12   0.19     0.88x     0.63x
//     prompt10%    0.50    1.68    1.15     1.18   0.65     1.46x     1.82x
//     prompt90%    0.97   11.45    5.60    10.48   4.63     2.04x     2.26x
//     body   0%    1.85   33.53   14.94    31.68  13.09     2.24x     2.42x
//     body   1%    1.96   35.18   15.68    33.22  13.72     2.24x     2.42x
//     body  10%    2.35   37.67   17.08    35.32  14.73     2.21x     2.40x
//     body  90%    4.21   53.06   26.38    48.85  22.17     2.01x     2.20x
//     (ms · 地板 = 谁都要付的那一趟正向 set 扫 · 补 = 总 - 地板, 即补起点这一层的钱)
//
//  · 12 格里 11 格 Frel 更快, 端到端 1.06~2.24 倍, 补起点这一层稳定 2.2~2.4 倍。
//    钱省在哪很清楚: d2 每个右端问一次反向锚定, Frel 每个分量问一次。
//  · 唯一慢的一格是 prompt 1% (0.19ms vs 0.12ms, 差 0.07ms)。这张表全是顶层交替支多 /
//    带非 ASCII 的形状, 命中稀 (528 处) 时分量几乎是一处一个, 省不出次数, 反而多付了
//    切分量的记账。命中一密 (10%/90%) 就反超到 1.8~2.3 倍。
//  · body 表四档都是 2.2 倍上下, 且 0% 那行也有 20 万处命中 —— 因为这张 160 条的混合表
//    连填充词都能命中。这就是产线大表的常态。
//  · 与原型的口径一致: doc/plan12/20260831_219re2scanFast.txt §十二说 g2 端到端 1.3~1.9
//    倍 d2, 补起点层 1.4~2.2 倍。产品版量出来只多不少。

// frelBenchWork 是 frel 那条路的可复用工作区。
type frelBenchWork struct {
	s   *Re2Set_frel_t
	req Re2Set_req_t
	st  Re2Set_stats_t
	n   int
}

func newFrelBenchWork(tb testing.TB) *frelBenchWork {
	fwd, _, _ := benchObjects(tb)
	s, err := fwd.NewRe2Set_frel()
	if err != nil {
		tb.Fatalf("开 Re2Set_frel_t: %v", err)
	}
	w := &frelBenchWork{s: s}
	w.req = Re2Set_req_t{
		Allocer:          NewRe2Set_alloc(),
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { w.n += len(rs); return true },
		StatsResultFn:    func(st Re2Set_stats_t) { w.st = st },
	}
	return w
}

func (w *frelBenchWork) run(tb testing.TB, text string) int {
	w.n = 0
	w.req.Body = text
	if err := w.s.Scan(w.req); err != nil {
		tb.Fatalf("frel Scan: %v", err)
	}
	return w.n
}

// frelBenchMs 是 fll 那条路的可复用策略对象。
type frelBenchMs struct {
	ms  *Re2Set_fll_t
	req Re2Set_req_t
	n   int
}

func newFrelBenchMs(tb testing.TB) *frelBenchMs {
	fwd, _, _ := benchObjects(tb)
	ms, err := fwd.NewRe2Set_fll()
	if err != nil {
		tb.Fatalf("开 Re2Set_fll_t: %v", err)
	}
	w := &frelBenchMs{ms: ms}
	w.req = Re2Set_req_t{
		Allocer:          NewRe2Set_alloc(),
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { w.n += len(rs); return true },
	}
	return w
}

func (w *frelBenchMs) run(tb testing.TB, text string) int {
	w.n = 0
	w.req.Body = text
	if err := w.ms.Scan(w.req); err != nil {
		tb.Fatalf("fll Scan: %v", err)
	}
	return w.n
}

func BenchmarkRe2SetFrel(b *testing.B) {
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
		b.Run(kind+"/fll", func(b *testing.B) {
			w := newFrelBenchMs(b)
			defer w.ms.Close()
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w.run(b, text)
			}
		})
		b.Run(kind+"/frel", func(b *testing.B) {
			w := newFrelBenchWork(b)
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

// TestRe2SetFrel_Cost 把三条路在三档语料上的【产出与代价】并排打出来 —— 光看 ns/op
// 说不清"为什么", 这里报的是 Frel 那条路真正花钱的两个数:
//
//	NSeg  切出来几个分量 (存活位干的活)
//	Tries 问了几次反向锚定 —— 这一层唯一按命中数增长的成本
//	Tries/NSeg 就是"平均每个分量里有几处不重叠的匹配"; 它越接近 1, 说明存活位把分量
//	           切得越干净, 一个分量一趟锚定就结完。
func TestRe2SetFrel_Cost(t *testing.T) {
	w := newFrelBenchWork(t)
	defer w.s.Close()
	o := newBenchOld(t)
	ms := newFrelBenchMs(t)
	defer ms.ms.Close()
	t.Logf("%-6s %8s %10s %10s %10s %8s %8s %10s", "语料", "字节",
		"旧实现处数", "fll处数", "frel处数", "分量数", "锚定数", "峰值字节")
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		nold := len(o.run(text)) / 3
		nms := ms.run(t, text)
		nfrel := w.run(t, text)
		st := w.st
		t.Logf("%-6s %8d %10d %10d %10d %8d %8d %10d",
			kind, len(text), nold, nms, nfrel, st.NSeg, st.Tries, st.UsedPeak)
	}
}
