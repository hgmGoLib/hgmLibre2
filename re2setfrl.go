// re2setfrl.go — Re2SetFrl: 一遍正向扫描, 直接交出不重叠的命中区间。
//
// 名字: F = first pass forward (第一趟正着扫), RL = rightmost-longest (去重叠的口径)。
//
// 替的还是那套两段式 —— 先 Set.Match 扫一遍拿"哪几条命中", 再为了知道【在哪】把命中的
// 每条各对整篇正文跑一遍 FindAllStringIndex (命中 k 条 = 1+k 遍全文)。与 MatchScanner
// 相比, 这一层把补起点的次数从"每个右端问一次"压到"每个分量里真有几处不重叠的匹配":
//
//	扫正文    整表正向 set 的 kManyMatch DFA, 一遍。
//	切分量    DFA 状态里带一个 per-pattern【存活位】—— 某条 pattern 在某个偏移上没有
//	          活线程, 就没有任何匹配能跨过这个偏移, 于是它左右两侧的命中互不影响。
//	          由活转死就当场把它挂着的那一段命中收口成一个【分量】。
//	          分量左界白送给下一步当反向锚定的 bound —— 回看不必走到正文开头。
//	结算      分量内部从最右的右端起: 反向锚定拿最靠左的左端 (= 这个右端上最长的匹配),
//	          收下, 然后把上界压到它的左端, 下一处必须整个落在左边。所以一个分量里
//	          问几次反向锚定 = 这个分量里真有几处不重叠的匹配。
//
// 🔴 几乎全部逻辑在 C++ 里 (cre2_frl.cpp + re2_dfa_spanscan_inl.h), 命中【不逐条过桥】:
//    游程攒在 native 侧, 分量整块结算, 一次 Step 把一批 (Index, Start, End) 直接写进
//    调用方给的三个 []int32。Go 侧只有 begin/step 两次 cgo 往返 × 批数。
//
// ── 口径 (逐字读) ────────────────────────────────────────────────────────────
//	① text[Start:End] 是第 Index 条 pattern 的一个【真匹配】;
//	② 【同一条 pattern】交出来的区间互不相交, 按 Start 升序;
//	③ 去重叠的口径是 rightmost-longest —— 从右往左"谁先占坑", 与 MatchScanner 的
//	   leftmost-longest 是照镜子的关系。两个真匹配不交叠的地方两者逐处相同; 交叠时
//	   才分家 (ab|b 撞 "aab": 最左最长给 [1,3)="ab", 最右最长给 [2,3)="b")。
//	   要与 stdlib 的 re.Longest().FindAllStringIndex 逐字节对上就用 MatchScanner;
//	   只是要"把这片正文里的东西都框出来"(脱敏 · 定位 · 计数), 这个更省。
//	④ 跨 pattern 一概不合并, 两条 pattern 在同一片正文上照样重叠 —— 那不是重复,
//	   是两个问题各要一个答案。批与批之间不同 pattern 的结果【交错】着来。
//
// 🔴 【要么全给, 要么整遍不算数】: Scan 返回 err 就是这一遍作废 (已经交出去的批也不算),
//    整篇请走老路 FindAll。没有"这几条没给全, 你自己补"的中间态。
package hgmLibre2

/*
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
	"unsafe"
)

// Re2SetFrlPattern_t 是初始化 Re2SetFrl 用的一条。
//
// ExistOnly = 这一条只要"有没有命中"(扫完看 Hit(i)), 不要 Start/End。挡掉的是这一层
// 真花钱的那步 —— 攒游程 + 盯存活位 + 收口 + 补起点, 不只是少交几处结果。门上很多位
// 只当外层短路的 bool 用, 从来没人问它在哪, 真表上光两条这样的 pattern 就能占掉一多半
// 的游程。回调里自己过滤顶替不了: 那是钱已经花完了才扔。
type Re2SetFrlPattern_t struct {
	Pattern   string
	ExistOnly bool
}

// Re2SetFrlBuf_t 是交给 C 侧写结果的批缓冲 (三个等长数组)。复用同一个 ⇒ 稳态零分配。
// 不是并发安全的, 并发各持一个。
type Re2SetFrlBuf_t struct {
	Index []int32
	Start []int32
	End   []int32
}

// NewRe2SetFrlBuf 开一个能装 n 条的批缓冲。n 给几百到几千都行 —— 它只决定一次 Step
// 交多少条 (过桥次数), 不影响结果。
func NewRe2SetFrlBuf(n int) *Re2SetFrlBuf_t {
	if n < 1 {
		n = 1
	}
	return &Re2SetFrlBuf_t{
		Index: make([]int32, n),
		Start: make([]int32, n),
		End:   make([]int32, n),
	}
}

// Re2SetFrl 见文件头。建一次留着反复 Scan; 【不是】并发安全的 (工作区有状态),
// 并发各建一个, 或者各自加锁。
type Re2SetFrl struct {
	h    *C.cre2_frl
	size int
}

var errClosedRe2SetFrl = errors.New("re2native: Re2SetFrl 已经 Close")

// NewRe2SetFrl 用一组 (正则, 要不要区间) 建工作区。内存预算走 RE2 默认 8MB。
func NewRe2SetFrl(pats []Re2SetFrlPattern_t) (*Re2SetFrl, error) {
	return NewRe2SetFrlMaxMem(pats, 0)
}

// NewRe2SetFrlMaxMem 同上, 但显式给内存预算 maxMem (字节; <=0 = RE2 默认 8MB)。
// 这个预算同时管【整表正向 set】和【惰性建出来的每条单条对象】。表大就得调大 ——
// 判据是扫一批互不相同的真 body 之后 Stats() 里 DFA 有没有在 flush (见 readme.txt)。
//
// 🔴 能匹配空串的 pattern (PatternLenRange 的 min <= 0) 只允许配 ExistOnly, 否则这里
//    当场报错: 每个偏移都是一处零长命中, 游程会长到与正文同量级, 分量也切不动。
//    这与正文无关, 建工作区那一刻就定死 —— 所以它能直接写成回归测试。
func NewRe2SetFrlMaxMem(pats []Re2SetFrlPattern_t, maxMem int64) (*Re2SetFrl, error) {
	n := len(pats)
	if n == 0 {
		return nil, errors.New("re2native: Re2SetFrl 至少要一条 pattern")
	}
	for i := range pats {
		if pats[i].ExistOnly {
			continue
		}
		if minL, _ := PatternLenRange(pats[i].Pattern); minL <= 0 {
			return nil, errors.New("re2native: Re2SetFrl 第 " + strconv.Itoa(i) +
				" 条能匹配空串, 只能配 ExistOnly=true (或走老路 FindAll): " + pats[i].Pattern)
		}
	}
	// pattern 原文要交给 C 侧存一份 (惰性编单条对象时还要用), 所以这里过一次 C 堆。
	cpats := make([]*C.char, n)
	clens := make([]C.int, n)
	cbool := make([]C.uchar, n)
	for i := range pats {
		if len(pats[i].Pattern) > maxCInt {
			for j := 0; j < i; j++ {
				C.free(unsafe.Pointer(cpats[j]))
			}
			return nil, errors.New("re2native: Re2SetFrl pattern 太长 (>2GiB)")
		}
		cpats[i] = C.CString(pats[i].Pattern)
		clens[i] = C.int(len(pats[i].Pattern))
		if pats[i].ExistOnly {
			cbool[i] = 1
		}
	}
	h := C.cre2_frl_new(&cpats[0], &clens[0], &cbool[0], C.int(n), C.int64_t(maxMem))
	for i := range cpats {
		C.free(unsafe.Pointer(cpats[i]))
	}
	if h == nil {
		return nil, errors.New("re2native: Re2SetFrl 建不出来 (OOM)")
	}
	var bad C.int
	if e := C.cre2_frl_error(h, &bad); e != nil {
		msg := C.GoString(e)
		C.cre2_frl_free(h)
		if int(bad) >= 0 && int(bad) < n {
			return nil, errors.New("re2native: " + msg + " (第 " + strconv.Itoa(int(bad)) +
				" 条: " + pats[int(bad)].Pattern + ")")
		}
		return nil, errors.New("re2native: " + msg)
	}
	return &Re2SetFrl{h: h, size: n}, nil
}

// Close 放掉整表 DFA 缓存和惰性建出来的那些单条对象。之后再用返回 err。
func (s *Re2SetFrl) Close() {
	if s == nil || s.h == nil {
		return
	}
	C.cre2_frl_free(s.h)
	s.h = nil
}

// Size 是 pattern 条数 (= Index 的上界)。
func (s *Re2SetFrl) Size() int { return s.size }

// Scan 扫一遍 text, 一批一批把命中区间交给 fn。fn 返回 false = 提前收工, 剩下的正文不再扫
// (这不算错, Scan 返回 nil)。
//
// 🔴 交给 fn 的三个切片是 buf 里那三块【内部缓冲本身】, 下一批原地覆写。要留自己拷。
// 🔴 err != nil 就是这一遍作废 (已经交出去的批也不算数), 整篇走老路 FindAll。
func (s *Re2SetFrl) Scan(text string, buf *Re2SetFrlBuf_t, fn func(Index, Start, End []int32) bool) error {
	if s == nil || s.h == nil {
		return errClosedRe2SetFrl
	}
	if len(text) > maxCInt {
		return errors.New("re2native: Re2SetFrl 正文太大 (>2GiB)")
	}
	if buf == nil || len(buf.Index) == 0 ||
		len(buf.Start) != len(buf.Index) || len(buf.End) != len(buf.Index) {
		return errors.New("re2native: Re2SetFrl 批缓冲不对 (三个数组要等长且非空, 用 NewRe2SetFrlBuf)")
	}
	if C.cre2_frl_begin(s.h, C.int(len(text))) == 0 {
		return s.lastErr("begin 失败")
	}
	tp := strBytePtr(text)
	tn := C.int(len(text))
	cap_ := C.int(len(buf.Index))
	pi := (*C.int32_t)(unsafe.Pointer(&buf.Index[0]))
	ps := (*C.int32_t)(unsafe.Pointer(&buf.Start[0]))
	pe := (*C.int32_t)(unsafe.Pointer(&buf.End[0]))
	var more C.int
	for {
		n := int(C.cre2_frl_step(s.h, tp, tn, pi, ps, pe, cap_, &more))
		runtime.KeepAlive(text)
		runtime.KeepAlive(buf)
		if n < 0 {
			return s.lastErr("扫描失败")
		}
		if n > 0 && fn != nil {
			if !fn(buf.Index[:n], buf.Start[:n], buf.End[:n]) {
				return nil
			}
		}
		if more == 0 {
			return nil
		}
	}
}

// lastErr 把 C 侧那句话取出来 (取不到就用 fallback)。
func (s *Re2SetFrl) lastErr(fallback string) error {
	var bad C.int
	if e := C.cre2_frl_error(s.h, &bad); e != nil {
		msg := C.GoString(e)
		if int(bad) >= 0 {
			msg += " (第 " + strconv.Itoa(int(bad)) + " 条 pattern)"
		}
		return errors.New("re2native: " + msg)
	}
	return errors.New("re2native: Re2SetFrl " + fallback)
}

// Hit 回答第 i 条这一遍命中过没有 —— ExistOnly 那几条唯一的产物, 要区间的那几条也照样有。
// 只在最近一次 Scan 之后有意义。
func (s *Re2SetFrl) Hit(i int) bool {
	if s == nil || s.h == nil || i < 0 || i >= s.size {
		return false
	}
	p := C.cre2_frl_hits(s.h)
	if p == nil {
		return false
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), s.size)[i] != 0
}

// HitIDs 把命中过的 pattern 下标升序追加进 dst (传 dst[:0] 复用 ⇒ 零分配)。
func (s *Re2SetFrl) HitIDs(dst []int32) []int32 {
	if s == nil || s.h == nil {
		return dst
	}
	p := C.cre2_frl_hits(s.h)
	if p == nil {
		return dst
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(p)), s.size)
	for i := 0; i < s.size; i++ {
		if b[i] != 0 {
			dst = append(dst, int32(i))
		}
	}
	return dst
}

// Re2SetFrlStats_t 是最近一次 Scan 的账 (每次 Scan 重算, 只有 Pool 的水位跨 Scan 保留)。
type Re2SetFrlStats_t struct {
	UsedPeak int64 // native 侧游程里【真正装着结束位置】的字节峰值 (8 × 游程条数)
	HeapPeak int64 // 这一遍为游程数组真实持有的堆字节高水位 (含回收池)
	Pool     int64 // 当前躺在回收池里等着被再发出去的字节
	NSeg     int64 // 收口的分量条数
	NResolve int64 // 问了几次反向锚定 —— 这一层唯一按命中数增长的成本, 盯它
}

// Stats 见 Re2SetFrlStats_t。NResolve/NSeg 是"平均每个分量里有几处不重叠的匹配";
// UsedPeak 是这一层的内存峰值 (与正文长度无关, 只与"同时开着的分量有多大"有关)。
func (s *Re2SetFrl) Stats() Re2SetFrlStats_t {
	var st Re2SetFrlStats_t
	if s == nil || s.h == nil {
		return st
	}
	var u, hp, pl, ns, nr C.longlong
	C.cre2_frl_stats(s.h, &u, &hp, &pl, &ns, &nr)
	st.UsedPeak = int64(u)
	st.HeapPeak = int64(hp)
	st.Pool = int64(pl)
	st.NSeg = int64(ns)
	st.NResolve = int64(nr)
	return st
}
