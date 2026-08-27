本包文档见同目录 README.md (Markdown 正文)。
调性能 (单条 Regexp 与 RegexpSet 都算) 看 doc/set性能优化经验.txt —— README 里那几节的长版:
三个旋钮按收益排是 方向 (反着扫) > 表的形状 > 内存预算, 以及量什么、怎么标定、哪些路已经排除。
本库 vs Go 标准库 regexp 怎么选 看 doc/与标准库regexp怎么选.md —— 哪种形状谁更快 (含实测表)、
本库缺哪些 stdlib API、一页判据。数据出自 stdlib_compare_test.go, 换机器重跑一遍就知道还成不成立。
一遍扫一张表要"命中在哪" (MatchScanner) 看 doc/MatchScanner的leftmost-longest保证.md ——
默认档凭什么敢说 leftmost-longest、对拍要拿哪个当 oracle、怎么用 fuzz 把更多条拉进 spanFast 快档。

要点速记 (详见 README.md):
* 【默认用本库】。标准库 regexp 的匹配是从零重写的 NFA 模拟, 只有提得出【字面量前缀】时才走
  memchr 快路; 提不出来 (pattern 以 (?i) / \b / 字符类 / 交替开头) 就逐位置起头 —— 同一条正则
  同一份正文实测差 11~85 倍, 最差的一格本库也没输 (1.1 倍)。而且匹配全在 C 侧, 稳态 0 B/op,
  不抬高 GC 堆目标。只有四种情况该留标准库, 都是一眼能认出来的:
    ① 运行期【现编】且不缓存的正则 (编译价进热路径: 实测 9.2us vs 21.6us 本库输) ——
       能提到包级变量就提, 是整张关键词表就编成一个 RegexpSet, 那又回到本库。
    ② 调用停在【过桥地板价】上 (一次 cgo 过桥 ~67ns, stdlib ~2ns): 输入只有几字节且正则简单到
       onepass 一遍就完。🔴 "输入短" 本身【不是】判据 —— 161B 的串上 6 条要回溯的正则,
       本库快 24 倍且零分配。决定胜负的是形状不是长度。
    ③ pattern【能匹配空串】且要 FindAll 整篇 (如 (?m)^[ \t]*$): 每行都成立 -> FindAll 被迫
       产出与行数同量级的空匹配, 本库这条路比标准库重, 115KiB 行式正文上是 0.23 倍。
       🔴 这是【pattern 形状】问题不是引擎问题: 同一个意图改成不可空匹配 (* -> +) 立刻翻成
       本库快 8.45 倍, 而且在标准库上也更快 —— 先试着改 pattern, 改不动再留标准库。
       数字见 empty_width_bench_test.go 与 doc/与标准库regexp怎么选.md 的 §2.5。
    ④ 编译的是【别人写的】正则 (用户/配置提供) 且语法面必须和 stdlib 逐字一致 —— 语义契约,
       与性能无关 (\C / 嵌套深度上限 / 个别 escape 两边不通)。
* 自带 cgo 的原生 RE2 正则库, 不用 go-re2 / 不用 abseil / 不用 cmake, 编译期不下载远程源
  (RE2 2023-03-01 已 vendored, 纯 C++11, zig 可交叉编译)。cgo 必须开启。
  另摘了上游 2023-03-01 之后的几条真修复 (交替因式分解丢大小写的静默漏报 / (?<name>) / 空宽计数重复
  不再展开 ...), 逐条判据与"没摘哪些、为什么"见 VENDOR.txt 的"从上游摘回来的修复"一节。
  另从上游【尚未合并的 PR】摘了 3 条 (最值钱的一条: 反向扫描的"太慢就退回 NFA"启发式因为
  p - resetp 是负数而从来没生效过 —— 修完某类 pattern 扫 1MB 从 234ms 掉到 34ms), 判据同上,
  对拍见 upstream_backport_test.go。
* 并发: 一个 *Regexp 给多个 goroutine 共用是安全的, 但不是线性扩展 —— 每次 DFA 搜索都要拿一次
  cache_mutex_ 读锁 (pthread_rwlock), 读者计数在同一条 cache line 上。16 goroutine 扫 14 字节:
  共用 43ns/op, 每 goroutine 各一个 9.5ns/op。【照常共用一个包级变量就行】: 差值才 33ns, 而各建
  一个要多付一次编译 + 每份一套独立 DFA 状态缓存 (内存峰值和 max_mem 预算都乘以 worker 数),
  不划算。真被 profile 指到这把锁再说。根治要改读者同步方式 (分片读者计数 / epoch), 现状与实测
  见 README.md 的 "Concurrency" 一节与 contention_bench_test.go。
* API 方法名/签名对齐 stdlib regexp 的 string 系与 []byte 系方法 (便于互读), 但【不是】*regexp.Regexp 的 drop-in;
  匹配为 leftmost-first (同 regexp.Compile)。支持方法清单见 README.md 的 Supported API。
  有意差异: ReplaceAllString 的 repl 是【字面】(不展开 $1/${name}/$$), 需捕获组替换用 ReplaceAllStringFunc。
* []byte 系 (Match/Find/FindIndex/FindSubmatch/FindAll…/ReplaceAll/ReplaceAllFunc, 见 bytes.go):
  与 string 系【共用同一套匹配内核】, 不是另一份实现 —— C 侧只吃「字节指针+长度」, 故传 []byte
  【任何长度都不产生 string(b) 拷贝】, 返回值是入参的子切片 (共享底层数组, 同 stdlib bytes 系)。
  正文本来就是 []byte 的调用方 (HTTP body / 文件 / 解码缓冲) 直接用这套, 别先转 string。
  两条约束: ①调用期间不得改写传入的 b (同 stdlib); ②Replace 系在【逐字节无改动】时直接返回原 src
  切片 (零分配, 本库惰性物化风格; stdlib 是返回新副本), 故【不得改写返回值】, 要可写副本自行 copy。
  命名: stdlib 有的照 stdlib (FindIndex ↔ FindStringIndex); 本库自有的加 Bytes 后缀
  (FindReplaceWithinBytes / RegexpSet.MatchBytes / MatchAnyBytes / FindStringIndex_ctx_t.FindIndex)。
* 非 stdlib 的额外方法:
    - FindReplaceWithin(strip, src, repl): 等价 find.ReplaceAllStringFunc(src, m=>strip.ReplaceAllString(m,repl)),
      但整循环 + 段内替换下沉到一次 cgo; 无改动路径零分配直接复用原串。详见 README.md#findreplacewithin。
    - AppendFindReplaceWithin(dst, strip, src, repl): 上面那个的【追加进调用方缓冲】孪生 ——
      同一个 C 内核、同一份 changed 判据, 差别只在结果落在哪: FindReplaceWithin 每趟现开一个
      Go string (整份 C.GoStringN 拷一次), 这个 memcpy 进你传的 dst ⇒ 复用同一块底就是稳态零
      Go 堆分配。产物"当场扫一遍就丢"的解码腿 (先归一化再匹配的那类变体) 用这个。
      changed=false 时 dst 一个字节都没动, 该用原 src; 返回的是 dst 上的视图, 一律用返回值。
      本条没有 ctx —— 外层循环与段内替换都在 C++ 里, Go 侧唯一按正文线性的分配就是结果本身。
    - AppendAllStringIndexFlat(dst, s, n): 同 FindAllStringIndex, 但把 [start,end) 直接追加进
      调用方的 []int (形如 [s0,e0,s1,e1,…]), 不产 [][]int 外壳也不产一次性 flat 表。
      传 buf[:0] 复用缓冲 ⇒ 稳态零分配。匹配集合/顺序/空匹配语义与 FindAllStringIndex 逐处相同
      (同一段 C 循环), 只回填 group0; 要子组用 FindAllStringSubmatchIndex。
      详见 README.md#appendallstringindexflat。
    - StepAllStringSubmatchIndex(s, n, batchFn) / StepAllStringIndex(s, n, batchFn) (match_step.go):
      全匹配的【sqlite3_step 式】形态 —— C 侧一次填一批命中进一块固定批缓冲交给 batchFn, Go 侧
      取走这批再 step 下一批, 内存里【从来没有全部命中信息】。零 Go 堆分配 (缓冲是库内 sync.Pool
      借的, 用完就还), 而且【无命中的调用也一分钱不花】。
        per := 2*(re.NumSubexp()+1)                    // Index 版恒为 2
        re.StepAllStringSubmatchIndex(body, -1, func(flat []int32) bool {
            for k := 0; k+per <= len(flat); k += per {
                loc := flat[k:k+per]                   // 布局与 FindAllStringSubmatchIndex 的单行逐字相同
                _ = loc                                //   未参与的组是 -1,-1
            }
            return true                                // 返 false = 提前停, 剩下的正文不再扫
        })
      🔴 flat 只在【本次回调内】有效: 下一批就地覆写同一块内存, 而且本次调用一返回这块就还回池子
         了。要留存自己 copy (存 int 下标而不是切片)。
      🔴 用它还是用 FindAll*, 看的是【要不要那张表】, 不是哪个新:
         · 拿到命中顺序过一遍就丢 (累加/改写/当场判断) ⇒ Step。
         · 要一张能来回走、能 append/过滤/交叉引用、要 len() 当门的表 ⇒ FindAll*。拿 Step 去物化
           实测是净亏 (Go append 阶梯累计收敛到 5N: 2 万处命中 1.45MB/4 笔 → 5.70MB/26 笔, CPU +17%)。
      匹配集合/顺序/空匹配去重推进与 FindAll* 逐处相同 (同一段 C 循环), 对拍门见 match_step_test.go。
    - ReplaceAllStringFunc_ctx_t (NewReplaceAllStringFunc_ctx / AppendReplaceAllStringFunc):
      ReplaceAllStringFunc 的【复用已有分配】变体 —— 结果追加进调用方自己的 []byte 底,
      匹配位置表挂 ctx 上, 两块都跨调用复用 ⇒ 稳态零分配 (逐段反复替换的热路径用这个)。
      返回 (dst, changed) —— 🔴 是 changed 不是 matched: 定义就是
      `re.ReplaceAllStringFunc(src,f) != src`。两种情况都报 false 且【dst 一个字节都不多】:
      ①压根没匹配; ②有匹配但每处 f 都把原文原样写回 (HTML 实体码点越界 / hex 奇数长或解出不可打印
      这类 f 自带合法性判断的情形) ⇒ 收尾把 dst 截回调用前的长度。拿 matched 当 changed 用会凭空多出
      一份与原文相同的产物 (多存一块底·多扫一遍·多一次去重)。回滚只退 len 不退 cap, 一律用返回值。
      ctx 非线程安全, 并发各持一个; 零值即可用。
      🔴 顺带修了 ReplaceAllStringFunc 本身: 原来是裸 strings.Builder 从 0 开始长,
      Go 大切片 1.25 倍增长 ⇒ 累计分配收敛到 5×len(src) (还白拷 4 份)。实测 64MB 正文的
      hex 解码腿上 Builder 累计 329MB = 每输入字节 4.9 字节。现在按 len(src) 一次开够
      (stdlib replaceAll 和本库 []byte 门面 ReplaceAllFunc 本来就是这么开的), 实测 1.02×。
      详见 README.md#appendreplaceallstringfunc。
    - MatchStats() / MemInfo(): 【按次调用】与【按 Set】的 DFA 计数 (清空次数/新建状态数/
      正文字节数/额度水位), 没有全局状态, 并发下各算各的。调预算就看 MemInfo().FlushesTotal
      是不是 0; 判"这张表贵不贵"看 StatesBuiltTotal 和 ScanStats 的 Bytes/StatesBuilt。
      不传 st 时 C 侧完全不统计, 热路径继续用 Match 即可。详见 set性能优化经验.txt。
    - Attrib(): 回答"这几万个状态是哪几条 pattern 造的 / 单个状态多宽 / 正文哪一段造的"。
      要 CGO_CXXFLAGS="-O2 -DRE2_DFA_ATTRIB=1" 编译才有数据, 默认构建里这套代码根本不存在。
      排序看 Excess 不要看 States (非锚定搜索下 States 会饱和成 100%)。
      榜单有消融验证 (按榜摘 20 条降 5.4×, 随机摘 20 条只降 1.4×) 且跨语料稳定。
    - DFAStats() / DFAStatsZero(): 进程级计数 —— DFA 状态缓存被【整表清空重建】了几次
      (RE2 的 DFA 缓存满了不是 LRU 淘汰, 是 ResetCache 全清)。结果仍然正确, 所以这件事本来
      在调用方眼里没有任何信号, 但它是【悬崖不是斜坡】: 实测 60 条 gap 型 pattern × 16 份互不
      相同的 64KB body, 1MB 预算 79 次 flush → 8.1MB/s, 8MB 预算 3 次 flush → 18.6MB/s,
      64MB 预算 0 次 flush → 164.7MB/s (一兆语料上 3 次 flush 就值 9×)。
      Resets 就是"maxMem 到底够不够"的直接读数: 单线程热身后跑一批【互不相同】的真 body,
      Resets 增量 =0 才叫够, 编得过不算。详见 README.md#dfa-cache-thrashing。
      🔴 单形状 benchmark(同一份 body 扫 N 遍)量不到这个 —— 缓存一热就再不新建状态, 换什么
      预算吞吐都一样。要量必须轮换多份 body, 且看 Resets 而不是只看吞吐。
      🔴 而且 flush 只是【诱因】: 踩到 flush 的那次调用只慢 1.3~2.8 倍, 真正的两个数量级在
      【建状态】—— 同一档 0 flush 下, 全新正文 23.7ms/份 vs 重扫同一批 0.36ms/份 (66 倍)。
      所以把预算调到 0 flush 是"别掉下悬崖"(必做, 且几乎不花物理内存), 不是"快 100 倍";
      每个请求都是新正文的场景, 那 23.7ms 就是上限, 要再快只能让【表】少造状态或者少扫正文。
    - RegexpSet.MatchAny / MatchAnyBytes: 只问"有没有命中"的快路径 —— 不回填 index (不传 buf),
      底下 RE2 打开 want_earliest_match, 扫到第一个命中位置立刻收工, 不把正文扫完 (不命中仍全扫)。
      实测 8MB 正文 + 命中落在第 0 字节: MatchAny 2.12µs vs len(Match(...))>0 11.18ms。
      与 Match 共用同一份 DFA 缓存, 不多占内存。要"哪几条"仍然用 Match —— 早退与完整命中集不可兼得。
    - CompileReverse / CompileReverseMaxMem / MustCompileReverse (类型 RegexpReverse, 方法
      MatchString / Match / MatchStats), 整表 NewRegexpSetReverseMaxMem (类型 RegexpSetReverse):
      【反着扫】—— 答案与 MatchString / 正向 Set 逐位相同, 但 DFA 从正文末尾往前走【原始 buffer】
      (不反转正文、不复制正文, 多字节 UTF-8 也不会被拆散: 反向程序是 RE2 编译器自己编的)。
      为的是 `S B{m,n} L` 这个形状 —— 起始类窄于重复类的计数重复, 正向 DFA 状态数对界指数,
      反向线性。实测 [A-Za-z][A-Za-z0-9]{2,19}key × 120 份 8KB 语料: 正向 35149 状态 / 5.35MB,
      反向 45 状态 / 0.01MB, 命中集一致。
      ⚠ Match 只回答"命中没有 / 哪几条命中", 不回答"在哪"。要位置别再走"命中之后正向
      FindStringIndex 重扫整篇"那条老路 —— 用 FindAllIndex (见下), 反向 set 直接给匹配左端,
      再用正向 set 的 ResolveSpan 取右端, 第二遍只碰命中那一段。
      🔴 正向 RegexpSet 和反向 RegexpSetReverse 是【两个类型】, 不是一个类型上的 Reverse() 开关
      (2026-08-25 拆的; 单条那边 Regexp / RegexpReverse 本来就是两个)。理由三条: 两份完全不同的
      DFA 状态缓存混在一个对象里 MemInfo() 说不清是哪份; 两边贵法差三个数量级 (155 条表扫 6.4MB:
      正向 18ms 零 flush, 反向 65 秒 · arena 顶满 254MB 还在 flush), 藏在 bool 后面看不见; 两边
      吐的位置【含义相反】(右端 vs 左端), 同名方法返回意思相反的数字最容易写错。
      ⚠ 方向是【每条 pattern 各自】的决定: (?s).{20}key 正向 21 状态 / 反向 1,
      key(?s).{20} 正好反过来。拿真语料各建一个单条 set 比 MemInfo().States 就知道该往哪边放。
      ⚠ 这跟"自己把 pattern 倒着写 + 把正文倒过来"不是一回事, 而且库这条更省: RE2 的 Simplify
      把 x{2,19} 展成"必需拷贝在前", 反序之后可选嵌套跑到读取顺序前面, 活跃起点集合互相嵌套
      而不是任意子集。同一条语言同一串字节实测 17 状态 vs 手写反转 25247 状态。
      详见 README.md#scanning-backwards。
    - CompileMaxMem(pattern, maxMem) / MaxMem(): 单条 Regexp 的内存预算 (以前只有 Set 能调,
      单条硬吃 RE2 默认 8MB)。同一个旋钮抬两条天花板 (编译期指令数上限 + 运行期 DFA 状态缓存额度),
      口径与 NewRegexpSetMaxMem 完全一致。实测同上那条 pattern × 60 份 16KB 语料:
      默认 8MB 下 flush 6 次, 256MB 下 0 次; 而反着扫在默认预算下就 0 次 (状态峰值 9)。
      —— 调预算是拿内存换, 换方向不花钱, 形状允许就先换方向。
    - Prefilter (NewPrefilter / Atoms / Potentials / Unfiltered): RE2 自己的「必需字面量」推导
      (FilteredRE2 + PrefilterTree, 本来就 vendored 在这, 现在接出来了)。回答三件事:
      这批 pattern 想命中【必须】先出现哪些字面量 (Atoms, 已小写去重) · 正文里找到了这几个原子
      之后还有哪几条【可能】命中 (Potentials, 没进名单的保证不命中) · 以及最要紧的反问:
      哪几条【没有】必需字面量因而任何粗筛都筛不掉 (Unfiltered)。
      本身不做匹配 —— 原子表交给调用方自己的字符串匹配器 (AC / memmem) 去找。
      🔴 Unfiltered 才是接它出来的动机: 「先用便宜的字面量门挡掉大多数正文」是唯一能抬吞吐上限的
      方向 (见 doc §4 G), 但天花板就是这批筛不掉的条数 —— 做之前先量, 别做完才发现天花板在 3%。
      🔴 这个数只有 RE2 的 prefilter 算得准: 手写抽取器在 `(?:foo|[A-Z]{5})` 上会答错 (含字面量 foo,
      但另一支不需要它 ⇒ 整条不可过滤)。minAtomLen 是真旋钮不是细节: 调大 ⇒ 原子更少更长 (匹配器更快)
      但更多 pattern 掉进不可过滤集。实测 112 条生产表: RE2 默认 1654 原子 / 4 条不可过滤, 但原子短到
      几乎每份正文都能找到 ⇒ 一条都筛不掉; minAtomLen=6 是 216 原子 / 38 条不可过滤, 筛得动但底线 34%。
      对拍见 prefilter_test.go (健全性: 真命中的必须一条不落地出现在 Potentials 里)。
    - FreeC: 显式释放 native 句柄 (否则靠 finalizer)。
    - RegexpSet: N 条正则编进一个 DFA, 一遍扫回答"哪几条命中"。详见 README.md#regexpset。
      条数多到 NewRegexpSet 报 "set compile failed (out of memory)" 时,【不要拆成两个 set】
      (两个 set = 正文扫两遍), 改用 NewRegexpSetMaxMem(patterns, maxMem) 把 RE2 预算翻倍到装下为止。
      ⚠ "编得过" ≠ "预算够": 那只过了编译期那道天花板。运行期那一半够不够是另一个问题,
      只能靠数 flush 次数(见下面 DFAStats)。真的在 thrash 的 set, 拆开反而更快 —— 上面这条
      "不要拆" 只管编译期天花板。
      Match/MatchAny/MatchBytes/MatchAnyBytes 不返回 error: Compile 过了之后运行期 DFA 不会爆
      (RE2 对 set 的 DFA 只 flush 缓存不 bail; 且 CompileSet 编译期就跑过 DFA 冒烟测试) —— 依据与
      实测见 README.md 的 "Why Match has no error return"。
    - RegexpSet.FindAllIndex / RegexpSetReverse.FindAllIndex (+ NewFindAllIndexAlloc,
      工作区类型 RegexpSet_FindAllIndex_Alloc_t, 另有 FindAllIndexBytes):
      【一遍扫正文, 边扫边说命中端点在哪】—— Match 只回答"哪几条命中", 这个回答"命中在哪"。
      位置本来就在 DFA 热循环里算出来了 (lastmatch), 上游只是把它丢掉; 这套 API 把它收下来。
        alloc, _ := set.NewFindAllIndexAlloc()          // 热路径上建一次留着; 不是并发安全的
        set.FindAllIndex(body, alloc, func(runs []RegexpSet_FindAllIndex_Run_t) {
            for _, r := range runs { … r.ReIndex / r.Lo / r.Hi … }   // 正向: Lo..Hi 是匹配右端
        })
        // 反向: rev.FindAllIndex(body, ralloc, func(runs []…Run_t) { … })  // Lo..Hi 是匹配左端
      为什么值得: 原来要位置只能"拿到 index 之后, 对那几条各跑一次 FindStringIndex", 每跑一次
      就是【又扫一遍整篇正文】, 而且用的是非锚定正则 —— .*? 前缀让"哪个位置能当匹配起点"
      变成"每个位置都能", 那正是状态数指数增长的引信 (见 doc/状态数为什么会相乘.txt:
      同一条 pattern 加个 \b 就是 967 倍)。有了位置, 第二遍只跑【命中那一段】, 而且可以写成
      锚定的 \A(?:pat) —— 起点只剩一个, 走不通当场 DeadState 收工, 那套爆炸机制从根上不存在。
      🔴 第二遍【必须锚定】才有这一半收益。继续拿原来那条非锚定正则去扫 text[start:],
      .*? 前缀会让它一路扫到正文末尾, 省下来的只有 start 之前那一截, 状态照建不误。
      吐的是【端点游程】(ReIndex, Lo, Hi) 不是逐个位置: kManyMatch 的 DFA 在每一个能结束匹配的
      位置都报一次, 一条可变长 pattern 会连出一串 (`[a-z]{3,}` 撞 "abcdef" 在 3/4/5/6 各报一次),
      按 pattern 把连号的收敛成一段再吐。正向 set 给的是匹配【右端】(不含), 反向 set 给的是
      【左端】(含); 【两端都含】的闭区间, 恒 Lo <= Hi (原文坐标)。说的是哪一端由【方向】定死,
      不靠字段名 —— 一个结构体里塞两套名字反而会骗人。
      🔴 闭区间不是笔误: 这三个数不是一个区间, 是"一串端点"的收敛写法, Hi 是一个真端点。
      写成半开就得记住 Hi 算不算, 而那正是最容易错的地方。
      🔴 两端都给是【必须】的, 不能只留一端: `ab|c` 撞 "abc" 的两个 end 是 2 和 3 —— 连号,
      只留 3 就把 [0,2) 那个匹配悄悄弄丢了, 而且不报错。展开 Lo..Hi 就能无损还原逐个位置。
      🔴 顺序【不保证】全局按位置升序 (游程要等"这条再次命中且不连号"或"整篇扫完"才收口,
      不同 pattern 会交错)。但【同一条 pattern 内部】是升序的 —— 上面那层 MatchScanner 的游标
      就靠这条。要全局有序自己排, 排的是游程条数不是位置数。
      🔴 语义【不是】FindAllStringIndex: 那个只给 leftmost-first 的不重叠序列, 这里给的是
      所有 pattern 的所有匹配端点, 重叠的也在里面 (`abcd|bc` 撞 "abcd" 两条都报)。要取舍
      (优先级贪心 / 相交即丢 / …) 是调用方的业务规则, 库这层不替它决定 —— 要成品区间用
      NewMatchScanner (见下)。
      🔴 给的是【一批一个数组】: 既不是"一次性全给"也不是"一条一个回调"。
      不能全给: 游程条数没有上界 (真表约 30741 条/MB, 200MB body = 47MB), 攒成一个数组等于
      让内存跟着正文长度走。也不该一条一回调: 那是每条游程一次不可内联的间接调用, 6.4MB 上
      19.7 万次。量过 (两条腿【交替】跑各 40 轮取中位数 —— 顺着跑量不出来, 进程内前后段漂移
      就有 ±5%, 比要量的东西还大): 一条一回调比一批一数组慢 +1.0% / +1.6% / +1.4%,
      合每条游程约 1.3ns。省下的只有 1% 出头, 但这个 API 的存在理由就是快, 白给的也不给。
      🔴 交给 batchFn 的那段切片是工作区里那块缓冲【本身】, 下一批原地覆写 —— 要留自己 append 走。
      alloc 是什么: native 那层是 sqlite3_step 式的, 攒满一批 (4096 条 / 48KB) 就【挂起】——
      按内容存下 DFA 状态、放掉缓存读锁、返回给 Go, 取走再进去接着扫。挂起期间一把锁都不持有
      (换成"C 直接回调进 Go"就得攥着读锁跑 Go 代码, 谁想 flush 谁等着)。不能改成"缓冲不够就
      扩容重跑": 重跑付的正是最贵的那一遍 (新正文现造状态, 同档 0-flush 下 23.7ms/份 vs
      重扫 0.36ms/份 = 66 倍)。批大小【不是旋钮】—— 没有值得调的余地, 多个参数只是多一处能填错。
      alloc 传 nil 也能用 (当场建一个用完就扔)。🔴 不是并发安全的, 一个 goroutine 一个;
      也不能跨 set 用 (正反算两个 set), 串用返回 error 而不是给错答案。
      🔴 偏移是 int32 不是 int 也不是 uint32: 宽度锁死且是 native 的原生宽度 (零转换);
      不用无符号是因为上面那层算起点要做 end-minLen, 正文开头几个端点上这是负数 —— 有符号下
      一眼可判, 无符号下回绕成 42 亿会【静默】通过边界检查然后在切片上炸。RE2 本来就把正文
      卡在 2GiB。
      ⚠ 没有"回调返 false 就地停"。只想知道"有没有命中"用 MatchAny —— 它在 RE2 那层打开
      want_earliest_match, 比在 Go 这侧半途刹车还早收工。
      ⚠ Set 是 never_capture(true) 编的, 永远不给子组位置 —— 需要 FindAllStringSubmatchIndex
      (要 group1 的偏移) 的调用方, 这条路服务不了, 只能照旧单独跑那条正则。
    - RegexpSet.ResolveSpan / ResolveSpanWithin / ResolveSpanBytes (RegexpSetReverse 上同名同形):
      【锚定解析】—— 把 FindAllIndex 吐的那【一端】补成一整段。方向跟着 set 走, 正反配成一对:
      正向 set 的 ResolveSpan(text, 左端, id) 给右端(不含); 反向 set 的给左端(含)。
      所以完整流水线是【两个方向各一个 set】: 一个负责扫出端点, 另一个负责把端点补全。
      🔴 这一步不要在 Go 那侧自己补。上面说的"第二遍必须锚定"就是这条: 拿原正则去扫
      text[start:] 是【非锚定】的, .*? 前缀让它一路扫到正文末尾, 白扫也白建状态; 自己另编
      一条 \A(?:pat) 倒是锚定了, 但那是每条 pattern 一个 Regexp 对象、一份独立 DFA 缓存,
      而且它跟 set 里那条是不是同一个语义全靠人工保证。ResolveSpan 走的是 set 自己那份程序
      里【不带 .*? 前缀的那个入口】—— 从外面根本够不着 (为此把 CompileSet 的两个入口按
      Compiler::Compile 的规矩摆开了, 见 VENDOR.txt), 用的也是 set 自己那份 DFA 缓存。
      🔴 给的是【最长】那个匹配的另一端, 不是碰到的第一个。变长 pattern 在同一个端点上通常
      有一串长度都成立 (AAA-[A-Za-z0-9]{8,16} 在同一个右端上有 9 个合法左端), "碰到第一个
      就收工"给的是最短那个 = 把命中截断, 下游做定长校验时会把真命中判成假命中。
      成本 = 这条命中实际能延伸到多远, 与正文长度无关 (走到 DeadState 就收工); 真能无限
      延伸的 pattern ((?s).*KEY 那种) 用 ResolveSpanWithin 的 bound 掐住回看距离。
      判定用的上下文恒是【整篇正文】, 所以 \b / ^ / $ 看到的永远是真实邻居字节, 而不是
      bound 切出来的假边界 —— 掐 bound 只会让答案变短, 不会让它变错。
      无状态、只读 (自己拿 DFA 的缓存读锁), 可以和别的 goroutine 的扫描并发调。
      🔴 补另一端【只能走这条】, 不能拿反向 FindAllIndex 去扫一遍。这里是"在一个点上问一句",
      代价 = 这处命中能延伸多远, 1KB 和 6.4MB 的正文上一样贵; 反向扫全表在 6.4MB 上是 65 秒
      (正向 18ms), 拆成一条一条反着扫倒是便宜, 可命中 k 条就是 k 遍全文 —— 正好是 FindAllIndex
      存在的意义 (把 1+k 遍压成 1 遍) 原样赔回去。两个 API 长得对称, 用途不对称。
    - RegexpSet.NewMatchScanner + (*MatchScanner).SetModes / Scan / HitIDs / Hit / Close
      + RegexpSet.PatternLenRange / ReverseOneStats:
      【一遍扫, 直接给不重复的命中区间】—— 把上面两件 (游程扫 + 锚定解析) 拼成调用方真正要的
      形状, 顺带把"同一处命中报出一串右端"那种重复在库里就解决掉。替的是这套两段式:
      先 Set.Match 扫一遍拿"哪几条命中", 再为了知道【在哪】把命中的每条各对整篇正文跑一遍
      FindAllStringIndex —— 命中 k 条就是 1+k 遍全文。
        ms, _ := set.NewMatchScanner()      // 热路径上建一次留着; 不是并发安全的
        defer ms.Close()
        ms.SetModes(modes)                  // 每条要什么, 三态 (见下); 不调 = 全默认档
        unres, _ := ms.Scan(body, func(ms []SetMatch) {   // ← 唯一一遍全文, 结果【一批一批】来
            for _, m := range ms { … m.Index / m.Lo / m.Hi … }   // text[Lo:Hi] 是第 Index 条的真匹配
        })                                  // 之后 Hit(i)/HitIDs() 就是门那张 bool 表
        // unres 里的那几条这次拿不到 (反向/正向单条对象编不出来 / 游程乱序 / DFA 预算不够)
        //   → 把本遍已经收到的这几条【全丢掉】, 照老路 re.FindAllStringIndex
        //   (作废可能发生在已经交出去几处之后; 配了 boolOnly 的【不】算作废, 一处都不会来)
        // 🔴 交给回调的切片是内部缓冲本身, 下一批原地覆写; 各条 pattern 的结果【交错】着来
        //    (同一条内部按 Lo 升序), 想按条归拢是调用方一句 append 的事
      🔴 库【故意不给】"一次性物化成数组"的接口 (AppendAllMatches 2026-08-27 删了)。要数组的
         自己在回调里 append 一行 —— 那一行写在调用方家里, 谁写谁看得见代价 (内存 ∝ 命中数)。

      ── 三态旋钮 SetModes ─────────────────────────────────────────────────────
        MatchScanMode_span      要区间。【零值 = 默认档】, 库自动分档, 对外保证 leftmost-longest。
        MatchScanMode_boolOnly  只要"命中没命中"。一处区间都不收口、一次端点都不补。
        MatchScanMode_spanFast  要区间, 快, 【不保证】leftmost-longest。见下面那段。
      🔴 零值就是最稳那一档 —— 漏配一条的后果是【慢】, 不是【错】。
      🔴 boolOnly 不是可选优化, 也不能靠"调用方在回调里自己过滤"顶替: 挡掉的是那几条的
         【端点补全】(这一层真花钱的那步), 不只是少交几处。门上很多位只当外层短路的 bool 用
         (bgAPACCombined 那种), 从来没人问它在哪 —— 真表上光两条这样的 pattern 就占了 57%
         的游程。回调里过滤是钱已经花完了才扔。
      🔴 能匹配空串的 pattern 配 span/spanFast 会被 SetModes 当场【报错】(每个偏移都是零长
         命中, 游标推不过去), 不是运行时静默退化。这种条只能配 boolOnly, 或者走老路。

      ── 默认档交出来的区间, 逐字读 ────────────────────────────────────────────
        ① text[Lo:Hi] 是这条 pattern 的一个【真匹配】;
        ② 【同一条 pattern】吐的区间互不相交, 按 Lo 升序;
        ③ 口径是 leftmost-longest, 即 stdlib 的 re.Longest().FindAllStringIndex。
      🔴 ③【不是】"和 FindAllStringIndex 一样"。stdlib 默认的 FindAll 是 leftmost-first
         (贪心), 两者在"同一起点上贪心先撞到的比最长的短"时给不同右端。对拍要拿 Longest()
         那个去对, 拿默认那个对会是【假红】。
      🔴 ② 只管【单条】。两条 pattern 在同一片正文上照样重叠 —— 那不是重复, 是两个问题各要
         一个答案 (见下面"跨 pattern 一概不合并")。
      怎么做到的 (三条腿, 详见 doc/MatchScanner的leftmost-longest保证.md):
        定长 (min == max)  Lo = Hi - min, 一句减法, 不进正则引擎。起点唯一 ⟹ 可论证。
        变长 (默认档)      从 max(游标, Hi-maxL) 起做一次【正向非锚定】搜索拿最左起点, 再从
                           这个起点锚定取最长右端 —— 那就是 leftmost-longest 的定义本身,
                           不挑 pattern 形状。整串 + startpos, 不切片 ⟹ \b / ^ / $ 看到的
                           是真实邻字节。代价: 每处比 spanFast 贵约 1.6x, 无上界的条目还要
                           走完空隙 (各轮窗口两两不交且递增, 累加封顶 = 多扫一遍正文, 2.00x)。
        补不出来的         进 unresolved, 请调用方照老路 —— 宁可退回去也不给"像是对的"答案。

      ── spanFast: 什么时候才该挂 ──────────────────────────────────────────────
      它强制走【游标启发式】那条路: 从右端往左锚定回推一次拿最靠左的起点, 代价与正文长度无关
      (这是它比默认档便宜的全部原因)。给的是【第三种口径】, 既不是 leftmost-first 也不是
      leftmost-longest: 随机撒 3000 条变长 pattern × 40 段正文 = 12 万处对账, 与 FindAll 相同
      119940, 与 Longest 相同 119972, 两个都不是 28。根子在这一遍扫描给的是【右端集合】,
      里面既没有 (起点,终点) 的配对也没有优先序 —— 要"所有右端"只能用 kManyMatch, 而它正是
      把优先序扔得最干净的那个 (kFirstMatch 撞到 Match 就 break, kLongestMatch 每段 sort);
      配对只能在 Go 这侧靠游标重建。所以"拿到全部右端"和"贪心/最长"是互斥的, 不是没写好。
      🔴 岔开那 28 处【是真匹配但边界偏了】: 拿去过校验位 (身份证 · IBAN mod-97 · Luhn) 会
         失败, 整条真命中被调用方自己毙掉 = 无声漏报; 拿去脱敏就是少盖几个字节的明文。
      🔴 但它【不是】下水道, 是【自动分档判错时的出口】: 库只按 min/max 分档, 判成变长就一律
         落到默认档那条贵路。可"变长"不等于"有歧义" —— 两头带 \b 的变长条实测岔开【0 处】
         (裸 pattern 60 处, 换成 \b(?:…)\b 之后 0 处)。这种条落在默认档上是白掏钱。
      🔴 挂之前先把凭据跑出来, 别凭感觉: 拿这条 pattern 自己 fuzz 一遍 (随机正文 × 两档对拍),
         数出岔开 0 处再挂。跑出来了就挂 —— 挂上之后它既是快的那条路, 又确实是 leftmost-longest。
         怎么写这个 fuzz、判据是什么、真实产品上拉进来了多少条: doc/MatchScanner的leftmost-longest保证.md。

      ── 内存: 这一层一个字节都不留 ────────────────────────────────────────────
      🔴 游标推进在回调里当场跑完, 收口出来的区间写进一块固定的 12KB 缓冲 (1024 处), 满了就
         交出去、就地复用。底下 native 那层本来就是 sqlite3_step 式的 (一批 4096 条 = 48KB,
         装满就挂起回调, 缓冲循环复用, 不随正文长度涨); 要是这一层把结果 append 起来等扫完
         再还给调用方, 那个固定缓冲就白设计了 —— 真表上游程约 30741 条/MB (0.23MB/MB), 收口
         后的输出还有 0.037MB/MB, 200MB 的 body 就是每个并发扫描 7.4MB 常驻。
         (归拢成"按 pattern 一张表"也是攒 —— 所以那一步交给调用方, 库这边只管一批一批交。)
         能边扫边收口是因为同一条 pattern 的游程【跨批次也是升序】(正向扫本来就从左往右走;
         真表 196744 条游程 / 50 批, 乱序 0 处)。万一乱序, 那一条当场作废进 unresolved。

      🔴 反向必须是【一条一个 set】, 整表建一个反向 set 是死路 —— set 里状态数是相乘的
         (doc/状态数为什么会相乘.txt): 155 条的真实规则表, 正向 6.4MB 上 18ms / 零 flush,
         整表反向 65 秒 / arena 顶满 254MB 还在 flush。拆成一条一个就没有这个乘法; 而且这些
         反向 set 【从不用来扫正文】, 只做锚定解析, 起点只有一个, 爆炸机制从根上不存在。
         惰性建 + 便宜得出奇: 真表上被问到的 32 条一共才 973 个状态 / 2.0MB (ReverseOneStats)。

      🔴 【只在同一条 pattern 内部去重, 跨 pattern 一概不合并】: 两条 pattern 撞在同一片正文上
         不是重复, 是两个问题各要一个答案 (带空格的和不带空格的两条, 下游正是靠"这段里有没有
         空格"分流; 合了就是漏检)。这条在真表上量过:
         6.4MB 真语料 74249 处命中里, 被 ≥2 条 pattern 盖住的字节占已盖住字节的 55.6%,
         同一字节最多被 8 条盖; 而"哪一条该赢"要等消费点把校验位跑完才知道 ——
         "Passport No: A123456780" 上台湾身份证规则与护照号规则抢完全同一段 [13,23), 身份证那条
         下标在前先占, 而它自己又过不了 mod-10 校验位 ⟹ 这段明文护照号一条都不出 = 无声漏报。
         跨条合并是调用方的事 (消费点按自己的优先级序合并 · 脱敏那层再按位置收一次)。

      ── 实测 ──────────────────────────────────────────────────────────────────
      🔴 下面两组数【表和语料不同, 不许横着比】, 而且要看清楚是哪一档量的。
      (一) 生产形态一张 set: 90 条 · 52 位要区间 · 7.03MB (2026-08-26, 三种路一起量的那次)
        old (老路: 门 Match 一遍 + 命中的每条各自 FindAll 整篇, 口径 leftmost-first)  78.2ms
        默认档 (窗口 + 正向非锚定)                                                    43.8ms = 1.79×
        spanFast (游标启发式)                                                         24.6ms = 3.18×
        只过门不收口                                                                  21.9ms
        三档交出的处数完全一样 (10956 处), 区间也完全一样 —— 这张表上的位两头都锚死。
      (二) 155 条规则表 × 6.4MB 真语料 · 命中 47 条 · 稳态 · 生产预算 64MB
        🔴 这组是【spanFast 那条路】量的 (量的时候还没有默认档那条路):
        整条腿 (Match+逐条 FindAll) → (Scan+按条取):  369.3ms → 24.6ms = 15.03× (兜底 0 条)
        只留定长档 (87 条要位置的里只有 28 条是定长):  368.8ms → 312.1ms = 1.18×
        接不了的是 25 条【没有长度上限】的 (邮箱那种), 没有 maxL 就框不出回看窗口; 它们配
        boolOnly 不进位置档, 不影响这个数。
        这张表上新旧两腿的结果【逐处全等】(31379 处命中, 假匹配 0 · 没覆盖到 0 · 自重叠 0)
        —— 因为这些是身份证/IBAN 那种刚性格式 (两头 \b 锚死)。但那是这张表这份语料的性质。
        分段:  Set Match 18.6ms · 同一遍收游程 18.7ms (位置白送) · Scan 全套 33.8ms
        内存:  进程 VmHWM 107.2MB → 108.1MB (+0.9MB) · Go 分配 4.0MB/2252 obj → ~0/146 obj
        按正文长度: 真语料 8KB 以下打平 (1.0×) · 32KB 3.1× · 512KB 6.1× · 2MB 14×
        最坏 (每 38 字节一处命中的合成串): 0.94×, 即 6% 慢 —— 每处命中要两次 cgo 往返,
        正文短到几乎全是命中时这笔固定开销赢不了。真 base64 碎片不长这样 (打平)。
    - 性能 (spanscan_bench_test.go · 64KiB 正文 · 10 条通用 pattern · Ryzen 5900X · 稳态复用):
      对照的"旧实现"就是今天调用方那一套 —— set.Match 当门 + 逐条命中 pattern 在整篇正文上
      FindAllStringIndex (命中 k 条 = 1+k 遍全文扫描, 而且后面那 k 遍是最贵的非锚定扫描)。
      🔴 这一档的"推荐用法"只适用于【要与 FindAllStringIndex 逐字节等价】的调用方 ——
         反向 set 扫 + 一次左到右的推进 (rev-cov, 判据抄在 matchscan.go 注释里), 与 FindAll 逐处全等
         (TestSpanPerf_Shape 直接对账), 三档都不劣于旧实现:
                              0 命中     稀疏(39 处)   最坏输入(见下)
        旧实现                 93.1us      400.8us      596.0us  给 4 处
        反向扫 + 左到右推进    89.7us       94.7us      611.7us  给 4 处   ← 推荐
        只扫不解析             94.3us       94.7us      271.1us
        扫 + 每游程解析一次    93.9us       97.7us      637.2us  给 4 处
        同上 + bound=64        89.2us       92.1us      271.1us
      全部 0 B/op 0 allocs/op (工作区复用 + 两处出参都走按值返回的孪生, 见 VENDOR.txt)。
      怎么读:
      · 0 命中 —— 产线绝大多数正文长这样。两边都是一遍扫, 打平 (±3% 噪声内)。
      · 稀疏命中 —— 4.2 倍。省掉的正是旧实现那 1+k 遍全文重扫。
      · 最坏输入 —— 语料是【全小写无空格】, 表里 [a-z]{4,} 一口吃掉整篇。这一档打平: 旧实现
        FindAll 内部同样要为那 4 处各回走一趟同样长的正文, 谁也没有便宜可占。想更快只能掐
        bound (给"能无限延伸"的 pattern 配一个回看上限, 解析成本从 O(正文) 钉回常数) —— 那是
        调用方自己声明的取舍: 比上限长的命中会被丢掉。
      · "左到右推进" 省的是【同一处命中被拆成多条游程】那种冗余: 变长尾巴每走到一个可收的
        位置就成一条游程 ([a-z]{5,}ing 撞一大段小写 = 每个 "ing" 各一条), 逐条各解析一次就是
        O(游程数 × 正文长); 推进之后落在上一处里面的游程直接跳过, 压回 O(正文长)。
      🔴 反过来推是【错的】—— 正向 set 从右往左推给的是 rightmost-longest, 不是这个口径:
         abc?|bcd 撞 "abcd" 时 FindAll 给 [0,3)="abc", 反着推给 [1,4)="bcd"。两个都自洽, 但
         需求要拿这一段做定长校验, 差一个字节就判成另一回事。钉死在 TestSpanPerf_CovDirection。
      🔴 还有一档叫 "-all"(把游程里每个端点都解析一次), 最坏输入上 5.6 秒 —— 它【不是】上面
         那个口径, 是调用方把游程展开之后的成本。那 65541 个右端里 65533 个来自同一条游程、
         同一个匹配 ([a-z]{4,} 吃掉整篇), 展开得到的是一串互相嵌套的区间, 没有新信息, 而每一个
         都要从自己那个右端走回偏移 0 = O(正文²)。留着这一档只是为了标明"别这么用"。
      内存峰值 (每条路一个子进程各报 VmHWM 增量, 喂 12 篇互不相同的 256KiB 最坏形状语料):
        旧实现 528KB = 1 份 set DFA 缓存 + 10 份各自独立的 Regexp DFA 缓存
        正向路 400KB · 反向路 596KB = 各 2 份 set DFA 缓存 (解析那份只建 160~170 个状态, 很便宜)
      预算口径上差得更远: 旧实现是 1+N 份各自 8MB 额度的缓存, 新实现恒 2 份。
      🔴 但【不要把这一档当成通用推荐】。它的证据是 10 条 pattern / 64KiB 的合成微基准, 那个
         规模【结构上】显不出 set 里状态数相乘这件事。同一条路换成真实的 155 条规则表 × 6.4MB:
         整表反向 set 一遍扫要 65 秒 (arena 顶满 254MB 仍在 flush), 正向同表 18ms 零 flush ——
         整整差四个数量级, 结论直接翻过来。不要求逐字节等价的调用方一律走上面的 MatchScanner
         (正向扫 + 单条反向锚定回推), 别建整表反向 set。
      ⚠ 量这类差别时【两条路必须共用同一批 set 对象】: 同一批 pattern 建两次, 两个 DFA 的
        状态区落在不同地址上, cache set 冲突不一样 —— 实测同一段代码只因为换了个 set 对象
        就能差 5~8%, 比要量的差别本身还大。
* vendored 的 re2 里改过的上游文件只有这几个 (完整清单与升级时怎么重打见 VENDOR.txt):
  re2_dfa.cc 是大头 —— 转移表槽位从 8 字节指针改成 4 字节 arena 下标 + arena 按需增长。
  同预算多装 1.74 倍状态 (0-flush 门槛降一档), 健康档吞吐不降, 峰值 RSS 约 -30%。
  命中集与原版逐位一致 (跨 pattern 表/语料/预算矩阵对拍钉死)。
  要编原版对照: CGO_CXXFLAGS="-O2 -DRE2_DFA_NEXT_BITS=64 -DRE2_DFA_ARENA=0"。详见 README.md。
  其余几处是【纯追加】: prog.h / set.h / re2_set.cc 上的观测出参与访问器, 外加
  re2_compile.cc / re2_set.cc 上给 Prog::CompileSet 和 RE2::Set 加的 reversed (反着扫, 见上),
  以及流式游程扫描 + 锚定解析 (re2/span_scan.h + re2_dfa_spanscan.inc, 由 re2_dfa.cc 末尾
  #include 进去 —— class DFA 整个定义就在 re2_dfa.cc 里, 别的编译单元看不见 State/RWLocker/
  StateSaver)。唯一一处【改了上游行为写法】的是 Compiler::CompileSet 的两个入口:
  上游写成 "anchor_start_=true + start_ 和 start_unanchored_ 都指向带 .*? 前缀的那个入口",
  靠 SearchDFA 里 anchor_start() 那一条把搜索强制成锚定; 现在改成与 Compiler::Compile 一模
  一样的摆法 (start_ = 不带前缀的真锚定入口, start_unanchored_ = 带前缀的, anchor_start_
  照实写 false), RE2::Set::Match 相应改传 kUnanchored —— 走的还是同一个带前缀的入口, 命中集
  逐位不变 (Set/reverse/prefilter/stats/maxmem 全套回归钉死), 换来的是 ResolveSpan 能进那个
  真锚定入口。两个入口都是 Prog::Flatten 本来就认得并 remap 的 root, 所以 Flatten 一个字没动。
  🔴 那份是【另写的一份热循环】, 没往 InlinedSearchLoop 里塞 if: 老循环是全库最热的那个,
  它只管把 id 塞进 SparseSet (塞一次就够, 还能 early-out); 游程扫描每个命中字节都要维护
  "这条 pattern 的游程长到哪了"且永不早退。混在一起是白给老循环加分支。
* 与 stdlib 的边角差异 (非法 UTF-8 上 . 的匹配、\C 等) 见 README.md 的 Caveats。
* 测试: go test ./... (每个方法对拍 stdlib regexp; FindReplaceWithin 对拍 stdlib 等价嵌套写法;
  []byte 系见 bytes_test.go: 同语料下双向对拍 stdlib 与自家 string 孪生 + 每方法手算的命中/不命中
  一对 (钉死 nil vs 空切片) + 零拷贝契约 (共享底层/输入不被改写/无改动复用 src/比 string(b) 少分配);
  反着扫见 reverse_test.go: 正反答案逐位对拍 (含 ^ $ \A \z (?m)^ \b (?i) (?s) 与多字节 UTF-8)
  + 状态数确实塌下来 + 方向是每条 pattern 各自的决定 + 库的反向比手写反转便宜 100 倍以上
  + 反向 DFA 放弃时退回正向且答案不变; 单条内存预算见 maxmem_test.go: 读回/编译失败/语义不变
  + "默认预算 flush、给够就 0 flush" 这条曲线;
  FindAllIndex 见 findallindex_test.go (与一个 O(n²) 暴力参考逐位对拍 —— 参考实现把前后缀按
  字面量钉死再拿【整篇正文】去匹配, 这样 ^ $ \b 看到的是真实位置, 拿 text[s:e] 单独匹配会判出
  一堆假命中; 外加"批大小只影响怎么吐不影响吐什么"、`ab|c` 连号游程可还原、同一个 alloc 反复
  扫不串味、alloc 跨 set/Close 之后当场报错而不是给错答案) · spanscan_need_test.go (按真实调用形态走完整流水线: 反向 set 拿左端 → 锚定
  正则只跑命中那一段拿右端 → 优先级贪心相交即丢 → 一次升序替换, 并钉死边界精确到字节、
  条数不多不少、以及"锚定的在错位置当场死 / 非锚定的会一路扫过来"这个机制本身; 同一个需求
  再用 ResolveSpan 走一遍, 正向路(扫右端→求左端)与反向路(扫左端→求右端)两条互相对账) ·
  spanresolve_test.go (锚定解析与另一个 O(n²) 暴力参考逐位对拍, 正反向各一遍; 外加"给的是
  最长不是碰到的第一个"、"同一端点上多条 pattern 各算各的"、bound 掐回看距离且不当词边界) ·
  spanscan_stress_test.go (把 maxMem 压到反复 flush + batch 压到 1 反复挂起, 结果必须与
  预算充足+大批一致 —— 挂起是"按内容存状态", 走错了不会崩, 会悄悄少吐几段; 外加同一个 Set
  上 8 个 goroutine 各拿一个 scanner 并发扫, -race 干净; 以及解析撞 flush: 解析与扫描共用同一份
  DFA 缓存, 起点状态被冲掉之后必须按内容重建, 判据是紧预算下的答案与预算充足时逐字相同)) ·
  spanscan_bench_test.go (新旧两种实现在 0 命中/稀疏/最坏输入三档语料上的对照测量, 见上面
  那张表; 另有 TestSpanPerf_Shape 摊开每档的规模并钉死"两条路各自都盖住旧实现给的每一处
  匹配", TestSpanPerf_NoAlloc 钉死稳态零分配, TestSpanPerf_Peak 把自己 fork 成子进程
  一条路一个进程各报 VmHWM —— 跑在同一个进程里高水位是共享的, 谁先跑谁吃亏)。
