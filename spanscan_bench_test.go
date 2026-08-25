package hgmLibre2

// spanscan_bench_test.go — 新旧两种"把每条 pattern 的命中位置全找出来"的实现, 在同一批语料上对照测量。
//
// ── 两种实现 ────────────────────────────────────────────────────────────────
//   旧 (benchOld)   今天调用方的写法: set.Match(text) 当快拒门 + 精确定位到"哪几条命中",
//                   再【逐条命中 pattern】拿它自己那条【非锚定】正则在【整篇正文】上
//                   FindAllStringIndex。命中 k 条就是 1+k 遍全文扫描, 每遍都带 .*? 前缀。
//   新 (benchScan…) 一遍流式扫描吐游程 (Index,Lo,Hi), 再对端点走 ResolveSpan 求另一端 ——
//                   走 set 自己那份程序里【真锚定】的入口, 代价 = 这条命中实际延伸多远,
//                   与正文长度无关。
//
// ── 为什么新实现要分两种取法 ─────────────────────────────────────────────────
//   游程是"这条 pattern 的连号端点", 一条可变长 pattern 撞上一段文本会连出一串。
//     ·-run  每条游程只解析【最外那个端点】= 取最长的那个匹配。这是与 FindAllStringIndex
//            可比的口径 (它给的也是每处一个匹配)。
//     ·-all  游程里【每一个】端点都解析一次 = 把所有长度都要回来。这比 FindAllStringIndex
//            给的信息【多】(重叠的匹配它一个都不给), 代价自然也高, 单列出来免得混为一谈。
//   scan-  只扫不解析, 是"定位"这一步的成本下界。
//
// ── 两个方向 ────────────────────────────────────────────────────────────────
//   一条完整流水线要【两个 set】(扫一个方向, 解析另一个方向):
//     fwd 正向路: 正向 set 扫出【右端】, 反向 set 把右端解析回左端。
//     rev 反向路: 反向 set 扫出【左端】, 正向 set 把左端解析成右端。
//   两条路答案相同 (spanscan_need_test.go 里逐字节对过账), 成本不同 —— 所以要各测一遍。
//
// 语料和正则都是现编的通用形状, 不对应任何具体产品的规则表。

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// benchPats: 10 条通用形状。定长/变长/变长尾/纯字面量前缀/无前缀全类都有一条,
// 最后一条 `[a-z]{4,}` 是"最坏输入"那一档的主力 —— 全小写正文上几乎每个位置都是它的端点。
var benchPats = []string{
	`AAA-[A-Za-z0-9]{12}`,
	`AAA-[A-Za-z0-9]{8,16}`,
	`BBB_[a-z]{6,}`,
	`[0-9]{4}-[0-9]{4}`,
	`CCC[0-9]{3,10}`,
	`[a-z]{5,}ing`,
	`xx[A-Za-z]{4,12}yy`,
	`kk[0-9a-f]{8,32}`,
	`[A-Z][a-z]{7,}`,
	`[a-z]{4,}`,
}

const benchTextLen = 64 << 10

// benchLCG 是个确定性伪随机源 (不用 math/rand, 免得换了 Go 版本语料就变了)。
func benchLCG(seed uint32) func(mod int) int {
	x := seed
	return func(mod int) int {
		x = x*1664525 + 1013904223
		return int((x >> 16) % uint32(mod))
	}
}

// benchCorpusZero: 只有大写字母和空格。上面 10 条一条也命中不了 ——
// 没有 '-' 没有 '_' 没有数字没有小写, 逐条对着看就知道为什么。这是"快拒门"那一档。
func benchCorpusZero(n int) string {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZ     "
	rnd := benchLCG(20260824)
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[rnd(len(alpha))]
	}
	return string(b)
}

// benchCorpusFew: 同样的填充, 埋 24 处真命中。这是"正常正文"那一档 ——
// 绝大多数字节什么都不是, 命中稀稀拉拉。
func benchCorpusFew(n int) string {
	b := []byte(benchCorpusZero(n))
	seeds := []string{
		"AAA-AbCdEf123456", "BBB_abcdefghij", "1234-5678", "CCC12345",
		"walking", "xxAbCdEfyy", "kk0123456789abcdef", "Abcdefghij",
	}
	for i := 0; i < 24; i++ {
		s := seeds[i%len(seeds)]
		at := (n/25)*(i+1) - len(s)/2
		copy(b[at:], s)
	}
	return string(b)
}

// benchCorpusMost: 全小写, 没有空格。`[a-z]{4,}` 在偏移 4 往后【每一个】位置都是一个匹配端点,
// `BBB_[a-z]{6,}` / `[a-z]{5,}ing` 也时不时接上 —— 这是最坏输入。
func benchCorpusMost(n int) string { return benchCorpusMostSeed(n, 776655) }

func benchCorpusMostSeed(n int, seed uint32) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz"
	rnd := benchLCG(seed)
	b := make([]byte, n)
	for i := range b {
		b[i] = alpha[rnd(len(alpha))]
	}
	return string(b)
}

func benchCorpus(kind string) string {
	switch kind {
	case "zero":
		return benchCorpusZero(benchTextLen)
	case "few":
		return benchCorpusFew(benchTextLen)
	case "most":
		return benchCorpusMost(benchTextLen)
	}
	panic("未知语料 " + kind)
}

var benchCorpusKinds = []string{"zero", "few", "most"}

// ── 三个 set / 正则对象由两条路【共用】 ──────────────────────────────────────
//
// 🔴 必须共用, 不能各建各的。同一批 pattern 建两次, 两个 DFA 的状态区落在不同的地址上,
//    cache set 冲突不一样 —— 实测同一段代码只因为换了个 set 对象就能差 5~8%, 比要量的
//    差别还大。共用之后"旧实现的门 set"和"新实现的扫描 set"是同一个对象, 剩下的差别
//    才是代码的差别。
var (
	benchObjOnce sync.Once
	benchFwdSet  *RegexpSet
	benchRevSet  *RegexpSetReverse
	benchRes     []*Regexp
	benchObjErr  error
)

func benchObjects(tb testing.TB) (fwd *RegexpSet, rev *RegexpSetReverse, res []*Regexp) {
	benchObjOnce.Do(func() {
		benchFwdSet, benchObjErr = NewRegexpSet(benchPats)
		if benchObjErr != nil {
			return
		}
		benchRevSet, benchObjErr = NewRegexpSetReverseMaxMem(benchPats, 0)
		if benchObjErr != nil {
			return
		}
		for _, p := range benchPats {
			re, err := Compile(p)
			if err != nil {
				benchObjErr = err
				return
			}
			benchRes = append(benchRes, re)
		}
	})
	if benchObjErr != nil {
		tb.Fatalf("建对象: %v", benchObjErr)
	}
	return benchFwdSet, benchRevSet, benchRes
}

// ── 旧实现 ──────────────────────────────────────────────────────────────────

// benchOld 是今天调用方那一套的可复用工作区。
type benchOld struct {
	set  *RegexpSet // 快拒门 + 定位到"哪几条命中"
	res  []*Regexp  // 每条 pattern 自己那条非锚定正则
	idx  []int32
	flat []int
	out  []int32 // (id, lo, hi) 三元组
}

func newBenchOld(tb testing.TB) *benchOld {
	set, _, res := benchObjects(tb)
	return &benchOld{set: set, res: res, idx: make([]int32, len(benchPats))}
}

// newBenchOldFresh 现建一套【只属于这条路】的对象。给峰值测量用: 那里要量的正是
// "这条路自己占多少", 共用对象会把另一条路的 set 也算进来。
func newBenchOldFresh(tb testing.TB) *benchOld {
	set, err := NewRegexpSet(benchPats)
	if err != nil {
		tb.Fatalf("建正向 set: %v", err)
	}
	o := &benchOld{set: set, idx: make([]int32, len(benchPats))}
	for _, p := range benchPats {
		re, err := Compile(p)
		if err != nil {
			tb.Fatalf("编 %q: %v", p, err)
		}
		o.res = append(o.res, re)
	}
	return o
}

func (o *benchOld) run(text string) []int32 {
	o.out = o.out[:0]
	for _, i := range o.set.Match(text, o.idx) {
		o.flat = o.res[i].AppendAllStringIndexFlat(o.flat[:0], text, -1)
		for j := 0; j+1 < len(o.flat); j += 2 {
			o.out = append(o.out, i, int32(o.flat[j]), int32(o.flat[j+1]))
		}
	}
	return o.out
}

// ── 新实现 ──────────────────────────────────────────────────────────────────

// benchNew 是新那一套的可复用工作区: 扫一个方向, 解析另一个方向。
type benchNew struct {
	forward bool  // 扫的是不是正向 set (正向 set 吐右端)
	bound   int32 // 解析时的回看上限 (0 = 不限)
	// 正反已经是两个类型了, 所以方向在建工作区的时候就收进这两个闭包 ——
	// 下面推进那段代码一个字都不用分方向。
	scan    func(text string, fn func(reIndex, lo, hi int32)) error
	resolve func(text string, from, bound, id int32) (int32, bool, error)
	// 峰值那条测试要分别报"扫描 set"和"解析 set"的水位, 所以也一起收进来。
	scanMem func() SetMemInfo
	resMem  func() SetMemInfo
	spans   []setSpan_t
	out     []int32
	cov     []int32 // runCov 用: 每条 pattern 上一处已解析命中的【内侧边界】
}

// newBenchNewFresh 同上, 现建自己的两个 set。只给峰值测量用。
func newBenchNewFresh(tb testing.TB, forward bool) *benchNew {
	fwd, err := NewRegexpSet(benchPats)
	if err != nil {
		tb.Fatalf("建正向 set: %v", err)
	}
	rev, err := NewRegexpSetReverseMaxMem(benchPats, 0)
	if err != nil {
		tb.Fatalf("建反向 set: %v", err)
	}
	return newBenchNewOn(tb, forward, fwd, rev)
}

func newBenchNew(tb testing.TB, forward bool) *benchNew {
	fwd, rev, _ := benchObjects(tb)
	return newBenchNewOn(tb, forward, fwd, rev)
}

func newBenchNewOn(tb testing.TB, forward bool, fwd *RegexpSet, rev *RegexpSetReverse) *benchNew {
	n := &benchNew{forward: forward}
	if forward {
		alloc, err := newFindAllIndexAlloc(fwd, 256)
		if err != nil {
			tb.Fatalf("NewFindAllIndexAlloc: %v", err)
		}
		n.scan = func(text string, fn func(reIndex, lo, hi int32) ) error {
			return fwd.FindAllIndex(text, alloc, fn)
		}
		n.resolve = rev.ResolveSpanWithin
		n.scanMem, n.resMem = fwd.MemInfo, rev.MemInfo
	} else {
		alloc, err := newFindAllIndexAlloc(rev.s, 256)
		if err != nil {
			tb.Fatalf("NewFindAllIndexAlloc: %v", err)
		}
		n.scan = func(text string, fn func(reIndex, lo, hi int32)) error {
			return rev.FindAllIndex(text, alloc, fn)
		}
		n.resolve = fwd.ResolveSpanWithin
		n.scanMem, n.resMem = rev.MemInfo, fwd.MemInfo
	}
	return n
}

// scanOnly 只做定位那一步 (吐游程), 是新实现的成本下界。
func (n *benchNew) scanOnly(tb testing.TB, text string) []setSpan_t {
	n.spans = n.spans[:0]
	if err := n.scan(text, func(reIndex, lo, hi int32) {
		n.spans = append(n.spans, setSpan_t{reIndex, lo, hi})
	}); err != nil {
		tb.Fatalf("FindAllIndex: %v", err)
	}
	return n.spans
}

// run 扫 + 解析。perEndpoint=false 时每条游程只解析最外那个端点 (取最长的匹配,
// 与 FindAllStringIndex 每处一个匹配的口径可比); true 时游程里每个端点都解析一次。
func (n *benchNew) run(tb testing.TB, text string, perEndpoint bool) []int32 {
	n.scanOnly(tb, text)
	n.out = n.out[:0]
	for _, sp := range n.spans {
		lo, hi := sp.Lo, sp.Hi
		if !perEndpoint {
			// 正向 set 吐的是右端: 取最大的那个 = 最长匹配。反向 set 吐左端: 取最小的。
			if n.forward {
				lo = hi
			} else {
				hi = lo
			}
		}
		for p := lo; p <= hi; p++ {
			// bound = 调用方声明的"这条命中最多能延伸多远"。给 ResolveSpanWithin 一个上限,
			// 走到死状态的成本就从"这条命中实际有多长"钉成常数 —— 见下面 -b64 那几档。
			bd := int32(-1)
			if n.bound > 0 {
				if n.forward {
					bd = p - n.bound // 反向 set 解析: bound 是左下界
					if bd < 0 {
						bd = 0
					}
				} else {
					bd = p + n.bound // 正向 set 解析: bound 是右上界
				}
			}
			other, ok, err := n.resolve(text, p, bd, sp.Index)
			if err != nil {
				tb.Fatalf("ResolveSpan: %v", err)
			}
			if !ok {
				// 不限回看时"解析不出来"一定是 bug (端点是这条 pattern 自己吐的);
				// 限了回看就是合法结果 —— 这条命中比调用方声明的上限还长, 按约定不要了。
				if n.bound <= 0 {
					tb.Fatalf("规则 #%d 在端点 %d 上解析不出另一端", sp.Index, p)
				}
				continue
			}
			if n.forward {
				n.out = append(n.out, sp.Index, other, p) // (id, 左端, 右端)
			} else {
				n.out = append(n.out, sp.Index, p, other)
			}
		}
	}
	return n.out
}

// runCov 在 run(perEndpoint=false) 之上再省一层, 【只走反向路】。
//
// 一条 pattern 在同一段正文上常常吐出好几条游程, 但按旧口径它们属于【同一处命中】: 变长尾巴
// 每走到一个可收的位置就成一条游程, [a-z]{5,}ing 撞上一大段小写就是每个 "ing" 各一条。逐条
// 游程各解析一次, 每次都从自己那个端点一路回走到同一个起点 —— 回走量 O(游程数 × 正文长)。
//
// 改成【一次左到右的推进】: 取当前游标右边最小的那个左端, 解析出它的最长右端, 记下来当新
// 游标; 后面凡是落在游标以内的左端全部跳过 (它们只可能落在刚吐出的那一处里面)。总回走量压回
// O(正文长)。这正是 FindAllStringIndex 自己那套 leftmost-longest 扫法, 所以口径逐字节相同 ——
// TestSpanPerf_Shape 里直接和旧实现【全等】对账, 不是"盖住"那种弱断言。
//
// 🔴 方向不能反 —— 拿正向 set (右端) 从右往左推是【错的】, 给的是 rightmost-longest, 和旧口径
//    不是一个东西。反例 abc?|bcd 在 "abcd" 上: 旧口径 [0,3)="abc", 从右往左推给 [1,4)="bcd"。
//    两个都自洽, 但下游要拿这一段做定长校验, 差一个字节就是另一回事。见 TestSpanPerf_CovDirection。
func (n *benchNew) runCov(tb testing.TB, text string) []int32 {
	if n.forward {
		tb.Fatalf("runCov 只能走反向路 (扫左端), 见上面那条红字")
	}
	n.scanOnly(tb, text)
	n.out = n.out[:0]
	if len(n.cov) != len(benchPats) {
		n.cov = make([]int32, len(benchPats))
	}
	for i := range n.cov {
		n.cov[i] = -1 // 游标: 这条 pattern 上一处命中的右端 (还没有 = -1, 任何左端都跳不过)
	}
	// 🔴 反向 set 是【从右往左】扫正文的, 吐出来的游程按位置【降序】排。这一趟推进要的是升序,
	//    所以倒着遍历。(run() 那几档不看次序, 所以从前没暴露过这件事。)
	for k := len(n.spans) - 1; k >= 0; k-- {
		sp := n.spans[k]
		id := sp.Index
		p := sp.Lo // 这条游程最左的那个左端 = 游标右边最靠左的起点
		if p < n.cov[id] {
			continue // 落在上一处命中里面 ⟹ 旧口径不会从这里再起一处
		}
		end, ok, err := n.resolve(text, p, -1, id)
		if err != nil {
			tb.Fatalf("ResolveSpan: %v", err)
		}
		if !ok {
			tb.Fatalf("规则 #%d 在端点 %d 上解析不出另一端", id, p)
		}
		n.cov[id] = end
		n.out = append(n.out, id, p, end)
	}
	return n.out
}

// TestSpanPerf_CovDirection 钉死上面那条红字: 同一个"跳过被盖住的游程"的省法, 反向路给的是
// 旧口径, 正向路给的【不是】。这不是实现 bug, 是 leftmost-longest 和 rightmost-longest 本来
// 就不是一个东西 —— 需求要的是前者 (下游拿这一段做定长校验)。
func TestSpanPerf_CovDirection(t *testing.T) {
	const pat, text = `abc?|bcd`, "abcd"
	re, err := Compile(pat)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := re.FindAllStringIndex(text, -1) // [[0 3]] —— 旧口径
	if len(want) != 1 || want[0][0] != 0 || want[0][1] != 3 {
		t.Fatalf("旧口径变了: %v", want)
	}
	fwd, err := NewRegexpSet([]string{pat})
	if err != nil {
		t.Fatalf("建正向 set: %v", err)
	}
	rev, err := NewRegexpSetReverseMaxMem([]string{pat}, 0)
	if err != nil {
		t.Fatalf("建反向 set: %v", err)
	}
	// 反向路: 最靠左的左端 0 → 最长右端 3 → [0,3) = 旧口径。
	if end, ok, err := fwd.ResolveSpanWithin(text, 0, -1, 0); err != nil || !ok || end != 3 {
		t.Fatalf("反向路解析: end=%d ok=%v err=%v, 要 3", end, ok, err)
	}
	// 正向路: 最靠右的右端 4 → 最长回走到 1 → [1,4), 与旧口径【不同】。
	if start, ok, err := rev.ResolveSpanWithin(text, 4, -1, 0); err != nil || !ok || start != 1 {
		t.Fatalf("正向路解析: start=%d ok=%v err=%v, 要 1", start, ok, err)
	}
}

// ── benchmark ───────────────────────────────────────────────────────────────

func BenchmarkSpanPerf(b *testing.B) {
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)

		b.Run("old/"+kind, func(b *testing.B) {
			o := newBenchOld(b)
			o.run(text) // 热身: 把 DFA 状态建出来, 量的是稳态
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.run(text)
			}
		})

		for _, dir := range []struct {
			tag string
			fwd bool
		}{{"fwd", true}, {"rev", false}} {
			b.Run("scan-"+dir.tag+"/"+kind, func(b *testing.B) {
				n := newBenchNew(b, dir.fwd)
				n.scanOnly(b, text)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n.scanOnly(b, text)
				}
			})
			b.Run(dir.tag+"-run/"+kind, func(b *testing.B) {
				n := newBenchNew(b, dir.fwd)
				n.run(b, text, false)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n.run(b, text, false)
				}
			})
			if !dir.fwd { // cov 只对反向路成立, 见 runCov 上面那条红字
				b.Run(dir.tag+"-cov/"+kind, func(b *testing.B) {
					n := newBenchNew(b, dir.fwd)
					n.runCov(b, text)
					b.ReportAllocs()
					b.SetBytes(int64(len(text)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						n.runCov(b, text)
					}
				})
			}
			b.Run(dir.tag+"-all/"+kind, func(b *testing.B) {
				n := newBenchNew(b, dir.fwd)
				n.run(b, text, true)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n.run(b, text, true)
				}
			})
			// -b64: 同上, 但给解析一个 64 字节的回看上限。答案会在超长命中上被截短 ——
			// 那正是调用方用 bound 时自己声明的取舍, 这里量的是它把成本钉回常数的效果。
			b.Run(dir.tag+"-run-b64/"+kind, func(b *testing.B) {
				n := newBenchNew(b, dir.fwd)
				n.bound = 64
				n.run(b, text, false)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n.run(b, text, false)
				}
			})
			b.Run(dir.tag+"-all-b64/"+kind, func(b *testing.B) {
				n := newBenchNew(b, dir.fwd)
				n.bound = 64
				n.run(b, text, true)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					n.run(b, text, true)
				}
			})
		}
	}
}

// TestSpanPerf_Shape 把每档语料的规模先摊开 (命中条数 / 游程条数 / 端点数),
// 没有这张表, 上面那些 ns/op 是没法解释的。顺带钉死"新实现给的是旧实现的超集"。
func TestSpanPerf_Shape(t *testing.T) {
	old := newBenchOld(t)
	fwd := newBenchNew(t, true)
	rev := newBenchNew(t, false)
	for _, kind := range benchCorpusKinds {
		text := benchCorpus(kind)
		oldOut := old.run(text)
		spans := fwd.scanOnly(t, text)
		pts := 0
		for _, sp := range spans {
			pts += int(sp.Hi - sp.Lo + 1)
		}
		revSpans := rev.scanOnly(t, text)
		revPts := 0
		for _, sp := range revSpans {
			revPts += int(sp.Hi - sp.Lo + 1)
		}
		t.Logf("%-5s 正文 %d 字节: 旧实现 %d 处匹配 | 正向路 %d 条游程/%d 个右端 | 反向路 %d 条游程/%d 个左端",
			kind, len(text), len(oldOut)/3, len(spans), pts, len(revSpans), revPts)
		// 🔴 两条路的【端点数】天生就不一样, 不是 bug: 一条可变长 pattern 的右端个数和左端
		// 个数没有任何关系 (BBB_[a-z]{6,} 撞上一段小写就是【一个】左端配【一串】右端)。
		// 两条路一致的是【取舍之后的区间】, 那个由 spanscan_need_test.go 逐字节钉。
		// 这里要钉的是另一条: 两条路各自都必须【盖住】旧实现给的每一处匹配。
		for _, r := range []struct {
			tag string
			n   *benchNew
		}{{"正向路", fwd}, {"反向路", rev}} {
			got := map[[3]int32]bool{}
			f := r.n.run(t, text, true)
			for i := 0; i+2 < len(f); i += 3 {
				got[[3]int32{f[i], f[i+1], f[i+2]}] = true
			}
			for i := 0; i+2 < len(oldOut); i += 3 {
				k := [3]int32{oldOut[i], oldOut[i+1], oldOut[i+2]}
				if !got[k] {
					t.Fatalf("%s %s: 旧实现的匹配 (规则 #%d, [%d,%d)) 没被盖住",
						kind, r.tag, k[0], k[1], k[2])
				}
			}
		}
		// 反向路的"跳过被盖住的游程"那一档 (runCov) 走的就是 FindAllStringIndex 自己那套
		// leftmost-longest 扫法, 所以这里要的是【全等】, 不是上面那种"盖住"的弱断言。
		covOut := rev.runCov(t, text)
		want := map[[3]int32]int{}
		for i := 0; i+2 < len(oldOut); i += 3 {
			want[[3]int32{oldOut[i], oldOut[i+1], oldOut[i+2]}]++
		}
		for i := 0; i+2 < len(covOut); i += 3 {
			k := [3]int32{covOut[i], covOut[i+1], covOut[i+2]}
			if want[k] == 0 {
				t.Fatalf("%s cov: 多给了一处 (规则 #%d, [%d,%d))", kind, k[0], k[1], k[2])
			}
			want[k]--
		}
		for k, n := range want {
			if n != 0 {
				t.Fatalf("%s cov: 少给了 %d 处 (规则 #%d, [%d,%d))", kind, n, k[0], k[1], k[2])
			}
		}
	}
}

// TestSpanPerf_NoAlloc 钉死"稳态复用下 Scan / ResolveSpan 一笔 Go 堆分配都不产生"。
//
// 🔴 这条曾经是【错的】: 两处各有一个 &局部变量 交给 C 当出参, 逃逸分析据此每次调用把它
//    搬上堆 —— 一次 Scan 一笔 4 字节, 一次 ResolveSpan 也是一笔。单看不起眼, 但"每个端点
//    解析一次"的用法上按端点数放大 (6.5 万个端点 = 6.5 万笔 262KB)。改成按值返回的 _r 孪生
//    (cre2_set_resolve_span_r) + 把 more 挪进工作区之后归零。留这条测试免得再退回去。
func TestSpanPerf_NoAlloc(t *testing.T) {
	fwd, rev, _ := benchObjects(t)
	alloc, err := newFindAllIndexAlloc(fwd, 256)
	if err != nil {
		t.Fatalf("NewFindAllIndexAlloc: %v", err)
	}
	defer alloc.Close()
	text := benchCorpus("few")

	var spans []setSpan_t
	if err := fwd.FindAllIndex(text, alloc, func(i, lo, hi int32) {
		spans = append(spans, setSpan_t{i, lo, hi})
	}); err != nil {
		t.Fatalf("热身 FindAllIndex: %v", err)
	}
	if len(spans) == 0 {
		t.Fatalf("语料一条都没命中, 这条测试白跑了")
	}

	// FindAllIndex: 回调什么都不做 (往切片里 append 是【调用方】的分配, 不是库的)。
	nop := func(_, _, _ int32) {}
	if n := testing.AllocsPerRun(50, func() {
		if err := fwd.FindAllIndex(text, alloc, nop); err != nil {
			t.Fatalf("FindAllIndex: %v", err)
		}
	}); n != 0 {
		t.Fatalf("FindAllIndex 每次分配 %g 笔, 要求 0", n)
	}

	// ResolveSpan: 正向 set 扫出来的右端, 拿反向 set 解析。
	if n := testing.AllocsPerRun(50, func() {
		for _, sp := range spans {
			if _, _, err := rev.ResolveSpan(text, sp.Hi, sp.Index); err != nil {
				t.Fatalf("ResolveSpan: %v", err)
			}
		}
	}); n != 0 {
		t.Fatalf("ResolveSpan 每轮分配 %g 笔, 要求 0", n)
	}
}

// ── 内存峰值 ────────────────────────────────────────────────────────────────
//
// 峰值只能【一个进程量一条路】: 两条路跑在同一个进程里, RSS 高水位是共享的, 谁先跑谁吃亏。
// 所以父测试把自己 fork 出来, 每条路一个子进程, 各报各的 VmHWM。
// 量的是整个进程的高水位 —— native 那边 DFA 状态缓存的 arena 就在里面, Go 那边也在里面。

const benchPerfChildEnv = "HGMLIBRE2_PERF_CHILD"

func vmHWM(t testing.TB) int64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("读不到 /proc/self/status: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb << 10
			}
		}
	}
	t.Skip("/proc/self/status 里没有 VmHWM")
	return 0
}

// TestSpanPerf_PeakChild 是子进程入口, 只在父测试设了环境变量时才干活。
func TestSpanPerf_PeakChild(t *testing.T) {
	path := os.Getenv(benchPerfChildEnv)
	if path == "" {
		t.Skip("不是子进程")
	}
	// 峰值要拿【每篇都不一样】的正文去逼: 同一篇扫 N 遍第二遍起全吃缓存, 状态一个都不新建,
	// 量出来的是"缓存没长起来"而不是"缓存长不大"。这里 12 篇 256KiB 各不相同的最坏形状语料,
	// 每篇都要现造一批状态 —— 这才是产线形态 (每个请求一份互不相同的 body)。
	texts := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		texts = append(texts, benchCorpusMostSeed(256<<10, uint32(1000+i*7919)))
	}
	texts = append(texts, benchCorpus("few"), benchCorpus("zero"))
	// base 在语料造完之后取: 语料本身 (3MB+) 两条路一样多, 算进去只会把要看的差别冲淡。
	base := vmHWM(t)
	switch path {
	case "old":
		o := newBenchOldFresh(t)
		for _, tx := range texts {
			o.run(tx)
		}
		m := o.set.MemInfo()
		fmt.Printf("PEAK %-3s 增量=%dKB 门set: arena=%dKB 状态=%d 生涯建过=%d flush=%d | 另有 %d 个独立 Regexp DFA 缓存 (各自 %dMB 额度)\n",
			path, (vmHWM(t)-base)>>10, m.ArenaCap>>10, m.States, m.StatesBuiltTotal, m.FlushesTotal,
			len(o.res), m.StateBudget>>20)
	case "fwd", "rev":
		n := newBenchNewFresh(t, path == "fwd")
		// 每条游程解析一次就够: 峰值看的是【状态缓存长到多大】, 而状态集是由"扫过哪些字节 +
		// 锚定入口走过哪些字节"定的, 不由解析【次数】定。全端点解析只是把同样的状态重走几万遍。
		for _, tx := range texts {
			n.run(t, tx, false)
		}
		a, c := n.scanMem(), n.resMem()
		fmt.Printf("PEAK %-3s 增量=%dKB 扫描set: arena=%dKB 状态=%d 生涯建过=%d flush=%d | 解析set: arena=%dKB 状态=%d 生涯建过=%d flush=%d | 一共 2 份 DFA 缓存 (各自 %dMB 额度)\n",
			path, (vmHWM(t)-base)>>10,
			a.ArenaCap>>10, a.States, a.StatesBuiltTotal, a.FlushesTotal,
			c.ArenaCap>>10, c.States, c.StatesBuiltTotal, c.FlushesTotal, a.StateBudget>>20)
	default:
		t.Fatalf("未知路径 %q", path)
	}
}

func TestSpanPerf_Peak(t *testing.T) {
	if os.Getenv(benchPerfChildEnv) != "" {
		t.Skip("子进程不再往下 fork")
	}
	if testing.Short() {
		t.Skip("-short")
	}
	for _, path := range []string{"old", "fwd", "rev"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestSpanPerf_PeakChild", "-test.v")
		cmd.Env = append(os.Environ(), benchPerfChildEnv+"="+path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("子进程 %s: %v\n%s", path, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "PEAK ") {
				t.Log(line)
			}
		}
	}
}
