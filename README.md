# hgmLibre2

A self-contained native [RE2](https://github.com/google/re2) regular-expression
library for Go. It vendors RE2's C++ source and exposes it through cgo, so it
needs **no abseil, no CMake, and downloads nothing at build time**.

The public API mirrors the standard library `regexp` package for the
**listed string-only methods** (see [Supported API](#supported-api)), so a
`*hgmLibre2.Regexp` can stand in for `*regexp.Regexp` as long as you only use
those methods. It is *not* a full drop-in: the `bytes`/`io.Reader` variants and
`Split`, `SubexpIndex`, `LiteralPrefix`, `Longest`, marshal/unmarshal, etc. are
not provided, and a few edge-case semantics differ from stdlib — see
[Caveats](#caveats).

## Why

`regexp` in the standard library is already RE2-based and is the right choice
almost everywhere. hgmLibre2 exists for the narrow cases where you need the
real native RE2 engine but cannot pay the cost of the usual options:

- **No wazero / WASM runtime.** Wrappers like `go-re2` run RE2 inside a wazero
  WebAssembly runtime, which probes stdio handles at startup. In environments
  with no standard handles (e.g. a Windows SCM service) that probing can fail.
  hgmLibre2 links RE2 natively, so there is no runtime to instantiate.
- **No abseil / CMake.** The vendored RE2 is the last pre-abseil release
  (tag `2023-03-01`), which is plain self-contained C++11. cgo compiles the
  `.cc` files directly; there is no separate build system to drive.
- **Single static binary, cross-compilable.** Because it is just C++11 + cgo,
  it cross-compiles with [zig](https://ziglang.org) as the C/C++ toolchain.

If none of the above applies to you, prefer the standard library `regexp`.

## Install

```sh
go get github.com/hgmGoLib/hgmLibre2
```

Requires Go 1.19+. cgo must be enabled (the default) and a C++11 compiler must
be available. Any of clang, gcc, or `zig c++` works.

## Usage

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
	fmt.Println(re.ReplaceAllString("x=1 y=2", "$key")) // x y
}
```

Because the signatures match `regexp`, you can alias the type to swap engines in
one place:

```go
type R = hgmLibre2.Regexp
```

## Supported API

Same names and signatures as `regexp`. Matching is **leftmost-first**, the same
as `regexp.Compile` (RE2's default Perl mode), *not* leftmost-longest — e.g.
`(a|aa)` on `"aa"` yields `"a"`, just like stdlib. UTF-8 input.

- `Compile`, `MustCompile`
- `String`, `NumSubexp`, `SubexpNames`
- `MatchString`
- `FindString`, `FindStringIndex`, `FindStringSubmatch`, `FindStringSubmatchIndex`
- `FindAllString`, `FindAllStringIndex`, `FindAllStringSubmatch`, `FindAllStringSubmatchIndex`
- `ReplaceAllString`, `ReplaceAllStringFunc` (with `$1` / `${name}` / `$$` expansion)
- `FindReplaceWithin` — *not* in stdlib; see [FindReplaceWithin](#findreplacewithin) below
- `FreeC` — *not* in stdlib; see [Resource management](#resource-management)

### FindReplaceWithin

`find.FindReplaceWithin(strip, src, repl)` is exactly equivalent to the two-regex
idiom

```go
find.ReplaceAllStringFunc(src, func(m string) string {
    return strip.ReplaceAllString(m, repl)
})
```

— locate each match of `find`, then run `strip`→`repl` *within* that matched
segment — **but the whole outer loop and every inner replacement run in one
cgo call**, instead of one cgo crossing per match plus one per separator. The
algorithm is byte-for-byte identical: `find` can stay zero-capture so it still
uses RE2's fastest no-submatch DFA, and `strip` still only edits inside the
located segment.

It is **lazy / zero-alloc on the no-change path**: the C++ side does not build
or copy a result string until the first replacement that actually changes bytes.
If `src` is unchanged (no match, or matched but `strip` removed nothing), it
returns `src` verbatim with no allocation. Only a genuinely-modified input pays
for one result buffer.

One syntax note: `repl` is an **RE2 rewrite string** (passed to RE2's
`GlobalReplace`), so capture references use `\1`..`\9` — *not* the `$1` / `${name}`
syntax of `ReplaceAllString`. For the common literal `repl` (e.g. `""`) there is
no difference.

Motivating use case: prompt-injection "healing" — `find` = a separator-tolerant
verb skeleton (`i[\s._-]{0,2}g…`), `strip` = the separator class, `repl = ""`.
On the common path (normal traffic, no split verbs) it is allocation-free and
matches the plain DFA scan throughput; on attack input (many split verbs) it is
~2× faster than the nested-`ReplaceAllStringFunc` form with allocations
collapsed from O(matches) to one.

The test suite (`hgmLibre2_test.go`) cross-checks every method against the
standard library `regexp` on a shared corpus of patterns and inputs; results
are identical on that corpus. `review_verify_test.go` additionally pins the
known [Caveats](#caveats) below as differential tests.

```sh
go test ./...
```

## Caveats

It runs the **native RE2 engine**, so a few corners differ from Go's
`regexp` (which is a from-scratch reimplementation). These are intentional and
covered by `review_verify_test.go`:

- **Invalid UTF-8 input.** stdlib treats each invalid byte as one-byte
  `U+FFFD` and lets `.` match it; native RE2 only matches whole valid runes, so
  on e.g. `[]byte{0xff,'a',0xfe}` the pattern `.` finds just the `a`. If you
  match on possibly-invalid UTF-8 and need stdlib's behavior, use stdlib.
- **`\C` is accepted** (RE2 "any byte"); stdlib `regexp` rejects `\C` at
  compile time. Patterns valid here may be rejected by stdlib and vice-versa
  for a handful of RE2-only / stdlib-only escapes.
- **Capture group names** of any length are returned in full (no truncation);
  duplicate named groups are accepted, same as stdlib.

## Resource management

A `Regexp` holds a native RE2 object freed automatically by a finalizer, so for
ordinary use you do nothing. When you compile a large number of patterns
dynamically and want the native memory reclaimed promptly instead of waiting for
GC, call `FreeC()` to release the C++ object immediately.

`FreeC` is deliberately minimal and **unguarded**: it is not safe for concurrent
use, and calling any method (or `FreeC` again *with a live match in flight*)
after the object is freed is a use-after-free. `FreeC` itself is idempotent
(a second call is a no-op). If you don't need prompt reclamation, don't call it
and let the finalizer handle cleanup.

The native object is freed **exactly once** under every call ordering — there is
no double-free between `FreeC` and the finalizer, for two independent reasons:

- `FreeC` clears the finalizer (`runtime.SetFinalizer(re, nil)`) in the same call
  that frees the object. Since you must hold a live reference to `re` to call
  `FreeC`, the finalizer cannot already be scheduled, so clearing it always wins
  and it never runs afterwards.
- Even if a `nil` handle ever reached the underlying `cre2_free`, that function is
  null-safe (it returns immediately on `nullptr`).

Note the asymmetry this implies: only the free path tolerates a `nil` handle. The
match/replace methods do **not** — calling any of them after `FreeC` dereferences
a freed/`nil` RE2 and crashes. The null-tolerance exists solely so the finalizer
can never misfire, not as a guard for post-free use.

## Vendored RE2

The RE2 C++ source is vendored in this directory (see `VENDOR.txt` for the
exact layout and how to upgrade). It is pinned to RE2 tag `2023-03-01`, the last
release before RE2 took an abseil dependency; later releases cannot be compiled
this way directly.

## License

BSD 3-Clause, the same license as RE2. See [LICENSE](LICENSE) and
[RE2_LICENSE.txt](RE2_LICENSE.txt). The vendored RE2 files retain the copyright
of the RE2 Authors.
