package hgmLibre2

import (
	"fmt"
	"testing"
	"time"
)

// matchscan2_bench_test.go —— 三条路 (A / B / D2) 在【同一张表 · 同一份正文 · 同一批
// set 对象】上的对照测量。
//
// 🔴 三条路必须共用同一个 *RegexpSet (benchObjects 那份)。同一批 pattern 建三次, 三个 DFA
//    的状态区落在不同地址上, cache set 冲突不一样 —— 实测同一段代码只因为换了个 set 对象
//    就能差 5~8%, 比要量的差别还大。共用之后剩下的差别才是【这三条路】的差别。
//
// 三条路各是什么见 matchscan2.go 文件头。一句话:
//   A  = MatchScanner + spanFast : 反向【只种 accept】回推一个起点 + 正向锚定取最长右端。快, 口径不严。
//   B  = MatchScanner 默认档     : 从 max(cur, e-maxL) 起正向【非锚定】搜一趟。口径严, 要走空隙。
//   D2 = MatchScanner2           : 反向【种全部状态】收全部候选 + 升序逐个正向锚定验。口径严。

// ms2Run 跑一遍并返回交出去的区间数 (拿它当 sink, 免得被优化掉)。
func ms2RunA(tb testing.TB, ms *MatchScanner, text string) int {
	n := 0
	if err := ms.Scan(text, func(mm []SetMatch) { n += len(mm) }); err != nil {
		tb.Fatal(err)
	}
	return n
}

func ms2RunD(tb testing.TB, ms *MatchScanner2, text string) int {
	n := 0
	if err := ms.Scan(text, func(mm []SetMatch) { n += len(mm) }); err != nil {
		tb.Fatal(err)
	}
	return n
}

// ms2Paths 给同一个 set 开出三条路的工作区 (A 那条把每条 pattern 都拧到 spanFast)。
// 顺手各跑一遍热身: 三条路要的单条对象 (fwd1 / rev1 / vp1) 都是惰性建的, 不热身的话
// 第一次计时会把编译价算进去。
func ms2Paths(tb testing.TB, set *RegexpSet, warm string) (a, b *MatchScanner, d *MatchScanner2) {
	a, _, err := set.NewMatchScanner()
	if err != nil {
		tb.Fatal(err)
	}
	modes := make([]MatchScanMode_t, set.GetPatternLen())
	for i := range modes {
		modes[i] = MatchScanMode_spanFast
	}
	if err := a.SetModes(modes); err != nil {
		tb.Fatal(err)
	}
	b, _, err = set.NewMatchScanner()
	if err != nil {
		tb.Fatal(err)
	}
	d, _, err = set.NewMatchScanner2()
	if err != nil {
		tb.Fatal(err)
	}
	ms2RunA(tb, a, warm)
	ms2RunA(tb, b, warm)
	ms2RunD(tb, d, warm)
	return a, b, d
}

// BenchmarkMatchScanPaths —— 三条路 × 三档语料。go test -run XXX -bench MatchScanPaths 看。
func BenchmarkMatchScanPaths(b *testing.B) {
	fwd, _, _ := benchObjects(b)
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		sa, sb, sd := ms2Paths(b, fwd, text)
		b.Run(kind+"/A_spanFast", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				ms2RunA(b, sa, text)
			}
		})
		b.Run(kind+"/B_default", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				ms2RunA(b, sb, text)
			}
		})
		b.Run(kind+"/D2", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				ms2RunD(b, sd, text)
			}
		})
		sa.Close()
		sb.Close()
		sd.Close()
	}
}

// ms2Time 把一段跑 rounds 遍取【最小值】—— 最小值比平均值抗噪 (噪声只会让它变慢)。
func ms2Time(rounds int, f func()) time.Duration {
	best := time.Duration(1<<62 - 1)
	for i := 0; i < rounds; i++ {
		t0 := time.Now()
		f()
		if d := time.Since(t0); d < best {
			best = d
		}
	}
	return best
}

// TestMatchScan2PerfTable —— 把三条路的价钱打成一张表 (Benchmark 的输出要拼三行才看得出
// 倍数, 这里直接给倍数)。顺手把 D2 多出来的两笔常驻开销也打出来:
// 每条 pattern 一份【反向 set】的 DFA 缓存, 以及"验了几个假候选"。
func TestMatchScan2PerfTable(t *testing.T) {
	fwd, _, _ := benchObjects(t)
	fmt.Printf("\n── 三条路 · benchPats(%d 条) × 64KiB 语料 · 取 %d 轮最小值 ──\n",
		len(benchPats), 7)
	fmt.Printf("%-6s %10s %10s %10s   %8s %8s   %s\n",
		"语料", "A(快档)", "B(默认)", "D2", "B/D2", "D2/A", "D2 的账")
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		sa, sb, sd := ms2Paths(t, fwd, text)
		var na, nb, nd int
		ta := ms2Time(7, func() { na = ms2RunA(t, sa, text) })
		tb := ms2Time(7, func() { nb = ms2RunA(t, sb, text) })
		td := ms2Time(7, func() { nd = ms2RunD(t, sd, text) })
		if nb != nd {
			t.Errorf("语料 %s: 路 B 交出 %d 处, D2 交出 %d 处 —— 两条严格口径不该不一样", kind, nb, nd)
		}
		st := sd.Stats()
		fmt.Printf("%-6s %10s %10s %10s   %8.2f %8.2f   walks=%d cands=%d tries=%d emits=%d (A 交 %d 处)\n",
			kind, ta, tb, td,
			float64(tb)/float64(td), float64(td)/float64(ta),
			st.Walks, st.Cands, st.Tries, st.Emits, na)
		sa.Close()
		sb.Close()
		sd.Close()
	}
	nRev, sRev, aRev := fwd.ReverseOneStats()
	nVp, sVp, aVp := fwd.ViableOneStats()
	fmt.Printf("常驻: 路 A 的反向单条 %d 条 / %d 状态 / %.2fMB · D2 的反向单条 set %d 条 / %d 状态 / %.2fMB\n\n",
		nRev, sRev, float64(aRev)/(1<<20), nVp, sVp, float64(aVp)/(1<<20))
}

// ms2HardPats 是三条路都会露怯的那几个形状 (doc/plan12 里那条"正向锚定收口无上界"的账):
// 起点定了之后正向取最长右端要一路走到机器死, 而机器【死不掉】。
// 拿它们量的不是"谁快", 是"谁在最坏形状上塌得更厉害"。
var ms2HardPats = []struct{ name, pat, fill string }{
	{"a|a+b", `a|a+b`, "a"},
	{"a|[ab]+c", `a|[ab]+c`, "ab"},
	{"err|err[a-z ]*fatal", `err|err[a-z ]*fatal`, "err "},
	{"AAA-{8,16}", `AAA-[A-Za-z0-9]{8,16}`, "AAA-abcdefgh12345 "},
}

// TestMatchScan2PerfHard —— 最坏形状上的对照。正文故意撑到 8KiB 就停:
// 这一档三条路都是二次的, 再翻一倍就是分钟级, 量不出更多东西。
func TestMatchScan2PerfHard(t *testing.T) {
	if testing.Short() {
		t.Skip("最坏形状档: -short 跳过")
	}
	fmt.Printf("\n── 最坏形状 · 单条 pattern × 8KiB 对抗正文 ──\n")
	fmt.Printf("%-22s %10s %10s %10s   %8s   %s\n", "pattern", "A(快档)", "B(默认)", "D2", "B/D2", "D2 的账")
	for _, hp := range ms2HardPats {
		text := ""
		for len(text) < 8<<10 {
			text += hp.fill
		}
		text = text[:8<<10]
		set, err := NewRegexpSet([]string{hp.pat})
		if err != nil {
			t.Fatal(err)
		}
		sa, sb, sd := ms2Paths(t, set, text)
		var nb, nd int
		ta := ms2Time(3, func() { ms2RunA(t, sa, text) })
		tb := ms2Time(3, func() { nb = ms2RunA(t, sb, text) })
		td := ms2Time(3, func() { nd = ms2RunD(t, sd, text) })
		if nb != nd {
			t.Errorf("%s: 路 B 交出 %d 处, D2 交出 %d 处 —— 两条严格口径不该不一样", hp.name, nb, nd)
		}
		st := sd.Stats()
		fmt.Printf("%-22s %10s %10s %10s   %8.2f   walks=%d cands=%d tries=%d emits=%d\n",
			hp.name, ta, tb, td, float64(tb)/float64(td),
			st.Walks, st.Cands, st.Tries, st.Emits)
		sa.Close()
		sb.Close()
		sd.Close()
	}
	fmt.Println()
}
