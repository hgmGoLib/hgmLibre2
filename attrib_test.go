package hgmLibre2

import (
	"strconv"
	"strings"
	"testing"
)

// 造 thrash 的形状照抄 scanstats_test.go 的教训: 必须是【容差间隙】(kw{0,W}tgt),
// 随机噪声和共享长前缀都造不出状态爆炸 (前者从不进窗口, 后者被并成一条路径)。
func attribPats(n, width int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "kw"+string(rune('A'+i%26))+string(rune('a'+i/26))+
			`[\s\S]{0,`+strconv.Itoa(width)+`}tgt`+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	return out
}

func attribBody(pats []string) string {
	var sb strings.Builder
	for i := range pats {
		sb.WriteString("kw" + string(rune('A'+i%26)) + string(rune('a'+i/26)))
		sb.WriteString(strings.Repeat("x", 40))
	}
	return sb.String()
}

// 归因编译不进默认构建, 所以没开宏时唯一该保证的是"API 还在、不 panic、明说没开"。
func TestAttrib_DisabledIsHonest(t *testing.T) {
	pats := attribPats(8, 20)
	set, err := NewRegexpSetMaxMem(pats, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]int32, set.GetPatternLen())
	set.Match(attribBody(pats), buf)

	a := set.Attrib()
	if !a.Enabled {
		if len(a.Pats) != 0 || a.StatesTotal != 0 {
			t.Fatalf("没开 RE2_DFA_ATTRIB 时不该有数据: %+v", a)
		}
		t.Skip("未开 RE2_DFA_ATTRIB 编译 —— 下面的断言用 CGO_CXXFLAGS=-DRE2_DFA_ATTRIB=1 跑")
	}
}

// 开了宏才跑: 归因必须自洽, 且必须真的把"贵的那条"排到前面。
func TestAttrib_RanksTheExpensivePattern(t *testing.T) {
	// 20 条窄窗口 + 1 条宽窗口。宽的那条 (下标 20) 该排第一。
	pats := attribPats(20, 2)
	pats = append(pats, `kwZz[\s\S]{0,200}tgtZz`)
	set, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatal(err)
	}

	body := attribBody(pats[:20]) + "kwZz" + strings.Repeat("y", 300)
	buf := make([]int32, set.GetPatternLen())
	set.Match(body, buf)

	a := set.Attrib()
	if !a.Enabled {
		t.Skip("未开 RE2_DFA_ATTRIB 编译")
	}
	if !a.Built || a.StatesTotal <= 0 {
		t.Fatalf("扫过之后必须有状态: %+v", a)
	}
	if len(a.Pats) == 0 {
		t.Fatal("一条都没归因到 —— inst_owner_ 全是 shared?")
	}
	// 自洽: 每条的 States 不能超过总状态数; Insts >= States (每状态至少一个零件)
	for _, p := range a.Pats {
		if p.States > a.StatesTotal {
			t.Fatalf("#%d States=%d > 总数 %d", p.Index, p.States, a.StatesTotal)
		}
		if p.Insts < p.States {
			t.Fatalf("#%d Insts=%d < States=%d", p.Index, p.Insts, p.States)
		}
	}
	// 降序 (排序键是 Excess, 不是 States —— States 在非锚定搜索下会饱和, 见 PatternCost 注释)
	for i := 1; i < len(a.Pats); i++ {
		if a.Pats[i-1].Excess < a.Pats[i].Excess {
			t.Fatal("Pats 没有按 Excess 降序")
		}
	}
	// 宽窗口那条 (下标 20) 必须排进前三 —— 这是这个功能存在的理由
	rank := -1
	for i, p := range a.Pats {
		if p.Index == 20 {
			rank = i
		}
	}
	if rank < 0 || rank > 2 {
		t.Fatalf("宽窗口那条 (#20) 排在第 %d 名, 归因没起作用: %+v", rank, a.Pats)
	}
	// 宽度直方图与 birth 直方图都得有内容
	var nh, bh int64
	for _, v := range a.NInstHist {
		nh += v
	}
	for _, v := range a.BirthHist {
		bh += v
	}
	if nh != a.StatesTotal || bh != a.StatesTotal {
		t.Fatalf("直方图总数对不上: ninst=%d birth=%d states=%d", nh, bh, a.StatesTotal)
	}
	if a.NInstMax <= 0 || a.NInstSum < a.StatesTotal {
		t.Fatalf("状态宽度不合理: max=%d sum=%d states=%d", a.NInstMax, a.NInstSum, a.StatesTotal)
	}
}

// 归因不能改变任何行为: 同一批 pattern/语料, 开不开宏命中集必须一致。
// (这里只能验"开着的时候命中集仍然对"; 开/关两种构建的命中集逐位对拍在库外的测台上做。)
func TestAttrib_SameMatchesAsPlain(t *testing.T) {
	pats := attribPats(12, 30)
	set, err := NewRegexpSetMaxMem(pats, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	body := attribBody(pats) + "tgtAa"
	buf := make([]int32, set.GetPatternLen())
	got := append([]int32(nil), set.Match(body, buf)...)

	// 逐条单独跑一遍作对照
	var want []int32
	for i, p := range pats {
		re, err := Compile(p)
		if err != nil {
			t.Fatal(err)
		}
		if re.MatchString(body) {
			want = append(want, int32(i))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("命中集不一致: Set=%v 逐条=%v", got, want)
	}
	seen := map[int32]bool{}
	for _, v := range got {
		seen[v] = true
	}
	for _, v := range want {
		if !seen[v] {
			t.Fatalf("Set 漏了 #%d: Set=%v 逐条=%v", v, got, want)
		}
	}
}
