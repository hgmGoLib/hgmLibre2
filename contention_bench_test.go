package hgmLibre2

// contention_bench_test.go — 量「一个 *Regexp 被多个 goroutine 共用」要付多少钱。
//
// 【病灶】RE2 每次 DFA 搜索都要在这个 Regexp 的 DFA 上拿一次【读锁】(DFA::cache_mutex_,
// util/mutex.h 在 Linux 上就是 pthread_rwlock)。读锁之间不互斥, 但 pthread_rwlock 的
// 读者计数是【一个共享 cache line 上的原子加减】—— 线程一多, 这条 cache line 就在核之间
// 来回弹, 于是"并发读"变成了串行。上游 issue #569 报的就是这件事 (C++ 侧 8 线程共享一个
// RE2 比每线程各建一个慢 5.6 倍)。
//
// 对本库更要命: Go 里 `var re = MustCompile(...)` 建一个全局的、所有 goroutine 共用,
// 是【最自然也最常见】的写法, 而 stdlib regexp 走的是 per-goroutine machine 池, 完全没有
// 这条共享路径。所以同一份代码从 stdlib 换到本库, 单线程更快, 高并发短输入反而更慢。
//
// 【怎么读这组数】(20 核 Ryzen 5900X, 16 goroutine, 14 字节输入, ns/op 越小越好)
//     shared_16_short   42~77     ← 共用一个 *Regexp: 不但没扩展, 还比单线程更慢
//     sep_16_short      9.5~13    ← 每 goroutine 一个 *Regexp: 正常扩展
//     std_16_short      4.3~4.5   ← stdlib
//   把读锁编掉 (-DRE2_NO_THREADS, 仅测量用, 不可上生产) 再测 shared_16_short = 8.0~8.5,
//   与 sep 持平 ⇒ 【差距 100% 来自那把读锁】, 不是 cgo 开销也不是别的。
//   4KB 输入下扫描本身变长, 摊薄后仍有 1.6 倍 (62~69 vs 38~42)。
//
// 【调用者该怎么做: 什么都不用做】照常共用一个包级 *Regexp。绝对差值才 33ns/op, 而"每个
// worker 各建一个"要多付一次编译, 而且每份都有自己的一整套 DFA 状态缓存 —— 内存峰值和
// max_mem 预算都乘以 worker 数, 还丢掉了跨 goroutine 复用已缓存 DFA 状态的好处。除非 profile
// 真指到这把锁, 否则不划算。这个 benchmark 是给【库自己】做根治改造 (分片读者计数 / epoch)
// 时量收益用的, 不是给调用者当优化指南的。

import (
	"regexp"
	"strings"
	"sync"
	"testing"
)

type matcher interface{ MatchString(string) bool }

var contentionLines = map[string]string{
	"short14": "example string",
	"long4k":  strings.Repeat("the quick brown fox jumps over the lazy dog. ", 91),
}

// 故意不命中: 让每次调用都真的把输入扫完, 而不是提前在第一个位置返回。
const contentionPat = `i won'?t match`

func contentionRun(b *testing.B, nw int, line string, mk func() matcher) {
	res := make([]matcher, nw)
	for i := range res {
		res[i] = mk()
	}
	n := b.N / nw
	var wg sync.WaitGroup
	b.ResetTimer()
	for w := 0; w < nw; w++ {
		wg.Add(1)
		go func(re matcher) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				re.MatchString(line)
			}
		}(res[w])
	}
	wg.Wait()
}

// shared: 所有 goroutine 共用一个 *Regexp (生产里最常见的写法)。
func contentionShared(b *testing.B, nw int, key string) {
	re := MustCompile(contentionPat)
	contentionRun(b, nw, contentionLines[key], func() matcher { return re })
}

// sep: 每个 goroutine 各一个 *Regexp (没有共享的 DFA, 就没有那把读锁的争用)。
func contentionSeparate(b *testing.B, nw int, key string) {
	contentionRun(b, nw, contentionLines[key], func() matcher { return MustCompile(contentionPat) })
}

// std: stdlib 的同一条 pattern 共用一个 *regexp.Regexp, 作参照系。
func contentionStd(b *testing.B, nw int, key string) {
	re := regexp.MustCompile(contentionPat)
	contentionRun(b, nw, contentionLines[key], func() matcher { return re })
}

func BenchmarkContentionShared1Short(b *testing.B)    { contentionShared(b, 1, "short14") }
func BenchmarkContentionShared4Short(b *testing.B)    { contentionShared(b, 4, "short14") }
func BenchmarkContentionShared16Short(b *testing.B)   { contentionShared(b, 16, "short14") }
func BenchmarkContentionSeparate16Short(b *testing.B) { contentionSeparate(b, 16, "short14") }
func BenchmarkContentionStd16Short(b *testing.B)      { contentionStd(b, 16, "short14") }
func BenchmarkContentionShared1Long(b *testing.B)     { contentionShared(b, 1, "long4k") }
func BenchmarkContentionShared16Long(b *testing.B)    { contentionShared(b, 16, "long4k") }
func BenchmarkContentionSeparate16Long(b *testing.B)  { contentionSeparate(b, 16, "long4k") }
func BenchmarkContentionStd16Long(b *testing.B)       { contentionStd(b, 16, "long4k") }
