// zexp_step_alloc.go — 【已定案 · 留作证据】"批缓冲从哪来" 这道题的两个落选方案的实现。
// 三条路的匹配循环完全相同 (同一段 C 代码或其孪生), 唯一差别是【批缓冲从哪来】:
//
//	主线  StepAllStringSubmatchIndex     库内 sync.Pool 持有 Go 缓冲 (match_step.go · 原实验里的变体 E)
//	B 落选 stepAllCAlloc                  C 侧首次命中时 malloc, 本次调用内缓存, 返回前 free
//	D 落选 stepAllGoLocal                 每次调用现 make 一块 Go 缓冲 (最朴素的写法)
//
// 三者对外形态一样 (re.XXX(s, n, batchFn)), 所以能同题对拍。定案理由与四方数字见
// zexp_step_alloc_bench_test.go 顶部 —— 一句话: B 只比主线快 5%, 却要动 C 代码、每条提前返回
// 路径都得记得 free, 而且契约从"读到旧数据"降级成 use-after-free; D 是四个里最差的那个。
// (原来还有个变体 A "调用方持有 *MatchStep_t 工作区" —— 它连同 MatchStep_t 一起已被主线取代,
//  理由见 match_step.go 头注"批缓冲从哪来"一节。)
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// ── B: C 侧持有缓冲 ────────────────────────────────────────────────────────────────
// flat 指向 C malloc 的内存: Go 的 GC 不管它 (无指针, 不移动), 语言层面完全合法。
// 契约与主线相同 —— flat 只在本次回调内有效, 要留存自己 copy。差别在于这块内存在本次调用
// 返回前就被 free 掉, 而不是还回池子等下次。
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
	capM := stepBufInts / per
	if capM < 1 {
		capM = 1
	}
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
	}
	if cbuf != nil {
		C.cre2_step_buf_free(cbuf) // 🔴 这一下是 B 相对主线多出来的一次 cgo 过境
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
	capM := stepBufInts / per
	if capM < 1 {
		capM = 1
	}
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
	}
}

func (re *Regexp) StepAllStringSubmatchIndexGoLocal(s string, n int, batchFn func(flat []int32) bool) {
	re.stepAllGoLocal(s, n, re.numSubexp+1, batchFn)
}

// ── 给基准用的 C 侧零件 (cgo 不能出现在 _test.go 里, 只好放这) ─────────────────────────
func xCgoNop()                              { C.cre2_nop() }
func xCMallocFreeRoundtrip(n, nbytes int)    { C.cre2_malloc_free_roundtrip(C.int(n), C.int(nbytes)) }
