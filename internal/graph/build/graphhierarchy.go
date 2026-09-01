package graphbuild

// graphhierarchy.go extracts type-hierarchy edges (extends, implements,
// contains_method) from the shard set's blob content and symbol index. It runs
// as a separate pass in the graph build alongside manifest and HTTP edges.
//
// Inheritance clauses are parsed per-language from raw content at graph-build
// time — no symbol-model change, no index-format change. Super names are
// resolved via the merged cross-shard symbol corpus.

import (
	"path/filepath"
	"strings"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// HierarchyReport accounts for one hierarchy extraction pass.
type HierarchyReport struct {
	TypesScanned     int
	MethodsScanned   int
	ExtendsEdges     int
	ImplementsEdges  int
	ContainsEdges    int
	UnresolvedSupers int
}

// addHierarchyEdges iterates every blob in the shard set, extracts type
// hierarchy from its content, and emits extends/implements/contains_method
// edges into the in-progress graph build. It processes each blob SHA once,
// deduplicating across shards.
func addHierarchyEdges(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
) (HierarchyReport, error) {
	var report HierarchyReport
	processed := make(map[string]bool, len(sweep.sites))

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
		syms := sweep.symbols[site.shard].Symbols(site.blob)
		if len(syms) == 0 {
			continue
		}

		ext := blobPrimaryExtension(blob)

		if err := emitContainmentEdges(builder, seen, blob.SHA, syms, &report); err != nil {
			return report, err
		}

		supers := extractInheritance(blob.Content, syms, ext)
		for _, sup := range supers {
			if err := resolveAndEmitSuper(builder, sweep, seen, blob.SHA, sup, &report); err != nil {
				return report, err
			}
		}
	}
	return report, nil
}

// emitContainmentEdges links each Method symbol to its innermost enclosing
// Type symbol within the same blob.
func emitContainmentEdges(
	builder *diskgraph.Builder,
	seen map[persistedGraphEdge]struct{},
	blobSHA string,
	syms []symbol.Symbol,
	report *HierarchyReport,
) error {
	types := make([]symbol.Symbol, 0, len(syms)/2)
	for _, s := range syms {
		if s.Kind == symbol.Type {
			types = append(types, s)
		}
	}
	if len(types) == 0 {
		return nil
	}

	for _, s := range syms {
		if s.Kind != symbol.Method {
			continue
		}
		report.MethodsScanned++
		enclosing := findEnclosingType(types, s.NameStart)
		if enclosing == nil {
			continue
		}
		record := persistedGraphEdge{
			sourceBlob:     blobSHA,
			sourceOffset:   uint64(enclosing.NameStart),
			typeID:         diskgraph.EdgeContainsMethod,
			targetBlob:     blobSHA,
			targetOffset:   uint64(s.NameStart),
			confidence:     uint64(graph.Proven),
			evidenceBlob:   blobSHA,
			evidence:       uint64(s.NameStart),
			evidenceLength: uint64(s.NameEnd - s.NameStart),
		}
		if _, dup := seen[record]; dup {
			continue
		}
		seen[record] = struct{}{}
		if err := builder.Add(blobSHA, uint64(enclosing.NameStart), diskgraph.Edge{
			Type:         diskgraph.EdgeContainsMethod,
			TargetBlob:   blobSHA,
			TargetOffset: uint64(s.NameStart),
			Confidence:   graph.Proven,
			Evidence: graph.Evidence{
				BlobSHA:    blobSHA,
				ByteOffset: uint64(s.NameStart),
				ByteLength: uint64(s.NameEnd - s.NameStart),
			},
		}); err != nil {
			return err
		}
		report.ContainsEdges++
	}
	return nil
}

// findEnclosingType returns the innermost Type symbol whose body range covers
// offset, or nil.
func findEnclosingType(types []symbol.Symbol, offset int) *symbol.Symbol {
	var best *symbol.Symbol
	for i := range types {
		t := &types[i]
		if offset >= t.BodyStart && offset < t.BodyEnd {
			if best == nil || (t.BodyEnd-t.BodyStart) < (best.BodyEnd-best.BodyStart) {
				best = t
			}
		}
	}
	return best
}

// inheritedSuper is one parsed super-type reference from a type declaration.
type inheritedSuper struct {
	typeOffset  int                // NameStart of the declaring type
	superName   string             // identifier of the super type
	superOffset int                // byte offset of the super name in blob content
	edgeType    diskgraph.EdgeType // EdgeExtends or EdgeImplements
	// guessedKind is true when edgeType came from csInferEdgeType's I-prefix
	// name heuristic rather than an explicit extends/implements keyword (TS
	// always has one). resolveAndEmitSuper uses it to know when a resolved
	// local super definition's own declaring keyword should override the
	// guess instead of being taken at face value.
	guessedKind bool
}

// extractInheritance dispatches to language-specific inheritance parsers.
func extractInheritance(content []byte, syms []symbol.Symbol, ext string) []inheritedSuper {
	switch ext {
	case ".cs":
		return csExtractInheritance(content, syms)
	case ".ts", ".tsx":
		return tsExtractInheritance(content, syms)
	default:
		return nil
	}
}

// csExtractInheritance extracts C# inheritance clauses from type declarations.
// For each Type symbol, it scans forward from NameEnd past optional generics
// to find `:`, then collects comma-separated identifiers until `{`, `where`,
// or `;`. Uses the I-prefix convention to distinguish interfaces as an
// initial guess (guessedKind); resolveAndEmitSuper overrides that guess when
// the super resolves to a local definition whose own declaring keyword is
// known.
func csExtractInheritance(content []byte, syms []symbol.Symbol) []inheritedSuper {
	var out []inheritedSuper
	for _, s := range syms {
		if s.Kind != symbol.Type {
			continue
		}
		supers := csParseSupers(content, s.NameEnd)
		for _, sup := range supers {
			out = append(out, inheritedSuper{
				typeOffset:  s.NameStart,
				superName:   sup.name,
				superOffset: sup.offset,
				edgeType:    csInferEdgeType(sup.name),
				guessedKind: true,
			})
		}
	}
	return out
}

type parsedSuper struct {
	name   string
	offset int
}

// csParseSupers scans C# content from nameEnd forward to extract super type
// names from an inheritance clause.
func csParseSupers(content []byte, nameEnd int) []parsedSuper {
	n := len(content)
	i := nameEnd

	i = hierSkipWhitespace(content, i)
	if i >= n {
		return nil
	}

	// Skip optional generic parameters <...>
	if content[i] == '<' {
		i = hierSkipAngleBrackets(content, i)
		if i < 0 {
			return nil
		}
		i = hierSkipWhitespace(content, i)
	}

	// Optional record parameter list (...)
	if i < n && content[i] == '(' {
		i = hierSkipParens(content, i)
		if i < 0 {
			return nil
		}
		i = hierSkipWhitespace(content, i)
	}

	// Must find ':'
	if i >= n || content[i] != ':' {
		return nil
	}
	i++

	return hierCollectIdents(content, i, n)
}

// tsExtractInheritance extracts TypeScript extends/implements from class and
// interface declarations.
func tsExtractInheritance(content []byte, syms []symbol.Symbol) []inheritedSuper {
	var out []inheritedSuper
	for _, s := range syms {
		if s.Kind != symbol.Type {
			continue
		}
		supers := tsParseSupers(content, s.NameEnd)
		out = append(out, supers...)
		for idx := range out[len(out)-len(supers):] {
			out[len(out)-len(supers)+idx].typeOffset = s.NameStart
		}
	}
	return out
}

// tsParseSupers scans TS content from nameEnd for extends/implements clauses.
func tsParseSupers(content []byte, nameEnd int) []inheritedSuper {
	n := len(content)
	i := nameEnd
	var out []inheritedSuper

	i = hierSkipWhitespace(content, i)

	// Skip optional generic params
	if i < n && content[i] == '<' {
		i = hierSkipAngleBrackets(content, i)
		if i < 0 {
			return nil
		}
		i = hierSkipWhitespace(content, i)
	}

	for i < n {
		if content[i] == '{' || content[i] == ';' {
			break
		}

		word, wordEnd := hierReadWord(content, i)
		if word == "" {
			i++
			continue
		}
		i = wordEnd

		switch word {
		case "extends":
			idents := hierCollectIdentsUntilKeyword(content, i, n)
			for _, id := range idents {
				out = append(out, inheritedSuper{
					superName:   id.name,
					superOffset: id.offset,
					edgeType:    diskgraph.EdgeExtends,
				})
			}
			i = hierAdvancePastIdents(content, i, n)
		case "implements":
			idents := hierCollectIdentsUntilKeyword(content, i, n)
			for _, id := range idents {
				out = append(out, inheritedSuper{
					superName:   id.name,
					superOffset: id.offset,
					edgeType:    diskgraph.EdgeImplements,
				})
			}
			i = hierAdvancePastIdents(content, i, n)
		default:
			// Skip unknown tokens
		}
	}
	return out
}

// resolveAndEmitSuper resolves a super type name via the merged symbol corpus
// and emits the appropriate hierarchy edge.
func resolveAndEmitSuper(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
	sourceBlobSHA string,
	sup inheritedSuper,
	report *HierarchyReport,
) error {
	report.TypesScanned++

	defs := sweep.merged.Definitions(sup.superName)
	if len(defs) == 0 {
		report.UnresolvedSupers++
		return nil
	}

	// Emit edge to each definition — content-addressed dedup handles identical
	// content across shards.
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

		// sup.edgeType is a name-based guess for C# (IFoo -> implements, Foo ->
		// extends); it has no positional or resolved-kind cross-check. When the
		// super resolves to a local definition, prefer that definition's own
		// declaring keyword (class/struct/enum/record vs interface) over the
		// guess. Unresolved supers (external/BCL) fall back to the guess, since
		// there is no local declaration to inspect.
		edgeType := sup.edgeType
		if sup.guessedKind {
			if kw, ok := csResolvedDeclKeyword(sweep, def, targetBlob); ok {
				if kw == "interface" {
					edgeType = diskgraph.EdgeImplements
				} else {
					edgeType = diskgraph.EdgeExtends
				}
			}
		}

		evidenceLen := uint64(len(sup.superName))
		if evidenceLen == 0 {
			evidenceLen = 1
		}

		record := persistedGraphEdge{
			sourceBlob:     sourceBlobSHA,
			sourceOffset:   uint64(sup.typeOffset),
			typeID:         edgeType,
			targetBlob:     targetBlob.SHA,
			targetOffset:   uint64(def.Start),
			confidence:     uint64(graph.Proven),
			evidenceBlob:   sourceBlobSHA,
			evidence:       uint64(sup.superOffset),
			evidenceLength: evidenceLen,
		}
		if _, dup := seen[record]; dup {
			continue
		}
		seen[record] = struct{}{}

		if err := builder.Add(sourceBlobSHA, uint64(sup.typeOffset), diskgraph.Edge{
			Type:         edgeType,
			TargetBlob:   targetBlob.SHA,
			TargetOffset: uint64(def.Start),
			Confidence:   graph.Proven,
			Evidence: graph.Evidence{
				BlobSHA:    sourceBlobSHA,
				ByteOffset: uint64(sup.superOffset),
				ByteLength: evidenceLen,
			},
		}); err != nil {
			return err
		}
		switch edgeType {
		case diskgraph.EdgeExtends:
			report.ExtendsEdges++
		case diskgraph.EdgeImplements:
			report.ImplementsEdges++
		}
	}
	return nil
}

// csInferEdgeType uses the C# I-prefix convention: IFoo → implements, Foo → extends.
// It is a fallback guess only — resolveAndEmitSuper overrides it with
// csResolvedDeclKeyword whenever the super resolves to a local definition.
func csInferEdgeType(name string) diskgraph.EdgeType {
	if len(name) >= 2 && name[0] == 'I' && name[1] >= 'A' && name[1] <= 'Z' {
		return diskgraph.EdgeImplements
	}
	return diskgraph.EdgeExtends
}

// csResolvedDeclKeyword reports the C# keyword a resolved local super
// definition was actually declared with ("class", "interface", "struct",
// "enum", or "record"), so csInferEdgeType's name-based guess can be
// overridden with ground truth instead of a naming convention. It returns
// ok=false — meaning the caller should keep the guess — when def isn't a Type
// symbol in a .cs blob, or when no declaring keyword sits immediately before
// its name.
func csResolvedDeclKeyword(sweep *graphSweep, def symbol.ShardRef, targetBlob *index.Blob) (kw string, ok bool) {
	if blobPrimaryExtension(targetBlob) != ".cs" {
		return "", false
	}
	if def.Shard < 0 || def.Shard >= len(sweep.symbols) || sweep.symbols[def.Shard] == nil {
		return "", false
	}
	for _, s := range sweep.symbols[def.Shard].Symbols(def.Blob) {
		if s.NameStart != def.Start {
			continue
		}
		if s.Kind != symbol.Type {
			return "", false
		}
		kw = csKeywordBeforeOffset(targetBlob.Content, s.NameStart)
		return kw, kw != ""
	}
	return "", false
}

// csKeywordBeforeOffset scans backward from off over whitespace to the
// preceding identifier token and returns it when it is one of the
// type-declaring keywords csTypeRe (internal/symbol/extract_cs.go) matches:
// class, interface, struct, enum, or record. This mirrors csTypeRe's own
// shape — keyword, whitespace, name — without a second regex pass over the
// blob. Returns "" when the preceding token isn't one of those keywords.
func csKeywordBeforeOffset(content []byte, off int) string {
	i := off
	for i > 0 {
		switch content[i-1] {
		case ' ', '\t', '\r', '\n':
			i--
			continue
		}
		break
	}
	end := i
	for i > 0 && hierIsIdentByte(content[i-1]) {
		i--
	}
	if i == end {
		return ""
	}
	switch word := string(content[i:end]); word {
	case "class", "interface", "struct", "enum", "record":
		return word
	default:
		return ""
	}
}

// blobPrimaryExtension returns the lower-cased file extension of the blob's
// first file reference.
func blobPrimaryExtension(blob *index.Blob) string {
	if blob == nil || len(blob.Files) == 0 {
		return ""
	}
	return strings.ToLower(filepath.Ext(blob.Files[0].RelPath))
}

// ---------------------------------------------------------------------------
// Scanning helpers — shared by the C# and TS parsers
// ---------------------------------------------------------------------------

func hierSkipWhitespace(content []byte, i int) int {
	for i < len(content) && (content[i] == ' ' || content[i] == '\t' || content[i] == '\r' || content[i] == '\n') {
		i++
	}
	return i
}

func hierSkipAngleBrackets(content []byte, i int) int {
	if i >= len(content) || content[i] != '<' {
		return i
	}
	depth := 1
	i++
	for i < len(content) && depth > 0 {
		switch content[i] {
		case '<':
			depth++
		case '>':
			depth--
		}
		i++
	}
	if depth != 0 {
		return -1
	}
	return i
}

func hierSkipParens(content []byte, i int) int {
	if i >= len(content) || content[i] != '(' {
		return i
	}
	depth := 1
	i++
	for i < len(content) && depth > 0 {
		switch content[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		i++
	}
	if depth != 0 {
		return -1
	}
	return i
}

func hierIsIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// hierReadWord reads a contiguous identifier token starting at i.
func hierReadWord(content []byte, i int) (string, int) {
	i = hierSkipWhitespace(content, i)
	if i >= len(content) || !hierIsIdentByte(content[i]) {
		return "", i
	}
	start := i
	for i < len(content) && hierIsIdentByte(content[i]) {
		i++
	}
	return string(content[start:i]), i
}

// hierCollectIdents collects comma-separated identifiers, skipping generic
// args, until '{', ';', 'where', or end of content. Used by the C# parser.
func hierCollectIdents(content []byte, i, n int) []parsedSuper {
	var out []parsedSuper
	for i < n {
		i = hierSkipWhitespace(content, i)
		if i >= n {
			break
		}
		if content[i] == '{' || content[i] == ';' {
			break
		}
		if i+5 <= n && string(content[i:i+5]) == "where" && (i+5 >= n || !hierIsIdentByte(content[i+5])) {
			break
		}

		start := i
		for i < n && hierIsIdentByte(content[i]) {
			i++
		}
		if i == start {
			i++
			continue
		}
		name := string(content[start:i])

		i = hierSkipWhitespace(content, i)
		if i < n && content[i] == '<' {
			i = hierSkipAngleBrackets(content, i)
			if i < 0 {
				return out
			}
		}

		// Skip dotted names (e.g. Namespace.Type) — take the last segment
		for i < n {
			j := hierSkipWhitespace(content, i)
			if j < n && content[j] == '.' {
				j++
				j = hierSkipWhitespace(content, j)
				segStart := j
				for j < n && hierIsIdentByte(content[j]) {
					j++
				}
				if j > segStart {
					name = string(content[segStart:j])
					start = segStart
					i = j
					j = hierSkipWhitespace(content, j)
					if j < n && content[j] == '<' {
						j = hierSkipAngleBrackets(content, j)
						if j < 0 {
							return out
						}
						i = j
					}
					continue
				}
			}
			break
		}

		out = append(out, parsedSuper{name: name, offset: start})

		i = hierSkipWhitespace(content, i)
		if i < n && content[i] == ',' {
			i++
		}
	}
	return out
}

// hierCollectIdentsUntilKeyword collects comma-separated idents until
// 'extends', 'implements', '{', or ';'. Used by the TS parser.
func hierCollectIdentsUntilKeyword(content []byte, i, n int) []parsedSuper {
	var out []parsedSuper
	for i < n {
		i = hierSkipWhitespace(content, i)
		if i >= n {
			break
		}
		if content[i] == '{' || content[i] == ';' {
			break
		}

		start := i
		for i < n && hierIsIdentByte(content[i]) {
			i++
		}
		if i == start {
			i++
			continue
		}
		name := string(content[start:i])
		if name == "extends" || name == "implements" {
			break
		}

		i = hierSkipWhitespace(content, i)
		if i < n && content[i] == '<' {
			i = hierSkipAngleBrackets(content, i)
			if i < 0 {
				return out
			}
		}

		// Skip dotted names
		for i < n {
			j := hierSkipWhitespace(content, i)
			if j < n && content[j] == '.' {
				j++
				j = hierSkipWhitespace(content, j)
				segStart := j
				for j < n && hierIsIdentByte(content[j]) {
					j++
				}
				if j > segStart {
					name = string(content[segStart:j])
					start = segStart
					i = j
					j = hierSkipWhitespace(content, j)
					if j < n && content[j] == '<' {
						j = hierSkipAngleBrackets(content, j)
						if j < 0 {
							return out
						}
						i = j
					}
					continue
				}
			}
			break
		}

		out = append(out, parsedSuper{name: name, offset: start})

		i = hierSkipWhitespace(content, i)
		if i < n && content[i] == ',' {
			i++
		}
	}
	return out
}

// hierAdvancePastIdents advances i past the identifier list so the TS parser
// can find the next keyword. It mirrors hierCollectIdentsUntilKeyword but
// discards the results.
func hierAdvancePastIdents(content []byte, i, n int) int {
	for i < n {
		i = hierSkipWhitespace(content, i)
		if i >= n {
			break
		}
		if content[i] == '{' || content[i] == ';' {
			break
		}
		start := i
		for i < n && hierIsIdentByte(content[i]) {
			i++
		}
		if i == start {
			break
		}
		word := string(content[start:i])
		if word == "extends" || word == "implements" {
			return start // back up — the caller's loop will read this keyword
		}
		i = hierSkipWhitespace(content, i)
		if i < n && content[i] == '<' {
			i = hierSkipAngleBrackets(content, i)
			if i < 0 {
				return n
			}
		}
		// Skip dots
		for {
			j := hierSkipWhitespace(content, i)
			if j < n && content[j] == '.' {
				j++
				j = hierSkipWhitespace(content, j)
				for j < n && hierIsIdentByte(content[j]) {
					j++
				}
				i = j
				j = hierSkipWhitespace(content, j)
				if j < n && content[j] == '<' {
					j = hierSkipAngleBrackets(content, j)
					if j < 0 {
						return n
					}
					i = j
				}
				continue
			}
			break
		}
		i = hierSkipWhitespace(content, i)
		if i < n && content[i] == ',' {
			i++
		} else {
			break
		}
	}
	return i
}
