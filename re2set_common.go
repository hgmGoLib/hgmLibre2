// re2set_common.go —— fll / rrl / frel 三个算法【共用的那半个 Go 门面】: 请求结构 ·
// 结果结构 · 缓冲对象 · 那一段把 native 的 step 循环走完的公共代码。
//
// ── 命名规矩 (三个类型的 doc 各引这一处, 只写一遍) ────────────────────────────
//
//	第一个字母 = 第一趟扫描的方向        f = forward   r = reverse
//	后面       = 重叠去重的口径          l = longest   锚【终点】的用 e 标出来, 不标的锚【起点】
//
//	Re2Set_fll_t    第一趟正向 · leftmost-longest       起点最靠左 · 同起点最长
//	Re2Set_rrl_t    第一趟反向 · rightmost-longest      起点最靠右 · 同起点最长
//	Re2Set_frel_t   第一趟正向 · rightmost-END-longest  终点最靠右 · 同终点最长
//
// 🔴 rrl 和 frel 都带个 r, 但锚的坐标不一样。b|abc 撞 "abc": frel 给 "abc", rrl 给中间那个 "b"。
//
// ── 三者的函数签名【同形】, 但它们【不是】一个 interface ─────────────────────
//
// 同形是为了好学好记好换 —— 学会一个另外两个不用再学。本库【没有任何地方需要 Go 的
// interface】, 也不许引进来: 真让它们可替换, "抄一份换个类型名"就从"人抄错"变成"泛型代码
// 天然会踩", 而三者的输出顺序本来就不一样 (fll 升序 / rrl 降序 / frel 按 start 升序)。
// 顺序写在各自类型的 doc 里, 抄错类型名是调用方自己的事, 库这边不为它付复杂度。
//
// ── 实现在哪 ────────────────────────────────────────────────────────────────
//
// 全部在 C++ (cre2_re2set.cpp): 一遍正向/反向 kManyMatch DFA 扫描 · per-pattern 存活位切
// 分量 · 游程留 native 按分量整块交付 · 反向锚定回推 · 正向锚定验证 —— 五样底座一套,
// 三个算法只差最后那一小块收口策略。Go 这边只剩句柄 + 一层薄壳。
//
// 🔴 【能匹配空串的 pattern 在编译入口就被全库拒了】(见 emptymatch.go), 所以这一层
//    无条件假设"每个匹配至少 1 字节" —— 没有 unsupported 名单, 没有"只能配 boolOnly"
//    的降级, 也没有任何一处零长匹配的兜底分支。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
	"sync"
	"unsafe"
)

// Re2Set_startEnd_t 是一处命中: body[Start:End] 是第 Index 条 pattern 的一个真匹配。
// 🔴 与 C 侧的 cre2_re2set_result 同布局 (三个紧挨着的 int32) —— C 直接往调用方的切片里写,
//    两边各有一条编译期断言钉着。
type Re2Set_startEnd_t struct {
	Index int32
	Start int32
	End   int32
}

// 编译期断言: 布局对不上就编不过 (常量表达式为负 ⇒ uint 转换失败)。
const (
	_ = uint(unsafe.Sizeof(Re2Set_startEnd_t{}) - C.sizeof_cre2_re2set_result)
	_ = uint(C.sizeof_cre2_re2set_result - unsafe.Sizeof(Re2Set_startEnd_t{}))
)

// Re2Set_req_t 是【一遍扫描】要什么。三个类型的 Scan 都吃它, 【按值传】。
//
// 零值合法 = "什么都不要": 三个回调一个都没有 ⟹ 这一遍没有任何可观测产物, 直接返回 nil,
// 正文一遍都不扫、一个字节都不分配。
type Re2Set_req_t struct {
	// Body 是这一遍要扫的正文。
	Body string

	// Allocer 是本遍要用的缓冲。nil = 现建一个用完就扔。
	//
	// 🔴 默认档【故意不是 sync.Pool】: 池的存期是一轮 GC, 而这条路真正的用法是"下一个
	//    ≥4KB 的正文", 中间必然隔着若干轮 GC ⟹ 池里一次都命中不了, 每遍照样现开一个,
	//    等于把这条路上最大的一笔开销藏进默认档 (asc 那边实测过 7.30MB)。要复用就自己
	//    持有一个 alloc, 显式 —— 库这层不替调用方决定缓冲活多久。
	// 🔴 它【不是】并发安全的: 一个 goroutine 一个。同一个 Re2Set_*_t 上可以并发 Scan,
	//    但每条腿得带自己的 alloc。
	Allocer *Re2Set_alloc_t

	// ExistOnlyIndexList 是【纯成本开关】: 这几条只报"命中没命中", 不花钱补端点
	// (fll/frel 连游程和存活位都不攒)。它们【不出现】在 StartEndResultFn 里, 但照常出现在
	// HitIndexResultFn 的全表命中位里。
	//
	// 🔴 这是【每遍】的参数, 不是建对象时定死的属性 —— "先问有没有, 有才问在哪"这个最自然
	//    的用法, 从此一个对象两种问法, 不必开两个对象两份缓存。
	// 🔴 StartEndResultFn 为 nil 时等价于"全表都在这张名单上": 一处区间都不收口。
	ExistOnlyIndexList []int32

	// StartEndResultFn 收命中区间, 扫描过程中【一批批】来。返回 false = 提前收工
	// (不算错, Scan 返回 nil)。
	// 🔴 交给它的切片是 Allocer 里那块缓冲【本身】, 下一批原地覆写 —— 要留就自己 append 走。
	StartEndResultFn func(resultList []Re2Set_startEnd_t) bool

	// HitIndexResultFn 收【全表】命中位 (升序 · 不重复 · 含 ExistOnly 的那几条), 扫完
	// 一次性交, 只调一次 (一条都没命中就不调)。
	// 🔴 为什么是扫完才交: 存活位只有扫完才稳定。一个在扫描中途交、一个在收尾交, 两路的
	//    时序关系就是确定的, 不存在"谁先谁后靠运气"这种测不出来的东西。
	HitIndexResultFn func(hitList []int32)

	// StatsResultFn 收这一遍的账 (见 Re2Set_stats_t), 扫完交, 只调一次。
	// 🔴 配了才算才交 —— 账是给调参用的, 生产路径上不配它, 一个 cgo 调用都不多花。
	StatsResultFn func(stats Re2Set_stats_t)
}

// Re2Set_alloc_t 是接口处的【纯缓冲】—— 里面只有 Go 侧那几块数组, 一个 native 句柄都没有。
//
// 🔴 正因为它是纯缓冲, 同一个 alloc 可以【跨对象、跨表】自由传: 不存在"这个 alloc 不是这张
//    表的"这种运行期错误, 那条几乎跑不到、因而基本没测过的分支从根上就不存在。
//    native 侧的工作区 (spanscan 句柄 · 游程池 · 候选缓冲) 归各个 Re2Set_*_t 自己持有。
//
// 零值可用; 缓冲按需扩容, 扩上去就留着。【不是】并发安全的: 一个 goroutine 一个。
type Re2Set_alloc_t struct {
	batch     int                 // 一批交几处 (<=0 = re2SetBatch)
	startEnd  []Re2Set_startEnd_t // StartEndResultFn 的批缓冲, C 直接往里写
	hitIndex  []int32             // HitIndexResultFn 的缓冲
	existOnly []byte              // 每遍传给 native 的"这几条只要位"位图
	// cmore 是 step 的 *more 出参。放这儿是因为局部变量的地址交给 C 之后逃逸分析会把它
	// 搬上堆 —— 一遍扫描一笔 4 字节堆分配, 在"每篇正文扫一遍"的用法上按正文数放大。
	// 同一招见 spanresolve.go 走 _r 孪生那一段。
	cmore C.int
}

// NewRe2Set_alloc 预开一个缓冲对象。热路径上自己持有一个长期复用。
func NewRe2Set_alloc() *Re2Set_alloc_t { return &Re2Set_alloc_t{} }

// NewRe2Set_allocBatch 同上, 但指定一批交几处 (<=0 = 默认 1024)。
// 🔴 这个数【只决定过桥次数, 不影响结果】—— 生产上没有理由去掐它, 它存在只是为了让
//    "一次一条和一次四千条必须逐处相同"这条回归写得出来 (分量跨批续上的那段逻辑靠它钉住)。
func NewRe2Set_allocBatch(batch int) *Re2Set_alloc_t { return &Re2Set_alloc_t{batch: batch} }

// re2SetBatch 是一批最多交出去几处命中区间的【默认值】。12 字节一处, 1024 处 = 12KB,
// 一次性的固定开销, 不随正文长度涨。
const re2SetBatch = 1024

// re2setScan 是三个 Scan 的同一份实现 —— 三者在 Go 这一侧【只差一个 mode】, 差别全在 C++。
//
// mu/hp 是调用方那个类型里护着 native 句柄的那两样: 读锁下把这一遍的 scan 句柄开出来就放手
// (C 那侧在 scan 上给对象记了一份引用), 之后整遍扫描不再碰 hp —— 所以并发 Scan 之间只在
// 开头那一瞬共享一把读锁, 而 Close 拿写锁, 不会把正在跑的那几遍拆掉。
// name 只用来拼错误文案。
//
// 🔴 req 一路【按值】传, 不取地址: 它里面的 Allocer 会流进 cgo 调用参数, 而 cgo 参数在逃逸
//    分析眼里一律逃逸 ⟹ 一旦有人写了 &req, 这个 req 就被判定逃逸, 每次 Scan 白搭一笔堆
//    分配 (TestRe2SetFllViableNoAlloc 就是钉这一笔的)。按值传只是 72 字节的栈上拷贝。
func re2setScan(mu *sync.RWMutex, hp **C.cre2_re2set, n int, name string, req Re2Set_req_t) error {
	if req.StartEndResultFn == nil && req.HitIndexResultFn == nil && req.StatsResultFn == nil {
		return nil // 什么都不要 ⟹ 没有可观测产物, 一遍都不扫
	}
	body := req.Body
	if len(body) > maxCInt {
		return errors.New("re2native: " + name + " 正文太大 (>2GiB)")
	}
	a := req.Allocer
	if a == nil {
		a = &Re2Set_alloc_t{} // 现建一个用完就扔, 见 Re2Set_req_t.Allocer 的红字
	}
	// ── existonly 位图: 名单 ∪ ("不要区间" ⟹ 全表) ──────────────────────────
	var ep *C.uchar
	wantSpan := req.StartEndResultFn != nil
	if n > 0 && (!wantSpan || len(req.ExistOnlyIndexList) > 0) {
		if cap(a.existOnly) < n {
			a.existOnly = make([]byte, n)
		}
		a.existOnly = a.existOnly[:n]
		fill := byte(0)
		if !wantSpan {
			fill = 1 // 一处区间都不收口 = 全表只要位
		}
		for i := range a.existOnly {
			a.existOnly[i] = fill
		}
		for _, idx := range req.ExistOnlyIndexList {
			if idx >= 0 && int(idx) < n {
				a.existOnly[idx] = 1
			}
		}
		ep = (*C.uchar)(unsafe.Pointer(&a.existOnly[0]))
	}
	// ── 开这一遍的 native 暂存 ──────────────────────────────────────────────
	mu.RLock()
	h := *hp
	var sh *C.cre2_re2set_scan
	if h != nil {
		sh = C.cre2_re2set_scan_new(h, C.int(len(body)), ep)
	}
	mu.RUnlock()
	runtime.KeepAlive(a)
	if h == nil {
		return errors.New("re2native: " + name + " 已经 Close")
	}
	if sh == nil {
		return errors.New("re2native: " + name + " 开不出这一遍的扫描暂存 (OOM)")
	}
	defer C.cre2_re2set_scan_free(sh)
	// 🔴 先用 badidx=NULL 问一声有没有错, 有才走 re2setErr —— re2setErr 里那个 &bad 是要
	//    交给 C 的, 逃逸分析会把它搬上堆, 每遍一笔。错误路上无所谓, 成功路上不能有。
	if C.cre2_re2set_scan_error(sh, nil) != nil {
		return re2setErr(sh, name, "开这一遍失败")
	}
	// ── step 循环 ───────────────────────────────────────────────────────────
	// 不要区间的时候也得把正文走完 (命中表要扫到底才稳定), 只是 native 一处都不会写,
	// 所以给一格占位缓冲就够。
	want := a.batch
	if want <= 0 {
		want = re2SetBatch
	}
	if !wantSpan {
		want = 1
	}
	if cap(a.startEnd) < want {
		a.startEnd = make([]Re2Set_startEnd_t, want)
	}
	buf := a.startEnd[:want]
	tp := strBytePtr(body)
	tn := C.int(len(body))
	pout := (*C.cre2_re2set_result)(unsafe.Pointer(&buf[0]))
	for {
		k := int(C.cre2_re2set_scan_step(sh, tp, tn, pout, C.int(want), &a.cmore))
		runtime.KeepAlive(body)
		runtime.KeepAlive(a)
		if k < 0 {
			return re2setErr(sh, name, "扫描失败")
		}
		if k > 0 && wantSpan {
			if !req.StartEndResultFn(buf[:k]) {
				return nil // 提前收工, 不算错
			}
		}
		if a.cmore == 0 {
			break
		}
	}
	// ── 全表命中位, 扫完一次性交 ────────────────────────────────────────────
	if req.HitIndexResultFn != nil && n > 0 {
		p := C.cre2_re2set_scan_hits(sh)
		if p != nil {
			hits := unsafe.Slice((*byte)(unsafe.Pointer(p)), n)
			a.hitIndex = a.hitIndex[:0]
			for i := 0; i < n; i++ {
				if hits[i] != 0 {
					a.hitIndex = append(a.hitIndex, int32(i))
				}
			}
			if len(a.hitIndex) > 0 {
				req.HitIndexResultFn(a.hitIndex)
			}
		}
	}
	// ── 这一遍的账, 配了才算 ────────────────────────────────────────────────
	if req.StatsResultFn != nil {
		req.StatsResultFn(re2setStats(sh))
	}
	return nil
}

// re2setErr 把 C 侧那句话取出来 (取不到就用 fallback)。只走错误路 —— 里面那个 &bad 交给 C
// 之后会被搬上堆, 成功路上一笔都不能有 (见调用处的红字)。
func re2setErr(sh *C.cre2_re2set_scan, name, fallback string) error {
	var bad C.int
	if e := C.cre2_re2set_scan_error(sh, &bad); e != nil {
		msg := C.GoString(e)
		if int(bad) >= 0 {
			msg += " (第 " + strconv.Itoa(int(bad)) + " 条 pattern)"
		}
		return errors.New("re2native: " + msg)
	}
	return errors.New("re2native: " + name + " " + fallback)
}

// Re2Set_stats_t 是【这一遍】Scan 的账 (走 Re2Set_req_t.StatsResultFn 交出来)。加它是因为变长条的钱全在"验了几个假候选"上,
// 而那一笔从外面一个字都看不见 —— 没有这几个数就没法判断某张表的形状适不适合这条路。
//
// 🔴 Walks/Cands/Tries 【只统计变长条】: 定长条走一句加减法, 一次锚定都不做, 不进这三个
//    分母。Emits 不一样, 它数的是【全部】吐出去的区间 (定长的也算) —— 所以要看"平均验了
//    几次"得用 Tries/Walks, 拿 Tries/Emits 会被定长条稀释成假象。
type Re2Set_stats_t struct {
	Walks int64 // 锚定回推了几趟 (fll = 处理了几个没被游标盖住的右端; rrl/frel = 每处命中一趟)
	Cands int64 // 这些趟一共给出多少候选起点 (只有 fll 会涨, 另外两个恒 0)
	Tries int64 // 一共拿锚定搜索验了几次 (fll: <= Cands, 命中即停)
	Emits int64 // 交出去几处区间 (含定长条)

	// 下面这几个是【存活位切分量】那一档的账, 只有 fll/frel 有 (rrl 不切分量, 恒 0)。
	NSeg int64 // 切出来几个分量。Tries/NSeg = "平均每个分量里有几处不重叠的匹配",
	//          越接近 1 说明存活位把分量切得越干净, 一个分量一趟锚定就结完。
	UsedPeak int64 // native 游程里【真正装着结束位置】的字节峰值 (8 × 游程条数)
	HeapPeak int64 // 这一遍为游程数组真实持有的堆字节高水位 (含回收池)
}

// re2setStats 把这一遍的账从 native 取出来。
func re2setStats(sh *C.cre2_re2set_scan) Re2Set_stats_t {
	var st Re2Set_stats_t
	if sh == nil {
		return st
	}
	var w, c, t, e, ns, up, hp C.longlong
	C.cre2_re2set_scan_stats(sh, &w, &c, &t, &e, &ns, &up, &hp)
	st.Walks = int64(w)
	st.Cands = int64(c)
	st.Tries = int64(t)
	st.Emits = int64(e)
	st.NSeg = int64(ns)
	st.UsedPeak = int64(up)
	st.HeapPeak = int64(hp)
	return st
}

// re2setNew 是三个构造函数的同一份实现: 把长度区间表摊成两个 int32 数组交给 C++。
// 🔴 C 那侧自己给 set 记了一份引用 (cre2_set_ref), 所以调用方【不必】再存一份 Go 侧的
//    set 引用替它保命 —— 表活得比这个对象久这件事由引用计数保证, 不由 GC 的时间表保证。
func re2setNew(setH *C.cre2_set, mode C.int, lens []patLen_t, name string) (*C.cre2_re2set, error) {
	n := len(lens)
	minl := make([]C.int32_t, n+1) // +1: n==0 时也有个合法地址可以取
	maxl := make([]C.int32_t, n+1)
	for i := range lens {
		minl[i] = C.int32_t(lens[i].min)
		maxl[i] = C.int32_t(lens[i].max)
	}
	h := C.cre2_re2set_new(setH, mode, &minl[0], &maxl[0], C.int(n))
	if h == nil {
		return nil, errors.New("re2native: " + name + " 建不出来 (OOM)")
	}
	var bad C.int
	if e := C.cre2_re2set_error(h, &bad); e != nil {
		msg := C.GoString(e)
		C.cre2_re2set_free(h)
		if int(bad) >= 0 {
			msg += " (第 " + strconv.Itoa(int(bad)) + " 条 pattern)"
		}
		return nil, errors.New("re2native: " + msg)
	}
	return h, nil
}
