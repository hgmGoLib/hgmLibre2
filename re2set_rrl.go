// re2set_rrl.go —— Re2Set_rrl_t: 第一趟【反向】· rightmost-longest。
//
// 🔴 交出来的区间, 同一条 pattern 内部按 Start 【降序】—— 这是它与 Re2Set_fll_t 最容易
//    写错的差别。要升序是调用方那边一句 reverse 的事; 库这边翻就得攒 = 又是缓冲, 而
//    "一个字节都不攒"正是这条路存在的理由。
//
// 命名规矩 · 请求/结果/缓冲结构 · "三者签名同形但不是 interface" 见 re2set_common.go 头注。
// 实现全在 C++ (cre2_re2set.cpp), 本文件只是句柄 + 薄壳。
//
// ── 为什么会有这一层 ────────────────────────────────────────────────────────
// 有一族 pattern 正着扫状态数对计数上界指数增长, 反着读就塌回线性 —— `S B{m,n} L` 里起始类
// 严格窄于重复类那一族 (doc/状态数为什么会相乘.txt §3, 实测同一条 pattern 正向 66572 状态 /
// 8.39MB, 反向 42 状态 / 0.07MB)。这种表本来就该反着扫。
//
// ── 反向【更好】做, 不是更难做 ──────────────────────────────────────────────
// 正向那一趟 DFA 交出来的是匹配的【右端】, 起点得回推; 反向交出来的是【左端】= 起点, 而
// leftmost/rightmost-longest 这个口径本来就定义在起点上, 所以这一侧连"收候选再逐个验"那
// 一步都没有 —— 每处命中恒等于【一趟正向锚定】, 代价 = 这处命中有多长, 与正文长度无关。
//
// ── 为什么"从右往左"仍然一个字节都不用攒 ─────────────────────────────────────
// 因为口径也跟着翻了。要在反向扫描上给 leftmost-longest 就得攒: 手上这一处随时可能被更
// 靠左、还没扫到的那一处整个吃掉, 无上界的 (邮箱那种) 得攒到整篇扫完 —— 内存跟着正文长,
// 正是这一层存在的理由被赔掉。改成 rightmost-longest 这件事就没了: 从右往左走,
// 【第一个见到的起点就是最终答案】。
//
// ── 对外那条保证, 逐字读 ─────────────────────────────────────────────────────
//	① body[Start:End] 是第 Index 条 pattern 的一个真匹配;
//	② 【同一条 pattern】吐的区间互不相交, 按 Start 【降序】;
//	③ 口径是 rightmost-longest: 在还没被占掉的那段正文里, 反复取【起点最靠右】的那个匹配,
//	   同起点取【最长】, 吐出去, 再往左接着找。
//
// 与 leftmost-longest 只在【两个真匹配互相交叠】的地方差, 不交叠的正文上两者逐处相同:
//
//	a|ab  撞 "abab"   fll [[0,2) [2,4)]   rrl [[2,4) [0,2)]   (同一批, 顺序反)
//	ab|b  撞 "aab"    fll [[1,3)]         rrl [[2,3)]         ← 这里才真差
//
// 两边都是真匹配, 都不重叠, 都不漏段。要跟 stdlib 的 re.Longest().FindAllStringIndex 逐
// 字节对上就用 fll; 只是要"把这片正文里的东西都框出来"(脱敏 · 定位 · 计数), 两个都行。
//
// 🔴 存活位切分量那一档 (fll/frel 用来把回推次数压到"分量里真有几处不重叠匹配"的那个)
//    只在正向 set 上有, 这一侧走的是不切分量的档。这不是缺角: rrl 的回看上界本来就是它
//    自己的游标, 分量左界压不出更紧的东西 —— 它每处命中恒等于一趟锚定, 没有可省的浪费。
//
// 🔴 反向 set 本身仍然该是【一条一个】或者至少是很小的一张表: set 里的状态数是相乘的,
//    155 条的反向表在 6.4MB 正文上实测 65 秒 / arena 顶满 254MB 还在 flush。这一层不改变
//    那件事 —— 它只是把"扫出来的左端"补成完整区间。
//
// ── 生命周期: 【进程级】· 建一次留着 · 可以并发 Scan ────────────────────────
// 一个策略一份, 跟策略同生共死。它身上一个字节的"这一遍"状态都没有 —— 每遍扫描的暂存
// (native 那份 spanscan 工作区 · 游程缓冲) 由 Scan 自己现开现关, 对调用方不可见; Go 侧的
// 输入输出缓冲走 Re2Set_req_t.Allocer (一条腿一个 alloc)。身上常驻的是补端点用的单条对象
// 缓存 (惰性建), 所以【别】每遍新建一个。
//
// 换策略的时候 Close 掉旧的: native 那侧是引用计数, 手上还在跑的那几遍照样安全跑完。
// 忘了 Close 也不漏 —— 有 finalizer 兜底, 只是释放时机交给了 GC。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
)

// Re2Set_rrl_t 见文件头。
type Re2Set_rrl_t struct {
	// mu 只护 h 这一个字段: Scan 拿【读锁】把这一遍的 native 暂存开出来就放手, Close 拿写锁。
	mu   sync.RWMutex
	h    *C.cre2_re2set
	size int
}

// NewRe2Set_rrl 给这张【反向】表编一个 rrl 策略对象。
// 🔴 建一次留着, 跟策略同生共死 —— 别每遍新建 (见文件头"生命周期")。
func (r *RegexpSetReverse) NewRe2Set_rrl() (*Re2Set_rrl_t, error) {
	if r == nil || r.s == nil || r.s.h == nil {
		return nil, errors.New("re2native: NewRe2Set_rrl 的表是空的")
	}
	h, err := re2setNew(r.s.h, C.CRE2_RE2SET_rrl, r.s.lens, "Re2Set_rrl_t")
	runtime.KeepAlive(r)
	if err != nil {
		return nil, err
	}
	w := &Re2Set_rrl_t{h: h, size: r.s.size}
	runtime.SetFinalizer(w, func(x *Re2Set_rrl_t) { x.Close() })
	return w, nil
}

// Scan 从末尾往前扫 req.Body 一遍 —— 这是【唯一】一遍全文。要什么见 Re2Set_req_t (零值 = 什么都不要)。
// 同一个对象上可以【并发】调, 每条腿带自己的 Allocer。
func (s *Re2Set_rrl_t) Scan(req Re2Set_req_t) error {
	if s == nil {
		return errors.New("re2native: Re2Set_rrl_t 是 nil")
	}
	return re2setScan(&s.mu, &s.h, s.size, "Re2Set_rrl_t", req)
}

// GetPatternLen 是 pattern 条数 (= Index 的上界)。
func (s *Re2Set_rrl_t) GetPatternLen() int {
	if s == nil {
		return 0
	}
	return s.size
}

// Close 放掉这个策略对象连同它身上的单条对象缓存。换策略的时候调它。
//
// 可重复调; 之后再 Scan 返回 err。native 那侧是引用计数, 所以【正在跑的 Scan 不受影响】。
// 不调也不漏 (finalizer 兜底), 只是释放时机交给 GC。
func (s *Re2Set_rrl_t) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	h := s.h
	s.h = nil
	s.mu.Unlock()
	if h != nil {
		runtime.SetFinalizer(s, nil)
		C.cre2_re2set_free(h)
	}
}
