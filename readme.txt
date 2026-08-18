本包文档见同目录 README.md (Markdown 正文)。
调 RegexpSet 的性能 (几百条正则一遍扫) 看 doc/set性能优化经验.txt —— README 里那几节的长版。

要点速记 (详见 README.md):
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
    - AppendAllStringIndexFlat(dst, s, n): 同 FindAllStringIndex, 但把 [start,end) 直接追加进
      调用方的 []int (形如 [s0,e0,s1,e1,…]), 不产 [][]int 外壳也不产一次性 flat 表。
      传 buf[:0] 复用缓冲 ⇒ 稳态零分配。匹配集合/顺序/空匹配语义与 FindAllStringIndex 逐处相同
      (同一段 C 循环), 只回填 group0; 要子组用 FindAllStringSubmatchIndex。
      详见 README.md#appendallstringindexflat。
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
    - MatchReverse / MatchReverseBytes / MatchReverseStats, NewRegexpSetReverse(MaxMem):
      【反着扫】—— 答案与 MatchString / 正向 Set 逐位相同, 但 DFA 从正文末尾往前走【原始 buffer】
      (不反转正文、不复制正文, 多字节 UTF-8 也不会被拆散: 反向程序是 RE2 编译器自己编的)。
      为的是 `S B{m,n} L` 这个形状 —— 起始类窄于重复类的计数重复, 正向 DFA 状态数对界指数,
      反向线性。实测 [A-Za-z][A-Za-z0-9]{2,19}key × 120 份 8KB 语料: 正向 35149 状态 / 5.35MB,
      反向 45 状态 / 0.01MB, 命中集一致。
      ⚠ 只回答"命中没有 / 哪几条命中", 不回答"在哪" (反向只知道匹配左端)。要位置就用它当便宜的门,
      少数命中的再正向 FindStringIndex 一次。
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
* vendored 的 re2 里改过的上游文件只有这几个 (完整清单与升级时怎么重打见 VENDOR.txt):
  re2_dfa.cc 是大头 —— 转移表槽位从 8 字节指针改成 4 字节 arena 下标 + arena 按需增长。
  同预算多装 1.74 倍状态 (0-flush 门槛降一档), 健康档吞吐不降, 峰值 RSS 约 -30%。
  命中集与原版逐位一致 (跨 pattern 表/语料/预算矩阵对拍钉死)。
  要编原版对照: CGO_CXXFLAGS="-O2 -DRE2_DFA_NEXT_BITS=64 -DRE2_DFA_ARENA=0"。详见 README.md。
  其余几处是【纯追加】: prog.h / set.h / re2_set.cc 上的观测出参与访问器, 外加
  re2_compile.cc / re2_set.cc 上给 Prog::CompileSet 和 RE2::Set 加的 reversed (反着扫, 见上)。
* 与 stdlib 的边角差异 (非法 UTF-8 上 . 的匹配、\C 等) 见 README.md 的 Caveats。
* 测试: go test ./... (每个方法对拍 stdlib regexp; FindReplaceWithin 对拍 stdlib 等价嵌套写法;
  []byte 系见 bytes_test.go: 同语料下双向对拍 stdlib 与自家 string 孪生 + 每方法手算的命中/不命中
  一对 (钉死 nil vs 空切片) + 零拷贝契约 (共享底层/输入不被改写/无改动复用 src/比 string(b) 少分配);
  反着扫见 reverse_test.go: 正反答案逐位对拍 (含 ^ $ \A \z (?m)^ \b (?i) (?s) 与多字节 UTF-8)
  + 状态数确实塌下来 + 方向是每条 pattern 各自的决定 + 库的反向比手写反转便宜 100 倍以上
  + 反向 DFA 放弃时退回正向且答案不变; 单条内存预算见 maxmem_test.go: 读回/编译失败/语义不变
  + "默认预算 flush、给够就 0 flush" 这条曲线)。
