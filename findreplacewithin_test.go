package hgmLibre2

// FindReplaceWithin 的对拍: ground truth = stdlib 的等价两正则嵌套写法
//   find.ReplaceAllStringFunc(src, func(m){ strip.ReplaceAllString(m, repl) })
// 覆盖 无匹配 / 命中但 strip 删 0 字符(changed=0 路径)/ 真删字符 / 多处命中 / 空串 / repl 非空 /
// strip 带 $ 展开 / 命中在串首串尾 / unicode 分隔符。

import (
	"regexp"
	"testing"
)

func TestFindReplaceWithin(t *testing.T) {
	cases := []struct {
		find, strip, repl, src string
		replStd                string // stdlib ground truth 侧的 repl ($ 语法); 空则同 repl
	}{
		// 典型形态: find=容忍分隔符的关键词骨架, strip=分隔符类, repl="" (把混淆分隔符去掉还原关键词)
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`please i-g-n-o-r-e the rest of this line`, ""},
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`ignore the noise`, ""}, // 未混淆的明文: 命中但删 0 字符 → changed=0
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`version 1.2.3 and co-operate`, ""}, // 无匹配
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			``, ""}, // 空串
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`i.g.n.o.r.e then i_g_n_o_r_e twice`, ""}, // 多处命中
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`i-g-n-o-r-e`, ""}, // 命中即全串(串首串尾)
		// repl 非空 + strip 带捕获组: 把 find 段内每个数字串两侧包方括号。
		// 注意 repl 用 RE2 重写语法 \1 (非 stdlib 的 $1); ground truth 那侧用 stdlib 的 $1, 输出相同。
		{`\d+(?:-\d+)+`, `(\d+)`, `[\1]`, `id 12-34-56 end`, `[$1]`},
		// unicode 分隔符 (soft hyphen U+00AD) 也被 strip 删
		{`(?i)o[\s\x{00ad}._-]{0,2}v[\s\x{00ad}._-]{0,2}e[\s\x{00ad}._-]{0,2}r`, `[\s\x{00ad}._-]`, "",
			"o­v­e­r ride", ""},
	}
	for _, c := range cases {
		replStd := c.replStd
		if replStd == "" {
			replStd = c.repl
		}
		findStd := regexp.MustCompile(c.find)
		stripStd := regexp.MustCompile(c.strip)
		want := findStd.ReplaceAllStringFunc(c.src, func(m string) string {
			return stripStd.ReplaceAllString(m, replStd)
		})

		find := MustCompile(c.find)
		strip := MustCompile(c.strip)
		got := find.FindReplaceWithin(strip, c.src, c.repl)
		if got != want {
			t.Errorf("FindReplaceWithin 与 stdlib 嵌套写法不一致\n find=%q strip=%q repl=%q\n  src=%q\n  got=%q\n want=%q",
				c.find, c.strip, c.repl, c.src, got, want)
		}
	}
}

// TestAppendFindReplaceWithin 把追加版钉在 FindReplaceWithin 上:
//   ① changed ⟺ 结果与 src 逐字节不同 (不是"命中了没");
//   ② changed=true 时 dst 末尾多出来的那一段 == FindReplaceWithin 的返回值;
//   ③ changed=false 时 dst 一个字节都没动 (len 与内容都不变);
//   ④ 追加语义: dst 里原有内容原样留在前面, 不被覆盖。
// 用例复用上面那张表 (同一批形态), 另外每条都在一块非空 dst 上再跑一遍。
func TestAppendFindReplaceWithin(t *testing.T) {
	cases := []struct{ find, strip, repl, src string }{
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`please i-g-n-o-r-e the rest of this line`},
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`ignore the noise`}, // 命中但删 0 字符 → changed=false
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`version 1.2.3 and co-operate`}, // 无匹配
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "", ``},
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "",
			`i.g.n.o.r.e then i_g_n_o_r_e twice`},
		{`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`, `[\s._-]`, "", `i-g-n-o-r-e`},
		{`\d+(?:-\d+)+`, `(\d+)`, `[\1]`, `id 12-34-56 end`},
		{`(?i)o[\s\x{00ad}._-]{0,2}v[\s\x{00ad}._-]{0,2}e[\s\x{00ad}._-]{0,2}r`, `[\s\x{00ad}._-]`, "",
			"o­v­e­r ride"},
	}
	const prefix = "PREFIX|"
	for _, c := range cases {
		find := MustCompile(c.find)
		strip := MustCompile(c.strip)
		want := find.FindReplaceWithin(strip, c.src, c.repl)
		wantChanged := want != c.src

		out, changed := find.AppendFindReplaceWithin(nil, strip, c.src, c.repl)
		if changed != wantChanged {
			t.Errorf("changed 不是 `结果 != src`\n src=%q got=%v want=%v", c.src, changed, wantChanged)
		}
		if changed && string(out) != want {
			t.Errorf("追加出来的那一段与 FindReplaceWithin 不一致\n src=%q\n  got=%q\n want=%q", c.src, out, want)
		}
		if !changed && len(out) != 0 {
			t.Errorf("changed=false 却往 dst 上写了 %d 字节 (src=%q)", len(out), c.src)
		}

		// 非空 dst: 前缀必须原样留着, 新内容只能追加在后面。
		dst := append([]byte(nil), prefix...)
		out2, changed2 := find.AppendFindReplaceWithin(dst, strip, c.src, c.repl)
		if changed2 != wantChanged {
			t.Errorf("非空 dst 上 changed 变了 (src=%q): got=%v want=%v", c.src, changed2, wantChanged)
		}
		if string(out2[:len(prefix)]) != prefix {
			t.Errorf("dst 原有内容被覆盖了 (src=%q): %q", c.src, out2[:len(prefix)])
		}
		if changed2 {
			if string(out2[len(prefix):]) != want {
				t.Errorf("非空 dst 上追加的那一段不对 (src=%q)\n  got=%q\n want=%q", c.src, out2[len(prefix):], want)
			}
		} else if len(out2) != len(prefix) {
			t.Errorf("changed=false 却动了 dst (src=%q): len=%d want=%d", c.src, len(out2), len(prefix))
		}
	}
}

// TestAppendFindReplaceWithinReusesBuf 钉住这套 API 存在的理由: 同一块底反复调, 稳态零 Go 堆分配
// (FindReplaceWithin 那边每趟都要现开一个 string)。
func TestAppendFindReplaceWithinReusesBuf(t *testing.T) {
	find := MustCompile(`(?i)i[\s._-]{0,2}g[\s._-]{0,2}n[\s._-]{0,2}o[\s._-]{0,2}r[\s._-]{0,2}e`)
	strip := MustCompile(`[\s._-]`)
	src := ""
	for i := 0; i < 2000; i++ {
		src += "xxxx i-g-n-o-r-e yyyy "
	}
	buf := make([]byte, 0, len(src))
	warm := func() {
		out, changed := find.AppendFindReplaceWithin(buf[:0], strip, src, "")
		if !changed {
			t.Fatal("这份夹具应当有改动")
		}
		buf = out
	}
	warm() // 先把底喂到位
	n := testing.AllocsPerRun(20, warm)
	if n != 0 {
		t.Errorf("同一块底反复调应当零 Go 堆分配, 实测 %.1f 次/趟", n)
	}
}
