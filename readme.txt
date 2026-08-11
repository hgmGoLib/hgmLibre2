本包文档见同目录 README.md (Markdown 正文)。

要点速记 (详见 README.md):
* 自带 cgo 的原生 RE2 正则库, 不用 go-re2 / 不用 abseil / 不用 cmake, 编译期不下载远程源
  (RE2 2023-03-01 已 vendored, 纯 C++11, zig 可交叉编译)。cgo 必须开启。
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
    - FreeC: 显式释放 native 句柄 (否则靠 finalizer)。
* 与 stdlib 的边角差异 (非法 UTF-8 上 . 的匹配、\C 等) 见 README.md 的 Caveats。
* 测试: go test ./... (每个方法对拍 stdlib regexp; FindReplaceWithin 对拍 stdlib 等价嵌套写法;
  []byte 系见 bytes_test.go: 同语料下双向对拍 stdlib 与自家 string 孪生 + 每方法手算的命中/不命中
  一对 (钉死 nil vs 空切片) + 零拷贝契约 (共享底层/输入不被改写/无改动复用 src/比 string(b) 少分配))。
