// Package hgmLibre2 — 自带 cgo 的原生 RE2 正则库: 不用 go-re2 / 不用 abseil / 不用 cmake,
// 编译期不下载远程源 (RE2 2023-03-01 源码已 vendored 在本目录, 纯 C++11, zig 可交叉编译).
//
// 相比 go-re2 的 wazero 后端: 原生 cgo 路径不实例化 wazero runtime, 也不做 stdio 句柄探测,
// 因此在无 std 句柄的环境 (如 Windows SCM service) 也能正常用; 同时是单文件静态链接.
//
// API 方法名/签名与 stdlib regexp 的 string 系与 []byte 系方法一致 (Compile/MustCompile + Find/Replace
// 系列; []byte 门面见 bytes.go, 与 string 系共用同一套匹配内核, 传 []byte 不产生拷贝),
// 便于互读; 但【不是】*regexp.Regexp 的 drop-in, 也不打算是. 匹配选择是 leftmost-first
// (同 regexp.Compile, 非 leftmost-longest). 与 stdlib 的有意差异: ReplaceAllString 的 repl 按【字面】
// 替换 (不展开 $1/${name}/$$, 见该方法注释); 以及原生 RE2 引擎的边角 (非法 UTF-8 上 . 的匹配、\C
// 任意字节等按 RE2 语义) —— 详见 README 的 "Differences from stdlib regexp" 一节.
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
	"unsafe"
)

// DefaultMaxMem 是 RE2 的默认内存预算 (RE2::Options::kDefaultMaxMem = 8MB)。
// Compile 用的就是它; CompileMaxMem 传 <=0 也回落到它。
const DefaultMaxMem int64 = 8 << 20

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
// 内存预算 = RE2 默认 DefaultMaxMem (8MB); 要自己定预算用 CompileMaxMem.
func Compile(pattern string) (*Regexp, error) { return CompileMaxMem(pattern, 0) }

// CompileMaxMem 同 Compile, 但显式指定这一条 pattern 的内存预算 maxMem (字节; <=0 = 用默认 8MB)。
//
// maxMem 就是 RE2::Options::max_mem, 一个旋钮同时抬两条天花板:
//   - 编译期: 这条 pattern 的程序指令条数上限。撞了 → 本函数返回 error
//     ("pattern too large - compile failed"), 翻倍重试即可。
//   - 运行期: 剩下的额度给 DFA 状态缓存 (正向 prog 拿 2/3, 反向 prog 拿 1/3)。缓存装不下
//     当前语料走出来的状态集时 DFA 不是 LRU 淘汰而是【整表清空重建】—— 结果仍然正确,
//     所以调用方看不见任何信号, 但吞吐是几十倍的悬崖 (见 dfastats.go 的开头)。
//
// 什么时候需要动它: 单条 pattern 里有【起始类窄于重复类】的计数重复 (如
// `[A-Za-z][A-Za-z0-9]{2,19}key`) 时, 正向 DFA 的状态数对界指数增长, 默认 8MB 装不下,
// 于是每份新正文都把缓存冲垮一次。两条出路二选一或都用:
//   - 把预算调大 (本函数), 用内存换掉 thrash;
//   - 反着扫 (CompileReverse 编一个 RegexpReverse), 让状态数从指数塌回线性 ——
//     这一条不花内存, 但只回答"命中没有"。
//
// 怎么标定: 拿一批【互不相同】的真语料单线程跑一遍, 看 RegexpReverse.MatchStats/ScanStats 的
// Flushes 或进程级 DFAStats().Resets 增量; >0 就翻倍重来, 直到增量归零。
func CompileMaxMem(pattern string, maxMem int64) (*Regexp, error) {
	if len(pattern) > maxCInt {
		return nil, errors.New("re2native: pattern too large (>2GiB)")
	}
	p := strBytePtr(pattern)
	h := C.cre2_new_max_mem(p, C.int(len(pattern)), C.int64_t(maxMem))
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

// MaxMem 返回这条 Regexp 编译时实际生效的内存预算 (字节)。Compile 出来的就是 DefaultMaxMem。
func (re *Regexp) MaxMem() int64 {
	n := int64(C.cre2_max_mem(re.h))
	runtime.KeepAlive(re)
	return n
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

// ReplaceAllString 把每处匹配整体换成【字面】repl 并返回新串。repl 按原始字节插入, 不解释任何
// 转义/捕获组引用 —— 既不照搬 stdlib 的 $1/${name}/$$ 展开, 也不照搬 RE2 GlobalReplace 的 \1 重写串
// (那两套都需各自的转义分析, 易错且本库无调用方需要; 见 README 的 Differences from stdlib 一节)。这意味着 ReplaceAllString
// 不是 stdlib *regexp.Regexp 的 drop-in —— 需要 $1 捕获展开请改用 ReplaceAllStringFunc 自行拼。
//
// 整循环 (逐处匹配 + 字面拼接) 下沉 C++ (cre2_replace_all_literal), 单次 cgo; 惰性物化: 全程无字节
// 改动 (无匹配 / repl 与命中段逐字节相同) 直接复用原 src, 零分配。
func (re *Regexp) ReplaceAllString(src, repl string) string {
	out, changed := re.replaceAllLiteralRaw(src, repl)
	if !changed {
		return src // 无改动: 原样返回, 零分配
	}
	return out
}

// replaceAllLiteralRaw 是 ReplaceAllString 与 ReplaceAll([]byte) 共用的内核: 跑一次 cgo 字面全替换,
// 把 C 缓冲一次性拷成 Go string 后立即 free。changed=false 表示【逐字节无改动】—— 含无匹配、repl 与
// 命中段相同、超 C.int 的超大输入、C 侧 malloc 失败这几种保守回退; 此时 out 为 "" 无意义, 由各门面
// 自行返回原输入 (string 门面返回原 src, []byte 门面返回原 src 切片)。
func (re *Regexp) replaceAllLiteralRaw(src, repl string) (out string, changed bool) {
	if len(src) > maxCInt {
		return "", false // 超 C.int 输入: 当无改动 (同其它方法对超大输入的保守处理)
	}
	sp := strBytePtr(src)
	rp := strBytePtr(repl)
	res := C.cre2_replace_all_literal(re.h, sp, C.int(len(src)), rp, C.int(len(repl)))
	runtime.KeepAlive(src)
	runtime.KeepAlive(repl)
	runtime.KeepAlive(re)
	if res.changed == 0 || res.out == nil {
		return "", false
	}
	out = C.GoStringN(res.out, res.outlen) // 一次性拷出 C 缓冲
	C.free(unsafe.Pointer(res.out))
	return out, true
}

// ReplaceAllStringFunc 用 f(匹配文本) 的返回值替换所有匹配。f 是 Go 回调无法下沉 C++ (下沉需每处
// 匹配回调 Go, 反而增加跨界), 故拼接循环留在 Go; 但匹配位置一次取齐, cgo 调用数已从 O(匹配数) 压到 1。
// 取位置的那段 C 循环已按 stdlib allMatches 语义做了空匹配去重 + UTF-8 rune 推进, 故每处投递的匹配都
// 满足 stdlib replaceAll 的写入条件 (m1>lastMatchEnd || m0==0), 这里无条件写即与 stdlib 逐字一致。
// 惰性物化 (同 ReplaceAllString): 全程无字节改动 —— 无匹配, 或有匹配但每处 f 都把原文照样写回 ——
// 直接复用原 src 返回, 零分配。
//
// 结果底按 len(src) 【一次开够】(同 stdlib replaceAll 的 make([]byte,0,len(src)), 也同本库 []byte 门面
// ReplaceAllFunc): 从 0 开始长的话累计分配收敛到 5×len(src) (Go 大切片 1.25 倍增长 ⇒ 1/(1-1/1.25)),
// 拷贝还白付 4 份 —— 实测 64MB 正文上是 329MB。要连这一块底都复用 (逐段反复调的热路径),
// 用 ReplaceAllStringFunc_ctx_t.AppendReplaceAllStringFunc 追加进自己的缓冲。
func (re *Regexp) ReplaceAllStringFunc(src string, f func(string) string) string {
	var ctx ReplaceAllStringFunc_ctx_t // 一次性用完就扔; 复用请直接持有 ctx 走 Append 版
	out, changed := ctx.AppendReplaceAllStringFunc(nil, re, src, f)
	if !changed {
		return src
	}
	return bytesStr(out) // out 是刚现开的底, 出了这里没有第二个引用 ⇒ 零拷贝转 string 安全
}

// FindReplaceWithin 等价于
//
//	find.ReplaceAllStringFunc(src, func(m string) string { return strip.ReplaceAllString(m, repl) })
//
// 但把【外层 find 逐处匹配循环 + 每处匹配内层 strip 替换】整体下沉到 C++ (cre2_find_replace_within),
// 全程只一次 cgo 跨界、Go 侧零 per-match 分配。算法与上式逐字一致: find 仍可零捕获组走最快 DFA,
// strip 仍只在【已命中段内】替换。典型用途: 去混淆还原 (find=被分隔符拆开的关键词骨架正则,
// strip=分隔符字符类, repl="")。
//
// 结果惰性物化: 若 src 经过替换后【逐字节没有任何变化】(最常见: 全程无匹配 / 命中但删 0 个字符),
// C 侧不分配也不拷贝, 本方法直接返回原 src (零分配)。仅在确有改动时才拷一次结果。
//
// 注意 repl 是 RE2 重写串 (交给 RE2 GlobalReplace), 捕获组引用用 \1..\9; 而 ReplaceAllString 的 repl
// 是纯字面 (不解释任何引用)。对常见的字面 repl (如 "") 二者无差别。
func (find *Regexp) FindReplaceWithin(strip *Regexp, src, repl string) string {
	out, changed := find.findReplaceWithinRaw(strip, src, repl)
	if !changed {
		return src // 无改动: 原样返回, 零分配
	}
	return out
}

// findReplaceWithinRaw 是 FindReplaceWithin 与 FindReplaceWithinBytes 共用的内核: 跑一次 cgo
// (外层 find 循环 + 段内 strip 替换全在 C++), 把 C 缓冲一次性拷成 Go string 后立即 free。
// changed 的含义与回退情形同 replaceAllLiteralRaw。
func (find *Regexp) findReplaceWithinRaw(strip *Regexp, src, repl string) (out string, changed bool) {
	if len(src) > maxCInt {
		return "", false // 超 C.int 输入: 当无改动 (同其它方法对超大输入的保守处理)
	}
	sp := strBytePtr(src)
	rp := strBytePtr(repl)
	res := C.cre2_find_replace_within(find.h, strip.h, sp, C.int(len(src)), rp, C.int(len(repl)))
	runtime.KeepAlive(src)
	runtime.KeepAlive(repl)
	runtime.KeepAlive(find)
	runtime.KeepAlive(strip)
	if res.changed == 0 || res.out == nil {
		return "", false
	}
	out = C.GoStringN(res.out, res.outlen) // 一次性拷出 C 缓冲
	C.free(unsafe.Pointer(res.out))
	return out, true
}
