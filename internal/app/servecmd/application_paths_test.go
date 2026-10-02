package servecmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/semantic"
)

type applicationPathLocation struct {
	Project string `json:"project"`
	Path    string `json:"path"`
	Offset  uint64 `json:"byte_offset"`
	Length  uint64 `json:"byte_length"`
	SHA     string `json:"source_sha256"`
}
type applicationPathCase struct {
	ID                        string                  `json:"id"`
	Message                   semantic.SymbolKey      `json:"message"`
	Caller                    applicationPathLocation `json:"caller"`
	CallerOwner               semantic.SymbolKey      `json:"caller_owner"`
	InterfaceMember           semantic.SymbolKey      `json:"interface_member"`
	InterfaceDeclaration      applicationPathLocation `json:"interface_declaration"`
	ImplementationType        semantic.SymbolKey      `json:"implementation_type"`
	ImplementationMethod      semantic.SymbolKey      `json:"implementation_method"`
	ImplementationDeclaration applicationPathLocation `json:"implementation_declaration"`
	Seed                      applicationPathLocation `json:"seed"`
	SeedCaseID                string                  `json:"seed_case_id"`
	SeedKind                  string                  `json:"seed_kind"`
	SeedRule                  string                  `json:"seed_rule"`
	Classification            string                  `json:"classification"`
}
type applicationPathGold struct {
	Version              int                   `json:"version"`
	Repo                 string                `json:"repo"`
	Commit               string                `json:"commit"`
	SourceGoldSHA        string                `json:"source_gold_sha256"`
	Paths                []applicationPathCase `json:"paths"`
	NegativeExpectations []struct {
		ID                string             `json:"id"`
		CallerCaseID      string             `json:"caller_case_id"`
		Message           semantic.SymbolKey `json:"message"`
		Count             int                `json:"expected_path_count"`
		ForbiddenFactKind string             `json:"forbidden_fact_kind"`
	} `json:"negative_expectations"`
}

func readApplicationPathGold(t *testing.T) (applicationPathGold, applicationGold) {
	t.Helper()
	base := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "application-outbox-20261001")
	b, err := os.ReadFile(filepath.Join(base, "wrapper-path-gold-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(b)) != "386cd0f02408434a08c65f54bc38d8e17d6c082395761441471efabcc70c6506" {
		t.Fatal("frozen pathgold changed")
	}
	var p applicationPathGold
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(base, "source-gold-v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(b)) != p.SourceGoldSHA {
		t.Fatal("path sourcegold changed")
	}
	var g applicationGold
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return p, g
}
func TestApplicationPathGoldPreservesDirectWrapperGaps(t *testing.T) {
	p, g := readApplicationPathGold(t)
	if p.Version != 1 || p.Repo != g.Repo || p.Commit != g.Commit || len(p.Paths) != 2 || len(p.NegativeExpectations) != 4 {
		t.Fatal("invalid pathgold")
	}
	cases := map[string]applicationCase{}
	for _, c := range g.Cases {
		cases[c.ID] = c
	}
	ids := map[string]bool{}
	for _, path := range p.Paths {
		c, seed := cases[path.ID], cases[path.SeedCaseID]
		if ids[path.ID] || c.Classification != "unsupported" || seed.Classification != "supported" || path.Message != seed.Target || path.Message != c.Target || path.Classification != "static_candidate" || path.SeedKind != "message_publish" || path.SeedRule != "csharp-framework-v1" || seed.Owner == nil || path.ImplementationMethod != *seed.Owner {
			t.Fatal("path changed direct domain classifications", path.ID)
		}
		ids[path.ID] = true
		for _, loc := range []applicationPathLocation{path.Caller, path.InterfaceDeclaration, path.ImplementationDeclaration, path.Seed} {
			if loc.Length == 0 || loc.SHA != g.ReviewedClosure.SourceHashes[loc.Path] {
				t.Fatal("path outside frozen source", loc.Path)
			}
		}
		if path.Caller.Offset != c.ByteOffset || path.Caller.Length != c.ByteLength || path.Seed.Offset != seed.ByteOffset || path.Seed.Length != seed.ByteLength {
			t.Fatal("path token drift", path.ID)
		}
	}
	for _, n := range p.NegativeExpectations {
		if !ids[n.CallerCaseID] || n.Count != 0 || (n.ForbiddenFactKind != "message_publish" && n.Message.Descriptor == "") {
			t.Fatal("invalid negative path expectation", n.ID)
		}
	}
}

// applicationPathMatches compares independently authored source expectations to
// normalized tool evidence. Context/snapshot scope is checked before this match.
func applicationPathMatches(want, got applicationPathCase) bool {
	return want.Message == got.Message && want.Caller == got.Caller && want.CallerOwner == got.CallerOwner && want.InterfaceMember == got.InterfaceMember && want.ImplementationType == got.ImplementationType && want.ImplementationMethod == got.ImplementationMethod && want.ImplementationDeclaration == got.ImplementationDeclaration && want.Seed == got.Seed && want.SeedKind == got.SeedKind && want.SeedRule == got.SeedRule && got.Classification == "static_candidate"
}
func TestApplicationPathScoringRejectsFalseJoins(t *testing.T) {
	gold, _ := readApplicationPathGold(t)
	want := gold.Paths[0]
	if !applicationPathMatches(want, want) {
		t.Fatal("valid path rejected")
	}
	for _, kind := range []string{"message", "caller-owner", "interface-namespace", "implementation", "source-hash", "source-span", "seed-kind", "runtime-proof"} {
		t.Run(kind, func(t *testing.T) {
			got := want
			switch kind {
			case "message":
				got.Message = gold.Paths[1].Message
			case "caller-owner":
				got.CallerOwner = gold.Paths[1].CallerOwner
			case "interface-namespace":
				got.InterfaceMember.Namespace = "foreign/Project.csproj"
			case "implementation":
				got.ImplementationMethod = gold.Paths[1].ImplementationMethod
			case "source-hash":
				got.Caller.SHA = "incorrect"
			case "source-span":
				got.ImplementationDeclaration.Offset++
			case "seed-kind":
				got.SeedKind = "message_publish_configuration"
			case "runtime-proof":
				got.Classification = "runtime_dispatch_proven"
			}
			if applicationPathMatches(want, got) {
				t.Fatal("false path join accepted", kind)
			}
		})
	}
}
