// patlen.go —— 每条 pattern 的【匹配字节长度区间】(min, max), 建集期算一次。
//
// 用来干什么: NewMatchScanner 拿到"这条 pattern 的匹配在第几字节结束"之后, 还得知道它从哪开始。
// 怎么找开始, 完全由这个区间决定, 三档差着数量级:
//
//	min == max        定长 (NRIC 9 字节 · 身份证 18 字节 · 邮编…): start = end - min, 一句减法,
//	                  【根本不进正则引擎】。
//	max  > 0          变长有上限 (\p{Han}{2,4} ≤ 12 字节): 从 end 往回最多看 max 字节, 常数代价。
//	max  < 0          没上限 ([a-z ]{4,} · .* · {n,}): 回看距离 = 正文长度, 这条不划算,
//	                  交给调用方走老路 (那条 pattern 自己扫一遍全文)。
//
// 🔴 为什么用 Go 的 regexp/syntax 解析而不是问 RE2: RE2 的 Regexp 树上没有这个量, 要么改
//    native 加一趟递归、要么在 Go 这侧解析。两边都是同一套语法 (Go 的 regexp 本来就是 RE2),
//    而这只在【建集期】跑一次 (155 条 < 1ms), 所以选了不动 native 的那条。
//    解析不出来 (RE2 支持而 Go 不支持的写法) 一律【当没上限】—— 保守方向: 只会让这条落回老路,
//    不会给出错误的 start。
package hgmLibre2

import (
	"regexp/syntax"
	"unicode"
	"unicode/utf8"
)

// PatLenUnbounded 是 max 的"没有上限"取值。
const PatLenUnbounded = -1

// PatternLenRange 算一条 pattern 的匹配字节长度区间。max = PatLenUnbounded 表示没有上限。
// pattern 解析不了时返回 (0, PatLenUnbounded) —— 与"没上限"同一档, 调用方照兜底路走即可。
func PatternLenRange(pattern string) (min, max int) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return 0, PatLenUnbounded
	}
	return lenRangeOf(re) // 不 Simplify: {1000} 那种会被展开成一千个节点, 而 OpRepeat 这里本来就直接算
}

// PatternLenRange 返回集合里第 i 条的长度区间, 越界返回 (0, PatLenUnbounded)。
// 结果在建集期算好存着, 这里只是查表。
func (s *RegexpSet) PatternLenRange(i int) (min, max int) {
	if i < 0 || i >= len(s.lens) {
		return 0, PatLenUnbounded
	}
	l := s.lens[i]
	return int(l.min), int(l.max)
}

// patLen_t 是存在 set 上的那份表。int32 够用 —— 长度上限超过 2GiB 的 pattern 早就是"没上限"。
type patLen_t struct {
	min, max int32
}

func buildPatLens(patterns []string) []patLen_t {
	out := make([]patLen_t, len(patterns))
	for i, p := range patterns {
		lo, hi := PatternLenRange(p)
		if hi > maxCInt || hi < 0 {
			hi = PatLenUnbounded
		}
		out[i] = patLen_t{min: int32(lo), max: int32(hi)}
	}
	return out
}

// lenRangeOf 递归。max < 0 一路向上传染 = 没上限。
func lenRangeOf(re *syntax.Regexp) (min, max int) {
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpEmptyMatch,
		syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0, 0

	case syntax.OpLiteral:
		// FoldCase 的字面量 Go 不展开成字符类, 得自己按大小写折叠的轨道取每个字符的最短/最长编码
		// (K 的轨道里有 3 字节的开尔文记号 U+212A, 差一个字节就够把 start 算错)。
		fold := re.Flags&syntax.FoldCase != 0
		for _, r := range re.Rune {
			lo, hi := runeByteRange(r, fold)
			min, max = min+lo, max+hi
		}
		return min, max

	case syntax.OpCharClass:
		if len(re.Rune) == 0 {
			return 0, 0 // 空类 = 不可能匹配; 长度上给个恒 0, 反正它一次也不会命中
		}
		min, max = 5, 0
		for i := 0; i+1 < len(re.Rune); i += 2 {
			lo := utf8.RuneLen(re.Rune[i])
			hi := utf8.RuneLen(re.Rune[i+1])
			// 区间跨编码长度分界时中间那些长度都在里面, 所以直接取两端的 min/max 即可。
			if lo < 1 || hi < 1 { // 代理区等非法码点, 保守放宽
				lo, hi = 1, 4
			}
			if lo < min {
				min = lo
			}
			if hi > max {
				max = hi
			}
		}
		return min, max

	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1, 4

	case syntax.OpCapture:
		return lenRangeOf(re.Sub[0])

	case syntax.OpStar:
		return 0, PatLenUnbounded

	case syntax.OpPlus:
		lo, _ := lenRangeOf(re.Sub[0])
		return lo, PatLenUnbounded

	case syntax.OpQuest:
		_, hi := lenRangeOf(re.Sub[0])
		return 0, hi

	case syntax.OpRepeat:
		lo, hi := lenRangeOf(re.Sub[0])
		if re.Max < 0 {
			return lo * re.Min, PatLenUnbounded
		}
		if hi < 0 {
			return lo * re.Min, PatLenUnbounded
		}
		return lo * re.Min, hi * re.Max

	case syntax.OpConcat:
		for _, sub := range re.Sub {
			lo, hi := lenRangeOf(sub)
			min += lo
			if max >= 0 {
				if hi < 0 {
					max = PatLenUnbounded
				} else {
					max += hi
				}
			}
		}
		return min, max

	case syntax.OpAlternate:
		min, max = -1, 0
		for _, sub := range re.Sub {
			lo, hi := lenRangeOf(sub)
			if min < 0 || lo < min {
				min = lo
			}
			if max >= 0 {
				if hi < 0 {
					max = PatLenUnbounded
				} else if hi > max {
					max = hi
				}
			}
		}
		if min < 0 {
			min = 0
		}
		return min, max
	}
	return 0, PatLenUnbounded
}

// runeByteRange 给一个字符的最短/最长 UTF-8 编码长度; fold=true 时把大小写折叠轨道上的
// 所有变体一起算进去。
func runeByteRange(r rune, fold bool) (min, max int) {
	n := utf8.RuneLen(r)
	if n < 1 {
		return 1, 4
	}
	min, max = n, n
	if !fold {
		return min, max
	}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		k := utf8.RuneLen(f)
		if k < 1 {
			return 1, 4
		}
		if k < min {
			min = k
		}
		if k > max {
			max = k
		}
	}
	return min, max
}
