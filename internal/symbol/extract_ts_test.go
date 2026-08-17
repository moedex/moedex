package symbol

import "testing"

func TestTSExtractor_ClassMethodsAndExports(t *testing.T) {
	src := []byte(`import { Injectable } from '@angular/core';

@Injectable()
export class OrderService {
  private cache = new Map();

  getOrder(id: string) {
    return this.cache.get(id);
  }

  async refresh(): Promise<void> {
    await this.load();
  }
}

export interface Order {
  id: string;
}

export type OrderId = string;

export enum Status { Open, Closed }

export function makeOrder(id: string): Order {
  return { id };
}

export const DEFAULT_ID = '0';

export const buildId = (n: number) => 'id-' + n;
`)
	syms, err := TSExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	by := symNames(syms)

	cases := []struct {
		name string
		kind Kind
	}{
		{"OrderService", Type},
		{"Order", Type},
		{"OrderId", Type},
		{"Status", Type},
		{"makeOrder", Func},
		{"DEFAULT_ID", Const},
		{"buildId", Const},
		{"getOrder", Method},
		{"refresh", Method},
	}
	for _, c := range cases {
		s, ok := by[c.name]
		if !ok {
			t.Errorf("missing symbol %q; got %v", c.name, names(syms))
			continue
		}
		if s.Kind != c.kind {
			t.Errorf("%q: kind = %v, want %v", c.name, s.Kind, c.kind)
		}
		if got := string(src[s.NameStart:s.NameEnd]); got != c.name {
			t.Errorf("%q: content[NameStart:NameEnd] = %q, want %q", c.name, got, c.name)
		}
		if s.BodyStart < 0 || s.BodyEnd > len(src) || s.BodyStart >= s.BodyEnd {
			t.Errorf("%q: invalid body range [%d,%d)", c.name, s.BodyStart, s.BodyEnd)
		}
	}
}

func TestTSExtractor_EnclosingResolvesMethod(t *testing.T) {
	src := []byte(`export class Svc {
  doThing() {
    const marker = 42;
  }
}
`)
	syms, _ := TSExtractor{}.Extract(src)
	ix := NewIndex()
	ix.Set(0, syms)
	off := indexOf(src, "marker")
	s, ok := ix.Enclosing(0, off)
	if !ok {
		t.Fatalf("Enclosing found nothing at %d", off)
	}
	if s.Name != "doThing" {
		t.Errorf("Enclosing = %q, want innermost doThing", s.Name)
	}
}

func TestTSExtractor_NeverPanics(t *testing.T) {
	inputs := [][]byte{
		nil, []byte(""),
		[]byte("export class Broken {"),
		[]byte("const s = `template with { brace and class Fake {`;"),
		[]byte("// class Commented {}\nexport class Real {}"),
	}
	for i, in := range inputs {
		syms, err := TSExtractor{}.Extract(in)
		if err != nil {
			t.Errorf("input %d: error %v", i, err)
		}
		for _, s := range syms {
			if s.NameStart < 0 || s.NameEnd > len(in) || s.BodyStart < 0 || s.BodyEnd > len(in) {
				t.Errorf("input %d: out-of-bounds %+v (len %d)", i, s, len(in))
			}
		}
	}
}

// tsCallFixture is the shape that used to dominate the corpus' "definitions":
// indented CALL statements. tsMethodRe's only structural guard was the leading
// indent, which these satisfy exactly as well as a class member does.
const tsCallFixture = `describe('cart', () => {
  beforeEach(() => {
    cy.visit('/cart');
  });

  it('adds an item', () => {
    expect(total).to.equal(2);
    expect(label).to.equal('a(b){c');
  });
});

export class Pipeline {
  run() {
    return this.source$.pipe(
      map(x => x * 2),
      mergeMap(x => this.load(x)),
    );
  }

  private helper() {
    doWork(1);
    this.other(2);
  }
}
`

// TestTSExtractor_CallsAreNotDefinitions pins the fix: an indented call statement
// is not a method declaration. The real members in the same fixture must survive.
func TestTSExtractor_CallsAreNotDefinitions(t *testing.T) {
	src := []byte(tsCallFixture)
	syms, err := TSExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	by := symNames(syms)

	for _, call := range []string{
		"describe", "beforeEach", "it", "expect", // test-framework globals
		"map", "mergeMap", // RxJS operators in a pipe
		"doWork", "visit", "pipe", "load", "other", "equal", // plain calls
	} {
		if s, ok := by[call]; ok {
			t.Errorf("call %q emitted as a %v definition at [%d,%d)", call, s.Kind, s.BodyStart, s.BodyEnd)
		}
	}
	// The real class and its two methods are still found.
	for name, kind := range map[string]Kind{"Pipeline": Type, "run": Method, "helper": Method} {
		s, ok := by[name]
		if !ok {
			t.Errorf("missing real definition %q; got %v", name, names(syms))
			continue
		}
		if s.Kind != kind {
			t.Errorf("%q: kind = %v, want %v", name, s.Kind, kind)
		}
	}
}

// TestTSExtractor_DeclarationShapesStillMatch guards the recall side of the same
// rule across the member shapes the Angular corpus actually uses.
func TestTSExtractor_DeclarationShapesStillMatch(t *testing.T) {
	src := []byte(`export abstract class Widget {
  constructor(
    private readonly http: HttpClient,
    private store: Store,
  ) {}

  ngOnInit(): void {
    this.load();
  }

  handle(e: Event) {
    e.preventDefault();
  }

  get total(): number {
    return 1;
  }

  private async save(x: Foo): Promise<void> {
    await this.http.post(x);
  }

  load<T>(id: string): T {
    return null as T;
  }

  abstract fetch(id: string): Promise<Widget>;

  allman(a: number)
  {
    return a;
  }
}

export const helpers = {
  doThing(x: number) {
    return x;
  },
};
`)
	syms, err := TSExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	by := symNames(syms)
	for _, name := range []string{
		"constructor", // multi-line dependency-injection parameter list
		"ngOnInit",    // return-type annotation
		"handle",      // plain body
		"total",       // getter
		"save",        // modifiers + generic return type
		"load",        // generic method
		"fetch",       // abstract signature (return type, no body)
		"allman",      // brace on the following line
		"doThing",     // object-literal method
	} {
		if _, ok := by[name]; !ok {
			t.Errorf("declaration %q was dropped; got %v", name, names(syms))
		}
	}
}

// TestTSExtractor_ReturnTypelessSignatureIsSkipped pins the one deliberate recall
// trade: `foo(a: number);` with no return type is byte-for-byte indistinguishable
// from a call statement, so it is not extracted. Documented in
// tsDeclFollowsParams; asserted here so a future change to it is a conscious one.
func TestTSExtractor_ReturnTypelessSignatureIsSkipped(t *testing.T) {
	src := []byte(`export interface Loader {
  load(id: string);
  fetch(id: string): Promise<void>;
}
`)
	syms, _ := TSExtractor{}.Extract(src)
	by := symNames(syms)
	if _, ok := by["load"]; ok {
		t.Error("return-type-less signature `load(id: string);` was extracted; the documented trade is to skip it")
	}
	if _, ok := by["fetch"]; !ok {
		t.Errorf("signature WITH a return type must still be extracted; got %v", names(syms))
	}
}
