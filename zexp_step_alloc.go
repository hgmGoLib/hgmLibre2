// zexp_step_alloc.go — 【实验, 未定案】"缓冲由 C 分配 / 同一次调用内缓存 / 返回前释放" 方案的实现,
// 以及两个对照实现。三者的匹配循环完全相同 (同一段 C 代码或其孪生), 唯一差别是【批缓冲从哪来】:
//
//	A 现行  StepAllStringSubmatchIndex     调用方持有 *MatchStep_t (Go 内存, 跨调用复用)
//	B 提案  stepAllCAlloc                  C 侧首次命中时 malloc, 本次调用内缓存, 返回前 free
//	D 对照  stepAllGoLocal                 每次调用 make 一块 Go 缓冲 (无工作区, 最朴素)
//	E 对照  stepAllPool                    库内 sync.Pool 持有 Go 缓冲 (API 与 B 一样干净)
//
// B/D/E 的对外形态都是 re.XXX(s, n, batchFn) —— 没有工作区参数。测这四个是为了回答:
// 干掉调用方工作区这件事, 最便宜的做法到底是哪个。结论写在 zexp_step_alloc_bench_test.go 顶部。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"runtime"
	"sync"
	"unsafe"
)

// ── B: C 侧持有缓冲 ────────────────────────────────────────────────────────────────
// flat 指向 C malloc 的内存: Go 的 GC 不管它 (无指针, 不移动), 语言层面完全合法。
// 契约与 A 相同 —— flat 只在本次回调内有效, 要留存自己 copy。差别在于这块内存在本次调用
// 返回前就被 free 掉, 而不是挂在调用方的工作区上等下次。
func (re *Regexp) stepAllCAlloc(s string, n int, nmatch int, batchFn func(flat []int32) bool) {
	if len(s) > maxCInt {
		return
	}
	if n < 0 {
		n = len(s) + 1
	}
	if n == 0 {
		return
	}
	per := 2 * nmatch
	capM := stepBatchFirst
	tp := strBytePtr(s)
	pos := C.int(0)
	prevEnd := C.int(-1)
	left := n
	var cbuf *C.int // nil ⇒ 让 C 侧在首次命中时才 malloc
	for {
		r := C.cre2_match_all_step_alloc(re.h, tp, C.int(len(s)), C.int(nmatch), C.int(left),
			pos, prevEnd, cbuf, C.int(capM))
		runtime.KeepAlive(s)
		runtime.KeepAlive(re)
		cbuf = r.buf
		if r.rc <= 0 {
			break
		}
		cnt := int(r.nmatches)
		if cnt > 0 {
			flat := unsafe.Slice((*int32)(unsafe.Pointer(cbuf)), cnt*per)
			if !batchFn(flat) {
				break
			}
			left -= cnt
		}
		if r.done != 0 || left <= 0 {
			break
		}
		pos, prevEnd = r.pos, r.prevEnd
		if cnt == capM && capM < stepBatchMatches {
			C.cre2_step_buf_free(cbuf) // 换大批: 旧的先还
			cbuf = nil
			capM = stepBatchMatches
		}
	}
	if cbuf != nil {
		C.cre2_step_buf_free(cbuf) // 🔴 这一下是 B 相对 A 多出来的一次 cgo 过境
	}
}

func (re *Regexp) StepAllStringSubmatchIndexCAlloc(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAllCAlloc(s, n, re.numSubexp+1, batchFn)
}

func (re *Regexp) StepAllStringIndexCAlloc(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAllCAlloc(s, n, 1, batchFn)
}

// ── D: 每次调用现 make 一块 Go 缓冲 (没有工作区的最朴素写法) ─────────────────────────
// 与 B 的关键区别: Go 侧拿不到"到底有没有命中"的先验, 缓冲必须在第一次 step 之前就备好,
// 所以 miss 路径也要付这一笔。B 的惰性 malloc 正是冲着这一点去的。
func (re *Regexp) stepAllGoLocal(s string, n int, nmatch int, batchFn func(flat []int32) bool) {
	if len(s) > maxCInt {
		return
	}
	if n < 0 {
		n = len(s) + 1
	}
	if n == 0 {
		return
	}
	per := 2 * nmatch
	capM := stepBatchFirst
	buf := make([]int32, capM*per)
	tp := strBytePtr(s)
	pos := C.int(0)
	prevEnd := C.int(-1)
	left := n
	for {
		r := C.cre2_match_all_step(re.h, tp, C.int(len(s)), C.int(nmatch), C.int(left),
			pos, prevEnd, (*C.int)(unsafe.Pointer(&buf[0])), C.int(capM))
		runtime.KeepAlive(s)
		runtime.KeepAlive(re)
		if r.rc <= 0 {
			return
		}
		cnt := int(r.nmatches)
		if cnt > 0 {
			if !batchFn(buf[:cnt*per]) {
				return
			}
			left -= cnt
		}
		if r.done != 0 || left <= 0 {
			return
		}
		pos, prevEnd = r.pos, r.prevEnd
		if cnt == capM && capM < stepBatchMatches {
			buf = make([]int32, stepBatchMatches*per)
			capM = stepBatchMatches
		}
	}
}

func (re *Regexp) StepAllStringSubmatchIndexGoLocal(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAllGoLocal(s, n, re.numSubexp+1, batchFn)
}

// ── E: 工作区藏进库内的 sync.Pool (对外 API 与 B 一样干净, 但没有 cgo 过境也没有 malloc) ──
var stepPool = sync.Pool{New: func() any { return new(MatchStep_t) }}

func (re *Regexp) stepAllPool(s string, n int, nmatch int, batchFn func(flat []int32) bool) {
	st := stepPool.Get().(*MatchStep_t)
	re.stepAll(st, s, n, nmatch, batchFn)
	stepPool.Put(st)
}

func (re *Regexp) StepAllStringSubmatchIndexPool(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAllPool(s, n, re.numSubexp+1, batchFn)
}

func (re *Regexp) StepAllStringIndexPool(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAllPool(s, n, 1, batchFn)
}

// ── 给基准用的 C 侧零件 (cgo 不能出现在 _test.go 里, 只好放这) ─────────────────────────
func xCgoNop()                              { C.cre2_nop() }
func xCMallocFreeRoundtrip(n, nbytes int)    { C.cre2_malloc_free_roundtrip(C.int(n), C.int(nbytes)) }
