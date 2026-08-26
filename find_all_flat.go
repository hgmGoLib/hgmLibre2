// find_all_flat.go — FindAllStringIndex 的【无中间结构】变体: 把全部匹配的 [start,end) 直接
// 追加进调用方的可复用 []int, 不产 [][]int 外壳、也不产一次性的 flat 表。
//
// 动机: FindAllStringIndex 每次调用要产两笔一次性分配 ——
//   ① matchAllFlat 的 flat []int (每匹配 2*nmatch 个 int, 即便调用方只要 group0);
//   ② res [][]int 外壳 (每匹配一个 24 字节切片头, 指回 flat)。
// 在"大正文 + 高命中数"的热路径上这两笔按匹配数线性放大: 实测 16MiB body 上 19 万处命中
// = 40 字节/命中 = 7.6MB 一次调用, 而调用方拿到 [][]int 之后无非是顺序遍历 loc[0]/loc[1]。
//
// 本变体只回填 group0 (nmatch=1, 同 FindStringIndex_ctx): C 侧的 vector<StringPiece> 也随之
// 从 numSubexp+1 缩到 1, 逐处匹配整循环仍留在 C 内 (一次 cgo 过境, 不是每处一次)。
// 结果写成 [s0,e0,s1,e1,…] 追加进 dst —— 调用方传 buf[:0] 即可跨调用复用同一块内存,
// 稳态零分配。语义与 FindAllStringIndex 逐处相同 (推进/空匹配去重都在同一段 C 代码里),
// 对拍门见 find_all_flat_test.go。
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

// 🔴 待删除 (2026-08-26) —— 新代码一律改用 (*Regexp).StepAllStringIndex (match_step.go)。
//
// 理由: 本方法只干掉了「Go 侧那笔 flat + [][]int 外壳」, 干不掉 C 侧 cre2_match_all 的
// std::vector 累积表 + malloc (峰值是整张命中表的两份, 纯 RSS, Go profile 上看不见);
// 而且它省下的累计分配是拿 live-max 换的 —— dst 这块复用缓冲会涨到历史最大命中数就再也不缩,
// 挂在 plan/pool 上 × 并发度常驻。step 形态两头都没有: C 直接写进 Go 缓冲, 缓冲固定一批大小。
// StepAllStringIndex 与本方法语义逐处相同 (同一段 C 循环), 对拍见 match_step_test.go 的
// TestStepAllStringIndex_VsFindAll —— 它每轮都拿本方法的结果再对一遍。
//
// 现在还留着只是让 7 个存量调用点能编过; 调用点全部换完、性能确认之后, 本文件连同
// cre2_match_all / cre2_match_all_r 一起删。
//
// AppendAllStringIndexFlat 把 re 在 s 上前 n 处匹配的 [start,end) 追加进 dst, 返回追加后的切片。
// n < 0 = 全部。无匹配时原样返回 dst (一个元素都不追加, 同 FindAllStringIndex 返 nil 的语义)。
//
// 追加的元素成对出现: 第 k 处匹配是 dst[2k], dst[2k+1]。要复用缓冲就传 buf[:0]。
// 与 FindAllStringIndex 的差别只有"结果放在哪里": 匹配集合、顺序、空匹配处理逐处一致。
// 子组不回填 —— 要子组请用 FindAllStringSubmatchIndex。
func (re *Regexp) AppendAllStringIndexFlat(dst []int, s string, n int) []int {
	if len(s) > maxCInt { // 超 C.int 的输入直接当无匹配 (同 matchAllFlat 守卫)
		return dst
	}
	if n < 0 {
		n = len(s) + 1
	}
	tp := strBytePtr(s)
	// nmatch=1: 只要 group0. 逐处匹配的循环在 C 内 (零 cgo/处), 与 matchAllFlat 同一个入口。
	// 🔴 用【按值返回】的 _r 孪生, 不用出参版: 出参版要把 &out/&cnt 两个 Go 指针交给 C,
	// 逃逸分析据此每次调用把这两个局部变量搬上堆 —— 那正好抵消掉本方法唯一的卖点。
	r := C.cre2_match_all_r(re.h, tp, C.int(len(s)), 1, C.int(n))
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	if r.rc <= 0 || r.out == nil || r.nmatches == 0 {
		return dst // 无匹配 (rc==0) 或 malloc 失败 (rc<0): 当作无匹配, 同 matchAllFlat
	}
	total := int(r.nmatches) * 2
	cflat := unsafe.Slice(r.out, total)
	if cap(dst)-len(dst) < total { // 一次开够, 免 append 翻倍阶梯
		grown := make([]int, len(dst), len(dst)+total)
		copy(grown, dst)
		dst = grown
	}
	for i := 0; i < total; i++ {
		dst = append(dst, int(cflat[i]))
	}
	C.free(unsafe.Pointer(r.out))
	return dst
}
