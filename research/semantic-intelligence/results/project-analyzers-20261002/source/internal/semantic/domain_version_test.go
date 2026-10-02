package semantic

import "testing"

func TestDomainExtractorVersions(t *testing.T) {
	for _, rule := range []string{"csharp-framework-v1", "csharp-framework-v2", "csharp-framework-v3", "csharp-framework-v4", "csharp-framework-v5", "csharp-framework-v6", "csharp-endpoint-v1", "csharp-mediatr-v1", "csharp-keyed-di-v1", "csharp-open-di-v1", "csharp-mediatr-scan-v1", "csharp-masstransit-scan-v1", "csharp-routing-slip-v1", "unknown"} {
		for _, version := range []string{"", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22"} {
			want := rule == "csharp-framework-v1" && (version == "2" || version == "3" || version == "4" || version == "5" || version == "6" || version == "7" || version == "8" || version == "9" || (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))))))) || rule == "csharp-framework-v2" && (version == "3" || version == "4" || version == "5" || version == "6" || version == "7" || version == "8" || version == "9" || (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))))))) || rule == "csharp-framework-v3" && (version == "4" || version == "5" || version == "6" || version == "7" || version == "8" || version == "9" || (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))))))) || rule == "csharp-framework-v4" && (version == "5" || version == "6" || version == "7" || version == "8" || version == "9" || (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))))))) || rule == "csharp-keyed-di-v1" && (version == "7" || version == "8" || version == "9" || (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))))))) || rule == "csharp-framework-v5" && (version == "10" || (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21"))))))))))) || rule == "csharp-framework-v6" && (version == "11" || (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))))) || rule == "csharp-endpoint-v1" && (version == "12" || (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21"))))))))) || rule == "csharp-mediatr-v1" && (version == "13" || (version == "14" || (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))))) || rule == "csharp-open-di-v1" && (version == "15" || (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))))) || rule == "csharp-mediatr-scan-v1" && (version == "16" || (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21"))))) || rule == "csharp-masstransit-scan-v1" && (version == "17" || (version == "18" || (version == "19" || (version == "20" || version == "21")))) || rule == "csharp-routing-slip-v1" && (version == "18" || (version == "19" || (version == "20" || version == "21")))
			if got := ValidateDomainExtractor(DomainFact{Rule: rule}, version) == nil; got != want {
				t.Errorf("rule %s worker %q: accepted=%v want %v", rule, version, got, want)
			}
		}
	}
}

func TestDomainArtifactWorker3AndDowngrade(t *testing.T) {
	for _, version := range []string{"2", "3"} {
		a := domainFixture()
		c := &a.Contexts[0]
		c.ExtractorVersion = version
		c.ID = c.ComputeID()
		o := &a.Occurrences[0]
		o.ContextID = c.ID
		o.ID = o.ComputeID()
		api := &a.Symbols[1]
		api.Key.Namespace = "Microsoft.EntityFrameworkCore, Version=8.0.4.0"
		api.Key.Descriptor = "M:Microsoft.EntityFrameworkCore.DbContext.Set``1"
		api.ID = api.ComputeID()
		b := &a.Bindings[0]
		b.ExtractorVersion = version
		b.OccurrenceID = o.ID
		b.SymbolID = api.ID
		b.DomainFacts = []DomainFact{{Kind: "storage_entity_use", Rule: "csharp-framework-v2", EvidenceScope: "compile_time", Targets: []DomainTarget{{Role: "entity", SymbolID: a.Symbols[0].ID}}}}
		b.ID = b.ComputeID()
		if err := a.Validate(); (err == nil) != (version == "3") {
			t.Fatalf("worker %s: %v", version, err)
		}
	}
}
