// matchscan2.go —— MatchScanner2: 与 MatchScanner 同样"一遍扫正文、一批批交出命中区间",
// 但补端点走的是【另一条路】(文档里的路 D2)。对外那三条保证与默认档【一字不差】,
// 差别只在价钱和它是怎么做到的。
//
// 🔴 MatchScanner 一个字都没动。这是【另一个类型】, 两者可以同时存在、各扫各的 ——
//    加它是为了把三条路 (A / B / D2) 摆在同一个库、同一份表、同一份正文上比价钱,
//    而不是把默认档换掉。选哪个见文件末尾"什么时候该用哪个"。
//
// ── 三条路都在解同一道题 ────────────────────────────────────────────────────
// 门 (正向 set + kManyMatch, 一遍全文) 只给【右端】: {e | 存在某个 s 使 text[s,e) 匹配}。
// 里面没有 (起点, 终点) 的配对信息, 也没有优先序 —— 配对只能在这一层重建。三条路的差别
// 就是"拿到右端 e 之后怎么把起点找回来":
//
//	路 B (MatchScanner 默认档)  从 max(cur, e-maxL) 起做一次【正向非锚定】搜索。
//	                            对: longest 口径一趟就同时给起点和终点。
//	                            贵: maxL 没上界时下界塌回游标, 要走完整段空隙。
//
//	路 A (MatchScanner 的 spanFast)  从 e 反向【只种 accept】锚定回推一个起点, 再正向取最长右端。
//	                            便宜: 两趟的代价都只跟"这处命中有多长"有关, 与正文长度无关。
//	                            错: 只种 accept 只看得见"正好在 e 结束"的那些起点 ——
//	                            \b(?:ab cd ef|cd)\b 撞 "ab cd ef", 门给的最小右端是 "cd" 那处,
//	                            回推只能到 "cd" 的左端, 真正的最左起点 0 根本不在候选里。
//	                            这就是文件头说的"第三种口径"的病根。
//
//	路 D2 (本文件)              从 e 反向【种全部状态】走一趟, 把 [cur, e) 里【全部候选起点】
//	                            一次收齐 (spanviable.go 的 ViableStarts), 然后【从小到大】
//	                            逐个拿正向锚定 longest 去验, 第一个验过的就是答案。
//	                            对: 候选集是"起点落在 [cur,e) 的匹配"的超集 (证明见下),
//	                            升序试 ⟹ 第一个通过的必然是 leftmost, 而 longest 口径的
//	                            锚定搜索直接给最长右端 ⟹ 严格 leftmost-longest。
//	                            价钱: 反向那一趟与路 A 同价 (走到死就收工); 多出来的是
//	                            "验了几个假候选"—— 这一笔用 Stats().Tries 量。
//
// ── 为什么 D2 是对的 (两条, 都要) ───────────────────────────────────────────
//
// ① 候选集不漏。设真答案是 [s, E) 且 s ∈ [cur, e)。E 是一个匹配右端且 E > s >= cur,
//    而 e 是【> cur 的最小右端】⟹ E >= e ⟹ text[s, e) 是 text[s, E) 的前缀 ⟹ 它是
//    一个可行前缀 ⟹ s 一定在 ViableStarts 给的候选里。∎
//
// ② 一个都没验过 ⟹ [cur, e) 里根本没有起点 ⟹ 游标可以直接推到 e (对①取逆否)。
//    所以"全军覆没"这一支不是放弃, 是【证明了这一段是空的】。
//
// 顺带一条: 验过的那个候选 s 给出的右端 E 必然 >= e (E 是右端且 E > cur ⟹ E >= e),
// 所以游标每次都真的越过 e —— 各轮的回看窗口 [cur, e] 两两不交且递增, 反向那一趟的
// 累加封顶 = 多扫一遍正文。
//
// ── 对外那条保证 (与 MatchScanner 默认档逐字相同) ───────────────────────────
//	① text[Lo:Hi] 是这条 pattern 的一个真匹配;
//	② 【同一条 pattern】吐的区间互不相交, 按 Lo 升序;
//	③ 口径是 leftmost-longest (= stdlib 的 re.Longest().FindAllStringIndex)。
// 🔴 ② 只管【单条】; 跨 pattern 一概不合并 —— 理由与 matchscan.go 里那一段一字不差。
// 🔴 ③ 【不是】"与 FindAllStringIndex 相同"。stdlib 默认那个是 leftmost-first (贪心),
//    要对拍就拿 Longest() 那个去对, 拿默认那个对会是【假红】。
//
// ── 档位 ────────────────────────────────────────────────────────────────────
// 复用 MatchScanMode_t, 但这里只有两态有意义:
//	MatchScanMode_boolOnly  只要命中表, 一处区间都不收口 (与 MatchScanner 同解, 同样值钱)。
//	MatchScanMode_span      要区间 (零值 = 默认)。
//	MatchScanMode_spanFast  【当 span 处理】。这条路上没有"快而不准"的档 —— 它整个存在的
//	                        理由就是拿严格口径换路 A 的价钱, 再挂一个不准的档就自相矛盾了。
//
// ── 生命周期 ────────────────────────────────────────────────────────────────
// 可复用工作区, 热路径上建一次留着,【不是并发安全的】。text 只在 Scan 那一遍里被引用。
// 用完 Close。
package hgmLibre2

import (
	"errors"
	"strconv"
)

// ms2CandBuf 是候选起点缓冲的起始长度。不够会翻倍重来一趟 —— 真表上一个右端的候选
// 通常是个位数, 这个数只是为了让"翻倍"基本不发生。
const ms2CandBuf = 64

// MatchScan2Stats_t 是一遍 Scan 的账。加它是因为 D2 的价钱全在"验了几个假候选"上,
// 而那一笔从外面一个字都看不见 —— 没有这三个数就没法判断某张表适不适合走这条路。
type MatchScan2Stats_t struct {
	Walks int64 // 反向走了几趟 (= 处理了几个"没被游标盖住的"右端)
	Cands int64 // 这些趟一共给出多少候选起点
	Tries int64 // 一共拿正向锚定验了几次 (<= Cands; 命中即停)
	Emits int64 // 交出去几处区间
}

// MatchScanner2 是可复用工作区。用 (*RegexpSet).NewMatchScanner2 开, 不用了 Close。
type MatchScanner2 struct {
	set   *RegexpSet
	alloc *RegexpSet_FindAllIndex_Alloc_t
	text  string
	per   []ms2Pat_t
	mode  []MatchScanMode_t
	hit   []bool
	hits  []int32
	// scanErr 是本遍出的错。收口跑在 FindAllIndex 的回调里, 那儿 return 不出去。
	scanErr error
	out     []SetMatch
	outN    int
	fn      func([]SetMatch)
	findCtx *FindStringIndex_ctx_t
	// cands 是候选起点缓冲 (native 直接往里写, 降序)。跨 pattern 复用, 只在一个右端的
	// 处理期间有意义。不够就翻倍, 翻上去就留着。
	cands []int32
	st    MatchScan2Stats_t
}

// ms2Pat_t 是每条 pattern 的推进状态。分档所需的那几件事全与正文无关, 一律在
// NewMatchScanner2 / SetModes 里算完, Scan 里一件都不算。
type ms2Pat_t struct {
	spanable bool // 能不能收口成区间 (false = 能匹配空串, 只能当 bool 用)
	fixed    bool // 定长: 起点唯一, 一句减法, 不进正则引擎
	minL     int32
	vp       *RegexpSetReverse // 反向 · 只装这一条的 set: 种全部状态回推【全部候选起点】
	fwd      *Regexp           // 正向 · longest 单条: 锚定在候选上验, 顺手给最长右端
	cur      int32             // 已吐出去的最右字节
	lastLo   int32             // 上一条游程的左端, 用来确认升序
}

// NewMatchScanner2 开一个工作区。热路径上建一次长期留着, 别每次扫描新建。
// unsupported 与 NewMatchScanner 同解: 能匹配空串 (minL <= 0) 的那几条走不了区间这条路,
// 只能配 MatchScanMode_boolOnly。这张名单在建工作区那一刻就定死, 与正文无关。
func (s *RegexpSet) NewMatchScanner2() (m *MatchScanner2, unsupported []int32, err error) {
	alloc, err := s.NewFindAllIndexAlloc()
	if err != nil {
		return nil, nil, err
	}
	m = &MatchScanner2{
		set:     s,
		alloc:   alloc,
		per:     make([]ms2Pat_t, s.size),
		hit:     make([]bool, s.size),
		out:     make([]SetMatch, matchScanBatch),
		findCtx: NewFindStringIndex_ctx(),
		cands:   make([]int32, ms2CandBuf),
	}
	for i := 0; i < s.size; i++ {
		p := &m.per[i]
		minL, maxL := s.PatternLenRange(i)
		if minL <= 0 {
			unsupported = append(unsupported, int32(i))
			continue // spanable 留 false: 之后一律当 bool 用
		}
		p.spanable = true
		p.minL = int32(minL)
		// 🔴 D2 用不到 maxL。路 B 靠它把回看窗口的下界抬起来 (没上界就塌回游标, 白扫一段),
		//    而 D2 的下界是【反向那一趟自己走到死的地方】—— 动态的, 不需要 maxL 兜底。
		//    这正是它比 B 便宜的全部原因。
		p.fixed = minL == maxL && maxL >= 0
	}
	return m, unsupported, nil
}

// SetModes 声明每条 pattern 要什么 (下标即 pattern 下标, 长度不足的按零值 = 默认档)。
// 语义同 (*MatchScanner).SetModes, 只有一处不同: MatchScanMode_spanFast 在这里【当 span 处理】
// (见文件头"档位")。unsupported 那几条只允许配 boolOnly, 否则当场报错。
func (m *MatchScanner2) SetModes(modes []MatchScanMode_t) error {
	for i := 0; i < len(modes) && i < len(m.per); i++ {
		if modes[i] == MatchScanMode_boolOnly {
			continue
		}
		if !m.per[i].spanable {
			return errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
				" 能匹配空串, 只能配 MatchScanMode_boolOnly; pattern=" + m.set.pats[i])
		}
	}
	m.mode = modes
	return nil
}

// modeOf 取第 i 条的档位 (越界 = 默认档)。
func (m *MatchScanner2) modeOf(i int) MatchScanMode_t {
	if i < 0 || i >= len(m.mode) {
		return MatchScanMode_span
	}
	return m.mode[i]
}

// Close 释放底层的 FindAllIndex 工作区。可重复调。
func (m *MatchScanner2) Close() {
	if m.alloc != nil {
		m.alloc.Close()
		m.alloc = nil
	}
	m.text = ""
}

// Stats 返回上一次 Scan 的账 (见 MatchScan2Stats_t)。
func (m *MatchScanner2) Stats() MatchScan2Stats_t { return m.st }

// Scan 扫 text 一遍 —— 这是【唯一】一遍全文。语义 (分批 · 缓冲原地覆写 · 交错 ·
// 要么全给要么整遍报错 · batchFn 传 nil 只要命中表) 与 (*MatchScanner).Scan 逐字相同。
func (m *MatchScanner2) Scan(text string, batchFn func(ms []SetMatch)) error {
	if m.alloc == nil {
		return errClosedMatchScanner2
	}
	for _, id := range m.hits {
		p := &m.per[id]
		p.cur, p.lastLo = 0, 0
		m.hit[id] = false
	}
	m.hits = m.hits[:0]
	m.scanErr = nil
	m.text = text
	m.fn = batchFn
	m.outN = 0
	m.st = MatchScan2Stats_t{}
	err := m.set.FindAllIndex(text, m.alloc, func(runs []RegexpSet_FindAllIndex_Run_t) {
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
			if batchFn == nil || !m.per[i].spanable || m.modeOf(i) == MatchScanMode_boolOnly {
				continue // 只当 bool 用的那几条: 到此为止, 一次端点都不补
			}
			m.feed(i, r.Lo, r.Hi)
		}
	})
	m.text = ""
	m.fn = nil
	if err != nil {
		m.outN = 0
		return err
	}
	if m.scanErr != nil {
		m.outN = 0 // 这一遍不算数, 余下那不足一批的也不交
		return m.scanErr
	}
	if m.outN > 0 && batchFn != nil {
		batchFn(m.out[:m.outN])
		m.outN = 0
	}
	return nil
}

// emit 把收口出来的一处区间写进那块固定缓冲, 满了就交出去。
func (m *MatchScanner2) emit(i int, lo, hi int32) {
	m.out[m.outN] = SetMatch{Index: int32(i), Lo: lo, Hi: hi}
	m.outN++
	m.st.Emits++
	if m.outN == len(m.out) {
		m.fn(m.out)
		m.outN = 0
	}
}

// fail 记下本遍的错, 只记第一个 (后面的多半是连锁)。
func (m *MatchScanner2) fail(err error) {
	if m.scanErr == nil {
		m.scanErr = err
	}
}

// feed 把一条游程 [lo,hi] (都是右端偏移) 喂给第 i 条的游标, 当场推进并收口。
func (m *MatchScanner2) feed(i int, lo, hi int32) {
	p := &m.per[i]
	if m.scanErr != nil {
		return // 这一遍已经不算数了, 后面一律空转
	}
	// 两个单条对象都是惰性建的: 没被真问到位置的 pattern 一个都不占。
	if !p.fixed {
		if p.fwd == nil {
			if p.fwd = m.set.forwardOne(i); p.fwd == nil {
				m.fail(errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
					" 补端点要的【正向单条】编不出来 (maxMem 配小了); pattern=" + m.set.pats[i]))
				return
			}
		}
		if p.vp == nil {
			if p.vp = m.set.viableOne(i); p.vp == nil {
				m.fail(errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
					" 补端点要的【反向单条 set】编不出来 (maxMem 配小了); pattern=" + m.set.pats[i]))
				return
			}
		}
	}
	if lo < p.lastLo {
		// 游程乱序 —— 推进的前提没了。这是本库的不变量崩了, 不是调用方的用法问题。
		m.fail(errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
			" 游程乱序 (lo=" + strconv.Itoa(int(lo)) + " < 上一条 " + strconv.Itoa(int(p.lastLo)) +
			") —— 这是【本库的 bug】, 请连 pattern 与正文一起报; pattern=" + m.set.pats[i]))
		return
	}
	p.lastLo = lo

	for e := lo; e <= hi; e++ {
		if e <= p.cur {
			continue // 落在已吐出去那一处里面
		}
		if p.fixed {
			// 定长: 起点唯一 (e-minL), 一句减法, 不进正则引擎。两条路在定长上必然同解。
			start := e - p.minL
			if start < p.cur {
				continue // 与已吐出去那一处相交
			}
			m.emit(i, start, e)
			p.cur = e
			continue
		}
		// ① 反向 · 种全部状态 · 回看不越过游标: 把 [cur, e) 里全部候选起点一次收齐。
		//    🔴 bound = cur 在这里和路 A 一样是【正确性】不是省钱: 越过游标推出来的起点
		//       会与刚吐出去的那一处相交, 那一处就得整个丢掉 = 无声漏报。
		n, err := p.vp.ViableStarts(m.text, e, p.cur, 0, m.cands)
		if err != nil {
			m.fail(errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
				" 可行前缀回推失败: " + err.Error() + "; pattern=" + m.set.pats[i]))
			return
		}
		if n > len(m.cands) {
			// 缓冲不够 —— 里面写下的是最大的那几个 (恰好最没用的), 整批作废, 换大的重来。
			m.cands = make([]int32, n*2) // 翻倍留余量: 下一个右端多半也是这个量级
			n, err = p.vp.ViableStarts(m.text, e, p.cur, 0, m.cands)
			if err != nil {
				m.fail(errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
					" 可行前缀回推失败: " + err.Error() + "; pattern=" + m.set.pats[i]))
				return
			}
			if n > len(m.cands) {
				m.fail(errors.New("re2native: match scanner2 pattern " + strconv.Itoa(i) +
					" 候选缓冲扩容后仍然不够 —— 这是【本库的 bug】; pattern=" + m.set.pats[i]))
				return
			}
		}
		m.st.Walks++
		m.st.Cands += int64(n)
		// ② 候选【从小到大】逐个验。缓冲是降序的, 所以倒着走 —— 第一个验过的就是 leftmost,
		//    而 p.fwd 是 longest 口径编的, 它给的右端就是最长的那个 ⟹ 严格 leftmost-longest。
		//    验不过说明这条"可行前缀"只是张空头支票 (能被某个后缀补成匹配, 但正文里跟着的
		//    不是那个后缀), 试下一个。
		got := false
		for k := n - 1; k >= 0; k-- {
			s := m.cands[k]
			m.st.Tries++
			loc := m.findCtx.FindStringIndexAtWithin(p.fwd, m.text, int(s), len(m.text))
			if loc == nil {
				continue
			}
			end := int32(loc[1])
			m.emit(i, s, end)
			p.cur = end
			got = true
			break
		}
		if !got {
			// 一个都没验过 ⟹ [cur, e) 里根本没有起点 (证明见文件头 ②), 游标直接推到 e。
			p.cur = e
		}
	}
}

// HitIDs 返回上一次 Scan 命中过的 pattern 下标 (无序 · 不重复), 与 Set.Match 同一张表。
func (m *MatchScanner2) HitIDs() []int32 { return m.hits }

// Hit 报第 i 条上一次 Scan 有没有命中 (O(1) 查表)。
func (m *MatchScanner2) Hit(i int) bool {
	return i >= 0 && i < len(m.hit) && m.hit[i]
}

// viableOne 返回第 i 条 pattern 自己那条【反向 · 只装这一条的 set】(惰性建, 建不出来返回 nil)。
// 只读用途 (ViableStarts), 并发安全。为什么是 set 不是单条 RegexpReverse: 见 regexpset.go
// 里 vp1 那段注释。
func (s *RegexpSet) viableOne(i int) *RegexpSetReverse {
	if i < 0 || i >= len(s.pats) {
		return nil
	}
	s.vp1Mu.Lock()
	defer s.vp1Mu.Unlock()
	if s.vp1 == nil {
		s.vp1 = make([]*RegexpSetReverse, len(s.pats))
		s.vp1No = make([]bool, len(s.pats))
	}
	if r := s.vp1[i]; r != nil {
		return r
	}
	if s.vp1No[i] {
		return nil // 上次就没建出来, 别再重编一遍 (失败是确定性的)
	}
	r, err := NewRegexpSetReverseMaxMem([]string{s.pats[i]}, s.maxMem)
	if err != nil {
		s.vp1No[i] = true
		return nil
	}
	s.vp1[i] = r
	return r
}

// ViableOneStats 报【已经被建出来】的那些"反向单条 set"的账: 几条 · 状态数合计 ·
// 状态区实际字节合计。与 ReverseOneStats 同一个用途 (量内存去哪了), 不制造状态。
// 惰性建 ⟹ 没被 MatchScanner2 问过位置的 pattern 一条都不占。
//
// 🔴 这是 D2 相对路 B 多出来的那笔【常驻】开销, 挂它之前先量这个数。
func (s *RegexpSet) ViableOneStats() (n int, states, arenaCap int64) {
	s.vp1Mu.Lock()
	defer s.vp1Mu.Unlock()
	for _, r := range s.vp1 {
		if r == nil {
			continue
		}
		mi := r.MemInfo()
		n++
		states += mi.States
		arenaCap += mi.ArenaCap
	}
	return n, states, arenaCap
}

// errClosedMatchScanner2 单独提出来, 免得每次构造一遍 error。
var errClosedMatchScanner2 = errors.New("re2native: match scanner2 closed")

// ── 什么时候该用哪个 ────────────────────────────────────────────────────────
//
//	MatchScanner (默认档 = 路 B)   通用默认。口径严格, 不需要额外的反向对象。
//	                              贵在"无上界的条目要走完空隙"。
//	MatchScanner (spanFast = 路 A) 只在【这条 pattern 自己 fuzz 出岔开 0 处】之后才挂。
//	                              口径是第三种, 见 matchscan.go 那一大节。
//	MatchScanner2 (路 D2)          要【严格 leftmost-longest】又不想付路 B 走空隙的钱时用。
//	                              代价是每条 pattern 多一份【反向 set】的 DFA 缓存 (惰性,
//	                              只有被真问到位置的那几条才建), 以及每个右端上"验了几个
//	                              假候选" —— 两笔都用 Stats() / MemInfo() 量, 别凭感觉。
