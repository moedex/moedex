package graph

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestConfidenceTiers(t *testing.T) {
	want := []struct {
		tier  ConfidenceTier
		name  string
		score float64
	}{
		{Proven, "Proven", 1.0},
		{Verified, "Verified", 0.85},
		{Pattern, "Pattern", 0.6},
		{Candidate, "Candidate", 0.3},
	}
	for _, tc := range want {
		if !tc.tier.Valid() || tc.tier.String() != tc.name || tc.tier.Score() != tc.score {
			t.Errorf("tier %d = (%q, %v, valid=%v), want (%q, %v, true)", tc.tier, tc.tier, tc.tier.Score(), tc.tier.Valid(), tc.name, tc.score)
		}
		got, err := json.Marshal(ConfidenceOf(tc.tier))
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["tier"] != tc.name || decoded["score"] != tc.score {
			t.Errorf("ConfidenceOf(%s) JSON = %s", tc.name, got)
		}
	}
	if ConfidenceTier(255).Valid() || ConfidenceTier(255).Score() != 0 {
		t.Fatal("invalid tier accepted")
	}
}

func TestParseMinConfidence(t *testing.T) {
	for input, want := range map[string]ConfidenceTier{
		"": DefaultMinConfidence, "Candidate": Candidate, "Pattern": Pattern,
		"Verified": Verified, "Proven": Proven,
	} {
		got, err := ParseMinConfidence(input)
		if err != nil || got != want {
			t.Errorf("ParseMinConfidence(%q) = %v, %v; want %v, nil", input, got, err, want)
		}
	}
	for _, input := range []string{"candidate", "unknown", " Proven ", "5"} {
		if _, err := ParseMinConfidence(input); err == nil {
			t.Errorf("ParseMinConfidence(%q) accepted invalid value", input)
		}
	}
}

func TestEvidenceDereferencesBytesAndSourceLine(t *testing.T) {
	content := []byte("before\nfunc Caller() { Target() }\nafter\n")
	evidence := Evidence{BlobSHA: "caller-sha", ByteOffset: 23, ByteLength: 6}
	if !evidence.Valid() {
		t.Fatal("valid evidence rejected")
	}
	span, ok := evidence.Bytes(content)
	if !ok || string(span) != "Target" {
		t.Fatalf("Bytes = %q, %v", span, ok)
	}
	line, ok := evidence.SourceLine(content)
	if !ok || !reflect.DeepEqual(line, []byte("func Caller() { Target() }")) {
		t.Fatalf("SourceLine = %q, %v", line, ok)
	}
	if _, ok := (Evidence{BlobSHA: "caller-sha", ByteOffset: 999, ByteLength: 1}).Bytes(content); ok {
		t.Fatal("out-of-range evidence dereferenced")
	}
}
