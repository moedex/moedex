package cli

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestSnapshotGraphFalseDelegation(t *testing.T) {
	var forwarded []string
	cmd := legacyPrefixedContextCommand("build", "snapshot build", "moedex-index", func(context.Context) {
		forwarded = append([]string(nil), os.Args...)
	}, "snapshot-build")
	cmd.SetArgs([]string{"--graph=false", "--corpus", "/corpus"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := []string{"moedex-index", "snapshot-build", "--graph=false", "--corpus", "/corpus"}
	if !reflect.DeepEqual(forwarded, want) {
		t.Fatalf("delegation = %q, want %q", forwarded, want)
	}
}
