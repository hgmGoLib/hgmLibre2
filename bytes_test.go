package hgmLibre2

// bytes 系方法的测试. 三层:
//  1. TestBytesEquivStdlib      —— 全部 12 个方法逐一对拍 stdlib regexp 的 []byte 系 (复用 string 系
//     的 testPatterns × testInputs 语料, 天然覆盖命中/不命中/空匹配/未参与组/unicode).
//  2. TestBytesHitAndMiss       —— 每个方法各钉一对【命中】与【不命中】的手算期望 (不对拍 stdlib),
//     把 nil 与空切片的区别也钉死.
//  3. 契约测试                  —— 零拷贝共享底层、输入不被改写、无改动复用原 src、比 string(b) 少分配.
// 只用 stdlib testing, 不引入外部依赖 (同本包其它测试).

import (
	"bytes"
	"regexp"
	"runtime"
	"testing"
)

// TestBytesEquivStdlib 把每个 []byte 方法与 stdlib 的同名方法逐一对拍.
// ReplaceAll 对拍 stdlib 的 ReplaceAllLiteral (而非 ReplaceAll): 本库 repl 是【字面】不展开 $1,
// stdlib 里语义相同的那个正是 ReplaceAllLiteral —— 故这里可以放心用含 '$' 的 repl 一起对拍.
func TestBytesEquivStdlib(t *testing.T) {
	replBytes := []string{"", " ", "X", "<m>", "$1", `\1`} // 含 $1/\1: 钉死字面性
	for _, pat := range testPatterns {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)

		for _, in := range testInputs {
			b := []byte(in)
			msg := func(m string) string { return m + " | pat=" + pat + " in=" + in }

			eq(t, mine.Match(b), std.Match(b), msg("Match"))
			eq(t, mine.Find(b), std.Find(b), msg("Find"))
			eq(t, mine.FindIndex(b), std.FindIndex(b), msg("FindIndex"))
			eq(t, mine.FindSubmatch(b), std.FindSubmatch(b), msg("FindSubmatch"))
			eq(t, mine.FindSubmatchIndex(b), std.FindSubmatchIndex(b), msg("FindSubmatchIndex"))

			for _, n := range []int{-1, 0, 1, 2} {
				eq(t, mine.FindAll(b, n), std.FindAll(b, n), msg("FindAll"))
				eq(t, mine.FindAllIndex(b, n), std.FindAllIndex(b, n), msg("FindAllIndex"))
				eq(t, mine.FindAllSubmatch(b, n), std.FindAllSubmatch(b, n), msg("FindAllSubmatch"))
				eq(t, mine.FindAllSubmatchIndex(b, n), std.FindAllSubmatchIndex(b, n), msg("FindAllSubmatchIndex"))
			}

			for _, repl := range replBytes {
				eq(t, mine.ReplaceAll(b, []byte(repl)), std.ReplaceAllLiteral(b, []byte(repl)),
					msg("ReplaceAll repl="+repl))
			}

			f := func(m []byte) []byte { return append(append([]byte("<"), m...), '>') }
			eq(t, mine.ReplaceAllFunc(b, f), std.ReplaceAllFunc(b, f), msg("ReplaceAllFunc"))
		}
	}
}

// TestBytesEquivStringMethods 交叉验证: 同一输入下 []byte 门面与 string 门面结果必须一致
// (两者共用同一套匹配内核, 任何一侧的 index→切片转换写错都会在这里暴露).
func TestBytesEquivStringMethods(t *testing.T) {
	for _, pat := range testPatterns {
		re := MustCompile(pat)
		for _, in := range testInputs {
			b := []byte(in)
			msg := func(m string) string { return m + " | pat=" + pat + " in=" + in }

			eq(t, re.Match(b), re.MatchString(in), msg("Match vs MatchString"))
			eq(t, string(re.Find(b)), re.FindString(in), msg("Find vs FindString"))
			eq(t, re.FindIndex(b), re.FindStringIndex(in), msg("FindIndex vs FindStringIndex"))
			eq(t, re.FindSubmatchIndex(b), re.FindStringSubmatchIndex(in), msg("FindSubmatchIndex vs string"))
			eq(t, re.FindAllIndex(b, -1), re.FindAllStringIndex(in, -1), msg("FindAllIndex vs string"))
			eq(t, re.FindAllSubmatchIndex(b, -1), re.FindAllStringSubmatchIndex(in, -1), msg("FindAllSubmatchIndex vs string"))
			eq(t, string(re.ReplaceAll(b, []byte("#"))), re.ReplaceAllString(in, "#"), msg("ReplaceAll vs ReplaceAllString"))
		}
	}
}

// TestBytesHitAndMiss 给每个 []byte 方法各钉一对【命中】与【不命中】的手算期望.
// 不命中一律要求 nil (而非长度 0 的非 nil 切片) —— 与 stdlib 一致, DeepEqual 会区分.
func TestBytesHitAndMiss(t *testing.T) {
	num := MustCompile(`\d+`)
	hit := []byte("ab12cd34")  // num 命中两处
	miss := []byte("abcdefgh") // num 完全不命中

	// Match
	eq(t, num.Match(hit), true, "Match 命中")
	eq(t, num.Match(miss), false, "Match 不命中")

	// Find
	eq(t, num.Find(hit), []byte("12"), "Find 命中")
	eq(t, num.Find(miss), []byte(nil), "Find 不命中应为 nil")

	// FindIndex
	eq(t, num.FindIndex(hit), []int{2, 4}, "FindIndex 命中")
	eq(t, num.FindIndex(miss), []int(nil), "FindIndex 不命中应为 nil")

	// FindAll
	eq(t, num.FindAll(hit, -1), [][]byte{[]byte("12"), []byte("34")}, "FindAll 命中")
	eq(t, num.FindAll(miss, -1), [][]byte(nil), "FindAll 不命中应为 nil")

	// FindAllIndex
	eq(t, num.FindAllIndex(hit, -1), [][]int{{2, 4}, {6, 8}}, "FindAllIndex 命中")
	eq(t, num.FindAllIndex(miss, -1), [][]int(nil), "FindAllIndex 不命中应为 nil")

	// 带捕获组的四个 Submatch 方法
	pair := MustCompile(`(\w)(\d)`)
	hitP := []byte("a1 b2")
	missP := []byte("xyz")

	eq(t, pair.FindSubmatch(hitP), [][]byte{[]byte("a1"), []byte("a"), []byte("1")}, "FindSubmatch 命中")
	eq(t, pair.FindSubmatch(missP), [][]byte(nil), "FindSubmatch 不命中应为 nil")

	eq(t, pair.FindSubmatchIndex(hitP), []int{0, 2, 0, 1, 1, 2}, "FindSubmatchIndex 命中")
	eq(t, pair.FindSubmatchIndex(missP), []int(nil), "FindSubmatchIndex 不命中应为 nil")

	eq(t, pair.FindAllSubmatch(hitP, -1), [][][]byte{
		{[]byte("a1"), []byte("a"), []byte("1")},
		{[]byte("b2"), []byte("b"), []byte("2")},
	}, "FindAllSubmatch 命中")
	eq(t, pair.FindAllSubmatch(missP, -1), [][][]byte(nil), "FindAllSubmatch 不命中应为 nil")

	eq(t, pair.FindAllSubmatchIndex(hitP, -1), [][]int{{0, 2, 0, 1, 1, 2}, {3, 5, 3, 4, 4, 5}},
		"FindAllSubmatchIndex 命中")
	eq(t, pair.FindAllSubmatchIndex(missP, -1), [][]int(nil), "FindAllSubmatchIndex 不命中应为 nil")

	// 未参与的捕获组 → nil (不是空切片)
	opt := MustCompile(`(x)?(y)`)
	eq(t, opt.FindSubmatch([]byte("zy")), [][]byte{[]byte("y"), []byte(nil), []byte("y")},
		"FindSubmatch 未参与组应为 nil")

	// ReplaceAll
	eq(t, num.ReplaceAll(hit, []byte("#")), []byte("ab#cd#"), "ReplaceAll 命中")
	eq(t, num.ReplaceAll(miss, []byte("#")), miss, "ReplaceAll 不命中应原样")

	// ReplaceAllFunc
	wrap := func(m []byte) []byte { return append(append([]byte("<"), m...), '>') }
	eq(t, num.ReplaceAllFunc(hit, wrap), []byte("ab<12>cd<34>"), "ReplaceAllFunc 命中")
	eq(t, num.ReplaceAllFunc(miss, wrap), miss, "ReplaceAllFunc 不命中应原样")
}

// TestReplaceAllBytesIsLiteral 钉死 ReplaceAll 的 repl 是【纯字面】(同 ReplaceAllString):
// $1/${name}/$$/\1 都按原始字节插入, 不展开. 期望值手算.
func TestReplaceAllBytesIsLiteral(t *testing.T) {
	cases := []struct{ pat, src, repl, want string }{
		{`a`, "banana", "$1X", "b$1Xn$1Xn$1X"},   // $1 当字面
		{`(a)`, "aba", "[$1]", "[$1]b[$1]"},      // 有捕获组也不展开
		{`o`, "foo", "$$", "f$$$$"},              // $$ 不折叠成 $
		{`x`, "axbxc", `\1`, `a\1b\1c`},          // RE2 的 \1 也不展开
		{`(?P<k>\d+)`, "n=42", "${k}", "n=${k}"}, // ${name} 当字面
		{`z`, "abc", "Q", "abc"},                 // 不命中: 原样
	}
	for _, c := range cases {
		got := MustCompile(c.pat).ReplaceAll([]byte(c.src), []byte(c.repl))
		eq(t, string(got), c.want, "ReplaceAll literal pat="+c.pat+" repl="+c.repl)
	}
}

// TestFindReplaceWithinBytes 对拍 string 版 FindReplaceWithin, 并各钉一条命中/不命中.
func TestFindReplaceWithinBytes(t *testing.T) {
	find := MustCompile(`a[\s\-]*b[\s\-]*c`) // 被分隔符拆开的骨架
	strip := MustCompile(`[\s\-]`)           // 段内要删掉的分隔符

	// 命中: 段内分隔符被删, 段外原样保留
	got := find.FindReplaceWithinBytes(strip, []byte("x a-b c y"), []byte(""))
	eq(t, string(got), "x abc y", "FindReplaceWithinBytes 命中")

	// 不命中: 原样返回
	missSrc := []byte("nothing here")
	gotMiss := find.FindReplaceWithinBytes(strip, missSrc, []byte(""))
	eq(t, string(gotMiss), "nothing here", "FindReplaceWithinBytes 不命中应原样")
	if &gotMiss[0] != &missSrc[0] {
		t.Error("FindReplaceWithinBytes 不命中时应复用原 src 切片 (零分配), 实际发生了拷贝")
	}

	// 与 string 版逐字一致
	for _, in := range []string{"", "x a-b c y", "nothing here", "a b c a  -  b--c"} {
		for _, repl := range []string{"", "_"} {
			eq(t, string(find.FindReplaceWithinBytes(strip, []byte(in), []byte(repl))),
				find.FindReplaceWithin(strip, in, repl),
				"FindReplaceWithinBytes vs string 版 in="+in+" repl="+repl)
		}
	}
}

// TestRegexpSetBytes: MatchBytes/MatchAnyBytes 的命中/不命中, 以及与 string 版一致.
func TestRegexpSetBytes(t *testing.T) {
	set, err := NewRegexpSet([]string{`foo\d`, `bar`, `^zed`})
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	buf := make([]int32, 0, 3)

	// 命中: 只有 bar 那条
	eq(t, set.MatchBytes([]byte("xx bar xx"), buf), []int32{1}, "MatchBytes 命中")
	eq(t, set.MatchAnyBytes([]byte("xx bar xx"), buf), true, "MatchAnyBytes 命中")

	// 命中多条
	got := set.MatchBytes([]byte("zed foo7 bar"), buf)
	eq(t, len(got), 3, "MatchBytes 三条全命中")

	// 不命中
	eq(t, set.MatchBytes([]byte("nothing at all"), buf), []int32{}, "MatchBytes 不命中应为空")
	eq(t, set.MatchAnyBytes([]byte("nothing at all"), buf), false, "MatchAnyBytes 不命中")

	// 与 string 版一致
	for _, in := range testInputs {
		eq(t, set.MatchBytes([]byte(in), buf), set.Match(in, buf), "MatchBytes vs Match in="+in)
	}
}

// TestFindIndexCtxBytes: ctx 的 []byte 变体, 命中/不命中 + 与 string 版一致.
func TestFindIndexCtxBytes(t *testing.T) {
	re := MustCompile(`\d+`)
	ctx := NewFindStringIndex_ctx()

	eq(t, ctx.FindIndex(re, []byte("ab12cd")), []int{2, 4}, "ctx.FindIndex 命中")
	eq(t, ctx.FindIndex(re, []byte("abcdef")), []int(nil), "ctx.FindIndex 不命中应为 nil")

	// 零值 ctx 也能用; 复用同一 ctx 反复调结果不串
	var zero FindStringIndex_ctx_t
	eq(t, zero.FindIndex(re, []byte("xx7")), []int{2, 3}, "零值 ctx.FindIndex 命中")

	for _, pat := range testPatterns {
		p := MustCompile(pat)
		for _, in := range testInputs {
			eq(t, ctx.FindIndex(p, []byte(in)), p.FindStringIndex(in),
				"ctx.FindIndex vs FindStringIndex pat="+pat+" in="+in)
		}
	}
}

// TestBytesShareSrcBacking 钉死零拷贝契约: Find/FindAll/FindSubmatch 返回的是 src 的子切片
// (共享底层数组), 不是副本. 改写返回值会改到 src —— 与 stdlib bytes 系一致.
func TestBytesShareSrcBacking(t *testing.T) {
	re := MustCompile(`\d+`)

	src := []byte("ab12cd")
	got := re.Find(src)
	got[0] = '9'
	eq(t, string(src), "ab92cd", "Find 返回值应与 src 共享底层数组")

	src2 := []byte("a1 b2")
	all := re.FindAll(src2, -1)
	all[1][0] = '8'
	eq(t, string(src2), "a1 b8", "FindAll 元素应与 src 共享底层数组")

	// cap 已限到匹配末尾: append 不会越写到 src 的后续字节
	src3 := []byte("a1XY")
	m := re.Find(src3)
	m = append(m, 'Z')
	eq(t, string(src3), "a1XY", "返回切片 cap 应限到匹配末尾, append 不得污染 src")
	eq(t, string(m), "1Z", "append 后内容")
}

// TestBytesNoChangeReusesSrc 钉死惰性物化: Replace 系逐字节无改动时直接返回原 src 切片 (零分配).
func TestBytesNoChangeReusesSrc(t *testing.T) {
	re := MustCompile(`\d+`)

	// 无匹配
	src := []byte("no digits")
	out := re.ReplaceAll(src, []byte("#"))
	if &out[0] != &src[0] {
		t.Error("ReplaceAll 无匹配时应复用原 src 切片, 实际发生了拷贝")
	}

	// 有匹配但 repl 与命中段逐字节相同 → 同样无改动
	src2 := []byte("a7b")
	out2 := re.ReplaceAll(src2, []byte("7"))
	if &out2[0] != &src2[0] {
		t.Error("ReplaceAll 替换后字节不变时应复用原 src 切片, 实际发生了拷贝")
	}

	// ReplaceAllFunc 无匹配同理
	out3 := re.ReplaceAllFunc(src, func(m []byte) []byte { return m })
	if &out3[0] != &src[0] {
		t.Error("ReplaceAllFunc 无匹配时应复用原 src 切片, 实际发生了拷贝")
	}
}

// TestBytesSrcNotMutated: 所有【只读】方法都不得改动传入的 b (C 侧只读它的底层数组).
func TestBytesSrcNotMutated(t *testing.T) {
	for _, pat := range testPatterns {
		re := MustCompile(pat)
		for _, in := range testInputs {
			b := []byte(in)
			re.Match(b)
			re.Find(b)
			re.FindIndex(b)
			re.FindSubmatch(b)
			re.FindSubmatchIndex(b)
			re.FindAll(b, -1)
			re.FindAllIndex(b, -1)
			re.FindAllSubmatch(b, -1)
			re.FindAllSubmatchIndex(b, -1)
			if string(b) != in {
				t.Fatalf("只读方法改动了输入 b: pat=%s in=%q got=%q", pat, in, string(b))
			}
		}
	}
}

// TestBytesNilAndEmptyInput: nil 与空切片输入不得 panic, 且与 stdlib 逐一同结果.
func TestBytesNilAndEmptyInput(t *testing.T) {
	for _, pat := range []string{`a*`, `\d+`, `^$`} {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		for _, b := range [][]byte{nil, {}} {
			label := "nil"
			if b != nil {
				label = "empty"
			}
			msg := func(m string) string { return m + " | pat=" + pat + " in=" + label }
			eq(t, mine.Match(b), std.Match(b), msg("Match"))
			eq(t, mine.Find(b), std.Find(b), msg("Find"))
			eq(t, mine.FindIndex(b), std.FindIndex(b), msg("FindIndex"))
			eq(t, mine.FindSubmatch(b), std.FindSubmatch(b), msg("FindSubmatch"))
			eq(t, mine.FindAll(b, -1), std.FindAll(b, -1), msg("FindAll"))
			eq(t, mine.ReplaceAll(b, []byte("X")), std.ReplaceAllLiteral(b, []byte("X")), msg("ReplaceAll"))
			eq(t, mine.ReplaceAllFunc(b, func(m []byte) []byte { return m }),
				std.ReplaceAllFunc(b, func(m []byte) []byte { return m }), msg("ReplaceAllFunc"))
		}
	}
}

// TestBytesCheaperThanStringCopy 钉死这套 API 的存在理由: 直接传 []byte 比 string(b) 转换少分配.
// 只比较分配【次数】相对关系 (不写死绝对值), 避免依赖 runtime 细节.
func TestBytesCheaperThanStringCopy(t *testing.T) {
	re := MustCompile(`needle\d+`)
	b := bytes.Repeat([]byte("x"), 64*1024) // 大正文: string(b) 要整块拷贝

	direct := testing.AllocsPerRun(50, func() { re.Match(b) })
	viaCopy := testing.AllocsPerRun(50, func() { re.MatchString(string(b)) })

	if direct >= viaCopy {
		t.Errorf("Match([]byte) 应比 MatchString(string(b)) 少分配: direct=%v viaCopy=%v", direct, viaCopy)
	}
	if direct != 0 {
		t.Errorf("Match([]byte) 应零分配, 实测 %v", direct)
	}
}

// TestBytesViewSurvivesGC: bytesStr 造的 string 视图指向 b 的底层数组, GC 压力下不得被回收/搬移
// 导致结果错乱 (unsafe 转换最主要的风险点).
func TestBytesViewSurvivesGC(t *testing.T) {
	re := MustCompile(`needle\d+`)
	for i := 0; i < 200; i++ {
		b := append(bytes.Repeat([]byte("x"), 3000), []byte("needle42")...)
		runtime.GC()
		if !re.Match(b) {
			t.Fatalf("第 %d 轮 Match 漏命中", i)
		}
		eq(t, re.FindIndex(b), []int{3000, 3008}, "GC 压力下 FindIndex")
		eq(t, string(re.Find(b)), "needle42", "GC 压力下 Find")
	}
}

func BenchmarkMatchBytes_Direct(b *testing.B) {
	re := MustCompile(`needle\d+`)
	body := bytes.Repeat([]byte("x"), 64*1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.Match(body)
	}
}

func BenchmarkMatchBytes_ViaStringCopy(b *testing.B) {
	re := MustCompile(`needle\d+`)
	body := bytes.Repeat([]byte("x"), 64*1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.MatchString(string(body))
	}
}
