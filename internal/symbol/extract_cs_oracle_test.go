package symbol

// Original source SHA256: ebb9a0ecaa66ce79bc20219ca6aa744b44d8964ba3f955bf58eddef85a3f6201.
// Frozen from extract_cs.go before the October 1 latency optimization. Keep this
// test oracle independent of optimized matching, masks, and body-range helpers.
// Only unchanged scan.go primitives are shared. The original regex semantics,
// output order, nil slices, and byte offsets are the compatibility contract.

import (
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"moedex/internal/diskstore"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// frozenOracleCSharpExtractor extracts C# type and method definitions with a dependency-free
// regex/byte scan (no Roslyn, no cgo, no third-party). It is best-effort: it
// favors precision on the common shapes seen in the configured corpus
// (public/internal classes, interfaces, structs, enums, records, and method
// declarations) over completeness. On anything it can't confidently match it
// emits fewer symbols rather than wrong ones, and it always returns a nil error.
//
// Name offsets: every pattern captures the declared identifier as a submatch,
// and FindAllSubmatchIndex gives the byte offsets of that submatch directly into
// content, so NameStart/NameEnd slice the exact identifier.
//
// Body ranges: from the match end we look for the next '{' on the same logical
// declaration; if found we brace-match it (matchBlock, which skips strings and
// comments). If there is no block (interface/abstract method, expression-bodied
// member ending in ';', or a record with no body) we fall back to the
// declaration line's byte range so BodyStart<=NameStart<BodyEnd still holds.
type frozenOracleCSharpExtractor struct{}

// frozenOraclecsLiteralMask treats interpolated strings as literals, like the shared
// scanner, but recognizes C# verbatim and raw delimiters. In particular, quotes
// embedded in raw test-source strings must not expose declarations as code.
func frozenOraclecsLiteralMask(content []byte) []bool {
	return literalMaskWithStringSkipper(content, frozenOraclecsSkipString)
}

func frozenOraclecsSkipString(content []byte, i int, quote byte) int {
	if quote != '"' {
		return skipString(content, i, quote)
	}
	n := len(content)
	verbatim := i > 0 && content[i-1] == '@' || i > 1 && content[i-1] == '$' && content[i-2] == '@'
	if verbatim {
		for j := i + 1; j < n; j++ {
			if content[j] == '"' {
				if j+1 < n && content[j+1] == '"' {
					j++ // doubled quotes are content; backslashes are ordinary bytes
					continue
				}
				return j + 1
			}
		}
		return n
	}
	end := i
	for end < n && content[end] == '"' {
		end++
	}
	quotes := end - i
	if quotes >= 3 {
		// Only whitespace followed by a newline makes this a multiline raw
		// literal. Recover an unterminated single-line literal at its newline,
		// rather than hiding all subsequent declarations through EOF.
		first := end
		for first < n && (content[first] == ' ' || content[first] == '\t' || content[first] == '\v' || content[first] == '\f') {
			first++
		}
		multiline := first < n && (content[first] == '\r' || content[first] == '\n')
		for j := end; j < n; {
			if !multiline && (content[j] == '\r' || content[j] == '\n') {
				return j
			}
			if content[j] != '"' {
				j++
				continue
			}
			start := j
			for j < n && content[j] == '"' {
				j++
			}
			if j-start >= quotes {
				return j
			}
		}
		return n
	}
	return skipString(content, i, quote)
}

// frozenOraclecsTypeRe matches a C# type declaration and captures the type name. It is
// anchored at line start (after optional whitespace) and consumes leading
// modifiers (public, abstract, sealed, partial, etc.) loosely, like
// frozenOraclecsMethodRe, so a `where T : class` constraint or `class`/`record`/...
// tokens appearing mid-line (e.g. in prose) never match. `record` may be
// `record class`/`record struct`; the optional group absorbs that. Matches
// starting inside a comment or string literal are additionally dropped via
// literalMask in Extract.
var frozenOraclecsTypeRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|sealed|abstract|partial|unsafe|new|readonly)\s+)*(class|interface|struct|enum|record)(?:\s+(?:class|struct))?\s+([A-Za-z_][A-Za-z0-9_]*)`)

// frozenOraclecsMethodRe matches a C# method declaration and captures the method name. It
// requires a return type token then Name( and is anchored at line start after
// whitespace. Access/modifier keywords are consumed loosely. The negative
// guards (excluding control-flow keywords as the "name") avoid matching
// `if (`, `while (`, etc. Constructors are matched too (return type == the
// type name reads as the return-type token), which is acceptable for ranking.
var frozenOraclecsMethodRe = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|async|partial|extern|unsafe|new|readonly)\s+)*[A-Za-z_][A-Za-z0-9_<>,.\[\] ?]*?\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^>]*>)?\s*\(`)

// frozenOraclecsControlKeywords are tokens frozenOraclecsMethodRe can falsely capture as a method name
// because `keyword (` looks like `Name(`. They are filtered out.
var frozenOraclecsControlKeywords = map[string]bool{
	"if": true, "for": true, "foreach": true, "while": true, "switch": true,
	"catch": true, "using": true, "lock": true, "fixed": true, "return": true,
	"do": true, "else": true, "get": true, "set": true, "nameof": true,
	"typeof": true, "sizeof": true, "default": true, "new": true,
}

// frozenOraclecsStatementKeywords are tokens that can appear in a STATEMENT but never
// anywhere in a member declaration's PREFIX (its modifiers plus return type).
// They filter the mirror-image false positive of frozenOraclecsControlKeywords: that map
// rejects a keyword captured as the method NAME (`if (` reading as `Name(`),
// while this one rejects a statement whose head was mistaken for a return type.
//
// The return-type character class must admit a space — `Dictionary<string, int>
// Foo(` is a real declaration — and `\s+` matches newlines, so the slot happily
// swallows a multi-token statement head:
//
//	throw new ArgumentException("x");     -> type "throw new",       name ArgumentException
//	return new Widget(1);                -> type "return new",      name Widget
//	yield return new Thing(2);           -> type "yield return new", name Thing
//	await SendAsync(x);                  -> type "await",           name SendAsync
//	id ?? throw new ArgumentException(   -> type "id ?? throw new", name ArgumentException
//
// Each emitted a bogus Method definition AND suppressed the real reference
// (frozenOraclecsReferencesFromDefs skips offsets a definition claimed). Measured on the
// configured corpus (491 repos), the shapes above accounted for 72,255
// spurious definitions — 29% of every definition in the corpus. Examples:
// ArgumentException reported 785 "definitions" across 88 repos (now 0, all 900
// occurrences being references), and a single generated file of
// `new WhoIsBatchCriteria(...)` lines reported 49,286 (now 2: the real class and
// its constructor).
//
// Every reserved word here is illegal as a C# identifier, so it can never be a
// real type name and rejecting it in any prefix position is safe; `await`,
// `var`, and `yield` are contextual rather than reserved, but none of them can
// appear in a declaration prefix either. `new` is deliberately NOT in this set —
// it is a legal modifier (member hiding: `public new void M()`) — and is handled
// positionally by frozenOraclecsDeclPrefixOK.
var frozenOraclecsStatementKeywords = map[string]bool{
	"throw": true, "return": true, "await": true, "yield": true, "var": true,
	"if": true, "else": true, "for": true, "foreach": true, "while": true,
	"do": true, "switch": true, "case": true, "lock": true, "using": true,
	"fixed": true, "try": true, "catch": true, "finally": true, "goto": true,
	"break": true, "continue": true, "base": true, "this": true,
	"checked": true, "unchecked": true, "typeof": true, "nameof": true,
	"sizeof": true, "default": true, "is": true, "as": true, "in": true,
	"out": true, "stackalloc": true,
}

// frozenOraclecsDeclPrefixOK reports whether the bytes in [from, to) — everything a
// frozenOraclecsMethodRe match consumed before the captured name, i.e. the modifiers and
// return type — read like a real declaration prefix rather than the head of a
// statement. It walks the identifier tokens and rejects on two grounds:
//
//   - any token is a statement keyword (see frozenOraclecsStatementKeywords). Scanning EVERY
//     token, not just the first, is what catches a continuation line such as
//     `subjectId ?? throw new ArgumentException(`, where the statement keyword
//     sits mid-prefix behind an ordinary identifier.
//   - the LAST token is `new`, which means `new Name(` — an object creation.
//     `new` is a legal modifier, but it can never be the return TYPE, so a
//     prefix ending in it is never a declaration. `public new void M()` keeps
//     `void` as its last token and is unaffected. This also independently covers
//     every `... new Name(` shape above.
//
// A prefix whose tokens are all modifiers is accepted: that is how a constructor
// (`public Foo(int x)`, where `public` lands in the return-type slot) matches.
func frozenOraclecsDeclPrefixOK(content []byte, from, to int) bool {
	if from < 0 || to > len(content) {
		return true // defensive: never drop a symbol over a bad offset
	}
	last := ""
	for i := from; i < to; {
		if !frozenOraclecsIdentByte(content[i]) {
			i++
			continue
		}
		start := i
		for i < to && frozenOraclecsIdentByte(content[i]) {
			i++
		}
		tok := string(content[start:i])
		if frozenOraclecsStatementKeywords[tok] {
			return false
		}
		last = tok
	}
	return last != "new"
}

// frozenOraclecsIdentByte reports whether b can appear in a C# identifier (ASCII subset —
// enough for keyword recognition, which is all frozenOraclecsLeadingToken needs).
func frozenOraclecsIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Extract scans C# content for type and method definitions. Always returns nil
// error.
func (frozenOracleCSharpExtractor) Extract(content []byte) ([]Symbol, error) {
	var syms []Symbol

	// typeNameStart records the NameStart byte of every emitted type, so the
	// method pass can skip a declaration that has already been claimed as a type
	// (e.g. `public record CertId(...)` reads like a method to frozenOraclecsMethodRe).
	typeNameStart := map[int]bool{}

	mask := frozenOraclecsLiteralMask(content)

	// Types: class/interface/struct/enum/record -> Type.
	for _, m := range frozenOraclecsTypeRe.FindAllSubmatchIndex(content, -1) {
		// m: [full0 full1 kw0 kw1 name0 name1]
		ns, ne := m[4], m[5]
		if ns < 0 || ne < 0 {
			continue
		}
		if mask[ns] {
			continue // inside a string/char literal or a comment
		}
		be := frozenOraclebodyRange(content, m[1])
		typeNameStart[ns] = true
		syms = append(syms, Symbol{
			Name:      string(content[ns:ne]),
			Kind:      Type,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   be,
		})
	}

	// Methods: any Name( declaration at line start. Method (we don't distinguish
	// receiver-bearing methods from free functions in C#; all members live in a
	// type, so Method is the right Kind).
	for _, m := range frozenOraclecsMethodRe.FindAllSubmatchIndex(content, -1) {
		ns, ne := m[2], m[3]
		if ns < 0 || ne < 0 {
			continue
		}
		if mask[ns] {
			continue // inside a string/char literal or a comment
		}
		name := string(content[ns:ne])
		if frozenOraclecsControlKeywords[name] || typeNameStart[ns] {
			continue
		}
		// A statement, not a declaration: what this matched as a "return type" is
		// really a statement head (`throw new` / `return new` / `await` / a bare
		// `new`). See frozenOraclecsStatementKeywords and frozenOraclecsDeclPrefixOK.
		if !frozenOraclecsDeclPrefixOK(content, m[0], ns) {
			continue
		}
		be := frozenOraclebodyRange(content, m[1])
		syms = append(syms, Symbol{
			Name:      name,
			Kind:      Method,
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   be,
		})
	}

	return syms, nil
}

// frozenOraclecsCallRe matches a call/construction site: an optional `new` keyword, then an
// identifier immediately followed by `(` (an optional generic argument list is
// allowed between the name and the paren). It captures the identifier. This is
// deliberately liberal — the literal/comment mask and the def-site exclusion in
// ExtractRefs prune the false positives that matter.
var frozenOraclecsCallRe = regexp.MustCompile(`\b(?:(new)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^<>;{}()]*>)?\s*\(`)

// ExtractRefs scans C# content for reference occurrences: method/constructor
// call sites (`Name(`) and `new` constructions (`new Type(`). It is best-effort
// and NAME-BASED (no semantic resolution). It excludes:
//
//   - any match inside a string/char literal or a comment (via literalMask);
//   - control-flow keywords that read like a call (`if (`, `while (`, ...);
//   - the identifier of a definition this same content declares (so a method's
//     own declaration site is not re-emitted as a reference, and a constructor
//     declaration `public Foo(` is not double-counted) — done by excluding
//     offsets that coincide with a Symbol.NameStart from Extract.
//
// Always returns a nil error.
func (e frozenOracleCSharpExtractor) ExtractRefs(content []byte) ([]Occurrence, error) {
	defs, _ := e.Extract(content)
	return frozenOraclecsReferencesFromDefs(content, defs), nil
}

// ExtractDefsRefs implements DefsRefsExtractor: it scans content for
// definitions ONCE (via Extract) and derives references from that same defs
// slice, instead of the two independent frozenOraclecsTypeRe/frozenOraclecsMethodRe scans that calling
// Extract then ExtractRefs separately would perform (ExtractRefs re-running
// Extract internally to get defSites).
func (e frozenOracleCSharpExtractor) ExtractDefsRefs(content []byte) ([]Symbol, []Occurrence, error) {
	defs, _ := e.Extract(content)
	return defs, frozenOraclecsReferencesFromDefs(content, defs), nil
}

// frozenOraclecsReferencesFromDefs scans content for reference occurrences, excluding any
// offset that coincides with a NameStart in defs (the declaration sites).
// Shared by ExtractRefs and ExtractDefsRefs so both derive references from a
// single set of already-computed definitions rather than recomputing them.
func frozenOraclecsReferencesFromDefs(content []byte, defs []Symbol) []Occurrence {
	defSites := map[int]bool{}
	for _, s := range defs {
		defSites[s.NameStart] = true
	}

	mask := frozenOraclecsLiteralMask(content)
	var occs []Occurrence
	for _, m := range frozenOraclecsCallRe.FindAllSubmatchIndex(content, -1) {
		// m: [full0 full1 new0 new1 name0 name1]
		ns, ne := m[4], m[5]
		if ns < 0 || ne < 0 {
			continue
		}
		if mask[ns] {
			continue // inside a string/char literal or a comment
		}
		name := string(content[ns:ne])
		if frozenOraclecsControlKeywords[name] {
			continue
		}
		if defSites[ns] {
			continue // this is the declaration site, not a use
		}
		// `new` construction -> Type reference; bare call -> Method reference.
		kind := Method
		if m[2] >= 0 {
			kind = Type
		}
		occs = append(occs, Occurrence{
			Name:  name,
			Kind:  kind,
			Role:  Reference,
			Start: ns,
			End:   ne,
		})
	}
	return occs
}

// frozenOraclebodyRange computes the end offset for a declaration whose match ended at
// declEnd. It searches forward for the next '{' (or ';'), brace-matches a '{'
// if found, and otherwise returns the declaration line end. Callers set
// BodyStart to their regex match start.
func frozenOraclebodyRange(content []byte, declEnd int) int {
	n := len(content)
	if declEnd < 0 {
		declEnd = 0
	}
	// Scan forward for the opening brace or a statement terminator. Allow a
	// bounded look so we don't run off into the next member; the brace for a
	// declaration normally follows within the same or next few lines.
	i := declEnd
	for i < n {
		c := content[i]
		if c == '{' {
			if end := matchBlock(content, i); end > i {
				return end
			}
			break
		}
		if c == ';' {
			// Body-less member (interface/abstract method, field-like). Range is
			// the declaration line up to and including the ';'.
			return i + 1
		}
		i++
	}
	// No block and no terminator found: fall back to the declaration line.
	return lineEnd(content, declEnd)
}

func compareCSharpFrozen(t *testing.T, label string, content []byte, separate bool) (int, int) {
	t.Helper()
	wantDefs, wantRefs, wantErr := (frozenOracleCSharpExtractor{}).ExtractDefsRefs(content)
	gotDefs, gotRefs, gotErr := (CSharpExtractor{}).ExtractDefsRefs(content)
	if !reflect.DeepEqual(gotDefs, wantDefs) || !reflect.DeepEqual(gotRefs, wantRefs) || !reflect.DeepEqual(gotErr, wantErr) {
		t.Fatalf("%s differs from frozen extractor (bytes=%d): definitions got=%+v want=%+v; refs got=%+v want=%+v; errors %v/%v", label, len(content), gotDefs, wantDefs, gotRefs, wantRefs, gotErr, wantErr)
	}
	if separate {
		defs, err := (CSharpExtractor{}).Extract(content)
		if !reflect.DeepEqual(defs, wantDefs) || !reflect.DeepEqual(err, wantErr) {
			t.Fatalf("%s standalone Extract differs", label)
		}
		refs, err := (CSharpExtractor{}).ExtractRefs(content)
		if !reflect.DeepEqual(refs, wantRefs) || !reflect.DeepEqual(err, wantErr) {
			t.Fatalf("%s standalone ExtractRefs differs", label)
		}
	}
	return len(gotDefs), len(gotRefs)
}

func TestCSharpFrozenExtractorParity(t *testing.T) {
	pieces := []string{
		"", "\n", "\r\n", "\r", "\x00\xff", "α界😀", "\xef\xbb\xbf",
		"class C {\n public void Main() { Call(); }\n}\n",
		"public\npartial\nrecord struct\nR<T>(T x);\n",
		"public Dictionary<string, int>\n M<T>(T x) where T:class { return Call<T>(); }\n",
		"public new void M() => Call();\n", " public C() {}\n", "new\nWidget();\n",
		"throw new Error();\nreturn new Widget();\nyield return Call();\nawait Send();\n",
		"id ?? throw new Error(\n", "foo<T, A.B[]> ( );\n", "classical C { }\n",
		"/*\nclass Fake {\npublic void Hidden() {}\n*/\n", "// Fake();\n",
		"@\"a\"\"b\nclass Fake {}\"\n", "$@\"Call() {stuff}\"\n", "@$\"Fake()\"\n",
		"\"\"\"\nclass Fake {\n public void Hidden() {}\n}\n\"\"\";\n",
		"\"\"\"bad raw\npublic void Visible() {}\n", "\"\"\"\"\n\"\"\"\nCall();\n\"\"\"\"\n",
		"\"escaped \\\" Hidden()\"; '\\''; /* unterminated", "/*/", "//\rclass CR {}\n",
		"public void Open( { { \"}\" /* } */ }", "public interface I {\n void M();\n}\n",
		"public int\t M < T\n> ( ) { F(); }\n", "record R(int X);\nclass D;\n",
		"new new T(); renew T(); newnew(); _new(); 0Foo(); αFoo(); Foo< >(); F<T\nU>(); new\vT(); new\fT(); new\rT();\n",
		"F<A<B>>(); F<\"x\">(); F<// comment\nT>(); Foo<<(); Foo<;>(); Foo<{}>();\n",
	}
	for i, p := range pieces {
		compareCSharpFrozen(t, fmt.Sprintf("piece-%d", i), []byte(p), true)
	}
	rng := rand.New(rand.NewSource(728493))
	for i := 0; i < 1000; i++ {
		var b strings.Builder
		for n := rng.Intn(18); n >= 0; n-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		content := []byte(b.String())
		if len(content) > 0 && i%3 == 0 {
			content = content[:rng.Intn(len(content))]
		}
		compareCSharpFrozen(t, fmt.Sprintf("seed-728493-case-%d", i), content, true)
	}
	alphabet := []byte("abcXYZ_019 ()<>[]{};,.?\\\"'/*@=$!\t\r\n\v\f\x00\xff")
	for i := 0; i < 1000; i++ {
		content := make([]byte, rng.Intn(256))
		for j := range content {
			content[j] = alphabet[rng.Intn(len(alphabet))]
		}
		compareCSharpFrozen(t, fmt.Sprintf("seed-728493-bytes-%d", i), content, true)
	}

}

// These three byte-identical lexical shards are the acceptance snapshot from
// pinned Roslyn commit 36d26c5466e4d25940657ccb8d5b9557ccaf7be1,
// source snapshot 20260930T230839Z-1. Content is inline, so shard SHA256 also
// authenticates every normalized source byte, raw Git identity, and file ref.
// Build a test binary and run this gate under the corpus resource guard; it
// intentionally compares every eligible C# blob without sampling or limits.
func TestPinnedRoslynCSharpFrozenExtractorParity(t *testing.T) {
	dir := os.Getenv("MOEDEX_ROSLYN_ORACLE_SHARDS")
	if dir == "" {
		t.Skip("set MOEDEX_ROSLYN_ORACLE_SHARDS to frozen Roslyn acceptance shards")
	}
	fixtures := []struct {
		name, hash string
		size       int64
	}{
		{"shard-0000.idx", "c7c1941da48b15d33902954a7733e7d2f83cae01d70f701febe3f353510e7320", 426353753},
		{"shard-0001.idx", "41fb5b8ceb6d875e656142355cfbce3bf17f1ce3054e897e889ecc761a6c5676", 418489359},
		{"shard-0002.idx", "c67034c72fcd3fe28cfb05299dcd974181fc6aeb06165e5ed58c2019c971c603", 368739716},
	}
	var blobs, defs, refs int
	var contentBytes int64
	for _, f := range fixtures {
		path := filepath.Join(dir, f.name)
		fd, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		n, err := io.Copy(h, fd)
		closeErr := fd.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read pinned shard %s: %v/%v", path, err, closeErr)
		}
		if n != f.size || fmt.Sprintf("%x", h.Sum(nil)) != f.hash {
			t.Fatalf("%s differs from pinned Roslyn commit/snapshot", path)
		}
		bs, err := diskstore.LoadBlobs(path)
		if err != nil {
			t.Fatal(err)
		}
		before := blobs
		for id, b := range bs {
			if len(b.Files) == 0 || !strings.EqualFold(filepath.Ext(b.Files[0].RelPath), ".cs") {
				continue
			}
			nd, nr := compareCSharpFrozen(t, fmt.Sprintf("%s blob=%d sha=%s path=%s", f.name, id, b.SHA, b.Files[0].RelPath), b.Content, false)
			blobs++
			defs += nd
			refs += nr
			contentBytes += int64(len(b.Content))
		}
		t.Logf("verified shard=%s csharp_blobs=%d cumulative_blobs=%d definitions=%d occurrences=%d content_bytes=%d", f.name, blobs-before, blobs, defs, refs, contentBytes)
	}
	if blobs == 0 || defs == 0 || refs == 0 {
		t.Fatal("empty C# oracle corpus")
	}
	t.Logf("full pinned Roslyn parity: commit=36d26c5466e4d25940657ccb8d5b9557ccaf7be1 shards=%d blobs=%d definitions=%d occurrences=%d bytes=%d", len(fixtures), blobs, defs, refs, contentBytes)
}
