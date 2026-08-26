// match_step_bench_test.go — step 形态 vs 老路的同题对比。
//
// 口径: 同一条 re · 同一份正文 · 同一批命中, 只换接口形状。
//   old_FindAllSubmatchIndex  = ① C 侧 vector 累积 + malloc + 拷贝 · ② Go 侧 flat · ③ [][]int 外壳
//   old_AppendAllStringIndex  = ① 照旧 (只是 nmatch=1 表更小), ②③ 已消
//   new_StepSubmatchIndex     = 三层全消, 只有一块固定批缓冲
// 🔴 老路刻意保持原实现不动 (没有让它绕道 step), 否则基准会被人为量歪。
package hgmLibre2

import (
	"strconv"
	"strings"
	"testing"
)

// stepBenchBody 造一份"多命中"的正文: hitEvery 字节一处命中。
func stepBenchBody(sizeKB, hitEvery int) string {
	var b strings.Builder
	b.Grow(sizeKB * 1024)
	filler := strings.Repeat("x", hitEvery-len("key=val "))
	for b.Len() < sizeKB*1024 {
		b.WriteString("key=val ")
		b.WriteString(filler)
	}
	return b.String()
}

var benchRe = MustCompile(`(\w+)=(\w+)`) // numSubexp=2 ⇒ per=6

func BenchmarkMatchAll_old_FindAllStringSubmatchIndex(b *testing.B) {
	for _, sz := range []int{64, 1024} {
		body := stepBenchBody(sz, 64)
		b.Run(stepSizeName(sz), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			sink := 0
			for i := 0; i < b.N; i++ {
				locs := benchRe.FindAllStringSubmatchIndex(body, -1)
				for _, l := range locs {
					sink += l[2] + l[3]
				}
			}
			_ = sink
		})
	}
}

func BenchmarkMatchAll_new_StepAllStringSubmatchIndex(b *testing.B) {
	for _, sz := range []int{64, 1024} {
		body := stepBenchBody(sz, 64)
		b.Run(stepSizeName(sz), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			st := &MatchStep_t{}
			sink := 0
			benchRe.StepAllStringSubmatchIndex(st, body, -1, func(flat []int32) bool { return true }) // 预热缓冲
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchRe.StepAllStringSubmatchIndex(st, body, -1, func(flat []int32) bool {
					for k := 0; k+6 <= len(flat); k += 6 {
						sink += int(flat[k+2]) + int(flat[k+3])
					}
					return true
				})
			}
			_ = sink
		})
	}
}

func BenchmarkMatchAll_old_AppendAllStringIndexFlat(b *testing.B) {
	for _, sz := range []int{64, 1024} {
		body := stepBenchBody(sz, 64)
		b.Run(stepSizeName(sz), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			var buf []int
			sink := 0
			for i := 0; i < b.N; i++ {
				buf = benchRe.AppendAllStringIndexFlat(buf[:0], body, -1)
				for k := 0; k+2 <= len(buf); k += 2 {
					sink += buf[k] + buf[k+1]
				}
			}
			_ = sink
		})
	}
}

func BenchmarkMatchAll_new_StepAllStringIndex(b *testing.B) {
	for _, sz := range []int{64, 1024} {
		body := stepBenchBody(sz, 64)
		b.Run(stepSizeName(sz), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			st := &MatchStep_t{}
			sink := 0
			benchRe.StepAllStringIndex(st, body, -1, func(flat []int32) bool { return true })
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchRe.StepAllStringIndex(st, body, -1, func(flat []int32) bool {
					for k := 0; k+2 <= len(flat); k += 2 {
						sink += int(flat[k]) + int(flat[k+1])
					}
					return true
				})
			}
			_ = sink
		})
	}
}

// miss 路径 (绝大多数调用): 新路应当与老路一样是 1 次过桥, 且新路稳态零分配。
func BenchmarkMatchAll_miss_old(b *testing.B) {
	body := stepBenchBody(64, 64)
	re := MustCompile(`zzzz(qqq)=(www)`)
	b.ReportAllocs()
	b.ResetTimer() // 🔴 语料构造(64KB Builder)必须排除在统计外, 否则 b.N 小的时候它就是全部的数
	for i := 0; i < b.N; i++ {
		_ = re.FindAllStringSubmatchIndex(body, -1)
	}
}

func BenchmarkMatchAll_miss_new(b *testing.B) {
	body := stepBenchBody(64, 64)
	re := MustCompile(`zzzz(qqq)=(www)`)
	st := &MatchStep_t{}
	re.StepAllStringSubmatchIndex(st, body, -1, func(flat []int32) bool { return true }) // 预热缓冲
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		re.StepAllStringSubmatchIndex(st, body, -1, func(flat []int32) bool { return true })
	}
}

func stepSizeName(kb int) string {
	if kb >= 1024 {
		return "1MB"
	}
	return "64KB"
}

// ── 决策用: FindAllStringSubmatchIndex 改走 step 内核到底是不是净赚? ────────────────
// 老路一次调用的账: C 侧 vector 增长阶梯 + malloc(精确) + C→Go 拷贝 + Go make(精确) + [][]int 外壳。
// step 物化版的账: C 侧全无 + Go 侧 (首批精确, 超一批之后走 append 阶梯) + [][]int 外壳 + 一块批缓冲。
// 命中数 ≤ 一批时 step 版是【一次精确分配 + 零 C 分配】, 应当净赚; 远超一批时它要付 append 阶梯,
// 而老路那边付的是 C 侧阶梯 —— 谁赢要量, 不能猜。

func benchFindAllSubStep(re *Regexp, s string, n int) [][]int {
	per := 2 * (re.NumSubexp() + 1)
	var st MatchStep_t
	var flat []int
	re.StepAllStringSubmatchIndex(&st, s, n, func(b []int32) bool {
		if flat == nil {
			flat = make([]int, 0, len(b)) // 首批: 多数调用一批装完 ⇒ 一次精确分配
		}
		for _, v := range b {
			flat = append(flat, int(v))
		}
		return true
	})
	if len(flat) == 0 {
		return nil
	}
	res := make([][]int, len(flat)/per)
	for k := range res {
		res[k] = flat[k*per : (k+1)*per : (k+1)*per]
	}
	return res
}

// hitsN 造一份正好 hits 处命中的正文。
func stepBenchNHits(hits int) string {
	var b strings.Builder
	for i := 0; i < hits; i++ {
		b.WriteString("key=val ....... ")
	}
	return b.String()
}

func BenchmarkFindAllSub_matAll_vs_step(b *testing.B) {
	for _, hits := range []int{1, 5, 50, 500, 20000} {
		body := stepBenchNHits(hits)
		b.Run("old/hits="+strconv.Itoa(hits), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = benchRe.FindAllStringSubmatchIndex(body, -1)
			}
		})
		b.Run("step/hits="+strconv.Itoa(hits), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = benchFindAllSubStep(benchRe, body, -1)
			}
		})
	}
}

// ── 决策用②: 调用方【必须物化】但底是【跨调用复用】的场景, step 打不打得过 Append? ──────
// 这与上面那个 FindAllSub 的决策不同: 那里的目的地是每次新开的 []int (append 阶梯每次都付),
// 这里 dst 传 buf[:0] —— 阶梯只在第一趟付一次, 稳态两边都是零 Go 分配。
// 于是差别只剩 C 侧:
//   Append: std::vector 逐处 push_back(倍增) → malloc(精确) → C→Go 一次全量拷贝
//   step  : C 直接写进固定批缓冲 → Go 侧一批一批抄进 dst (多一次小拷贝, 但没有 vector/malloc)
// asc 里 cred_credential.go 那两处(倒序遍历 flat / 与 fillerLocs 共用底)正是这个形状,
// 它们能不能换, 由这条基准说了算。
//
// 实测结论 (5900X · 2026-08-26 · 两边稳态都是 0 B/op 0 allocs/op):
//   hits=5      append  704ns  ·  step  668ns
//   hits=50     append 5681ns  ·  step 5515ns
//   hits=500    append 54.0µs  ·  step 53.6µs
//   hits=20000  append 2.136ms ·  step 2.118ms
// ⟹ 打平到略优。而这条基准【看不见】的那一头是 step 真正赚的地方: C 侧那份 std::vector 累积表
// 与随后的 malloc(峰值是整张命中表的两份, 纯 RSS)在 step 这边根本不存在。
// ⟹ 结论: 连"调用方必须物化"的复用底场景, Append 形态也没有存在理由 —— 它可以全量退役。

func stepMaterializeReuse(re *Regexp, st *MatchStep_t, dst []int, s string) []int {
	re.StepAllStringIndex(st, s, -1, func(flat []int32) bool {
		for _, v := range flat {
			dst = append(dst, int(v))
		}
		return true
	})
	return dst
}

func BenchmarkReuseBuf_append_vs_step(b *testing.B) {
	for _, hits := range []int{5, 50, 500, 20000} {
		body := stepBenchNHits(hits)
		b.Run("append/hits="+strconv.Itoa(hits), func(b *testing.B) {
			b.ReportAllocs()
			var buf []int
			sink := 0
			buf = benchRe.AppendAllStringIndexFlat(buf[:0], body, -1) // 预热底
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf = benchRe.AppendAllStringIndexFlat(buf[:0], body, -1)
				sink += len(buf)
			}
			_ = sink
		})
		b.Run("step/hits="+strconv.Itoa(hits), func(b *testing.B) {
			b.ReportAllocs()
			var st MatchStep_t
			var buf []int
			sink := 0
			buf = stepMaterializeReuse(benchRe, &st, buf[:0], body) // 预热两块底
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf = stepMaterializeReuse(benchRe, &st, buf[:0], body)
				sink += len(buf)
			}
			_ = sink
		})
	}
}
