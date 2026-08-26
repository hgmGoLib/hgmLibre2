// zexp_step_alloc_safety_test.go — B 方案 (Go 切片的底其实是 C malloc 的内存) 的两条安全门。
package hgmLibre2

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// ① 回调里触发 GC + 制造大量 Go 堆活动, C 内存上的那块 flat 必须一个字节都不变。
// 理论上必然成立 (那块内存不在 Go 堆里, GC 既不扫也不移动它, 且里面没有 Go 指针),
// 这条门是为了把"理论上"钉成"实测过"。
func TestStepCAlloc_SurvivesGC(t *testing.T) {
	re := MustCompile(`(\w+)=(\w+)`)
	body := stepBenchNHits(500)
	want := collectMain(re, body, -1, re.NumSubexp()+1)

	var got []int32
	batch := 0
	re.StepAllStringSubmatchIndexCAlloc(body, -1, func(f []int32) bool {
		head := append([]int32(nil), f...) // 回调进来时的快照
		// 往 Go 堆里砸垃圾并强制 GC —— 若那块内存真在 Go 堆上, 这里就是它被回收/移动的时候。
		junk := make([][]byte, 0, 64)
		for i := 0; i < 64; i++ {
			junk = append(junk, make([]byte, 4096))
		}
		runtime.GC()
		runtime.GC()
		runtime.KeepAlive(junk)
		for i := range f {
			if f[i] != head[i] {
				t.Fatalf("第 %d 批第 %d 个 int 在 GC 后变了: %d → %d", batch, i, head[i], f[i])
			}
		}
		got = append(got, f...)
		batch++
		return true
	})
	if len(got) != len(want) {
		t.Fatalf("GC 之后结果条数不对: want=%d got=%d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("GC 之后第 %d 个 int 不对: want=%d got=%d", i, want[i], got[i])
		}
	}
	t.Logf("跨 %d 批 · 每批回调内两次 runtime.GC() + 256KB Go 堆垃圾 · %d 个 int 全部一致", batch, len(got))
}

// ② 泄漏门: C 的 malloc 只有 Go 侧那句 free 兜底, 漏一次就是永久 RSS 增长。
// 跑 20 万次带命中的调用 (含"换大批"那条要 free 旧块的路径 + 提前停那条路径), 看 RSS。
func TestStepCAlloc_NoLeak(t *testing.T) {
	re := MustCompile(`(\w+)=(\w+)`)
	body := stepBenchNHits(50) // 50 处 ⇒ 必然经过 首批填满→free旧块→malloc大块
	const rounds = 200000

	warm := func() {
		for i := 0; i < 2000; i++ {
			re.StepAllStringSubmatchIndexCAlloc(body, -1, func(f []int32) bool { return true })
		}
	}
	warm()
	runtime.GC()
	before := rssKB(t)
	for i := 0; i < rounds; i++ {
		re.StepAllStringSubmatchIndexCAlloc(body, -1, func(f []int32) bool { return true })
		if i%7 == 0 { // 提前停: 那条路也必须把缓冲还掉
			re.StepAllStringSubmatchIndexCAlloc(body, -1, func(f []int32) bool { return false })
		}
	}
	runtime.GC()
	after := rssKB(t)
	// 泄漏的话每轮至少漏一块 3072B ⇒ 20 万轮 = 600MB, 不可能藏得住。给 32MB 的宽容度。
	if after > before+32*1024 {
		t.Fatalf("疑似泄漏: %d 轮之后 RSS %dKB → %dKB (+%dKB)", rounds, before, after, after-before)
	}
	t.Logf("%d 轮 (含换大批 + 提前停) 之后 RSS %dKB → %dKB (+%dKB), 无泄漏", rounds, before, after, after-before)
}

func rssKB(t *testing.T) int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("拿不到 RSS: %v", err)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "VmRSS:") {
			f := strings.Fields(ln)
			n, _ := strconv.Atoi(f[1])
			return n
		}
	}
	t.Skip("/proc/self/status 里没有 VmRSS")
	return 0
}
