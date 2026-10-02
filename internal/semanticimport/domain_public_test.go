package semanticimport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
)

type domainGoldProvider struct {
	index              *semanticindex.Index
	artifact           string
	acquired, released int
}

func (p *domainGoldProvider) AcquireCompilerSession(ctx context.Context) (mcp.CompilerSession, error) {
	p.acquired++
	return mcp.CompilerSession{Reader: p.index, SnapshotID: "domain-gold-generation", ArtifactSHA256: p.artifact, CorpusFingerprint: strings.Repeat("b", 64), Release: func() { p.released++ }}, ctx.Err()
}

type domainGoldFact struct {
	Kind     string                              `json:"kind"`
	Targets  []struct{ Role, Descriptor string } `json:"targets"`
	Lifetime string                              `json:"lifetime"`
	Table    string                              `json:"table"`
	Schema   string                              `json:"schema"`
}
type domainGoldCase struct {
	ID         string           `json:"id"`
	Path       string           `json:"path"`
	Expected   []domainGoldFact `json:"expected"`
	Incomplete bool             `json:"incomplete"`
}

// The source fixture and expected facts were authored independently of worker
// matching. This gate uses real compiler output, writes/reopens both persistence
// formats, then checks the public MCP handler including typed target identities.
func TestPublicDomainSourceToMCP(t *testing.T) {
	stream, root := os.Getenv("MOEDEX_DOMAIN_STREAM"), os.Getenv("MOEDEX_DOMAIN_ROOT")
	if stream == "" || root == "" {
		t.Skip("set MOEDEX_DOMAIN_STREAM and MOEDEX_DOMAIN_ROOT from check-domain.py")
	}
	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	a, err := semanticimport.Import(context.Background(), bytes.NewReader(raw), semanticimport.Options{Repo: "domain-gold", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Complete() {
		t.Fatal("positive corpus compilation is incomplete")
	}
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "domain.semantic")
	if err := semantic.Write(artifactPath, a); err != nil {
		t.Fatal(err)
	}
	a, err = semantic.Read(artifactPath, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	artifactHash := fmt.Sprintf("%x", sha256.Sum256(artifactBytes))
	provenance := semanticindex.Provenance{ArtifactSHA256: artifactHash, CorpusFingerprint: strings.Repeat("b", 64)}
	indexPath := filepath.Join(dir, "domain.index")
	if err := semanticindex.Build(indexPath, a, provenance); err != nil {
		t.Fatal(err)
	}
	x, err := semanticindex.Open(indexPath, provenance, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	provider := &domainGoldProvider{index: x, artifact: artifactHash}
	var tool, definitionsTool mcp.ToolHandler
	targetsToNavigate := make(map[string]mcp.CompilerSymbol)
	for _, candidate := range mcp.CompilerTools(provider) {
		if candidate.Name() == "compiler_definitions" {
			definitionsTool = candidate
		}
		if candidate.Name() == "compiler_binding_at" {
			tool = candidate
		}
	}
	if tool == nil {
		t.Fatal("compiler_binding_at unavailable")
	}
	goldBytes, err := os.ReadFile("../eval/testdata/semantic-intelligence/domain-v1/gold.json")
	if err != nil {
		t.Fatal(err)
	}
	var gold struct {
		Cases []domainGoldCase `json:"cases"`
	}
	if err := json.Unmarshal(goldBytes, &gold); err != nil {
		t.Fatal(err)
	}
	checked, positives := 0, 0
	for _, tc := range gold.Cases {
		if tc.Incomplete {
			continue
		}
		t.Run(tc.ID, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, tc.Path))
			if err != nil {
				t.Fatal(err)
			}
			marker := []byte("/*gold:" + tc.ID + "*/")
			if bytes.Count(content, marker) != 1 {
				t.Fatal("gold marker drift")
			}
			offset := bytes.Index(content, marker) + len(marker)
			var source semantic.Source
			for _, s := range a.Sources {
				if s.Path == tc.Path {
					source = s
					break
				}
			}
			if source.ID == "" || source.RawSHA256 != fmt.Sprintf("%x", sha256.Sum256(content)) {
				t.Fatal("source identity drift")
			}
			contextID := ""
			for _, c := range a.Contexts {
				for _, id := range c.SourceIDs {
					if id == source.ID {
						if contextID != "" && contextID != c.ID {
							t.Fatal("ambiguous fixture context")
						}
						contextID = c.ID
					}
				}
			}
			args, _ := json.Marshal(map[string]any{"repo": "domain-gold", "path": tc.Path, "byte_offset": offset, "raw_sha256": source.RawSHA256, "context_id": contextID})
			response, err := tool.Call(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(response["structuredContent"])
			if err != nil {
				t.Fatal(err)
			}
			var result mcp.CompilerResult
			if err := json.Unmarshal(wire, &result); err != nil {
				t.Fatal(err)
			}
			if result.Error != nil || result.Truncated || len(result.Results) != 1 {
				t.Fatalf("unexpected MCP response %s", wire)
			}
			binding := result.Results[0]
			if binding.ContextID != contextID || binding.RawSHA256 != source.RawSHA256 || binding.ByteOffset != uint64(offset) || binding.ExtractorVersion != "2" {
				t.Fatalf("lost compiler provenance: %+v", binding)
			}
			if tc.ID == "consumes" {
				foundOwner := false
				for _, sym := range a.Symbols {
					if sym.ID == binding.EnclosingSymbolID && sym.Key.Descriptor == "T:DomainGold.Consumer" {
						foundOwner = true
					}
				}
				if !foundOwner {
					t.Fatal("consumer enclosing class identity lost")
				}
			}
			if len(binding.DomainFacts) != len(tc.Expected) {
				t.Fatalf("facts=%+v expected=%+v", binding.DomainFacts, tc.Expected)
			}
			for i, fact := range binding.DomainFacts {
				expected := tc.Expected[i]
				if fact.Kind != expected.Kind || fact.Rule != "csharp-framework-v1" || fact.EvidenceScope != "compile_time" || fact.Lifetime != expected.Lifetime || fact.Table != expected.Table || fact.Schema != expected.Schema {
					t.Fatalf("fact differs: %+v want %+v", fact, expected)
				}
				got := []struct{ Role, Descriptor string }{}
				for _, target := range fact.Targets {
					targetsToNavigate[target.Symbol.ID] = target.Symbol
					if target.Symbol.NamespaceKind != "project" || target.Symbol.Namespace != "domain-gold/Domain.csproj" {
						t.Fatalf("target identity erased: %+v", target)
					}
					got = append(got, struct{ Role, Descriptor string }{target.Role, target.Symbol.Descriptor})
				}
				if !reflect.DeepEqual(got, expected.Targets) {
					t.Fatalf("targets=%+v want=%+v", got, expected.Targets)
				}
				positives++
			}
			checked++
			t.Logf("source_to_mcp case=%s offset=%d facts=%d structured_sha256=%x", tc.ID, offset, len(binding.DomainFacts), sha256.Sum256(wire))
		})
	}
	emitted := 0
	for _, binding := range a.Bindings {
		emitted += len(binding.DomainFacts)
	}
	if emitted != positives {
		t.Fatalf("unaccounted emitted facts=%d gold=%d", emitted, positives)
	}
	if definitionsTool == nil {
		t.Fatal("definitions tool unavailable")
	}
	for id, target := range targetsToNavigate {
		args, _ := json.Marshal(map[string]any{"symbol_id": id, "repo": "domain-gold"})
		response, err := definitionsTool.Call(context.Background(), args)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(response["structuredContent"])
		if err != nil {
			t.Fatal(err)
		}
		var result mcp.CompilerResult
		if err := json.Unmarshal(wire, &result); err != nil {
			t.Fatal(err)
		}
		if result.Error != nil || result.Truncated || len(result.Results) != 1 {
			t.Fatalf("target navigation %s: %s", target.Descriptor, wire)
		}
		declaration := result.Results[0]
		if declaration.Symbol == nil || *declaration.Symbol != target || declaration.Path != "Domain.cs" {
			t.Fatalf("wrong target declaration: %+v", declaration)
		}
		t.Logf("compiler_definitions target=%s offset=%d structured_sha256=%x", target.Descriptor, declaration.ByteOffset, sha256.Sum256(wire))
	}
	if checked < 15 || positives < 9 {
		t.Fatalf("insufficient real compiler gold coverage cases=%d positives=%d", checked, positives)
	}
	if provider.acquired != provider.released {
		t.Fatal("compiler serving lease leaked")
	}
	t.Logf("real source→worker→artifact→index→MCP cases=%d positive_facts=%d", checked, positives)
}

func TestPublicDomainRejectsForgedCaptureFacts(t *testing.T) {
	stream, root := os.Getenv("MOEDEX_DOMAIN_STREAM"), os.Getenv("MOEDEX_DOMAIN_ROOT")
	if stream == "" || root == "" {
		t.Skip("set real domain capture paths")
	}
	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"lookalike_api", "method_target", "invalid_lifetime", "runtime_claim"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			changed := false
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
				var row map[string]any
				if err := json.Unmarshal(line, &row); err != nil {
					t.Fatal(err)
				}
				facts, _ := row["domain_facts"].([]any)
				if !changed && len(facts) > 0 && facts[0].(map[string]any)["kind"] == "di_registration" {
					f := facts[0].(map[string]any)
					switch mode {
					case "lookalike_api":
						row["symbol"].(map[string]any)["namespace"] = "Unrelated, Version=1.0.0.0, Culture=neutral, PublicKeyToken=null"
					case "method_target":
						f["targets"].([]any)[0].(map[string]any)["symbol"].(map[string]any)["descriptor"] = "M:DomainGold.Service.Fake"
					case "invalid_lifetime":
						f["lifetime"] = "forever"
					case "runtime_claim":
						f["evidence_scope"] = "runtime_delivery"
					}
					changed = true
				}
				b, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				out.Write(b)
				out.WriteByte('\n')
			}
			if !changed {
				t.Fatal("no real DI fact in capture")
			}
			if _, err := semanticimport.Import(context.Background(), &out, semanticimport.Options{Repo: "domain-gold", Root: root}); err == nil {
				t.Fatal("forged domain fact admitted")
			}
		})
	}
}

func TestPublicDomainIncompleteFactsNotServable(t *testing.T) {
	stream, root := os.Getenv("MOEDEX_DOMAIN_STREAM"), os.Getenv("MOEDEX_DOMAIN_ROOT")
	if stream == "" || root == "" {
		t.Skip("set real domain capture paths")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(stream), "incomplete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := semanticimport.Import(context.Background(), bytes.NewReader(raw), semanticimport.Options{Repo: "domain-gold", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if a.Complete() {
		t.Fatal("incomplete real compilation admitted as complete")
	}
	for _, binding := range a.Bindings {
		if len(binding.DomainFacts) > 0 {
			t.Fatal("incomplete context exposes domain facts")
		}
	}
	if err := semanticindex.Build(filepath.Join(t.TempDir(), "incomplete.index"), a, semanticindex.Provenance{ArtifactSHA256: strings.Repeat("a", 64), CorpusFingerprint: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("incomplete corpus received serving index")
	}
}
