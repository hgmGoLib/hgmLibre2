// match_step_test.go — StepAllStringSubmatchIndex / StepAllStringIndex 的对拍门。
//
// 照 find_all_flat_test.go 那四条办, 另加两条 step 形态特有的:
//   ① 与本库 FindAllStringSubmatchIndex 逐处逐组对拍 (含未参与组的 -1,-1);
//   ② 与 stdlib regexp 对拍 (空匹配 / (?m)^$ / 嵌套组 / n 截断);
//   ③ 缓冲复用不串味 (同一 MatchStep_t 跨 re / 跨正文反复用);
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
func stepCollectSub(re *Regexp, st *MatchStep_t, s string, n int) [][]int {
	per := 2 * (re.NumSubexp() + 1)
	var got [][]int
	re.StepAllStringSubmatchIndex(st, s, n, func(flat []int32) bool {
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
			for _, batch := range []int{1, 2, 3, 0} { // 0 = 默认策略 (首批 8, 满了长到 128)
				st := &MatchStep_t{}
				if batch > 0 {
					st = newMatchStepFixed(batch)
				}
				got := stepCollectSub(re, st, s, -1)
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
				st := &MatchStep_t{}
				if batch > 0 {
					st = newMatchStepFixed(batch)
				}
				got := stepCollectSub(re, st, s, -1)
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
					st := &MatchStep_t{}
					if batch > 0 {
						st = newMatchStepFixed(batch)
					}
					got := stepCollectSub(re, st, s, n)
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
				st := &MatchStep_t{}
				if batch > 0 {
					st = newMatchStepFixed(batch)
				}
				var got [][]int
				re.StepAllStringIndex(st, s, -1, func(flat []int32) bool {
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
	st := &MatchStep_t{}
	calls := 0
	re.StepAllStringSubmatchIndex(st, "abcabcabc", -1, func(flat []int32) bool { calls++; return false })
	if calls != 0 {
		t.Fatalf("无匹配却调了 batchFn %d 次", calls)
	}
	re.StepAllStringIndex(st, "abcabcabc", -1, func(flat []int32) bool { calls++; return false })
	if calls != 0 {
		t.Fatalf("无匹配却调了 batchFn %d 次 (Index)", calls)
	}
}

// TestStepAllString_EarlyStop 条⑥ 提前停: batchFn 返回 false 之后不得再有回调。
func TestStepAllString_EarlyStop(t *testing.T) {
	re := MustCompile(`a`)
	s := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 32 处, 批容量 2 ⇒ 至少 16 批
	st := newMatchStepFixed(2)
	calls, seen := 0, 0
	re.StepAllStringSubmatchIndex(st, s, -1, func(flat []int32) bool {
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

// TestStepAllString_ReuseNoBleed 条③ 复用不串味: 同一个 MatchStep_t 跨 re (per 不同) 跨正文
// 反复用, 每次结果都要与一次性 API 相同。
func TestStepAllString_ReuseNoBleed(t *testing.T) {
	st := &MatchStep_t{} // 一个工作区打全场
	for round := 0; round < 3; round++ {
		for _, pat := range stepPatterns {
			re := MustCompile(pat)
			for _, s := range stepInputs {
				want := re.FindAllStringSubmatchIndex(s, -1)
				got := stepCollectSub(re, st, s, -1)
				if !eqLocs(want, got) {
					t.Fatalf("round=%d pat=%q s=%q:\n want %v\n got  %v", round, pat, s, want, got)
				}
			}
		}
	}
}

// TestStepAllString_SteadyZeroAlloc 条④ 稳态零分配: 缓冲长到位之后, 再调不得有任何 Go 堆分配。
func TestStepAllString_SteadyZeroAlloc(t *testing.T) {
	re := MustCompile(`(\w+)=(\w+)`)
	s := ""
	for i := 0; i < 200; i++ {
		s += "key=val "
	}
	st := &MatchStep_t{}
	var sink int
	run := func() {
		re.StepAllStringSubmatchIndex(st, s, -1, func(flat []int32) bool {
			sink += len(flat)
			return true
		})
	}
	run() // 预热: 让缓冲长到大批
	if got := testing.AllocsPerRun(50, run); got != 0 {
		t.Fatalf("稳态每次调用分配 %v 笔, 期望 0", got)
	}
	if sink == 0 {
		t.Fatal("没扫到任何命中, 这个测试没测到东西")
	}
}

// TestStepAllString_BufferGrowth 批容量增长策略: 首批 stepBatchFirst 处, 被填满之后长到
// stepBatchMatches 处, 之后不再变; miss 的调用不该把缓冲撑大。
func TestStepAllString_BufferGrowth(t *testing.T) {
	re := MustCompile(`a`) // per = 2
	st := &MatchStep_t{}
	re.StepAllStringSubmatchIndex(st, "bbbb", -1, func(flat []int32) bool { return true }) // miss
	if cap(st.buf) != stepBatchFirst*2 {
		t.Fatalf("miss 之后缓冲 = %d int32, 期望首批 %d", cap(st.buf), stepBatchFirst*2)
	}
	few := "a b a" // 2 处, 装不满首批
	re.StepAllStringSubmatchIndex(st, few, -1, func(flat []int32) bool { return true })
	if cap(st.buf) != stepBatchFirst*2 {
		t.Fatalf("少量命中之后缓冲 = %d int32, 期望仍是首批 %d", cap(st.buf), stepBatchFirst*2)
	}
	many := ""
	for i := 0; i < 50; i++ { // 50 处 > stepBatchFirst, 必定填满首批
		many += "a"
	}
	re.StepAllStringSubmatchIndex(st, many, -1, func(flat []int32) bool { return true })
	if cap(st.buf) != stepBatchMatches*2 {
		t.Fatalf("多命中之后缓冲 = %d int32, 期望长到大批 %d", cap(st.buf), stepBatchMatches*2)
	}
}
