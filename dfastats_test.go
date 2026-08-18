// dfastats_test.go — DFAStats 的两道门: 该动的时候真的动, 不该动的时候一次都不动。
//
// 这两条合起来才说明计数有用: 只验"会涨", 一个恒 +1 的假计数也过; 只验"不涨", 一个恒 0 的死
// 计数也过。所以【同一批 pattern · 同一批 body】只改预算, 跑两遍对照。
package hgmLibre2

import (
	"strconv"
	"strings"
	"testing"
)

// dfaStatsPatterns 造 n 条 `kwN [\s\S]{0,w} tgtN` 形状的 pattern。
//
// 为什么是这个形状: 它是 DFA 状态爆炸的经典构造 —— 扫到一个 kwN 之后, DFA 必须把"窗口内还有
// 哪几个 kw 悬着、各自还剩多少字节"全部编进状态里, 于是状态数随 (条数 × 窗口宽度) 组合爆炸。
// 内容检测类的规则表里这个形状很常见 (关键词骨架 + `\W{0,8}` 之类的容忍间隔), 不是造出来的极端。
//
// 反面教材 (试过, 不成立): 几百条共享同一长前缀的 pattern 撑不开状态集 —— 公共前缀在 DFA 里
// 就是一条路径, 区分度全在尾巴上, 状态数反而很少, 预算饿到编译下限都不会 flush。
func dfaStatsPatterns(n, w int) []string {
	pats := make([]string, 0, n)
	for i := 0; i < n; i++ {
		s := strconv.Itoa(i)
		pats = append(pats, `(?i)kw`+s+`[\s\S]{0,`+strconv.Itoa(w)+`}tgt`+s)
	}
	return pats
}

// dfaStatsBodies 造 n 份【互不相同】的 body: 随机 kw token + 随机字母噪声 (确定性 LCG, 不用
// math/rand 免得换 Go 版本换语料)。
//
// 互不相同是关键, 也是这套计数存在的理由: 同一份 body 扫 N 遍, 第一遍之后缓存全热就再不新建
// 状态 —— 那种形状下预算多小都量不到 thrash, 换预算也看不出吞吐差别。生产是每个请求一份新
// body, 每换一份就把状态集往外撑一次, 这才是会撞预算的那个形状。
func dfaStatsBodies(n, size, npat int) []string {
	seed := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return seed >> 33
	}
	bodies := make([]string, 0, n)
	for k := 0; k < n; k++ {
		var sb strings.Builder
		sb.Grow(size + 64)
		for sb.Len() < size {
			sb.WriteString("kw" + strconv.Itoa(int(next())%npat))
			for j := int(next())%10 + 1; j > 0; j-- {
				sb.WriteByte(byte('a' + next()%26))
			}
		}
		bodies = append(bodies, sb.String())
	}
	return bodies
}

// minViableSetMem 二分找【编得过的最小预算】。RE2 编译期会跑一次 DFA 冒烟搜索, 编得过说明状态
// 缓存至少放得下几个状态 —— 但也就那么多, 正是最容易 thrash 的那一档。
// 要二分不要折半: 折半只能得到 2× 粒度的答案, 而"刚好编得过"与"再多一倍"是完全不同的两档。
func minViableSetMem(t *testing.T, pats []string) int64 {
	t.Helper()
	hi := int64(64 << 20)
	if _, err := NewRegexpSetMaxMem(pats, hi); err != nil {
		t.Fatalf("%d 条 pattern 在 %dMB 都编不过 —— 测试前提垮了: %v", len(pats), hi>>20, err)
	}
	lo := int64(1) // 恒编不过的下界 (不能用 0: <=0 在本库是"走 RE2 默认 8MB"的意思)
	for lo+1024 < hi {
		mid := lo + (hi-lo)/2
		if _, err := NewRegexpSetMaxMem(pats, mid); err != nil {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// TestDFAStats_ThrashIsVisible — 饿着预算扫多形状语料, Resets 必须涨, 且 Last* 带回现场信息。
func TestDFAStats_ThrashIsVisible(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(16, 64<<10, len(pats))
	mem := minViableSetMem(t, pats)
	t.Logf("最小可编预算 = %d bytes (%.2f MB)", mem, float64(mem)/(1<<20))

	set, err := NewRegexpSetMaxMem(pats, mem)
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	buf := make([]int32, set.GetPatternLen())
	DFAStatsZero()
	for _, b := range bodies {
		set.Match(b, buf)
	}
	st := DFAStats()
	t.Logf("starved: %+v", st)
	if st.Resets == 0 {
		t.Fatalf("预算饿到 %d bytes 扫 %d 份互不相同的 body 都没有一次 ResetCache —— "+
			"要么钩子没装上, 要么语料撑不开状态集", mem, len(bodies))
	}
	if st.LastStateBudget <= 0 || st.LastCacheStates <= 0 {
		t.Fatalf("Resets=%d 但现场信息没带回来: LastStateBudget=%d LastCacheStates=%d",
			st.Resets, st.LastStateBudget, st.LastCacheStates)
	}
}

// TestDFAStats_QuietWhenBudgetFits — 同一批 pattern/body, 预算给宽以后一次都不许涨。
// 这才是"预算够了"的定义: 不是编得过, 是扫的时候不再清缓存。
func TestDFAStats_QuietWhenBudgetFits(t *testing.T) {
	pats := dfaStatsPatterns(60, 8)
	bodies := dfaStatsBodies(16, 64<<10, len(pats))

	set, err := NewRegexpSetMaxMem(pats, 64<<20)
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	buf := make([]int32, set.GetPatternLen())
	for _, b := range bodies { // 热身: 先把这批语料的状态集走出来
		set.Match(b, buf)
	}
	DFAStatsZero()
	for _, b := range bodies {
		set.Match(b, buf)
	}
	if st := DFAStats(); st.Resets != 0 {
		t.Fatalf("64MB 预算下热身后仍有 %d 次 ResetCache: %+v", st.Resets, st)
	}
}

// TestDFAStats_ZeroClears — 归零是真归零 (分段测量靠它)。
func TestDFAStats_ZeroClears(t *testing.T) {
	DFAStatsZero()
	if st := DFAStats(); st != (DFAStats_t{}) {
		t.Fatalf("DFAStatsZero 之后不是全零: %+v", st)
	}
}
