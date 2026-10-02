package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

func forwardingFixture() (ImplementationFact, []Symbol, Occurrence, Binding, Source, BuildContext) {
	symbol := func(desc string) Symbol {
		s := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A.csproj", DescriptorKind: "documentation_comment_id", Descriptor: desc}}
		s.ID = s.ComputeID()
		return s
	}
	typ, iface, template, cast, impl := symbol("T:Handler"), symbol("M:IBase.Run(System.Object)"), symbol("M:IHandler`1.IBase#Run(System.Object)"), symbol("T:Message"), symbol("M:Handler.Accept(Message)")
	closed := func(desc string) Symbol {
		s := symbol(desc)
		raw, _ := json.Marshal(constructedMethod{Definition: desc, Arguments: []SymbolKey{cast.Key}})
		s.Key.Descriptor = string(raw)
		s.Key.DescriptorKind = "constructed_interface_method_v1"
		s.ID = s.ComputeID()
		return s
	}
	selected, target := closed(template.Key.Descriptor), closed("M:IHandler`1.Accept(`0)")
	source := Source{ID: "source:" + strings.Repeat("a", 64), SnapshotID: "snapshot", Path: "A.cs", RawSHA256: strings.Repeat("b", 64), ByteSize: 100}
	context := BuildContext{ID: "context:" + strings.Repeat("c", 64), SnapshotID: "snapshot", Project: "A.csproj", Status: "complete", Extractor: "msbuild-roslyn", ExtractorVersion: "9"}
	o := Occurrence{SourceID: source.ID, ContextID: context.ID, Role: "reference", Kind: "invocation", Offset: 20, Length: 6}
	o.ID = o.ComputeID()
	original := symbol("M:IHandler`1.Accept(`0)")
	b := Binding{OccurrenceID: o.ID, Status: "resolved", SymbolID: original.ID, EnclosingSymbolID: template.ID, Method: "roslyn-semantic-model", Extractor: "msbuild-roslyn", ExtractorVersion: "9"}
	f := ImplementationFact{Kind: "interface_default_selection", Rule: "csharp-interface-selection-v1", EvidenceScope: "compile_time", InterfaceSymbolID: iface.ID, ImplementingTypeSymbolID: typ.ID, SelectedDefaultSymbolID: selected.ID, DefaultTemplateSymbolID: template.ID, Forwarding: &DefaultForwarding{Rule: "csharp-default-forward-v1", Receiver: "this", Conversion: "explicit_type_parameter_cast", InterfaceSymbolID: target.ID, ImplementationSymbolID: impl.ID, CastTypeSymbolID: cast.ID, CallOccurrenceID: o.ID, CallContextID: context.ID, CallSourceID: source.ID, CallPath: source.Path, CallSHA256: source.RawSHA256, CallOffset: o.Offset, CallLength: o.Length}}
	return f, []Symbol{typ, iface, selected, template, target, impl, cast}, o, b, source, context
}

func TestDefaultForwardingValidation(t *testing.T) {
	f, s, o, b, source, c := forwardingFixture()
	check := func(f ImplementationFact, s []Symbol) error {
		return ValidateImplementationSymbols(f, s[0], s[1], s[0], s[2:]...)
	}
	if err := ValidateImplementationFacts([]ImplementationFact{f}); err != nil {
		t.Fatal(err)
	}
	if err := check(f, s); err != nil {
		t.Fatal(err)
	}
	if err := ValidateForwardingWitness(f, "snapshot", o, b, source, c, s[3], s[4]); err != nil {
		t.Fatal(err)
	}
	if ValidateImplementationExtractor(f, "8") == nil || ValidateImplementationExtractor(f, "9") != nil || ValidateImplementationExtractor(f, "10") != nil || ValidateImplementationExtractor(f, "11") != nil || ValidateImplementationExtractor(f, "12") != nil || ValidateImplementationExtractor(f, "13") != nil || ValidateImplementationExtractor(f, "14") != nil || ValidateImplementationExtractor(f, "15") != nil || ValidateImplementationExtractor(f, "16") != nil || ValidateImplementationExtractor(f, "17") != nil || ValidateImplementationExtractor(f, "18") != nil || ValidateImplementationExtractor(f, "19") != nil || ValidateImplementationExtractor(f, "20") != nil || ValidateImplementationExtractor(f, "22") == nil {
		t.Fatal("version gate")
	}
	for name, change := range map[string]func(*DefaultForwarding){
		"other receiver": func(v *DefaultForwarding) { v.Receiver = "other" },
		"as cast":        func(v *DefaultForwarding) { v.Conversion = "as" },
		"wrong ordinal":  func(v *DefaultForwarding) { v.TypeParameterOrdinal = 1 },
		"wrong cast":     func(v *DefaultForwarding) { v.CastTypeSymbolID = s[0].ID },
		"wrong target":   func(v *DefaultForwarding) { v.ImplementationSymbolID = s[1].ID },
		"wrong offset":   func(v *DefaultForwarding) { v.CallOffset++ },
		"no witness":     func(v *DefaultForwarding) { v.CallOccurrenceID = "" },
		"overflow":       func(v *DefaultForwarding) { v.CallOffset = ^uint64(0) },
	} {
		t.Run(name, func(t *testing.T) {
			f, s, _, _, _, _ := forwardingFixture()
			change(f.Forwarding)
			if check(f, s) == nil {
				t.Fatal("invalid forwarding admitted")
			}
		})
	}
	for name, change := range map[string]func(*Binding, *Source, *BuildContext){
		"wrong call":    func(b *Binding, s *Source, c *BuildContext) { b.SymbolID = "wrong" },
		"wrong owner":   func(b *Binding, s *Source, c *BuildContext) { b.EnclosingSymbolID = "wrong" },
		"stale source":  func(b *Binding, s *Source, c *BuildContext) { s.RawSHA256 = strings.Repeat("f", 64) },
		"wrong context": func(b *Binding, s *Source, c *BuildContext) { c.Project = "Other.csproj" },
		"incomplete":    func(b *Binding, s *Source, c *BuildContext) { c.Status = "incomplete" },
	} {
		t.Run(name, func(t *testing.T) {
			f, s, o, b, source, c := forwardingFixture()
			change(&b, &source, &c)
			if ValidateForwardingWitness(f, "snapshot", o, b, source, c, s[3], s[4]) == nil {
				t.Fatal("invalid call witness admitted")
			}
		})
	}
}
