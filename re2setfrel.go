// re2setfrel.go — Re2SetFrel: 一遍正向扫描, 直接交出不重叠的命中区间。
//
// 名字: F = first pass forward (第一趟正着扫), RL = 【最右终点最长】(去重叠的口径, 见下)。
//
// 🔴 不要把这一层说成 "rightmost-longest" —— 本库里那个词已经是【反向 MatchScanner】的
//    口径 (matchscan_reverse.go), 那个挑的是【起点】最靠右, 与这里不是一回事。要么写全
//    "最右终点最长", 要么用下面那句等价说法。
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
// 🔴 几乎全部逻辑在 C++ 里 (cre2_frel.cpp + re2_dfa_spanscan_inl.h), 命中【不逐条过桥】:
//    游程攒在 native 侧, 分量整块结算, 一次 Step 把一批 Re2SetFrel_result_t 直接写进
//    调用方那个切片 (C 的 cre2_frel_result 与它同一个布局, 两侧各有静态断言钉着)。
//    Go 侧只有 begin/step 两次 cgo 往返 × 批数, 每批一次分配都没有。
//
// ── 口径 (逐字读) ────────────────────────────────────────────────────────────
//	① text[Start:End] 是第 Index 条 pattern 的一个【真匹配】;
//	② 【同一条 pattern】交出来的区间互不相交, 按 Start 升序;
//	③ 去重叠的口径是【最右终点最长】, 逐字是这样:
//	     上界从正文末尾起 => 取"终点 <= 上界"里【终点最靠右】的那个匹配 => 同一个终点上
//	     取【最长】(也就是起点最靠左) => 收下 => 把上界压到它的【起点】=> 继续往左。
//	   等价的一句话 (更好记): 把正文倒过来看, 它就是普通的 leftmost-longest —— 倒过来
//	   之后终点变起点, "终点最靠右"就是"起点最靠左", "同终点最长"就是"同起点最长"。
//
//	   🔴 三个口径, 锚点各挑一个坐标, 别混:
//	       口径              锚点            谁
//	       leftmost-longest  【起点】最靠左   stdlib Longest / MatchScanner
//	       rightmost-longest 【起点】最靠右   MatchScannerReverse
//	       最右终点最长      【终点】最靠右   本层 (Re2SetFrel)
//	   三个真的会给出三个不同答案 (TestRe2SetFrel_Shape 就钉这一格, 否则对拍是空转):
//	       aa|a 撞 "aaa":  本层 [[0,1) [1,3)]  反向MS [[2,3) [1,2) [0,1)]  stdlib [[0,2) [2,3)]
//
//	   🔴 为什么锚终点不锚起点 (当初的取舍, 不是随手):
//	       b|abc 撞 "abc":  本层 [[0,3)="abc"]        反向MS [[1,2)="b"]
//	     锚起点那一侧会把 "abc" 截成中间那个 "b" —— 下游拿这一段去过校验位 (身份证 ·
//	     IBAN · Luhn) 会失败, 真命中被自己毙掉 = 无声漏报。
//
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

// Re2SetFrelPattern_t 是初始化 Re2SetFrel 用的一条。
//
// ExistOnly = 这一条只要"有没有命中"(扫完看 IsHit(i)), 不要 Start/End。挡掉的是这一层
// 真花钱的那步 —— 攒游程 + 盯存活位 + 收口 + 补起点, 不只是少交几处结果。门上很多位
// 只当外层短路的 bool 用, 从来没人问它在哪, 真表上光两条这样的 pattern 就能占掉一多半
// 的游程。回调里自己过滤顶替不了: 那是钱已经花完了才扔。
type Re2SetFrelPattern_t struct {
	Pattern   string
	ExistOnly bool
}

// Re2SetFrel_result_t 是一处命中: text[Start:End] 是第 Index 条 pattern 的一处匹配。
//
// 🔴 布局与 C 的 cre2_frel_result 【必须一致】(三个 int32, 没有洞): Scan 把调用方那个
//    切片的地址直接交给 C 写, 不做逐字段搬运, 所以字段【不许加、不许换序、不许换类型】。
//    下面那两条 const 是编译期断言, 谁动了这里编不过。
type Re2SetFrel_result_t struct {
	Index int32
	Start int32
	End   int32
}

const (
	_ = uint(unsafe.Sizeof(Re2SetFrel_result_t{}) - C.sizeof_cre2_frel_result)
	_ = uint(C.sizeof_cre2_frel_result - unsafe.Sizeof(Re2SetFrel_result_t{}))
)

// Re2SetFrel 见文件头。建一次留着反复 Scan; 【不是】并发安全的 (工作区有状态),
// 并发各建一个, 或者各自加锁。
type Re2SetFrel struct {
	h    *C.cre2_frel
	size int
}

var errClosedRe2SetFrel = errors.New("re2native: Re2SetFrel 已经 Close")

// NewRe2SetFrel 用一组 (正则, 要不要区间) 建工作区。内存预算走 RE2 默认 8MB。
func NewRe2SetFrel(pats []Re2SetFrelPattern_t) (*Re2SetFrel, error) {
	return NewRe2SetFrelMaxMem(pats, 0)
}

// NewRe2SetFrelMaxMem 同上, 但显式给内存预算 maxMem (字节; <=0 = RE2 默认 8MB)。
// 这个预算同时管【整表正向 set】和【惰性建出来的每条单条对象】。表大就得调大 ——
// 判据是扫一批互不相同的真 body 之后 GetStats() 里 DFA 有没有在 flush (见 readme.txt)。
//
// 🔴 能匹配空串的 pattern (GetPatternLenRange 的 min <= 0) 只允许配 ExistOnly, 否则这里
//    当场报错: 每个偏移都是一处零长命中, 游程会长到与正文同量级, 分量也切不动。
//    这与正文无关, 建工作区那一刻就定死 —— 所以它能直接写成回归测试。
func NewRe2SetFrelMaxMem(pats []Re2SetFrelPattern_t, maxMem int64) (*Re2SetFrel, error) {
	n := len(pats)
	if n == 0 {
		return nil, errors.New("re2native: Re2SetFrel 至少要一条 pattern")
	}
	for i := range pats {
		if pats[i].ExistOnly {
			continue
		}
		if minL, _ := GetPatternLenRange(pats[i].Pattern); minL <= 0 {
			return nil, errors.New("re2native: Re2SetFrel 第 " + strconv.Itoa(i) +
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
			return nil, errors.New("re2native: Re2SetFrel pattern 太长 (>2GiB)")
		}
		cpats[i] = C.CString(pats[i].Pattern)
		clens[i] = C.int(len(pats[i].Pattern))
		if pats[i].ExistOnly {
			cbool[i] = 1
		}
	}
	h := C.cre2_frel_new(&cpats[0], &clens[0], &cbool[0], C.int(n), C.int64_t(maxMem))
	for i := range cpats {
		C.free(unsafe.Pointer(cpats[i]))
	}
	if h == nil {
		return nil, errors.New("re2native: Re2SetFrel 建不出来 (OOM)")
	}
	var bad C.int
	if e := C.cre2_frel_error(h, &bad); e != nil {
		msg := C.GoString(e)
		C.cre2_frel_free(h)
		if int(bad) >= 0 && int(bad) < n {
			return nil, errors.New("re2native: " + msg + " (第 " + strconv.Itoa(int(bad)) +
				" 条: " + pats[int(bad)].Pattern + ")")
		}
		return nil, errors.New("re2native: " + msg)
	}
	return &Re2SetFrel{h: h, size: n}, nil
}

// Close 放掉整表 DFA 缓存和惰性建出来的那些单条对象。之后再用返回 err。
func (s *Re2SetFrel) Close() {
	if s == nil || s.h == nil {
		return
	}
	C.cre2_frel_free(s.h)
	s.h = nil
}

// GetPatternLen 是 pattern 条数 (= Index 的上界)。与 RegexpSet.GetPatternLen 同解。
func (s *Re2SetFrel) GetPatternLen() int { return s.size }

// Scan 扫一遍 text, 一批一批把命中交给 fn。fn 返回 false = 提前收工, 剩下的正文不再扫
// (这不算错, Scan 返回 nil)。
//
// buf 是调用方给的批缓冲, C 侧直接往它里面写。留着复用 ⇒ 稳态零分配; 长度只决定一批交
// 多少条 (过桥次数), 不影响结果, 几百到几千都行:
//
//	buf := make([]Re2SetFrel_result_t, 1024)
//	err := s.Scan(text, buf, func(rs []Re2SetFrel_result_t) bool {
//		for _, r := range rs { use(idx(r.Index), text[r.Start:r.End]) }
//		return true
//	})
//
// 🔴 交给 fn 的切片就是 buf 本身, 下一批【原地覆写】。要留自己拷。
// 🔴 err != nil 就是这一遍作废 (已经交出去的批也不算数), 整篇走老路 FindAll。
func (s *Re2SetFrel) Scan(text string, buf []Re2SetFrel_result_t, fn func(rs []Re2SetFrel_result_t) bool) error {
	if s == nil || s.h == nil {
		return errClosedRe2SetFrel
	}
	if len(text) > maxCInt {
		return errors.New("re2native: Re2SetFrel 正文太大 (>2GiB)")
	}
	if len(buf) == 0 {
		return errors.New("re2native: Re2SetFrel 批缓冲是空的 (make([]Re2SetFrel_result_t, 1024))")
	}
	if C.cre2_frel_begin(s.h, C.int(len(text))) == 0 {
		return s.lastErr("begin 失败")
	}
	tp := strBytePtr(text)
	tn := C.int(len(text))
	cap_ := C.int(len(buf))
	pout := (*C.cre2_frel_result)(unsafe.Pointer(&buf[0]))
	var more C.int
	for {
		n := int(C.cre2_frel_step(s.h, tp, tn, pout, cap_, &more))
		runtime.KeepAlive(text)
		runtime.KeepAlive(buf)
		if n < 0 {
			return s.lastErr("扫描失败")
		}
		if n > 0 && fn != nil {
			if !fn(buf[:n]) {
				return nil
			}
		}
		if more == 0 {
			return nil
		}
	}
}

// lastErr 把 C 侧那句话取出来 (取不到就用 fallback)。
func (s *Re2SetFrel) lastErr(fallback string) error {
	var bad C.int
	if e := C.cre2_frel_error(s.h, &bad); e != nil {
		msg := C.GoString(e)
		if int(bad) >= 0 {
			msg += " (第 " + strconv.Itoa(int(bad)) + " 条 pattern)"
		}
		return errors.New("re2native: " + msg)
	}
	return errors.New("re2native: Re2SetFrel " + fallback)
}

// IsHit 回答第 i 条这一遍命中过没有 —— ExistOnly 那几条唯一的产物, 要区间的那几条也照样有。
// 只在最近一次 Scan 之后有意义。
func (s *Re2SetFrel) IsHit(i int) bool {
	if s == nil || s.h == nil || i < 0 || i >= s.size {
		return false
	}
	p := C.cre2_frel_hits(s.h)
	if p == nil {
		return false
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), s.size)[i] != 0
}

// GetHitIDs 把命中过的 pattern 下标升序追加进 dst (传 dst[:0] 复用 ⇒ 零分配)。
func (s *Re2SetFrel) GetHitIDs(dst []int32) []int32 {
	if s == nil || s.h == nil {
		return dst
	}
	p := C.cre2_frel_hits(s.h)
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

// Re2SetFrelStats_t 是最近一次 Scan 的账 (每次 Scan 重算, 只有 Pool 的水位跨 Scan 保留)。
type Re2SetFrelStats_t struct {
	UsedPeak int64 // native 侧游程里【真正装着结束位置】的字节峰值 (8 × 游程条数)
	HeapPeak int64 // 这一遍为游程数组真实持有的堆字节高水位 (含回收池)
	Pool     int64 // 当前躺在回收池里等着被再发出去的字节
	NSeg     int64 // 收口的分量条数
	NResolve int64 // 问了几次反向锚定 —— 这一层唯一按命中数增长的成本, 盯它
}

// Stats 见 Re2SetFrelStats_t。NResolve/NSeg 是"平均每个分量里有几处不重叠的匹配";
// UsedPeak 是这一层的内存峰值 (与正文长度无关, 只与"同时开着的分量有多大"有关)。
func (s *Re2SetFrel) GetStats() Re2SetFrelStats_t {
	var st Re2SetFrelStats_t
	if s == nil || s.h == nil {
		return st
	}
	var u, hp, pl, ns, nr C.longlong
	C.cre2_frel_stats(s.h, &u, &hp, &pl, &ns, &nr)
	st.UsedPeak = int64(u)
	st.HeapPeak = int64(hp)
	st.Pool = int64(pl)
	st.NSeg = int64(ns)
	st.NResolve = int64(nr)
	return st
}
