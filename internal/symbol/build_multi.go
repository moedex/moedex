package symbol

import (
	"path/filepath"
	"strings"

	"moedex/internal/index"
)

// ExtractorForPath returns the Extractor to use for a file path, chosen by its
// extension, or nil when the extension is not one moedex extracts symbols for.
// Callers and tests use this to dispatch a single file/blob. Recognized:
//
//	.go          -> GoExtractor
//	.cs          -> CSharpExtractor
//	.ts, .tsx    -> TSExtractor
//	.cfm, .cfc   -> CFExtractor
//	.sql         -> SQLExtractor
//
// The match is case-insensitive on the extension.
func ExtractorForPath(relPath string) Extractor {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".go":
		return GoExtractor{}
	case ".cs":
		return CSharpExtractor{}
	case ".ts", ".tsx":
		return TSExtractor{}
	case ".cfm", ".cfc":
		return CFExtractor{}
	case ".sql":
		return SQLExtractor{}
	default:
		return nil
	}
}

// BuildMulti runs the language-appropriate extractor over every blob in ix and
// returns a symbol Index. The extractor is chosen per blob from the extension of
// blob.Files[0].RelPath via ExtractorForPath.
//
// Limitation: a blob may map to multiple files (content dedup — see
// index.AddFile). BuildMulti only inspects the FIRST file's extension. In the
// rare case where identical content is committed under different-language
// extensions, the other files' languages are ignored. This matches the
// content-addressed model: one blob, one symbol set.
//
// Blobs with no files, an unrecognized first extension, or no extracted symbols
// are skipped — the same "no symbols, fall back to heuristic scoping" contract
// as Build. Extractors here never error; any error is treated as no symbols.
func BuildMulti(ix *index.Index) *Index {
	out := NewIndex()
	out.deferNames = true
	defer out.rebuildNames()
	if ix == nil {
		return out
	}
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		blob := ix.Blob(id)
		if blob == nil || len(blob.Files) == 0 {
			continue
		}
		ext := ExtractorForPath(blob.Files[0].RelPath)
		if ext == nil {
			continue
		}
		// References (find-refs scaffolding) only materialize for extractors
		// that implement RefExtractor/DefsRefsExtractor — Go (precise) and C#
		// (best-effort) in this slice; TS/CFML/SQL produce definitions only and
		// simply carry no references. extractDefsRefs prefers a single combined
		// parse/scan pass when the extractor supports one.
		syms, occs, err := extractDefsRefs(ext, blob.Content)
		if err != nil || len(syms) == 0 {
			continue
		}
		out.Set(id, syms)
		if len(occs) > 0 {
			out.SetRefs(id, occs)
		}
	}
	return out
}
