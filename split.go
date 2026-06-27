// split.go — Split: 按匹配处切分字符串。与 stdlib regexp.Split 逐字符等价 (照搬 stdlib 实现 ·
// 内部走本库 FindAllStringIndex)。原 README 未列, 为让 *hgmLibre2.Regexp 完全替换 *regexp.Regexp 而补上。
package hgmLibre2

// Split 把 s 按正则匹配处切开, 返回匹配之间的子串 (不含匹配本身)。n>0 最多返回 n 段 (最后一段是余下全部);
// n<0 返回全部。语义与 stdlib regexp.Split 一致 (含空匹配处理)。
func (re *Regexp) Split(s string, n int) []string {
	if n == 0 {
		return nil
	}
	if len(re.expr) > 0 && s == "" {
		return []string{""}
	}
	matches := re.FindAllStringIndex(s, n)
	out := make([]string, 0, len(matches))
	beg := 0
	end := 0
	for _, match := range matches {
		if n > 0 && len(out) >= n-1 {
			break
		}
		end = match[0]
		if match[1] != 0 {
			out = append(out, s[beg:end])
		}
		beg = match[1]
	}
	if end != len(s) {
		out = append(out, s[beg:])
	}
	return out
}
