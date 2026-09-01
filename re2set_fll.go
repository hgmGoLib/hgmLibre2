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
// 生命周期: 建一次留着反复 Scan;【不是】并发安全的, 一条腿一个或者池化。不用了 Close。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import "errors"

// Re2Set_fll_t 见文件头。
type Re2Set_fll_t struct {
	h    *C.cre2_re2set
	set  *RegexpSet // 🔴 必须存着: C 侧【借】它的 native 句柄, 不存就会被 finalizer 提前释放
	size int
}

// NewRe2Set_fll 给这张【正向】表开一个 fll 工作区。热路径上建一次长期留着, 别每遍新建。
//
// 补端点要用的那些单条对象缓存在【表】上 (不在工作区里), 所以同一张表开多个工作区
// (按 GOMAXPROCS 池化) 不会把那份缓存乘以份数。
func (s *RegexpSet) NewRe2Set_fll() (*Re2Set_fll_t, error) {
	if s == nil || s.h == nil {
		return nil, errors.New("re2native: NewRe2Set_fll 的表是空的")
	}
	h, err := re2setNew(s.h, C.CRE2_RE2SET_fll, s.lens, "Re2Set_fll_t")
	if err != nil {
		return nil, err
	}
	return &Re2Set_fll_t{h: h, set: s, size: s.size}, nil
}

// Scan 扫 body 一遍 —— 这是【唯一】一遍全文。要什么见 Re2Set_req_t (传 nil = 什么都不要)。
func (s *Re2Set_fll_t) Scan(body string, req *Re2Set_req_t) error {
	if s == nil {
		return errors.New("re2native: Re2Set_fll_t 是 nil")
	}
	return re2setScan(s.h, s.size, "Re2Set_fll_t", body, req)
}

// GetPatternLen 是 pattern 条数 (= Index 的上界)。
func (s *Re2Set_fll_t) GetPatternLen() int {
	if s == nil {
		return 0
	}
	return s.size
}

// GetStats 是最近一次 Scan 的账, 见 Re2Set_stats_t。
func (s *Re2Set_fll_t) GetStats() Re2Set_stats_t {
	if s == nil {
		return Re2Set_stats_t{}
	}
	return re2setStats(s.h)
}

// Close 放掉 native 那份扫描工作区。可重复调; 之后再 Scan 返回 err。
// 表本身 (以及挂在表上的单条对象缓存) 不受影响 —— 那是表的事, 别的工作区还在用。
func (s *Re2Set_fll_t) Close() {
	if s == nil || s.h == nil {
		return
	}
	C.cre2_re2set_free(s.h)
	s.h = nil
}
