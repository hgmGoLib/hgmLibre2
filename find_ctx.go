// find_ctx.go — FindStringIndex 的【复用 scratch】变体: 把每次调用都要新分配的 cgo 回填缓冲
// 与返回切片挂到一个 ctx 上, 单线程顺序反复调稳态零分配 (需调用方持有 ctx 配合)。
//
// 动机: (*Regexp).FindStringIndex 每次都 make 一个 []C.int 回填缓冲 + (命中时)一个 []int 返回切片;
// 在「大正文逐段反复跑同一批正则」的热路径上 (如逐 message/block 扫 MCP 注入), 这两笔分配按匹配
// 次数线性放大, 是分配次数大头. 本变体把缓冲挪进 ctx, 同一 ctx 反复调只在首次分配.
//
// 与 FindStringIndex 语义一致: 只取 group0 ([start,end))、leftmost、非锚定。返回的切片指向 ctx
// 内部, 仅在【下次用同一 ctx 调用前】有效; 需留存请自行 copy. ctx 非线程安全, 并发各持一个。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import "runtime"

// FindStringIndex_ctx_t 持有 FindStringIndex 复用所需的全部 scratch:
// cbuf 是喂给 cgo 回填 group0 [start,end) 的 C.int 缓冲 (len 2); ret 是返回给调用方的 [start,end]。
// 零值即可用 (首次调用惰性分配 cbuf); 也可用 NewFindStringIndex_ctx 预分配。
type FindStringIndex_ctx_t struct {
	cbuf []C.int
	ret  [2]int
}

// NewFindStringIndex_ctx 预分配好 scratch, 返回一个可复用的 ctx。
func NewFindStringIndex_ctx() *FindStringIndex_ctx_t {
	return &FindStringIndex_ctx_t{cbuf: make([]C.int, 2)}
}

// FindStringIndex 同 (*Regexp).FindStringIndex, 但复用 ctx 的 scratch 缓冲, 单线程顺序反复调
// 稳态零分配. 返回最左匹配的 [start,end) (切自 ctx.ret · 仅在下次用本 ctx 调用前有效), 无匹配返回 nil。
func (ctx *FindStringIndex_ctx_t) FindStringIndex(re *Regexp, s string) []int {
	if len(s) > maxCInt { // 超 C.int 的输入直接当无匹配 (同 findFrom 守卫)
		return nil
	}
	if cap(ctx.cbuf) < 2 {
		ctx.cbuf = make([]C.int, 2)
	}
	cbuf := ctx.cbuf[:2]
	tp := strBytePtr(s)
	// nmatch=1: 只回填 group0; cre2_match_at 内部 vector<StringPiece>(1), 不取子组 (比 findFrom 更省)。
	ok := C.cre2_match_at(re.h, tp, C.int(len(s)), 0, &cbuf[0], 1) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if !ok {
		return nil
	}
	ctx.ret[0] = int(cbuf[0])
	ctx.ret[1] = int(cbuf[1])
	return ctx.ret[:]
}
