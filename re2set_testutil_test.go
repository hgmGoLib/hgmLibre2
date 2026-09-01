package hgmLibre2

// re2set_testutil_test.go —— 三个 Scan 对象的测试共用的那几句。
//
// 🔴 归拢是【调用方】的事: 库这边一个字节都不攒 (攒就是分批接口要躲开的那块缓冲),
//    所以这里的 append 就是"调用方那一句"本身。

import "testing"

// re2setScanFn 是三个 Scan 同形签名的那一份 —— 测试里用它把三家并排喂同一段驱动代码。
// 🔴 这【不是】一个对外的 interface: 库里没有 interface, 这个 func 类型只活在测试里。
type re2setScanFn func(req Re2Set_req_t) error

// scanFlat 跑一遍, 把区间按 pattern 归拢成扁平 (start,end) 表, 同时把命中位表和这一遍的
// 账带回来。
func scanFlat(t *testing.T, scan re2setScanFn, text string) (map[int32][]int32, []int32, Re2Set_stats_t) {
	t.Helper()
	out := map[int32][]int32{}
	var hits []int32
	var st Re2Set_stats_t
	err := scan(Re2Set_req_t{
		Body: text,
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool {
			for _, r := range rs {
				out[r.Index] = append(out[r.Index], r.Start, r.End)
			}
			return true
		},
		HitIndexResultFn: func(h []int32) { hits = append(hits, h...) },
		StatsResultFn:    func(s Re2Set_stats_t) { st = s },
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, hits, st
}

// scanList 跑一遍, 把区间按【交出来的原顺序】收成一条流 (顺序断言用它)。
func scanList(t *testing.T, scan re2setScanFn, text string) []Re2Set_startEnd_t {
	t.Helper()
	var all []Re2Set_startEnd_t
	err := scan(Re2Set_req_t{
		Body: text,
		StartEndResultFn: func(rs []Re2Set_startEnd_t) bool {
			all = append(all, rs...)
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// newFll / newRrl / newFrel 开一个策略对象 (连同它那张表), 测试结束自动 Close。
func newFll(t *testing.T, pats []string) *Re2Set_fll_t {
	t.Helper()
	set, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatal(err)
	}
	s, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func newFrel(t *testing.T, pats []string) *Re2Set_frel_t {
	t.Helper()
	set, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatal(err)
	}
	s, err := set.NewRe2Set_frel()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func newRrl(t *testing.T, pats []string) *Re2Set_rrl_t {
	t.Helper()
	set, err := NewRegexpSetReverseMaxMem(pats, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, err := set.NewRe2Set_rrl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
