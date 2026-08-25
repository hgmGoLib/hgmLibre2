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
//	SetWanted(mask)            【哪几条要位置】。门上很多位只当 bool 用 (某某类内容在不在),
//	                           从来没人问它在哪 —— 那几条一个字节都不该攒。默认全要。
//	Scan(text)                 一遍全文。命中表 (HitIDs / Hit, 与 Set.Match 同解) +
//	                           要位置的那几条的命中区间, 【边扫边收口】。
//	AppendMatches(dst, id)     取第 id 条的结果 (照抄一份, 不再算)。
//
// 🔴 边扫边收口是【内存】上的要求, 不是风格。native 那层本来就是 sqlite3_step 式的:
//    一批 4096 条游程 (48KB) 装满就挂起、回调给 Go、取走再进去接着扫, 缓冲循环复用, 不随
//    正文长度涨。要是 Go 这侧把每一批都 append 攒起来等扫完再算, 那个固定缓冲就白设计了 ——
//    实测真表上游程约 30741 条/MB (0.23MB/MB), 200MB 的 body 就是每个并发扫描 47MB。
//    所以游标推进在回调里当场跑完, Go 这侧留下的只有【输出】(去重后的命中区间), 而输出是
//    调用方本来就要拿走的东西 —— 而且比老路的 [][]int 还省 (扁平 int32 对 vs 每处一个切片头)。
//    真表实测: 游程 196744 条 → 输出 74249 处, 而且其中 57% 的游程来自两条【只当 bool 用】的
//    pattern, SetWanted 一挡就没了。
//
// 能这么做是因为同一条 pattern 的游程【跨批次也是升序】的 (正向扫本来就从左往右走)。
// 万一不是 (真出现了乱序), 那一条当场标成"补不出来", AppendMatches 返回 ok=false 让调用方
// 走老路 —— 宁可退回去也不给错答案。
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
// 🔴 这套推进【只有在起点随右端单调不减时】才等于 FindAll。反例: abc|b 撞 "abc" 的右端是
//    2 和 3 —— 右端 2 回推到起点 1 ("b"), 当场吐 [1,2) 把 cur 推到 2, 右端 3 那个真正的
//    [0,3)="abc" 就因为起点 0 < cur 被永远跳过, 【无声漏掉】。
//    造出这种非单调的是"一条更短的分支结在另一条更长分支【内部】"—— 交替 (abc|b) 会,
//    而 ? * + {m,n}(min≠max) 也都是【长度不齐的交替】: (?:ab)? 就是 ab| 。所以这件事
//    在整个变长档上是常态, 不是某几条 pattern 的毛病。
//
// 🔴 【只在同一条 pattern 内部去重, 跨 pattern 一概不合并】。两条 pattern 在同一片正文上各自
//    命中不是重复, 是两个问题各要一个答案 (带空格的和不带空格的两条 pattern, 下游正是靠
//    "这一段里有没有空格"分流的; 合了就是漏检)。
//    实例 (doc/plan12/20260825_209re2setNoOverlap.txt 有全文): "Passport No: A123456780"
//    上 \b[A-Z][12]\d{8}\b (台湾身份证) 与 (?i)\b[A-Z]{1,2}\d{7,9}[A-Z]?\b (护照号) 抢
//    【完全同一段】[13,23)。合并的话下标在前的台湾身份证先占, 而它自己又过不了 mod-10
//    校验位被消费点毙掉 ⟹ 这段明文护照号一条都不出。"谁赢"只由常量写在第几行决定, 而
//    "谁能活"要等消费点把校验位跑完才知道 —— 库这层两样都不知道。真语料上被 ≥2 条 pattern
//    盖住的字节占已盖住字节的 55.6%, 同一字节最多被 8 条盖, 所以这是常态不是边角。
//
// ── 和 FindAllStringIndex 的关系: 分两档, 只有一档敢说"一样" ─────────────────
//
//	定长档 (min == max)      与 FindAllStringIndex 【逐字节相同】, 而且是可以论证的:
//	                         右端 e 的起点只能是 e-min (唯一), 所以"起点随右端单调"必然成立;
//	                         长度也唯一, 不存在"取最长还是取贪心先撞上的"之分。剩下的就是
//	                         "从左往右贪心取不相交", 那正是 FindAll 的规则。
//	                         回归钉在 TestMatchScanStrictVsFindAll + 6 万条随机定长 pattern 对拍。
//
//	变长档 (min < max)       【第三种口径】, 既不是 leftmost-first (贪心 / FindAll), 也不是
//	                         leftmost-longest (POSIX / re.Longest())。只保证三条:
//	                         ① text[Lo:Hi] 是这条 pattern 的一个【真匹配】
//	                         ② 同一条吐的区间互不相交、按 Lo 升序
//	                         ③ 有匹配的地方一定吐点什么 (不整段静默)
//
// 🔴 为什么必然是第三种, 而不是"选一个模式就行"—— 这一遍扫描给的是【右端集合】
//    {e | 存在某个 s 使 text[s:e] 匹配}, 里面【没有】(起点,终点) 的配对信息, 也【没有】
//    优先序: RE2 的贪心就活在 NFA 指令的优先序里 (re2_dfa.cc:1197 kFirstMatch 撞到 Match
//    就 break, 把后面低优先级的线程整段截掉), 而 kLongestMatch 干脆把每段 std::sort 掉
//    (:1272), kManyMatch 连 Mark 都没有、整串 sort (:1289) —— 我们要"所有右端"就只能用
//    kManyMatch, 而它正是把优先序扔得最干净的那个。一遍搜索本来也只吐【一个】右端
//    (want_earliest_match 第一处就 return :2562, 否则跑完取 lastmatch :2631)。
//    所以"拿到全部右端"和"贪心/最长"是互斥的, 配对只能在 Go 这侧用游标启发式【重建】,
//    重建出来的就是上面那第三种。
//
//    实测 (随机撒 3000 条变长 pattern × 40 段正文 = 12 万处对账):
//      与 FindAll(贪心) 相同  119940 / 120000
//      与 Longest(最长) 相同  119972 / 120000
//      两个都不是                 28 / 120000
//    差的那些长这样:
//      x{1,3}[a-c]?(?:ab|cd)?  撞 "xab"        本路 [0,3)="xab"   FindAll [0,2)="xa"
//      (?:ab)?[bc]{1,2}        撞 "axbabbyxx"  本路 [4,6)="bb"    FindAll [3,6)="abb"
//      (?:ab)*b{1,3}           撞 "yaxyabbbb"  本路 [5,8)+[8,9)   FindAll [4,9)="abbbb"
//
// 🔴 差出来的段【是真匹配, 但比 FindAll 那一处长/短/偏】。对于拿这一段去过校验位的下游
//    (身份证 · IBAN mod-97 · Luhn), 边界差一个字节就是校验失败 = 无声漏报 —— 比"没检测到"
//    更难查。所以: 要"和老路逐字节一致"的调用方【只准用定长档】; 变长档的位置只能当
//    "这附近有东西"的定位用, 别切片再判。
//
//    (曾经有过一道 PatternLeftmostLongestSafe 静态闸, 想把"最长 ≠ 贪心"的 pattern 挡在
//     门外。2026-08-25 删掉了: 它只查 OpAlternate, 而 ? * + {m,n} 同样是长度不齐的交替,
//     把口子堵严的话它就退化成 min == max 本身 —— 也就是变长快路整个清空。既然它兑现不了
//     "等于 FindAll"这个承诺, 就不该拿 8 倍的钱去买: 真表上闸装着 1.82×, 拆掉 15.01×。)
//
// ok=false 的那几条 (没在 SetWanted 里 / 能匹配空串 / 反向编不出来 /
// 游程乱序 / DFA 预算不够) 请调用方照老路对它跑 FindAll ——
// 库这边宁可退回去也不给一个"像是对的"答案。
//
// 生命周期同 SpanScanner: 可复用工作区, 热路径上建一次留着, 【不是并发安全的】。
// Scan 之后 text 一直被 scanner 引用着 (AppendMatches 要用), 直到下一次 Scan 或 Reset。
package hgmLibre2

import "errors"

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
	per  []msPat_t
	want []bool // nil = 全要
	hit  []bool
	hits []int32 // 上一遍命中过的下标 (= Set.Match 那张表), 去重且只含真命中
	err  error   // 回调里出的错 (回调没法返 error)
}

// msPat_t 是每条 pattern 的推进状态。cur 就是那把游标, out 是已经收口的输出。

type msPat_t struct {
	inited bool
	fixed  bool
	bad    bool // 补不出左端 (能匹配空串 / 反向编不出来 / 游程乱序) → 调用方走老路
	minL   int32
	rev    *RegexpSet
	cur    int32 // 已吐出去的最右字节
	lastLo int32 // 上一条游程的左端, 用来确认升序
	out    []int32
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
		set: s,
		sc:  sc,
		per: make([]msPat_t, s.size),
		hit: make([]bool, s.size),
	}, nil
}

// SetWanted 声明【哪几条要位置】(下标即 pattern 下标, 长度不足的当 false)。传 nil = 全要。
// 没要位置的那几条照样进命中表 (Hit/HitIDs), 只是一个游程都不攒、一次左端都不补 ——
// 真表上光这一挡就去掉 57% 的游程。调用方那边这是【静态】信息: 哪几个门分支的消费点会问
// 位置, 哪几个只是外层短路的 bool, 建集的时候就知道。
func (m *MatchScanner) SetWanted(mask []bool) { m.want = mask }

func (m *MatchScanner) wants(i int) bool {
	if m.want == nil {
		return true
	}
	return i < len(m.want) && m.want[i]
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
		p := &m.per[id]
		p.inited, p.fixed, p.bad = false, false, false
		p.cur, p.lastLo = 0, 0
		p.out = p.out[:0]
		m.hit[id] = false
	}
	m.hits = m.hits[:0]
	m.text = text
	m.err = nil
	err := m.sc.Scan(text, func(spans []SetSpan) bool {
		for _, sp := range spans {
			i := int(sp.Index)
			if i < 0 || i >= len(m.per) {
				continue
			}
			if !m.hit[i] {
				m.hit[i] = true
				m.hits = append(m.hits, sp.Index)
			}
			if !m.wants(i) {
				continue // 只当 bool 用的那几条: 到此为止, 一个字节不攒
			}
			m.feed(i, sp.Lo, sp.Hi)
		}
		return m.err == nil
	})
	if err != nil {
		return err
	}
	return m.err
}

// feed 把一条游程 [lo,hi] (都是右端偏移) 喂给第 i 条的游标, 当场推进并收口。
func (m *MatchScanner) feed(i int, lo, hi int32) {
	p := &m.per[i]
	if p.bad {
		return
	}
	if !p.inited {
		p.inited = true
		minL, maxL := m.set.PatternLenRange(i)
		if minL <= 0 {
			// 能匹配【空串】的 pattern 不走这条路: 每个位置都是一处零长命中, 游标压不住,
			// 吐出来的 text[Lo:Lo] 对下游也没有意义。这种交给老路。
			p.bad = true
			return
		}
		p.minL = int32(minL)
		p.fixed = minL == maxL && maxL >= 0
		if !p.fixed {
			// 变长: 起点靠这一条自己的反向 set 锚定回推。给出来的口径不是 FindAll ——
			// 见文件头"变长档"那一节, 调用方自己判断能不能用。
			if p.rev = m.set.reverseOne(i); p.rev == nil {
				p.bad = true
				return
			}
		}
	}
	if lo < p.lastLo {
		p.bad = true // 游程乱序 —— 推进的前提没了, 宁可退回老路也不给错答案
		return
	}
	p.lastLo = lo

	for e := lo; e <= hi; e++ {
		if e <= p.cur {
			continue // 落在已吐出去那一处里面
		}
		if p.fixed {
			// 定长: 起点唯一 (e-minL), 一句减法, 不进正则引擎。
			start := e - p.minL
			if start < p.cur {
				continue // 与已吐出去那一处相交
			}
			p.out = append(p.out, start, e)
			p.cur = e
			continue
		}
		// 🔴 bound = cur: 回看【绝不越过游标】。是正确性不是省钱, 见文件头 ②。
		// 顺带它把回看代价钉成"离游标多远", 与正文长度无关。
		start, ok, err := p.rev.ResolveSpanWithin(m.text, e, p.cur, 0)
		if err != nil {
			// DFA 放弃 (预算不够) —— 不是"没有匹配", 也不该把整遍扫描带崩。
			// 这一条当场作废, AppendMatches 返回 ok=false, 调用方照老路 FindAll。
			p.bad = true
			return
		}
		if !ok {
			continue
		}
		// 从这个起点取【最长】右端 —— 变长 pattern 在同一起点上有一串长度都成立,
		// 取最短就是把命中截断, 下游拿去做定长校验会把真命中判成假。
		end := e
		pos, ok, err := m.set.ResolveSpan(m.text, start, int32(i))
		if err != nil {
			p.bad = true
			return
		}
		if ok && pos > end {
			end = pos
		}
		p.out = append(p.out, start, end)
		p.cur = end
	}
}

// HitIDs 返回上一次 Scan 命中过的 pattern 下标 (无序 · 不重复), 与 Set.Match 给的是同一张表。
// 切片下次 Scan 会被覆写。
func (m *MatchScanner) HitIDs() []int32 { return m.hits }

// Hit 报第 i 条上一次 Scan 有没有命中 (O(1) 查表)。
func (m *MatchScanner) Hit(i int) bool {
	return i >= 0 && i < len(m.hit) && m.hit[i]
}

// AppendMatches 把第 id 条 pattern 的命中区间以扁平 (lo,hi) 对 append 进 dst 并返回。
// 结果在 Scan 那一遍里就算好了, 这里只是照抄一份。
//
// ok=false 表示这一条这次拿不到 (没在 SetWanted 里 / 能匹配空串 / 反向编不出来 / 游程乱序)
// —— 调用方照老路对它跑一遍 FindAll。没命中的条返回 dst 原样 + ok=true。
func (m *MatchScanner) AppendMatches(dst []int32, id int32) (out []int32, ok bool, err error) {
	if m.sc == nil {
		return dst, false, errClosedMatchScanner
	}
	i := int(id)
	if i < 0 || i >= len(m.per) || !m.hit[i] {
		return dst, true, nil
	}
	if !m.wants(i) || m.per[i].bad {
		return dst, false, nil
	}
	return append(dst, m.per[i].out...), true, nil
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
