package hgmLibre2

// spanscan_stress_test.go — 把流式扫描最脆的两条路压出来:
//
//   ① 挂起/恢复 撞上 缓存整表清空 (flush)。
//      挂起的做法是"把当前 DFA 状态按内容存下来, 放掉缓存读锁, 返回给 Go"; 恢复的做法是
//      "重新拿锁, 按内容把状态查回来"。两者中间别的线程 (或者自己下一段扫描) 随时可能把
//      整张状态表清掉 —— 存的是【内容】不是指针, 就是为了扛这个。这条路走错了不会崩,
//      会【悄悄少吐几段】, 所以必须拿"结果不随预算/批大小变化"来钉。
//
//   ② 同一个 Set 上多个 goroutine 各拿一个 scanner 并发扫。
//      挂起期间是【一把锁都不持有】的, 所以并发下的交错比普通扫描更花样百出。
//
// 判据都是同一条不变量: batch 和 maxMem 只影响【怎么吐】和【多快】, 不影响【吐什么】。

import (
	"sync"
	"testing"
)

// stressPatterns 里第一条是库文档里那个著名的状态爆炸形状 (起始类窄于重复类的计数重复):
// 非锚定搜索下它的活跃起点集会退化成任意子集, 状态数对计数上界指数增长 —— 正是最容易
// 把状态缓存撑爆、逼出 flush 的形状。见 doc/状态数为什么会相乘.txt。
var stressPatterns = []string{
	`[A-Za-z][A-Za-z0-9]{2,19}key`,
	`[a-z]{4,}`,
	`[0-9]{2,8}`,
	`x[a-z0-9]{3,12}z`,
	`QQ[A-Z]{3,10}`,
}

// stressText 造一篇确定性的长正文 (自带 LCG, 不用 math/rand, 免得换了 Go 版本语料就变了)。
func stressText(n int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "
	buf := make([]byte, n)
	x := uint32(12345)
	for i := range buf {
		x = x*1664525 + 1013904223
		buf[i] = alpha[(x>>16)%uint32(len(alpha))]
	}
	// 埋几个一定命中的形状, 免得全靠随机语料撞。
	for _, seed := range []struct {
		at  int
		val string
	}{
		{n / 7, "Abc123key"}, {n / 3, "xq7v9z"}, {n / 2, "QQABCDE"},
		{n * 2 / 3, "Zz9999999key"}, {n * 5 / 6, "xabcdefghijkz"},
	} {
		copy(buf[seed.at:], seed.val)
	}
	return string(buf)
}

func spansEqual(t *testing.T, tag string, got, ref []SetSpan) {
	t.Helper()
	sortSpans(got)
	sortSpans(ref)
	if len(got) != len(ref) {
		t.Fatalf("%s: 游程条数 %d, 参照 %d", tag, len(got), len(ref))
	}
	for i := range got {
		if got[i] != ref[i] {
			t.Fatalf("%s: 第 %d 条 %+v, 参照 %+v", tag, i, got[i], ref[i])
		}
	}
}

// TestSpanScan_FlushDuringSuspend 用一个小得离谱的预算逼出反复 flush, 同时把 batch 压到最小
// 逼出反复挂起, 结果必须与"预算充足 + 一批装得下"完全一致。
func TestSpanScan_FlushDuringSuspend(t *testing.T) {
	text := stressText(256 << 10)

	ref, err := NewRegexpSetMaxMem(stressPatterns, 64<<20)
	if err != nil {
		t.Fatalf("建参照 set: %v", err)
	}
	refSc, err := ref.NewSpanScanner(1 << 16)
	if err != nil {
		t.Fatalf("参照 scanner: %v", err)
	}
	defer refSc.Close()
	want, err := refSc.AppendSpans(nil, text)
	if err != nil {
		t.Fatalf("参照扫: %v", err)
	}
	if len(want) == 0 {
		t.Fatalf("语料一条都没命中, 这个压力测试白跑了")
	}
	if f := ref.MemInfo().FlushesTotal; f != 0 {
		t.Logf("参照 set 也 flush 了 %d 次 (不影响判据, 只是说明预算还是紧)", f)
	}

	tight, err := NewRegexpSetMaxMem(stressPatterns, 1<<20)
	if err != nil {
		t.Fatalf("建紧预算 set: %v", err)
	}
	sc, err := tight.NewSpanScanner(1) // 最小批: 每凑够一条就挂起一次
	if err != nil {
		t.Fatalf("紧 scanner: %v", err)
	}
	defer sc.Close()
	got, err := sc.AppendSpans(nil, text)
	if err != nil {
		t.Fatalf("紧预算扫: %v", err)
	}
	spansEqual(t, "紧预算+最小批", got, want)

	// 这个测试的价值全在"真的 flush 了"上 —— 没 flush 就只是又跑了一遍普通扫描。
	if f := tight.MemInfo().FlushesTotal; f == 0 {
		t.Fatalf("紧预算 set 一次都没 flush, 没压到 StateSaver 那条路 (该把 maxMem 再调小或语料再调复杂)")
	} else {
		t.Logf("紧预算 set flush 了 %d 次", f)
	}
}

// TestSpanScan_ConcurrentScanners: 同一个 Set, 每个 goroutine 一个 scanner, 各扫各的。
// 挂起期间不持任何锁, 所以并发下的交错比普通扫描多得多; 预算故意开小让 flush 也掺进来。
func TestSpanScan_ConcurrentScanners(t *testing.T) {
	texts := []string{
		stressText(64 << 10),
		stressText(64<<10) + "Abc123key tail",
		stressText(17 << 10),
	}

	ref, err := NewRegexpSetMaxMem(stressPatterns, 64<<20)
	if err != nil {
		t.Fatalf("建参照 set: %v", err)
	}
	refSc, err := ref.NewSpanScanner(1 << 16)
	if err != nil {
		t.Fatalf("参照 scanner: %v", err)
	}
	defer refSc.Close()
	want := make([][]SetSpan, len(texts))
	for i, tx := range texts {
		want[i], err = refSc.AppendSpans(nil, tx)
		if err != nil {
			t.Fatalf("参照扫 #%d: %v", i, err)
		}
	}

	shared, err := NewRegexpSetMaxMem(stressPatterns, 1<<20)
	if err != nil {
		t.Fatalf("建共享 set: %v", err)
	}
	var wg sync.WaitGroup
	errCh := make(chan string, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sc, err := shared.NewSpanScanner(1 + g) // 每个 goroutine 批大小不同
			if err != nil {
				errCh <- "NewSpanScanner: " + err.Error()
				return
			}
			defer sc.Close()
			var buf []SetSpan
			for round := 0; round < 6; round++ {
				i := (g + round) % len(texts)
				buf, err = sc.AppendSpans(buf[:0], texts[i])
				if err != nil {
					errCh <- "Scan: " + err.Error()
					return
				}
				cp := append([]SetSpan(nil), buf...)
				sortSpans(cp)
				w := append([]SetSpan(nil), want[i]...)
				sortSpans(w)
				if len(cp) != len(w) {
					errCh <- "并发结果条数不一致"
					return
				}
				for k := range cp {
					if cp[k] != w[k] {
						errCh <- "并发结果内容不一致"
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatalf("%s", msg)
	}
	t.Logf("共享 set flush 了 %d 次", shared.MemInfo().FlushesTotal)
}
