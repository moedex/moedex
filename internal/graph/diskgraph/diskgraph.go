// Package diskgraph persists the graph layer's content-addressed adjacency list.
//
// A node is keyed by (git blob SHA, symbol byte offset). Each node points at a
// contiguous range of fixed-width edge records, so Open can mmap the file and a
// lookup only binary-searches the node table and decodes the requested range.
// Blob SHAs and edge names are interned once in variable-width tables; node and
// edge records refer to them by uint32 ID.
//
// Beyond adjacency the file records two things for incremental refresh:
//
//   - Every edge carries the NAME that generated it and the GENERATION it was
//     last computed in. The graph is built name by name, so the name is the unit
//     an incremental rebuild can recompute or carry forward, and the generation
//     is the per-edge staleness stamp.
//   - The CORPUS ROSTER holds one opaque identity token per blob the graph was
//     built over, including blobs that produced no edge. Diffing the roster
//     against the current corpus yields the added/removed delta.
package diskgraph

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"moedex/internal/graph"
)

const (
	magic         = "MDXGRF01"
	formatVersion = 3
	headerSize    = 112
	nodeSize      = 32
	edgeSize      = 64
)

// FirstGeneration is the generation stamped on a graph built from scratch.
// Zero is reserved for "unstamped", so a caller can tell an edge that predates
// generation tracking from one computed in the first build.
const FirstGeneration uint64 = 1

// EdgeType is the persisted relationship type. Values are deliberately a
// uint32 on disk so later graph phases can add semantic types without changing
// the record layout.
type EdgeType uint32

const (
	EdgeUnknown EdgeType = iota
	EdgeCandidate
	EdgeCalls
	EdgeImports
	EdgeUsesType
	EdgeReferences
	EdgeSiblingDefinition
	// EdgePublishes links a publisher method/type to the event or message it
	// emits. EdgeConsumes links a consumer type to the event or message it
	// handles. Both retain the graph's dependency -> definition direction.
	EdgePublishes
	EdgeConsumes
	EdgeSimilarTo
	// EdgeHTTPCalls is a cross-service HTTP relationship: a client call site
	// whose URL matches a route handler's template (internal/graph/httproute).
	EdgeHTTPCalls
	// EdgeDependsOn is a package-manifest dependency: the source content declares
	// a dependency that the target content declares it publishes. Unlike the edge
	// types above it is not derived from a symbol, so its source node is the
	// manifest file itself at offset 0 and its evidence points at the declaration.
	EdgeDependsOn
	// EdgeExtends links a type to its base type (C# class inheritance, TS extends,
	// Go struct embedding). Source is the derived type, target is the base type.
	EdgeExtends
	// EdgeImplements links a type to an interface it implements (C# : IFoo,
	// TS implements IFoo, Go interface embedding). Source is the implementor,
	// target is the interface.
	EdgeImplements
	// EdgeContainsMethod links a type to a method defined within its body. Source
	// is the type, target is the method. Both share the same blob SHA.
	EdgeContainsMethod
	// EdgeInjects links a DI registration site to the service type it registers.
	// For two-argument registrations (AddScoped<IFoo, Foo>), a companion
	// EdgeImplements may also be emitted when the hierarchy pass missed it.
	EdgeInjects
	// EdgeQueries links a method that accesses an EF DbSet to the entity type
	// it queries. Source is the querying method, target is the entity definition.
	EdgeQueries
	// EdgeRenders links a parent component to a child component it renders in
	// its template. Source is the parent, target is the child.
	EdgeRenders
)

// String renders an edge type using the stable names exposed by graph-query
// APIs. Unknown future values remain representable instead of being collapsed.
func (t EdgeType) String() string {
	switch t {
	case EdgeCandidate:
		return "candidate"
	case EdgeCalls:
		return "calls"
	case EdgeImports:
		return "imports"
	case EdgeUsesType:
		return "uses_type"
	case EdgeReferences:
		return "references"
	case EdgeSiblingDefinition:
		return "sibling_definition"
	case EdgePublishes:
		return "publishes"
	case EdgeConsumes:
		return "consumes"
	case EdgeSimilarTo:
		return "similar_to"
	case EdgeHTTPCalls:
		return "http_calls"
	case EdgeDependsOn:
		return "depends_on"
	case EdgeExtends:
		return "extends"
	case EdgeImplements:
		return "implements"
	case EdgeContainsMethod:
		return "contains_method"
	case EdgeInjects:
		return "injects"
	case EdgeQueries:
		return "queries"
	case EdgeRenders:
		return "renders"
	default:
		return fmt.Sprintf("edge_type_%d", uint32(t))
	}
}

// Key is a graph node: a definition (or, when no enclosing definition exists,
// an evidence occurrence) within content identified by its git blob SHA.
type Key struct {
	BlobSHA      string
	SymbolOffset uint64
}

// Node is an alias retained for callers that prefer graph terminology.
type Node = Key

// Edge is one outgoing adjacency record. TargetBlob is a git blob SHA.
// Confidence is one of the four provenance-backed tiers. EdgeSimilarTo also
// persists its cosine metric separately in Similarity; that metric does not
// change the edge's Candidate provenance.
//
// Name is the symbol name whose candidate sweep produced the edge, and
// Generation is the refresh that last computed it — an edge carried forward
// unchanged keeps the older stamp, which is what makes edge staleness
// observable per edge rather than per file.
type Edge struct {
	Type       EdgeType
	TargetBlob string
	// TargetOffset is the byte offset of the target definition within its blob.
	TargetOffset uint64
	Confidence   graph.ConfidenceTier
	Evidence     graph.Evidence
	// Similarity is the exact cosine metric for EdgeSimilarTo. Other edge types
	// leave this field zero.
	Similarity float64
	Name       string
	Generation uint64
}

// Weight returns the relationship weight used by clustering. Confidence stays
// tier-derived; semantic edges use their separate cosine metric as graph weight.
func (e Edge) Weight() float64 {
	if e.Type == EdgeSimilarTo {
		return e.Similarity
	}
	return e.Confidence.Score()
}

func validateSimilarity(edge Edge) error {
	if edge.Type != EdgeSimilarTo {
		if edge.Similarity != 0 {
			return fmt.Errorf("diskgraph: similarity metric on non-similar edge")
		}
		return nil
	}
	if math.IsNaN(edge.Similarity) || edge.Similarity < -1 || edge.Similarity > 1 {
		return fmt.Errorf("diskgraph: invalid cosine similarity %g", edge.Similarity)
	}
	return nil
}

// MarshalJSON exposes the structured confidence and evidence contracts used by
// MCP graph tools while keeping the compact tier enum in memory and on disk.
func (e Edge) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type         string           `json:"type"`
		TargetBlob   string           `json:"target_blob"`
		TargetOffset uint64           `json:"target_offset"`
		Confidence   graph.Confidence `json:"confidence"`
		Evidence     graph.Evidence   `json:"evidence"`
		Similarity   float64          `json:"similarity,omitempty"`
	}{
		Type:         e.Type.String(),
		TargetBlob:   e.TargetBlob,
		TargetOffset: e.TargetOffset,
		Confidence:   graph.ConfidenceOf(e.Confidence),
		Evidence:     e.Evidence,
		Similarity:   e.Similarity,
	})
}

// Builder accumulates a graph offline before it is written in mmap-friendly
// form. The zero value is ready to use. Edges retain insertion order within a
// node; nodes (including zero-degree nodes), blob SHAs, and names are sorted
// when saved for reproducible lookup.
type Builder struct {
	adjacency  map[Key][]Edge
	edges      uint64
	generation uint64
	corpus     map[string]struct{}
}

// NewBuilder returns an empty offline graph builder stamped with
// FirstGeneration.
func NewBuilder() *Builder {
	return &Builder{adjacency: make(map[Key][]Edge), generation: FirstGeneration}
}

// SetGeneration records the refresh generation this graph is being written in.
func (b *Builder) SetGeneration(generation uint64) {
	if b != nil {
		b.generation = generation
	}
}

// Generation reports the generation the builder will stamp on the file.
func (b *Builder) Generation() uint64 {
	if b == nil {
		return 0
	}
	return b.generation
}

// AddCorpusEntry records one identity token of the corpus this graph was built
// over. Repeats and the empty token are ignored.
func (b *Builder) AddCorpusEntry(entry string) {
	if b == nil || entry == "" {
		return
	}
	if b.corpus == nil {
		b.corpus = make(map[string]struct{})
	}
	b.corpus[entry] = struct{}{}
}

// NumCorpusEntries reports how many distinct identity tokens the roster holds.
func (b *Builder) NumCorpusEntries() int {
	if b == nil {
		return 0
	}
	return len(b.corpus)
}

// AddNode retains key even when it has no outgoing edges. It is idempotent and
// lets graph consumers distinguish an isolated definition from an absent node.
func (b *Builder) AddNode(key Key) error {
	if b == nil {
		return fmt.Errorf("diskgraph: nil builder")
	}
	if key.BlobSHA == "" {
		return fmt.Errorf("diskgraph: empty source blob SHA")
	}
	if b.adjacency == nil {
		b.adjacency = make(map[Key][]Edge)
	}
	if _, exists := b.adjacency[key]; !exists {
		b.adjacency[key] = nil
	}
	return nil
}

// Add appends one outgoing edge to the node identified by blobSHA and
// symbolOffset.
func (b *Builder) Add(blobSHA string, symbolOffset uint64, edge Edge) error {
	return b.AddEdge(Key{BlobSHA: blobSHA, SymbolOffset: symbolOffset}, edge)
}

// AddEdge appends one outgoing edge to key.
func (b *Builder) AddEdge(key Key, edge Edge) error {
	if err := b.AddNode(key); err != nil {
		return err
	}
	if err := validateEdge(edge); err != nil {
		return err
	}
	if b.edges == ^uint64(0) {
		return fmt.Errorf("diskgraph: too many edges")
	}
	b.adjacency[key] = append(b.adjacency[key], edge)
	b.edges++
	return nil
}

// AddOrUpgradeEdge adds edge unless the same relationship and evidence already
// exists at key. When it does exist, the stronger confidence tier replaces the
// weaker one in place. This is the LSP verification seam: a Proven result must
// upgrade the regex-tier CALLS edge it confirms, not leave two records for the
// same call site. It reports whether a new record was appended.
func (b *Builder) AddOrUpgradeEdge(key Key, edge Edge) (bool, error) {
	if err := b.AddNode(key); err != nil {
		return false, err
	}
	if err := validateEdge(edge); err != nil {
		return false, err
	}
	for i := range b.adjacency[key] {
		existing := &b.adjacency[key][i]
		if !sameRelationship(*existing, edge) {
			continue
		}
		if edge.Confidence > existing.Confidence {
			*existing = edge
		}
		return false, nil
	}
	if b.edges == ^uint64(0) {
		return false, fmt.Errorf("diskgraph: too many edges")
	}
	b.adjacency[key] = append(b.adjacency[key], edge)
	b.edges++
	return true, nil
}

func validateEdge(edge Edge) error {
	if edge.TargetBlob == "" {
		return fmt.Errorf("diskgraph: empty target blob SHA")
	}
	if !edge.Confidence.Valid() {
		return fmt.Errorf("diskgraph: invalid confidence tier %d", uint8(edge.Confidence))
	}
	if !edge.Evidence.Valid() {
		return fmt.Errorf("diskgraph: invalid evidence link %+v", edge.Evidence)
	}
	if err := validateSimilarity(edge); err != nil {
		return err
	}
	return nil
}

func sameRelationship(a, b Edge) bool {
	return a.Type == b.Type &&
		a.TargetBlob == b.TargetBlob &&
		a.TargetOffset == b.TargetOffset &&
		a.Evidence == b.Evidence &&
		a.Similarity == b.Similarity
}

// AddEdges appends all edges to key in order.
func (b *Builder) AddEdges(key Key, edges ...Edge) error {
	for _, edge := range edges {
		if err := b.AddEdge(key, edge); err != nil {
			return err
		}
	}
	return nil
}

// NumNodes reports how many nodes are retained, including zero-degree nodes.
func (b *Builder) NumNodes() int {
	if b == nil {
		return 0
	}
	return len(b.adjacency)
}

// NumEdges reports how many edge records will be written.
func (b *Builder) NumEdges() uint64 {
	if b == nil {
		return 0
	}
	return b.edges
}

// Save writes the builder to path.
func (b *Builder) Save(path string) error { return Save(b, path) }

// Save writes b atomically to path in the diskgraph format.
func Save(b *Builder, path string) error {
	if b == nil {
		return fmt.Errorf("diskgraph: nil builder")
	}
	keys := make([]Key, 0, len(b.adjacency))
	blobSet := make(map[string]struct{})
	nameSet := make(map[string]struct{})
	var edgeCount uint64
	for key, edges := range b.adjacency {
		if key.BlobSHA == "" {
			return fmt.Errorf("diskgraph: empty source blob SHA")
		}
		keys = append(keys, key)
		blobSet[key.BlobSHA] = struct{}{}
		for _, edge := range edges {
			if edge.TargetBlob == "" {
				return fmt.Errorf("diskgraph: empty target blob SHA")
			}
			if !edge.Confidence.Valid() {
				return fmt.Errorf("diskgraph: invalid confidence tier %d", uint8(edge.Confidence))
			}
			if !edge.Evidence.Valid() {
				return fmt.Errorf("diskgraph: invalid evidence link %+v", edge.Evidence)
			}
			if err := validateSimilarity(edge); err != nil {
				return err
			}
			blobSet[edge.TargetBlob] = struct{}{}
			blobSet[edge.Evidence.BlobSHA] = struct{}{}
			nameSet[edge.Name] = struct{}{}
		}
		if uint64(len(edges)) > ^uint64(0)-edgeCount {
			return fmt.Errorf("diskgraph: too many edges")
		}
		edgeCount += uint64(len(edges))
	}

	blobs, blobIDs, blobBytes, err := internTable(blobSet, "blob SHA")
	if err != nil {
		return err
	}
	names, nameIDs, nameBytes, err := internTable(nameSet, "edge name")
	if err != nil {
		return err
	}
	corpus, _, corpusBytes, err := internTable(b.corpus, "corpus roster entry")
	if err != nil {
		return err
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := blobIDs[keys[i].BlobSHA], blobIDs[keys[j].BlobSHA]
		if left != right {
			return left < right
		}
		return keys[i].SymbolOffset < keys[j].SymbolOffset
	})

	nodeBytes, ok := mul64(uint64(len(keys)), nodeSize)
	if !ok {
		return fmt.Errorf("diskgraph: node table is too large")
	}
	edgeBytes, ok := mul64(edgeCount, edgeSize)
	if !ok {
		return fmt.Errorf("diskgraph: edge table is too large")
	}
	blobOff := uint64(headerSize)
	nameOff, ok := add64(blobOff, blobBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file is too large")
	}
	corpusOff, ok := add64(nameOff, nameBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file is too large")
	}
	nodeOff, ok := add64(corpusOff, corpusBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file is too large")
	}
	edgeOff, ok := add64(nodeOff, nodeBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file is too large")
	}
	fileSize, ok := add64(edgeOff, edgeBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file is too large")
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)

	hdr := make([]byte, headerSize)
	copy(hdr[0:8], magic)
	binary.LittleEndian.PutUint32(hdr[8:12], formatVersion)
	binary.LittleEndian.PutUint32(hdr[12:16], headerSize)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(len(blobs)))
	binary.LittleEndian.PutUint64(hdr[24:32], uint64(len(names)))
	binary.LittleEndian.PutUint64(hdr[32:40], uint64(len(corpus)))
	binary.LittleEndian.PutUint64(hdr[40:48], uint64(len(keys)))
	binary.LittleEndian.PutUint64(hdr[48:56], edgeCount)
	binary.LittleEndian.PutUint64(hdr[56:64], blobOff)
	binary.LittleEndian.PutUint64(hdr[64:72], nameOff)
	binary.LittleEndian.PutUint64(hdr[72:80], corpusOff)
	binary.LittleEndian.PutUint64(hdr[80:88], nodeOff)
	binary.LittleEndian.PutUint64(hdr[88:96], edgeOff)
	binary.LittleEndian.PutUint64(hdr[96:104], fileSize)
	binary.LittleEndian.PutUint64(hdr[104:112], b.generation)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	for _, table := range [][]string{blobs, names, corpus} {
		if err := writeStringTable(w, table); err != nil {
			return err
		}
	}
	var scratch [edgeSize]byte
	var firstEdge uint64
	for _, key := range keys {
		clear(scratch[:nodeSize])
		binary.LittleEndian.PutUint32(scratch[0:4], blobIDs[key.BlobSHA])
		binary.LittleEndian.PutUint64(scratch[8:16], key.SymbolOffset)
		binary.LittleEndian.PutUint64(scratch[16:24], firstEdge)
		binary.LittleEndian.PutUint64(scratch[24:32], uint64(len(b.adjacency[key])))
		if _, err := w.Write(scratch[:nodeSize]); err != nil {
			return err
		}
		firstEdge += uint64(len(b.adjacency[key]))
	}
	for _, key := range keys {
		for _, edge := range b.adjacency[key] {
			clear(scratch[:])
			binary.LittleEndian.PutUint32(scratch[0:4], uint32(edge.Type))
			binary.LittleEndian.PutUint32(scratch[4:8], blobIDs[edge.TargetBlob])
			binary.LittleEndian.PutUint64(scratch[8:16], edge.TargetOffset)
			binary.LittleEndian.PutUint32(scratch[16:20], uint32(edge.Confidence))
			binary.LittleEndian.PutUint32(scratch[20:24], blobIDs[edge.Evidence.BlobSHA])
			binary.LittleEndian.PutUint64(scratch[24:32], edge.Evidence.ByteOffset)
			binary.LittleEndian.PutUint64(scratch[32:40], edge.Evidence.ByteLength)
			binary.LittleEndian.PutUint64(scratch[40:48], math.Float64bits(edge.Similarity))
			binary.LittleEndian.PutUint32(scratch[48:52], nameIDs[edge.Name])
			binary.LittleEndian.PutUint64(scratch[56:64], edge.Generation)
			if _, err := w.Write(scratch[:edgeSize]); err != nil {
				return err
			}
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// internTable sorts set into a canonical string table and returns it with its
// value->ID map and its on-disk byte size.
func internTable(set map[string]struct{}, label string) ([]string, map[string]uint32, uint64, error) {
	table := make([]string, 0, len(set))
	for s := range set {
		table = append(table, s)
	}
	sort.Strings(table)
	if uint64(len(table)) > uint64(^uint32(0)) {
		return nil, nil, 0, fmt.Errorf("diskgraph: too many %ss", label)
	}
	ids := make(map[string]uint32, len(table))
	var size uint64
	for i, s := range table {
		if uint64(len(s)) > uint64(^uint32(0)) {
			return nil, nil, 0, fmt.Errorf("diskgraph: %s is too long", label)
		}
		if uint64(len(s))+4 > ^uint64(0)-size {
			return nil, nil, 0, fmt.Errorf("diskgraph: %s table is too large", label)
		}
		ids[s] = uint32(i)
		size += 4 + uint64(len(s))
	}
	return table, ids, size, nil
}

func writeStringTable(w *bufio.Writer, table []string) error {
	var length [4]byte
	for _, s := range table {
		binary.LittleEndian.PutUint32(length[:], uint32(len(s)))
		if _, err := w.Write(length[:]); err != nil {
			return err
		}
		if _, err := w.WriteString(s); err != nil {
			return err
		}
	}
	return nil
}

// Graph is a read-only mmap-backed graph. Close must not race with lookups.
type Graph struct {
	data        []byte
	blobIDs     map[string]uint32
	blobs       []string
	names       []string
	corpusOff   int
	corpusCount int
	nodeOff     int
	edgeOff     int
	nodeCount   int
	edgeCount   int
	generation  uint64
}

// Open memory-maps path and validates its complete directory and record layout.
func Open(path string) (*Graph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.Size() < headerSize || uint64(info.Size()) > uint64(maxInt()) {
		f.Close()
		return nil, fmt.Errorf("diskgraph: invalid file size %d", info.Size())
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		syscall.Munmap(data)
		return nil, closeErr
	}
	g, err := parse(data)
	if err != nil {
		syscall.Munmap(data)
		return nil, err
	}
	return g, nil
}

// Load is an alias for Open. Both names return an mmap-backed graph; Load does
// not materialize the adjacency list on the Go heap.
func Load(path string) (*Graph, error) { return Open(path) }

func parse(data []byte) (*Graph, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("diskgraph: file too small")
	}
	if string(data[0:8]) != magic {
		return nil, fmt.Errorf("diskgraph: bad magic %q", data[0:8])
	}
	if version := binary.LittleEndian.Uint32(data[8:12]); version != formatVersion {
		return nil, fmt.Errorf("diskgraph: unsupported version %d", version)
	}
	if size := binary.LittleEndian.Uint32(data[12:16]); size != headerSize {
		return nil, fmt.Errorf("diskgraph: unsupported header size %d", size)
	}
	blobCount := binary.LittleEndian.Uint64(data[16:24])
	nameCount := binary.LittleEndian.Uint64(data[24:32])
	corpusCount := binary.LittleEndian.Uint64(data[32:40])
	nodeCount := binary.LittleEndian.Uint64(data[40:48])
	edgeCount := binary.LittleEndian.Uint64(data[48:56])
	blobOff := binary.LittleEndian.Uint64(data[56:64])
	nameOff := binary.LittleEndian.Uint64(data[64:72])
	corpusOff := binary.LittleEndian.Uint64(data[72:80])
	nodeOff := binary.LittleEndian.Uint64(data[80:88])
	edgeOff := binary.LittleEndian.Uint64(data[88:96])
	fileSize := binary.LittleEndian.Uint64(data[96:104])
	generation := binary.LittleEndian.Uint64(data[104:112])
	if fileSize != uint64(len(data)) || blobOff != headerSize ||
		blobOff > nameOff || nameOff > corpusOff || corpusOff > nodeOff || nodeOff > edgeOff || edgeOff > fileSize {
		return nil, fmt.Errorf("diskgraph: corrupt section offsets")
	}
	nodeBytes := edgeOff - nodeOff
	edgeBytes := fileSize - edgeOff
	if nodeBytes%nodeSize != 0 || nodeBytes/nodeSize != nodeCount {
		return nil, fmt.Errorf("diskgraph: corrupt node section")
	}
	if edgeBytes%edgeSize != 0 || edgeBytes/edgeSize != edgeCount {
		return nil, fmt.Errorf("diskgraph: corrupt edge section")
	}
	if blobCount > uint64(^uint32(0)) || nameCount > uint64(^uint32(0)) ||
		blobCount > uint64(maxInt()) || nameCount > uint64(maxInt()) || corpusCount > uint64(maxInt()) ||
		nodeCount > uint64(maxInt()) || edgeCount > uint64(maxInt()) {
		return nil, fmt.Errorf("diskgraph: record count exceeds platform limits")
	}

	g := &Graph{
		data:        data,
		blobIDs:     make(map[string]uint32, int(blobCount)),
		blobs:       make([]string, 0, int(blobCount)),
		names:       make([]string, 0, int(nameCount)),
		corpusOff:   int(corpusOff),
		corpusCount: int(corpusCount),
		nodeOff:     int(nodeOff),
		edgeOff:     int(edgeOff),
		nodeCount:   int(nodeCount),
		edgeCount:   int(edgeCount),
		generation:  generation,
	}

	// Parse blob table.
	pos := int(blobOff)
	var previous []byte
	for i := 0; i < int(blobCount); i++ {
		entry, next, err := readTableEntry(data, pos, int(nameOff), "blob")
		if err != nil {
			return nil, err
		}
		pos = next
		if len(entry) == 0 {
			return nil, fmt.Errorf("diskgraph: empty blob SHA in table")
		}
		if i > 0 && bytes.Compare(previous, entry) >= 0 {
			return nil, fmt.Errorf("diskgraph: unsorted blob table")
		}
		previous = entry
		g.blobs = append(g.blobs, string(entry))
		g.blobIDs[g.blobs[i]] = uint32(i)
	}
	if pos != int(nameOff) {
		return nil, fmt.Errorf("diskgraph: blob table length mismatch")
	}

	// Parse name table.
	previous = nil
	for i := 0; i < int(nameCount); i++ {
		name, next, err := readTableEntry(data, pos, int(corpusOff), "name")
		if err != nil {
			return nil, err
		}
		pos = next
		if i > 0 && bytes.Compare(previous, name) >= 0 {
			return nil, fmt.Errorf("diskgraph: unsorted name table")
		}
		previous = name
		g.names = append(g.names, string(name))
	}
	if pos != int(corpusOff) {
		return nil, fmt.Errorf("diskgraph: name table length mismatch")
	}

	// Validate corpus roster in place (it stays in the mmap — serving never needs it).
	previous = nil
	for i := 0; i < int(corpusCount); i++ {
		entry, next, err := readTableEntry(data, pos, int(nodeOff), "corpus roster")
		if err != nil {
			return nil, err
		}
		pos = next
		if len(entry) == 0 {
			return nil, fmt.Errorf("diskgraph: empty corpus roster entry")
		}
		if i > 0 && bytes.Compare(previous, entry) >= 0 {
			return nil, fmt.Errorf("diskgraph: unsorted corpus roster")
		}
		previous = entry
	}
	if pos != int(nodeOff) {
		return nil, fmt.Errorf("diskgraph: corpus roster length mismatch")
	}

	// Validate node table.
	var previousBlob uint32
	var previousOffset uint64
	var nextEdge uint64
	for i := 0; i < g.nodeCount; i++ {
		record := data[g.nodeOff+i*nodeSize : g.nodeOff+(i+1)*nodeSize]
		blobID := binary.LittleEndian.Uint32(record[0:4])
		if blobID >= uint32(len(g.blobs)) || binary.LittleEndian.Uint32(record[4:8]) != 0 {
			return nil, fmt.Errorf("diskgraph: corrupt node %d", i)
		}
		offset := binary.LittleEndian.Uint64(record[8:16])
		first := binary.LittleEndian.Uint64(record[16:24])
		count := binary.LittleEndian.Uint64(record[24:32])
		if i > 0 && (blobID < previousBlob || (blobID == previousBlob && offset <= previousOffset)) {
			return nil, fmt.Errorf("diskgraph: unsorted node table")
		}
		if first != nextEdge || count > edgeCount-first {
			return nil, fmt.Errorf("diskgraph: corrupt adjacency range for node %d", i)
		}
		nextEdge = first + count
		previousBlob, previousOffset = blobID, offset
	}
	if nextEdge != edgeCount {
		return nil, fmt.Errorf("diskgraph: unclaimed edge records")
	}

	// Validate edge records.
	for i := 0; i < g.edgeCount; i++ {
		record := data[g.edgeOff+i*edgeSize : g.edgeOff+(i+1)*edgeSize]
		if binary.LittleEndian.Uint32(record[4:8]) >= uint32(len(g.blobs)) {
			return nil, fmt.Errorf("diskgraph: edge %d has invalid target blob", i)
		}
		rawTier := binary.LittleEndian.Uint32(record[16:20])
		if rawTier > uint32(graph.Proven) {
			return nil, fmt.Errorf("diskgraph: edge %d has invalid confidence tier %d", i, rawTier)
		}
		if binary.LittleEndian.Uint32(record[20:24]) >= uint32(len(g.blobs)) {
			return nil, fmt.Errorf("diskgraph: edge %d has invalid evidence blob", i)
		}
		evidence := graph.Evidence{
			BlobSHA:    g.blobs[binary.LittleEndian.Uint32(record[20:24])],
			ByteOffset: binary.LittleEndian.Uint64(record[24:32]),
			ByteLength: binary.LittleEndian.Uint64(record[32:40]),
		}
		if !evidence.Valid() {
			return nil, fmt.Errorf("diskgraph: edge %d has invalid evidence span", i)
		}
		if len(g.names) > 0 && binary.LittleEndian.Uint32(record[48:52]) >= uint32(len(g.names)) {
			return nil, fmt.Errorf("diskgraph: edge %d has invalid name", i)
		}
		if binary.LittleEndian.Uint32(record[52:56]) != 0 {
			return nil, fmt.Errorf("diskgraph: edge %d has a non-zero reserved field", i)
		}
		typeID := EdgeType(binary.LittleEndian.Uint32(record[0:4]))
		similarity := math.Float64frombits(binary.LittleEndian.Uint64(record[40:48]))
		if err := validateSimilarity(Edge{Type: typeID, Similarity: similarity}); err != nil {
			return nil, fmt.Errorf("diskgraph: edge %d: %w", i, err)
		}
	}
	return g, nil
}

// readTableEntry decodes one length-prefixed string-table entry starting at pos
// and returns it (aliasing data) plus the offset just past it.
func readTableEntry(data []byte, pos, limit int, label string) ([]byte, int, error) {
	if pos+4 > limit {
		return nil, 0, fmt.Errorf("diskgraph: truncated %s table", label)
	}
	n := uint64(binary.LittleEndian.Uint32(data[pos : pos+4]))
	pos += 4
	if n > uint64(limit-pos) {
		return nil, 0, fmt.Errorf("diskgraph: truncated %s table entry", label)
	}
	return data[pos : pos+int(n)], pos + int(n), nil
}

// Load returns the outgoing edges for (blobSHA, symbolOffset), or nil when the
// graph has no such node. The returned slice owns its strings and survives Close.
func (g *Graph) Load(blobSHA string, symbolOffset uint64) []Edge {
	return g.Edges(Key{BlobSHA: blobSHA, SymbolOffset: symbolOffset})
}

// Edges returns the outgoing edges for key, preserving their build order.
func (g *Graph) Edges(key Key) []Edge {
	if g == nil || g.data == nil {
		return nil
	}
	blobID, ok := g.blobIDs[key.BlobSHA]
	if !ok {
		return nil
	}
	i := sort.Search(g.nodeCount, func(i int) bool {
		record := g.data[g.nodeOff+i*nodeSize : g.nodeOff+(i+1)*nodeSize]
		recordBlob := binary.LittleEndian.Uint32(record[0:4])
		recordOffset := binary.LittleEndian.Uint64(record[8:16])
		return recordBlob > blobID || (recordBlob == blobID && recordOffset >= key.SymbolOffset)
	})
	if i == g.nodeCount {
		return nil
	}
	record := g.data[g.nodeOff+i*nodeSize : g.nodeOff+(i+1)*nodeSize]
	if binary.LittleEndian.Uint32(record[0:4]) != blobID || binary.LittleEndian.Uint64(record[8:16]) != key.SymbolOffset {
		return nil
	}
	first := binary.LittleEndian.Uint64(record[16:24])
	count := binary.LittleEndian.Uint64(record[24:32])
	out := make([]Edge, int(count))
	for j := range out {
		out[j] = g.decodeEdge(int(first) + j)
	}
	return out
}

// decodeEdge reads one fixed-width edge record by index. Blob SHAs and names
// come from tables interned at Open, so the returned Edge allocates nothing.
func (g *Graph) decodeEdge(i int) Edge {
	record := g.data[g.edgeOff+i*edgeSize : g.edgeOff+(i+1)*edgeSize]
	var name string
	if nameID := binary.LittleEndian.Uint32(record[48:52]); int(nameID) < len(g.names) {
		name = g.names[nameID]
	}
	return Edge{
		Type:         EdgeType(binary.LittleEndian.Uint32(record[0:4])),
		TargetBlob:   g.blobs[binary.LittleEndian.Uint32(record[4:8])],
		TargetOffset: binary.LittleEndian.Uint64(record[8:16]),
		Confidence:   graph.ConfidenceTier(binary.LittleEndian.Uint32(record[16:20])),
		Evidence: graph.Evidence{
			BlobSHA:    g.blobs[binary.LittleEndian.Uint32(record[20:24])],
			ByteOffset: binary.LittleEndian.Uint64(record[24:32]),
			ByteLength: binary.LittleEndian.Uint64(record[32:40]),
		},
		Similarity: math.Float64frombits(binary.LittleEndian.Uint64(record[40:48])),
		Name:       name,
		Generation: binary.LittleEndian.Uint64(record[56:64]),
	}
}

// Generation reports the refresh generation this graph file was written in.
func (g *Graph) Generation() uint64 {
	if g == nil {
		return 0
	}
	return g.generation
}

// Names returns the distinct edge names interned in the file, sorted.
func (g *Graph) Names() []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.names...)
}

// NumCorpusEntries reports how many identity tokens the corpus roster holds.
func (g *Graph) NumCorpusEntries() int {
	if g == nil {
		return 0
	}
	return g.corpusCount
}

// EachCorpusEntry calls fn for every roster token in sorted order, stopping
// early if fn returns false.
func (g *Graph) EachCorpusEntry(fn func(entry string) bool) {
	if g == nil || g.data == nil {
		return
	}
	pos := g.corpusOff
	for i := 0; i < g.corpusCount; i++ {
		n := int(binary.LittleEndian.Uint32(g.data[pos : pos+4]))
		pos += 4
		entry := string(g.data[pos : pos+n])
		pos += n
		if !fn(entry) {
			return
		}
	}
}

// CorpusEntrySet folds the roster onto the Go heap. The offline refresh's one
// materialization of the largest table in the file; serving never calls it.
func (g *Graph) CorpusEntrySet() map[string]struct{} {
	if g == nil {
		return nil
	}
	out := make(map[string]struct{}, g.corpusCount)
	g.EachCorpusEntry(func(entry string) bool {
		out[entry] = struct{}{}
		return true
	})
	return out
}

// NodeAt returns the i'th node's key and the half-open range of edge records it
// owns, or ok=false for an out-of-range index.
func (g *Graph) NodeAt(i int) (key Key, firstEdge, count int, ok bool) {
	if g == nil || g.data == nil || i < 0 || i >= g.nodeCount {
		return Key{}, 0, 0, false
	}
	record := g.data[g.nodeOff+i*nodeSize : g.nodeOff+(i+1)*nodeSize]
	return Key{
		BlobSHA:      g.blobs[binary.LittleEndian.Uint32(record[0:4])],
		SymbolOffset: binary.LittleEndian.Uint64(record[8:16]),
	}, int(binary.LittleEndian.Uint64(record[16:24])), int(binary.LittleEndian.Uint64(record[24:32])), true
}

// EdgeAt decodes the i'th edge record, or ok=false for an out-of-range index.
func (g *Graph) EdgeAt(i int) (Edge, bool) {
	if g == nil || g.data == nil || i < 0 || i >= g.edgeCount {
		return Edge{}, false
	}
	return g.decodeEdge(i), true
}

// EdgeName returns just the name of the i'th edge record without decoding the
// rest of it. An out-of-range index returns "".
func (g *Graph) EdgeName(i int) string {
	if g == nil || g.data == nil || i < 0 || i >= g.edgeCount {
		return ""
	}
	record := g.data[g.edgeOff+i*edgeSize : g.edgeOff+(i+1)*edgeSize]
	if nameID := binary.LittleEndian.Uint32(record[48:52]); int(nameID) < len(g.names) {
		return g.names[nameID]
	}
	return ""
}

// EachEdge visits every persisted edge in on-disk order, passing the source node
// it belongs to. Returning false stops the walk.
func (g *Graph) EachEdge(fn func(source Key, edge Edge) bool) {
	if g == nil || g.data == nil || fn == nil {
		return
	}
	for i := 0; i < g.nodeCount; i++ {
		key, first, count, ok := g.NodeAt(i)
		if !ok {
			return
		}
		for j := 0; j < count; j++ {
			if !fn(key, g.decodeEdge(first+j)) {
				return
			}
		}
	}
}

// Keys returns all source nodes in on-disk order.
func (g *Graph) Keys() []Key {
	if g == nil || g.data == nil || g.nodeCount == 0 {
		return nil
	}
	out := make([]Key, g.nodeCount)
	for i := range out {
		record := g.data[g.nodeOff+i*nodeSize : g.nodeOff+(i+1)*nodeSize]
		out[i] = Key{
			BlobSHA:      g.blobs[binary.LittleEndian.Uint32(record[0:4])],
			SymbolOffset: binary.LittleEndian.Uint64(record[8:16]),
		}
	}
	return out
}

// NumNodes reports the number of stored nodes, including zero-degree nodes.
func (g *Graph) NumNodes() int {
	if g == nil {
		return 0
	}
	return g.nodeCount
}

// NumEdges reports the total number of persisted edges.
func (g *Graph) NumEdges() int {
	if g == nil {
		return 0
	}
	return g.edgeCount
}

// Close releases the mmap. It is idempotent; the Graph must not be queried
// concurrently with Close.
func (g *Graph) Close() error {
	if g == nil || g.data == nil {
		return nil
	}
	err := syscall.Munmap(g.data)
	g.data = nil
	return err
}

func add64(a, b uint64) (uint64, bool) {
	if b > ^uint64(0)-a {
		return 0, false
	}
	return a + b, true
}

func mul64(a, b uint64) (uint64, bool) {
	if a != 0 && b > ^uint64(0)/a {
		return 0, false
	}
	return a * b, true
}

func maxInt() int { return int(^uint(0) >> 1) }
