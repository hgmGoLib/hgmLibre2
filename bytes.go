// bytes.go — []byte 系方法: 与 string 系【同一套定位内核】的另一个门面, 供正文本来就是 []byte
// 的调用方 (HTTP body / 文件内容 / 解码缓冲) 免掉 string(b) 的全量拷贝。
//
// 零拷贝原理: 匹配本身在 C 侧只吃「字节指针 + 长度」(见 strBytePtr / cre2_match_at), 与 Go 侧是
// string 还是 []byte 无关。故本文件不重写任何匹配逻辑, 只把 b 零拷贝成 string 视图喂给既有内核
// (findFrom / matchAllFlat / replaceAllLiteralRaw / findReplaceWithinRaw), 再按返回的 index 从
// 【原 b】切子切片 —— 与 stdlib bytes 系一样, 结果与输入共享底层数组, 全程零字节拷贝。
//
// 命名规则: stdlib *regexp.Regexp 有同名 []byte 方法的照搬 stdlib (Find/FindIndex/… ↔ FindString/
// FindStringIndex/…); 本库自有的方法加 Bytes 后缀 (FindReplaceWithinBytes / RegexpSet.MatchBytes)。
//
// 语义与 stdlib 的两点差异 (与本库 string 系保持一致, 非 stdlib drop-in):
//  1. Replace 系在【逐字节无改动】时直接返回原 src 切片 (零分配), 不像 stdlib 总返回新副本;
//     调用方【不得改写】返回值, 需要独立可写副本请自行 copy。
//  2. 其余 nil / 空切片语义与 stdlib 逐一对齐 (结果为空 → nil), 见各方法注释。
//
// 并发/可变性约束 (同 stdlib): 调用期间不得改写传入的 b —— C 侧直接读它的底层数组。
package hgmLibre2

import (
	"runtime"
	"unsafe"
)

// bytesStr 把 b 零拷贝成只读 string 视图 (共享底层数组, 不分配); b 为空返回 ""。
//
// 仅用于把 []byte 喂给本包内那些「只读 s、不留存 s」的 string 内核; 视图逃逸出包会让调用方
// 通过改写 b 破坏 string 不可变性, 故本函数私有且返回值绝不外传。
//
// GC 安全: 视图的数据指针即 b 的底层数组指针, 内核里的 runtime.KeepAlive(s) 保活的正是该数组;
// 各门面另加 runtime.KeepAlive(b) 双保险。用 slice/string header 前两字段同布局的经典转换
// (而非 go1.20 才有的 unsafe.String), 以兼容 go 1.19 —— 同 strBytePtr 的取舍。
func bytesStr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return *(*string)(unsafe.Pointer(&b))
}

// subBytes 把一组 index 区间转成 [][]byte (未参与的组 = nil, 同 stdlib)。
// 各元素是 b 的子切片 (零拷贝), cap 限到区间末尾, 防调用方 append 越写到下一段。
// 与 subStrings 一一对应 (那个切 string, 这个切 []byte)。
func (re *Regexp) subBytes(b []byte, m []int) [][]byte {
	res := make([][]byte, len(m)/2)
	for i := range res {
		if m[2*i] >= 0 {
			res[i] = b[m[2*i]:m[2*i+1]:m[2*i+1]]
		}
	}
	return res
}

// Match 报告 b 是否含任意匹配 (非锚定)。同 MatchString, 走不取子组的快路径。
func (re *Regexp) Match(b []byte) bool {
	ok := re.MatchString(bytesStr(b))
	runtime.KeepAlive(b)
	return ok
}

// Find 返回最左匹配的字节 (b 的子切片, 零拷贝), 无匹配返回 nil。
func (re *Regexp) Find(b []byte) []byte {
	m := re.findWithin(bytesStr(b), 0, len(b))
	runtime.KeepAlive(b)
	if m == nil {
		return nil
	}
	return b[m[0]:m[1]:m[1]]
}

// FindIndex 返回最左匹配的 [start,end), 无匹配返回 nil。
func (re *Regexp) FindIndex(b []byte) []int {
	loc := re.FindStringIndex(bytesStr(b))
	runtime.KeepAlive(b)
	return loc
}

// FindSubmatch 返回最左匹配 + 各子组的字节 (都是 b 的子切片; 未参与的组为 nil), 无匹配返回 nil。
func (re *Regexp) FindSubmatch(b []byte) [][]byte {
	m := re.findWithin(bytesStr(b), 0, len(b))
	runtime.KeepAlive(b)
	if m == nil {
		return nil
	}
	return re.subBytes(b, m)
}

// FindSubmatchIndex 返回最左匹配 + 各子组的 index 区间, 无匹配返回 nil。
func (re *Regexp) FindSubmatchIndex(b []byte) []int {
	m := re.findWithin(bytesStr(b), 0, len(b))
	runtime.KeepAlive(b)
	return m
}

// FindAll 返回前 n 个匹配的字节 (各为 b 的子切片) (n<0 = 全部), 无匹配返回 nil。
func (re *Regexp) FindAll(b []byte, n int) [][]byte {
	if n < 0 {
		n = len(b) + 1
	}
	flat, count := re.matchAllFlat(bytesStr(b), n)
	runtime.KeepAlive(b)
	if count == 0 {
		return nil
	}
	per := 2 * (re.numSubexp + 1)
	res := make([][]byte, count) // 单次分配; 各元素是 b 的子切片 (零拷贝, 同 FindAllString)
	for k := 0; k < count; k++ {
		base := k * per
		res[k] = b[flat[base]:flat[base+1]:flat[base+1]]
	}
	return res
}

// FindAllIndex 返回前 n 个匹配的 [start,end) (n<0 = 全部), 无匹配返回 nil。
func (re *Regexp) FindAllIndex(b []byte, n int) [][]int {
	locs := re.FindAllStringIndex(bytesStr(b), n)
	runtime.KeepAlive(b)
	return locs
}

// FindAllSubmatch 返回前 n 个匹配的 (匹配+各子组字节) (n<0 = 全部), 无匹配返回 nil。
func (re *Regexp) FindAllSubmatch(b []byte, n int) [][][]byte {
	if n < 0 {
		n = len(b) + 1
	}
	flat, count := re.matchAllFlat(bytesStr(b), n)
	runtime.KeepAlive(b)
	if count == 0 {
		return nil
	}
	per := 2 * (re.numSubexp + 1)
	res := make([][][]byte, count)
	for k := 0; k < count; k++ {
		base := k * per
		res[k] = re.subBytes(b, flat[base:base+per])
	}
	return res
}

// FindAllSubmatchIndex 返回前 n 个匹配的 index 区间 (n<0 = 全部), 无匹配返回 nil。
func (re *Regexp) FindAllSubmatchIndex(b []byte, n int) [][]int {
	idx := re.FindAllStringSubmatchIndex(bytesStr(b), n)
	runtime.KeepAlive(b)
	return idx
}

// ReplaceAll 把每处匹配整体换成【字面】repl 并返回结果。repl 按原始字节插入, 不解释 $1/${name}/\1
// —— 与 ReplaceAllString 同一内核、同一字面语义 (故同样不是 stdlib drop-in, 需捕获组展开用 ReplaceAllFunc)。
//
// 惰性物化: 全程无字节改动 (无匹配 / repl 与命中段逐字节相同) 时【直接返回原 src 切片, 零分配】,
// 此时返回值与 src 共享底层数组, 不得改写。确有改动时才拷一次新切片。结果为空返回 nil (同 stdlib)。
func (re *Regexp) ReplaceAll(src, repl []byte) []byte {
	out, changed := re.replaceAllLiteralRaw(bytesStr(src), bytesStr(repl))
	runtime.KeepAlive(src)
	runtime.KeepAlive(repl)
	if !changed {
		if len(src) == 0 {
			return nil // 对齐 stdlib: 空输入无改动 → nil (而非长度 0 的非 nil 切片)
		}
		return src
	}
	if len(out) == 0 {
		return nil // 全被替换空: stdlib 此时也返回 nil
	}
	return []byte(out)
}

// ReplaceAllFunc 用 f(匹配字节) 的返回值替换所有匹配。与 ReplaceAllStringFunc 同一套匹配定位
// (matchAllFlat 一次取齐所有位置, cgo 跨界只 1 次), 差别仅在这里按 []byte 拼接。
//
// 传给 f 的是 src 的子切片 (零拷贝, cap 已限到匹配末尾; stdlib 不限 cap, 本库限住以防 f 内 append
// 越写到 src 后续字节)。f 的返回值会被立即拷进结果缓冲, 可复用。
// 无匹配时【直接返回原 src 切片, 零分配】(不得改写); 结果为空返回 nil (同 stdlib)。
func (re *Regexp) ReplaceAllFunc(src []byte, f func([]byte) []byte) []byte {
	flat, count := re.matchAllFlat(bytesStr(src), len(src)+1) // len+1 = 最大可能匹配数, 即全部
	runtime.KeepAlive(src)
	if count == 0 {
		if len(src) == 0 {
			return nil
		}
		return src
	}
	per := 2 * (re.numSubexp + 1)
	buf := make([]byte, 0, len(src))
	lastMatchEnd := 0
	for k := 0; k < count; k++ {
		m0, m1 := flat[k*per], flat[k*per+1] // 只需 group0 (整体匹配字节)
		buf = append(buf, src[lastMatchEnd:m0]...)
		buf = append(buf, f(src[m0:m1:m1])...)
		lastMatchEnd = m1
	}
	buf = append(buf, src[lastMatchEnd:]...)
	if len(buf) == 0 {
		return nil // 对齐 stdlib: 结果为空 → nil
	}
	return buf
}

// FindReplaceWithinBytes 是 FindReplaceWithin 的 []byte 门面 (同一次 cgo 内核): 等价于
// find.ReplaceAllFunc(src, func(m []byte) []byte { return strip.ReplaceAll(m, repl) }),
// 但外层逐处匹配循环 + 每处命中段内的 strip 替换整体下沉 C++, 全程一次 cgo、Go 侧零 per-match 分配。
//
// repl 是 RE2 重写串 (捕获组引用用 \1..\9), 与 FindReplaceWithin 一致 —— 注意不同于 ReplaceAll 的纯字面 repl。
// 惰性物化同 ReplaceAll: 逐字节无改动时直接返回原 src 切片 (零分配, 不得改写), 结果为空返回 nil。
func (find *Regexp) FindReplaceWithinBytes(strip *Regexp, src, repl []byte) []byte {
	out, changed := find.findReplaceWithinRaw(strip, bytesStr(src), bytesStr(repl))
	runtime.KeepAlive(src)
	runtime.KeepAlive(repl)
	if !changed {
		if len(src) == 0 {
			return nil
		}
		return src
	}
	if len(out) == 0 {
		return nil
	}
	return []byte(out)
}
