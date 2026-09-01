package hgmLibre2

import (
	"fmt"
	"testing"
	"time"
)

// re2set_fll_perf_test.go —— fll 补起点那条路的价钱与它的账 (GetStats / 常驻内存)。
//
// 🔴 2026-08-28 之前这里是【三条路的对照台】(A=spanFast · B=默认档 · D2=MatchScanner2),
//    整个文件的骨架就是"同一个 set 上开三个工作区各跑一遍比倍数"。三条路合成一条之后
//    对照没有了对手, 于是这里只剩【一条路的绝对值 + 它自己的账】。
//    真正的换路凭据在调用方产品的扫描基准 (11 份 100MB 真语料 × 9 张生产真门表), 不在这儿 ——
//    这个文件量的是 64KiB 量级的形状, 只够看"有没有突然塌一个数量级"。
//
// 🔴 msPerfHardPats 那张表是【故意留着】的: 它是这条路最坏形状的哨兵 (起点定了之后正向
//    取最长右端要一路走到机器死, 而机器死不掉), 也是"试/看 > 1"唯一真的会发生的地方 ——
//    真门表上 99 格全是 1.00。哪天有人动了候选那一步, 先看这张表塌没塌。

// msPerfRun 跑一遍并返回交出去的区间数 (拿它当 sink, 免得被优化掉)。
func msPerfRun(tb testing.TB, ms *msPerfWork, text string) int {
	ms.n = 0
	if err := ms.s.Scan(text, ms.req); err != nil {
		tb.Fatal(err)
	}
	return ms.n
}

// msPerfWork 是工作区 + 那一份建一次留着的请求 (每遍现造一个 req 就是每遍一笔分配)。
type msPerfWork struct {
	s   *Re2Set_fll_t
	req *Re2Set_req_t
	n   int
}

func (w *msPerfWork) Close() { w.s.Close() }

// msPerfOpen 开一个工作区并热身一遍 —— 补端点的单条对象都是惰性建的, 不热身的话
// 第一次计时会把编译价算进去。
func msPerfOpen(tb testing.TB, set *RegexpSet, warm string) *msPerfWork {
	s, err := set.NewRe2Set_fll()
	if err != nil {
		tb.Fatal(err)
	}
	w := &msPerfWork{s: s}
	w.req = &Re2Set_req_t{
		Allocer:          NewRe2Set_alloc(),
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { w.n += len(rs); return true },
	}
	msPerfRun(tb, w, warm)
	return w
}

// msPerfTime 把一段跑 rounds 遍取【最小值】—— 最小值比平均值抗噪 (噪声只会让它变慢)。
func msPerfTime(rounds int, f func()) time.Duration {
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

// BenchmarkRe2SetFll —— 真表 × 三档语料。go test -run XXX -bench Re2SetFll 看。
func BenchmarkRe2SetFll(b *testing.B) {
	set, _, _ := benchObjects(b)
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		ms := msPerfOpen(b, set, text)
		b.Run(kind, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				msPerfRun(b, ms, text)
			}
		})
		ms.Close()
	}
}

// TestRe2SetFllPerfTable —— 把价钱和账打成一张表。
//
// 🔴 "试/看" = Tries/Walks = 每次回看正向锚定验了几次。1.00 = 升序第一个候选就是答案,
//    一次假候选都没验。这是这条路便不便宜的【唯一】那个数 —— 生产真门表上 99 格全是 1.00。
//    Emits 不进这个分母: 它把定长条 (走 e-minL 减法, 一次回看都不做) 也数进去了。
func TestRe2SetFllPerfTable(t *testing.T) {
	set, _, _ := benchObjects(t)
	fmt.Printf("\n── Re2Set_fll_t · benchPats(%d 条) × 64KiB 语料 · 取 7 轮最小值 ──\n", len(benchPats))
	fmt.Printf("%-6s %10s %8s   %s\n", "语料", "墙钟", "试/看", "账")
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		ms := msPerfOpen(t, set, text)
		var n int
		d := msPerfTime(7, func() { n = msPerfRun(t, ms, text) })
		st := ms.s.GetStats()
		fmt.Printf("%-6s %10s %8.2f   walks=%d cands=%d tries=%d emits=%d (交出 %d 处)\n",
			kind, d, float64(st.Tries)/float64(max1(st.Walks)),
			st.Walks, st.Cands, st.Tries, st.Emits, n)
		ms.Close()
	}
	nVp, sVp, aVp := set.GetViableOneStats()
	fmt.Printf("常驻: 反向单条 set %d 条 / %d 状态 / %.2fMB —— 这是这条路相对老的路 B 净增的那一笔\n\n",
		nVp, sVp, float64(aVp)/(1<<20))
}

// msPerfHardPats 是这条路会露怯的那几个形状 (doc/plan12 里那条"正向锚定收口无上界"的账):
// 起点定了之后正向取最长右端要一路走到机器死, 而机器【死不掉】。
// 拿它们量的不是"快不快", 是"最坏形状上塌得有多厉害"。
var msPerfHardPats = []struct{ name, pat, fill string }{
	{"a|a+b", `a|a+b`, "a"},
	{"a|[ab]+c", `a|[ab]+c`, "ab"},
	{"err|err[a-z ]*fatal", `err|err[a-z ]*fatal`, "err "},
	{"AAA-{8,16}", `AAA-[A-Za-z0-9]{8,16}`, "AAA-abcdefgh12345 "},
}

// TestRe2SetFllPerfHard —— 最坏形状。正文故意撑到 8KiB 就停: 这一档本来就是二次的,
// 再翻一倍就是分钟级, 量不出更多东西。
func TestRe2SetFllPerfHard(t *testing.T) {
	if testing.Short() {
		t.Skip("最坏形状档: -short 跳过")
	}
	fmt.Printf("\n── 最坏形状 · 单条 pattern × 8KiB 对抗正文 ──\n")
	fmt.Printf("%-22s %10s %8s   %s\n", "pattern", "墙钟", "试/看", "账")
	for _, hp := range msPerfHardPats {
		text := ""
		for len(text) < 8<<10 {
			text += hp.fill
		}
		text = text[:8<<10]
		set, err := NewRegexpSet([]string{hp.pat})
		if err != nil {
			t.Fatal(err)
		}
		ms := msPerfOpen(t, set, text)
		d := msPerfTime(3, func() { msPerfRun(t, ms, text) })
		st := ms.s.GetStats()
		fmt.Printf("%-22s %10s %8.2f   walks=%d cands=%d tries=%d emits=%d\n",
			hp.name, d, float64(st.Tries)/float64(max1(st.Walks)),
			st.Walks, st.Cands, st.Tries, st.Emits)
		ms.Close()
	}
	fmt.Println()
}
