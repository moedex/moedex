package blobstore

// BlobManifest is the CAS's repo->blobset record: the storage-layer freshness
// sidecar that replaces parity's repo->shard mapping (internal/parity/manifest.go)
// with repo->{set of blob SHAs}. Because a repo's identity is its blob set, a
// per-repo delta is a pure set-diff: RefreshCAS adds only the SHAs a changed repo
// gained and drops a removed repo's entry, never touching co-resident repos.
//
// Each repo records its ordered file entries (SHA + repo-relative path), not just
// the SHA set, so ExportShardDir can replay the EXACT same (sha, content, repo,
// rel, abs) tuples through index.AddFile that a direct parity build would — which
// is what keeps ripgrep parity and the served-shard FileRefs byte-equivalent. The
// AbsPath is reconstructed as filepath.Join(repo.Dir, entry.RelPath), matching
// ingest.Repo, so it is not stored redundantly.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// BlobManifestVersion is the schema version. Bump on incompatible changes.
const BlobManifestVersion = 1

// BlobManifestName is the manifest filename written under the CAS dir.
const BlobManifestName = "blobmanifest.json"

// FileEntry is one tracked text file of a repo, by content SHA and relative path.
// Two entries with the same SHA (identical content at two paths) are both kept:
// the dedup is on storage (one blob in the CAS), not on file references.
type FileEntry struct {
	SHA     string `json:"sha"`
	RelPath string `json:"rel"`
}

// RepoBlobs is a repo's recorded blob set at ingest: its directory, label, git
// HEAD (the freshness key, "" if unreadable — same convention as ingest.Head),
// and its ordered file entries.
type RepoBlobs struct {
	Dir   string      `json:"dir"`
	Label string      `json:"label"`
	Head  string      `json:"head"`
	Files []FileEntry `json:"files"`
}

// blobSet returns the deduplicated, sorted set of blob SHAs this repo references.
func (r RepoBlobs) blobSet() map[string]bool {
	s := make(map[string]bool, len(r.Files))
	for _, f := range r.Files {
		s[f.SHA] = true
	}
	return s
}

// SortedBlobs returns the repo's distinct blob SHAs, sorted — the canonical blob
// set used for delta set-diffs.
func (r RepoBlobs) SortedBlobs() []string {
	set := r.blobSet()
	out := make([]string, 0, len(set))
	for sha := range set {
		out = append(out, sha)
	}
	sort.Strings(out)
	return out
}

// Stats are the global dedup figures recorded with the manifest.
type Stats struct {
	// UniqueBlobs is the number of distinct blob SHAs stored in the CAS.
	UniqueBlobs int `json:"unique_blobs"`
	// StoredBytes is the total content bytes physically stored (sum of unique
	// blob content lengths) — the deduplicated footprint.
	StoredBytes int64 `json:"stored_bytes"`
	// RawBytes is the total content bytes SEEN across all repos' files (counting
	// every file reference). RawBytes - StoredBytes is the dedup saving.
	RawBytes int64 `json:"raw_bytes"`
	// FileRefs is the total number of (repo,file) references across all repos.
	FileRefs int `json:"file_refs"`
}

// DedupRatio is RawBytes/StoredBytes (1.0 == no duplication; higher means more
// duplication eliminated). >1 quantifies the cross-shard dedup win. Returns 0
// for an empty store.
func (s Stats) DedupRatio() float64 {
	if s.StoredBytes == 0 {
		return 0
	}
	return float64(s.RawBytes) / float64(s.StoredBytes)
}

// BlobManifest maps every repo to its blob set and records global dedup stats.
type BlobManifest struct {
	Version int         `json:"version"`
	Root    string      `json:"root"`
	CASDir  string      `json:"cas_dir"`
	Repos   []RepoBlobs `json:"repos"`
	Stats   Stats       `json:"stats"`
}

// RepoOf returns the recorded RepoBlobs for a repo directory and whether present.
func (m *BlobManifest) RepoOf(dir string) (RepoBlobs, bool) {
	for _, r := range m.Repos {
		if r.Dir == dir {
			return r, true
		}
	}
	return RepoBlobs{}, false
}

// WriteBlobManifest serializes m to path atomically (temp+rename), fsyncing
// before the rename, mirroring parity.WriteManifest so a crash mid-write never
// leaves a half-manifest AND never leaves the directory entry pointing at bytes
// that aren't yet durable.
func WriteBlobManifest(path string, m *BlobManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileDurable(path, data, 0o644)
}

// LoadBlobManifest reads and parses a manifest written by WriteBlobManifest.
func LoadBlobManifest(path string) (*BlobManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m BlobManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse blob manifest %s: %w", path, err)
	}
	return &m, nil
}

// diffBlobs returns the SHAs in cur that are not in old (the net-new set for a
// changed repo's delta). Both are blob-set maps.
func diffBlobs(old, cur map[string]bool) []string {
	var added []string
	for sha := range cur {
		if !old[sha] {
			added = append(added, sha)
		}
	}
	sort.Strings(added)
	return added
}

// referencedBlobs returns the union of every repo's blob set: the set of SHAs
// still live (referenced by at least one repo). Blobs in the CAS pack not in this
// set are unreferenced garbage (left for a future compactor; see RefreshCAS).
func (m *BlobManifest) referencedBlobs() map[string]bool {
	live := map[string]bool{}
	for _, r := range m.Repos {
		for sha := range r.blobSet() {
			live[sha] = true
		}
	}
	return live
}
