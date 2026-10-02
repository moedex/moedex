// Package semanticimport validates compiler-worker output before admitting it to
// an offline semantic artifact. It does not execute projects or publish CURRENT.
package semanticimport

const WorkerSchema = "moedex.semantic-worker.v1"
const DefaultMaxBytes int64 = 64 << 20

// MaximumWireBytes bounds an explicitly enlarged JSONL stream, not source inputs or artifacts.
const MaximumWireBytes int64 = 256 << 20

type Options struct {
	MaxWireBytes           int64 // zero retains MaxBytes/default stream limit; does not enlarge input budgets
	Repo                   string
	Root                   string
	ProjectID              int64
	Commit                 string
	Origin                 string
	DependencyBundleSHA256 string
	MaxBytes               int64
}

type record struct {
	Schema              string                     `json:"schema"`
	Type                string                     `json:"record_type"`
	Repo                string                     `json:"repo"`
	Project             string                     `json:"project"`
	Context             string                     `json:"build_context"`
	Status              string                     `json:"compilation_status"`
	Extractor           string                     `json:"extractor"`
	ExtractorVersion    string                     `json:"extractor_version"`
	CompilerVersion     string                     `json:"compiler_version"`
	CaptureJSON         string                     `json:"capture_json"`
	Sources             []sourceRecord             `json:"sources"`
	ProjectReferences   []dependencyRecord         `json:"project_references"`
	Issues              []string                   `json:"issues"`
	SourcePath          string                     `json:"source_path"`
	SourceSHA           string                     `json:"source_sha256"`
	Span                spanRecord                 `json:"span"`
	SourceText          string                     `json:"source_text"`
	BindingStatus       string                     `json:"binding_status"`
	ReferenceKind       string                     `json:"reference_kind"`
	BindingMethod       string                     `json:"binding_method"`
	Symbol              *symbolRecord              `json:"symbol"`
	EnclosingSymbol     *symbolRecord              `json:"enclosing_symbol"`
	Candidates          []symbolRecord             `json:"candidates"`
	DomainFacts         []domainFactRecord         `json:"domain_facts,omitempty"`
	ImplementationFacts []implementationFactRecord `json:"implementation_facts,omitempty"`
	Declarations        int                        `json:"declarations"`
	References          int                        `json:"references"`
	Errors              int                        `json:"errors"`
	Projects            int                        `json:"projects"`
	Code                string                     `json:"code"`
	Severity            string                     `json:"severity"`
	Message             string                     `json:"message"`
}
type implementationFactRecord struct {
	Forwarding            *forwardingRecord `json:"forwarding,omitempty"`
	SelectedDefaultSymbol *symbolRecord     `json:"selected_default_symbol,omitempty"`
	DefaultTemplateSymbol *symbolRecord     `json:"default_template_symbol,omitempty"`
	Kind                  string            `json:"kind"`
	Rule                  string            `json:"rule"`
	EvidenceScope         string            `json:"evidence_scope"`
	InterfaceSymbol       *symbolRecord     `json:"interface_symbol"`
	ImplementingType      *symbolRecord     `json:"implementing_type"`
}
type domainFactRecord struct {
	Kind          string               `json:"kind"`
	Rule          string               `json:"rule"`
	EvidenceScope string               `json:"evidence_scope"`
	Targets       []domainTargetRecord `json:"targets"`
	Lifetime      string               `json:"lifetime,omitempty"`
	Table         string               `json:"table,omitempty"`
	Schema        string               `json:"schema,omitempty"`
	RoutePattern  string               `json:"route_pattern,omitempty"`
}
type domainTargetRecord struct {
	Role   string        `json:"role"`
	Symbol *symbolRecord `json:"symbol"`
}
type sourceRecord struct {
	Path          string  `json:"path"`
	SHA           string  `json:"sha256"`
	ByteSize      uint64  `json:"byte_size"`
	Generated     bool    `json:"generated"`
	ContentBase64 *string `json:"content_base64"`
}
type dependencyRecord struct {
	Project string `json:"project"`
	Context string `json:"build_context"`
}
type spanRecord struct {
	Offset uint64 `json:"byte_offset"`
	Length uint64 `json:"byte_length"`
}
type symbolRecord struct {
	Language       string `json:"language"`
	NamespaceKind  string `json:"namespace_kind"`
	Namespace      string `json:"namespace"`
	Descriptor     string `json:"descriptor"`
	DescriptorKind string `json:"descriptor_kind"`
}

type forwardingRecord struct {
	Rule                 string        `json:"rule"`
	Receiver             string        `json:"receiver"`
	Conversion           string        `json:"conversion"`
	TypeParameterOrdinal int           `json:"type_parameter_ordinal"`
	InterfaceSymbol      *symbolRecord `json:"interface_symbol"`
	ImplementationSymbol *symbolRecord `json:"implementation_symbol"`
	CastType             *symbolRecord `json:"cast_type"`
	CallProject          string        `json:"call_project"`
	CallContext          string        `json:"call_context"`
	CallPath             string        `json:"call_path"`
	CallSHA256           string        `json:"call_sha256"`
	CallSpan             spanRecord    `json:"call_span"`
}
