package hgmLibre2

// batch_match_test.go — cre2_match_all 批量全匹配的 A/B 基准 + 大正文对拍.
// A/B: 同一份正文/正则, 对比「批量(单次 cgo, 现 allMatches)」与「逐处匹配(每命中一次 cgo,
// 批量化之前的老路径)」. 老路径在此文件内用 allMatchesPerCall 原样复刻 (仅 bench/test 用),
// 以便同一二进制里直接对照, 不往库主体留死代码.

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// allMatchesPerCall 是批量化之前的逐处匹配循环 (每处命中一次 findFrom→cre2_match_at→一次 cgo).
// 仅作基准对照; 语义与 stdlib regexp.allMatches 一致.
func (re *Regexp) allMatchesPerCall(s string, n int, deliver func([]int)) {
	end := len(s)
	for pos, i, prevMatchEnd := 0, 0, -1; i < n && pos <= end; {
		m := re.findFrom(s, pos)
		if m == nil {
			break
		}
		accept := true
		if m[1] == m[0] {
			if m[0] == prevMatchEnd {
				accept = false
			}
			_, width := utf8.DecodeRuneInString(s[pos:])
			if width > 0 {
				pos += width
			} else {
				pos = end + 1
			}
		} else {
			pos = m[1]
		}
		prevMatchEnd = m[1]
		if accept {
			deliver(m)
			i++
		}
	}
}

// benchBody 造一份多命中的大正文 (~约 48KB · 数千 token), 夹一个多字节 rune 以走 utf8 推进分支.
func benchBody() string {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("token")
		b.WriteByte(byte('0' + i%10))
		b.WriteString("abc DEF_42 x ")
		if i%37 == 0 {
			b.WriteString("nbhy‑phen ") // U+2011 非断连字符: 3 字节 rune
		}
	}
	return b.String()
}

// BenchmarkFindAll_Batched: 现路径 (单次 cgo 批量全匹配 + 单块 flat 分配).
func BenchmarkFindAll_Batched(b *testing.B) {
	re := MustCompile(`[A-Za-z0-9_]{3,}`)
	body := benchBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := re.FindAllStringIndex(body, -1)
		if len(res) == 0 {
			b.Fatal("no matches")
		}
	}
}

// BenchmarkFindAll_PerCall: 老路径 (每处命中一次 cgo).
func BenchmarkFindAll_PerCall(b *testing.B) {
	re := MustCompile(`[A-Za-z0-9_]{3,}`)
	body := benchBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var res [][]int
		re.allMatchesPerCall(body, len(body)+1, func(m []int) { res = append(res, []int{m[0], m[1]}) })
		if len(res) == 0 {
			b.Fatal("no matches")
		}
	}
}

// TestBatchVsPerCallVsStdlib: 大正文上三方对拍 (批量 == 逐处 == stdlib), 覆盖 index/submatch/string
// 与含空匹配的正则, 确保下沉到 C 的循环逐字等价.
func TestBatchVsPerCallVsStdlib(t *testing.T) {
	body := benchBody()
	pats := []string{
		`[A-Za-z0-9_]{3,}`,        // 多命中, 无空匹配
		`(?i)def_(\d+)`,           // 带子组
		`x*`,                      // 会产空匹配 (推进/去重路径)
		`(token)(\d)`,             // 多子组
		`\d{2}[-\x{2011}]\w+`,     // 多字节 rune 邻接
	}
	for _, pat := range pats {
		std := regexp.MustCompile(pat)
		mine := MustCompile(pat)

		// 逐处(老循环) vs 批量(新公开 API): 全 submatch index 对拍.
		batched := mine.FindAllStringSubmatchIndex(body, -1)
		var perCall [][]int
		mine.allMatchesPerCall(body, len(body)+1, func(m []int) { perCall = append(perCall, append([]int(nil), m...)) })
		eq(t, batched, perCall, "batched(FindAllStringSubmatchIndex) vs perCall pat="+pat)

		// 批量(新) vs stdlib: 走公开 API.
		eq(t, mine.FindAllStringIndex(body, -1), std.FindAllStringIndex(body, -1), "FindAllStringIndex vs stdlib pat="+pat)
		eq(t, mine.FindAllStringSubmatchIndex(body, -1), std.FindAllStringSubmatchIndex(body, -1), "FindAllStringSubmatchIndex vs stdlib pat="+pat)
		eq(t, mine.FindAllString(body, -1), std.FindAllString(body, -1), "FindAllString vs stdlib pat="+pat)
		eq(t, mine.FindAllStringSubmatch(body, -1), std.FindAllStringSubmatch(body, -1), "FindAllStringSubmatch vs stdlib pat="+pat)

		// 有限 n 截断也须一致.
		for _, n := range []int{0, 1, 3, 50} {
			eq(t, mine.FindAllStringIndex(body, n), std.FindAllStringIndex(body, n), "FindAllStringIndex n vs stdlib pat="+pat)
		}
	}
}
