package graphbuild

// graphinjection.go extracts DI container wiring edges from C# service
// registrations. It runs as a supplemental pass in BuildGraphWithOptions,
// scanning each blob for AddScoped/AddTransient/AddSingleton<IFoo, Foo>()
// patterns and emitting EdgeInjects edges from the registration site to
// possible implementation types. Two-argument registrations also emit
// EdgeImplements candidates. Syntax-only bindings are at most Pattern, never
// Verified: observing a registration does not prove its target identity.

import (
	"regexp"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/symbol"
)

// InjectionReport accounts for one DI injection extraction pass.
type InjectionReport struct {
	BlobsScanned       int
	RegistrationsFound int
	InjectsEdges       int
	ImplEdges          int
	UnresolvedTypes    int
}

var diRegistrationRE = regexp.MustCompile(`\bAdd(?:Scoped|Transient|Singleton)\s*<`)

// addInjectionEdges iterates every .cs blob in the shard set, extracts DI
// service registrations, and emits EdgeInjects edges from the enclosing
// method/type at the registration site to possible implementation types.
// Two-argument registrations also emit EdgeImplements from the
// implementation to the interface when the hierarchy pass didn't capture it.
func addInjectionEdges(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
) (InjectionReport, error) {
	var report InjectionReport
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

		ext := blobPrimaryExtension(blob)
		if ext != ".cs" {
			continue
		}
		report.BlobsScanned++

		content := blob.Content
		mask := diLiteralMask(content)
		syms := sweep.symbols[site.shard].Symbols(site.blob)

		matches := diRegistrationRE.FindAllIndex(content, -1)
		if len(matches) == 0 {
			continue
		}

		for _, m := range matches {
			if diMasked(mask, m[0]) {
				continue
			}
			report.RegistrationsFound++

			openAngle := m[1] - 1
			args := diTypeArguments(content, openAngle, mask)
			if len(args) == 0 {
				continue
			}

			sourceOffset := findEnclosingOffset(syms, m[0])
			evidenceEnd := diMatchEnd(content, openAngle, mask)
			evidenceLen := uint64(evidenceEnd - m[0])
			if evidenceLen == 0 {
				evidenceLen = uint64(m[1] - m[0])
			}

			implName := args[len(args)-1].name
			if err := emitInjectionEdge(
				builder, sweep, seen, sha, sourceOffset,
				implName, m[0], evidenceLen,
				diskgraph.EdgeInjects, &report,
			); err != nil {
				return report, err
			}

			if len(args) == 2 {
				ifaceName := args[0].name
				if err := emitImplementsFromDI(
					builder, sweep, seen, sha,
					implName, ifaceName, m[0], evidenceLen,
					&report,
				); err != nil {
					return report, err
				}
			}
		}
	}
	return report, nil
}

type diArg struct {
	name string
}

// diTypeArguments extracts type arguments from angle-bracketed content at
// openAngle, respecting nesting. Returns the last identifier of each
// top-level comma-separated segment (handles Namespace.Type and generics).
func diTypeArguments(content []byte, openAngle int, mask []bool) []diArg {
	if openAngle < 0 || openAngle >= len(content) || content[openAngle] != '<' {
		return nil
	}
	closeAngle := diMatchingAngle(content, openAngle, mask)
	if closeAngle < 0 {
		return nil
	}
	inner := content[openAngle+1 : closeAngle]

	var args []diArg
	depth := 0
	segStart := 0
	for i := 0; i < len(inner); i++ {
		if diMasked(mask, openAngle+1+i) {
			continue
		}
		switch inner[i] {
		case '<', '(':
			depth++
		case '>', ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if name := extractLastIdent(inner[segStart:i]); name != "" {
					args = append(args, diArg{name: name})
				}
				segStart = i + 1
			}
		}
	}
	if name := extractLastIdent(inner[segStart:]); name != "" {
		args = append(args, diArg{name: name})
	}
	return args
}

// extractLastIdent returns the last dotted segment's identifier from a type
// argument like "Namespace.Type<T>" → "Type", or "Foo" → "Foo".
func extractLastIdent(segment []byte) string {
	depth := 0
	lastStart := -1
	lastEnd := -1
	for i := 0; i < len(segment); i++ {
		switch segment[i] {
		case '<', '(':
			depth++
		case '>', ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && hierIsIdentByte(segment[i]) {
				if lastStart < 0 || (i > 0 && segment[i-1] == '.') {
					lastStart = i
				}
				lastEnd = i + 1
			} else if depth == 0 && segment[i] == '.' {
				// dot separates segments; the next ident is the new "last"
			} else if depth == 0 {
				// whitespace or other — ident run ended
				if lastStart >= 0 && lastEnd > lastStart {
					// keep it, a later dot may override
				}
			}
		}
	}
	if lastStart < 0 || lastEnd <= lastStart {
		return ""
	}
	// Walk back from lastEnd to capture the contiguous ident run
	start := lastEnd - 1
	for start > 0 && hierIsIdentByte(segment[start-1]) {
		start--
	}
	// But also make sure we pick up from lastStart if it's earlier
	if start > lastStart {
		start = lastStart
	}
	return string(segment[start:lastEnd])
}

// diMatchEnd finds the end of the DI registration expression for evidence
// (up to and including the closing '>' of the generic arguments).
func diMatchEnd(content []byte, openAngle int, mask []bool) int {
	closeAngle := diMatchingAngle(content, openAngle, mask)
	if closeAngle < 0 {
		return openAngle + 1
	}
	return closeAngle + 1
}

// findEnclosingOffset returns the NameStart of the innermost symbol whose
// body range covers off. Falls back to 0 if no enclosing symbol is found.
func findEnclosingOffset(syms []symbol.Symbol, off int) uint64 {
	var best *symbol.Symbol
	for i := range syms {
		s := &syms[i]
		if off >= s.BodyStart && off < s.BodyEnd {
			if best == nil || (s.BodyEnd-s.BodyStart) < (best.BodyEnd-best.BodyStart) {
				best = s
			}
		}
	}
	if best != nil {
		return uint64(best.NameStart)
	}
	return 0
}

// emitInjectionEdge classifies targetName matches in the source context and
// emits edges at their binding confidence, separately from the observed syntax.
func emitInjectionEdge(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
	sourceBlobSHA string,
	sourceOffset uint64,
	targetName string,
	evidenceStart int,
	evidenceLen uint64,
	edgeType diskgraph.EdgeType,
	report *InjectionReport,
) error {
	defs := scopedTypeTargets(sweep, sourceBlobSHA, targetName)
	if len(defs) == 0 {
		report.UnresolvedTypes++
		return nil
	}

	emitted := false
	for _, target := range defs {
		def := target.ref
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
			sourceOffset:   sourceOffset,
			typeID:         edgeType,
			targetBlob:     targetBlob.SHA,
			targetOffset:   uint64(def.Start),
			confidence:     uint64(target.confidence),
			evidenceBlob:   sourceBlobSHA,
			evidence:       uint64(evidenceStart),
			evidenceLength: evidenceLen,
		}
		if _, dup := seen[record]; dup {
			continue
		}
		seen[record] = struct{}{}

		if err := builder.Add(sourceBlobSHA, sourceOffset, diskgraph.Edge{
			Type:         edgeType,
			TargetBlob:   targetBlob.SHA,
			TargetOffset: uint64(def.Start),
			Confidence:   target.confidence,
			Evidence: graph.Evidence{
				BlobSHA:    sourceBlobSHA,
				ByteOffset: uint64(evidenceStart),
				ByteLength: evidenceLen,
			},
		}); err != nil {
			return err
		}
		emitted = true
	}
	if emitted {
		switch edgeType {
		case diskgraph.EdgeInjects:
			report.InjectsEdges++
		case diskgraph.EdgeImplements:
			report.ImplEdges++
		}
	}
	return nil
}

// emitImplementsFromDI retains possible implementation/interface pairs from DI
// syntax. A pair is no stronger than its weakest target binding; registration
// syntax alone does not establish that either type actually resolves.
func emitImplementsFromDI(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
	sourceBlobSHA string,
	implName, ifaceName string,
	evidenceStart int,
	evidenceLen uint64,
	report *InjectionReport,
) error {
	implDefs := scopedTypeTargets(sweep, sourceBlobSHA, implName)
	ifaceDefs := scopedTypeTargets(sweep, sourceBlobSHA, ifaceName)
	if len(implDefs) == 0 || len(ifaceDefs) == 0 {
		return nil
	}

	for _, implTarget := range implDefs {
		implDef := implTarget.ref
		if implDef.Shard < 0 || implDef.Shard >= len(sweep.idxs) {
			continue
		}
		implBlob := sweep.idxs[implDef.Shard].Blob(implDef.Blob)
		if implBlob == nil || implBlob.SHA == "" || implDef.Start < 0 {
			continue
		}

		for _, ifaceTarget := range ifaceDefs {
			ifaceDef := ifaceTarget.ref
			confidence := min(implTarget.confidence, ifaceTarget.confidence)
			if ifaceDef.Shard < 0 || ifaceDef.Shard >= len(sweep.idxs) {
				continue
			}
			ifaceBlob := sweep.idxs[ifaceDef.Shard].Blob(ifaceDef.Blob)
			if ifaceBlob == nil || ifaceBlob.SHA == "" || ifaceDef.Start < 0 {
				continue
			}

			record := persistedGraphEdge{
				sourceBlob:     implBlob.SHA,
				sourceOffset:   uint64(implDef.Start),
				typeID:         diskgraph.EdgeImplements,
				targetBlob:     ifaceBlob.SHA,
				targetOffset:   uint64(ifaceDef.Start),
				confidence:     uint64(confidence),
				evidenceBlob:   sourceBlobSHA,
				evidence:       uint64(evidenceStart),
				evidenceLength: evidenceLen,
			}
			if _, dup := seen[record]; dup {
				continue
			}
			seen[record] = struct{}{}

			if err := builder.Add(implBlob.SHA, uint64(implDef.Start), diskgraph.Edge{
				Type:         diskgraph.EdgeImplements,
				TargetBlob:   ifaceBlob.SHA,
				TargetOffset: uint64(ifaceDef.Start),
				Confidence:   confidence,
				Evidence: graph.Evidence{
					BlobSHA:    sourceBlobSHA,
					ByteOffset: uint64(evidenceStart),
					ByteLength: evidenceLen,
				},
			}); err != nil {
				return err
			}
			report.ImplEdges++
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Lexical helpers — mask comments and strings so DI patterns inside them
// don't produce spurious edges.
// ---------------------------------------------------------------------------

func diMasked(mask []bool, off int) bool {
	return off < 0 || off >= len(mask) || mask[off]
}

func diMatchingAngle(content []byte, open int, mask []bool) int {
	if open < 0 || open >= len(content) || content[open] != '<' {
		return -1
	}
	depth := 0
	for i := open; i < len(content); i++ {
		if diMasked(mask, i) {
			continue
		}
		switch content[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i
			}
		case ';', '{', '}':
			return -1
		}
	}
	return -1
}

func diLiteralMask(content []byte) []bool {
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
