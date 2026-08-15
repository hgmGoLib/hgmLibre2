package hgmLibre2

// find_all_flat_test.go — AppendAllStringIndexFlat 与 FindAllStringIndex / stdlib 逐处对拍,
// 并钉死"复用同一块缓冲不串味"与"稳态零分配"(本方法存在的全部理由就是后者)。
// 复用 hgmLibre2_test.go 的语料 (testPatterns 里有空匹配 `a*`、锚定 `^the\b`、多子组、命名组 ——
// 子组那几条尤其要测: 本方法按 nmatch=1 只回填 group0, 必须证明 group0 的集合不因此变化)。

import (
	"regexp"
	"testing"
)

// flatOf 把 [][]int 摊平成 [s0,e0,s1,e1,…], 作为对拍基准形态。
func flatOf(locs [][]int) []int {
	out := make([]int, 0, len(locs)*2)
	for _, l := range locs {
		out = append(out, l[0], l[1])
	}
	return out
}

func TestAppendAllStringIndexFlat_EquivFindAll(t *testing.T) {
	var buf []int // 单块缓冲跨所有 pattern × input × n 复用 (模拟热路径)
	for _, pat := range testPatterns {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		for _, in := range testInputs {
			for _, n := range []int{-1, 0, 1, 2, 3, 1000} {
				want := flatOf(mine.FindAllStringIndex(in, n))
				wantStd := flatOf(std.FindAllStringIndex(in, n))
				buf = mine.AppendAllStringIndexFlat(buf[:0], in, n)
				if !sameIntSlice(buf, want) {
					t.Errorf("flat vs FindAllStringIndex 不一致 pat=%q in=%q n=%d flat=%v want=%v",
						pat, in, n, buf, want)
				}
				if !sameIntSlice(buf, wantStd) {
					t.Errorf("flat vs stdlib 不一致 pat=%q in=%q n=%d flat=%v std=%v",
						pat, in, n, buf, wantStd)
				}
			}
		}
	}
}

// TestAppendAllStringIndexFlat_AppendsNotOverwrites — 语义是【追加】: 已有内容必须原样保留,
// 无匹配时一个元素都不许写 (调用方靠 len 差判"有没有命中")。
func TestAppendAllStringIndexFlat_AppendsNotOverwrites(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	dst := []int{-7, -8}
	dst = re.AppendAllStringIndexFlat(dst, "abc123def45", -1)
	if !sameIntSlice(dst, []int{-7, -8, 3, 6, 9, 11}) {
		t.Fatalf("追加语义错 got=%v", dst)
	}
	before := len(dst)
	dst = re.AppendAllStringIndexFlat(dst, "no digits here", -1)
	if len(dst) != before {
		t.Fatalf("无匹配却往 dst 写了东西 got=%v", dst)
	}
}

// TestAppendAllStringIndexFlat_ReuseNoBleed — 命中多的一趟之后紧跟命中少的一趟,
// buf[:0] 复用不得回放上一趟的尾巴 (切片长度必须由本趟决定)。
func TestAppendAllStringIndexFlat_ReuseNoBleed(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	var buf []int
	buf = re.AppendAllStringIndexFlat(buf[:0], "1 2 3 4 5 6 7 8", -1)
	if len(buf) != 16 {
		t.Fatalf("首趟应 8 处命中 got=%v", buf)
	}
	buf = re.AppendAllStringIndexFlat(buf[:0], "only 42 here", -1)
	if !sameIntSlice(buf, []int{5, 7}) {
		t.Fatalf("复用后串味 got=%v", buf)
	}
}

// TestAppendAllStringIndexFlat_SteadyStateNoAlloc — 本方法存在的全部理由。
// 缓冲吃饱之后反复调必须是【零 Go 堆分配】(C 侧那块 malloc/free 不计 Go 堆);
// 同一语料的 FindAllStringIndex 则必然线性分配 —— 两边都量, 单边写不出判别力。
func TestAppendAllStringIndexFlat_SteadyStateNoAlloc(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	in := ""
	for i := 0; i < 500; i++ {
		in += "word 12345 "
	}
	var buf []int
	buf = re.AppendAllStringIndexFlat(buf[:0], in, -1) // 预热: 让缓冲长到位
	if len(buf) != 1000 {
		t.Fatalf("语料没有 500 处命中, 量不出东西 got=%d", len(buf))
	}
	flat := testing.AllocsPerRun(20, func() {
		buf = re.AppendAllStringIndexFlat(buf[:0], in, -1)
	})
	old := testing.AllocsPerRun(20, func() {
		sinkLocs = re.FindAllStringIndex(in, -1)
	})
	t.Logf("RESULT: flat=%.0f allocs/次 · FindAllStringIndex=%.0f allocs/次 (500 处命中)", flat, old)
	if flat != 0 {
		t.Fatalf("🔴 稳态仍有 %.0f 次分配 —— 复用缓冲没生效", flat)
	}
	if old < 2 {
		t.Fatalf("🔴 对照组 FindAllStringIndex 只分配 %.0f 次, 本锚量不出差别 (库改过了?)", old)
	}
}

var sinkLocs [][]int
