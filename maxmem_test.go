package hgmLibre2

// maxmem_test.go — 单条 Regexp 的内存预算 (CompileMaxMem / MaxMem)。
//
// 为什么这个旋钮要有: RE2 的 DFA 状态缓存装不下当前语料走出来的状态集时不是 LRU 淘汰,
// 而是【整表清空重建】—— 结果仍然正确, 所以调用方看不见任何信号, 但吞吐是几十倍的悬崖。
// Set 早就能调预算 (NewRegexpSetMaxMem), 单条 Regexp 以前只能吃 RE2 默认的 8MB。

import (
	"strings"
	"testing"
	"time"
)

func TestCompileMaxMem_Readback(t *testing.T) {
	re := MustCompile(`abc`)
	defer re.FreeC()
	if got := re.GetMaxMem(); got != DefaultMaxMem {
		t.Fatalf("Compile 出来的 MaxMem=%d, want DefaultMaxMem=%d", got, DefaultMaxMem)
	}
	for _, mm := range []int64{1 << 20, 64 << 20, 1 << 30} {
		r, err := CompileMaxMem(`abc`, mm)
		if err != nil {
			t.Fatalf("CompileMaxMem(%d): %v", mm, err)
		}
		if got := r.GetMaxMem(); got != mm {
			t.Fatalf("CompileMaxMem(%d).GetMaxMem()=%d", mm, got)
		}
		r.FreeC()
	}
	// <=0 回落到默认, 与 NewRegexpSetMaxMem 的约定一致。
	for _, mm := range []int64{0, -1} {
		r, err := CompileMaxMem(`abc`, mm)
		if err != nil {
			t.Fatalf("CompileMaxMem(%d): %v", mm, err)
		}
		if got := r.GetMaxMem(); got != DefaultMaxMem {
			t.Fatalf("CompileMaxMem(%d).GetMaxMem()=%d, want 默认 %d", mm, got, DefaultMaxMem)
		}
		r.FreeC()
	}
}

func TestCompileMaxMem_TooSmallFails(t *testing.T) {
	// 预算小到装不下【编译期程序指令】时 Compile 必须报错, 而不是悄悄编出个残废的机器。
	_, err := CompileMaxMem(`[A-Za-z][A-Za-z0-9]{2,19}key`, 1024)
	if err == nil {
		t.Fatal("1KB 预算下这条 pattern 应当编译失败")
	}
	if !strings.Contains(err.Error(), "compile failed") {
		t.Fatalf("错误文案没说清是编译失败: %v", err)
	}
	// 语法错依然要报语法错, 不能被预算逻辑吃掉。
	if _, err := CompileMaxMem(`(`, 64<<20); err == nil {
		t.Fatal("坏 pattern 应当报错")
	}
}

func TestCompileMaxMem_SemanticsUnchanged(t *testing.T) {
	// 预算只影响"快不快 / 占多少内存", 不许影响"对不对"。
	const pat = `[A-Za-z][A-Za-z0-9]{2,19}key`
	corpus := append(revBodies(8, 4<<10), "abc123key", "", "no hit here", "AZzz9key tail")
	base := MustCompile(pat)
	defer base.FreeC()
	for _, mm := range []int64{1 << 16, 1 << 20, 8 << 20, 256 << 20} {
		re, err := CompileMaxMem(pat, mm)
		if err != nil {
			t.Fatalf("CompileMaxMem(%d): %v", mm, err)
		}
		rev, err := CompileReverseMaxMem(pat, mm)
		if err != nil {
			t.Fatalf("CompileReverseMaxMem(%d): %v", mm, err)
		}
		for _, s := range corpus {
			if got, want := re.MatchString(s), base.MatchString(s); got != want {
				t.Fatalf("maxMem=%d text 前 40 字节 %q: %v != %v", mm, head(s), got, want)
			}
			if got, want := rev.MatchString(s), base.MatchString(s); got != want {
				t.Fatalf("maxMem=%d (反向) text 前 40 字节 %q: %v != %v", mm, head(s), got, want)
			}
			if got, want := re.FindStringIndex(s), base.FindStringIndex(s); !sameIdx(got, want) {
				t.Fatalf("maxMem=%d text 前 40 字节 %q: FindStringIndex %v != %v", mm, head(s), got, want)
			}
		}
		rev.FreeC()
		re.FreeC()
	}
}

func head(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

func sameIdx(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCompileMaxMem_BiggerBudgetStopsThrash 是这个旋钮存在的理由本身:
// 同一条 pattern、同一批【互不相同】的正文, 默认 8MB 下 DFA 反复整表清空, 预算给够就一次都不清。
//
// 口径: GetDFAStats() 是【进程级】计数, 所以这个用例必须单线程跑 (Go test 默认同包串行, 且本用例
// 不调 t.Parallel)。断言只看 Resets 的方向, 不看吞吐 —— 吞吐在跑测试的机器上不稳。
func TestCompileMaxMem_BiggerBudgetStopsThrash(t *testing.T) {
	const pat = `[A-Za-z][A-Za-z0-9]{2,19}key`
	bodies := revBodies(60, 16<<10)

	run := func(mm int64) (resets uint64, d time.Duration) {
		re, err := CompileMaxMem(pat, mm)
		if err != nil {
			t.Fatalf("CompileMaxMem(%d): %v", mm, err)
		}
		defer re.FreeC()
		ResetDFAStats()
		t0 := time.Now()
		for _, s := range bodies {
			re.MatchString(s)
		}
		d = time.Since(t0)
		return GetDFAStats().Resets, d
	}

	small, dSmall := run(DefaultMaxMem)
	big, dBig := run(256 << 20)
	t.Logf("maxMem=%dMB: resets=%d (%v)", DefaultMaxMem>>20, small, dSmall)
	t.Logf("maxMem=256MB: resets=%d (%v)", big, dBig)
	if small == 0 {
		t.Fatalf("默认 8MB 预算下一次都没 flush —— 这份语料没把悬崖跑出来, 本用例等于没测")
	}
	if big != 0 {
		t.Fatalf("256MB 预算下还 flush 了 %d 次 —— 那这个旋钮没起作用", big)
	}

	// 反着扫是同一个问题的另一条出路, 而且不花内存: 默认预算下就一次都不 flush。
	rev := MustCompileReverse(pat)
	defer rev.FreeC()
	ResetDFAStats()
	var st ScanStats
	worst := int64(0)
	for _, s := range bodies {
		rev.MatchStats(s, &st)
		if st.FellBack {
			t.Fatalf("反向本不该在这条 pattern 上放弃 (预算 %d)", rev.GetMaxMem())
		}
		if st.StatesEnd > worst {
			worst = st.StatesEnd
		}
	}
	t.Logf("同一条 pattern 反着扫 (默认 8MB): resets=%d 状态峰值=%d", GetDFAStats().Resets, worst)
	if got := GetDFAStats().Resets; got != 0 {
		t.Fatalf("反向在默认预算下 flush 了 %d 次 —— 反着扫这条路没生效", got)
	}
}
