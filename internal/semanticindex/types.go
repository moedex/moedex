// Package semanticindex provides bounded mmap lookups over immutable compiler evidence.
package semanticindex

import "moedex/internal/semantic"

const Format = "moedex.semantic-index"
const Version = 5
const MaxBytes int64 = 128 << 20
const MaxResults = 100

type Provenance struct {
	ArtifactSHA256    string
	CorpusFingerprint string
}
type Expected = Provenance
type Limits struct{ MaxBytes int64 }
type Position struct {
	Repo           string
	Path           string
	Offset         uint64
	BuildContextID string
}
type Filter struct {
	Repo           string
	PathPrefix     string
	BuildContextID string
}

// Result owns its strings. Context omits capture/input manifests; the audit
// artifact retains them. Source omits inline generated content.
type Result struct {
	Snapshot              semantic.SourceSnapshot `json:"snapshot"`
	Context               semantic.BuildContext   `json:"context"`
	Source                semantic.Source         `json:"source"`
	Occurrence            semantic.Occurrence     `json:"occurrence"`
	Binding               semantic.Binding        `json:"binding"`
	Symbol                *semantic.Symbol        `json:"symbol,omitempty"`
	Candidates            []semantic.Symbol       `json:"candidates,omitempty"`
	ImplementationSymbols []semantic.Symbol       `json:"implementation_symbols,omitempty"`
	DomainSymbols         []semantic.Symbol       `json:"domain_symbols,omitempty"`
}
type QueryResult struct {
	Results   []Result `json:"results"`
	Truncated bool     `json:"truncated"`
}

type SourceContext struct {
	Snapshot semantic.SourceSnapshot `json:"snapshot"`
	Context  semantic.BuildContext   `json:"context"`
	Source   semantic.Source         `json:"source"`
}

// ContractImpactMatch is one recorded domain observation, never a runtime edge.
// TargetRoles coalesces multiple roles of the queried symbol in the same fact.
type ContractImpactMatch struct {
	Result          Result
	DomainFactIndex int
	TargetRoles     []string
	Owner           *semantic.Symbol
}
type ContractImpactResult struct {
	materializationBytes uint64
	Supported            bool
	Contract             *semantic.Symbol
	Contexts             []SourceContext
	Matches              []ContractImpactMatch
	Truncated            bool
	PostingsVisited      int
	WorkLimit            int
}

// ImplementationMatch is an exact compiler relationship at a method declaration.
// Its ordinal selects one interface member from the declaration's recorded facts.
type ImplementationMatch struct {
	Result                  Result
	ImplementationFactIndex int
}
type ImplementationsResult struct {
	Supported   bool
	Interface   *semantic.Symbol
	Contexts    []SourceContext
	Matches     []ImplementationMatch
	Truncated   bool
	RowsVisited int
	WorkLimit   int
}
