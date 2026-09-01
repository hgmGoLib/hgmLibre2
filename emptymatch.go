// emptymatch.go —— 全库一条规矩: 【能匹配空串的 pattern 一律拒绝, 在编译入口当场报错】。
//
// 为什么: "任何字符串里都有空串", 所以一条能匹配空串的 pattern 在任何正文上都必然命中,
// 拿它当正则没有信息量 —— 这是 pattern 写错了, 不是引擎该伺候的用法。
// 本库过去为这种写法养着一整套逃生通道 (MatchScanner 的 unsupported 名单 · SetModes 的
// boolOnly 降级 · Re2SetFrel 的逐条校验 · asc 那边的 bodyGateSpanShapeOK 静态排除),
// 通道本身就是一批几乎跑不到、因而基本没测过的分支。2026-09-01 决定整条拆掉:
// 编译入口一道门, 后面所有代码都可以【无条件】假设"每个匹配至少 1 字节"。
//
// 调用方怎么改: 把可空的量词改成不可空即可, 一行的事, 语义还是 RE2 的语义 ——
//
//	a*        -> a+
//	(?m)^[ \t]*$   -> (?m)^[ \t]+$    (要"空行"就别用正则, 用 len(line)==0)
//	x{0,3}    -> x{1,3}
//	(a|)      -> a
//
// 🔴 为什么不选"照编, 但引擎里不产出零长匹配": 后置过滤 != 引擎内禁止零长。默认口径是
//    leftmost-first, `a*|b` 撞 "b" 时引擎在位置 0 先试 a*、匹配到空串就收工给 [0,0);
//    后置一滤就变成"无匹配", 而正确的非空答案是 [0,1)="b"。要给对答案就得改 program,
//    从此本库的匹配语义和 RE2 上游分家, VENDOR.txt 那套"从上游摘修复"的做法一路更难。
//
// 🔴 边角 (故意留的口子): 判断走 Go 的 regexp/syntax (理由见 patlen.go 文件头)。
//    极少数 RE2 认而 Go 的解析器不认的写法, 这里【解析不出来就放行】—— 宁可漏一条,
//    也不能把本来能用的 pattern 拒掉。真是坏 pattern, 后面 RE2 自己的编译会报错。
package hgmLibre2

import (
	"errors"
	"regexp/syntax"
)

// checkNoEmptyMatch 是全库编译入口共用的那道门。pattern 能匹配空串就返回 error。
// where 只用来拼错误文案 (如 "set pattern at index 3")。
func checkNoEmptyMatch(where string, pattern string) error {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil // 解析不出来就放行, 见文件头最后一条
	}
	if min, _ := lenRangeOf(re); min > 0 {
		return nil
	}
	return errors.New("re2native: " + where + " 能匹配空串, 本库一律拒绝 (任何正文里都有空串, " +
		"这条 pattern 必然处处命中): " + pattern + " —— 把可空的量词改成不可空即可 (a* -> a+, " +
		"x{0,3} -> x{1,3}, (a|) -> a)")
}
