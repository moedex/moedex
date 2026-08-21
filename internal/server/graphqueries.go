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
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// QueriesReport accounts for one query-edge extraction pass.
type QueriesReport struct {
	DbContextsScanned    int
	DbSetProperties      int
	QuerySitesFound      int
	QueriesEdges         int
	UnresolvedProperties int
	UnresolvedTargets    int
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

	csharpNamespaceRE = regexp.MustCompile(`(?m)^[\t ]*namespace[\t ]+([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\s*(?:;|\{)`)
	csharpUsingRE     = regexp.MustCompile(`(?m)^[\t ]*(?:global[\t ]+)?using[\t ]+(?:static[\t ]+)?([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)[\t ]*;`)
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
	// repo -> propertyName -> entity names. A corpus-global property map makes
	// every DbContext.Carts declaration compete with every other repository and
	// can even change by map iteration order.
	registry := make(map[string]map[string]map[string]struct{})
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
			entityName := strings.TrimSpace(string(blob.Content[m[2]:m[3]]))
			propertyName := string(blob.Content[m[4]:m[5]])
			for _, file := range blob.Files {
				if file.Repo == "" {
					continue
				}
				if registry[file.Repo] == nil {
					registry[file.Repo] = make(map[string]map[string]struct{})
				}
				if registry[file.Repo][propertyName] == nil {
					registry[file.Repo][propertyName] = make(map[string]struct{})
				}
				registry[file.Repo][propertyName][entityName] = struct{}{}
			}
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
			entityName, ok := queryRegistryEntity(registry, blob, propertyName)
			if !ok {
				report.UnresolvedProperties++
				continue
			}
			report.QuerySitesFound++
			if err := emitQueryEdge(builder, sweep, seen, blob, syms, m[0], m[1]-m[0], entityName, &report); err != nil {
				return report, err
			}
		}

		// Match context.Set<T>() patterns
		for _, m := range contextSetRE.FindAllSubmatchIndex(blob.Content, -1) {
			if queryMasked(mask, m[0]) {
				continue
			}
			entityName := strings.TrimSpace(string(blob.Content[m[2]:m[3]]))
			report.QuerySitesFound++
			if err := emitQueryEdge(builder, sweep, seen, blob, syms, m[0], m[1]-m[0], entityName, &report); err != nil {
				return report, err
			}
		}
	}

	return report, nil
}

func queryRegistryEntity(registry map[string]map[string]map[string]struct{}, blob *index.Blob, property string) (string, bool) {
	entities := make(map[string]struct{})
	for _, file := range blob.Files {
		for entity := range registry[file.Repo][property] {
			entities[entity] = struct{}{}
		}
	}
	if len(entities) != 1 {
		return "", false
	}
	for entity := range entities {
		return entity, true
	}
	return "", false
}

// emitQueryEdge resolves an entity type name and emits an EdgeQueries edge
// from the enclosing symbol at the query site to the entity definition.
func emitQueryEdge(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
	sourceBlob *index.Blob,
	syms []symbol.Symbol,
	evidenceOffset int,
	evidenceLength int,
	entityName string,
	report *QueriesReport,
) error {
	if sourceBlob == nil || sourceBlob.SHA == "" {
		report.UnresolvedTargets++
		return nil
	}
	sourceBlobSHA := sourceBlob.SHA
	// Find enclosing symbol for the source node.
	sourceOffset := queryEnclosingOffset(syms, evidenceOffset)

	defs := resolveQueryTargets(sweep, sourceBlob, entityName)
	if len(defs) == 0 {
		report.UnresolvedTargets++
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

// resolveQueryTargets promotes only the target half of an EF relationship that
// can be resolved. The query syntax proves a DbSet access; repository identity,
// symbol kind, and C# namespace/imports prove which entity definition it names.
// Ambiguous targets emit no Verified edge.
func resolveQueryTargets(sweep *graphSweep, sourceBlob *index.Blob, entityName string) []symbol.ShardRef {
	shortName := queryLastName(entityName)
	defs := sweep.merged.Definitions(shortName)
	if len(defs) == 0 {
		return nil
	}
	qualifiedNamespace := ""
	if i := strings.LastIndexByte(entityName, '.'); i >= 0 {
		qualifiedNamespace = entityName[:i]
	}
	allowedNamespaces := csharpNamespacesInScope(sourceBlob.Content)
	type candidate struct {
		ref       symbol.ShardRef
		namespace string
	}
	var sameRepo []candidate
	seen := make(map[diskgraph.Key]struct{})
	for _, def := range defs {
		if def.Shard < 0 || def.Shard >= len(sweep.idxs) || def.Start < 0 {
			continue
		}
		targetBlob := sweep.idxs[def.Shard].Blob(def.Blob)
		if targetBlob == nil || targetBlob.SHA == "" || !sweep.blobSHAsShareRepository(sourceBlob.SHA, targetBlob.SHA) {
			continue
		}
		kind, ok := queryDefinitionKind(sweep, def)
		if !ok || (kind != symbol.Type && kind != symbol.Table) {
			continue
		}
		key := diskgraph.Key{BlobSHA: targetBlob.SHA, SymbolOffset: uint64(def.Start)}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		sameRepo = append(sameRepo, candidate{ref: def, namespace: csharpNamespace(targetBlob.Content)})
	}
	var matched []symbol.ShardRef
	for _, candidate := range sameRepo {
		if qualifiedNamespace != "" {
			if candidate.namespace == qualifiedNamespace {
				matched = append(matched, candidate.ref)
			}
			continue
		}
		if allowedNamespaces[candidate.namespace] {
			matched = append(matched, candidate.ref)
		}
	}
	if len(matched) == 1 {
		return matched
	}
	if len(matched) > 1 {
		return nil
	}
	if len(sameRepo) == 1 {
		return []symbol.ShardRef{sameRepo[0].ref}
	}
	return nil
}

func queryDefinitionKind(sweep *graphSweep, def symbol.ShardRef) (symbol.Kind, bool) {
	if def.Shard < 0 || def.Shard >= len(sweep.symbols) {
		return symbol.KindUnknown, false
	}
	for _, candidate := range sweep.symbols[def.Shard].Symbols(def.Blob) {
		if candidate.NameStart == def.Start {
			return candidate.Kind, true
		}
	}
	return symbol.KindUnknown, false
}

func csharpNamespacesInScope(content []byte) map[string]bool {
	out := make(map[string]bool)
	if namespace := csharpNamespace(content); namespace != "" {
		out[namespace] = true
	}
	for _, match := range csharpUsingRE.FindAllSubmatch(content, -1) {
		out[string(match[1])] = true
	}
	return out
}

func csharpNamespace(content []byte) string {
	match := csharpNamespaceRE.FindSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	return string(match[1])
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
