// arena_equiv_test.go — 饿着预算扫, 命中集必须仍与【逐条 Compile】对拍一致。
//
// 这道门盯的是本库对 re2_dfa.cc 的改动 (转移表槽位 8 字节 State* → 4 字节 arena 下标 +
// arena 按需增长)。这两件事只在【预算不宽裕】那一档才真正发生: flush 要重建整张表, arena
// 搬家要把 state_cache_ 的键 / 每个 State 的 inst_ / start_[] 全部重定位 —— 错一处就是
// 悄悄漏命中, 而漏命中在 API 上没有任何信号 (Match 不返回 error)。
//
// 🔴 为什么 ground truth 必须是逐条 Compile: 拿 Match 跟 MatchStats 对拍验不出这类错 —— 两条
// 路走的是同一张 kManyMatch DFA, 一起错就一起错。单条 Regexp 走的是另一张 DFA (kFirstMatch,
// 且能退回 NFA/OnePass), 所以它是独立的一票。
//
// 与 regexpset_test.go 的分工: 那边是【宽预算 · 小表】验语义 (空匹配/大小写/交替),
// 这边是【饿预算 · 大表 · 多份互不相同的正文】验存储层改动。
package hgmLibre2

import (
	"strconv"
	"strings"
	"sync"
	"testing"
)

// arenaPats 造 n 条 `kwI[\s\S]{0,w}tgtI` —— 与 dfaStatsPatterns 同形状 (容差窗口是撑爆状态集
// 的唯一有效形状), 区别是这里的语料真的会命中, 因为对拍需要非空的命中集。
func arenaPats(n, w int) []string {
	pats := make([]string, 0, n)
	for i := 0; i < n; i++ {
		s := strconv.Itoa(i)
		pats = append(pats, `(?i)kw`+s+`[\s\S]{0,`+strconv.Itoa(w)+`}tgt`+s)
	}
	return pats
}

// arenaBodies 造 n 份互不相同的正文, 每份里既有【窗口内】的 kwI…tgtI (必命中), 也有
// 【差一个字符出窗】的 kwJ…tgtJ (必不命中)。后者是关键: 只放命中样本的话, 一个"全都报命中"
// 的坏实现照样过门。
func arenaBodies(n, size, npat, w int) []string {
	seed := uint64(0x243f6a8885a308d3)
	next := func() uint64 {
		seed = seed*6364136223846793005 + 1442695040888963407
		return seed >> 33
	}
	noise := func(sb *strings.Builder, k int) {
		for ; k > 0; k-- {
			sb.WriteByte(byte('a' + next()%26))
		}
	}
	bodies := make([]string, 0, n)
	for b := 0; b < n; b++ {
		var sb strings.Builder
		sb.Grow(size + 64)
		for sb.Len() < size {
			i := int(next()) % npat
			s := strconv.Itoa(i)
			sb.WriteString("kw" + s)
			if next()%2 == 0 {
				noise(&sb, int(next())%(w+1)) // 窗口内 ⇒ 命中
			} else {
				noise(&sb, w+1+int(next())%8) // 出窗一点点 ⇒ 不命中 (除非别处凑巧凑上)
			}
			sb.WriteString("tgt" + s)
			noise(&sb, int(next())%12+1)
		}
		bodies = append(bodies, sb.String())
	}
	return bodies
}

// truthFor 逐条 Compile 跑一遍, 返回每份正文的命中集 (下标 → bool)。
func truthFor(t *testing.T, pats, bodies []string) []map[int32]bool {
	t.Helper()
	out := make([]map[int32]bool, len(bodies))
	for i := range out {
		out[i] = map[int32]bool{}
	}
	for p, pat := range pats {
		re, err := Compile(pat)
		if err != nil {
			t.Fatalf("第 %d 条 pattern 编不过: %v", p, err)
		}
		for i, body := range bodies {
			if re.MatchString(body) {
				out[i][int32(p)] = true
			}
		}
		re.FreeC()
	}
	return out
}

// sameHits 把 Set 的命中切片与 ground truth 比对, 不一致时返回人话。
func sameHits(got []int32, want map[int32]bool) string {
	seen := make(map[int32]bool, len(got))
	for _, v := range got {
		if seen[v] {
			return "命中集里有重复下标 " + strconv.Itoa(int(v))
		}
		seen[v] = true
		if !want[v] {
			return "多报了 #" + strconv.Itoa(int(v))
		}
	}
	for v := range want {
		if !seen[v] {
			return "漏报了 #" + strconv.Itoa(int(v))
		}
	}
	return ""
}

// TestSetHitsMatchTruthUnderStarvedBudget — 饿着预算 (必 flush, 默认构建下 arena 也必搬家),
// 每一份正文的命中集都要与逐条 Compile 完全一致。
func TestSetHitsMatchTruthUnderStarvedBudget(t *testing.T) {
	const w = 8
	pats := arenaPats(60, w)
	bodies := arenaBodies(16, 64<<10, len(pats), w)
	truth := truthFor(t, pats, bodies)

	var nonEmpty int
	for _, m := range truth {
		if len(m) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty < len(bodies) {
		t.Fatalf("前提垮了: %d/%d 份正文一条都命不中, 这道门就成了空跑", len(bodies)-nonEmpty, len(bodies))
	}

	mem := minViableSetMem(t, pats)
	set, err := NewRegexpSetMaxMem(pats, mem)
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	buf := make([]int32, set.Size())
	var st ScanStats
	var flushes, grows, built int64
	for round := 0; round < 2; round++ { // 第二轮: flush 之后重建出来的状态照样得对
		for i, body := range bodies {
			hits := set.MatchStats(body, buf, &st)
			flushes += st.Flushes
			grows += st.Grows
			built += st.StatesBuilt
			if bad := sameHits(hits, truth[i]); bad != "" {
				t.Fatalf("第 %d 轮第 %d 份正文命中集与逐条对拍不符 (%s): got=%v want=%v 本次 %+v",
					round, i, bad, hits, truth[i], st)
			}
		}
	}
	t.Logf("预算 %d bytes: flush %d 次, arena 扩容 %d 次, 建状态 %d 个", mem, flushes, grows, built)
	if flushes == 0 {
		t.Fatalf("饿到 %d bytes 扫 %d 份互不相同的正文都没 flush —— 这道门没走到它要盯的那条路",
			mem, len(bodies))
	}
	if grows == 0 {
		// 原版编码 (-DRE2_DFA_NEXT_BITS=64 -DRE2_DFA_ARENA=0) 没有 arena, 恒 0, 不是错。
		t.Logf("Grows=0: 要么是原版编码的对照构建, 要么这批语料没撑到 arena 扩容")
	}
}

// TestSetHitsMatchTruthConcurrent — 同一个饿预算的 Set 多协程一起扫, 命中集仍要逐份对得上。
// flush / arena 搬家都要拿写锁并把别人手里的 State* 作废, 并发才是这条路真正的考场;
// 只在单线程下验过等于没验。
func TestSetHitsMatchTruthConcurrent(t *testing.T) {
	const w = 8
	pats := arenaPats(48, w)
	bodies := arenaBodies(12, 32<<10, len(pats), w)
	truth := truthFor(t, pats, bodies)

	set, err := NewRegexpSetMaxMem(pats, minViableSetMem(t, pats))
	if err != nil {
		t.Fatalf("建集失败: %v", err)
	}
	const nw = 8
	var wg sync.WaitGroup
	fail := make([]string, nw)
	for k := 0; k < nw; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			buf := make([]int32, set.Size())
			var st ScanStats
			for round := 0; round < 3; round++ {
				for i := range bodies {
					j := (i + k) % len(bodies) // 错开起点, 让各协程踩在不同的正文上
					hits := set.MatchStats(bodies[j], buf, &st)
					if bad := sameHits(hits, truth[j]); bad != "" && fail[k] == "" {
						fail[k] = "协程 " + strconv.Itoa(k) + " 第 " + strconv.Itoa(round) +
							" 轮第 " + strconv.Itoa(j) + " 份: " + bad
					}
				}
			}
		}(k)
	}
	wg.Wait()
	for _, f := range fail {
		if f != "" {
			t.Fatalf("并发下命中集与逐条对拍不符: %s (flush 或 arena 搬家把别人手里的状态弄丢了)", f)
		}
	}
	t.Logf("并发 %d 协程 × 3 轮 × %d 份正文全部对上; %+v", nw, len(bodies), set.MemInfo())
}
