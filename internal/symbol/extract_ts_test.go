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
