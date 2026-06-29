# hgmLibre2

A self-contained native [RE2](https://github.com/google/re2) regular-expression
library for Go. It vendors RE2's C++ source and exposes it through cgo, so it
needs **no abseil, no CMake, and downloads nothing at build time**.

The listed string-only methods use the same **names and signatures** as the
standard library `regexp` package (see [Supported API](#supported-api)), so the
two are easy to read interchangeably. It is **not** a drop-in replacement for
`*regexp.Regexp`, and not meant to be: the `bytes`/`io.Reader` variants,
`SubexpIndex`, `LiteralPrefix`, `Longest`, marshal/unmarshal, etc. are not
provided, and some semantics differ from stdlib on purpose — most notably
**`ReplaceAllString` substitutes a *literal* `repl` (no `$1` / `${name}` / `$$`
expansion)** — plus a few edge cases; see [Differences from stdlib](#differences-from-stdlib-regexp).

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
	fmt.Println(re.ReplaceAllString("x=1 y=2", "*"))   // * *  (repl is literal)
}
```

## Supported API

The listed methods share their names and signatures with `regexp`. Matching is **leftmost-first**, the same
as `regexp.Compile` (RE2's default Perl mode), *not* leftmost-longest — e.g.
`(a|aa)` on `"aa"` yields `"a"`, just like stdlib. UTF-8 input.

- `Compile`, `MustCompile`
- `String`, `NumSubexp`, `SubexpNames`
- `MatchString`
- `FindString`, `FindStringIndex`, `FindStringSubmatch`, `FindStringSubmatchIndex`
- `FindAllString`, `FindAllStringIndex`, `FindAllStringSubmatch`, `FindAllStringSubmatchIndex`
- `ReplaceAllString` (repl is **literal** — no `$1` / `${name}` / `$$` expansion, unlike stdlib), `ReplaceAllStringFunc`
- `Split`
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

One syntax note: here `repl` is an **RE2 rewrite string**. (`RE2::GlobalReplace`
is RE2's own built-in replace-all; its *rewrite* string is RE2's native
substitution syntax: `\1`..`\9` expand to the corresponding capture group, `\0`
to the whole match, `\\` is a literal backslash, everything else is literal.)
So this differs from *both* stdlib's `$1` / `${name}` *and* this library's
literal `ReplaceAllString` repl — three different conventions. For the common
literal `repl` (e.g. `""`, which has no `\`), all three coincide.

Motivating use case: prompt-injection "healing" — `find` = a separator-tolerant
verb skeleton (`i[\s._-]{0,2}g…`), `strip` = the separator class, `repl = ""`.
On the common path (normal traffic, no split verbs) it is allocation-free and
matches the plain DFA scan throughput; on attack input (many split verbs) it is
~2× faster than the nested-`ReplaceAllStringFunc` form with allocations
collapsed from O(matches) to one.

The test suite (`hgmLibre2_test.go`) cross-checks every method against the
standard library `regexp` on a shared corpus of patterns and inputs; results
are identical on that corpus (the corpus uses only literal `ReplaceAllString`
repls, see the API difference below). `TestReplaceAllStringIsLiteral` pins the
literal-repl behavior, and `review_verify_test.go` pins the engine-level
[differences](#differences-from-stdlib-regexp) below as differential tests.

```sh
go test ./...
```

## Differences from stdlib `regexp`

This is the complete list of concrete behavior differences from Go's standard
library `regexp`. The first is an **API-design choice** (this library
deliberately is not a drop-in); the rest follow from running the **native RE2
engine** instead of Go's from-scratch reimplementation. All are intentional and
covered by tests.

1. **`ReplaceAllString` repl is literal — no `$` expansion.** stdlib expands
   `$1` / `${name}` / `$$` in the replacement string; here `repl` is inserted
   byte-for-byte with no expansion and no escaping (so `"$1"` stays `"$1"`,
   `"$$"` stays `"$$"`). This is the one method that is *not* signature-compatible
   in behavior. If you need capture-group substitution, use `ReplaceAllStringFunc`
   and build the replacement yourself. (`FindReplaceWithin` is a different,
   non-stdlib method and uses RE2's `\1` rewrite syntax — see its section above.)
2. **Invalid UTF-8 input.** stdlib treats each invalid byte as one-byte
   `U+FFFD` and lets `.` match it; native RE2 only matches whole valid runes, so
   on e.g. `[]byte{0xff,'a',0xfe}` the pattern `.` finds just the `a`. If you
   match on possibly-invalid UTF-8 and need stdlib's behavior, use stdlib.
3. **`\C` is accepted** (RE2 "any byte"); stdlib `regexp` rejects `\C` at
   compile time. More generally a handful of escapes are RE2-only or stdlib-only,
   so a pattern valid in one may be rejected by the other.
4. **2 GiB input limit.** Lengths/offsets cross the cgo boundary as 32-bit
   `int`, so inputs (and patterns) longer than `2^31-1` bytes are conservatively
   treated as *no match* / returned unchanged rather than matched. stdlib has no
   such limit. (Irrelevant unless you feed multi-gigabyte strings.)

Not a difference, but worth stating: matching is **leftmost-first** here, which
is also stdlib's default (`regexp.Compile`); stdlib's opt-in leftmost-longest
mode (`(*Regexp).Longest`) is not provided. Capture-group names of any length
are returned in full and duplicate named groups are accepted — same as stdlib.

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
