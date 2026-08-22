// find_replace_within_append.go — FindReplaceWithin 的【追加进调用方缓冲】变体。
//
// 动机 (2026-08-22 · 200MB 语料 memprofilerate=1 实测): asc 的 healInjectionSeparators 走
// FindReplaceWithin, 而后者在【有改动】那条路上把 C 侧结果整份 C.GoStringN 拷成一个新的 Go string
// —— 200MB 正文上就是每次调用一份 200MB 的 Go 堆分配, 一次注入检测走两趟 (#5 heal / #7 combo),
// 单这一条在那份 profile 里排第五 (400MB, 全场 7.5%)。而那两份产物都是【当场喂进 Set 扫一遍就丢】,
// 没有一个字节需要留存 ⇒ 典型的"该往调用方自己那块复用底上写"的形状。
//
// 契约与 AppendReplaceAllStringFunc 一条线 (见 replace_func_ctx.go): 返回 (dst, changed),
// changed=false 时 dst 一个字节都没动。changed 的定义同 FindReplaceWithin —— 结果与 src 逐字节
// 不同才叫变了 (C 侧惰性物化: 无匹配 / 命中但删 0 个字符 都报 0, 且 C 侧连缓冲都不开)。
//
// 本文件不带 ctx: 这条路的 scratch 全在 C 侧 (外层循环 + 段内替换都在 C++ 里), Go 侧唯一那笔
// 按正文线性的分配就是结果本身, 而结果由调用方传 dst 进来 ⇒ 没有第二块需要跨调用留着的东西。
package hgmLibre2

/*
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// AppendFindReplaceWithin 把 find.FindReplaceWithin(strip, src, repl) 的结果追加进 dst,
// 返回 (追加后的切片, 结果与 src 相比是否真的变了)。变了的话 dst 末尾多出来的那一段就是
// FindReplaceWithin 会返回的那个串; 没变的话 dst 一个字节都没多 —— 调用方该用原 src。
//
// 语义与 FindReplaceWithin 逐字节一致 (同一个 cre2_find_replace_within 内核, 同一份 changed 判据):
//
//	out, changed := find.AppendFindReplaceWithin(buf[:0], strip, src, repl)
//	// changed ⟺ find.FindReplaceWithin(strip, src, repl) != src
//	// changed ⟹ string(out) == find.FindReplaceWithin(strip, src, repl)
//
// 与 FindReplaceWithin 的唯一差别是结果落在哪: 那边每趟现开一个 Go string, 这边拷进调用方
// 已有的底 (cap 够就零 Go 堆分配)。C 侧那块 malloc 缓冲两边都要付, 拷完立刻 free。
//
// 🔴 返回的是调用方那块底上的视图: 再往同一块底上追加 (或把它切回 [:0]) 之后就失效, 要留存自己物化。
// 🔴 一律用返回值 —— cap 不够时里面换了底, 原来那个 dst 变量就落后了。
func (find *Regexp) AppendFindReplaceWithin(dst []byte, strip *Regexp, src, repl string) ([]byte, bool) {
	if len(src) > maxCInt {
		return dst, false // 超 C.int 输入: 当无改动 (同其它方法对超大输入的保守处理)
	}
	sp := strBytePtr(src)
	rp := strBytePtr(repl)
	res := C.cre2_find_replace_within(find.h, strip.h, sp, C.int(len(src)), rp, C.int(len(repl)))
	runtime.KeepAlive(src)
	runtime.KeepAlive(repl)
	runtime.KeepAlive(find)
	runtime.KeepAlive(strip)
	if res.changed == 0 || res.out == nil {
		return dst, false // 无改动 (含 malloc 失败的 rc=-1 保守退回): dst 不动
	}
	n := int(res.outlen)
	if n > 0 {
		// C 缓冲不归 GC 管, 但 append 只是从它 memcpy 出来 —— 拷完这块就跟 Go 侧再无关系。
		dst = append(dst, unsafe.Slice((*byte)(unsafe.Pointer(res.out)), n)...)
	}
	C.free(unsafe.Pointer(res.out))
	return dst, true
}
