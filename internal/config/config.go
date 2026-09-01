// Package config provides the single typed registry for moedex runtime
// settings. Existing MOEDEX_* names are permanent compatibility contracts.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AppDir is the single default home for mutable Moedex state. It is separate
// from ~/.moedex, which is the managed Git corpus and must remain clean.
const AppDir = ".moedex-state"

// DefaultHomePath returns a conventional location under the current user's
// Moedex state directory. It returns an empty string only when the home
// directory cannot be resolved.
func DefaultHomePath(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home, AppDir}, parts...)...)
}

func DefaultCorpusDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".moedex")
}
func DefaultIndexDir() string        { return DefaultHomePath("index") }
func DefaultCASDir() string          { return DefaultHomePath("index", "cas") }
func DefaultShardDir() string        { return DefaultHomePath("index", "shards") }
func DefaultLSPWorkspaceDir() string { return DefaultHomePath("index", "lsp-workspaces") }
func DefaultToolsDir() string        { return DefaultHomePath("tools") }

type Kind string

const (
	String   Kind = "string"
	Integer  Kind = "integer"
	Float    Kind = "float"
	Boolean  Kind = "boolean"
	Duration Kind = "duration"
	Enum     Kind = "enum"
)

type Source string

const (
	SourceDefault Source = "default"
	SourceEnv     Source = "env"
	SourceFile    Source = "file"
	SourceFlag    Source = "flag"
)

// Setting is one runtime knob. Env is the stable public identifier.
type Setting struct {
	Env         string   `json:"name"`
	Flag        string   `json:"flag,omitempty"`
	Kind        Kind     `json:"type"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description"`
	Commands    []string `json:"commands"`
	Secret      bool     `json:"secret,omitempty"`
	Allowed     []string `json:"allowed,omitempty"`
}

// Value is an effective setting plus its provenance.
type Value struct {
	Name         string   `json:"name"`
	Value        string   `json:"value"`
	Source       Source   `json:"source"`
	SourceDetail string   `json:"source_detail,omitempty"`
	Default      string   `json:"default,omitempty"`
	Type         Kind     `json:"type"`
	Commands     []string `json:"commands"`
	Secret       bool     `json:"secret,omitempty"`
}

// Registry is immutable after construction.
type Registry struct {
	settings    []Setting
	byName      map[string]Setting
	passthrough map[string]bool
}

// State captures ambient and file values before applying them to legacy
// application packages.
type State struct {
	registry *Registry
	env      map[string]string
	file     map[string]string
	filePath string
}

// DefaultRegistry returns the complete runtime registry for the unified
// binary. Test/evaluation-only environment variables intentionally stay out.
func DefaultRegistry() *Registry {
	settings := []Setting{
		{Env: "MOEDEX_CORPUS", Flag: "--corpus", Kind: String, Description: "Managed corpus root", Commands: []string{"corpus", "index", "parity"}},
		{Env: "MOEDEX_CAS_DIR", Flag: "--cas-dir", Kind: String, Description: "Content-addressable store root", Commands: []string{"index cas"}},
		{Env: "MOEDEX_SHARD_DIR", Flag: "--shard-dir", Kind: String, Description: "Servable shard directory", Commands: []string{"search", "serve", "mcp", "graph", "doctor"}},
		{Env: "MOEDEX_INDEX_DIR", Flag: "--index-dir", Kind: String, Description: "Atomic snapshot root", Commands: []string{"search", "serve", "mcp", "index snapshot", "doctor"}},
		{Env: "MOEDEX_AUTH_TOKEN", Flag: "--auth-token", Kind: String, Description: "Bearer token for HTTP and MCP", Commands: []string{"serve"}, Secret: true},
		{Env: "MOEDEX_HTTP_ADDR", Flag: "--http", Kind: String, Description: "Retrieval HTTP bind address", Commands: []string{"serve"}},
		{Env: "MOEDEX_MCP_HTTP_ADDR", Flag: "--mcp-http", Kind: String, Description: "MCP HTTP bind address", Commands: []string{"serve", "doctor"}},
		{Env: "MOEDEX_SEARCH_MAX_CONCURRENCY", Flag: "--search-max-concurrency", Kind: Integer, Default: "8", Description: "Maximum concurrent HTTP searches", Commands: []string{"serve"}},
		{Env: "MOEDEX_MCP_MAX_CONCURRENCY", Flag: "--mcp-max-concurrency", Kind: Integer, Default: "8", Description: "Maximum concurrent MCP requests", Commands: []string{"serve"}},
		{Env: "MOEDEX_EMBED", Flag: "--embed", Kind: Enum, Default: "auto", Allowed: []string{"auto", "onnx", "http", "none"}, Description: "Dense embedder selection", Commands: []string{"serve", "mcp", "index"}},
		{Env: "MOEDEX_EMBED_URL", Kind: String, Description: "HTTP embedding service URL", Commands: []string{"serve", "mcp"}},
		{Env: "MOEDEX_EMBED_MODEL", Kind: String, Description: "HTTP embedding model identifier", Commands: []string{"serve", "mcp"}},
		{Env: "MOEDEX_ONNX_INTRA_OP_THREADS", Flag: "--onnx-intra-op-threads", Kind: Integer, Default: "0", Description: "ONNX threads within operators", Commands: []string{"serve", "index"}},
		{Env: "MOEDEX_ONNX_INTER_OP_THREADS", Flag: "--onnx-inter-op-threads", Kind: Integer, Default: "0", Description: "ONNX threads across operators", Commands: []string{"serve", "index"}},
		{Env: "MOEDEX_TLS_CERT", Flag: "--tls-cert", Kind: String, Description: "TLS certificate path", Commands: []string{"serve"}},
		{Env: "MOEDEX_TLS_KEY", Flag: "--tls-key", Kind: String, Description: "TLS private-key path", Commands: []string{"serve"}, Secret: true},
		{Env: "MOEDEX_GRAPH_SIMILAR_TOP_K", Kind: Integer, Default: "5", Description: "Semantic graph neighbors per symbol", Commands: []string{"graph", "index"}},
		{Env: "MOEDEX_GRAPH_SIMILAR_THRESHOLD", Kind: Float, Default: "0.60", Description: "Semantic graph similarity floor", Commands: []string{"graph", "index"}},
		{Env: "MOEDEX_GRAPH_SIMILAR_EXACT_LIMIT", Kind: Integer, Default: "4096", Description: "Exact semantic comparison limit", Commands: []string{"graph", "index"}},
		{Env: "MOEDEX_GRAPH_SIMILAR_MAX_CANDIDATES", Kind: Integer, Default: "2048", Description: "Maximum semantic graph candidates", Commands: []string{"graph", "index"}},
		{Env: "MOEDEX_GRAPH_LSP_CONCURRENCY", Kind: Integer, Default: "32", Description: "Concurrent graph LSP requests", Commands: []string{"graph", "index"}},
		{Env: "MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND", Kind: Float, Default: "100", Description: "Graph LSP request start rate", Commands: []string{"graph", "index"}},
		{Env: "MOEDEX_GRAPH_CLUSTER_MAX_NODES", Kind: Integer, Default: "250000", Description: "Maximum nodes considered for clustering", Commands: []string{"graph", "doctor"}},
		{Env: "MOEDEX_VERIFY_CONTENT", Kind: Boolean, Default: "true", Description: "Verify content hashes while loading", Commands: []string{"serve", "index"}},
		{Env: "MOEDEX_LSP_DEBUG", Kind: Boolean, Default: "false", Description: "Enable LSP protocol debugging", Commands: []string{"nav", "serve", "graph"}},
		{Env: "MOEDEX_LSP_WORKSPACE_DIR", Kind: String, Description: "Writable isolated workspace cache for managed-corpus language servers", Commands: []string{"nav", "serve", "graph", "index"}},
		{Env: "MOEDEX_MMAP", Kind: Boolean, Default: "false", Description: "Measure mmap reload in scale runs", Commands: []string{"corpus scale"}},
		{Env: "MOEDEX_SELECTIVE", Kind: Float, Description: "Selective trigram maximum document fraction", Commands: []string{"corpus scale"}},
	}
	byName := make(map[string]Setting, len(settings))
	for _, setting := range settings {
		byName[setting.Env] = setting
	}
	return &Registry{
		settings: settings,
		byName:   byName,
		passthrough: map[string]bool{
			"ONNXRUNTIME_LIB_PATH": true,
			"GITLAB_TOKEN":         true,
		},
	}
}

func (r *Registry) Settings() []Setting {
	out := append([]Setting(nil), r.settings...)
	sort.Slice(out, func(i, j int) bool { return out[i].Env < out[j].Env })
	return out
}

// Resolve validates ambient values and an optional explicit env file without
// mutating the process environment.
func (r *Registry) Resolve(environ []string, filePath string) (*State, []string, error) {
	env := parseEnviron(environ)
	state := &State{registry: r, env: env, file: map[string]string{}, filePath: filePath}
	if filePath != "" {
		file, err := os.Open(filePath)
		if err != nil {
			return nil, nil, err
		}
		defer file.Close()
		parsed, err := parseFile(file)
		if err != nil {
			return nil, nil, err
		}
		for key, value := range parsed {
			setting, registered := r.byName[key]
			if registered {
				if err := validate(setting, value); err != nil {
					return nil, nil, fmt.Errorf("%s: %w", key, err)
				}
				state.file[key] = value
				continue
			}
			if r.passthrough[key] {
				state.file[key] = value
				continue
			}
			if strings.HasPrefix(key, "MOEDEX_") {
				return nil, nil, fmt.Errorf("unregistered setting %s%s", key, r.suggestion(key))
			}
		}
	}

	var warnings []string
	for key, value := range env {
		setting, registered := r.byName[key]
		if registered && value != "" {
			if err := validate(setting, value); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", key, err)
			}
			continue
		}
		if strings.HasPrefix(key, "MOEDEX_") && !registered && !knownAuxiliary[key] {
			warnings = append(warnings, fmt.Sprintf("%s is set but not registered%s; ignoring it", key, r.suggestion(key)))
		}
	}
	sort.Strings(warnings)
	return state, warnings, nil
}

// Apply overlays explicit file values onto the process environment. This is
// the compatibility bridge for application packages that still read os.Getenv.
func (s *State) Apply() (restore func(), err error) {
	type previous struct {
		value string
		set   bool
	}
	old := make(map[string]previous, len(s.file))
	for key, value := range s.file {
		prior, set := os.LookupEnv(key)
		old[key] = previous{value: prior, set: set}
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}
	return func() {
		for key, prior := range old {
			if prior.set {
				_ = os.Setenv(key, prior.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}, nil
}

func (s *State) Values(diff bool) []Value {
	settings := s.registry.Settings()
	values := make([]Value, 0, len(settings))
	for _, setting := range settings {
		value := setting.Default
		source := SourceDefault
		detail := ""
		if envValue := s.env[setting.Env]; envValue != "" {
			value, source, detail = envValue, SourceEnv, setting.Env
		}
		if fileValue, ok := s.file[setting.Env]; ok {
			value, source, detail = fileValue, SourceFile, s.filePath
		}
		if diff && source == SourceDefault {
			continue
		}
		if setting.Secret && value != "" {
			value = "********"
		}
		values = append(values, Value{
			Name: setting.Env, Value: value, Source: source, SourceDetail: detail,
			Default: setting.Default, Type: setting.Kind, Commands: setting.Commands, Secret: setting.Secret,
		})
	}
	return values
}

func WriteJSON(w io.Writer, values []Value) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(struct {
		Schema   int     `json:"schema"`
		Settings []Value `json:"settings"`
	}{Schema: 1, Settings: values})
}

func parseEnviron(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		if index := strings.IndexByte(entry, '='); index > 0 {
			out[entry[:index]] = entry[index+1:]
		}
	}
	return out
}

func parseFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		index := strings.IndexByte(text, '=')
		if index <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", line)
		}
		key := strings.TrimSpace(text[:index])
		if !validKey(key) {
			return nil, fmt.Errorf("line %d: invalid key %q", line, key)
		}
		value := strings.TrimSpace(text[index+1:])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		out[key] = value
	}
	return out, scanner.Err()
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for index := 0; index < len(key); index++ {
		char := key[index]
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func validate(setting Setting, value string) error {
	if value == "" {
		return nil
	}
	switch setting.Kind {
	case String:
		return nil
	case Integer:
		_, err := strconv.Atoi(value)
		return err
	case Float:
		_, err := strconv.ParseFloat(value, 64)
		return err
	case Boolean:
		_, err := strconv.ParseBool(value)
		return err
	case Duration:
		_, err := time.ParseDuration(value)
		return err
	case Enum:
		for _, allowed := range setting.Allowed {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(setting.Allowed, ", "))
	default:
		return errors.New("unknown setting type")
	}
}

func (r *Registry) suggestion(key string) string {
	best, distance := "", len(key)+1
	for candidate := range r.byName {
		if current := editDistance(key, candidate); current < distance {
			best, distance = candidate, current
		}
	}
	if distance <= 8 {
		return "; closest match: " + best
	}
	return ""
}

func editDistance(left, right string) int {
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex := 1; leftIndex <= len(left); leftIndex++ {
		current := make([]int, len(right)+1)
		current[0] = leftIndex
		for rightIndex := 1; rightIndex <= len(right); rightIndex++ {
			cost := 0
			if left[leftIndex-1] != right[rightIndex-1] {
				cost = 1
			}
			current[rightIndex] = min(previous[rightIndex]+1, current[rightIndex-1]+1, previous[rightIndex-1]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}

var knownAuxiliary = map[string]bool{
	"MOEDEX_BENCH_SHARDS": true, "MOEDEX_CAS_PARITY_CORPUS": true,
	"MOEDEX_CAS_PARITY_MAXREPOS": true, "MOEDEX_CAS_PARITY_SHARDBYTES": true,
	"MOEDEX_CODE_DIM": true, "MOEDEX_CODE_MODEL": true, "MOEDEX_CODE_TOKENIZER": true,
	"MOEDEX_CORPUS_ROOT": true, "MOEDEX_EVAL_CORPUS": true,
	"MOEDEX_GRAPH_EVAL_SHARDS": true, "MOEDEX_GRAPH_GOLD": true,
	"MOEDEX_PRIVACY_CAS_MANIFEST": true, "MOEDEX_PRIVACY_CORPUS": true,
	"MOEDEX_PRIVACY_SERVED_MANIFEST": true,
	"MOEDEX_MCP_URL":                 true, "MOEDEX_TOKEN": true,
}
