package snapshot

import "testing"

func TestSemanticIndexLegacyVersionRemainsAccepted(t *testing.T) {
	_, _, m := stageSemanticIndexFixture(t)
	component := m.Components[SemanticIndexComponent]
	for _, version := range []int{1, 2, 3, 4} {
		component.Version = version
		m.Components[SemanticIndexComponent] = component
		if err := Validate(m); err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
	}
}
