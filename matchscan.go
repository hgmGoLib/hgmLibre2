// matchscan.go —— 一遍扫正文, 边扫边【一批一批】交出各条 pattern 的不重复命中区间。
//
// 🔴 一句话: 【默认档已经保证 leftmost-longest】, 除非调用方自己挂了 spanFast 又挂错了。
//    怎么用 · 这句保证怎么兑现的 · 怎么用 fuzz 把更多条拉进 spanFast 快档:
//    doc/MatchScanner的leftmost-longest保证.md。
//
// 🔴 镜像那一半在 matchscan_reverse.go: RegexpSetReverse.NewMatchScanner —— 从末尾往前扫,
//    口径 rightmost-longest, 区间按 Lo 【降序】。表里有"正着扫爆状态、反着读塌回线性"的
//    pattern (S B{m,n} L 那一族) 才用它; 两种口径的差别见那份 doc 第 8 节。
//
// ── 它替掉的是哪段路 ────────────────────────────────────────────────────────
// 调用方今天的写法是"两段式": 先 Set.Match 扫一遍拿到"哪几条命中"(一张 bool 表), 然后为了知道
// 【命中在哪】, 把这几条各自的 Regexp 拿出来对整篇正文再跑一遍 FindAllStringIndex。命中 k 条
// 就是 1 + k 遍全文。
//
// 可是位置本来就在第一遍里算出来过 —— kManyMatch 的 DFA 每走到一个能结束匹配的字节都会记一次,
// 走完把它扔了 (这正是 FindAllIndex 接出来的东西, 且【不额外要钱】: 同一份正文同一份 DFA
// 缓存, 6.4MB 上实测 Match 18.5ms / 收游程 18.4ms)。MatchScanner 就是把 FindAllIndex 给的
// 右端补成完整区间 —— 它整个就是【搭在 FindAllIndex 上面的一层】, 自己不碰 native。
//
//	SetModes(modes)            【每条要什么】三态: 只要 bool / 要区间(默认, 自动分档) /
//	                           要区间但强制走便宜那条路。全文见下一节。
//	Scan(text, batchFn)        一遍全文。命中表 (HitIDs / Hit, 与 Set.Match 同解) 边扫边填,
//	                           命中区间【边扫边收口、攒够一批就交出去】。
//	                           只返回 err: 要么全给, 要么整遍不算数 —— 见下面那一节。
//
// 🔴 一批一批交出去是【内存】上的要求, 不是风格。底下 FindAllIndex 本来就是 sqlite3_step
//    式的: 一批 4096 条游程 (48KB) 装满就挂起、交给 Go、取走再进去接着扫, 缓冲循环复用,
//    不随正文长度涨。要是这一层把结果全 append 攒起来等扫完再还给调用方, 那个固定缓冲就白
//    设计了 —— 实测真表上游程约 30741 条/MB, 收口后的输出还有 0.037MB/MB, 200MB 的 body
//    就是每个并发扫描 7.4MB 常驻, 而且 AppendMatches 那种"再照抄一份给调用方"的接口等于
//    两份缓冲。现在这一层【一个字节都不留】: 游标推进在回调里当场跑完, 收口出来的区间写进
//    一块固定的 matchScanBatch 缓冲, 满了就交出去、就地复用。
//    真表实测: 游程 196744 条 → 输出 74249 处, 而且其中 57% 的游程来自两条【只当 bool 用】的
//    pattern, boolOnly 一挡就没了 —— 挡掉的是那几条的【端点补全】(真花钱的那步), 不只是内存。
//
// 🔴 交给 batchFn 的那段切片是内部缓冲【本身】, 下一批原地覆写。要留就自己 append 走。
//
// 🔴 交出来的顺序【不按 pattern 分组】, 各条 pattern 的结果按收口先后交错着来 (同一条 pattern
//    内部仍按 Lo 升序)。想按条归拢是调用方那边一句 append 的事, 库这边归拢就得攒 = 又是缓冲。
//
// 能边扫边收口是因为同一条 pattern 的游程【跨批次也是升序】的 (正向扫本来就从左往右走)。
//
// ── "没能全给你"这件事: 一张建工作区就交的名单 + 一个整遍的 err ─────────────
//
// 🔴 这一层【没有】"扫到一半反悔、这几条你自己补"的中间态 (2026-08-27 拆掉的; 之前那个
//    Scan 返回 unresolved 名单的设计是错的, 原委见下)。现在只有两处交代:
//
//	NewMatchScanner 的 unsupported   走不了区间这条路的那几条 pattern (当下只有一个原因:
//	                                 能匹配空串)。【与正文无关】, 建工作区那一刻就定死,
//	                                 想写回归测试就写 —— 扔一条 a* 进去必然报出它。
//	Scan 的 err                      这一遍不算数, 整篇走老路 FindAll。三种来由都是
//	                                 maxMem 配小了 (底下那遍 FindAllIndex 失败 / 单条对象
//	                                 编不出来 / 锚定解析放弃), 另有一种"游程乱序"是本库
//	                                 的 bug。
//
// 🔴 为什么不做成"部分成功": 一个调用方【造不出来】的错误码, 不该出现在返回值里。它逼出
//    的是这么一条链 —— 调用方必须写兜底 → 兜底跑不到 → 跑不到就没法测 → 没法测的代码基本
//    是错的 → 真出事那天走的是一段从没执行过的路。那比"根本没有兜底、直接整遍失败"更危险。
//    量过: 锚定解析用的是【小 DFA】(起点唯一), 不是扫全文那个; 三种形状的 pattern 在"刚好
//    编得出来"的那道墙上面 3000 字节的带子里逐档细扫 (60KB 正文 · 每 3 字节一个起点),
//    放弃 0 次 —— 墙底下则是 NewRegexpSetMaxMem 当场干净报错。所以这条分支本来就该是
//    "整遍失败"这种粗粒度的交代, 而不是一套调用方永远验不了的补偿逻辑。
//
// ── 三态旋钮 (SetModes): 每条 pattern 要什么 ─────────────────────────────────
//
//	MatchScanMode_boolOnly           只要"命中没命中"。一处区间都不收口、一次端点都不补。
//	                                 门上很多位只当 bool 用 (某某类内容在不在), 从来没人问它
//	                                 在哪 —— 那几条不该为它花补端点的钱。
//	MatchScanMode_span               要区间。【零值 = 默认档】。库自动分档 (下一节),
//	                                 对外保证 leftmost-longest。
//	MatchScanMode_spanFast           要区间, 快, 【不保证】leftmost-longest。自动分档把某条
//	                                 判成变长而落到路 B、性能又确实亏了的时候, 调用方可以自己
//	                                 拿 fuzz 把"这条走路 A 也是 leftmost-longest"跑出来, 跑出来
//	                                 了就挂这一档 —— 挂上之后它既快, 又确实是 leftmost-longest。
//
// 🔴 三态而不是"两张 mask"(要不要位置 × 走哪条路), 是因为 want=false 且 pathB=true 是个
//    无意义组合 —— 两张 mask 早晚打架, 一张三态的表打不起来。
//
// ── 对外那条保证, 逐字读 ─────────────────────────────────────────────────────
//
// 默认档 (MatchScanMode_span) 交出来的区间:
//   ① text[Lo:Hi] 是这条 pattern 的一个真匹配;
//   ② 【同一条 pattern】吐的区间互不相交, 按 Lo 升序;
//   ③ 口径是 leftmost-longest (= stdlib 的 re.Longest().FindAllStringIndex)。
//
// 三处容易读错的地方:
//   🔴 ② 只管【单条】。两条 pattern 在同一片正文上照样重叠 —— 那不是重复, 是两个问题各要
//      一个答案 (下面那段"只在同一条 pattern 内部去重"讲的就是这件事)。
//   🔴 ③ 【不是】"与 FindAllStringIndex 相同"。stdlib 默认的 FindAll 是 leftmost-first
//      (贪心), 两者在"同一起点上贪心先撞到的比最长的短"时给不同的右端。要对拍就拿
//      Longest() 那个去对, 拿默认那个对会是【假红】。
//   🔴 能匹配空串的 pattern 只能配 boolOnly, SetModes 当场报错 —— 不是运行时静默退化。
//
// ── 端点怎么补: 两条路 + 一句减法 ────────────────────────────────────────────
//
//	定长 (min == max)  Lo = Hi - min。【不进正则引擎】, 一句减法。两条路在定长上必然同解
//	                   (起点唯一), 所以定长恒走这句, 档位对它不生效。
//
//	路 B (默认档)      从 max(cur, Hi-maxL) 起做一次【正向非锚定】搜索拿最左起点 s, 再从 s
//	                   锚定取最长右端。恒等于 leftmost-longest (那就是它的定义), 不挑
//	                   pattern 形状。用的是【这一条自己一条】的正向 Regexp
//	                   (RegexpSet.forwardOne → Regexp.FindStringIndexFrom, 整串 + startpos,
//	                   不切片 ⟹ \b / ^ / $ 看到的是真实邻字节)。
//	                   代价: 每处命中比路 A 贵约 1.6x, 外加无上界的条目要走完空隙 ——
//	                   各轮窗口两两不交且递增, 累加封顶 = 多扫一遍正文 (2.00x)。
//
//	路 A (降级档)      从 Hi 往左【锚定】推一次, 用【这一条自己一条】的反向对象
//	                   (RegexpSet.reverseOne → RegexpSetReverse.ResolveSpanWithin), 一次调用
//	                   给最靠左的那个起点。代价 = 回看多远, 与正文长度【无关】—— 这是它比
//	                   B 便宜的全部原因。给的口径见下面"第三种口径"那一节。
//
//	两条路要的那个单条对象编译不出来 = maxMem 配小了, Scan 整遍报错 (见上面那一节)。
//
// 🔴 反向必须是【一条一个 set】。整表建一个反向 set 是死路: set 里状态数是相乘的
//    (doc/状态数为什么会相乘.txt), 155 条的反向表在 6.4MB 正文上实测 65 秒 / arena 顶满 254MB
//    还在 flush, 而正向同一张表 18ms 零 flush。拆成一条一个就没有这个乘法; 而且这些反向 set
//    【从不用来扫正文】, 只做锚定解析 —— 起点只有一个, 那套 `.*?` 前缀引起的状态爆炸机制
//    从根上就不存在。再加上惰性: 只有真被问到的那几条才会被建出来。
//
// ── 重复: 每条 pattern 一个游标, 一次左到右推进 ──────────────────────────────
// 变长 pattern 在一片正文上会在每个可收的位置各报一个右端 (\p{Han}{2,4} 撞 "张三李四王五"
// 报 6/9/12/15/18 五个), 它们说的其实是同一片区域。推进的规矩:
//   ① 右端落在已吐出去那一处里面 (Hi <= cur) → 跳过;
//   ② 否则求【起点不早于 cur 的最靠左那个】起点 —— 回看窗口掐在 cur 上, 绝不越过游标;
//   ③ 从这个起点取最长右端吐出去, cur 推到它。
// 🔴 ② 的窗口掐在 cur 上是【正确性】不是省钱: 不掐的话上例 Hi=18 会回推到 6, 与刚吐出去的
//    [0,12) 相交而被丢掉, "王五"就无声无息漏了。掐上之后回推到 12, 吐 [12,18)。
//
// 🔴 这套推进【只有在起点随右端单调不减时】才等于 FindAll。反例: abc|b 撞 "abc" 的右端是
//    2 和 3 —— 右端 2 回推到起点 1 ("b"), 当场吐 [1,2) 把 cur 推到 2, 右端 3 那个真正的
//    [0,3)="abc" 就因为起点 0 < cur 被永远跳过, 【无声漏掉】。
//    造出这种非单调的是"一条更短的分支结在另一条更长分支【内部】"—— 交替 (abc|b) 会,
//    而 ? * + {m,n}(min≠max) 也都是【长度不齐的交替】: (?:ab)? 就是 ab| 。所以这件事
//    在整个变长档上是常态, 不是某几条 pattern 的毛病。
//
// 🔴 【只在同一条 pattern 内部去重, 跨 pattern 一概不合并】。两条 pattern 在同一片正文上各自
//    命中不是重复, 是两个问题各要一个答案 (带空格的和不带空格的两条 pattern, 下游正是靠
//    "这一段里有没有空格"分流的; 合了就是漏检)。
//    实例: "Passport No: A123456780"
//    上 \b[A-Z][12]\d{8}\b (台湾身份证) 与 (?i)\b[A-Z]{1,2}\d{7,9}[A-Z]?\b (护照号) 抢
//    【完全同一段】[13,23)。合并的话下标在前的台湾身份证先占, 而它自己又过不了 mod-10
//    校验位被消费点毙掉 ⟹ 这段明文护照号一条都不出。"谁赢"只由常量写在第几行决定, 而
//    "谁能活"要等消费点把校验位跑完才知道 —— 库这层两样都不知道。真语料上被 ≥2 条 pattern
//    盖住的字节占已盖住字节的 55.6%, 同一字节最多被 8 条盖, 所以这是常态不是边角。
//
// ── 路 A 给的是"第三种口径" (挂 spanFast 之前必须读完这一节) ─────────────────
//
// 下面整节讲的都是【路 A】。默认档走 B, 没有这些坑 —— 代价是上一节那 1.6x / 2.00x。
// 🔴 挂 spanFast 要的那份凭据怎么跑 (语料必须从 pattern 自己的 AST 生成再交叉构造; 对拍要拿
//    Longest() 当 oracle; 对拍自己先要有一道"已知反例必须红"的自检门), 见
//    doc/MatchScanner的leftmost-longest保证.md 第 5 节 —— 少一件就是空转绿。
//
//	定长档 (min == max)      与 FindAllStringIndex 【逐字节相同】, 而且是可以论证的:
//	                         右端 e 的起点只能是 e-min (唯一), 所以"起点随右端单调"必然成立;
//	                         长度也唯一, 不存在"取最长还是取贪心先撞上的"之分。剩下的就是
//	                         "从左往右贪心取不相交", 那正是 FindAll 的规则。
//	                         回归钉在 TestMatchScanStrictVsFindAll + 6 万条随机定长 pattern 对拍。
//
//	变长档 (min < max)       【第三种口径】(路 A 才有), 既不是 leftmost-first (贪心 / FindAll), 也不是
//	                         leftmost-longest (POSIX / re.Longest())。只保证三条:
//	                         ① text[Lo:Hi] 是这条 pattern 的一个【真匹配】
//	                         ② 同一条吐的区间互不相交、按 Lo 升序
//	                         ③ 有匹配的地方一定吐点什么 (不整段静默)
//
// 🔴 为什么必然是第三种, 而不是"选一个模式就行"—— 这一遍扫描给的是【右端集合】
//    {e | 存在某个 s 使 text[s:e] 匹配}, 里面【没有】(起点,终点) 的配对信息, 也【没有】
//    优先序: RE2 的贪心就活在 NFA 指令的优先序里 (re2_dfa.cc:1197 kFirstMatch 撞到 Match
//    就 break, 把后面低优先级的线程整段截掉), 而 kLongestMatch 干脆把每段 std::sort 掉
//    (:1272), kManyMatch 连 Mark 都没有、整串 sort (:1289) —— 我们要"所有右端"就只能用
//    kManyMatch, 而它正是把优先序扔得最干净的那个。一遍搜索本来也只吐【一个】右端
//    (want_earliest_match 第一处就 return :2562, 否则跑完取 lastmatch :2631)。
//    所以"拿到全部右端"和"贪心/最长"是互斥的, 配对只能在 Go 这侧用游标启发式【重建】,
//    重建出来的就是上面那第三种。
//
//    实测 (随机撒 3000 条变长 pattern × 40 段正文 = 12 万处对账):
//      与 FindAll(贪心) 相同  119940 / 120000
//      与 Longest(最长) 相同  119972 / 120000
//      两个都不是                 28 / 120000
//    差的那些长这样:
//      x{1,3}[a-c]?(?:ab|cd)?  撞 "xab"        本路 [0,3)="xab"   FindAll [0,2)="xa"
//      (?:ab)?[bc]{1,2}        撞 "axbabbyxx"  本路 [4,6)="bb"    FindAll [3,6)="abb"
//      (?:ab)*b{1,3}           撞 "yaxyabbbb"  本路 [5,8)+[8,9)   FindAll [4,9)="abbbb"
//
// 🔴 差出来的段【是真匹配, 但比 FindAll 那一处长/短/偏】, 两个真后果:
//    ① 少盖的字节 —— 调用方按这个区间脱敏, 那几个字节的明文原样留在输出里。
//    ② 边界偏了 —— text[Lo:Hi] 拿去过校验位 (身份证 · IBAN mod-97 · Luhn) 会失败,
//       整条真命中被调用方自己毙掉 = 无声漏报, 比"没检测到"更难查。
//
// 🔴 路 A 这件事【两头 \b 一锚就没了】: word boundary 把起点钉死, 回看窗口里合法起点只剩一个,
//    "挑哪个"根本不发生。量过 (各 12 万处对账): 裸 pattern 与 FindAll 不同 60 处 (少盖 30
//    字节 · 多盖 17 字节 · 处数变了 16 处), 换成 \b(?:…)\b 之后【0 处】。
//    所以规矩是: 变长条【两头有锚点】(\b · ^ · $ · 固定分隔符) 才可以拿位置去切片再判;
//    没有锚点的变长条, 位置只当"这附近有东西"的定位用。定长档 (min == max) 永远可以。
//
//    (曾经有过一道 PatternLeftmostLongestSafe 静态闸, 想把"最长 ≠ 贪心"的 pattern 挡在
//     门外。2026-08-25 删掉了: 它只查 OpAlternate, 而 ? * + {m,n} 同样是长度不齐的交替,
//     把口子堵严的话它就退化成 min == max 本身 —— 也就是变长快路整个清空。既然它兑现不了
//     "等于 FindAll"这个承诺, 就不该拿 8 倍的钱去买: 真表上闸装着 1.82×, 拆掉 15.03×。)
//
// Scan 报了 err 就整篇走老路 FindAll —— 库这边宁可退回去也不给一个"像是对的"答案。
// 老路要是想从某个偏移接着扫, 【别切片】: (*Regexp).FindStringIndexFrom(text, pos) 参数是
// 原串偏移, \b / ^ / $ 看到的还是真邻居 (见 find_from.go)。
// 配了 boolOnly 的那几条从来不参与收口, 也就无所谓补不补。
//
// 生命周期同 FindAllIndex 的 alloc: 可复用工作区, 热路径上建一次留着,【不是并发安全的】。
// text 只在 Scan 那一遍里被引用 (补左端要读它), Scan 返回之后这一层不再持有它。
package hgmLibre2

import (
	"errors"
	"strconv"
)

// matchScanBatch 是一批最多交出去几处命中区间。12 字节一处, 1024 处 = 12KB, 一次性的固定
// 开销, 不随正文长度涨。跟 findAllIndexBatch 一样【不做成旋钮】—— 底下那批 4096 条游程收口
// 出来大约 1/3 到 1/2 是输出, 这个数就是照着它配的, 没有值得调用方去调的余地。
const matchScanBatch = 1024

// SetMatch 是一处命中: text[Lo:Hi] 是第 Index 条 pattern 的一个真匹配。
type SetMatch struct {
	Index int32
	Lo    int32
	Hi    int32
}

// MatchScanner 是可复用工作区。用 (*RegexpSet).NewMatchScanner 开, 不用了 Close。
type MatchScanner struct {
	set   *RegexpSet
	alloc *RegexpSet_FindAllIndex_Alloc_t
	text  string
	per   []msPat_t
	mode  []MatchScanMode_t // 每条要什么 (见 SetModes); nil = 全走默认档
	hit   []bool
	hits  []int32 // 上一遍命中过的下标 (= Set.Match 那张表), 去重且只含真命中
	// scanErr 是本遍出的错。收口是在 FindAllIndex 的回调里跑的, 那里没法 return, 所以记
	// 在这儿由 Scan 收尾时交出去。🔴 只记第一个不覆盖: 后面的错多半是第一个的连锁。
	scanErr error
	// out 是那块固定的输出缓冲 (长度恒为 matchScanBatch), outN 是已填几处。
	// 满了就交给 fn 再从头填 —— 整个 Scan 期间这一层留下的就只有这 12KB。
	out  []SetMatch
	outN int
	fn   func([]SetMatch)
	// findCtx 是 B 路那一步"从游标起找最左匹配"的复用 scratch (稳态零分配)。
	findCtx *FindStringIndex_ctx_t
}

// msPat_t 是每条 pattern 的推进状态。cur 就是那把游标。
//
// 🔴 分档所需的那几件事 (spanable · fixed · minL · maxL · pathB) 全是【与正文无关】的,
//    所以一律在 NewMatchScanner / SetModes 里算完, Scan 里一件都不算 —— 这正是"没有
//    unresolved"的前提: 一条 pattern 能不能走这条路, 建工作区那一刻就有答案。
type msPat_t struct {
	spanable bool // 能不能收口成区间 (false = 能匹配空串, 只能当 bool 用)
	fixed    bool
	pathB    bool
	minL     int32
	maxL     int32             // PatternLenRange 的上界; <0 = 无上界 (窗口退化成"从游标起")
	rev      *RegexpSetReverse // 路 A 用: 反向锚定回推起点, 惰性建
	fwd      *Regexp           // 路 B 用: 这一条自己那条正向正则, 惰性建
	cur      int32             // 已吐出去的最右字节
	lastLo   int32             // 上一条游程的左端, 用来确认升序
	done     bool              // 从游标起整篇再没有匹配了 —— 本遍这一条到此为止
}

// NewMatchScanner 开一个工作区。热路径上建一次长期留着, 别每次扫描新建。
//
// unsupported 是【走不了区间这条路】的那几条 pattern 的下标 —— 当下只有一个原因: 这条能
// 匹配空串 (PatternLenRange 的 min <= 0)。每个位置都是一处零长命中, 游标压不住, 吐出来的
// text[Lo:Lo] 对下游也没有意义。
//
// 🔴 这张名单是【建工作区那一刻就定死】的: 它只看 pattern 本身, 与你之后喂什么正文无关,
//    所以它也是唯一一处"这条给不了你"的交代 —— Scan 那一遍要么全给, 要么整遍报错, 不存在
//    "扫到一半反悔"。名单上的那几条: 配 MatchScanMode_boolOnly (命中表照样有它们), 或者
//    自己走老路 FindAll。不配 boolOnly 而配了要区间的档, SetModes 当场报错; 压根不调
//    SetModes (全默认档) 的, 它们自动按 boolOnly 处理 —— 反正你在这里已经知道了。
//
// 名单可以直接写回归测试: 把 a* 之类扔进 set, 这里必然报出它的下标。
func (s *RegexpSet) NewMatchScanner() (m *MatchScanner, unsupported []int32, err error) {
	alloc, err := s.NewFindAllIndexAlloc()
	if err != nil {
		return nil, nil, err
	}
	m = &MatchScanner{
		set:     s,
		alloc:   alloc,
		per:     make([]msPat_t, s.size),
		hit:     make([]bool, s.size),
		out:     make([]SetMatch, matchScanBatch),
		findCtx: NewFindStringIndex_ctx(),
	}
	for i := 0; i < s.size; i++ {
		p := &m.per[i]
		minL, maxL := s.PatternLenRange(i)
		if minL <= 0 {
			unsupported = append(unsupported, int32(i))
			continue // spanable 留 false: 之后一律当 bool 用
		}
		p.spanable = true
		p.minL, p.maxL = int32(minL), int32(maxL)
		p.fixed = minL == maxL && maxL >= 0
		p.pathB = !p.fixed // 默认档 = 路 B; SetModes 里按档位再拧一遍
	}
	return m, unsupported, nil
}

// MatchScanMode_t 是【每条 pattern 要什么】。三态, 零值 = 默认档 (要区间, 自动分档)。
// 全文见文件头"三态旋钮"那一节。
type MatchScanMode_t string

// MatchScanMode_span 要区间, 由库【自动分档】, 保证 leftmost-longest (定长走减法, 变长走
// 路 B)。零值就是它 —— mask 里没显式写的那几条都是这一档。
const MatchScanMode_span MatchScanMode_t = ""

// MatchScanMode_boolOnly 只要"命中没命中", 一处区间都不收口、一次端点都不补。
// 这几条照样进命中表 (Hit/HitIDs), 只是一处区间都不给。
//
// 🔴 这是最值钱的一档: 真表上 57% 的游程来自两条只当 bool 用的宽 pattern, 这一挡挡掉的是
//    它们的端点补全 (真花钱的那步), 不只是内存。
const MatchScanMode_boolOnly MatchScanMode_t = "boolOnly"

// MatchScanMode_spanFast 要区间, 快, 【不保证】leftmost-longest —— 它强制走路 A (游标启发式),
// 而路 A 给的是文件头说的第三种口径: 只在"起点随右端单调"时才等于 leftmost-longest。
// 🔴 不保证【不等于】"不是": 12 万处对账里 119972 处与 Longest 相同, 岔开的是 28 处。
//    每一处也都是真匹配 (①), 同一条内部照样不重叠升序 (②)。少的只有③那一条。
//
// 这一档是给【自动分档判错了】的那种情况留的出口: 库这边只按 min/max 分, 判成变长就一律
// 落到路 B (每处 1.6x, 走空档另加, 上限 2.00x)。可是"变长"不等于"有歧义" —— 两头带 \b 的
// 变长条实测岔开【0 处】。这种条落在 B 上是白掏钱。
//
// 🔴 挂之前先把凭据跑出来, 别凭感觉: 拿这条 pattern 自己 fuzz 一遍 (随机正文 × 两档对拍),
//    数出岔开 0 处再挂。跑出来了就挂 —— 挂上之后它既是快的那条路, 又确实是 leftmost-longest。
//    跑不出 0 就别挂, 默认档的保证是无条件的; 岔开那几处【是真匹配但边界偏了】, 拿去过
//    校验位 (身份证 · IBAN mod-97 · Luhn) 会失败, 整条真命中被调用方自己毙掉 = 无声漏报。
//    asc/engine/sd_body_gate_span_cross_test.go 是一份现成的写法。
const MatchScanMode_spanFast MatchScanMode_t = "spanFast"

// SetModes 声明每条 pattern 要什么 (下标即 pattern 下标, 长度不足的按零值 = 默认档)。
// 传 nil = 全默认档。调用方那边这是【静态】信息, 建集的时候就知道, 热路径上不该每遍改。
//
// 🔴 NewMatchScanner 报出来的 unsupported 那几条只允许配 boolOnly, 否则这里【当场报错】——
//    那张名单就是给这一步对的。理由见 NewMatchScanner。
func (m *MatchScanner) SetModes(modes []MatchScanMode_t) error {
	for i := 0; i < len(modes) && i < len(m.per); i++ {
		if modes[i] == MatchScanMode_boolOnly {
			continue
		}
		if !m.per[i].spanable {
			return errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
				" 能匹配空串, 只能配 MatchScanMode_boolOnly; pattern=" + m.set.pats[i])
		}
	}
	m.mode = modes
	// 档位定了才知道变长条走哪条路 —— 这里一次算完, Scan 里不再判。
	for i := range m.per {
		p := &m.per[i]
		p.pathB = p.spanable && !p.fixed && m.modeOf(i) != MatchScanMode_spanFast
	}
	return nil
}

// modeOf 取第 i 条的档位 (越界 = 默认档)。
func (m *MatchScanner) modeOf(i int) MatchScanMode_t {
	if i < 0 || i >= len(m.mode) {
		return MatchScanMode_span
	}
	return m.mode[i]
}

// Close 释放底层的 FindAllIndex 工作区。可重复调。
func (m *MatchScanner) Close() {
	if m.alloc != nil {
		m.alloc.Close()
		m.alloc = nil
	}
	m.text = ""
}

// Scan 扫 text 一遍 —— 这是【唯一】一遍全文。命中区间攒够一批 (matchScanBatch 处) 就调一次
// batchFn; 扫完把不足一批的余数也交出去。全程没有任何命中就一次都不调。
// 返回之后 HitIDs/Hit 可用。
//
// 🔴 交给 batchFn 的切片是内部缓冲本身, 下一批原地覆写 —— 要留就 append 走。
// 🔴 各条 pattern 的结果是【交错】着来的 (同一条内部按 Lo 升序), 不按 pattern 分组。
//
// 🔴 【要么全给, 要么整遍报错】—— 没有"这几条没给全, 你自己补"这种中间态。返回 err 的时候
//    这一遍不算数 (交出去的批次也不算), 调用方就整篇走老路 FindAll。三种 err:
//      ① 底下那一遍 FindAllIndex 自己失败 (native 侧 DFA 预算不够);
//      ② 某条 pattern 补端点要的那个【单条对象】编不出来 —— 配置错 (maxMem 太小), 与正文
//         无关, 把 maxMem 调大即可;
//      ③ 某条 pattern 的锚定解析失败 (DFA 放弃) —— 同样是 maxMem 的事。
//    另有一种"游程乱序", 那是【库内不变量崩了】= 本库的 bug, 也从这里以 err 交出来。
//    能匹配空串的那几条不在这里出现: NewMatchScanner 建的时候就报过名单了。
//
// batchFn 传 nil 合法: 只要命中表 (等价于 Set.Match), 一处区间都不收口。
func (m *MatchScanner) Scan(text string, batchFn func(ms []SetMatch)) error {
	if m.alloc == nil {
		return errClosedMatchScanner
	}
	for _, id := range m.hits {
		p := &m.per[id]
		p.cur, p.lastLo, p.done = 0, 0, false
		m.hit[id] = false
	}
	m.hits = m.hits[:0]
	m.scanErr = nil
	m.text = text
	m.fn = batchFn
	m.outN = 0
	err := m.set.FindAllIndex(text, m.alloc, func(runs []RegexpSet_FindAllIndex_Run_t) {
		for k := range runs {
			r := &runs[k]
			i := int(r.ReIndex)
			if i < 0 || i >= len(m.per) {
				continue
			}
			if !m.hit[i] {
				m.hit[i] = true
				m.hits = append(m.hits, r.ReIndex)
			}
			if batchFn == nil || !m.per[i].spanable || m.modeOf(i) == MatchScanMode_boolOnly {
				continue // 只当 bool 用的那几条: 到此为止, 一次端点都不补
			}
			m.feed(i, r.Lo, r.Hi)
		}
	})
	m.text = ""
	m.fn = nil
	if err != nil {
		m.outN = 0
		return err
	}
	if m.scanErr != nil {
		m.outN = 0 // 这一遍不算数, 余下那不足一批的也不交
		return m.scanErr
	}
	if m.outN > 0 && batchFn != nil {
		batchFn(m.out[:m.outN])
		m.outN = 0
	}
	return nil
}

// emit 把收口出来的一处区间写进那块固定缓冲, 满了就交出去。
func (m *MatchScanner) emit(i int, lo, hi int32) {
	m.out[m.outN] = SetMatch{Index: int32(i), Lo: lo, Hi: hi}
	m.outN++
	if m.outN == len(m.out) {
		m.fn(m.out)
		m.outN = 0
	}
}

// fail 记下本遍的错。收口跑在 FindAllIndex 的回调里, 那里 return 不出去, 只能记下来由
// Scan 收尾时交出去; 记完之后 feed 对所有 pattern 一概空转 (这一遍已经不算数了)。
// 🔴 只记第一个: 后面的错多半是第一个的连锁, 覆盖掉反而把真凶盖了。
func (m *MatchScanner) fail(err error) {
	if m.scanErr == nil {
		m.scanErr = err
	}
}

// failCompile: 补端点要的那个单条对象编不出来。与正文无关, 是 maxMem 配小了。
func (m *MatchScanner) failCompile(i int) {
	m.fail(errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
		" 补端点要的单条对象编不出来 (maxMem 配小了); 用 NewRegexpSetMaxMem 把 maxMem 调大" +
		"; pattern=" + m.set.pats[i]))
}

// failResolve: 锚定解析时 DFA 放弃了。同样是 maxMem 的事 —— 这一层不猜、不静默跳过。
func (m *MatchScanner) failResolve(i int, err error) {
	m.fail(errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
		" 锚定解析失败: " + err.Error() + "; pattern=" + m.set.pats[i]))
}

// failRunOrder: 同一条 pattern 的游程不是升序了。推进的前提没了 ⟹ 再往下走就是错答案。
// 🔴 这一条【不是】调用方能做错的事, 它是本库的不变量崩了 = bug。原样报出来别吞。
func (m *MatchScanner) failRunOrder(i int, lo, last int32) {
	m.fail(errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
		" 游程乱序 (lo=" + strconv.Itoa(int(lo)) + " < 上一条 " + strconv.Itoa(int(last)) +
		") —— 这是【本库的 bug】, 不是调用方的用法问题, 请连 pattern 与正文一起报" +
		"; pattern=" + m.set.pats[i]))
}

// feed 把一条游程 [lo,hi] (都是右端偏移) 喂给第 i 条的游标, 当场推进并收口。
func (m *MatchScanner) feed(i int, lo, hi int32) {
	p := &m.per[i]
	if m.scanErr != nil {
		return // 这一遍已经不算数了, 后面一律空转
	}
	// 分档 (定长 / 路 A / 路 B) 早在 NewMatchScanner + SetModes 里算完了, 这里只把那条路
	// 要的对象补建出来 —— 惰性: 没被真问到位置的 pattern 一个对象都不占。
	switch {
	case p.fixed:
		// 定长: 起点唯一 (e-minL), 一句减法, 不进正则引擎。什么对象都不用建。
	case p.pathB:
		// 路 B: 只要这一条自己那条【正向】正则 + 一个长度上界当窗口。
		// 🔴 反向对象一个都不建 —— 这正是 B 相对 A 省下的那一半。
		if p.fwd == nil {
			if p.fwd = m.set.forwardOne(i); p.fwd == nil {
				m.failCompile(i)
				return
			}
		}
	default:
		// 路 A: 起点靠这一条自己的反向对象锚定回推。给出来的口径是第三种 ——
		// 见文件头那一节, 选了 spanFast 的调用方自己举证。
		if p.rev == nil {
			if p.rev = m.set.reverseOne(i); p.rev == nil {
				m.failCompile(i)
				return
			}
		}
	}
	if lo < p.lastLo {
		// 游程乱序 —— 推进的前提没了, 再走下去就是错答案。这是不变量崩了, 见 failRunOrder。
		m.failRunOrder(i, lo, p.lastLo)
		return
	}
	p.lastLo = lo

	for e := lo; e <= hi; e++ {
		if e <= p.cur {
			continue // 落在已吐出去那一处里面
		}
		if p.fixed {
			// 定长: 起点唯一 (e-minL), 一句减法, 不进正则引擎。
			start := e - p.minL
			if start < p.cur {
				continue // 与已吐出去那一处相交
			}
			m.emit(i, start, e)
			p.cur = e
			continue
		}
		if p.pathB {
			if p.done {
				return // 从游标起整篇已无匹配, 后面的右端不必再看
			}
			// 窗口下界 from: 要找的是"起点 >= cur 的最左匹配"[s,E)。E 是一个右端且 E > s >= cur,
			// 而 e 是 > cur 的最小右端 ⟹ E >= e; 又 E - s <= maxL ⟹ s >= e - maxL。
			// 同时 s >= cur。故 s >= max(cur, e-maxL)。∎ 从这里起搜不会漏掉答案。
			from := p.cur
			if p.maxL >= 0 {
				if w := e - p.maxL; w > from {
					from = w
				}
			}
			loc := m.findCtx.FindStringIndexFrom(p.fwd, m.text, int(from))
			if loc == nil {
				// 从 from 起没有匹配 ⟹ 从任何 >= from 的位置起也没有 (后面的 from 只增不减)。
				p.done = true
				return
			}
			start, end := int32(loc[0]), int32(loc[1])
			// leftmost-first 与 leftmost-longest 选【同一个起点】, 只在终点上分歧 ——
			// 所以终点这一下要重新取最长 (锚定, 代价 = 这处命中有多长)。
			pos, ok, err := m.set.ResolveSpan(m.text, start, int32(i))
			if err != nil {
				m.failResolve(i, err)
				return
			}
			if ok && pos > end {
				end = pos
			}
			m.emit(i, start, end)
			p.cur = end
			continue
		}
		// 🔴 bound = cur: 回看【绝不越过游标】。是正确性不是省钱, 见文件头 ②。
		// 顺带它把回看代价钉成"离游标多远", 与正文长度无关。
		start, ok, err := p.rev.ResolveSpanWithin(m.text, e, p.cur, 0)
		if err != nil {
			// DFA 放弃 (预算不够) —— 不是"没有匹配"。这一层不猜、不静默跳过, 整遍报错。
			m.failResolve(i, err)
			return
		}
		if !ok {
			continue
		}
		// 从这个起点取【最长】右端 —— 变长 pattern 在同一起点上有一串长度都成立,
		// 取最短就是把命中截断, 下游拿去做定长校验会把真命中判成假。
		end := e
		pos, ok, err := m.set.ResolveSpan(m.text, start, int32(i))
		if err != nil {
			m.failResolve(i, err)
			return
		}
		if ok && pos > end {
			end = pos
		}
		m.emit(i, start, end)
		p.cur = end
	}
}

// HitIDs 返回上一次 Scan 命中过的 pattern 下标 (无序 · 不重复), 与 Set.Match 给的是同一张表。
// 切片下次 Scan 会被覆写。
func (m *MatchScanner) HitIDs() []int32 { return m.hits }

// Hit 报第 i 条上一次 Scan 有没有命中 (O(1) 查表)。
func (m *MatchScanner) Hit(i int) bool {
	return i >= 0 && i < len(m.hit) && m.hit[i]
}

// 🔴 本库【故意不提供】"把命中区间一次性物化成一个数组"的对外接口 (2026-08-27 删掉了
// AppendAllMatches)。有这么一个便利版在, 它就一定会从量具/对拍爬进生产路径 —— dst 是一块
// ∝ 命中数的 ratchet 缓冲, 正是分批接口要躲开的那个东西 (真表 0.037MB/MB, 200MB 的 body
// 就是每个并发扫描 7.4MB 常驻), 而一行"生产路径别用"的注释拦不住任何人。
// 要一次性数组的调用方自己在 Scan 的回调里 append 一行就有了 —— 那一行写在调用方自己家里,
// 谁写谁看得见代价。Scan 报 err 的那一遍收到的东西一概作数不得 —— 整篇重来 (见 Scan 的说明)。

// errClosedMatchScanner 单独提出来, 免得每次构造一遍 error。
var errClosedMatchScanner = errors.New("re2native: match scanner closed")
