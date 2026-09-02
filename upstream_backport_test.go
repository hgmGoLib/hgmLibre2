package hgmLibre2

// upstream_backport_test.go — 钉死从上游 RE2 (2023-03-01 之后) 摘回来的那几条修复。
// 每条都拿 stdlib regexp 当 ground truth 对拍 (两边都是 RE2 语义, 结果须一致)。
// 对应的 C++ 侧改动见 VENDOR.txt 的「已改动的上游文件」一节 (标了 backport re2 <commit>)。

import (
	"regexp"
	"strings"
	"testing"
)

// upstream 9d0b5bf (issue 467): 交替被因式分解成字符类时, 折叠字面量的另一半大小写会丢。
// `0a|0[aA]` 里 [aA] 被解析成 litfold{a}, 与前一支的 'a' 合并时 AddFoldedRange 看见 'a'
// 已在类里就提前返回, 'A' 再没进去 —— 于是【整条 pattern 匹配不到 "0A"】, 静默漏报。
func TestBackport_FoldedAlternationFactoring(t *testing.T) {
	cases := []struct {
		pat    string
		inputs []string
	}{
		{`0a|0[aA]`, []string{"0A", "0a", "0b", "x0A"}},
		{`0A|0[aA]`, []string{"0A", "0a"}},
		{`0[aA]|0a`, []string{"0A", "0a"}},
		{`0s|0[sS]`, []string{"0S", "0s"}},
		{`[a-c]|[aA]`, []string{"A", "a", "b", "c", "d", "yK00BASsy"}},
		{`(?s:(?m:[a-c]|[aA]))`, []string{"A", "y-BÉkAS\nA--", "é\nkA__\n"}},
		{`(?:(?:(?s:a)|(?i:A))|(?:(?m:[sS])|(?s:s)))`, []string{" KAKy ska", "A", "S", "s", "a"}},
		{`x(?i:k)|x[kK]`, []string{"xK", "xk"}},
		{`(?i)0a|0[aA]`, []string{"0A", "0a"}},
	}
	for _, c := range cases {
		std := regexp.MustCompile(c.pat)
		mine := MustCompile(c.pat)
		for _, in := range c.inputs {
			eq(t, mine.MatchString(in), std.MatchString(in), "MatchString "+c.pat+" vs "+in)
			eq(t, mine.FindStringIndex(in), std.FindStringIndex(in), "FindStringIndex "+c.pat+" vs "+in)
		}
	}
}

// upstream 6148386: (?<name>expr) 形式的命名组 (.NET/Perl 5.10 写法)。stdlib 从 Go 1.22 起支持,
// 本库原来只认 (?P<name>expr) —— 同一条 pattern 在 stdlib 编得过、在这里报错, 是实打实的口径差。
func TestBackport_AngleNamedCapture(t *testing.T) {
	pats := []string{
		`(?<name>a)`,
		`(?<key>\w+)=(?<num>\d+)`,
		`(?P<a>x)(?<b>y)`, // 两种写法混用
	}
	for _, p := range pats {
		std, err := regexp.Compile(p)
		if err != nil {
			t.Fatalf("stdlib 都编不过, 用例写错了: %s: %v", p, err)
		}
		mine, err := Compile(p)
		if err != nil {
			t.Errorf("Compile(%s) 失败: %v", p, err)
			continue
		}
		eq(t, mine.SubexpNames(), std.SubexpNames(), "SubexpNames "+p)
		eq(t, mine.NumSubexp(), std.NumSubexp(), "NumSubexp "+p)
	}

	mine := MustCompile(`(?<key>\w+)=(?<num>\d+)`)
	std := regexp.MustCompile(`(?<key>\w+)=(?<num>\d+)`)
	for _, in := range []string{"port=8080", "a=1 b=2", "nope"} {
		eq(t, mine.FindStringSubmatch(in), std.FindStringSubmatch(in), "FindStringSubmatch "+in)
	}
}

// upstream c042630: 环视断言仍然不支持 (RE2 不做回溯), 但报错要报成 "invalid perl operator: (?<="
// 而不是被 (?<name>) 那条分支吞成 "invalid named capture"。stdlib 两种都报错, 这里只要求本库报错
// 且错误里带上完整的算子。
func TestBackport_LookAroundErrorArg(t *testing.T) {
	for _, c := range []struct{ pat, want string }{
		{`(?=foo).*`, "(?="},
		{`(?!foo).*`, "(?!"},
		{`(?<=foo).*`, "(?<="},
		{`(?<!foo).*`, "(?<!"},
	} {
		if _, err := regexp.Compile(c.pat); err == nil {
			t.Fatalf("stdlib 居然编过了 %s, 用例写错了", c.pat)
		}
		_, err := Compile(c.pat)
		if err == nil {
			t.Errorf("Compile(%s) 应当报错", c.pat)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("Compile(%s) 错误里没带上算子 %q: %v", c.pat, c.want, err)
		}
	}
}

// upstream e664633: 空宽算子的计数重复 (\b{1000} / (?:^|$){500}) 原来会老老实实展开成上千条指令,
// 而它们语义上就等于一份。改后指令数塌回个位数, 匹配结果不变 —— 拿 stdlib 对拍这一点。
func TestBackport_EmptyWidthRepeatNoBlowup(t *testing.T) {
	// 🔴 原来这张表里还有 `(?:^|$){500}` · `(?:$){28,}` · `\b(?:\b|\B){999}\B` 三条 ——
	//    它们【整条都是零宽】, 匹配的就是空串。2026-09-01 起全库编译入口一律拒能匹配空串的
	//    pattern (见 emptymatch.go), 这三条编不出来了。剩下四条各带至少一个占字节的部分,
	//    照样把"空宽算子的计数重复不该展开成上千条指令"这件事钉住。
	pats := []string{
		`\b{1000}foo`,
		`(?:^){1000}foo`,
		`(?:\b\B){500}x`,
		`a(?:\b){0,900}b`,
	}
	inputs := []string{"", "foo", " foo ", "xfoo", "ab", "a b", "x", "aXb"}
	for _, p := range pats {
		std := regexp.MustCompile(p)
		mine := MustCompile(p)
		for _, in := range inputs {
			eq(t, mine.MatchString(in), std.MatchString(in), "MatchString "+p+" vs "+in)
			eq(t, mine.FindStringIndex(in), std.FindStringIndex(in), "FindStringIndex "+p+" vs "+in)
		}
	}
}

// upstream PR#648: `\p` / `\P` 后面直接跟坏 UTF-8 时, StringPieceToRune 返回 -1,
// 而调用处写的是 !x (只有返回 0 才为真) —— 错误被漏过去, 最后报成 "invalid character
// class range"。stdlib 报的是 invalid UTF-8, 这里对齐口径: 两边都必须拒绝, 且本库的
// 错误要是 UTF-8 那一类。
func TestBackport_BadUTF8AfterPFlag(t *testing.T) {
	for _, p := range []string{"\\p\xff", "\\P\xff", "\\p\xc3", "\\p\xff{Han}", "[\\p\xff]"} {
		if _, err := regexp.Compile(p); err == nil {
			t.Fatalf("stdlib 居然编过了 %q, 用例写错了", p)
		}
		_, err := Compile(p)
		if err == nil {
			t.Errorf("Compile(%q) 应当报错", p)
			continue
		}
		if !strings.Contains(err.Error(), "invalid UTF-8") {
			t.Errorf("Compile(%q) 该报 invalid UTF-8, 实得: %v", p, err)
		}
	}
	// 正常的 \pL / \p{Han} 不能被误伤。
	for _, p := range []string{`\pL+`, `\p{Han}+`, `\PL+`, `[\p{Greek}\pN]+`} {
		std := regexp.MustCompile(p)
		mine := MustCompile(p)
		for _, in := range []string{"abc", "漢字", "123", "αβγ", ""} {
			eq(t, mine.FindStringIndex(in), std.FindStringIndex(in), "FindStringIndex "+p+" vs "+in)
		}
	}
}

// upstream PR#609: 反向扫描 (RE2 找到匹配末尾后倒着扫回起点, 用的是 kLongestMatch 的
// 反向 DFA) 里 p 是递减的, 而"造状态太慢就退回 NFA"那条启发式算的是 p - resetp ——
// 负数转 size_t 后永远大于阈值, 于是【反向 DFA 从来不退回】, 只会一遍遍清空自己重建。
// 下面这几条 pattern 的反向程序状态数爆炸, 修前扫 1MB 要 flush 40+ 次 (~230ms),
// 修后 flush 1 次就交给 NFA (~34ms)。这里只钉结果正确性 —— 时间不进断言
// (机器间差异太大), 要看实测数字自己按上面的 pattern 跑一遍 MatchStats 的 Flushes。
func TestBackport_ReverseDFASlowBail(t *testing.T) {
	if testing.Short() {
		t.Skip("要扫 1MB × 若干条, -short 下跳过")
	}
	buf := make([]byte, 1<<20)
	x := uint32(2463534242)
	for i := range buf {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		buf[i] = byte('a' + x%4)
	}
	txt := string(buf)
	for _, p := range []string{
		`(?s)a[a-d]{24}b[a-d]*`,
		`(?s)a[a-d]{22}b[a-d]*`,
		`(?s)a[a-d]{20}b.*`,
		`(?s)ab[a-d]{24}c[a-d]*`,
	} {
		std := regexp.MustCompile(p)
		mine := MustCompile(p)
		eq(t, mine.FindStringIndex(txt), std.FindStringIndex(txt), "FindStringIndex "+p)
		eq(t, mine.MatchString(txt), std.MatchString(txt), "MatchString "+p)
	}
}

// upstream PR#636: RE2::Set::Compile() 一进门就把 compiled_ 置 true, 失败时 prog_ 仍是
// 空指针, 而 Set::Match() 只看 compiled_ —— C++ 层面是个空指针解引用。Go 这边走不到
// (NewRegexpSetMaxMem 见到 Compile 失败会把整个 set 释放并返回 error), 这条用例钉的就是
// 「走不到」这个前提: 预算不够时必须是 error, 不能给出一个能被 Match 的 set。
func TestBackport_SetCompileFailureIsError(t *testing.T) {
	for _, mm := range []int64{1, 16, 64} {
		s, err := NewRegexpSetMaxMem([]string{"a", "b+", "[0-9]+"}, mm)
		if err == nil {
			t.Errorf("maxMem=%d 竟然编译成功了: %v", mm, s.Match("a1b", nil))
		}
	}
	s, err := NewRegexpSetMaxMem([]string{"a", "b+", "[0-9]+"}, 1<<20)
	if err != nil {
		t.Fatalf("1MB 预算不该失败: %v", err)
	}
	eq(t, len(s.Match("a1b", nil)), 3, "预算够时三条都该命中")
}
