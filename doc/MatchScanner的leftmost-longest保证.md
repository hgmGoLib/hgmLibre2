# MatchScanner 的 leftmost-longest 保证

> 一句话: **MatchScanner 默认已经保证 leftmost-longest, 除非调用方自己挂了 `spanFast` 又挂错了。**
>
> 这一页讲四件事: 怎么用 · 这句保证是怎么兑现的 · 怎么用 fuzz 把更多正则从默认档拉进 `spanFast` 快档 ·
> 以及**反向 MatchScanner** 那一侧的 rightmost-longest(第 8 节)。
>
> API 速查在 `../readme.txt`, 英文长版在 `../README.md` 的 *Where a set matched: `MatchScanner`*,
> 实现和逐条论证在 `../matchscan.go` 的文件头注释。

---

## 0. 历史包袱: 别再引用"第三种口径"那段

2026-08-26 之前, `MatchScanner` 只有一条路 —— 游标启发式 —— 而它给的是**第三种口径**
(既不是 leftmost-first 也不是 leftmost-longest)。那一版的文档措辞是"变长档不保证和谁一样",
现在**只适用于 `spanFast` 这一档**。

之后加了路 B (窗口 + 正向非锚定), 并把它设成**零值 = 默认档**。所以今天的默认行为是有保证的。
看到"MatchScanner 不保证 leftmost-longest"这种话, 先确认它说的是不是 `spanFast`。

---

## 1. 怎么用

```go
set, _ := hgmLibre2.NewRegexpSet(patterns)   // 包级变量, 建一次

ms, unsup, _ := set.NewMatchScanner()        // 热路径上建一次留着; 不是并发安全的
defer ms.Close()
// unsup: 走不了区间这条路的那几条下标 (当下只有"能匹配空串"一个原因)。与正文无关,
// 建工作区那一刻就定死 —— 装表这一步把它们配成 boolOnly 或者干脆走老路。

// 可选: 每条 pattern 要什么。不调 = 全默认档 (要区间, 保证 leftmost-longest)。
ms.SetModes(modes)

err := ms.Scan(body, func(batch []hgmLibre2.SetMatch) {
    for _, m := range batch {
        // body[m.Lo:m.Hi] 是第 m.Index 条 pattern 的真匹配
        handle(m.Index, body[m.Lo:m.Hi])
    }
})   // 🔴 要么全给, 要么整遍不算数: err != nil ⟹ 这一遍作废, 整篇走老路 FindAll
ids := ms.HitIDs()      // 与 Set.Match 同解的那张命中表; ms.Hit(i) 是 O(1) 的同一个答案
```

三态旋钮:

| mode | 含义 |
|---|---|
| `MatchScanMode_span` (**零值**) | 要区间。库自动分档, 对外**保证 leftmost-longest**。 |
| `MatchScanMode_boolOnly` | 只要"命中没命中"。一处区间都不收口、一次端点都不补。 |
| `MatchScanMode_spanFast` | 要区间, 快, **不保证** leftmost-longest。见 §4。 |

🔴 **零值就是最稳那一档** —— 漏配一条的后果是**慢**, 不是**错**。这是故意选的零值方向。

🔴 `boolOnly` 不是可选优化: 挡掉的是那几条的**端点补全**(这一层真花钱的那步), 不只是少交几处。
门上很多位只当外层短路的 bool 用, 从来没人问它在哪 —— 真表上光两条这样的 pattern 就占了
**57%** 的游程。在回调里过滤是钱已经花完了才扔。

🔴 **能匹配空串的 pattern** 在 `NewMatchScanner` 就进 `unsupported` 名单, 配 `span`/`spanFast`
会被 `SetModes` **当场报错**(每个偏移都是零长命中, 游标推不过去), 不是运行时静默退化。
这种条只能配 `boolOnly` 或者走老路。钉在 `TestMatchScanSetModesRejectsEmptyCapable` 与
`TestMatchScanEmptyCapableFallback`。

---

## 2. 默认档凭什么敢说 leftmost-longest

分三条腿, 每条各自成立:

### (a) 定长 (`min == max`) —— 一句减法, 可论证

```
Lo = Hi - min
```

不进正则引擎。右端 `e` 的合法起点只能是 `e-min`(唯一), 长度也唯一 ⟹ 不存在"挑哪个起点"和
"取最长还是取贪心先撞上的"这两个问题。剩下的"从左往右取不相交"两种口径的规则一样。
所以**定长条上两条路必然同解, 档位对它不生效**。
钉在 `TestMatchScanStrictVsFindAll`(6 万条随机定长 pattern 对拍)。

### (b) 变长 —— 路 B 就是 leftmost-longest 的定义本身

拿到一个右端 `Hi` 之后, 从 `from = max(游标, Hi-maxL)` 起做**一次**正向非锚定搜索, 直接拿到
整段区间。

"最靠左的起点, 该起点上最长的匹配" —— 这就是 leftmost-longest 的定义。它**不挑 pattern 形状**,
不需要对 pattern 做任何静态判断, 也没有"某些形状会漏"的口子。

🔴 **为什么是一趟而不是两趟**(2026-08-27 之前是两趟)。leftmost-first(贪心)与
leftmost-longest **选同一个起点**, 只在终点上分歧。所以旧写法是"贪心搜一次定起点 → 再锚定
一次把终点重取成最长"; 现在直接用 **longest 口径编的那条单条正则**
(`RegexpSet.forwardOne` → `CompileLongestMaxMem`), 起点和终点在同一次搜索里一起给了。
答案一字不差 —— 钉在 `matchscan_paths_test.go`(与旧的两趟 set 路子逐字节对账)。

🔴 关键实现约束: 那次搜索传的是**整串 + startpos**(`Regexp.FindStringIndexFrom`), **不切片**。
切片会让 `\b` / `^` / `$` 在两个切口上看到假的邻字节 —— `re.FindStringIndex(s[lo:hi])` 是错的
写法, 别这么补。

代价: 每处命中只要一趟(比走两趟的 `spanFast` 还少); 贵在**没有长度上界的条目要走完空隙**
(各轮窗口两两不交且递增, 累加封顶 = 多扫一遍正文, **2.00x**)。实测 `benchPats`/64KB:
命中密集时 B 是 A 的 1.38x, 命中稀疏时 2.94x, 差价几乎全在走空隙上。

🔴 顺带的一件大事: 这一趟走的是 `RE2::Match` 那条**完整的路**(DFA → OnePass/BitState/NFA
逐级回退)。旧写法的第二趟走的是 set 的锚定解析, 那是 kManyMatch 的 DFA **独一条**, DFA 放弃
就只能整遍失败。换掉之后, 默认档这一档的 "DFA 放弃" 分支**根本不再发生**。

钉在 `TestMatchScanSpanIsLongest`: 语料就是路 A 的已知反例, 默认档必须逐字节等于
`re.Longest().FindAllStringIndex`。这个测试还**同时**数了路 A 岔开几条 —— 岔开 0 条就报错,
因为那说明这批反例失效了、测试变成空转绿。

### (c) 补不出来的 —— 退回去, 不猜

**宁可退回去也不给一个"像是对的"答案** —— 这是第三条腿, 也是前两条腿敢写成"保证"的前提。

"退回去"只有两个形状, 而且两个都是调用方**造得出来**的:

| 在哪儿交代 | 说的是什么 |
|---|---|
| `NewMatchScanner` 的 `unsupported` | `[]int32` —— 走不了区间这条路的那几条 pattern 下标。当下只有一个原因: 这条能匹配空串 (`PatternLenRange` 的 `min <= 0`), 每个位置都是一处零长命中, 游标压不住。 |
| `Scan` 的 `err` | 这一遍不算数 (已经交出去的批次也不算), 整篇走老路 `FindAll`。 |

`unsupported` **与正文无关**, 建工作区那一刻就定死、扫多少遍都不变 ⟹ 它能直接写成回归测试
(扔一条 `a*` 进 set, 必然报出它的下标, 见 `TestMatchScanEmptyCapableFallback`)。名单上的那几条
配成 `boolOnly` (命中表照样有它们) 或者自己走老路; 配了要区间的档 `SetModes` 当场报错。

`Scan` 的 `err` 三种来由都是 `maxMem` 配小了 —— 底下那遍 `FindAllIndex` 失败 / 补端点的单条
对象编不出来 / 锚定解析时 DFA 放弃。另有一种"游程不按扫描方向单调", 那是**本库的不变量崩了**
= bug, 也从这里以 `err` 交出来, 不吞。

#### 为什么不做成"部分成功"

旧版这里是一张 `unresolved` 名单, 每条带 `{Index, Reason, ResumeFrom}` 断点, 意思是"这几条
没给全, 你从断点起自己补"。2026-08-27 整个拆掉了。

理由不是那个断点算错了 (它是对的), 而是它服务的那条分支**调用方永远验不了**:

> 一个调用方造不出来的错误码, 不该出现在返回值里。

它逼出的是这么一条链 —— 调用方必须写兜底 → 兜底跑不到 → 跑不到就没法测 → 没法测的代码
基本是错的 → 真出事那天走的是一段从没执行过的路。那比"根本没有兜底、直接整遍失败"**更危险**。

而"锚定解析 DFA 放弃"实测**造不出来**: 它跑的是**小 DFA**(起点只有一个, 不是扫全文那个)。
三种形状 (`ab` · `[A-Za-z][A-Za-z0-9]{2,19}key` · `(?i)[a-z0-9]{3,20}@[a-z0-9.\-]{3,20}`)
在"刚好编得出来"的那道墙 (`maxMem` 分别是 2400 / 5800 / 24400) 上面 3000 字节的带子里每
100 字节一档, 拿 60KB 带中文的正文每 3 字节一个起点全量解析 —— **放弃 0 次**。墙底下则是
`NewRegexpSetMaxMem` 当场干净报错。根子在 prog 与 DFA 花的是同一笔 `maxMem`: prog 编得下,
剩的钱就够放一次解析走过的那几个状态; prog 编不下就在建 set 那一步失败。中间那条"编得出来
但解析不出来"的缝扫不到。

🔴 顺带一句"为什么不退化成 NFA": set 这条路上**根本没有 NFA**。单条正则有回退
(`re2_re2.cc` 里那一串 `Fall back to NFA below`), 但 `RE2::Set::Match` 上游自己就是
DFA-only (`re2_set.cc:216`, `dfa_failed` ⟹ 直接 `return false`) —— NFA 那套接口不回答
"命中的是哪一条", 而 `kManyMatch` 的 id 是 DFA 状态里那串 id 列表给的。要给 set 的锚定解析
配 NFA, 只能给每条 pattern 单独编一个 `\A(?:pat)` 的 `RE2` 对象, 那正是 `ResolveSpan` 存在
的全部理由被推翻。

---

### (d) 一条总规矩: 扫正文走 set, 补端点**全走单条对象**

2026-08-27 定的。扫正文那一遍走整表 set(那正是这一层存在的理由), 之后**补端点的每一趟都走
这一条 pattern 自己的单条对象**, 一趟都不再回 set。

| | 趟数 | 走谁 |
|---|---|---|
| 定长 | 0 | 一句减法, 不进引擎 |
| 默认档 | 1 | 正向单条 longest 非锚定 `FindStringIndexFrom` |
| `spanFast` | 2 | 反向单条锚定 `RegexpReverse.ResolveSpanWithin` → 正向单条 longest 锚定 `FindStringIndexAtWithin` |
| 反向 MatchScanner | 1 | 正向单条 longest 锚定 `FindStringIndexAtWithin`(bound 掐在游标上) |

**三条理由**:

1. 单条走的是 `RE2::Match` 那条完整的路(DFA → OnePass/BitState/NFA 逐级回退), DFA 放弃了
   还有下家; set 那侧的锚定解析是 kManyMatch 的 DFA **独一条**, 没有下家 —— "DFA 放弃"在
   那边只能整遍失败(`re2_set.cc:216`, `dfa_failed` ⟹ 直接 `return false`, 上游也是这么写的)。
   换掉之后, 只剩 `spanFast` 的第一趟(反向锚定)还会碰到这件事 —— 因为 RE2 自己求匹配左端
   也只有 DFA 一条路。
2. 补端点的流量不再冲刷整表那份大 DFA 缓存(真表 155 条那份), 两边互不干扰。
3. 状态更小: 单条不必背 kManyMatch 每个状态那张 id 表。

**答案一字不差**: `matchscan_paths_test.go` 的 `TestMatchScanPathsSameAsSetRoute` ——
两条路各 300 轮 AST 生成的语料, 共对账 5.4 万处区间, 与旧的 set 路子逐字节相同。
参照实现故意用**没删的那几个 set 入口**重算一遍, 所以这份钉子长期有效: 它钉的是"单条那条路
与 set 那条路同解"这件事本身, 不是某一次改动的快照。

**价钱**(64KB 语料 · `benchPats` · `BenchmarkMatchScanOldVsNew`, 新旧两套在同一个二进制里
同一次运行对照):

| 语料 | 默认档 旧→新 | `spanFast` 旧→新 |
|---|---|---|
| 命中密集 (`most`) | 968µs → 618µs (**-36%**) | 614µs → 447µs (**-27%**) |
| 命中稀疏 (`few`) | 持平 | 持平 |
| 零命中 (`zero`) | 持平 | 持平 |

稀疏档持平是对的: 那一档的时间全在"走空隙"上, 补端点那几趟本来就没跑几次。

🔴 顺带记一笔实现坑: `cre2_match_at` 原本每次调用 `std::vector<StringPiece> sub(nmatch)`,
即**每处命中一次 malloc/free**。补端点是按处命中调用的, 这笔常数在低命中密度上能量出来
(实测慢 2%)。改成 nmatch≤8 走栈上那块之后打平。

**缺的那个 API 是怎么补的**: 这条路上原本缺三样, 都补进 cre2 了 ——
`cre2_new_longest_max_mem`(longest 口径编译)· `cre2_match_at_anchored`(锚定在 startpos 的
`RE2::ANCHOR_START` 搜索)· `cre2_resolve_span_reverse_r`(单条的反向锚定解析, 实现就是
`RE2::Match` 自己求匹配左端时走的那一句: 反向程序 + `kAnchored` + `kLongestMatch`)。
最后那个**不能**拿"一条 pattern 的 set" 去凑: 单条 `Compile` 会把 `^` / `$` 从程序里摘成
`anchor_start_`/`anchor_end_` 两个标志, 而只有 `SearchDFA` 会去查这两个标志 —— 绕开它自己
驱动 DFA 的话 `^foo` 会在正文中间也认(钉在 `TestReverseResolveSpanWithinAnchorPattern`)。

## 3. 对拍要拿 `Longest()` 当 oracle

最容易踩的一个坑, 单独拎出来:

```go
want := regexp.MustCompile(pat)
want.Longest()                       // 🔴 少这一行, 下面全是假红
locs := want.FindAllStringIndex(text, -1)
```

`regexp.Compile` 默认是 **leftmost-first**(贪心), 和 leftmost-longest 在
"同一起点上贪心先撞到的比最长的短"时给不同右端。最小例子: `a|ab` 撞 `"abab"` ——
贪心给 `[0,1) [2,3)`, 最长给 `[0,2) [2,4)`。

拿默认那个 `FindAllStringIndex` 去对默认档, **对不上是 oracle 错了, 不是库错了**。

---

## 4. `spanFast`: 它是什么, 什么时候才该挂

`spanFast` 强制走**游标启发式**那条路, 两趟:

1. **反向单条**锚定回推一次(`RegexpReverse.ResolveSpanWithin`, 回看窗口掐在游标上)拿最靠左的起点;
2. **正向单条 longest** 锚定在那个起点上取最长右端(`Regexp.FindStringIndexAtWithin`)。

两趟的代价都与**正文长度无关**(= 这处命中有多长 / 回看多远)—— 这是它比默认档便宜的全部原因。
默认档只要一趟, 可它那一趟是**非**锚定的, 没有长度上界的条目要一路走完空隙。

### 它给的是"第三种口径", 而且这是结构性的

随机撒 3000 条变长 pattern × 40 段正文 = **12 万处**对账:

| 与谁相同 | 处数 |
|---|---|
| `FindAll` (leftmost-first / 贪心) | 119 940 / 120 000 |
| `Longest` (leftmost-longest) | 119 972 / 120 000 |
| 两个都不是 | **28** / 120 000 |

为什么必然是第三种, 而不是"选一个模式就行": 这一遍扫描给的是**右端集合**
`{e | 存在某个 s 使 text[s:e] 匹配}`, 里面**既没有 (起点,终点) 的配对, 也没有优先序**。
RE2 的贪心活在 NFA 指令的优先序里 —— `kFirstMatch` 撞到 Match 就 break 把低优先级线程整段截掉,
`kLongestMatch` 干脆把每段 sort 掉, 而要"所有右端"只能用 `kManyMatch`, 它正是把优先序扔得最
干净的那个(连 Mark 都没有)。**"拿到全部右端"和"贪心/最长"是互斥的**, 配对只能在 Go 这侧靠
游标重建 —— 重建出来的就是第三种。

已知的三个反例(也是 `TestMatchScanSpanIsLongest` 的语料):

```
x{1,3}[a-c]?(?:ab|cd)?   撞 "xab"        路A [0,3)="xab"      Longest [0,2)="xa"
(?:ab)?[bc]{1,2}         撞 "axbabbyxx"  路A [4,6)="bb"       Longest [3,6)="abb"
(?:ab)*b{1,3}            撞 "yaxyabbbb"  路A [5,8)+[8,9)      Longest [4,9)="abbbb"
```

造出这种"起点不随右端单调"的是**一条更短的分支结在另一条更长分支内部**。交替 (`abc|b`) 会,
而 `?` `*` `+` `{m,n}`(min≠max) 也**都是长度不齐的交替**(`(?:ab)?` 就是 `ab|`) —— 所以这在整个
变长档上是常态, 不是某几条 pattern 的毛病。

🔴 岔开那几处**是真匹配, 但边界偏了**, 两个真后果:
1. **无声漏报** —— `text[Lo:Hi]` 拿去过校验位(身份证 · IBAN mod-97 · Luhn)会失败,
   整条真命中被调用方自己毙掉, 比"没检测到"更难查;
2. **少盖字节** —— 拿这个区间去脱敏, 那几个字节的明文原样留在输出里。

### 但它不是下水道, 是"自动分档判错时的出口"

库只按 `min`/`max` 分档: 判成变长就一律落到默认档那条贵路。可是 **"变长" ≠ "有歧义"** ——
两头带 `\b` 的变长条实测岔开 **0 处**(同一份 12 万处对账: 裸 pattern 岔开 60 处, 写成
`\b(?:…)\b` 之后 **0 处**)。word boundary 把起点钉死, 回看窗口里合法起点只剩一个,
"挑哪个"根本不发生。

这种条落在默认档上是**白掏那笔走空隙的钱**(实测 `benchPats`/64KB: 命中密集时默认档是
`spanFast` 的 1.38x, 命中稀疏时 2.94x)。`spanFast` 就是给它们留的出口。

🔴 **挂之前先把凭据跑出来, 别凭感觉。** 跑出 0 岔开再挂 —— 挂上之后它既是快的那条路,
**又确实是 leftmost-longest**(只是这句由调用方的 fuzz 背书, 不由库背书)。
跑不出 0 就别挂, 默认档的保证是无条件的。

---

## 5. 怎么 fuzz 出这份凭据

现成的写法在 `asc/engine/sd_body_gate_span_cross_test.go`(`TestBodyGateSpan_EquivCrossFuzz`)。
照抄的时候**四件事一件都不能省**, 每一件都对应一种"空转绿":

### (a) 语料要从 pattern 自己的 AST 生成, 不是随机字节

随机字节撞不上 `\b[A-Z]{2}\d{6}\b` 这种条 —— 跑一亿例、零命中、绿。
正确做法是把 pattern 用 `regexp/syntax.Parse` 解析出来, 顺着 AST **反向生成一个能匹配的串**
(交替随机挑一支, `{m,n}` 随机取个次数, 字符类随机取个字符)。

### (b) 光有"能匹配的串"还不够, 要**交叉构造**

路 A 岔开的前提是"一条更短的匹配结在一条更长的内部", 单独一个干净实例撞不出来。
生成 **两个**实例 a / b, 然后:

```
a                                  // 单条本身: 内部可能就藏着一条更短的子匹配
a[:k] + b                          // b 从 a 中间起头  (k 扫遍 0..len(a))
a[:k] + b + a[k:]                  // b 整个嵌进 a 里
a + b[j:]                          // 两条首尾咬合    (j 扫遍 0..len(b))
a[:k] + a[k+1:] + b                // 挖掉 a 中间一个字符: 制造"只有更长的那条还成立"
```

### (c) oracle 按档取, 别取错

```go
if 这一位挂的是 spanFast {
    // 路 A 要对的是"我打算相信它等于 leftmost-longest"这句话
    std := regexp.MustCompile(pat); std.Longest()
    want = std.FindAllStringIndex(text, -1)
}
```

🔴 这里有个更隐蔽的坑: 如果你的对拍代码把默认档也一起跑了, 那部分**恒绿**(B 本来就等于
leftmost-longest), 它证明不了任何关于 A 的事。**要证的那一位必须显式配成 `spanFast`。**

### (d) 先给这套对拍装一道自检门 —— 它自己必须先能红

```go
// 已知反例必须被抓到, 否则下面全是空转绿
for _, c := range 那三个已知反例 {
    if 对拍(c, spanFast) == 通过 {
        t.Fatalf("已知反例居然对拍通过 —— 这套对拍看不见差异")
    }
}
// 同样这三条, 换成默认档必须【全过】—— 否则"B 恒等于 leftmost-longest"在库那侧没兑现
```

对应 `TestBodyGateSpan_CrossFuzzSelfCheck` 与 `TestBodyGateSpan_CrossFuzzPathBSelfCheck`。
**没有这道门的 fuzz 结果不算凭据。**

### (e) 库整遍报错 (`Scan` 的 `err`) 不算差异

生产上那种情形就是照走 `FindAll`, 不是错答案。对拍里要跳过, 不要记成岔开。

### 判据

**这条 pattern 的岔开处数 == 0** ⟹ 可以挂 `spanFast`。
不是 0 就别挂 —— 岔开处数少不等于安全, 那几处正是校验位会失败的地方。

---

## 6. 真实产品上跑出来的结果

ASCP 的敏感数据门(`asc/engine/`)把整张规则表接到了 `MatchScanner` 上, 现状:

| | |
|---|---|
| 接管的位数 | **56**(静态名单 53 位 + 凭据锚段 3 位) |
| 走默认档 (路 B) 的 | **0** —— `bodyGateSpanLongestBits` 是空表 |
| 走 `spanFast` 的 | **56**, 每一位都有 fuzz 凭据 |
| 凭据规模 | **3 983 754** 例交叉语料, 与各自 oracle **零差异** |

而**静态分析只能论证其中一部分**:

| 论证到什么程度 | 位数 |
|---|---|
| ① 定长 (`min == max`), 可论证 | 21 |
| ② 变长但两头锚定, 可论证 | 10 |
| ③ **只有对拍钉着**, 论证不出来 | **25** |

也就是说: 库自己那套按 `min`/`max` 的自动分档, 会把 ②③ 那 **35 位**全判成变长、全推到默认档
那条贵路上 —— 而 fuzz 说这 35 位**一位都不需要**。判错 35/56 = **62%**;
就算加上"两头锚定"这道判据, 仍有 25/56 = **45%** 只能靠对拍。

**这就是 `spanFast` 存在的全部理由。** 不是"给不在乎正确性的人用的下水道",
是自动分档判错时, 调用方拿凭据把它掰回来的那个出口。

---

## 7. 成本

🔴 下面两组数**表和语料不同, 不许横着比**, 而且要看清是哪一档量的。

**(一) 生产形态一张 set: 90 条 · 52 位要区间 · 7.03MB**(2026-08-26, 三条路一起量的那次)

| 档 | 总时间 | 相对老路 |
|---|---|---|
| old (门 Match 一遍 + 命中的每条各自 `FindAll` 整篇, 口径 leftmost-first) | 78.2ms | 1.00× |
| **默认档** (窗口 + 正向非锚定) | 43.8ms | **1.79×** |
| **`spanFast`** (游标启发式) | 24.6ms | **3.18×** |
| 只过门不收口 (全 `boolOnly`) | 21.9ms | 3.57× |

三档交出的处数完全一样(10956 处), 区间也完全一样 —— 这张表上的位两头都锚死。

**(二) 155 条规则表 × 6.4MB 真语料 · 命中 47 条 · 稳态 · 生产预算 64MB**

🔴 这组是 **`spanFast` 那条路**量的(量的时候还没有默认档那条路):

- 整条腿 369.3ms → 24.6ms = **15.03×**(兜底 0 条)
- 只留定长档(87 条要位置的里只有 28 条是定长): 368.8ms → 312.1ms = 1.18×
- 按正文长度: 8KB 以下打平 (1.0×) · 32KB 3.1× · 512KB 6.1× · 2MB 14×
- 最坏(每 38 字节一处命中的合成串): **0.94×**, 即 6% 慢 —— 每处命中两次 cgo 往返,
  正文短到几乎全是命中时这笔固定开销赢不了
- 内存: 进程 VmHWM +0.9MB · Go 分配 4.0MB/2252 obj → ~0/146 obj


---

## 8. 反向 MatchScanner: 同一件事的镜像, 口径是 rightmost-longest

`RegexpSetReverse.NewMatchScanner()` 开出来的是 `*MatchScannerReverse`。方法名、回调形状、
`unsupported` 名单与 `err` 的语义 (见第 2 节 (c))、那块固定 12KB 缓冲 —— 与正向那个
**逐字相同**。只有两处不一样:

1. 交出来的区间按 `Lo` **降序**(正向是升序);
2. 去重叠的口径是 **rightmost-longest**(正向是 leftmost-longest)。

### 8.1 两种口径没有本质区别

两边保证的三条是同一组:① `text[Lo:Hi]` 是真匹配 · ② 同一条 pattern 内部互不相交 ·
③ 有匹配的地方不静默。差别只出现在**两个真匹配互相交叠**的地方 —— 不交叠的正文上逐处相同:

| 正文 | pattern | leftmost-longest | rightmost-longest |
|---|---|---|---|
| `abab` | `a\|ab` | `[0,2) [2,4)` | `[2,4) [0,2)` —— 同一批, 顺序反 |
| `aab` | `ab\|b` | `[1,3)` = `"ab"` | `[2,3)` = `"b"` —— 这里才真差 |

后一种局面下"谁赢"由方向定, 两边给的都是真匹配、都不重叠、都不漏段。怎么选:

- 要跟 stdlib 的 `re.Longest().FindAllStringIndex` **逐字节对上** ⟹ 用正向那个;
- 只是要"把这片正文里的东西都框出来"(脱敏 · 定位 · 计数)⟹ 两个都行。

### 8.2 什么时候该反着扫

表里有**正着扫爆状态、反着读塌回线性**的 pattern 的时候 —— `S B{m,n} L` 里起始类严格窄于
重复类那一族(全文见 `状态数为什么会相乘.txt` §3)。实测 `[A-Za-z][A-Za-z0-9]{2,19}key`
× 120 份 8KB 语料:

| | 状态数 | 状态区 |
|---|---:|---:|
| 正向 | 66572 | 8.39MB |
| 反向 | 42 | 0.07MB |

在这一层补上之前(2026-08-27), 这种表反着扫只能当**门**: `Match` 回答哪几条命中, 要位置
还得正向再扫一遍全文 —— 而"把 1+k 遍压成 1 遍"正是 MatchScanner 存在的全部意义。

🔴 方向是**每条 pattern 各自**的决定, 不是一张表的属性: 各建一个单条正/反向 set, 拿真语料
比 `MemInfo().States`, 小的那边就是它该去的那一组。反向 set 本身仍然必须**一条一个或者很小
一张表** —— set 里状态数是相乘的, 155 条的反向表在 6.4MB 上是 65 秒 / arena 顶满 254MB。

### 8.3 反向【更好】做, 不是更难做

正向那一遍 DFA 交出来的是匹配**右端**, 起点得在 Go 这侧猜回去 —— 第 4 节那一整段"第三种
口径"讲的就是这件事的代价。反向交出来的是**左端** = 起点, 而 leftmost/rightmost-longest
这个口径本来就定义在起点上, 所以这一层**没有"猜"这一步**:

```
反向 set FindAllIndex                                      → 匹配左端, 按扫描方向(从右往左)单调
正向【单条】FindStringIndexAtWithin(from=左端, bound=游标)  → 【最长】右端, 且绝不越过游标
```

于是:

- 没有路 A / 路 B 之分;
- 没有 `spanFast` 这一档(配了 `SetModes` **当场报错**, 不是静默忽略);
- 不需要 `maxL` 窗口 —— 每处命中恒等于一趟锚定搜索, 代价 = 这处命中有多长, 与正文长度无关。

🔴 顺带解掉了正向那边接不了的一族: 正向默认档要 `maxL` 才框得出回看窗口, **没有长度上限**
的 pattern(邮箱那种)框不出来; 反向这一侧根本不需要窗口。

### 8.4 为什么"从右往左"仍然一个字节都不用攒

因为口径也跟着翻了。硬要在反向扫描上给 leftmost-longest 才要攒: 手上这一处随时可能被更靠左、
还没扫到的那一处整个吃掉 —— 有上界的还能靠一个 `maxL` 宽的延迟缓冲兜住, 无上界的得攒到整篇
扫完, 内存跟着正文长, 正是这一层存在的理由被赔掉。

改成 rightmost-longest 这件事就没了: 从右往左走,**第一个见到的起点就是最终答案**, 左边不可能
再来一个把它顶掉 —— 与正向"第一个见到的就是最终答案"是同一句话照镜子。所以游标照样在回调里
当场推完, 输出照样写进那块固定的 12KB 缓冲。

### 8.5 正确性怎么钉的

`matchscan_reverse_test.go`, 四件缺一不可(与第 5 节同一套规矩):

1. **语料从每条 pattern 自己的 AST 生成** —— 随机字节撞不出真匹配, 那是空转绿;
2. **判据与本库无关**: 拿 stdlib 的 `\A(?:pat)\z` 逐 `(s,e)` 穷举出 rightmost-longest 序列;
3. **名单里前五条正是把正向路 A 搞岔的那几个反例** —— `abc|b` · `a|ab` ·
   `x{1,3}[a-c]?(?:ab|cd)?` · `(?:ab)?[bc]{1,2}` · `(?:ab)*b{1,3}`。反向这一侧**一处都不岔**,
   因为没有"猜"这一步;
4. **判据自身有一道自检门**: `ab|b` 撞 `"aab"` 两种口径必须给出不同答案, 否则整套对拍是空转。

🔴 语料生成只取 ASCII 可见字符, 这是判据的限制不是被测对象的: 判据是拿 `text[s:e]` 切片跑
stdlib 的, 切在多字节 UTF-8 中间会被当成 U+FFFD, 从而多报一处起点 —— 那是判据的伪影。
(这一条是实测踩出来的: 不限 ASCII 时判据在 `[^...]{0,255}` 那条上报了 296/300 份"岔开",
逐处看下去全是切在 `À` / `Ĥ` 中间。)
