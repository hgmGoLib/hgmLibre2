package hgmLibre2

// regexpset_test.go — RegexpSet.Match 与「逐条 MatchString」对拍: 命中 index 集合必须一致。

import (
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRegexpSet_EquivPerPattern(t *testing.T) {
	// 一组有重叠关键词 / 交替 / 大小写的 pattern —— 形状(共享词、交替、(?i))照着真实规则表捏,
	// 词汇本身刻意用中性的, 免得测试文件里夹带任何具体业务规则。
	patterns := []string{
		`(?i)cancel\s+(all\s+)?(pending|prior|earlier)\s+orders?`,
		`(?i)revoke\s+(all\s+)?(pending|prior)\s+(orders?|holds?)`,
		`(?i)you\s+are\s+now\s+(an?\s+)?(admin|operator)`,
		`(?i)maintenance\s+mode`,
		`(?i)system\s*:\s*reset`,
		`[0-9]{3,}`,
		`(cat|dog|fish)`,
	}
	set, err := NewRegexpSet(patterns)
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	compiled := make([]*Regexp, len(patterns))
	for i, p := range patterns {
		compiled[i] = MustCompile(p)
	}

	inputs := []string{
		"",
		"please CANCEL all pending orders and do X",
		"revoke prior holds",
		"you are now an admin account",
		"enter maintenance mode now",
		"system: reset everything",
		"benign sentence with a cat and 12345",
		"no triggers here at all",
		"REVOKE ALL PENDING ORDERS. maintenance mode. 9999", // 多条同时命中
		"the dog cancelled pending plans",                   // 含关键词但结构不符 → 不该命中 cancel 那条
	}

	var buf []int32
	for _, in := range inputs {
		// 期望集 = 逐条 MatchString 命中的 index。
		var want []int
		for i, re := range compiled {
			if re.MatchString(in) {
				want = append(want, i)
			}
		}
		got32 := set.Match(in, buf)
		buf = got32 // 复用
		got := make([]int, len(got32))
		for i, v := range got32 {
			got[i] = int(v)
		}
		sort.Ints(got)
		sort.Ints(want)
		if !sameIntSlice(got, want) {
			t.Errorf("Match 集合不一致 in=%q\n  got =%v\n  want=%v", in, got, want)
		}
	}
}

// TestRegexpSetMaxMem — maxMem 旋钮: 同一张表在小预算下 Compile 失败(有可诊断的 error),
// 预算调大后装得下, 且命中结果与逐条 MatchString 一致(调大预算不改语义, 只改容量)。
// 语料用 (?s)a[\s\S]{80..95}b: 编译期程序体积可控地大, 64KB 必然装不下、8MB 必然装得下。
func TestRegexpSetMaxMem(t *testing.T) {
	var patterns []string
	for n := 80; n <= 95; n++ {
		patterns = append(patterns, `(?s)a[\s\S]{`+strconv.Itoa(n)+`}b`)
	}
	_, err := NewRegexpSetMaxMem(patterns, 64<<10)
	if err == nil {
		t.Fatal("64KB 预算装不下这 16 条, 期望 error")
	}
	// error 必须把"该调哪个旋钮"说清楚, 否则调用方只看见 out of memory 会去拆表。
	if !strings.Contains(err.Error(), "maxMem=65536") || !strings.Contains(err.Error(), "NewRegexpSetMaxMem") {
		t.Errorf("compile 失败的 error 缺少可诊断信息: %v", err)
	}

	set, err := NewRegexpSetMaxMem(patterns, 8<<20)
	if err != nil {
		t.Fatalf("8MB 预算应装得下: %v", err)
	}
	if set.Size() != len(patterns) {
		t.Fatalf("Size=%d want %d", set.Size(), len(patterns))
	}
	compiled := make([]*Regexp, len(patterns))
	for i, p := range patterns {
		compiled[i] = MustCompile(p)
	}
	inputs := []string{
		"",
		"a" + strings.Repeat("x", 80) + "b",  // 只命中 N=80 那条
		"a" + strings.Repeat("x", 95) + "b",  // 只命中 N=95 那条
		"a" + strings.Repeat("x", 200) + "b", // 长跑: 每条都能在某处对齐 → 全命中
		strings.Repeat("nope ", 100),
	}
	var buf []int32
	for _, in := range inputs {
		var want []int
		for i, re := range compiled {
			if re.MatchString(in) {
				want = append(want, i)
			}
		}
		got32 := set.Match(in, buf)
		buf = got32
		got := make([]int, len(got32))
		for i, v := range got32 {
			got[i] = int(v)
		}
		sort.Ints(got)
		if !sameIntSlice(got, want) {
			t.Errorf("Match 集合不一致 len(in)=%d\n  got =%v\n  want=%v", len(in), got, want)
		}
	}
}

func TestRegexpSet_BadPattern(t *testing.T) {
	if _, err := NewRegexpSet([]string{`ok`, `(unclosed`}); err == nil {
		t.Error("非法 pattern 应返回 error")
	}
}

func TestRegexpSet_Empty(t *testing.T) {
	set, err := NewRegexpSet(nil)
	if err != nil {
		t.Fatalf("空集合 New: %v", err)
	}
	if got := set.Match("anything", nil); len(got) != 0 {
		t.Errorf("空集合应无命中, got %v", got)
	}
}
