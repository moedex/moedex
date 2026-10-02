package indexcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"moedex/internal/semantic"
)

// A local publication input, not persisted provenance. Keys must cover exactly
// the artifact's snapshots; relative roots are resolved beside the map file.
func semanticWorkspaces(path string, a *semantic.Artifact) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxBytes = 1 << 20
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxBytes {
		return nil, fmt.Errorf("semantic workspaces: expected a regular map file of at most 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("semantic workspaces: map exceeds 1 MiB")
	}
	return decodeSemanticWorkspaces(raw, filepath.Dir(path), a)
}

func decodeSemanticWorkspaces(raw []byte, base string, a *semantic.Artifact) (map[string]string, error) {
	bad := func() (map[string]string, error) {
		return nil, fmt.Errorf("semantic workspaces: expected one nonempty directory per artifact snapshot, with no duplicate or unknown IDs")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return bad()
	}
	expected := map[string]bool{}
	for _, snapshot := range a.Snapshots {
		expected[snapshot.ID] = true
	}
	roots := map[string]string{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return bad()
		}
		id, ok := token.(string)
		if !ok || !expected[id] || roots[id] != "" {
			return bad()
		}
		var root string
		if err := dec.Decode(&root); err != nil || root == "" {
			return bad()
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(base, root)
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		roots[id] = root
	}
	token, err = dec.Token()
	if err != nil || token != json.Delim('}') || len(roots) != len(expected) {
		return bad()
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad()
	}
	return roots, nil
}
