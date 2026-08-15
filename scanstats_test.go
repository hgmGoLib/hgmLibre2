// scanstats_test.go — 按对象归因的 DFA 计数 (MatchStats / MemInfo) 的门。
//
// 跟 dfastats_test.go 的区别就是这套东西存在的理由: 那份计数是【进程级】的, 只能回答
// "这个进程里有人在 thrash"; 这份要能回答"是哪个 Set"和"是哪一次调用"。所以下面的门里
// 有一道是【两个 Set 同时跑, 饿的那个涨、宽的那个一次都不许涨】—— 进程级计数过不了这道门。
package hgmLibre2

import (
	"sync"
	"testing"
)

// TestScanStats_AttributesFlushToTheCall — 饿着预算扫多形状语料:
// ①至少有一次调用报了 Flushes>0; ②所有调用的 Flushes 之和 == 同期进程级 Resets 增量。
// 第②条是口径自检: 两套计数数的是同一件事, 对不上就是漏计或重复计。
func TestScanStats_AttributesFlushToTheCall(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(16, 64<<10, len(pats))
	mem := minViableSetMem(t, pats)

	set, err := NewRegexpSetMaxMem(pats, mem)
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	buf := make([]int32, set.Size())

	DFAStatsZero()
	var st ScanStats
	var sumFlush, sumBuilt, withFlush int64
	for i, b := range bodies {
		set.MatchStats(b, buf, &st)
		if st.Bytes != int64(len(b)) {
			t.Fatalf("第 %d 份: Bytes=%d 应为 %d", i, st.Bytes, len(b))
		}
		if st.StateBudget <= 0 {
			t.Fatalf("第 %d 份: StateBudget=%d, 没把 DFA 的额度带回来", i, st.StateBudget)
		}
		if st.Flushes > 0 {
			withFlush++
		}
		sumFlush += st.Flushes
		sumBuilt += st.StatesBuilt
	}
	global := DFAStats().Resets
	t.Logf("预算 %d bytes: %d/%d 次调用踩到 flush, 合计 flush %d 次 (进程级 Resets %d), 共建状态 %d 个",
		mem, withFlush, len(bodies), sumFlush, global, sumBuilt)

	if sumFlush == 0 {
		t.Fatalf("饿到 %d bytes 扫 %d 份互不相同的 body, 每次调用都报 Flushes=0 —— 计数没接上",
			mem, len(bodies))
	}
	if sumFlush != int64(global) {
		t.Fatalf("口径对不上: per-scan 合计 %d 次, 进程级 Resets %d 次", sumFlush, global)
	}
	if sumBuilt <= 0 {
		t.Fatalf("StatesBuilt 全程为 0 —— 扫了 %d 份新语料不可能一个状态都不建", len(bodies))
	}
}

// TestScanStats_QuietWhenBudgetFits — 预算给宽 + 热身之后, 每一次调用都必须报 0。
// 恒 +1 的假计数过不了这道门。
func TestScanStats_QuietWhenBudgetFits(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(16, 64<<10, len(pats))
	set, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	buf := make([]int32, set.Size())
	for _, b := range bodies {
		set.Match(b, buf)
	}
	var st ScanStats
	for i, b := range bodies {
		set.MatchStats(b, buf, &st)
		if st.Flushes != 0 || st.Grows != 0 {
			t.Fatalf("64MB 预算热身后第 %d 份仍有动静: %+v", i, st)
		}
	}
}

// TestScanStats_PerSetAttribution — 这道门是整套东西的存在理由。
// 同一个进程里两个 Set: A 饿着(必 thrash), B 喂饱(必安静)。进程级计数这时候是涨的,
// 但 B 自己的每次调用和 B 的 MemInfo.FlushesTotal 必须一次都不涨。
func TestScanStats_PerSetAttribution(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(12, 64<<10, len(pats))
	memA := minViableSetMem(t, pats)

	starved, err := NewRegexpSetMaxMem(pats, memA)
	if err != nil {
		t.Fatalf("建饿集失败: %v", err)
	}
	fat, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建宽集失败: %v", err)
	}
	bufA := make([]int32, starved.Size())
	bufB := make([]int32, fat.Size())
	for _, b := range bodies { // 把宽集热身好
		fat.Match(b, bufB)
	}

	DFAStatsZero()
	var sa, sb ScanStats
	var flushA, flushB int64
	for _, b := range bodies {
		starved.MatchStats(b, bufA, &sa)
		fat.MatchStats(b, bufB, &sb)
		flushA += sa.Flushes
		flushB += sb.Flushes
	}
	global := DFAStats().Resets
	t.Logf("饿集 flush %d 次, 宽集 flush %d 次, 进程级 Resets %d", flushA, flushB, global)

	if flushA == 0 {
		t.Fatalf("饿集一次都没 flush —— 前提垮了")
	}
	if flushB != 0 {
		t.Fatalf("宽集被算进了 %d 次 flush —— 归因串台了", flushB)
	}
	if mi := fat.MemInfo(); mi.FlushesTotal != 0 {
		t.Fatalf("宽集 MemInfo.FlushesTotal=%d, 应为 0: %+v", mi.FlushesTotal, mi)
	}
	if mi := starved.MemInfo(); mi.FlushesTotal != flushA {
		t.Fatalf("饿集 MemInfo.FlushesTotal=%d 与 per-scan 合计 %d 对不上", mi.FlushesTotal, flushA)
	}
	if int64(global) != flushA+flushB {
		t.Fatalf("进程级 %d != 两个 Set 之和 %d", global, flushA+flushB)
	}
}

// TestSetMemInfo_TracksUsage — MemInfo 的三条: 没扫过时不建 DFA; 扫过之后水位有意义;
// 生涯计数与 per-scan 合计一致。
func TestSetMemInfo_TracksUsage(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(8, 64<<10, len(pats))
	set, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	// RE2::Set::Compile 自己会跑一次冒烟搜索, 所以编译完 DFA 就已经存在了 (States=1)。
	// 这里钉住的是"查询本身不制造状态": 查两次, 水位不动。
	before := set.MemInfo()
	if !before.Built || before.States > 4 {
		t.Fatalf("刚编完的水位不像话 (期望 Built 且只有个位数状态): %+v", before)
	}
	if again := set.MemInfo(); again.States != before.States || again.StatesBuiltTotal != before.StatesBuiltTotal {
		t.Fatalf("连查两次水位就变了 —— 查询在制造状态: %+v -> %+v", before, again)
	}
	buf := make([]int32, set.Size())
	var st ScanStats
	var built int64
	for _, b := range bodies {
		set.MatchStats(b, buf, &st)
		built += st.StatesBuilt
	}
	mi := set.MemInfo()
	t.Logf("%+v used=%d", mi, mi.Used())
	if !mi.Built {
		t.Fatalf("扫过之后仍报 Built=false: %+v", mi)
	}
	if mi.States <= 0 || mi.Used() <= 0 || mi.StateBudget <= 0 {
		t.Fatalf("水位不像话: %+v used=%d", mi, mi.Used())
	}
	if mi.MemLeft > mi.StateBudget {
		t.Fatalf("剩余额度比总额度还大: %+v", mi)
	}
	if mi.StatesBuiltTotal != built+before.StatesBuiltTotal {
		t.Fatalf("生涯建状态数 %d 与 (编译期 %d + per-scan 合计 %d) 对不上",
			mi.StatesBuiltTotal, before.StatesBuiltTotal, built)
	}
	if mi.States <= before.States {
		t.Fatalf("扫了 %d 份新语料, 状态数却没涨: %d -> %d", len(bodies), before.States, mi.States)
	}
	if mi.ArenaCap < 0 || mi.ArenaCap > mi.StateBudget+(1<<20) {
		t.Fatalf("ArenaCap=%d 不合理 (额度 %d)", mi.ArenaCap, mi.StateBudget)
	}
}

// TestScanStats_SameResultAsMatch — 带统计的那条路不许改结果。
func TestScanStats_SameResultAsMatch(t *testing.T) {
	pats := dfaStatsPatterns(40, 8)
	bodies := dfaStatsBodies(24, 8<<10, len(pats))
	set, err := NewRegexpSetMaxMem(pats, minViableSetMem(t, pats))
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	a := make([]int32, set.Size())
	b := make([]int32, set.Size())
	var st ScanStats
	for i, body := range bodies {
		want := append([]int32(nil), set.Match(body, a)...)
		got := set.MatchStats(body, b, &st)
		if len(want) != len(got) {
			t.Fatalf("第 %d 份命中数不同: %d vs %d", i, len(want), len(got))
		}
		for j := range want {
			if want[j] != got[j] {
				t.Fatalf("第 %d 份命中集不同: %v vs %v", i, want, got)
			}
		}
	}
	set.MatchStats(bodies[0], b, nil) // st=nil 那条路不许崩
}

// TestScanStats_ConcurrentDoesNotCrash — 并发下各算各的: 计数对象在各自栈上, 不许串台,
// 合计仍等于该 Set 的生涯计数 (StatesBuilt 那一项并发下会互相算, 故不校验)。
func TestScanStats_ConcurrentDoesNotCrash(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(12, 32<<10, len(pats))
	set, err := NewRegexpSetMaxMem(pats, minViableSetMem(t, pats))
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	const nw = 8
	var wg sync.WaitGroup
	flush := make([]int64, nw)
	for w := 0; w < nw; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			buf := make([]int32, set.Size())
			var st ScanStats
			for r := 0; r < 3; r++ {
				for _, b := range bodies {
					set.MatchStats(b, buf, &st)
					flush[w] += st.Flushes
				}
			}
		}(w)
	}
	wg.Wait()
	var sum int64
	for _, f := range flush {
		sum += f
	}
	mi := set.MemInfo()
	t.Logf("并发 %d 线程合计 flush %d 次, Set 生涯 %d 次: %+v", nw, sum, mi.FlushesTotal, mi)
	if sum != mi.FlushesTotal {
		t.Fatalf("并发下 per-scan 合计 %d != 生涯 %d", sum, mi.FlushesTotal)
	}
}
