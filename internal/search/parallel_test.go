package search

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"

	"moedex/internal/index"
)

func parallelTestIndex(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	for i := 0; i < 96; i++ {
		content := fmt.Sprintf("package p%d\nhandler response payload x%d\n", i, i)
		if i%7 == 0 {
			content += "special needle line\n"
		}
		ix.AddFile("r", fmt.Sprintf("f%d.go", i), fmt.Sprintf("/abs/f%d.go", i), fmt.Sprintf("sha-%d", i), []byte(content))
	}
	return ix
}

func fillVerifyPermits() int {
	n := 0
	for {
		select {
		case verifyPermits <- struct{}{}:
			n++
		default:
			return n
		}
	}
}

func drainVerifyPermits(n int) {
	for i := 0; i < n; i++ {
		<-verifyPermits
	}
}

func TestParallelRegexMatchesSerial(t *testing.T) {
	old := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(old)

	ix := parallelTestIndex(t)
	filled := fillVerifyPermits()
	serial, serialStats, err := RegexWithStats(ix, "handler|response|payload")
	drainVerifyPermits(filled)
	if err != nil {
		t.Fatal(err)
	}
	parallel, parallelStats, err := RegexWithStats(ix, "handler|response|payload")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("parallel regex results differ from serial:\nserial=%v\nparallel=%v", serial, parallel)
	}
	if serialStats.ParallelWorkers != 1 {
		t.Fatalf("forced-serial regex workers = %d, want 1", serialStats.ParallelWorkers)
	}
	if parallelStats.ParallelWorkers <= 1 {
		t.Fatalf("parallel regex workers = %d, want >1", parallelStats.ParallelWorkers)
	}
}

func TestParallelShortLiteralMatchesSerial(t *testing.T) {
	old := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(old)

	ix := parallelTestIndex(t)
	filled := fillVerifyPermits()
	serial, serialStats := LiteralWithStats(ix, "x")
	drainVerifyPermits(filled)
	parallel, parallelStats := LiteralWithStats(ix, "x")
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatalf("parallel literal results differ from serial:\nserial=%v\nparallel=%v", serial, parallel)
	}
	if serialStats.ParallelWorkers != 1 {
		t.Fatalf("forced-serial literal workers = %d, want 1", serialStats.ParallelWorkers)
	}
	if parallelStats.ParallelWorkers <= 1 {
		t.Fatalf("parallel literal workers = %d, want >1", parallelStats.ParallelWorkers)
	}
}

func TestParallelRegexDeterministic(t *testing.T) {
	ix := parallelTestIndex(t)
	want, _, err := RegexWithStats(ix, "special|payload")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, _, err := RegexWithStats(ix, "special|payload")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("parallel regex iteration %d differed:\nwant=%v\ngot=%v", i, want, got)
		}
	}
}
