// prefilter.go — Prefilter: RE2 自己的「必需字面量」推导 (FilteredRE2 / PrefilterTree 的门面)。
//
// 回答三个问题:
//
//	Atoms()              这批 pattern 想命中, 正文里【必须】先出现哪些字面量?
//	                     (已小写化、去重 —— 拿它们去正文里找的时候要么大小写不敏感, 要么先折成小写)
//	Potentials(found)    正文里找到了这几个原子, 那么还有哪几条 pattern 【可能】命中?
//	                     没进这个名单的 pattern 【保证】不命中, 可以整条跳过。
//	Unfiltered()         哪几条 pattern 【没有】必需字面量, 因而任何前置粗筛都筛不掉?
//
// ── 第三个问题才是接这套出来的动机 ────────────────────────────────────────────
//
// 「先用一道便宜的字面量门挡掉大多数正文, 剩下的才进大表」是本库文档里唯一能抬高吞吐上限的方向
// (doc/set性能优化经验.txt §4 E)。但这个方向有个天花板: 有些 pattern 根本没有必需字面量 ——
// 纯字符类驱动的, 形如 `[A-Za-z0-9+/=_-]{20,}` 或 `(?-i:\([A-Z]{2,5}\))` —— 它们无论正文长什么样
// 都得跑。这批的规模直接决定了粗筛能省下多少, 所以做粗筛之前必须先量它。
//
// 🔴 这个数【只有 RE2 自己的 prefilter 算得准】。手写一个"从 pattern 源串里抠字面量"的抽取器
// 会在 `(?:foo|[A-Z]{5})` 上答错: 它含字面量 foo, 但整条【不可过滤】—— 另一支不需要 foo,
// 所以 foo 不出现的正文里它照样可能命中。AND-OR 树上的这类推理没法凭直觉做对。
//
// ── 与 RegexpSet 的分工 ───────────────────────────────────────────────────────
//
// RegexpSet 是"把 N 条编进一个 DFA, 一遍扫回答哪几条命中"—— 它自己就是答案。
// Prefilter 不做匹配, 它只出【筛子】: 原子表给调用方拿去用自己的字符串匹配器 (AC 自动机 / memmem
// 都行) 找, 找完回来问还剩哪几条。两者是可以叠的: 先 Prefilter 缩小候选, 再拿缩小后的子集建 Set。
//
// 生命周期: 构建期一次 (每条 pattern 各编一个 RE2 + 推 AND-OR 树, 不便宜), 之后只读。
// AllPotentials 内部只读遍历自己的树, 并发安全。
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

// Prefilter 持有一棵编译好的 AND-OR 原子树 + 每条 pattern 各一个 RE2 对象。
type Prefilter struct {
	h     *C.cre2_prefilter
	atoms []string
	n     int // pattern 条数
}

// NewPrefilter 把 patterns 顺序喂进 FilteredRE2 并编译。
//
// minAtomLen 是原子的最短长度 (<=0 用 RE2 默认): 调大 ⇒ 原子更少更长 (匹配器更快, 但更多 pattern
// 掉进不可过滤集); 调小 ⇒ 筛得更细但原子表膨胀、短原子在任何正文里都到处是, 筛不掉东西。
// maxMem 是每条 pattern 各自那个 RE2 的预算 (<=0 = RE2 默认 8MB)。
//
// 任一条解析失败即返回 error —— 与 NewRegexpSet 一致: 静默丢掉一条会让 Potentials 的下标
// 与调用方的 patterns 下标错位, 那是最难查的一类错。
func NewPrefilter(patterns []string, minAtomLen int, maxMem int64) (*Prefilter, error) {
	h := C.cre2_prefilter_new(C.int(minAtomLen), C.int64_t(maxMem))
	if h == nil {
		return nil, errors.New("re2native: prefilter out of memory")
	}
	p := &Prefilter{h: h}
	for i, pat := range patterns {
		if len(pat) > maxCInt {
			C.cre2_prefilter_free(h)
			return nil, errors.New("re2native: prefilter pattern too large (>2GiB)")
		}
		id := int(C.cre2_prefilter_add(h, strBytePtr(pat), C.int(len(pat))))
		runtime.KeepAlive(pat)
		if id != i {
			C.cre2_prefilter_free(h)
			return nil, errors.New("re2native: prefilter bad pattern at index " + strconv.Itoa(i) + ": " + pat)
		}
		p.n++
	}
	nAtom := int(C.cre2_prefilter_compile(h))
	if nAtom < 0 {
		C.cre2_prefilter_free(h)
		return nil, errors.New("re2native: prefilter compile failed")
	}
	p.atoms = make([]string, nAtom)
	for i := 0; i < nAtom; i++ {
		var cp *C.char
		l := int(C.cre2_prefilter_atom(h, C.int(i), &cp))
		p.atoms[i] = C.GoStringN(cp, C.int(l))
	}
	runtime.SetFinalizer(p, func(x *Prefilter) { C.cre2_prefilter_free(x.h) })
	return p, nil
}

// Atoms 返回原子表 (已小写化、去重)。下标就是 Potentials 要的那个 atom 下标。
// 返回的是内部切片, 【不要改写】。
func (p *Prefilter) Atoms() []string { return p.atoms }

// GetPatternLen 返回 pattern 条数。
func (p *Prefilter) GetPatternLen() int { return p.n }

// Potentials 给定"正文里找到的原子下标"(升序不升序都行, 重复也无所谓), 返回还可能命中的
// pattern 下标 (升序)。没进名单的 pattern 保证不命中。
//
// 传 nil / 空切片 ⟹ 返回【不可过滤集】, 见 Unfiltered。
func (p *Prefilter) Potentials(atomIdx []int32) []int32 {
	out := make([]int32, p.n)
	var ap *C.int
	if len(atomIdx) > 0 {
		ap = (*C.int)(unsafe.Pointer(&atomIdx[0]))
	}
	n := int(C.cre2_prefilter_potentials(p.h, ap, C.int(len(atomIdx)),
		(*C.int)(unsafe.Pointer(&out[0])), C.int(p.n)))
	runtime.KeepAlive(atomIdx)
	runtime.KeepAlive(p)
	if n < 0 {
		n = 0
	}
	if n > p.n {
		n = p.n
	}
	return out[:n]
}

// Unfiltered 返回【一个原子都不需要】的那批 pattern 下标 —— 它们无论正文长什么样都得跑。
//
// 这个数是任何"前置字面量粗筛"方案的天花板: 筛得掉的那部分再便宜, 也省不掉这批的钱。
// 做粗筛之前先量它, 别先做完再发现天花板在 3%。
func (p *Prefilter) Unfiltered() []int32 { return p.Potentials(nil) }

// FreeC 显式释放 native 资源 (否则靠 finalizer)。释放后不得再调本对象任何方法。
func (p *Prefilter) FreeC() {
	if p.h != nil {
		runtime.SetFinalizer(p, nil)
		C.cre2_prefilter_free(p.h)
		p.h = nil
	}
}
