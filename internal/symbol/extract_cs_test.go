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
