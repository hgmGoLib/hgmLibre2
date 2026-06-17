// Package hgmLibre2 — 自带 cgo 的原生 RE2 正则库: 不用 go-re2 / 不用 abseil / 不用 cmake,
// 编译期不下载远程源 (RE2 2023-03-01 源码已 vendored 在本目录, 纯 C++11, zig 可交叉编译).
//
// 相比 go-re2 的 wazero 后端: 原生 cgo 路径不实例化 wazero runtime, 也不做 stdio 句柄探测,
// 因此在无 std 句柄的环境 (如 Windows SCM service) 也能正常用; 同时是单文件静态链接.
//
// API 的方法签名与 stdlib regexp 完全一致 (Compile/MustCompile + Find/Replace 系列),
// 可作为 `type R = hgmLibre2.Regexp` 直接替换 *regexp.Regexp. 语义对齐 stdlib (RE2 leftmost).
package hgmLibre2

/*
#cgo CXXFLAGS: -std=c++11 -O2 -DNDEBUG -I${SRCDIR}/internal_include
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// Regexp 持有一个原生 RE2 句柄. 用 finalizer 释放 (与 go-re2 一致, 不强制 Close).
type Regexp struct {
	h           *C.cre2_re
	expr        string   // 源 pattern, String() 用
	numSubexp   int      // 捕获组数 (不含 group0)
	subexpNames []string // len = numSubexp+1, [0]="", 命名捕获组的名字, 无名为 ""
}

// strBytePtr 返回 s 底层字节的指针 (零拷贝); s 为空返回 nil. 仅可用于紧随其后的同步 C
// 调用, 且调用点须 runtime.KeepAlive(s) 保活该内存. 用 reflect.StringHeader 取指针
// (而非 go1.20 才有的 unsafe.StringData), 以兼容 go 1.19.
func strBytePtr(s string) *C.char {
	if len(s) == 0 {
		return nil
	}
	return (*C.char)(unsafe.Pointer((*reflect.StringHeader)(unsafe.Pointer(&s)).Data))
}

// Compile 编译一个 RE2 正则. 编译错误返回 error (不 panic).
func Compile(pattern string) (*Regexp, error) {
	p := strBytePtr(pattern)
	h := C.cre2_new(p, C.int(len(pattern)))
	runtime.KeepAlive(pattern)
	if h == nil {
		return nil, errors.New("re2native: out of memory")
	}
	if C.cre2_ok(h) == 0 {
		msg := C.GoString(C.cre2_error(h))
		C.cre2_free(h)
		return nil, errors.New("re2native: " + msg)
	}
	ng := int(C.cre2_num_groups(h))
	names := make([]string, ng+1)
	var nbuf [256]C.char
	for i := 1; i <= ng; i++ {
		n := int(C.cre2_group_name(h, C.int(i), &nbuf[0], C.int(len(nbuf))))
		if n > 0 {
			if n > len(nbuf) {
				n = len(nbuf)
			}
			names[i] = C.GoStringN(&nbuf[0], C.int(n))
		}
	}
	re := &Regexp{h: h, expr: pattern, numSubexp: ng, subexpNames: names}
	runtime.SetFinalizer(re, func(r *Regexp) { C.cre2_free(r.h) })
	return re, nil
}

// MustCompile 同 Compile, 失败 panic. 对齐 go-re2/stdlib MustCompile.
func MustCompile(pattern string) *Regexp {
	re, err := Compile(pattern)
	if err != nil {
		panic(`re2native: Compile(` + strings.TrimSpace(pattern) + `): ` + err.Error())
	}
	return re
}

// String 返回编译时的源 pattern.
func (re *Regexp) String() string { return re.expr }

// NumSubexp 返回捕获组个数 (不含整体匹配).
func (re *Regexp) NumSubexp() int { return re.numSubexp }

// SubexpNames 返回各捕获组的名字 (下标 0 为整体匹配, 恒为 "").
func (re *Regexp) SubexpNames() []string { return re.subexpNames }

// findFrom 返回从 pos 起【非锚定】下一处匹配的子组区间 (长度 2*(numSubexp+1) 的 [start,end) 对,
// 未参与的组为 -1,-1), 无匹配返回 nil. 等价 stdlib doExecute(pos).
func (re *Regexp) findFrom(s string, pos int) []int {
	nmatch := re.numSubexp + 1
	cbuf := make([]C.int, 2*nmatch)
	tp := strBytePtr(s)
	ok := C.cre2_match_at(re.h, tp, C.int(len(s)), C.int(pos), &cbuf[0], C.int(nmatch)) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if !ok {
		return nil
	}
	out := make([]int, 2*nmatch)
	for i := range out {
		out[i] = int(cbuf[i])
	}
	return out
}

// subStrings 把一组 index 区间转成 []string (nil 组 = "").
func (re *Regexp) subStrings(s string, m []int) []string {
	res := make([]string, len(m)/2)
	for i := range res {
		if m[2*i] >= 0 {
			res[i] = s[m[2*i]:m[2*i+1]]
		}
	}
	return res
}

// MatchString 报告 s 是否含任意匹配 (非锚定). 走快路径, 不取子组.
func (re *Regexp) MatchString(s string) bool {
	p := strBytePtr(s)
	ok := C.cre2_partial_match(re.h, p, C.int(len(s))) != 0
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	return ok
}

// FindStringIndex 返回最左匹配的 [start,end), 无匹配返回 nil.
func (re *Regexp) FindStringIndex(s string) []int {
	m := re.findFrom(s, 0)
	if m == nil {
		return nil
	}
	return []int{m[0], m[1]}
}

// FindString 返回最左匹配的文本, 无匹配返回 "".
func (re *Regexp) FindString(s string) string {
	m := re.findFrom(s, 0)
	if m == nil {
		return ""
	}
	return s[m[0]:m[1]]
}

// FindStringSubmatch 返回最左匹配 + 各子组文本, 无匹配返回 nil.
func (re *Regexp) FindStringSubmatch(s string) []string {
	m := re.findFrom(s, 0)
	if m == nil {
		return nil
	}
	return re.subStrings(s, m)
}

// FindStringSubmatchIndex 返回最左匹配 + 各子组的 index 区间, 无匹配返回 nil.
func (re *Regexp) FindStringSubmatchIndex(s string) []int {
	return re.findFrom(s, 0)
}

// allMatches 遍历所有匹配, 逐个回调 deliver (移植 stdlib regexp.allMatches 的空匹配推进逻辑).
func (re *Regexp) allMatches(s string, n int, deliver func([]int)) {
	end := len(s)
	for pos, i, prevMatchEnd := 0, 0, -1; i < n && pos <= end; {
		m := re.findFrom(s, pos)
		if m == nil {
			break
		}
		accept := true
		if m[1] == m[0] {
			// 空匹配: 紧贴上一处匹配末尾的空匹配丢弃, 避免重复.
			if m[0] == prevMatchEnd {
				accept = false
			}
			_, width := utf8.DecodeRuneInString(s[pos:])
			if width > 0 {
				pos += width
			} else {
				pos = end + 1
			}
		} else {
			pos = m[1]
		}
		prevMatchEnd = m[1]
		if accept {
			deliver(m)
			i++
		}
	}
}

// FindAllString 返回前 n 个匹配文本 (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllString(s string, n int) []string {
	if n < 0 {
		n = len(s) + 1
	}
	var res []string
	re.allMatches(s, n, func(m []int) { res = append(res, s[m[0]:m[1]]) })
	return res
}

// FindAllStringIndex 返回前 n 个匹配的 [start,end) (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllStringIndex(s string, n int) [][]int {
	if n < 0 {
		n = len(s) + 1
	}
	var res [][]int
	re.allMatches(s, n, func(m []int) { res = append(res, []int{m[0], m[1]}) })
	return res
}

// FindAllStringSubmatch 返回前 n 个匹配的 (匹配+各子组文本) (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllStringSubmatch(s string, n int) [][]string {
	if n < 0 {
		n = len(s) + 1
	}
	var res [][]string
	re.allMatches(s, n, func(m []int) { res = append(res, re.subStrings(s, m)) })
	return res
}

// FindAllStringSubmatchIndex 返回前 n 个匹配的 index 区间 (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllStringSubmatchIndex(s string, n int) [][]int {
	if n < 0 {
		n = len(s) + 1
	}
	var res [][]int
	re.allMatches(s, n, func(m []int) { res = append(res, append([]int(nil), m...)) })
	return res
}

// replaceAllString 移植 stdlib regexp.replaceAll 的字符串版 (含空匹配推进).
func (re *Regexp) replaceAllString(src string, repl func(m []int) string) string {
	lastMatchEnd := 0
	searchPos := 0
	var b strings.Builder
	end := len(src)
	for searchPos <= end {
		m := re.findFrom(src, searchPos)
		if m == nil {
			break
		}
		b.WriteString(src[lastMatchEnd:m[0]])
		// 与 stdlib replaceAll 完全一致的写入条件: 匹配末尾超过上次匹配末尾, 或匹配在串首.
		if m[1] > lastMatchEnd || m[0] == 0 {
			b.WriteString(repl(m))
		}
		lastMatchEnd = m[1]
		_, width := utf8.DecodeRuneInString(src[searchPos:])
		if searchPos+width > m[1] {
			searchPos += width
		} else if searchPos+1 > m[1] {
			searchPos++
		} else {
			searchPos = m[1]
		}
	}
	b.WriteString(src[lastMatchEnd:])
	return b.String()
}

// ReplaceAllString 用 repl 替换所有匹配; repl 支持 $1 / ${name} 展开 (语义对齐 stdlib).
func (re *Regexp) ReplaceAllString(src, repl string) string {
	if strings.IndexByte(repl, '$') < 0 {
		return re.replaceAllString(src, func(m []int) string { return repl })
	}
	return re.replaceAllString(src, func(m []int) string {
		return string(re.expand(nil, repl, src, m))
	})
}

// ReplaceAllStringFunc 用 f(匹配文本) 的返回值替换所有匹配.
func (re *Regexp) ReplaceAllStringFunc(src string, f func(string) string) string {
	return re.replaceAllString(src, func(m []int) string {
		return f(src[m[0]:m[1]])
	})
}

// expand 移植 stdlib regexp.expand: 把 template 里的 $1/${1}/$name/${name}/$$ 展开.
func (re *Regexp) expand(dst []byte, template string, src string, match []int) []byte {
	for len(template) > 0 {
		before, after, ok := strings.Cut(template, "$")
		if !ok {
			break
		}
		dst = append(dst, before...)
		template = after
		if template != "" && template[0] == '$' {
			dst = append(dst, '$') // $$ → $
			template = template[1:]
			continue
		}
		name, num, rest, ok := extractGroupName(template)
		if !ok {
			dst = append(dst, '$') // 非法引用, $ 当字面
			continue
		}
		template = rest
		if num >= 0 {
			if 2*num+1 < len(match) && match[2*num] >= 0 {
				dst = append(dst, src[match[2*num]:match[2*num+1]]...)
			}
		} else {
			for i, namei := range re.subexpNames {
				if name == namei && 2*i+1 < len(match) && match[2*i] >= 0 {
					dst = append(dst, src[match[2*i]:match[2*i+1]]...)
					break
				}
			}
		}
	}
	dst = append(dst, template...)
	return dst
}

// extractGroupName 解析 template (已去掉前导 '$') 开头的 name / {name}; 纯数字时 num 为该数, 否则 num=-1.
// 移植 stdlib regexp.extract.
func extractGroupName(str string) (name string, num int, rest string, ok bool) {
	if len(str) == 0 {
		return
	}
	brace := false
	if str[0] == '{' {
		brace = true
		str = str[1:]
	}
	i := 0
	for i < len(str) {
		r, size := utf8.DecodeRuneInString(str[i:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		i += size
	}
	if i == 0 {
		return // 空名不合法
	}
	name = str[:i]
	if brace {
		if i >= len(str) || str[i] != '}' {
			return // 缺右花括号
		}
		rest = str[i+1:]
	} else {
		rest = str[i:]
	}
	num = 0
	for j := 0; j < len(name); j++ {
		if name[j] < '0' || '9' < name[j] || num >= 1e8 {
			num = -1
			break
		}
		num = num*10 + int(name[j]) - '0'
	}
	if name[0] == '0' && len(name) > 1 {
		num = -1 // 禁前导零
	}
	ok = true
	return
}
