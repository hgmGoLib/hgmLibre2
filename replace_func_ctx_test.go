package hgmLibre2

// replace_func_ctx_test.go — AppendReplaceAllStringFunc 与 ReplaceAllStringFunc / stdlib 逐字节对拍,
// 并钉死本变体存在的三条理由: 追加语义 (已有内容不动 · 没变化一个字节不写) · 同一 ctx+同一块底复用
// 不串味 · 稳态零分配。另钉死两件事: 第二个返回值是 changed 而不是 matched (有匹配但每处 f 都原样
// 写回 ⇒ 回滚 + false), 以及 ReplaceAllStringFunc 自己那块底是【一次开够】而不是增长阶梯
// (裸 Builder 累计要 5×len(src), 那正是本文件这轮改动要杀掉的东西)。
// 语料复用 hgmLibre2_test.go 的 testPatterns/testInputs (含空匹配 a*、锚定 ^the\b、多子组、命名组 ——
// 子组那几条尤其要测: 本变体按 nmatch=1 只回填 group0, 必须证明拼接结果不因此变化)。

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/quick"
)

// replFuncs: 三种典型形状 —— 变长、恒不长于输入 (解码类)、比输入长 (转义类, 逼出"开够之后还要再长")。
var replFuncs = []struct {
	name string
	f    func(string) string
}{
	{"upper", strings.ToUpper},
	{"shrink", func(m string) string {
		if len(m) == 0 {
			return ""
		}
		return m[:1]
	}},
	{"grow", func(m string) string { return m + m + m + m }},
	{"empty", func(string) string { return "" }},
}

func TestAppendReplaceAllStringFunc_EquivLibAndStdlib(t *testing.T) {
	var ctx ReplaceAllStringFunc_ctx_t // 零值可用, 且跨所有 pattern × input × f 复用 (模拟热路径)
	var buf []byte
	for _, pat := range testPatterns {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		for _, in := range testInputs {
			for _, rf := range replFuncs {
				want := std.ReplaceAllStringFunc(in, rf.f)
				if got := mine.ReplaceAllStringFunc(in, rf.f); got != want {
					t.Errorf("ReplaceAllStringFunc 与 stdlib 不一致 pat=%q in=%q f=%s got=%q want=%q",
						pat, in, rf.name, got, want)
				}
				var changed bool
				buf, changed = ctx.AppendReplaceAllStringFunc(buf[:0], mine, in, rf.f)
				got := string(buf)
				if !changed { // 没变化 (无匹配 / 有匹配但逐字节相同): 什么都不写, 结果就是原 src
					if len(buf) != 0 {
						t.Errorf("changed=false 却写了东西 pat=%q in=%q got=%q", pat, in, got)
					}
					got = in
				}
				if changed && got == in { // changed 的定义就是"与 src 不同", 反向也钉住
					t.Errorf("changed=true 但产物与 src 逐字节相同 pat=%q in=%q f=%s", pat, in, rf.name)
				}
				if got != want {
					t.Errorf("Append 版与 stdlib 不一致 pat=%q in=%q f=%s got=%q want=%q",
						pat, in, rf.name, got, want)
				}
			}
		}
	}
}

// TestAppendReplaceAllStringFunc_AppendsNotOverwrites — 语义是【追加】: dst 已有内容原样保留,
// changed=false 时一个字节都不许写 (调用方靠 changed 而不是靠长度差判有没有变化)。
// 两条 false 的腿都走一遍: ①压根没匹配; ②有匹配但每处 f 都原样写回 —— ②正是回滚那句在管的,
// 它跟 engine 那边 `if ncr := DecodeNumericCharRefs(s); ncr != s` 的取值必须一致。
func TestAppendReplaceAllStringFunc_AppendsNotOverwrites(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	var ctx ReplaceAllStringFunc_ctx_t
	dst := []byte("KEEP|")
	dst, changed := ctx.AppendReplaceAllStringFunc(dst, re, "abc123def45", func(m string) string { return "#" })
	if !changed || string(dst) != "KEEP|abc#def#" {
		t.Fatalf("追加语义错 changed=%v got=%q", changed, dst)
	}
	before := string(dst)
	dst, changed = ctx.AppendReplaceAllStringFunc(dst, re, "no digits here", func(m string) string { return "#" })
	if changed || string(dst) != before {
		t.Fatalf("①无匹配却动了 dst changed=%v got=%q", changed, dst)
	}
	dst, changed = ctx.AppendReplaceAllStringFunc(dst, re, "abc123def45", func(m string) string { return m })
	if changed || string(dst) != before {
		t.Fatalf("②有匹配但每处都原样写回, 该回滚成没写过 changed=%v got=%q", changed, dst)
	}
	// 回滚只退 len 不退 cap: 底已经换大了, 但 len/内容与调用前一致 —— 下一趟接着写不受影响。
	dst, changed = ctx.AppendReplaceAllStringFunc(dst, re, "x9y", func(m string) string { return "#" })
	if !changed || string(dst) != before+"x#y" {
		t.Fatalf("回滚之后再写就乱了 changed=%v got=%q", changed, dst)
	}
}

// TestAppendReplaceAllStringFunc_ReuseNoBleed — 长的一趟之后紧跟短的一趟, buf[:0] 复用不得回放
// 上一趟的尾巴; ctx 里的位置表同理 (上趟 8 处命中, 这趟 1 处)。
func TestAppendReplaceAllStringFunc_ReuseNoBleed(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	ctx := NewReplaceAllStringFunc_ctx(8)
	var buf []byte
	buf, _ = ctx.AppendReplaceAllStringFunc(buf[:0], re, "1 2 3 4 5 6 7 8", func(m string) string { return "<" + m + ">" })
	if string(buf) != "<1> <2> <3> <4> <5> <6> <7> <8>" {
		t.Fatalf("首趟就不对 got=%q", buf)
	}
	buf, _ = ctx.AppendReplaceAllStringFunc(buf[:0], re, "only 42 here", func(m string) string { return "<" + m + ">" })
	if string(buf) != "only <42> here" {
		t.Fatalf("复用后串味 got=%q", buf)
	}
}

// TestAppendReplaceAllStringFunc_SteadyStateNoAlloc — 本变体存在的全部理由。
// 两块底 (结果 buf + ctx 位置表) 吃饱之后反复调必须是【零 Go 堆分配】(C 侧那块 malloc/free 不计
// Go 堆; f 这里返回 src 的子串故自己不分配); 同语料的 ReplaceAllStringFunc 则必然线性分配 ——
// 两边都量, 单边写不出判别力。
func TestAppendReplaceAllStringFunc_SteadyStateNoAlloc(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("word 12345 ")
	}
	in := sb.String()
	f := func(m string) string { return m[:1] } // 不分配: 返回入参子串
	ctx := NewReplaceAllStringFunc_ctx(500)
	var buf []byte
	buf, _ = ctx.AppendReplaceAllStringFunc(buf[:0], re, in, f) // 预热: 两块底长到位
	if len(buf) == 0 {
		t.Fatalf("语料没命中, 量不出东西")
	}
	reuse := testing.AllocsPerRun(20, func() {
		buf, _ = ctx.AppendReplaceAllStringFunc(buf[:0], re, in, f)
	})
	old := testing.AllocsPerRun(20, func() {
		sinkStr = re.ReplaceAllStringFunc(in, f)
	})
	t.Logf("RESULT: Append+ctx=%.0f allocs/次 · ReplaceAllStringFunc=%.0f allocs/次 (500 处命中)", reuse, old)
	if reuse != 0 {
		t.Fatalf("🔴 稳态仍有 %.0f 次分配 —— 复用没生效", reuse)
	}
	if old < 2 {
		t.Fatalf("🔴 对照组 ReplaceAllStringFunc 只分配 %.0f 次, 本锚量不出差别 (库改过了?)", old)
	}
}

// TestReplaceAllStringFunc_ResultBufferGrownOnce — ReplaceAllStringFunc 自己那块底必须【一次开够】。
// 裸 strings.Builder 从 0 长到 N 是 1→2→4→…→N 的阶梯, 累计分配收敛到 5N; 一次开够则是 1N。
// 语料故意做成【命中稀疏】(每 1KB 一处), 好让位置表小到可忽略, 量出来的就是结果底本身:
// f 返回等长的定串 ⇒ 产物与输入等长, 一趟的 Go 堆增量卡在 2×len(src) 以内才算一次开够。
func TestReplaceAllStringFunc_ResultBufferGrownOnce(t *testing.T) {
	re := MustCompile(`[0-9]+`)
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		sb.WriteString(strings.Repeat("a", 1019))
		sb.WriteString("12345")
	}
	in := sb.String() // 约 2MB, 2000 处命中 (位置表 4000 个 int = 32KB, 可忽略)
	// 等长但【真改字节】: 等长是为了让量出来的就是 1×len(src); 真改字节是因为逐字节没变会走回滚
	// 那条快返 (零分配直接还 src), 那样这个门就空转了。
	f := func(m string) string { return "54321" }

	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	sinkStr = re.ReplaceAllStringFunc(in, f)
	runtime.ReadMemStats(&m1)
	grew := m1.TotalAlloc - m0.TotalAlloc
	t.Logf("RESULT: len(src)=%d · 本趟 Go 堆分配=%d 字节 (%.2f×len(src))", len(in), grew, float64(grew)/float64(len(in)))
	if len(sinkStr) != len(in) {
		t.Fatalf("产物长度不对 got=%d want=%d", len(sinkStr), len(in))
	}
	if grew > uint64(2*len(in)) {
		t.Fatalf("🔴 结果底不是一次开够: 分配 %d 字节 = %.2f×len(src) (阶梯法收敛到 5×)",
			grew, float64(grew)/float64(len(in)))
	}
}

var sinkStr string

// TestReplaceAllStringFunc_QuickDiffStdlib — 手挑 + 随机的 stdlib 差分门。
// (2026-08-22 从调用方搬来:那边原本有一份"一次开到位"的本地实现,门是拿它跟库对拍;
// 这轮把那份实现合进库里了,门也就该跟着回到库这边。)
// 语料的着眼点与 EquivLibAndStdlib 不同,故两个都留:调用方真在用的那两条(\uXXXX / &#…;)
// + 一条多子组的。(原来还挑了一条能匹配空串的 `x*`, 全库拒空串之后它编不出来了。)
func TestReplaceAllStringFunc_QuickDiffStdlib(t *testing.T) {
	pats := []string{
		`\\u[0-9a-fA-F]{4}`,
		`&#(?:[xX]([0-9a-fA-F]{1,6})|([0-9]{1,7}));`,
		// 🔴 原来这里有一条 `x*` (能匹配空串)。全库拒空串之后它编不出来了。
		`[0-9]+`,
	}
	fns := []func(string) string{
		strings.ToUpper,
		func(m string) string { return "" },
		func(m string) string { return m + m + m },
		func(m string) string { return m }, // 恒等: 逼出"有匹配但一个字节没变"那条回滚
	}
	cases := []string{
		"", "no match here", `中文`, `head A tail`,
		"&#x49;gnore &#73; all", "&#0; &#x110000; &#x41;", "xxx", "axxxbxc",
		"1234", "a1b22c333", strings.Repeat(`é`, 300), strings.Repeat("&#65;", 300),
	}
	var ctx ReplaceAllStringFunc_ctx_t
	var buf []byte
	for _, pat := range pats {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		for _, f := range fns {
			for _, s := range cases {
				want := std.ReplaceAllStringFunc(s, f)
				if got := mine.ReplaceAllStringFunc(s, f); got != want {
					t.Fatalf("pat=%q in=%.30q: 本库 %.60q != stdlib %.60q", pat, s, got, want)
				}
				var changed bool
				buf, changed = ctx.AppendReplaceAllStringFunc(buf[:0], mine, s, f)
				got := s
				if changed {
					got = string(buf)
				} else if len(buf) != 0 {
					t.Fatalf("pat=%q in=%.30q: changed=false 却写了 %.60q", pat, s, buf)
				}
				if got != want {
					t.Fatalf("pat=%q in=%.30q: Append 版 %.60q != stdlib %.60q", pat, s, got, want)
				}
			}
			std, mine, f := std, mine, f
			if err := quick.Check(func(s string) bool {
				return mine.ReplaceAllStringFunc(s, f) == std.ReplaceAllStringFunc(s, f)
			}, &quick.Config{MaxCount: 3000}); err != nil {
				t.Fatalf("pat=%q quick 差分失败: %v", pat, err)
			}
		}
	}
}
