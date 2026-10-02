package indexcmd

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/parity"
	"moedex/internal/semantic"
)

func TestSemanticOptionalInputsAcrossRetainedVariants(t *testing.T) {
	const path = "obj/Debug/net8.0/Generated.editorconfig"
	repos := map[string]string{"one": "repo", "two": "repo"}
	makeArtifact := func(second string) *semantic.Artifact {
		a := &semantic.Artifact{}
		for i, content := range []string{"root=/retained/one", second} {
			capture, err := json.Marshal(map[string]any{"input": map[string]any{"path": path, "scope": "source", "sha256": semanticSnapshotSHA([]byte(content)), "byte_size": len(content)}})
			if err != nil {
				t.Fatal(err)
			}
			a.Contexts = append(a.Contexts, semantic.BuildContext{SnapshotID: []string{"one", "two"}[i], Project: "App.csproj", Capture: string(capture)})
		}
		return a
	}
	makeIndex := func(content string) *parity.Manifest {
		ix := index.New()
		raw := []byte(content)
		h := sha1.New()
		fmt.Fprintf(h, "blob %d\x00", len(raw))
		h.Write(raw)
		ix.AddFile("repo", path, "/ignored", fmt.Sprintf("%x", h.Sum(nil)), raw)
		shard := filepath.Join(t.TempDir(), "shard.idx")
		if err := diskstore.Save(ix, shard); err != nil {
			t.Fatal(err)
		}
		return &parity.Manifest{Shards: []parity.ShardManifest{{Path: shard}}}
	}
	// The retained roots have already passed Revalidate independently. Absent
	// optional derived inputs have no current lexical bytes to contradict.
	different := makeArtifact("root=/retained/two")
	if err := matchSemanticIndex(context.Background(), different, repos, &parity.Manifest{}); err != nil {
		t.Fatal(err)
	}
	indexed := makeIndex("root=/retained/one")
	if err := matchSemanticIndex(context.Background(), different, repos, indexed); err == nil {
		t.Fatal("conflicting indexed build input admitted")
	}
	if err := matchSemanticIndex(context.Background(), makeArtifact("root=/retained/one"), repos, indexed); err != nil {
		t.Fatal(err)
	}
	for _, generated := range []bool{false, true} {
		a := makeArtifact("root=/retained/two")
		a.Sources = []semantic.Source{{SnapshotID: "one", Path: path, RawSHA256: semanticSnapshotSHA([]byte("root=/retained/one")), ByteSize: uint64(len("root=/retained/one")), Generated: generated}}
		err := matchSemanticIndex(context.Background(), a, repos, &parity.Manifest{})
		if generated && err != nil {
			t.Fatal(err)
		}
		if !generated && err == nil {
			t.Fatal("conflicting ordinary source admitted")
		}
	}
	a := makeArtifact("root=/retained/two")
	a.Contexts[1].Project = path // Promote the later variant to a required project input.
	if err := matchSemanticIndex(context.Background(), a, repos, &parity.Manifest{}); err == nil {
		t.Fatal("conflicting required project admitted")
	}
}
