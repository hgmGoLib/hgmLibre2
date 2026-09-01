// re2set_fll.go —— Re2Set_fll_t: 第一趟【正向】· leftmost-longest。
//
// 🔴 交出来的区间, 同一条 pattern 内部按 Start 【升序】。
//
// 命名规矩 · 请求/结果/缓冲结构 · "三者签名同形但不是 interface" 见 re2set_common.go 头注。
// 实现全在 C++ (cre2_re2set.cpp), 本文件只是句柄 + 薄壳。
//
// ── 口径 ────────────────────────────────────────────────────────────────────
// 在还没被占掉的那段正文里, 反复取【起点最靠左】的那个匹配, 同起点取【最长】, 吐出去,
// 再往右接着找。等价于 stdlib 的 re.Longest().FindAllStringIndex。
//
// 🔴 【不是】"与 FindAllStringIndex 相同"。stdlib 默认那个是 leftmost-first (贪心), 两者在
//    "同一起点上贪心先撞到的比最长的短"时给不同的右端。要对拍就拿 Longest() 那个去对,
//    拿默认那个对会是【假红】。
//
// ── 对外那条保证, 逐字读 ─────────────────────────────────────────────────────
//	① body[Start:End] 是第 Index 条 pattern 的一个真匹配;
//	② 【同一条 pattern】吐的区间互不相交, 按 Start 升序;
//	③ 口径是 leftmost-longest, 【无条件】, 没有旋钮, 没有"快而不准"的档。
//
// 🔴 ② 只管【单条】。两条 pattern 在同一片正文上照样重叠 —— 那不是重复, 是两个问题各要
//    一个答案。实例: "Passport No: A123456780" 上 \b[A-Z][12]\d{8}\b (台湾身份证) 与
//    (?i)\b[A-Z]{1,2}\d{7,9}[A-Z]?\b (护照号) 抢【完全同一段】; 合并的话谁赢只由常量写在
//    第几行决定, 而谁能活要等下游把校验位跑完才知道 —— 库这层两样都不知道。真语料上被
//    ≥2 条 pattern 盖住的字节占已盖住字节的 55.6%, 所以这是常态不是边角。
//
// 🔴 各条 pattern 的结果是【交错】着来的, 不按 pattern 分组 (同一条内部才有序)。
//    想按条归拢是调用方那边一句 append 的事, 库这边归拢就得攒 = 又是缓冲。
//
// ── 要么全给, 要么整遍不算数 ────────────────────────────────────────────────
// Scan 返回 err 就是这一遍作废 (已经交出去的批也不算数), 调用方整篇走老路 FindAll。
// err 的来由全是 maxMem 配小了 (扫描那遍 DFA 放弃 / 补端点的单条对象编不出来 / 回推放弃)。
// 【没有】"这几条没给全你自己补"的中间态: 一个调用方造不出来的错误码不该出现在返回值里 ——
// 它逼出的兜底跑不到 → 跑不到就没法测 → 没法测的代码基本是错的。
//
// ── 生命周期: 【进程级】· 建一次留着 · 可以并发 Scan ────────────────────────
// 一个策略一份, 跟策略同生共死。它身上一个字节的"这一遍"状态都没有 —— 每遍扫描的暂存
// (native 那份 spanscan 工作区 · 游程缓冲 · 候选缓冲) 由 Scan 自己现开现关, 对调用方不可见;
// Go 侧的输入输出缓冲走 Re2Set_req_t.Allocer (一条腿一个 alloc)。
//
// 它身上真正常驻的是【补端点用的单条正向/反向对象缓存】: 惰性建, 最大那张 158 条生产表
// 实测 9.6MB。所以【别】每遍新建一个 —— 那是把 9.6MB 重编一遍再扔掉。
//
// 换策略的时候 Close 掉旧的: native 那侧是引用计数, 手上还在跑的那几遍照样安全跑完,
// 最后一个走的人关灯。忘了 Close 也不漏 —— 有 finalizer 兜底, 只是释放时机交给了 GC。
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

// Re2Set_fll_t 见文件头。
type Re2Set_fll_t struct {
	// mu 只护 h 这一个字段: Scan 拿【读锁】把这一遍的 native 暂存开出来就放手, Close 拿写锁。
	// 读锁是共享的 ⟹ 并发 Scan 之间只在开头那一瞬碰一下, 之后互不相干。
	mu   sync.RWMutex
	h    *C.cre2_re2set
	size int
}

// NewRe2Set_fll 给这张【正向】表编一个 fll 策略对象。
//
// 🔴 建一次留着, 跟策略同生共死 —— 别每遍新建 (见文件头"生命周期")。建的时候顺带把整表的
// DFA 建出来, 那是建策略该付的钱。
func (s *RegexpSet) NewRe2Set_fll() (*Re2Set_fll_t, error) {
	if s == nil || s.h == nil {
		return nil, errors.New("re2native: NewRe2Set_fll 的表是空的")
	}
	h, err := re2setNew(s.h, C.CRE2_RE2SET_fll, s.lens, "Re2Set_fll_t")
	runtime.KeepAlive(s)
	if err != nil {
		return nil, err
	}
	w := &Re2Set_fll_t{h: h, size: s.size}
	runtime.SetFinalizer(w, func(x *Re2Set_fll_t) { x.Close() })
	return w, nil
}

// Scan 扫 req.Body 一遍 —— 这是【唯一】一遍全文。要什么见 Re2Set_req_t (零值 = 什么都不要)。
// 同一个对象上可以【并发】调, 每条腿带自己的 Allocer。
func (s *Re2Set_fll_t) Scan(req Re2Set_req_t) error {
	if s == nil {
		return errors.New("re2native: Re2Set_fll_t 是 nil")
	}
	return re2setScan(&s.mu, &s.h, s.size, "Re2Set_fll_t", req)
}

// GetPatternLen 是 pattern 条数 (= Index 的上界)。
func (s *Re2Set_fll_t) GetPatternLen() int {
	if s == nil {
		return 0
	}
	return s.size
}

// GetViableOneStats 报【已经被建出来】的那些"反向单条 set"的账: 几条 · 状态数合计 ·
// 状态区实际字节合计。与 RegexpSet.GetMemInfo 同一个用途 (量内存去哪了), 不制造状态。
//
// 那些单条对象是补起点用的, 惰性建 ⟹ 没被真问过位置的 pattern 一条都不占。
// 🔴 这是 fll 这条路的【常驻】开销 —— 挂新表之前先量这个数。
//
//	最大的那张 158 条生产表实测: 89 条被真问到位置, 合计 9.6MB。
func (s *Re2Set_fll_t) GetViableOneStats() (n int, states, arenaCap int64) {
	if s == nil {
		return 0, 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.h == nil {
		return 0, 0, 0
	}
	var cn C.int
	var cs, ca C.longlong
	C.cre2_re2set_one_viable_stats(s.h, &cn, &cs, &ca)
	return int(cn), int64(cs), int64(ca)
}

// Close 放掉这个策略对象连同它身上的单条对象缓存 (那 9.6MB)。换策略的时候调它。
//
// 可重复调; 之后再 Scan 返回 err。native 那侧是引用计数, 所以【正在跑的 Scan 不受影响】——
// 它们各攥着一份, 最后一个走的人才真拆。不调也不漏 (finalizer 兜底), 只是释放时机交给 GC。
func (s *Re2Set_fll_t) Close() {
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
