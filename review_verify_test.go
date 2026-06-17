package hgmLibre2

// review_verify_test.go — 验证一份外部 review 的各条断言.
// 不改实际代码, 只用差分测试/静态边界把每条断言坐实成 可观测的事实.
// 跑法: go test -run 'TestReview' -v  (每个子测试打印 mine vs stdlib 的实际结果)
//
// 设计: 凡 review 断言"应与 stdlib 一致"的, 用 eqReport 比对并把分歧 t.Logf 出来;
//       明确的 drop-in 语义分歧不让整个 suite 变红 (用 t.Logf 记录 FINDING), 便于一次跑完看全貌.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// reportDiff 比对 mine/std, 一致打印 OK, 不一致打印 FINDING(分歧) 但不 fail.
func reportDiff(t *testing.T, label string, mine, std any) bool {
	t.Helper()
	same := sprint(mine) == sprint(std)
	if same {
		t.Logf("[OK]      %s: mine==std = %s", label, sprint(mine))
	} else {
		t.Logf("[FINDING] %s: DIVERGES\n            mine = %s\n            std  = %s", label, sprint(mine), sprint(std))
	}
	return same
}

func sprint(v any) string {
	return strings.TrimSpace(stringify(v))
}

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return "<" + x + ">"
	case error:
		if x == nil {
			return "<nil-err>"
		}
		return "ERR(" + x.Error() + ")"
	default:
		return fmt.Sprintf("%#v", v)
	}
}

// review 点1: README 说 "leftmost-longest", 但默认 RE2(Perl mode)与 stdlib 都是 leftmost-first.
// (a|aa) 匹配 "aa" → leftmost-first 取 "a", leftmost-longest 取 "aa".
func TestReview_LeftmostFirst(t *testing.T) {
	pat, in := `(a|aa)`, `aa`
	std := regexp.MustCompile(pat)
	mine := MustCompile(pat)
	reportDiff(t, "FindString (a|aa) on aa", mine.FindString(in), std.FindString(in))
	if mine.FindString(in) == "a" {
		t.Logf("[CONFIRM] 引擎是 leftmost-first (取 'a'); README 写 'leftmost-longest' 与实现不符.")
	}
	if mine.FindString(in) == "aa" {
		t.Logf("[NOTE] 引擎确为 leftmost-longest, 与 stdlib 不一致 (stdlib=%q).", std.FindString(in))
	}
}

// review 点4: 捕获组名 256 字节硬截断 → 超长命名组 SubexpNames 与 stdlib 不等.
func TestReview_LongGroupNameTruncation(t *testing.T) {
	name := strings.Repeat("x", 300)
	pat := `(?P<` + name + `>a)`
	std, errStd := regexp.Compile(pat)
	mine, errMine := Compile(pat)
	t.Logf("compile: stdlib err=%v ; mine err=%v", errStd, errMine)
	if errStd != nil || errMine != nil {
		t.Logf("[NOTE] 一方拒绝编译超长组名, 无法对拍 SubexpNames; 见上.")
		return
	}
	mn := mine.SubexpNames()
	sn := std.SubexpNames()
	if len(mn) > 1 && len(sn) > 1 {
		t.Logf("name len: mine=%d std=%d (源 300)", len(mn[1]), len(sn[1]))
		reportDiff(t, "SubexpNames[1] 长度对齐", len(mn[1]), len(sn[1]))
		if len(mn[1]) == 256 && len(sn[1]) == 300 {
			t.Logf("[CONFIRM] 组名被截断到 256 字节, 与 stdlib(300) 分歧. (hgmLibre2.go nbuf[256])")
		}
	}
}

// review 建议: 重复命名组. stdlib 拒绝 (duplicate capture group name); 看 mine 是否同样拒绝.
func TestReview_DuplicateGroupNames(t *testing.T) {
	pat := `(?P<bob>a+)(?P<bob>b+)`
	_, errStd := regexp.Compile(pat)
	_, errMine := Compile(pat)
	t.Logf("stdlib err=%v ; mine err=%v", errStd, errMine)
	bothReject := errStd != nil && errMine != nil
	bothAccept := errStd == nil && errMine == nil
	if bothReject {
		t.Logf("[OK] 双方都拒绝重复命名组.")
	} else if bothAccept {
		t.Logf("[FINDING] 双方都接受重复命名组 (行为一致但与 stdlib 语义需确认).")
	} else {
		t.Logf("[FINDING] 编译行为分歧: stdlib 与 mine 对重复命名组处理不同 (drop-in 风险).")
	}
}

// review 建议: \C (匹配任意字节). stdlib regexp 明确不支持; 看 mine 是否拒绝.
func TestReview_BackslashC(t *testing.T) {
	pat := `\C`
	_, errStd := regexp.Compile(pat)
	_, errMine := Compile(pat)
	t.Logf("stdlib err=%v ; mine err=%v", errStd, errMine)
	switch {
	case errStd != nil && errMine != nil:
		t.Logf("[OK] 双方都拒绝 \\C.")
	case errStd != nil && errMine == nil:
		t.Logf("[FINDING] stdlib 拒绝 \\C 而 mine 接受 → drop-in 分歧 (RE2 引擎支持 \\C).")
	default:
		t.Logf("[NOTE] stdlib err=%v mine err=%v", errStd, errMine)
	}
}

// review 建议: 非法 UTF-8 输入的匹配一致性.
func TestReview_InvalidUTF8(t *testing.T) {
	in := string([]byte{0xff, 'a', 0xfe})
	for _, pat := range []string{`a`, `.`, `.+`, `\xff`, `[\x00-\xff]`} {
		std, errStd := regexp.Compile(pat)
		mine, errMine := Compile(pat)
		if errStd != nil || errMine != nil {
			t.Logf("pat=%q compile std=%v mine=%v (跳过)", pat, errStd, errMine)
			continue
		}
		reportDiff(t, "FindStringIndex pat="+pat+" on invalid-utf8", mine.FindStringIndex(in), std.FindStringIndex(in))
		reportDiff(t, "FindAllStringIndex pat="+pat+" on invalid-utf8", mine.FindAllStringIndex(in, -1), std.FindAllStringIndex(in, -1))
	}
}

// review 点5(静态边界辅证): 没有 len>MaxInt32 的守卫, 这里只确认正常长度路径下 helper 行为正常,
// 真正的 2GB 溢出无法在单测里实际分配, 仅作静态结论. 这里跑一个较大但安全的输入确保不崩.
func TestReview_LargeInputSmoke(t *testing.T) {
	in := strings.Repeat("ab", 1<<16) // 128KiB, 安全
	pat := `a.`
	std := regexp.MustCompile(pat)
	mine := MustCompile(pat)
	reportDiff(t, "large input FindAllStringIndex count",
		len(mine.FindAllStringIndex(in, -1)), len(std.FindAllStringIndex(in, -1)))
	t.Logf("[NOTE] 2GB 级溢出 (C.int 截断 len/pos) 无法单测复现, 属静态结论: hgmLibre2.go 把 len(s)/pos cast 成 C.int, cre2.cpp 用 int textlen/startpos, 无 len>MaxInt32 守卫.")
}
