package hgmLibre2

// spanscan_test.go — 流式游程扫描的【语义正确性】: 与暴力参考实现逐位对拍。
//
// 参考实现故意写成 O(n²) 的最笨形态 (枚举每一对 (s,e), 用 stdlib 判 text[s:e] 是不是一个
// 完整匹配), 因为它与被测实现【没有任何共享逻辑】—— 用被测库自己的 FindAll 去对拍等于自证。
// 正文都很短 (<=64B), 笨算法跑得完。
//
// 对拍的口径 (见 spanscan.go 文件头):
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

// expandSpans 把游程按 pattern 展开成逐个位置 (去重 + 升序), 用来跟暴力参考比。
func expandSpans(spans []SetSpan, idx int32) []int32 {
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
	sc, err := set.NewSpanScanner(4) // 故意开小, 逼出多次挂起/恢复
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc.Close()

	var buf []SetSpan
	for _, text := range spanScanInputs {
		buf, err = sc.AppendSpans(buf[:0], text)
		if err != nil {
			t.Fatalf("Scan(%q): %v", text, err)
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
	sc, err := set.NewSpanScanner(4)
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc.Close()

	var buf []SetSpan
	for _, text := range spanScanInputs {
		buf, err = sc.AppendSpans(buf[:0], text)
		if err != nil {
			t.Fatalf("Scan(%q): %v", text, err)
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
		var set *RegexpSet
		var err error
		if rev {
			set, err = NewRegexpSetReverseMaxMem(spanScanPatterns, 0)
		} else {
			set, err = NewRegexpSet(spanScanPatterns)
		}
		if err != nil {
			t.Fatalf("建 set (rev=%v): %v", rev, err)
		}
		for _, text := range spanScanInputs {
			var ref []SetSpan
			for _, batch := range []int{1, 2, 5, 17, 4096} {
				sc, err := set.NewSpanScanner(batch)
				if err != nil {
					t.Fatalf("NewSpanScanner(%d): %v", batch, err)
				}
				got, err := sc.AppendSpans(nil, text)
				sc.Close()
				if err != nil {
					t.Fatalf("Scan: %v", err)
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

func sortSpans(s []SetSpan) {
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
	sc, err := set.NewSpanScanner(8)
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc.Close()
	got, err := sc.AppendSpans(nil, "abc")
	if err != nil {
		t.Fatalf("Scan: %v", err)
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
	sc, err := set.NewSpanScanner(8)
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc.Close()

	for _, text := range []string{"", "abc", "no digits and no z-z-z here"} {
		calls := 0
		if err := sc.Scan(text, func(spans []SetSpan) bool { calls++; return true }); err != nil {
			t.Fatalf("Scan(%q): %v", text, err)
		}
		if calls != 0 {
			t.Errorf("text=%q 无命中却回调了 %d 次", text, calls)
		}
	}

	// 工作区复用: 上一次没命中不该污染下一次。
	got, err := sc.AppendSpans(nil, "abc 42 zzz")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(expandSpans(got, 0)) == 0 || len(expandSpans(got, 1)) == 0 {
		t.Fatalf("复用后两条都该命中, 拿到 %+v", got)
	}
}

// TestSpanScan_EarlyStop 钉死"回调返 false 就地停", 且【停在半截的工作区能重新用】——
// 挂起点上还攥着一份 DFA 状态副本和一张没收口的游程表, 下一次 Scan 必须先清干净。
func TestSpanScan_EarlyStop(t *testing.T) {
	set, err := NewRegexpSet(spanScanPatterns)
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	sc, err := set.NewSpanScanner(1)
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc.Close()

	text := "the quick brown fox jumps over the lazy dog end"
	calls := 0
	if err := sc.Scan(text, func(spans []SetSpan) bool { calls++; return false }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if calls != 1 {
		t.Fatalf("返 false 之后还被调了, calls=%d", calls)
	}

	full, err := sc.AppendSpans(nil, text)
	if err != nil {
		t.Fatalf("重扫: %v", err)
	}
	sortSpans(full)
	sc2, err := set.NewSpanScanner(4096)
	if err != nil {
		t.Fatalf("NewSpanScanner: %v", err)
	}
	defer sc2.Close()
	ref, err := sc2.AppendSpans(nil, text)
	if err != nil {
		t.Fatalf("参照扫: %v", err)
	}
	sortSpans(ref)
	if len(full) != len(ref) {
		t.Fatalf("半途而废之后重扫结果不对: %d 条 vs %d 条", len(full), len(ref))
	}
	for i := range full {
		if full[i] != ref[i] {
			t.Fatalf("半途而废之后重扫第 %d 条不一致: %+v vs %+v", i, full[i], ref[i])
		}
	}
}
