package semantic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const DefaultMaxBytes int64 = 64 << 20

type Limits struct{ MaxBytes int64 }
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
// those decisions. Readers and writers use the same 64 MiB default bound.
func Write(path string, a *Artifact) error {
	if e := a.Validate(); e != nil {
		return e
	}
	payload, e := json.Marshal(a)
	if e != nil {
		return e
	}
	h := sha256.Sum256(payload)
	b, e := json.Marshal(envelope{Format, FormatVersion, hex.EncodeToString(h[:]), payload})
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
// file changes during reading. This decoder intentionally materializes the
// bounded artifact and is unsuitable for corpus-scale serving.
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
	return ReadFrom(f, Limits{MaxBytes: n})
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
	b, e := io.ReadAll(io.LimitReader(r, n+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > n {
		return nil, fmt.Errorf("semantic: artifact exceeds byte limit")
	}
	var env envelope
	if e = strict(b, &env); e != nil {
		return nil, e
	}
	if env.Format != Format || env.Version != FormatVersion {
		return nil, fmt.Errorf("semantic: unsupported envelope")
	}
	h := sha256.Sum256(env.Payload)
	if env.SHA256 != hex.EncodeToString(h[:]) {
		return nil, fmt.Errorf("semantic: payload digest mismatch")
	}
	var a Artifact
	if e = strict(env.Payload, &a); e != nil {
		return nil, e
	}
	if e = a.Validate(); e != nil {
		return nil, e
	}
	return &a, nil
}
