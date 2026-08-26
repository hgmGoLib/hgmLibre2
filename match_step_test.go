// match_step_test.go — StepAllStringSubmatchIndex / StepAllStringIndex 的对拍门。
//
// 照 find_all_flat_test.go 那四条办, 另加两条 step 形态特有的:
//   ① 与本库 FindAllStringSubmatchIndex 逐处逐组对拍 (含未参与组的 -1,-1);
//   ② 与 stdlib regexp 对拍 (空匹配 / (?m)^$ / 嵌套组 / n 截断);
//   ③ 池里那块批缓冲反复借还不串味 (连着跨 re / 跨正文调, per 每次都不一样);
//   ④ 稳态零分配 (testing.AllocsPerRun);
//   ⑤ 🔴 强制切在批边界上 (batch=1/2/3): 跨批携带的 pos/prevEnd 是整件事里唯一会静默出错的
//      地方, 批一大就一次装完, 这条路径永远测不到;
//   ⑥ 提前停 (batchFn 返回 false) 与 miss 路径 (batchFn 一次都不调)。
package hgmLibre2

import (
	"regexp"
	"testing"
)

// stepPatterns 覆盖: 无子组 / 有子组 / 可选组(未参与 ⇒ -1,-1) / 嵌套组 / 空匹配 / 多行锚。
var stepPatterns = []string{
	`a+`,
	`(a)(b)?`,
	`(foo)|(bar)`,
	`((a)(b))c`,
	`\b\w+\b`,
	`a*`,
	`(?m)^$`,
	`(?m)^(\w+)=(\w*)$`,
	`[0-9]{2,4}`,
	`(?i)(https?)://([a-z0-9.-]+)`,
}

var stepInputs = []string{
	"",
	"a",
	"ab",
	"aaabbbaaa",
	"foo bar foo baz bar",
	"abcabcabc",
	"k=v\nkk=\n=vv\n\nx=1",
	"中文abc中文123abc",
	"http://a.b HTTPS://C.D/e http://x.y",
	"12 345 6789 0 11111",
	"\n\n\n",
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 长于任何批容量, 逼出多批
}

// stepCollectSub 用 step 收集全部命中, 物化成 [][]int (布局与 FindAllStringSubmatchIndex 相同)。
// batch>0 走 stepAll 的写死批容量入口 (把批边界切在指定处数上); batch=0 = 生产口径。
func stepCollectSub(re *Regexp, batch int, s string, n int) [][]int {
	per := 2 * (re.NumSubexp() + 1)
	var got [][]int
	re.stepAll(s, n, re.NumSubexp()+1, batch, func(flat []int32) bool {
		if len(flat)%per != 0 {
			panic("flat 长度不是 per 的整数倍")
		}
		for k := 0; k+per <= len(flat); k += per {
			one := make([]int, per)
			for i := 0; i < per; i++ {
				one[i] = int(flat[k+i])
			}
			got = append(got, one)
		}
		return true
	})
	return got
}

func eqLocs(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// TestStepAllStringSubmatchIndex_VsFindAll 条① + 条⑤: 与本库一次性 API 逐处逐组对拍,
// 批容量 1/2/3 与默认策略各跑一遍 —— 前三个把批边界强行切在命中之间。
func TestStepAllStringSubmatchIndex_VsFindAll(t *testing.T) {
	for _, pat := range stepPatterns {
		re := MustCompile(pat)
		for _, s := range stepInputs {
			want := re.FindAllStringSubmatchIndex(s, -1)
			for _, batch := range []int{1, 2, 3, 0} { // 0 = 生产口径 (一批 stepBufInts/per 处)
				got := stepCollectSub(re, batch, s, -1)
				if !eqLocs(want, got) {
					t.Fatalf("pat=%q s=%q batch=%d:\n want %v\n got  %v", pat, s, batch, want, got)
				}
			}
		}
	}
}

// TestStepAllStringSubmatchIndex_VsStdlib 条②: 与 stdlib regexp 对拍 (同样切批边界)。
// 只拿 RE2 与 stdlib 语义一致的 pattern (本库 README 记了非法 UTF-8 等少数差异, 此处输入都是合法的)。
func TestStepAllStringSubmatchIndex_VsStdlib(t *testing.T) {
	for _, pat := range stepPatterns {
		re := MustCompile(pat)
		std := regexp.MustCompile(pat)
		for _, s := range stepInputs {
			want := std.FindAllStringSubmatchIndex(s, -1)
			for _, batch := range []int{1, 2, 3, 0} {
				got := stepCollectSub(re, batch, s, -1)
				if !eqLocs(want, got) {
					t.Fatalf("stdlib pat=%q s=%q batch=%d:\n want %v\n got  %v", pat, s, batch, want, got)
				}
			}
		}
	}
}

// TestStepAllStringSubmatchIndex_NTrunc 条②的 n 截断部分: 各种 n 与 stdlib 逐处对拍,
// 且批容量小于 n 时截断必须发生在跨批之后。
func TestStepAllStringSubmatchIndex_NTrunc(t *testing.T) {
	for _, pat := range stepPatterns {
		re := MustCompile(pat)
		std := regexp.MustCompile(pat)
		for _, s := range stepInputs {
			for _, n := range []int{0, 1, 2, 3, 5, 100, -1} {
				want := std.FindAllStringSubmatchIndex(s, n)
				for _, batch := range []int{1, 2, 0} {
					got := stepCollectSub(re, batch, s, n)
					if !eqLocs(want, got) {
						t.Fatalf("n=%d pat=%q s=%q batch=%d:\n want %v\n got  %v", n, pat, s, batch, want, got)
					}
				}
			}
		}
	}
}

// TestStepAllStringIndex_VsFindAll group0-only 版与 FindAllStringIndex / stdlib 三方对拍。
func TestStepAllStringIndex_VsFindAll(t *testing.T) {
	for _, pat := range stepPatterns {
		re := MustCompile(pat)
		std := regexp.MustCompile(pat)
		for _, s := range stepInputs {
			want := std.FindAllStringIndex(s, -1)
			for _, batch := range []int{1, 2, 3, 0} {
				var got [][]int
				re.stepAll(s, -1, 1, batch, func(flat []int32) bool {
					for k := 0; k+2 <= len(flat); k += 2 {
						got = append(got, []int{int(flat[k]), int(flat[k+1])})
					}
					return true
				})
				if !eqLocs(want, got) {
					t.Fatalf("Index pat=%q s=%q batch=%d:\n want %v\n got  %v", pat, s, batch, want, got)
				}
				// 与本库自己的 AppendAllStringIndexFlat (待删除的老路) 也对一遍, 保证换形状不换语义。
				old := re.AppendAllStringIndexFlat(nil, s, -1)
				if len(old) != 2*len(got) {
					t.Fatalf("vs AppendAllStringIndexFlat pat=%q s=%q: 处数不同 %d vs %d", pat, s, len(old)/2, len(got))
				}
				for k := range got {
					if old[2*k] != got[k][0] || old[2*k+1] != got[k][1] {
						t.Fatalf("vs AppendAllStringIndexFlat pat=%q s=%q 第%d处: %v vs %v",
							pat, s, k, old[2*k:2*k+2], got[k])
					}
				}
			}
		}
	}
}

// TestStepAllString_Miss 条⑥ miss 路径: 一次都不调 batchFn。
func TestStepAllString_Miss(t *testing.T) {
	re := MustCompile(`zzz+`)
	calls := 0
	re.StepAllStringSubmatchIndex("abcabcabc", -1, func(flat []int32) bool { calls++; return false })
	if calls != 0 {
		t.Fatalf("无匹配却调了 batchFn %d 次", calls)
	}
	re.StepAllStringIndex("abcabcabc", -1, func(flat []int32) bool { calls++; return false })
	if calls != 0 {
		t.Fatalf("无匹配却调了 batchFn %d 次 (Index)", calls)
	}
}

// TestStepAllString_EarlyStop 条⑥ 提前停: batchFn 返回 false 之后不得再有回调。
func TestStepAllString_EarlyStop(t *testing.T) {
	re := MustCompile(`a`)
	s := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 32 处, 批容量 2 ⇒ 至少 16 批
	calls, seen := 0, 0
	re.stepAll(s, -1, 1, 2, func(flat []int32) bool {
		calls++
		seen += len(flat) / 2
		return calls < 3 // 第 3 批返回 false
	})
	if calls != 3 {
		t.Fatalf("提前停失败: batchFn 被调了 %d 次, 期望 3", calls)
	}
	if seen != 6 {
		t.Fatalf("提前停时收到 %d 处, 期望 6", seen)
	}
}

// TestStepAllString_ReuseNoBleed 条③ 借还不串味: 连着跨 re (per 不同) 跨正文调, 每块都是从池子里
// 借来的旧块 (上一次调用刚还回去、里面还留着上一次的命中下标), 每次结果都要与一次性 API 相同。
func TestStepAllString_ReuseNoBleed(t *testing.T) {
	for round := 0; round < 3; round++ {
		for _, pat := range stepPatterns {
			re := MustCompile(pat)
			for _, s := range stepInputs {
				want := re.FindAllStringSubmatchIndex(s, -1)
				got := stepCollectSub(re, 0, s, -1)
				if !eqLocs(want, got) {
					t.Fatalf("round=%d pat=%q s=%q:\n want %v\n got  %v", round, pat, s, want, got)
				}
			}
		}
	}
}

// TestStepAllString_SteadyZeroAlloc 条④ 零分配 —— 而且【第一次调用就零】: 批缓冲是从库内池子借的,
// 不是调用方现开的。第一版那套"调用方持有工作区"做不到这一条 (进 C 之前必须先 make 一块,
// 命不命中都付), 换池子的全部理由就在这。
func TestStepAllString_SteadyZeroAlloc(t *testing.T) {
	re := MustCompile(`(\w+)=(\w+)`)
	s := ""
	for i := 0; i < 200; i++ {
		s += "key=val "
	}
	var sink int
	run := func() {
		re.StepAllStringSubmatchIndex(s, -1, func(flat []int32) bool {
			sink += len(flat)
			return true
		})
	}
	run() // 让池子里先有一块 (第一块的 make 是一辈子一次, 不算稳态)
	if got := testing.AllocsPerRun(50, run); got != 0 {
		t.Fatalf("稳态每次调用分配 %v 笔, 期望 0", got)
	}
	if sink == 0 {
		t.Fatal("没扫到任何命中, 这个测试没测到东西")
	}
}

// TestStepAllString_MissZeroAlloc 🔴 miss 路径也必须零分配。
// 这一条是为一个真踩过的坑立的判据: 老形状每次调用无条件先 make 一块 ~200B 的首批缓冲,
// 【命不命中都付】, 而扫描型负载 (一张规则表挨个打同一份正文) 绝大多数调用是 miss ⟹ 换 step
// 之后 Go 分配字节数反而涨 (asc 8.2MB 档 920.4M → 922.7M)。池化之后这条路一分钱不花。
func TestStepAllString_MissZeroAlloc(t *testing.T) {
	re := MustCompile(`(zzz)=(qqq)`) // per=6, 扫不到
	s := ""
	for i := 0; i < 200; i++ {
		s += "key=val "
	}
	run := func() {
		re.StepAllStringSubmatchIndex(s, -1, func(flat []int32) bool { return true })
	}
	run()
	if got := testing.AllocsPerRun(50, run); got != 0 {
		t.Fatalf("miss 路径每次调用分配 %v 笔, 期望 0", got)
	}
}

// TestStepAllString_BufSize 批容量按 int32 数定死: 一块恒是 stepBufInts 个 int32, 一批装
// stepBufInts/per 处 —— 与命中数、正文长度都无关, 也不再有"首批小、装满长大"那套两段式。
func TestStepAllString_BufSize(t *testing.T) {
	p := stepBufPool.Get().(*[]int32)
	if len(*p) != stepBufInts || cap(*p) != stepBufInts {
		t.Fatalf("池块 = %d/%d int32, 期望恒为 %d", len(*p), cap(*p), stepBufInts)
	}
	stepBufPool.Put(p)

	// per=2 (无子组) ⟹ 一批 stepBufInts/2 处。喂 2 批多 5 处的命中 ⟹ 必然分成 3 批。
	perBatch := stepBufInts / 2
	hits := perBatch*2 + 5
	re := MustCompile(`a`)
	s := ""
	for i := 0; i < hits; i++ {
		s += "a"
	}
	batches, seen := 0, 0
	re.StepAllStringIndex(s, -1, func(flat []int32) bool {
		batches++
		n := len(flat) / 2
		if n > perBatch {
			t.Fatalf("一批给了 %d 处, 超过 %d", n, perBatch)
		}
		seen += n
		return true
	})
	if seen != hits {
		t.Fatalf("收到 %d 处, 期望 %d", seen, hits)
	}
	if batches != 3 {
		t.Fatalf("分了 %d 批, 期望 3 (一批 %d 处)", batches, perBatch)
	}
}
