// prefilter_test.go — Prefilter 的三条:
//   1. 【健全性】(唯一要害) 真命中的 pattern 必须一条不少地出现在 GetPotentials 里。
//      筛子多放几条只是白跑, 少放一条就是漏检 —— 整个"前置粗筛"方向的正确性全压在这一条上。
//   2. 不可过滤集认得准 —— 特别是 `(?:foo|[A-Z]{5})` 这种含字面量但整条筛不掉的。
//   3. 原子表的形状 (小写化、最短长度旋钮生效)。
package hgmLibre2

import (
	"strings"
	"testing"
)

// prefilterPats 混了四类: 纯字面量 / 字面量+字符类 / 纯字符类(不可过滤) / 交替里有一支不可过滤。
var prefilterPats = []string{
	`api_key`,
	`(?i)secret[_ ]?key`,
	`AKIA[0-9A-Z]{16}`,
	`password\s*[:=]\s*\S{6,}`,
	`[A-Za-z0-9+/=_-]{20,}`,      // 纯字符类 → 不可过滤
	`(?-i:\([A-Z]{2,5}\))`,       // 纯字符类 → 不可过滤
	`(?:foo|[A-Z]{5})`,           // 含字面量 foo, 但另一支不需要它 → 整条不可过滤
	`bearer\s+[A-Za-z0-9._-]{10,}`,
	`-----BEGIN [A-Z ]+PRIVATE KEY-----`,
	`\bpwd\b`,
}

func newTestPrefilter(t *testing.T) *Prefilter {
	t.Helper()
	p, err := NewPrefilter(prefilterPats, 0, 0)
	if err != nil {
		t.Fatalf("NewPrefilter: %v", err)
	}
	if p.GetPatternLen() != len(prefilterPats) {
		t.Fatalf("条数对不上: want %d got %d", len(prefilterPats), p.GetPatternLen())
	}
	return p
}

// TestPrefilterSoundness — 健全性对拍: 对每份正文, 拿"正文里真的出现了的原子"去问 GetPotentials,
// 结果必须【覆盖】所有真命中的 pattern。这一条挂了, 前置粗筛就是在漏检。
func TestPrefilterSoundness(t *testing.T) {
	p := newTestPrefilter(t)
	defer p.FreeC()

	res := make([]*Regexp, len(prefilterPats))
	for i, s := range prefilterPats {
		re, err := Compile(s)
		if err != nil {
			t.Fatalf("Compile(%q): %v", s, err)
		}
		res[i] = re
		defer re.FreeC()
	}

	texts := []string{
		"",
		"nothing interesting here at all",
		"api_key = 12345678901234567890",
		"Secret Key: abcdefghijklmnop",
		"AKIAIOSFODNN7EXAMPLE and more",
		"password = hunter2hunter2",
		"authorization: bearer eyJhbGciOiJIUzI1NiJ9",
		"-----BEGIN RSA PRIVATE KEY-----",
		"my pwd is short",
		"(ABC) (DEFG) HIJ",
		"foo",
		"ABCDE",
		"aGVsbG8gd29ybGQgdGhpcyBpcyBiYXNlNjQ=",
		"mixed api_key AKIAIOSFODNN7EXAMPLE (XY) foo pwd",
		strings.Repeat("x", 5000) + "api_key" + strings.Repeat("y", 5000),
	}

	for _, text := range texts {
		lower := strings.ToLower(text)
		var found []int32
		for i, a := range p.GetAtoms() {
			if strings.Contains(lower, a) {
				found = append(found, int32(i))
			}
		}
		pot := map[int32]bool{}
		for _, id := range p.GetPotentials(found) {
			pot[id] = true
		}
		for i, re := range res {
			if !re.MatchString(text) {
				continue
			}
			if !pot[int32(i)] {
				t.Errorf("🔴 漏筛: 正文 %q 上 pattern[%d]=%q 真的命中, 但 GetPotentials 没放它进来\n"+
					"  找到的原子 %v · 全部原子 %v", text, i, prefilterPats[i], found, p.GetAtoms())
			}
		}
	}
}

// TestPrefilterUnfiltered — 不可过滤集: 纯字符类的三条必须在里面, 纯字面量的必须不在。
func TestPrefilterUnfiltered(t *testing.T) {
	p := newTestPrefilter(t)
	defer p.FreeC()

	un := map[int32]bool{}
	for _, id := range p.GetUnfiltered() {
		un[id] = true
	}
	// 4 = [A-Za-z0-9+/=_-]{20,} · 5 = (?-i:\([A-Z]{2,5}\)) · 6 = (?:foo|[A-Z]{5})
	for _, i := range []int32{4, 5, 6} {
		if !un[i] {
			t.Errorf("pattern[%d]=%q 没有必需字面量, 应该在不可过滤集里, 实际不在(不可过滤集 %v)",
				i, prefilterPats[i], p.GetUnfiltered())
		}
	}
	// 0 = api_key · 8 = -----BEGIN … 这两条有硬字面量, 必须筛得掉。
	for _, i := range []int32{0, 8} {
		if un[i] {
			t.Errorf("pattern[%d]=%q 有硬字面量, 不该在不可过滤集里", i, prefilterPats[i])
		}
	}
	// 第 6 条是这套东西存在的理由: 手写抽取器会看见 foo 就以为筛得掉。
	if !un[6] {
		t.Error("🔴 (?:foo|[A-Z]{5}) 必须判为不可过滤 —— 它含字面量 foo 但另一支不需要 foo。" +
			"这一条判错, 说明接出来的不是 RE2 的 prefilter")
	}
}

// TestPrefilterAtoms — 原子表: 全小写; minAtomLen 调大之后原子变少 (或不变), 且不可过滤集只增不减。
func TestPrefilterAtoms(t *testing.T) {
	p := newTestPrefilter(t)
	defer p.FreeC()
	if len(p.GetAtoms()) == 0 {
		t.Fatal("原子表空 —— 这批 pattern 里有硬字面量, 不该一个原子都推不出来")
	}
	for _, a := range p.GetAtoms() {
		if a != strings.ToLower(a) {
			t.Errorf("原子 %q 没小写化 —— 文档承诺是小写的, 调用方会照着直接在小写正文里找", a)
		}
	}

	big, err := NewPrefilter(prefilterPats, 8, 0)
	if err != nil {
		t.Fatalf("NewPrefilter(minAtomLen=8): %v", err)
	}
	defer big.FreeC()
	if len(big.GetAtoms()) > len(p.GetAtoms()) {
		t.Errorf("minAtomLen 调大之后原子反而变多了: %d → %d", len(p.GetAtoms()), len(big.GetAtoms()))
	}
	if len(big.GetUnfiltered()) < len(p.GetUnfiltered()) {
		t.Errorf("minAtomLen 调大之后不可过滤集反而变小了: %d → %d —— 原子更长只会让更多 pattern 筛不掉",
			len(p.GetUnfiltered()), len(big.GetUnfiltered()))
	}
}
