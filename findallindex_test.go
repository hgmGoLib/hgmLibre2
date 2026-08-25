package hgmLibre2

// findallindex_test.go — FindAllIndex 的【语义正确性】: 与暴力参考实现逐位对拍。
//
// 参考实现故意写成 O(n²) 的最笨形态 (枚举每一对 (s,e), 用 stdlib 判 text[s:e] 是不是一个
// 完整匹配), 因为它与被测实现【没有任何共享逻辑】—— 用被测库自己的 FindAll 去对拍等于自证。
// 正文都很短 (<=64B), 笨算法跑得完。
//
// 对拍的口径 (见 findallindex.go 文件头):
//   正向 set: 游程展开后的位置集 == { e | 存在 s<=e 使 text[s:e] 完整匹配这条 pattern }
//   反向 set: 游程展开后的位置集 == { s | 存在 e>=s 使 text[s:e] 完整匹配这条 pattern }
// 注意这【不是】FindAllStringIndex 的集合 —— FindAll 只给 leftmost-first 的不重叠序列,
// 这里要的是全部端点 (重叠的也算), 那才是"命中在哪"这个问题的完整答案。

import (
	"regexp"
	"sort"
	"testing"
)

// spanScanPatterns 是对拍用的 pattern 表。刻意混进几类容易出错的形状:
//   · 变长重复      → 一条匹配会连出一串端点 (游程收敛的主战场)
//   · 前缀互相包含  → 同一位置多条 pattern 同时命中
//   · 交替 ab|c     → 两个独立匹配的端点【连号】, 只留一端就会悄悄丢一个
//   · 空匹配 a*     → 端点集合几乎覆盖全文, 且起点状态本身就是 match 状态
//   · 锚定 ^…/…$    → DeadState 早退路径
//   · \b 词边界     → 起点状态要带 flag
var spanScanPatterns = []string{
	"ab|c",
	"[a-z]{3,}",
	"a*",
	"abc",
	"abcd",
	"bc",
	"[0-9]+",
	`\bkey[0-9]+`,
	"^the",
	"end$",
	"x(?:yz)+",
	"[A-Z]{2}[0-9]{2}",
}

var spanScanInputs = []string{
	"",
	"a",
	"abc",
	"abcd",
	"abcabcabc",
	"the key123 and key4567 end",
	"xyzyzyz AB12 CD34 zzz",
	"0123456789",
	"no match here at all",
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"the quick brown fox jumps over the lazy dog end",
}

// bruteEnds / bruteStarts 是暴力参考实现。
//
// 关键在于【在原文的上下文里】判定 —— ^ $ \b 这些零宽断言看的是它在【整篇正文】里的位置,
// 拿 text[s:e] 单独去匹配会把子串的两头当成正文的两头, 判出一堆假命中 (第一版就栽在这)。
// 做法: 把前后缀按字面量钉死, 让 pat 只能吃掉正好那一段, 然后拿【整篇正文】去匹配:
//   "e 是不是某个匹配的右端" ⟺ (?s:.*)(?:pat)<后缀字面量> 能整篇匹配
//   "s 是不是某个匹配的左端" ⟺ <前缀字面量>(?:pat)(?s:.*) 能整篇匹配
// 前后缀是字面量 + 两头锚死 ⇒ 中间那段只可能是 text[s:e], 而零宽断言看到的位置是真实位置。
// 用的是 stdlib regexp, 与被测实现没有任何共享逻辑。
func bruteEnds(t *testing.T, pat, text string) []int32 {
	t.Helper()
	var out []int32
	for e := 0; e <= len(text); e++ {
		re := regexp.MustCompile(`\A(?s:.*)(?:` + pat + `)` + regexp.QuoteMeta(text[e:]) + `\z`)
		if re.MatchString(text) {
			out = append(out, int32(e))
		}
	}
	return out
}

func bruteStarts(t *testing.T, pat, text string) []int32 {
	t.Helper()
	var out []int32
	for s := 0; s <= len(text); s++ {
		re := regexp.MustCompile(`\A` + regexp.QuoteMeta(text[:s]) + `(?:` + pat + `)(?s:.*)\z`)
		if re.MatchString(text) {
			out = append(out, int32(s))
		}
	}
	return out
}

// setSpan_t 是自动测试收集游程用的三元组。库里【没有】这个类型 —— FindAllIndex 直接把三个数
// 交给回调, 不给数组 (为什么: findallindex.go 文件头"为什么给回调而不是给一个数组")。
// 但对拍代码要把整篇的游程攒起来跟暴力参考比, 所以在测试这侧留一个。
type setSpan_t struct{ Index, Lo, Hi int32 }

// appendAllIndex / appendAllIndexRev 是"我就要全部, 一次拿走"的测试版。
// batch <= 0 = 用库的默认批大小。
func appendAllIndex(s *RegexpSet, dst []setSpan_t, text string, batch int) ([]setSpan_t, error) {
	alloc, err := newAllocBatch(s, batch)
	if err != nil {
		return dst, err
	}
	defer alloc.Close()
	err = s.FindAllIndex(text, alloc, func(reIndex, endLo, endHi int32) {
		dst = append(dst, setSpan_t{reIndex, endLo, endHi})
	})
	return dst, err
}

func appendAllIndexRev(r *RegexpSetReverse, dst []setSpan_t, text string, batch int) ([]setSpan_t, error) {
	alloc, err := newAllocBatch(r.s, batch)
	if err != nil {
		return dst, err
	}
	defer alloc.Close()
	err = r.FindAllIndex(text, alloc, func(reIndex, startLo, startHi int32) {
		dst = append(dst, setSpan_t{reIndex, startLo, startHi})
	})
	return dst, err
}

func newAllocBatch(s *RegexpSet, batch int) (*RegexpSet_FindAllIndex_Alloc_t, error) {
	if batch <= 0 {
		batch = findAllIndexBatch
	}
	return newFindAllIndexAlloc(s, batch)
}

// expandSpans 把游程按 pattern 展开成逐个位置 (去重 + 升序), 用来跟暴力参考比。
func expandSpans(spans []setSpan_t, idx int32) []int32 {
	seen := map[int32]bool{}
	for _, sp := range spans {
		if sp.Index != idx {
			continue
		}
		for p := sp.Lo; p <= sp.Hi; p++ {
			seen[p] = true
		}
	}
	out := make([]int32, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

func sameInt32(a, b []int32) bool {
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

func TestSpanScan_ForwardMatchesBrute(t *testing.T) {
	set, err := NewRegexpSet(spanScanPatterns)
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	var buf []setSpan_t
	for _, text := range spanScanInputs {
		// batch 故意开小 (4), 逼出多次挂起/恢复。
		buf, err = appendAllIndex(set, buf[:0], text, 4)
		if err != nil {
			t.Fatalf("FindAllIndex(%q): %v", text, err)
		}
		for i, pat := range spanScanPatterns {
			got := expandSpans(buf, int32(i))
			want := bruteEnds(t, pat, text)
			if !sameInt32(got, want) {
				t.Errorf("正向 end 集不一致 pat=%q text=%q\n got=%v\nwant=%v", pat, text, got, want)
			}
		}
	}
}

func TestSpanScan_ReverseMatchesBrute(t *testing.T) {
	set, err := NewRegexpSetReverseMaxMem(spanScanPatterns, 0)
	if err != nil {
		t.Fatalf("NewRegexpSetReverseMaxMem: %v", err)
	}
	var buf []setSpan_t
	for _, text := range spanScanInputs {
		buf, err = appendAllIndexRev(set, buf[:0], text, 4)
		if err != nil {
			t.Fatalf("FindAllIndex(%q): %v", text, err)
		}
		for i, pat := range spanScanPatterns {
			got := expandSpans(buf, int32(i))
			want := bruteStarts(t, pat, text)
			if !sameInt32(got, want) {
				t.Errorf("反向 start 集不一致 pat=%q text=%q\n got=%v\nwant=%v", pat, text, got, want)
			}
		}
	}
}

// TestSpanScan_BatchSizeInvariant 钉死【挂起/恢复】没丢东西:
// batch 只决定"一批吐几条", 不该改变吐出来的内容。batch 开到最小时一遍正文里会挂起几十次,
// 每次都要把 DFA 状态按内容存下来 (放锁) 再按内容查回来 —— 这条路走错了这里立刻炸。
func TestSpanScan_BatchSizeInvariant(t *testing.T) {
	for _, rev := range []bool{false, true} {
		// 正反是两个类型, 所以这里收成一个闭包再往下走。
		var scan func(text string, batch int) ([]setSpan_t, error)
		if rev {
			r, err := NewRegexpSetReverseMaxMem(spanScanPatterns, 0)
			if err != nil {
				t.Fatalf("建反向 set: %v", err)
			}
			scan = func(text string, batch int) ([]setSpan_t, error) {
				return appendAllIndexRev(r, nil, text, batch)
			}
		} else {
			f, err := NewRegexpSet(spanScanPatterns)
			if err != nil {
				t.Fatalf("建正向 set: %v", err)
			}
			scan = func(text string, batch int) ([]setSpan_t, error) {
				return appendAllIndex(f, nil, text, batch)
			}
		}
		for _, text := range spanScanInputs {
			var ref []setSpan_t
			for _, batch := range []int{1, 2, 5, 17, 4096} {
				got, err := scan(text, batch)
				if err != nil {
					t.Fatalf("FindAllIndex: %v", err)
				}
				sortSpans(got)
				if ref == nil {
					ref = got
					continue
				}
				if len(got) != len(ref) {
					t.Fatalf("rev=%v text=%q batch=%d 游程条数变了: %d vs %d",
						rev, text, batch, len(got), len(ref))
				}
				for i := range got {
					if got[i] != ref[i] {
						t.Fatalf("rev=%v text=%q batch=%d 第 %d 条不一致: %+v vs %+v",
							rev, text, batch, i, got[i], ref[i])
					}
				}
			}
		}
	}
}

func sortSpans(s []setSpan_t) {
	sort.Slice(s, func(a, b int) bool {
		if s[a].Index != s[b].Index {
			return s[a].Index < s[b].Index
		}
		if s[a].Lo != s[b].Lo {
			return s[a].Lo < s[b].Lo
		}
		return s[a].Hi < s[b].Hi
	})
}

// TestSpanScan_AdjacentRunsNotCollapsed 是那条"收敛必须可逆"的反例本身:
// `ab|c` 撞上 "abc" 的两个 end 是 2 和 3 —— 连号。如果只吐游程的一端 (只留 3),
// [0,2) 这个匹配就没了, 而且不报错。所以必须两端都给。
func TestSpanScan_AdjacentRunsNotCollapsed(t *testing.T) {
	set, err := NewRegexpSet([]string{"ab|c"})
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	got, err := appendAllIndex(set, nil, "abc", 8)
	if err != nil {
		t.Fatalf("FindAllIndex: %v", err)
	}
	ends := expandSpans(got, 0)
	if !sameInt32(ends, []int32{2, 3}) {
		t.Fatalf("end 集 = %v, 想要 [2 3] (游程本身 = %+v)", ends, got)
	}
	// 顺带钉死: 这两个 end 确实是被收敛成【一条】游程吐出来的 (不是两条),
	// 也就是说收敛真的发生了 —— 否则这个测试测不到"收敛之后还能还原"。
	if len(got) != 1 || got[0].Lo != 2 || got[0].Hi != 3 {
		t.Fatalf("想要一条 (0,2,3) 的游程, 拿到 %+v", got)
	}
}

func TestSpanScan_EmptyAndNoMatch(t *testing.T) {
	set, err := NewRegexpSet([]string{"zzz", "[0-9]+"})
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	alloc, err := newAllocBatch(set, 8)
	if err != nil {
		t.Fatalf("NewFindAllIndexAlloc: %v", err)
	}
	defer alloc.Close()

	for _, text := range []string{"", "abc", "no digits and no z-z-z here"} {
		calls := 0
		if err := set.FindAllIndex(text, alloc, func(_, _, _ int32) { calls++ }); err != nil {
			t.Fatalf("FindAllIndex(%q): %v", text, err)
		}
		if calls != 0 {
			t.Errorf("text=%q 无命中却回调了 %d 次", text, calls)
		}
	}

	// 工作区复用: 上一次没命中不该污染下一次。
	var got []setSpan_t
	if err := set.FindAllIndex("abc 42 zzz", alloc, func(i, lo, hi int32) {
		got = append(got, setSpan_t{i, lo, hi})
	}); err != nil {
		t.Fatalf("FindAllIndex: %v", err)
	}
	if len(expandSpans(got, 0)) == 0 || len(expandSpans(got, 1)) == 0 {
		t.Fatalf("复用后两条都该命中, 拿到 %+v", got)
	}
}

// TestSpanScan_AllocReuse 钉死【同一个 alloc 反复扫】不串味: 挂起点上攥着一份 DFA 状态副本和
// 一张没收口的游程表, 每次 FindAllIndex 都必须先把它清干净, 否则上一篇正文的尾巴会漏到下一篇。
// batch 开到 1 是为了让每篇正文里都真的发生几十次挂起/恢复。
//
// (这里原来测的是"回调返 false 就地停"。FindAllIndex 的回调【没有】返回值 —— 想要"有没有命中"
//  就地收工的调用方用 MatchAny, 它在 RE2 那层打开 want_earliest_match, 比在 Go 这侧半途刹车
//  还早收工。少一个返回值, 就少一处调用方要想"我返 true 还是 false"的地方。)
func TestSpanScan_AllocReuse(t *testing.T) {
	set, err := NewRegexpSet(spanScanPatterns)
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	alloc, err := newAllocBatch(set, 1)
	if err != nil {
		t.Fatalf("NewFindAllIndexAlloc: %v", err)
	}
	defer alloc.Close()

	// 每篇正文各扫两遍 (一遍用复用的 alloc, 一遍用现开的), 逐条比。
	for round := 0; round < 2; round++ {
		for _, text := range spanScanInputs {
			var got []setSpan_t
			if err := set.FindAllIndex(text, alloc, func(i, lo, hi int32) {
				got = append(got, setSpan_t{i, lo, hi})
			}); err != nil {
				t.Fatalf("复用 alloc 扫 %q: %v", text, err)
			}
			ref, err := appendAllIndex(set, nil, text, 4096)
			if err != nil {
				t.Fatalf("参照扫 %q: %v", text, err)
			}
			sortSpans(got)
			sortSpans(ref)
			if len(got) != len(ref) {
				t.Fatalf("round=%d text=%q 游程条数不一致: %d vs %d", round, text, len(got), len(ref))
			}
			for i := range got {
				if got[i] != ref[i] {
					t.Fatalf("round=%d text=%q 第 %d 条不一致: %+v vs %+v", round, text, i, got[i], ref[i])
				}
			}
		}
	}
}

// TestFindAllIndex_AllocIsSetBound 钉死 alloc 不能串 set 用 —— 报错, 而不是给一个错答案。
func TestFindAllIndex_AllocIsSetBound(t *testing.T) {
	a, err := NewRegexpSet([]string{"abc", "[0-9]+"})
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	b, err := NewRegexpSet([]string{"abc", "[0-9]+"}) // 同样的表, 但是另一个对象
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	alloc, err := a.NewFindAllIndexAlloc()
	if err != nil {
		t.Fatalf("NewFindAllIndexAlloc: %v", err)
	}
	defer alloc.Close()
	if err := b.FindAllIndex("abc 42", alloc, func(_, _, _ int32) {}); err == nil {
		t.Fatal("拿 a 的 alloc 去扫 b 该报错")
	}
	// 反向 set 的 alloc 也不能拿到正向来用。
	rev, err := NewRegexpSetReverseMaxMem([]string{"abc", "[0-9]+"}, 0)
	if err != nil {
		t.Fatalf("NewRegexpSetReverseMaxMem: %v", err)
	}
	ralloc, err := rev.NewFindAllIndexAlloc()
	if err != nil {
		t.Fatalf("NewFindAllIndexAlloc(rev): %v", err)
	}
	defer ralloc.Close()
	if err := a.FindAllIndex("abc 42", ralloc, func(_, _, _ int32) {}); err == nil {
		t.Fatal("拿反向 set 的 alloc 去正向扫该报错")
	}

	// alloc 传 nil 是合法的 (当场建一个用完就扔)。
	n := 0
	if err := a.FindAllIndex("abc 42", nil, func(_, _, _ int32) { n++ }); err != nil {
		t.Fatalf("alloc=nil: %v", err)
	}
	if n == 0 {
		t.Fatal("alloc=nil 时一条都没吐")
	}

	// Close 过的 alloc 再用要报错, 不能悄悄跑。
	closed, err := a.NewFindAllIndexAlloc()
	if err != nil {
		t.Fatalf("NewFindAllIndexAlloc: %v", err)
	}
	closed.Close()
	closed.Close() // 可重复调
	if err := a.FindAllIndex("abc 42", closed, func(_, _, _ int32) {}); err == nil {
		t.Fatal("Close 过的 alloc 该报错")
	}
}
