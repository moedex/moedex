package blobstore

// ExportDedupedShardDir is the SERVED-SHARD-DEDUP export: it materializes a
// servable shard dir whose content is stored ONCE for the whole served corpus, in
// a shared content store (diskstore blobs.dat), referenced by content hash from
// content-less MOEDEX05 shards. This realizes the CAS's cross-shard dedup on the
// SERVED side, which ExportShardDir (the parity-preserving MOEDEX03 bridge) does
// not: that one re-inlines a blob's content into every shard holding a carrying
// repo (dedup only within a shard).
//
// PARITY: the export streams the EXACT same (sha, content, repo, rel, abs) tuples
// through index.AddFile that ExportShardDir / a direct parity build does, so the
// trigram postings, blob-to-file mapping, and searchable bytes are byte-identical.
// The ONLY difference is WHERE content is stored: once in blobs.dat (deduped)
// instead of inlined per shard. server.Open / server.OpenRank auto-detect the
// shared store and resolve content from it, returning byte-identical (file,line)
// matches. The shard packing (which repos land in which shard) is identical to
// ExportShardDir, so the manifest is the same shape.

import (
	"fmt"
	"path/filepath"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/parity"
)

// ExportDedupedShardDir reads the CAS + BlobManifest at casDir and writes a deduped
// servable shard dir to outShardDir: content-less MOEDEX05 *.idx shards (byte-
// bucketed exactly as ExportShardDir, checked at repo boundaries), a single shared
// content store (diskstore.ContentStoreName / blobs.dat) holding each unique blob's
// content ONCE, and a parity.Manifest. Pass shardBytes<=0 for parity.DefaultShardBytes.
//
// The shard byte-bucketing uses the SAME accumulated-content threshold as
// ExportShardDir (it accounts for every file reference's content bytes, including
// repeats of an already-stored blob), so the repo→shard packing — and thus the
// manifest and the cross-shard merge behavior — is identical between the two
// exports. The dedup is purely in the physical content storage: blobs.dat holds
// the unique set once, whereas ExportShardDir's shards re-inline repeats.
//
// It returns the manifest and the total content bytes physically stored in the
// shared store (the deduped footprint, ~= blobstore StoredBytes for the referenced
// set) so callers/tests can assert the dedup win directly.
func ExportDedupedShardDir(casDir, outShardDir string, shardBytes int64) (*parity.Manifest, int64, error) {
	m, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		return nil, 0, fmt.Errorf("blobstore: load blob manifest: %w", err)
	}
	store, err := Open(casDir)
	if err != nil {
		return nil, 0, err
	}
	defer store.Close()

	// One shared content-store writer for the WHOLE dir: PutContent is idempotent on
	// the content hash, so a blob carried by repos that land in different shards is
	// stored exactly once here — the served-side cross-shard dedup.
	cw, err := diskstore.NewContentStoreWriter()
	if err != nil {
		return nil, 0, err
	}
	defer cw.Close()
	saveShard := func(ix *index.Index, path string) error {
		// AddFile (in exportShards) builds the trigram postings + blob/file mapping
		// exactly as ExportShardDir does; SaveDeduped here registers the content in
		// cw and writes the shard WITHOUT inlining it.
		return diskstore.SaveDeduped(ix, path, cw)
	}
	out, err := exportShards(m, store, outShardDir, shardBytes, saveShard)
	if err != nil {
		return nil, 0, err
	}

	// Write the single shared content store the shards reference. This is the only
	// place the corpus's content bytes are stored — once per unique blob.
	if err := cw.Write(filepath.Join(outShardDir, diskstore.ContentStoreName)); err != nil {
		return nil, 0, fmt.Errorf("blobstore: write shared content store: %w", err)
	}
	return out, cw.BytesStored(), nil
}
