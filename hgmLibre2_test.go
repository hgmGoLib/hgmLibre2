package hgmLibre2

// hgmLibre2 的每个方法都与 stdlib regexp 逐一对拍 (两者都是 RE2 语义, 结果须完全一致),
// 覆盖 9 个 Find/Replace 方法 + 边角 (空匹配/命名组/$展开/unicode/无匹配).
// 公开库, 只用 stdlib testing, 不引入任何外部依赖.

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"
)

// eq 用 reflect.DeepEqual 对拍 got/want, 不等就 t.Errorf 报出 msg.
func eq(t *testing.T, got, want any, msg string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s\n  got  = %#v\n  want = %#v", msg, got, want)
	}
}

// 通用正则样本: 覆盖 字面/(?i)/字符类/量词/词边界/unicode/多子组/命名组/空匹配/可选组/交替/锚定.
// 故意不引用任何业务检测规则 —— 这是公开库, 只测引擎行为本身.
var testPatterns = []string{
	`(?i)foo\d+bar`,
	`[a-z]+`,
	`\b\w{3}\b`,
	`(\w+)-(\w+)`,               // 多子组
	`(?P<key>\w+)=(?P<num>\d+)`, // 命名组
	`a*`,                        // 会产空匹配
	`(x)?(y)`,                   // 含可不参与的组
	`\s+`,                       // split-like
	`[0-9]+`,
	`\d{2}[-\x{2011}]\d{2}`, // 含 unicode 码点 (non-breaking hyphen)
	`(cat|dog|fish)`,        // 交替
	`^the\b`,                // 锚定
}

var testInputs = []string{
	"",
	"xx FOO123BAR yy foo9bar",
	"the quick brown fox jumps",
	"alpha-beta gamma-delta zeta",
	"k1=10 and k2=200 plus k3=3",
	"aaabbbaaa",
	"y xy yy xyxy",
	"  multiple   spaces\tand tabs ",
	"abc123def456",
	"12-34 56‑78 90",
	"a cat a dog and one fish here",
	"no match of note in this line",
}

// replTemplates: 喂给 ReplaceAllString 的替换串, 含字面/数字引用/命名引用/$$.
var replTemplates = []string{"", " ", "X", "$1", "${1}", "[$1-$2]", "$key:$num", "$$", "$0"}

func TestMatchesStdlib(t *testing.T) {
	for _, pat := range testPatterns {
		std, errStd := regexp.Compile(pat)
		mine, errMine := Compile(pat)
		if errStd != nil {
			t.Fatalf("stdlib compile failed: %s err=%v", pat, errStd)
		}
		if errMine != nil {
			t.Fatalf("hgmLibre2 compile failed: %s err=%v", pat, errMine)
		}

		// 元数据
		eq(t, mine.String(), std.String(), "String() pat="+pat)
		eq(t, mine.NumSubexp(), std.NumSubexp(), "NumSubexp() pat="+pat)
		eq(t, mine.SubexpNames(), std.SubexpNames(), "SubexpNames() pat="+pat)

		for _, in := range testInputs {
			msg := func(m string) string { return m + " | pat=" + pat + " in=" + in }

			eq(t, mine.MatchString(in), std.MatchString(in), msg("MatchString"))
			eq(t, mine.FindString(in), std.FindString(in), msg("FindString"))
			eq(t, mine.FindStringIndex(in), std.FindStringIndex(in), msg("FindStringIndex"))
			eq(t, mine.FindStringSubmatch(in), std.FindStringSubmatch(in), msg("FindStringSubmatch"))
			eq(t, mine.FindStringSubmatchIndex(in), std.FindStringSubmatchIndex(in), msg("FindStringSubmatchIndex"))

			for _, n := range []int{-1, 0, 1, 2} {
				eq(t, mine.FindAllString(in, n), std.FindAllString(in, n), msg("FindAllString"))
				eq(t, mine.FindAllStringIndex(in, n), std.FindAllStringIndex(in, n), msg("FindAllStringIndex"))
				eq(t, mine.FindAllStringSubmatch(in, n), std.FindAllStringSubmatch(in, n), msg("FindAllStringSubmatch"))
				eq(t, mine.FindAllStringSubmatchIndex(in, n), std.FindAllStringSubmatchIndex(in, n), msg("FindAllStringSubmatchIndex"))
			}

			for _, repl := range replTemplates {
				eq(t, mine.ReplaceAllString(in, repl), std.ReplaceAllString(in, repl),
					msg("ReplaceAllString repl="+repl))
			}
			// ReplaceAllStringFunc: 用一个会改变内容的函数, 验证替换位置/拼接一致.
			f := func(s string) string { return "<" + s + ">" }
			eq(t, mine.ReplaceAllStringFunc(in, f), std.ReplaceAllStringFunc(in, f),
				msg("ReplaceAllStringFunc"))
		}
	}
}

// TestCompileError: 非法正则应返回 error 而非 panic; MustCompile 非法应 panic.
func TestCompileError(t *testing.T) {
	if _, err := Compile(`(unclosed`); err == nil {
		t.Error("expected compile error for unbalanced paren")
	}
	if msg := panicToErrMsg(func() { MustCompile(`[z-a]`) }); msg == "" {
		t.Error("expected MustCompile to panic on bad class")
	}
}

// panicToErrMsg 运行 f, 返回它 panic 的信息字符串; 没 panic 返回 "".
func panicToErrMsg(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}
