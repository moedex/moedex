package symbol

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

func assertCSCallParity(t *testing.T, content []byte) {
	t.Helper()
	want := csCallRe.FindAllSubmatchIndex(content, -1)
	var got [][]int
	for match := range csCallMatches(content) {
		got = append(got, append([]int(nil), match[:]...))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("content %q\nscanner %v\nregexp  %v", content, got, want)
	}
}

func TestCSCallScannerAdversarialParity(t *testing.T) {
	cases := []string{
		"", "new(", "new (", "new new Foo(", "new new(", "new new<T>(",
		"renew Foo( new\nFoo <T> ( new\vFoo( new\fFoo(",
		"_Foo( 0Foo( éFoo( Fooé( Foo\xff( \xffFoo(",
		"Foo<Bar<Baz>>( Next( Foo<> ( Foo<A B\n\x00\xff> (",
		"Foo<A;B>( Foo<A{B>( Foo<A}B>( Foo<A(B>( Foo<A)B>(",
		"Foo<A> Nope( new Foo<A> Nope( new new new Foo(",
		"Foo(Bar(Baz( Foo < unmatched Bar( Foo < unmatched",
		"// new Comment(\n\"new Quoted(\" @\"new Verbatim(\" \"\"\"new Raw(\"\"\" Real(",
		"Foo<\"(> Other( new/*gap*/Foo( new \t\r\n\f Name (",
	}
	for _, content := range cases {
		assertCSCallParity(t, []byte(content))
	}
	// Every byte next to a boundary and in each whitespace/generic position:
	// this includes all malformed UTF-8, Unicode continuation bytes, and VT.
	for b := 0; b < 256; b++ {
		for _, pieces := range [][2]string{
			{"", "Foo("}, {"Foo", "("}, {"new", "Foo("},
			{"new ", "Foo("}, {"Foo<", ">("}, {"Foo<T>", "("},
		} {
			assertCSCallParity(t, append(append([]byte(pieces[0]), byte(b)), pieces[1]...))
		}
	}
}

func TestCSCallScannerRandomParity(t *testing.T) {
	rng := rand.New(rand.NewSource(731904))
	tokens := []string{"new", "new ", "new\t", "Foo", "Foo_1", "0Foo", "_", " ", "\t", "\r\n", "\v", "\f", "<", ">", "(", ")", "{", "}", ";", "é", "\xff", "\x00", "//", "\"", "@\"", "\"\"\"", ".", "<T>", "<A B>", "()"}
	for trial := 0; trial < 20000; trial++ {
		var content []byte
		for n := rng.Intn(100); n > 0; n-- {
			if rng.Intn(8) == 0 {
				content = append(content, byte(rng.Intn(256)))
			} else {
				content = append(content, tokens[rng.Intn(len(tokens))]...)
			}
		}
		assertCSCallParity(t, content)
	}
}

func TestCSCallScannerStop(t *testing.T) {
	n := 0
	for range csCallMatches([]byte("A( B( C(")) {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("early stop yielded %d matches", n)
	}
}

var csCallBenchmarkSink int

func BenchmarkCSCallMatching(b *testing.B) {
	content := bytes.Repeat([]byte("public Foo<T> Build(int value) { var x = new Foo<T>(value); return Factory.Create<T>(x); }\n// \"new Fake(\"\n"), 256)
	b.Run("regexp", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(content)))
		for b.Loop() {
			total := 0
			for _, m := range csCallRe.FindAllSubmatchIndex(content, -1) {
				total += m[4]
			}
			csCallBenchmarkSink = total
		}
	})
	b.Run("scanner", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(content)))
		for b.Loop() {
			total := 0
			for m := range csCallMatches(content) {
				total += m[4]
			}
			csCallBenchmarkSink = total
		}
	})
}
