package hgmLibre2

// re2set_diff_test.go —— 三个口径【并排】的那几格。
//
// 🔴 为什么要有这个文件: 三者签名同形之后, "统一接口"最容易悄悄变成"统一实现" —— 三家
//    走同一条底座, 哪天有人把某个收口策略抄岔了, 各自那份对拍照样绿 (它们各自与自己的
//    判据对), 只有把三个答案【并排放】才看得出"它们不再是三个口径了"。

import (
	"fmt"
	"testing"
)

// TestRe2Set_ThreeCoordsDiffer —— 同一份语料同一条 pattern, 三个口径【必须互不相同】。
//
// 每一行的答案都是手算的, 不是抄运行结果:
//
//	aa|a  撞 "aaa"   fll 从最左起点起 [0,2)+[2,3); rrl 从最右起点起 [2,3)+[1,2)+[0,1);
//	                 frel 从最右终点起 —— 终点 3 上最长的是 [1,3), 上界压到 1, 再吃 [0,1)。
//	b|abc 撞 "abc"   fll/frel 都给整条 [0,3); rrl 锚【起点】最靠右 ⟹ 只给中间那个 "b"。
//	ab|b  撞 "aab"   fll [1,3)="ab"; rrl [2,3)="b"; frel 终点 3 上最长的是 [1,3)。
func TestRe2Set_ThreeCoordsDiffer(t *testing.T) {
	cases := []struct {
		pat, text        string
		fll, rrl, frel   string
		wantAllDifferent bool
	}{
		{`aa|a`, "aaa", "[{0 0 2} {0 2 3}]", "[{0 2 3} {0 1 2} {0 0 1}]", "[{0 0 1} {0 1 3}]", true},
		{`b|abc`, "abc", "[{0 0 3}]", "[{0 1 2}]", "[{0 0 3}]", false},
		{`ab|b`, "aab", "[{0 1 3}]", "[{0 2 3}]", "[{0 1 3}]", false},
		{`a|ab`, "abab", "[{0 0 2} {0 2 4}]", "[{0 2 4} {0 0 2}]", "[{0 0 2} {0 2 4}]", false},
		// "12 345 6789" 的下标: 1=0 2=1 ' '=2 3=3 4=4 5=5 ' '=6 6=7 7=8 8=9 9=10。
		// 四位数那一段上三家分家: fll 取最左起点 7 ⟹ "678"; rrl 取最右起点 8 ⟹ "789";
		// frel 取最右终点 11 ⟹ "789" (再往左才轮到 "345", 所以它的输出是升序的 [3,6) 在前)。
		{`\d{3}`, "12 345 6789", "[{0 3 6} {0 7 10}]", "[{0 8 11} {0 3 6}]", "[{0 3 6} {0 8 11}]", true},
	}
	for _, c := range cases {
		gotF := fmt.Sprint(scanList(t, newFll(t, []string{c.pat}).Scan, c.text))
		gotR := fmt.Sprint(scanList(t, newRrl(t, []string{c.pat}).Scan, c.text))
		gotE := fmt.Sprint(scanList(t, newFrel(t, []string{c.pat}).Scan, c.text))
		if gotF != c.fll {
			t.Errorf("%q 撞 %q: fll 给 %s, 该是 %s", c.pat, c.text, gotF, c.fll)
		}
		if gotR != c.rrl {
			t.Errorf("%q 撞 %q: rrl 给 %s, 该是 %s", c.pat, c.text, gotR, c.rrl)
		}
		if gotE != c.frel {
			t.Errorf("%q 撞 %q: frel 给 %s, 该是 %s", c.pat, c.text, gotE, c.frel)
		}
		if c.wantAllDifferent && (gotF == gotR || gotF == gotE || gotR == gotE) {
			t.Errorf("%q 撞 %q: 三个口径本该互不相同, 却撞上了: fll=%s rrl=%s frel=%s",
				c.pat, c.text, gotF, gotR, gotE)
		}
	}
}

// TestRe2Set_AllocCrossObject —— 一个 alloc 依次喂给 fll / rrl / frel 和不同大小的表,
// 结果与"各用各的 alloc"逐处相同。
//
// 🔴 这一格钉的是【alloc 是纯缓冲】这个前提 —— 里面一个 native 句柄都没有, 所以不存在
//    "这个 alloc 不是这张表的"这种运行期错误, 那条几乎跑不到、因而基本没测过的分支从根上
//    就不存在。哪天有人往 alloc 里塞了个跟表绑死的东西, 这一格会响。
func TestRe2Set_AllocCrossObject(t *testing.T) {
	small := []string{`\d{3}`}
	big := []string{`\d{3}`, `[a-z]{2,4}`, `[A-Z]{2}\d{2}`, `x[a-f]{1,4}y`}
	text := "abc 123 defg 4567 AB12 xaby zz"

	shared := NewRe2Set_alloc()
	collect := func(scan re2setScanFn, a *Re2Set_alloc_t) string {
		var all []Re2Set_startEnd_t
		var hits []int32
		err := scan(text, &Re2Set_req_t{
			Allocer:          a,
			StartEndResultFn: func(rs []Re2Set_startEnd_t) bool { all = append(all, rs...); return true },
			HitIndexResultFn: func(h []int32) bool { hits = append(hits, h...); return true },
		})
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(all) + " hits=" + fmt.Sprint(hits)
	}
	type row struct {
		name string
		scan re2setScanFn
	}
	rows := []row{
		{"fll/small", newFll(t, small).Scan},
		{"frel/big", newFrel(t, big).Scan},
		{"rrl/big", newRrl(t, big).Scan},
		{"fll/big", newFll(t, big).Scan},
		{"rrl/small", newRrl(t, small).Scan},
		{"frel/small", newFrel(t, small).Scan},
	}
	// 先各用各的 alloc 取基准, 再拿同一个 alloc 依次跑一遍 —— 两边必须逐字相同。
	want := make([]string, len(rows))
	for i, r := range rows {
		want[i] = collect(r.scan, NewRe2Set_alloc())
	}
	for i, r := range rows {
		if got := collect(r.scan, shared); got != want[i] {
			t.Fatalf("%s: 共用 alloc 之后结果变了\n  共用 %s\n  独用 %s", r.name, got, want[i])
		}
	}
	// 反过来再来一遍 (顺序也不许影响结果)。
	for i := len(rows) - 1; i >= 0; i-- {
		if got := collect(rows[i].scan, shared); got != want[i] {
			t.Fatalf("%s: 倒着跑一遍结果变了\n  共用 %s\n  独用 %s", rows[i].name, got, want[i])
		}
	}
}

// TestRe2Set_NilReq —— req == nil (以及两个回调都是 nil) 是合法调用: 不报错、不回调、不分配。
// 🔴 "零值是合法调用"这件事得钉住, 否则调用方会在每个调用点写一圈 nil 判断。
func TestRe2Set_NilReq(t *testing.T) {
	text := "abc 123 defg"
	fll := newFll(t, []string{`\d{3}`, `[a-z]{2,4}`})
	rrl := newRrl(t, []string{`\d{3}`, `[a-z]{2,4}`})
	frel := newFrel(t, []string{`\d{3}`, `[a-z]{2,4}`})
	for _, r := range []struct {
		name string
		scan re2setScanFn
	}{{"fll", fll.Scan}, {"rrl", rrl.Scan}, {"frel", frel.Scan}} {
		if err := r.scan(text, nil); err != nil {
			t.Fatalf("%s: Scan(body, nil) 不该报错: %v", r.name, err)
		}
		empty := &Re2Set_req_t{}
		if err := r.scan(text, empty); err != nil {
			t.Fatalf("%s: 两个回调都 nil 不该报错: %v", r.name, err)
		}
		scan := r.scan
		if n := testing.AllocsPerRun(20, func() { _ = scan(text, nil) }); n != 0 {
			t.Errorf("%s: Scan(body, nil) 该是 0 笔分配, 实得 %.1f", r.name, n)
		}
	}
}

// TestRe2Set_HitsOnlyNoSpan —— 只要命中位表 (StartEndResultFn 为 nil) 时: 正文照样扫完,
// 命中位表与 RegexpSet.Match 逐条相同, 但一处区间都不收口。
func TestRe2Set_HitsOnlyNoSpan(t *testing.T) {
	pats := []string{`\d{3}`, `[a-z]{2,4}`, `ZZZQQQ`}
	set, err := NewRegexpSet(pats)
	if err != nil {
		t.Fatal(err)
	}
	fll, err := set.NewRe2Set_fll()
	if err != nil {
		t.Fatal(err)
	}
	defer fll.Close()
	for _, text := range []string{"abc 123", "nothing", "", "ZZZQQQ 999 zz"} {
		var hits []int32
		if err := fll.Scan(text, &Re2Set_req_t{
			HitIndexResultFn: func(h []int32) bool { hits = append(hits, h...); return true },
		}); err != nil {
			t.Fatal(err)
		}
		var want []int32
		for i := range pats {
			if MustCompile(pats[i]).MatchString(text) {
				want = append(want, int32(i))
			}
		}
		if fmt.Sprint(hits) != fmt.Sprint(want) {
			t.Fatalf("text=%q 命中位表 %v, 该是 %v", text, hits, want)
		}
		if st := fll.GetStats(); st.Emits != 0 {
			t.Fatalf("text=%q 只要位的时候不该收口, 却交了 %d 处", text, st.Emits)
		}
	}
}
