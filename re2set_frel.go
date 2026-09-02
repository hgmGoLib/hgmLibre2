// re2set_frel.go —— Re2Set_frel_t: 第一趟【正向】· rightmost-END-longest (最右终点最长)。
//
// 🔴 交出来的区间, 同一条 pattern 内部按 Start 【升序】。
//
// 命名规矩 · 请求/结果/缓冲结构 · "三者签名同形但不是 interface" 见 re2set_common.go 头注。
// 实现全在 C++ (cre2_re2set.cpp), 本文件只是句柄 + 薄壳。
//
// ── 口径 ────────────────────────────────────────────────────────────────────
// 上界从末尾起, 取【终点】最靠右的匹配, 同终点取最长 (起点最靠左), 收下之后把上界压到它
// 的起点, 继续往左。等价说法: 把正文倒过来看就是普通的 leftmost-longest。
//
// 🔴 别把它读成 "rightmost-longest" —— 那个词在本库里是 Re2Set_rrl_t 的口径, 挑的是
//    【起点】最靠右, 不是一回事。名字里那个 e 就是"锚终点"的标记:
//
//	b|abc   撞 "abc"    frel 给 [0,3)="abc"    rrl 给 [1,2)="b"
//	aa|a    撞 "aaa"    frel 给 [1,3)="aa"     fll  给 [0,2)="aa"
//
// ── 它比 fll 省在哪 ─────────────────────────────────────────────────────────
// 补起点那一步不再"逐个右端各问一次回看", 而是靠 DFA 状态里的【per-pattern 存活位】把一条
// pattern 的命中切成若干【分量】, 分量内部一次结算 —— 一处命中定下来之后, 下一处的搜索
// 上界立刻被压到它的左端, 所以一个分量里问几次反向锚定 = 这个分量里真的有几处不重叠的匹配。
//
// ── 对外那条保证, 逐字读 ─────────────────────────────────────────────────────
//	① body[Start:End] 是第 Index 条 pattern 的一个真匹配;
//	② 【同一条 pattern】吐的区间互不相交, 按 Start 升序;
//	③ 口径是最右终点最长, 无条件。
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

// Re2Set_frel_t 见文件头。
type Re2Set_frel_t struct {
	// mu 只护 h 这一个字段: Scan 拿【读锁】把这一遍的 native 暂存开出来就放手, Close 拿写锁。
	mu   sync.RWMutex
	h    *C.cre2_re2set
	size int
}

// NewRe2Set_frel 给这张【正向】表编一个 frel 策略对象。
// 🔴 建一次留着, 跟策略同生共死 —— 别每遍新建 (见文件头"生命周期")。
func (s *RegexpSet) NewRe2Set_frel() (*Re2Set_frel_t, error) {
	if s == nil || s.h == nil {
		return nil, errors.New("re2native: NewRe2Set_frel 的表是空的")
	}
	h, err := re2setNew(s.h, C.CRE2_RE2SET_frel, s.lens, "Re2Set_frel_t")
	runtime.KeepAlive(s)
	if err != nil {
		return nil, err
	}
	w := &Re2Set_frel_t{h: h, size: s.size}
	runtime.SetFinalizer(w, func(x *Re2Set_frel_t) { x.Close() })
	return w, nil
}

// Scan 扫 req.Body 一遍 —— 这是【唯一】一遍全文。要什么见 Re2Set_req_t (零值 = 什么都不要)。
// 同一个对象上可以【并发】调, 每条腿带自己的 Allocer。
func (s *Re2Set_frel_t) Scan(req Re2Set_req_t) error {
	if s == nil {
		return errors.New("re2native: Re2Set_frel_t 是 nil")
	}
	return re2setScan(&s.mu, &s.h, s.size, "Re2Set_frel_t", req)
}

// GetPatternLen 是 pattern 条数 (= Index 的上界)。
func (s *Re2Set_frel_t) GetPatternLen() int {
	if s == nil {
		return 0
	}
	return s.size
}

// Close 放掉这个策略对象连同它身上的单条对象缓存。换策略的时候调它。
//
// 可重复调; 之后再 Scan 返回 err。native 那侧是引用计数, 所以【正在跑的 Scan 不受影响】。
// 不调也不漏 (finalizer 兜底), 只是释放时机交给 GC。
func (s *Re2Set_frel_t) Close() {
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
