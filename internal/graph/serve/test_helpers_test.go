package graphserve

import (
	"path/filepath"
	"sort"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
)

func buildShard(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	ix := index.New()
	for path, content := range files {
		ix.AddFile("repoA", path, filepath.Join("/abs", path), path, []byte(content))
	}
	if err := diskstore.Save(ix, filepath.Join(dir, name)); err != nil {
		t.Fatalf("save shard %s: %v", name, err)
	}
}

func buildDedupedDir(t *testing.T, repos map[string]map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writer, err := diskstore.NewContentStoreWriter()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	labels := make([]string, 0, len(repos))
	for label := range repos {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for indexNumber, label := range labels {
		ix := index.New()
		paths := make([]string, 0, len(repos[label]))
		for path := range repos[label] {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			content := []byte(repos[label][path])
			ix.AddFile(label, path, filepath.Join("/abs", label, path), diskstore.GitBlobSHA1(content), content)
		}
		if err := diskstore.SaveDeduped(ix, filepath.Join(dir, "shard-"+pad4(indexNumber)+".idx"), writer); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Write(filepath.Join(dir, diskstore.ContentStoreName)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pad4(value int) string {
	digits := []byte{'0', '0', '0', '0'}
	for index := 3; index >= 0 && value > 0; index-- {
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits)
}
