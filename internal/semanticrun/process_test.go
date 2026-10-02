package semanticrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessBoundsAndEnvironment(t *testing.T) {
	t.Setenv("MOEDEX_PARENT_SECRET", "hidden")
	s := ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "printf '%s' \"${MOEDEX_PARENT_SECRET-absent}\""}, Dir: t.TempDir(), StdoutLimit: 1024, StderrLimit: 1024}
	r, e := RunProcess(context.Background(), s)
	if e != nil || string(r.Stdout) != "absent" {
		t.Fatalf("inherited environment: %q %v", r.Stdout, e)
	}
	s.Args = []string{"-c", "while :; do printf '0123456789'; done"}
	s.StdoutLimit = 100
	r, e = RunProcess(context.Background(), s)
	if e == nil || !strings.Contains(e.Error(), "limit") || len(r.Stdout) > 100 {
		t.Fatalf("output bound: %d %v", len(r.Stdout), e)
	}
	s.Args = []string{"-c", "sleep 10"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e = RunProcess(ctx, s); e == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout: %v", e)
	}
}
func TestSuccessfulLeaderDoesNotLeaveChild(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "leaked")
	s := ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "(sleep 0.4; echo leaked > leaked) & exit 0"}, Dir: dir, StdoutLimit: 1024, StderrLimit: 1024}
	if _, e := RunProcess(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	time.Sleep(600 * time.Millisecond)
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("child survived successful leader")
	}
}
