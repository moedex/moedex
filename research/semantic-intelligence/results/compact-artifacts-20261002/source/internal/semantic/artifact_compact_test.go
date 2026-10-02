package semantic

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCompactArtifactRoundtripAndLegacy(t *testing.T) {
	a := fixture()
	// Audit capture data is intentionally lossless, including whitespace.
	a.Contexts[0].Capture = strings.Repeat(" ", 70<<10) + a.Contexts[0].Capture
	a.Contexts[0].InputFingerprint = sum(a.Contexts[0].Capture)
	old := a.Contexts[0].ID
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	for i := range a.Occurrences {
		if a.Occurrences[i].ContextID == old {
			a.Occurrences[i].ContextID = a.Contexts[0].ID
		}
		a.Occurrences[i].ID = a.Occurrences[i].ComputeID()
	}
	a.Bindings[0].OccurrenceID = a.Occurrences[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	p := filepath.Join(t.TempDir(), "a.semantic")
	if err := Write(p, a); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	var env compactEnvelope
	if err := strict(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Version != 2 || env.PayloadBytes <= 64<<10 || len(raw) >= 64<<10 {
		t.Fatalf("not compact: %+v", env)
	}
	got, err := ReadFrom(bytes.NewReader(raw), Limits{MaxBytes: int64(len(raw))})
	if err != nil || !reflect.DeepEqual(a, got) {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := ReadFrom(bytes.NewReader(raw), Limits{MaxBytes: int64(len(raw) - 1)}); err == nil {
		t.Fatal("file bound ignored")
	}
	payload, _ := json.Marshal(a)
	second, err := encodeCompact(payload)
	if err != nil || !bytes.Equal(raw, second) {
		t.Fatal("non-deterministic encoding", err)
	}
	legacy, _ := json.Marshal(envelope{Format, FormatVersion, sum(string(payload)), payload})
	got, err = ReadFrom(bytes.NewReader(legacy), Limits{})
	if err != nil || !reflect.DeepEqual(a, got) {
		t.Fatal("legacy read", err)
	}
	if err := Write(p, a); err == nil {
		t.Fatal("overwrote immutable artifact")
	}
}

func TestCompactEnvelopeRejectsCorruptionAndExpansion(t *testing.T) {
	payload, _ := json.Marshal(fixture())
	raw, _ := encodeCompact(payload)
	var base compactEnvelope
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*compactEnvelope){
		"format":          func(e *compactEnvelope) { e.Format = "other" },
		"encoding":        func(e *compactEnvelope) { e.Encoding = "zip" },
		"zero":            func(e *compactEnvelope) { e.PayloadBytes = 0 },
		"negative":        func(e *compactEnvelope) { e.PayloadBytes = -1 },
		"oversize":        func(e *compactEnvelope) { e.PayloadBytes = MaxPayloadBytes + 1 },
		"underreported":   func(e *compactEnvelope) { e.PayloadBytes = 1 },
		"overreported":    func(e *compactEnvelope) { e.PayloadBytes++ },
		"hash":            func(e *compactEnvelope) { e.SHA256 = sum("bad") },
		"truncated":       func(e *compactEnvelope) { e.Payload = e.Payload[:len(e.Payload)-1] },
		"checksum":        func(e *compactEnvelope) { e.Payload[len(e.Payload)-8] ^= 1 },
		"trailing":        func(e *compactEnvelope) { e.Payload = append(e.Payload, 0) },
		"multistream":     func(e *compactEnvelope) { e.Payload = append(e.Payload, e.Payload...) },
		"unknown-version": func(e *compactEnvelope) { e.Version = 3 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := base
			env.Payload = append([]byte(nil), base.Payload...)
			mutate(&env)
			bad, _ := json.Marshal(env)
			if _, err := ReadFrom(bytes.NewReader(bad), Limits{}); err == nil {
				t.Fatal("accepted invalid envelope")
			}
		})
	}
	// A highly compressible payload must still obey its declared expansion cap.
	bomb, _ := encodeCompact(bytes.Repeat([]byte("x"), 1<<20))
	var env compactEnvelope
	json.Unmarshal(bomb, &env)
	env.PayloadBytes = 1024
	bad, _ := json.Marshal(env)
	if _, err := ReadFrom(bytes.NewReader(bad), Limits{}); err == nil {
		t.Fatal("expansion cap ignored")
	}
}

func TestArtifactExpandedReadLimit(t *testing.T) {
	payload, _ := json.Marshal(fixture())
	compact, _ := encodeCompact(payload)
	legacy, _ := json.Marshal(envelope{Format, FormatVersion, sum(string(payload)), payload})
	for _, raw := range [][]byte{compact, legacy} {
		for _, limit := range []int64{-1, MaxPayloadBytes + 1, int64(len(payload) - 1)} {
			if _, err := ReadFrom(bytes.NewReader(raw), Limits{MaxPayloadBytes: limit}); err == nil {
				t.Fatalf("accepted limit %d", limit)
			}
		}
		if _, err := ReadFrom(bytes.NewReader(raw), Limits{MaxPayloadBytes: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "artifact")
		os.WriteFile(p, raw, 0600)
		if _, err := Read(p, Limits{MaxPayloadBytes: int64(len(payload) - 1)}); err == nil {
			t.Fatal("file reader discarded expanded limit")
		}
	}
}
