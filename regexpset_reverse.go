// regexpset_reverse.go —— RegexpSetReverse: 反着编译的多正则集合, 与正向的 RegexpSet
// 是【两个类型】, 不是一个类型上的一个开关。
//
// ── 为什么必须拆成两个类型 ──────────────────────────────────────────────────
// ① 两份完全不同的 DFA 状态缓存。正向和反向是两套程序 (Prog / ReverseProg), 各自挂各自的
//    状态缓存。混在一个对象里, GetMemInfo() 报的是哪一份就说不清了 —— 而"状态缓存有多大"
//    正是这个库唯一真正要盯的成本。
// ② 两边的贵法差着三个数量级, 而这件事必须写在类型上让人看见。真表实测: 155 条正向 set
//    扫 6.4MB = 18ms / 零 flush; 同一张表反向 = 65 秒 / arena 顶满 254MB 还在 flush
//    (set 里的状态数是【相乘】的, 见 doc/状态数为什么会相乘.md)。藏在一个 Reverse() bool
//    后面, 调用方不会知道自己刚踩了什么。
// ③ 两边吐的位置【含义相反】: 正向吐匹配的右端, 反向吐左端。同一个方法名返回意思相反的
//    数字, 是最容易写错又最难查的那种错。分成两个类型, 参数名就能直说 (endLo/endHi 对
//    startLo/startHi)。
//
// 这也是本库单条正则早就在用的形状 (Regexp / RegexpReverse, 见 reverse.go)。
// set 这边一直是个例外, 现在补齐。
//
// ── 只做两件事 ──────────────────────────────────────────────────────────────
//	Match / MatchAny      "哪几条命中" —— 与正向【逐条相同】(同一条语言换个方向读, 不是近似)
//	FindAllIndex          "各条的匹配【左端】落在哪一段"
//	ResolveSpan*          给一个右端, 锚定回推出左端 (不扫正文)
//
// 反向 set 最主要的用途【不是】扫正文, 是最后那个 ResolveSpan*: 单点、锚定、有界,
// 代价只跟"回看多远"有关, 与正文长度无关。要拿它扫全文之前先量一遍 GetMemInfo().Flushes。
package hgmLibre2

// RegexpSetReverse 是反向编译的多正则集合 (构建期一次编译 · 扫描期只读 · 并发安全)。
// 用 NewRegexpSetReverseMaxMem 建。
type RegexpSetReverse struct {
	// s 是内部那份反向编译的 set。不导出 —— 拿到它就能调到正向语义的方法上去,
	// 而这个类型存在的全部意义就是不让那件事发生。
	s *RegexpSet
}

// GetPatternLen 返回集合里的 pattern 条数 (= Match 输出 index 的上界, 也是 buf 该开的长度)。
func (r *RegexpSetReverse) GetPatternLen() int { return r.s.size }

// Match 从正文【末尾往前】扫一遍原始 buffer (不反转正文, 不复制正文), 把命中的 pattern index
// 写进 buf 并返回其前缀切片。命中集与正向 (*RegexpSet).Match 逐条相同, 只是扫的方向反了。
func (r *RegexpSetReverse) Match(text string, buf []int32) []int32 { return r.s.Match(text, buf) }

// MatchBytes 同 Match, 但正文是 []byte (零拷贝)。
func (r *RegexpSetReverse) MatchBytes(text []byte, buf []int32) []int32 {
	return r.s.MatchBytes(text, buf)
}

// MatchAny 报告 text 是否命中集合里【任一】正则 —— 第一个命中位置就返回, 不把正文扫完。
func (r *RegexpSetReverse) MatchAny(text string) bool { return r.s.MatchAny(text) }

// MatchAnyBytes 同 MatchAny, 但正文是 []byte (零拷贝)。
func (r *RegexpSetReverse) MatchAnyBytes(text []byte) bool { return r.s.MatchAnyBytes(text) }

// MatchStats 同 Match, 外加把【这一次扫描】的 DFA 计数写进 st (st 可为 nil)。
//
// 这是标定"这张表该正着扫还是反着扫"的量器: 同一批 pattern 建一个 RegexpSet 和一个
// RegexpSetReverse, 拿同一批真语料各跑一遍, 比 Flushes (>0 = 在悬崖上) 与 StatesEnd。
func (r *RegexpSetReverse) MatchStats(text string, buf []int32, st *ScanStats) []int32 {
	return r.s.MatchStats(text, buf, st)
}

// MatchStatsBytes 同 MatchStats, 但正文是 []byte (零拷贝)。
func (r *RegexpSetReverse) MatchStatsBytes(text []byte, buf []int32, st *ScanStats) []int32 {
	return r.s.MatchStatsBytes(text, buf, st)
}

// GetMemInfo 查这个 set 当前的 DFA 缓存水位 (额度用掉多少 · 装了多少状态 · 生涯清空过几次)。
// 反向 set 上这个数尤其该看 —— 反向的状态数是每条各自最坏情况【相乘】出来的。
func (r *RegexpSetReverse) GetMemInfo() SetMemInfo { return r.s.GetMemInfo() }

// Attrib 查建状态的归因 (要 -DRE2_DFA_ATTRIB=1 编译), 含义同 (*RegexpSet).Attrib。
func (r *RegexpSetReverse) GetAttrib() AttribInfo { return r.s.GetAttrib() }
