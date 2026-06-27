// regexpset.go — RegexpSet: 多正则「一次扫描·返回哪几条命中」的 litscan 风格 API。
//
// 动机: 调用方常把 N 条正则拼成 (?:re1)|(?:re2)|… 做一道"任一命中"快拒门, 命中后还得再逐条
// 跑一遍才知道是哪条。RE2::Set 把 N 条编进【一个 DFA】, 一遍扫就直接回答"哪几条命中"——
// 取代那道粗门, 且把"是哪条"的信息一并拿到, 命中后不必再逐条全跑 (只需要位置的调用方再对
// 命中条单独取 FindStringIndex)。语义 unanchored/partial (正文任意位置出现即命中)。
//
// ⚠ 只回答"哪几条", 不回答"在哪" (无位置)。需要 fragment/offset 的调用方拿到命中 index 后,
//    对那几条 (通常 0 条) 各跑一次 FindStringIndex 即可。
//
// 生命周期: NewRegexpSet 构建期一次 (编译 DFA), 之后只读、并发安全; Match 传入复用 buf 即零分配。
package hgmLibre2

/*
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
	"strconv"
	"unsafe"
)

// RegexpSet 是多正则集合 (构建期一次编译 · 扫描期只读 · 并发安全)。
type RegexpSet struct {
	h    *C.cre2_set
	size int // Add 成功的 pattern 数 (= Match 输出 index 的上界)
}

// NewRegexpSet 把 patterns 顺序编进一个 RE2::Set。任一条解析失败 / 编译失败 → 返回 error
// (并释放已分配的 native 资源)。Match 输出的 index 即 patterns 的下标。
func NewRegexpSet(patterns []string) (*RegexpSet, error) {
	h := C.cre2_set_new()
	if h == nil {
		return nil, errors.New("re2native: set out of memory")
	}
	s := &RegexpSet{h: h}
	for i, p := range patterns {
		if len(p) > maxCInt {
			C.cre2_set_free(h)
			return nil, errors.New("re2native: set pattern too large (>2GiB)")
		}
		idx := int(C.cre2_set_add(h, strBytePtr(p), C.int(len(p))))
		runtime.KeepAlive(p)
		if idx < 0 {
			C.cre2_set_free(h)
			return nil, errors.New("re2native: set bad pattern at index " + strconv.Itoa(i) + ": " + p)
		}
		s.size++
	}
	if C.cre2_set_compile(h) == 0 {
		C.cre2_set_free(h)
		return nil, errors.New("re2native: set compile failed (out of memory)")
	}
	runtime.SetFinalizer(s, func(x *RegexpSet) { C.cre2_set_free(x.h) })
	return s, nil
}

// Size 返回集合里的 pattern 数。
func (s *RegexpSet) Size() int { return s.size }

// Match 扫 text 一遍, 把命中的 pattern index 写进 buf (传入复用切片避免每次分配) 并返回其前缀切片。
// 返回切片里每个元素是 patterns 的下标 (无序 · 不重复)。无命中返回长度 0 的切片。
//
// buf 用 int32 (= C.int): 直接给 cgo 回填, 避免 Go int(64位) 与 C.int(32位) 尺寸不符的拷贝。
func (s *RegexpSet) Match(text string, buf []int32) []int32 {
	if s.size == 0 || len(text) > maxCInt {
		return buf[:0]
	}
	if cap(buf) < s.size {
		buf = make([]int32, s.size)
	}
	buf = buf[:s.size]
	tp := strBytePtr(text)
	n := int(C.cre2_set_match(s.h, tp, C.int(len(text)),
		(*C.int)(unsafe.Pointer(&buf[0])), C.int(s.size)))
	runtime.KeepAlive(text)
	runtime.KeepAlive(s)
	if n < 0 {
		n = 0
	}
	if n > s.size {
		n = s.size // 防御: C 侧最多写 outcap 个, count 截断到 size
	}
	return buf[:n]
}

// MatchAny 报告 text 是否命中集合里【任一】正则 (一次扫描 · 不取具体 index · 走快路径)。
func (s *RegexpSet) MatchAny(text string, buf []int32) bool {
	return len(s.Match(text, buf)) > 0
}
