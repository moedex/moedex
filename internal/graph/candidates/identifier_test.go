package candidates

import (
	"reflect"
	"testing"
)

const mixed = "Add(Address, _priv, x$y, 42, Ünïcode) // Add again\n\tAdd\n"

func TestEachIdentifierReportsMaximalRuns(t *testing.T) {
	var got []string
	EachIdentifier([]byte(mixed), func(run []byte) bool {
		got = append(got, string(run))
		return true
	})
	want := []string{"Add", "Address", "_priv", "x$y", "42", "Ünïcode", "Add", "again", "Add"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runs = %#v, want %#v", got, want)
	}

	stopped := 0
	EachIdentifier([]byte(mixed), func([]byte) bool { stopped++; return false })
	if stopped != 1 {
		t.Fatalf("visited %d run(s) after returning false, want 1", stopped)
	}
}

func TestEachIdentifierAgreesWithIdentifierAt(t *testing.T) {
	content := []byte(mixed)
	for _, needle := range []string{"Add", "Address", "dd", "ddr", "_priv", "x$y", "42", "Ünïcode", "again", "missing"} {
		var byFilter int
		for off := 0; off+len(needle) <= len(content); off++ {
			if string(content[off:off+len(needle)]) != needle {
				continue
			}
			if identifierAt(content, off, len(needle)) {
				byFilter++
			}
		}
		var byRuns int
		EachIdentifier(content, func(run []byte) bool {
			if string(run) == needle {
				byRuns++
			}
			return true
		})
		if byFilter != byRuns {
			t.Fatalf("%q: the fan-out filter accepts %d position(s), maximal runs give %d", needle, byFilter, byRuns)
		}
	}
}

func TestEachIdentifierHandlesEdges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"all separators", " \t\n().,", nil},
		{"single run", "Whole", []string{"Whole"}},
		{"leading and trailing", "-A-", []string{"A"}},
		{"adjacent runs", "a.b/c", []string{"a", "b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			EachIdentifier([]byte(tc.content), func(run []byte) bool {
				got = append(got, string(run))
				return true
			})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("runs = %#v, want %#v", got, tc.want)
			}
		})
	}
}
