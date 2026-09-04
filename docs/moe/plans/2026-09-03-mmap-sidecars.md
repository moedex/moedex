# Mmap Sidecars Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the BM25 token index and the dense embedding store off the Go heap onto mmap-backed fixed-width files, cutting live heap from 8,748 MB to about 1,204 MB.

**Architecture:** Make each sidecar's on-disk format its memory layout. Both become fixed-width, little-endian, section-addressed files that `Load` maps and reinterprets rather than parses. A shared `internal/mmapslice` package holds the only `unsafe` in the codebase. Phase 1 (Tasks 2-7) converts the token index; Phase 2 (Tasks 8-12) converts the dense store. Task 13 closes both out.

**Tech Stack:** Go 1.26 stdlib only. `syscall.Mmap`, `unsafe.Slice`, `slices.SortFunc`. No new module dependencies — the default build stays pure Go with zero ML/runtime deps.

**Spec:** `docs/moe/specs/2026-09-03-mmap-sidecars-design.md`

## Global Constraints

- **Module is `moedex`, Go 1.26.** No new `go.mod` entries. The default build must stay pure Go stdlib.
- **`Tokenize` is frozen.** Do not change tokenization rules in `internal/tokenindex/tokenindex.go`. It feeds BM25 and changing it shifts ranking.
- **The eight frozen `TokenIndex` signatures keep their shape:** `Build`, `Tokenize`, `NumDocs`, `AvgDocLen`, `DocLen`, `DocFreq`, `TermFreq`, `Save`, `Load`. Add methods; do not alter these.
- **Ripgrep parity must stay exact.** `make verify` green at the end. The token index is not in the parity path, but the gate is the gate.
- **All formats little-endian.** Every integer written with `encoding/binary.LittleEndian`.
- **Target platforms are `darwin/{arm64,amd64}` and `linux/{arm64,amd64}`** (`PLUGIN_TARGETS` in the Makefile). All little-endian. No Windows path required.
- **Commit after every task.** Conventional-commit prefixes (`feat:`, `refactor:`, `test:`, `docs:`, `perf:`).

## Open Decisions

- **D1 — Memory-regression assertion form** · `research` · AFK
  - **Question:** What exactly does the memory regression test assert, given the threshold cannot be known until the CSR reader exists?
  - **Options:** [a] a ratio — live heap after `Load` must be under 20% of the sidecar's on-disk size / [b] an absolute MB ceiling measured at implementation time
  - **Recommendation:** [a]. A ratio needs no magic number, survives corpus growth, and directly encodes the invariant the work buys ("the data is in the mapping, not the heap"). An absolute ceiling silently rots as fixtures change.
  - **Blocked by:** —
  - **Blocks:** Task 7, Task 12
  - **Resolution:** _(fill in at Task 7)_

- **D2 — Width of `termOff` and `postOff`** · `conversation` · HITL
  - **Question:** Keep the CSR offset arrays `uint32`, or widen to `uint64`?
  - **Options:** [a] `uint32` with an explicit Save-time overflow error / [b] `uint64` throughout
  - **Recommendation:** [a]. `uint32` caps `termText` at 4 GiB (currently 190 MB) and postings at 4.29 B (currently 40.6 M — the corpus would need roughly 6 M blobs to reach it). [b] costs a further 141 MB of mapped pages permanently to buy headroom nothing is near. Widening later is a version bump this plan already establishes the machinery for.
  - **Blocked by:** —
  - **Blocks:** Task 2, Task 3
  - **Resolution:** _(needs a yes before dispatch)_

## Not Yet Specified

- Whether `internal/diskstore` should migrate its own hand-rolled `mmapOpenMin`/`mmapRegion` onto `internal/mmapslice` once that package exists. Specifiable after Task 1 shows what the shared surface actually looks like; deliberately not attempted here, because it would put the ripgrep-parity path in a memory-work diff.
- Whether `symbol.Index` (84 MB) deserves the same treatment. Its 5.3x inflation is the same shape as the token index's, but at a scale that does not yet justify a format.

## Out of Scope

- **Promoting int8 to the dense default.** This plan ships int8 as opt-in and measures it. Flipping the default is a separate decision on separate evidence.
- **The remaining 1,204 MB of heap** — blob content (866 MB), `lineStarts` (254 MB), symbol index (84 MB). Blob content is already solved by re-exporting the shard dir as deduped `MOEDEX05` and needs no code.
- **`GOMEMLIMIT` and ONNX arena tuning.** Zero-code operational levers, unrelated to these formats.
- **`internal/diskstore` postings.** Already off-heap since ADR 0005.

---

# Phase 0 — Shared

### Task 1: `internal/mmapslice`

**Files:**
- Create: `internal/mmapslice/mmapslice.go`
- Create: `internal/mmapslice/mmap.go`
- Test: `internal/mmapslice/mmapslice_test.go`
- Test: `internal/mmapslice/fuzz_test.go`

**Interfaces:**
- Consumes: `None`
- Produces:
  - `func Open(path string) (*Mapping, error)`
  - `func (m *Mapping) Bytes() []byte`
  - `func (m *Mapping) Close() error`
  - `func Uint32s(b []byte, n int) ([]uint32, error)`
  - `func Uint64s(b []byte, n int) ([]uint64, error)`
  - `func Float32s(b []byte, n int) ([]float32, error)`
  - `func Int8s(b []byte, n int) ([]int8, error)`
  - `var ErrAlignment, ErrShort, ErrOverflow error`

- [ ] **Step 1: Write the failing tests**

Create `internal/mmapslice/mmapslice_test.go`:

```go
package mmapslice

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUint32sReinterpretsLittleEndian(t *testing.T) {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint32(b[0:], 1)
	binary.LittleEndian.PutUint32(b[4:], 70000)
	binary.LittleEndian.PutUint32(b[8:], 4294967295)
	got, err := Uint32s(b, 3)
	if err != nil {
		t.Fatalf("Uint32s: %v", err)
	}
	want := []uint32{1, 70000, 4294967295}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("elem %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUint32sRejectsShortBuffer(t *testing.T) {
	if _, err := Uint32s(make([]byte, 7), 2); !errors.Is(err, ErrShort) {
		t.Fatalf("want ErrShort, got %v", err)
	}
}

func TestUint32sRejectsMisalignedBuffer(t *testing.T) {
	b := make([]byte, 16)
	// b[1:] is guaranteed misaligned for a 4-byte type.
	if _, err := Uint32s(b[1:], 2); !errors.Is(err, ErrAlignment) {
		t.Fatalf("want ErrAlignment, got %v", err)
	}
}

func TestUint32sRejectsNegativeAndOverflowingCounts(t *testing.T) {
	if _, err := Uint32s(make([]byte, 8), -1); err == nil {
		t.Fatal("want error for negative n")
	}
	if _, err := Uint32s(make([]byte, 8), (1<<62)+1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestZeroLengthIsEmptyNotError(t *testing.T) {
	got, err := Uint32s(nil, 0)
	if err != nil || got != nil {
		t.Fatalf("Uint32s(nil,0) = %v, %v; want nil, nil", got, err)
	}
}

func TestFloat32sReinterprets(t *testing.T) {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:], 0x3F800000) // 1.0
	binary.LittleEndian.PutUint32(b[4:], 0xC0000000) // -2.0
	got, err := Float32s(b, 2)
	if err != nil {
		t.Fatalf("Float32s: %v", err)
	}
	if got[0] != 1.0 || got[1] != -2.0 {
		t.Fatalf("got %v, want [1 -2]", got)
	}
}

func TestInt8sReinterprets(t *testing.T) {
	got, err := Int8s([]byte{0x01, 0xFF, 0x7F, 0x80}, 4)
	if err != nil {
		t.Fatalf("Int8s: %v", err)
	}
	want := []int8{1, -1, 127, -128}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("elem %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestAliasingIsZeroCopy(t *testing.T) {
	b := make([]byte, 4)
	got, err := Uint32s(b, 1)
	if err != nil {
		t.Fatalf("Uint32s: %v", err)
	}
	binary.LittleEndian.PutUint32(b, 0xDEADBEEF)
	if got[0] != 0xDEADBEEF {
		t.Fatalf("slice did not alias the backing array: got %#x", got[0])
	}
}

func TestOpenMapsFileAndCloseUnmaps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.bin")
	payload := []byte("hello mmap")
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(m.Bytes()) != string(payload) {
		t.Fatalf("Bytes() = %q, want %q", m.Bytes(), payload)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close must be a no-op, got %v", err)
	}
}

func TestOpenRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("want error mapping an empty file")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mmapslice/ -count=1`
Expected: FAIL — the package does not exist yet.

- [ ] **Step 3: Implement the reinterpreters**

Create `internal/mmapslice/mmapslice.go`:

```go
// Package mmapslice reinterprets a byte range — typically an mmap'd file — as a
// typed slice without copying, and opens the mappings themselves.
//
// This is the ONLY package in moedex that uses unsafe. It exists so that the
// mmap-backed sidecars (internal/tokenindex, internal/embed) share one audited
// implementation of the same three guards: length, alignment, and byte order.
//
// # Why unsafe rather than encoding/binary
//
// The dense arm scans every stored vector on a query — 953,451 chunks x 768
// dimensions is ~732M float32 reads per search. Decoding each through
// binary.LittleEndian.Uint32 costs a function call and a bounds check per
// element on the hottest loop in the system. Aliasing the mapping is what makes
// mmap-backed vectors viable at all.
//
// # Safety contract
//
// A returned slice ALIASES the caller's bytes. It is valid only while those
// bytes are: (a) still mapped, and (b) not written. Every caller in moedex maps
// PROT_READ and holds the Mapping for the returned slice's whole lifetime.
// Reading a slice after its Mapping is closed dereferences unmapped memory and
// will crash the process, exactly as diskstore's posting sub-slices already do.
package mmapslice

import (
	"errors"
	"fmt"
	"math"
	"unsafe"
)

var (
	// ErrShort means the byte range is too small to hold n elements.
	ErrShort = errors.New("mmapslice: buffer too short")
	// ErrAlignment means the byte range does not start on the element's
	// natural alignment boundary, so aliasing it would be undefined.
	ErrAlignment = errors.New("mmapslice: misaligned buffer")
	// ErrOverflow means n * elementSize does not fit in an int.
	ErrOverflow = errors.New("mmapslice: element count overflows")
)

func init() {
	// Aliasing bytes as multi-byte integers is only correct when the host byte
	// order matches the format's declared little-endian order. Every moedex
	// target (darwin/linux on amd64/arm64) is little-endian; fail loudly rather
	// than silently returning byte-swapped data if that ever stops being true.
	var probe uint16 = 1
	if *(*byte)(unsafe.Pointer(&probe)) != 1 {
		panic("mmapslice: big-endian architecture is not supported")
	}
}

// check validates n elements of size bytes against b and returns the base
// pointer. It is the single place all four reinterpreters get their guards.
func check(b []byte, n, size int) (unsafe.Pointer, error) {
	if n < 0 {
		return nil, fmt.Errorf("mmapslice: negative element count %d", n)
	}
	if n == 0 {
		return nil, nil
	}
	if n > math.MaxInt/size {
		return nil, fmt.Errorf("%w: %d elements of %d bytes", ErrOverflow, n, size)
	}
	need := n * size
	if len(b) < need {
		return nil, fmt.Errorf("%w: need %d bytes, have %d", ErrShort, need, len(b))
	}
	p := unsafe.Pointer(&b[0])
	if size > 1 && uintptr(p)%uintptr(size) != 0 {
		return nil, fmt.Errorf("%w: %d-byte alignment required", ErrAlignment, size)
	}
	return p, nil
}

// Uint32s reinterprets the first n*4 bytes of b as a []uint32 without copying.
func Uint32s(b []byte, n int) ([]uint32, error) {
	p, err := check(b, n, 4)
	if p == nil {
		return nil, err
	}
	return unsafe.Slice((*uint32)(p), n), nil
}

// Uint64s reinterprets the first n*8 bytes of b as a []uint64 without copying.
func Uint64s(b []byte, n int) ([]uint64, error) {
	p, err := check(b, n, 8)
	if p == nil {
		return nil, err
	}
	return unsafe.Slice((*uint64)(p), n), nil
}

// Float32s reinterprets the first n*4 bytes of b as a []float32 without copying.
func Float32s(b []byte, n int) ([]float32, error) {
	p, err := check(b, n, 4)
	if p == nil {
		return nil, err
	}
	return unsafe.Slice((*float32)(p), n), nil
}

// Int8s reinterprets the first n bytes of b as a []int8 without copying. Single
// bytes have no alignment constraint.
func Int8s(b []byte, n int) ([]int8, error) {
	p, err := check(b, n, 1)
	if p == nil {
		return nil, err
	}
	return unsafe.Slice((*int8)(p), n), nil
}
```

- [ ] **Step 4: Implement the mapping**

Create `internal/mmapslice/mmap.go`:

```go
package mmapslice

import (
	"fmt"
	"os"
	"syscall"
)

// Mapping is a read-only mmap of a whole file. Close is idempotent.
//
// The file descriptor is closed immediately after mapping: on both darwin and
// linux the mapping keeps the underlying file alive on its own, so holding the
// fd would only leak a descriptor per open sidecar.
type Mapping struct {
	data []byte
}

// Open maps path read-only in its entirety. An empty file is an error: there is
// nothing to address, and every moedex format has a mandatory header.
func Open(path string) (*Mapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size <= 0 {
		return nil, fmt.Errorf("mmapslice: %s is empty", path)
	}
	if size > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("mmapslice: %s is too large to map (%d bytes)", path, size)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmapslice: mmap %s: %w", path, err)
	}
	return &Mapping{data: data}, nil
}

// Bytes returns the mapped region. The slice is valid only until Close.
func (m *Mapping) Bytes() []byte {
	if m == nil {
		return nil
	}
	return m.data
}

// Close unmaps the region. It is safe to call more than once.
func (m *Mapping) Close() error {
	if m == nil || m.data == nil {
		return nil
	}
	err := syscall.Munmap(m.data)
	m.data = nil
	return err
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/mmapslice/ -count=1 -v`
Expected: PASS, all cases.

- [ ] **Step 6: Add the fuzz test**

The repo has no fuzz tests yet; this is the first. Create `internal/mmapslice/fuzz_test.go`:

```go
package mmapslice

import "testing"

// FuzzReinterpretersNeverPanic drives every reinterpreter with arbitrary
// buffers and counts. The contract is total: any (b, n) either returns a slice
// of exactly n readable elements, or an error. It must never panic and never
// return a slice that reads out of bounds.
func FuzzReinterpretersNeverPanic(f *testing.F) {
	f.Add([]byte{}, 0)
	f.Add([]byte{1, 2, 3, 4}, 1)
	f.Add([]byte{1, 2, 3}, 1)
	f.Add(make([]byte, 64), 16)

	f.Fuzz(func(t *testing.T, b []byte, n int) {
		// Keep n in a range the fuzzer can explore without allocating absurdly.
		if n > 1<<20 || n < -16 {
			t.Skip()
		}
		if s, err := Uint32s(b, n); err == nil {
			if len(s) != n {
				t.Fatalf("Uint32s len %d != n %d", len(s), n)
			}
			for i := range s {
				_ = s[i]
			}
		}
		if s, err := Uint64s(b, n); err == nil {
			if len(s) != n {
				t.Fatalf("Uint64s len %d != n %d", len(s), n)
			}
			for i := range s {
				_ = s[i]
			}
		}
		if s, err := Float32s(b, n); err == nil {
			if len(s) != n {
				t.Fatalf("Float32s len %d != n %d", len(s), n)
			}
			for i := range s {
				_ = s[i]
			}
		}
		if s, err := Int8s(b, n); err == nil {
			if len(s) != n {
				t.Fatalf("Int8s len %d != n %d", len(s), n)
			}
			for i := range s {
				_ = s[i]
			}
		}
	})
}
```

- [ ] **Step 7: Run the fuzzer**

Run: `go test ./internal/mmapslice/ -run FuzzReinterpretersNeverPanic -fuzz FuzzReinterpretersNeverPanic -fuzztime=60s`
Expected: no failures. Then run the seed corpus only: `go test ./internal/mmapslice/ -count=1`.

- [ ] **Step 8: Verify the package passes the repo gates**

Run: `make vet && go test ./internal/depfence -count=1`
Expected: PASS. `internal/mmapslice` is a leaf package importing only stdlib, so no import fence applies to it.

- [ ] **Step 9: Commit**

```bash
git add internal/mmapslice/
git commit -m "feat(mmapslice): zero-copy typed views over mmap'd files

The only unsafe in moedex, behind three guards: length, alignment, and
an init-time little-endian assertion. Shared by the token index and the
dense store so both get the same audited implementation.

Includes the repo's first fuzz test."
```

---

# Phase 1 — Token index

### Task 2: CSR representation, accessors, and the TKI2 codec

**Blocked by:** D2

**Files:**
- Modify: `internal/tokenindex/tokenindex.go:88-96` (the `TokenIndex` struct), `:240-289` (accessors)
- Rewrite: `internal/tokenindex/codec.go`
- Create: `internal/tokenindex/postings.go`
- Test: `internal/tokenindex/tokenindex_test.go` (existing, must keep passing)
- Test: `internal/tokenindex/codec_test.go` (existing, must keep passing)
- Test: `internal/tokenindex/postings_test.go`

**Interfaces:**
- Consumes: `None` (Task 1's package is not used until Task 3)
- Produces:
  - `type PostingList struct` with `Len() int`, `Blob(i int) uint64`, `TF(i int) int`, `TFOf(blob uint64) int`
  - `func (ti *TokenIndex) Postings(term string) PostingList`
  - `func (ti *TokenIndex) Close() error`
  - `var ErrLegacyFormat error`
  - TKI2 on-disk format (128-byte header, six sections)

- [ ] **Step 1: Write the failing test for `PostingList`**

Create `internal/tokenindex/postings_test.go`:

```go
package tokenindex

import (
	"testing"

	"moedex/internal/index"
)

// threeBlobIndex builds a tiny index whose term statistics are known by hand.
func threeBlobIndex(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.AddFile("r", "a.go", "/r/a.go", "sha-a", []byte("alpha beta alpha"))
	ix.AddFile("r", "b.go", "/r/b.go", "sha-b", []byte("beta gamma"))
	ix.AddFile("r", "c.go", "/r/c.go", "sha-c", []byte("gamma"))
	return ix
}

func TestPostingsExposesSortedBlobsAndFrequencies(t *testing.T) {
	ti := Build(threeBlobIndex(t))

	p := ti.Postings("alpha")
	if p.Len() != 1 {
		t.Fatalf("alpha df = %d, want 1", p.Len())
	}
	if p.Blob(0) != 0 || p.TF(0) != 2 {
		t.Fatalf("alpha posting = (blob %d, tf %d), want (0, 2)", p.Blob(0), p.TF(0))
	}

	b := ti.Postings("beta")
	if b.Len() != 2 {
		t.Fatalf("beta df = %d, want 2", b.Len())
	}
	if b.Blob(0) != 0 || b.Blob(1) != 1 {
		t.Fatalf("beta blobs = [%d %d], want ascending [0 1]", b.Blob(0), b.Blob(1))
	}
}

func TestPostingsAbsentTermIsEmptyNotPanic(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	p := ti.Postings("nosuchterm")
	if p.Len() != 0 {
		t.Fatalf("absent term df = %d, want 0", p.Len())
	}
	if p.TFOf(0) != 0 {
		t.Fatalf("absent term TFOf = %d, want 0", p.TFOf(0))
	}
}

func TestTFOfMatchesTermFreq(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	for _, term := range []string{"alpha", "beta", "gamma", "absent"} {
		p := ti.Postings(term)
		for blob := uint64(0); blob < 4; blob++ {
			if got, want := p.TFOf(blob), ti.TermFreq(term, blob); got != want {
				t.Fatalf("TFOf(%q,%d) = %d, TermFreq = %d", term, blob, got, want)
			}
		}
	}
}

func TestTermsAreStoredInLexicographicOrder(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	prev := ""
	for i := 0; i < len(ti.termOff)-1; i++ {
		cur := string(ti.termBytes(i))
		if i > 0 && cur <= prev {
			t.Fatalf("term %d (%q) does not follow %q in ascending order", i, cur, prev)
		}
		prev = cur
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tokenindex/ -run 'TestPostings|TestTFOf|TestTermsAre' -count=1`
Expected: FAIL — `ti.Postings` and `ti.termOff` do not exist.

- [ ] **Step 3: Replace the `TokenIndex` struct with CSR arrays**

In `internal/tokenindex/tokenindex.go`, replace the struct at lines 88-96 and its doc comment:

```go
// TokenIndex holds term statistics over all blobs in a corpus in a compressed
// sparse row (CSR) layout that is identical in memory and on disk.
//
// Storage layout — six parallel arrays, no maps:
//   - docLen:   blob ID -> token count. Dense; 0 for an unused ID, which matches
//     DocLen's "0 if unknown" contract exactly.
//   - termOff:  numTerms+1 offsets into termText. Term i is
//     termText[termOff[i]:termOff[i+1]].
//   - termText: every term's bytes, concatenated in ascending lexicographic
//     order so a term lookup is a binary search.
//   - postOff:  numTerms+1 offsets into postBlob/postTF. Term i's postings are
//     the range [postOff[i], postOff[i+1]).
//   - postBlob: blob IDs, ascending within each term's range.
//   - postTF:   term frequencies, parallel to postBlob.
//
// This replaces a map[string]map[uint64]int that cost 115 bytes per posting.
// Average document frequency is 2.3, so nearly every term paid a whole Go map
// header plus a bucket allocation to hold two entries.
//
// A TokenIndex is either BUILT (arrays on the Go heap, mm == nil) or LOADED
// (arrays aliasing an mmap, mm != nil). Every accessor is identical for both;
// only Close differs. See ADR 0005 for the same discipline applied to postings.
type TokenIndex struct {
	numDocs  int
	totalLen int

	docLen   []uint32
	termOff  []uint32
	termText []byte
	postOff  []uint32
	postBlob []uint32
	postTF   []uint32

	// mm is the mapping backing the arrays above, or nil when they are on the
	// Go heap. Close unmaps it; reading any array after that will crash.
	mm io.Closer
}
```

Add `"io"` and `"bytes"` to the file's imports.

- [ ] **Step 4: Implement `PostingList` and the term lookup**

Create `internal/tokenindex/postings.go`:

```go
package tokenindex

import (
	"bytes"
	"sort"
)

// PostingList is one term's posting range, resolved once. Callers that need a
// term's frequencies for many blobs should resolve the term with Postings and
// then use this handle: the alternative, calling TermFreq per (term, blob)
// pair, re-runs the term binary search every single time.
//
// The slices alias the index's storage and must not be modified.
type PostingList struct {
	blobs []uint32
	tfs   []uint32
}

// Len is the term's document frequency.
func (p PostingList) Len() int { return len(p.blobs) }

// Blob returns the i'th blob ID. Blob IDs ascend with i.
func (p PostingList) Blob(i int) uint64 { return uint64(p.blobs[i]) }

// TF returns the term frequency of the i'th posting.
func (p PostingList) TF(i int) int { return int(p.tfs[i]) }

// TFOf returns the term frequency in blob, or 0 if the term does not occur
// there. O(log df) — prefer a merge walk over Blob/TF when scanning an
// already-sorted candidate set.
func (p PostingList) TFOf(blob uint64) int {
	if blob > 0xFFFFFFFF {
		return 0
	}
	want := uint32(blob)
	i := sort.Search(len(p.blobs), func(i int) bool { return p.blobs[i] >= want })
	if i < len(p.blobs) && p.blobs[i] == want {
		return int(p.tfs[i])
	}
	return 0
}

// termBytes returns term i's raw bytes. They alias termText; do not modify.
func (ti *TokenIndex) termBytes(i int) []byte {
	return ti.termText[ti.termOff[i]:ti.termOff[i+1]]
}

// termIndex binary-searches the sorted term dictionary, returning -1 if absent.
func (ti *TokenIndex) termIndex(term string) int {
	n := len(ti.termOff) - 1
	if n <= 0 {
		return -1
	}
	// One allocation per lookup, not one per probe: bytes.Compare against the
	// stored bytes avoids materialising a string for each of the ~24 probes a
	// 17.6M-term dictionary needs.
	want := []byte(term)
	i := sort.Search(n, func(i int) bool { return bytes.Compare(ti.termBytes(i), want) >= 0 })
	if i < n && bytes.Equal(ti.termBytes(i), want) {
		return i
	}
	return -1
}

// Postings resolves term to its posting range. An absent term yields a zero
// PostingList, whose Len is 0 and whose TFOf always returns 0.
func (ti *TokenIndex) Postings(term string) PostingList {
	i := ti.termIndex(term)
	if i < 0 {
		return PostingList{}
	}
	lo, hi := ti.postOff[i], ti.postOff[i+1]
	return PostingList{blobs: ti.postBlob[lo:hi], tfs: ti.postTF[lo:hi]}
}
```

- [ ] **Step 5: Reimplement the frozen accessors on top of CSR**

In `internal/tokenindex/tokenindex.go`, replace the accessor block (lines 240-289 in the pre-change file) with:

```go
// NumDocs returns the number of documents (blobs) indexed.
func (ti *TokenIndex) NumDocs() int { return ti.numDocs }

// AvgDocLen returns the mean document length in tokens. Zero for an empty corpus.
func (ti *TokenIndex) AvgDocLen() float64 {
	if ti.numDocs == 0 {
		return 0
	}
	return float64(ti.totalLen) / float64(ti.numDocs)
}

// DocLen returns the token count of the given blob (0 if unknown).
func (ti *TokenIndex) DocLen(blob uint64) int {
	if blob >= uint64(len(ti.docLen)) {
		return 0
	}
	return int(ti.docLen[blob])
}

// DocFreq returns how many documents contain term. term must already be a
// single canonical token (the caller runs Tokenize on the query). Returns 0 if
// the term is absent.
func (ti *TokenIndex) DocFreq(term string) int { return ti.Postings(term).Len() }

// TermFreq returns the number of occurrences of term within blob. term must
// already be a single canonical token. Returns 0 if absent.
//
// This resolves the term on every call. A caller looping over many blobs for
// the same term should hold a PostingList from Postings instead.
func (ti *TokenIndex) TermFreq(term string, blob uint64) int {
	return ti.Postings(term).TFOf(blob)
}

// Docs returns the blob IDs that contain term, in ascending order. term must
// already be a single canonical token. Returns nil if the term is absent.
func (ti *TokenIndex) Docs(term string) []uint64 {
	p := ti.Postings(term)
	if p.Len() == 0 {
		return nil
	}
	out := make([]uint64, p.Len())
	for i := range out {
		out[i] = p.Blob(i)
	}
	return out
}

// Close releases the mapping backing a loaded index. It is a no-op for an index
// produced by Build, whose arrays are on the Go heap. Close is idempotent.
// Reading any accessor after Close on a loaded index reads unmapped memory.
func (ti *TokenIndex) Close() error {
	if ti == nil || ti.mm == nil {
		return nil
	}
	err := ti.mm.Close()
	ti.mm = nil
	return err
}
```

- [ ] **Step 6: Make `Build` emit CSR**

Keep `Build`'s existing map-based accumulation for now (Task 4 replaces it) and convert at the end. In `internal/tokenindex/tokenindex.go`, replace the body of `Build`:

```go
// Build tokenizes every blob in ix and accumulates term statistics.
func Build(ix *index.Index) *TokenIndex {
	postings := make(map[string]map[uint64]int)
	docLen := make(map[uint64]int)
	numDocs, totalLen := 0, 0
	n := ix.NumBlobs()
	for id := uint64(0); id < uint64(n); id++ {
		b := ix.Blob(id)
		if b == nil {
			continue
		}
		numDocs++
		toks := Tokenize(b.Content)
		docLen[b.ID] = len(toks)
		totalLen += len(toks)
		for _, t := range toks {
			post := postings[t]
			if post == nil {
				post = make(map[uint64]int)
				postings[t] = post
			}
			post[b.ID]++
		}
	}
	return fromMaps(numDocs, totalLen, docLen, postings)
}

// fromMaps converts accumulated maps into the CSR arrays. Task 4 removes the
// map stage entirely; until then this keeps Build's output shape correct while
// the representation changes underneath it.
func fromMaps(numDocs, totalLen int, docLen map[uint64]int, postings map[string]map[uint64]int) *TokenIndex {
	ti := &TokenIndex{numDocs: numDocs, totalLen: totalLen}

	var maxBlob uint64
	for id := range docLen {
		if id > maxBlob {
			maxBlob = id
		}
	}
	if len(docLen) > 0 {
		ti.docLen = make([]uint32, maxBlob+1)
		for id, l := range docLen {
			ti.docLen[id] = uint32(l)
		}
	}

	terms := make([]string, 0, len(postings))
	for t := range postings {
		terms = append(terms, t)
	}
	sort.Strings(terms)

	numPost := 0
	for _, p := range postings {
		numPost += len(p)
	}
	ti.termOff = make([]uint32, len(terms)+1)
	ti.postOff = make([]uint32, len(terms)+1)
	ti.termText = make([]byte, 0, 16*len(terms))
	ti.postBlob = make([]uint32, 0, numPost)
	ti.postTF = make([]uint32, 0, numPost)

	for i, t := range terms {
		ti.termOff[i] = uint32(len(ti.termText))
		ti.postOff[i] = uint32(len(ti.postBlob))
		ti.termText = append(ti.termText, t...)

		post := postings[t]
		blobs := make([]uint64, 0, len(post))
		for b := range post {
			blobs = append(blobs, b)
		}
		sort.Slice(blobs, func(a, b int) bool { return blobs[a] < blobs[b] })
		for _, b := range blobs {
			ti.postBlob = append(ti.postBlob, uint32(b))
			ti.postTF = append(ti.postTF, uint32(post[b]))
		}
	}
	ti.termOff[len(terms)] = uint32(len(ti.termText))
	ti.postOff[len(terms)] = uint32(len(ti.postBlob))
	return ti
}
```

- [ ] **Step 7: Run the existing package tests**

Run: `go test ./internal/tokenindex/ -count=1`
Expected: the `Postings` tests PASS. `codec_test.go` FAILS, because `Save`/`Load` still target the old struct fields. That is Step 8.

- [ ] **Step 8: Rewrite the codec as TKI2**

Replace `internal/tokenindex/codec.go` entirely:

```go
package tokenindex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TKI2 on-disk format (all integers little-endian).
//
// The format IS the memory layout: every section is a fixed-width array that a
// loader maps and reinterprets rather than parses. See ADR 0005 for the same
// decision applied to positional postings.
//
//	HEADER (128 bytes, zero-padded)
//	  0   magic       [4]byte "TKI2"
//	  4   version     uint32 = 2
//	  8   numDocs     uint64   documents with content (== NumDocs)
//	  16  totalLen    uint64   sum of all document lengths
//	  24  docLenCount uint64   length of the docLen array (maxBlobID+1)
//	  32  numTerms    uint64   distinct terms
//	  40  numPostings uint64   total (term, blob) pairs
//	  48  docLenOff   uint64
//	  56  termOffOff  uint64
//	  64  termTextOff uint64
//	  72  termTextLen uint64
//	  80  postOffOff  uint64
//	  88  postBlobOff uint64
//	  96  postTFOff   uint64
//	  104 fileLen     uint64   total file size, for validation
//	  112 (padding to 128)
//
//	SECTIONS, each starting on a 4-byte boundary, in this order:
//	  docLen   docLenCount   x uint32
//	  termOff  numTerms+1    x uint32
//	  termText termTextLen   x byte
//	  postOff  numTerms+1    x uint32
//	  postBlob numPostings   x uint32
//	  postTF   numPostings   x uint32
//
// Fixed width costs disk (656 MB against the previous varint format's 372 MB on
// the reference corpus) and buys addressability: a loader hands out sub-slices
// instead of decoding 40.6M varints onto the heap.
const (
	tkiMagic      = "TKI2"
	tkiVersion    = 2
	tkiHeaderSize = 128
)

// ErrLegacyFormat means the file is a readable older moedex format that this
// build no longer parses. Callers hold a CACHE: serve's loadPersistedTokens
// already treats any Load error as a miss and rebuilds, so no converter exists
// or is needed.
var ErrLegacyFormat = errors.New("tokenindex: legacy format; rebuild required")

// maxU32 guards every uint32 offset field at write time. Exceeding it means the
// corpus outgrew the format rather than that anything is corrupt, so the error
// names the limit.
const maxU32 = 1<<32 - 1

type tkiHeader struct {
	numDocs     uint64
	totalLen    uint64
	docLenCount uint64
	numTerms    uint64
	numPostings uint64
	docLenOff   uint64
	termOffOff  uint64
	termTextOff uint64
	termTextLen uint64
	postOffOff  uint64
	postBlobOff uint64
	postTFOff   uint64
	fileLen     uint64
}

// align4 rounds n up to the next 4-byte boundary. Every uint32 section must
// start aligned or mmapslice.Uint32s refuses to alias it.
func align4(n uint64) uint64 { return (n + 3) &^ 3 }

// Save writes ti to path in TKI2, via a temp sibling and an atomic rename so a
// crash never leaves a truncated sidecar where a valid one was.
func Save(ti *TokenIndex, path string) error {
	numTerms := uint64(len(ti.termOff))
	if numTerms > 0 {
		numTerms--
	}
	numPostings := uint64(len(ti.postBlob))
	if uint64(len(ti.termText)) > maxU32 {
		return fmt.Errorf("tokenindex: term text %d bytes exceeds the %d-byte TKI2 limit", len(ti.termText), maxU32)
	}
	if numPostings > maxU32 {
		return fmt.Errorf("tokenindex: %d postings exceeds the %d TKI2 limit", numPostings, maxU32)
	}

	h := tkiHeader{
		numDocs:     uint64(ti.numDocs),
		totalLen:    uint64(ti.totalLen),
		docLenCount: uint64(len(ti.docLen)),
		numTerms:    numTerms,
		numPostings: numPostings,
		termTextLen: uint64(len(ti.termText)),
	}
	off := uint64(tkiHeaderSize)
	h.docLenOff = off
	off = align4(off + h.docLenCount*4)
	h.termOffOff = off
	off = align4(off + (h.numTerms+1)*4)
	h.termTextOff = off
	off = align4(off + h.termTextLen)
	h.postOffOff = off
	off = align4(off + (h.numTerms+1)*4)
	h.postBlobOff = off
	off = align4(off + h.numPostings*4)
	h.postTFOff = off
	off = align4(off + h.numPostings*4)
	h.fileLen = off

	buf := make([]byte, h.fileLen)
	copy(buf, tkiMagic)
	le := binary.LittleEndian
	le.PutUint32(buf[4:], tkiVersion)
	le.PutUint64(buf[8:], h.numDocs)
	le.PutUint64(buf[16:], h.totalLen)
	le.PutUint64(buf[24:], h.docLenCount)
	le.PutUint64(buf[32:], h.numTerms)
	le.PutUint64(buf[40:], h.numPostings)
	le.PutUint64(buf[48:], h.docLenOff)
	le.PutUint64(buf[56:], h.termOffOff)
	le.PutUint64(buf[64:], h.termTextOff)
	le.PutUint64(buf[72:], h.termTextLen)
	le.PutUint64(buf[80:], h.postOffOff)
	le.PutUint64(buf[88:], h.postBlobOff)
	le.PutUint64(buf[96:], h.postTFOff)
	le.PutUint64(buf[104:], h.fileLen)

	putU32s(buf[h.docLenOff:], ti.docLen)
	putU32s(buf[h.termOffOff:], ti.termOff)
	copy(buf[h.termTextOff:], ti.termText)
	putU32s(buf[h.postOffOff:], ti.postOff)
	putU32s(buf[h.postBlobOff:], ti.postBlob)
	putU32s(buf[h.postTFOff:], ti.postTF)

	return writeAtomic(path, buf)
}

func putU32s(dst []byte, src []uint32) {
	le := binary.LittleEndian
	for i, v := range src {
		le.PutUint32(dst[i*4:], v)
	}
}

// writeAtomic writes buf to a temp sibling of path, fsyncs it, then renames.
func writeAtomic(path string, buf []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			f.Close()
			os.Remove(tmp)
		}
	}()
	// os.CreateTemp makes the file 0600; match os.Create's umask-respecting 0644.
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// parseHeader validates every section offset against size before any of them is
// used to slice. An mmap'd reader that trusts a bad offset segfaults where a
// parser would have returned an error, so this is the safety boundary.
func parseHeader(b []byte, size uint64) (tkiHeader, error) {
	var h tkiHeader
	if uint64(len(b)) < tkiHeaderSize {
		return h, fmt.Errorf("tokenindex: file too small (%d bytes)", len(b))
	}
	magic := string(b[:4])
	if magic == "TKI1" {
		return h, fmt.Errorf("%w: found TKI1", ErrLegacyFormat)
	}
	if magic != tkiMagic {
		return h, fmt.Errorf("tokenindex: bad magic %q", magic)
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[4:]); v != tkiVersion {
		return h, fmt.Errorf("%w: version %d", ErrLegacyFormat, v)
	}
	h = tkiHeader{
		numDocs:     le.Uint64(b[8:]),
		totalLen:    le.Uint64(b[16:]),
		docLenCount: le.Uint64(b[24:]),
		numTerms:    le.Uint64(b[32:]),
		numPostings: le.Uint64(b[40:]),
		docLenOff:   le.Uint64(b[48:]),
		termOffOff:  le.Uint64(b[56:]),
		termTextOff: le.Uint64(b[64:]),
		termTextLen: le.Uint64(b[72:]),
		postOffOff:  le.Uint64(b[80:]),
		postBlobOff: le.Uint64(b[88:]),
		postTFOff:   le.Uint64(b[96:]),
		fileLen:     le.Uint64(b[104:]),
	}
	if h.fileLen != size {
		return h, fmt.Errorf("tokenindex: header says %d bytes, file is %d", h.fileLen, size)
	}
	sections := []struct {
		name string
		off  uint64
		n    uint64
	}{
		{"docLen", h.docLenOff, h.docLenCount * 4},
		{"termOff", h.termOffOff, (h.numTerms + 1) * 4},
		{"termText", h.termTextOff, h.termTextLen},
		{"postOff", h.postOffOff, (h.numTerms + 1) * 4},
		{"postBlob", h.postBlobOff, h.numPostings * 4},
		{"postTF", h.postTFOff, h.numPostings * 4},
	}
	for _, s := range sections {
		end := s.off + s.n
		if s.off < tkiHeaderSize || end < s.off || end > size {
			return h, fmt.Errorf("tokenindex: section %s [%d,%d) out of bounds for %d-byte file", s.name, s.off, end, size)
		}
	}
	return h, nil
}

// Load reads path into a heap-backed TokenIndex. Task 3 replaces the body with
// an mmap; the signature and semantics are identical either way.
func Load(path string) (*TokenIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h, err := parseHeader(b, uint64(len(b)))
	if err != nil {
		return nil, err
	}
	ti := &TokenIndex{
		numDocs:  int(h.numDocs),
		totalLen: int(h.totalLen),
		termText: b[h.termTextOff : h.termTextOff+h.termTextLen],
	}
	ti.docLen = readU32s(b[h.docLenOff:], int(h.docLenCount))
	ti.termOff = readU32s(b[h.termOffOff:], int(h.numTerms+1))
	ti.postOff = readU32s(b[h.postOffOff:], int(h.numTerms+1))
	ti.postBlob = readU32s(b[h.postBlobOff:], int(h.numPostings))
	ti.postTF = readU32s(b[h.postTFOff:], int(h.numPostings))
	if err := validateCSR(ti); err != nil {
		return nil, err
	}
	return ti, nil
}

func readU32s(b []byte, n int) []uint32 {
	out := make([]uint32, n)
	le := binary.LittleEndian
	for i := range out {
		out[i] = le.Uint32(b[i*4:])
	}
	return out
}

// validateCSR checks the invariants the accessors rely on but the section
// bounds cannot express: offsets must be non-decreasing and must end exactly at
// their array's length. Without this a corrupt file produces a slice expression
// that panics deep inside a query instead of failing at Load.
func validateCSR(ti *TokenIndex) error {
	n := len(ti.termOff) - 1
	if n < 0 || len(ti.postOff) != n+1 {
		return errors.New("tokenindex: term/posting offset arrays disagree")
	}
	for i := 0; i < n; i++ {
		if ti.termOff[i] > ti.termOff[i+1] || ti.postOff[i] > ti.postOff[i+1] {
			return fmt.Errorf("tokenindex: non-monotonic offsets at term %d", i)
		}
	}
	if n >= 0 && len(ti.termOff) > 0 && int(ti.termOff[n]) != len(ti.termText) {
		return errors.New("tokenindex: termOff does not end at termText length")
	}
	if n >= 0 && len(ti.postOff) > 0 && int(ti.postOff[n]) != len(ti.postBlob) {
		return errors.New("tokenindex: postOff does not end at postBlob length")
	}
	if len(ti.postTF) != len(ti.postBlob) {
		return errors.New("tokenindex: postTF and postBlob lengths differ")
	}
	return nil
}
```

- [ ] **Step 9: Update the codec test for round-trip identity**

Replace the body of `internal/tokenindex/codec_test.go` with a round-trip that compares through the public accessors:

```go
package tokenindex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTKI2RoundTripPreservesEveryStatistic(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	p := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(ti, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer got.Close()

	if got.NumDocs() != ti.NumDocs() {
		t.Fatalf("NumDocs %d != %d", got.NumDocs(), ti.NumDocs())
	}
	if got.AvgDocLen() != ti.AvgDocLen() {
		t.Fatalf("AvgDocLen %v != %v", got.AvgDocLen(), ti.AvgDocLen())
	}
	for blob := uint64(0); blob < 4; blob++ {
		if got.DocLen(blob) != ti.DocLen(blob) {
			t.Fatalf("DocLen(%d) %d != %d", blob, got.DocLen(blob), ti.DocLen(blob))
		}
	}
	for _, term := range []string{"alpha", "beta", "gamma", "absent"} {
		if got.DocFreq(term) != ti.DocFreq(term) {
			t.Fatalf("DocFreq(%q) %d != %d", term, got.DocFreq(term), ti.DocFreq(term))
		}
		for blob := uint64(0); blob < 4; blob++ {
			if got.TermFreq(term, blob) != ti.TermFreq(term, blob) {
				t.Fatalf("TermFreq(%q,%d) mismatch", term, blob)
			}
		}
	}
}

func TestLoadRejectsTKI1AsLegacy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.tki")
	// A TKI1 magic plus enough bytes to clear the header-size check.
	buf := make([]byte, tkiHeaderSize)
	copy(buf, "TKI1")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrLegacyFormat) {
		t.Fatalf("want ErrLegacyFormat, got %v", err)
	}
}

func TestSaveLoadEmptyIndex(t *testing.T) {
	ti := Build(nil)
	p := filepath.Join(t.TempDir(), "empty.tki")
	if err := Save(ti, p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer got.Close()
	if got.NumDocs() != 0 || got.DocFreq("anything") != 0 {
		t.Fatal("empty index did not round-trip as empty")
	}
}
```

Note: `Build(nil)` must not panic. If `index.Index` methods do not tolerate a nil receiver, guard the top of `Build` with `if ix == nil { return &TokenIndex{} }`.

- [ ] **Step 10: Run the full package suite**

Run: `go test ./internal/tokenindex/ -count=1 -v`
Expected: PASS.

- [ ] **Step 11: Run the consumers**

Run: `go test ./internal/rank/ ./internal/serve/ ./internal/eval/ -count=1`
Expected: PASS — the frozen accessors kept their shape, so nothing downstream changed.

- [ ] **Step 12: Commit**

```bash
git add internal/tokenindex/
git commit -m "refactor(tokenindex): CSR layout and the TKI2 format

Replaces map[string]map[uint64]int with six parallel arrays that are
identical in memory and on disk. The old layout cost 115 bytes per
posting because average document frequency is 2.3, so nearly every one
of 17.6M terms paid a whole map header to hold two entries.

Adds PostingList so a caller resolves a term once instead of per
(term, blob) pair. The eight frozen signatures are unchanged.

TKI1 files now load as ErrLegacyFormat; serve already treats any load
error as a cache miss and rebuilds."
```

---

### Task 3: Map the token index instead of reading it

**Blocked by:** D2

**Files:**
- Modify: `internal/tokenindex/codec.go` (the `Load` function and `readU32s`)
- Test: `internal/tokenindex/codec_test.go` (add mapping-specific cases)

**Interfaces:**
- Consumes: `mmapslice.Open`, `mmapslice.Uint32s` (Task 1)
- Produces: `Load` returns an index whose arrays alias a mapping; `Close` unmaps.

- [ ] **Step 1: Write the failing test**

Append to `internal/tokenindex/codec_test.go`:

```go
func TestLoadedIndexIsBackedByAMapping(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	p := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(ti, p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.mm == nil {
		t.Fatal("Load returned a heap-backed index; want an mmap-backed one")
	}
	if got.DocFreq("beta") != 2 {
		t.Fatalf("mapped DocFreq(beta) = %d, want 2", got.DocFreq("beta"))
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close must be idempotent, got %v", err)
	}
}

func TestBuiltIndexCloseIsNoOp(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	if ti.mm != nil {
		t.Fatal("Build must not produce a mapped index")
	}
	if err := ti.Close(); err != nil {
		t.Fatalf("Close on a built index: %v", err)
	}
	if ti.DocFreq("beta") != 2 {
		t.Fatal("a built index must stay usable after Close")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tokenindex/ -run 'TestLoadedIndexIsBackedByAMapping|TestBuiltIndexCloseIsNoOp' -count=1`
Expected: FAIL — "Load returned a heap-backed index".

- [ ] **Step 3: Replace `Load` with the mapping path**

In `internal/tokenindex/codec.go`, replace `Load` and delete `readU32s`:

```go
// Load maps path and returns an index whose arrays alias the mapping. Nothing
// but the slice headers enters the Go heap, so a 656 MB sidecar costs a few
// hundred bytes of heap instead of 4.7 GB.
//
// The caller MUST Close the returned index; until then the mapping stays
// resident. Reading any accessor after Close reads unmapped memory and crashes.
func Load(path string) (*TokenIndex, error) {
	m, err := mmapslice.Open(path)
	if err != nil {
		return nil, err
	}
	ti, err := loadFrom(m.Bytes())
	if err != nil {
		m.Close()
		return nil, err
	}
	ti.mm = m
	return ti, nil
}

// loadFrom builds an index over b without owning it. Split out so the fuzz
// target in Task 5 can drive the parser over arbitrary bytes with no file.
func loadFrom(b []byte) (*TokenIndex, error) {
	h, err := parseHeader(b, uint64(len(b)))
	if err != nil {
		return nil, err
	}
	ti := &TokenIndex{
		numDocs:  int(h.numDocs),
		totalLen: int(h.totalLen),
		termText: b[h.termTextOff : h.termTextOff+h.termTextLen],
	}
	if ti.docLen, err = mmapslice.Uint32s(b[h.docLenOff:], int(h.docLenCount)); err != nil {
		return nil, fmt.Errorf("tokenindex: docLen: %w", err)
	}
	if ti.termOff, err = mmapslice.Uint32s(b[h.termOffOff:], int(h.numTerms+1)); err != nil {
		return nil, fmt.Errorf("tokenindex: termOff: %w", err)
	}
	if ti.postOff, err = mmapslice.Uint32s(b[h.postOffOff:], int(h.numTerms+1)); err != nil {
		return nil, fmt.Errorf("tokenindex: postOff: %w", err)
	}
	if ti.postBlob, err = mmapslice.Uint32s(b[h.postBlobOff:], int(h.numPostings)); err != nil {
		return nil, fmt.Errorf("tokenindex: postBlob: %w", err)
	}
	if ti.postTF, err = mmapslice.Uint32s(b[h.postTFOff:], int(h.numPostings)); err != nil {
		return nil, fmt.Errorf("tokenindex: postTF: %w", err)
	}
	if err := validateCSR(ti); err != nil {
		return nil, err
	}
	return ti, nil
}
```

Update the imports: add `"moedex/internal/mmapslice"`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/tokenindex/ -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Verify the mapping is actually released**

Run: `go test ./internal/tokenindex/ -count=1 -race`
Expected: PASS. A missing `Close` shows up here as a leaked mapping under repeated runs, not as a race, so also confirm no test leaves a file open by running the suite twice: `go test ./internal/tokenindex/ -count=2`.

- [ ] **Step 6: Commit**

```bash
git add internal/tokenindex/codec.go internal/tokenindex/codec_test.go
git commit -m "perf(tokenindex): serve the token index from an mmap

Load now maps the TKI2 file and aliases its sections through
mmapslice instead of decoding them onto the heap. The sidecar's
bytes become page cache the GC never scans and the OS can evict.

Close is required on a loaded index and a no-op on a built one."
```

---

### Task 4: Sort-based builder

**Files:**
- Modify: `internal/tokenindex/tokenindex.go` (`Build`, remove `fromMaps`)
- Test: `internal/tokenindex/tokenindex_test.go` (add determinism + equivalence cases)

**Interfaces:**
- Consumes: the CSR fields from Task 2
- Produces: `Build` with no map-of-maps stage. Signature unchanged.

- [ ] **Step 1: Write the failing test**

Append to `internal/tokenindex/tokenindex_test.go`:

```go
func TestBuildIsDeterministic(t *testing.T) {
	a := Build(threeBlobIndex(t))
	b := Build(threeBlobIndex(t))
	if string(a.termText) != string(b.termText) {
		t.Fatal("termText differs between two builds of the same corpus")
	}
	if len(a.postBlob) != len(b.postBlob) {
		t.Fatalf("posting counts differ: %d vs %d", len(a.postBlob), len(b.postBlob))
	}
	for i := range a.postBlob {
		if a.postBlob[i] != b.postBlob[i] || a.postTF[i] != b.postTF[i] {
			t.Fatalf("posting %d differs between builds", i)
		}
	}
}

func TestBuildEmitsAscendingBlobsWithinATerm(t *testing.T) {
	ix := index.New()
	// Same term in several blobs, added so that map iteration order cannot
	// accidentally produce the right answer.
	for i := 0; i < 32; i++ {
		ix.AddFile("r", fmt.Sprintf("f%d.go", i), fmt.Sprintf("/r/f%d.go", i), fmt.Sprintf("sha%d", i), []byte("shared unique"+fmt.Sprint(i)))
	}
	ti := Build(ix)
	p := ti.Postings("shared")
	if p.Len() != 32 {
		t.Fatalf("df = %d, want 32", p.Len())
	}
	for i := 1; i < p.Len(); i++ {
		if p.Blob(i-1) >= p.Blob(i) {
			t.Fatalf("blob ids not ascending at %d: %d then %d", i, p.Blob(i-1), p.Blob(i))
		}
	}
}
```

Add `"fmt"` to that file's imports.

- [ ] **Step 2: Run to verify the tests pass against the current implementation**

Run: `go test ./internal/tokenindex/ -run 'TestBuildIsDeterministic|TestBuildEmitsAscending' -count=1`
Expected: PASS. These characterise behaviour the rewrite must preserve, so they pass before and after. Confirm they pass now, so a failure after Step 3 is unambiguous.

- [ ] **Step 3: Replace `Build` with the sort-based implementation**

In `internal/tokenindex/tokenindex.go`, replace `Build` and delete `fromMaps`:

```go
// postRec is one (term, blob, tf) triple during a build. 12 bytes, so 40.6M
// postings cost 487 MB rather than the 3.5 GB of map overhead the previous
// map[string]map[uint64]int paid for the same information.
type postRec struct {
	term uint32
	blob uint32
	tf   uint32
}

// Build tokenizes every blob in ix and accumulates term statistics.
//
// The build is sort-based rather than map-of-maps: intern each term to a dense
// id, emit one flat record per posting, sort the dictionary lexicographically
// so ids ARE ranks, then sort records by (term, blob) straight into CSR order.
// Peak build memory is about 1.7 GB on the reference corpus against 4.7 GB for
// the previous shape, which is what lets a memory-constrained machine build the
// index at all rather than only serve it.
func Build(ix *index.Index) *TokenIndex {
	ti := &TokenIndex{}
	if ix == nil {
		ti.termOff = []uint32{0}
		ti.postOff = []uint32{0}
		return ti
	}

	dict := make(map[string]uint32)
	var terms []string
	var recs []postRec
	perBlob := make(map[uint32]uint32)

	n := ix.NumBlobs()
	for id := uint64(0); id < uint64(n); id++ {
		b := ix.Blob(id)
		if b == nil {
			continue
		}
		ti.numDocs++
		toks := Tokenize(b.Content)
		for uint64(len(ti.docLen)) <= b.ID {
			ti.docLen = append(ti.docLen, 0)
		}
		ti.docLen[b.ID] = uint32(len(toks))
		ti.totalLen += len(toks)

		clear(perBlob)
		for _, t := range toks {
			tid, ok := dict[t]
			if !ok {
				tid = uint32(len(terms))
				dict[t] = tid
				terms = append(terms, t)
			}
			perBlob[tid]++
		}
		for tid, tf := range perBlob {
			recs = append(recs, postRec{term: tid, blob: uint32(b.ID), tf: tf})
		}
	}

	// Sort the dictionary, then remap ids so that sorting records by term id
	// yields lexicographic term order with no second pass over the text.
	order := make([]uint32, len(terms))
	for i := range order {
		order[i] = uint32(i)
	}
	slices.SortFunc(order, func(a, b uint32) int { return strings.Compare(terms[a], terms[b]) })
	remap := make([]uint32, len(terms))
	for rank, old := range order {
		remap[old] = uint32(rank)
	}
	for i := range recs {
		recs[i].term = remap[recs[i].term]
	}
	slices.SortFunc(recs, func(a, b postRec) int {
		if a.term != b.term {
			return int(a.term) - int(b.term)
		}
		switch {
		case a.blob < b.blob:
			return -1
		case a.blob > b.blob:
			return 1
		}
		return 0
	})

	ti.termOff = make([]uint32, len(terms)+1)
	ti.postOff = make([]uint32, len(terms)+1)
	ti.termText = make([]byte, 0, len(terms)*12)
	ti.postBlob = make([]uint32, 0, len(recs))
	ti.postTF = make([]uint32, 0, len(recs))

	r := 0
	for rank, old := range order {
		ti.termOff[rank] = uint32(len(ti.termText))
		ti.postOff[rank] = uint32(len(ti.postBlob))
		ti.termText = append(ti.termText, terms[old]...)
		for r < len(recs) && int(recs[r].term) == rank {
			ti.postBlob = append(ti.postBlob, recs[r].blob)
			ti.postTF = append(ti.postTF, recs[r].tf)
			r++
		}
	}
	ti.termOff[len(terms)] = uint32(len(ti.termText))
	ti.postOff[len(terms)] = uint32(len(ti.postBlob))
	return ti
}
```

Update imports: add `"slices"` and `"strings"`; drop `"sort"` if nothing else in the file uses it.

- [ ] **Step 4: Run the full package suite**

Run: `go test ./internal/tokenindex/ -count=1 -v`
Expected: PASS, including the determinism and ordering tests from Step 1.

- [ ] **Step 5: Run the consumers**

Run: `go test ./internal/rank/ ./internal/serve/ ./internal/eval/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tokenindex/
git commit -m "perf(tokenindex): sort-based build, 4.7GB -> ~1.7GB peak

Intern terms to dense ids, emit one flat 12-byte record per posting,
sort the dictionary so ids are ranks, then sort records straight into
CSR order. Removes the map-of-maps from the build path, so a machine
that can serve the index can now also build it."
```

---

### Task 5: Harden the reader against corrupt and truncated files

**Files:**
- Create: `internal/tokenindex/fuzz_test.go`
- Test: `internal/tokenindex/codec_test.go` (add truncation cases)

**Interfaces:**
- Consumes: `loadFrom` (Task 3)
- Produces: `None`

- [ ] **Step 1: Write the truncation and corruption tests**

Append to `internal/tokenindex/codec_test.go`:

```go
func TestLoadRejectsTruncatedFileAtEveryPrefix(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	dir := t.TempDir()
	full := filepath.Join(dir, "full.tki")
	if err := Save(ti, full); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	// Every proper prefix must be rejected, never panic and never succeed.
	for n := 0; n < len(b); n++ {
		p := filepath.Join(dir, "trunc.tki")
		if err := os.WriteFile(p, b[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Load(p)
		if err == nil {
			got.Close()
			t.Fatalf("prefix of %d bytes loaded successfully; want an error", n)
		}
	}
}

func TestLoadRejectsOutOfBoundsSectionOffset(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.tki")
	if err := Save(ti, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// postBlobOff (header offset 88) points past the end of the file.
	binary.LittleEndian.PutUint64(b[88:], uint64(len(b))+4096)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("an out-of-bounds section offset loaded successfully")
	}
}

func TestLoadRejectsNonMonotonicOffsets(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.tki")
	if err := Save(ti, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseHeader(b, uint64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	// Make termOff[0] larger than termOff[1].
	binary.LittleEndian.PutUint32(b[h.termOffOff:], 0xFFFF)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("non-monotonic offsets loaded successfully")
	}
}
```

Add `"encoding/binary"` to that file's imports.

- [ ] **Step 2: Run to verify they pass or expose gaps**

Run: `go test ./internal/tokenindex/ -run 'TestLoadRejects' -count=1 -v`
Expected: PASS. If any case fails, the bounds check it exposes is a real bug — fix `parseHeader` or `validateCSR` before moving on.

- [ ] **Step 3: Add the fuzz target**

Create `internal/tokenindex/fuzz_test.go`:

```go
package tokenindex

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoadFromNeverPanics drives the TKI2 parser over arbitrary bytes. The
// reader hands mmap'd sections straight to callers as slices, so a bad offset
// that survives validation becomes a segfault inside a query rather than an
// error at load. The contract is total: any input either parses into a usable
// index or returns an error.
func FuzzLoadFromNeverPanics(f *testing.F) {
	ti := Build(nil)
	dir := f.TempDir()
	p := filepath.Join(dir, "seed.tki")
	if err := Save(ti, p); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
			if len(b) > 8 {
				f.Add(b[:len(b)/2])
			}
		}
	}
	f.Add([]byte("TKI2"))
	f.Add(make([]byte, tkiHeaderSize))

	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := loadFrom(b)
		if err != nil {
			return
		}
		// A successfully parsed index must be safe to exercise fully.
		_ = got.NumDocs()
		_ = got.AvgDocLen()
		for blob := uint64(0); blob < 8; blob++ {
			_ = got.DocLen(blob)
		}
		for i := 0; i < len(got.termOff)-1; i++ {
			term := string(got.termBytes(i))
			p := got.Postings(term)
			for j := 0; j < p.Len(); j++ {
				_ = p.Blob(j)
				_ = p.TF(j)
			}
			_ = p.TFOf(0)
		}
	})
}
```

- [ ] **Step 4: Run the fuzzer**

Run: `go test ./internal/tokenindex/ -run FuzzLoadFromNeverPanics -fuzz FuzzLoadFromNeverPanics -fuzztime=120s`
Expected: no crashers. Any crasher is a genuine bug — fix it, and commit the generated `testdata/fuzz/` corpus entry alongside the fix.

- [ ] **Step 5: Run the seed corpus in the normal suite**

Run: `go test ./internal/tokenindex/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tokenindex/
git commit -m "test(tokenindex): fuzz and truncation coverage for the TKI2 reader

An mmap reader that trusts a bad offset segfaults where a parser would
have returned an error, so the bounds checks are the safety property.
Covers every truncation prefix, out-of-bounds section offsets, and
non-monotonic CSR offsets."
```

---

### Task 6: Galloping-cursor BM25 in the ranker

**Files:**
- Modify: `internal/rank/ranker.go:447-489` (`lexicalArm`), `:533-550` (`tokenCandidateBlobs`)
- Test: `internal/rank/ranker_test.go` (add an equivalence test)

**Interfaces:**
- Consumes: `tokenindex.PostingList` (Task 2)
- Produces: `None` — behaviour is identical; only the traversal changes.

- [ ] **Step 1: Write the equivalence test**

Append to `internal/rank/ranker_test.go`:

```go
// TestLexicalArmMatchesNaiveBM25 pins the galloping-cursor rewrite against a
// straightforward per-pair implementation. The cursor walk is only valid
// because candidateBlobs and tokenCandidateBlobs both return ascending blob
// ids and CSR posting lists are ascending too; if either ordering is ever lost
// this test is what catches it.
func TestLexicalArmMatchesNaiveBM25(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "a.go", "/r/a.go", "sha-a", []byte("refund refund payment gateway"))
	ix.AddFile("r", "b.go", "/r/b.go", "sha-b", []byte("payment gateway timeout"))
	ix.AddFile("r", "c.go", "/r/c.go", "sha-c", []byte("refund policy"))
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})

	terms := tokenindex.Tokenize([]byte("refund payment"))
	got := r.lexicalArm(terms)

	// Naive reference: same candidates, same idf, per-pair TermFreq.
	cand := r.candidateBlobs(terms)
	seen := map[string]bool{}
	var distinct []string
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			distinct = append(distinct, t)
		}
	}
	n := float64(ti.NumDocs())
	avgdl := ti.AvgDocLen()
	want := map[uint64]float64{}
	for _, blob := range cand {
		score := 0.0
		dl := float64(ti.DocLen(blob))
		denomBase := r.cfg.K1 * (1 - r.cfg.B + r.cfg.B*dl/avgdl)
		for _, t := range distinct {
			df := float64(ti.DocFreq(t))
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			tf := float64(ti.TermFreq(t, blob))
			if tf == 0 {
				continue
			}
			score += idf * (tf * (r.cfg.K1 + 1)) / (tf + denomBase)
		}
		if score > 0 {
			want[blob] = score
		}
	}

	if len(got) != len(want) {
		t.Fatalf("got %d scored blobs, want %d", len(got), len(want))
	}
	for _, s := range got {
		w, ok := want[s.blob]
		if !ok {
			t.Fatalf("blob %d scored but not in the naive result", s.blob)
		}
		if math.Abs(s.score-w) > 1e-12 {
			t.Fatalf("blob %d score %v != naive %v", s.blob, s.score, w)
		}
	}
}
```

`rank.New` is `func New(ix *index.Index, ti *tokenindex.TokenIndex, store *embed.Store, emb embed.Embedder, cfg Config) *Ranker` (`internal/rank/ranker.go:193`), and it applies `cfg.withDefaults()`, so `Config{}` yields the documented K1=1.2 / B=0.75.

- [ ] **Step 2: Run to verify it passes against the current implementation**

Run: `go test ./internal/rank/ -run TestLexicalArmMatchesNaiveBM25 -count=1`
Expected: PASS. This characterises the behaviour the rewrite must preserve.

- [ ] **Step 3: Rewrite `lexicalArm` to use resolved posting lists**

In `internal/rank/ranker.go`, replace the scoring loop inside `lexicalArm` (everything from `idf := make(...)` to the `sortByScoreThenBlob` call):

```go
	// idf depends only on the term and the corpus, not on the candidate blob,
	// so it is computed once per distinct term.
	//
	// Each term's posting list is ALSO resolved once, here. Calling
	// ti.TermFreq(t, blob) per (blob, term) pair would re-run the term binary
	// search over the whole dictionary every time; with 17.6M terms that is
	// ~24 string comparisons per pair. Instead each term keeps a cursor into
	// its own ascending posting list, and because cand is ascending too, the
	// whole nested loop is one merge walk: O(len(cand) + sum of df).
	idf := make([]float64, len(distinct))
	lists := make([]tokenindex.PostingList, len(distinct))
	cursor := make([]int, len(distinct))
	for i, t := range distinct {
		lists[i] = r.ti.Postings(t)
		df := float64(lists[i].Len())
		idf[i] = math.Log(1 + (n-df+0.5)/(df+0.5))
	}

	out := make([]lexScore, 0, len(cand))
	for _, blob := range cand {
		score := 0.0
		dl := float64(r.ti.DocLen(blob))
		denomBase := r.cfg.K1 * (1 - r.cfg.B + r.cfg.B*dl/avgdl)
		for i := range distinct {
			p := lists[i]
			// Advance this term's cursor to the first posting >= blob.
			c := cursor[i]
			for c < p.Len() && p.Blob(c) < blob {
				c++
			}
			cursor[i] = c
			if c >= p.Len() || p.Blob(c) != blob {
				continue // tf == 0
			}
			tf := float64(p.TF(c))
			denom := tf + denomBase
			score += idf[i] * (tf * (r.cfg.K1 + 1)) / denom
		}
		if score > 0 {
			out = append(out, lexScore{blob: blob, score: score})
		}
	}
	sortByScoreThenBlob(out, func(s lexScore) float64 { return s.score }, func(s lexScore) uint64 { return s.blob })
	return out
}
```

- [ ] **Step 4: Rewrite `tokenCandidateBlobs` to union posting lists directly**

Replace its body:

```go
func (r *Ranker) tokenCandidateBlobs(terms []string) []uint64 {
	seen := map[uint64]bool{}
	for _, t := range terms {
		p := r.ti.Postings(t)
		for i := 0; i < p.Len(); i++ {
			seen[p.Blob(i)] = true
		}
	}
	out := make([]uint64, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

This drops the per-term `[]uint64` allocation and sort that `ti.Docs` performed; the ascending output contract is unchanged.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/rank/ -count=1 -v`
Expected: PASS, including `TestLexicalArmMatchesNaiveBM25`.

- [ ] **Step 6: Verify the ascending-candidates precondition is actually enforced**

The cursor walk is only correct if `cand` ascends. Add a guard test:

```go
func TestCandidateBlobsAreAscending(t *testing.T) {
	ix := index.New()
	for i := 0; i < 40; i++ {
		ix.AddFile("r", fmt.Sprintf("f%d.go", i), fmt.Sprintf("/r/f%d.go", i), fmt.Sprintf("sha%d", i), []byte("refund payment gateway"))
	}
	ti := tokenindex.Build(ix)
	r := New(ix, ti, nil, nil, Config{})
	terms := tokenindex.Tokenize([]byte("refund payment"))
	for _, cand := range [][]uint64{r.candidateBlobs(terms), r.tokenCandidateBlobs(terms)} {
		for i := 1; i < len(cand); i++ {
			if cand[i-1] >= cand[i] {
				t.Fatalf("candidates not ascending at %d: %d then %d", i, cand[i-1], cand[i])
			}
		}
	}
}
```

Run: `go test ./internal/rank/ -run TestCandidateBlobsAreAscending -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/rank/
git commit -m "perf(rank): merge-walk BM25 instead of per-pair term lookup

Resolve each distinct query term to a PostingList once, then walk the
ascending candidate set with a cursor per term. The inner loop drops
from a dictionary lookup per (blob, term) pair to O(cand + sum df).

Pinned by an equivalence test against a naive per-pair implementation
and by a guard that both candidate generators stay ascending."
```

---

### Task 7: Wire the lifecycle and prove the memory win

**Blocked by:** D1

**Files:**
- Modify: `internal/serve/rankcorpus.go:97-104` (`Close`), `:584-624` (persisted-sidecar helpers)
- Modify: `internal/app/mcpcmd/main.go` (add `defer ti.Close()`)
- Modify: `internal/eval/runner.go` (add `defer ti.Close()`)
- Test: `internal/tokenindex/memory_test.go`
- Test: `internal/serve/rankcorpus_test.go` (add a close-releases-mapping case)

**Interfaces:**
- Consumes: `TokenIndex.Close` (Task 2)
- Produces: `RankCorpus.Close` releases the token-index mapping.

- [ ] **Step 1: Resolve D1 and write the memory regression test**

Record the resolution in the plan header, then create `internal/tokenindex/memory_test.go`:

```go
package tokenindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"moedex/internal/index"
)

// TestLoadedIndexHeapIsAFractionOfFileSize is the memory invariant this whole
// format exists to buy: a loaded index must keep its data in the mapping, not
// on the Go heap.
//
// The assertion is a RATIO rather than an absolute ceiling (decision D1): a
// ratio needs no magic number, survives corpus growth, and states the actual
// invariant. Before this change the ratio was about 12.6x; the ceiling here is
// 0.20, which a regression back to heap-resident arrays cannot satisfy.
func TestLoadedIndexHeapIsAFractionOfFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a multi-thousand-blob fixture")
	}
	ix := index.New()
	for i := 0; i < 4000; i++ {
		content := fmt.Sprintf("package p%d\nfunc HandleRefundRequest%d(ctx Context) error {\n\treturn processPaymentGateway%d(ctx)\n}\n", i, i, i)
		ix.AddFile("r", fmt.Sprintf("f%d.go", i), fmt.Sprintf("/r/f%d.go", i), fmt.Sprintf("sha%d", i), []byte(content))
	}
	p := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(Build(ix), p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ti, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ti.Close()
	// Touch every accessor so nothing is lazily deferred past the measurement.
	if ti.NumDocs() == 0 {
		t.Fatal("loaded an empty index")
	}
	for i := 0; i < len(ti.termOff)-1; i++ {
		_ = ti.Postings(string(ti.termBytes(i))).Len()
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew < 0 {
		grew = 0
	}
	ratio := float64(grew) / float64(fi.Size())
	t.Logf("file %d bytes, heap grew %d bytes, ratio %.3f", fi.Size(), grew, ratio)
	if ratio > 0.20 {
		t.Fatalf("loaded index put %.1f%% of its file on the heap; want under 20%% (the data must live in the mapping)", ratio*100)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/tokenindex/ -run TestLoadedIndexHeapIsAFractionOfFileSize -count=1 -v`
Expected: PASS, with a logged ratio well under 0.20.

- [ ] **Step 3: Thread `Close` through `RankCorpus`**

In `internal/serve/rankcorpus.go`, replace `Close`:

```go
// Close releases every mapping backing the corpus: the shared content store
// (deduped dirs), and the token index when it was loaded from a persisted
// sidecar rather than built. Close is idempotent and safe on a corpus over a
// legacy dir, where content is heap-copied and Close is a no-op.
//
// The daemon's rankSnapshot.retire drains readers before calling this, so a
// hot reload never unmaps under a live query.
func (rc *RankCorpus) Close() error {
	var firstErr error
	if rc.ti != nil {
		if err := rc.ti.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if rc.content != nil {
		if err := rc.content.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		rc.content = nil
	}
	return firstErr
}
```

- [ ] **Step 4: Add `defer Close()` at the other build sites**

In `internal/eval/runner.go` and `internal/app/mcpcmd/main.go`, every `tokenindex.Build(ix)` result that is not handed to a longer-lived owner gets a `defer ti.Close()`. `Close` is a no-op for built indexes, so this documents the contract uniformly rather than only where it currently bites.

- [ ] **Step 5: Add the serve-level close test**

Append to `internal/serve/rankcorpus_test.go`:

```go
func TestRankCorpusCloseReleasesTokenIndexMapping(t *testing.T) {
	dir := buildDedupedDir(t, map[string]map[string]string{
		"repoA": {"a.go": "refund payment gateway"},
		"repoB": {"b.go": "payment gateway timeout"},
	})
	rc, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close must be idempotent, got %v", err)
	}
}
```

`buildDedupedDir(t, repos)` is the existing fixture helper at `internal/serve/rankcorpus_test.go:22`; do not add a new one.

- [ ] **Step 6: Run the affected suites**

Run: `go test ./internal/tokenindex/ ./internal/serve/ ./internal/eval/ ./internal/app/... -count=1`
Expected: PASS.

- [ ] **Step 7: Benchmark the BM25 arm against the pre-change baseline**

Run: `go test ./internal/serve/ -run '^$' -bench BenchmarkRankArms -benchmem -count=5 > /tmp/after.txt`
Then check out the merge-base and repeat into `/tmp/before.txt`, and compare. Record both numbers in the Task 13 ADR. Expected: allocations per op down (no per-term `Docs` slice), time per op flat or better.

- [ ] **Step 8: Full gate**

Run: `make health`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/tokenindex/ internal/serve/ internal/eval/ internal/app/
git commit -m "feat(serve): release the token-index mapping on Close

RankCorpus.Close now releases the token index alongside the content
store. The daemon's existing rankSnapshot.retire already drains
readers before calling it, so a hot reload cannot unmap under a live
query -- no new lifecycle machinery was required.

Adds the memory regression test: a loaded index must keep under 20%
of its file size on the heap (decision D1)."
```

---

# Phase 2 — Dense store

### Task 8: Flatten the vector block in memory

**Files:**
- Modify: `internal/embed/embed.go:347-352` (`Store`), `:1010-1060` (`Search`), `KeyVectors`
- Modify: `internal/embed/codec.go` (`Save`, `LoadStore` to read into a flat block)
- Test: `internal/embed/embed_test.go`

**Interfaces:**
- Consumes: `None`
- Produces:
  - `func (s *Store) vecAt(i int) []float32`
  - `Store.vec []float32` flat block replacing `vectors []Vector`

- [ ] **Step 1: Write the failing test**

Append to `internal/embed/embed_test.go`:

```go
func TestStoreUsesAFlatVectorBlock(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
	})
	if len(s.vec) != 12 {
		t.Fatalf("flat block length %d, want 12", len(s.vec))
	}
	for i := 0; i < s.Len(); i++ {
		v := s.vecAt(i)
		if len(v) != 4 {
			t.Fatalf("vecAt(%d) length %d, want 4", i, len(v))
		}
		if v[i] != 1 {
			t.Fatalf("vecAt(%d)[%d] = %v, want 1", i, i, v[i])
		}
	}
}

func TestVecAtAliasesTheBlock(t *testing.T) {
	s := storeFromVectors(t, 2, [][]float32{{1, 0}, {0, 1}})
	s.vec[0] = 9
	if got := s.vecAt(0)[0]; got != 9 {
		t.Fatalf("vecAt did not alias the block: got %v", got)
	}
}
```

Add this helper to the test file; Tasks 9-12 reuse it:

```go
// storeFromVectors builds an in-memory Store over the given vectors: one Chunk
// and one distinct ChunkKey per vector, and a single flat float32 block.
func storeFromVectors(t *testing.T, dim int, vecs [][]float32) *Store {
	t.Helper()
	s := &Store{
		dim:    dim,
		chunks: make([]Chunk, len(vecs)),
		keys:   make([]ChunkKey, len(vecs)),
		vec:    make([]float32, 0, len(vecs)*dim),
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Fatalf("vector %d has dim %d, want %d", i, len(v), dim)
		}
		s.chunks[i] = Chunk{
			Blob:      uint64(i),
			StartLine: 1,
			EndLine:   2,
			StartByte: 0,
			EndByte:   len(v),
		}
		s.keys[i][0] = byte(i)
		s.keys[i][1] = byte(i >> 8)
		s.vec = append(s.vec, v...)
	}
	return s
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/embed/ -run 'TestStoreUsesAFlatVectorBlock|TestVecAtAliases' -count=1`
Expected: FAIL — `s.vec` and `s.vecAt` do not exist.

- [ ] **Step 3: Replace the slice-of-slices with a flat block**

In `internal/embed/embed.go`, change the `Store` struct:

```go
// Store is a flat, brute-force cosine index over chunk embeddings.
//
// Vectors live in ONE contiguous float32 block rather than a slice of slices.
// The old shape cost 953,451 separate 3,072-byte allocations plus 23 MB of
// slice headers on the reference corpus, and made a mapped backing impossible.
// A flat block also makes a mixed-dimension store structurally impossible,
// which is what lets Search drop its per-query validation sweep.
type Store struct {
	dim    int
	chunks []Chunk
	keys   []ChunkKey
	vec    []float32 // len == len(chunks)*dim; unit-normalized, row-major
}

// vecAt returns chunk i's vector as a sub-slice of the flat block. The result
// aliases the block and must not be modified.
func (s *Store) vecAt(i int) []float32 {
	return s.vec[i*s.dim : (i+1)*s.dim]
}
```

Update `Len` to `return len(s.chunks)`, and every construction site (`BuildStore`, incremental refresh) to append into `s.vec` instead of appending a `Vector`.

- [ ] **Step 4: Delete the per-query validation loop in `Search`**

In `internal/embed/embed.go`, remove this block from `Search` (currently lines 1025-1032):

```go
	for i, v := range s.vectors {
		if len(v) != s.dim {
			return nil, fmt.Errorf("embed: chunk %d vector dim %d != store dim %d", i, len(v), s.dim)
		}
	}
```

Replace the comment above it with:

```go
	// No per-vector dimension sweep: the flat block has exactly len(chunks)*dim
	// entries by construction, so a mixed-dimension store cannot be represented.
	// The old check walked all 953,451 vectors on EVERY query to find nothing.
```

Then change the scoring loop's `s.vectors[i]` reads to `s.vecAt(i)`.

- [ ] **Step 5: Update `KeyVectors` and `Save`/`LoadStore`**

`KeyVectors` returns `m[k] = s.vecAt(i)` — still aliasing, as its doc already promises. `Save` writes `s.vec` contiguously (it already wrote vectors contiguously, so the bytes are unchanged). `LoadStore` reads into one `make([]float32, count*dim)` instead of per-chunk allocations.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/embed/ -count=1 -v`
Expected: PASS. The on-disk bytes are unchanged, so existing MDXE v2 round-trip tests must still pass.

- [ ] **Step 7: Run the consumers**

Run: `go test ./internal/rank/ ./internal/serve/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/embed/
git commit -m "refactor(embed): one flat float32 block instead of 953k slices

Removes 953,451 allocations and 23 MB of slice headers, and deletes
Search's per-query dimension sweep over every vector -- a flat block
makes a mixed-dimension store unrepresentable, so the check had
nothing left to find. On-disk bytes are unchanged."
```

---

### Task 9: MDXE v3 format

**Files:**
- Modify: `internal/embed/codec.go` (header, `Save`, `LoadStore`)
- Modify: `internal/embed/embed.go` (`Store` gains `quant`, `mm`, and `Close`)
- Test: `internal/embed/codec_test.go`

**Interfaces:**
- Consumes: Task 8's flat block
- Produces: MDXE v3 (64-byte header, `quant` field, explicit section offsets, 64-byte-aligned vector block); `embed.ErrLegacyFormat`; `func (s *Store) Close() error`

- [ ] **Step 1: Write the failing round-trip and alignment tests**

Append to `internal/embed/codec_test.go`:

```go
func TestMDXEv3RoundTrip(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadStore(p)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	defer got.Close()
	if got.Dim() != s.Dim() || got.Len() != s.Len() {
		t.Fatalf("dim/len mismatch: (%d,%d) vs (%d,%d)", got.Dim(), got.Len(), s.Dim(), s.Len())
	}
	for i := 0; i < s.Len(); i++ {
		a, b := s.vecAt(i), got.vecAt(i)
		for j := range a {
			if a[j] != b[j] {
				t.Fatalf("vector %d component %d: %v != %v", i, j, a[j], b[j])
			}
		}
	}
}

func TestMDXEv3VectorBlockIs64ByteAligned(t *testing.T) {
	s := storeFromVectors(t, 3, [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parseStoreHeader(b, uint64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if h.vecOff%64 != 0 {
		t.Fatalf("vector block at offset %d is not 64-byte aligned", h.vecOff)
	}
}

func TestLoadStoreRejectsV1AndV2AsLegacy(t *testing.T) {
	for _, version := range []uint32{1, 2} {
		p := filepath.Join(t.TempDir(), "old.store")
		buf := make([]byte, storeHeaderSize)
		copy(buf, "MDXE")
		binary.LittleEndian.PutUint32(buf[4:], version)
		if err := os.WriteFile(p, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(p); !errors.Is(err, ErrLegacyFormat) {
			t.Fatalf("version %d: want ErrLegacyFormat, got %v", version, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/embed/ -run 'TestMDXEv3|TestLoadStoreRejectsV1' -count=1`
Expected: FAIL — `parseStoreHeader`, `storeHeaderSize`, and `ErrLegacyFormat` do not exist.

- [ ] **Step 3: Define the v3 header**

In `internal/embed/codec.go`, replace the format comment and constants:

```go
// MDXE v3 on-disk format (all integers little-endian).
//
//	HEADER (64 bytes, zero-padded)
//	  0  magic     [4]byte "MDXE"
//	  4  version   uint32 = 3
//	  8  dim       uint32
//	  12 count     uint32   number of chunks
//	  16 quant     uint8    0 = float32, 1 = int8 + per-vector scale
//	  17 (7 bytes reserved, zero)
//	  24 chunksOff uint64
//	  32 keysOff   uint64
//	  40 vecOff    uint64   ALWAYS a multiple of 64
//	  48 scaleOff  uint64   quant==1 only; 0 when quant==0
//	  56 fileLen   uint64
//
//	SECTIONS
//	  chunks count x 32 bytes: blob u64, startLine u32, endLine u32,
//	                           startByte u64, endByte u64
//	  keys   count x 16 bytes
//	  vec    quant==0: count*dim x float32   (64-byte aligned)
//	         quant==1: count*dim x int8
//	  scale  quant==1: count x float32
//
// vecOff is padded to 64 bytes so mmapslice.Float32s can alias the block and
// each vector starts on a cache line. Versions 1 and 2 load as ErrLegacyFormat:
// the store is a CACHE whose .meta validator already forces a rebuild on any
// load failure, so no converter exists.
const (
	storeMagic4     = "MDXE"
	storeVersion    = 3
	storeHeaderSize = 64
	quantF32        = 0
	quantInt8       = 1
	chunkRecordSize = 32
	keyRecordSize   = 16
	vecAlign        = 64
)

// ErrLegacyFormat means the file is an older MDXE version this build no longer
// parses. Callers treat it as a cache miss and re-embed.
var ErrLegacyFormat = errors.New("embed: legacy store format; rebuild required")

type storeHeader struct {
	dim       uint32
	count     uint32
	quant     uint8
	chunksOff uint64
	keysOff   uint64
	vecOff    uint64
	scaleOff  uint64
	fileLen   uint64
}

func alignUp(n, to uint64) uint64 { return (n + to - 1) &^ (to - 1) }
```

- [ ] **Step 3b: Give `Store` the fields v3 needs**

In `internal/embed/embed.go`, add to the `Store` struct and add `Close`. The
mapping is always nil until Task 10 populates it; introducing the field and the
method here keeps this task's own round-trip test compilable and makes `Close`
part of the contract from the moment the format exists.

```go
	quant uint8     // quantF32 or quantInt8; how vec/vecI8 is stored
	mm    io.Closer // mapping backing the vectors, or nil when heap-built
```

```go
// Close releases the mapping backing a loaded store. It is a no-op for a store
// produced by BuildStore, whose vectors are on the Go heap. Close is
// idempotent. Reading vectors after Close on a mapped store reads unmapped
// memory.
func (s *Store) Close() error {
	if s == nil || s.mm == nil {
		return nil
	}
	err := s.mm.Close()
	s.mm = nil
	return err
}
```

Add `"io"` to that file's imports.

- [ ] **Step 4: Implement `Save` for v3**

Compute the layout, allocate one buffer, fill it, and reuse the existing atomic temp-and-rename write (keep the current `Save`'s temp/chmod/rename block verbatim — it already handles the multi-GB torn-write hazard).

```go
func (s *Store) Save(path string) error {
	if len(s.chunks) > 0 && !s.HasKeys() {
		return fmt.Errorf("embed: refusing to save store without content keys (%d chunks, %d keys); call FillKeys first", len(s.chunks), len(s.keys))
	}
	count := uint64(len(s.chunks))
	dim := uint64(s.dim)

	h := storeHeader{dim: uint32(s.dim), count: uint32(count), quant: quantF32}
	off := uint64(storeHeaderSize)
	h.chunksOff = off
	off += count * chunkRecordSize
	h.keysOff = off
	off += count * keyRecordSize
	h.vecOff = alignUp(off, vecAlign)
	off = h.vecOff + count*dim*4
	h.fileLen = off

	buf := make([]byte, h.fileLen)
	le := binary.LittleEndian
	copy(buf, storeMagic4)
	le.PutUint32(buf[4:], storeVersion)
	le.PutUint32(buf[8:], h.dim)
	le.PutUint32(buf[12:], h.count)
	buf[16] = h.quant
	le.PutUint64(buf[24:], h.chunksOff)
	le.PutUint64(buf[32:], h.keysOff)
	le.PutUint64(buf[40:], h.vecOff)
	le.PutUint64(buf[48:], h.scaleOff)
	le.PutUint64(buf[56:], h.fileLen)

	for i, c := range s.chunks {
		r := buf[h.chunksOff+uint64(i)*chunkRecordSize:]
		le.PutUint64(r[0:], c.Blob)
		le.PutUint32(r[8:], uint32(c.StartLine))
		le.PutUint32(r[12:], uint32(c.EndLine))
		le.PutUint64(r[16:], uint64(c.StartByte))
		le.PutUint64(r[24:], uint64(c.EndByte))
	}
	for i, k := range s.keys {
		copy(buf[h.keysOff+uint64(i)*keyRecordSize:], k[:])
	}
	for i, v := range s.vec {
		le.PutUint32(buf[h.vecOff+uint64(i)*4:], math.Float32bits(v))
	}
	return writeStoreAtomic(path, buf)
}
```

Move the existing temp-file/chmod/rename logic into `writeStoreAtomic(path string, buf []byte) error`.

- [ ] **Step 5: Implement `parseStoreHeader` and `LoadStore`**

```go
// parseStoreHeader validates every section against size before any offset is
// used to slice. Same safety boundary as the token index's parseHeader.
func parseStoreHeader(b []byte, size uint64) (storeHeader, error) {
	var h storeHeader
	if uint64(len(b)) < storeHeaderSize {
		return h, fmt.Errorf("embed: file too small (%d bytes)", len(b))
	}
	if string(b[:4]) != storeMagic4 {
		return h, fmt.Errorf("embed: bad magic %q", b[:4])
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[4:]); v != storeVersion {
		return h, fmt.Errorf("%w: version %d", ErrLegacyFormat, v)
	}
	h = storeHeader{
		dim:       le.Uint32(b[8:]),
		count:     le.Uint32(b[12:]),
		quant:     b[16],
		chunksOff: le.Uint64(b[24:]),
		keysOff:   le.Uint64(b[32:]),
		vecOff:    le.Uint64(b[40:]),
		scaleOff:  le.Uint64(b[48:]),
		fileLen:   le.Uint64(b[56:]),
	}
	if h.fileLen != size {
		return h, fmt.Errorf("embed: header says %d bytes, file is %d", h.fileLen, size)
	}
	if h.quant != quantF32 && h.quant != quantInt8 {
		return h, fmt.Errorf("embed: unknown quantization %d", h.quant)
	}
	if h.vecOff%vecAlign != 0 {
		return h, fmt.Errorf("embed: vector block at %d is not %d-byte aligned", h.vecOff, vecAlign)
	}
	n := uint64(h.count)
	d := uint64(h.dim)
	vecBytes := n * d * 4
	if h.quant == quantInt8 {
		vecBytes = n * d
	}
	sections := []struct {
		name string
		off  uint64
		n    uint64
	}{
		{"chunks", h.chunksOff, n * chunkRecordSize},
		{"keys", h.keysOff, n * keyRecordSize},
		{"vec", h.vecOff, vecBytes},
	}
	if h.quant == quantInt8 {
		sections = append(sections, struct {
			name string
			off  uint64
			n    uint64
		}{"scale", h.scaleOff, n * 4})
	}
	for _, s := range sections {
		end := s.off + s.n
		if s.off < storeHeaderSize || end < s.off || end > size {
			return h, fmt.Errorf("embed: section %s [%d,%d) out of bounds for %d-byte file", s.name, s.off, end, size)
		}
	}
	return h, nil
}

// LoadStore reads path into a heap-backed Store. Task 10 maps it instead.
func LoadStore(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h, err := parseStoreHeader(b, uint64(len(b)))
	if err != nil {
		return nil, err
	}
	return storeFrom(h, b, nil)
}
```

Add `storeFrom(h storeHeader, b []byte, mm io.Closer) (*Store, error)` that decodes chunks and keys into slices and reads the vector block into a flat `[]float32`. Task 10 changes only how `vec` is obtained.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/embed/ -count=1 -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/embed/
git commit -m "feat(embed): MDXE v3 with explicit sections and a quant field

64-byte header carrying per-section offsets, a quantization field, and
a vector block padded to a 64-byte boundary so it can be aliased. v1
and v2 now load as ErrLegacyFormat; the .meta validator already treats
a load failure as a cache miss and re-embeds."
```

---

### Task 10: Map the dense store

**Files:**
- Modify: `internal/embed/codec.go` (`LoadStore`, `storeFrom`)
- Modify: `internal/embed/embed.go` (`storeFrom` wiring only; `mm`/`Close` land in Task 9)
- Test: `internal/embed/codec_test.go`
- Test: `internal/embed/refresh_test.go`
- Create: `internal/embed/fuzz_test.go`

**Interfaces:**
- Consumes: `mmapslice.Open`, `mmapslice.Float32s` (Task 1)
- Produces: `LoadStore` returns a store whose vector block aliases a mapping.

- [ ] **Step 1: Write the failing tests**

Append to `internal/embed/codec_test.go`:

```go
func TestLoadedStoreIsBackedByAMapping(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.mm == nil {
		t.Fatal("LoadStore returned a heap-backed store; want mmap-backed")
	}
	if got.vecAt(1)[1] != 1 {
		t.Fatalf("mapped vector read back wrong: %v", got.vecAt(1))
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := got.Close(); err != nil {
		t.Fatalf("Close must be idempotent, got %v", err)
	}
}

func TestBuiltStoreCloseIsNoOp(t *testing.T) {
	s := storeFromVectors(t, 2, [][]float32{{1, 0}})
	if err := s.Close(); err != nil {
		t.Fatalf("Close on a built store: %v", err)
	}
	if s.Len() != 1 {
		t.Fatal("a built store must stay usable after Close")
	}
}
```

Create `internal/embed/refresh_test.go` for the aliasing hazard the spec names:

```go
package embed

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveOverAnOpenMappedStoreKeepsItReadable pins the RefreshEmbeddings
// hazard: a refresh reuses vectors from the CURRENTLY OPEN store while writing
// its replacement to the same path. Save writes a temp sibling and renames, so
// the old mapping stays valid for the whole write -- unlinked but mapped. That
// is load-bearing rather than incidental, so it gets a test.
func TestSaveOverAnOpenMappedStoreKeepsItReadable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.store")

	orig := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	if err := orig.Save(p); err != nil {
		t.Fatal(err)
	}
	open, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()

	// Build a replacement that ALIASES the open mapping, as a refresh does.
	reused := open.vecAt(0)
	next := storeFromVectors(t, 4, [][]float32{reused, {0, 0, 1, 0}})
	if err := next.Save(p); err != nil {
		t.Fatalf("Save over an open store: %v", err)
	}

	// The still-open mapping must remain readable and unchanged.
	if got := open.vecAt(0); got[0] != 1 || got[1] != 0 {
		t.Fatalf("open mapping changed under a rename: %v", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("replacement not in place: %v", err)
	}
	fresh, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.Len() != 2 || fresh.vecAt(1)[2] != 1 {
		t.Fatal("replacement store did not round-trip")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/embed/ -run 'TestLoadedStoreIsBackedByAMapping|TestBuiltStoreCloseIsNoOp|TestSaveOverAnOpenMappedStore' -count=1`
Expected: FAIL — `Store.mm` and `Store.Close` do not exist.

- [ ] **Step 3: Confirm the lifecycle surface from Task 9**

`Store.mm` and `Store.Close` already exist (Task 9, Step 3b) and `Close` is
currently always a no-op because nothing sets `mm`. This task is what makes it
real. No new declarations are needed here.

- [ ] **Step 4: Map in `LoadStore`**

```go
// LoadStore maps path and returns a store whose vector block aliases the
// mapping. On the reference corpus that moves 2.93 GB off the Go heap.
//
// The caller MUST Close the returned store.
func LoadStore(path string) (*Store, error) {
	m, err := mmapslice.Open(path)
	if err != nil {
		return nil, err
	}
	b := m.Bytes()
	h, err := parseStoreHeader(b, uint64(len(b)))
	if err != nil {
		m.Close()
		return nil, err
	}
	s, err := storeFrom(h, b, m)
	if err != nil {
		m.Close()
		return nil, err
	}
	return s, nil
}
```

In `storeFrom`, when `mm != nil` and `h.quant == quantF32`, obtain the block with `mmapslice.Float32s(b[h.vecOff:], int(count)*int(dim))` and set `s.mm = mm`.

**Deviation from the spec, taken deliberately.** The spec says chunks and keys
"decode on touch". This plan decodes them EAGERLY into `[]Chunk` and
`[]ChunkKey` at load instead. Decode-on-touch would need a second backing
(`chunksRaw []byte` plus a `chunkAt(i)` accessor and a separate `count` field)
threaded through `Search`, `KeyVectors`, and `Save`, to save 953,451 x 40 bytes
= about 38 MB against a 2.93 GB store — 1.3%. Eager decode keeps one
representation for chunks and costs 38 MB. If that 38 MB is later wanted, it is
a contained follow-up, not a redesign.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/embed/ -count=1 -v`
Expected: PASS, including the refresh-aliasing test.

- [ ] **Step 5b: Fuzz and truncation-test the store reader**

The spec's third gate requires fuzzing BOTH readers, not just the token index.
Create `internal/embed/fuzz_test.go`:

```go
package embed

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseStoreHeaderNeverPanics drives the MDXE v3 parser over arbitrary
// bytes. LoadStore hands the vector block out as a slice aliasing the mapping,
// so an offset that survives validation becomes a segfault inside a query
// rather than an error at load.
func FuzzParseStoreHeaderNeverPanics(f *testing.F) {
	s := &Store{dim: 2, chunks: make([]Chunk, 0), keys: make([]ChunkKey, 0)}
	dir := f.TempDir()
	p := filepath.Join(dir, "seed.store")
	if err := s.Save(p); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
			if len(b) > 8 {
				f.Add(b[:len(b)/2])
			}
		}
	}
	f.Add([]byte("MDXE"))
	f.Add(make([]byte, storeHeaderSize))

	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := parseStoreHeader(b, uint64(len(b)))
		if err != nil {
			return
		}
		got, err := storeFrom(h, b, nil)
		if err != nil {
			return
		}
		// A successfully parsed store must be safe to exercise fully.
		for i := 0; i < got.Len(); i++ {
			v := got.vecAt(i)
			for j := range v {
				_ = v[j]
			}
		}
	})
}

func TestLoadStoreRejectsTruncatedFileAtEveryPrefix(t *testing.T) {
	s := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	dir := t.TempDir()
	full := filepath.Join(dir, "full.store")
	if err := s.Save(full); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(b); n++ {
		p := filepath.Join(dir, "trunc.store")
		if err := os.WriteFile(p, b[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := LoadStore(p)
		if err == nil {
			got.Close()
			t.Fatalf("prefix of %d bytes loaded successfully; want an error", n)
		}
	}
}
```

Run: `go test ./internal/embed/ -run FuzzParseStoreHeaderNeverPanics -fuzz FuzzParseStoreHeaderNeverPanics -fuzztime=120s`
Expected: no crashers. Then `go test ./internal/embed/ -count=1`.

- [ ] **Step 6: Commit**

```bash
git add internal/embed/
git commit -m "perf(embed): serve dense vectors from an mmap

LoadStore maps the MDXE v3 file and aliases its 64-byte-aligned vector
block through mmapslice. 2.93 GB leaves the Go heap and becomes
evictable page cache.

Pins the RefreshEmbeddings hazard: a refresh aliases the open mapping
while writing its replacement, and temp-plus-rename keeps the old
mapping readable throughout."
```

---

### Task 11: int8 quantization, opt-in

**Files:**
- Create: `internal/embed/quantize.go`
- Modify: `internal/embed/codec.go` (`Save` honours a quant setting; `storeFrom` handles `quantInt8`)
- Modify: `internal/embed/embed.go` (`Search` dots mixed f32 x int8)
- Test: `internal/embed/quantize_test.go`

**Interfaces:**
- Consumes: MDXE v3 `quant` field (Task 9)
- Produces:
  - `func Quantize(v []float32) ([]int8, float32)`
  - `func (s *Store) SaveQuantized(path string) error`
  - `Store.vecI8 []int8`, `Store.scales []float32`

- [ ] **Step 1: Write the failing tests**

Create `internal/embed/quantize_test.go`:

```go
package embed

import (
	"math"
	"path/filepath"
	"testing"
)

func TestQuantizeRoundTripsWithinTolerance(t *testing.T) {
	v := normalize([]float32{0.5, -0.25, 0.8, -0.1, 0.3, 0.02})
	q, scale := Quantize(v)
	if len(q) != len(v) {
		t.Fatalf("quantized length %d, want %d", len(q), len(v))
	}
	for i := range v {
		got := float32(q[i]) * scale
		if math.Abs(float64(got-v[i])) > float64(scale) {
			t.Fatalf("component %d: %v vs %v (scale %v)", i, got, v[i], scale)
		}
	}
}

func TestQuantizeHandlesZeroVector(t *testing.T) {
	q, scale := Quantize([]float32{0, 0, 0})
	if scale != 0 {
		t.Fatalf("zero vector scale = %v, want 0", scale)
	}
	for i, x := range q {
		if x != 0 {
			t.Fatalf("component %d = %d, want 0", i, x)
		}
	}
}

func TestInt8StoreCosineTracksFloat32(t *testing.T) {
	vecs := [][]float32{
		normalize([]float32{1, 0.2, 0.1, 0}),
		normalize([]float32{0, 1, 0.3, 0.1}),
		normalize([]float32{0.4, 0.4, 1, 0.2}),
	}
	f := storeFromVectors(t, 4, vecs)
	dir := t.TempDir()
	pf := filepath.Join(dir, "f32.store")
	pq := filepath.Join(dir, "int8.store")
	if err := f.Save(pf); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveQuantized(pq); err != nil {
		t.Fatal(err)
	}
	qs, err := LoadStore(pq)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()
	if qs.quant != quantInt8 {
		t.Fatalf("quant = %d, want %d", qs.quant, quantInt8)
	}

	q := normalize([]float32{1, 0.2, 0.1, 0})
	for i := range vecs {
		want := dot(q, f.vecAt(i))
		got := qs.scoreAgainst(q, i)
		// int8 with a per-vector scale keeps ~2 decimal places on unit vectors.
		if math.Abs(float64(got-want)) > 0.02 {
			t.Fatalf("chunk %d: int8 cosine %v vs f32 %v", i, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/embed/ -run 'TestQuantize|TestInt8Store' -count=1`
Expected: FAIL — `Quantize`, `SaveQuantized`, and `scoreAgainst` do not exist.

- [ ] **Step 3: Implement quantization**

Create `internal/embed/quantize.go`:

```go
package embed

import "math"

// Quantize maps a unit-normalized vector to int8 with a single per-vector
// scale, so v[i] is recovered as float32(q[i]) * scale to within scale/2.
//
// Scoring stays MIXED: the query stays float32 and only the stored vector is
// quantized. Quantizing both would compound the error for no further memory
// saving, since the query vector is one transient allocation.
func Quantize(v []float32) ([]int8, float32) {
	var max float32
	for _, x := range v {
		if a := float32(math.Abs(float64(x))); a > max {
			max = a
		}
	}
	q := make([]int8, len(v))
	if max == 0 {
		return q, 0
	}
	scale := max / 127
	for i, x := range v {
		r := math.Round(float64(x / scale))
		if r > 127 {
			r = 127
		}
		if r < -128 {
			r = -128
		}
		q[i] = int8(r)
	}
	return q, scale
}

// scoreAgainst returns the cosine of query q against stored chunk i, reading
// whichever representation this store holds.
func (s *Store) scoreAgainst(q []float32, i int) float32 {
	if s.quant == quantF32 {
		return dot(q, s.vecAt(i))
	}
	row := s.vecI8[i*s.dim : (i+1)*s.dim]
	var sum float32
	for j, x := range row {
		sum += q[j] * float32(x)
	}
	return sum * s.scales[i]
}
```

Add `vecI8 []int8` and `scales []float32` to `Store` (`quant` already exists from Task 9). Implement `SaveQuantized` as `Save` with `h.quant = quantInt8`, writing the int8 block plus the scale array, and extend `storeFrom` to alias `vecI8` via `mmapslice.Int8s` and `scales` via `mmapslice.Float32s`.

- [ ] **Step 4: Route `Search` through `scoreAgainst`**

Replace the `dot(q, s.vecAt(i))` call inside `Search`'s parallel scoring loop with `s.scoreAgainst(q, i)`. Behaviour for `quantF32` is byte-identical.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/embed/ -count=1 -v`
Expected: PASS.

- [ ] **Step 6: Measure the recall delta**

Run the dense measurement gate against both representations:

Run: `go test ./internal/eval -run '^TestDenseHybridMeasurement$' -count=1 -v`
Record NDCG@10 and recall@10 for f32. Repeat with an int8-quantized store and record the delta. Write both numbers into the Task 13 ADR. **Do not change the default** — int8 stays opt-in regardless of the result; promoting it is explicitly Out of Scope.

- [ ] **Step 7: Commit**

```bash
git add internal/embed/
git commit -m "feat(embed): opt-in int8 quantization for dense vectors

Per-vector scale, mixed float32 query against int8 stored vectors, so
error is not compounded. 2.93 GB -> 736 MB mapped when enabled.

Ships opt-in and measured: the eval delta is recorded, and promoting
int8 to the default is a separate decision on separate evidence."
```

---

### Task 12: Wire the dense lifecycle and prove the second win

**Blocked by:** D1

**Files:**
- Modify: `internal/serve/rankcorpus.go` (`Close`)
- Test: `internal/embed/memory_test.go`
- Test: `internal/serve/rankcorpus_test.go`

**Interfaces:**
- Consumes: `Store.Close` (Task 10)
- Produces: `RankCorpus.Close` releases all three mappings.

- [ ] **Step 1: Write the memory regression test**

Create `internal/embed/memory_test.go`:

```go
package embed

import (
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoadedStoreHeapIsAFractionOfFileSize is the dense half of the same
// invariant Task 7 pins for the token index (decision D1): a loaded store keeps
// its vectors in the mapping, not on the heap. Before this change the store
// held its vectors 1:1 on the heap, so the ratio was about 1.0.
func TestLoadedStoreHeapIsAFractionOfFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a multi-thousand-vector fixture")
	}
	const (
		dim   = 128
		count = 8000
	)
	rng := rand.New(rand.NewSource(1))
	vecs := make([][]float32, count)
	for i := range vecs {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vecs[i] = normalize(v)
	}
	s := storeFromVectors(t, dim, vecs)
	p := filepath.Join(t.TempDir(), "e.store")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	got, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	// Touch every vector so nothing is deferred past the measurement.
	var sink float32
	for i := 0; i < got.Len(); i++ {
		sink += got.vecAt(i)[0]
	}
	_ = sink

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew < 0 {
		grew = 0
	}
	ratio := float64(grew) / float64(fi.Size())
	t.Logf("file %d bytes, heap grew %d bytes, ratio %.3f", fi.Size(), grew, ratio)
	// Chunks and keys stay decoded on the heap by design (48 bytes per chunk
	// against dim*4 = 512 bytes of vector), so the ceiling is above the token
	// index's 0.20.
	if ratio > 0.25 {
		t.Fatalf("loaded store put %.1f%% of its file on the heap; want under 25%%", ratio*100)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/embed/ -run TestLoadedStoreHeapIsAFractionOfFileSize -count=1 -v`
Expected: PASS, with a logged ratio well under 0.25.

- [ ] **Step 3: Add the store to `RankCorpus.Close`**

```go
func (rc *RankCorpus) Close() error {
	var firstErr error
	if rc.ti != nil {
		if err := rc.ti.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if rc.store != nil {
		if err := rc.store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if rc.content != nil {
		if err := rc.content.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		rc.content = nil
	}
	return firstErr
}
```

- [ ] **Step 4: Benchmark the dense scan**

Run: `go test ./internal/embed/ -run '^$' -bench . -benchmem -count=5`
Compare against the merge-base. Expected: f32 scan time flat or better (the deleted validation loop is pure removal); int8 scan roughly 4x faster.

- [ ] **Step 5: Run the affected suites**

Run: `go test ./internal/embed/ ./internal/serve/ ./internal/rank/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/embed/ internal/serve/
git commit -m "feat(serve): release the dense-store mapping on Close

RankCorpus.Close now releases all three mappings: content store, token
index, and dense store. Adds the dense memory regression test."
```

---

### Task 13: Close out both phases

**Files:**
- Create: `docs/adr/0025-mmap-bm25-and-dense-sidecars.md`
- Modify: `docs/adr/README.md` (index the new ADR)
- Modify: `ARCHITECTURE.md` (the on-disk formats table: TKI1 -> TKI2, MDXE v2 -> v3)
- Modify: `docs/moe/specs/2026-09-03-mmap-sidecars-design.md` (Status line)

**Interfaces:**
- Consumes: every prior task
- Produces: `None`

- [ ] **Step 1: Re-measure the real corpus**

Rebuild the sidecars and re-run the attribution probe against `~/.moedex-index/shards-managed`, recording live heap per component. Expected: token index and dense store each near zero; total live heap near 1,204 MB against the 8,748 MB baseline.

- [ ] **Step 2: Run the ranking-parity gate**

Run: `go test ./internal/eval -run '^TestCorpusGoldGate$' -count=1 -v`
Run: `go test ./internal/eval -run '^TestFixtureMeasurement$' -count=1 -v`
Expected: PASS, with IR metrics identical to the pre-change run for the f32 path. Any NDCG or recall movement on the f32 path is a bug, not a tuning result — the arithmetic is unchanged.

- [ ] **Step 3: Run the master gate**

Run: `make verify`
Expected: PASS. Requires the corpus at `MOEDEX_CORPUS` and `rg` on PATH.

- [ ] **Step 4: Write ADR 0025**

Follow the house shape of `docs/adr/0005-mmap-compact-postings.md`: Status / Date / Context owner, then Context, Decision, Consequences (Positive, Negative / costs), Evidence, Related. The Evidence section carries the real before/after numbers from Steps 1-2 and the benchmarks from Task 7 Step 7 and Task 12 Step 4. Related must link 0005, whose Negative section named these sidecars as the remaining binding constraint.

- [ ] **Step 5: Update `ARCHITECTURE.md`**

In the on-disk formats table, change the Token index row's magic from `TKI1` to `TKI2` and describe the six CSR sections; change the Embedding store row from `MDXE (v1)` to `MDXE (v3)` and note the quant field and 64-byte-aligned vector block. Add `mmapslice` to the package map.

- [ ] **Step 6: Update the spec's Status line**

Change it to record what actually landed, with the measured numbers, and what did not.

- [ ] **Step 7: Full gate and commit**

```bash
make health && make verify
git add docs/ ARCHITECTURE.md
git commit -m "docs(adr): record ADR 0025, mmap'd BM25 and dense sidecars

Live heap 8,748 MB -> [measured] on the 492-shard corpus. Closes the
gap ADR 0005 named in its own Negative section: it moved postings off
the heap and left the corpus-wide sidecars as the binding constraint."
```
