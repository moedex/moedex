package symbol

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestSymbolStreamRoundTrip(t *testing.T) {
	ix := NewIndex()
	ix.Set(2, []Symbol{{Name: "Call", Kind: Method, NameStart: 3, NameEnd: 7, BodyStart: 0, BodyEnd: 20}})
	ix.SetRefs(2, []Occurrence{{Name: "Call", Kind: Method, Role: Reference, Start: 10, End: 14}})
	var data bytes.Buffer
	if err := Encode(&data, ix); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Symbols(2), ix.Symbols(2)) || !reflect.DeepEqual(got.Refs(2), ix.Refs(2)) || !reflect.DeepEqual(got.References("Call"), ix.References("Call")) {
		t.Fatal("stream round trip changed symbols/references")
	}
	for end := 0; end < data.Len(); end++ {
		if _, err := Decode(bytes.NewReader(data.Bytes()[:end]), int64(end)); err == nil {
			t.Fatalf("accepted truncation at %d", end)
		}
	}
	if _, err := Decode(bytes.NewReader(data.Bytes()), int64(data.Len()+1)); err == nil {
		t.Fatal("accepted premature EOF")
	}
}

func TestSymbolStreamRejectsMalformed(t *testing.T) {
	ints := func(v ...uint64) []byte {
		var out []byte
		for _, n := range v {
			out = binary.AppendUvarint(out, n)
		}
		return out
	}
	cases := map[string][]byte{
		"legacy":                []byte("SYM1\x00"),
		"trailing":              []byte("SYM2\x00\x00x"),
		"kind truncation":       append([]byte("SYM2"), ints(1, 0, 1, 0, 256, 0, 0, 0, 0, 0)...),
		"reversed name":         append([]byte("SYM2"), ints(1, 0, 1, 0, 1, 2, 1, 0, 4, 0)...),
		"offset overflow":       append([]byte("SYM2"), ints(1, 0, 1, 0, 1, 0, ^uint64(0), 0, 4, 0)...),
		"role truncation":       append([]byte("SYM2"), ints(0, 1, 0, 1, 0, 1, 257, 0, 0)...),
		"duplicate blob":        append([]byte("SYM2"), ints(2, 0, 0, 0, 0, 0)...),
		"unordered definitions": append([]byte("SYM2"), ints(1, 0, 2, 0, 1, 0, 0, 2, 4, 0, 1, 0, 0, 1, 4, 0)...),
		"unordered references":  append([]byte("SYM2"), ints(0, 1, 0, 2, 0, 1, 1, 2, 3, 0, 1, 1, 1, 2)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("accepted malformed stream")
			}
		})
	}
	// A plausible count under a large caller-supplied size must not eagerly
	// reserve gigabytes before discovering the actual stream has ended.
	for _, body := range [][]byte{ints(1, 0, 100_000_000), ints(0, 1, 0, 100_000_000)} {
		data := append([]byte("SYM2"), body...)
		if _, err := Decode(bytes.NewReader(data), 1<<30); err == nil {
			t.Fatal("accepted count without records")
		}
	}
}
