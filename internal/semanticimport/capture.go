package semanticimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// verifyInputs rechecks source-projection build inputs, including project files,
// imports and restore assets. SDK/external inputs retain their captured digests;
// this importer does not claim to independently attest to another toolchain.
func verifyInputs(ctx context.Context, root *os.Root, capture string, limit int64, seen map[string]string, total *int64) error {
	var value any
	decoder := json.NewDecoder(strings.NewReader(capture))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var visit func(any) error
	visit = func(value any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		case map[string]any:
			scope, hasScope := node["scope"].(string)
			_, hasHash := node["sha256"]
			if hasScope {
				if !hasHash {
					return fmt.Errorf("semantic import: captured input digest missing")
				}
				if scope != "source" && scope != "sdk" && scope != "external" {
					return fmt.Errorf("semantic import: unknown captured input scope")
				}
				if scope == "source" {
					name, ok := node["path"].(string)
					if !ok || !validPath(name) {
						return fmt.Errorf("semantic import: invalid captured input path")
					}
					expected, ok := node["sha256"].(string)
					if !ok {
						return fmt.Errorf("semantic import: captured input digest missing")
					}
					number, ok := node["byte_size"].(json.Number)
					if !ok {
						return fmt.Errorf("semantic import: captured input size missing")
					}
					size, err := number.Int64()
					if err != nil || size < 0 {
						return fmt.Errorf("semantic import: captured input size exceeds limit")
					}
					signature := fmt.Sprintf("%s:%d", expected, size)
					if prior, ok := seen[name]; ok {
						if prior != signature {
							return fmt.Errorf("semantic import: conflicting captured input metadata")
						}
						return nil
					}
					if size > limit-*total {
						return fmt.Errorf("semantic import: captured input size exceeds limit")
					}
					file, err := root.Open(name)
					if err != nil {
						return fmt.Errorf("semantic import: build input %s: %w", name, err)
					}
					stat, err := file.Stat()
					if err != nil {
						file.Close()
						return err
					}
					if !stat.Mode().IsRegular() || stat.Size() != size {
						file.Close()
						return fmt.Errorf("semantic import: build input size/type changed: %s", name)
					}
					hash := sha256.New()
					n, err := io.Copy(hash, io.LimitReader(file, size+1))
					closeErr := file.Close()
					if err != nil {
						return err
					}
					if closeErr != nil {
						return closeErr
					}
					if n != size || hex.EncodeToString(hash.Sum(nil)) != expected {
						return fmt.Errorf("semantic import: build input changed: %s", name)
					}
					*total += size
					seen[name] = signature
				}
			}
			for _, child := range node {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}
