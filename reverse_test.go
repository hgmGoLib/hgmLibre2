package hgmLibre2

// reverse_test.go — 反着扫 (RegexpReverse / NewRegexpSetReverseMaxMem) 的自动测试。
//
// 三件事各自成组:
//   - 【等价】反向的答案必须与正向逐位相同 (含 ^ $ \A \z (?m)^ \b (?i) (?s) 与多字节 UTF-8)。
//     这是全套的前提: 反向只是换个方向读同一条语言, 不缩语义、不做近似。
//   - 【收益】起始类窄于重复类的计数重复上, 反向的状态数必须塌下来 (这是做这件事的唯一理由)。
//   - 【边界】方向是【每条 pattern 各自】的决定 (镜像 pattern 反向反而更贵);
//     反向 DFA 放弃时必须自动退回正向且答案仍然正确。

import (
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// revEquivPatterns 覆盖"反向编译必须自己处理对"的几类: 文本锚 (^ $ \A \z)、行锚 ((?m)^$)、
// 词边界 \b、大小写折叠 (?i)、点匹配换行 (?s)、多字节 UTF-8 字符类、计数重复、交替、可空。
var revEquivPatterns = []string{
	`[A-Za-z][A-Za-z0-9]{2,19}key`,
	`^foo`, `bar$`, `^baz$`, `\Aqux`, `quux\z`,
	`(?m)^line`, `(?m)tail$`,
	`\bword\b`, `\Bin\B`,
	`(?i)Hello`, `(?i)[a-f]{2,4}X`,
	`(?s)a.{3}b`, `a.{3}b`,
	`中[文字]{1,3}串`, `[^\x{0000}-\x{007f}]{2,}`, `✓+`,
	// 🔴 原来这一行末尾还有 `a*` 和 `` (空 pattern) 两条。全库拒空串之后它们编不出来了。
	`x|yy|zzz`, `(?:ab)+c`, `\d{3}-\d{4}`,
	`(?:sk|rk)_(?:live|test)_[0-9a-zA-Z]{6,12}`,
}

// revEquivCorpus 是手捏的边界样本 (全部合法 UTF-8 —— 非法 UTF-8 上本库按原生 RE2 语义,
// 与 stdlib 有已知差异, 见 README, 不在本测试的对拍范围内)。
var revEquivCorpus = []string{
	"", "a", "foo", "xfoo", "foox", "bar", "xbar", "barx",
	"baz", "xbaz", "bazx", "qux", "xqux", "quux", "quuxx",
	"line1\nline2", "head\ntail\nrest", "tail",
	"word", "xword", "word ", " a word here", "swordfish", "inside", "in",
	"hello HELLO HeLLo", "abcdX", "AbCdX",
	"a\n\n\nb", "aXYZb", "中文串", "中文字串", "中串", "✓✓✓", "ünïcödé 中文 ok",
	"123-4567", "x123-4567y", "abcabc", "ababc",
	"abc123key ", "AZzz9key", "aa1key", "sk_live_abcdef12", "rk_test_ABCDEF123456",
}

// revRandomCorpus 造一批【合法 UTF-8】的随机正文, 补手捏样本盖不到的组合。
func revRandomCorpus(n int) []string {
	rng := rand.New(rand.NewSource(20260818))
	alpha := []rune("abcxyzABXZ019 \n-_键中文✓kesy")
	out := make([]string, n)
	for i := range out {
		b := make([]rune, rng.Intn(80))
		for j := range b {
			b[j] = alpha[rng.Intn(len(alpha))]
		}
		out[i] = string(b)
	}
	return out
}

func TestMatchReverse_EquivForward(t *testing.T) {
	corpus := append(append([]string{}, revEquivCorpus...), revRandomCorpus(400)...)
	for _, pat := range revEquivPatterns {
		re, err := Compile(pat)
		if err != nil {
			t.Fatalf("Compile(%q): %v", pat, err)
		}
		rr, err := CompileReverse(pat)
		if err != nil {
			t.Fatalf("CompileReverse(%q): %v", pat, err)
		}
		std := regexp.MustCompile(pat)
		for _, s := range corpus {
			fwd := re.MatchString(s)
			rev := rr.MatchString(s)
			if fwd != rev {
				t.Fatalf("pat=%q text=%q: 正向 %v 反向 %v (stdlib %v)", pat, s, fwd, rev, std.MatchString(s))
			}
			// 顺带把正向钉在 stdlib 上, 免得"两边都错成一样"这种对拍盲区。
			if want := std.MatchString(s); fwd != want {
				t.Fatalf("pat=%q text=%q: 本库 %v stdlib %v", pat, s, fwd, want)
			}
			if got := rr.Match([]byte(s)); got != rev {
				t.Fatalf("pat=%q text=%q: RegexpReverse.Match %v != MatchString %v", pat, s, got, rev)
			}
		}
		rr.FreeC()
		re.FreeC()
	}
}

func TestRegexpSetReverse_EquivForward(t *testing.T) {
	fwdSet, err := NewRegexpSet(revEquivPatterns)
	if err != nil {
		t.Fatalf("NewRegexpSet: %v", err)
	}
	revSet, err := NewRegexpSetReverseMaxMem(revEquivPatterns, DefaultSetMaxMem)
	if err != nil {
		t.Fatalf("NewRegexpSetReverseMaxMem: %v", err)
	}
	// (方向不再是对象上的一个 bool —— NewRegexpSet 给 *RegexpSet, NewRegexpSetReverseMaxMem
	//  给 *RegexpSetReverse, 走错方向是【编译期】错误, 不需要运行期问一句。)
	if revSet.GetPatternLen() != len(revEquivPatterns) {
		t.Fatalf("GetPatternLen=%d want %d", revSet.GetPatternLen(), len(revEquivPatterns))
	}

	corpus := append(append([]string{}, revEquivCorpus...), revRandomCorpus(400)...)
	var fbuf, rbuf []int32
	total := 0
	for _, s := range corpus {
		// 🔴 Match 返回的下标【无序】(真表上实测拿到过 [63 60 64 7] 这种), 比对前必须排。
		a := sortedCopy(fwdSet.Match(s, fbuf))
		b := sortedCopy(revSet.Match(s, rbuf))
		if !equalIdx(a, b) {
			t.Fatalf("text=%q: 正向命中 %v 反向命中 %v", s, a, b)
		}
		total += len(a)
	}
	if total == 0 {
		t.Fatal("整批语料一条都没命中 —— 这个对拍是空的, 语料要改")
	}
	t.Logf("正反两个 set 在 %d 份语料上命中集完全一致 (共 %d 处命中)", len(corpus), total)
}

func sortedCopy(v []int32) []int32 {
	out := append([]int32(nil), v...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalIdx(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// revBombPattern 是那个典型形状: 起始类 [A-Za-z] 严格窄于重复类 [A-Za-z0-9]。
// 正向 DFA 要记住"最近 20 个位置里哪些还可能是起点", 这个集合是任意子集 → 状态数对界指数。
const revBombPattern = `[A-Za-z][A-Za-z0-9]{2,19}key`

// revBodies 造 n 份互不相同的随机正文。
// ⚠ 必须互不相同: 同一份正文反复扫会全程命中 DFA 缓存, 状态数量不到任何东西 (见 dfastats.go 开头)。
func revBodies(n, size int) []string {
	return revBodiesFrom(n, size, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 \n{}\"':,")
}

// revBodiesNoKey 同上, 但字母表里【没有 k/e/y】—— 于是随机正文里必然一次都不出现 "key"。
// 为什么要这一版: 带触发词的 pattern 在"语料里到底有没有出现过触发词"这件事上状态数差好几倍,
// 用全字母表的随机语料时它是【概率事件】(480KB 随机文本里 "key" 的期望出现次数就在 1 上下),
// 测试会时灵时不灵。要拿它当断言就得把这个变量钉死。
func revBodiesNoKey(n, size int) []string {
	return revBodiesFrom(n, size, "abcdfghijlmnopqrstuvwxzABCDFGHIJLMNOPQRSTUVWXZ0123456789 \n{}\"':,")
}

func revBodiesFrom(n, size int, alphabet string) []string {
	rng := rand.New(rand.NewSource(int64(size)*7919 + int64(n) + int64(len(alphabet))))
	al := []byte(alphabet)
	out := make([]string, n)
	for i := range out {
		b := make([]byte, size)
		for j := range b {
			b[j] = al[rng.Intn(len(al))]
		}
		out[i] = string(b)
	}
	return out
}

// setStates 用一个【单条 pattern 的 Set】量状态数 —— 单条 Regexp 没有按对象的水位读数, Set 有 (GetMemInfo)。
// 方向由 reversed 决定, 其余一律相同。预算给足 64MB, 免得读到的是 flush 之后的残值。
func setStates(t *testing.T, pat string, reversed bool, bodies []string) (states int64, usedMB float64, hits int) {
	t.Helper()
	// 正反是两个类型, 所以这里收成两个闭包再往下走。
	var match func(text string, buf []int32) []int32
	var memInfo func() SetMemInfo
	if reversed {
		s, err := NewRegexpSetReverseMaxMem([]string{pat}, 64<<20)
		if err != nil {
			t.Fatalf("建反向 set (pat=%q): %v", pat, err)
		}
		match, memInfo = s.Match, s.GetMemInfo
	} else {
		s, err := NewRegexpSetMaxMem([]string{pat}, 64<<20)
		if err != nil {
			t.Fatalf("建正向 set (pat=%q): %v", pat, err)
		}
		match, memInfo = s.Match, s.GetMemInfo
	}
	var buf []int32
	for _, b := range bodies {
		hits += len(match(b, buf))
	}
	mi := memInfo()
	return mi.States, float64(mi.GetUsedBytes()) / (1 << 20), hits
}

func TestReverseCollapsesStateExplosion(t *testing.T) {
	bodies := revBodies(120, 8<<10)
	// 往一部分正文里塞真命中, 免得"命中集一致"这条断言是空的。
	for i := 0; i < len(bodies); i += 10 {
		bodies[i] = bodies[i][:100] + "ab12key" + bodies[i][100:]
	}
	fs, fm, fh := setStates(t, revBombPattern, false, bodies)
	rs, rm, rh := setStates(t, revBombPattern, true, bodies)
	t.Logf("%s\n  正向 %6d 状态 · %6.2fMB · %d 处命中\n  反向 %6d 状态 · %6.2fMB · %d 处命中",
		revBombPattern, fs, fm, fh, rs, rm, rh)
	if fh != rh || fh == 0 {
		t.Fatalf("命中数 正向 %d 反向 %d (且都不该是 0) —— 反向改了语义就是 bug 不是优化", fh, rh)
	}
	if fs < 1000 {
		t.Fatalf("正向只有 %d 个状态 —— 这份语料没把爆炸跑出来, 本用例等于没测", fs)
	}
	if rs*100 > fs {
		t.Fatalf("反向 %d 状态 / 正向 %d 状态, 没有塌到 1/100 —— 反着扫这条路没生效", rs, fs)
	}
}

// TestReverseDirectionIsPerPattern 钉住"方向是【每条 pattern 各自】的决定, 不是全局开关"。
//
// 用一对镜像形状: 计数窗口在字面量【左边】的正向贵, 在【右边】的反向贵。
// ⚠ 幅度: 本库的反向是 RE2 编译器级的, 不是"把 pattern 倒着写" —— 见
// TestReverseIsNotHandRolledTextReversal, 前者对 {m,n} 友好得多, 所以这里的
// "反向更贵" 只是几倍, 不像正向那边是几万倍。方向依然要按 pattern 各自量, 但选错的代价不对称。
func TestReverseDirectionIsPerPattern(t *testing.T) {
	bodies := revBodiesNoKey(40, 8<<10)
	const revWins = `(?s).{20}key` // 计数窗口在字面量左边: 正向每个字节都开一个起点
	const fwdWins = `key(?s).{20}` // 计数窗口在字面量右边: 反着读就成了上面那个形状

	af, _, _ := setStates(t, revWins, false, bodies)
	ar, _, _ := setStates(t, revWins, true, bodies)
	bf, _, _ := setStates(t, fwdWins, false, bodies)
	br, _, _ := setStates(t, fwdWins, true, bodies)
	t.Logf("%-14s 正向 %d 状态 / 反向 %d 状态  → 该反着扫", revWins, af, ar)
	t.Logf("%-14s 正向 %d 状态 / 反向 %d 状态  → 该正着扫", fwdWins, bf, br)
	if ar >= af {
		t.Fatalf("%s 上反向 %d 状态没有少于正向 %d —— 这一半的前提垮了", revWins, ar, af)
	}
	if br <= bf {
		t.Fatalf("%s 上反向 %d 状态没有多于正向 %d —— 那就不存在'反向也可能更贵'这回事了, "+
			"调用方就不用逐条量方向了, 文档要改", fwdWins, br, bf)
	}
}

// TestReverseIsNotHandRolledTextReversal 钉住这个库为什么值得存在:
// 【库的反向】≠【调用方自己把 pattern 和正文都倒过来】。
//
// 同一条语言、同一串字节, 只是编译方式不同:
//   - 库的反向: RE2 编译器把 concat 反序; Simplify 展开 x{2,19} 得到的"必需拷贝在前、
//     可选嵌套在后", 反序之后可选嵌套跑到了读取顺序的【前面】—— 于是各个起点的活跃集合
//     互相嵌套 (只取最外层), 不构成任意子集, 状态数不炸。
//   - 手写反转: 把 pattern 文本倒过来再正向编, 必需拷贝仍在读取顺序的前面 —— 照炸不误。
//
// 顺带: 手写反转还要多复制一份正文, 且会把多字节 UTF-8 拆散。
func TestReverseIsNotHandRolledTextReversal(t *testing.T) {
	bodies := revBodiesNoKey(60, 8<<10)
	for i := 0; i < len(bodies); i += 10 { // 塞几处真命中, 免得"命中数一致"是空断言
		bodies[i] = bodies[i][:100] + "key12ab" + bodies[i][100:]
	}
	rev := make([]string, len(bodies))
	for i, b := range bodies {
		rev[i] = reverseBytes(b)
	}
	// `key[A-Za-z0-9]{2,19}[A-Za-z]` 的手写反转 (字符类本身没有方向)。
	const pat = `key[A-Za-z0-9]{2,19}[A-Za-z]`
	const handRolled = `[A-Za-z][A-Za-z0-9]{2,19}yek`

	lib, libMB, libHits := setStates(t, pat, true, bodies)         // 库的反向 · 原正文
	hand, handMB, handHits := setStates(t, handRolled, false, rev) // 手写反转 · 反转正文
	t.Logf("库的反向   %6d 状态 · %6.2fMB · %d 处命中", lib, libMB, libHits)
	t.Logf("手写反转   %6d 状态 · %6.2fMB · %d 处命中", hand, handMB, handHits)
	if libHits != handHits {
		t.Fatalf("两条路的命中数不一致 (%d vs %d) —— 本用例的前提是它俩识别同一件事", libHits, handHits)
	}
	if lib*100 > hand {
		t.Fatalf("库的反向 %d 状态 / 手写反转 %d 状态, 没有便宜到 1/100 —— "+
			"那就没必要把这条能力做进库里, 调用方自己反转就够了", lib, hand)
	}
}

func reverseBytes(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func TestMatchReverseFallsBackButStaysCorrect(t *testing.T) {
	// 预算掐到反向 DFA 一定跑不完 —— 这时 SearchDFA 会 bail, 库必须自动退回正向,
	// 答案仍然正确, 只是这一次没省到状态。调用方靠 ScanStats.FellBack 看得见。
	const fbPat = `key[A-Za-z0-9]{2,19}[A-Za-z]`
	rev, err := CompileReverseMaxMem(fbPat, 64<<10)
	if err != nil {
		t.Fatalf("CompileReverseMaxMem: %v", err)
	}
	defer rev.FreeC()
	re, err := CompileMaxMem(fbPat, 64<<10)
	if err != nil {
		t.Fatalf("CompileMaxMem: %v", err)
	}
	defer re.FreeC()
	text := revBodies(1, 1<<20)[0]
	var st ScanStats
	got := rev.MatchStats(text, &st)
	if want := re.MatchString(text); got != want {
		t.Fatalf("退回路径答案错了: 反向 %v 正向 %v", got, want)
	}
	if !st.FellBack {
		t.Skipf("这份预算/语料下反向 DFA 没放弃 (flushes=%d states=%d) —— 退回路径本次没被覆盖到",
			st.Flushes, st.StatesEnd)
	}
	if st.StatesEnd != 0 || st.Bytes != 0 {
		t.Fatalf("FellBack 时计数应当全 0 (结果不是反向 DFA 给的), 实得 %+v", st)
	}
	t.Logf("反向 DFA 放弃 → 自动退回正向, 答案一致 (FellBack=%v)", st.FellBack)
}

func TestMatchReverse_EmptyAndEdges(t *testing.T) {
	// 🔴 原来这里先拿 `a*` 钉"空串/nil 上也该匹配"。全库拒空串之后 `a*` 编不出来了
	//    (见 emptymatch.go), 空正文上一律无匹配 —— 就是下面 zzz 那一段。
	none := MustCompileReverse(`zzz`)
	defer none.FreeC()
	if none.MatchString("") {
		t.Fatal(`zzz 不该匹配空串`)
	}
	// 坏 pattern: 与正向 Compile 同样在构造期就报错
	if _, err := CompileReverse(`(`); err == nil {
		t.Fatal("坏 pattern 应当在 CompileReverse 就报错")
	}
	// 反向 set 的空表 / 单条表
	if _, err := NewRegexpSetReverseMaxMem(nil, DefaultSetMaxMem); err != nil {
		t.Fatalf("空 pattern 表: %v", err)
	}
	if _, err := NewRegexpSetReverseMaxMem([]string{`(`}, DefaultSetMaxMem); err == nil {
		t.Fatal("坏 pattern 应当报错")
	} else if !strings.Contains(err.Error(), "bad pattern") {
		t.Fatalf("错误文案没说清是哪条坏: %v", err)
	}
}

func TestMatchReverseStats_NilIsSameAsMatchReverse(t *testing.T) {
	re := MustCompileReverse(revBombPattern)
	defer re.FreeC()
	for _, s := range []string{"", "abc123key", "nothing here"} {
		if re.MatchStats(s, nil) != re.MatchString(s) {
			t.Fatalf("text=%q: 传 nil st 与不传的结果不一致", s)
		}
	}
}

// TestMatchReverse_ConcurrentLazyInit 盯住反向程序的【惰性构建】: 它是 std::call_once 建的,
// 多个 goroutine 同时第一次调 RegexpReverse.MatchString 时只能建一份, 且谁都不能拿到半成品。
// 这个用例要在 -race 下跑才有意义 (go test -race)。
func TestMatchReverse_ConcurrentLazyInit(t *testing.T) {
	fwd := MustCompile(revBombPattern)
	defer fwd.FreeC()
	re := MustCompileReverse(revBombPattern)
	defer re.FreeC()
	const n = 16
	texts := []string{"abc123key tail", "no hit at all", "", "AZzz9key"}
	want := make([]bool, len(texts))
	for i, s := range texts {
		want[i] = fwd.MatchString(s)
	}
	errs := make(chan string, n)
	done := make(chan struct{})
	for g := 0; g < n; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for k := 0; k < 200; k++ {
				i := k % len(texts)
				if got := re.MatchString(texts[i]); got != want[i] {
					errs <- "并发下 RegexpReverse.MatchString 结果不对: " + texts[i]
					return
				}
			}
		}()
	}
	for g := 0; g < n; g++ {
		<-done
	}
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// TestRegexpReverse_StringAndMaxMem 钉住 RegexpReverse 的两个访问器。
//
// String 的门是【反的是程序, 不是 pattern 文本】: 反向对象里没有任何"反转过的 pattern 串",
// 所以 String() 必须逐字节等于编译时给的原文 —— 一旦哪天有人真去反转文本, 这道门会响。
// GetMaxMem 的门是它与 CompileMaxMem 同一套约定: 显式预算原样读回, <=0 回落 DefaultMaxMem。
func TestRegexpReverse_StringAndMaxMem(t *testing.T) {
	const pat = `[A-Za-z][A-Za-z0-9]{2,19}key`

	rr, err := CompileReverse(pat)
	if err != nil {
		t.Fatalf("CompileReverse(%q): %v", pat, err)
	}
	defer rr.FreeC()
	if got := rr.String(); got != pat {
		t.Fatalf("String()=%q, want 源 pattern %q", got, pat)
	}
	if got := rr.GetMaxMem(); got != DefaultMaxMem {
		t.Fatalf("CompileReverse 出来的 MaxMem=%d, want DefaultMaxMem=%d", got, DefaultMaxMem)
	}

	// MustCompileReverse 走的是同一条路; 顺带盖住带锚 / 多字节的原文回读。
	// 🔴 原来这张表末尾还有一条空 pattern ``。全库拒空串之后它编不出来了 (见 emptymatch.go)。
	for _, p := range []string{pat, `^中[文字]{1,3}串$`, `\bword\b`, `(?i)(?s)a.{3}b`} {
		m := MustCompileReverse(p)
		if got := m.String(); got != p {
			t.Fatalf("MustCompileReverse(%q).String()=%q", p, got)
		}
		m.FreeC()
	}

	// 显式预算原样读回。
	for _, mm := range []int64{1 << 20, 32 << 20, 1 << 30} {
		r, err := CompileReverseMaxMem(pat, mm)
		if err != nil {
			t.Fatalf("CompileReverseMaxMem(%d): %v", mm, err)
		}
		if got := r.GetMaxMem(); got != mm {
			t.Fatalf("CompileReverseMaxMem(%d).GetMaxMem()=%d", mm, got)
		}
		if got := r.String(); got != pat {
			t.Fatalf("CompileReverseMaxMem(%d).String()=%q", mm, got)
		}
		r.FreeC()
	}
	// <=0 回落到默认, 与 CompileMaxMem 的约定一致。
	for _, mm := range []int64{0, -1} {
		r, err := CompileReverseMaxMem(pat, mm)
		if err != nil {
			t.Fatalf("CompileReverseMaxMem(%d): %v", mm, err)
		}
		if got := r.GetMaxMem(); got != DefaultMaxMem {
			t.Fatalf("CompileReverseMaxMem(%d).GetMaxMem()=%d, want 默认 %d", mm, got, DefaultMaxMem)
		}
		r.FreeC()
	}

	// 反向对象读回的预算必须与同预算的正向对象一致 —— 两边共用 cre2 的同一个 max_mem 回读。
	fwd, err := CompileMaxMem(pat, 32<<20)
	if err != nil {
		t.Fatalf("CompileMaxMem: %v", err)
	}
	defer fwd.FreeC()
	rev32, err := CompileReverseMaxMem(pat, 32<<20)
	if err != nil {
		t.Fatalf("CompileReverseMaxMem: %v", err)
	}
	defer rev32.FreeC()
	if fwd.GetMaxMem() != rev32.GetMaxMem() {
		t.Fatalf("同预算下正反读回不一致: 正向 %d, 反向 %d", fwd.GetMaxMem(), rev32.GetMaxMem())
	}
}

// TestMustCompileReverse_PanicsOnBadPattern — 坏 pattern 必须 panic (而不是返回一个空对象),
// 且 panic 文案要说清是哪个函数、哪条 pattern、什么错; 与 MustCompile 的文案格式对齐。
func TestMustCompileReverse_PanicsOnBadPattern(t *testing.T) {
	const bad = `(`
	var got interface{}
	func() {
		defer func() { got = recover() }()
		rr := MustCompileReverse(bad) // 不该返回
		rr.FreeC()
	}()
	if got == nil {
		t.Fatalf("MustCompileReverse(%q) 应当 panic", bad)
	}
	msg, ok := got.(string)
	if !ok {
		t.Fatalf("panic 值应当是 string (对齐 MustCompile), 实得 %T: %v", got, got)
	}
	for _, want := range []string{"re2native: CompileReverse(", bad, "missing )"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("panic 文案 %q 里没有 %q", msg, want)
		}
	}
	// 好 pattern 一定不 panic —— 免得上面那道门被"无脑 panic"糊弄过去。
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("好 pattern 不该 panic: %v", r)
			}
		}()
		MustCompileReverse(`a+`).FreeC()
	}()
}
