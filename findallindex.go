// findallindex.go —— FindAllIndex: 一遍扫正文, 边扫边【一批一批】把"哪条 pattern 的匹配端点
// 落在哪一段"交出来。这是本库多正则一侧的【底座】, 上面那三档"算出完整区间"(re2set_fll.go /
// re2set_frel.go / re2set_rrl.go) 就搭在它身上。
//
// ── 它替掉的是哪段路 ────────────────────────────────────────────────────────
// 调用方今天的写法是两段式: 先 Set.Match 扫一遍拿到"哪几条命中", 再为了知道【在哪】把这几条
// 各自的 Regexp 对整篇正文跑一遍 FindAllStringIndex。命中 k 条就是 1 + k 遍全文, 而且那 k 遍
// 用的是【非锚定】正则 —— `.*?` 前缀让"哪个位置能当起点"变成"每个位置都能", 状态数对计数
// 上界指数增长 (doc/状态数为什么会相乘.txt: 同一条 pattern 加个 \b 就是 967 倍的差距)。
//
// 可位置本来就在第一遍里算出来过: kManyMatch 的 DFA 每走到一个能结束匹配的字节都记了一次,
// 走完把它扔了。FindAllIndex 就是把这一遍记下来的东西接出来 ——【不额外要钱】, 同一份正文
// 同一份 DFA 缓存, 6.4MB 上实测 Match 18.5ms / 收端点 18.4ms。
//
// ── 吐的是【端点游程】, 不是匹配区间 ────────────────────────────────────────
// 交出来的每一条是 (ReIndex, Lo, Hi): 第 ReIndex 条 pattern 的匹配端点落在 Lo..Hi 里的
// 【每一个】值上 (原文字节偏移,【两端都含】, 恒 Lo <= Hi)。
//
//	正向 RegexpSet        : 端点 = 匹配【右端】(不含), 即 text[?:Hi] 是一个匹配
//	反向 RegexpSetReverse : 端点 = 匹配【左端】(含),   即匹配从 text[Lo] 开始
//
// 🔴 两端【都给】所以无信息损失: 展开 Lo..Hi 就还原成逐个端点。这一点不能省 —— 只留一端
//    (比如只留最右那个) 会把两个真实独立的匹配悄悄并成一个: `ab|c` 撞 "abc" 的右端是 2 和 3,
//    连号, 只留 3 就把 [0,2) 那个匹配弄丢了, 而且【不报错】。
//
// 🔴 为什么是【闭区间】而不是 Go 习惯的半开: 这三个数不是一个区间, 是"一串端点"的收敛写法。
//    Hi 是一个真端点, 不是"末端后一位"。写成半开就得记住 Hi 那个位置到底算不算, 而这正是
//    最容易错的地方。字段名里也没有 start/end 的暗示 —— 说的是哪一端由【方向】定死
//    (正向 = 右端, 反向 = 左端), 一个类型里塞两套名字反而会骗人。
//
// 🔴 顺序【不保证】全局按位置升序。一段游程要等"这条 pattern 再次命中且与上次不连号"或者
//    "整篇扫完"才收口, 所以不同 pattern 的游程会交错, 最后一批还会在扫完时集中吐出来。
//    但【同一条 pattern 内部】按【扫描方向】单调 —— 正向 set 升序, 反向 set 【降序】
//    (扫的方向本来就是单向的)。上面那几层的游标就靠这条 (re2set_fll.go / re2set_rrl.go)。
//
// 🔴 语义不是 FindAllStringIndex。FindAll 给的是 leftmost-first 的【不重叠】匹配序列;
//    这里给的是"所有 pattern 的所有匹配端点", 重叠的也在里面 (`abcd|bc` 撞 "abcd" 两条都报)。
//    要"不重叠的匹配序列"那个口径的调用方看 re2set_fll.go / re2set_frel.go / re2set_rrl.go
//    (三种去重叠口径, doc/三种去重叠模式.md 有对比)。再往外的取舍规则 (跨 pattern 的优先级
//    / 相交即丢 / …) 是调用方的业务, 这一层不替它决定。
//
// ── 为什么是"一批一个数组", 不是"一条一个回调" ──────────────────────────────
// 游程条数没有上界 (真表实测约 30741 条/MB, 200MB 的 body 就是 47MB), 所以【不能】攒成一个
// 完整数组还给调用方 —— 那等于让内存跟着正文长度走。但反过来"一条调一次回调"也是白扔钱:
// 那是每条游程一次【不可内联的间接调用】, 6.4MB 上 19.7 万次。
//
// 量过 (两条腿交替跑各 40 轮取中位数 —— 顺着跑量不出来, 进程内前后段的漂移就有 ±5%,
// 比要量的东西还大):
//
//	一条一回调  18.47 / 19.11 / 19.90ms      一批一数组  18.29 / 18.81 / 19.62ms
//	差 +1.0% / +1.6% / +1.4%  ⟹ 每条游程约 1.3ns
//
// 一批一个数组把这笔钱摊掉 (4096 条才一次调用), 而且调用方拿到的是一段连续内存, for 循环
// 能矢量化、边界检查能提到循环外。省下的只有 1% 出头, 但这个 API 的存在理由就是快,
// 白给的 1% 也不该给。
//
// ── alloc 是什么 ────────────────────────────────────────────────────────────
// native 那层是 sqlite3_step 式的: 攒满一批就【挂起】(按内容存下当前 DFA 状态, 放掉 DFA 的
// 缓存读锁, 返回给 Go), 取走之后再进去、重新拿锁、按内容把状态查回来接着扫。挂起期间一把锁
// 都不持有 —— 反过来说, 如果做成"C 直接回调进 Go", 回调期间还攥着读锁, 谁想 flush 谁就得
// 等 Go 跑完。那个挂起点 + 那一批缓冲就是 alloc。
//
// 为什么不能"缓冲不够就扩容重跑": 重跑要付的正是最贵的那一遍 (新正文现造 DFA 状态,
// 实测比命中缓存的重复扫慢 66 倍)。
//
// 🔴 交给 batchFn 的那段切片是 alloc 里那块缓冲【本身】, 不是拷贝 —— 下一批会原地覆写它。
//    要留就自己 append 走。
//
// alloc 传 nil 也能用 (当场建一个、用完就扔), 只是每次 FindAllIndex 多一笔 native 分配。
// 热路径上建一个长期留着。🔴 alloc 【不是并发安全的】: 一个 goroutine 一个。
// 同一个 set 上开多个 alloc 并发扫是可以的 (set 本身只读)。alloc 认它出生的那个 set,
// 串用会报错。
//
// ── 偏移为什么是 int32 ──────────────────────────────────────────────────────
// ① 宽度锁死, 32 位/64 位平台上一样宽; native 侧本来就是 int32, 这块缓冲是 C 直接写进去的,
//    换任何别的宽度都得多一趟【逐条转换】—— 正是上面那 2.4% 要躲的东西。
// ② 不用 uint32: 上面那层算起点要做 end - minLen, 正文开头几个端点上这是【负数】。
//    有符号下它一眼可判; 无符号下它回绕成 42 亿, 边界判断会【静默】放行, 然后在
//    text[42亿:end] 上炸 —— 这类下溢是最难查的一种。
// ③ RE2 本来就把正文卡在 2GiB (见各处 maxCInt 检查), int32 装得下。
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

// findAllIndexBatch 是一批最多吐几条游程。
//
// 每一批都是一次 cgo 往返 + 一次 DFA 状态挂起/恢复。取"pattern 条数"那种下限值时,
// 155 条的表在 6.4MB 正文上要往返 1270 次, 实测比 4096 那档慢一倍。4096 条 × 12 字节 = 48KB,
// 一次性的固定开销, 不随正文长度涨 —— 所以这里【不做成旋钮】: 没有值得调用方去调的余地,
// 多一个参数只是多一处能填错的地方。
const findAllIndexBatch = 4096

// RegexpSet_FindAllIndex_Run_t 是一条【端点游程】: 第 ReIndex 条 pattern 的匹配端点落在
// Lo..Hi 里的每一个值上 (两端都含)。正向 set 里是匹配【右端】(不含), 反向 set 里是匹配
// 【左端】(含) —— 见文件头。
//
// 🔴 这个布局是 native 直接写进来的 (紧挨着的 int32 三元组), 底下两条常量把它钉在编译期:
//    尺寸不是 12 字节就编不过。别在中间加字段、别改字段顺序、别换宽度。
type RegexpSet_FindAllIndex_Run_t struct {
	ReIndex int32
	Lo      int32
	Hi      int32
}

const _ uintptr = unsafe.Sizeof(RegexpSet_FindAllIndex_Run_t{}) - 12
const _ uintptr = 12 - unsafe.Sizeof(RegexpSet_FindAllIndex_Run_t{})

// RegexpSet_FindAllIndex_Alloc_t 是 FindAllIndex 的可复用工作区 (native 侧的游程表 + 挂起点 +
// 一批输出缓冲)。用 (*RegexpSet).NewFindAllIndexAlloc 或 (*RegexpSetReverse).NewFindAllIndexAlloc
// 开, 不用了调 Close (不调也有 finalizer 兜底)。
//
// 🔴 不是并发安全的: 一个 goroutine 一个。也不能跨 set 用 —— native 句柄是从那个 set 的程序
// 上开出来的, 串用会返回 error 而不是给错答案。
type RegexpSet_FindAllIndex_Alloc_t struct {
	h   *C.cre2_spanscan
	set *RegexpSet
	// buf 是一批游程的落地处 —— native 直接往这块内存写, Go 这侧一条都不转换。
	// 长度恒为 batch; 每批交给 batchFn 的就是它的前 n 条 (原地复用, 不拷贝)。
	buf []RegexpSet_FindAllIndex_Run_t
	// more 是 step 的"还有没有下一批"出参。放在这儿而不是每次 step 开个局部变量:
	// &局部变量 交给 C 会被逃逸分析判成逃逸, 每次 step 一笔 4 字节堆分配 —— 而 FindAllIndex
	// 本该是零分配的 (工作区本身就是长期复用的, 这个字段跟着它一起活)。
	more C.int
}

// NewFindAllIndexAlloc 给这个正向 set 开一个 FindAllIndex 工作区。
// 热路径上建一次长期留着, 别每次扫描新建。
func (s *RegexpSet) NewFindAllIndexAlloc() (*RegexpSet_FindAllIndex_Alloc_t, error) {
	return newFindAllIndexAlloc(s, findAllIndexBatch)
}

// NewFindAllIndexAlloc 给这个反向 set 开一个 FindAllIndex 工作区。
// 正向 set 的 alloc 【不能】拿到这里来用, 反过来也不行。
func (r *RegexpSetReverse) NewFindAllIndexAlloc() (*RegexpSet_FindAllIndex_Alloc_t, error) {
	return newFindAllIndexAlloc(r.s, findAllIndexBatch)
}

// newFindAllIndexAlloc 带 batch 的内部版。batch 会被抬到至少 pattern 条数 —— native 侧要
// 保证"再来一个命中字节也一定塞得下这一批", 而一个 DFA 状态最多带走全表那么多条 pattern。
func newFindAllIndexAlloc(s *RegexpSet, batch int) (*RegexpSet_FindAllIndex_Alloc_t, error) {
	if s.size == 0 {
		return nil, errors.New("re2native: FindAllIndex alloc on empty set")
	}
	if batch < s.size {
		batch = s.size
	}
	h := C.cre2_set_spanscan_new(s.h)
	runtime.KeepAlive(s)
	if h == nil {
		return nil, errors.New("re2native: FindAllIndex alloc failed")
	}
	a := &RegexpSet_FindAllIndex_Alloc_t{
		h:   h,
		set: s,
		buf: make([]RegexpSet_FindAllIndex_Run_t, batch),
	}
	runtime.SetFinalizer(a, func(x *RegexpSet_FindAllIndex_Alloc_t) { x.freeC() })
	return a, nil
}

func (a *RegexpSet_FindAllIndex_Alloc_t) freeC() {
	if a.h != nil {
		C.cre2_spanscan_free(a.h)
		a.h = nil
	}
}

// Close 释放 native 侧的工作区。可重复调; 调过之后拿它去 FindAllIndex 返回 error。
func (a *RegexpSet_FindAllIndex_Alloc_t) Close() {
	runtime.SetFinalizer(a, nil)
	a.freeC()
}

// FindAllIndex 扫 text 一遍, 每攒够一批端点游程就调一次 batchFn。
// runs 里每一条的 Lo..Hi 是匹配【右端】(不含) 的取值范围, 两端都含。
//
// 🔴 runs 是工作区里那块缓冲本身, 下一批原地覆写 —— 要留就 append 走。
//
// alloc 传 nil = 当场建一个用完就扔 (多一笔 native 分配); 热路径上传一个长期复用的。
// 没有任何命中时 batchFn 一次都不会被调 (不会拿空切片去骚扰调用方)。
//
// 返回 error 只有三种情况: alloc 已 Close / alloc 不是这个 set 的 / native 侧 DFA 中途放弃
// (预算实在不够) —— 最后一种跟 Match 返回空是同一类事故, 用 NewRegexpSetMaxMem 调大即可。
func (s *RegexpSet) FindAllIndex(text string, alloc *RegexpSet_FindAllIndex_Alloc_t,
	batchFn func(runs []RegexpSet_FindAllIndex_Run_t)) error {
	return findAllIndex(s, text, alloc, batchFn)
}

// FindAllIndexBytes 同 FindAllIndex, 但正文是 []byte (零拷贝)。
func (s *RegexpSet) FindAllIndexBytes(text []byte, alloc *RegexpSet_FindAllIndex_Alloc_t,
	batchFn func(runs []RegexpSet_FindAllIndex_Run_t)) error {
	err := findAllIndex(s, bytesStr(text), alloc, batchFn)
	runtime.KeepAlive(text)
	return err
}

// FindAllIndex 扫 text 一遍 (从末尾往前走【原始 buffer】, 不反转正文, 不复制正文),
// 每攒够一批端点游程就调一次 batchFn。runs 里每一条的 Lo..Hi 是匹配【左端】(含) 的
// 取值范围, 两端都含。
//
// 🔴 与正向的差别不只是"端点换了一头": 反向 set 的状态数是每条 pattern 各自最坏情况
//    【相乘】出来的。真表实测 155 条反向扫 6.4MB = 65 秒 / arena 顶满 254MB 还在 flush,
//    正向同一张表 18ms / 零 flush。拿反向 set 扫全文之前先量一遍 GetMemInfo().FlushesTotal;
//    只是想补一处命中的左端, 用 ResolveSpanWithin (单点锚定, 代价与正文长度无关)。
//
// 其余 (alloc 语义 · 闭区间 · 顺序 · 缓冲原地复用 · error) 与正向那个完全一致。
func (r *RegexpSetReverse) FindAllIndex(text string, alloc *RegexpSet_FindAllIndex_Alloc_t,
	batchFn func(runs []RegexpSet_FindAllIndex_Run_t)) error {
	return findAllIndex(r.s, text, alloc, batchFn)
}

// FindAllIndexBytes 同 FindAllIndex, 但正文是 []byte (零拷贝)。
func (r *RegexpSetReverse) FindAllIndexBytes(text []byte, alloc *RegexpSet_FindAllIndex_Alloc_t,
	batchFn func(runs []RegexpSet_FindAllIndex_Run_t)) error {
	err := findAllIndex(r.s, bytesStr(text), alloc, batchFn)
	runtime.KeepAlive(text)
	return err
}

// findAllIndex 是正反两个方向共用的扫描体 —— 方向早在建 set 的时候就编进程序里了,
// 这一层一个字都不用分方向。
func findAllIndex(s *RegexpSet, text string, alloc *RegexpSet_FindAllIndex_Alloc_t,
	batchFn func(runs []RegexpSet_FindAllIndex_Run_t)) error {
	if s.size == 0 {
		return nil
	}
	if len(text) > maxCInt {
		return errors.New("re2native: FindAllIndex text too large (>2GiB)")
	}
	if alloc == nil {
		tmp, err := newFindAllIndexAlloc(s, findAllIndexBatch)
		if err != nil {
			return err
		}
		defer tmp.Close()
		alloc = tmp
	}
	if alloc.h == nil {
		return errClosedFindAllIndexAlloc
	}
	if alloc.set != s {
		return errors.New("re2native: FindAllIndex alloc 属于另一个 set (正向/反向也算两个)")
	}
	if C.cre2_spanscan_begin(alloc.h, C.int(len(text))) == 0 {
		return errors.New("re2native: FindAllIndex begin failed")
	}
	// native 侧【不保存】正文指针 (只存偏移), 所以每次 step 都要重新传进去。
	tp := strBytePtr(text)
	tn := C.int(len(text))
	outp := (*C.int32_t)(unsafe.Pointer(&alloc.buf[0]))
	outcap := C.int(len(alloc.buf) * 3) // C 那侧按 int32 个数算
	for {
		alloc.more = 0
		n := int(C.cre2_spanscan_step(alloc.h, tp, tn, outp, outcap, &alloc.more))
		runtime.KeepAlive(text)
		runtime.KeepAlive(alloc)
		if n < 0 {
			return errors.New("re2native: FindAllIndex failed (DFA gave up); patterns=" +
				strconv.Itoa(s.size) + "; 用 NewRegexpSetMaxMem 把 maxMem 调大")
		}
		if n > len(alloc.buf) {
			n = len(alloc.buf) // 防御: C 侧最多写 outcap/3 条
		}
		if n > 0 {
			batchFn(alloc.buf[:n])
		}
		if alloc.more == 0 {
			return nil
		}
	}
}

// errClosedFindAllIndexAlloc 单独提出来, 免得每次构造一遍 error。
var errClosedFindAllIndexAlloc = errors.New("re2native: FindAllIndex alloc closed")
