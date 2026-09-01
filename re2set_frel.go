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
// 生命周期: 建一次留着反复 Scan;【不是】并发安全的, 一条腿一个或者池化。不用了 Close。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import "errors"

// Re2Set_frel_t 见文件头。
type Re2Set_frel_t struct {
	h    *C.cre2_re2set
	set  *RegexpSet // 🔴 必须存着: C 侧【借】它的 native 句柄
	size int
}

// NewRe2Set_frel 给这张【正向】表开一个 frel 工作区。热路径上建一次长期留着, 别每遍新建。
func (s *RegexpSet) NewRe2Set_frel() (*Re2Set_frel_t, error) {
	if s == nil || s.h == nil {
		return nil, errors.New("re2native: NewRe2Set_frel 的表是空的")
	}
	h, err := re2setNew(s.h, C.CRE2_RE2SET_frel, s.lens, "Re2Set_frel_t")
	if err != nil {
		return nil, err
	}
	return &Re2Set_frel_t{h: h, set: s, size: s.size}, nil
}

// Scan 扫 body 一遍 —— 这是【唯一】一遍全文。要什么见 Re2Set_req_t (nil = 什么都不要)。
func (s *Re2Set_frel_t) Scan(body string, req *Re2Set_req_t) error {
	if s == nil {
		return errors.New("re2native: Re2Set_frel_t 是 nil")
	}
	return re2setScan(s.h, s.size, "Re2Set_frel_t", body, req)
}

// GetPatternLen 是 pattern 条数 (= Index 的上界)。
func (s *Re2Set_frel_t) GetPatternLen() int {
	if s == nil {
		return 0
	}
	return s.size
}

// GetStats 是最近一次 Scan 的账, 见 Re2Set_stats_t (frel 的 Cands 恒 0: 它没有候选这一步,
// Walks == Tries == 问了几次反向锚定)。
func (s *Re2Set_frel_t) GetStats() Re2Set_stats_t {
	if s == nil {
		return Re2Set_stats_t{}
	}
	return re2setStats(s.h)
}

// Close 放掉 native 那份扫描工作区。可重复调; 之后再 Scan 返回 err。
func (s *Re2Set_frel_t) Close() {
	if s == nil || s.h == nil {
		return
	}
	C.cre2_re2set_free(s.h)
	s.h = nil
}
