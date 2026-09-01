# hgmLibre2

A self-contained native [RE2](https://github.com/google/re2) regular-expression
library for Go. It vendors RE2's C++ source and exposes it through cgo, so it
needs **no abseil, no CMake, and downloads nothing at build time**.

The listed methods use the same **names and signatures** as the standard library
`regexp` package (see [Supported API](#supported-api)), so the two are easy to
read interchangeably. Both the `string` and the `[]byte` families are provided,
and they share one matching core — passing a `[]byte` costs no copy (see
[Byte-slice methods](#byte-slice-methods)). It is **not** a drop-in replacement
for `*regexp.Regexp`, and not meant to be: the `io.Reader` variants,
`SubexpIndex`, `LiteralPrefix`, `Longest`, marshal/unmarshal, etc. are not
provided, and some semantics differ from stdlib on purpose — most notably
**`ReplaceAllString` substitutes a *literal* `repl` (no `$1` / `${name}` / `$$`
expansion)** — plus a few edge cases; see [Differences from stdlib](#differences-from-stdlib-regexp).
Which of the two engines to use for a given call site is answered — with numbers — in
[`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md);
the short version is in [Why](#why).

## Why

**Use this library by default. Reach for the standard library `regexp` only in the
three cases listed below** — they are all recognizable by inspection, so no
experiment is needed to tell them apart.

Go's `regexp` is RE2-*derived* in syntax and in its linear-time guarantee, but its
matcher is a from-scratch NFA simulation (onepass / bitstate backtracking / one-pass
NFA). It gets a fast path only when a **literal prefix** can be extracted; when it
cannot, it restarts at every position. This library runs the real native RE2 lazy
DFA: one linear pass, essentially independent of input shape. That single difference
is where the numbers come from — it is not "C beats Go".

On identical patterns and identical corpora the native engine did not lose a single
*throughput* measurement (compile cost and the per-call floor are separate, and are
the exceptions below): **1.1× at worst, and 11–85× wherever the standard library
cannot extract a literal prefix** — a leading `(?i)`, `\b`, character class or alternation is enough
to lose it. Matching also happens entirely on the C side, so the steady state is
**0 B/op** on the Go heap, which keeps the GC heap goal (and therefore the process
peak) from moving at all.

Full numbers, method, corpora and every exception:
[`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md)
("choosing between this library and stdlib `regexp`", Chinese). Every number in it is
reproduced by `go test -run TestStdlibCompare -v .` (`stdlib_compare_test.go`), so
re-run it after changing the library or moving to another machine.

**Prefer the standard library `regexp` when:**

- **The pattern is compiled at run time and not cached.** Compiling costs 1.2–4.6×
  more here (native RE2 does more work up front), and a freshly compiled pattern used
  once cannot earn that back: measured 9.2 µs (stdlib) vs 21.6 µs here for
  "compile `(?i)\b<word>\b`, then match one 90-byte sentence". Hoist the pattern to a
  package-level variable if you can — or, if it is a whole keyword table, compile it
  into one [`RegexpSet`](#regexpset) instead, which puts you back on this library.
- **The call sits at the cgo bridge-cost floor.** One crossing costs ~67 ns here
  versus ~2 ns for a stdlib call (Ryzen 5900X · linux/amd64 · go1.26.5). That only
  decides the outcome when the *match itself* is cheaper than the crossing — i.e. a
  few bytes of input and a pattern simple enough for onepass. Note that "short input"
  alone is **not** the criterion: on a 161-byte string with six backtracking-shaped
  patterns this library is 24× faster and allocation-free.
- **You are compiling somebody else's pattern** — user- or config-supplied — and the
  accepted syntax must match stdlib byte for byte. The two engines disagree at the
  edges (`\C`, nesting-depth limit, a few escapes; see
  [Differences](#differences-from-stdlib-regexp)). That is a semantic contract, not a
  performance question.

One more thing that is not about speed: sharing a single `*Regexp` across goroutines
does not scale linearly here (every search takes a read lock on the DFA state cache),
so on **tiny inputs under heavy concurrency** the two engines come out even. On
body-sized input the lock is irrelevant and this library is still ~100× ahead at 1000
goroutines. See [Concurrency](#concurrency-sharing-one-regexp-is-fine-it-just-doesnt-scale-linearly).
- **the pattern can match the empty string and you `FindAll` over a whole document** — e.g.
  `(?m)^[ \t]*$` or `(?m)^\s*(?://.*)?$`. An empty-matchable pattern succeeds on every line,
  so `FindAll` is forced to emit one match per line and pay the advance-and-restart path for
  each; on 115 KiB of line-shaped text that is **0.23x** (10.4 ms vs 2.4 ms). This is a *pattern
  shape* problem, not an engine problem: making the same intent non-empty-matchable (`*` → `+`)
  flips it to **8.45x** in this library's favour, and is faster under the standard library too.
  Try rewriting the pattern before you settle for the standard library here.
  Numbers: `go test -run TestEmptyWidthMultiline -v .`


Beyond speed, this library also avoids the costs of the usual ways to get native RE2
into Go:

- **No wazero / WASM runtime.** Wrappers like `go-re2` run RE2 inside a wazero
  WebAssembly runtime, which probes stdio handles at startup. In environments
  with no standard handles (e.g. a Windows SCM service) that probing can fail.
  hgmLibre2 links RE2 natively, so there is no runtime to instantiate.
- **No abseil / CMake.** The vendored RE2 is the last pre-abseil release
  (tag `2023-03-01`), which is plain self-contained C++11. cgo compiles the
  `.cc` files directly; there is no separate build system to drive.
- **Single static binary, cross-compilable.** Because it is just C++11 + cgo,
  it cross-compiles with [zig](https://ziglang.org) as the C/C++ toolchain.

The one hard requirement is cgo: it must stay enabled, and a C++11 compiler (clang,
gcc or `zig c++`) must be available. A pure-Go / `CGO_ENABLED=0` build cannot use
this library at all.

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

- `Compile`, `MustCompile`, `QuoteMeta`
- `CompileMaxMem` / `GetMaxMem` — *not* in stdlib; the RE2 memory budget for **one** pattern
  (`Compile` uses RE2's 8 MB default); see [Scanning backwards](#scanning-backwards) for when it matters
- `String`, `NumSubexp`, `SubexpNames`
- `MatchString`
- `FindString`, `FindStringIndex`, `FindStringSubmatch`, `FindStringSubmatchIndex`
- `FindAllString`, `FindAllStringIndex`, `FindAllStringSubmatch`, `FindAllStringSubmatchIndex`
- `ReplaceAllString` (repl is **literal** — no `$1` / `${name}` / `$$` expansion, unlike stdlib), `ReplaceAllStringFunc`
- `Split`
- The `[]byte` counterparts of all of the above: `Match`, `Find`, `FindIndex`,
  `FindSubmatch`, `FindSubmatchIndex`, `FindAll`, `FindAllIndex`,
  `FindAllSubmatch`, `FindAllSubmatchIndex`, `ReplaceAll`, `ReplaceAllFunc` —
  see [Byte-slice methods](#byte-slice-methods) below
- `FindReplaceWithin` / `FindReplaceWithinBytes` / `AppendFindReplaceWithin` — *not* in
  stdlib; replace inside every match of another pattern in one C-side pass; see
  [FindReplaceWithin](#findreplacewithin) and
  [AppendFindReplaceWithin](#appendfindreplacewithin) below
- `RegexpSet` (`NewRegexpSet`, `NewRegexpSetMaxMem`, `GetPatternLen`, `Match`, `MatchAny`,
  `MatchBytes`, `MatchAnyBytes`) — *not* in stdlib; one DFA answering "which of these N patterns
  hit" in a single scan; `MatchAny` drops the indices and returns at the **first** hit position
  instead of scanning to the end; see [RegexpSet](#regexpset) below
- `RegexpSet.MatchStats` / `MatchStatsBytes` (`ScanStats`) and `RegexpSet.GetMemInfo` (`SetMemInfo`)
  — *not* in stdlib; **per-scan** and **per-Set** DFA counters (flushes, states built, budget
  left); see [Measuring a Set](#measuring-a-set) below
- `RegexpSet.Attrib` (`AttribInfo`, `PatternCost`) — *not* in stdlib; a diagnostic build answers
  *which patterns* are building all those DFA states; see [Attribution](#attribution-which-patterns-build-the-states)
- `RegexpSet.FindAllIndex` / `FindAllIndexBytes` (`NewFindAllIndexAlloc`,
  `RegexpSet_FindAllIndex_Alloc_t`, `RegexpSet_FindAllIndex_Run_t`) — *not* in stdlib;
  one scan reporting **where** each pattern of the set can end, handed back in batches;
  see [FindAllIndex](#findallindex-the-raw-end-point-runs) below
- `RegexpSet.NewMatchScanner` (`MatchScanner`, `SetMatch`, `Scan`, `SetModes`,
  `MatchScanMode_t`, `IsHit`,
  `GetHitIDs`, `GetStats`, `MatchScanStats_t`, `Close`) — *not* in stdlib; the finished form of the
  above: one pass giving the hit table **and** de-duplicated match spans, replacing
  `Match` + one `FindAllStringIndex` per hit pattern;
  see [Where a set matched](#where-a-set-matched-matchscanner) below
- `RegexpSetReverse.NewMatchScanner` (`MatchScannerReverse`, same method set) — *not* in
  stdlib; the same thing from the other end: one backwards pass giving de-duplicated
  spans under **rightmost-longest**, for tables whose patterns are cheap in reverse and
  explosive forwards; see
  [Where a reverse set matched](#where-a-reverse-set-matched-matchscannerreverse) below
- `RegexpSet.ResolveSpan` / `ResolveSpanWithin` / `ResolveSpanBytes` (the same three on
  `RegexpSetReverse`, in the opposite direction) — *not* in stdlib; complete one end
  point into a whole span with a single anchored question whose cost does not depend on
  input length; see [ResolveSpan](#resolvespan-complete-one-end-point-into-a-span) below
- `GetPatternLenRange` / `RegexpSet.GetPatternLenRange` / `PatLenUnbounded` — *not* in stdlib;
  the byte-length range a pattern can match; see [GetPatternLenRange](#getpatternlenrange) below
- `RegexpSet.GetViableOneStats` — *not* in stdlib; how much the lazily built per-pattern
  reverse sets (used to recover left edges) cost in states and bytes
- `CompileLongest` / `CompileLongestMaxMem` / `MustCompileLongest` — *not* in stdlib as
  constructors (the stdlib spelling is the `re.Longest()` mutator); compile one pattern
  under **leftmost-longest (POSIX)** semantics instead of the default leftmost-first.
  Both pick the *same start*; they differ only on the end. Needed whenever you want
  "the longest match starting here" in a single search instead of two, and required if
  you feed the span into a checksum — a greedy short end truncates the hit
- `Regexp.FindStringIndexAtWithin` (and the same method on `FindStringIndex_ctx_t`) —
  *not* in stdlib; a search **anchored at `from`** and not crossing `bound`, with
  offsets in the original string so `\b` / `^` / `$` still see the real neighbours.
  Paired with a longest-mode object this is `ResolveSpan` for a single pattern — but
  over `RE2::Match`, so it inherits the NFA fallback that the set path does not have
- `RegexpReverse.ResolveSpanWithin` — *not* in stdlib; the single-pattern twin of
  `RegexpSetReverse.ResolveSpanWithin`: given a match end, the leftmost start, bounded.
  Implemented as the very call `RE2::Match` makes internally to find a match's left
  edge (reverse program + `kAnchored` + `kLongestMatch`)
- `RegexpReverse.GetMemInfo` — *not* in stdlib; the DFA cache high-water mark of that
  pattern's reverse program, `Built=false` if it has never been walked (querying never builds it)
- `GetDFAStats` / `ResetDFAStats` (`DFAStats_t`) — *not* in stdlib; process-wide counters for DFA
  state-cache flushes; the per-Set counters above are usually what you want instead;
  see [DFA cache thrashing](#dfa-cache-thrashing) below
- `FindStringIndex_ctx_t` (`NewFindStringIndex_ctx`, `FindStringIndex`, `FindIndex`) — *not* in stdlib;
  a scratch-reusing `FindStringIndex` that is steady-state allocation-free
- `AppendAllStringIndexFlat` — *not* in stdlib; `FindAllStringIndex` without the intermediate
  `[][]int`; see [AppendAllStringIndexFlat](#appendallstringindexflat) below
- `ReplaceAllStringFunc_ctx_t` (`NewReplaceAllStringFunc_ctx`, `AppendReplaceAllStringFunc`) —
  *not* in stdlib; `ReplaceAllStringFunc` appending into a caller-owned `[]byte`, steady-state
  allocation-free, reporting *changed* rather than *matched*;
  see [AppendReplaceAllStringFunc](#appendreplaceallstringfunc) below
- `RegexpReverse` (`CompileReverse`, `CompileReverseMaxMem`, `MustCompileReverse`, `MatchString`,
  `Match`, `MatchStats`) — *not* in stdlib; a **separate object** that gives the same yes/no
  answer as a forward `Regexp`, with the DFA walking the **original buffer back to front**;
  see [Scanning backwards](#scanning-backwards) below
- `RegexpSetReverse` (`NewRegexpSetReverseMaxMem`, `GetPatternLen`, `Match`, `MatchBytes`,
  `MatchAny`, `MatchAnyBytes`, `MatchStats`, `GetMemInfo`, `GetAttrib`, plus the `FindAllIndex`
  and `ResolveSpan` families above) — *not* in stdlib; the reverse-compiled twin of
  `RegexpSet`, and a **separate type**: it used to be a `*RegexpSet` carrying a
  `Reverse() bool`, which made two opposite meanings share one method name;
  see [Scanning backwards](#scanning-backwards) below
- `FreeC` — *not* in stdlib; see [Resource management](#resource-management)

### Byte-slice methods

The `[]byte` methods are a second facade over the **same** matching core, not a
separate implementation. Matching only ever needs a byte pointer plus a length
on the C side, so a `[]byte` is handed to RE2 directly: **no `string(b)` copy at
any input size**, and results (`Find`, `FindSubmatch`, `FindAll`, …) are
sub-slices of your input sharing its backing array, exactly like stdlib's
`bytes` family. Two consequences worth knowing:

- The input must not be mutated while a call is in flight (same rule as stdlib).
- On the **no-change path**, `ReplaceAll` / `ReplaceAllFunc` /
  `FindReplaceWithinBytes` return the original `src` slice with zero allocation
  (matching this library's lazy-materialization style) rather than a fresh copy
  as stdlib does — so do not write to the returned slice. Copy it if you need an
  independently writable buffer. Everything else, including the `nil`-vs-empty
  result conventions, matches stdlib and is pinned by differential tests in
  `bytes_test.go`.

Naming: where stdlib has the method, the stdlib name is used (`FindIndex` ↔
`FindStringIndex`); this library's own methods take a `Bytes` suffix
(`FindReplaceWithinBytes`, `RegexpSet.MatchBytes`).

### RegexpSet

`RegexpSet` compiles N patterns into **one** DFA and answers "which of these N
hit" in a single unanchored scan, instead of `(?:re1)|(?:re2)|…` as a coarse
pre-filter followed by a second per-pattern pass. `Match` returns the hit
indices (into the `patterns` slice you passed); it deliberately does **not**
return positions — callers needing an offset run `FindStringIndex` on the few
hit patterns afterwards.

```go
set, err := hgmLibre2.NewRegexpSet(patterns)         // RE2 default budget: 8 MB
set, err := hgmLibre2.NewRegexpSetMaxMem(patterns, 32<<20) // explicit budget

var buf []int32                       // reuse across calls → zero alloc
for _, text := range corpus {
    for _, idx := range set.Match(text, buf) {
        ...                           // idx indexes `patterns`
    }
}
```

#### Sizing `maxMem`

`maxMem` is RE2's `RE2::Options::max_mem`. `Prog::CompileSet` splits it once,
so the single knob raises two different ceilings:

- **Compile time** — an instruction-count ceiling for the whole set
  (`(maxMem-sizeof(Prog))/4/sizeof(Inst)`, capped at 2²⁴). Too many patterns, or
  patterns that are individually heavy (case-folded classes, `{n,m}` repeats),
  overrun it and `RE2::Set::Compile()` fails. That is the **only** thing that
  makes `NewRegexpSet`/`NewRegexpSetMaxMem` return an out-of-memory error, and
  the error text carries the pattern count and the budget in force.
- **Run time** — whatever is left funds the DFA's state cache. Overrunning
  *this* is not an error: the DFA flushes its cache and keeps going with the
  same result. It is, however, a **cliff and not a slope** — see
  [DFA cache thrashing](#dfa-cache-thrashing) — and it is invisible unless you
  count the flushes (`set.GetMemInfo().FlushesTotal`, or `GetDFAStats` process-wide;
  see [Measuring a Set](#measuring-a-set)).
  See also [Why `Match` has no error return](#why-match-has-no-error-return).

So: if you got "set compile failed (out of memory)", **do not split the table
into two sets** — two sets means two scans over every input, which costs far
more than the memory. Double `maxMem` until it compiles (8 → 16 → 32 MB). It is
a build-time, once-per-process cost, and the set is read-only and concurrency-safe
afterwards. Capacity scales roughly linearly with the budget: on one real-world
509-pattern table, 1 MB held 132 of them, 2 MB held 204, and 4 MB held all 509.

⚠️ "It compiles" is **not** the same as "the budget is big enough": that only
clears the compile-time ceiling. Whether the run-time half is big enough is a
separate question with a separate answer, and the only way to know is to count
flushes — see below. And once a set is genuinely thrashing, splitting it *does*
become the better trade (two warm scans beat one thrashing scan by a wide
margin), so the "do not split" advice above applies to the compile-time ceiling
only.

#### DFA cache thrashing

When the DFA's state cache cannot hold the state set the current input is
walking, RE2 does **not** evict LRU-style: `DFA::ResetCache` throws the whole
cache away and rebuilds from scratch. The answer stays correct, so nothing in
the API changes — which is exactly why this can quietly cost you an order of
magnitude for a long time.

Two properties make it worth counting rather than reasoning about:

- **It is a cliff, not a slope.** A working set 1% over budget does not cost 1%.
  Every flush is followed by rebuilding thousands of states, each one an NFA
  closure computation. Synthetic `kwN[\s\S]{0,8}tgtN` set, 60 patterns, 16
  distinct 64 KB bodies (the helpers in `dfastats_test.go`), on one box:

  | `maxMem` | flushes | throughput |
  |---------:|--------:|-----------:|
  |     1 MB |      79 |   8.1 MB/s |
  |     8 MB |       3 |  18.6 MB/s |
  |    64 MB |       0 | 164.7 MB/s |

  Three flushes over a megabyte of input are worth a 9× slowdown.
- **A single-shape benchmark cannot see it.** Scanning *the same* body N times
  warms the cache on the first pass and never allocates a state again, so every
  budget looks identical. Production scans a *different* body per request, and
  each new shape pushes the state set outward. Benchmark with a rotation of
  distinct bodies, and look at flush counts, not just at throughput.

`GetDFAStats()` returns a snapshot of process-wide counters; `ResetDFAStats()`
zeroes them for segmented measurement:

```go
hgmLibre2.ResetDFAStats()
for _, body := range distinctBodies {   // single-threaded, one warm-up pass first
    set.Match(body, buf)
}
st := hgmLibre2.GetDFAStats()
// st.Resets == 0  ⇒  this budget holds this pattern table on this corpus.
// st.Resets  > 0  ⇒  double maxMem, rebuild the set, measure again.
// st.LastStateBudget / st.LastCacheStates report the budget and the state count
// in force at the most recent flush — the working set's lower bound.
```

`SearchFailures` counts the other failure mode, where the DFA gives up on a
search entirely; a lone `Regexp` falls back to the NFA (correct, an order of
magnitude slower), while `RE2::Set` never gets there (see below).

The counters are process-wide: RE2's hook carries no context, and set matching
has none at all, so a delta only attributes to a specific scan if you measure
single-threaded. Under concurrency, read them as a rate. For anything finer,
use the per-Set / per-scan counters below.

#### The flush is not the whole cliff

Counting flushes is how you find out whether the budget is big enough, but once
it *is* big enough the cost does not disappear — it moves. Measured per call on
one 223-pattern table over 32 distinct 80 KB bodies, splitting the calls into
"this call hit a flush" and "this call did not":

- A call that hits a flush is only **1.3–2.8×** slower than one that does not.
  The flush itself is not an order of magnitude.
- The order of magnitude is in **building states**. At a budget with zero
  flushes: first pass over brand-new text, 23.7 ms per body; second pass over
  the *same* bodies (not a single new state), 0.36 ms per body. That is **66×**,
  with no flush anywhere in sight.

So the cache only ever pays off on **repeated** text. Novel text keeps asking
for new states and does not converge: scanning 1657 distinct real records, the
new-states-per-KB rate fell from 84.7 to 16.2 and then stopped falling.

Practical consequence: sizing `maxMem` to zero flushes buys you "do not fall off
the cliff" — it is necessary, and it is cheap. It does not buy the 66×. If every
request carries fresh text, the first-pass number is your ceiling, and getting
under it means making the *table* build fewer states (see
[What drives state explosion](#what-drives-state-explosion)) or scanning less
text (a cheap literal pre-filter in front of the big set).

#### Measuring a Set

`GetDFAStats` is process-wide. Two finer counters attribute to one `RegexpSet` and
to one call, with no globals and no `thread_local` — so they work under
concurrency and in a process holding many sets.

```go
// Per scan: pass a stack-allocated ScanStats. Passing nil (i.e. using Match)
// costs nothing — the C side does not count at all.
var st hgmLibre2.ScanStats
hits := set.MatchStats(text, buf, &st)
// st.Flushes     whole-table cache wipes during THIS call (>0 = part of this
//                call ran two orders of magnitude slow, and it took a write
//                lock that stalled every other goroutine scanning this set)
// st.Grows       cache growth events — these lose NO states; not thrashing
// st.StatesBuilt states created during this call (→ 0 in steady state)
// st.Bytes       input size; Bytes/StatesBuilt = bytes of text per new state
// st.StatesEnd, st.StateBudget, st.MemLeft   water level at the end

// Per set: cumulative, read-only, never builds a DFA just to answer.
mi := set.GetMemInfo()
// mi.FlushesTotal  == 0 after a run over distinct bodies ⇒ this budget fits
// mi.StatesBuiltTotal  the direct "how expensive is this table" number
// mi.States, mi.GetUsedBytes(), mi.StateBudget, mi.ArenaCap
```

`mi.ArenaCap` is the memory actually obtained from the system, as opposed to
`StateBudget`, which is only a ceiling. It is normally far below the budget:
raising `maxMem` does not, by itself, cost resident memory.

Caveat on `StatesBuilt`: it is the delta of a counter on the shared DFA, so a
concurrent scan of the same set is counted in too. Attribute single-threaded.

#### What drives state explosion

Measured by rewriting one real 223-pattern table and rescanning the same corpus
at a budget with zero flushes, so the only variable is the pattern source:

- **Tolerance windows dominate, roughly linearly.** Narrowing every `{0,N}` in
  the table to `{0,2}` took 51844 states down to 4366 (**11.9×**), about +700
  states per unit of window width. An automaton has no cheap way to remember
  "how far in am I", so `X{0,W}Y` is expanded W times.
- **Shape splits the table cleanly.** The 84 patterns containing a `{0,N}` gap
  accounted for 76.8% of the states; the other 139 accounted for **3.4%**.
- **Combining patterns is super-additive, but only mildly.** Two halves scanned
  separately built 32573 states; merged into one set, 51844 — **1.59×**, not a
  product. Pattern *count* is the second-order term; window *width* is the first.
- **Input entropy is not a driver.** Random full-byte noise never enters a
  window and builds almost nothing. Minified JavaScript is the *cheapest* corpus
  measured: 87 KB of `jquery.min.js` built 947 states where 82 KB of real text
  built 3906. Per-offset attribution shows why — the four hottest 1 KB segments
  of that file are the only natural English in it (the licence comment, an
  exception string, a MIME-type string, un-minified property names).
- **Character-class-driven branches are the trap.** A branch like
  `(?-i:\([A-Z]{2,5}\))` has no literal anchor, so its cost tracks whatever
  alphabet the corpus happens to use — it was the one pattern that cost *more*
  on minified JS than on real text. The same missing literal anchor is what
  makes such a pattern unfilterable. Give every branch a real literal.
- **Narrowing windows does not make a state cheaper.** State "width" (how many
  NFA threads are live at once) is the price of building one state. Narrowing
  the whole table to `{0,2}` cut the state *count* 11.9× while average width
  moved 37.3 → 35.7 (**4%**). Window width controls how *many* states exist,
  not what one costs.

#### Attribution: which patterns build the states

Not compiled in by default. Rebuild with the macro and the `GetAttrib` accessor
starts returning data (without it, `Enabled` is false and everything is zero —
there are no fields and no branches in the default build):

```sh
CGO_CXXFLAGS="-O2 -DRE2_DFA_ATTRIB=1" go build ./...
```

```go
a := set.GetAttrib()
for _, p := range a.Pats[:20] {   // already sorted, most expensive first
    fmt.Println(p.Index, p.Excess)   // p.Index indexes your patterns slice
}
// a.NInstHist / a.NInstMax  distribution of state width = per-state build cost
// a.BirthHist[64]           states built per 1/64 of the input; flat = the
//                           whole text is building states, spiky = go read
//                           those offsets and you will see what triggers it
```

Sort by `Excess` (`= Insts - States`), which is what `Pats` is already sorted
by. Do **not** sort by `States`: under an unanchored search the DFA must
consider a match starting at every position, so every pattern's entry
instruction sits in nearly every state and `States` saturates at 100% for most
of the table.

The ranking has been validated by ablation: dropping the top 20 by `Excess` took
one table from 51844 states to 9606 (**5.4×**), where dropping 20 patterns at
random only reached 36810 (1.4×); top 39 gave 11.5× against 1.03× for 39 random.
It is also stable across corpora — three corpora of completely different shape
agreed on 10–11 of the top 12 — so one calibration run on any real corpus is
enough. Absolute state counts are *not* portable that way; they move 4–8× with
the corpus.

Ablation is a *diagnostic*, not a deployment: removing patterns changes what you
detect. Use the ranking to decide which few patterns to rewrite, or which few to
isolate into their own set — and if you do split, split by that ranking or by
shape, never round-robin by index. Splitting barely reduces the *total* state
count (51844 → 44573 at k=8); what it reduces is the *largest* shard, which is
what the budget has to cover. A round-robin k=8 split measured 3× *slower* than
not splitting at all, because the input is scanned eight times.

#### Why `Match` has no error return

`RE2::Set::Match` reports DFA failure by returning `false` — indistinguishable
from "nothing matched" unless you ask for its `ErrorInfo`. That would be a
silent-miss hazard for a detector, so it is worth being precise about when it
can actually happen: **once `Compile()` has succeeded, a DFA out-of-memory on
the match path is not reachable**, which is why `Match` and friends return no
error.

Two facts in the vendored RE2 make that hold:

- The DFA's "cache thrashing, bail out to the NFA" branch is explicitly disabled
  for set matching — `re2_dfa.cc` guards it with `kind_ != Prog::kManyMatch`
  ("*RE2::Set cannot fall back, so we just have to keep on keeping on*"). A set
  scan therefore flushes and rebuilds its state cache indefinitely rather than
  failing. The remaining failure paths need a *single* state not to fit in a
  freshly flushed cache, and the DFA constructor already refuses to initialize
  unless the budget holds at least 20 states.
- That constructor check is exercised at **compile** time, not first use:
  `Compiler::CompileSet` finishes by running a `"hello, world"` DFA search
  (`Prog::kManyMatch`) and returns `NULL` if it fails, precisely because a set
  has no NFA to fall back on. A too-small budget therefore fails `Compile()`,
  which you get as an error from the constructor.

Verified empirically as well as by reading the source: the 509-pattern corpus
above, compiled at its **minimum** viable budget (3.71 MB, i.e. the least
runtime cache RE2 will accept for it), scanned against adversarial inputs
(random full-byte, random ASCII, near-miss fragments, byte-cycle, CJK, single-
character runs; 256 KB–8 MB each, with and without needles appended). One 8 MB
scan alone forced 5 679 state-cache flushes. Across every run: zero
`kOutOfMemory` from `ErrorInfo`, zero hits from RE2's own
`hooks::DFASearchFailure`, and the hit set matched a per-pattern `MatchString`
oracle exactly, every time. The same DFA-failure hook *does* fire, as expected,
for single-`Regexp` matching under a starved budget — and there it is harmless,
because a lone `RE2` falls back to the NFA and still returns the correct answer.

### Where a set matched: `MatchScanner`

`RegexpSet.Match` answers *which* of the N patterns hit. Learning *where* they hit
used to cost a second pass per hit pattern: `Match` once to narrow the table down,
then `FindAllStringIndex` on a forward `Regexp` for each of the k patterns that
matched — `1 + k` passes over the whole input, and those k are the expensive
**unanchored** kind (the `.*?` prefix makes *every* offset a candidate start, which
is the state-explosion fuse described in
[What drives state explosion](#what-drives-state-explosion)).

Those positions were already computed by the first pass and then thrown away: RE2's
`kManyMatch` DFA notes every byte at which some pattern can end, and drops the note
when the scan ends. This library keeps it. On a 6.4 MB body, `Match` alone costs
18.5 ms and `Match` *plus* collecting every end point costs 18.4 ms — within noise,
so the positions are free.

`MatchScanner` is the finished form of that: **one** pass over the input, giving both
the hit table and de-duplicated match spans, in batches, with a fixed memory
footprint.

```go
ms, unsupported, err := set.NewMatchScanner()  // reusable workspace: build once, keep it, Close it
defer ms.Close()
// unsupported: pattern indices that cannot produce spans at all (today: they match the
// empty string). Fixed at construction, independent of any input — settle them here.

ms.SetModes(modes)                // optional: what each pattern needs (default: spans, auto-tiered)

err = ms.Scan(body, func(batch []hgmLibre2.SetMatch) {
    for _, m := range batch {     // body[m.Lo:m.Hi] is a real match of pattern m.Index
        handle(m.Index, body[m.Lo:m.Hi])
    }
})                                // err != nil ⟹ the whole pass is void; redo it with FindAll
ids := ms.GetHitIDs()                // the same hit table Set.Match would have returned
// ms.IsHit(i) is the O(1) form of the same answer
st := ms.GetStats()                  // Walks / Cands / Tries / Emits for that pass
// st.Tries/st.Walks is the number to watch: 1.00 means the first candidate start was
// always the answer. Measured 1.00 on every production table. The first three counters
// cover variable-length patterns only (fixed-length ones never look back); Emits counts
// every span, so Tries/Emits is NOT "tries per look-back".
```

**What it buys.** Two measurements, on **different tables and corpora** — do not
compare them across.

On a production-shaped set (90 patterns, 52 of them wanting spans, 7.03 MB), with all
three modes measured in the same run:

| mode | whole leg | vs. the old path |
|---|---|---|
| old path (`Match`, then `FindAll` over the whole body per hit pattern) | 78.2 ms | 1.00× |
| **`span`** (default, leftmost-longest) | 43.8 ms | **1.79×** |
| **`spanFast`** (removed 2026-08-28) | 24.6 ms | **3.18×** |
| gate only (everything `boolOnly`) | 21.9 ms | 3.57× |

All three hand back the identical 10 956 spans here, because every pattern in that
table is anchored at both ends.

On a real 155-pattern table over a 6.4 MB body (47 patterns hitting, steady state,
64 MB budget) the whole leg drops from **369.3 ms** (`Match` + per-pattern `FindAll`)
to **24.6 ms** — **15.0×** — and Go-heap allocation from 4.0 MB / 2252 objects to
~0 / 146. By input size on that corpus: ≤ 8 KB break-even (1.0×), 32 KB 3.1×,
512 KB 6.1×, 2 MB 14×. The worst case measured is **0.94×** (6% slower): a synthetic
string with a hit every 38 bytes, where nearly every byte is inside a match and the
two cgo crossings per hit have nothing to amortise against.
🔴 Both sets of numbers predate the 2026-08-28 path change; they answer a different
question — *how much better is this layer than not having it* (versus the old "gate `Match`
plus one `FindAll` per hit pattern") — and that ratio only improved. Post-change numbers are
in `doc/补起点换路的实测账_20260828.txt`.

Rules that matter, all pinned by tests (`matchscan_test.go`, `spanscan_*_test.go`):

- **The batch slice is the internal buffer itself**, overwritten in place by the
  next batch. Keep what you need by appending it elsewhere. This is why the API is
  batched and not "give me one array at the end": run counts scale with the input
  (~30 741 runs/MB on that table), so accumulating would make memory track document
  size. `Scan` itself keeps a fixed 12 KB of output buffer plus the 48 KB run buffer
  underneath, whatever the input length.
- **Results are interleaved across patterns**, ordered by when a span closes.
  Within one pattern they are ascending by `Lo` and non-overlapping. Grouping by
  pattern is one `append` on your side; doing it in the library would mean
  accumulating, which is exactly what the batching avoids.
- **Passing `nil` as `batchFn` is legal** — you then get only the hit table, i.e.
  `Set.Match` semantics, with no span work done at all.
- **The workspace is not concurrency-safe**: one `MatchScanner` per goroutine. The
  `RegexpSet` behind it is read-only and shared freely, so several scanners can run
  against one set concurrently.
- **`text` is only referenced during `Scan`** (the left edge is recovered by reading
  it); the scanner does not retain it afterwards.

#### `SetModes`: what each pattern needs

`ms.SetModes(modes)` declares, per pattern index, one of two things (indices beyond
a short slice take the zero value; `nil` = all default):

| mode | what it means |
|---|---|
| `MatchScanMode_span` (zero value) | give me spans. **Leftmost-longest, unconditionally** — no pattern shape escapes it. |
| `MatchScanMode_boolOnly` | I only need "did it hit". No span is ever closed, no endpoint recovered. |

🔴 Until 2026-08-28 there was a third mode, `MatchScanMode_spanFast`, forcing a cheap
cursor path that did **not** guarantee leftmost-longest and required the caller to fuzz
out a per-pattern clearance first. It is **gone**. The path that replaced it is both
strictly leftmost-longest *and* cheaper than `spanFast` was, so the trade it offered no
longer exists — see *How the three paths became one* below.

`boolOnly` is the one that pays: patterns left out of span work still appear in the
hit table, they just never pay for endpoint recovery, which is the expensive half.
Many entries in a big table are pure booleans ("does this class of content appear at
all") and nobody ever asks where they hit; on the table above, two such patterns
alone accounted for **57%** of all runs. This is static information — you know at
build time which branches consult a position — so set it once, right after building
the scanner.

It is one three-state table rather than two masks ("wants a span" × "which path")
because *wants-no-span-but-use-path-B* is not a meaningful combination; two masks
would eventually contradict each other.

`SetModes` returns an error if a pattern that can match the empty string is given
anything other than `boolOnly` — every offset would be a zero-length hit and the
cursor cannot advance past it. That is rejected up front rather than degrading
silently at scan time.

#### What `Scan` guarantees

Long-form, in Chinese:
[`doc/MatchScanner的leftmost-longest保证.md`](doc/MatchScanner%E7%9A%84leftmost-longest%E4%BF%9D%E8%AF%81.md).

In the default mode (`MatchScanMode_span`) the spans handed to you satisfy:

1. `text[Lo:Hi]` is a **real** match of that pattern;
2. spans **of one pattern** are non-overlapping and ascending by `Lo`;
3. the semantics are **leftmost-longest** — i.e. `re.Longest().FindAllStringIndex`.

Three things are easy to misread there:

- (2) is per pattern. Two *different* patterns still overlap freely; that is not
  duplication, it is two questions each wanting an answer.
- (3) is **not** "same as `FindAllStringIndex`". The stdlib default is
  leftmost-*first* (greedy); the two disagree wherever the greedy first hit at a
  start is shorter than the longest one. Compare against `Longest()`, or you get a
  false red.
- Empty-capable patterns are rejected by `SetModes`, not silently degraded.

How the endpoint is recovered, per pattern:

| pattern shape | mode | how |
|---|---|---|
| `min == max` (fixed length) | any | `Lo = Hi - min`, one subtraction, the regex engine is never entered. Both paths agree here, so the mode does not apply. |
| variable | `span` (default) | **two steps.** ① A reverse pass from the end `e` with **every live state seeded**, walking left until the machine dies, collecting *all* viable-prefix starts in `[cursor, e)` (`RegexpSetReverse.GetViableStarts`, on a reverse set holding **just that one pattern**). ② Try those candidates **in ascending order** with an anchored longest forward search; the first one that verifies is the answer. Ascending ⟹ leftmost; longest-mode ⟹ longest end. Strictly leftmost-longest for any pattern shape, and **no `maxLen` is needed** — the look-back's lower bound is wherever the reverse machine died. Cost: the look-back windows are pairwise **disjoint** (bounded by one extra pass over the text) plus however many false candidates get verified — measured `1.00` tries per look-back on every production table. |
| — | — | if the single-pattern object will not compile, `maxMem` is too small and `Scan` fails the whole pass. |

**One rule governs all of it (2026-08-27): the pass over the text uses the set; every
endpoint-completion call after it uses that pattern's own single-pattern object, never
the set.** Three reasons:

1. A single-pattern object goes through `RE2::Match`, which falls back DFA →
   OnePass/BitState/NFA. The set's anchored resolve is a `kManyMatch` DFA **and
   nothing else** — upstream included (`re2_set.cc:216`: `dfa_failed` ⟹ `return
   false`) — so "the DFA gave up" there can only fail the whole pass. After the
   change only the reverse look-back can still hit that, because RE2 itself has no
   other way to find a match's left edge.
2. Endpoint traffic no longer churns the big shared DFA cache of the whole table.
3. Smaller states: a single pattern does not carry `kManyMatch`'s per-state id list.

The answers were byte-for-byte unchanged (`TestMatchScanPathsSameAsSetRoute`: 300
AST-generated corpora per path, 54 000 spans, zero differences), and dense-hit corpora
got 27–36% cheaper. 🔴 That test was removed on 2026-08-28 along with the two paths it
replayed; the same AST-generated corpus is now pinned in `matchscan_astfuzz_test.go`
against stdlib's `Longest()`, which is a harder oracle than a replica of our own old
code.

#### How the three paths became one (2026-08-28)

For the fixed-length tier the equality is provable, not merely measured: an end `e`
has exactly one possible start `e-min`, so starts are monotone in ends and "greedy
leftmost non-overlapping" is precisely what the cursor produces. It is cross-checked
against `FindAllStringIndex` on 60 000 random fixed-length patterns
(`TestMatchScanStrictVsFindAll`).

For the variable-length tier the pairing has to be **rebuilt** on the Go side, and
*how* you rebuild it decides what semantics you get. This scan yields the *set of end
offsets*, with no start/end pairing and no priority information in it — RE2's
greediness lives in the NFA instruction priority order, which `kManyMatch` (the only
mode that reports *all* ends) discards. Until 2026-08-28 three rebuilds coexisted:

| | how | semantics | expensive because |
|---|---|---|---|
| **path A** (`spanFast`) | reverse machine seeded with **accept only** → one start, then an anchored longest end | **a third kind** — neither leftmost-first nor leftmost-longest | two calls per hit, look-back windows **overlap** |
| **path B** (old default) | one forward **unanchored** longest search from `max(cursor, e-maxLen)` | leftmost-longest | patterns with no length bound must **walk the gaps** (up to 2.00× the text) |
| **path D2** (separate type `MatchScanner2`) | what the table above now describes | leftmost-longest | verifying false candidates (never happens on real tables) |

Path A's defect is structural: seeding accept only sees the starts where a match ends
*exactly* at `e`. `\b(?:ab cd ef|cd)\b` against `"ab cd ef"` — the smallest end the set
reports is the one for `"cd"` (offset 5), so the look-back can only reach 3, while the
real leftmost start is 0 (`text[0:5)="ab cd"` is *not* a match, but it **is** a viable
prefix: append `" ef"` and it becomes one). Only seeding every live state sees that
candidate, which is exactly what step ① above does.

**The evidence for collapsing them** — 11 corpora at the 100 MB scale (console build
output · eight credential-dense generators · product source + manuals + endpoint ELF · a real
local Claude history) × 9 **production** gate tables = 99 cells. Raw reports in
`doc/补起点换路的实测账_20260828.txt`.

- **Semantics.** Compared span by span, after sorting into a canonical `(pattern, Lo, Hi)`
  order: D2 vs path B — **zero** differences across **161.9 million** spans. D2 vs path A —
  **37** differences, all in the source/manual/ELF corpus, and every one of them is path A
  **truncating the left edge**: a UAE Emirates ID came out `1985-1234567-1` where the real
  answer is `784-1985-1234567-1`; a prompt-injection marker came out `<SYS>` where the real
  answer is `<<SYS>>`. 🔴 That is precisely the failure mode this document keeps warning
  about — feed a shifted span to a check digit (mod-10 / Luhn / mod-97) and it fails, so the
  consumer discards a real hit and you get a **silent miss**. Switching paths did not just
  save time; it **fixed 37 wrong boundaries on real text**.
  🔴 Do **not** compare in emission order: the guarantees only cover ordering *within* one
  pattern, so a stream comparison measures "different permutation", not "different spans"
  (it flagged all 85 000 spans of an 8 MB corpus as mismatched; sorted, zero differed).
- **Cost.** Summed over each gate chain, D2 was the fastest in all 11 — `0.48–0.91×` path A,
  `0.6–1.0×` path B — and `Tries/Walks` was `1.00` in all 99 cells.
- **Memory.** D2 needs a per-pattern *reverse set* (`vp1`); path A needed a per-pattern
  *reverse object* (`rev1`). Same order of magnitude: on the largest table (158 patterns,
  89 of them actually asked for a position) 9.6 MB vs 7.6 MB. Against path B it is a **net
  add**, since B built no reverse object at all. Measure it with `GetViableOneStats()`.

Removed along with the two paths: `MatchScanMode_spanFast`, the guard rejecting it in
`MatchScannerReverse.SetModes`, the `MatchScanner2` type, `RegexpSet.reverseOne` /
`ReverseOneStats` / `rev1`, and the tests that existed only to compare paths.

**Anchoring used to remove path A's problem entirely** — wrapping a variable pattern in
word boundaries (`\b(?:…)\b`) pins the start, so "which start to pick" never arose: in a
120 000-span comparison the 60 differing spans of the bare patterns became **0**. That is
now a property of the patterns rather than a requirement on the caller: fixed-length spans
were always safe to slice with, and variable-length spans are too, since they are strictly
leftmost-longest.

#### "I could not give you everything": exactly two ways to say it

🔴 There is no "I bailed halfway through, these patterns are yours to finish" middle
state (it was removed on 2026-08-27). Only two:

| where | what it says |
|---|---|
| `NewMatchScanner`'s `unsupported` | `[]int32` — pattern indices that cannot produce spans at all. Today there is exactly one reason: the pattern matches the empty string (`GetPatternLenRange`'s `min <= 0`), so every position is a zero-length hit the cursor cannot pin down. |
| `Scan`'s `err` | this pass is void — the batches you already received do not count either. Redo the whole input with `FindAll`. |

`unsupported` **does not depend on the input**: it is decided when the workspace is
built and never changes, so you can pin it in a regression test (put an `a*` in the
set and the index comes back). Configure those patterns as `boolOnly` — they still
appear in the hit table — or run them the old way. Asking for spans on one makes
`SetModes` fail immediately; if you never call `SetModes` they are handled as
`boolOnly`, since you were already told at construction.

`Scan`'s `err` has three causes, all of them "`maxMem` is too small": the underlying
`FindAllIndex` pass failed; one of the two single-pattern objects needed to fill in the
other end would not compile; the reverse look-back gave up.

🔴 "The reverse set would not compile" is deliberately **not** given a fallback. Measured
byte-exactly on 2026-08-28: of 590 production patterns only **16** cost more in reverse
than forward (all open-ended `{n,}` shapes, in one table), at a maximum ratio of **1.021×**.
For "the forward set compiles but the reverse single-pattern set does not" to actually
happen, the set must hold essentially **one** pattern *and* `maxMem` must land inside a
2%-wide band above that pattern's own threshold — for the worst pattern found (a three-part
JWT) that band is **74 bytes** wide: forward 3580, reverse 3654. On a table with several
patterns the forward set costs orders of magnitude more, so the window cannot exist.
Reaching this branch only ever means the caller misconfigured `maxMem` — which should be
reported, not silently papered over by quietly switching implementations. A fourth, "runs did not arrive
monotonically in scan order", is a **broken invariant inside this library** — a bug,
reported through the same `err` rather than swallowed.

**Why not partial success?** An error code the caller cannot construct has no business
being in a return value. It forces this chain: the caller must write a fallback → the
fallback never runs → what never runs cannot be tested → untested code is usually
wrong → the day it finally matters, execution goes down a path that has never
executed. That is worse than having no fallback and failing the whole pass.

And the anchored resolve giving up could not be provoked at all: it runs the *small*
DFA (a single start position, not the whole-text scan). Three shapes (`ab`,
`[A-Za-z][A-Za-z0-9]{2,19}key`, `(?i)[a-z0-9]{3,20}@[a-z0-9.\-]{3,20}`) were swept
every 100 bytes across the 3 000-byte band just above the wall where they first
compile (`maxMem` 2400 / 5800 / 24400), resolving every 3rd offset of a 60 KB text
with CJK in it — **zero** give-ups. Below the wall, `NewRegexpSetMaxMem` fails
cleanly instead. The program and the DFA are funded from the same `maxMem`: if the
program fits, what is left covers the handful of states one resolve walks; if it does
not, you never get a set. There is no window in between.

🔴 And no, it cannot "fall back to the NFA": on the set path there **is** no NFA.
A single `RE2` falls back (see the `Fall back to NFA below` chain in `re2_re2.cc`),
but `RE2::Set::Match` is DFA-only upstream too (`re2_set.cc:216` — `dfa_failed` just
returns false). The NFA interface does not answer *which* pattern matched, and the
`kManyMatch` ids come out of the id list inside a DFA state. Giving the set's anchored
resolve an NFA path means compiling a separate `\A(?:pat)` `RE2` per pattern — which
is the whole thing `ResolveSpan` exists to avoid.

🔴 There is deliberately **no** "give me one array at the end" entry point
(`AppendAllMatches` was removed on 2026-08-27). Such a convenience form always creeps
from tests and measurement into production, and its `dst` is a ratchet buffer that
tracks input length (~0.037 MB per MB of input on the table above) — exactly what the
batched interface exists to avoid. Callers who want an array append one line inside
their own `Scan` callback, where the cost is visible to whoever wrote it.

#### `FindAllIndex`: the raw end-point runs

`MatchScanner` is built on top of a lower layer, exported for callers who want to
apply their own pairing or overlap policy:

```go
alloc, _ := set.NewFindAllIndexAlloc()   // reusable workspace; not concurrency-safe
defer alloc.Close()

err := set.FindAllIndex(body, alloc, func(runs []hgmLibre2.RegexpSet_FindAllIndex_Run_t) {
    for _, r := range runs {
        // pattern r.ReIndex ends at every offset in r.Lo..r.Hi (both inclusive)
    }
})
```

- A run is `{ReIndex, Lo, Hi}`: pattern `ReIndex` has a match end point at **every**
  value in `Lo..Hi`, a **closed** interval in original-input byte offsets
  (`Lo <= Hi` always). Closed, not half-open, because these are collapsed *points*,
  not a span — `Hi` is itself a real end point.
- Which end is meant is fixed by the **direction of the set**, not by a field name:
  a forward `RegexpSet` reports match **ends** (exclusive, i.e. `text[?:Hi]` is a
  match), a `RegexpSetReverse` reports match **starts** (inclusive).
- Both ends of the run are reported because collapsing them would lose matches
  without any error: `ab|c` on `"abc"` ends at 2 and 3, which are consecutive, and
  keeping only 3 silently drops the `[0,2)` match.
- **This is not `FindAllStringIndex` semantics.** That returns a leftmost-first,
  non-overlapping sequence; this returns *all* end points of *all* patterns, overlaps
  included (`abcd|bc` on `"abcd"` reports both). Choosing between overlapping hits is
  policy, and the library does not decide it for you — use `MatchScanner` if you want
  finished spans.
- Ordering is **not** globally ascending (a run closes only when its pattern hits
  again non-consecutively, or at end of input, so patterns interleave), but it *is*
  ascending within one pattern, which is what the cursor above relies on.
- Offsets are `int32`: it is the native width (so the C side writes the buffer
  directly, with no per-run conversion), it is signed because `end - minLen` is
  legitimately negative near the start of the input, and RE2 caps input at 2 GiB
  anyway.
- `alloc` may be `nil` (one is created and thrown away per call, costing one native
  allocation); on a hot path keep one. It is bound to the set it was created from —
  using it with another set returns an error rather than a wrong answer — and it is
  **not** concurrency-safe. `FindAllIndexBytes` is the zero-copy `[]byte` twin.
- Batch size is fixed at 4096 runs (48 KB) and is deliberately not a knob. The native
  side suspends like `sqlite3_step` — it saves DFA state by value, releases the DFA
  cache read lock, returns to Go, and resumes on the next call — so no lock is held
  while your callback runs.
- There is no "stop early" return from the callback. If you only need a yes/no
  answer, `MatchAny` stops at the first hit inside RE2, which is earlier than any Go
  side brake.

#### `ResolveSpan`: complete one end point into a span

```go
end, ok, err := set.ResolveSpan(text, start, id)              // forward: start (incl) -> end (excl)
lo, ok2, err2 := rev.ResolveSpan(text, end, id)               // reverse: end (excl) -> start (incl)
pos, ok3, err3 := set.ResolveSpanWithin(text, from, bound, id) // bound = how far to look, <0 = no limit
```

This is a **single anchored question at one offset**, not a scan: the cost is how far
that one match can extend, and is *independent of input length* — asking it on a 1 KB
input and on a 6.4 MB input costs the same. It returns the **longest** match at that
end point, not the first one found, because stopping at the first gives the shortest
span, which truncates the hit and makes downstream fixed-length or checksum
validation reject a genuine match. `ok == false` means the pattern does not match at
that end point at all (wrong offset or wrong `id`). It is read-only and may be called
concurrently with scans on other goroutines. `ResolveSpanBytes` is the `[]byte` twin.

Use it to complete an end point; **do not** run a reverse `FindAllIndex` over the
whole input to do the same job (a whole-table reverse scan of that 6.4 MB body takes
65 s versus 18 ms forward, and one-pattern-at-a-time reverse scans put back exactly
the `1 + k` passes this API removes). Equally, do not rebuild it on the Go side: an
unanchored `re.FindStringIndex(text[from:])` keeps the `.*?` prefix and scans to the
end of the input, while a hand-written `\A(?:pat)` means a second `Regexp` object with
its own DFA cache and a semantic equivalence you have to maintain by hand.
`ResolveSpan` uses the set's own program and DFA cache, through its anchored entry
point.

`ResolveSpanWithin`'s `bound` limits how far the resolution may look (right limit
going forward, left limit going backward). It is what makes patterns that can extend
without limit (`(?s).*KEY`) constant-cost instead of O(input). Matching context is
always the **whole** input, so `\b`, `^` and `$` still see the real neighbouring
bytes — a bound can only make the answer shorter, never wrong.

#### `GetPatternLenRange`

```go
min, max := hgmLibre2.GetPatternLenRange(`[A-Z]\d{3}`)   // 4, 4
min, max = set.GetPatternLenRange(i)                     // same, from the table built with the set
// max == hgmLibre2.PatLenUnbounded (-1) means "no upper bound"
```

The **byte**-length range a pattern can match, computed once at set-build time (155
patterns in under 1 ms) with Go's `regexp/syntax` — the same grammar, and no change
to the vendored RE2. Three tiers drive everything above: `min == max` means the start
is a subtraction; a finite `max` means a bounded look-back; `PatLenUnbounded` means
there is no window to look back over, and the pattern falls back to the caller.
Patterns that RE2 accepts but Go's parser rejects return `(0, PatLenUnbounded)` —
conservative in the safe direction, i.e. it can only push a pattern onto the fallback
path, never produce a wrong start.

### Where a reverse set matched: `MatchScannerReverse`

The mirror of `MatchScanner`: one pass from the **end** of the input, handing back
de-duplicated, non-overlapping spans in batches. The API is identical — open it on a
`*RegexpSetReverse` instead of a `*RegexpSet`:

```go
rs, _ := hgmLibre2.NewRegexpSetReverseMaxMem(patterns, hgmLibre2.DefaultSetMaxMem)
ms, unsupported, _ := rs.NewMatchScanner()   // reusable workspace; not concurrency-safe
defer ms.Close()

err := ms.Scan(body, func(batch []hgmLibre2.SetMatch) {
    for _, m := range batch { _ = body[m.Lo:m.Hi] }   // a real match of pattern m.Index
})
```

Two things differ from the forward scanner, and only two:

1. spans come back in **descending** `Lo` order (forward: ascending);
2. the de-overlap rule is **rightmost-longest** (forward: leftmost-longest).

Both directions guarantee the same three things: every span is a real match, spans of
the *same* pattern never overlap, and no region containing a match is silently skipped.
The two rules differ only where two real matches actually overlap — everywhere else
they agree span for span:

| input | pattern | leftmost-longest | rightmost-longest |
|---|---|---|---|
| `abab` | `a\|ab` | `[0,2) [2,4)` | `[2,4) [0,2)` — same set, reversed order |
| `aab` | `ab\|b` | `[1,3)` = `"ab"` | `[2,3)` = `"b"` — genuinely different |

If you need to match `re.Longest().FindAllStringIndex` byte for byte, use the forward
scanner. If you just need everything in the text framed (masking, locating, counting),
either rule does.

**When to reach for it.** When the table contains patterns that explode forwards and
collapse in reverse — the `S B{m,n} L` shape of
[Scanning backwards](#scanning-backwards). Before this layer existed, such a table
could only be scanned backwards as a *gate*: `Match` said which patterns hit, and
getting positions meant a second forward pass over the whole body — which is exactly
the `1 + k` passes `MatchScanner` exists to collapse.

**Reverse is the easier direction, not the harder one.** A forward DFA pass reports
match **ends**, so the start has to be guessed back on the Go side — that guess is
what the two-step recovery described above is about. A reverse pass
reports match **starts**, and leftmost/rightmost-longest is *defined* on starts. So
there is no guess here:

- reverse `FindAllIndex` → match starts, monotone in scan order (right to left);
- forward **single-pattern** `FindStringIndexAtWithin(from: start, bound: cursor)` →
  the **longest** end that does not cross the cursor.

Hence no candidate-collection step at all, and no `maxLen`
window. Each span costs exactly one anchored search — proportional to the length of
that match, not to the length of the input. (That call used to go through a
one-pattern *set*; since 2026-08-27 it uses the pattern's own single-pattern
longest-mode object, for the same three reasons listed under the forward scanner.) It also picks up the family the forward
default tier cannot take: patterns with **no upper length bound** (email and friends),
which forward needs `maxL` to bound a look-back window for.

**Why scanning right-to-left still keeps nothing.** Only because the rule flipped with
it. Insisting on leftmost-longest *while* scanning backwards is what would force
buffering: the span in your hand can still be swallowed by one further left that you
have not reached yet — bounded patterns could ride a `maxL`-wide delay buffer, but
unbounded ones would have to accumulate until the scan ends, and memory tracking input
length is precisely what this layer exists to avoid. Under rightmost-longest the
problem does not arise: going right to left, **the first start you see is final**,
because nothing further left can outrank it — the same sentence as the forward
scanner's, in a mirror. So the cursor still advances inside the callback and output
still goes into the same fixed 12 KB buffer.

**How it is pinned.** `matchscan_reverse_test.go` generates its corpus from each
pattern's own `regexp/syntax` AST (random bytes never produce real matches — that is a
vacuously green test) and checks against an exhaustive rightmost-longest oracle written
against stdlib alone. The first five patterns in that list are exactly the
counterexamples that broke the forward `spanFast` path removed in 2026-08-28 (`abc|b`, `a|ab`,
`x{1,3}[a-c]?(?:ab|cd)?`, `(?:ab)?[bc]{1,2}`, `(?:ab)*b{1,3}`); reverse diverges on
none of them, because there is no guess to get wrong. The oracle carries its own
self-check: `ab|b` against `"aab"` must produce different answers under the two rules,
or the whole comparison is vacuous.

### One forward pass, spans straight out: `Re2SetFrel`

`Re2SetFrel` answers the same question as `MatchScanner` — *where did each pattern
match?* — but almost all of it lives in C++. Go only hands it a
`[]Re2SetFrel_result_t` and the C side writes the results straight into it, a batch per
`step` — that Go struct and C's `cre2_frel_result` are the same memory layout (three
`int32`, no padding; a static assert on each side), so nothing is copied field by
field. The name: **F** = first pass forward, **RL** = **r**ightmost-end **l**ongest.
Do not call this rule plain "rightmost-longest": in this library that name already means
`MatchScannerReverse`'s rule, which anchors on the **start**. This one anchors on the
**end** — spelled out below, and they give different answers.

```go
s, _ := hgmLibre2.NewRe2SetFrel([]hgmLibre2.Re2SetFrelPattern_t{
    {Pattern: `\d{3}-\d{2}-\d{4}`},                 // wants spans
    {Pattern: `(?i)authorization`, ExistOnly: true},  // only "did it hit?"
})
defer s.Close()

buf := make([]hgmLibre2.Re2SetFrel_result_t, 1024)   // reuse it => 0 allocs/op
err := s.Scan(body, buf, func(rs []hgmLibre2.Re2SetFrel_result_t) bool {
    for _, r := range rs {
        _ = body[r.Start:r.End]          // a real match of pattern r.Index
    }
    return true                          // false = stop early
})
// afterwards s.IsHit(i) / s.GetHitIDs(nil) is the hit table (the only output of ExistOnly rows)
```

The slice handed to the callback **is** `buf`; the next batch overwrites it in place.
Copy what you keep. Its length only sets how many results cross the bridge per `step`,
never the answer. And as with `MatchScanner`, it is all-or-nothing: a
non-nil `err` voids the whole pass (including batches already handed over) — fall back
to `FindAll` for that body.

**How it differs from `MatchScanner`.** `MatchScanner` looks back once per match *end*
that the cursor has not already covered. This layer looks back once per **component**.
Components come from a per-pattern *liveness bit* carried in each DFA state
(`State::live_`): if a pattern has no live threads at some offset, then no match of it
can cross that offset, so hits on either side are independent. The moment a pattern
goes live→dead its pending hits are closed off as one component — and the component's
left edge doubles as the lower bound for the look-back, so resolving never has to walk
to the start of the body. Three stages, each where it is cheapest:

| stage | where | what |
|---|---|---|
| scan the body | the table's forward `kManyMatch` DFA | one pass |
| collect + split | native side, no per-hit bridge crossing | match ends are kept as run-lengths in a per-pattern buffer, handed over one component at a time |
| resolve starts | that one pattern's **single** `Regexp`, reverse-anchored | one call per non-overlapping match in the component |

`GetStats().NResolve / GetStats().NSeg` is *how many reverse-anchored searches each component
cost*; on the ten general-purpose patterns of the benchmark it is exactly 1.00 across
all three corpora. `GetStats().UsedPeak` — the native-side run buffer — stays in the tens
of bytes and does not grow with the body.

**The de-overlap rule — rightmost *end*, longest.** Start with the bound at the end of
the body; repeatedly take the match whose **end** is furthest right and still `<=` the
bound, break ties by taking the **longest** one (leftmost start), emit it, then drop the
bound to its start. That is a different rule from `MatchScannerReverse` (which picks the
rightmost *start* first) and from stdlib's leftmost-longest:

| input | pattern | `Re2SetFrel` | `MatchScannerReverse` | stdlib `Longest` |
|---|---|---|---|---|
| `aaa` | `aa\|a` | `[0,1) [1,3)` | `[2,3) [1,2) [0,1)` | `[0,2) [2,3)` |
| `abc` | `b\|abc` | `[0,3)` = `"abc"` | `[1,2)` = `"b"` | `[0,3)` = `"abc"` |

That second row is why this layer picks ends rather than starts: picking starts truncates
`"abc"` down to the `"b"` in the middle, and a caller that feeds the span to a checksum
(national ID, IBAN, Luhn) then rejects its own true hit — a silent miss. If you need byte
equivalence with `re.Longest().FindAllStringIndex`, use `MatchScanner` instead; if you
just need everything framed (masking, locating, counting), this rule is fine.

Correctness is pinned by `re2setfrel_test.go`: the oracle is an exhaustive, library-free
search (`\A(?:pat)\z` over every `(e, s)`), corpora are generated from each pattern's own
AST so random bytes cannot make the test vacuously green, and `aa|a` on `"aaa"` must give
three *different* answers under the three rules or the whole comparison is a no-op. The
oracle has no notion of components, so it also pins the claim that splitting into
components does not change the answer.

**`ExistOnly` is not an optional tweak.** A row marked `ExistOnly` sets one byte when it
hits: no runs collected, no liveness watched, no component closed, no start resolved.
That is the expensive part of this layer, not just a few results you would have thrown
away — filtering inside the callback is throwing away work already paid for. Patterns
that can match the empty string (`GetPatternLenRange` min `<= 0`) may *only* be `ExistOnly`;
otherwise `NewRe2SetFrel` fails immediately, independent of any body.

**Measuring it.** These DFA loops are extraordinarily sensitive to code layout on the
benchmark machine: adding one *never-called* Go function to the test package moves the
zero-hit 64 KiB figure between 98 µs and 199 µs. Numbers from a single binary are
meaningless — sweep the layout (0..7 unused functions, one binary each) and take the
**minimum** per variant. Done that way, on 64 KiB with the ten benchmark patterns:

| corpus | old two-stage | `MatchScanner` | `Re2SetFrel` |
|---|---|---|---|
| zero hits | 97.5 µs | 98.5 µs | **97.2 µs** |
| 39 sparse hits | 430.9 µs | 104.8 µs | **104.3 µs** |
| worst case (all lowercase) | 598.9 µs | **478.7 µs** | 575.6 µs |

Zero hits costs exactly what a plain scan costs — the liveness machinery is genuinely
free until the first hit, because the idle loop is a separate instantiation that never
reads a liveness bit at all. The worst-case corpus is the one where `[a-z]{4,}` never
dies, so the whole body is a single component and every byte is watched.

Those ten patterns **understate it**. On three real gate tables (368 pattern literals
harvested from the product source, split by shape into cred/64, prompt/31, body/160),
256 KiB of text, four hit densities, same eight-layout sweep — harness in
`tmp/frlbench`:

| table | hits | floor | `MatchScanner` | `Re2SetFrel` | end-to-end | start-resolution layer |
|---|---|---|---|---|---|---|
| cred | 1% | 0.40 ms | 0.45 ms | **0.42 ms** | 1.07× | 2.50× |
| cred | 90% | 1.00 ms | 5.42 ms | **3.00 ms** | 1.81× | 2.21× |
| prompt | 1% | 0.40 ms | **0.52 ms** | 0.59 ms | 0.88× | 0.63× |
| prompt | 90% | 0.97 ms | 11.45 ms | **5.60 ms** | 2.04× | 2.26× |
| body | 1% | 1.96 ms | 35.18 ms | **15.68 ms** | 2.24× | 2.42× |
| body | 90% | 4.21 ms | 53.06 ms | **26.38 ms** | 2.01× | 2.20× |

*Floor* is the forward set scan both paths must pay; the start-resolution column is
`(total − floor)` compared. Eleven of the twelve cells favour `Re2SetFrel`, by 1.06×
to 2.24× end to end and a steady 2.2–2.4× on the layer that actually differs. The one
loss is the prompt table at 1% density: hits are so sparse that nearly every hit is its
own component, so there are no reverse-anchored probes to save and the component
bookkeeping is pure overhead. Where the saving comes from, in one line: `MatchScanner`
runs one reverse-anchored probe **per match end**, `Re2SetFrel` runs one **per
component** (body at 90%: 254k ends, 145k components).

### Scanning backwards

`S B{m,n} L` — a counted repeat whose **start class is strictly narrower than
its repeat class**, ending in a literal — is the shape that blows the state
count up. `[A-Za-z][A-Za-z0-9]{2,19}key` is the canonical one: every letter can
open a candidate match, every following alphanumeric keeps it alive, so a DFA
state has to remember *which* of the last 20 offsets are still live. That set is
an arbitrary subset, so the state count is exponential in the bound.

No rewrite fixes this. `(a|b)*a(a|b)^k` needs 2^k states in *any* DFA, minimal
included, so it is a property of the language and not of RE2. But the **reverse**
language, `(a|b)^k a(a|b)*`, needs k+2. Direction is the lever.

Forward and reverse are **two objects**, not two methods on one — a pattern's two
directions are two programs with two DFA caches, and a pattern normally only ever
runs in one of them:

```go
rev, _ := hgmLibre2.CompileReverse(`[A-Za-z][A-Za-z0-9]{2,19}key`)
hit := rev.MatchString(text)          // same answer a forward Regexp would give

revSet, _ := hgmLibre2.NewRegexpSetReverseMaxMem(patterns, hgmLibre2.DefaultSetMaxMem)
idx := revSet.Match(text, buf)       // a *RegexpSetReverse; same hit set as a forward set
```

The reverse program is built by RE2's own compiler (concatenations reversed,
`^`/`$` swapped, `\b` unchanged, multi-byte UTF-8 sequences re-encoded
back-to-front) and the DFA then walks **your buffer** from the end. Nothing is
copied and nothing is reversed by the caller.

Measured on 120 distinct 8 KB bodies, one pattern per set so the memory is
attributable:

| | states | state cache | hit set |
|---|---:|---:|---|
| forward | 35 149 | 5.35 MB | 16 |
| reverse | 45 | 0.01 MB | 16 |

**What it costs you (single `RegexpReverse`).** A `RegexpReverse` answers "is there a
match", not "where". There is no `Find` on it: a reverse scan meets the *last* match in
the input first, so a reverse `Find` could only ever be rightmost — a different
semantics from the forward leftmost-first one. Use reverse as the cheap gate and run
`FindStringIndex` on a forward `Regexp` for the few inputs that hit.

On the **set** side that objection is answered rather than avoided: rightmost is a
perfectly good de-overlap rule as long as it is the declared one, so
[`MatchScannerReverse`](#where-a-reverse-set-matched-matchscannerreverse) gives spans
under **rightmost-longest** and says so.

`RegexpSetReverse` does report positions, in its own direction:
[`FindAllIndex`](#findallindex-the-raw-end-point-runs) gives match **starts**
(inclusive) where the forward set gives ends, and
[`ResolveSpan`](#resolvespan-complete-one-end-point-into-a-span) turns a known end into
the matching start. That second one is what a reverse set is really for.
🔴 Scanning a *whole table* backwards is a different proposition from scanning one
pattern backwards: state counts inside a set **multiply**, so a 155-pattern table that
scans a 6.4 MB body in 18 ms with zero flushes forward takes **65 s** in reverse, with
the arena pinned at its 254 MB ceiling and still flushing. Measure
`GetMemInfo().FlushesTotal` before pointing a reverse set at whole documents; to recover
the left edge of a hit you already found, use `ResolveSpanWithin`, whose cost is
independent of input length. `MatchScanner` does exactly this internally — a lazily
built **one-pattern** reverse set per pattern that ever needs a left edge, never used
to scan text (see `GetViableOneStats` for what those cost: 32 patterns, 973 states,
2.0 MB on that table).

**Direction is a per-pattern decision, not a global switch.** The mirror shape
loses by the same mechanism it wins by: on a corpus containing no `key`,
`(?s).{20}key` costs 21 states forward and 1 reverse, while `key(?s).{20}` costs
1 forward and 21 reverse. Measure both directions on real input — build a
one-pattern set each way and compare `GetMemInfo().States` — then put each pattern
in whichever set matches its cheap direction. Two scans over the input still
beat one scan that is thrashing.

**This is not "reverse the pattern text yourself".** Writing the pattern
backwards and reversing the input gets the same *answer* but not the same cost.
RE2's `Simplify` expands `x{2,19}` with the mandatory copies first and the
optional nest after; reversing the concatenation moves that optional nest to the
*front* of the read order, and the live-start sets then nest inside one another
instead of forming arbitrary subsets. Same language, same bytes, different
automaton: 17 states through this API versus 25 247 for the hand-rolled version
(`TestReverseIsNotHandRolledTextReversal`). Hand-rolled reversal also splits
multi-byte UTF-8 and needs a second copy of the input.

**Why a separate type.** A reverse scan runs a *second* `Prog`
(`Regexp::CompileToReverseProg`), and a DFA state cache belongs to its `Prog`
(`Prog::dfa_first_` / `dfa_longest_`) — so the two directions are two programs
and two caches no matter how the Go API is shaped. `RegexpReverse` makes that
visible: one object, one direction, one cache, and the caller can see from the
type which direction the pattern is running. Want both directions for the same
pattern? Compile both objects.

**Budgets and the fallback.** The reverse program is compiled lazily on the first
scan, with the budget from `CompileReverseMaxMem` (`CompileReverse` gives it
RE2's 8 MB default). If the reverse DFA gives up mid-scan — RE2 bails out of a
`Prog` search that is building states faster than it consumes input — the object
silently falls back to one forward match of its own. The answer is always
correct; `MatchStats` reports `FellBack` so you can tell that a scan did not get
the saving. `RegexpSet` never bails (RE2 only flushes for `kManyMatch`), so a
reverse set has no fallback path.

**The other lever is memory.** `CompileMaxMem` raises the budget for a single
pattern the way `NewRegexpSetMaxMem` does for a set — same knob, same two
ceilings ([Sizing `maxMem`](#sizing-maxmem)). On the pattern above, 60 distinct
16 KB bodies flush the cache 6 times at the 8 MB default and 0 times at 256 MB.
Reverse scanning gets to 0 flushes at the *default* budget, with a peak of 9
states. Raising the budget buys throughput with RAM; scanning backwards buys it
with nothing, when the shape allows.

### AppendAllStringIndexFlat

`re.AppendAllStringIndexFlat(dst, s, n)` returns the same matches as
`re.FindAllStringIndex(s, n)`, appended to a caller-owned `[]int` as
`[s0, e0, s1, e1, …]` instead of being wrapped in a fresh `[][]int`.

`FindAllStringIndex` makes two throwaway allocations per call that both scale
with the match count: the flat `[]int` the C side is copied into (`2*nmatch`
ints per match — all groups, even though only group 0 is returned), and the
`[][]int` shell (one 24-byte slice header per match). On a large body with a
high hit count that dominates: 190k matches ≈ 40 bytes each ≈ 7.6 MB for a
single call, all of it garbage as soon as the caller has walked the matches
once. This variant fills group 0 only (`nmatch=1`, which also shrinks the C-side
`vector<StringPiece>`), and appends into your buffer, so passing `buf[:0]` makes
repeat calls steady-state allocation-free.

```go
var locs []int                          // reuse across calls
for _, text := range corpus {
    locs = re.AppendAllStringIndexFlat(locs[:0], text, -1)
    for i := 0; i+1 < len(locs); i += 2 {
        start, end := locs[i], locs[i+1]
        ...
    }
}
```

The match set, its order, and the empty-match handling are identical to
`FindAllStringIndex` — both go through the same C loop — and are pinned against
it and against stdlib in `find_all_flat_test.go`. Use
`FindAllStringSubmatchIndex` when you need capture groups.

### AppendReplaceAllStringFunc

`ctx.AppendReplaceAllStringFunc(dst, re, src, f)` produces exactly what
`re.ReplaceAllStringFunc(src, f)` produces, but appends it to a caller-owned
`[]byte` and keeps the match-position table on the `ctx`, so both buffers are
reused across calls.

It returns `(dst, changed)`. **`changed` means "the result differs from `src`",
not "the regexp matched"** — it is defined to be exactly
`re.ReplaceAllStringFunc(src, f) != src`. Two cases report `false`, and both
leave `dst` byte-for-byte as you passed it in:

1. nothing matched — fast return, no bytes written;
2. something matched but every `f` handed the original text straight back, so
   the result is byte-identical to `src` — the appended bytes are rolled back.

Case 2 is not a nicety. Replacements written against this API are usually
decoders or de-obfuscators whose `f` carries its own validity check and returns
`m` unchanged when it fails: an HTML numeric entity `&#…;` whose code point is
out of range, a hex run of odd length or one that decodes to non-printable
bytes. Those match but change nothing, and a caller that treats `matched` as
`changed` ends up with a spurious extra copy of the original — one more buffer
to hold, one more pass to scan, one more duplicate to reconcile. The check costs
essentially nothing: a length mismatch decides it outright, and only an
exactly-equal length pays for a `memcmp`.

The rollback restores the length and the contents, but not the capacity: `dst`
may come back pointing at a larger backing array (the `len(src)` bytes just
reserved). That is a win for the next call — just always use the returned slice
rather than the one you passed in.

```go
var ctx hgmLibre2.ReplaceAllStringFunc_ctx_t   // zero value is usable; not goroutine-safe
var buf []byte                                 // reuse across calls
for _, text := range corpus {
    out, changed := ctx.AppendReplaceAllStringFunc(buf[:0], re, text, decode)
    buf = out         // keep the (possibly grown) buffer either way
    if !changed {
        use(text)     // decoding changed nothing, nothing allocated
        continue
    }
    use(string(buf))  // or keep working on the bytes
}
```

Why it exists: `ReplaceAllStringFunc` pays two throwaway allocations per call
that both scale with the body — the match table (`2*(numSubexp+1)` ints per
match, of which the concatenation loop reads only group 0), and the result
buffer. The result buffer used to be a bare `strings.Builder` growing from
nothing: for a large byte slice Go grows by 1.25×, so the cumulative allocation
converges to `1/(1-1/1.25) = 5×len(src)`, with 4× of that also paid in memcpy.
Measured on a 64 MB body in a hex-decoding leg: 329 MB of `Builder` growth, 4.9
bytes allocated per input byte. `ReplaceAllStringFunc` now sizes that buffer to
`len(src)` in one shot (the same thing stdlib's `replaceAll` and this library's
own `ReplaceAllFunc` byte facade already did), which is a 1× buffer and no
regrow copies. This variant goes one step further and reuses the buffer you
already own, which is what a hot loop calling it per segment actually wants.

`ReplaceAllStringFunc` itself is now a thin shell over this method, so the two
share the match set, the order, the call sites of `f` and the concatenation
(both read group 0 out of the same C loop) — and the same lazy materialization
as `ReplaceAllString`: a call that changes no bytes hands the original `src`
back with zero allocation. That, the append contract, the rollback contract, the
no-bleed-on-reuse contract and the steady-state-zero-allocation claim are pinned
in `replace_func_ctx_test.go`.

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

Motivating use case: undoing separator obfuscation — `find` = a
separator-tolerant keyword skeleton (`i[\s._-]{0,2}g…`), `strip` = the separator
class, `repl = ""`, so `i.g-n_o r.e` is normalized back to `ignore`. On the
common path (ordinary text, nothing obfuscated) it is allocation-free and
matches the plain DFA scan throughput; on input full of split keywords it is
~2× faster than the nested-`ReplaceAllStringFunc` form, with allocations
collapsed from O(matches) to one.

#### AppendFindReplaceWithin

`find.AppendFindReplaceWithin(dst, strip, src, repl) ([]byte, bool)` is the
**append-into-your-own-buffer** twin, for callers that consume the result once
and throw it away (build a decoded view → scan it with a `RegexpSet` → drop it).
Same C kernel, same `changed` predicate; the only difference is where the result
lands: `FindReplaceWithin` mints a fresh Go `string` on every changed call
(one `C.GoStringN` copy of the whole result), while this one memcpy's it into
the `dst` you pass — so a reused buffer makes the steady state zero Go-heap
allocation.

```go
out, changed := find.AppendFindReplaceWithin(buf[:0], strip, src, "")
// changed ⟺ find.FindReplaceWithin(strip, src, "") != src
// changed ⟹ string(out) == find.FindReplaceWithin(strip, src, "")
if changed {
    buf = out          // always keep the returned slice: it may have re-based
    scanSet.Match(bytesStrView(out), hits)
}
```

`changed=false` leaves `dst` untouched down to its length — the caller should
use the original `src`. The returned bytes are a view into the caller's buffer:
appending to that buffer again (or reslicing it to `[:0]`) invalidates them.

There is no `_ctx_t` for this one: the outer match loop and the inner
replacement both live in C++, so the result itself is the only Go-side
allocation that scales with the input — and that one is now the caller's.

The test suite (`hgmLibre2_test.go`) cross-checks every method against the
standard library `regexp` on a shared corpus of patterns and inputs; results
are identical on that corpus (the corpus uses only literal `ReplaceAllString`
repls, see the API difference below). `TestReplaceAllStringIsLiteral` pins the
literal-repl behavior, and `review_verify_test.go` pins the engine-level
[differences](#differences-from-stdlib-regexp) below as differential tests.

`bytes_test.go` does the same for the `[]byte` family over the same corpus — each
method against both its stdlib counterpart and its own `string` twin — plus a
hand-computed hit/miss pair per method (pinning `nil` vs empty), and the
zero-copy contracts: results share the input's backing array, read-only methods
never mutate the input, the no-change path reuses `src`, and `Match([]byte)`
allocates less than `MatchString(string(b))`.

```sh
go test ./...
```

## Prefilter: which literals must appear, and which patterns can never be filtered

`Prefilter` exposes RE2's own prefilter machinery (`FilteredRE2` / `PrefilterTree`,
both already vendored here). It answers three questions:

```go
p, err := hgmLibre2.NewPrefilter(patterns, 0 /*minAtomLen: 0 = RE2 default*/, 0 /*maxMem*/)
atoms := p.GetAtoms()              // lowercased, distinct literals that must appear
live  := p.GetPotentials(found)    // given the atom indices found in the text: which patterns can still match
hard  := p.GetUnfiltered()         // which patterns need NO literal at all -> they always have to run
```

`Prefilter` does no matching of its own. It hands you the atom list; you find
those atoms with whatever string matcher you like (an Aho-Corasick automaton, or
`memmem`), then ask which patterns survive. A pattern that does not survive is
**guaranteed** not to match. Matching must be case-insensitive, or done on a
lowercased copy of the text, because the atoms are lowercased.

**`GetUnfiltered()` is the reason this is exposed.** "Screen the text with a cheap
literal gate first, and only run the big table on what gets through" is the one
direction that raises the throughput ceiling (see
`doc/set性能优化经验.txt` §4 G) — but it has a hard cap: patterns with no required
literal (`[A-Za-z0-9+/=_-]{20,}`, `(?-i:\([A-Z]{2,5}\))`) have to run no matter
what the text looks like. Measure that set **before** building a prefilter stage,
not after.

🔴 Only RE2's own prefilter gets this right. A hand-rolled "pull the literals out
of the pattern source" extractor answers wrongly on `(?:foo|[A-Z]{5})`: it
contains the literal `foo`, yet the pattern as a whole is unfilterable, because
the other alternative does not need `foo`. That reasoning lives in an AND-OR tree
and is not something you can eyeball. `prefilter_test.go` pins this case, along
with the soundness property the whole idea rests on: every pattern that really
matches a text must appear in `GetPotentials()` for the atoms found in that text.

`minAtomLen` is a real trade-off knob, not a tuning detail. Raising it yields
fewer, longer atoms — a faster matcher, but more patterns fall into
`GetUnfiltered()`. Measured on a 112-pattern production table: RE2's default gives
1654 atoms and only 4 unfilterable patterns, but the atoms are so short that they
occur in nearly every text, so nothing gets filtered out; `minAtomLen=6` gives 216
atoms and 38 unfilterable patterns, which filters much harder but starts from a
34% floor. Measure both ends on your own table.

## Tuning for the DFA state cache

[`doc/set性能优化经验.txt`](doc/set%E6%80%A7%E8%83%BD%E4%BC%98%E5%8C%96%E7%BB%8F%E9%AA%8C.txt) is the long-form version of the
performance material above, for a single `Regexp` as well as for a `RegexpSet`:
the mental model, what to measure and in what order, what actually drives state
explosion, the three knobs in benefit order (direction > pattern shape > memory
budget), how to split a table, and a list of approaches that were measured and
rejected. Read it before tuning a table of hundreds of patterns, or before
reaching for `CompileMaxMem`/`CompileReverse`; the sections above are the summary.

## Differences from stdlib `regexp`

This is the complete list of concrete behavior differences from Go's standard
library `regexp`. The first two are **API-design choices** (this library
deliberately is not a drop-in); the rest follow from running the **native RE2
engine** instead of Go's from-scratch reimplementation. All are intentional and
covered by tests.

For a migration checklist that pairs each gap (here and in
[Supported API](#supported-api)) with what to use instead — together with the
performance side of the same decision — see
[`doc/与标准库regexp怎么选.md`](doc/%E4%B8%8E%E6%A0%87%E5%87%86%E5%BA%93regexp%E6%80%8E%E4%B9%88%E9%80%89.md) §4.

1. **`ReplaceAllString` repl is literal — no `$` expansion.** stdlib expands
   `$1` / `${name}` / `$$` in the replacement string; here `repl` is inserted
   byte-for-byte with no expansion and no escaping (so `"$1"` stays `"$1"`,
   `"$$"` stays `"$$"`). This is the one method that is *not* signature-compatible
   in behavior. If you need capture-group substitution, use `ReplaceAllStringFunc`
   and build the replacement yourself. (`FindReplaceWithin` is a different,
   non-stdlib method and uses RE2's `\1` rewrite syntax — see its section above.)
2. **`[]byte` replace methods reuse `src` on the no-change path.** stdlib's
   `ReplaceAll` / `ReplaceAllFunc` always return a freshly allocated slice; here
   an input that comes out byte-for-byte unchanged (no match, or a replacement
   that changes nothing) is returned **as the original `src` slice**, allocation-free
   — so the result must not be written to. The `string` methods have always
   behaved this way; strings being immutable, it is only observable in the
   `[]byte` family. Content-wise the results are identical, including the
   `nil`-vs-empty conventions. See [Byte-slice methods](#byte-slice-methods).
3. **Invalid UTF-8 input.** stdlib treats each invalid byte as one-byte
   `U+FFFD` and lets `.` match it; native RE2 only matches whole valid runes, so
   on e.g. `[]byte{0xff,'a',0xfe}` the pattern `.` finds just the `a`. If you
   match on possibly-invalid UTF-8 and need stdlib's behavior, use stdlib.
4. **`\C` is accepted** (RE2 "any byte"); stdlib `regexp` rejects `\C` at
   compile time. More generally a handful of escapes are RE2-only or stdlib-only,
   so a pattern valid in one may be rejected by the other.
5. **2 GiB input limit.** Lengths/offsets cross the cgo boundary as 32-bit
   `int`, so inputs (and patterns) longer than `2^31-1` bytes are conservatively
   treated as *no match* / returned unchanged rather than matched. stdlib has no
   such limit. (Irrelevant unless you feed multi-gigabyte strings.)
6. **Case-folded literals fold over the full Unicode orbit when they are merged
   into a character class.** When a case-folded literal is one branch of an
   alternation that RE2 factors into a single character class, the class picks up
   every fold-equivalent rune, not just the ASCII pair: `\w|[kK]` also matches
   U+212A KELVIN SIGN, where stdlib matches only `k`/`K`. (`[sS]|\w` has always
   matched U+017F this way.) This is upstream RE2 behavior and only shows up for
   non-ASCII fold-equivalents.
7. **Capture names may be non-ASCII.** `(?P<中文>a)` compiles here; stdlib has
   rejected non-ASCII capture names since Go 1.22. Both forms of named group —
   `(?P<name>expr)` and `(?<name>expr)` — are accepted, as in stdlib (Go 1.22+).
8. **No nesting-depth limit.** stdlib rejects patterns whose parse tree nests
   deeper than 1000 (`expression nests too deeply`, Go 1.19+); this library
   accepts them. 200 000 nested groups compile in ~100 ms with linear memory and
   no stack growth (parsing, simplification and teardown are all iterative), and
   400 000 fails cleanly on the capture-group limit — so this is only a matter of
   *accepting more* than stdlib, not a robustness gap. If you compile untrusted
   patterns and want stdlib's ceiling, check the depth yourself before compiling.

Not a difference, but worth stating: matching is **leftmost-first** here, which
is also stdlib's default (`regexp.Compile`); stdlib's opt-in leftmost-longest
mode (`(*Regexp).Longest`) is not provided. Capture-group names of any length
are returned in full and duplicate named groups are accepted — same as stdlib.

## Concurrency: sharing one `Regexp` is fine (it just doesn't scale linearly)

Share one package-level `*Regexp`, the way you would with stdlib. This section
exists to explain a scaling curve, not to ask you to do anything about it.

A `Regexp` is safe to use from multiple goroutines, but it does not scale
*linearly*. Every DFA search takes a **read lock** on that `Regexp`'s DFA state cache
(`DFA::cache_mutex_`, a `pthread_rwlock` on Linux); the lock exists only so the
rare whole-cache flush can run exclusively, yet every single search pays for it.
Read locks do not exclude each other, but the reader count is an atomic on one
shared cache line, so with enough goroutines that line ping-pongs between cores
and the "concurrent" searches serialize.

Measured on a 20-core Ryzen 5900X, non-matching pattern, ns/op:

| | 14-byte input | 4 KB input |
|---|---|---|
| one shared `*Regexp`, 1 goroutine | 69–74 | 453–467 |
| one shared `*Regexp`, 16 goroutines | 42–77 | 62–69 |
| one `*Regexp` per goroutine, 16 | 9.5–13 | 38–51 |
| stdlib `*regexp.Regexp` shared, 16 | 4.3–4.5 | 67–70 |

Compiling the read lock out (measurement only) makes the shared case match the
per-goroutine case exactly (8.0–8.5 ns at 16 goroutines), so the entire gap is
that one lock. Short inputs suffer most, but even 4 KB inputs lose ~1.6×.

**This is not a reason to stop sharing.** The whole effect is ~33 ns per call at
16 goroutines on 14-byte inputs, and buying it back is a bad trade in most
programs: one `*Regexp` per worker means one compile per worker (microseconds to
milliseconds each, and the pattern is usually compiled once at init today), one
**separate DFA state cache** per worker — so the peak native memory and the
`max_mem` budget you tuned both multiply by the worker count — and lifetime
management (pooling, `FreeC`) that a package-level variable doesn't need. A
shared `Regexp` also *reuses* cached DFA states across goroutines; N private
copies each rebuild them.

Only consider per-worker copies if a profile actually points at this lock — i.e.
regex matching is a top cost in your program, inputs are short, and the
concurrency is high. Otherwise keep the one shared variable. Correctness,
`RegexpSet`, and low concurrency are unaffected either way.

Note what the stdlib column does *not* say: at 14 bytes a single cgo call
(~50 ns) already costs more than the whole match, so stdlib wins there whatever
the locking does; that row is a scaling reference, not a throughput comparison.
At 4 KB the shared case is level with stdlib and the per-goroutine case is
~1.7× faster. This is upstream RE2 issue #569; the benchmark that produces the
table is `contention_bench_test.go`.

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

A small set of **later upstream fixes is backported** on top of that tag — the
ones that are real fixes rather than abseil churn, most notably a silent
false-negative in alternation factoring (`0a|0[aA]` used not to match `"0A"`),
support for `(?<name>expr)`, and not expanding counted repetitions of
zero-width operators (`\b{1000}`). Each site is tagged `[backport re2 <commit>]`
in the source; `VENDOR.txt` lists them, together with the upstream commits that
were deliberately *not* taken and why.

Three further fixes come from upstream pull requests that are still **open**
(tagged `[backport re2 PR#NNN]`), reproduced and cross-checked against stdlib
here before being taken. The one that mattered: RE2's "if the DFA is rebuilding
its cache this fast, fall back to the NFA" heuristic compared `p - resetp`, which
is negative during the **reverse** scan that locates a match's start — so the
heuristic had never fired in that direction and the reverse DFA would flush
itself indefinitely. Fixing it takes `(?s)a[a-d]{24}b[a-d]*` over 1 MB from 43
flushes / 234 ms to 1 flush / 34 ms, with identical results.

### Local changes to the DFA

The vendored DFA is **not** byte-for-byte upstream. `re2_dfa.cc` stores each
transition-table slot as a 4-byte offset into an arena of states instead of an
8-byte `State*`, and grows that arena on demand instead of reserving the whole
budget up front. Three other vendored files carry small *additive*
changes for the counters above — `re2_set.cc` and the `re2_prog.h` / `re2_set.h`
headers gain optional out-parameters and accessors — plus one new header,
`re2_dfa_stats.h`, which is not upstream at all. Every other `.cc` file is
stock. `VENDOR.txt` lists the same set — that is the list to re-apply when the
vendored RE2 is upgraded.

Consequences, all measured on real pattern tables and real corpora:

- The same budget holds **1.74×** more states, so the budget at which a given
  table stops flushing drops by one step (e.g. 128 MB → 64 MB).
- Throughput does not regress on tables that never flush (it is a few percent
  better), and improves by two orders of magnitude on a table that *was*
  flushing at that budget — the win is crossing the cliff, not the encoding.
- Peak RSS falls ~30% on large working sets. On a budget that is genuinely
  saturated it rises ~10%, because the same bytes now hold 1.74× more states.

Match results are unchanged and this is enforced, not assumed: hit-set digests
are compared bit-for-bit against a build of the original 8-byte-pointer code
across a matrix of pattern tables, corpora, and budgets. The original encoding
is still in the source and can be restored with
`CGO_CXXFLAGS="-O2 -DRE2_DFA_NEXT_BITS=64 -DRE2_DFA_ARENA=0"`, which is useful
as a control when bisecting a performance question.

The only other build-time macro a caller might want is `-DRE2_DFA_ATTRIB=1`,
which turns on [attribution](#attribution-which-patterns-build-the-states).
Both default to off/stock, and the default build carries no fields, branches, or
counters for either.

## License

BSD 3-Clause, the same license as RE2. See [LICENSE](LICENSE) and
[RE2_LICENSE.txt](RE2_LICENSE.txt). The vendored RE2 files retain the copyright
of the RE2 Authors.
