// matchscan.go —— 一遍扫正文, 边扫边【一批一批】交出各条 pattern 的不重复命中区间。
//
// 🔴 一句话: 【口径无条件是 leftmost-longest】, 没有旋钮, 没有"快而不准"的档。
//    这句保证怎么兑现的 · 对拍要拿哪个当 oracle:
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
//	SetModes(modes)            【每条要什么】两态: 只要 bool / 要区间 (默认)。全文见下一节。
//	Scan(text, batchFn)        一遍全文。命中表 (HitIDs / Hit, 与 Set.Match 同解) 边扫边填,
//	                           命中区间【边扫边收口、攒够一批就交出去】。
//	                           只返回 err: 要么全给, 要么整遍不算数 —— 见下面那一节。
//	Stats()                    上一遍的账 (回看了几次 · 收到几个候选 · 验了几次 · 吐了几处)。
//	                           变长条的钱全在"验了几个假候选"上, 没这几个数就看不见它。
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
//	Scan 的 err                      这一遍不算数, 整篇走老路 FindAll。来由都是 maxMem 配小了
//	                                 (底下那遍 FindAllIndex 失败 / 补端点要的单条对象编不出来 /
//	                                 反向回推那一趟 DFA 放弃), 另有一种"游程乱序"是本库的 bug。
//
// 🔴 为什么不做成"部分成功": 一个调用方【造不出来】的错误码, 不该出现在返回值里。它逼出
//    的是这么一条链 —— 调用方必须写兜底 → 兜底跑不到 → 跑不到就没法测 → 没法测的代码基本
//    是错的 → 真出事那天走的是一段从没执行过的路。那比"根本没有兜底、直接整遍失败"更危险。
//    量过: 锚定解析用的是【小 DFA】(起点唯一), 不是扫全文那个; 三种形状的 pattern 在"刚好
//    编得出来"的那道墙上面 3000 字节的带子里逐档细扫 (60KB 正文 · 每 3 字节一个起点),
//    放弃 0 次 —— 墙底下则是 NewRegexpSetMaxMem 当场干净报错。所以这条分支本来就该是
//    "整遍失败"这种粗粒度的交代, 而不是一套调用方永远验不了的补偿逻辑。
//
// 🔴 "补端点要的单条对象编不出来"这一条同理【不兜底】: 2026-08-28 逐字节量过 —— 590 条
//    生产 pattern 里, "反向单条 set 比正向单条贵"的只有 16 条 (全在同一张凭据表,
//    全是 {n,} 开放尾巴), 最大倍率 1.021×。要让"正向 set 编得出来而反向单条编不
//    出来"真的发生, set 里得只装【一条】pattern 且 maxMem 恰好卡进那条 pattern 阈值往上
//    2% 的带子里 (实测那条 JWT 三段式: 正向 3580 字节 / 反向 3654 字节, 窗口 74 字节)。
//    多条的表上正向 set 本身就贵出几个量级, 这个窗口从结构上不存在。能走到这里只说明
//    调用方 maxMem 配错了 —— 那就该报错让他调大, 静默换一条实现只会把配置错误藏起来。
//
// ── 两态旋钮 (SetModes): 每条 pattern 要什么 ─────────────────────────────────
//
//	MatchScanMode_boolOnly           只要"命中没命中"。一处区间都不收口、一次端点都不补。
//	                                 门上很多位只当 bool 用 (某某类内容在不在), 从来没人问它
//	                                 在哪 —— 那几条不该为它花补端点的钱。
//	MatchScanMode_span               要区间。【零值 = 默认档】, 无条件 leftmost-longest。
//
// 🔴 2026-08-28 之前这里是【三态】: 多一个 MatchScanMode_spanFast, 强制走那条便宜但
//    【不保证】leftmost-longest 的"路 A"(游标启发式), 挂之前要调用方自己拿 fuzz 跑一份
//    "这一条走 A 也不岔"的凭据。整档删掉了 —— 换上来的这条路 (下面那节) 既是严格口径,
//    价钱又比 A 还便宜, 那一档就没有存在理由了, 留着只会让人以为还有便宜可占。
//
// ── 端点怎么补: 一遍 set 扫完, 之后【全走单条对象】────────────────────────────
//
// 🔴 一句话的规矩 (2026-08-27 定的): 【扫正文那一遍走 set, 之后补端点的每一趟都走这一条
//    pattern 自己的单条对象, 一趟都不再回 set】。三条理由:
//    ① 单条对象走的是 RE2::Match 那条完整的路 (DFA → OnePass/BitState/NFA 逐级回退),
//       DFA 放弃了还有下家; set 那侧的锚定解析是 kManyMatch 的 DFA 独一条, 没有下家 ——
//       "DFA 放弃"在那边只能整遍失败 (RE2::Set::Match 里 dfa_failed 就直接 return false,
//       上游也是这么写的: re2_set.cc:216)。
//    ② 补端点的流量不再冲刷整表那份大 DFA 缓存 (真表 155 条那份), 两边互不干扰。
//    ③ 状态更小: 单条不必背 kManyMatch 每个状态那张 id 表。
//
//	定长 (min == max)  Lo = Hi - min。【不进正则引擎】, 一句减法, 一趟都不用。起点唯一,
//	                   所以它跟下面那条变长路必然同解。
//
//	变长 (min < max)   【两步】:
//	                   ① 反向 · 种全部状态 · 从右端 e 往左走到死 —— 把起点落在 [cur, e) 里的
//	                      【全部可行前缀起点】一次收齐 (spanviable.go 的 ViableStarts,
//	                      走的是 RegexpSet.viableOne 那条"反向 · 只装这一条的 set")。
//	                   ② 候选【从小到大】逐个拿正向单条 longest 锚定去验 (forwardOne →
//	                      Regexp.FindStringIndexAtWithin), 第一个验过的就是答案。
//	                   为什么这就是 leftmost-longest, 见下一节那两条证明。
//
//	要的那个单条对象编译不出来 = maxMem 配小了, Scan 整遍报错 (见上面那一节)。
//
// 🔴 反向必须是【一条一个】。整表建一个反向 set 是死路: set 里状态数是相乘的
//    (doc/状态数为什么会相乘.txt), 155 条的反向表在 6.4MB 正文上实测 65 秒 / arena 顶满 254MB
//    还在 flush, 而正向同一张表 18ms 零 flush。一条一个就没有这个乘法; 而且这些反向对象
//    【从不用来扫正文】, 只做锚定回推 —— 起点只有一个, 那套 `.*?` 前缀引起的状态爆炸机制
//    从根上就不存在。再加上惰性: 只有真被问到的那几条才会被建出来。
//
// ── 为什么这条路是对的 (两条, 都要) ─────────────────────────────────────────
//
// ① 候选集不漏。设真答案是 [s, E) 且 s ∈ [cur, e)。E 是一个匹配右端且 E > s >= cur,
//    而 e 是【> cur 的最小右端】⟹ E >= e ⟹ text[s, e) 是 text[s, E) 的前缀 ⟹ 它是
//    一个可行前缀 ⟹ s 一定在 ViableStarts 给的候选里。∎
//    升序试 ⟹ 第一个通过的必然是 leftmost; 而 fwd 是 longest 口径编的, 锚定在 s 上给的
//    就是最长右端 ⟹ 严格 leftmost-longest。
//
// ② 一个都没验过 ⟹ [cur, e) 里根本没有起点 ⟹ 游标可以直接推到 e (对①取逆否)。
//    所以"全军覆没"这一支不是放弃, 是【证明了这一段是空的】。
//
// 顺带一条: 验过的那个候选 s 给出的右端 E 必然 >= e (E 是右端且 E > cur ⟹ E >= e),
// 所以游标每次都真的越过 e —— 各轮的回看窗口 [cur, e] 两两不交且递增, 反向那一趟的
// 累加封顶 = 多扫一遍正文。这正是它比老路便宜的地方: 老的"路 A"每处命中固定两趟且窗口
// 【相交】, 老的默认档"路 B"没有上界的条目要走完空隙。
//
// ── 对外那条保证, 逐字读 ─────────────────────────────────────────────────────
//
// 交出来的区间:
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
// ── 重复: 每条 pattern 一个游标, 一次左到右推进 ──────────────────────────────
// 变长 pattern 在一片正文上会在每个可收的位置各报一个右端 (\p{Han}{2,4} 撞 "张三李四王五"
// 报 6/9/12/15/18 五个), 它们说的其实是同一片区域。推进的规矩:
//   ① 右端落在已吐出去那一处里面 (Hi <= cur) → 跳过;
//   ② 否则求【起点不早于 cur 的最靠左那个】起点 —— 回看窗口掐在 cur 上, 绝不越过游标;
//   ③ 从这个起点取最长右端吐出去, cur 推到它。
// 🔴 ② 的窗口掐在 cur 上是【正确性】不是省钱: 不掐的话上例 Hi=18 会回推到 6, 与刚吐出去的
//    [0,12) 相交而被丢掉, "王五"就无声无息漏了。掐上之后回推到 12, 吐 [12,18)。
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
// ── 老账: 这条路是怎么换上来的 (2026-08-28) ─────────────────────────────────
//
// 在这之前补端点有三条路并存, 默认档 B + 一个旋钮 A, 外加一个用来比价的独立类型
// MatchScanner2 (路 D2)。现在只剩 D2 这一条, A/B 与 MatchScanner2 这个类型一起删了。
//
//	路 A (旧 spanFast)  反向【只种 accept】回推一个起点 + 正向锚定取最长右端。两趟, 窗口
//	                    【相交】。给的是"第三种口径"—— 既不是 leftmost-first 也不是
//	                    leftmost-longest: \b(?:ab cd ef|cd)\b 撞 "ab cd ef" 时门给的最小右端
//	                    是 "cd" 那处, 只种 accept 就只回推得到 "cd" 的左端, 真正的最左起点 0
//	                    根本不在候选里。这是它的病根, 也正是本路"种全部状态"要解掉的那件事。
//	路 B (旧默认档)     从 max(cur, e-maxL) 起做一次正向【非锚定】longest 搜索。一趟, 口径严,
//	                    贵在【没有上界的条目要走完空隙】(maxL < 0 时下界塌回游标)。
//
// 换掉的凭据 (2026-08-28, 全部在 100MB 量级的真语料 × 9 张生产真门表上跑; 11 份语料 =
// console 前端产物 + 凭据二次方八腿 + 产品源码/说明书/端点ELF 混合 + 本机 claude 真历史):
//	口径   11 份语料 × 9 张门 = 99 格, 逐区间按 (条, Lo, Hi) 排序对拍。
//	       与路 B 【一处不差】—— 合计对账 1.619 亿处区间, 差 0 处。这是"敢把默认档换掉"
//	       的全部凭据。
//	       与路 A 差 【37 处】, 全在那份产品源码/说明书/ELF 的混合语料上, 而且每一处都是
//	       路 A 把左端截短了 —— 例如阿联酋身份证 A 给 "1985-1234567-1" 而真答案是
//	       "784-1985-1234567-1", 提示注入标记 A 给 "<SYS>" 而真答案是 "<<SYS>>"。
//	       🔴 这正是文件里一直警告的那种伤: 区间偏了, 拿去过校验位 (mod-10 / Luhn /
//	          mod-97) 会失败, 整条真命中被下游自己毙掉 = 无声漏报。所以这次换路不只是
//	          省钱, 是【真的在真语料上修掉了 37 处错边界】。
//	价钱   11 条门链合计, 本路【每一条都是最快的】。相对路 A 0.48~0.91×, 相对路 B 视语料
//	       0.6~1.0×。"试/看"(每次回看正向锚定验了几次) 在 99 格里【全是 1.00】——
//	       升序第一个候选就是答案, 人造反例 a|[ab]+c 那种 2× 退化在真表上不发生。
//	内存   本路的常驻是"反向单条 set"(vp1), 路 A 是"反向单条"(rev1), 同一个量级:
//	       最大的那张 158 条表上 89 条被真问到位置, vp1 9.6MB vs rev1 7.6MB (1.26×)。
//	       相对路 B 是【净增】—— B 只要正向单条, 一条反向都不建。这一笔是这次换路的
//	       全部代价, 量它用 (*RegexpSet).ViableOneStats()。
//
// 更早那一笔 (2026-08-27): 两条老路的第二趟原先都回整表 set.ResolveSpan, 换成单条对象之后
// 答案一字不差而价钱降了 27~36% (TestMatchScanPathsSameAsSetRoute)。那次换的是"回不回 set",
// 这次换的是"起点怎么找回来", 是两件事。
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
	// findCtx 是"锚定在候选起点上取最长右端"那一步的复用 scratch (稳态零分配)。
	findCtx *FindStringIndex_ctx_t
	// cands 是候选起点缓冲 (native 直接往里写, 降序)。跨 pattern 复用, 只在一个右端的
	// 处理期间有意义。不够就翻倍, 翻上去就留着。
	cands []int32
	st    MatchScanStats_t
}

// matchScanCandBuf 是候选起点缓冲的起始长度。不够会翻倍重来一趟 —— 真表上一个右端的候选
// 通常是个位数, 这个数只是为了让"翻倍"基本不发生。
const matchScanCandBuf = 64

// MatchScanStats_t 是一遍 Scan 的账。加它是因为变长条的钱全在"验了几个假候选"上,
// 而那一笔从外面一个字都看不见 —— 没有这几个数就没法判断某张表的形状适不适合这条路。
//
// 🔴 前三个【只统计变长条】: 定长条走 e-minL 那句减法, 一次回看都不做, 不进这三个分母。
//    Emits 不一样, 它数的是【全部】吐出去的区间 (定长的也算) —— 所以要看"平均验了几次"
//    得用 Tries/Walks, 拿 Tries/Emits 会被定长条稀释成假象。
type MatchScanStats_t struct {
	Walks int64 // 反向走了几趟 (= 处理了几个"没被游标盖住的"右端)
	Cands int64 // 这些趟一共给出多少候选起点
	Tries int64 // 一共拿正向锚定验了几次 (<= Cands; 命中即停)
	Emits int64 // 交出去几处区间 (含定长条)
}

// msPat_t 是每条 pattern 的推进状态。cur 就是那把游标。
//
// 🔴 分档所需的那两件事 (spanable · fixed) 全是【与正文无关】的, 所以一律在
//    NewMatchScanner 里算完, Scan 里一件都不算 —— 这正是"没有 unresolved"的前提:
//    一条 pattern 能不能走这条路, 建工作区那一刻就有答案。
//
// 🔴 这里【没有 maxL】。老的路 B 靠它把回看窗口的下界抬起来 (没上界就塌回游标, 白扫一段),
//    而这条路的下界是【反向那一趟自己走到死的地方】—— 动态的, 不需要 maxL 兜底。
//    这正是它比路 B 便宜的全部原因。
type msPat_t struct {
	spanable bool // 能不能收口成区间 (false = 能匹配空串, 只能当 bool 用)
	fixed    bool // 定长: 起点唯一, 一句减法, 不进正则引擎
	minL     int32
	vp       *RegexpSetReverse // 反向 · 只装这一条的 set: 种全部状态回推【全部候选起点】, 惰性建
	fwd      *Regexp           // 正向 · longest 单条: 锚定在候选上验, 顺手给最长右端, 惰性建
	cur      int32             // 已吐出去的最右字节
	lastLo   int32             // 上一条游程的左端, 用来确认升序
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
		cands:   make([]int32, matchScanCandBuf),
	}
	for i := 0; i < s.size; i++ {
		p := &m.per[i]
		minL, maxL := s.PatternLenRange(i)
		if minL <= 0 {
			unsupported = append(unsupported, int32(i))
			continue // spanable 留 false: 之后一律当 bool 用
		}
		p.spanable = true
		p.minL = int32(minL)
		p.fixed = minL == maxL && maxL >= 0
	}
	return m, unsupported, nil
}

// MatchScanMode_t 是【每条 pattern 要什么】。两态, 零值 = 默认档 (要区间)。
// 全文见文件头"两态旋钮"那一节。
type MatchScanMode_t string

// MatchScanMode_span 要区间, 无条件 leftmost-longest (定长走减法, 变长走"回推候选 + 升序验",
// 两者必然同解)。零值就是它 —— mask 里没显式写的那几条都是这一档。
const MatchScanMode_span MatchScanMode_t = ""

// MatchScanMode_boolOnly 只要"命中没命中", 一处区间都不收口、一次端点都不补。
// 这几条照样进命中表 (Hit/HitIDs), 只是一处区间都不给。
//
// 🔴 这是最值钱的一档: 真表上 57% 的游程来自两条只当 bool 用的宽 pattern, 这一挡挡掉的是
//    它们的端点补全 (真花钱的那步), 不只是内存。
const MatchScanMode_boolOnly MatchScanMode_t = "boolOnly"

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
	return nil
}

// modeOf 取第 i 条的档位 (越界 = 默认档)。
func (m *MatchScanner) modeOf(i int) MatchScanMode_t {
	if i < 0 || i >= len(m.mode) {
		return MatchScanMode_span
	}
	return m.mode[i]
}

// Stats 返回上一次 Scan 的账 (见 MatchScanStats_t)。
func (m *MatchScanner) Stats() MatchScanStats_t { return m.st }

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
		p.cur, p.lastLo = 0, 0
		m.hit[id] = false
	}
	m.hits = m.hits[:0]
	m.scanErr = nil
	m.text = text
	m.fn = batchFn
	m.outN = 0
	m.st = MatchScanStats_t{}
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
	m.st.Emits++
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

// failCompile: 补端点要的那个单条对象编不出来 (what 说的是哪一个)。与正文无关, 是 maxMem
// 配小了。🔴 这里【不兜底、不换一条实现】, 理由见文件头那段 74 字节窗口的账。
func (m *MatchScanner) failCompile(i int, what string) {
	m.fail(errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
		" 补端点要的" + what + "编不出来 (maxMem 配小了); 用 NewRegexpSetMaxMem 把 maxMem 调大" +
		"; pattern=" + m.set.pats[i]))
}

// failResolve: 反向回推那一趟 DFA 放弃了。同样是 maxMem 的事 —— 这一层不猜、不静默跳过。
// 🔴 只有这一处会走到: 正向锚定那一趟走的是单条 RE2::Match, 那条路有 NFA 兜底。
func (m *MatchScanner) failResolve(i int, err error) {
	m.fail(errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
		" 可行前缀回推失败: " + err.Error() + "; pattern=" + m.set.pats[i]))
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
	// 分档 (定长 / 变长) 早在 NewMatchScanner 里算完了, 这里只把变长那条路要的两个对象
	// 补建出来 —— 惰性: 没被真问到位置的 pattern 一个对象都不占。
	// 🔴 建出来的一律是【这一条 pattern 自己的单条对象】, 不是 set。扫正文那一遍之后,
	//    补端点的每一趟都不再回 set —— 理由见 regexpset.go 里 fwd1/vp1 那段。
	if !p.fixed {
		if p.fwd == nil {
			if p.fwd = m.set.forwardOne(i); p.fwd == nil {
				m.failCompile(i, "【正向单条】")
				return
			}
		}
		if p.vp == nil {
			if p.vp = m.set.viableOne(i); p.vp == nil {
				m.failCompile(i, "【反向单条 set】")
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
		// ① 反向 · 种全部状态 · 回看不越过游标: 把 [cur, e) 里全部候选起点一次收齐。
		//    🔴 bound = cur 是【正确性】不是省钱: 越过游标推出来的起点会与刚吐出去的
		//       那一处相交, 那一处就得整个丢掉 = 无声漏报。
		n, err := p.vp.ViableStarts(m.text, e, p.cur, 0, m.cands)
		if err != nil {
			m.failResolve(i, err)
			return
		}
		if n > len(m.cands) {
			// 缓冲不够 —— 里面写下的是最大的那几个 (恰好最没用的), 整批作废, 换大的重来。
			m.cands = make([]int32, n*2) // 翻倍留余量: 下一个右端多半也是这个量级
			n, err = p.vp.ViableStarts(m.text, e, p.cur, 0, m.cands)
			if err != nil {
				m.failResolve(i, err)
				return
			}
			if n > len(m.cands) {
				m.fail(errors.New("re2native: match scanner pattern " + strconv.Itoa(i) +
					" 候选缓冲扩容后仍然不够 —— 这是【本库的 bug】; pattern=" + m.set.pats[i]))
				return
			}
		}
		m.st.Walks++
		m.st.Cands += int64(n)
		// ② 候选【从小到大】逐个验。缓冲是降序的, 所以倒着走 —— 第一个验过的就是 leftmost,
		//    而 p.fwd 是 longest 口径编的, 它给的右端就是最长的那个 ⟹ 严格 leftmost-longest。
		//    验不过说明这条"可行前缀"只是张空头支票 (能被某个后缀补成匹配, 但正文里跟着的
		//    不是那个后缀), 试下一个。
		got := false
		for k := n - 1; k >= 0; k-- {
			st := m.cands[k]
			m.st.Tries++
			loc := m.findCtx.FindStringIndexAtWithin(p.fwd, m.text, int(st), len(m.text))
			if loc == nil {
				continue
			}
			end := int32(loc[1])
			m.emit(i, st, end)
			p.cur = end
			got = true
			break
		}
		if !got {
			// 一个都没验过 ⟹ [cur, e) 里根本没有起点 (证明见文件头 ②), 游标直接推到 e。
			p.cur = e
		}
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
