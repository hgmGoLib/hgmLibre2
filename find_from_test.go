package hgmLibre2

import (
	"reflect"
	"testing"
)

// TestFindWithinKeepsRealNeighbours —— 这组入口的【全部理由】就是这一条: 圈定一段来搜, 但
// ^ / $ / \b 看到的仍是整串的真实邻字节。所以这里逐条对的不是"能不能搜出来", 而是
// "与自己切一刀 s[from:bound] 再搜相比, 答案该不该不一样"。
func TestFindWithinKeepsRealNeighbours(t *testing.T) {
	cases := []struct {
		name         string
		pat          string
		s            string
		from, bound  int
		want         []int // group0 的 [start,end); nil = 该无匹配
		wantSliced   []int // 同一问法但先切片 s[from:bound] 再搜(偏移已加回 from), 用来证明两者【真的】不同
		sliceDiffers bool
	}{
		{
			name: "\\b 左边: 段内看着是词首, 整串上不是",
			pat:  `\bcat\b`, s: "concatx cat", from: 3, bound: 6,
			want: nil, wantSliced: []int{3, 6}, sliceDiffers: true,
		},
		{
			name: "^ 不因为 from 挪了就成立",
			pat:  `^cat`, s: "xxcat", from: 2, bound: 5,
			want: nil, wantSliced: []int{2, 5}, sliceDiffers: true,
		},
		{
			name: "$ 不因为 bound 提前就成立",
			pat:  `cat$`, s: "catx", from: 0, bound: 3,
			want: nil, wantSliced: []int{0, 3}, sliceDiffers: true,
		},
		{
			name: "bound 真的挡住越界的贪心",
			pat:  `a+`, s: "aaaa", from: 0, bound: 2,
			want: []int{0, 2}, wantSliced: []int{0, 2},
		},
		{
			name: "bound 之后的那一处不该被找到",
			pat:  `cat`, s: "xx cat", from: 0, bound: 3,
			want: nil, wantSliced: nil,
		},
		{
			name: "整段就是一处完整匹配(findAllSub 那条路的形状)",
			pat:  `(?:^|,)\s*(\w+)\s*:`, s: `{"a":1,  bcd :2}`, from: 6, bound: 14,
			want: []int{6, 14}, wantSliced: []int{6, 14},
		},
	}
	for _, c := range cases {
		re := MustCompile(c.pat)
		m := re.FindStringSubmatchIndexWithin(c.s, c.from, c.bound)
		var got []int
		if m != nil {
			got = m[:2]
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Within(%q,%d,%d) = %v, 想要 %v", c.name, c.s, c.from, c.bound, got, c.want)
		}
		// 切片版:证明"不切片"这件事真的改变答案, 免得这张表退化成空转绿。
		sm := re.FindStringSubmatchIndex(c.s[c.from:c.bound])
		var sliced []int
		if sm != nil {
			sliced = []int{sm[0] + c.from, sm[1] + c.from}
		}
		if !reflect.DeepEqual(sliced, c.wantSliced) {
			t.Errorf("%s: 切片版 = %v, 想要 %v(这条是对照组, 它变了说明用例本身该改)",
				c.name, sliced, c.wantSliced)
		}
		if c.sliceDiffers && reflect.DeepEqual(got, sliced) {
			t.Errorf("%s: 标了 sliceDiffers 却两边一样 —— 这条用例没在测它该测的东西", c.name)
		}
	}
}

// TestFindWithinBounds —— 越界一律当无匹配, 不能把坏区间递给 C。
func TestFindWithinBounds(t *testing.T) {
	re := MustCompile(`a`)
	s := "aaa"
	for _, c := range [][2]int{{-1, 3}, {0, 4}, {2, 1}, {4, 4}} {
		if m := re.FindStringSubmatchIndexWithin(s, c[0], c[1]); m != nil {
			t.Errorf("Within(%q,%d,%d) = %v, 越界该返 nil", s, c[0], c[1], m)
		}
	}
	// 空段合法, 只是搜不到东西(除非 pattern 能匹配空串)。
	if m := re.FindStringSubmatchIndexWithin(s, 1, 1); m != nil {
		t.Errorf("空段该无匹配, 得到 %v", m)
	}
	// 🔴 原来这里还有一句: `x*` 在空段 [1,1) 上该给 [1,1)。全库拒空串之后 `x*` 编不出来了
	//    (见 emptymatch.go) —— 空段上一律无匹配, 就是上面那一句。
}
