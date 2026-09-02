// emptymatch.go —— 全库一条规矩: 【能匹配空串的 pattern 一律拒绝, 在编译入口当场报错】。
//
// 为什么: "任何字符串里都有空串", 所以一条能匹配空串的 pattern 在任何正文上都必然命中,
// 拿它当正则没有信息量 —— 这是 pattern 写错了, 不是引擎该伺候的用法。
// 本库过去为这种写法养着一整套逃生通道 (Re2Set_fll_t 的 unsupported 名单 · SetModes 的
// boolOnly 降级 · Re2Set_frel_t 的逐条校验 · 调用方那边再来一道静态排除),
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
// 🔴 判定【在 C 侧用 RE2 自己的解析器做】(cre2_emptymatch.cpp), 不在 Go 侧用 regexp/syntax。
//    这一条是 2026-09-01 修掉的一个 bug: 两个解析器不是同一个语言, 老实现"Go 解析不了就
//    放行"的口子被 PerlX 的 \C (匹配任意一个字节, RE2 认 · Go 报 invalid escape sequence)
//    整条绕过, `\C*` 照常编出来并产出零长匹配。换成 RE2 自己的解析器之后没有这个口子:
//    RE2 编得出来的 pattern, 这道门一定看得懂。
//
// 🔴 判据是【结构上能不能吃 0 个字节走完】, 不是"在空文本上能不能命中"。零宽断言
//    (^ $ \b \B \A \z) 一律算可空 —— \b 在空文本上并不命中, 可它在 "ab" 上照样产出零长
//    匹配。所以运行期拿空串探一次【不能】当判据, 那会漏掉一整族。
//
// 🔴 为什么不选"照编, 但引擎里不产出零长匹配": 后置过滤 != 引擎内禁止零长。默认口径是
//    leftmost-first, `a*|b` 撞 "b" 时引擎在位置 0 先试 a*、匹配到空串就收工给 [0,0);
//    后置一滤就变成"无匹配", 而正确的非空答案是 [0,1)="b"。要给对答案就得改 program,
//    从此本库的匹配语义和 RE2 上游分家, VENDOR.txt 那套"从上游摘修复"的做法一路更难。
package hgmLibre2

/*
#include <stdlib.h>
#include "cre2.h"
*/
import "C"

import (
	"errors"
	"runtime"
)

// checkNoEmptyMatch 是全库编译入口共用的那道门。pattern 能匹配空串就返回 error。
// where 只用来拼错误文案 (如 "set pattern at index 3")。
//
// RE2 解析不了的 pattern 这里【放行】(返回 nil): 那不是"可空", 是"这条 pattern 本身坏了",
// 该由紧接着的 cre2_new / cre2_set_add 去报 RE2 自己那条更准的错, 不该被这道门截胡。
func checkNoEmptyMatch(where string, pattern string) error {
	r := C.cre2_pattern_can_match_empty(strBytePtr(pattern), C.int(len(pattern)))
	runtime.KeepAlive(pattern)
	if r != 1 {
		return nil
	}
	return errors.New("re2native: " + where + " 能匹配空串, 本库一律拒绝 (任何正文里都有空串, " +
		"这条 pattern 必然处处命中): " + pattern + " —— 把可空的量词改成不可空即可 (a* -> a+, " +
		"x{0,3} -> x{1,3}, (a|) -> a)")
}
