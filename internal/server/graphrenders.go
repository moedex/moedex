package server

// graphrenders.go extracts component-rendering edges from Angular templates.
// Pass 1 builds a selector→component registry from @Component({selector: '...'})
// in .component.ts blobs. Pass 2 scans .component.html blobs for custom element
// tags, resolves them via the registry, and emits EdgeRenders from the owning
// component to the rendered child component.

import (
	"bytes"
	"regexp"
	"strings"

	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/symbol"
)

// RenderReport accounts for one component-rendering extraction pass.
type RenderReport struct {
	ComponentsScanned  int
	SelectorsExtracted int
	TemplatesScanned   int
	RendersEdges       int
	UnresolvedTags     int
}

// componentEntry maps an Angular selector to the component that declares it.
type componentEntry struct {
	blobSHA      string
	symbolOffset uint64
	className    string
}

// addRenderEdges iterates every blob in the shard set, extracts Angular
// component selectors and template tag usage, and emits EdgeRenders edges
// into the in-progress graph build.
func addRenderEdges(
	builder *diskgraph.Builder,
	sweep *graphSweep,
	seen map[persistedGraphEdge]struct{},
) (RenderReport, error) {
	var report RenderReport
	processed := make(map[string]bool, len(sweep.sites))

	// Pass 1: build selector → component registry from .component.ts blobs.
	registry := make(map[string]componentEntry)
	// Also build a path→(blobSHA, symbolOffset) map for resolving template owners.
	type ownerKey struct {
		blobSHA      string
		symbolOffset uint64
	}
	ownerByPath := make(map[string]ownerKey)

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
		if ext != ".ts" {
			continue
		}
		relPath := ""
		if len(blob.Files) > 0 {
			relPath = blob.Files[0].RelPath
		}
		if relPath == "" || !strings.HasSuffix(relPath, ".component.ts") {
			continue
		}

		syms := sweep.symbols[site.shard].Symbols(site.blob)
		if len(syms) == 0 {
			continue
		}

		report.ComponentsScanned++

		selectors := extractAngularSelectors(blob.Content)
		if len(selectors) == 0 {
			continue
		}

		typeSym := findFirstTypeSymbol(syms)
		if typeSym == nil {
			continue
		}

		className := string(blob.Content[typeSym.NameStart:typeSym.NameEnd])

		for _, sel := range selectors {
			registry[sel] = componentEntry{
				blobSHA:      blob.SHA,
				symbolOffset: uint64(typeSym.NameStart),
				className:    className,
			}
			report.SelectorsExtracted++
		}

		ownerByPath[relPath] = ownerKey{
			blobSHA:      blob.SHA,
			symbolOffset: uint64(typeSym.NameStart),
		}
	}

	if len(registry) == 0 {
		return report, nil
	}

	// Pass 2: scan .component.html blobs for custom element tags.
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
		if ext != ".html" {
			continue
		}
		relPath := ""
		if len(blob.Files) > 0 {
			relPath = blob.Files[0].RelPath
		}
		if relPath == "" || !strings.HasSuffix(relPath, ".component.html") {
			continue
		}

		report.TemplatesScanned++

		// Resolve owning component via path convention.
		ownerPath := strings.TrimSuffix(relPath, ".component.html") + ".component.ts"
		owner, ok := ownerByPath[ownerPath]
		if !ok {
			continue
		}

		tags := extractTemplateTags(blob.Content)
		for _, tag := range tags {
			target, found := registry[tag.name]
			if !found {
				// Try cross-shard fallback: convert tag name to PascalCase
				// class name and look up in merged symbol corpus.
				className := selectorToClassName(tag.name)
				if className == "" {
					report.UnresolvedTags++
					continue
				}
				defs := sweep.merged.Definitions(className)
				if len(defs) == 0 {
					report.UnresolvedTags++
					continue
				}
				// Use first definition as target.
				def := defs[0]
				if def.Shard < 0 || def.Shard >= len(sweep.idxs) {
					report.UnresolvedTags++
					continue
				}
				targetBlob := sweep.idxs[def.Shard].Blob(def.Blob)
				if targetBlob == nil || targetBlob.SHA == "" || def.Start < 0 {
					report.UnresolvedTags++
					continue
				}
				target = componentEntry{
					blobSHA:      targetBlob.SHA,
					symbolOffset: uint64(def.Start),
					className:    className,
				}
			}

			// Don't self-reference.
			if owner.blobSHA == target.blobSHA && owner.symbolOffset == target.symbolOffset {
				continue
			}

			evidenceLen := uint64(len(tag.name))
			if evidenceLen == 0 {
				evidenceLen = 1
			}

			record := persistedGraphEdge{
				sourceBlob:     owner.blobSHA,
				sourceOffset:   owner.symbolOffset,
				typeID:         diskgraph.EdgeRenders,
				targetBlob:     target.blobSHA,
				targetOffset:   target.symbolOffset,
				confidence:     uint64(graph.Verified),
				evidenceBlob:   blob.SHA,
				evidence:       uint64(tag.offset),
				evidenceLength: evidenceLen,
			}
			if _, dup := seen[record]; dup {
				continue
			}
			seen[record] = struct{}{}

			if err := builder.Add(owner.blobSHA, owner.symbolOffset, diskgraph.Edge{
				Type:         diskgraph.EdgeRenders,
				TargetBlob:   target.blobSHA,
				TargetOffset: target.symbolOffset,
				Confidence:   graph.Verified,
				Evidence: graph.Evidence{
					BlobSHA:    blob.SHA,
					ByteOffset: uint64(tag.offset),
					ByteLength: evidenceLen,
				},
			}); err != nil {
				return report, err
			}
			report.RendersEdges++
		}
	}

	return report, nil
}

// ---------------------------------------------------------------------------
// Angular selector extraction
// ---------------------------------------------------------------------------

var componentDecoratorRE = regexp.MustCompile(`@Component\s*\(\s*\{`)

// selectorRE matches selector: 'value' or selector: "value" inside a
// @Component decorator object literal.
var selectorRE = regexp.MustCompile(`selector\s*:\s*['"]([^'"]+)['"]`)

// extractAngularSelectors parses @Component({selector: '...'}) from a
// .component.ts file and returns the element selectors. Attribute selectors
// like [stlrButton] and compound selectors are split into individual entries.
func extractAngularSelectors(content []byte) []string {
	loc := componentDecoratorRE.FindIndex(content)
	if loc == nil {
		return nil
	}

	// Find the closing } of the decorator object — scan from the opening {.
	braceStart := loc[1] - 1
	braceEnd := findMatchingBrace(content, braceStart)
	if braceEnd < 0 {
		return nil
	}

	decoratorBody := content[loc[0]:braceEnd]
	match := selectorRE.FindSubmatch(decoratorBody)
	if match == nil {
		return nil
	}

	raw := string(match[1])
	return parseAngularSelector(raw)
}

// parseAngularSelector splits a compound Angular selector string into
// individual element/attribute selector names.
func parseAngularSelector(raw string) []string {
	var out []string
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Attribute selector: "button[stlrButton]" or "[stlrButton]"
		if idx := strings.Index(part, "["); idx >= 0 {
			end := strings.Index(part[idx:], "]")
			if end > 1 {
				attrName := part[idx+1 : idx+end]
				if attrName != "" && !isAngularBuiltinAttr(attrName) {
					out = append(out, attrName)
				}
			}
			// Also keep the element selector if present.
			if idx > 0 {
				elem := part[:idx]
				if isCustomElement(elem) {
					out = append(out, elem)
				}
			}
			continue
		}

		// Element selector: "app-header", "stlr-dropdown"
		if isCustomElement(part) {
			out = append(out, part)
		}
	}
	return out
}

// findMatchingBrace finds the closing } that matches the { at content[start].
func findMatchingBrace(content []byte, start int) int {
	if start >= len(content) || content[start] != '{' {
		return -1
	}
	depth := 1
	i := start + 1
	for i < len(content) && depth > 0 {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
		case '\'', '"', '`':
			q := content[i]
			i++
			for i < len(content) && content[i] != q {
				if content[i] == '\\' {
					i++
				}
				i++
			}
		}
		i++
	}
	if depth != 0 {
		return -1
	}
	return i
}

// ---------------------------------------------------------------------------
// Template tag extraction
// ---------------------------------------------------------------------------

type templateTag struct {
	name   string
	offset int // byte offset of the tag name in the template content
}

// Angular built-in elements to skip.
var angularBuiltins = map[string]bool{
	"ng-content":   true,
	"ng-container": true,
	"ng-template":  true,
	"router-outlet": true,
	"ng-component": true,
}

// extractTemplateTags scans HTML template content for custom element tags
// (elements containing a hyphen, per the web component spec) that are not
// Angular built-ins.
func extractTemplateTags(content []byte) []templateTag {
	var tags []templateTag
	n := len(content)

	for i := 0; i < n; i++ {
		if content[i] != '<' {
			continue
		}
		i++
		// Skip closing tags </foo>
		if i < n && content[i] == '/' {
			continue
		}
		// Skip comments <!-- ... -->
		if i+2 < n && content[i] == '!' && content[i+1] == '-' && content[i+2] == '-' {
			i += 3
			for i+2 < n {
				if content[i] == '-' && content[i+1] == '-' && content[i+2] == '>' {
					i += 2 // land on '>', for-loop i++ advances past it
					break
				}
				i++
			}
			continue
		}

		// Read the tag name.
		start := i
		for i < n && isTagNameByte(content[i]) {
			i++
		}
		if i == start {
			continue
		}
		name := string(content[start:i])

		// Only custom elements (containing a hyphen).
		if !strings.Contains(name, "-") {
			continue
		}
		if angularBuiltins[name] {
			continue
		}

		tags = append(tags, templateTag{name: name, offset: start})
	}
	return tags
}

func isTagNameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_'
}

// isCustomElement returns true if the name looks like a custom element
// (contains a hyphen, per the web component naming convention).
func isCustomElement(name string) bool {
	return strings.Contains(name, "-") && !angularBuiltins[name]
}

func isAngularBuiltinAttr(name string) bool {
	return strings.HasPrefix(name, "ng") || strings.HasPrefix(name, "router")
}

// ---------------------------------------------------------------------------
// Selector-to-class-name conversion
// ---------------------------------------------------------------------------

// selectorToClassName converts a kebab-case Angular selector to a PascalCase
// class name for cross-shard lookup. E.g. "app-header" → "AppHeaderComponent",
// "stlr-dropdown" → "StlrDropdownComponent".
func selectorToClassName(selector string) string {
	if selector == "" {
		return ""
	}
	parts := strings.Split(selector, "-")
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		b.WriteByte(part[0] &^ 0x20) // uppercase first byte
		b.WriteString(part[1:])
	}
	b.WriteString("Component")
	return b.String()
}

// findFirstTypeSymbol returns the first Type symbol in the list, or nil.
func findFirstTypeSymbol(syms []symbol.Symbol) *symbol.Symbol {
	for i := range syms {
		if syms[i].Kind == symbol.Type {
			return &syms[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Inline template support
// ---------------------------------------------------------------------------

// inlineTemplateRE matches template: `...` or template: '...' inside a
// @Component decorator. Used to detect components with inline templates
// rather than templateUrl.
var inlineTemplateRE = regexp.MustCompile("template\\s*:\\s*`([^`]*)`")

// extractInlineTemplateTags handles components that use inline templates
// (template: `<child-comp></child-comp>`) instead of templateUrl. Returns
// tags found in the inline template content.
func extractInlineTemplateTags(content []byte) []templateTag {
	loc := componentDecoratorRE.FindIndex(content)
	if loc == nil {
		return nil
	}

	braceStart := loc[1] - 1
	braceEnd := findMatchingBrace(content, braceStart)
	if braceEnd < 0 {
		return nil
	}

	decoratorBody := content[loc[0]:braceEnd]

	// Check for templateUrl — if present, the template is in a separate file.
	if bytes.Contains(decoratorBody, []byte("templateUrl")) {
		return nil
	}

	match := inlineTemplateRE.FindSubmatchIndex(decoratorBody)
	if match == nil {
		return nil
	}

	// match[2]:match[3] is the capture group (inline template content).
	templateContent := decoratorBody[match[2]:match[3]]
	tags := extractTemplateTags(templateContent)

	// Adjust offsets to be relative to the original content.
	baseOffset := loc[0] + match[2]
	for i := range tags {
		tags[i].offset += baseOffset
	}
	return tags
}
