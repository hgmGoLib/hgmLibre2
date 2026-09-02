// regexpset.go — RegexpSet: 多正则「一次扫描·返回哪几条命中」的 litscan 风格 API。
//
// 动机: 调用方常把 N 条正则拼成 (?:re1)|(?:re2)|… 做一道"任一命中"快拒门, 命中后还得再逐条
// 跑一遍才知道是哪条。RE2::Set 把 N 条编进【一个 DFA】, 一遍扫就直接回答"哪几条命中"——
// 取代那道粗门, 且把"是哪条"的信息一并拿到, 命中后不必再逐条全跑 (只需要位置的调用方再对
// 命中条单独取 FindStringIndex)。语义 unanchored/partial (正文任意位置出现即命中)。
//
// ⚠ 只回答"哪几条", 不回答"在哪" (无位置)。需要 fragment/offset 的调用方拿到命中 index 后,
//    对那几条 (通常 0 条) 各跑一次 FindStringIndex 即可。
//
// 生命周期: NewRegexpSet 构建期一次 (编译 DFA), 之后只读、并发安全; Match 传入复用 buf 即零分配。
package hgmLibre2

/*
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"sort"
	"strconv"
	"unsafe"
)

// RegexpSet 是多正则集合 (构建期一次编译 · 扫描期只读 · 并发安全)。
type RegexpSet struct {
	h    *C.cre2_set
	size int // Add 成功的 pattern 数 (= Match 输出 index 的上界)
	// lens 是每条 pattern 的匹配字节长度区间 (见 patlen.go), 建集期算一次。
	// NewRe2Set_fll 靠它决定"这条的 start 怎么找": 定长 = 减法, 别的 = 回推。
	lens []patLen_t
	// pats / maxMem 留着建集期用 (以及给调用方查)。存的是切片头和调用方那份字符串, 不复制内容。
	pats   []string
	maxMem int64
}

// DefaultSetMaxMem 是 RE2 的默认内存预算 (RE2::Options::kDefaultMaxMem = 8MB)。
// NewRegexpSet 用的就是它; NewRegexpSetMaxMem 传 <=0 也回落到它。
const DefaultSetMaxMem int64 = DefaultMaxMem

// NewRegexpSet 把 patterns 顺序编进一个 RE2::Set (内存预算 = RE2 默认 8MB)。任一条解析失败 /
// 编译失败 → 返回 error (并释放已分配的 native 资源)。Match 输出的 index 即 patterns 的下标。
//
// 条数多了会撞上 8MB 这道【编译期】预算 (报 "set compile failed"), 此时别把表拆成两个 set,
// 用 NewRegexpSetMaxMem 调大预算即可。
func NewRegexpSet(patterns []string) (*RegexpSet, error) {
	return NewRegexpSetMaxMem(patterns, DefaultSetMaxMem)
}

// NewRegexpSetMaxMem 同 NewRegexpSet, 但显式指定 RE2 的内存预算 maxMem (字节; <=0 = 用默认 8MB)。
//
// maxMem 是 RE2::Options::max_mem, 一个旋钮同时抬两条天花板 (Prog::CompileSet 拿它一次算完):
//   - 编译期: 整个 set 的程序指令条数上限 ((maxMem-sizeof(Prog))/4/sizeof(Inst), 封顶 2^24)。
//     pattern 多 / 每条复杂 → 撞的是这条; 撞了 Compile 直接失败, 即本函数返回 error。
//   - 运行期: 剩下的额度给 DFA 状态缓存。缓存满了 DFA 自己 flush 重来 (只是变慢, 结果仍正确),
//     Set 不会退化成"没命中"。
//
// 怎么定: 从默认 8MB 起, 撞了就翻倍 (16/32/64MB) 直到 Compile 通过。构建期一次性开销, 之后常驻
// 只读; 内存换的是"一个 set 装下整表" —— 拆成两个 set 要扫两遍正文, 更贵。
func NewRegexpSetMaxMem(patterns []string, maxMem int64) (*RegexpSet, error) {
	return newRegexpSet(patterns, maxMem, false)
}

// newRegexpSet 是正向 (NewRegexpSetMaxMem) 与反向 (NewRegexpSetReverseMaxMem) 共用的构建体。
// 两者只差 cre2_set_new_ex 的 reversed 位, 其余 (逐条 Add · 编译 · finalizer · 错误文案) 一样。
func newRegexpSet(patterns []string, maxMem int64, reversed bool) (*RegexpSet, error) {
	if maxMem <= 0 {
		maxMem = DefaultSetMaxMem
	}
	crev := C.int(0)
	if reversed {
		crev = C.int(1)
	}
	h := C.cre2_set_new_ex(C.int64_t(maxMem), crev)
	if h == nil {
		return nil, errors.New("re2native: set out of memory")
	}
	s := &RegexpSet{h: h}
	for i, p := range patterns {
		if len(p) > maxCInt {
			C.cre2_set_free(h)
			return nil, errors.New("re2native: set pattern too large (>2GiB)")
		}
		if err := checkNoEmptyMatch("set pattern at index "+strconv.Itoa(i), p); err != nil { // 见 emptymatch.go
			C.cre2_set_free(h)
			return nil, err
		}
		idx := int(C.cre2_set_add(h, strBytePtr(p), C.int(len(p))))
		runtime.KeepAlive(p)
		if idx < 0 {
			C.cre2_set_free(h)
			return nil, errors.New("re2native: set bad pattern at index " + strconv.Itoa(i) + ": " + p)
		}
		s.size++
	}
	if C.cre2_set_compile(h) == 0 {
		C.cre2_set_free(h)
		// 唯一失败原因就是编译期预算不够 (RE2 Set::Compile → Prog::CompileSet 返回 NULL), 把
		// 当时的预算和条数写进 error, 免得调用方只看见一句 "out of memory" 不知道该调哪个旋钮。
		return nil, errors.New("re2native: set compile failed (out of memory): patterns=" +
			strconv.Itoa(s.size) + " maxMem=" + strconv.FormatInt(maxMem, 10) +
			" bytes; 用 NewRegexpSetMaxMem 把 maxMem 调大 (翻倍试) 即可, 不必拆成多个 set")
	}
	s.lens = buildPatLens(patterns)
	s.pats = patterns
	s.maxMem = maxMem
	runtime.SetFinalizer(s, func(x *RegexpSet) { C.cre2_set_free(x.h) })
	return s, nil
}

// GetPatternLen 返回集合里的 pattern 条数 (= Match 输出 index 的上界, 也是 buf 该开的长度)。
func (s *RegexpSet) GetPatternLen() int { return s.size }

// Match 扫 text 一遍, 把命中的 pattern index 写进 buf (传入复用切片避免每次分配) 并返回其前缀切片。
// 返回切片里每个元素是 patterns 的下标 (无序 · 不重复)。无命中返回长度 0 的切片。
//
// buf 用 int32 (= C.int): 直接给 cgo 回填, 避免 Go int(64位) 与 C.int(32位) 尺寸不符的拷贝。
func (s *RegexpSet) Match(text string, buf []int32) []int32 {
	if s.size == 0 || len(text) > maxCInt {
		return buf[:0]
	}
	if cap(buf) < s.size {
		buf = make([]int32, s.size)
	}
	buf = buf[:s.size]
	tp := strBytePtr(text)
	n := int(C.cre2_set_match(s.h, tp, C.int(len(text)),
		(*C.int)(unsafe.Pointer(&buf[0])), C.int(s.size)))
	runtime.KeepAlive(text)
	runtime.KeepAlive(s)
	if n < 0 {
		n = 0
	}
	if n > s.size {
		n = s.size // 防御: C 侧最多写 outcap 个, count 截断到 size
	}
	return buf[:n]
}

// ScanStats 是【一次 Match 调用】的 DFA 计数 (字段名与 C 侧 cre2_scan_stats 逐字一致)。
//
// 跟 GetDFAStats() 那份【进程级】计数是两回事: 那份挂在 re2 的全局钩子上, 回调不带上下文,
// 只能回答"这个进程里有人在 thrash"; 这份是调用方自己在栈上开的对象, 沿调用链传下去,
// 能回答"这一次调用发生了什么"。没有全局状态, 没有 thread_local, 并发下各算各的。
// 做法照搬 Rust regex-automata 的 per-Cache 计数 (clear_count / 已扫字节数)。
type ScanStats struct {
	// Flushes 是本次扫描里状态缓存被【整表清空】的次数。
	// 0 = 这次调用全程吃缓存; >0 = 这次调用有一段在"每个字节都重新造状态"的速度上跑
	// (慢两个数量级), 而且清空要拿写锁, 同时扫这个 Set 的其它 goroutine 全停。
	Flushes int64
	// Grows 是本次扫描里 arena 扩容的次数。扩容【不丢任何状态】, 只是把状态区 realloc 到
	// 更大再重定位 —— 是"缓存在长大", 不是 thrash。单独列出来免得跟 Flushes 混。
	Grows int64
	// StatesBuilt 是本次扫描新建的状态数 (缓存未命中才建)。
	// 稳态下趋近 0。⚠ 口径是 DFA 上一个累计计数器的前后差, 并发扫同一个 Set 时会把
	// 别的 goroutine 建的算进来 —— 要精确归因请单线程量。
	StatesBuilt int64
	// Bytes 是本次扫描的正文字节数。Bytes/Flushes 就是 Rust 那边判"缓存还有没有用"的比值。
	Bytes int64
	// StatesEnd 是扫完时缓存里的状态数。
	StatesEnd int64
	// StateBudget 是这个 Set 的 DFA 状态缓存额度 (字节, 约等于 maxMem 扣掉程序占用),
	// MemLeft 是扫完时的剩余额度。已用 = StateBudget - MemLeft; MemLeft 见底就是下次 Flush 的前夜。
	StateBudget int64
	MemLeft     int64
	// FellBack 只有 (*RegexpReverse).MatchStats 会置 true: 反向 DFA 这次没跑成 (反向程序
	// 编译不出来 / DFA 中途放弃), 结果是退回正向 MatchString 得到的 —— 答案仍然正确, 只是
	// 这次没省到状态, 其余字段全 0。RegexpSet 的 MatchStats 恒为 false。
	FellBack bool
}

// MatchStats 同 Match, 外加把【这一次扫描】的 DFA 计数写进 st (st 可为 nil)。
// st 不必预先清零。热路径上不想要这份开销就继续用 Match —— 不传 st 时 C 侧完全不统计。
func (s *RegexpSet) MatchStats(text string, buf []int32, st *ScanStats) []int32 {
	if st == nil {
		return s.Match(text, buf)
	}
	*st = ScanStats{}
	if s.size == 0 || len(text) > maxCInt {
		return buf[:0]
	}
	if cap(buf) < s.size {
		buf = make([]int32, s.size)
	}
	buf = buf[:s.size]
	var cs C.cre2_scan_stats
	n := int(C.cre2_set_match_stats(s.h, strBytePtr(text), C.int(len(text)),
		(*C.int)(unsafe.Pointer(&buf[0])), C.int(s.size), &cs))
	runtime.KeepAlive(text)
	runtime.KeepAlive(s)
	st.Flushes = int64(cs.Flushes)
	st.Grows = int64(cs.Grows)
	st.StatesBuilt = int64(cs.StatesBuilt)
	st.Bytes = int64(cs.Bytes)
	st.StatesEnd = int64(cs.StatesEnd)
	st.StateBudget = int64(cs.StateBudget)
	st.MemLeft = int64(cs.MemLeft)
	if n < 0 {
		n = 0
	}
	if n > s.size {
		n = s.size
	}
	return buf[:n]
}

// MatchStatsBytes 同 MatchStats, 但正文是 []byte (零拷贝)。
func (s *RegexpSet) MatchStatsBytes(text []byte, buf []int32, st *ScanStats) []int32 {
	hit := s.MatchStats(bytesStr(text), buf, st)
	runtime.KeepAlive(text)
	return hit
}

// SetMemInfo 是一个 RegexpSet 的 DFA 状态缓存水位 + 生涯累计。
type SetMemInfo struct {
	// Built=false 表示这个 Set 的 DFA 还没被建出来, 其余字段无意义。
	// 实测上 NewRegexpSet* 返回时它就已经是 true 了 —— RE2::Set::Compile 自己会跑一次冒烟
	// 搜索, 顺手把 DFA 建出来并留下 1 个状态。查询本身【不会】制造状态, 也不会替你建 DFA。
	Built bool
	// StateBudget 是状态缓存额度 (字节), MemLeft 是当前剩余。Used = StateBudget - MemLeft。
	StateBudget int64
	MemLeft     int64
	// States 是当前缓存里的状态数。
	States int64
	// ArenaCap 是实际向系统要到的状态区字节数。默认构建 (arena 按需翻倍) 下它才有意义:
	// 它 << StateBudget 说明"额度给多了也不占内存", 它逼近 StateBudget 说明快满了。
	ArenaCap int64
	// FlushesTotal / StatesBuiltTotal 是这个 Set 生涯里整表清空的次数 / 建过的状态数。
	// 前者就是 Rust regex-automata 那个 clear_count 的等价物, 只不过按 Set 归因。
	FlushesTotal     int64
	StatesBuiltTotal int64
}

// GetUsedBytes 返回状态缓存已用额度 (字节)。Built=false 时返回 0。
func (m SetMemInfo) GetUsedBytes() int64 {
	if !m.Built {
		return 0
	}
	return m.StateBudget - m.MemLeft
}

// PatternCost 是一条 pattern 在"造状态"这件事上的账 (要 -DRE2_DFA_ATTRIB=1 编译)。
type PatternCost struct {
	// Index 是它在 Set 里的下标 (与 Match 返回的下标同一套)。
	Index int
	// States 是有多少个新建状态里出现了这条 pattern 【独占】的 NFA 指令。
	// 🔴 非锚定搜索下这个数会【饱和】: DFA 在每个位置都得考虑"新的匹配可能从这里开始",
	// 所以每条 pattern 的入口指令躺在几乎每个状态里 ⇒ 大半条 pattern 都是 100%。
	// 它只能用来看"这条 pattern 有没有参与", 不能用来排序。
	States int64
	// Insts 是那些独占指令一共出现了多少次 (按状态加权)。这才是排序该看的数:
	// 一条 pattern 只有在它的容差窗口【正在悬着】的时候, 才会往状态里塞多个零件。
	Insts int64
	// Excess = Insts - States, 即"扣掉每状态都躺着的那 1 个入口指令之后, 多出来的零件数"。
	// 这是最干净的病灶指标: 窄窗口/纯字面量的 pattern 常年 Excess≈0, 共现型才会飙上去。
	Excess int64
}

// AttribInfo 回答"这几万个状态是谁造的、有多贵、在正文哪一段造的"。
//
// 🔴 要 CGO_CXXFLAGS=-DRE2_DFA_ATTRIB=1 编译才有数据, 否则 Enabled=false。
// 默认构建里这套采集代码根本不存在 (零字段零开销), 因为它只在排障时才有用。
//
// ⚠ 这是【归因】不是【因果】: "状态里有 #31 独占的指令"不等于"这个状态是 #31 一条造成的"。
// 共现型 pattern 的状态本来就是多条 pattern 的位集乘出来的, 这里给的是排序, 不是分解。
type AttribInfo struct {
	Enabled bool // 编译时没开 RE2_DFA_ATTRIB
	Built   bool // 这个 Set 还没扫过 (DFA 都没建)
	// StatesTotal 是生涯建过的状态数 (同 SetMemInfo.StatesBuiltTotal)。
	StatesTotal int64
	// SharedInsts 是落在"多条 pattern 共用"指令上的次数。最典型的是非锚定搜索开头
	// 那个 .* 循环 —— 它每条 pattern 都能走到, 归不了因, 所以单列。
	SharedInsts int64
	// NInstSum/StatesTotal = 平均"状态宽度"。宽度就是【单个状态的造价】:
	// 建一个状态是 O(ninst), 所以这两个数回答的是"状态变多了"还是"状态变胖了"。
	NInstSum int64
	NInstMax int64
	// NInstHist[i] = ninst 落在 [2^i, 2^(i+1)) 的状态数。
	NInstHist [16]int64
	// BirthHist[i] = 建状态时读到了正文的第 i/64 段。
	// 平坦 = 全篇都在造状态 (缓存对这种语料没用); 集中在几个桶 = 有特定文本形态在触发。
	BirthHist [64]int64
	// Pats 按 Excess 降序 (Excess 相同再看 Insts), 只含 States>0 的条目。
	Pats []PatternCost
}

// Attrib 查建状态的归因。只读, 短暂拿一次 DFA 读锁, 不会因为查询而建状态。
// 没开 RE2_DFA_ATTRIB 编译时返回 AttribInfo{Enabled: false}。
//
// 典型用法 (找出该从表里拎走的那几条):
//
//	a := set.GetAttrib()
//	for _, p := range a.Pats[:10] {
//	    fmt.Printf("#%d 多塞了 %d 个零件 (平均每状态 %.2f 个)\n",
//	        p.Index, p.Excess, float64(p.Excess)/float64(a.StatesTotal))
//	}
func (s *RegexpSet) GetAttrib() AttribInfo {
	var agg C.cre2_set_attrib
	n := s.size
	if n <= 0 {
		n = 1
	}
	ps := make([]int64, n)
	pi := make([]int64, n)
	C.cre2_set_attrib_info(s.h, &agg,
		(*C.int64_t)(unsafe.Pointer(&ps[0])), (*C.int64_t)(unsafe.Pointer(&pi[0])), C.int(n))
	runtime.KeepAlive(s)
	out := AttribInfo{
		Enabled:     agg.Enabled != 0,
		Built:       agg.Built != 0,
		StatesTotal: int64(agg.StatesTotal),
		SharedInsts: int64(agg.SharedInsts),
		NInstSum:    int64(agg.NInstSum),
		NInstMax:    int64(agg.NInstMax),
	}
	for i := range out.NInstHist {
		out.NInstHist[i] = int64(agg.NInstHist[i])
	}
	for i := range out.BirthHist {
		out.BirthHist[i] = int64(agg.BirthHist[i])
	}
	if !out.Enabled {
		return out
	}
	npat := int(agg.NPat)
	if npat > n {
		npat = n
	}
	for i := 0; i < npat; i++ {
		if ps[i] > 0 {
			out.Pats = append(out.Pats, PatternCost{
				Index: i, States: ps[i], Insts: pi[i], Excess: pi[i] - ps[i]})
		}
	}
	sort.Slice(out.Pats, func(a, b int) bool {
		if out.Pats[a].Excess != out.Pats[b].Excess {
			return out.Pats[a].Excess > out.Pats[b].Excess
		}
		if out.Pats[a].Insts != out.Pats[b].Insts {
			return out.Pats[a].Insts > out.Pats[b].Insts
		}
		return out.Pats[a].Index < out.Pats[b].Index
	})
	return out
}

// GetMemInfo 查这个 Set 当前的 DFA 缓存水位: 额度用掉多少、装了多少状态、生涯清空过几次。
// 只读, 内部短暂拿一次 DFA 读锁, 可以和扫描并发调 (会和正在 flush 的写锁互斥, 别在热路径上高频调)。
func (s *RegexpSet) GetMemInfo() SetMemInfo {
	var mi C.cre2_set_mem
	C.cre2_set_mem_info(s.h, &mi)
	runtime.KeepAlive(s)
	return SetMemInfo{
		Built:            mi.Built != 0,
		StateBudget:      int64(mi.StateBudget),
		MemLeft:          int64(mi.MemLeft),
		States:           int64(mi.States),
		ArenaCap:         int64(mi.ArenaCap),
		FlushesTotal:     int64(mi.FlushesTotal),
		StatesBuiltTotal: int64(mi.StatesBuiltTotal),
	}
}

// MatchAny 报告 text 是否命中集合里【任一】正则 —— 【第一个命中位置就返回】, 不把正文扫完。
//
// 与 len(Match(...))>0 的差别不只是省一个切片: Match 要回答"哪几条", DFA 必须走到正文末尾才
// 知道命中集全不全; MatchAny 不取 index, 底下 RE2 的 SearchDFA 就打开 want_earliest_match,
// 扫到第一个命中位置立刻收工 —— 命中越早、正文越长, 省得越多 (不命中仍然是全扫一遍)。
// 因为不回填 index, 这里也不需要调用方传 buf。
//
// 走的是与 Match 同一份 DFA 状态缓存 (kManyMatch 那一份), 不会因为多这条快路径而多占一份。
func (s *RegexpSet) MatchAny(text string) bool {
	if s.size == 0 || len(text) > maxCInt {
		return false
	}
	hit := C.cre2_set_match_any(s.h, strBytePtr(text), C.int(len(text))) != 0
	runtime.KeepAlive(text)
	runtime.KeepAlive(s)
	return hit
}

// MatchBytes 同 Match, 但正文是 []byte (零拷贝喂给同一内核, 不做 string(text) 全量拷贝)。
// 供正文本来就是 []byte 的调用方直接用; 语义/返回值与 Match 完全一致。见 bytes.go 的说明。
func (s *RegexpSet) MatchBytes(text []byte, buf []int32) []int32 {
	hit := s.Match(bytesStr(text), buf)
	runtime.KeepAlive(text)
	return hit
}

// MatchAnyBytes 同 MatchAny, 但正文是 []byte (零拷贝)。
func (s *RegexpSet) MatchAnyBytes(text []byte) bool {
	hit := s.MatchAny(bytesStr(text))
	runtime.KeepAlive(text)
	return hit
}
