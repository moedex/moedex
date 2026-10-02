package semanticcmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCaptureSummaryRestoreEvaluation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		b, err := json.Marshal(CaptureSummary{RestoreStandardEvaluation: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"restore_standard_evaluation":true`) != enabled {
			t.Fatalf("restore evaluation summary: %s", b)
		}
		if !enabled && strings.Contains(string(b), "restore_standard_evaluation") {
			t.Fatalf("default should be omitted: %s", b)
		}
	}
}
