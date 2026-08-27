// spanviable.go —— ViableStarts: 给一个匹配【右端】, 把它左边全部【候选起点】收下来。
//
// ── 它和 ResolveSpan 差在哪 (只有一处, 但是决定性的) ─────────────────────────
// 反向 set 的 ResolveSpan  : 反向机器只种【accept】—— 回答的是"哪些 s 使 text[s,e) 【正好】
//                            是一个匹配", 而且只给最靠左的那一个。
// 反向 set 的 ViableStarts : 反向机器种【全部指令】—— 回答的是"哪些 s 起头的匹配【路过】了 e",
//                            即 text[s,e) 是个【可行前缀】(还能被某个后缀补成真匹配)。
//                            后者是前者的超集, 而且【全部】给出来。
//
// 🔴 为什么需要这个超集: 门 (正向 set + kManyMatch) 只给右端, 不给"起点终点的配对"。
//    拿最小的那个右端去只种 accept 地回推, 得到的起点未必是真正的最左起点 ——
//      \b(?:ab cd ef|cd)\b 撞 "ab cd ef": 门给的最小右端是 "cd" 那一处的右端 (偏移 5),
//      只种 accept 只能回推到 3 ("cd" 的左端); 而真正的 leftmost 起点是 0 ——
//      text[0:5) = "ab cd" 【不是】匹配, 但它是可行前缀 (再补 " ef" 就成了)。
//    种全部状态才看得见 0 这个候选。2026-08-28 之前 MatchScanner 有一档 spanFast 走的正是
//    "只种 accept"那条路 (老的"路 A"), 上面这个例子就是它那个"第三种口径"的病根;
//    整档删了, 现在 MatchScanner 补起点【只走本函数这一条路】, 换来的就是严格 leftmost-longest。
//
// 🔴 为什么"种全部状态"就等于可行前缀 (证明): 反向 set 的程序 R 认的是 reverse(L)。
//    这一趟从 e 往左吃字节, 吃进去的串正好是 reverse(text[s,e)); 种子是 R 的【全部活状态】,
//    终点是 R 的 accept。于是 —— 走到 accept ⟺ 存在某个活状态 q 能吃着 reverse(text[s,e))
//    走到 accept ⟺ reverse(text[s,e)) 是 reverse(L) 里某个词的【后缀】 ⟺ text[s,e) 是 L 里
//    某个词的【前缀】= 可行前缀。∎
//
// 🔴 这一步与 ResolveSpan 一样【只能在库里做】: 种全部状态要的是 DFA 起始状态的构造权
//    (re2_dfa.cc 的 start_[kStartViable]), 从外面根本够不着。
//
// 代价 = 这处命中能往回够多远 (可行前缀集合空了机器就死, 当场收工), 与正文长度无关 ——
// 与 ResolveSpan 同一个量级, 同一个道理。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
	"unsafe"
)

// ViableStarts 把 [bound, from) 里全部候选起点写进 out, 返回【找到的总条数】n。
//
//	from  匹配右端 (不含) —— 就是正向 set 的 FindAllIndex 吐出来的那种端点;
//	bound 回看的左下界 (含), 负数 = 不限。判定用的上下文恒是【整篇正文】, 所以 \b / ^ / $
//	      看到的永远是真实邻居字节, 掐 bound 只会让候选变少, 不会让它变错。
//	id    第几条 pattern (与 Match 返回的下标同一套)。
//
// 🔴 out 里是【降序】的 (机器从右往左走, 先看见的位置更大)。要 leftmost 就【倒着遍历】。
//
// 🔴 n 可能【大于 len(out)】—— 那表示缓冲不够, 里面写下的是最大的那几个 (恰好最没用的
//    那几个)。调用方该按 n 换个更大的缓冲重来一次, 不要拿这批半成品往下走。
//
// 🔴 from 这个位置本身【不算】候选 (text[from:from) 是空的可行前缀, 对调用方没有意义)。
//
// 无状态、只读 (自己拿 DFA 的缓存读锁), 可以和别的 goroutine 的扫描并发调。
func (r *RegexpSetReverse) ViableStarts(text string, from, bound, id int32, out []int32) (n int, err error) {
	s := r.s
	if s.size == 0 {
		return 0, errors.New("re2native: viable starts on empty set")
	}
	if len(text) > maxCInt {
		return 0, errors.New("re2native: viable starts text too large (>2GiB)")
	}
	if id < 0 || int(id) >= s.size {
		return 0, errors.New("re2native: viable starts bad pattern index " + strconv.Itoa(int(id)))
	}
	if from < 0 || int(from) > len(text) {
		return 0, errors.New("re2native: viable starts bad offset " + strconv.Itoa(int(from)))
	}
	// out 允许是空的 (只想问"有几个"): 那就给 C 一个合法但容量为 0 的落点 —— C 那侧
	// outcap=0 时一个字节都不写, 只把总条数数出来。
	p := &viableStartsNoOut[0]
	outcap := 0
	if len(out) > 0 {
		p = &out[0]
		outcap = len(out)
	}
	rc := int(C.cre2_set_viable_starts(s.h, strBytePtr(text), C.int(len(text)),
		C.int(from), C.int(bound), C.int(id),
		(*C.int32_t)(unsafe.Pointer(p)), C.int(outcap)))
	runtime.KeepAlive(text)
	runtime.KeepAlive(out)
	runtime.KeepAlive(s)
	if rc < 0 {
		return 0, errors.New("re2native: viable starts failed (DFA gave up); patterns=" +
			strconv.Itoa(s.size) + "; 用 NewRegexpSetReverseMaxMem 把 maxMem 调大")
	}
	return rc, nil
}

// viableStartsNoOut 是 len(out)==0 时给 C 的那个合法落点。
//
// 🔴 必须是【包级】的, 不能在函数里开个局部数组: 局部数组的地址要交给 C, 逃逸分析据此
//    每次调用把它搬上堆 —— 一次回推一笔分配, 在"每个右端问一次"的用法上按右端数放大
//    (实测 benchPats/命中稀疏 上 33 次回推 = 33 笔, 而这个方法本该是零分配的)。
//    同一类账见 spanresolve.go 里走 _r 孪生那一段。
// 🔴 包级共享是安全的: 这条路上 outcap 恒为 0, C 那侧一个字节都不写。
var viableStartsNoOut [1]int32
