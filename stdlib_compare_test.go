package hgmLibre2

// stdlib_compare_test.go — 本库与 Go 标准库 regexp 的同语料对照测量。
//
// 这份测量是 doc/与标准库regexp怎么选.md 里每一个数字的来源。文档说"哪种形状该用哪个引擎",
// 全部由这里跑出来; 改了库或者换了机器, 重跑一遍就知道结论还成不成立:
//
//	go test -run 'TestStdlibCompare' -v .
//
// 量四样, 缺一样都会得出错误结论:
//   - CPU: 分【热缓存】(同一份正文反复扫) 与【换语料】(每次一份新正文 = 产线形态) 两档 ——
//     RE2 的 DFA 是懒构造的, 单形状 benchmark 第二遍起一个状态都不再建, 量不到造状态的代价。
//   - Go 侧分配: 标准库每次匹配都要在 Go 堆上留东西, 本库的匹配全在 C 侧做, 稳态是 0 B/op。
//   - 并发: 本库每次搜索要拿一次 DFA 状态缓存的读锁 (见 README "Concurrency"), 不线性扩展;
//     标准库没有这把锁。只量串行会把这条漏掉, 而它正好是短输入那一档翻盘的原因。
//   - 内存峰值: 一条路一个子进程各报 VmHWM。跑在同一个进程里高水位是共享的, 谁先跑谁吃亏。
//
// 语料与 pattern 都是【按形状合成的】, 不取自任何具体调用方: 结论要能按形状套用, 而不是按业务。

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ── 语料 ────────────────────────────────────────────────────────────────────

type stdcmpLCG struct{ s uint64 }

func (r *stdcmpLCG) next() uint64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return r.s >> 33
}

var stdcmpWords = strings.Fields(`the quick brown fox jumps over a lazy dog while summer rain falls
on the old stone bridge and every traveller who passes counts three white boats moored below near
the mill where children throw bread to the ducks until the light fades behind distant hills`)

// stdcmpProse 造英文散文 —— 这一档对标准库最有利: 里面几乎不出现 " / 等标点,
// 所以标准库那条"提出字面量前缀再 memchr"的快路能一路走到底。
func stdcmpProse(n int, seed uint64) string {
	r := &stdcmpLCG{s: seed}
	var b strings.Builder
	b.Grow(n + 32)
	for b.Len() < n {
		b.WriteString(stdcmpWords[r.next()%uint64(len(stdcmpWords))])
		if r.next()%9 == 0 {
			b.WriteString(".\n")
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// stdcmpJSON 造满篇 " { } : 的流式 JSON —— 首字节是 " 的 pattern 在这一档上,
// 标准库的 memchr 快路每隔几个字节就要停一次, 退化成逐位置重试。
func stdcmpJSON(n int, seed uint64) string {
	r := &stdcmpLCG{s: seed}
	var b strings.Builder
	b.Grow(n + 64)
	for b.Len() < n {
		b.WriteString(`data: {"kind":"chunk","seq":`)
		fmt.Fprintf(&b, "%d", r.next()%40)
		b.WriteString(`,"item":{"kind":"text","text":"`)
		for j := 0; j < 6; j++ {
			b.WriteString(stdcmpWords[r.next()%uint64(len(stdcmpWords))])
			b.WriteByte(' ')
		}
		b.WriteString("\"}}\n\n")
	}
	return b.String()
}

// stdcmpDense 造全小写无空格的最坏形状 —— 字符类 + 计数重复那一档在这里从每个偏移都能起头。
func stdcmpDense(n int, seed uint64) string {
	r := &stdcmpLCG{s: seed}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + r.next()%26)
	}
	return string(b)
}

const (
	stdcmpBodySize = 100 << 10 // 每份正文 100KiB
	stdcmpRotate   = 16        // 互不相同的正文份数 (产线是每个请求一份新正文)
)

func stdcmpBodies(kind string, k, n int) []string {
	out := make([]string, k)
	for i := range out {
		seed := uint64(i)*1099511628211 + 7
		switch kind {
		case "json":
			out[i] = stdcmpJSON(n, seed)
		case "dense":
			out[i] = stdcmpDense(n, seed)
		default:
			out[i] = stdcmpProse(n, seed)
		}
	}
	return out
}

// ── 被测形状 ────────────────────────────────────────────────────────────────

type stdcmpCase struct {
	Name  string
	Pat   string
	Kind  string // body = 实参是整篇正文; short = 实参是几十字节的标识符
	Shape string
}

var stdcmpCases = []stdcmpCase{
	// 整篇正文量级的输入
	{"lit_plain", `ZZQ-MARKER-42`, "body", "纯字面量 (标准库能提前缀走 memchr)"},
	{"lit_ci", `(?i)host\.example\.invalid/`, "body", "大小写不敏感的字面量 (标准库提不出前缀)"},
	{"alt_lit", `alpha_beta|\bnew\s+Gamma\s*\(`, "body", "两支交替, 一支纯字面量一支带 \\b"},
	{"ci_alt_bword", `(?i)alpha\s+(all\s+)?(beta|gamma|delta)\b`, "body", "(?i) + 交替 + \\b, 无字面量前缀"},
	{"ci_alt_nest", `(?i)(alpha|beta|gamma)\s+(delta\s+|epsilon\s+(if\s+)?)?(zeta\s+(eta|theta)\s+)`, "body", "多层可选的交替"},
	{"ci_alt_opt", `(?i)(alpha|beta|gamma)(the|all|your)?(delta)?(epsilon|zetas?|eta|thetas?)`, "body", "四段几乎全可选的交替"},
	{"digit_groups", `\b(?:\d{4}[- ]){3}\d{4}\b`, "body", "定长数字分组 + 两侧 \\b"},
	{"ci_kv_class", `(?i)[?&](?:alpha[_-]?beta|beta|gamma[_-]?delta|delta|epsilon)=[^&\s"']+`, "body", "查询串里取 key=value 的值"},
	{"class_repeat", `[A-Za-z0-9+/]{40,}={0,2}`, "body", "字符类 + 开区间计数重复"},
	{"json_field", `"(alpha|beta)":"((?:[^"\\]|\\.)*)"`, "body", "JSON 字段值 (值里含转义)"},

	// 几十字节的短输入 (标识符 / 主机名 / 摘要一类的校验)
	{"anch_class", `^[A-Za-z0-9_-]+$`, "short", "锚定字符类校验"},
	{"anch_hex", `^[a-f0-9]{64}$`, "short", "锚定定长十六进制"},
	{"anch_host", `^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`, "short", "锚定主机名校验"},
	{"anch_alt_lit", `^(?:aa_|bb_|cc_|dd_|ee_|ff_gg_|hh-|ii-jj-|ii-|kk-)`, "short", "锚定的字面量前缀交替"},
}

// 短输入语料: 与各形状对得上、且【不命中】—— 校验器绝大多数调用走的是拒绝路径。
var stdcmpShortInput = map[string]string{
	"anch_class":   "item-prod-eu-west-1",
	"anch_hex":     "3b1f9d2c4e6a8b0d1f3a5c7e9b2d4f6a8c0e2b4d6f8a1c3e5b7d9f0a2c4e6b8d",
	"anch_host":    "api.example.invalid",
	"anch_alt_lit": "XYZQIOSFODNN7EXAMPLE",
}

func stdcmpVerdict(stdNs, reNs float64) string {
	if stdNs < reNs {
		return fmt.Sprintf("stdlib 快 %.2fx", reNs/stdNs)
	}
	return fmt.Sprintf("本库快 %.2fx", stdNs/reNs)
}

func stdcmpRow(name, tag string, sw, sr testing.BenchmarkResult) string {
	return fmt.Sprintf("%-14s %-6s %12.0f %12.0f  %7dB/%-3d %7dB/%-3d  %s",
		name, tag, float64(sw.NsPerOp()), float64(sr.NsPerOp()),
		sw.AllocedBytesPerOp(), sw.AllocsPerOp(),
		sr.AllocedBytesPerOp(), sr.AllocsPerOp(),
		stdcmpVerdict(float64(sw.NsPerOp()), float64(sr.NsPerOp())))
}

// ── 一、吞吐 + 分配 ─────────────────────────────────────────────────────────

func TestStdlibCompare_Throughput(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	bodies := stdcmpBodies("prose", stdcmpRotate, stdcmpBodySize)

	fmt.Printf("\n=== 一、吞吐 + Go 侧分配 (正文 %dKiB 英文散文 · %d 份互不相同) ===\n",
		stdcmpBodySize>>10, stdcmpRotate)
	fmt.Printf("%-14s %-6s %12s %12s  %11s %11s  %s\n",
		"case", "档", "stdlib ns", "本库 ns", "stdlib 分配", "本库 分配", "结论")

	for _, c := range stdcmpCases {
		std := regexp.MustCompile(c.Pat)
		re2 := MustCompile(c.Pat)

		if c.Kind == "short" {
			s := stdcmpShortInput[c.Name]
			if std.MatchString(s) != re2.MatchString(s) {
				t.Fatalf("%s: 两引擎不同解", c.Name)
			}
			sw := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = std.MatchString(s)
				}
			})
			sr := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = re2.MatchString(s)
				}
			})
			fmt.Println(stdcmpRow(c.Name, "短串", sw, sr))
			continue
		}

		for _, s := range bodies {
			if std.MatchString(s) != re2.MatchString(s) {
				t.Fatalf("%s: 两引擎不同解", c.Name)
			}
		}
		warm := bodies[0]
		sw := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = std.MatchString(warm)
			}
		})
		sr := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = re2.MatchString(warm)
			}
		})
		fmt.Println(stdcmpRow(c.Name, "热缓存", sw, sr))

		sw2 := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = std.MatchString(bodies[i%stdcmpRotate])
			}
		})
		sr2 := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = re2.MatchString(bodies[i%stdcmpRotate])
			}
		})
		fmt.Println(stdcmpRow(c.Name, "换语料", sw2, sr2))
	}

	// 地板价: 空输入, 量的是"过一次桥要多少钱"。
	{
		std := regexp.MustCompile(`(?i)alpha\s+(all\s+)?beta`)
		re2 := MustCompile(`(?i)alpha\s+(all\s+)?beta`)
		sw := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = std.MatchString("")
			}
		})
		sr := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = re2.MatchString("")
			}
		})
		fmt.Println(stdcmpRow("empty_input", "地板价", sw, sr))
	}
}

// ── 二、语料形态 ────────────────────────────────────────────────────────────

func TestStdlibCompare_Corpus(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	kinds := []string{"prose", "json", "dense"}
	corpora := map[string][]string{}
	for _, k := range kinds {
		corpora[k] = stdcmpBodies(k, stdcmpRotate, stdcmpBodySize)
	}
	fmt.Printf("\n=== 二、换语料形态 (同样 %dKiB · 同样 %d 份) ===\n", stdcmpBodySize>>10, stdcmpRotate)
	fmt.Printf("%-14s %-6s %12s %12s  %s\n", "case", "语料", "stdlib ns", "本库 ns", "结论")
	for _, c := range stdcmpCases {
		if c.Kind != "body" {
			continue
		}
		std := regexp.MustCompile(c.Pat)
		re2 := MustCompile(c.Pat)
		for _, k := range kinds {
			bs := corpora[k]
			for _, s := range bs {
				if std.MatchString(s) != re2.MatchString(s) {
					t.Fatalf("%s/%s: 两引擎不同解", c.Name, k)
				}
			}
			sw := testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					_ = std.MatchString(bs[i%stdcmpRotate])
				}
			})
			sr := testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					_ = re2.MatchString(bs[i%stdcmpRotate])
				}
			})
			fmt.Printf("%-14s %-6s %12.0f %12.0f  %s\n", c.Name, k,
				float64(sw.NsPerOp()), float64(sr.NsPerOp()),
				stdcmpVerdict(float64(sw.NsPerOp()), float64(sr.NsPerOp())))
		}
	}
}

// ── 三、并发 ────────────────────────────────────────────────────────────────

// TestStdlibCompare_Parallel —— 共享一个 *Regexp 往上加 goroutine。
// 本库每次搜索要拿一次 DFA 状态缓存的读锁, 标准库没有这把锁: 每次匹配的活越少,
// 这把锁占的比重越大。短输入那一档就是这么翻盘的。
func TestStdlibCompare_Parallel(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	body := stdcmpProse(stdcmpBodySize, 1)
	rows := []struct {
		name, pat, input string
		par              int
	}{
		{"anch_host/短串", `^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`, "api.example.invalid", 1},
		{"anch_host/短串", `^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`, "api.example.invalid", 10},
		{"anch_host/短串", `^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`, "api.example.invalid", 50},
		{"ci_alt_bword/100KiB", `(?i)alpha\s+(all\s+)?(beta|gamma|delta)\b`, body, 1},
		{"ci_alt_bword/100KiB", `(?i)alpha\s+(all\s+)?(beta|gamma|delta)\b`, body, 10},
		{"ci_alt_bword/100KiB", `(?i)alpha\s+(all\s+)?(beta|gamma|delta)\b`, body, 50},
	}
	fmt.Printf("\n=== 三、并发 (GOMAXPROCS=%d) ===\n", runtime.GOMAXPROCS(0))
	fmt.Printf("%-22s %9s %13s %13s  %s\n", "case", "goroutine", "stdlib ns/op", "本库 ns/op", "结论")
	for _, r := range rows {
		std := regexp.MustCompile(r.pat)
		re2 := MustCompile(r.pat)
		sw := testing.Benchmark(func(b *testing.B) {
			b.SetParallelism(r.par)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = std.MatchString(r.input)
				}
			})
		})
		sr := testing.Benchmark(func(b *testing.B) {
			b.SetParallelism(r.par)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = re2.MatchString(r.input)
				}
			})
		})
		fmt.Printf("%-22s %9d %13.1f %13.1f  %s\n", r.name, r.par*runtime.GOMAXPROCS(0),
			float64(sw.NsPerOp()), float64(sr.NsPerOp()),
			stdcmpVerdict(float64(sw.NsPerOp()), float64(sr.NsPerOp())))
	}
}

// ── 四、编译成本 ────────────────────────────────────────────────────────────

func TestStdlibCompare_Compile(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	fmt.Printf("\n=== 四、编译成本 (包级变量 = 进程启动一次性; 运行期现编则进热路径) ===\n")
	fmt.Printf("%-14s %-6s %12s %12s  %11s %11s  %s\n",
		"case", "档", "stdlib ns", "本库 ns", "stdlib 分配", "本库 分配", "结论")
	for _, c := range stdcmpCases {
		sw := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = regexp.MustCompile(c.Pat)
			}
		})
		sr := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				re := MustCompile(c.Pat)
				re.FreeC()
			}
		})
		fmt.Println(stdcmpRow(c.Name, "编译", sw, sr))
	}
}

// ── 五、两种"短输入"的分岔 ──────────────────────────────────────────────────

// TestStdlibCompare_ShortPath —— "输入短"本身不是判据, 形状才是。
// ① 一次调用拿 N 条正则打同一条几百字节的串: 每条都要过一次桥, 但只要形状是标准库要回溯的,
//    过桥价照样赢回来, 而且本库这条路稳态零分配。
// ② 运行期【现编】一条正则再打一句话: 编译价进热路径, 这一档标准库赢。
func TestStdlibCompare_ShortPath(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	pats := []string{
		`(?i)(--alpha[-_]?beta\s*[=:]\s*)('[^']*'|"[^"]*"|\S+)`,
		`(?i)(--(?:gamma[-_]?|delta[-_]?)?epsilon\s*[=:]\s*)('[^']*'|"[^"]*"|\S+)`,
		`(?i)(--(?:zeta|zetaeta)\s*[=:]\s*)('[^']*'|"[^"]*"|\S+)`,
		`(?i)((?:ALPHA_BETA_GAMMA|ALPHA_DELTA_EPSILON|ALPHA_ZETA_ETA)\s*=\s*)('[^']*'|"[^"]*"|\S+)`,
		`(?i)((?:Theta:\s*Iota\s+))('[^']*'|"[^"]*"|\S+)`,
		`(?i)((?:\w*(?:KAPPA|LAMBDA|MU|NU|XI_OMICRON)\w*)\s*=\s*)('[^']*'|"[^"]*"|\S+)`,
	}
	line := `/opt/tool/bin/runner --mode=serve --listen 127.0.0.1:18080 --config /etc/tool/tool.yaml --log-level info --group acme-prod --upstream https://api.example.invalid`
	stds := make([]*regexp.Regexp, len(pats))
	re2s := make([]*Regexp, len(pats))
	for i, p := range pats {
		stds[i] = regexp.MustCompile(p)
		re2s[i] = MustCompile(p)
	}
	sw := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s := line
			for _, re := range stds {
				s = re.ReplaceAllString(s, "${1}[REDACTED]")
			}
			_ = s
		}
	})
	sr := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s := line
			for _, re := range re2s {
				// 本库的 ReplaceAllString 是【字面】repl, 要 ${1} 只能走 Func 版。
				s = re.ReplaceAllStringFunc(s, func(m string) string { return m })
			}
			_ = s
		}
	})
	fmt.Printf("\n=== 五、两种短输入 ===\n")
	fmt.Printf("① %d 条正则 × 一条 %dB 的串 (不命中)\n", len(pats), len(line))
	fmt.Println("  " + stdcmpRow("multi_short", "替换", sw, sr))

	word := "revenue"
	sent := "the quarterly revenues were restated after the auditor flagged a revenue recognition issue"
	sw2 := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `(?:e|es|ed|d|ing|ings|ion|ions|ment|ments|s)?\b`)
			_ = re.MatchString(sent)
		}
	})
	sr2 := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			re := MustCompile(`(?i)\b` + QuoteMeta(word) + `(?:e|es|ed|d|ing|ings|ion|ions|ment|ments|s)?\b`)
			_ = re.MatchString(sent)
			re.FreeC()
		}
	})
	fmt.Printf("② 运行期现编 (?i)\\b<词>\\b 再打一句 %dB 的话\n", len(sent))
	fmt.Println("  " + stdcmpRow("compile_hot", "现编", sw2, sr2))
}

// ── 六、内存峰值 ────────────────────────────────────────────────────────────

const stdcmpPeakChildEnv = "HGMLIBRE2_STDCMP_CHILD"

// stdcmpPeakPats 把那 10 条正文形状复制 rep 份 (各带一个互不相同的无害后缀, 防止两边各自去重),
// 模拟"一个进程里常驻 N 条包级正则"。
func stdcmpPeakPats(rep int) []string {
	var out []string
	for i := 0; i < rep; i++ {
		for _, c := range stdcmpCases {
			if c.Kind == "body" {
				out = append(out, c.Pat+fmt.Sprintf(`(?:zz%d)?`, i))
			}
		}
	}
	return out
}

func TestStdlibCompare_PeakChild(t *testing.T) {
	mode := os.Getenv(stdcmpPeakChildEnv)
	if mode == "" {
		t.Skip("不是子进程")
	}
	// mode = "<engine>-<scan|hold>", engine ∈ {none,std,re2}
	parts := strings.SplitN(mode, "-", 2)
	engine, action := parts[0], parts[1]

	pats := stdcmpPeakPats(11) // 110 条
	var bodies []string
	if action == "scan" {
		bodies = stdcmpBodies("dense", stdcmpRotate, stdcmpBodySize)
	}
	// base 在语料造完之后取: 语料本身两条路一样多, 算进去只会把要看的差别冲淡。
	base := vmHWM(t)
	hits := 0
	switch engine {
	case "std":
		res := make([]*regexp.Regexp, len(pats))
		for i, p := range pats {
			res[i] = regexp.MustCompile(p)
		}
		for _, s := range bodies {
			for _, re := range res {
				if re.MatchString(s) {
					hits++
				}
			}
		}
		runtime.KeepAlive(res)
	case "re2":
		res := make([]*Regexp, len(pats))
		for i, p := range pats {
			res[i] = MustCompile(p)
		}
		for _, s := range bodies {
			for _, re := range res {
				if re.MatchString(s) {
					hits++
				}
			}
		}
		runtime.KeepAlive(res)
	}
	extra := ""
	if engine == "re2" {
		extra = fmt.Sprintf(" DFA整表清空(flush)=%d", GetDFAStats().Resets)
	}
	fmt.Printf("PEAK %-9s 正则=%3d 正文=%2d×%dKiB VmHWM增量=%6.2fMB 命中=%d%s\n",
		mode, len(pats), len(bodies), stdcmpBodySize>>10,
		float64(vmHWM(t)-base)/(1<<20), hits, extra)
}

func TestStdlibCompare_Peak(t *testing.T) {
	if os.Getenv(stdcmpPeakChildEnv) != "" {
		t.Skip("子进程不再往下 fork")
	}
	if testing.Short() {
		t.Skip("-short")
	}
	fmt.Printf("\n=== 六、内存峰值 (一条路一个子进程各报 VmHWM) ===\n")
	for _, mode := range []string{"none-hold", "std-hold", "re2-hold", "none-scan", "std-scan", "re2-scan"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestStdlibCompare_PeakChild", "-test.v")
		cmd.Env = append(os.Environ(), stdcmpPeakChildEnv+"="+mode)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("子进程 %s: %v\n%s", mode, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "PEAK ") {
				fmt.Println(line)
			}
		}
	}
}
