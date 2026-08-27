// matchscan_reverse.go —— 反向 MatchScanner: 从正文【末尾往前】一遍扫, 边扫边一批一批交出
// 各条 pattern 的不重叠命中区间。口径是 rightmost-longest。
//
// 🔴 一句话: 它是 matchscan.go 那个的【镜像】, 两处不一样 ——
//    ① 交出来的区间按 Lo 【降序】(正向是升序);
//    ② 去重叠的口径是 rightmost-longest (正向是 leftmost-longest)。
//    两种口径没有本质区别, 只是结果不一样: 同一片正文上"谁先占坑"从左边换到了右边。
//    全文见 doc/MatchScanner的leftmost-longest保证.md 第 8 节。
//
// ── 为什么会有这一层 ────────────────────────────────────────────────────────
//
// 有一族 pattern 正着扫状态数对计数上界指数增长, 反着读就塌回线性 —— `S B{m,n} L` 里起始类
// 严格窄于重复类那一族 (doc/状态数为什么会相乘.txt §3, 实测同一条 pattern 正向 66572 状态 /
// 8.39MB, 反向 42 状态 / 0.07MB)。这种表本来就该反着扫。可在这一层补上之前, 反向 set 只回答
// 得了"命中没有 / 哪几条命中", 要位置就得命中之后再正向扫一遍全文 —— 而"把 1+k 遍压成 1 遍"
// 正是 MatchScanner 存在的全部意义。这一层把那一遍补回来。
//
// ── 反向【更好】做, 不是更难做 ──────────────────────────────────────────────
//
// 正向那一遍 DFA 交出来的是匹配的【右端】, 起点得在 Go 这侧猜回去 —— matchscan.go 里那一整节
// "第三种口径"讲的就是这件事的代价。反向交出来的是【左端】= 起点, 而 leftmost/rightmost-longest
// 这个口径本来就定义在起点上, 所以这一层【没有】"猜"这一步:
//
//	反向 set FindAllIndex                       → 匹配左端, 按扫描方向 (从右往左) 单调
//	正向 set ResolveSpanWithin(from=左端, bound=游标) → 【最长】右端, 且绝不越过游标
//
// 于是: 没有路 A / 路 B 之分, 没有 spanFast 这一档, 也不需要 maxL 窗口。每处命中恒等于
// "一次锚定解析", 代价 = 这处命中有多长, 与正文长度无关。
//
// ── 为什么"从右往左"仍然一个字节都不用攒 ─────────────────────────────────────
//
// 因为口径也跟着翻了。要是硬要在反向扫描上给 leftmost-longest, 那就得攒: 手上这一处随时可能
// 被更靠左、还没扫到的那一处整个吃掉, 有上界的还能靠一个 maxL 宽的延迟缓冲兜住, 无上界的
// (邮箱那种) 得攒到整篇扫完 —— 内存跟着正文长, 正是这一层存在的理由被赔掉。
// 改成 rightmost-longest 这件事就没了: 从右往左走, 【第一个见到的起点就是最终答案】,
// 左边不可能再来一个把它顶掉 —— 与正向"第一个见到的就是最终答案"是同一句话照镜子。
// 所以游标在回调里当场推完, 输出写进固定的 matchScanBatch 缓冲 (12KB), 满了就交出去、就地复用。
//
// ── 对外那条保证, 逐字读 ─────────────────────────────────────────────────────
//
//	① text[Lo:Hi] 是这条 pattern 的一个真匹配;
//	② 【同一条 pattern】吐的区间互不相交, 按 Lo 【降序】;
//	③ 口径是 rightmost-longest: 在还没被占掉的那段正文里, 反复取【起点最靠右】的那个匹配,
//	   同起点取【最长】, 吐出去, 再往左接着找。
//
// 三处容易读错的地方:
//   🔴 ② 的【降序】是这个类型与正向那个最容易写错的差别。要升序是调用方那边一句 reverse 的事,
//      库这边翻就得攒 = 又是缓冲 (见上一节)。
//   🔴 ② 只管【单条】。跨 pattern 一概不合并, 理由与正向完全一样 (matchscan.go 那段护照号/
//      身份证抢同一段的实例)。
//   🔴 能匹配空串的 pattern 只能配 boolOnly, SetModes 当场报错。
//
// ── rightmost-longest 与 leftmost-longest 差在哪 ─────────────────────────────
//
// 只在【两个真匹配互相交叠】的地方差, 不交叠的正文上两者逐处相同。例:
//
//	a|ab  撞 "abab"   leftmost-longest [[0,2) [2,4)]     rightmost-longest [[2,4) [0,2)]  (同一批, 顺序反)
//	ab|b  撞 "aab"    leftmost-longest [[1,3)]           rightmost-longest [[2,3)]        ← 这里才真差
//
// 后一种局面下"谁赢"由方向定, 两边都是【真匹配】, 都不重叠, 都不漏段。选哪个是调用方的事:
// 要跟 stdlib 的 re.Longest().FindAllStringIndex 逐字节对上就用正向那个; 只是要"把这片正文里
// 的东西都框出来"(脱敏 · 定位 · 计数), 两个都行。
//
// 生命周期同正向那个: 可复用工作区, 热路径上建一次留着,【不是并发安全的】。
package hgmLibre2

import (
	"errors"
	"strconv"
)

// MatchScannerReverse 是反向那一侧的可复用工作区。用 (*RegexpSetReverse).NewMatchScanner 开,
// 不用了 Close。字段含义与 MatchScanner 一一对应, 差别只在 cur 的方向。
type MatchScannerReverse struct {
	set   *RegexpSetReverse
	alloc *RegexpSet_FindAllIndex_Alloc_t
	text  string
	per   []msrPat_t
	mode  []MatchScanMode_t // 每条要什么 (见 SetModes); nil = 全走默认档
	hit   []bool
	hits  []int32 // 上一遍命中过的下标 (= Set.Match 那张表), 去重且只含真命中
	bad   []MatchScanUnresolved // 本遍作废的那几条, Scan 收尾时原样还给调用方
	// out 是那块固定的输出缓冲 (长度恒为 matchScanBatch), outN 是已填几处。
	out  []SetMatch
	outN int
	fn   func([]SetMatch)
}

// msrPat_t 是每条 pattern 的推进状态。cur 是那把游标, 方向与正向相反。
type msrPat_t struct {
	inited bool
	fixed  bool
	bad    bool // 补不出右端 (能匹配空串 / 编不出单条正向 set / 游程乱序) → 调用方走老路
	minL   int32
	fwd    *RegexpSet // 这一条自己一条的【正向 set】, 惰性建; 拿它 ResolveSpanWithin 取最长右端
	cur    int32      // 已吐出去那一处的【左端】: 新命中必须整个落在 [0, cur)
	lastHi int32      // 上一条游程的右端, 用来确认降序
}

// NewMatchScanner 开一个反向工作区。热路径上建一次长期留着, 别每次扫描新建。
//
// 🔴 反向 set 本身仍然该是【一条一个】或者至少是很小的一张表: set 里的状态数是相乘的
//    (doc/状态数为什么会相乘.txt), 155 条的反向表在 6.4MB 正文上实测 65 秒 / arena 顶满
//    254MB 还在 flush。这一层不改变那件事 —— 它只是把"扫出来的左端"补成完整区间。
func (r *RegexpSetReverse) NewMatchScanner() (*MatchScannerReverse, error) {
	alloc, err := r.NewFindAllIndexAlloc()
	if err != nil {
		return nil, err
	}
	return &MatchScannerReverse{
		set:   r,
		alloc: alloc,
		per:   make([]msrPat_t, r.s.size),
		hit:   make([]bool, r.s.size),
		out:   make([]SetMatch, matchScanBatch),
	}, nil
}

// SetModes 声明每条 pattern 要什么 (下标即 pattern 下标, 长度不足的按零值 = 默认档)。
// 传 nil = 全默认档。与正向那个同解, 但只认两档:
//
//	MatchScanMode_span      要区间 (零值 · 默认)。口径 rightmost-longest, 无条件。
//	MatchScanMode_boolOnly  只要"命中没命中", 一处区间都不收口、一次端点都不补。
//
// 🔴 MatchScanMode_spanFast 在这一侧【当场报错】, 不是静默忽略: 反向只有一条路, 而那条路
//    本来就是最便宜的那条 (每处命中 = 一次锚定解析), 口径也是有保证的。正向那边 spanFast
//    换来的那点便宜, 在这边不存在, 一个"看起来还能再快一档"的名字只会让人以为自己漏配了。
//
// 🔴 能匹配空串的 pattern (PatternLenRange 的 min <= 0) 只允许配 boolOnly, 否则这里当场报错
//    而不是运行时静默退回老路 —— 理由同正向: 每个位置都是一处零长命中, 游标压不住。
func (m *MatchScannerReverse) SetModes(modes []MatchScanMode_t) error {
	for i := 0; i < len(modes) && i < len(m.per); i++ {
		if modes[i] == MatchScanMode_boolOnly {
			continue
		}
		if modes[i] == MatchScanMode_spanFast {
			return errors.New("re2native: reverse match scanner pattern " + strconv.Itoa(i) +
				" 配了 MatchScanMode_spanFast; 反向只有一条路且它就是最便宜的那条, 没有这一档" +
				"; pattern=" + m.set.s.pats[i])
		}
		if minL, _ := m.set.s.PatternLenRange(i); minL <= 0 {
			return errors.New("re2native: reverse match scanner pattern " + strconv.Itoa(i) +
				" 能匹配空串, 只能配 MatchScanMode_boolOnly; pattern=" + m.set.s.pats[i])
		}
	}
	m.mode = modes
	return nil
}

// modeOf 取第 i 条的档位 (越界 = 默认档)。
func (m *MatchScannerReverse) modeOf(i int) MatchScanMode_t {
	if i < 0 || i >= len(m.mode) {
		return MatchScanMode_span
	}
	return m.mode[i]
}

// Close 释放底层的 FindAllIndex 工作区。可重复调。
func (m *MatchScannerReverse) Close() {
	if m.alloc != nil {
		m.alloc.Close()
		m.alloc = nil
	}
	m.text = ""
}

// Scan 从末尾往前扫 text 一遍 —— 这是【唯一】一遍全文。命中区间攒够一批 (matchScanBatch 处)
// 就调一次 batchFn; 扫完把不足一批的余数也交出去。全程没有任何命中就一次都不调。
// 返回之后 HitIDs/Hit 可用。
//
// 🔴 交给 batchFn 的切片是内部缓冲本身, 下一批原地覆写 —— 要留就 append 走。
// 🔴 各条 pattern 的结果是【交错】着来的, 同一条 pattern 内部按 Lo 【降序】(不是升序)。
//
// unresolved 是这一遍里没能全给出来的那几条 (能匹配空串 / 单条正向 set 编不出来 / 游程乱序 /
// DFA 预算不够), 每条带 Index · Reason · ResumeFrom。【已经交出去的那些不作废】: 反向的
// 已交付部分覆盖 [ResumeFrom, len(text)), 调用方只要对这几条在 text[:ResumeFrom] 这一段上
// 补一遍老路 (别切片 —— 用 bound 参数, \b / ^ / $ 才看得到真邻居)。切片下次 Scan 会被覆写。
//
// batchFn 传 nil 合法: 只要命中表 (等价于 RegexpSetReverse.Match), 一处区间都不收口。
func (m *MatchScannerReverse) Scan(text string, batchFn func(ms []SetMatch)) (unresolved []MatchScanUnresolved, err error) {
	if m.alloc == nil {
		return nil, errClosedMatchScanner
	}
	for _, id := range m.hits {
		p := &m.per[id]
		p.inited, p.fixed, p.bad = false, false, false
		p.cur, p.lastHi = 0, 0
		m.hit[id] = false
	}
	m.hits = m.hits[:0]
	m.bad = m.bad[:0]
	m.text = text
	m.fn = batchFn
	m.outN = 0
	err = m.set.FindAllIndex(text, m.alloc, func(runs []RegexpSet_FindAllIndex_Run_t) {
		for k := range runs {
			r := &runs[k]
			i := int(r.ReIndex)
			if i < 0 || i >= len(m.per) {
				continue
			}
			if !m.hit[i] {
				m.hit[i] = true
				m.hits = append(m.hits, r.ReIndex)
			}
			if batchFn == nil || m.modeOf(i) == MatchScanMode_boolOnly {
				continue // 只当 bool 用的那几条: 到此为止, 一次端点都不补
			}
			m.feed(i, r.Lo, r.Hi)
		}
	})
	if err != nil {
		m.text = ""
		m.fn = nil
		return nil, err
	}
	if m.outN > 0 && batchFn != nil {
		batchFn(m.out[:m.outN])
		m.outN = 0
	}
	m.text = ""
	m.fn = nil
	return m.bad, nil
}

// emit 把收口出来的一处区间写进那块固定缓冲, 满了就交出去。
func (m *MatchScannerReverse) emit(i int, lo, hi int32) {
	m.out[m.outN] = SetMatch{Index: int32(i), Lo: lo, Hi: hi}
	m.outN++
	if m.outN == len(m.out) {
		m.fn(m.out)
		m.outN = 0
	}
}

// markBad 把第 i 条当场作废: 之后它的游程一律不看, 收尾时进 unresolved。
//
// ResumeFrom 就是这一条的游标 —— 反向的已交付部分覆盖 [cur, len(text)), 缺的只有它左边
// 那一截。只有 dfaBudget 能走到"已经交出去几处"的地步, 其余一律 len(text) = 整篇重来。
// 见 matchscan_unresolved.go。
func (m *MatchScannerReverse) markBad(i int, reason MatchScanUnresolvedReason_t) {
	p := &m.per[i]
	p.bad = true
	from := int32(len(m.text))
	if reason == MatchScanUnresolvedReason_dfaBudget {
		from = p.cur
	}
	m.bad = append(m.bad, MatchScanUnresolved{Index: int32(i), ResumeFrom: from, Reason: reason})
}

// feed 把一条游程 [lo,hi] (都是【左端】偏移) 喂给第 i 条的游标, 当场推进并收口。
//
// 推进的规矩 (与正向那套照镜子):
//
//	① 左端落在已吐出去那一处里面 (s >= cur) → 跳过;
//	② 否则从 s 起【锚定】取最长右端, 上界掐在 cur 上 —— 绝不越过游标;
//	③ 取不到 (这个左端在游标底下伸不出一个完整匹配) → 跳过; 取到就吐, cur 推到 s。
//
// 🔴 ② 的上界掐在 cur 上是【正确性】不是省钱: 不掐的话新吐的这一处会与刚吐出去的那一处
//    相交, 而 ② 承诺的是"同一条 pattern 内部互不相交"。
func (m *MatchScannerReverse) feed(i int, lo, hi int32) {
	p := &m.per[i]
	if p.bad {
		return
	}
	if !p.inited {
		p.inited = true
		minL, maxL := m.set.s.PatternLenRange(i)
		if minL <= 0 {
			// 能匹配【空串】的 pattern 不走这条路: 每个位置都是一处零长命中, 游标压不住。
			m.markBad(i, MatchScanUnresolvedReason_emptyMatch)
			return
		}
		p.minL = int32(minL)
		p.fixed = minL == maxL && maxL >= 0
		p.cur = int32(len(m.text)) // 游标从正文【末尾】起, 往左退
		p.lastHi = p.cur
		if !p.fixed {
			// 变长: 右端靠这一条自己那份【正向 set】锚定往右伸。定长不用建任何对象。
			if p.fwd = m.set.s.forwardSetOne(i); p.fwd == nil {
				m.markBad(i, MatchScanUnresolvedReason_compile)
				return
			}
		}
	}
	if hi > p.lastHi {
		// 游程乱序 —— 反向流必须降序, 前提没了就宁可退回老路。
		m.markBad(i, MatchScanUnresolvedReason_runOrder)
		return
	}
	p.lastHi = lo

	for s := hi; s >= lo; s-- {
		if s >= p.cur {
			continue // 落在已吐出去那一处里面
		}
		if p.fixed {
			// 定长: 右端唯一 (s+minL), 一句加法, 不进正则引擎。
			end := s + p.minL
			if end > p.cur {
				continue // 会与已吐出去那一处相交
			}
			m.emit(i, s, end)
			p.cur = s
			continue
		}
		// 🔴 bound = cur: 往右伸【绝不越过游标】。是正确性不是省钱, 见上面 ②。
		// 顺带它把解析代价钉成"离游标多远", 与正文长度无关。
		// 🔴 ResolveSpanWithin 给的是【最长】的那个右端 —— 取最短就是把命中截断,
		//    下游拿去做定长校验会把真命中判成假 (见 spanresolve.go 那段红字)。
		end, ok, err := p.fwd.ResolveSpanWithin(m.text, s, p.cur, 0)
		if err != nil {
			// DFA 放弃 (预算不够) —— 不是"没有匹配", 也不该把整遍扫描带崩, 更不该让
			// 【已经交出去的那些】跟着作废: 报出游标当断点, 调用方从那儿往左补。
			m.markBad(i, MatchScanUnresolvedReason_dfaBudget)
			return
		}
		if !ok {
			continue // 这个左端在游标底下伸不出一个完整匹配
		}
		m.emit(i, s, end)
		p.cur = s
	}
}

// HitIDs 返回上一次 Scan 命中过的 pattern 下标 (无序 · 不重复), 与 RegexpSetReverse.Match
// 给的是同一张表。切片下次 Scan 会被覆写。
func (m *MatchScannerReverse) HitIDs() []int32 { return m.hits }

// Hit 报第 i 条上一次 Scan 有没有命中 (O(1) 查表)。
func (m *MatchScannerReverse) Hit(i int) bool {
	return i >= 0 && i < len(m.hit) && m.hit[i]
}

// ForwardSetOneStats 报【已经被建出来】的那些单条正向 set 的账: 几条 · 状态数合计 ·
// 状态区实际字节合计。惰性建 ⟹ 没被问过位置的 pattern 一条都不占。
// 与 (*RegexpSet).ReverseOneStats 是同一个用途 (量内存去哪了) 的镜像, 不制造状态。
func (r *RegexpSetReverse) ForwardSetOneStats() (n int, states, arenaCap int64) {
	r.s.fwdSet1Mu.Lock()
	defer r.s.fwdSet1Mu.Unlock()
	for _, f := range r.s.fwdSet1 {
		if f == nil {
			continue
		}
		mi := f.MemInfo()
		n++
		states += mi.States
		arenaCap += mi.ArenaCap
	}
	return n, states, arenaCap
}
