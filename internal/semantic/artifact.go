package semantic

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DefaultMaxBytes int64 = 64 << 20

// MaxPayloadBytes bounds expanded JSON independently of the on-disk budget.
// It is a serialization bound, not a process heap limit.
const MaxPayloadBytes int64 = 256 << 20

const compactEnvelopeVersion = 2

type compactEnvelope struct {
	Format       string `json:"format"`
	Version      int    `json:"version"`
	SHA256       string `json:"sha256"`
	Encoding     string `json:"encoding"`
	PayloadBytes int64  `json:"payload_bytes"`
	Payload      []byte `json:"payload"`
}

type Limits struct {
	MaxBytes int64
	// MaxPayloadBytes optionally lowers the expanded JSON bound for this read.
	MaxPayloadBytes int64
}
type envelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
	// Base64 preserves the exact hashed payload without JSON reserialization.
	Payload []byte `json:"payload"`
}

func strict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("semantic: trailing JSON")
	}
	return nil
}

// Write validates and publishes a single immutable file inside a caller-owned
// staging directory. It never overwrites an existing file, changes CURRENT, or
// makes an incomplete artifact eligible for serving. Snapshot transactions own
// those decisions. Files remain bounded to 64 MiB. Large payloads use a
// deterministic gzip envelope with a separate 256 MiB expansion bound.
func Write(path string, a *Artifact) error {
	if e := a.Validate(); e != nil {
		return e
	}
	payload, e := json.Marshal(a)
	if e != nil {
		return e
	}
	if int64(len(payload)) > MaxPayloadBytes {
		return fmt.Errorf("semantic: payload exceeds %d-byte limit", MaxPayloadBytes)
	}
	h := sha256.Sum256(payload)
	var b []byte
	if len(payload) > 64<<10 {
		b, e = encodeCompact(payload)
	} else {
		b, e = json.Marshal(envelope{Format, FormatVersion, hex.EncodeToString(h[:]), payload})
	}
	if e != nil {
		return e
	}
	if int64(len(b)) > DefaultMaxBytes {
		return fmt.Errorf("semantic: artifact exceeds %d-byte limit (encoded size %d bytes)", DefaultMaxBytes, len(b))
	}
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".semantic-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	// Link is an atomic exclusive publication on the same filesystem. Rename
	// would silently replace a previously staged immutable artifact.
	if e = os.Link(f.Name(), path); e != nil {
		return fmt.Errorf("semantic: exclusive publication: %w", e)
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Read verifies the format, payload hash, IDs and all cross-record references.
// MaxBytes bounds the entire file (including base64 overhead), even when the
// file changes during reading. Expanded payloads are independently bounded by
// MaxPayloadBytes. This decoder materializes the artifact; neither bound is a
// process heap ceiling.
func Read(path string, limits Limits) (*Artifact, error) {
	n := limits.MaxBytes
	if n == 0 {
		n = DefaultMaxBytes
	}
	if n < 1 || n > DefaultMaxBytes {
		return nil, fmt.Errorf("semantic: invalid byte limit (max %d)", DefaultMaxBytes)
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > n {
		return nil, fmt.Errorf("semantic: invalid or oversized artifact")
	}
	limits.MaxBytes = n
	return ReadFrom(f, limits)
}

// ReadFrom verifies one bounded envelope from r without closing it. Callers own
// source-file regularity checks, cancellation, and aggregate actual-byte counts.
func ReadFrom(r io.Reader, limits Limits) (*Artifact, error) {
	n := limits.MaxBytes
	if n == 0 {
		n = DefaultMaxBytes
	}
	if n < 1 || n > DefaultMaxBytes {
		return nil, fmt.Errorf("semantic: invalid byte limit (max %d)", DefaultMaxBytes)
	}
	expanded := limits.MaxPayloadBytes
	if expanded == 0 {
		expanded = MaxPayloadBytes
	}
	if expanded < 1 || expanded > MaxPayloadBytes {
		return nil, fmt.Errorf("semantic: invalid expanded byte limit")
	}
	b, e := io.ReadAll(io.LimitReader(r, n+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > n {
		return nil, fmt.Errorf("semantic: artifact exceeds byte limit")
	}
	var header struct {
		Version int `json:"version"`
	}
	if e = json.Unmarshal(b, &header); e != nil {
		return nil, e
	}
	var payload []byte
	var digest string
	switch header.Version {
	case FormatVersion:
		var env envelope
		if e = strict(b, &env); e != nil {
			return nil, e
		}
		if env.Format != Format {
			return nil, fmt.Errorf("semantic: unsupported envelope")
		}
		payload, digest = env.Payload, env.SHA256
	case compactEnvelopeVersion:
		var env compactEnvelope
		if e = strict(b, &env); e != nil {
			return nil, e
		}
		payload, e = decodeCompact(env, expanded)
		if e != nil {
			return nil, e
		}
		digest = env.SHA256
	default:
		return nil, fmt.Errorf("semantic: unsupported envelope")
	}
	if int64(len(payload)) > expanded {
		return nil, fmt.Errorf("semantic: payload exceeds expanded byte limit")
	}
	h := sha256.Sum256(payload)
	if digest != hex.EncodeToString(h[:]) {
		return nil, fmt.Errorf("semantic: payload digest mismatch")
	}
	var a Artifact
	if e = strict(payload, &a); e != nil {
		return nil, e
	}
	if e = a.Validate(); e != nil {
		return nil, e
	}
	return &a, nil
}

func encodeCompact(payload []byte) ([]byte, error) {
	if int64(len(payload)) > MaxPayloadBytes {
		return nil, fmt.Errorf("semantic: payload exceeds byte limit")
	}
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(payload); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	h := sha256.Sum256(payload)
	return json.Marshal(compactEnvelope{Format, compactEnvelopeVersion, hex.EncodeToString(h[:]), "gzip", int64(len(payload)), compressed.Bytes()})
}

func decodeCompact(env compactEnvelope, expanded int64) ([]byte, error) {
	if env.Format != Format || env.Encoding != "gzip" || env.PayloadBytes < 1 || env.PayloadBytes > expanded {
		return nil, fmt.Errorf("semantic: invalid compact envelope or expanded byte limit")
	}
	raw := bytes.NewReader(env.Payload)
	r, err := gzip.NewReader(raw)
	if err != nil {
		return nil, fmt.Errorf("semantic: compressed payload: %w", err)
	}
	r.Multistream(false)
	defer r.Close()
	payload, err := io.ReadAll(io.LimitReader(r, env.PayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("semantic: compressed payload: %w", err)
	}
	if int64(len(payload)) != env.PayloadBytes || raw.Len() != 0 {
		return nil, fmt.Errorf("semantic: expanded length mismatch or trailing compressed data")
	}
	return payload, nil
}
