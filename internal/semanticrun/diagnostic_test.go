package semanticrun

import (
	"strings"
	"testing"
)

func TestWorkerFailureDiagnosticsExcludeSource(t *testing.T) {
	raw := []byte("{\"record_type\":\"declaration\",\"message\":\"PRIVATE SOURCE\"}\n{\"record_type\":\"diagnostic\",\"severity\":\"hidden\",\"message\":\"HIDDEN\"}\n{\"record_type\":\"diagnostic\",\"severity\":\"error\",\"project\":\"App.csproj\",\"code\":\"CS0246\",\"message\":\"Missing component\",\"source\":\"PRIVATE SOURCE\"}\nmalformed\n")
	out := string(workerFailureDiagnostics([]byte("worker stderr"), raw))
	if strings.Contains(out, "PRIVATE") || strings.Contains(out, "HIDDEN") || !strings.Contains(out, "CS0246") || !strings.Contains(out, "worker stderr") {
		t.Fatal(out)
	}
	many := []byte(strings.Repeat(string(raw), 10000))
	if got := workerFailureDiagnostics(nil, many); len(got) > 6144 {
		t.Fatal(len(got))
	}
}
