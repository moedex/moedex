package semantic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ValidateConstructedNamedType checks one closed generic level with qualified
// simple named arguments. The producer additionally excludes tuple responses.
func ValidateConstructedNamedType(k SymbolKey) error {
	bad := func() error { return fmt.Errorf("semantic: invalid constructed named type") }
	if k.Language != "csharp" || !oneOf(k.NamespaceKind, "project", "assembly") || !required(k.Namespace) || k.DescriptorKind != "constructed_named_type_v1" || len(k.Descriptor) > 32768 {
		return bad()
	}
	var value struct {
		Definition string      `json:"definition"`
		Arguments  []SymbolKey `json:"arguments"`
	}
	d := json.NewDecoder(bytes.NewBufferString(k.Descriptor))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil {
		return bad()
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return bad()
	}
	name, arityText, ok := strings.Cut(value.Definition, "`")
	arity, err := strconv.Atoi(arityText)
	if !ok || !strings.HasPrefix(name, "T:") || len(name) <= 2 || strings.ContainsAny(name, "{}[]#()") || err != nil || strconv.Itoa(arity) != arityText || arity < 1 || arity > 8 || arity != len(value.Arguments) {
		return bad()
	}
	for _, arg := range value.Arguments {
		if !oneOf(arg.NamespaceKind, "project", "assembly") || !required(arg.Namespace) || !required(arg.Descriptor) || ValidateDomainTarget(Symbol{Key: arg}) != nil {
			return bad()
		}
	}
	return nil
}
