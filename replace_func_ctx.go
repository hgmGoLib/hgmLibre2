// replace_func_ctx.go — ReplaceAllStringFunc 的【复用已有分配】变体: 结果直接追加进调用方自己的
// []byte 底, 匹配位置表挂在 ctx 上跨调用复用。同一 ctx + 同一块底反复调, 稳态零 Go 堆分配
// (f 自己返回的串除外)。
//
// 动机 (2026-08-22 · 16/32/64MB 语料 memprofilerate=1 实测): (*Regexp).ReplaceAllStringFunc 每趟
// 要付两笔按正文线性放大的一次性分配 ——
//   ① matchAllFlat 的 flat []int (每匹配 2*(numSubexp+1) 个 int, 而拼接只读 group0);
//   ② 收尾 strings.Builder 的增长阶梯。裸 Builder 从 0 开始翻倍/1.25 倍地长到 N,
//      累计分配收敛到 5N (Go 大切片 1.25 倍增长 ⇒ 1/(1-1/1.25)=5), 拷贝也白付 4N。
// 实测 engine 的 hex 解码腿在 64MB 正文上 Builder 累计 329MB = 每输入字节 4.9 字节。
// (①②在 ReplaceAllStringFunc 里现在也修了 —— 见该方法注释的"一次开到位"; 但那仍是每趟一块新底,
// 逐段反复调的热路径要的是【连新底都不要】, 那就是本文件。)
//
// 契约同 find_ctx.go 那套: ctx 非线程安全, 并发各持一个; 零值即可用 (首次调用惰性长出 scratch)。
// 结果不放 ctx 里 —— 由调用方传 dst 进来, 因为这类热路径的调用方手上通常已经有一块要往上追加的底
// (拿回来的切片生命周期就归调用方, 不存在"下次调用前有效"这种限制)。
package hgmLibre2

// ReplaceAllStringFunc_ctx_t 持有 ReplaceAllStringFunc 复用所需的 scratch: idx 是匹配位置表
// [s0,e0,s1,e1,…] (走 AppendAllStringIndexFlat 回填, 只要 group0 —— 拼接本来也只读 group0)。
// 零值即可用; 也可用 NewReplaceAllStringFunc_ctx 预分配。
type ReplaceAllStringFunc_ctx_t struct {
	idx []int
}

// NewReplaceAllStringFunc_ctx 预分配好 scratch (够放 nMatchHint 处匹配的位置表), 返回可复用的 ctx。
// nMatchHint <= 0 就不预分配 (等首次调用自己长)。
func NewReplaceAllStringFunc_ctx(nMatchHint int) *ReplaceAllStringFunc_ctx_t {
	if nMatchHint <= 0 {
		return &ReplaceAllStringFunc_ctx_t{}
	}
	return &ReplaceAllStringFunc_ctx_t{idx: make([]int, 0, nMatchHint*2)}
}

// AppendReplaceAllStringFunc 把「re 在 src 上每处匹配整体换成 f(匹配文本) 之后的完整结果」追加进
// dst, 返回 (追加后的切片, 结果与 src 相比是否真的变了)。变了的话 dst 末尾多出来的那一段就是
// re.ReplaceAllStringFunc(src, f); 没变的话 dst 一个字节都没多 (见下面那条回滚)。
//
// 🔴 第二个返回值是 changed 而【不是】matched —— 它的定义就是 `re.ReplaceAllStringFunc(src, f) != src`,
// 与那句常见的 `if out := re.ReplaceAllStringFunc(s, f); out != s` 取值一模一样。两种情况都报 false:
//   ① 压根没匹配 —— 快返, 什么都不写;
//   ② 有匹配, 但每一处 f 都把原文照样写了回去 ⇒ 逐字节没变 —— 收尾把 dst 截回调用前的长度。
// ②不是可有可无的保险。走这套 API 的替换绝大多数是"解码 / 去混淆", f 自己带着合法性判断, 判不过
// 就 `return m` 原样退回 —— HTML 数字实体 `&#…;` 里 ParseInt 失败或码点越界 · 十六进制串长度为奇数
// 或解出来不可打印, 都是【正则命中了但一个字节没改】。调用方问的是"到底变没变", 拿 matched 当
// changed 用就会凭空多出一份与原文相同的产物: 多存一块底 · 多扫一遍 · 多一次去重, 甚至多一条告警。
// 代价接近零: 长度不等直接判定变了, 只有恰好等长才真跑一趟 memcmp; 而解码类替换的产物几乎恒比
// 输入短, 那一比通常在长度上就否掉了。
//
// 两处复用: ①dst 传 buf[:0] 跨调用复用结果底; ②同一个 ctx 复用匹配位置表。第一趟仍要把两块底长到位,
// 之后稳态零分配。首趟先按 len(src) 一次开够 —— 换字符串的结果长度事先不可知, 但绝大多数替换
// (解码/去混淆/脱敏) 的产物与输入同量级或更短, 按 len(src) 开既躲开增长阶梯又不浪费; 真长出去了
// 后面的 append 照常接着长, 只是多付那一小截。
//
// 🔴 ②那条回滚只退 len 不退 cap: 报 false 时 dst 的【长度与内容】与传进来的完全一致, 但底可能已经
// 换成一块更大的了 (刚为它开的那 len(src) 字节)。这对调用方是好事 —— 下一趟不用再开; 唯一的要求
// 是别把传进去的那个切片变量当作"还指着老底"接着用, 一律拿返回值。
//
// 传给 f 的是 src 的子串 (零拷贝); f 的返回值会被立即拷进 dst, 可复用。
func (ctx *ReplaceAllStringFunc_ctx_t) AppendReplaceAllStringFunc(dst []byte, re *Regexp, src string, f func(string) string) ([]byte, bool) {
	// n<0 = 全部; 内部按 len(src)+1 (含逐字节空匹配的最大可能匹配数) 走, 与 ReplaceAllStringFunc 同。
	ctx.idx = re.AppendAllStringIndexFlat(ctx.idx[:0], src, -1)
	if len(ctx.idx) == 0 {
		return dst, false // ①无匹配 (含超 C.int 的超大输入): 什么都不写
	}
	n := len(dst)
	if cap(dst)-n < len(src) { // 一次开够, 免 append 的翻倍/1.25 倍阶梯
		grown := make([]byte, n, n+len(src))
		copy(grown, dst)
		dst = grown
	}
	lastMatchEnd := 0
	for k := 0; k+1 < len(ctx.idx); k += 2 {
		m0, m1 := ctx.idx[k], ctx.idx[k+1]
		dst = append(dst, src[lastMatchEnd:m0]...)
		dst = append(dst, f(src[m0:m1])...)
		lastMatchEnd = m1
	}
	dst = append(dst, src[lastMatchEnd:]...)
	if len(dst)-n == len(src) && bytesStr(dst[n:]) == src {
		return dst[:n], false // ②有匹配但逐字节没变: 回滚成没写过的样子
	}
	return dst, true
}
