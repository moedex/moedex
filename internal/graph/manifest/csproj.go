package manifest

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"
)

// captureKind names the simple element whose character data the walk is
// currently collecting. Capture is tracked by kind and index rather than by a
// pointer into m.Requires, which append is free to reallocate.
type captureKind uint8

const (
	captureNone captureKind = iota
	captureVersion
	capturePackageID
	captureAssemblyName
)

// parseCSProj reads an MSBuild project file.
//
// Consumed declarations are <PackageReference Include="…"> (a NuGet package) and
// <ProjectReference Include="…"> (a relative path to another project file). The
// identity the project provides is MSBuild's own resolution order: PackageId if
// declared, else AssemblyName, else the project file's base name — the last being
// a documented default rather than a guess, and recorded as such by
// RuleProjectFileName.
//
// The parse is a single token walk, not a document unmarshal, because each
// declaration needs the byte offset of its own evidence. xml.Decoder in
// non-strict mode is used rather than a regexp so that comments, CDATA,
// attribute order, single-quoted attributes and multi-line elements are handled
// by the same rules the .NET toolchain applies. A syntax error returns the
// declarations recovered up to that point alongside the error.
func parseCSProj(relPath string, content []byte) (Manifest, error) {
	m := Manifest{Kind: CSProj, Path: relPath}
	var (
		dec   = xml.NewDecoder(bytes.NewReader(content))
		stack []string
		prev  int64

		packageID   string
		packageOff  int
		assembly    string
		assemblyOff int

		capturing  captureKind
		captureIdx int
		captureBuf []byte
		captureOff int

		// pending is the index in m.Requires of the reference element currently
		// open, so a <Version> child element can complete it. -1 when none is.
		pending      = -1
		pendingDepth int
	)
	// dec.Strict=false lets a stray unescaped ampersand or unknown entity — both
	// common in hand-edited project files — degrade to literal text instead of
	// truncating the whole manifest at the first offense.
	dec.Strict = false

	for {
		token, err := dec.Token()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return finishCSProj(m, relPath, packageID, packageOff, assembly, assemblyOff), err
			}
			break
		}
		spanStart, spanEnd := int(prev), int(dec.InputOffset())
		prev = dec.InputOffset()
		if spanStart < 0 || spanEnd > len(content) || spanStart > spanEnd {
			continue // defensive: never index outside the content we were handed
		}
		span := content[spanStart:spanEnd]

		switch t := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			// Any nested element abandons an in-progress capture: the elements
			// whose text we read have no children of their own.
			capturing = captureNone
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, name)

			switch {
			case name == "packagereference" || name == "projectreference":
				include := attrValue(t, "include")
				if include == "" {
					// <PackageReference Update="…"> retargets the version of an
					// already-declared package; it declares no new dependency.
					break
				}
				ref := PackageRef
				if name == "projectreference" {
					ref = ProjectRef
				}
				m.Requires = append(m.Requires, Declaration{
					Kind:    CSProj,
					Ref:     ref,
					Name:    strings.TrimSpace(include),
					Version: strings.TrimSpace(attrValue(t, "version")),
					Offset:  attrValueOffset(span, spanStart, "include", include),
				})
				pending, pendingDepth = len(m.Requires)-1, len(stack)
			case name == "version" && pending >= 0 && len(stack) == pendingDepth+1 && m.Requires[pending].Version == "":
				capturing, captureIdx, captureBuf, captureOff = captureVersion, pending, captureBuf[:0], -1
			case name == "packageid" && parent == "propertygroup":
				capturing, captureBuf, captureOff = capturePackageID, captureBuf[:0], -1
			case name == "assemblyname" && parent == "propertygroup":
				capturing, captureBuf, captureOff = captureAssemblyName, captureBuf[:0], -1
			}
		case xml.CharData:
			if capturing == captureNone {
				break
			}
			if captureOff < 0 {
				if trimmed := bytes.TrimLeft(span, " \t\r\n"); len(trimmed) > 0 {
					captureOff = spanEnd - len(trimmed)
				}
			}
			captureBuf = append(captureBuf, t...)
		case xml.EndElement:
			if capturing != captureNone {
				text := strings.TrimSpace(string(captureBuf))
				switch capturing {
				case captureVersion:
					if captureIdx < len(m.Requires) {
						m.Requires[captureIdx].Version = text
					}
				case capturePackageID:
					packageID, packageOff = text, max(captureOff, 0)
				case captureAssemblyName:
					assembly, assemblyOff = text, max(captureOff, 0)
				}
				capturing = captureNone
			}
			if len(stack) > 0 {
				if pending >= 0 && len(stack) == pendingDepth {
					pending = -1
				}
				stack = stack[:len(stack)-1]
			}
		}
	}
	return finishCSProj(m, relPath, packageID, packageOff, assembly, assemblyOff), nil
}

// finishCSProj installs the single NuGet identity the project publishes under,
// following MSBuild's documented fallback chain. A value containing an MSBuild
// property reference is not resolvable without evaluating the whole project, so
// it is passed over in favor of the next rule rather than registered as a
// literal name no consumer could ever declare.
func finishCSProj(m Manifest, relPath, packageID string, packageOff int, assembly string, assemblyOff int) Manifest {
	switch {
	case usableMSBuildValue(packageID):
		m.Provides = append(m.Provides, Provided{Kind: CSProj, Name: packageID, Offset: packageOff, Rule: RuleDeclaredName})
	case usableMSBuildValue(assembly):
		m.Provides = append(m.Provides, Provided{Kind: CSProj, Name: assembly, Offset: assemblyOff, Rule: RuleDeclaredName})
	default:
		base := path.Base(relPath)
		if ext := path.Ext(base); ext != "" {
			base = strings.TrimSuffix(base, ext)
		}
		if base != "" && base != "." && base != "/" {
			// The evidence is the file's own name, so there is no offset within
			// the content to point at.
			m.Provides = append(m.Provides, Provided{Kind: CSProj, Name: base, Offset: 0, Rule: RuleProjectFileName})
		}
	}
	return m
}

func usableMSBuildValue(value string) bool {
	return value != "" && !strings.Contains(value, "$(")
}

// attrValue returns the value of the named attribute, compared case-insensitively
// because MSBuild attribute names are.
func attrValue(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if strings.EqualFold(attr.Name.Local, name) {
			return attr.Value
		}
	}
	return ""
}

// attrValueOffset locates value inside the start tag's raw text so a declaration
// points at the name itself rather than at the element. It searches after the
// attribute name to avoid colliding with an identical value elsewhere in the tag,
// and falls back to the tag's own offset when the raw text differs from the
// decoded value (an XML entity inside the name).
func attrValueOffset(span []byte, spanStart int, attr, value string) int {
	base := spanStart
	if at := bytes.IndexByte(span, '<'); at >= 0 {
		base = spanStart + at
	}
	if value == "" {
		return base
	}
	from := 0
	if i := strings.Index(strings.ToLower(string(span)), strings.ToLower(attr)); i >= 0 {
		from = i + len(attr)
	}
	if i := bytes.Index(span[from:], []byte(value)); i >= 0 {
		return spanStart + from + i
	}
	return base
}
