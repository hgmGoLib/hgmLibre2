// zexp_step_alloc_bench_test.go — "批缓冲改由 C 分配 / 本次调用内缓存 / 返回前 free" 这个方案的定价。
//
// ══ 结论 (5900X · 2026-08-26 · 别再改回去) ═══════════════════════════════════════════
// 提案的三条前提【全部成立, 实测过】:
//   ① "Go 切片的底是 C 内存没问题" —— 成立。那块内存不在 Go 堆里, GC 既不扫也不移动它,
//      里面又没有 Go 指针。TestStepCAlloc_SurvivesGC: 每批回调里砸 256KB Go 垃圾 + 两次
//      runtime.GC(), 3000 个 int 一个字节没变。
//   ② "C 的 malloc/free 是个高效的 sync.Pool" —— 成立, 而且比 Go 的分配器还快:
//      BenchmarkX_cMallocFree  192B/1032B = 4.8ns 一个来回 (glibc tcache, 每线程一份, 无锁)
//                              3072B      = 15.0ns  (超过 tcache_max=1032, 落到 bin)
//      BenchmarkX_goMake       192B = 30.5ns · 3072B = 235ns (还要付 GC)
//      并发 20 线程下 B 与 A 完全打平 (BenchmarkX_stepVariantsParallel 3145 vs 3150ns),
//      arena 争用这个担心不成立。
//   ③ "惰性到第一次命中才分配, miss 路径一分钱不花" —— 成立, C 侧确实做得到 (Go 侧做不到,
//      因为 Go 在第一次 step 之前拿不到"有没有命中"的先验, 见下面变体 D)。
//
// 但【方案整体仍然打不过现行的 A】, 因为它要替换掉的东西本身是零成本 —— 零是打不过的。
// 而且 B 那笔额外开销的大头不是 malloc, 是【free 必须单独过一次 cgo 桥】:
//   BenchmarkX_cgoNop = 20.1ns/次 ≫ malloc+free 的 4.8ns。
// free 躲不掉这次过境: 最后一批的数据要先交给 batchFn 用完, Go 才能让 C 放手。
//
// 四方对拍 (per=6 子组版 · 单次调用):
//                     hits=0     hits=1    hits=5    hits=50   hits=500
//   A 调用方工作区     117.4ns   217.4ns   762.0ns   6874ns    68666ns   0 B/op
//   B C 侧 malloc      113.7ns   246.5ns   795.3ns   7002ns    68025ns   0 B/op
//   D 每次 make        167.9ns   262.1ns   798.7ns   7092ns    72039ns   192~3264 B/op
//   E 库内 sync.Pool   128.6ns   230.8ns   773.8ns   6874ns    67651ns   0 B/op
// ⟹ B 在 miss 路径与 A 打平 (惰性生效), 每有一次命中就付约 +26ns (20ns 过境 + 5ns malloc)。
//
// 规则表形状 (16~20 条 re 扫一份正文, 只有 p 条命中 —— 调用方产品的真实形状):
//   p=0/16   A 1581ns/0B · B 1662ns/0B · E 1767ns/0B · D 2258ns/3072B/16 笔
//   p=4/20   A 3399ns/0B · B 3585ns/0B · E 3668ns/0B · D 4174ns/3840B/20 笔
// ⟹ 名次在所有命中率上都一样: A < B < E < D。
//
// ══ 定案 (第一版: 留 A + 按情况用 E) ══════════════════════════════════════════════
// · 调用方本来就有个能挂工作区的对象 ⇒ 用 A。零开销。
// · 没有这种对象 ⇒ 用 E (库内 sync.Pool): 9.5ns/次 · 零分配 · 不用动一行 C · 契约不变。
//   B 比 E 再快约 5%, 但要付: 动 C 代码 · 每条提前返回路径都得记得 free (batchFn panic 就漏) ·
//   而且契约从"读到旧数据"降级成"use-after-free"—— 同样是 []int32, 类型上分辨不出来。
//   这 5% 换不来这三样。
// · 每次调用现 make 一块 (D) 是四个里最差的一个, 该消灭。
//
// ══ 复审 (同日 · 现行) ════════════════════════════════════════════════════════════
// A 被整个下线, E 升为主线 —— MatchStep_t 与工作区参数一并删除。理由是上面那份表【单位不对】:
// A 领先 E 的那 11.6ns/次, 是在"正文只有几十到几百字节"的基准里量出来的; 生产里一份 body 是
// 240KB 起步, 光 RE2 扫一遍就是几十微秒, 一次 Get/Put 占 万分之一都不到 —— 这个领先在真实
// 尺度上【量不出来】。而 A 换来的代价是实打实的:
//   · 调用方得自己找地方安置工作区, 没地方的就写成 var st (=变体 D, 四个里最差的那个) ——
//     第一版落地当天 12 个调用点里就有 6 个这么写, 说明这不是"用错了", 是这个 API 的默认答案;
//   · batchFn 里再起一条 step 扫描会与外层共用同一块 st, 就地互相覆写 (静默错结果), 而池化之后
//     各借各的, 这条坑直接消失;
//   · 每个持有工作区的壳都要多一个字段 + 一段"跟着谁借还"的注释。
// 一句话: 那 11.6ns 只在基准里存在, 而那三样代价在每个调用点上都存在。
//
// 🔴 顺带记下 D 有多毒 (第一版落地当天量出来的真问题): 函数内 var st 每次调用白付 192B + 一笔
//    分配【命不命中都付】, miss 路径上比 A 慢 43%。调用方产品 8.2MB 档 FindAll→Step 换完之后
//    Go 分配 920.4M → 922.7M(字节反而涨), 就是这个。判据见 match_step_test.go 的
//    TestStepAllString_MissZeroAlloc。
package hgmLibre2

import (
	"runtime"
	"strconv"
	"testing"
)

// ── 先给零件定价 ────────────────────────────────────────────────────────────────────
// 一次空 cgo 过境要多少 —— B 方案相对 A 铁定多出来的那一次 cre2_step_buf_free 就是这个价。
func BenchmarkX_cgoNop(b *testing.B) {
	for i := 0; i < b.N; i++ {
		xCgoNop()
	}
}

// glibc malloc/free 一个来回 (在 C 内循环, 不含 cgo 过境)。
// 192B = 首批 (8 处 × per6 × 4B), 3072B = 大批 (128 处 × per6 × 4B)。
// tcache 默认只覆盖 ≤1032B, 所以这两档预计不是一个价 —— 这正是"C 的 malloc 是高效 sync.Pool"
// 这个前提要检验的地方。
func BenchmarkX_cMallocFree(b *testing.B) {
	for _, nb := range []int{192, 1032, 3072} {
		b.Run(strconv.Itoa(nb)+"B", func(b *testing.B) {
			const inner = 1000
			for i := 0; i < b.N; i++ {
				xCMallocFreeRoundtrip(inner, nb)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*inner), "ns/malloc+free")
		})
	}
}

// Go 侧同尺寸 make (mcache, 无锁, 每 P 一份) —— 对照 glibc。
func BenchmarkX_goMake(b *testing.B) {
	for _, n := range []int{48, 768} { // int32 个数: 48*4=192B, 768*4=3072B
		b.Run(strconv.Itoa(n*4)+"B", func(b *testing.B) {
			b.ReportAllocs()
			var sink []int32
			for i := 0; i < b.N; i++ {
				sink = make([]int32, n)
			}
			runtime.KeepAlive(sink)
		})
	}
}

// sync.Pool 一个 Get/Put 来回。
func BenchmarkX_syncPoolRoundtrip(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p := stepBufPool.Get().(*[]int32)
		stepBufPool.Put(p)
	}
}

// ── 整条路径的四方对拍 ──────────────────────────────────────────────────────────────
// hits=0 是 miss 路径 (扫描型负载里占绝大多数的那一类调用)。
func BenchmarkX_stepVariants(b *testing.B) {
	missBody := stepBenchNHits(500) // 同样长的正文, 换一条扫不到的 re
	missRe := MustCompile(`(zzz)=(qqq)`)
	sink := 0
	eat := func(f []int32) bool {
		for k := 0; k+6 <= len(f); k += 6 {
			sink += int(f[k]) + int(f[k+1])
		}
		return true
	}

	run := func(name string, re *Regexp, body string) {
		b.Run(name+"/B_cMalloc", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				re.StepAllStringSubmatchIndexCAlloc(body, -1, eat)
			}
		})
		b.Run(name+"/D_goMakeEach", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				re.StepAllStringSubmatchIndexGoLocal(body, -1, eat)
			}
		})
		b.Run(name+"/main_pool", func(b *testing.B) {
			b.ReportAllocs()
			re.StepAllStringSubmatchIndex(body, -1, eat)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				re.StepAllStringSubmatchIndex(body, -1, eat)
			}
		})
	}

	run("hits=0", missRe, missBody)
	for _, hits := range []int{1, 5, 50, 500, 20000} {
		run("hits="+strconv.Itoa(hits), benchRe, stepBenchNHits(hits))
	}
	_ = sink
}

// 并发下的四方对拍: sync.Pool 是每 P 一份, C malloc 在多线程下要走 arena。
func BenchmarkX_stepVariantsParallel(b *testing.B) {
	body := stepBenchNHits(50)
	eat := func(f []int32) bool { return true }
	b.Run("B_cMalloc", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				benchRe.StepAllStringSubmatchIndexCAlloc(body, -1, eat)
			}
		})
	})
	b.Run("main_pool", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				benchRe.StepAllStringSubmatchIndex(body, -1, eat)
			}
		})
	})
}

// ── 决定性一测: 调用方产品的真实形状 —— 一张规则表 (N 条 re) 扫一份正文, 绝大多数条目 miss ────
// 命中率 p 是这里唯一的自变量: B 的账是 0×(1-p) + 约26ns×p, E 的账是恒定约 10ns,
// D 的账是恒定约 50ns + 192B/次。交叉点在哪, 由这条基准直接给出。
func BenchmarkX_ruleTable(b *testing.B) {
	body := "user report: contact alice at 555-0100, id=A17 token abc " +
		"and some more prose that does not match most of the table at all. " +
		"see https://example.com/x for details. 2026-08-26 10:11:12"
	// 表里只有前 hitN 条能命中, 其余全 miss。
	hitPats := []string{`(\w+)=(\w+)`, `(\d{3})-(\d{4})`, `(https?)://(\S+)`, `(\d{4})-(\d{2})`}
	missPats := []string{
		`(zzq)=(qqz)`, `(BEGIN) (RSA)`, `(AKIA)([0-9A-Z]{16})`, `(ghp)_([0-9a-zA-Z]{36})`,
		`(xox[baprs])-(\d+)`, `(sk)-([0-9a-zA-Z]{48})`, `(eyJ)([0-9a-zA-Z_-]{20,})`, `(-----)(PRIVATE)`,
		`(mongodb)\+(srv)`, `(postgres)://(\S+)`, `(AIza)([0-9A-Za-z_-]{35})`, `(ya29)\.([0-9A-Za-z_-]+)`,
		`(SG)\.([0-9A-Za-z_-]{22})`, `(rk_live)_([0-9a-zA-Z]{24})`, `(npm_)([0-9A-Za-z]{36})`,
		`(glpat)-([0-9A-Za-z_-]{20})`,
	}
	for _, hitN := range []int{0, 1, 2, 4} {
		var tbl []*Regexp
		for i := 0; i < hitN; i++ {
			tbl = append(tbl, MustCompile(hitPats[i]))
		}
		for _, p := range missPats {
			tbl = append(tbl, MustCompile(p))
		}
		name := "p=" + strconv.Itoa(hitN) + "/" + strconv.Itoa(len(tbl))
		eat := func(f []int32) bool { return true }
		b.Run(name+"/B_cMalloc", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, re := range tbl {
					re.StepAllStringSubmatchIndexCAlloc(body, -1, eat)
				}
			}
		})
		b.Run(name+"/D_goMakeEach", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, re := range tbl {
					re.StepAllStringSubmatchIndexGoLocal(body, -1, eat)
				}
			}
		})
		b.Run(name+"/main_pool", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, re := range tbl {
					re.StepAllStringSubmatchIndex(body, -1, eat)
				}
			}
		})
	}
}
