package symbol

import "testing"

// symNames returns the set of symbol names keyed for assertion.
func symNames(syms []Symbol) map[string]Symbol {
	m := make(map[string]Symbol, len(syms))
	for _, s := range syms {
		m[s.Name] = s
	}
	return m
}

func TestCSharpExtractor_TypesAndMethods(t *testing.T) {
	src := []byte(`namespace TC.Certificates
{
    public class ReIssueCertificate
    {
        public void Issue()
        {
            DoWork();
        }

        private int Count(string id)
        {
            return 0;
        }
    }

    public interface ICertStore
    {
        void Save(Cert c);
    }

    public enum CertState { Pending, Active }

    public record CertId(string Value);

    public struct Point { public int X; }
}
`)
	syms, err := CSharpExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	by := symNames(syms)

	wantType := []string{"ReIssueCertificate", "ICertStore", "CertState", "CertId", "Point"}
	for _, n := range wantType {
		s, ok := by[n]
		if !ok {
			t.Errorf("missing type symbol %q; got %v", n, names(syms))
			continue
		}
		if s.Kind != Type {
			t.Errorf("%q: kind = %v, want Type", n, s.Kind)
		}
		if got := string(src[s.NameStart:s.NameEnd]); got != n {
			t.Errorf("%q: content[NameStart:NameEnd] = %q, want %q", n, got, n)
		}
	}

	wantMethod := []string{"Issue", "Count", "Save"}
	for _, n := range wantMethod {
		s, ok := by[n]
		if !ok {
			t.Errorf("missing method symbol %q; got %v", n, names(syms))
			continue
		}
		if s.Kind != Method {
			t.Errorf("%q: kind = %v, want Method", n, s.Kind)
		}
		if got := string(src[s.NameStart:s.NameEnd]); got != n {
			t.Errorf("%q: content[NameStart:NameEnd] = %q, want %q", n, got, n)
		}
	}
}

func TestCSharpExtractor_BodyRangesValidAndEnclosing(t *testing.T) {
	src := []byte(`public class A
{
    public void Run()
    {
        var x = 1;
    }
}
`)
	syms, _ := CSharpExtractor{}.Extract(src)
	for _, s := range syms {
		if s.BodyStart < 0 || s.BodyEnd > len(src) || s.BodyStart >= s.BodyEnd {
			t.Errorf("%q: invalid body range [%d,%d) (len %d)", s.Name, s.BodyStart, s.BodyEnd, len(src))
		}
		if !(s.BodyStart <= s.NameStart && s.NameEnd <= s.BodyEnd) {
			t.Errorf("%q: name [%d,%d) not within body [%d,%d)", s.Name, s.NameStart, s.NameEnd, s.BodyStart, s.BodyEnd)
		}
	}

	ix := NewIndex()
	ix.Set(0, syms)
	// An offset inside Run()'s body should resolve to the innermost symbol Run.
	off := indexOf(src, "var x")
	s, ok := ix.Enclosing(0, off)
	if !ok {
		t.Fatalf("Enclosing found nothing at offset %d", off)
	}
	if s.Name != "Run" {
		t.Errorf("Enclosing = %q, want innermost Run", s.Name)
	}
}

func TestCSharpExtractor_NoBodyMethod(t *testing.T) {
	src := []byte(`public interface I
{
    void Ping();
}
`)
	syms, _ := CSharpExtractor{}.Extract(src)
	by := symNames(syms)
	s, ok := by["Ping"]
	if !ok {
		t.Fatalf("missing Ping; got %v", names(syms))
	}
	if s.BodyStart >= s.BodyEnd || s.BodyEnd > len(src) {
		t.Errorf("Ping: invalid fallback body range [%d,%d)", s.BodyStart, s.BodyEnd)
	}
}

func TestCSharpExtractor_TypeRe_IgnoresCommentsAndStrings(t *testing.T) {
	src := []byte(`// helper for class Fake bookkeeping
public class Real
{
    /* TODO: refactor
    class Hidden
    should not be a type
    */
    string s = "class StringLiteral { }";
    public void Foo() { }
}
`)
	syms, err := CSharpExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	by := symNames(syms)

	if _, ok := by["Real"]; !ok {
		t.Errorf("missing legitimate type symbol %q; got %v", "Real", names(syms))
	}
	for _, n := range []string{"Fake", "Hidden", "StringLiteral"} {
		if s, ok := by[n]; ok {
			t.Errorf("spurious type symbol %q emitted from comment/string: %+v", n, s)
		}
	}
}

func TestCSharpExtractor_NeverPanicsOnGarbage(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte(""),
		[]byte("public class Unterminated {"),
		[]byte("class A { void B( {{{ \"unterminated"),
		[]byte("/* comment with class Fake { */ public class Real {}"),
	}
	for i, in := range inputs {
		syms, err := CSharpExtractor{}.Extract(in)
		if err != nil {
			t.Errorf("input %d: unexpected error %v", i, err)
		}
		for _, s := range syms {
			if s.BodyStart < 0 || s.BodyEnd > len(in) || s.NameStart < 0 || s.NameEnd > len(in) {
				t.Errorf("input %d: out-of-bounds offsets in %+v (len %d)", i, s, len(in))
			}
		}
	}
}

func names(syms []Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}

// csStatementFixture holds one statement line per shape that the return-type slot
// used to swallow. Each line, when it STARTS a line, previously read as a member
// declaration and emitted a bogus Method definition of the constructed/called
// name (see csStatementKeywords).
const csStatementFixture = `namespace N
{
    public class C
    {
        public int Work(int x)
        {
            if (x < 0) throw new ArgumentException("inline");
            await SendAsync(x);
            return new Widget(x);
        }

        public IEnumerable<int> Stream()
        {
            yield return new Thing(1);
        }

        public void Guard(int x)
        {
            throw new ArgumentOutOfRangeException(nameof(x));
        }

        public int Fallback(int? x) => x ?? throw new InvalidOperationException("none");

        public void Chained(string? id)
        {
            Register(
                id ?? throw new ArgumentNullException(nameof(id)));
            Register(
                new Registration("fallback"));
        }

        // Member hiding: new IS a legal modifier here, so these must still be
        // definitions -- the last prefix token is the return type, not new.
        public new void Hide() { }

        new void Hide2() { }
    }
}
`

// TestCSharpExtractor_StatementsAreNotDefinitions pins the fix for the
// statement-prefix false positive: a line beginning with a statement keyword is
// never a definition, and the name it constructs/calls surfaces as a REFERENCE
// instead (it used to be suppressed, because a def site shadows the reference).
func TestCSharpExtractor_StatementsAreNotDefinitions(t *testing.T) {
	src := []byte(csStatementFixture)
	defs, occs, err := extractDefsRefs(CSharpExtractor{}, src)
	if err != nil {
		t.Fatalf("extractDefsRefs: %v", err)
	}

	got := symNames(defs)
	// The real members must still be extracted.
	for name, kind := range map[string]Kind{
		"C":        Type,
		"Work":     Method,
		"Stream":   Method,
		"Guard":    Method,
		"Fallback": Method,
		"Chained":  Method,
		"Hide":     Method,
		"Hide2":    Method,
	} {
		s, ok := got[name]
		if !ok {
			t.Errorf("missing real definition %q; got %v", name, names(defs))
			continue
		}
		if s.Kind != kind {
			t.Errorf("%q: kind = %v, want %v", name, s.Kind, kind)
		}
	}
	// None of the constructed/called names may be a definition.
	for _, bogus := range []string{
		"ArgumentException",
		"ArgumentOutOfRangeException",
		"InvalidOperationException",
		"Widget",
		"Thing",
		"SendAsync",
		"ArgumentNullException", // `id ?? throw new ...` on a continuation line
		"Registration",          // a bare `new Registration(` continuation line
	} {
		if s, ok := got[bogus]; ok {
			t.Errorf("%q emitted as a %v definition at [%d,%d) — statement, not declaration",
				bogus, s.Kind, s.BodyStart, s.BodyEnd)
		}
	}

	// Each one must instead appear as a reference, with `new` constructions typed.
	wantRefKind := map[string]Kind{
		"ArgumentException":           Type,
		"ArgumentOutOfRangeException": Type,
		"InvalidOperationException":   Type,
		"Widget":                      Type,
		"Thing":                       Type,
		"SendAsync":                   Method,
		"ArgumentNullException":       Type,
		"Registration":                Type,
	}
	for name, kind := range wantRefKind {
		found := occByName(occs, name)
		if len(found) == 0 {
			t.Errorf("%q: no reference emitted; refs = %v", name, occNames(occs))
			continue
		}
		if found[0].Role != Reference {
			t.Errorf("%q: role = %v, want Reference", name, found[0].Role)
		}
		if found[0].Kind != kind {
			t.Errorf("%q: reference kind = %v, want %v", name, found[0].Kind, kind)
		}
		if s := string(src[found[0].Start:found[0].End]); s != name {
			t.Errorf("%q: offsets [%d,%d) = %q", name, found[0].Start, found[0].End, s)
		}
	}
}

// TestCSharpExtractor_DeclarationsWithSpacedTypesStillMatch guards the other
// direction: the return-type slot must keep admitting spaces, so generic and
// array types with spaces in them remain real declarations. A fix that tightened
// the character class instead of filtering the leading token would break these.
func TestCSharpExtractor_DeclarationsWithSpacedTypesStillMatch(t *testing.T) {
	src := []byte(`namespace N
{
    public class C
    {
        public Dictionary<string, int> Counts(string id)
        {
            return null;
        }

        protected internal static async Task<IReadOnlyList<Widget>> LoadAsync(int id)
        {
            return null;
        }

        public C(int seed)
        {
        }

        private int[] Sizes(int n)
        {
            return null;
        }
    }
}
`)
	defs, _, err := extractDefsRefs(CSharpExtractor{}, src)
	if err != nil {
		t.Fatalf("extractDefsRefs: %v", err)
	}
	got := symNames(defs)
	for _, name := range []string{"Counts", "LoadAsync", "Sizes", "C"} {
		if _, ok := got[name]; !ok {
			t.Errorf("declaration %q was dropped; got %v", name, names(defs))
		}
	}
}

// occNames lists occurrence names in order, for failure messages.
func occNames(occs []Occurrence) []string {
	out := make([]string, 0, len(occs))
	for _, o := range occs {
		out = append(out, o.Name)
	}
	return out
}
