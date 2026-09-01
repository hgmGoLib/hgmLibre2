package hgmLibre2

// re2set_concurrent_test.go —— 钉住这次拆分的那条核心承诺:
//
//	Re2Set_*_t 是【进程级】的, 同一个对象上可以并发 Scan;
//	Close 可以在别的 goroutine 正在 Scan 的时候调, 不崩、不 use-after-free。
//
// 🔴 这两格必须带 -race 跑才算数。它们看的是"有没有共享可变状态漏在对象上"——
//    2026-09-01 之前每遍扫描的暂存(spanscan 句柄 · 游程缓冲 · 候选缓冲 · 游标)全挂在
//    对象身上, 那时候这两格是必红的。

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

var re2setConcPats = []string{
	`\d{3}-\d{4}`,
	`[A-Z]{2}\d{2,6}`,
	`[a-z]+@[a-z]+\.[a-z]{2,4}`,
	`x[a-f]{1,8}y`,
	`\b[0-9a-f]{8}\b`,
}

// re2setConcTexts 造 32 段互不相同的正文 —— 每条腿扫自己那段, 所以"结果串味"会直接判红。
func re2setConcTexts() []string {
	out := make([]string, 32)
	for i := range out {
		var sb strings.Builder
		for k := 0; k < 40; k++ {
			fmt.Fprintf(&sb, "id%02d-%03d %d%d%d-%d%d%d%d AB%04d u%d@ex.com xdeadbeefy 0123abcd ",
				i, k, i%10, k%10, (i+k)%10, k%10, i%10, (i*k)%10, (i+1)%10, (i*97+k)%10000, i%10)
		}
		out[i] = sb.String()
	}
	return out
}

// TestRe2Set_ConcurrentScan —— 同一个 Re2Set_fll_t 上 32 条腿同时扫, 每处结果必须与
// 【串行跑同一段正文】逐字节相同。每条腿自己带一个 Allocer (alloc 不是并发安全的)。
func TestRe2Set_ConcurrentScan(t *testing.T) {
	set, err := NewRegexpSet(re2setConcPats)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	texts := re2setConcTexts()
	// 先串行取基准 (顺带把补端点的单条对象都惰性建出来, 免得并发那轮全挤在建对象上)。
	want := make([]string, len(texts))
	for i, text := range texts {
		got, hits, _ := scanFlat(t, ms.Scan, text)
		want[i] = fmt.Sprint(got) + " hits=" + fmt.Sprint(hits)
	}

	const rounds = 8
	got := make([]string, len(texts))
	var wg sync.WaitGroup
	for i := range texts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := NewRe2Set_alloc() // 🔴 一条腿一个: alloc 是纯缓冲, 不并发安全
			for r := 0; r < rounds; r++ {
				var all []Re2Set_startEnd_t
				var hits []int32
				err := ms.Scan(Re2Set_req_t{
					Body:             texts[i],
					Allocer:          a,
					StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { all = append(all, rs...); return true },
					HitIndexResultFn: func(h []int32) { hits = append(hits, h...) },
				})
				if err != nil {
					t.Errorf("腿 %d 轮 %d: %v", i, r, err)
					return
				}
				byPat := map[int32][]int32{}
				for _, m := range all {
					byPat[m.Index] = append(byPat[m.Index], m.Start, m.End)
				}
				s := fmt.Sprint(byPat) + " hits=" + fmt.Sprint(hits)
				if r == 0 {
					got[i] = s
				} else if s != got[i] {
					t.Errorf("腿 %d 轮 %d 自己前后不一致", i, r)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	for i := range texts {
		if got[i] != want[i] {
			t.Fatalf("腿 %d 并发结果与串行基准不同:\n 并发 %s\n 串行 %s", i, got[i], want[i])
		}
	}
}

// TestRe2Set_CloseDuringScan —— 一边扫一边 Close。允许的结局只有两种: 扫成功, 或者报
// "已经 Close"。不允许的是崩 / -race 报警 / 释放了还在用的 native 对象。
//
// 🔴 这一格钉的是 native 那侧的【引用计数】: Close 只是引用减一, 手上还在跑的那几遍各攥着
//    一份, 最后一个走的人才真拆。没有引用计数的话这里就是 use-after-free。
func TestRe2Set_CloseDuringScan(t *testing.T) {
	set, err := NewRegexpSet(re2setConcPats)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	texts := re2setConcTexts()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := NewRe2Set_alloc()
			for r := 0; r < 20; r++ {
				n := 0
				err := ms.Scan(Re2Set_req_t{
					Body:             texts[i],
					Allocer:          a,
					StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { n += len(rs); return true },
				})
				if err != nil && !strings.Contains(err.Error(), "已经 Close") {
					t.Errorf("腿 %d 轮 %d 报了个意料之外的错: %v", i, r, err)
					return
				}
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		go ms.Close() // 幂等 · 可以并发调
	}
	wg.Wait()
	ms.Close()
	if err := ms.Scan(Re2Set_req_t{Body: "abc", StartEndResultFn: func([]Re2Set_startEnd_t) bool { return true }}); err == nil {
		t.Fatal("Close 之后再 Scan 该报错")
	}
}
