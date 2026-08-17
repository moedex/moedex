// Package diskgraph persists the graph layer's content-addressed adjacency list.
//
// A node is keyed by (git blob SHA, symbol byte offset). Each node points at a
// contiguous range of fixed-width edge records, so Open can mmap the file and a
// lookup only binary-searches the node table and decodes the requested range.
// Blob SHAs are interned once in a variable-width table; node and edge records
// refer to them by uint32 ID.
package diskgraph

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

const (
	magic         = "MDXGRF01"
	formatVersion = 1
	headerSize    = 80
	nodeSize      = 32
	edgeSize      = 32
)

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
)

// Key is a graph node: a definition (or, when no enclosing definition exists,
// an evidence occurrence) within content identified by its git blob SHA.
type Key struct {
	BlobSHA      string
	SymbolOffset uint64
}

// Node is an alias retained for callers that prefer graph terminology.
type Node = Key

// Edge is one outgoing adjacency record. TargetBlob is a git blob SHA.
// EvidenceOffset points at the source-side byte occurrence that justified the
// relationship; it may differ from the source node's enclosing symbol offset.
type Edge struct {
	Type           EdgeType
	TargetBlob     string
	TargetOffset   uint64
	Confidence     float64
	EvidenceOffset uint64
}

// Builder accumulates a graph offline before it is written in mmap-friendly
// form. The zero value is ready to use. Edges retain insertion order within a
// node; nodes and blob SHAs are sorted when saved for reproducible lookup.
type Builder struct {
	adjacency map[Key][]Edge
	edges     uint64
}

// NewBuilder returns an empty offline graph builder.
func NewBuilder() *Builder { return &Builder{adjacency: make(map[Key][]Edge)} }

// Add appends one outgoing edge to the node identified by blobSHA and
// symbolOffset.
func (b *Builder) Add(blobSHA string, symbolOffset uint64, edge Edge) error {
	return b.AddEdge(Key{BlobSHA: blobSHA, SymbolOffset: symbolOffset}, edge)
}

// AddEdge appends one outgoing edge to key.
func (b *Builder) AddEdge(key Key, edge Edge) error {
	if b == nil {
		return fmt.Errorf("diskgraph: nil builder")
	}
	if key.BlobSHA == "" {
		return fmt.Errorf("diskgraph: empty source blob SHA")
	}
	if edge.TargetBlob == "" {
		return fmt.Errorf("diskgraph: empty target blob SHA")
	}
	if b.edges == ^uint64(0) {
		return fmt.Errorf("diskgraph: too many edges")
	}
	if b.adjacency == nil {
		b.adjacency = make(map[Key][]Edge)
	}
	b.adjacency[key] = append(b.adjacency[key], edge)
	b.edges++
	return nil
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

// NumNodes reports how many source nodes have outgoing edges.
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
			blobSet[edge.TargetBlob] = struct{}{}
		}
		if uint64(len(edges)) > ^uint64(0)-edgeCount {
			return fmt.Errorf("diskgraph: too many edges")
		}
		edgeCount += uint64(len(edges))
	}

	blobs := make([]string, 0, len(blobSet))
	for sha := range blobSet {
		blobs = append(blobs, sha)
	}
	sort.Strings(blobs)
	if uint64(len(blobs)) > uint64(^uint32(0)) {
		return fmt.Errorf("diskgraph: too many blob SHAs")
	}
	blobIDs := make(map[string]uint32, len(blobs))
	var blobBytes uint64
	for i, sha := range blobs {
		if uint64(len(sha)) > uint64(^uint32(0)) {
			return fmt.Errorf("diskgraph: blob SHA is too long")
		}
		if uint64(len(sha))+4 > ^uint64(0)-blobBytes {
			return fmt.Errorf("diskgraph: blob table is too large")
		}
		blobIDs[sha] = uint32(i)
		blobBytes += 4 + uint64(len(sha))
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
	nodeOff, ok := add64(blobOff, blobBytes)
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
	binary.LittleEndian.PutUint64(hdr[24:32], uint64(len(keys)))
	binary.LittleEndian.PutUint64(hdr[32:40], edgeCount)
	binary.LittleEndian.PutUint64(hdr[40:48], blobOff)
	binary.LittleEndian.PutUint64(hdr[48:56], nodeOff)
	binary.LittleEndian.PutUint64(hdr[56:64], edgeOff)
	binary.LittleEndian.PutUint64(hdr[64:72], fileSize)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	var scratch [32]byte
	for _, sha := range blobs {
		binary.LittleEndian.PutUint32(scratch[:4], uint32(len(sha)))
		if _, err := w.Write(scratch[:4]); err != nil {
			return err
		}
		if _, err := w.WriteString(sha); err != nil {
			return err
		}
	}
	var firstEdge uint64
	for _, key := range keys {
		clear(scratch[:])
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
			binary.LittleEndian.PutUint32(scratch[0:4], uint32(edge.Type))
			binary.LittleEndian.PutUint32(scratch[4:8], blobIDs[edge.TargetBlob])
			binary.LittleEndian.PutUint64(scratch[8:16], edge.TargetOffset)
			binary.LittleEndian.PutUint64(scratch[16:24], math.Float64bits(edge.Confidence))
			binary.LittleEndian.PutUint64(scratch[24:32], edge.EvidenceOffset)
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

// Graph is a read-only mmap-backed graph. Close must not race with lookups.
type Graph struct {
	data      []byte
	blobIDs   map[string]uint32
	blobs     []string
	nodeOff   int
	edgeOff   int
	nodeCount int
	edgeCount int
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
	nodeCount := binary.LittleEndian.Uint64(data[24:32])
	edgeCount := binary.LittleEndian.Uint64(data[32:40])
	blobOff := binary.LittleEndian.Uint64(data[40:48])
	nodeOff := binary.LittleEndian.Uint64(data[48:56])
	edgeOff := binary.LittleEndian.Uint64(data[56:64])
	fileSize := binary.LittleEndian.Uint64(data[64:72])
	if fileSize != uint64(len(data)) || blobOff != headerSize || blobOff > nodeOff || nodeOff > edgeOff || edgeOff > fileSize {
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
	if blobCount > uint64(^uint32(0)) || blobCount > uint64(maxInt()) || nodeCount > uint64(maxInt()) || edgeCount > uint64(maxInt()) {
		return nil, fmt.Errorf("diskgraph: record count exceeds platform limits")
	}

	g := &Graph{
		data:      data,
		blobIDs:   make(map[string]uint32, int(blobCount)),
		blobs:     make([]string, 0, int(blobCount)),
		nodeOff:   int(nodeOff),
		edgeOff:   int(edgeOff),
		nodeCount: int(nodeCount),
		edgeCount: int(edgeCount),
	}
	pos := int(blobOff)
	for i := 0; i < int(blobCount); i++ {
		if pos+4 > int(nodeOff) {
			return nil, fmt.Errorf("diskgraph: truncated blob table")
		}
		n := uint64(binary.LittleEndian.Uint32(data[pos : pos+4]))
		pos += 4
		if n > uint64(int(nodeOff)-pos) {
			return nil, fmt.Errorf("diskgraph: truncated blob SHA")
		}
		sha := string(data[pos : pos+int(n)])
		pos += int(n)
		if sha == "" {
			return nil, fmt.Errorf("diskgraph: empty blob SHA in table")
		}
		if _, duplicate := g.blobIDs[sha]; duplicate {
			return nil, fmt.Errorf("diskgraph: duplicate blob SHA %q", sha)
		}
		g.blobIDs[sha] = uint32(i)
		g.blobs = append(g.blobs, sha)
	}
	if pos != int(nodeOff) {
		return nil, fmt.Errorf("diskgraph: blob table length mismatch")
	}

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
	for i := 0; i < g.edgeCount; i++ {
		record := data[g.edgeOff+i*edgeSize : g.edgeOff+(i+1)*edgeSize]
		if binary.LittleEndian.Uint32(record[4:8]) >= uint32(len(g.blobs)) {
			return nil, fmt.Errorf("diskgraph: edge %d has invalid target blob", i)
		}
	}
	return g, nil
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
		start := g.edgeOff + int(first+uint64(j))*edgeSize
		edgeRecord := g.data[start : start+edgeSize]
		targetID := binary.LittleEndian.Uint32(edgeRecord[4:8])
		out[j] = Edge{
			Type:           EdgeType(binary.LittleEndian.Uint32(edgeRecord[0:4])),
			TargetBlob:     g.blobs[targetID],
			TargetOffset:   binary.LittleEndian.Uint64(edgeRecord[8:16]),
			Confidence:     math.Float64frombits(binary.LittleEndian.Uint64(edgeRecord[16:24])),
			EvidenceOffset: binary.LittleEndian.Uint64(edgeRecord[24:32]),
		}
	}
	return out
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

// NumNodes reports the number of source nodes with outgoing adjacency.
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
