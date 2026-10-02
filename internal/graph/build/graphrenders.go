package graphbuild

// graphrenders.go extracts component-rendering edges from Angular templates.
// Pass 1 builds a selector→component registry from @Component({selector: '...'})
// in .component.ts blobs. Pass 2 scans .component.html blobs for custom element
// tags, resolves them via the registry, and emits EdgeRenders from the owning
// component to the rendered child component.

import (
	"bytes"
	"regexp"
	"sort"
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

// componentEntry preserves both source identity and the repository/path context
// in which a selector was observed. The existing graph format folds contexts by
// content, so shared-content bindings can only be diagnostic Candidates.
type componentEntry struct {
	blobSHA           string
	symbolOffset      uint64
	context           renderContext
	uniqueDeclaration bool
}

type renderContext struct{ repo, path string }

func renderContexts(sweep *graphSweep, sha string) []renderContext {
	seen := make(map[renderContext]bool)
	for _, site := range sweep.sites[sha] {
		blob := sweep.idxs[site.shard].Blob(site.blob)
		if blob == nil {
			continue
		}
		for _, file := range blob.Files {
			if file.Repo != "" && file.RelPath != "" {
				seen[renderContext{file.Repo, file.RelPath}] = true
			}
		}
	}
	out := make([]renderContext, 0, len(seen))
	for context := range seen {
		out = append(out, context)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].repo != out[j].repo {
			return out[i].repo < out[j].repo
		}
		return out[i].path < out[j].path
	})
	return out
}

// addRenderEdges preserves every selector match without treating a filename or
// selector convention as Angular module/import resolution. Exact local owners
// and unique local selectors reach Pattern at most; ambiguity and name-derived
// fallbacks remain Candidate. Iteration order cannot select a winner.
func addRenderEdges(builder *diskgraph.Builder, sweep *graphSweep, seen map[persistedGraphEdge]struct{}) (RenderReport, error) {
	var report RenderReport
	registry := make(map[string][]componentEntry)
	ownerByPath := make(map[renderContext][]componentEntry)
	shas := make([]string, 0, len(sweep.sites))
	for sha := range sweep.sites {
		shas = append(shas, sha)
	}
	sort.Strings(shas)

	for _, sha := range shas {
		site := sweep.sites[sha][0]
		blob := sweep.idxs[site.shard].Blob(site.blob)
		if blob == nil || len(blob.Content) == 0 {
			continue
		}
		contexts := renderContexts(sweep, sha)
		var componentContexts []renderContext
		for _, context := range contexts {
			if strings.HasSuffix(context.path, ".component.ts") {
				componentContexts = append(componentContexts, context)
			}
		}
		if len(componentContexts) == 0 {
			continue
		}
		decorators := componentDecoratorRE.FindAllIndex(blob.Content, -1)
		if len(decorators) == 0 {
			continue
		}
		var types []symbol.Symbol
		for _, sym := range sweep.symbols[site.shard].Symbols(site.blob) {
			if sym.Kind == symbol.Type {
				types = append(types, sym)
			}
		}
		if len(types) == 0 {
			continue
		}
		report.ComponentsScanned++
		selectors := extractAngularSelectors(blob.Content)
		report.SelectorsExtracted += len(selectors)
		for _, context := range componentContexts {
			for _, typ := range types {
				entry := componentEntry{blobSHA: sha, symbolOffset: uint64(typ.NameStart), context: context, uniqueDeclaration: len(types) == 1 && len(decorators) == 1}
				ownerByPath[context] = append(ownerByPath[context], entry)
				for _, selector := range selectors {
					// A comma-list may repeat a selector. Do not turn duplicate
					// spelling of one declaration into artificial ambiguity.
					duplicate := false
					for _, old := range registry[selector] {
						if old == entry {
							duplicate = true
							break
						}
					}
					if !duplicate {
						registry[selector] = append(registry[selector], entry)
					}
				}
			}
		}
	}

	for _, sha := range shas {
		site := sweep.sites[sha][0]
		blob := sweep.idxs[site.shard].Blob(site.blob)
		if blob == nil || len(blob.Content) == 0 {
			continue
		}
		counted := false
		for _, context := range renderContexts(sweep, sha) {
			if !strings.HasSuffix(context.path, ".component.html") {
				continue
			}
			if !counted {
				report.TemplatesScanned++
				counted = true
			}
			ownerPath := renderContext{context.repo, strings.TrimSuffix(context.path, ".component.html") + ".component.ts"}
			owners := ownerByPath[ownerPath]
			if len(owners) == 0 {
				continue
			}
			for _, tag := range extractTemplateTags(blob.Content) {
				targets := registry[tag.name]
				fallback := len(targets) == 0
				if fallback {
					// Class-name resemblance is only a diagnostic hint. Never
					// pick the first definition or promote this to Pattern.
					for _, match := range scopedTypeTargets(sweep, owners[0].blobSHA, selectorToClassName(tag.name)) {
						ref := match.ref
						targetBlob := sweep.idxs[ref.Shard].Blob(ref.Blob)
						if typeContextLanguage(sweep, targetBlob.SHA) != "typescript" {
							continue
						}
						for _, targetContext := range renderContexts(sweep, targetBlob.SHA) {
							targets = append(targets, componentEntry{blobSHA: targetBlob.SHA, symbolOffset: uint64(ref.Start), context: targetContext})
						}
					}
				}
				if len(targets) == 0 {
					report.UnresolvedTags++
					continue
				}
				localTargets := 0
				for _, target := range targets {
					if target.context.repo == context.repo {
						localTargets++
					}
				}
				for _, owner := range owners {
					for _, target := range targets {
						if owner.blobSHA == target.blobSHA && owner.symbolOffset == target.symbolOffset {
							continue
						}
						tier := graph.Candidate
						_, ownerUnique := singleTypeContext(sweep, owner.blobSHA)
						_, targetUnique := singleTypeContext(sweep, target.blobSHA)
						_, templateUnique := singleTypeContext(sweep, sha)
						if !fallback && len(owners) == 1 && localTargets == 1 && target.context.repo == context.repo && owner.uniqueDeclaration && target.uniqueDeclaration && ownerUnique && targetUnique && templateUnique {
							tier = graph.Pattern
						}
						evidence := graph.Evidence{BlobSHA: sha, ByteOffset: uint64(tag.offset), ByteLength: uint64(len(tag.name))}
						record := persistedGraphEdge{sourceBlob: owner.blobSHA, sourceOffset: owner.symbolOffset, typeID: diskgraph.EdgeRenders, targetBlob: target.blobSHA, targetOffset: target.symbolOffset, confidence: uint64(tier), evidenceBlob: sha, evidence: evidence.ByteOffset, evidenceLength: evidence.ByteLength}
						if _, duplicate := seen[record]; duplicate {
							continue
						}
						seen[record] = struct{}{}
						if err := builder.Add(owner.blobSHA, owner.symbolOffset, diskgraph.Edge{Type: diskgraph.EdgeRenders, TargetBlob: target.blobSHA, TargetOffset: target.symbolOffset, Confidence: tier, Evidence: evidence}); err != nil {
							return report, err
						}
						report.RendersEdges++
					}
				}
			}
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
	"ng-content":    true,
	"ng-container":  true,
	"ng-template":   true,
	"router-outlet": true,
	"ng-component":  true,
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
