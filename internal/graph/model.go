// Package graph defines the shared value types carried by graph edges.
package graph

import (
	"encoding/json"
	"fmt"
)

// ConfidenceTier is the provenance-backed confidence assigned to a graph edge.
// The values are ordered from weakest to strongest so callers can compare tiers
// directly without converting them to their presentation scores.
type ConfidenceTier uint8

const (
	// Candidate is an unverified trigram/name collision.
	Candidate ConfidenceTier = iota
	// Pattern is a regex or language-convention match.
	Pattern
	// Verified is an AST or attribute match.
	Verified
	// Proven is an LSP-confirmed or manifest-declared relationship.
	Proven

	// DefaultMinConfidence is the agent-facing graph floor used when a tool
	// invocation omits min_confidence. Candidate evidence remains available for
	// explicit diagnostics but is not traversed by default.
	DefaultMinConfidence = Pattern
)

// ParseMinConfidence parses the public min_confidence spelling. The empty
// string selects DefaultMinConfidence; accepted non-empty values intentionally
// match the exact tier names emitted by String and JSON.
func ParseMinConfidence(value string) (ConfidenceTier, error) {
	switch value {
	case "":
		return DefaultMinConfidence, nil
	case "Candidate":
		return Candidate, nil
	case "Pattern":
		return Pattern, nil
	case "Verified":
		return Verified, nil
	case "Proven":
		return Proven, nil
	default:
		return 0, fmt.Errorf("min_confidence must be one of Candidate, Pattern, Verified, or Proven")
	}
}

// Valid reports whether t is one of the four defined confidence tiers.
func (t ConfidenceTier) Valid() bool { return t <= Proven }

// Score returns the stable numeric score associated with the tier. Scores are
// derived presentation metadata, never persisted independently of the tier.
func (t ConfidenceTier) Score() float64 {
	switch t {
	case Proven:
		return 1.0
	case Verified:
		return 0.85
	case Pattern:
		return 0.6
	case Candidate:
		return 0.3
	default:
		return 0
	}
}

// String returns the public tier name used by MCP responses and diagnostics.
func (t ConfidenceTier) String() string {
	switch t {
	case Proven:
		return "Proven"
	case Verified:
		return "Verified"
	case Pattern:
		return "Pattern"
	case Candidate:
		return "Candidate"
	default:
		return fmt.Sprintf("ConfidenceTier(%d)", uint8(t))
	}
}

// MarshalJSON renders the enum by name instead of leaking its compact on-disk
// ordinal into APIs.
func (t ConfidenceTier) MarshalJSON() ([]byte, error) {
	if !t.Valid() {
		return nil, fmt.Errorf("graph: invalid confidence tier %d", uint8(t))
	}
	return json.Marshal(t.String())
}

// Confidence is the structured API representation of a tier. Score is derived
// from Tier so the two values cannot drift in persisted graph data.
type Confidence struct {
	Tier  ConfidenceTier `json:"tier"`
	Score float64        `json:"score"`
}

// ConfidenceOf returns the structured representation of tier.
func ConfidenceOf(tier ConfidenceTier) Confidence {
	return Confidence{Tier: tier, Score: tier.Score()}
}

// Evidence identifies the exact source bytes that justify an edge. BlobSHA is
// the git blob identity; ByteOffset and ByteLength select a half-open span in
// that blob's indexed content.
type Evidence struct {
	BlobSHA    string `json:"blob_sha"`
	ByteOffset uint64 `json:"byte_offset"`
	ByteLength uint64 `json:"byte_length"`
}

// End returns the first byte after the evidence span and reports false on
// uint64 overflow.
func (e Evidence) End() (uint64, bool) {
	if e.ByteLength > ^uint64(0)-e.ByteOffset {
		return 0, false
	}
	return e.ByteOffset + e.ByteLength, true
}

// Valid reports whether the link has a blob identity and a non-empty,
// non-overflowing span.
func (e Evidence) Valid() bool {
	_, ok := e.End()
	return e.BlobSHA != "" && e.ByteLength > 0 && ok
}

// Bytes dereferences the evidence span against content already resolved by
// BlobSHA. It returns false when the span falls outside content.
func (e Evidence) Bytes(content []byte) ([]byte, bool) {
	end, ok := e.End()
	if !ok || e.ByteOffset > uint64(len(content)) || end > uint64(len(content)) {
		return nil, false
	}
	return content[int(e.ByteOffset):int(end)], true
}

// SourceLine returns the complete source line containing the evidence span,
// excluding trailing CR/LF bytes. Content must be the blob named by BlobSHA.
func (e Evidence) SourceLine(content []byte) ([]byte, bool) {
	if _, ok := e.Bytes(content); !ok {
		return nil, false
	}
	start := int(e.ByteOffset)
	for start > 0 && content[start-1] != '\n' {
		start--
	}
	end, _ := e.End()
	lineEnd := int(end)
	for lineEnd < len(content) && content[lineEnd] != '\n' && content[lineEnd] != '\r' {
		lineEnd++
	}
	return content[start:lineEnd], true
}
