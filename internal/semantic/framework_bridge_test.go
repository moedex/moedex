package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClosedInterfaceIdentityAndDefaultCorrespondence(t *testing.T) {
	key := SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/contracts", DescriptorKind: "constructed_interface_method_v1"}
	arg := SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/events", Descriptor: "T:Message", DescriptorKind: "documentation_comment_id"}
	descriptor := func(a SymbolKey) string {
		b, _ := json.Marshal(map[string]any{"definition": "M:IHandler`1.Handle(`0)", "arguments": []SymbolKey{a}})
		return string(b)
	}
	key.Descriptor = descriptor(arg)
	if err := ValidateConstructedInterfaceMethod(key); err != nil {
		t.Fatal(err)
	}
	a := Symbol{Key: key}
	arg.Namespace = "repo/other"
	key.Descriptor = descriptor(arg)
	b := Symbol{Key: key}
	if a.ComputeID() == b.ComputeID() {
		t.Fatal("qualified generic arguments collapsed")
	}
	for _, bad := range []string{`{"definition":"M:I.Handle","arguments":[]}`, `{"definition":"M:I` + "`1" + `.Handle","arguments":[null]}`, `{"definition":"M:I` + "`1" + `.Handle","arguments":[],"extra":true}`} {
		key.Descriptor = bad
		if ValidateConstructedInterfaceMethod(key) == nil {
			t.Fatal("bad constructed key admitted")
		}
	}
	f := ImplementationFact{Kind: "interface_method_implementation", Rule: "csharp-interface-default-v1", EvidenceScope: "compile_time", InterfaceSymbolID: "iface", ImplementingTypeSymbolID: "type"}
	symbol := func(id, desc string) Symbol {
		return Symbol{ID: id, Key: SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/contracts", Descriptor: desc, DescriptorKind: "documentation_comment_id"}}
	}
	impl, iface, typ := symbol("impl", "M:IHandler`1.IBridge#Handle(System.Object)"), symbol("iface", "M:IBridge.Handle(System.Object)"), symbol("type", "T:IHandler`1")
	if err := ValidateImplementationSymbols(f, impl, iface, typ); err != nil {
		t.Fatal(err)
	}
	typ.Key.Descriptor = "T:Other`1"
	if ValidateImplementationSymbols(f, impl, iface, typ) == nil {
		t.Fatal("wrong default owner admitted")
	}
	if ValidateImplementationExtractor(f, "6") == nil || ValidateImplementationExtractor(f, "7") != nil {
		t.Fatal("default rule downgrade allowed")
	}
}

func TestKeyedRegistrationShapeAndWitness(t *testing.T) {
	for _, lifetime := range []string{"scoped", "transient", "singleton"} {
		name := map[string]string{"scoped": "AddKeyedScoped", "transient": "AddKeyedTransient", "singleton": "AddKeyedSingleton"}[lifetime]
		api := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "assembly", Namespace: "Microsoft.Extensions.DependencyInjection.Abstractions, Version=8.0.0.0", DescriptorKind: "documentation_comment_id", Descriptor: "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions." + name + "``2(Microsoft.Extensions.DependencyInjection.IServiceCollection,System.Object)"}}
		api.ID = api.ComputeID()
		f := DomainFact{Kind: "di_keyed_registration", Rule: "csharp-keyed-di-v1", EvidenceScope: "compile_time", Lifetime: lifetime, Targets: []DomainTarget{{"service", "symbol:" + strings.Repeat("a", 64)}, {"implementation", "symbol:" + strings.Repeat("b", 64)}, {"key_type", "symbol:" + strings.Repeat("c", 64)}, {"registration_api", api.ID}}}
		if ValidateDomainFacts([]DomainFact{f}) != nil || ValidateDomainAPI(f, api) != nil || ValidateDomainFactTarget(f, "registration_api", api) != nil {
			t.Fatal("valid keyed fact rejected")
		}
		if ValidateDomainExtractor(f, "6") == nil || ValidateDomainExtractor(f, "7") != nil {
			t.Fatal("keyed rule downgrade accepted")
		}
		fake := api
		fake.Key.NamespaceKind = "project"
		if ValidateDomainFactTarget(f, "registration_api", fake) == nil {
			t.Fatal("lookalike API witness accepted")
		}
		f.Kind = "di_keyed_registration_configuration"
		helper := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/helpers", DescriptorKind: "documentation_comment_id", Descriptor: "M:Helpers.Register``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)"}}
		if ValidateDomainAPI(f, helper) != nil || ValidateDomainAPI(f, api) == nil {
			t.Fatal("source helper scope not enforced")
		}
		f.Targets[2].Role = "message"
		if ValidateDomainFacts([]DomainFact{f}) == nil {
			t.Fatal("keyed role confusion")
		}
	}
}
