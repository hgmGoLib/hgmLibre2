// zexp_step_alloc_test.go — 落选变体 B/D 与主线 (库内池) 的逐处对拍 (先证明它们等价, 再谈快慢)。
package hgmLibre2

import (
	"reflect"
	"testing"
)

// collectMain 用主线 API 收全部命中 —— 对拍的基准边。
func collectMain(re *Regexp, s string, n int, nmatch int) []int32 {
	var got []int32
	fn := func(f []int32) bool { got = append(got, f...); return true }
	if nmatch == 1 {
		re.StepAllStringIndex(s, n, fn)
	} else {
		re.StepAllStringSubmatchIndex(s, n, fn)
	}
	return got
}

func TestStepAllocVariants_Equiv(t *testing.T) {
	cases := []struct{ pat, body string }{
		{`(\w+)=(\w+)`, "a=1 bb=22 ccc=333 dddd=4444 " + "x=9 "},
		{`(\w+)=(\w+)`, "完全没有命中的一段正文"},
		// 🔴 原来这里有一条 {`a*`, "baaacaaad"} (空匹配去重路径)。全库拒空串之后它编不出来了。
		{`(a)(b)?`, "ab a ab a"},         // 未参与组 -1,-1
		{`\d+`, "1 22 333 4444 55555 666666 7777777 88 9 10 11 12 13 14 15 16 17 18 19 20"},
	}
	for _, c := range cases {
		re := MustCompile(c.pat)
		for _, nmatch := range []int{1, re.NumSubexp() + 1} {
			for _, n := range []int{-1, 3, 0, 1} {
				want := collectMain(re, c.body, n, nmatch)

				var gotB []int32
				fnB := func(f []int32) bool { gotB = append(gotB, f...); return true }
				if nmatch == 1 {
					re.StepAllStringIndexCAlloc(c.body, n, fnB)
				} else {
					re.StepAllStringSubmatchIndexCAlloc(c.body, n, fnB)
				}
				if !reflect.DeepEqual(want, gotB) {
					t.Fatalf("B(CAlloc) 不等价 pat=%q n=%d nmatch=%d\nwant=%v\ngot =%v", c.pat, n, nmatch, want, gotB)
				}

				if nmatch != 1 {
					var gotD []int32
					re.StepAllStringSubmatchIndexGoLocal(c.body, n, func(f []int32) bool { gotD = append(gotD, f...); return true })
					if !reflect.DeepEqual(want, gotD) {
						t.Fatalf("D(GoLocal) 不等价 pat=%q n=%d", c.pat, n)
					}
				}
			}
		}
	}
}

// 跨批是 B 唯一会静默出错的地方 —— 每批之后游标 (pos/prevEnd) 得接得上, C 缓冲还得留到扫完。
// 用一份 500 处命中的正文压这条路 (一批 stepBufInts/6=42 处 ⟹ 必然跨十几批)。
func TestStepAllocVariants_CrossBatch(t *testing.T) {
	re := MustCompile(`(\w+)=(\w+)`)
	body := stepBenchNHits(500)
	want := collectMain(re, body, -1, re.NumSubexp()+1)
	if len(want) != 500*6 {
		t.Fatalf("语料不对: %d", len(want))
	}
	var gotB []int32
	re.StepAllStringSubmatchIndexCAlloc(body, -1, func(f []int32) bool { gotB = append(gotB, f...); return true })
	if !reflect.DeepEqual(want, gotB) {
		t.Fatalf("B(CAlloc) 跨批不等价: len want=%d got=%d", len(want), len(gotB))
	}
	var gotD []int32
	re.StepAllStringSubmatchIndexGoLocal(body, -1, func(f []int32) bool { gotD = append(gotD, f...); return true })
	if !reflect.DeepEqual(want, gotD) {
		t.Fatalf("D(GoLocal) 跨批不等价")
	}
}

// 提前停: B 必须照样把 C 缓冲还掉 (否则就是泄漏)。这里只能验行为不崩 + 结果对;
// 真泄漏由 TestStepCAlloc_NoLeak 用 RSS 量。
func TestStepAllocVariants_EarlyStop(t *testing.T) {
	re := MustCompile(`(\w+)=(\w+)`)
	body := stepBenchNHits(500)
	cnt := 0
	re.StepAllStringSubmatchIndexCAlloc(body, -1, func(f []int32) bool { cnt += len(f) / 6; return false })
	if cnt == 0 || cnt > stepBufInts/6 {
		t.Fatalf("提前停应当只收到第一批(≤%d 处), 实收 %d", stepBufInts/6, cnt)
	}
}
