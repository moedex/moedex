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
