// quotemeta.go — QuoteMeta: 把字符串里的正则元字符转义。与 stdlib regexp.QuoteMeta 逐字符等价
// (照搬 stdlib 实现)。原 README 未列此函数, 但调用方常用「QuoteMeta(用户输入) 拼进 pattern 再 Compile」,
// 为了让 *hgmLibre2.Regexp 能完全替换 *regexp.Regexp(去掉对 stdlib regexp 的依赖)而补上。
package hgmLibre2

// specialBytes 标记需要转义的元字符 (ASCII): \.+*?()|[]{}^$。位图布局照搬 stdlib regexp。
var specialBytes [16]byte

func special(b byte) bool {
	return b < 0x80 && specialBytes[b%16]&(1<<(b/16)) != 0
}

func init() {
	for _, b := range []byte(`\.+*?()|[]{}^$`) {
		specialBytes[b%16] |= 1 << (b / 16)
	}
}

// QuoteMeta 返回把 s 中所有正则元字符转义后的字符串 (匹配该串字面量的 pattern)。
// 与 stdlib regexp.QuoteMeta 逐字符一致。元字符全是 ASCII, 故按字节遍历即正确。
func QuoteMeta(s string) string {
	var i int
	for i = 0; i < len(s); i++ {
		if special(s[i]) {
			break
		}
	}
	if i >= len(s) {
		return s // 无元字符 · 原样返回 (零分配)
	}
	b := make([]byte, 2*len(s)-i)
	copy(b, s[:i])
	j := i
	for ; i < len(s); i++ {
		if special(s[i]) {
			b[j] = '\\'
			j++
		}
		b[j] = s[i]
		j++
	}
	return string(b[:j])
}
