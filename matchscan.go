// matchscan.go —— 一遍扫正文, 之后【按需】给出某条 pattern 的不重复命中区间。
//
// ── 它替掉的是哪段路 ────────────────────────────────────────────────────────
// 调用方今天的写法是"两段式": 先 Set.Match 扫一遍拿到"哪几条命中"(一张 bool 表), 然后为了知道
// 【命中在哪】, 把这几条各自的 Regexp 拿出来对整篇正文再跑一遍 FindAllStringIndex。命中 k 条
// 就是 1 + k 遍全文。
//
// 可是位置本来就在第一遍里算出来过 —— kManyMatch 的 DFA 每走到一个能结束匹配的字节都会记一次,
// 走完把它扔了 (这正是 NewSpanScanner 接出来的东西, 且【不额外要钱】: 同一份正文同一份 DFA
// 缓存, 6.4MB 上实测 Match 18.5ms / 收游程 18.4ms)。MatchScanner 就是把这一遍留下来的右端
// 补成完整区间。
//
//	Scan(text)                 一遍全文。收右端游程 + 命中表 (HitIDs / Hit, 与 Set.Match 同解)。
//	AppendMatches(dst, id)     【只有问到的这条才算】: 把第 id 条的命中区间补出来。不碰正文全长,
//	                           只在这一条自己的游程上走。
//
// 分成两步是有意的: 门上很多位【只当 bool 用】(某某类内容在不在), 从来没人问它在哪。
// 一遍扫的时候把全表的左端都补出来是白付 —— 实测这一项占了全表 34ms 里的大头, 而真去问的
// 只有几条。所以补左端这件事挂在"谁问谁付"上。
//
// ── 左端怎么补: 看 PatternLenRange 的两档 (patlen.go) ────────────────────────
//	min == max   定长 (NRIC 9 字节 · 身份证 18 字节): Lo = Hi - min。【不进正则引擎】, 一句减法。
//	其余         从 Hi 往左【锚定】推一次, 用【这一条 pattern 自己一条】的反向 set
//	             (RegexpSet.reverseOne → ResolveSpanWithin), 一次调用给最靠左的那个起点。
//	             反向编译不出来的那几条 AppendMatches 返回 ok=false, 请调用方照老路 FindAll。
//
// 🔴 反向必须是【一条一个 set】。整表建一个反向 set 是死路: set 里状态数是相乘的
//    (doc/状态数为什么会相乘.txt), 155 条的反向表在 6.4MB 正文上实测 65 秒 / arena 顶满 254MB
//    还在 flush, 而正向同一张表 18ms 零 flush。拆成一条一个就没有这个乘法; 而且这些反向 set
//    【从不用来扫正文】, 只做锚定解析 —— 起点只有一个, 那套 `.*?` 前缀引起的状态爆炸机制
//    从根上就不存在。再加上惰性: 只有真被问到的那几条才会被建出来。
//
// ── 重复: 每条 pattern 一个游标, 一次左到右推进 ──────────────────────────────
// 变长 pattern 在一片正文上会在每个可收的位置各报一个右端 (\p{Han}{2,4} 撞 "张三李四王五"
// 报 6/9/12/15/18 五个), 它们说的其实是同一片区域。推进的规矩:
//   ① 右端落在已吐出去那一处里面 (Hi <= cur) → 跳过;
//   ② 否则求【起点不早于 cur 的最靠左那个】起点 —— 回看窗口掐在 cur 上, 绝不越过游标;
//   ③ 从这个起点取最长右端吐出去, cur 推到它。
// 🔴 ② 的窗口掐在 cur 上是【正确性】不是省钱: 不掐的话上例 Hi=18 会回推到 6, 与刚吐出去的
//    [0,12) 相交而被丢掉, "王五"就无声无息漏了。掐上之后回推到 12, 吐 [12,18)。
//
// 🔴 【只在同一条 pattern 内部去重, 跨 pattern 一概不合并】。两条 pattern 在同一片正文上各自
//    命中不是重复, 是两个问题各要一个答案 (带空格的和不带空格的两条 pattern, 下游正是靠
//    "这一段里有没有空格"分流的; 合了就是漏检)。
//
// ── 它不是 FindAllStringIndex ────────────────────────────────────────────────
// 保证的只有三条: ① 吐出去的 text[Lo:Hi] 一定是这条 pattern 的一个【真匹配】;
// ② 正文里有匹配的地方一定会吐出至少一处覆盖它 (不丢召回); ③ 同一条 pattern 吐出去的区间
// 互不相交、按 Lo 升序。至于"和 FindAll 挑的是不是同一处", 交替式 pattern (ab|abcd) 上会
// 不一样 —— FindAll 是 leftmost-first, 这里恒取最长。要逐字节等价的调用方别用这个。
//
// 生命周期同 SpanScanner: 可复用工作区, 热路径上建一次留着, 【不是并发安全的】。
// Scan 之后 text 一直被 scanner 引用着 (AppendMatches 要用), 直到下一次 Scan 或 Reset。
package hgmLibre2

import (
	"errors"
	"sort"
)

// SetMatch 是一处命中: text[Lo:Hi] 是第 Index 条 pattern 的一个真匹配。
type SetMatch struct {
	Index int32
	Lo    int32
	Hi    int32
}

// MatchScanner 是可复用工作区。用 (*RegexpSet).NewMatchScanner 开, 不用了 Close。
type MatchScanner struct {
	set  *RegexpSet
	sc   *SpanScanner
	text string
	// runs[i] 是第 i 条 pattern 上一遍收到的右端游程, 扁平存 (lo,hi) 对。
	runs [][]int32
	hit  []bool
	hits []int32 // 上一遍命中过的下标 (= Set.Match 那张表), 去重且只含真命中
}

// NewMatchScanner 开一个工作区。热路径上建一次长期留着, 别每次扫描新建。
func (s *RegexpSet) NewMatchScanner() (*MatchScanner, error) {
	// batch 开大: 每一批都是一次 cgo 往返 + 一次 DFA 状态挂起/恢复。取 155 条那种默认值
	// (= pattern 条数) 时 6.4MB 正文上要往返 1270 次, 实测比 4096 那档慢一倍。
	sc, err := s.NewSpanScanner(4096)
	if err != nil {
		return nil, err
	}
	return &MatchScanner{
		set:  s,
		sc:   sc,
		runs: make([][]int32, s.size),
		hit:  make([]bool, s.size),
	}, nil
}

// Close 释放底层 SpanScanner。可重复调。
func (m *MatchScanner) Close() {
	if m.sc != nil {
		m.sc.Close()
		m.sc = nil
	}
	m.text = ""
}

// Scan 扫 text 一遍 —— 这是【唯一】一遍全文。之后 HitIDs/Hit 就能用, 想要位置再按条问
// AppendMatches。text 会被留住直到下一次 Scan。
func (m *MatchScanner) Scan(text string) error {
	if m.sc == nil {
		return errClosedMatchScanner
	}
	for _, id := range m.hits {
		m.runs[id] = m.runs[id][:0]
		m.hit[id] = false
	}
	m.hits = m.hits[:0]
	m.text = text
	return m.sc.Scan(text, func(spans []SetSpan) bool {
		for _, sp := range spans {
			i := int(sp.Index)
			if i < 0 || i >= len(m.runs) {
				continue
			}
			if !m.hit[i] {
				m.hit[i] = true
				m.hits = append(m.hits, sp.Index)
			}
			m.runs[i] = append(m.runs[i], sp.Lo, sp.Hi)
		}
		return true
	})
}

// HitIDs 返回上一次 Scan 命中过的 pattern 下标 (无序 · 不重复), 与 Set.Match 给的是同一张表。
// 切片下次 Scan 会被覆写。
func (m *MatchScanner) HitIDs() []int32 { return m.hits }

// Hit 报第 i 条上一次 Scan 有没有命中 (O(1) 查表)。
func (m *MatchScanner) Hit(i int) bool {
	return i >= 0 && i < len(m.hit) && m.hit[i]
}

// AppendMatches 把第 id 条 pattern 的命中区间以扁平 (lo,hi) 对 append 进 dst 并返回。
// 只在这一条自己的游程上走, 不碰正文全长。同一条问两次给的是同一份结果 (幂等)。
//
// ok=false 表示这一条补不出左端 (反向编译不出来) —— 调用方照老路对它跑一遍 FindAll。
// 没命中的条返回 dst 原样 + ok=true。
func (m *MatchScanner) AppendMatches(dst []int32, id int32) (out []int32, ok bool, err error) {
	if m.sc == nil {
		return dst, false, errClosedMatchScanner
	}
	i := int(id)
	if i < 0 || i >= len(m.runs) || !m.hit[i] {
		return dst, true, nil
	}
	rs := m.sortedRuns(i)

	minL, maxL := m.set.PatternLenRange(i)
	if minL <= 0 {
		// 能匹配【空串】的 pattern 不走这条路: 每个位置都是一处零长命中, 推进的游标压不住,
		// 吐出来的 text[Lo:Lo] 对下游也没有意义。这种交给老路。
		return dst, false, nil
	}
	fixed := minL == maxL && maxL >= 0
	lo32 := int32(minL)
	var rev *RegexpSet
	if !fixed {
		if rev = m.set.reverseOne(i); rev == nil {
			return dst, false, nil // 补不出来, 请调用方走老路
		}
	}
	cur := int32(0)

	for k := 0; k+1 < len(rs); k += 2 {
		for e := rs[k]; e <= rs[k+1]; e++ {
			if e <= cur {
				continue // 落在已吐出去那一处里面
			}
			var start int32
			if fixed {
				start = e - lo32
				if start < cur {
					continue // 与已吐出去那一处相交
				}
			} else {
				// 🔴 bound = cur: 回看【绝不越过游标】。是正确性不是省钱, 见文件头 ②。
				// 顺带它把回看代价钉成"离游标多远", 与正文长度无关。
				pos, hit, e2 := rev.ResolveSpanWithin(m.text, e, cur, 0)
				if e2 != nil {
					return dst, false, e2
				}
				if !hit {
					continue
				}
				start = pos
			}
			// 从这个起点取【最长】右端 —— 变长 pattern 在同一起点上有一串长度都成立,
			// 取最短就是把命中截断, 下游拿去做定长校验会把真命中判成假。
			end := e
			if !fixed {
				pos, hit, e2 := m.set.ResolveSpan(m.text, start, id)
				if e2 != nil {
					return dst, false, e2
				}
				if hit && pos > end {
					end = pos
				}
			}
			dst = append(dst, start, end)
			cur = end
		}
	}
	return dst, true, nil
}

// sortedRuns 保证第 i 条的游程按左端升序。正向扫本来就是从左往右走的, 所以通常已经有序 ——
// SpanScanner 的文档只保证"同一批里", 这里先确认一次, 有序就不排。
func (m *MatchScanner) sortedRuns(i int) []int32 {
	rs := m.runs[i]
	for k := 2; k < len(rs); k += 2 {
		if rs[k] < rs[k-2] {
			pairs := make([][2]int32, 0, len(rs)/2)
			for j := 0; j+1 < len(rs); j += 2 {
				pairs = append(pairs, [2]int32{rs[j], rs[j+1]})
			}
			sort.Slice(pairs, func(a, b int) bool { return pairs[a][0] < pairs[b][0] })
			rs = rs[:0]
			for _, p := range pairs {
				rs = append(rs, p[0], p[1])
			}
			m.runs[i] = rs
			break
		}
	}
	return rs
}

// AppendAllMatches 是"全表都要"的便利版 (量具 / 对拍用): 扫一遍再把每条命中的都补出来。
// 生产路径别用这个 —— 它把没人问的那些条也补了, 正是分成两步要躲开的那笔钱。
// unresolved 里是补不出左端、需要调用方走老路的下标。
func (m *MatchScanner) AppendAllMatches(dst []SetMatch, text string) (out []SetMatch, unresolved []int32, err error) {
	if err := m.Scan(text); err != nil {
		return dst, nil, err
	}
	var buf []int32
	for _, id := range m.hits {
		buf = buf[:0]
		buf, ok, err := m.AppendMatches(buf, id)
		if err != nil {
			return dst, unresolved, err
		}
		if !ok {
			unresolved = append(unresolved, id)
			continue
		}
		for k := 0; k+1 < len(buf); k += 2 {
			dst = append(dst, SetMatch{Index: id, Lo: buf[k], Hi: buf[k+1]})
		}
	}
	return dst, unresolved, nil
}

// errClosedMatchScanner 单独提出来, 免得每次构造一遍 error。
var errClosedMatchScanner = errors.New("re2native: match scanner closed")
