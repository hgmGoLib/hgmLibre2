# hgmLibre2

给 Go 用的自带原生 [RE2](https://github.com/google/re2) 正则库。RE2 的 C++ 源码
已经 vendored 进本目录, 经 cgo 接出来, 所以**不要 abseil · 不要 CMake · 编译期不下载任何东西**。

下面列出的方法与标准库 `regexp` 的**方法名和签名一模一样**(见
[支持的 API](#supported-api)), 两边的代码可以对着读。`string` 系和 `[]byte` 系都给,
共用同一个匹配内核 —— 传 `[]byte` 不产生任何拷贝(见 [`[]byte` 系方法](#byte-slice-methods))。
它**不是** `*regexp.Regexp` 的 drop-in 替代品, 也不打算是: `io.Reader` 那几个变体 ·
`SubexpIndex` · `LiteralPrefix` · `Longest` · marshal/unmarshal 等都没有, 还有几处语义是**故意**
与 stdlib 不同的 —— 最要紧的一条是 **`ReplaceAllString` 的 `repl` 按【字面量】插入
(没有 `$1` / `${name}` / `$$` 展开)** —— 外加几个边角, 见
[与标准库 regexp 的差异](#differences-from-stdlib-regexp)。
某个调用点该用哪个引擎, 带数字的答案在
[`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md);
短版在[为什么用它](#why)。

<a id="what-is-in-here"></a>

## 这里都有什么

| 你想要 | 看 |
|---|---|
| 一条 pattern, stdlib 形状的 API | [支持的 API](#supported-api) · [`[]byte` 系方法](#byte-slice-methods) |
| 在本库和标准库 `regexp` 之间选 | [为什么用它](#why) · [`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md) |
| N 条 pattern, 一遍扫出"哪几条命中" | [`RegexpSet`](#regexpset) |
| N 条 pattern, 一遍扫出**"哪几条命中【以及在哪】"** | [一遍扫完拿整段: 三种去重叠模式](#one-scan-whole-spans-the-three-de-overlap-modes) · [`doc/三种去重叠模式.md`](doc/%E4%B8%89%E7%A7%8D%E5%8E%BB%E9%87%8D%E5%8F%A0%E6%A8%A1%E5%BC%8F.md) |
| 区间那条规则为什么是 leftmost-longest, 凭什么敢这么说 | [`doc/fll的leftmost-longest保证.md`](doc/fll%E7%9A%84leftmost-longest%E4%BF%9D%E8%AF%81.md) |
| 2026-08-28 换补起点那条路的原始报表 | [`doc/补起点换路的实测账_20260828.md`](doc/%E8%A1%A5%E8%B5%B7%E7%82%B9%E6%8D%A2%E8%B7%AF%E7%9A%84%E5%AE%9E%E6%B5%8B%E8%B4%A6_20260828.md) |
| 原始端点, 重叠策略我自己定 | [`FindAllIndex`](#findallindex-the-raw-end-point-runs) · [`ResolveSpan`](#resolvespan-complete-one-end-point-into-a-span) · [底座那一层的性能对照](#the-substrate-layer-benchmarks) |
| 全匹配走一遍就丢, 不物化成表 | [`StepAllStringIndex`](#stepallstringindex) |
| 让一张大表跑快 / 别再抖动 | [调 DFA 状态缓存](#tuning-for-the-dfa-state-cache) · [`doc/set性能优化经验.md`](doc/set%E6%80%A7%E8%83%BD%E4%BC%98%E5%8C%96%E7%BB%8F%E9%AA%8C.md) · [`doc/状态数为什么会相乘.md`](doc/%E7%8A%B6%E6%80%81%E6%95%B0%E4%B8%BA%E4%BB%80%E4%B9%88%E4%BC%9A%E7%9B%B8%E4%B9%98.md) |
| 反着扫一张表 | [反着扫](#scanning-backwards) |
| 与 stdlib 到底差在哪 | [差异](#differences-from-stdlib-regexp) · [`doc/已有库的坑.md`](doc/%E5%B7%B2%E6%9C%89%E5%BA%93%E7%9A%84%E5%9D%91.md) |
| vendored 的 RE2 改了什么 | [vendored 的 RE2](#vendored-re2) · `VENDOR.txt` |
| 每个测试钉的是什么, 怎么跑 | [测试怎么跑, 钉了什么](#how-the-tests-run) |

`doc/` 下全是中文; 里面每一个数字都由本仓库的某个测试复现, 引用处会写清楚是哪一个。

<a id="why"></a>

## 为什么用它

**默认用本库。只有下面四种情况才回标准库 `regexp`** —— 四种都能一眼认出来, 不用做实验去分辨。

Go 的 `regexp` 在语法和线性时间保证上是 RE2 派生的, 但它的匹配器是从零重写的 NFA 模拟
(onepass / bitstate 回溯 / one-pass NFA)。只有提得出**字面量前缀**时它才有快路; 提不出来就
每个位置重起一次。本库跑的是真正的原生 RE2 惰性 DFA: 一遍线性扫, 基本不受输入形状影响。
数字全部来自这一个差别 —— 不是"C 比 Go 快"。

同一条 pattern 同一份语料, 原生引擎在**吞吐**上一格没输过(编译价和每次调用的地板价是另一回事,
就是下面那几个例外): **最差 1.1 倍, 而标准库提不出字面量前缀的地方是 11~85 倍** ——
pattern 以 `(?i)` / `\b` / 字符类 / 交替开头就足以让它提不出来。匹配又全在 C 侧发生, 稳态
**0 B/op**(Go 堆), 于是 GC 的堆目标(以及进程峰值)根本不动。

完整数字、口径、语料和每一条例外:
[`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md)。
里面每个数字都由 `go test -run TestStdlibCompare -v .`(`stdlib_compare_test.go`)复现,
改了库或者换了机器就重跑一遍。

**这四种情况留标准库 `regexp`:**

- **运行期现编且不缓存的 pattern。** 本库编译要贵 1.2~4.6 倍(原生 RE2 前期干的活多),
  现编现用一次赚不回来: 实测"编 `(?i)\b<word>\b` 再匹配一句 90 字节的话",
  stdlib 9.2 µs vs 本库 21.6 µs。能提到包级变量就提 —— 或者, 如果它本来就是一整张关键词表,
  编成一个 [`RegexpSet`](#regexpset), 那又回到本库了。
- **调用停在 cgo 过桥的地板价上。** 一次过桥本库 ~67 ns, stdlib 一次调用 ~2 ns
  (Ryzen 5900X · linux/amd64 · go1.26.5)。只有当**匹配本身比过桥还便宜**时这条才决定胜负 ——
  即输入只有几字节、pattern 简单到 onepass 一遍就完。注意"输入短"本身**不是**判据:
  161 字节的串上放 6 条要回溯形状的 pattern, 本库快 24 倍且零分配。
- **pattern 能匹配空串** —— `a*` · `x{0,3}` · `(a|)` · `(?m)^[ \t]*$` · `\b` 这些。本库
  **在每一个编译入口都拒**(见[拒收可空 pattern](#empty-capable-patterns-are-rejected)),
  所以这不是判断题: 要么改 pattern, 要么用标准库。先改 —— 可空 pattern 处处都命中, `FindAll`
  于是被迫每行吐一个匹配、每次都走"前进一格再重起"那条路; 115 KiB 的行状文本上是 **0.23 倍**
  (10.4 ms vs 2.4 ms), 而把同一个意图改成不可空(`*` → `+`)当场变成本库的 **8.45 倍**,
  在标准库上也更快。只有当零长匹配真的就是你要的语义时才留 stdlib。数字:
  `go test -run TestEmptyWidthMultiline -v .`
- **你编的是别人写的 pattern** —— 用户或配置提供的 —— 而且认的语法必须与 stdlib 逐字节一致。
  两个引擎在边角上不一致(`\C` · 嵌套深度上限 · 个别 escape, 见
  [差异](#differences-from-stdlib-regexp))。这是语义契约, 不是性能问题。

还有一条与速度无关: 跨 goroutine 共享同一个 `*Regexp` 在这里**不线性扩展**(每次搜索都要拿
DFA 状态缓存的读锁), 所以**输入极短 + 高并发**时两个引擎打平。body 量级的输入上这把锁无关紧要,
1000 个 goroutine 本库仍领先约 100 倍。见[并发](#concurrency-sharing-one-regexp-is-fine-it-just-doesnt-scale-linearly)。

除了速度, 本库还躲开了"把原生 RE2 弄进 Go"的几种常见办法各自的代价:

- **不用 wazero / WASM。** `go-re2` 之类的包装把 RE2 跑在 wazero WebAssembly 运行时里,
  它启动时会探标准输入输出句柄。在没有标准句柄的环境(比如 Windows SCM 服务)里那次探测会失败。
  hgmLibre2 是原生链接的, 没有运行时要实例化。
- **不用 abseil / CMake。** vendored 的是 RE2 依赖 abseil **之前**的最后一个版本
  (tag `2023-03-01`), 纯自足的 C++11。cgo 直接编那些 `.cc`, 没有第二套构建系统要伺候。
- **单一静态二进制, 可交叉编译。** 因为只是 C++11 + cgo, 拿 [zig](https://ziglang.org)
  当 C/C++ 工具链就能交叉编译。

唯一的硬要求是 cgo: 必须开着, 而且要有 C++11 编译器(clang / gcc / `zig c++` 都行)。
纯 Go / `CGO_ENABLED=0` 的构建根本用不了本库。

<a id="install"></a>

## 安装

```sh
go get github.com/hgmGoLib/hgmLibre2
```

要 Go 1.19+。cgo 必须开着(默认就是), 而且要有 C++11 编译器 —— clang / gcc / `zig c++` 任选。

<a id="usage"></a>

## 用法

```go
package main

import (
	"fmt"

	"github.com/hgmGoLib/hgmLibre2"
)

func main() {
	re := hgmLibre2.MustCompile(`(?P<key>\w+)=(?P<num>\d+)`)

	fmt.Println(re.MatchString("a=1"))                 // true
	fmt.Println(re.FindStringSubmatch("port=8080"))    // [port=8080 port 8080]
	fmt.Println(re.ReplaceAllString("x=1 y=2", "*"))   // * *  (repl 是字面量)
}
```

<a id="supported-api"></a>

## 支持的 API

下列方法与 `regexp` 同名同签名。匹配口径是 **leftmost-first**, 与 `regexp.Compile`
(RE2 默认的 Perl 模式)一致, **不是** leftmost-longest —— 例如 `(a|aa)` 在 `"aa"` 上给 `"a"`,
和 stdlib 一样。输入按 UTF-8 处理。

- `Compile` · `MustCompile` · `QuoteMeta`
- `CompileMaxMem` / `GetMaxMem` —— stdlib **没有**; **单条** pattern 的 RE2 内存预算
  (`Compile` 用 RE2 默认的 8 MB); 什么时候要紧见[反着扫](#scanning-backwards)
- `String` · `NumSubexp` · `SubexpNames`
- `MatchString`
- `FindString` · `FindStringIndex` · `FindStringSubmatch` · `FindStringSubmatchIndex`
- `FindAllString` · `FindAllStringIndex` · `FindAllStringSubmatch` · `FindAllStringSubmatchIndex`
- `ReplaceAllString`(repl 是**字面量** —— 没有 `$1` / `${name}` / `$$` 展开, 与 stdlib 不同)· `ReplaceAllStringFunc`
- `Split`
- 上面全部的 `[]byte` 对应版: `Match` · `Find` · `FindIndex` · `FindSubmatch` ·
  `FindSubmatchIndex` · `FindAll` · `FindAllIndex` · `FindAllSubmatch` ·
  `FindAllSubmatchIndex` · `ReplaceAll` · `ReplaceAllFunc` —— 见下面的
  [`[]byte` 系方法](#byte-slice-methods)
- `FindReplaceWithin` / `FindReplaceWithinBytes` / `AppendFindReplaceWithin` —— stdlib **没有**;
  在另一条 pattern 的每一处匹配**内部**做替换, 全程一次 cgo 调用; 见下面的
  [FindReplaceWithin](#findreplacewithin) 与
  [AppendFindReplaceWithin](#appendfindreplacewithin)
- `RegexpSet`(`NewRegexpSet` · `NewRegexpSetMaxMem` · `GetPatternLen` · `Match` · `MatchAny` ·
  `MatchBytes` · `MatchAnyBytes`)—— stdlib **没有**; 一个 DFA 一遍回答"这 N 条里哪几条命中";
  `MatchAny` 不要下标, 在**第一处**命中的位置就返回, 不扫到底; 见下面的 [RegexpSet](#regexpset)
- `RegexpSet.MatchStats` / `MatchStatsBytes`(`ScanStats`)与 `RegexpSet.GetMemInfo`(`SetMemInfo`)
  —— stdlib **没有**; **每次扫描**和**每个 Set** 的 DFA 计数器(flush 次数 · 建了多少状态 ·
  预算还剩多少); 见下面的[怎么量一个 Set](#measuring-a-set)
- `RegexpSet.Attrib`(`AttribInfo` · `PatternCost`)—— stdlib **没有**; 打开诊断编译宏之后能回答
  **是哪几条 pattern** 在造那么多 DFA 状态; 见[归因: 哪几条 pattern 在造状态](#attribution-which-patterns-build-the-states)
- `RegexpSet.FindAllIndex` / `FindAllIndexBytes`(`NewFindAllIndexAlloc` ·
  `RegexpSet_FindAllIndex_Alloc_t` · `RegexpSet_FindAllIndex_Run_t`)—— stdlib **没有**;
  一遍扫出 set 里每条 pattern 能在**哪些位置**结束, 分批交回; 见下面的
  [FindAllIndex](#findallindex-the-raw-end-point-runs)
- `RegexpSet.NewRe2Set_fll`(`Re2Set_fll_t` · `Scan` · `GetPatternLen` ·
  `GetViableOneStats` · `Close`, 外加共用的 `Re2Set_req_t` / `Re2Set_alloc_t` /
  `Re2Set_startEnd_t` / `Re2Set_stats_t` / `NewRe2Set_alloc` / `NewRe2Set_allocBatch`)
  —— stdlib **没有**; 上面那一层的成品: 一遍同时给出命中表**和**去重叠后的匹配区间,
  口径 **leftmost-longest**, 顶掉"`Match` + 每条命中 pattern 各跑一次 `FindAllStringIndex`";
  见下面的[正向 set 命中在哪](#where-a-set-matched-re2set_fll_t)
- `RegexpSet.NewRe2Set_frel`(`Re2Set_frel_t`, **函数签名完全一样**)—— stdlib **没有**;
  同样是一遍正向扫, 口径 **rightmost-end-longest**, 补起点是按**连通块**一次而不是按匹配端点一次
  —— 三个里通常最便宜的一个; 见下面的
  [一遍正向扫直接吐区间](#one-forward-pass-spans-straight-out-re2set_frel_t)
- `RegexpSetReverse.NewRe2Set_rrl`(`Re2Set_rrl_t`, **函数签名完全一样**)—— stdlib **没有**;
  同一件事从另一头做: 一遍反向扫给出去重叠后的区间, 口径 **rightmost-longest**,
  给那种正向爆炸、反向收敛的表用; 见
  [反向 set 命中在哪](#where-a-reverse-set-matched-re2set_rrl_t)
- 🔴 上面这三个就是**三种去重叠模式**; 怎么选、同一个输入上三者答案差在哪、各自什么价钱,
  见下面的[一遍扫完拿整段](#one-scan-whole-spans-the-three-de-overlap-modes),
  长版在 [`doc/三种去重叠模式.md`](doc/%E4%B8%89%E7%A7%8D%E5%8E%BB%E9%87%8D%E5%8F%A0%E6%A8%A1%E5%BC%8F.md)
- `RegexpSet.ResolveSpan` / `ResolveSpanWithin` / `ResolveSpanBytes`(`RegexpSetReverse`
  上同名的三个, 方向相反)—— stdlib **没有**; 用一次锚定提问把一个端点补成一整段,
  代价与输入长度无关; 见下面的
  [ResolveSpan](#resolvespan-complete-one-end-point-into-a-span)
- `GetPatternLenRange` / `RegexpSet.GetPatternLenRange` / `PatLenUnbounded` —— stdlib **没有**;
  一条 pattern 能匹配的字节长度区间; 见下面的 [GetPatternLenRange](#getpatternlenrange)
- `RegexpSet.GetViableOneStats` —— stdlib **没有**; 惰性建出来的那些"单条反向 set"
  (用来补左端)花了多少状态、多少字节
- `RegexpSetReverse.GetViableStarts` —— stdlib **没有**; **可行前缀回推**: 给一个匹配末端,
  把回看窗口里**全部**候选起点一次收齐(降序), 这是 `Re2Set_fll_t` 拿到严格 leftmost-longest
  的那一步; 见下面的 [GetViableStarts](#getviablestarts-viable-prefix-starts)
- `CompileLongest` / `CompileLongestMaxMem` / `MustCompileLongest` —— stdlib 没有这种**构造函数**
  (stdlib 的写法是 `re.Longest()` 这个 mutator); 把单条 pattern 编成
  **leftmost-longest(POSIX)** 口径而不是默认的 leftmost-first。两者挑的**起点相同**,
  只在末端不同。当你要的是"从这里开始的最长匹配"且想一次搜完(而不是搜两次)时需要它;
  要把区间喂给校验位算法时更是必须 —— 贪心给的短末端会把命中截断
- `Regexp.FindStringIndexAtWithin`(以及 `FindStringIndex_ctx_t` 上的同名方法)——
  stdlib **没有**; **锚定在 `from`** 且不越过 `bound` 的搜索, 偏移仍按原串计,
  所以 `\b` / `^` / `$` 看到的还是真邻居。配上 longest 模式的对象, 它就是单条 pattern 的
  `ResolveSpan` —— 但它走 `RE2::Match`, 因而继承了 set 那条路没有的 NFA 回落
- `RegexpReverse.ResolveSpanWithin` —— stdlib **没有**;
  `RegexpSetReverse.ResolveSpanWithin` 的单条孪生: 给一个匹配末端, 求最左的起点, 带上界。
  实现就是 `RE2::Match` 内部找左端时发的那一句(反向 program + `kAnchored` + `kLongestMatch`)
- `RegexpReverse.GetMemInfo` —— stdlib **没有**; 该 pattern 反向 program 的 DFA 缓存水位,
  从没走过就 `Built=false`(查询本身不会把它建出来)
- `GetDFAStats` / `ResetDFAStats`(`DFAStats_t`)—— stdlib **没有**; 进程级的 DFA 状态缓存
  flush 计数; 通常你要的是上面那两个 per-Set 的; 见下面的
  [DFA 状态缓存抖动](#dfa-cache-thrashing)
- `FindStringIndex_ctx_t`(`NewFindStringIndex_ctx` · `FindStringIndex` · `FindIndex`)——
  stdlib **没有**; 复用暂存的 `FindStringIndex`, 稳态零分配
- `AppendAllStringIndexFlat` —— stdlib **没有**; 不建中间 `[][]int` 的 `FindAllStringIndex`;
  见下面的 [AppendAllStringIndexFlat](#appendallstringindexflat)
- `StepAllStringIndex` / `StepAllStringSubmatchIndex` —— stdlib **没有**; `FindAll*` 的
  **`sqlite3_step` 式**形态: C 侧一次填一批命中交给回调, 内存里从来没有全部命中信息,
  零 Go 堆分配; 见下面的 [StepAllStringIndex](#stepallstringindex)
- `ReplaceAllStringFunc_ctx_t`(`NewReplaceAllStringFunc_ctx` · `AppendReplaceAllStringFunc`)——
  stdlib **没有**; 结果追加进调用方自己的 `[]byte` 的 `ReplaceAllStringFunc`, 稳态零分配,
  报的是**变没变**而不是**命中没命中**; 见下面的
  [AppendReplaceAllStringFunc](#appendreplaceallstringfunc)
- `RegexpReverse`(`CompileReverse` · `CompileReverseMaxMem` · `MustCompileReverse` ·
  `MatchString` · `Match` · `MatchStats`)—— stdlib **没有**; 一个**独立对象**, 给出的是与正向
  `Regexp` 相同的是/否答案, 但 DFA 是把**原缓冲区从后往前**走的; 见下面的
  [反着扫](#scanning-backwards)
- `RegexpSetReverse`(`NewRegexpSetReverseMaxMem` · `GetPatternLen` · `Match` · `MatchBytes` ·
  `MatchAny` · `MatchAnyBytes` · `MatchStats` · `GetMemInfo` · `GetAttrib`, 加上上面那几族
  `FindAllIndex` 与 `ResolveSpan`)—— stdlib **没有**; `RegexpSet` 的反向编译孪生,
  而且是**独立类型**: 从前它是带 `Reverse() bool` 的 `*RegexpSet`, 那让两个相反的含义
  共用了一个方法名; 见下面的[反着扫](#scanning-backwards)
- `FreeC` —— stdlib **没有**; 见[资源管理](#resource-management)

<a id="byte-slice-methods"></a>

### `[]byte` 系方法

`[]byte` 那一族是**同一个**匹配内核的第二张脸, 不是另一份实现。C 侧匹配只需要一个字节指针
加一个长度, 所以 `[]byte` 是直接交给 RE2 的: **任何输入尺寸下都没有 `string(b)` 拷贝**,
而结果(`Find` · `FindSubmatch` · `FindAll` …)是你输入的子切片, 共用同一块底层数组,
与 stdlib 的 `bytes` 系完全一样。两条后果值得记住:

- 调用在飞的时候不许改输入(与 stdlib 同一条规矩)。
- **无变化那条路**上, `ReplaceAll` / `ReplaceAllFunc` / `FindReplaceWithinBytes` 返回的是
  原来那个 `src` 切片, 零分配(这是本库一贯的惰性物化风格), 而不像 stdlib 那样新拷一份 ——
  所以别往返回的切片里写。要一个独立可写的缓冲就自己拷一份。其余一切, 包括
  `nil` vs 空切片的约定, 都与 stdlib 相同, 由 `bytes_test.go` 的差分测试钉住。

命名: stdlib 有的方法就用 stdlib 的名字(`FindIndex` ↔ `FindStringIndex`);
本库自己的方法加 `Bytes` 后缀(`FindReplaceWithinBytes` · `RegexpSet.MatchBytes`)。

<a id="regexpset"></a>

### RegexpSet

`RegexpSet` 把 N 条 pattern 编成**一个** DFA, 一遍非锚定扫描回答"这 N 条里哪几条命中",
而不是先拿 `(?:re1)|(?:re2)|…` 当粗筛再逐条扫第二遍。`Match` 返回命中下标(对应你传进来的
`patterns` 切片); 它**故意不返回位置** —— 要偏移的调用方事后对那几条命中的 pattern
跑 `FindStringIndex`。

```go
set, err := hgmLibre2.NewRegexpSet(patterns)         // RE2 默认预算: 8 MB
set, err := hgmLibre2.NewRegexpSetMaxMem(patterns, 32<<20) // 显式预算

var buf []int32                       // 跨调用复用 → 零分配
for _, text := range corpus {
    for _, idx := range set.Match(text, buf) {
        ...                           // idx 是 `patterns` 的下标
    }
}
```

只问"有没有命中"就用 `MatchAny` / `MatchAnyBytes`: 不回填下标(不传 buf), 底下 RE2 打开
`want_earliest_match`, 扫到第一处命中的位置立刻收工, 不把正文扫完(不命中仍然全扫)。
8 MB 正文而命中落在第 0 字节时实测 **2.12 µs** vs `len(set.Match(...)) > 0` 的 **11.18 ms**。
它与 `Match` 共用同一份 DFA 缓存, 不多占内存。要知道"是哪几条"就还得用 `Match` ——
早退和完整命中集不可兼得。

<a id="sizing-maxmem"></a>

#### `maxMem` 怎么定

`maxMem` 就是 RE2 的 `RE2::Options::max_mem`。`Prog::CompileSet` 会把它一分为二,
所以这一个旋钮同时抬高两个天花板:

- **编译期** —— 整张表的指令数上限
  (`(maxMem-sizeof(Prog))/4/sizeof(Inst)`, 封顶 2²⁴)。pattern 太多, 或者单条本身太重
  (大小写折叠的字符类 · `{n,m}` 重复), 就会超, `RE2::Set::Compile()` 失败。这是
  `NewRegexpSet` / `NewRegexpSetMaxMem` 返回 out-of-memory 错误的**唯一**原因,
  错误文本里带着 pattern 条数和当时的预算。
- **运行期** —— 剩下的那部分供 DFA 的状态缓存用。超**这一半**不是错误: DFA 把缓存 flush 掉
  接着跑, 结果一样。但它是**悬崖不是斜坡** —— 见
  [DFA 状态缓存抖动](#dfa-cache-thrashing) —— 而且你不数 flush 就看不见它
  (`set.GetMemInfo().FlushesTotal`, 或者进程级的 `GetDFAStats`;
  见[怎么量一个 Set](#measuring-a-set))。
  另见[为什么 `Match` 不返回 error](#why-match-has-no-error-return)。

所以: 如果你收到 "set compile failed (out of memory)", **不要把表拆成两个 set** ——
两个 set 意味着每份输入扫两遍, 那比内存贵得多。把 `maxMem` 翻倍直到编得过去
(8 → 16 → 32 MB)。这是编译期、每进程一次的代价, 建好之后 set 是只读且并发安全的。
容量大致随预算线性: 某张真实的 509 条表上, 1 MB 装得下 132 条, 2 MB 装 204 条, 4 MB 装全部 509 条。

⚠️ "编得过去"**不等于**"预算够大": 那只过了编译期那道天花板。运行期那一半够不够是另一个问题、
另一个答案, 而唯一的判定办法是数 flush —— 见下文。而且一旦一个 set 真的在抖动, 拆开它**反而**
是更好的交换(两遍热扫远胜一遍抖动扫), 所以上面那句"不要拆"只针对编译期那道天花板。

<a id="dfa-cache-thrashing"></a>

#### DFA 状态缓存抖动

当 DFA 的状态缓存装不下当前输入正在走的那个状态集时, RE2 **不是** LRU 淘汰:
`DFA::ResetCache` 把整个缓存扔掉从头重建。结果照样正确, API 上什么都不变 ——
这正是它能悄悄让你贵一个数量级、而且贵很久的原因。

有两条性质决定了它该被**数**而不是被推理:

- **它是悬崖, 不是斜坡。** 工作集超预算 1% 不是贵 1%。每次 flush 之后要重建几千个状态,
  每个状态都是一次 NFA 闭包计算。合成的 `kwN[\s\S]{0,8}tgtN` 表, 60 条 pattern,
  16 份互不相同的 64 KB 正文(`dfastats_test.go` 里的辅助函数), 某台机器上:

  | `maxMem` | flush 次数 | 吞吐 |
  |---------:|--------:|-----------:|
  |     1 MB |      79 |   8.1 MB/s |
  |     8 MB |       3 |  18.6 MB/s |
  |    64 MB |       0 | 164.7 MB/s |

  一兆输入上 3 次 flush 就值 9 倍。
- **单一形状的基准量不出来。** 拿**同一份**正文扫 N 遍, 第一遍就把缓存热了, 之后一个状态都不再建,
  于是每个预算看起来都一样。生产上每个请求是**另一份**正文, 每一份新形状都把状态集往外顶。
  基准要用一组互不相同的正文轮着扫, 而且看 flush 次数, 别只看吞吐。

`GetDFAStats()` 返回进程级计数器的快照; `ResetDFAStats()` 清零, 便于分段测量:

```go
hgmLibre2.ResetDFAStats()
for _, body := range distinctBodies {   // 单线程, 先跑一遍热身
    set.Match(body, buf)
}
st := hgmLibre2.GetDFAStats()
// st.Resets == 0  ⇒  这个预算在这份语料上装得下这张表。
// st.Resets  > 0  ⇒  maxMem 翻倍, 重建 set, 再量一次。
// st.LastStateBudget / st.LastCacheStates 是最近一次 flush 当时的预算和状态数 —— 工作集的下界。
```

`SearchFailures` 数的是另一种失败: DFA 整个放弃这次搜索。单条 `Regexp` 会回落到 NFA
(结果正确, 慢一个数量级), 而 `RE2::Set` 根本走不到那里(见下文)。

这些计数器是进程级的: RE2 的 hook 不带上下文, set 匹配更是完全没有, 所以只有单线程测量时
一段 delta 才归得到某一次扫描头上。并发时把它当速率读。要更细的, 用下面 per-Set / per-scan 的。

<a id="the-flush-is-not-the-whole-cliff"></a>

#### flush 不是全部代价

数 flush 是用来判断预算够不够的; 可一旦预算**确实**够了, 代价不会消失 —— 它只是挪了地方。
某张 223 条的表在 32 份互不相同的 80 KB 正文上逐次调用测量, 把调用分成"这次撞了 flush"和
"这次没撞":

- 撞了 flush 的那次只比没撞的慢 **1.3~2.8 倍**。flush 本身不是一个数量级。
- 数量级在**建状态**上。在 0 flush 的预算下: 第一遍扫全新正文, 每份 23.7 ms;
  第二遍扫**同一批**正文(一个新状态都不建), 每份 0.36 ms。这是 **66 倍**, 全程没有一次 flush。

所以缓存只在**重复正文**上才赚。全新正文一直在要新状态, 而且不收敛: 扫 1657 份互不相同的
真实记录, 每 KB 新建状态数从 84.7 降到 16.2 就不再降了。

实践结论: 把 `maxMem` 调到 0 flush 买到的是"不掉下悬崖" —— 这是必要的, 而且便宜。
它买不到那 66 倍。如果每个请求都带着新正文, 第一遍那个数就是你的天花板, 想再往下只能让**表**
少建状态(见[状态爆炸由什么驱动](#what-drives-state-explosion))或者少扫正文
(在大表前面加一道便宜的字面量预筛)。

<a id="measuring-a-set"></a>

#### 怎么量一个 Set

`GetDFAStats` 是进程级的。下面两个更细的计数器能归到某一个 `RegexpSet` 和某一次调用上,
不用全局变量也不用 `thread_local` —— 所以并发下、以及一个进程里握着很多 set 时都能用。

```go
// 每次扫描: 传一个栈上的 ScanStats。传 nil (即直接用 Match) 不要钱 —— C 侧根本不数。
var st hgmLibre2.ScanStats
hits := set.MatchStats(text, buf, &st)
// st.Flushes     本次调用里整表缓存被清空的次数 (>0 = 这次调用有一段慢了两个数量级,
//                而且它拿了写锁, 把同时扫这个 set 的其他 goroutine 全卡住了)
// st.Grows       缓存扩容次数 —— 这个【不丢】状态, 不是抖动
// st.StatesBuilt 本次调用建出来的状态数 (稳态 → 0)
// st.Bytes       输入大小; Bytes/StatesBuilt = 每建一个新状态吃掉多少字节正文
// st.StatesEnd, st.StateBudget, st.MemLeft   结束时的水位

// 每个 set: 累计值, 只读, 不会为了回答而去建 DFA。
mi := set.GetMemInfo()
// mi.FlushesTotal  扫完一批互不相同的正文之后 == 0 ⇒ 这个预算够
// mi.StatesBuiltTotal  "这张表有多贵"最直接的那个数
// mi.States, mi.GetUsedBytes(), mi.StateBudget, mi.ArenaCap
```

`mi.ArenaCap` 是真正向系统要到的内存, 而 `StateBudget` 只是个上限。它通常远低于预算:
把 `maxMem` 调大, 本身并不花常驻内存。

`StatesBuilt` 有个注意点: 它是共享 DFA 上一个计数器的差值, 所以同时在扫这个 set 的并发调用
也会被算进来。归因要单线程。

<a id="what-drives-state-explosion"></a>

#### 状态爆炸由什么驱动

做法是把某张真实的 223 条表改写之后在同一份语料上重扫, 预算取 0 flush 的档, 于是唯一变量是
pattern 本身:

- **容差窗口是主项, 大致线性。** 把表里每个 `{0,N}` 收到 `{0,2}`, 状态数从 51844 降到 4366
  (**11.9 倍**), 大约每加宽一格窗口 +700 个状态。自动机没有便宜的办法记住"我走进来多远了",
  所以 `X{0,W}Y` 会被展开 W 次。
- **形状把表干净地劈成两半。** 含 `{0,N}` 空隙的那 84 条占了 76.8% 的状态; 另外 139 条只占 **3.4%**。
- **合表是超加性的, 但只是轻微超。** 两半各扫建 32573 个状态; 合成一个 set 是 51844 ——
  **1.59 倍**, 不是乘积。pattern **条数**是二阶项; 窗口**宽度**才是一阶。
- **输入的熵不是驱动因素。** 全字节随机噪声根本进不了窗口, 几乎什么都不建。压缩过的
  JavaScript 是量过的语料里**最便宜**的一份: 87 KB 的 `jquery.min.js` 建 947 个状态,
  而 82 KB 真实文本建 3906 个。按偏移归因就知道为什么 —— 那份文件最烫的四段 1 KB 恰好是
  里面仅有的自然英语(许可证注释 · 一句异常字符串 · 一个 MIME 类型串 · 没被压掉的属性名)。
- **字符类驱动的分支才是陷阱。** 像 `(?-i:\([A-Z]{2,5}\))` 这种分支没有字面量锚点,
  代价就跟着语料恰好用了哪个字母表走 —— 它是唯一一条在压缩 JS 上比在真实文本上**更贵**的
  pattern。同一个"缺字面量锚点"也正是这种 pattern 过滤不掉的原因。给每个分支留一个真的字面量。
- **收窄窗口不会让单个状态变便宜。** 状态的"宽度"(同时有多少 NFA 线程活着)才是建一个状态的价钱。
  把整张表收到 `{0,2}` 让状态**数**降了 11.9 倍, 而平均宽度只从 37.3 动到 35.7(**4%**)。
  窗口宽度控制的是有**多少**个状态, 不是一个状态多贵。

<a id="attribution-which-patterns-build-the-states"></a>

#### 归因: 哪几条 pattern 在造状态

默认不编进去。带上这个宏重编, `GetAttrib` 就开始返回数据(不带的话 `Enabled` 是 false、
全是零 —— 默认构建里既没有字段也没有分支):

```sh
CGO_CXXFLAGS="-O2 -DRE2_DFA_ATTRIB=1" go build ./...
```

```go
a := set.GetAttrib()
for _, p := range a.Pats[:20] {   // 已经排好序, 最贵的在前
    fmt.Println(p.Index, p.Excess)   // p.Index 是你那个 patterns 切片的下标
}
// a.NInstHist / a.NInstMax  状态宽度的分布 = 单个状态的建造成本
// a.BirthHist[64]           输入每 1/64 段建了多少状态; 平的 = 整篇正文都在建状态,
//                           有尖峰 = 去读那几个偏移, 一眼就能看见是什么触发的
```

按 `Excess`(`= Insts - States`)排序 —— `Pats` 本来就是按它排好的。**不要**按 `States` 排:
非锚定搜索下 DFA 必须考虑"匹配从每个位置开始", 于是每条 pattern 的入口指令几乎在每个状态里都在,
`States` 对表里大多数条都饱和到 100%。

这个榜单做过消融验证: 按 `Excess` 摘掉前 20 条, 某张表从 51844 个状态降到 9606(**5.4 倍**),
而随机摘 20 条只到 36810(1.4 倍); 前 39 条是 11.5 倍, 随机 39 条是 1.03 倍。它还**跨语料稳定** ——
三份形态完全不同的语料在前 12 名里有 10~11 名一致 —— 所以拿任一份真实语料标定一次就够了。
状态的绝对值**不**能这样外推; 换语料能差 4~8 倍。

消融是**诊断**手段, 不是上线方案: 摘掉 pattern 就改变了你检测什么。用这个榜单去决定改写哪几条、
或者把哪几条隔离到自己的 set 里 —— 真要拆, 就按这个榜单或者按形状拆, **绝不要**按下标轮流分配。
拆表几乎不降**总**状态数(k=8 时 51844 → 44573); 它降的是**最大那一片**, 而预算要覆盖的正是它。
按下标轮流分的 k=8 拆法实测比不拆还**慢 3 倍**, 因为输入被扫了八遍。

<a id="why-match-has-no-error-return"></a>

#### 为什么 `Match` 不返回 error

`RE2::Set::Match` 用返回 `false` 来报告 DFA 失败 —— 除非你去问它的 `ErrorInfo`,
否则跟"什么都没匹配上"分不开。对一个检测器来说那是静默漏报的隐患, 所以值得把话说准:
**一旦 `Compile()` 成功, 匹配路径上的 DFA out-of-memory 就是不可达的**,
这正是 `Match` 那一族不返回 error 的原因。

vendored 的 RE2 里有两件事撑着这个结论:

- DFA 那条"缓存抖得太厉害, 回落到 NFA"的分支对 set 匹配是**显式关掉**的 ——
  `re2_dfa.cc` 用 `kind_ != Prog::kManyMatch` 守着它
  (*"RE2::Set cannot fall back, so we just have to keep on keeping on"*)。所以 set 扫描宁可
  无限次 flush 重建也不会失败。剩下的失败路径要求**单个**状态在一个刚清空的缓存里都放不下,
  而 DFA 的构造函数本来就拒绝在"预算装不下 20 个状态"时初始化。
- 那道构造检查是在**编译期**跑的, 不是首次使用时: `Compiler::CompileSet` 最后会跑一次
  `"hello, world"` 的 DFA 搜索(`Prog::kManyMatch`), 失败就返回 `NULL` ——
  恰恰因为 set 没有 NFA 可回落。所以预算太小会让 `Compile()` 失败, 你从构造函数那里拿到 error。

除了读源码, 也实测过: 上面那张 509 条的表, 用它**最小**可行预算(3.71 MB, 即 RE2 肯接受的
最少运行期缓存)编出来, 拿对抗性输入去扫(全字节随机 · 随机 ASCII · 近似片段 · 字节循环 ·
CJK · 单字符长游程; 每份 256 KB~8 MB, 分别加和不加针)。单是一次 8 MB 的扫描就逼出了
5 679 次状态缓存 flush。所有跑次合计: `ErrorInfo` 里零次 `kOutOfMemory`, RE2 自己的
`hooks::DFASearchFailure` 零命中, 命中集与逐条 `MatchString` 的 oracle 每次都完全一致。
同一个 DFA 失败 hook 在预算被饿着的**单条** `Regexp` 匹配上确实会响 —— 那里是无害的,
因为单个 `RE2` 会回落到 NFA, 答案仍然正确。

<a id="one-scan-whole-spans-the-three-de-overlap-modes"></a>

### 一遍扫完拿整段: 三种去重叠模式

`RegexpSet.Match` 说的是 N 条里**哪几条**命中。下面三个类型回答的是**在哪** ——
一遍扫过输入, 命中表**和**去重叠后的匹配区间一起给, 分批交回, 内存占用固定。

一遍扫交回来的是一个**端点集合**, 不是一串匹配: 没有起止配对, 没有优先级信息,
而且同一条 pattern 自己的匹配之间是重叠的(`aa|a` 在 `"aaa"` 上确实在 1 · 2 · 3 都结束)。
把它收成互不重叠的区间是一个**选择**, 而合理的选择不止一个。本库给了三种:

| | `Re2Set_fll_t` | `Re2Set_frel_t` | `Re2Set_rrl_t` |
|---|---|---|---|
| 名字怎么读 | **f**orward + **l**eftmost-**l**ongest | **f**orward + **r**ightmost-**e**nd-**l**ongest | **r**everse + **r**ightmost-**l**ongest |
| 第一遍扫 | 正向 | 正向 | **反向** |
| 开在谁身上 | `*RegexpSet` | `*RegexpSet` | `*RegexpSetReverse` |
| 锚在哪 | 最左**起点** | 最右**末端** | 最右**起点** |
| 输出顺序(同一条 pattern 内) | `Start` **升序** | `Start` **升序** | `Start` **降序** |
| 与 `re.Longest().FindAllStringIndex` 逐字节相同 | ✅ | ❌ | ❌ |
| 锚定回看次数 | 每个匹配**末端**一次 | 每个**连通块**一次 | 每个匹配一次 |
| 什么时候选它 | 你要 stdlib 那个一模一样的答案 | 默认选它 —— 命中密 / 表大 | 这张表正向爆炸、反向收敛 |

🔴 **`leftmost-longest` 只是三者之一 —— 它不是"正确的那个", 也不自动就是你要的那个。**
它唯一的特权是能与 stdlib 逐字节对照。它不是最快的(量过的 12 格里 11 格是 `frel` 赢),
也不是防截断防得最狠的那个(`fll` 和 `frel` 都防; 真把命中截短的是 2026-08-28 删掉的
`spanFast` 那条路)。

三者**函数签名完全一样**, 但**故意不做成** Go `interface` —— 形状一样是为了好学好换手,
不是为了可互换, 而且它们的输出顺序不同。

完整版 —— 三条规则在同一个输入上并排、各自为什么存在、实测价钱表、怎么选、怎么量、
以及各自是怎么被穷举 oracle 钉住的 —— 在
[`doc/三种去重叠模式.md`](doc/%E4%B8%89%E7%A7%8D%E5%8E%BB%E9%87%8D%E5%8F%A0%E6%A8%A1%E5%BC%8F.md)。

<a id="where-a-set-matched-re2set_fll_t"></a>

### 正向 set 命中在哪: `Re2Set_fll_t`

`RegexpSet.Match` 回答 N 条里**哪几条**命中。想知道它们命中在**哪**, 从前要为每条命中的
pattern 再扫一遍: 先 `Match` 把表缩小, 再对命中的那 k 条各跑一次正向 `Regexp` 的
`FindAllStringIndex` —— 整份输入 `1 + k` 遍, 而且那 k 遍是最贵的**非锚定**那种
(`.*?` 前缀让**每个**偏移都成为候选起点, 正是
[状态爆炸由什么驱动](#what-drives-state-explosion)里那根引信)。

这些位置其实第一遍就已经算出来又扔掉了: RE2 的 `kManyMatch` DFA 会记下"某条 pattern 能在
这个字节结束", 扫完就把这个记录丢了。本库把它留下来。6.4 MB 的正文上, 光 `Match` 是 18.5 ms,
`Match` **外加**收下每一个端点是 18.4 ms —— 在噪声之内, 也就是说位置是白送的。

`Re2Set_fll_t` 就是这件事的成品: 对输入**一**遍, 同时给出命中表和去重叠后的匹配区间,
分批交回, 内存占用固定。

```go
// 进程级: 一条策略一个, 建一次留着。它身上挂着惰性建出来的补起点用单条对象
// (最大那张生产规则表上 9.6 MB), 所以每遍新建 = 把这么多东西重编一次再扔掉。
// 一个对象上的 Scan 可以并发调; 策略变了就 Close 它 (原生侧引用计数, 在飞的扫描不受影响 ——
// 忘了 Close 还有 finalizer 兜底)。
ms, err := set.NewRe2Set_fll()

// 请求按值传。Allocer 是调用方 Go 侧的缓冲: 每个 goroutine 一个, 复用 (它【不能】跨 goroutine 共享)。
err = ms.Scan(hgmLibre2.Re2Set_req_t{
    Body:               body,
    Allocer:            alloc,
    ExistOnlyIndexList: []int32{3, 7},   // 可选: 这几条只要"命中没命中"
    StartEndResultFn: func(batch []hgmLibre2.Re2Set_startEnd_t) bool {
        for _, m := range batch {        // body[m.Start:m.End] 是第 m.Index 条 pattern 的一个真匹配
            handle(m.Index, body[m.Start:m.End])
        }
        return true                      // 返回 false 提前停 (不是错误)
    },
    HitIndexResultFn: func(ids []int32) {
        // 扫完之后交一次: 就是 Set.Match 会返回的那张命中表
        // (升序, 含 ExistOnly 那几行)
    },
    StatsResultFn: func(st hgmLibre2.Re2Set_stats_t) {
        // 本遍的 Walks / Cands / Tries / Emits。只有你设了它才会被调,
        // 所以生产路径不为它花钱。
        // 要盯的数是 st.Tries/st.Walks: 1.00 表示第一个候选起点永远就是答案。
        // 每张生产规则表上实测都是 1.00。前三个计数器只统计变长 pattern (定长的从不回看);
        // Emits 数的是每一个区间, 所以 Tries/Emits 【不是】"每次回看试了几次"。
    },
})                                // err != nil ⟹ 整遍作废; 拿 FindAll 重做
// 零值请求是合法的, 意思是"我什么都不要": 不扫也不分配。
```

**它买到了什么。** 两组测量, 出自**不同的表和不同的语料** —— 不要横向比。

一张生产形状的 set(90 条 pattern, 其中 52 条要区间, 7.03 MB), 三种模式在同一次运行里都量了:

| 模式 | 整条腿 | 相对老路 |
|---|---|---|
| 老路(`Match`, 然后每条命中 pattern 各扫一遍全文 `FindAll`) | 78.2 ms | 1.00× |
| **`span`**(默认, leftmost-longest) | 43.8 ms | **1.79×** |
| **`spanFast`**(2026-08-28 已删) | 24.6 ms | **3.18×** |
| 只当门(全部 `boolOnly`) | 21.9 ms | 3.57× |

这里三者交回的是完全相同的 10 956 个区间, 因为那张表里每条 pattern 两头都有锚。

某张真实的 155 条表在 6.4 MB 正文上(47 条命中, 稳态, 64 MB 预算): 整条腿从 **369.3 ms**
(`Match` + 逐条 `FindAll`)降到 **24.6 ms** —— **15.0 倍** —— Go 堆分配从 4.0 MB / 2252 个对象
降到 ~0 / 146 个。同一份语料按输入尺寸看: ≤ 8 KB 打平(1.0 倍), 32 KB 3.1 倍, 512 KB 6.1 倍,
2 MB 14 倍。量到的最差一格是 **0.94 倍**(慢 6%): 一个每 38 字节就命中一次的合成串,
几乎每个字节都在某个匹配里面, 每次命中两趟 cgo 过桥没有东西可摊。
🔴 这两组数都在 2026-08-28 换路之前; 它们回答的是另一个问题 —— *有这一层比没有这一层好多少*
(对照的是老的"门 `Match` 加每条命中 pattern 一次 `FindAll`") —— 而这个比值只变好了。
换路之后的数在 `doc/补起点换路的实测账_20260828.md`。

要紧的规矩, 全部由测试钉住(`re2set_fll_test.go` · `spanscan_*_test.go`):

- **交给回调的那个 batch 切片就是内部缓冲本身**, 下一批会原地覆盖它。要留就 append 到别处。
  这也正是这个 API 分批而不是"最后给我一个数组"的原因: 游程数随输入涨(那张表上约
  30 741 条/MB), 攒起来就等于让内存跟着文档尺寸走。不管输入多长, `Scan` 自己只占固定的
  12 KB 输出缓冲加底下 48 KB 游程缓冲。
- **结果是跨 pattern 交错的**, 按区间闭合的先后排。同一条 pattern 内部按 `Lo` 升序且互不重叠。
  想按 pattern 归拢是你那边一句 `append` 的事; 库这边做就得攒, 而攒正是分批要躲的东西。
- **`StartEndResultFn` 留 nil 是合法的** —— 那你只拿到命中表, 即 `Set.Match` 的语义,
  区间那部分的活一点都不干。零值请求(每个回调都 nil)意思是"我什么都不要": 不扫, 不分配。
- **一个对象上的 `Scan` 可以并发调**; **不**并发安全的是 `Re2Set_alloc_t` —— 每 goroutine 一个。
  后面那个 `RegexpSet` 是只读的, 随便共享。
- **`text` 只在 `Scan` 期间被引用**(补左端要读它); 扫完之后扫描器不再持有它。

<a id="object-lifetimes"></a>

#### 四层各自的存期(2026-09-01 分死的)

| 谁 | 是什么 | 存期 |
|---|---|---|
| `RegexpSet` | 整表那一份 `kManyMatch` DFA | 进程级 · 只读 · 随便共享 |
| `Re2Set_*_t` | 策略对象: set 引用 + 模式 + 长度区间表 + **补端点用的单条正/反向对象缓存**(惰性建, 最大那张生产规则表上 9.6 MB) | 进程级 · `Scan` 可并发 |
| `Scan(req)` | 这一遍的全部暂存(原生侧的 spanscan 工作区 · 游程缓冲 · 候选缓冲 · 游标), 它自己现开现关 | 每次调用一份 · 调用方看不见 |
| `req.Allocer` | Go 侧的输入输出缓冲 | 调用方持有 · **一个 goroutine 一个** |

- 🔴 **别每遍新建 `Re2Set_*_t`** —— 那是每篇正文把那 9.6 MB 单条对象缓存重编一遍再扔掉。
- 🔴 每遍那份原生暂存走 `malloc`/`free`, **不池化**: 二十来次分配, 最大一块几 KB,
  离 glibc 那道 128 KB 的 mmap 门槛远得很, tcache/smallbin 就已经是 `sync.Pool` 的效果了,
  而且不欠 GC。调用方那侧试过"带上限的空闲链"和"每遍现开现关"两版 —— 前者池的是暂存
  (只涨不缩, 常驻按并发份数乘), 后者扔的是缓存; 拆成上面这四层之后, 两难就不存在了。
- 🔴 `Close` 是**引用减一**(原生侧计数): 手上还在跑的 `Scan` 各攥着一份, 最后一个走的人才真拆,
  所以换策略的时候尽管 `Close` 旧的。忘了 `Close` 也不漏(finalizer 兜底, 只是释放时机交给 GC)。
  `Close` 幂等, 之后再 `Scan` 返回 `err`。

**`Re2Set_alloc_t` 是接口处的纯缓冲。** 里面只有 Go 侧那几块数组(区间批缓冲 · 命中表缓冲 ·
`ExistOnly` 位图), **一个原生句柄都没有** ⟹ 同一个 alloc 可以**跨对象、跨表**自由传,
不存在"这个 alloc 不是这张表的"这种运行期错误。(注意别和
[`FindAllIndex`](#findallindex-the-raw-end-point-runs) 那个更底层的
`RegexpSet_FindAllIndex_Alloc_t` 搞混 —— 那一个**是**绑在建它的 set 上的, 配错了会报错。)

- 🔴 alloc **不是**并发安全的: 一个 goroutine 一个。
- 🔴 `Allocer` 传 `nil` 的默认档是**现建一个用完就扔**, **不是** `sync.Pool`:
  池的存期是一轮 GC, 而这条路真正的用法是"下一份 ≥4 KB 的正文", 中间必然隔着若干轮 GC ⟹
  一次都命中不了, 等于把这条路上最大的一笔开销藏进默认档。要复用就自己持有一个, 显式地持有。
- `NewRe2Set_allocBatch` 那个批大小**只决定过桥次数, 不影响结果**。生产上没有理由去掐它;
  它存在只是为了让"一次交一条和一次交四千条必须逐处相同"这条回归写得出来。

<a id="existonlyindexlist-a-pure-cost-switch"></a>

#### `ExistOnlyIndexList`: 纯成本开关

`req.ExistOnlyIndexList` 点名那些只要**"命中没命中"**的 pattern 下标:

| 在名单里? | 什么意思 |
|---|---|
| 不在(默认) | 给我区间。**无条件 leftmost-longest** —— 没有哪种 pattern 形状能例外。 |
| 在 | 我只要"命中没命中"。不闭合任何区间, 不补任何端点 —— 但这一行照样出现在命中表里。 |

🔴 2026-08-28 之前这是个构造时定死的三态旋钮(`SetModes`), 第三态
`MatchScanMode_spanFast` 强制走一条便宜的游标路, 它**不保证** leftmost-longest,
还要求调用方先逐条 fuzz 出一个安全余量。那个模式**没了** —— 顶掉它的那条路既严格
leftmost-longest **又**比 `spanFast` 更便宜(见下面的*三条路怎么并成一条*)——
剩下的两态在 2026-09-01 变成了现在这个 per-call 名单。

🔴 它是**每次调用**的参数, 不是构造时的属性。"先问在不在, 在了再问在哪"是用这一层最自然的方式;
有了 per-call 名单, 一个对象就能回答两个问题, 而不必开两个对象、养两份缓存。

`ExistOnly` 省下来的是真金白银: 它跳过的是**补端点**, 也就是贵的那一半, 不只是少几条结果。
大表里很多条本来就是纯布尔("这类内容到底出没出现过"), 从来没人问它们命中在哪;
在上面那张表上, 光两条这样的 pattern 就占了全部游程的 **57%**。
在回调里过滤替代不了它: 到那时候钱已经花完了。

<a id="what-scan-guarantees"></a>

#### `Scan` 保证什么

长版(中文): [`doc/fll的leftmost-longest保证.md`](doc/fll%E7%9A%84leftmost-longest%E4%BF%9D%E8%AF%81.md)。

对每一条不在 `ExistOnlyIndexList` 里的 pattern, 交给你的区间满足:

1. `body[Start:End]` 是那条 pattern 的一个**真**匹配;
2. **同一条 pattern 的**区间互不重叠, 且按 `Start` 升序;
3. 口径是 **leftmost-longest** —— 即 `re.Longest().FindAllStringIndex`。

这里有三处容易读错:

- (2) 是**按条**的。**不同的**两条 pattern 之间照样自由重叠; 那不是重复, 那是两个问题各要一个答案。
  🔴 **只在同一条 pattern 内部去重, 跨 pattern 一概不合并** —— 这条量过: 6.4 MB 真语料上
  74 249 处命中里, 被 **≥2 条** pattern 盖住的字节占已盖住字节的 **55.6%**, 同一个字节最多被
  **8 条**盖。而"哪一条该赢"要等消费点把校验位跑完才知道: `"Passport No: A123456780"` 上
  台湾身份证规则和护照号规则抢的是**完全同一段** `[13,23)`, 身份证那条下标在前会先占坑,
  可它自己又过不了 mod-10 校验 ⟹ 这段明文护照号一条都不出 = **静默漏报**。所以跨条合并归
  调用方(消费点按自己的优先级序合并, 脱敏那层再按位置收一次), 库这层不替它决定。
  "带分隔符的"和"不带分隔符的"两条 pattern 同理 —— 下游正是靠这个差别分流, 合了就是漏检。
- (3) **不是**"和 `FindAllStringIndex` 一样"。stdlib 的默认口径是 leftmost-**first**(贪心),
  凡是"同一个起点上贪心的第一个匹配比最长的那个短"的地方两者就不一致。对拍要拿
  `Longest()` 比, 否则量出来是假红。
- 这一切里**没有零长的情况**: 能匹配空串的 pattern 在全库每一个编译入口都被拒
  (见[拒收可空 pattern](#empty-capable-patterns-are-rejected)), 所以这一层**无条件**假设
  每个匹配至少 1 字节。

端点是怎么补的, 按 pattern 分档:

| pattern 形状 | 档 | 怎么补 |
|---|---|---|
| `min == max`(定长) | 任何 | `Start = End - min`, 一句减法, 根本不进正则引擎。 |
| 变长 | 要区间(默认) | **两步。** ① 从末端 `e` 起做一次反向扫描, **把全部活状态都种上**, 一路向左走到机器死掉为止, 收下 `[游标, e)` 里**所有**可行前缀起点(`RegexpSetReverse.GetViableStarts`, 跑在一个**只含这一条** pattern 的反向 set 上)。② 把这些候选**按升序**逐个拿去做锚定的 longest 正向搜索, 第一个验过的就是答案。升序 ⟹ 最左; longest 模式 ⟹ 最长末端。对任何 pattern 形状都严格 leftmost-longest, 而且**不需要 `maxLen`** —— 回看的下界就是反向机死掉的地方。代价: 回看窗口两两**不交**(合计不超过在正文上多走一遍)加上验掉几个假候选 —— 每张生产规则表上实测每次回看试 `1.00` 次。 |
| — | — | 单条对象编不出来的话, 就是 `maxMem` 太小, `Scan` 让整遍失败。 |

**有一条规矩管着这一切(2026-08-27): 在正文上扫的那一遍用 set; 之后每一次补端点的调用都用
那条 pattern 自己的单条对象, 绝不用 set。** 三个理由:

1. 单条对象走的是 `RE2::Match`, 它会 DFA → OnePass/BitState/NFA 逐级回落。而 set 的锚定
   resolve 是一个 `kManyMatch` DFA **且仅此而已** —— 上游也一样(`re2_set.cc:216`:
   `dfa_failed` ⟹ `return false`)—— 所以在那里"DFA 放弃了"只能让整遍失败。改完之后
   还会撞上这条的只剩反向回看, 因为 RE2 自己也没有别的办法去找匹配的左端。
2. 补端点的流量不再搅动整张表那个共享的大 DFA 缓存。
3. 状态更小: 单条 pattern 不背 `kManyMatch` 的每状态 id 列表。

答案逐字节不变(`TestMatchScanPathsSameAsSetRoute`: 每条路 300 份 AST 生成语料, 54 000 个区间,
零差异), 而命中密集的语料便宜了 27~36%。🔴 那个测试在 2026-08-28 随着它对拍的那两条路一起删了;
同一批 AST 生成语料现在钉在 `re2set_fll_astfuzz_test.go` 上, 对的是 stdlib 的 `Longest()` ——
比"拿自家旧代码的复制品"硬一档的 oracle。

<a id="how-the-three-paths-became-one"></a>

#### 三条路怎么并成一条(2026-08-28)

定长那一档的相等是**可证**的, 不只是实测出来的: 一个末端 `e` 只有唯一可能的起点 `e-min`,
于是起点随末端单调, 而"贪心地取最左的不重叠区间"恰好就是游标产出的东西。它拿
6 万条随机定长 pattern 对着 `FindAllStringIndex` 对拍过(`TestRe2SetFllStrictVsFindAll`)。

变长那一档, 配对关系必须在 Go 侧**重建**, 而**怎么**重建决定了你得到什么语义。这一遍扫交出来的是
*末端偏移的集合*, 里面既没有起止配对也没有优先级信息 —— RE2 的贪心性活在 NFA 的指令优先级顺序里,
而 `kManyMatch`(唯一会报告**全部**末端的模式)把那个丢掉了。2026-08-28 之前三种重建并存:

| | 怎么做 | 语义 | 贵在哪 |
|---|---|---|---|
| **路 A**(`spanFast`) | 反向机**只种 accept** → 一个起点, 再做一次锚定的 longest 求末端 | **第三种口径** —— 既不是 leftmost-first 也不是 leftmost-longest | 每次命中两趟调用, 回看窗口**相交** |
| **路 B**(老默认) | 从 `max(游标, e-maxLen)` 起做一次正向**非锚定** longest 搜索 | leftmost-longest | 没有长度上界的条必须**走完空隙**(最多到正文的 2.00 倍) |
| **路 D2**(独立类型 `Re2Set_fll_t2`) | 就是上面那张表现在描述的做法 | leftmost-longest | 要验掉假候选(真表上从没发生过) |

路 A 的缺陷是结构性的: 只种 accept, 看见的只是"匹配**恰好**在 `e` 结束"的那些起点。
`\b(?:ab cd ef|cd)\b` 对 `"ab cd ef"` —— set 报出来的最小末端是 `"cd"` 那个(偏移 5),
于是回看最多只能够到 3, 而真正最左的起点是 0(`text[0:5)="ab cd"` **不是**一个匹配,
但它**是**一个可行前缀: 后面接上 `" ef"` 它就是了)。只有把每个活状态都种上才看得见这个候选,
而那正是上面第 ① 步做的事。

**并路的凭据** —— 11 份 100 MB 量级语料(web 前端构建产物 · 凭据密集的八种生成器 ·
源码/说明书/原生可执行文件混合 · 一份真实的长聊天记录)× 9 张**生产**规则表 = 99 格。
原始报表在 `doc/补起点换路的实测账_20260828.md`。

- **口径。** 把区间排成规范的 `(条, Lo, Hi)` 序之后逐处比: D2 对路 B —— **1.619 亿**处区间,
  **零**差异。D2 对路 A —— **37** 处差异, 全在源码/说明书/ELF 那份语料上, 而且每一处都是路 A
  **把左端截短了**: 阿联酋身份证给成 `1985-1234567-1` 而真答案是 `784-1985-1234567-1`;
  提示注入标记给成 `<SYS>` 而真答案是 `<<SYS>>`。🔴 这正是本文一直在警告的那种伤 ——
  把偏了的区间喂给校验位(mod-10 / Luhn / mod-97)会算不过, 于是下游把一条真命中丢掉,
  你得到一次**静默漏报**。换路不只是省时间; 它**在真实文本上修掉了 37 个错边界**。
  🔴 **不要**按吐出来的先后比: 保证只管到同一条 pattern **内部**的顺序, 所以流式对比量到的是
  "排列不同"而不是"区间不同"(它把某份 8 MB 语料的全部 8.5 万处都判成了不一致; 排完序一处不差)。
- **价钱。** 每条表链合计, 11 份语料里 D2 全都是最快的 —— 相对路 A `0.48~0.91×`,
  相对路 B `0.6~1.0×` —— 而 `Tries/Walks` 在 99 格里全是 `1.00`。
- **内存。** D2 要的是每条 pattern 一个*反向 set*(`vp1`); 路 A 要的是每条 pattern 一个
  *反向对象*(`rev1`)。同一个数量级: 最大那张表(158 条, 其中 89 条真被问过位置)上
  9.6 MB vs 7.6 MB。相对路 B 这是**净增**, 因为 B 根本不建反向对象。用 `GetViableOneStats()` 量它。

随那两条路一起删掉的还有: `MatchScanMode_spanFast`、反向那侧 `SetModes` 里拒它的守卫、
`Re2Set_fll_t2` 这个类型、`RegexpSet.reverseOne` / `ReverseOneStats` / `rev1`,
以及那些只为对拍而存在的测试。(`SetModes` 本身连同剩下两个模式在 2026-09-01 也没了 ——
见上面的 `ExistOnlyIndexList`。)

**从前"加锚"能把路 A 的问题整个消掉** —— 把变长 pattern 包进词边界(`\b(?:…)\b`)就把起点钉死了,
"该挑哪个起点"根本不会发生: 12 万处区间的对比里, 裸 pattern 那 60 处差异变成 **0**。
现在这变成了 pattern 自己的性质而不是对调用方的要求: 定长区间一向可以直接拿去切,
变长区间现在也可以, 因为它严格 leftmost-longest。

<a id="i-could-not-give-you-everything"></a>

#### "我给不全你": 只有一种说法

🔴 没有"我中途放弃了, 这几条你自己补"这种中间态(2026-08-27 删掉了)。只有一种:

| 在哪 | 它说的是 |
|---|---|
| `Scan` 的 `err` | 这一遍作废 —— 你已经收到的那些批次也不算数。整份输入拿 `FindAll` 重做。 |

🔴 2026-09-01 之前还有第二种: 扫描器构造函数会交回一份 `unsupported` 名单
(那些能匹配空串、因而产不出区间的 pattern)。现在可空 pattern 在全库每个编译入口都被拒,
这种 pattern 根本进不了 set —— 名单以及它背后那整条逃生通道都没了。

`Scan` 的 `err` 有三个来源, 全都是"`maxMem` 太小": 扫描那一遍自己放弃了;
补另一端要用的两个单条对象之一编不出来; 反向回看放弃了。

🔴 "反向 set 编不出来"这件事**故意不给**回落。2026-08-28 逐字节量过: 590 条生产 pattern 里
只有 **16** 条反向比正向贵(全是开口的 `{n,}` 形状, 集中在一张表里), 最大比值 **1.021×**。
要让"正向 set 编得出来但反向单条 set 编不出来"真的发生, set 里必须基本只有**一**条 pattern,
**且** `maxMem` 正好落在那条 pattern 自身阈值往上 2% 宽的带子里 —— 找到的最坏那条
(三段式 JWT)那条带子宽 **74 字节**: 正向 3580, 反向 3654。表里但凡多几条 pattern,
正向 set 的代价就贵好几个数量级, 这个窗口根本不存在。走到这条分支只可能是调用方
把 `maxMem` 配错了 —— 那该报出来, 而不是靠偷偷换一个实现去糊上。第四种,
"游程没有按扫描顺序单调到达", 是**本库内部不变量被破坏** —— 那是 bug,
同样从这个 `err` 报出来而不是吞掉。

**为什么不做"部分成功"?** 一个调用方**造不出来**的错误码不该出现在返回值里。它逼出这条链:
调用方必须写兜底 → 兜底永远跑不到 → 跑不到就没法测 → 没测过的代码基本是错的 →
真到要紧那天, 执行走进了一条从没执行过的路。那比"没有兜底、整遍失败"更糟。

而锚定 resolve 放弃这件事根本诱发不出来: 它跑的是**小** DFA(单个起点, 不是全文扫描)。
三种形状(`ab` · `[A-Za-z][A-Za-z0-9]{2,19}key` · `(?i)[a-z0-9]{3,20}@[a-z0-9.\-]{3,20}`)
在各自刚编得出来那道墙往上 3 000 字节的带子里每 100 字节取一档(`maxMem` 2400 / 5800 / 24400),
在一份 60 KB 带 CJK 的正文上每隔 3 个偏移 resolve 一次 —— **零**次放弃。墙以下,
`NewRegexpSetMaxMem` 会干脆利落地失败。program 和 DFA 是从同一个 `maxMem` 里出钱的:
program 装得下, 剩下的就够一次 resolve 走的那几个状态; 装不下, 你根本拿不到 set。
中间没有窗口。

🔴 而且不, 它不能"回落到 NFA": set 那条路上**没有** NFA。单条 `RE2` 会回落
(见 `re2_re2.cc` 里那串 `Fall back to NFA below`), 但 `RE2::Set::Match` 在上游也是 DFA-only
(`re2_set.cc:216` —— `dfa_failed` 直接返回 false)。NFA 接口回答不了**是哪条** pattern 命中,
而 `kManyMatch` 的 id 是从 DFA 状态里那张 id 列表出来的。要给 set 的锚定 resolve 一条 NFA 路,
就得为每条 pattern 另编一个 `\A(?:pat)` 的 `RE2` —— 那正是 `ResolveSpan` 存在的意义所在。

🔴 **故意没有**"最后给我一个数组"这种入口(`AppendAllMatches` 2026-08-27 删了)。
这种方便形状总会从测试和测量里爬进生产, 而它的 `dst` 是一个跟着输入长度走的棘轮缓冲
(上面那张表上约每 MB 输入 0.037 MB)—— 正是分批接口要躲的东西。要数组的调用方在自己的
`Scan` 回调里加一行 append, 代价对写它的人是可见的。

<a id="findallindex-the-raw-end-point-runs"></a>

#### `FindAllIndex`: 原始端点游程

`Re2Set_fll_t` 建在更底下一层上, 那一层也导出了, 给想自己定配对或重叠策略的调用方用:

```go
alloc, _ := set.NewFindAllIndexAlloc()   // 可复用的工作区; 非并发安全
defer alloc.Close()

err := set.FindAllIndex(body, alloc, func(runs []hgmLibre2.RegexpSet_FindAllIndex_Run_t) {
    for _, r := range runs {
        // 第 r.ReIndex 条 pattern 在 r.Lo..r.Hi 里的每个偏移上都有一个匹配端点 (两端都含)
    }
})
```

- 一条游程是 `{ReIndex, Lo, Hi}`: 第 `ReIndex` 条 pattern 在 `Lo..Hi` 里**每一个**值上都有匹配端点,
  这是按原输入字节偏移计的**闭**区间(永远 `Lo <= Hi`)。是闭区间而不是半开, 因为这是被收敛起来的
  一串**点**, 不是一个区间 —— `Hi` 本身就是一个真端点。
- 说的是哪一端由 **set 的方向**定, 不由字段名定: 正向 `RegexpSet` 报的是匹配**末端**
  (不含, 即 `text[?:Hi]` 是一个匹配), `RegexpSetReverse` 报的是匹配**起点**(含)。
- 两端都报, 因为收成一端会**无声地**丢匹配: `ab|c` 在 `"abc"` 上在 2 和 3 结束, 两者连号,
  只留 3 就悄悄丢掉了 `[0,2)` 那个匹配。
- **这不是 `FindAllStringIndex` 的语义。** 那个返回的是 leftmost-first 的不重叠序列;
  这里返回的是**所有** pattern 的**所有**端点, 含重叠(`abcd|bc` 撞 `"abcd"` 两条都报)。
  在重叠命中之间取舍是策略, 库不替你决定 —— 要成品区间就用 `Re2Set_fll_t`。
- 顺序**不是**全局升序(一条游程只有在它那条 pattern 不连号地再次命中、或者输入结束时才闭合,
  所以各 pattern 交错), 但同一条 pattern 内部**是**升序 —— 上面那层游标依赖的就是这一条。
- 偏移是 `int32`: 这是原生宽度(于是 C 侧直接写缓冲, 不用逐条转换), 带符号是因为
  `end - minLen` 在正文开头是真的会为负, 而且 RE2 本来就把输入封在 2 GiB。
- `alloc` 可以是 `nil`(那每次调用建一个用完扔, 花一次原生分配); 热路径上留一个。
  它绑在建它的那个 set 上 —— 拿去配另一个 set 会返回错误而不是给出错答案 —— 而且它
  **非并发安全**。`FindAllIndexBytes` 是零拷贝的 `[]byte` 孪生。
- 批大小固定 4096 条(48 KB), 故意不做成旋钮。原生侧像 `sqlite3_step` 那样挂起 ——
  按值存下 DFA 状态, 放掉 DFA 缓存的读锁, 返回 Go, 下次调用再续上 —— 所以你的回调跑的时候
  没有任何锁被持着。
- 回调没有"提前停"的返回值。只要是/否答案的话, `MatchAny` 在 RE2 内部第一处命中就停,
  比任何 Go 侧刹车都早。

<a id="resolvespan-complete-one-end-point-into-a-span"></a>

#### `ResolveSpan`: 把一个端点补成一整段

```go
end, ok, err := set.ResolveSpan(text, start, id)              // 正向: 起点(含) -> 末端(不含)
lo, ok2, err2 := rev.ResolveSpan(text, end, id)               // 反向: 末端(不含) -> 起点(含)
pos, ok3, err3 := set.ResolveSpanWithin(text, from, bound, id) // bound = 最多看多远, <0 = 不限
```

这是**在一个偏移上的一次锚定提问**, 不是扫描: 代价取决于那一个匹配能伸多长,
**与输入长度无关** —— 在 1 KB 的输入上问和在 6.4 MB 的输入上问一样贵。它返回的是那个端点上
**最长**的匹配, 不是最先找到的那个, 因为停在第一个上给的是最短区间, 那会把命中截断,
让下游的定长校验或者校验和把一条真命中拒掉。`ok == false` 表示这条 pattern 在那个端点上
根本不匹配(偏移给错了, 或者 `id` 给错了)。它只读, 可以与其他 goroutine 上的扫描并发调用。
`ResolveSpanBytes` 是 `[]byte` 孪生。

拿它来补端点; **不要**为了同一件事去对整份输入跑一遍反向 `FindAllIndex`
(那份 6.4 MB 正文的整表反向扫要 65 秒, 正向 18 毫秒; 而逐条 pattern 的反向扫等于把这个 API
消掉的 `1 + k` 遍又装回去)。同样, 也别在 Go 侧自己重建: 非锚定的
`re.FindStringIndex(text[from:])` 保留着 `.*?` 前缀、会一直扫到输入结束;
而手写 `\A(?:pat)` 意味着第二个 `Regexp` 对象、它自己的 DFA 缓存, 外加一份要你手工维护的
语义等价性。`ResolveSpan` 用的是 set 自己的 program 和 DFA 缓存, 走它的锚定入口。

`ResolveSpanWithin` 的 `bound` 限制解析最多能看多远(正向是右界, 反向是左界)。
正是它让那些可以无限伸展的 pattern(`(?s).*KEY`)变成常数代价而不是 O(输入)。
匹配上下文永远是**整份**输入, 所以 `\b` · `^` · `$` 看到的仍是真的邻居字节 ——
一个 bound 只会让答案更短, 不会让它变错。

`RegexpReverse.ResolveSpanWithin` 是这一族的**单条**孪生: 给一个匹配末端 `from`(不含),
返回最靠左的那个起点(含), `bound` 是回看的左下界(负数 = 不限)。语义与 `RegexpSetReverse`
上那个同名方法逐字相同, 差别只在对象是一条 pattern 还是一张表; 实现就是 `RE2::Match`
自己求匹配左端时发的那一句(反向 program + `kAnchored` + `kLongestMatch`)。
🔴 **别拿"只装一条 pattern 的 set"去凑这件事**: 那是 `kManyMatch` 的 DFA
(每个状态都多背一张 id 表), 而且 set 和单条对 `^` / `$` 的处理**不是同一条代码路** ——
单条 `Compile` 会把 `^` / `$` 摘成两个标志, 只有 `SearchDFA` 才去查它们。

<a id="getviablestarts-viable-prefix-starts"></a>

#### `GetViableStarts`: 可行前缀回推

```go
n, err := rev.GetViableStarts(text, from, bound, id, out)  // out 收到的是【降序】的候选起点
// n 是找到的总条数, 可能 > len(out) —— 那就是缓冲不够, 换大的重来一次
```

给一个匹配末端 `from`, 把 `[bound, from)` 里**全部**候选起点一次收齐: 即那些位置 `s`,
使得 `text[s:from)` 是第 `id` 条的一个**可行前缀**(还能被某个后缀补成真匹配, 不一定当场就是匹配)。

- 🔴 与 [`ResolveSpan`](#resolvespan-complete-one-end-point-into-a-span) 的差别只有一处,
  但是决定性的: `ResolveSpan` 的反向机器**只种 accept**, 所以只认"正好在 `from` 结束"的那些起点;
  这一个**把全部状态都种上**, 连"路过 `from` 的更长匹配"的起点也认。前者是后者的子集,
  而**最左的起点到底在哪**只有问后者才问得对 —— `\b(?:ab cd ef|cd)\b` 撞 `"ab cd ef"`:
  第一遍给出的最小末端是 `"cd"` 那处(偏移 5), 只种 accept 只能回推到 3,
  而真正最左的起点是 0(`text[0:5)` = `"ab cd"` 不是匹配, 但**是**一个可行前缀)。
  2026-08-28 之前 `Re2Set_fll_t` 那一档 `spanFast` 走的正是"只种 accept"这条路,
  上面这个例子就是它那个"第三种口径"的病根。
- 🔴 这一步和 `ResolveSpan` 一样**只能在库里做**: 种全部状态要的是 DFA 起始状态的构造权
  (`re2_dfa.cc` 的 `start_[kStartViable]`), 从外面根本够不着。而且种的是**锚定入口的可达闭包**,
  不是"把 program 里 1..size 全塞进去" —— set 的 program 还挂着一截 `.*?` 非锚定前缀,
  种上它机器就永远死不掉, 回答的也变成了另一个问题。
- 代价 = 这处命中能往回够多远(可行前缀集合空了机器就死), **与输入长度无关** ——
  与 `ResolveSpan` 同一个量级、同一个道理。`Re2Set_fll_t` 严格的 leftmost-longest 就是靠它换来的。

<a id="the-substrate-layer-benchmarks"></a>

#### 底座那一层的性能对照

`spanscan_bench_test.go`: 64 KiB 正文 · 10 条通用 pattern · Ryzen 5900X · 稳态复用。
对照的"旧实现"就是调用方常见的那一套 —— `set.Match` 当门, 再对每条命中的 pattern 在**整篇**
正文上跑 `FindAllStringIndex`(命中 k 条 = `1+k` 遍全文, 而且后 k 遍是最贵的非锚定扫描)。

🔴 这一档的"推荐用法"只适用于**要与 `FindAllStringIndex` 逐字节等价**的调用方 ——
反向 set 扫 + 一次从左到右的推进(rev-cov, 判据抄在 `re2set_fll.go` 的注释里),
与 `FindAll` 逐处全等(`TestSpanPerf_Shape` 直接对账), 三档都不劣于旧实现:

```text
                      0 命中     稀疏(39 处)   最坏输入(见下)
旧实现                 93.1us      400.8us      596.0us  给 4 处
反向扫 + 左到右推进    89.7us       94.7us      611.7us  给 4 处   ← 推荐
只扫不解析             94.3us       94.7us      271.1us
扫 + 每游程解析一次    93.9us       97.7us      637.2us  给 4 处
同上 + bound=64        89.2us       92.1us      271.1us
```

全部 0 B/op 0 allocs/op(工作区复用 + 两处出参都走按值返回的孪生, 见 `VENDOR.txt`)。怎么读:

- **0 命中** —— 产线绝大多数正文长这样。两边都是一遍扫, 打平(±3% 噪声内)。
- **稀疏命中** —— **4.2 倍**。省掉的正是旧实现那 `1+k` 遍全文重扫。
- **最坏输入** —— 语料是**全小写无空格**, 表里 `[a-z]{4,}` 一口吃掉整篇。这一档打平: 旧实现
  `FindAll` 内部同样要为那 4 处各回走一趟同样长的正文, 谁也占不到便宜。想更快只能掐 `bound`
  (给"能无限延伸"的 pattern 配一个回看上限, 解析成本从 O(正文) 钉回常数)——
  那是调用方自己声明的取舍: 比上限长的命中会被丢掉。
- **"左到右推进"** 省的是**同一处命中被拆成多条游程**那种冗余: 变长尾巴每走到一个可收的位置
  就成一条游程(`[a-z]{5,}ing` 撞一大段小写 = 每个 `"ing"` 各一条), 逐条各解析一次就是
  O(游程数 × 正文长); 推进之后落在上一处里面的游程直接跳过, 压回 O(正文长)。

🔴 反过来推是**错的** —— 正向 set 从右往左推给的是 rightmost-longest, 不是这个口径:
`abc?|bcd` 撞 `"abcd"` 时 `FindAll` 给 `[0,3)="abc"`, 反着推给 `[1,4)="bcd"`。
两个都自洽, 但需求要拿这一段做定长校验时, 差一个字节就判成另一回事。
钉死在 `TestSpanPerf_CovDirection`。

🔴 还有一档叫 `-all`(把游程里每个端点都解析一次), 最坏输入上 **5.6 秒** —— 它**不是**上面那个
口径, 是调用方把游程展开之后的成本。那 65 541 个末端里有 65 533 个来自同一条游程、同一个匹配
(`[a-z]{4,}` 吃掉整篇), 展开得到的是一串互相嵌套的区间, 没有新信息, 而每一个都要从自己那个
末端走回偏移 0 = O(正文²)。留着这一档只是为了标明"别这么用"。

内存峰值(每条路一个子进程各报 `VmHWM` 增量, 喂 12 篇互不相同的 256 KiB 最坏形状语料):

```text
旧实现 528KB = 1 份 set DFA 缓存 + 10 份各自独立的 Regexp DFA 缓存
正向路 400KB · 反向路 596KB = 各 2 份 set DFA 缓存 (解析那份只建 160~170 个状态, 很便宜)
```

预算口径上差得更远: 旧实现是 `1+N` 份各自 8 MB 额度的缓存, 新实现恒 2 份。

🔴 但**不要把这一档当成通用推荐**。它的证据是 10 条 pattern / 64 KiB 的合成微基准,
那个规模**结构上**显不出 set 里状态数相乘这件事。同一条路换成真实的 155 条规则表 × 6.4 MB:
整表反向 set 一遍扫要 **65 秒**(arena 顶满 254 MB 仍在 flush), 正向同表 **18 ms** 零 flush ——
整整差四个数量级, 结论直接翻过来。不要求逐字节等价的调用方一律走上面的
[`Re2Set_fll_t`](#where-a-set-matched-re2set_fll_t)(正向扫 + 单条反向锚定回推),
别建整表反向 set。

⚠ 量这类差别时**两条路必须共用同一批 set 对象**: 同一批 pattern 建两次, 两个 DFA 的状态区
落在不同地址上, cache set 冲突不一样 —— 实测同一段代码只因为换了个 set 对象就能差 5~8%,
比要量的差别本身还大。

<a id="getpatternlenrange"></a>

#### `GetPatternLenRange`

```go
min, max := hgmLibre2.GetPatternLenRange(`[A-Z]\d{3}`)   // 4, 4
min, max = set.GetPatternLenRange(i)                     // 同上, 取自建 set 时算好的那张表
// max == hgmLibre2.PatLenUnbounded (-1) 表示"没有上界"
```

一条 pattern 能匹配的**字节**长度区间, 在建 set 时用 Go 的 `regexp/syntax` 算一次
(155 条 pattern 不到 1 ms)—— 同一套文法, 而且不动 vendored 的 RE2。上面那一切靠三档驱动:
`min == max` 意味着起点是一句减法; `max` 有限意味着回看有界; `PatLenUnbounded` 意味着
没有窗口可回看, 这条 pattern 落回调用方。RE2 认、但 Go 的解析器不认的 pattern 返回
`(0, PatLenUnbounded)` —— 朝安全方向保守, 即它只可能把一条 pattern 推到回落路径上,
绝不会产出错的起点。

<a id="where-a-reverse-set-matched-re2set_rrl_t"></a>

### 反向 set 命中在哪: `Re2Set_rrl_t`

`Re2Set_fll_t` 的镜像: 从输入的**末尾**起一遍扫, 分批交回去重叠、互不重叠的区间。
函数签名完全一样(同一套 `Re2Set_req_t` / `Re2Set_alloc_t` / `Re2Set_startEnd_t`)——
只是开在 `*RegexpSetReverse` 上而不是 `*RegexpSet` 上:

```go
rs, _ := hgmLibre2.NewRegexpSetReverseMaxMem(patterns, hgmLibre2.DefaultSetMaxMem)
ms, _ := rs.NewRe2Set_rrl()   // 进程级: 建一次留着, Scan 可并发
defer ms.Close()

err := ms.Scan(hgmLibre2.Re2Set_req_t{
    Body:    body,
    Allocer: hgmLibre2.NewRe2Set_alloc(),   // 每 goroutine 一个, 复用
    StartEndResultFn: func(batch []hgmLibre2.Re2Set_startEnd_t) bool {
        for _, m := range batch { _ = body[m.Start:m.End] } // 第 m.Index 条 pattern 的真匹配
        return true
    },
})
```

🔴 三个类型**函数签名一样**, 但故意**不做成** Go `interface` —— 本库不需要, 也没定义。
形状一样是为了好学好换手, 不代表可互换, 而且输出顺序不同(fll 升序 / rrl 降序 /
frel 按 `Start` 升序)。

与正向扫描器只有两处不同, 就两处:

1. 区间按 `Start` **降序**交回来(正向: 升序);
2. 去重叠规则是 **rightmost-longest**(正向: leftmost-longest)。

两个方向保证的三件事是一样的: 每个区间都是真匹配, **同一条** pattern 的区间从不重叠,
含有匹配的区域不会被无声跳过。两条规则只在两个真匹配确实重叠的地方不同, 其余地方逐个区间一致:

| 输入 | pattern | leftmost-longest | rightmost-longest |
|---|---|---|---|
| `abab` | `a\|ab` | `[0,2) [2,4)` | `[2,4) [0,2)` —— 同一个集合, 顺序反过来 |
| `aab` | `ab\|b` | `[1,3)` = `"ab"` | `[2,3)` = `"b"` —— 真的不一样 |

要和 `re.Longest().FindAllStringIndex` 逐字节一致就用正向那个。只是"把这片正文里的东西
都框出来"(脱敏 · 定位 · 计数), 两条规则都行。

**什么时候该用它。** 当表里有那种正向爆炸、反向收敛的 pattern 时 —— 也就是
[反着扫](#scanning-backwards)里那个 `S B{m,n} L` 形状。这一层出现之前, 这种表反着扫只能当
**门**用: `Match` 说哪几条命中, 要位置就得再正向扫一遍全文 —— 那正是 `Re2Set_fll_t`
要消掉的 `1 + k` 遍。

**反向是容易的那个方向, 不是难的那个。** 正向 DFA 报的是匹配**末端**, 所以起点得在 Go 侧
往回猜 —— 上面那套两步补起点就是在做这件事。反向扫报的是匹配**起点**, 而
leftmost/rightmost-longest 本来就是**定义在起点上**的。所以这里没有猜:

- 反向 `FindAllIndex` → 匹配起点, 按扫描顺序(从右往左)单调;
- 正向**单条** `FindStringIndexAtWithin(from: 起点, bound: 游标)` → 不越过游标的**最长**末端。

于是完全没有候选收集那一步, 也没有 `maxLen` 窗口。每个区间恰好一次锚定搜索 ——
代价与那个匹配自身的长度成正比, 与输入长度无关。(那一句从前走的是"只含一条 pattern 的 set";
2026-08-27 起改用该 pattern 自己的单条 longest 模式对象, 理由与正向扫描器下列的那三条相同。)
它还接得住正向默认档接不住的那一族: **没有长度上界**的 pattern(email 之类),
正向需要 `maxL` 才能框出回看窗口。

**从右往左扫为什么照样不用攒。** 只因为规则跟着一起翻了。在反着扫的同时坚持 leftmost-longest
才会逼出缓冲: 你手里这个区间还可能被更左边、还没走到的那个吞掉 —— 有界的 pattern 还能靠一个
`maxL` 宽的延迟缓冲扛住, 无界的就只能一直攒到扫完, 而"内存跟着输入长度走"正是这一层要躲的。
换成 rightmost-longest 这个问题就不存在: 从右往左走, **你看见的第一个起点就是终局**,
因为更左边的东西不可能压过它 —— 和正向扫描器那句话一模一样, 镜像过来而已。
所以游标照样在回调里推进, 输出照样落在同一个固定的 12 KB 缓冲里。

**它是怎么被钉住的。** `re2set_rrl_test.go` 的语料是从每条 pattern 自己的 `regexp/syntax`
AST 生成的(随机字节产不出真匹配, 那种测试是空绿的), 对的是一个只用 stdlib 写的穷举
rightmost-longest oracle。那份 pattern 列表里的前五条正是把 2026-08-28 删掉的正向
`spanFast` 打挂的那几个反例(`abc|b` · `a|ab` · `x{1,3}[a-c]?(?:ab|cd)?` ·
`(?:ab)?[bc]{1,2}` · `(?:ab)*b{1,3}`); 反向在它们身上一个都不岔, 因为这里没有猜可猜错。
oracle 自带自检: `ab|b` 对 `"aab"` 在两条规则下必须给出不同答案, 否则整个对比就是空的。

<a id="one-forward-pass-spans-straight-out-re2set_frel_t"></a>

### 一遍正向扫直接吐区间: `Re2Set_frel_t`

`Re2Set_frel_t` 回答的问题和 `Re2Set_fll_t` 一样 —— *每条 pattern 命中在哪* ——
但几乎整个实现都在 C++ 里。Go 只递给它一个 `[]Re2Set_startEnd_t`, C 侧每 `step` 一批
直接把结果写进去 —— 那个 Go 结构体和 C 的 `cre2_re2set_result` 是同一份内存布局
(三个 `int32`, 无填充; 两侧各有一条静态断言), 所以没有逐字段的拷贝。名字:
**F** = 第一遍正向, **RL** = **r**ightmost-**e**nd **l**ongest。别把这条规则简称成
"rightmost-longest": 在本库里那个名字已经归 `Re2Set_rrl_t` 那条规则了, 它锚在**起点**上。
这一条锚在**末端**上 —— 下面写清楚, 两者答案不同。

```go
set, _ := hgmLibre2.NewRegexpSet([]string{`\d{3}-\d{2}-\d{4}`, `(?i)authorization`})
s, _ := set.NewRe2Set_frel()
defer s.Close()

err := s.Scan(hgmLibre2.Re2Set_req_t{
    Body:               body,
    Allocer:            hgmLibre2.NewRe2Set_alloc(),
    ExistOnlyIndexList: []int32{1},       // 第 1 行只要"命中没命中"
    StartEndResultFn: func(rs []hgmLibre2.Re2Set_startEnd_t) bool {
        for _, r := range rs {
            _ = body[r.Start:r.End]       // 第 r.Index 条 pattern 的真匹配
        }
        return true                       // false = 提前停
    },
    // HitIndexResultFn 在扫完之后交一次, 就是命中表
    // (ExistOnly 那几行唯一的输出)
})
```

交给回调的切片**就是** `Allocer` 的缓冲; 下一批原地覆盖它。要留的自己拷。它的长度只决定每次
`step` 有多少条结果过桥, 不影响答案。和 `Re2Set_fll_t` 一样, 这里也是全有或全无:
`err` 非 nil 就整遍作废(包括已经交出去的批次)—— 那份 body 回落到 `FindAll`。

**它和 `Re2Set_fll_t` 差在哪。** `Re2Set_fll_t` 是每一个游标还没盖过的匹配**末端**回看一次。
这一层是每个**连通块**回看一次。连通块来自每个 DFA 状态里带的**逐 pattern 存活位**
(`State::live_`): 如果某条 pattern 在某个偏移上没有活着的线程, 那么它的匹配就不可能跨过那个偏移,
两边的命中因而互相独立。一条 pattern 由活转死的那一刻, 它挂着的那些命中被收成一个连通块 ——
而这个块的左边界同时就是回看的下界, 于是 resolve 永远不必走到 body 开头。三个阶段各在最便宜的地方:

| 阶段 | 在哪 | 干什么 |
|---|---|---|
| 扫 body | 表的正向 `kManyMatch` DFA | 一遍 |
| 收集 + 切块 | 原生侧, 不为每次命中过桥 | 匹配末端按游程长度存进逐 pattern 的缓冲, 一个连通块一次交出去 |
| 补起点 | 那一条 pattern 自己的**单条** `Regexp`, 反向锚定 | 块里每个互不重叠的匹配一次调用 |

`Tries / NSeg`(来自 `StatsResultFn`)是*每个连通块花了几次反向锚定搜索*;
在基准那十条通用 pattern 上, 三份语料全都恰好是 1.00。`UsedPeak` —— 原生侧的游程缓冲 ——
稳在几十字节量级, 不随 body 增长。

**去重叠规则 —— 最右**末端**, 取最长。** 界从 body 末尾起; 反复取**末端**最靠右且仍 `<=` 界的
那个匹配, 平局时取**最长**的那个(起点最左), 吐出来, 然后把界降到它的起点。这与
`Re2Set_rrl_t`(先挑最右**起点**)不同, 也与 stdlib 的 leftmost-longest 不同:

| 输入 | pattern | `Re2Set_frel_t` | `Re2Set_rrl_t` | stdlib `Longest` |
|---|---|---|---|---|
| `aaa` | `aa\|a` | `[0,1) [1,3)` | `[2,3) [1,2) [0,1)` | `[0,2) [2,3)` |
| `abc` | `b\|abc` | `[0,3)` = `"abc"` | `[1,2)` = `"b"` | `[0,3)` = `"abc"` |

第二行就是这一层挑末端而不是挑起点的原因: 挑起点会把 `"abc"` 截成中间那个 `"b"`,
而一个把区间喂给校验和(身份证 · IBAN · Luhn)的调用方就会把自己的真命中拒掉 —— 静默漏报。
要和 `re.Longest().FindAllStringIndex` 逐字节一致就改用 `Re2Set_fll_t`;
只是要把东西都框出来(脱敏 · 定位 · 计数), 这条规则够用。

正确性由 `re2set_frel_test.go` 钉住: oracle 是不依赖本库的穷举搜索
(对每个 `(e, s)` 跑 `\A(?:pat)\z`), 语料从每条 pattern 自己的 AST 生成、免得随机字节把测试变成
空绿, 而且 `aa|a` 在 `"aaa"` 上三条规则必须给出三个**不同**的答案, 否则整个对比是空转。
oracle 完全没有"连通块"这个概念, 所以它顺带钉住了另一条主张: 切块不改变答案。

**`ExistOnlyIndexList` 不是可有可无的微调。** 被点名的一行命中时只置一个字节:
不收游程, 不盯存活位, 不闭合连通块, 不补起点。那是这一层里贵的那部分, 不只是几条你反正要扔的结果 ——
在回调里过滤等于把已经付过钱的活扔掉。
🔴 它是**每次调用**的参数, 不是构造时属性: 同一个对象这一遍可以要区间、下一遍只要位,
两种情形下命中表完全一致(`TestRe2SetFrel_ExistOnly` 钉的就是这条)。2026-09-01 之前它是构造时的
字段 `Re2SetFrelPattern_t.ExistOnly`, 而当时唯一的硬理由是"哪几行可以产区间由 `min <= 0` 决定" ——
全库拒收可空 pattern 之后, 这个理由没了。

**怎么量它。** 这几个 DFA 循环对基准机上的代码布局极其敏感: 往测试包里加一个**没人调用**的
Go 函数, 零命中 64 KiB 那个数就在 98 µs 和 199 µs 之间跳。单个二进制量出来的数没有意义 ——
要扫布局(0..7 个没人用的函数, 各编一个二进制)然后每个变体取**最小值**。这样量, 64 KiB 上,
用那十条基准 pattern:

| 语料 | 老的两段式 | `Re2Set_fll_t` | `Re2Set_frel_t` |
|---|---|---|---|
| 零命中 | 97.5 µs | 98.5 µs | **97.2 µs** |
| 39 处稀疏命中 | 430.9 µs | 104.8 µs | **104.3 µs** |
| 最坏情况(全小写) | 598.9 µs | **478.7 µs** | 575.6 µs |

零命中就是一次普通扫描的价钱 —— 存活位那套机器在第一处命中之前是真的免费, 因为空转循环是另一个
实例化版本, 它根本不读存活位。最坏那份语料是 `[a-z]{4,}` 从头到尾不断气, 于是整个 body 是一个
连通块, 每个字节都在被盯着。

那十条 pattern **低估了它**。三张真实的 pattern 表(从某个调用方产品的源码里静态抠出来的
368 条 pattern 字面量, 按形状切成 cred/64 · prompt/31 · body/160), 256 KiB 正文, 四档命中密度,
同样的八种布局扫 —— 测试床在本库之外, 这里只记结论:

| 表 | 命中密度 | 地板 | `Re2Set_fll_t` | `Re2Set_frel_t` | 端到端 | 补起点那一层 |
|---|---|---|---|---|---|---|
| cred | 1% | 0.40 ms | 0.45 ms | **0.42 ms** | 1.07× | 2.50× |
| cred | 90% | 1.00 ms | 5.42 ms | **3.00 ms** | 1.81× | 2.21× |
| prompt | 1% | 0.40 ms | **0.52 ms** | 0.59 ms | 0.88× | 0.63× |
| prompt | 90% | 0.97 ms | 11.45 ms | **5.60 ms** | 2.04× | 2.26× |
| body | 1% | 1.96 ms | 35.18 ms | **15.68 ms** | 2.24× | 2.42× |
| body | 90% | 4.21 ms | 53.06 ms | **26.38 ms** | 2.01× | 2.20× |

*地板*是两条路都必须付的那一遍正向 set 扫描; 补起点那一列比的是 `(总时间 − 地板)`。
12 格里 11 格是 `Re2Set_frel_t` 赢, 端到端 1.06~2.24 倍, 而在真正有差别的那一层上稳定 2.2~2.4 倍。
唯一输的一格是 prompt 表 1% 密度: 命中稀到几乎每处命中自成一个连通块, 于是没有反向锚定探测可省,
连通块记账纯属额外开销。省在哪, 一句话: `Re2Set_fll_t` 是**每个匹配末端**一次反向锚定探测,
`Re2Set_frel_t` 是**每个连通块**一次(body 表 90% 那格: 25.4 万个末端, 14.5 万个连通块)。

<a id="scanning-backwards"></a>

### 反着扫

`S B{m,n} L` —— 一个带计数的重复, 其**起始字符类严格窄于重复体的字符类**, 末尾是个字面量 ——
就是把状态数顶爆的那个形状。`[A-Za-z][A-Za-z0-9]{2,19}key` 是典型: 每个字母都能开一个候选匹配,
后面每个字母数字都让它继续活着, 于是一个 DFA 状态必须记住最近 20 个偏移里**哪些**还活着。
那是一个任意子集, 所以状态数对上界是指数的。

改写救不了。`(a|b)*a(a|b)^k` 在**任何** DFA 里(含最小 DFA)都要 2^k 个状态,
所以这是语言的性质而不是 RE2 的问题。但**反过来**的语言 `(a|b)^k a(a|b)*` 只要 k+2 个。
方向才是杠杆。

正向和反向是**两个对象**, 不是一个对象上的两个方法 —— 一条 pattern 的两个方向是两个 program、
两份 DFA 缓存, 而一条 pattern 通常只会跑其中一个方向:

```go
rev, _ := hgmLibre2.CompileReverse(`[A-Za-z][A-Za-z0-9]{2,19}key`)
hit := rev.MatchString(text)          // 与正向 Regexp 给出同一个答案

revSet, _ := hgmLibre2.NewRegexpSetReverseMaxMem(patterns, hgmLibre2.DefaultSetMaxMem)
idx := revSet.Match(text, buf)       // 一个 *RegexpSetReverse; 命中集与正向 set 相同
```

反向 program 由 RE2 自己的编译器建(连接顺序反转, `^`/`$` 对调, `\b` 不变,
多字节 UTF-8 序列重新按倒序编码), 然后 DFA 从末尾开始走**你那份缓冲区**。
什么都不拷贝, 调用方也不用把输入反转。

在 120 份互不相同的 8 KB 正文上实测, 每个 set 只放一条 pattern 好把内存归到它头上:

| | 状态数 | 状态缓存 | 命中集 |
|---|---:|---:|---|
| 正向 | 35 149 | 5.35 MB | 16 |
| 反向 | 45 | 0.01 MB | 16 |

**它的代价(单条 `RegexpReverse`)。** `RegexpReverse` 回答的是"有没有匹配", 不是"在哪"。
它上面没有 `Find`: 反向扫先撞上输入里**最后**一个匹配, 所以反向的 `Find` 只可能是 rightmost 的 ——
和正向 leftmost-first 是两种语义。把反向当便宜的门用, 对命中的那几份输入再拿正向 `Regexp`
跑 `FindStringIndex`。

在 **set** 那一侧这个反对意见是被**回答**了而不是被绕开: 只要事先声明清楚,
rightmost 就是一条完全合格的去重叠规则 —— 所以
[`Re2Set_rrl_t`](#where-a-reverse-set-matched-re2set_rrl_t) 按 **rightmost-longest** 给区间,
并且把话说明白。

`RegexpSetReverse` 是报位置的, 按它自己的方向:
[`FindAllIndex`](#findallindex-the-raw-end-point-runs) 给的是匹配**起点**(含),
正向 set 给的是末端; 而 [`ResolveSpan`](#resolvespan-complete-one-end-point-into-a-span)
把一个已知的末端变成对应的起点。后面这个才是反向 set 真正的用处。
🔴 反着扫**一整张表**和反着扫一条 pattern 是两回事: set 里的状态数是**相乘**的,
所以一张 155 条的表正向 18 ms 零 flush 扫完的 6.4 MB 正文, 反向要 **65 秒**,
arena 顶在 254 MB 的天花板上还在 flush。把反向 set 指向整篇文档之前先量
`GetMemInfo().FlushesTotal`; 只是要给已经找到的命中补左端, 就用 `ResolveSpanWithin`,
它的代价与输入长度无关。`Re2Set_fll_t` 内部干的正是这件事 —— 每条真需要左端的 pattern
惰性建一个**单条**反向 set, 从不拿它扫正文(那些东西多贵见 `GetViableOneStats`:
那张表上 32 条 pattern · 973 个状态 · 2.0 MB)。

**方向是逐条决定的, 不是一个全局开关。** 镜像形状会以它取胜的同一个机制输掉:
在一份不含 `key` 的语料上, `(?s).{20}key` 正向 21 个状态、反向 1 个,
而 `key(?s).{20}` 正向 1 个、反向 21 个。拿真实输入把两个方向都量一遍 ——
两个方向各建一个单条 set, 比 `GetMemInfo().States` —— 然后把每条 pattern 放进它便宜的那个方向。
就算因此要在输入上扫两遍, 也仍然胜过一遍在抖动的扫描。

**这不是"你自己把 pattern 文本反过来写"。** 把 pattern 倒着写、把输入也倒过来, 得到的**答案**
一样但**代价**不一样。RE2 的 `Simplify` 展开 `x{2,19}` 时把必需的拷贝放前面、可选的嵌套放后面;
反转连接顺序会把那个可选嵌套挪到读取顺序的**前面**, 于是活起点集合是层层嵌套的而不是任意子集。
同一个语言、同一批字节、不同的自动机: 走这个 API 是 17 个状态, 手工反转版是 25 247 个
(`TestReverseIsNotHandRolledTextReversal`)。手工反转还会把多字节 UTF-8 劈开, 并且需要输入的第二份拷贝。

**为什么是独立类型。** 反向扫跑的是**第二个** `Prog`(`Regexp::CompileToReverseProg`),
而 DFA 状态缓存属于它的 `Prog`(`Prog::dfa_first_` / `dfa_longest_`)—— 所以不管 Go 那侧
API 长什么样, 两个方向就是两个 program 两份缓存。`RegexpReverse` 把这件事摆到明面上:
一个对象, 一个方向, 一份缓存, 而且调用方从类型上就看得出这条 pattern 在往哪个方向跑。
同一条 pattern 两个方向都要? 那就两个对象都编。

**预算与回落。** 反向 program 在第一次扫描时惰性编译, 预算取自 `CompileReverseMaxMem`
(`CompileReverse` 给它 RE2 默认的 8 MB)。如果反向 DFA 扫到一半放弃 —— RE2 会从一个
"建状态比吃输入还快"的 `Prog` 搜索里退出来 —— 这个对象会静默回落到自己做一次正向匹配。
答案永远正确; `MatchStats` 会报 `FellBack`, 你据此知道这一次没拿到那份省。
`RegexpSet` 从不退出(RE2 对 `kManyMatch` 只 flush), 所以反向 set 没有回落路径。

**另一个杠杆是内存。** `CompileMaxMem` 抬高单条 pattern 的预算, 和 `NewRegexpSetMaxMem`
对一张表做的事一样 —— 同一个旋钮, 同样的两道天花板(见 [`maxMem` 怎么定](#sizing-maxmem))。
在上面那条 pattern 上, 60 份互不相同的 16 KB 正文在 8 MB 默认预算下 flush 6 次, 256 MB 下 0 次。
而反向扫在**默认**预算下就是 0 次 flush, 峰值 9 个状态。抬预算是拿内存买吞吐;
形状允许的时候, 换个方向扫是白买。

<a id="appendallstringindexflat"></a>

### AppendAllStringIndexFlat

`re.AppendAllStringIndexFlat(dst, s, n)` 返回的匹配与 `re.FindAllStringIndex(s, n)` 相同,
但是以 `[s0, e0, s1, e1, …]` 追加进调用方自己的 `[]int`, 而不是包进一个新建的 `[][]int`。

`FindAllStringIndex` 每次调用有两笔用完就扔的分配, 而且都随匹配数增长: C 侧结果拷进来的那个
扁平 `[]int`(每个匹配 `2*nmatch` 个 int —— 所有分组都算, 尽管只返回第 0 组),
以及那层 `[][]int` 外壳(每个匹配一个 24 字节的 slice header)。大 body 高命中时这就是主项:
19 万个匹配 ≈ 每个 40 字节 ≈ 单次调用 7.6 MB, 而且调用方把匹配走一遍之后它们全成了垃圾。
这个变体只填第 0 组(`nmatch=1`, 顺带也缩小了 C 侧的 `vector<StringPiece>`),
并且追加进你的缓冲, 所以传 `buf[:0]` 就能让重复调用稳态零分配。

```go
var locs []int                          // 跨调用复用
for _, text := range corpus {
    locs = re.AppendAllStringIndexFlat(locs[:0], text, -1)
    for i := 0; i+1 < len(locs); i += 2 {
        start, end := locs[i], locs[i+1]
        ...
    }
}
```

匹配集合、顺序、以及空匹配的处理都与 `FindAllStringIndex` 完全一致 —— 两者走的是同一个 C 循环 ——
并且在 `find_all_flat_test.go` 里对着它和 stdlib 双向钉住。要捕获分组就用
`FindAllStringSubmatchIndex`。

<a id="stepallstringindex"></a>

### StepAllStringIndex / StepAllStringSubmatchIndex

全匹配的 **`sqlite3_step` 式**形态(`match_step.go`)—— C 侧一次填一批命中进一块固定的批缓冲
交给 `batchFn`, Go 侧取走这批再 step 下一批, **内存里从来没有全部命中信息**。
零 Go 堆分配(缓冲是库内 `sync.Pool` 借的, 用完就还), 而且**无命中的调用一分钱不花**。

```go
per := 2 * (re.NumSubexp() + 1)                // Index 版恒为 2
re.StepAllStringSubmatchIndex(body, -1, func(flat []int32) bool {
    for k := 0; k+per <= len(flat); k += per {
        loc := flat[k : k+per]                 // 布局与 FindAllStringSubmatchIndex 的单行逐字相同
        _ = loc                                //   未参与的组是 -1,-1
    }
    return true                                // 返 false = 提前停, 剩下的正文不再扫
})
```

- 🔴 `flat` 只在**本次回调内**有效: 下一批就地覆写同一块内存, 而且本次调用一返回这块就还回
  池子了。要留存自己 copy(存 `int` 下标, 别存切片)。
- 🔴 用它还是用 `FindAll*`, 看的是**要不要那张表**, 不是哪个新:
  - 拿到命中顺序过一遍就丢(累加 · 改写 · 当场判断)⟹ Step。
  - 要一张能来回走、能 append/过滤/交叉引用、要 `len()` 当门的表 ⟹ `FindAll*`。
    拿 Step 去物化实测是**净亏**(Go 的 append 阶梯累计收敛到 5N: 2 万处命中
    1.45 MB / 4 笔 → 5.70 MB / 26 笔, CPU +17%)。

匹配集合、顺序、空匹配的推进都与 `FindAll*` 逐处相同(同一段 C 循环), 对拍在 `match_step_test.go`。

<a id="appendreplaceallstringfunc"></a>

### AppendReplaceAllStringFunc

`ctx.AppendReplaceAllStringFunc(dst, re, src, f)` 产出的东西和
`re.ReplaceAllStringFunc(src, f)` 完全一样, 但它追加进调用方自己的 `[]byte`,
并把匹配位置表留在 `ctx` 上, 于是两个缓冲都跨调用复用。

它返回 `(dst, changed)`。**`changed` 的意思是"结果与 `src` 不同", 不是"正则匹配上了"** ——
它的定义严格就是 `re.ReplaceAllStringFunc(src, f) != src`。两种情况报 `false`,
而且都让 `dst` 逐字节保持你传进来的样子:

1. 什么都没匹配上 —— 快速返回, 一个字节都不写;
2. 匹配上了, 但每次 `f` 都把原文原样交回, 结果与 `src` 逐字节相同 —— 追加的字节被回滚掉。

第 2 种不是锦上添花。照着这个 API 写的替换通常是解码器或者去混淆器, 它们的 `f` 自带有效性检查,
失败时原样返回 `m`: 码点越界的 HTML 数字实体 `&#…;`, 长度为奇数或者解出来是不可打印字节的
十六进制串。这些都匹配得上却什么都没改, 而一个把 `matched` 当 `changed` 用的调用方
就会多拿到一份原文的副本 —— 多一个缓冲要拿着, 多一遍要扫, 多一个重复要对账。
这个检查基本不要钱: 长度不等当场判定, 只有长度恰好相等时才付一次 `memcmp`。

回滚恢复长度和内容, 但不恢复容量: `dst` 回来时可能指着一块更大的底层数组
(刚刚预留的那 `len(src)` 字节)。这对下一次调用是好事 —— 只要永远用返回的那个切片,
别用你传进去的那个。

```go
var ctx hgmLibre2.ReplaceAllStringFunc_ctx_t   // 零值可用; 非 goroutine 安全
var buf []byte                                 // 跨调用复用
for _, text := range corpus {
    out, changed := ctx.AppendReplaceAllStringFunc(buf[:0], re, text, decode)
    buf = out         // 不管怎样都把 (可能已经长大的) 缓冲留下
    if !changed {
        use(text)     // 解码什么都没改, 也没分配
        continue
    }
    use(string(buf))  // 或者继续在字节上干活
}
```

它为什么存在: `ReplaceAllStringFunc` 每次调用要付两笔用完就扔、且都随 body 增长的分配 ——
匹配表(每个匹配 `2*(numSubexp+1)` 个 int, 而拼接循环只读第 0 组), 以及结果缓冲。
结果缓冲从前是一个从零开始长的裸 `strings.Builder`: 大字节切片下 Go 按 1.25 倍扩容,
累计分配收敛到 `1/(1-1/1.25) = 5×len(src)`, 其中 4 倍还要再付一遍 memcpy。
64 MB body 的十六进制解码腿上实测: `Builder` 增长了 329 MB, 每字节输入分配 4.9 字节。
`ReplaceAllStringFunc` 现在一次性把那个缓冲定成 `len(src)`(stdlib 的 `replaceAll` 和
本库自己的 `ReplaceAllFunc` 字节版早就这么干了), 那是 1 倍缓冲、没有重扩容拷贝。
这个变体再进一步, 复用你已经拿着的缓冲 —— 那才是一个按段落调用它的热循环真正想要的。

`ReplaceAllStringFunc` 现在就是这个方法上薄薄一层壳, 所以两者共用匹配集合、顺序、`f` 的调用点
和拼接(都从同一个 C 循环里读第 0 组)—— 也共用与 `ReplaceAllString` 相同的惰性物化:
一次没改任何字节的调用把原来的 `src` 零分配交回。这一条、追加契约、回滚契约、
复用不串味契约, 以及稳态零分配这个主张, 都钉在 `replace_func_ctx_test.go` 里。

<a id="findreplacewithin"></a>

### FindReplaceWithin

`find.FindReplaceWithin(strip, src, repl)` 与下面这个双正则写法完全等价

```go
find.ReplaceAllStringFunc(src, func(m string) string {
    return strip.ReplaceAllString(m, repl)
})
```

—— 先定位 `find` 的每一处匹配, 再在那一段匹配**内部**做 `strip`→`repl` ——
**但整个外层循环和每一次内层替换都在一次 cgo 调用里跑完**, 而不是每处匹配一次过桥、
每个分隔符再一次。算法逐字节相同: `find` 可以保持零捕获, 于是它仍走 RE2 最快的无子匹配 DFA,
而 `strip` 也仍然只在定位到的那一段里面改。

它在**无变化那条路上是惰性的 / 零分配**: C++ 侧在第一次真正改动字节之前不建也不拷结果串。
如果 `src` 没变(没匹配上, 或者匹配上了但 `strip` 什么都没删), 它原样返回 `src`, 不分配。
只有真被改过的输入才付一个结果缓冲的钱。

一条语法提示: 这里的 `repl` 是 **RE2 的 rewrite 串**。(`RE2::GlobalReplace` 是 RE2 自带的
替换全部; 它的 *rewrite* 串用的是 RE2 原生的替换语法: `\1`..`\9` 展开成对应的捕获组,
`\0` 是整个匹配, `\\` 是一个反斜杠字面量, 其余一律字面量。)所以它既不同于 stdlib 的
`$1` / `${name}`, 也不同于本库那个字面量 `ReplaceAllString` 的 repl —— 三套不同的约定。
常见的字面量 `repl`(比如 `""`, 里面没有 `\`)三者一致。

它的来由: 撤销分隔符混淆 —— `find` = 容忍分隔符的关键词骨架(`i[\s._-]{0,2}g…`),
`strip` = 分隔符字符类, `repl = ""`, 于是 `i.g-n_o r.e` 被正规化回 `ignore`。
常见路径上(普通文本, 没有混淆)它零分配, 吞吐与一次普通 DFA 扫描持平;
在满是拆分关键词的输入上, 它比嵌套 `ReplaceAllStringFunc` 那个写法快约 2 倍,
分配从 O(匹配数) 收到 1 次。

<a id="appendfindreplacewithin"></a>

#### AppendFindReplaceWithin

`find.AppendFindReplaceWithin(dst, strip, src, repl) ([]byte, bool)` 是
**追加进你自己缓冲**的那个孪生, 给"结果只消费一次就扔"的调用方用
(造一个解码后的视图 → 拿 `RegexpSet` 扫它 → 扔掉)。同一个 C 内核, 同一个 `changed` 判据;
唯一的差别是结果落在哪: `FindReplaceWithin` 每次有变化都新造一个 Go `string`
(把整个结果 `C.GoStringN` 拷一份), 而这个是 memcpy 进你传的 `dst` —— 复用一个缓冲就让稳态
Go 堆零分配。

```go
out, changed := find.AppendFindReplaceWithin(buf[:0], strip, src, "")
// changed ⟺ find.FindReplaceWithin(strip, src, "") != src
// changed ⟹ string(out) == find.FindReplaceWithin(strip, src, "")
if changed {
    buf = out          // 永远留返回的那个切片: 它可能换了底
    scanSet.Match(bytesStrView(out), hits)
}
```

`changed=false` 时 `dst` 连长度都没动 —— 调用方该用原来的 `src`。返回的字节是调用方缓冲上的一个视图:
再往那个缓冲 append(或者把它 reslice 成 `[:0]`)就让它们失效。

这个没有 `_ctx_t`: 外层匹配循环和内层替换都在 C++ 里, 所以结果本身是 Go 侧唯一一笔随输入增长的
分配 —— 而现在这笔归调用方了。它对拍的是 stdlib 那个等价的嵌套写法, 见
[测试怎么跑, 钉了什么](#how-the-tests-run)。

<a id="prefilter-which-literals-must-appear-and-which-patterns-can-never-be-filtered"></a>

## Prefilter: 哪些字面量必须出现, 哪些 pattern 永远过滤不掉

`Prefilter` 把 RE2 自己那套预筛机器(`FilteredRE2` / `PrefilterTree`, 两个都已经 vendored 在这里)
接了出来。它回答三个问题:

```go
p, err := hgmLibre2.NewPrefilter(patterns, 0 /*minAtomLen: 0 = RE2 默认*/, 0 /*maxMem*/)
atoms := p.GetAtoms()              // 必须出现的字面量, 已小写化、去重
live  := p.GetPotentials(found)    // 给定在正文里找到的 atom 下标: 还有哪些 pattern 可能命中
hard  := p.GetUnfiltered()         // 哪些 pattern 一个字面量都不要 -> 它们永远得跑
```

`Prefilter` 自己不做匹配。它把 atom 列表交给你; 你用任何字符串匹配器
(Aho-Corasick, 或者 `memmem`)去找这些 atom, 再回来问哪些 pattern 还活着。
没活下来的 pattern **保证**不匹配。匹配必须大小写不敏感, 或者在正文的小写副本上做,
因为 atom 是小写化过的。

**`GetUnfiltered()` 才是这东西被导出的原因。** "先拿便宜的字面量门筛一遍正文, 只把过了门的
喂给大表"是唯一能抬高吞吐天花板的方向(见 `doc/set性能优化经验.md` §4 G)——
但它有一道硬上限: 不需要任何字面量的 pattern(`[A-Za-z0-9+/=_-]{20,}` ·
`(?-i:\([A-Z]{2,5}\))`)不管正文长什么样都得跑。要在**造预筛这一级之前**先量这个集合, 别事后才量。

🔴 只有 RE2 自己那套预筛做得对。手写的"从 pattern 源串里抠字面量"提取器在
`(?:foo|[A-Z]{5})` 上会给出错答案: 它含有字面量 `foo`, 可整条 pattern 是过滤不掉的,
因为另一个分支不需要 `foo`。这套推理活在一棵 AND-OR 树里, 不是拿眼睛能看出来的。
`prefilter_test.go` 钉住了这个 case, 也钉住了整个想法赖以成立的那条可靠性性质:
凡是真能匹配某份正文的 pattern, 必须出现在"该正文里找到的那些 atom"对应的 `GetPotentials()` 里。

`minAtomLen` 是一个真的取舍旋钮, 不是调参细节。调大它得到更少更长的 atom —— 匹配器更快,
但更多 pattern 掉进 `GetUnfiltered()`。某张 112 条的生产表实测: RE2 默认给 1654 个 atom、
只有 4 条不可过滤, 但 atom 短到几乎每份正文里都有, 于是什么都筛不掉;
`minAtomLen=6` 给 216 个 atom、38 条不可过滤, 筛得狠得多, 但起点是 34% 的地板。
在你自己的表上把两头都量一量。

<a id="tuning-for-the-dfa-state-cache"></a>

## 调 DFA 状态缓存

[`doc/set性能优化经验.md`](doc/set%E6%80%A7%E8%83%BD%E4%BC%98%E5%8C%96%E7%BB%8F%E9%AA%8C.md)
是上面那些性能材料的长版, 单条 `Regexp` 和 `RegexpSet` 都讲: 心智模型、该量什么、按什么顺序量、
状态爆炸真正的驱动因素、三个旋钮按收益排序(方向 > 表的形状 > 内存预算)、怎么拆表,
以及一串量过之后被否掉的做法。要调一张几百条的表、或者要伸手去拿
`CompileMaxMem`/`CompileReverse` 之前, 先读它; 上面那些小节是它的摘要。

<a id="differences-from-stdlib-regexp"></a>

## 与标准库 `regexp` 的差异

这是与 Go 标准库 `regexp` 之间具体行为差异的完整清单。前三条是 **API 设计取舍**
(本库故意不是 drop-in); 其余的来自"跑的是**原生 RE2 引擎**"而不是 Go 那份从零重写的实现。
全部是有意为之, 且有测试覆盖。

想要一份迁移检查表(把这里和[支持的 API](#supported-api)里的每一处缺口配上"改用什么",
连同同一个决定的性能面), 见
[`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md) §4。

<a id="empty-capable-patterns-are-rejected"></a>

1. **能匹配空串的 pattern 一律拒。** `a*` · `x{0,3}` · `(a|)` · `a?` · `""` ·
   `(?m)^[ \t]*$` · 一个裸 `\b` —— 本库的每一个编译入口
   (`Compile` / `MustCompile` / `CompileLongest*` / `CompileReverse*` / `NewRegexpSet` /
   `NewRegexpSetMaxMem` / `NewRegexpSetReverseMaxMem` / `NewPrefilter`)当场返回错误
   (`MustCompile` panic)。stdlib 则照编不误。

   *为什么。* 任何字符串里都有空串, 所以这种 pattern 处处命中、没有信息量 ——
   它是 pattern 写错了, 不是引擎该伺候的东西。全库拒掉它(2026-09-01)之后, 一整族逃生通道
   得以删除(set 扫描器的 `unsupported` 名单 · `SetModes` 的 `boolOnly` 降级 ·
   `NewRe2SetFrel` 的逐条校验)—— 这些通道按其构造就是几乎没人执行、因而也几乎没人测过的分支。
   现在上面每一层都可以**无条件**假设"一个匹配至少 1 字节"。

   *该怎么改。* 把量词改成不可空: `a*` → `a+` · `x{0,3}` → `x{1,3}` · `(a|)` → `a`;
   要判"这行是不是空的"用 `len(line) == 0` 而不是正则。除了正确性, 这也是更快的形状 ——
   115 KiB 的行状 body 上, `(?m)^[ \t]*$` 是 stdlib 的 **0.23 倍**, 而 `(?m)^[ \t]+$`
   是 **8.45 倍**(`doc/与标准库regexp怎么选.md` §2.5)。如果零长语义真的是必需的,
   那一条 pattern 用 stdlib。

   *为什么不"照编, 但永不吐零长匹配"。* 事后过滤和"在引擎内部禁止零长"不是一回事。
   默认口径是 leftmost-first, 所以 `a*|b` 对 `"b"` 会在偏移 0 上试 `a*`, 匹配到空串就停,
   给出 `[0,0)`。把它过滤掉得到的是"没匹配", 而正确的非空答案是 `[0,1)="b"`。
   要做对就得改 program, 那会让本库的匹配语义从上游 RE2 分叉出去, 也让 `VENDOR.txt` 里
   "摘上游修复"那套安排从此更难。

   *这道门是怎么判的 —— 以及为什么没有口子。* 判定在 C 侧的 `cre2_emptymatch.cpp` 里,
   用的是 **RE2 自己的解析器**: 与 `RE2::Init` 同一句 `Regexp::Parse`、
   同一份 `RE2::Options::ParseFlags()` 产的标志, 再用 RE2 自己那个非递归的 `Regexp::Walker`
   自底向上算可空性。RE2 编得出来的东西, 这道门就读得懂。RE2 自己都解析不了的 pattern 直接放行,
   由紧接着的编译去报 RE2 那条更准确的错。

   这件事的第一版(2026-09-01)是用 Go 的 `regexp/syntax` 解析、Go 解析不了就放行。
   那是个 bug: 两个解析器认的不是同一个语言。PerlX 的 `\C`(匹配任意单个字节)RE2 认、
   Go 报 `invalid escape sequence`, 于是 `\C*` 径直绕过这道门, 照旧产零长匹配 ——
   `\C*?` 在 `"ab"` 上给 `[[0 0] [1 1] [2 2]]`。

   *判据是结构上的可空性*, 不是"它能不能匹配空文本"。零宽断言(`^ $ \b \B \A \z`)一律算可空:
   `\b` **并不**匹配空文本, 可它在 `"ab"` 里照样产出零长匹配。所以拿 `""` 在运行期探一次
   会漏掉一整族, 不能当判据。

   钉在 `emptymatch_test.go` 里: 每个入口一个 case, 一组正对照保证不可空的形状
   (含 `\C` · `\C+`)照编不误, 外加 4000 条 pattern 的差分 —— 在两个解析器都接受的输入上,
   这道门与 Go `regexp/syntax` 的判断处处一致。

2. **`ReplaceAllString` 的 repl 是字面量 —— 没有 `$` 展开。** stdlib 会在替换串里展开
   `$1` / `${name}` / `$$`; 这里 `repl` 逐字节插入, 不展开也不转义(所以 `"$1"` 还是 `"$1"`,
   `"$$"` 还是 `"$$"`)。这是唯一一个签名兼容但**行为不兼容**的方法。要按捕获组替换,
   用 `ReplaceAllStringFunc` 自己拼。(`FindReplaceWithin` 是另一个非 stdlib 的方法,
   它用 RE2 的 `\1` rewrite 语法 —— 见上面那一节。)
3. **`[]byte` 系的替换方法在无变化那条路上复用 `src`。** stdlib 的
   `ReplaceAll` / `ReplaceAllFunc` 永远返回新分配的切片; 这里, 逐字节没变的输入
   (没匹配上, 或者替换什么都没改)**原样返回 `src` 切片**, 零分配 —— 所以不许往结果里写。
   `string` 系的方法一向如此; 字符串不可变, 所以这只在 `[]byte` 那一族里看得出来。
   内容上结果完全一致, 包括 `nil` vs 空的约定。见 [`[]byte` 系方法](#byte-slice-methods)。
4. **非法 UTF-8 输入。** stdlib 把每个非法字节当成一个 `U+FFFD`, 并让 `.` 匹配它;
   原生 RE2 只匹配完整的合法 rune, 所以在比如 `[]byte{0xff,'a',0xfe}` 上, pattern `.`
   只找得到那个 `a`。如果你要在可能非法的 UTF-8 上匹配且需要 stdlib 的行为, 用 stdlib。
5. **`\C` 是认的**(RE2 的"任意一个字节"); stdlib `regexp` 在编译期就拒 `\C`。
   更一般地说, 有一小把 escape 是 RE2 独有或 stdlib 独有的, 所以一边合法的 pattern
   另一边可能拒收。
6. **2 GiB 输入上限。** 长度和偏移过 cgo 边界时是 32 位 `int`, 所以超过 `2^31-1` 字节的输入
   (以及 pattern)被保守地当成*没匹配* / 原样返回, 而不是去匹配。stdlib 没这个限制。
   (除非你喂多 GB 的串, 否则无关。)
7. **大小写折叠的字面量被并进字符类时, 会按完整的 Unicode 折叠轨道折叠。**
   当一个折叠过的字面量是某个交替的分支、而 RE2 把这个交替因式分解成单个字符类时,
   这个类会把每一个折叠等价的 rune 都收进来, 不只是 ASCII 那一对: `\w|[kK]` 也匹配
   U+212A KELVIN SIGN, 而 stdlib 只匹配 `k`/`K`。(`[sS]|\w` 一向就这样匹配 U+017F。)
   这是上游 RE2 的行为, 只在非 ASCII 折叠等价物上才显出来。
8. **捕获组名可以是非 ASCII。** `(?P<中文>a)` 在这里编得过; stdlib 从 Go 1.22 起拒收非 ASCII
   捕获组名。两种命名分组写法 —— `(?P<name>expr)` 和 `(?<name>expr)` —— 都认, 和 stdlib
   (Go 1.22+)一样。
9. **没有嵌套深度上限。** stdlib 拒收解析树嵌套超过 1000 层的 pattern
   (`expression nests too deeply`, Go 1.19+); 本库接受。20 万层嵌套分组约 100 ms 编完,
   内存线性且不长栈(解析 · 简化 · 析构全是迭代式的), 40 万层则干净地卡在捕获组数量上限上 ——
   所以这只是比 stdlib **接受得更多**, 不是健壮性缺口。如果你编的是不可信的 pattern
   而且想要 stdlib 那个天花板, 编译前自己查一下深度。

不算差异但值得说明: 这里的匹配是 **leftmost-first**, 也是 stdlib 的默认口径
(`regexp.Compile`); stdlib 那个可选的 leftmost-longest 模式(`(*Regexp).Longest`)这里没有提供。
任意长度的捕获组名都完整返回, 重名分组也接受 —— 与 stdlib 相同。

<a id="concurrency-sharing-one-regexp-is-fine-it-just-doesnt-scale-linearly"></a>

## 并发: 共享一个 `Regexp` 没问题(只是不线性扩展)

像用 stdlib 那样共享一个包级 `*Regexp` 就好。这一节是为了解释一条扩展曲线, 不是要你做什么。

`Regexp` 可以多 goroutine 使用, 但它不**线性**扩展。每次 DFA 搜索都要拿那个 `Regexp` 的
DFA 状态缓存的**读锁**(`DFA::cache_mutex_`, Linux 上是 `pthread_rwlock`);
这把锁存在的唯一理由是让罕见的整表 flush 能独占运行, 可每一次搜索都在为它付钱。
读锁之间不互斥, 但读者计数是同一条共享缓存行上的一个原子量, 于是 goroutine 一多,
那条缓存行就在核之间乒乓, "并发"的搜索被串行化了。

20 核 Ryzen 5900X 上实测, 不命中的 pattern, ns/op:

| | 14 字节输入 | 4 KB 输入 |
|---|---|---|
| 共享一个 `*Regexp`, 1 个 goroutine | 69–74 | 453–467 |
| 共享一个 `*Regexp`, 16 个 goroutine | 42–77 | 62–69 |
| 每 goroutine 一个 `*Regexp`, 16 个 | 9.5–13 | 38–51 |
| stdlib `*regexp.Regexp` 共享, 16 个 | 4.3–4.5 | 67–70 |

把读锁编译掉(只为测量)之后, 共享那一档与每 goroutine 一份那一档完全持平
(16 goroutine 下 8.0–8.5 ns), 所以整个差距就是那一把锁。短输入受伤最重,
但 4 KB 输入也要输约 1.6 倍。

**这不构成"别再共享"的理由。** 整个效应在 16 goroutine、14 字节输入上是每次调用约 33 ns,
而买回它在多数程序里是笔坏交易: 每 worker 一个 `*Regexp` 意味着每 worker 编一次
(每次几微秒到几毫秒, 而如今 pattern 通常在 init 时编一次), 每 worker 一份**独立的
DFA 状态缓存** —— 于是原生内存峰值和你调好的 `max_mem` 预算都要乘以 worker 数 ——
外加包级变量根本不需要的生命周期管理(池化 · `FreeC`)。共享的 `Regexp` 还能让
缓存下来的 DFA 状态**跨 goroutine 复用**; N 份私有副本各建各的。

只有当 profile 真的指向这把锁时才考虑每 worker 一份 —— 也就是正则匹配是你程序里的头部开销、
输入很短、并发很高。否则就留着那一个共享变量。正确性 · `RegexpSet` · 低并发这三件事
两种做法都一样。

注意 stdlib 那一列**没有**在说什么: 14 字节时一次 cgo 调用(~50 ns)已经比整个匹配还贵,
所以不管锁怎么样 stdlib 在那里都赢; 那一行是扩展性参照, 不是吞吐对比。
4 KB 时共享那一档与 stdlib 持平, 每 goroutine 一份则快约 1.7 倍。这是上游 RE2 的 issue #569;
产生这张表的基准是 `contention_bench_test.go`。

<a id="resource-management"></a>

## 资源管理

`Regexp` 持有一个原生 RE2 对象, 由 finalizer 自动释放, 所以一般用法下你什么都不用做。
当你动态编译大量 pattern 且希望原生内存尽快回收、不想等 GC 时, 调 `FreeC()`
立刻释放那个 C++ 对象。

`FreeC` 故意做得极简且**不设防**: 它不是并发安全的, 而且对象释放之后再调任何方法
(或者**在有匹配在飞的时候**再调一次 `FreeC`)就是 use-after-free。`FreeC` 自身是幂等的
(第二次调用什么都不做)。不需要及时回收就别调它, 交给 finalizer 收拾。

任何调用顺序下原生对象都**恰好释放一次** —— `FreeC` 与 finalizer 之间不存在 double-free,
有两个互相独立的理由:

- `FreeC` 在释放对象的同一次调用里清掉 finalizer(`runtime.SetFinalizer(re, nil)`)。
  既然你必须持有 `re` 的活引用才调得动 `FreeC`, finalizer 就不可能已经被排上,
  所以清除永远赢, 之后它也不会再跑。
- 就算真有一个 `nil` 句柄走到底下的 `cre2_free`, 那个函数也是 null 安全的
  (碰到 `nullptr` 立即返回)。

注意这里隐含的不对称: 只有释放那条路容忍 `nil` 句柄。匹配/替换那些方法**不**容忍 ——
`FreeC` 之后再调它们会解引用一个已释放/`nil` 的 RE2 然后崩掉。容忍 null 的存在只是为了让
finalizer 永远不会误伤, 不是给"释放后继续用"做的护栏。

<a id="vendored-re2"></a>

## vendored 的 RE2

RE2 的 C++ 源码 vendored 在本目录里(确切布局和怎么升级见 `VENDOR.txt`)。它钉在 RE2 的
`2023-03-01` tag 上, 那是 RE2 引入 abseil 依赖之前的最后一个版本; 更晚的版本没法这样直接编。

在那个 tag 之上**回摘了一小批上游后来的修复** —— 挑的是真修复而不是 abseil 那类改动,
最要紧的是交替因式分解里的一个静默漏报(`0a|0[aA]` 从前匹配不上 `"0A"`)、
对 `(?<name>expr)` 的支持, 以及不展开零宽算子的计数重复(`\b{1000}`)。
每一处在源码里都标了 `[backport re2 <commit>]`; `VENDOR.txt` 列出它们,
连同那些**故意没摘**的上游 commit 以及不摘的理由。

另有三条修复来自上游**尚未合并**的 pull request(标 `[backport re2 PR#NNN]`),
是在这里复现、并与 stdlib 对拍之后才摘的。真正要紧的那条: RE2 那条
"如果 DFA 重建缓存重建得这么快, 就回落到 NFA"的启发式比较的是 `p - resetp`,
而在定位匹配起点的**反向**扫描里这个值是负的 —— 于是那条启发式在那个方向上从来没生效过,
反向 DFA 会无限地自我 flush。修掉之后, `(?s)a[a-d]{24}b[a-d]*` 在 1 MB 上从
43 次 flush / 234 ms 变成 1 次 flush / 34 ms, 结果一致。

<a id="local-changes-to-the-dfa"></a>

### 对 DFA 的本地改动

vendored 的 DFA **不是**逐字节的上游原版。`re2_dfa.cc` 把转移表的每一格从 8 字节的 `State*`
改成 4 字节的"状态 arena 偏移", 并且按需增长这个 arena 而不是一上来就把整个预算占住。
另有三个 vendored 文件带着小的**增量**改动, 为的是上面那些计数器 —— `re2_set.cc` 与
`re2_prog.h` / `re2_set.h` 两个头文件多了可选的出参和访问器 —— 外加一个全新的头文件
`re2_dfa_stats.h`(上游根本没有)。其余每个 `.cc` 都是原装。`VENDOR.txt` 列的是同一份清单 ——
升级 vendored RE2 时要重新贴的就是它。

后果, 全部在真实 pattern 表和真实语料上量过:

- 同样的预算能装下 **1.74 倍**多的状态, 于是"某张表不再 flush"的那个预算档降一级
  (例如 128 MB → 64 MB)。
- 在从不 flush 的表上吞吐不回退(还好几个百分点), 而在那个预算下**本来就在** flush 的表上
  提升两个数量级 —— 赚的是跨过悬崖, 不是这个编码本身。
- 大工作集下 RSS 峰值降约 30%。在真正被打满的预算上则升约 10%,
  因为同样的字节现在装着 1.74 倍的状态。

匹配结果不变, 而且这是被强制的、不是假设的: 命中集摘要在"pattern 表 × 语料 × 预算"的矩阵上
与原来那份 8 字节指针代码编出来的二进制逐位对比过。原来那套编码仍在源码里, 可以用
`CGO_CXXFLAGS="-O2 -DRE2_DFA_NEXT_BITS=64 -DRE2_DFA_ARENA=0"` 恢复,
这在二分一个性能问题时可以当对照组。

调用方可能会想用的另一个编译期宏只有 `-DRE2_DFA_ATTRIB=1`, 它打开
[归因](#attribution-which-patterns-build-the-states)。两个宏默认都是关/原装,
默认构建里为它们既没有字段, 也没有分支和计数器。

其余几处都是**纯追加**: `prog.h` / `set.h` / `re2_set.cc` 上的观测出参与访问器;
`re2_compile.cc` / `re2_set.cc` 上给 `Prog::CompileSet` 和 `RE2::Set` 加的 reversed(反着扫);
以及流式游程扫描 + 锚定解析(`re2_span_scan.h` + `re2_dfa_spanscan_inl.h`, 由 `re2_dfa.cc`
末尾 `#include` 进去 —— `class DFA` 整个定义就在 `re2_dfa.cc` 里, 别的编译单元看不见
`State` / `RWLocker` / `StateSaver`)。

唯一一处**改了上游行为写法**的是 `Compiler::CompileSet` 的两个入口: 上游写成
"`anchor_start_ = true`, 而 `start_` 和 `start_unanchored_` 都指向带 `.*?` 前缀的那个入口",
靠 `SearchDFA` 里 `anchor_start()` 那一条把搜索强制成锚定。现在改成与 `Compiler::Compile`
一模一样的摆法(`start_` = 不带前缀的真锚定入口, `start_unanchored_` = 带前缀的,
`anchor_start_` 照实写 `false`), `RE2::Set::Match` 相应改传 `kUnanchored` —— 走的还是同一个
带前缀的入口, 命中集**逐位不变**(Set / reverse / prefilter / stats / maxmem 全套回归钉死),
换来的是 [`ResolveSpan`](#resolvespan-complete-one-end-point-into-a-span) 能进那个真锚定入口。
两个入口本来就是 `Prog::Flatten` 认得并 remap 的 root, 所以 `Flatten` 一个字没动。

🔴 游程扫描那份是**另写的一份热循环**, 没往 `InlinedSearchLoop` 里塞 `if`: 老循环是全库最热的
那个, 它只管把 id 塞进 `SparseSet`(塞一次就够, 还能 early-out); 游程扫描每个命中字节都要维护
"这条 pattern 的游程长到哪了"且**永不早退**。混在一起是白给老循环加分支。

<a id="how-the-tests-run"></a>

## 测试怎么跑, 钉了什么

```sh
go test ./...
```

每个方法都在一份共享的 pattern 与输入语料上与标准库 `regexp` 对拍
(`hgmLibre2_test.go`); `FindReplaceWithin` 对拍的是 stdlib 那个等价的嵌套写法。
语料里的 `ReplaceAllString` repl 全是字面量, 原因见[与标准库 regexp 的差异](#differences-from-stdlib-regexp);
`TestReplaceAllStringIsLiteral` 单钉这一条, `review_verify_test.go` 把那一节里的
**引擎级差异**逐条做成差分测试。

- **`bytes_test.go`**(`[]byte` 系): 同一份语料下双向对拍 stdlib 与自家 `string` 孪生,
  外加每个方法一对手算的命中/不命中样例(钉死 `nil` vs 空切片), 以及那几条零拷贝契约
  (结果与输入共用底层数组 · 只读方法不改输入 · 无变化那条路复用 `src` ·
  `Match([]byte)` 比 `MatchString(string(b))` 分配更少)。
- **`reverse_test.go`**(反着扫): 正反答案逐位对拍(含 `^ $ \A \z (?m)^ \b (?i) (?s)` 与
  多字节 UTF-8)+ 状态数确实塌下来 + 方向是每条 pattern 各自的决定 + 库的反向比手写反转
  便宜 100 倍以上 + 反向 DFA 放弃时退回正向且答案不变。
- **`maxmem_test.go`**(单条内存预算): 读回 / 编译失败 / 语义不变, 外加
  "默认预算 flush, 给够就 0 flush"这条曲线。
- **`emptymatch_test.go`**: 逐个编译入口拒空串 + 一组正对照(含 `\C` · `\C+` 这类不可空形状
  必须照编不误)+ 4000 条 pattern 与 Go `regexp/syntax` 的差分。
- **`find_all_flat_test.go`** / **`match_step_test.go`**: `AppendAllStringIndexFlat` 与
  Step 形态各自对着 `FindAll*` 和 stdlib 双向钉住 —— 匹配集合 · 顺序 · 空匹配推进逐处相同。
- **`replace_func_ctx_test.go`**: 追加契约 · 回滚契约(`changed` 是"变没变"不是"匹配没匹配") ·
  复用不串味 · 稳态零分配。
- **`findallindex_test.go`**: 与一个 O(n²) 暴力参考逐位对拍 —— 参考实现把前后缀按字面量钉死
  再拿**整篇**正文去匹配, 这样 `^ $ \b` 看到的是真实位置(拿 `text[s:e]` 单独匹配会判出一堆
  假命中); 外加"批大小只影响怎么吐、不影响吐什么" · `ab|c` 连号游程可还原 ·
  同一个 alloc 反复扫不串味 · alloc 跨 set 或 `Close` 之后当场报错而不是给错答案。
- **`spanresolve_test.go`**: 锚定解析与另一个 O(n²) 暴力参考逐位对拍, 正反向各一遍;
  外加"给的是最长不是碰到的第一个" · "同一端点上多条 pattern 各算各的" ·
  `bound` 掐回看距离且不当词边界。
- **`spanscan_need_test.go`**: 按真实调用形态走完整流水线(反向 set 拿左端 → 锚定正则只跑命中
  那一段拿右端 → 优先级贪心相交即丢 → 一次升序替换), 钉死边界精确到字节 · 条数不多不少 ·
  以及"锚定的在错位置当场死 / 非锚定的会一路扫过来"这个机制本身; 同一个需求再用
  `ResolveSpan` 走一遍, 正向路(扫右端 → 求左端)与反向路(扫左端 → 求右端)两条互相对账。
- **`spanscan_stress_test.go`**: 把 `maxMem` 压到反复 flush + 批大小压到 1 反复挂起,
  结果必须与"预算充足 + 大批"一致 —— 挂起是"按内容存状态", 走错了不会崩, 会**悄悄少吐几段**;
  外加同一个 Set 上 8 个 goroutine 各拿一个 scanner 并发扫, `-race` 干净; 以及解析撞 flush
  (解析与扫描共用同一份 DFA 缓存, 起点状态被冲掉之后必须按内容重建,
  判据是紧预算下的答案与预算充足时逐字相同)。
- **`spanscan_bench_test.go`**: 新旧两种实现在 0 命中 / 稀疏 / 最坏输入三档语料上的对照测量
  (见[底座那一层的性能对照](#the-substrate-layer-benchmarks)); 另有 `TestSpanPerf_Shape`
  摊开每档的规模并钉死"两条路各自都盖住旧实现给的每一处匹配", `TestSpanPerf_NoAlloc`
  钉死稳态零分配, `TestSpanPerf_Peak` 把自己 fork 成子进程、一条路一个进程各报 `VmHWM`
  (跑在同一个进程里高水位是共享的, 谁先跑谁吃亏)。
- **三个 `Re2Set` 类型**: `re2set_fll_test.go` · `re2set_fll_astfuzz_test.go` ·
  `re2set_fll_viable_test.go` · `re2set_frel_test.go` · `re2set_rrl_test.go` ——
  语料按**每条 pattern 自己的 AST** 生成(随机字节撞不出真匹配 = 空转绿), 判据是与本库无关的
  **穷举实现**, 而且每套自带一格"三种口径必须给出不同答案"的自检(否则整套对拍是空转)。
  怎么钉的全文见
  [`doc/三种去重叠模式.md`](doc/%E4%B8%89%E7%A7%8D%E5%8E%BB%E9%87%8D%E5%8F%A0%E6%A8%A1%E5%BC%8F.md) §9。
- **`prefilter_test.go`**: 健全性 —— 真能匹配的 pattern 必须一条不落地出现在 `GetPotentials()` 里;
  外加 `(?:foo|[A-Z]{5})` 那个手写抽取器会答错的 case。
- **`arena_equiv_test.go`**: 把预算**饿着**扫(4 字节 arena 编码那两件事 —— flush 重建整张表 ·
  arena 搬家重定位 `state_cache_` 的键 / 每个 `State` 的 `inst_` / `start_[]` —— 只在这一档
  才真正发生), 命中集必须与**逐条 `Compile`** 的结果一致。🔴 ground truth 必须是逐条 `Compile`:
  拿 `Match` 跟 `MatchStats` 对拍验不出这类错, 两条路走的是同一张 `kManyMatch` DFA,
  一起错就一起错; 单条 `Regexp` 走的是另一张 DFA(`kFirstMatch`, 且能退回 NFA/OnePass),
  所以它是独立的一票。
- **`upstream_backport_test.go`**: 从上游(含尚未合并的 PR)回摘的每一条修复各自对拍 stdlib,
  清单与"没摘哪些、为什么"见 `VENDOR.txt`。

`doc/` 下每一篇引用到的数字, 都写清楚了是哪一个测试复现的。

<a id="license"></a>

## License

BSD 3-Clause, 与 RE2 相同。见 [LICENSE](LICENSE) 与 [RE2_LICENSE.txt](RE2_LICENSE.txt)。
vendored 的 RE2 文件保留 RE2 Authors 的版权。
