package symbol

import "testing"

const csRefFixture = `namespace Example.Demo
{
    public class Service
    {
        public void Run()
        {
            DoWork();              // call ref
            var c = new Cert(id);  // new construction ref
            // CommentedCall();    -- inside a comment: NOT a ref
            var s = "Inline(99)";  // inside a string: NOT a ref
            Helper<int>(5);        // generic call ref
        }

        private void DoWork()
        {
            return;
        }
    }
}
`

func TestCSharpExtractRefs_CallsAndConstructions(t *testing.T) {
	src := []byte(csRefFixture)
	occs, err := CSharpExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}

	for _, o := range occs {
		if o.Role != Reference {
			t.Errorf("occurrence %q role = %v, want Reference", o.Name, o.Role)
		}
		if got := string(src[o.Start:o.End]); got != o.Name {
			t.Errorf("occurrence offsets [%d,%d) = %q, want %q", o.Start, o.End, got, o.Name)
		}
	}

	names := map[string]int{}
	for _, o := range occs {
		names[o.Name]++
	}

	// DoWork() call site is emitted once. The DoWork() DEFINITION site must NOT
	// be double-counted (Extract emits it as a Method def, ExtractRefs excludes
	// that NameStart).
	if names["DoWork"] != 1 {
		t.Errorf("DoWork refs = %d, want 1 (call site only, def excluded)", names["DoWork"])
	}
	// `new Cert(...)` -> a Type reference to Cert.
	if names["Cert"] != 1 {
		t.Errorf("Cert refs = %d, want 1 (new construction)", names["Cert"])
	}
	for _, o := range occs {
		if o.Name == "Cert" && o.Kind != Type {
			t.Errorf("Cert ref kind = %v, want Type (new construction)", o.Kind)
		}
	}
	// Generic call Helper<int>(...) -> a call ref to Helper.
	if names["Helper"] != 1 {
		t.Errorf("Helper refs = %d, want 1 (generic call)", names["Helper"])
	}
}

func TestCSharpExtractRefs_SkipsStringsAndComments(t *testing.T) {
	src := []byte(csRefFixture)
	occs, err := CSharpExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	for _, o := range occs {
		if o.Name == "CommentedCall" {
			t.Errorf("emitted ref %q from inside a comment", o.Name)
		}
		if o.Name == "Inline" {
			t.Errorf("emitted ref %q from inside a string literal", o.Name)
		}
	}
}

func TestCSharpExtractRefs_DefSiteNotDoubleCounted(t *testing.T) {
	// A constructor declaration `public Foo(...)` reads like a call to csCallRe.
	// ExtractRefs must exclude it because its NameStart coincides with a def the
	// extractor emits (Extract treats the constructor as a Method def).
	src := []byte(`public class Foo
{
    public Foo(int x) { Init(); }
    private void Init() { }
}
`)
	occs, err := CSharpExtractor{}.ExtractRefs(src)
	if err != nil {
		t.Fatalf("ExtractRefs: %v", err)
	}
	for _, o := range occs {
		if o.Name == "Foo" {
			t.Errorf("constructor declaration Foo( emitted as a reference at %d", o.Start)
		}
	}
	// The genuine call Init() inside the constructor body IS a reference.
	gotInit := 0
	for _, o := range occs {
		if o.Name == "Init" {
			gotInit++
		}
	}
	if gotInit != 1 {
		t.Errorf("Init refs = %d, want 1 (the call inside the constructor body)", gotInit)
	}
}
