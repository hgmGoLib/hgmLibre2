// Package hgmLibre2 — 自带 cgo 的原生 RE2 正则库: 不用 go-re2 / 不用 abseil / 不用 cmake,
// 编译期不下载远程源 (RE2 2023-03-01 源码已 vendored 在本目录, 纯 C++11, zig 可交叉编译).
//
// 相比 go-re2 的 wazero 后端: 原生 cgo 路径不实例化 wazero runtime, 也不做 stdio 句柄探测,
// 因此在无 std 句柄的环境 (如 Windows SCM service) 也能正常用; 同时是单文件静态链接.
//
// API 的方法签名与 stdlib regexp 的 string 系方法一致 (Compile/MustCompile + Find/Replace 系列),
// 可作为 `type R = hgmLibre2.Regexp` 替换 *regexp.Regexp 上这批方法. 匹配选择是 leftmost-first
// (同 regexp.Compile, 非 leftmost-longest). 注意它走原生 RE2 引擎, 在少数边角与 stdlib 不同:
// 非法 UTF-8 输入上 . 的匹配、\C(任意字节)等按 RE2 语义而非 stdlib —— 详见 README 的 caveats.
package hgmLibre2

/*
#cgo CXXFLAGS: -std=c++11 -O2 -DNDEBUG -fno-exceptions -fno-rtti -I${SRCDIR}/internal_include
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

// maxCInt 是 C.int 能表示的最大正值. Go 侧把 len/pos cast 成 C.int 前据此守卫,
// 避免超 2GiB 字符串溢出成错误偏移甚至越界 (C ABI 用 int 传长度/偏移).
const maxCInt = 1<<31 - 1

// Regexp 持有一个原生 RE2 句柄. 默认靠 finalizer 释放 (不强制 Close);
// 大量动态编译 pattern 想及时回收 native 内存时可显式调 FreeC.
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
	if len(pattern) > maxCInt {
		return nil, errors.New("re2native: pattern too large (>2GiB)")
	}
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
		// cre2_group_name 回填 buf 并返回名字真实长度. 名字超过栈 buffer 时按真实长度
		// 精确分配再取一次, 不截断 (超长命名捕获组的 SubexpNames/${name} 才不会失真).
		n := int(C.cre2_group_name(h, C.int(i), &nbuf[0], C.int(len(nbuf))))
		switch {
		case n <= 0:
			// 无名组, 留 ""
		case n <= len(nbuf):
			names[i] = C.GoStringN(&nbuf[0], C.int(n))
		default:
			big := make([]C.char, n)
			n2 := int(C.cre2_group_name(h, C.int(i), &big[0], C.int(n)))
			if n2 > n {
				n2 = n
			}
			names[i] = C.GoStringN(&big[0], C.int(n2))
		}
	}
	re := &Regexp{h: h, expr: pattern, numSubexp: ng, subexpNames: names}
	runtime.SetFinalizer(re, func(r *Regexp) { C.cre2_free(r.h) })
	return re, nil
}

// FreeC 立即释放内部的原生 RE2(C++)资源并清掉 finalizer. 用于大量动态编译 pattern、
// 想及时回收 native 内存而不等 GC 的场景. 释放后该 Regexp 的所有方法不可再用.
//
// 注意(故意不做防护, 由调用方保证): 非线程安全, 不可与其它方法/另一个 FreeC 并发调用;
// 释放后再调用任何方法是 use-after-free, 行为未定义. 不需要及时回收就别调, 交给 finalizer 兜底即可.
func (re *Regexp) FreeC() {
	if re.h == nil {
		return
	}
	C.cre2_free(re.h)
	re.h = nil
	runtime.SetFinalizer(re, nil)
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
	if len(s) > maxCInt { // 超 C.int 的输入直接当无匹配, 不让 len/pos 溢出成错偏移
		return nil
	}
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
	if len(s) > maxCInt {
		return false
	}
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

// matchAllFlat 跑批量全匹配 (单次 cgo), 把 C 返回的所有匹配 index 一次性拷进【单块】Go []int 返回:
// 每处匹配 per=2*(numSubexp+1) 个 int (group0.start,group0.end, group1.start,...; 未参与组 -1,-1),
// 顺序排布. 无匹配返回 nil,0.
//
// 两件事下沉/合并:
//   1. 「逐处匹配」循环在 C 的 cre2_match_all 里一次跑完 → cgo 跨界从 O(匹配数) 压成 1 次.
//   2. 结果只在这一块 flat 上分配一次; Find* 系列直接对它切片 (见各方法), 不再每匹配 make 小 slice
//      → 分配次数从 O(匹配数) 压成 O(1). 大正文多命中时这是分配次数的大头 (defillage 等).
//
// 内存正确性: flat 是本次调用的局部块 (并发各自持有, 不挂 re); re.h 只读, RE2 Match 可并发.
// cflat 是 C malloc 内存上的视图, 仅在 C.free 前一次性拷出, 拷完即 free, 不外泄 C 指针.
func (re *Regexp) matchAllFlat(s string, n int) (flat []int, count int) {
	if len(s) > maxCInt { // 超 C.int 的输入直接当无匹配, 不让 len/pos 溢出成错偏移
		return nil, 0
	}
	nmatch := re.numSubexp + 1
	tp := strBytePtr(s)
	var out *C.int
	var cnt C.int
	rc := C.cre2_match_all(re.h, tp, C.int(len(s)), C.int(nmatch), C.int(n), &out, &cnt)
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if rc <= 0 || out == nil || cnt == 0 {
		return nil, 0 // 无匹配 (rc==0) 或 malloc 失败 (rc<0): 当作无匹配
	}
	count = int(cnt)
	total := count * 2 * nmatch
	cflat := unsafe.Slice(out, total)
	flat = make([]int, total)
	for i := 0; i < total; i++ {
		flat[i] = int(cflat[i])
	}
	C.free(unsafe.Pointer(out))
	return flat, count
}

// FindAllString 返回前 n 个匹配文本 (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllString(s string, n int) []string {
	if n < 0 {
		n = len(s) + 1
	}
	flat, count := re.matchAllFlat(s, n)
	if count == 0 {
		return nil
	}
	per := 2 * (re.numSubexp + 1)
	res := make([]string, count) // 单次分配; 各元素是 s 的子串 (零拷贝 header, 同 stdlib)
	for k := 0; k < count; k++ {
		base := k * per
		res[k] = s[flat[base]:flat[base+1]]
	}
	return res
}

// FindAllStringIndex 返回前 n 个匹配的 [start,end) (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllStringIndex(s string, n int) [][]int {
	if n < 0 {
		n = len(s) + 1
	}
	flat, count := re.matchAllFlat(s, n)
	if count == 0 {
		return nil
	}
	per := 2 * (re.numSubexp + 1)
	res := make([][]int, count) // 单次分配外壳; 各元素切 flat 的 group0 段 (共享同一 backing)
	for k := 0; k < count; k++ {
		base := k * per
		res[k] = flat[base : base+2 : base+2] // 限 cap 防外部 append 越写到下一匹配
	}
	return res
}

// FindAllStringSubmatch 返回前 n 个匹配的 (匹配+各子组文本) (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllStringSubmatch(s string, n int) [][]string {
	if n < 0 {
		n = len(s) + 1
	}
	flat, count := re.matchAllFlat(s, n)
	if count == 0 {
		return nil
	}
	per := 2 * (re.numSubexp + 1)
	res := make([][]string, count)
	for k := 0; k < count; k++ {
		base := k * per
		res[k] = re.subStrings(s, flat[base:base+per])
	}
	return res
}

// FindAllStringSubmatchIndex 返回前 n 个匹配的 index 区间 (n<0 = 全部), 无匹配返回 nil.
func (re *Regexp) FindAllStringSubmatchIndex(s string, n int) [][]int {
	if n < 0 {
		n = len(s) + 1
	}
	flat, count := re.matchAllFlat(s, n)
	if count == 0 {
		return nil
	}
	per := 2 * (re.numSubexp + 1)
	res := make([][]int, count) // 单次分配外壳; 各元素切 flat 的整 per 段 (共享同一 backing)
	for k := 0; k < count; k++ {
		base := k * per
		res[k] = flat[base : base+per : base+per] // 限 cap 防 append 越界到下一匹配
	}
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

// FindReplaceWithin 等价于
//
//	find.ReplaceAllStringFunc(src, func(m string) string { return strip.ReplaceAllString(m, repl) })
//
// 但把【外层 find 逐处匹配循环 + 每处匹配内层 strip 替换】整体下沉到 C++ (cre2_find_replace_within),
// 全程只一次 cgo 跨界、Go 侧零 per-match 分配。算法与上式逐字一致: find 仍可零捕获组走最快 DFA,
// strip 仍只在【已命中段内】替换。典型用途: 注入愈合 (find=被分隔符拆开的动词骨架正则,
// strip=分隔符字符类, repl="")。
//
// 结果惰性物化: 若 src 经过替换后【逐字节没有任何变化】(最常见: 全程无匹配 / 命中但删 0 个字符),
// C 侧不分配也不拷贝, 本方法直接返回原 src (零分配)。仅在确有改动时才拷一次结果。
func (find *Regexp) FindReplaceWithin(strip *Regexp, src, repl string) string {
	if len(src) > maxCInt {
		return src // 超 C.int 输入: 退化为原样 (同其它方法对超大输入的保守处理)
	}
	sp := strBytePtr(src)
	rp := strBytePtr(repl)
	res := C.cre2_find_replace_within(find.h, strip.h, sp, C.int(len(src)), rp, C.int(len(repl)))
	runtime.KeepAlive(src)
	runtime.KeepAlive(repl)
	runtime.KeepAlive(find)
	runtime.KeepAlive(strip)
	if res.changed == 0 || res.out == nil {
		return src // 无改动 (含 rc<0 malloc 失败): 原样返回, 零分配
	}
	out := C.GoStringN(res.out, res.outlen) // 一次性拷出 C 缓冲
	C.free(unsafe.Pointer(res.out))
	return out
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
