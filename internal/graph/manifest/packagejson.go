package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// parsePackageJSON reads an npm manifest: the package name it publishes under and
// its dependencies and devDependencies.
//
// Only those two sections are read. peerDependencies and optionalDependencies
// describe what a consumer must supply or may omit rather than what this package
// consumes, so folding them in would change what a DEPENDS_ON edge means.
//
// The walk is over json.Decoder tokens rather than an Unmarshal into a struct
// because each entry needs the byte offset of its own key. A syntax error returns
// what was read up to that point alongside the error.
func parsePackageJSON(relPath string, content []byte) (Manifest, error) {
	m := Manifest{Kind: PackageJSON, Path: relPath}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()

	token, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return m, nil
		}
		return m, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return m, nil // an array or scalar is not a package manifest
	}

	for dec.More() {
		key, ok, err := jsonKey(dec, content)
		if err != nil {
			return m, err
		}
		if !ok {
			return m, nil
		}
		switch key.text {
		case "name":
			value, err := dec.Token()
			if err != nil {
				return m, err
			}
			name, isString := value.(string)
			if !isString || strings.TrimSpace(name) == "" {
				break
			}
			// The name's own offset, not the key's: the identity is the value.
			offset := quotedTextStart(content, int(dec.InputOffset()))
			if offset < 0 {
				offset = key.off
			}
			m.Provides = append(m.Provides, Provided{
				Kind:   PackageJSON,
				Name:   strings.TrimSpace(name),
				Offset: offset,
				Rule:   RuleDeclaredName,
			})
		case "dependencies", "devDependencies":
			declarations, err := jsonDependencySection(dec, content, key.text == "devDependencies")
			m.Requires = append(m.Requires, declarations...)
			if err != nil {
				return m, err
			}
		default:
			if err := skipJSONValue(dec); err != nil {
				return m, err
			}
		}
	}
	return m, nil
}

// jsonDependencySection reads one {name: range} object into declarations. A
// section that is not an object (null, or a string in a malformed manifest) is
// skipped without complaint.
func jsonDependencySection(dec *json.Decoder, content []byte, dev bool) ([]Declaration, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		if ok {
			return nil, skipJSONRemainder(dec, delim)
		}
		return nil, nil
	}

	var out []Declaration
	for dec.More() {
		key, ok, err := jsonKey(dec, content)
		if err != nil || !ok {
			return out, err
		}
		value, err := dec.Token()
		if err != nil {
			return out, err
		}
		if delim, isDelim := value.(json.Delim); isDelim {
			// A nested object where a version range belongs: not a dependency.
			if err := skipJSONRemainder(dec, delim); err != nil {
				return out, err
			}
			continue
		}
		if strings.TrimSpace(key.text) == "" {
			continue
		}
		version, _ := value.(string)
		out = append(out, Declaration{
			Kind:    PackageJSON,
			Ref:     PackageRef,
			Name:    strings.TrimSpace(key.text),
			Version: version,
			Offset:  key.off,
			Dev:     dev,
		})
	}
	// Consume the closing brace so the caller resumes at the next key.
	if _, err := dec.Token(); err != nil {
		return out, err
	}
	return out, nil
}

// jsonKey reads an object key and locates the first byte of its text in content.
func jsonKey(dec *json.Decoder, content []byte) (field, bool, error) {
	token, err := dec.Token()
	if err != nil {
		return field{}, false, err
	}
	text, ok := token.(string)
	if !ok {
		return field{}, false, nil // the closing brace, or malformed input
	}
	offset := quotedTextStart(content, int(dec.InputOffset()))
	if offset < 0 {
		offset = 0
	}
	return field{text: text, off: offset}, true, nil
}

// quotedTextStart maps the decoder position just past a JSON string's closing
// quote to the offset of the string's first content byte. Escapes are honored so
// a name containing \" is still located correctly. It returns -1 when end does
// not in fact sit just past a closing quote.
func quotedTextStart(content []byte, end int) int {
	if end <= 0 || end > len(content) || content[end-1] != '"' {
		return -1
	}
	for i := end - 2; i >= 0; i-- {
		if content[i] != '"' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && content[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return i + 1
		}
	}
	return -1
}

// skipJSONValue consumes the next value, whatever its shape.
func skipJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		return skipJSONRemainder(dec, delim)
	}
	return nil
}

// skipJSONRemainder consumes the rest of a composite value whose opening
// delimiter has already been read.
func skipJSONRemainder(dec *json.Decoder, open json.Delim) error {
	if open != '{' && open != '[' {
		return nil // a closing delimiter: nothing left to skip
	}
	depth := 1
	for depth > 0 {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delim {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return nil
}
