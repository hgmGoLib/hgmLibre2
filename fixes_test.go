package hgmLibre2

// fixes_test.go — 锁定本轮针对 review 极端条件做的修复:
//   1) 捕获组名不再 256 字节截断
//   2) FreeC 释放 native 资源 + 幂等
//   3) 超长 pattern/输入的 C.int 溢出守卫

import (
	"regexp"
	"strings"
	"testing"
)

// 组名 >256 字节: 修复后与 stdlib 一致, 不截断.
func TestFix_LongGroupNameNoTruncation(t *testing.T) {
	for _, ln := range []int{255, 256, 257, 300, 1000} {
		name := strings.Repeat("x", ln)
		pat := `(?P<` + name + `>a)`
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)
		gotMine := mine.SubexpNames()
		gotStd := std.SubexpNames()
		if len(gotMine) < 2 || len(gotMine[1]) != ln {
			t.Errorf("len=%d: mine 组名长度=%d, 期望 %d (被截断?)", ln, len(gotMine[1]), ln)
		}
		eq(t, gotMine, gotStd, "SubexpNames 超长组名 len="+strings.Repeat("x", 0)+itoa(ln))
		// ${name} 展开也应取到完整组名.
		eq(t, mine.ReplaceAllString("a", "${"+name+"}"), std.ReplaceAllString("a", "${"+name+"}"),
			"ReplaceAllString ${超长名} len="+itoa(ln))
	}
}

// FreeC: 释放后幂等, 多次调用安全; 未调 FreeC 的实例靠 finalizer 兜底(此处只验证不 panic).
func TestFix_FreeC(t *testing.T) {
	re := MustCompile(`(?P<k>\w+)=(\d+)`)
	// 用一下确认正常.
	if got := re.FindStringSubmatch("port=8080"); len(got) != 3 || got[0] != "port=8080" {
		t.Fatalf("pre-free match wrong: %#v", got)
	}
	re.FreeC()
	re.FreeC() // 幂等: 第二次应直接返回, 不 double-free
	// 释放后不再调用任何匹配方法(那是 use-after-free, 文档已声明不防护).

	// 另开一个不调 FreeC, 让 GC/finalizer 兜底, 仅确保不 panic.
	_ = MustCompile(`abc`)
}

// 溢出守卫: 正常长度不受影响(走真实匹配路径). 真正 >2GiB 无法在单测分配, 仅验证守卫不误伤.
func TestFix_OverflowGuardNoFalsePositive(t *testing.T) {
	re := MustCompile(`a.`)
	in := strings.Repeat("ab", 1<<14)
	if got, want := len(re.FindAllStringIndex(in, -1)), 1<<14; got != want {
		t.Errorf("正常长度被守卫误伤: got=%d want=%d", got, want)
	}
	if !re.MatchString("ab") {
		t.Error("MatchString 正常输入被守卫误伤")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
