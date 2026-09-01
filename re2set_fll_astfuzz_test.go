package hgmLibre2

// re2set_fll_astfuzz_test.go —— Re2Set_fll_t × 【从 pattern 自己的 AST 生成的语料】×
// stdlib 的 re.Longest().FindAllStringIndex, 300 轮逐字节对拍。
//
// 🔴 语料必须从 AST 生成再拌噪声, 不能纯随机撒字节: 纯随机正文上这批 pattern 一处都撞不上,
//    那就是【空转绿】(同 TestRe2SetRrl_VsBrute 的红字)。生成器 msrGen 在
//    re2set_rrl_test.go。测试末尾那道 "nSpan < 1000 就 Fatal" 是这件事的哨兵。
//
// 🔴 oracle 必须是 Longest() 那个。stdlib 默认的 FindAll 是 leftmost-first (贪心),
//    两者在"同一起点上贪心先撞到的比最长的短"时给不同的右端, 拿默认那个对是【假红】。
//
// 🔴 2026-08-28 之前这里是 TestMatchScanPathsSameAsSetRoute: 拿一份【旧实现的复刻】
//    (msOldScanner: 补端点两趟都回整表 set.ResolveSpan) 当参照, 把路 A / 路 B 各对一遍。
//    A/B 都删了之后那份复刻没有了被参照的对象; 而且拿自家旧实现当 oracle 本来就弱一档 ——
//    换成 stdlib 之后这份语料反而钉得更死。同一份文件里那个 BenchmarkMatchScanOldVsNew
//    (新旧路子同一次运行里比价) 也跟着走了, 它比的是 2026-08-27 那次改动, 已经过时。

import (
	"fmt"
	"math/rand"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// msAstPats 是各种形状都覆盖到的一张小表: 定长 · 有上界变长 · 无上界变长 ·
// 长度不齐的交替 · 两头带 \b 的 · 多字节的 · 起始类窄于重复类的那一族。
var msAstPats = []string{
	`[A-Z]\d{3}`,
	`[a-f]{2,6}`,
	`q[0-9a-z]{3,}q`,
	`[\x{4e00}-\x{9fff}]{2,4}`,
	`w+x`,
	`ab|abcd`,
	`\b[A-F0-9]{8}\b`,
	`x[a-f]{1,4}y`,
	`[a-f]{2}-[a-f]{2,4}`,
	`\d{3}-\d{4}`,
	`[A-Za-z][A-Za-z0-9]{2,19}key`,
	`(?:ab)?[bc]{1,2}`,
	`x{1,3}[a-c]?(?:ab|cd)?`,
}

func TestRe2SetFllAstFuzzVsLongest(t *testing.T) {
	set, err := NewRegexpSet(msAstPats)
	if err != nil {
		t.Fatal(err)
	}
	asts := make([]*syntax.Regexp, len(msAstPats))
	std := make([]*regexp.Regexp, len(msAstPats))
	for i, pat := range msAstPats {
		a, err := syntax.Parse(pat, syntax.Perl)
		if err != nil {
			t.Fatalf("pat=%q 解析失败: %v", pat, err)
		}
		asts[i] = a.Simplify()
		std[i] = regexp.MustCompile(pat)
		std[i].Longest()
	}
	noiseR := []rune(" ,;:-_/@.\n\tabcdefkqwxyz019ABFKZ张三李")

	ms, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	rng := rand.New(rand.NewSource(20260827))
	nSpan := 0
	var st Re2Set_stats_t
	for round := 0; round < 300; round++ {
		var sb strings.Builder
		target := 60 + rng.Intn(400)
		for sb.Len() < target {
			if rng.Intn(3) == 0 {
				sb.WriteRune(noiseR[rng.Intn(len(noiseR))])
				continue
			}
			msrGen(asts[rng.Intn(len(asts))], rng, &sb, 0)
			sb.WriteRune(noiseR[rng.Intn(len(noiseR))])
		}
		text := sb.String()
		var got map[int32][]int32
		got, _, st = scanFlat(t, ms.Scan, text)
		for id := range msAstPats {
			var flat []int32
			for _, loc := range std[id].FindAllStringIndex(text, -1) {
				flat = append(flat, int32(loc[0]), int32(loc[1]))
			}
			nSpan += len(flat) / 2
			if fmt.Sprint(got[int32(id)]) != fmt.Sprint(flat) {
				t.Fatalf("轮 %d: #%d %q 岔开:\n  给的    %v\n  Longest %v\n正文 %q",
					round, id, msAstPats[id], got[int32(id)], flat, text)
			}
		}
	}
	if nSpan < 1000 {
		t.Fatalf("只对账了 %d 处区间 —— 语料没造对, 这是空转绿", nSpan)
	}
	t.Logf("300 轮 · 对账 %d 处区间, 与 Longest() 逐字节相同 (末轮账: walks=%d cands=%d tries=%d)",
		nSpan, st.Walks, st.Cands, st.Tries)
}
