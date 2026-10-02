package semantic

import (
	"fmt"
	"strconv"
	"strings"
)

// OpenGenericTemplateArity validates an original, non-nested generic definition.
// The producing compiler establishes interface/class kind and correspondence.
func OpenGenericTemplateArity(s Symbol) (int, error) {
	k := s.Key
	name, arityText, ok := strings.Cut(k.Descriptor, "`")
	arity, err := strconv.Atoi(arityText)
	if k.Language != "csharp" || !oneOf(k.NamespaceKind, "project", "assembly") || !required(k.Namespace) || k.DescriptorKind != "documentation_comment_id" || !ok || !strings.HasPrefix(name, "T:") || len(name) <= 2 || strings.ContainsAny(name, "{}[]#()+") || err != nil || strconv.Itoa(arity) != arityText || arity < 1 || arity > 8 {
		return 0, fmt.Errorf("semantic: invalid open generic template")
	}
	return arity, nil
}

// ValidateDomainTargetCorrespondence validates relationships between targets
// after the caller has validated their individual shape and identity references.
func ValidateDomainTargetCorrespondence(f DomainFact, targets []Symbol) error {
	if f.Kind != "di_open_generic_registration_configuration" {
		return nil
	}
	if len(targets) != 2 {
		return fmt.Errorf("semantic: open DI requires two templates")
	}
	service, e1 := OpenGenericTemplateArity(targets[0])
	implementation, e2 := OpenGenericTemplateArity(targets[1])
	if e1 != nil || e2 != nil || service != implementation || targets[0].ID == targets[1].ID {
		return fmt.Errorf("semantic: incompatible open DI templates")
	}
	return nil
}
