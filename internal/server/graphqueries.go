package server

// graphqueries.go extracts EF DbSet query edges from the shard set. It scans
// blobs for DbSet<T> property declarations to build a property→entity registry,
// then scans for query sites (context.PropertyName.Method()) and emits
// EdgeQueries edges from the enclosing method to the entity type definition.

import (
	"regexp"
	"strings"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/symbol"
)

// QueriesReport accounts for one query-edge extraction pass.
type QueriesReport struct {
	DbContextsScanned   int
	DbSetProperties      int
	QuerySitesFound      int
	QueriesEdges         int
	UnresolvedProperties int
}

// dbSetEntry is one DbSet<T> property declaration: the entity type name and
// the property name used to access it from a DbContext instance.
type dbSetEntry struct {
	entityName   string
	propertyName string
}

var (
	// dbSetPropertyRE matches DbSet<T> property declarations inside DbContext classes.
	// Group 1 = entity type name, group 2 = property name.
	dbSetPropertyRE = regexp.MustCompile(`\bDbSet\s*<\s*([A-Za-z_][A-Za-z0-9_.]*)\s*>\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`)

	// contextAccessRE matches context variable access patterns like _context.Foo. or db.Foo.
	// Group 1 = the property name being accessed.
	contextAccessRE = regexp.MustCompile(`\b(?:_?context|_dbContext|_ctx|_?db)\s*\.\s*([A-Za-z_][A-Za-z0-9_]*)\s*\.`)

	// contextSetRE matches context.Set<T>() explicit access.
	// Group 1 = the entity type name.
	contextSetRE = regexp.MustCompile(`\b(?:_?context|_dbContext|_ctx|_?db)\s*\.\s*Set\s*<\s*([A-Za-z_][A-Za-z0-9_.]*)\s*>`)
)

// addQueryEdges scans every .cs blob to build a DbSet property registry, then
// scans for context.PropertyName. access patterns and emits EdgeQueries edges
// from the enclosing method/type to the entity type definition.
func addQueryEdges(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
) (QueriesReport, error) {
	var report QueriesReport
	processed := make(map[string]bool, len(sweep.sites))

	// Phase A: build the DbSet property registry.
	registry := make(map[string]string) // propertyName → entityName
	for sha, sites := range sweep.sites {
		if processed[sha] {
			continue
		}
		processed[sha] = true

		site := sites[0]
		blob := sweep.idxs[site.shard].Blob(site.blob)
		if blob == nil || len(blob.Content) == 0 {
			continue
		}
		ext := blobPrimaryExtension(blob)
		if ext != ".cs" {
			continue
		}

		mask := queryLiteralMask(blob.Content)
		for _, m := range dbSetPropertyRE.FindAllSubmatchIndex(blob.Content, -1) {
			if queryMasked(mask, m[0]) {
				continue
			}
			entityName := queryLastName(string(blob.Content[m[2]:m[3]]))
			propertyName := string(blob.Content[m[4]:m[5]])
			registry[propertyName] = entityName
			report.DbSetProperties++
		}
		report.DbContextsScanned++
	}

	if len(registry) == 0 {
		return report, nil
	}

	// Phase B: scan for query sites and emit edges.
	processed2 := make(map[string]bool, len(sweep.sites))
	for sha, sites := range sweep.sites {
		if processed2[sha] {
			continue
		}
		processed2[sha] = true

		site := sites[0]
		blob := sweep.idxs[site.shard].Blob(site.blob)
		if blob == nil || len(blob.Content) == 0 {
			continue
		}
		ext := blobPrimaryExtension(blob)
		if ext != ".cs" {
			continue
		}

		syms := sweep.symbols[site.shard].Symbols(site.blob)
		mask := queryLiteralMask(blob.Content)

		// Match context.PropertyName. patterns
		for _, m := range contextAccessRE.FindAllSubmatchIndex(blob.Content, -1) {
			if queryMasked(mask, m[0]) {
				continue
			}
			propertyName := string(blob.Content[m[2]:m[3]])
			entityName, ok := registry[propertyName]
			if !ok {
				report.UnresolvedProperties++
				continue
			}
			report.QuerySitesFound++
			if err := emitQueryEdge(builder, sweep, seen, blob.SHA, syms, m[0], m[1]-m[0], entityName, &report); err != nil {
				return report, err
			}
		}

		// Match context.Set<T>() patterns
		for _, m := range contextSetRE.FindAllSubmatchIndex(blob.Content, -1) {
			if queryMasked(mask, m[0]) {
				continue
			}
			entityName := queryLastName(string(blob.Content[m[2]:m[3]]))
			report.QuerySitesFound++
			if err := emitQueryEdge(builder, sweep, seen, blob.SHA, syms, m[0], m[1]-m[0], entityName, &report); err != nil {
				return report, err
			}
		}
	}

	return report, nil
}

// emitQueryEdge resolves an entity type name and emits an EdgeQueries edge
// from the enclosing symbol at the query site to the entity definition.
func emitQueryEdge(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
	sourceBlobSHA string,
	syms []symbol.Symbol,
	evidenceOffset int,
	evidenceLength int,
	entityName string,
	report *QueriesReport,
) error {
	// Find enclosing symbol for the source node.
	sourceOffset := queryEnclosingOffset(syms, evidenceOffset)

	defs := sweep.merged.Definitions(entityName)
	if len(defs) == 0 {
		return nil
	}

	for _, def := range defs {
		if def.Shard < 0 || def.Shard >= len(sweep.idxs) {
			continue
		}
		targetBlob := sweep.idxs[def.Shard].Blob(def.Blob)
		if targetBlob == nil || targetBlob.SHA == "" {
			continue
		}
		if def.Start < 0 {
			continue
		}

		record := persistedGraphEdge{
			sourceBlob:     sourceBlobSHA,
			sourceOffset:   uint64(sourceOffset),
			typeID:         diskgraph.EdgeQueries,
			targetBlob:     targetBlob.SHA,
			targetOffset:   uint64(def.Start),
			confidence:     uint64(graph.Verified),
			evidenceBlob:   sourceBlobSHA,
			evidence:       uint64(evidenceOffset),
			evidenceLength: uint64(evidenceLength),
		}
		if _, dup := seen[record]; dup {
			continue
		}
		seen[record] = struct{}{}

		if err := builder.Add(sourceBlobSHA, uint64(sourceOffset), diskgraph.Edge{
			Type:         diskgraph.EdgeQueries,
			TargetBlob:   targetBlob.SHA,
			TargetOffset: uint64(def.Start),
			Confidence:   graph.Verified,
			Evidence: graph.Evidence{
				BlobSHA:    sourceBlobSHA,
				ByteOffset: uint64(evidenceOffset),
				ByteLength: uint64(evidenceLength),
			},
		}); err != nil {
			return err
		}
		report.QueriesEdges++
	}
	return nil
}

// queryEnclosingOffset returns the NameStart of the innermost enclosing
// Method or Func symbol at offset, falling back to the innermost Type,
// then 0 if nothing encloses the offset.
func queryEnclosingOffset(syms []symbol.Symbol, offset int) int {
	var bestMethod *symbol.Symbol
	var bestType *symbol.Symbol
	for i := range syms {
		s := &syms[i]
		if offset < s.BodyStart || offset >= s.BodyEnd {
			continue
		}
		switch s.Kind {
		case symbol.Method, symbol.Func:
			if bestMethod == nil || (s.BodyEnd-s.BodyStart) < (bestMethod.BodyEnd-bestMethod.BodyStart) {
				bestMethod = s
			}
		case symbol.Type:
			if bestType == nil || (s.BodyEnd-s.BodyStart) < (bestType.BodyEnd-bestType.BodyStart) {
				bestType = s
			}
		}
	}
	if bestMethod != nil {
		return bestMethod.NameStart
	}
	if bestType != nil {
		return bestType.NameStart
	}
	return 0
}

// queryLastName extracts the last dot-separated segment from a possibly
// qualified name like "Namespace.EntityType" → "EntityType".
func queryLastName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// queryMasked reports whether offset falls inside a masked region.
func queryMasked(mask []bool, off int) bool {
	return off < 0 || off >= len(mask) || mask[off]
}

// queryLiteralMask marks comments and string/char literals so framework-looking
// text inside documentation or configuration strings cannot trigger false
// edges. Mirrors the classify package's literalMask.
func queryLiteralMask(content []byte) []bool {
	mask := make([]bool, len(content))
	mark := func(lo, hi int) {
		if hi > len(mask) {
			hi = len(mask)
		}
		for i := lo; i < hi; i++ {
			mask[i] = true
		}
	}
	for i := 0; i < len(content); {
		switch {
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '/':
			start := i
			i += 2
			for i < len(content) && content[i] != '\n' {
				i++
			}
			mark(start, i)
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '*':
			start := i
			i += 2
			for i+1 < len(content) && !(content[i] == '*' && content[i+1] == '/') {
				i++
			}
			if i+1 < len(content) {
				i += 2
			}
			mark(start, i)
		case content[i] == '"' || content[i] == '\'':
			start, quote := i, content[i]
			if quote == '"' && i+2 < len(content) && content[i+1] == '"' && content[i+2] == '"' {
				i += 3
				for i+2 < len(content) && !(content[i] == '"' && content[i+1] == '"' && content[i+2] == '"') {
					i++
				}
				if i+2 < len(content) {
					i += 3
				}
				mark(start, i)
				continue
			}
			verbatim := quote == '"' && i > 0 && content[i-1] == '@'
			i++
			for i < len(content) {
				if verbatim && content[i] == '"' && i+1 < len(content) && content[i+1] == '"' {
					i += 2
					continue
				}
				if !verbatim && content[i] == '\\' {
					i += 2
					continue
				}
				i++
				if content[i-1] == quote {
					break
				}
			}
			mark(start, i)
		default:
			i++
		}
	}
	return mask
}
