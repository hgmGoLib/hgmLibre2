// reverse.go — 反着扫: 让 DFA 从正文【末尾往前】走原始 buffer。
//
// 【为什么需要它】
// `S B{m,n} L` 这种【起始类窄于重复类】的计数重复 —— 典型如 `[A-Za-z][A-Za-z0-9]{2,19}key` ——
// 正向 DFA 的一个状态要记住"当前活跃的起点集合"。起始类 S 严格窄于重复类 B 时, 这个集合不再是
// 一段连续的后缀 (k+1 种), 而可以是任意子集 (2^k 种), 于是状态数对界 n 指数增长。
//
// 这不是实现问题: `(a|b)*a(a|b)^k` 在 Myhill-Nerode 意义上就需要 2^k 个状态, 【任何】保语言的
// 改写都消不掉。但同一条语言【反过来读】—— `(a|b)^k a(a|b)*` —— 只要 k+2 个状态。
// 所以真正有效的一招不是改正则, 是改方向。
//
// 【为什么由库来做, 而不是调用方自己反转】
// 调用方自己把 pattern 和正文都按字节反转能凑合出效果, 但有三个坑:
//   - 字节反转会把多字节 UTF-8 拆散 (rune 的字节序列反了就不是那个 rune);
//   - pattern 的反转要正确处理 ^ $ \b 与所有 concat 的嵌套, 自己写的反转器只能保证"存在性等价";
//   - 正文要多复制一份。
//
// RE2 的编译器【本来就会】编反向程序 (它内部用反向 DFA 找匹配左端), concat 反序、^/$ 对调、
// 多字节 rune 的字节序列反编、\b 不变, 全都是现成的。本文件只是把这条既有能力接出来:
// 程序反着跑, 正文【原封不动】, 一个字节都不复制。
//
// 而且它比手写反转【更省】, 不只是更省事: RE2 的 Simplify 把 `x{2,19}` 展开成"必需拷贝在前、
// 可选嵌套在后", 编译器反序之后可选嵌套跑到了读取顺序的【前面】—— 各个起点的活跃集合于是
// 互相嵌套 (只取最外层) 而不构成任意子集, 状态数不炸。手写反转 pattern 文本再正向编,
// 必需拷贝仍在读取顺序的前面, 照炸不误。实测同一条语言同一串字节: 库的反向 17 个状态,
// 手写反转 25247 个 (reverse_test.go 的 TestReverseIsNotHandRolledTextReversal)。
//
// 【怎么用: 正反是两个对象】
// 单条走 CompileReverse (类型 RegexpReverse), 整表走 NewRegexpSetReverseMaxMem —— 都是独立对象,
// 不是正向对象上的一个开关。一条 pattern 的两个方向本来就是两套程序、两份 DFA 状态缓存,
// 而方向是每条 pattern 各自的决定, 所以哪条走哪个方向, 由调用方建对象的时候定死。
//
// 【语义边界 —— 只回答"命中没有 / 哪几条命中", 不回答"在哪"】
// 不提供 Find 系列, 也暂不打算补: 反向搜索只走到匹配的【左端】就停, 拿不到右端, 而且它先撞上的
// 是正文里靠后的那处匹配 —— 真做 Find 只能是 rightmost 语义, 与正向的 leftmost-first 是两套东西。
// 需要位置的调用方: 先用反向这道便宜的门筛, 极少数命中的再走一次正向 FindStringIndex,
// 位置语义仍然是正向那套。命中【与否】两个方向逐字相同 —— 这是"同一条语言换个方向读",
// 不是近似, 不缩语义。
//
// 【什么时候该用】
//   - 该用: pattern 里有起始类窄于重复类的计数重复, 且只需要"命中没有"。
//   - 不该用: 需要位置; 或 pattern 反过来才是那个坏形状 —— 方向是【每条 pattern 各自】的决定,
//     不是全局开关。镜像的一对: `(?s).{20}key` 正向 21 个状态 / 反向 1 个, `key(?s).{20}` 反过来
//     (reverse_test.go 的 TestReverseDirectionIsPerPattern 钉着这两个数)。
//     哪个方向便宜, 拿真语料量一遍 Flushes/StatesEnd 就知道 (见 RegexpReverse.MatchStats)。
//   - 单条 RegexpReverse 的路径【一定走 DFA】, 不走 RE2 对短文本的 bitstate/onepass 快路径,
//     所以小正文上它不一定比正向 Regexp.MatchString 快。它换的是状态数, 不是常数。
package hgmLibre2

/*
#include "cre2.h"
*/
import "C"

import (
	"runtime"
)

// RegexpReverse 是【反着扫】的正则对象 —— 与正向的 Regexp 分开的一个类型, 各编各的。
//
// 【为什么是两个对象, 不是一个对象上的两个方法】
// 一条 pattern 的两个方向是【两套程序、两份 DFA 状态缓存】(缓存挂在 re2::Prog 上, 反向那份是
// Regexp::CompileToReverseProg 单独编的)。方向是每条 pattern 各自的决定, 且一条 pattern 通常只
// 走一个方向 —— 拆成两个对象, 调用方一眼就知道自己手里这条走的是哪个方向, 内存也是一条一份,
// 不会出现"一个对象悄悄挂了两份缓存"。要两个方向就编两个对象。
//
// 【只回答"有没有匹配", 不回答"在哪"】
// 没有 Find 系列, 现在也不打算补: 反向扫天然只走到匹配的【左端】就停, 拿不到右端, 而且它先撞上的
// 是正文里【靠后】的那处匹配 —— 真要做 Find, 语义只能是 rightmost 一路, 与正向 Regexp 的
// leftmost-first 不是一回事, 徒增两套语义。需要位置的调用方: 拿这个当便宜的门筛一道,
// 极少数命中的再走一次正向 Regexp 的 FindStringIndex, 位置语义仍然是正向那套。
type RegexpReverse struct {
	re *Regexp
}

// CompileReverse 编译一条【反向】正则 (内存预算 = 默认 8MB; 要自己定预算用 CompileReverseMaxMem)。
// 编译错误返回 error (不 panic) —— 错误判定与 Compile 完全一样, 反向程序本身是首次扫描时才惰性编的。
func CompileReverse(pattern string) (*RegexpReverse, error) {
	return CompileReverseMaxMem(pattern, 0)
}

// CompileReverseMaxMem 同 CompileReverse, 但显式指定内存预算 maxMem (字节; <=0 = 默认 8MB)。
// 含义同 CompileMaxMem: 编译期指令上限 + 运行期 DFA 状态缓存额度。反着扫的意义正是把状态数从
// 指数塌回线性, 所以这里通常【不需要】调大预算 —— 先按默认量一遍 MatchStats 的 Flushes 再说。
func CompileReverseMaxMem(pattern string, maxMem int64) (*RegexpReverse, error) {
	re, err := CompileMaxMem(pattern, maxMem)
	if err != nil {
		return nil, err
	}
	return &RegexpReverse{re: re}, nil
}

// MustCompileReverse 同 CompileReverse, 失败 panic。
func MustCompileReverse(pattern string) *RegexpReverse {
	rr, err := CompileReverse(pattern)
	if err != nil {
		panic(`re2native: CompileReverse(` + pattern + `): ` + err.Error())
	}
	return rr
}

// String 返回编译时的源 pattern (不是反转后的文本 —— 反的是程序, 不是 pattern 文本)。
func (rr *RegexpReverse) String() string { return rr.re.String() }

// MaxMem 返回实际生效的内存预算 (字节)。
func (rr *RegexpReverse) MaxMem() int64 { return rr.re.MaxMem() }

// FreeC 立即释放原生资源, 语义与 (*Regexp).FreeC 一致 (非线程安全, 释放后不可再用)。
func (rr *RegexpReverse) FreeC() { rr.re.FreeC() }

// MatchString 报告 s 是否含任意匹配 (非锚定) —— 命中与否与正向 Regexp.MatchString 逐字相同,
// 只是 DFA 从正文末尾往前走【原始 buffer】(不反转正文, 不复制正文)。
//
// 反向程序在首次调用时惰性编出来 (线程安全, 预算用这条 pattern 的 MaxMem)。
// 万一反向程序编不出来 / 反向 DFA 中途放弃, 自动退回一次正向匹配: 答案永远正确, 只是那次没省到
// 状态 (退回走的是这个对象内部自己的正向程序, 也只有退回时才会建正向那份缓存)。
// 想知道有没有退回过, 用 MatchStats 看 FellBack。
func (rr *RegexpReverse) MatchString(s string) bool {
	re := rr.re
	if len(s) > maxCInt {
		return false
	}
	r := C.cre2_partial_match_reverse(re.h, strBytePtr(s), C.int(len(s)), 0)
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	return r.Matched != 0
}

// Match 同 MatchString, 但正文是 []byte (零拷贝)。
func (rr *RegexpReverse) Match(b []byte) bool {
	ok := rr.MatchString(bytesStr(b))
	runtime.KeepAlive(b)
	return ok
}

// MatchStats 同 MatchString, 外加把【这一次扫描】的 DFA 计数写进 st (st 可为 nil)。
// st 不必预先清零。热路径上不想要这份开销就用 MatchString —— 不传 st 时 C 侧完全不统计。
//
// 这是标定"该正着扫还是反着扫"的量器: 同一条 pattern 编一个 Regexp 和一个 RegexpReverse,
// 拿同一批真语料各跑一遍, 比 Flushes (>0 = 在悬崖上) 与 StatesEnd (缓存里堆了多少状态),
// 小的那个方向就是这条 pattern 该留下的那个对象。
func (rr *RegexpReverse) MatchStats(s string, st *ScanStats) bool {
	if st == nil {
		return rr.MatchString(s)
	}
	re := rr.re
	*st = ScanStats{}
	if len(s) > maxCInt {
		return false
	}
	r := C.cre2_partial_match_reverse(re.h, strBytePtr(s), C.int(len(s)), 1)
	runtime.KeepAlive(s)
	runtime.KeepAlive(re)
	st.Flushes = int64(r.Stats.Flushes)
	st.Grows = int64(r.Stats.Grows)
	st.StatesBuilt = int64(r.Stats.StatesBuilt)
	st.Bytes = int64(r.Stats.Bytes)
	st.StatesEnd = int64(r.Stats.StatesEnd)
	st.StateBudget = int64(r.Stats.StateBudget)
	st.MemLeft = int64(r.Stats.MemLeft)
	st.FellBack = r.FellBack != 0
	return r.Matched != 0
}

// NewRegexpSetReverseMaxMem 同 NewRegexpSetMaxMem, 但整个 set 反向编译: Match 从正文末尾
// 往前扫【原始 buffer】(不反转正文, 不复制正文)。命中集与正向逐位相同, 仍然只回答"哪几条命中"。
// maxMem 的含义与 NewRegexpSetMaxMem 完全一样 (<=0 = RE2 默认 8MB) —— 反向 set 只有这一个
// 构造函数: 会想反着扫的表通常就是正向撞过预算的那张, 建它的时候顺手把预算定了。
//
// ⚠ 方向是【整个 set 一个】的选择: 一条 pattern 反着便宜不等于另一条也便宜。
// 实践做法是把表按方向拆成两个 set, 各扫一遍 —— 正文过两遍 DFA 仍然远比一张表在悬崖上跑便宜
// (Match 的两份结果按下标并集即可; 注意 Match 返回的下标【无序】, 要比对得先排)。
// 拆之前先量: 每条 pattern 各建一个单条的正向 set 和反向 set, 用同一批真语料跑一遍比
// MemInfo().States, 小的那边就是它该去的那一组。
func NewRegexpSetReverseMaxMem(patterns []string, maxMem int64) (*RegexpSet, error) {
	return newRegexpSet(patterns, maxMem, true)
}

// Reverse 报告这个 set 是不是反向编译的。
func (s *RegexpSet) Reverse() bool {
	rev := C.cre2_set_reversed(s.h) != 0
	runtime.KeepAlive(s)
	return rev
}
