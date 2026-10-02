package diskgraph

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// TargetSetID identifies an immutable, ordered set of unique target keys.
// IDs are local to one builder or graph and must be remapped when carrying
// records into another builder.
type TargetSetID uint64

type factoredRef struct {
	targets    TargetSetID
	exclude    int
	moveTarget int
	moveAfter  int
}

// FactoredSource is source evidence shared by all targets in Targets, except
// Exclude (-1 means no exclusion). Prototype contains no target fields.
type FactoredSource struct {
	Source     Key
	Prototype  Edge
	Targets    TargetSetID
	Exclude    int
	MoveTarget int
	MoveAfter  int
}

// PhysicalRecord preserves insertion order across explicit and factored edges.
// A factored record's Edge is its target-free prototype; a normal record's Edge
// is complete. Logical iteration expands a factored record in target-set order.
type PhysicalRecord struct {
	Source     Key
	Edge       Edge
	Factored   bool
	Targets    TargetSetID
	Exclude    int
	MoveTarget int
	MoveAfter  int
}

// AddTargetSet interns a nonempty ordered set. Duplicate targets are rejected;
// callers perform any source/shard-aware deduplication before constructing it.
func (b *Builder) AddTargetSet(targets []Key) (TargetSetID, error) {
	if b == nil {
		return 0, fmt.Errorf("diskgraph: nil builder")
	}
	if len(targets) == 0 {
		return 0, fmt.Errorf("diskgraph: empty target set")
	}
	seen := make(map[Key]struct{}, len(targets))
	h := sha256.New()
	var scratch [8]byte
	for _, target := range targets {
		if target.BlobSHA == "" {
			return 0, fmt.Errorf("diskgraph: empty target blob SHA")
		}
		if _, ok := seen[target]; ok {
			return 0, fmt.Errorf("diskgraph: duplicate target in set")
		}
		seen[target] = struct{}{}
		binary.LittleEndian.PutUint64(scratch[:], uint64(len(target.BlobSHA)))
		h.Write(scratch[:])
		h.Write([]byte(target.BlobSHA))
		binary.LittleEndian.PutUint64(scratch[:], target.SymbolOffset)
		h.Write(scratch[:])
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	if id, ok := b.targetSetIDs[digest]; ok {
		existing := b.targetSets[id]
		if len(existing) != len(targets) {
			return 0, fmt.Errorf("diskgraph: target-set digest collision")
		}
		for i := range targets {
			if existing[i] != targets[i] {
				return 0, fmt.Errorf("diskgraph: target-set digest collision")
			}
		}
		return id, nil
	}
	if b.targetSetIDs == nil {
		b.targetSetIDs = make(map[[32]byte]TargetSetID)
	}
	id := TargetSetID(len(b.targetSets))
	b.targetSets = append(b.targetSets, append([]Key(nil), targets...))
	b.targetSetIDs[digest] = id
	return id, nil
}

// AddFactoredSource appends a source record without expanding its targets. Like
// AddEdge it preserves duplicates and insertion order; graph construction owns
// relationship deduplication. Exclude is -1 or an index in the target set.
func (b *Builder) AddFactoredSource(source Key, prototype Edge, setID TargetSetID, exclude int) error {
	return b.AddFactoredSourceOrdered(source, prototype, setID, exclude, -1, -1)
}

// AddFactoredSourceOrdered additionally moves one canonical target forward,
// after MoveAfter's canonical slot. Both move fields are -1 when unused. The
// moved target cannot be excluded; MoveAfter may be an excluded slot because it
// still describes an unambiguous ordering boundary.
func (b *Builder) AddFactoredSourceOrdered(source Key, prototype Edge, setID TargetSetID, exclude, moveTarget, moveAfter int) error {
	if b == nil {
		return fmt.Errorf("diskgraph: nil builder")
	}
	if uint64(setID) >= uint64(len(b.targetSets)) {
		return fmt.Errorf("diskgraph: invalid target set %d", setID)
	}
	targets := b.targetSets[setID]
	if exclude < -1 || exclude >= len(targets) {
		return fmt.Errorf("diskgraph: invalid target exclusion %d", exclude)
	}
	ref := factoredRef{targets: setID, exclude: exclude, moveTarget: moveTarget, moveAfter: moveAfter}
	if !validMove(ref, len(targets)) {
		return fmt.Errorf("diskgraph: invalid target move")
	}
	count := len(targets)
	if exclude >= 0 {
		count--
	}
	// Even an empty logical record must validate the caller's prototype.
	first := 0
	if count > 0 {
		first = orderedTargetIndex(ref, 0)
	}
	prototype.TargetBlob = targets[first].BlobSHA
	prototype.TargetOffset = targets[first].SymbolOffset
	if err := validateEdge(prototype); err != nil {
		return err
	}
	if err := b.AddNode(source); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if uint64(count) > ^uint64(0)-b.NumEdges() {
		return fmt.Errorf("diskgraph: too many logical edges")
	}
	at := len(b.adjacency[source])
	if err := b.AddEdge(source, prototype); err != nil {
		return err
	}
	if b.factored == nil {
		b.factored = make(map[Key]map[int]factoredRef)
	}
	if b.factored[source] == nil {
		b.factored[source] = make(map[int]factoredRef)
	}
	b.factored[source][at] = ref
	b.factoredExtra += uint64(count - 1)
	return nil
}

func (b *Builder) hasFactored() bool {
	for _, refs := range b.factored {
		if len(refs) > 0 {
			return true
		}
	}
	return false
}

// upgradeFactored splits a shared record around one upgraded relationship.
// The other targets remain factored; no Cartesian expansion is needed. This
// preserves AddOrUpgradeEdge's first-match and exact insertion-order contract.
func (b *Builder) upgradeFactored(source Key, at int, ref factoredRef, edge Edge) (bool, error) {
	old := b.adjacency[source][at]
	probe := old
	probe.TargetBlob = edge.TargetBlob
	probe.TargetOffset = edge.TargetOffset
	if !sameRelationship(probe, edge) {
		return false, nil
	}
	targets := b.targetSets[ref.targets]
	match := -1
	count := len(targets)
	if ref.exclude >= 0 {
		count--
	}
	for rank := 0; rank < count; rank++ {
		i := orderedTargetIndex(ref, rank)
		target := targets[i]
		if target.BlobSHA == edge.TargetBlob && target.SymbolOffset == edge.TargetOffset {
			match = rank
			break
		}
	}
	if match < 0 {
		return false, nil
	}
	if edge.Confidence <= old.Confidence {
		return true, nil
	}
	type part struct {
		edge Edge
		ref  *factoredRef
	}
	parts := make([]part, 0, 3)
	addPart := func(start, end int) error {
		var subset []Key
		for rank := start; rank < end; rank++ {
			subset = append(subset, targets[orderedTargetIndex(ref, rank)])
		}
		if len(subset) == 0 {
			return nil
		}
		id, err := b.AddTargetSet(subset)
		if err != nil {
			return err
		}
		prototype := old
		prototype.TargetBlob = subset[0].BlobSHA
		prototype.TargetOffset = subset[0].SymbolOffset
		parts = append(parts, part{edge: prototype, ref: &factoredRef{targets: id, exclude: -1, moveTarget: -1, moveAfter: -1}})
		return nil
	}
	if err := addPart(0, match); err != nil {
		return false, err
	}
	parts = append(parts, part{edge: edge})
	if err := addPart(match+1, count); err != nil {
		return false, err
	}
	delta := len(parts) - 1
	oldRefs := b.factored[source]
	nextRefs := make(map[int]factoredRef, len(oldRefs)+1)
	for index, value := range oldRefs {
		if index < at {
			nextRefs[index] = value
		} else if index > at {
			nextRefs[index+delta] = value
		}
	}
	oldEdges := b.adjacency[source]
	nextEdges := make([]Edge, 0, len(oldEdges)+delta)
	nextEdges = append(nextEdges, oldEdges[:at]...)
	for i, part := range parts {
		nextEdges = append(nextEdges, part.edge)
		if part.ref != nil {
			nextRefs[at+i] = *part.ref
		}
	}
	nextEdges = append(nextEdges, oldEdges[at+1:]...)
	b.adjacency[source] = nextEdges
	b.factored[source] = nextRefs
	b.edges += uint64(delta)
	b.factoredExtra -= uint64(delta)
	return true, nil
}

const (
	compactVersion    = 4
	compactHeaderSize = 112
	targetSetSize     = 16
	targetSize        = 16
	groupSize         = 40
)

type compactGraph struct {
	data                        []byte
	setOff, targetOff, groupOff int
	sets, targets, groups       int
	logicalEdges                int
	// One scalar per PHYSICAL factored source, never per logical target pair.
	logicalStarts []int
}

// saveFactored wraps the existing physical-record format in an atomic v4 file.
// A physical graph record contains the first surviving target as a validation
// witness. Its group reference supplies the complete ordered target set.
func saveFactored(b *Builder, path string) error {
	dir := filepath.Dir(path)
	baseFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".base-*")
	if err != nil {
		return err
	}
	basePath := baseFile.Name()
	baseFile.Close()
	defer os.Remove(basePath)
	if err := saveLegacy(b, basePath); err != nil {
		return err
	}
	base, err := Open(basePath)
	if err != nil {
		return err
	}
	defer base.Close()
	var groups uint64
	for _, refs := range b.factored {
		groups += uint64(len(refs))
	}
	var targets uint64
	for _, set := range b.targetSets {
		var ok bool
		targets, ok = add64(targets, uint64(len(set)))
		if !ok {
			return fmt.Errorf("diskgraph: target table too large")
		}
	}
	baseOff := uint64(compactHeaderSize)
	setOff, ok := add64(baseOff, uint64(len(base.data)))
	if !ok {
		return fmt.Errorf("diskgraph: file too large")
	}
	setBytes, ok := mul64(uint64(len(b.targetSets)), targetSetSize)
	if !ok {
		return fmt.Errorf("diskgraph: target directory too large")
	}
	targetOff, ok := add64(setOff, setBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file too large")
	}
	targetBytes, ok := mul64(targets, targetSize)
	if !ok {
		return fmt.Errorf("diskgraph: targets too large")
	}
	groupOff, ok := add64(targetOff, targetBytes)
	if !ok {
		return fmt.Errorf("diskgraph: file too large")
	}
	groupBytes, ok := mul64(groups, groupSize)
	if !ok {
		return fmt.Errorf("diskgraph: groups too large")
	}
	fileSize, ok := add64(groupOff, groupBytes)
	if !ok || fileSize > uint64(maxInt()) || b.NumEdges() > uint64(maxInt()) {
		return fmt.Errorf("diskgraph: compact graph too large")
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0644); err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	var hdr [compactHeaderSize]byte
	copy(hdr[:8], magic)
	binary.LittleEndian.PutUint32(hdr[8:12], compactVersion)
	binary.LittleEndian.PutUint32(hdr[12:16], compactHeaderSize)
	values := []uint64{fileSize, baseOff, uint64(len(base.data)), setOff, uint64(len(b.targetSets)), targetOff, targets, groupOff, groups, b.NumEdges()}
	for i, value := range values {
		binary.LittleEndian.PutUint64(hdr[16+i*8:24+i*8], value)
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(base.data); err != nil {
		return err
	}
	var record [groupSize]byte
	var first uint64
	for _, set := range b.targetSets {
		clear(record[:])
		binary.LittleEndian.PutUint64(record[:8], first)
		binary.LittleEndian.PutUint64(record[8:16], uint64(len(set)))
		if _, err := w.Write(record[:targetSetSize]); err != nil {
			return err
		}
		first += uint64(len(set))
	}
	for _, set := range b.targetSets {
		for _, key := range set {
			clear(record[:])
			binary.LittleEndian.PutUint32(record[:4], base.blobIDs[key.BlobSHA])
			binary.LittleEndian.PutUint64(record[8:16], key.SymbolOffset)
			if _, err := w.Write(record[:targetSize]); err != nil {
				return err
			}
		}
	}
	var written uint64
	for node := 0; node < base.nodeCount; node++ {
		key, start, count, _ := base.physicalNodeAt(node)
		refs := b.factored[key]
		for i := 0; i < count; i++ {
			if ref, ok := refs[i]; ok {
				clear(record[:])
				binary.LittleEndian.PutUint64(record[:8], uint64(start+i))
				binary.LittleEndian.PutUint64(record[8:16], uint64(ref.targets))
				binary.LittleEndian.PutUint64(record[16:24], uint64(ref.exclude))
				binary.LittleEndian.PutUint64(record[24:32], uint64(ref.moveTarget))
				binary.LittleEndian.PutUint64(record[32:40], uint64(ref.moveAfter))
				if _, err := w.Write(record[:]); err != nil {
					return err
				}
				written++
			}
		}
	}
	if written != groups {
		return fmt.Errorf("diskgraph: invalid factored record directory")
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	committed = true
	return nil
}

func parseFactored(data []byte) (*Graph, error) {
	fail := func(message string) (*Graph, error) {
		return nil, fmt.Errorf("diskgraph: invalid compact graph: %s", message)
	}
	if len(data) < compactHeaderSize || string(data[:8]) != magic || binary.LittleEndian.Uint32(data[12:16]) != compactHeaderSize {
		return fail("header")
	}
	values := make([]uint64, 10)
	for i := range values {
		values[i] = binary.LittleEndian.Uint64(data[16+i*8 : 24+i*8])
		if values[i] > uint64(maxInt()) {
			return fail("integer overflow")
		}
	}
	size, baseOff, baseLen, setOff, sets, targetOff, targets, groupOff, groups, logical := values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9]
	for _, v := range data[96:compactHeaderSize] {
		if v != 0 {
			return fail("reserved header")
		}
	}
	if size != uint64(len(data)) || baseOff != compactHeaderSize || baseLen < headerSize || sets == 0 || groups == 0 {
		return fail("section sizes")
	}
	end, ok := add64(baseOff, baseLen)
	if !ok || end != setOff {
		return fail("base extent")
	}
	n, ok := mul64(sets, targetSetSize)
	if !ok {
		return fail("set extent")
	}
	end, ok = add64(setOff, n)
	if !ok || end != targetOff {
		return fail("set extent")
	}
	n, ok = mul64(targets, targetSize)
	if !ok {
		return fail("target extent")
	}
	end, ok = add64(targetOff, n)
	if !ok || end != groupOff {
		return fail("target extent")
	}
	n, ok = mul64(groups, groupSize)
	if !ok {
		return fail("group extent")
	}
	end, ok = add64(groupOff, n)
	if !ok || end != size {
		return fail("group extent")
	}
	if setOff > size || targetOff > size || groupOff > size {
		return fail("section bounds")
	}
	g, err := parseLegacy(data[int(baseOff):int(setOff)])
	if err != nil {
		return nil, fmt.Errorf("diskgraph: compact base: %w", err)
	}
	c := &compactGraph{data: data, setOff: int(setOff), targetOff: int(targetOff), groupOff: int(groupOff), sets: int(sets), targets: int(targets), groups: int(groups), logicalEdges: int(logical), logicalStarts: make([]int, int(groups))}
	g.compact = c
	g.mapping = data
	var next uint64
	for i := 0; i < c.sets; i++ {
		first, count := g.targetRange(TargetSetID(i))
		if uint64(first) != next || count < 1 || uint64(count) > targets-next {
			return fail("target-set range")
		}
		seen := make(map[Key]struct{}, count)
		for j := 0; j < count; j++ {
			record := data[c.targetOff+(first+j)*targetSize : c.targetOff+(first+j+1)*targetSize]
			id := binary.LittleEndian.Uint32(record[:4])
			if uint64(id) >= uint64(len(g.blobs)) || binary.LittleEndian.Uint32(record[4:8]) != 0 {
				return fail("target record")
			}
			key := Key{BlobSHA: g.blobs[id], SymbolOffset: binary.LittleEndian.Uint64(record[8:16])}
			if _, dup := seen[key]; dup {
				return fail("duplicate target")
			}
			seen[key] = struct{}{}
		}
		next += uint64(count)
	}
	if next != targets {
		return fail("target coverage")
	}
	var extra uint64
	previous := -1
	for i := 0; i < c.groups; i++ {
		physical, ref := g.groupAt(i)
		if physical <= previous || physical >= g.edgeCount || uint64(ref.targets) >= sets {
			return fail("group reference")
		}
		previous = physical
		count := g.TargetCount(ref.targets)
		if ref.exclude < -1 || ref.exclude >= count || !validMove(ref, count) {
			return fail("target exclusion")
		}
		if ref.exclude >= 0 {
			count--
		}
		if count == 0 {
			return fail("empty factored source")
		}
		firstLogical, ok := add64(uint64(physical), extra)
		if !ok || firstLogical > uint64(maxInt()) {
			return fail("logical index overflow")
		}
		c.logicalStarts[i] = int(firstLogical)
		extra, ok = add64(extra, uint64(count-1))
		if !ok {
			return fail("logical count overflow")
		}
		first := orderedTargetIndex(ref, 0)
		witness, _ := g.TargetAt(ref.targets, first)
		edge := g.decodeEdge(physical)
		if edge.TargetBlob != witness.BlobSHA || edge.TargetOffset != witness.SymbolOffset {
			return fail("prototype target witness")
		}
	}
	total, ok := add64(uint64(g.edgeCount), extra)
	if !ok || total != logical {
		return fail("logical edge count")
	}
	return g, nil
}

func (g *Graph) targetRange(id TargetSetID) (int, int) {
	c := g.compact
	if c == nil || uint64(id) >= uint64(c.sets) {
		return 0, 0
	}
	at := c.setOff + int(id)*targetSetSize
	record := c.data[at : at+targetSetSize]
	first, count := binary.LittleEndian.Uint64(record[:8]), binary.LittleEndian.Uint64(record[8:16])
	if first > uint64(maxInt()) || count > uint64(maxInt()) {
		return -1, -1
	}
	return int(first), int(count)
}
func (g *Graph) groupAt(i int) (int, factoredRef) {
	at := g.compact.groupOff + i*groupSize
	record := g.compact.data[at : at+groupSize]
	physical := binary.LittleEndian.Uint64(record[:8])
	exclude := binary.LittleEndian.Uint64(record[16:24])
	moveTarget := binary.LittleEndian.Uint64(record[24:32])
	moveAfter := binary.LittleEndian.Uint64(record[32:40])
	validSigned := func(value uint64) bool { return value <= uint64(maxInt()) || value == ^uint64(0) }
	if physical > uint64(maxInt()) || !validSigned(exclude) || !validSigned(moveTarget) || !validSigned(moveAfter) {
		return -1, factoredRef{exclude: -2}
	}
	return int(physical), factoredRef{targets: TargetSetID(binary.LittleEndian.Uint64(record[8:16])), exclude: int(exclude), moveTarget: int(moveTarget), moveAfter: int(moveAfter)}
}

func (g *Graph) NumTargetSets() int {
	if g == nil || g.data == nil || g.compact == nil {
		return 0
	}
	return g.compact.sets
}
func (g *Graph) TargetCount(id TargetSetID) int {
	if g == nil || g.data == nil {
		return 0
	}
	_, count := g.targetRange(id)
	return count
}
func (g *Graph) TargetAt(id TargetSetID, index int) (Key, bool) {
	if g == nil || g.data == nil || g.compact == nil {
		return Key{}, false
	}
	first, count := g.targetRange(id)
	if index < 0 || index >= count {
		return Key{}, false
	}
	at := g.compact.targetOff + (first+index)*targetSize
	record := g.compact.data[at : at+targetSize]
	return Key{BlobSHA: g.blobs[binary.LittleEndian.Uint32(record[:4])], SymbolOffset: binary.LittleEndian.Uint64(record[8:16])}, true
}
func (g *Graph) EachTarget(id TargetSetID, fn func(Key) bool) {
	if fn == nil {
		return
	}
	for i := 0; i < g.TargetCount(id); i++ {
		key, _ := g.TargetAt(id, i)
		if !fn(key) {
			return
		}
	}
}

// NumRecords reports stored source records, not expanded target relationships.
func (g *Graph) NumRecords() int {
	if g == nil || g.data == nil {
		return 0
	}
	return g.edgeCount
}

func (g *Graph) EachRecord(fn func(PhysicalRecord) bool) {
	if g == nil || g.data == nil || fn == nil {
		return
	}
	group := 0
	next := -1
	var ref factoredRef
	if g.compact != nil && g.compact.groups > 0 {
		next, ref = g.groupAt(0)
	}
	for node := 0; node < g.nodeCount; node++ {
		key, first, count, _ := g.physicalNodeAt(node)
		for i := first; i < first+count; i++ {
			record := PhysicalRecord{Source: key, Edge: g.decodeEdge(i), Exclude: -1, MoveTarget: -1, MoveAfter: -1}
			if i == next {
				record.Factored = true
				record.Targets = ref.targets
				record.Exclude = ref.exclude
				record.MoveTarget = ref.moveTarget
				record.MoveAfter = ref.moveAfter
				record.Edge.TargetBlob = ""
				record.Edge.TargetOffset = 0
				group++
				next = -1
				if group < g.compact.groups {
					next, ref = g.groupAt(group)
				}
			}
			if !fn(record) {
				return
			}
		}
	}
}
func (g *Graph) EachExplicitEdge(fn func(Key, Edge) bool) {
	if fn == nil {
		return
	}
	g.EachRecord(func(r PhysicalRecord) bool {
		if r.Factored {
			return true
		}
		return fn(r.Source, r.Edge)
	})
}
func (g *Graph) EachFactoredSource(fn func(FactoredSource) bool) {
	if fn == nil {
		return
	}
	g.EachRecord(func(r PhysicalRecord) bool {
		if !r.Factored {
			return true
		}
		return fn(FactoredSource{Source: r.Source, Prototype: r.Edge, Targets: r.Targets, Exclude: r.Exclude, MoveTarget: r.MoveTarget, MoveAfter: r.MoveAfter})
	})
}

func (g *Graph) visitRecord(record PhysicalRecord, allow func(Edge) bool, fn func(Edge) bool) bool {
	if allow != nil && !allow(record.Edge) {
		return true
	}
	if !record.Factored {
		return fn(record.Edge)
	}
	ref := factoredRef{targets: record.Targets, exclude: record.Exclude, moveTarget: record.MoveTarget, moveAfter: record.MoveAfter}
	count := g.TargetCount(record.Targets)
	if record.Exclude >= 0 {
		count--
	}
	for rank := 0; rank < count; rank++ {
		target, _ := g.TargetAt(record.Targets, orderedTargetIndex(ref, rank))
		edge := record.Edge
		edge.TargetBlob = target.BlobSHA
		edge.TargetOffset = target.SymbolOffset
		if !fn(edge) {
			return false
		}
	}
	return true
}

func (g *Graph) nodeIndex(key Key) int {
	if g == nil || g.data == nil {
		return -1
	}
	id, ok := g.blobIDs[key.BlobSHA]
	if !ok {
		return -1
	}
	i := sort.Search(g.nodeCount, func(i int) bool {
		at := g.nodeOff + i*nodeSize
		r := g.data[at : at+nodeSize]
		blob := binary.LittleEndian.Uint32(r[:4])
		offset := binary.LittleEndian.Uint64(r[8:16])
		return blob > id || blob == id && offset >= key.SymbolOffset
	})
	if i == g.nodeCount {
		return -1
	}
	found, _, _, _ := g.physicalNodeAt(i)
	if found != key {
		return -1
	}
	return i
}

// EachOutgoing applies allow to source evidence before expanding any targets.
// For factored records allow must not depend on target fields (they are unset).
// Returning false from emit stops immediately, enabling bounded/paged visitors.
func (g *Graph) EachOutgoing(key Key, allow func(Edge) bool, emit func(Edge) bool) {
	_ = g.EachOutgoingContext(context.Background(), key, allow, emit)
}

// EachOutgoingContext is EachOutgoing with cancellation, including records
// rejected by allow and targets skipped before the caller sees them.
func (g *Graph) EachOutgoingContext(ctx context.Context, key Key, allow func(Edge) bool, emit func(Edge) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if emit == nil {
		return nil
	}
	node := g.nodeIndex(key)
	if node < 0 {
		return nil
	}
	_, first, count, _ := g.physicalNodeAt(node)
	group := 0
	next := -1
	var ref factoredRef
	if g.compact != nil {
		group = sort.Search(g.compact.groups, func(i int) bool { physical, _ := g.groupAt(i); return physical >= first })
		if group < g.compact.groups {
			next, ref = g.groupAt(group)
		}
	}
	for i := first; i < first+count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		record := PhysicalRecord{Source: key, Edge: g.decodeEdge(i), Exclude: -1, MoveTarget: -1, MoveAfter: -1}
		if i == next {
			record.Factored = true
			record.Targets = ref.targets
			record.Exclude = ref.exclude
			record.MoveTarget = ref.moveTarget
			record.MoveAfter = ref.moveAfter
			record.Edge.TargetBlob = ""
			record.Edge.TargetOffset = 0
			group++
			next = -1
			if group < g.compact.groups {
				next, ref = g.groupAt(group)
			}
		}
		if !g.visitRecord(record, allow, func(edge Edge) bool { return ctx.Err() == nil && emit(edge) }) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// EachIncoming selects requested targets within shared sets and visits matching
// physical sources in normal graph order. Its bounded temporary selection cache
// avoids repeated target scans for admitted sets; wider sets stream matches. allow follows EachOutgoing's prototype contract.
func (g *Graph) EachIncoming(targets map[Key]bool, allow func(Edge) bool, emit func(Key, Edge) bool) {
	_ = g.EachIncomingContext(context.Background(), targets, allow, emit)
}

// EachIncomingContext checks cancellation during target-set preselection and
// while scanning nonmatching source records, not just emitted relationships.
func (g *Graph) EachIncomingContext(ctx context.Context, targets map[Key]bool, allow func(Edge) bool, emit func(Key, Edge) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g == nil || g.data == nil || len(targets) == 0 || emit == nil {
		return nil
	}
	const maxSelectionBytes = 1 << 20
	const maxSelectionEntries = 4096
	const maxSelectedTargets = 65536
	type selection struct {
		indices []int
		wide    bool
	}
	matching := make(map[TargetSetID]selection)
	selectionBytes := 0
	selectTargets := func(id TargetSetID) (selection, error) {
		if value, ok := matching[id]; ok {
			return value, nil
		}
		var value selection
		for i := 0; i < g.TargetCount(id); i++ {
			if i&255 == 0 {
				if err := ctx.Err(); err != nil {
					return selection{}, err
				}
			}
			key, _ := g.TargetAt(id, i)
			if targets[key] {
				if len(value.indices) == maxSelectedTargets {
					value = selection{wide: true}
					break
				}
				value.indices = append(value.indices, i)
			}
		}
		// The lookup is only an optimization. If admission is exhausted, exact
		// selection is recomputed; broad sets use a streaming membership filter.
		bytes := cap(value.indices) * (strconv.IntSize / 8)
		if len(matching) < maxSelectionEntries && bytes <= maxSelectionBytes-selectionBytes {
			matching[id] = value
			selectionBytes += bytes
		}
		return value, nil
	}
	var selectionErr error

	g.EachRecord(func(record PhysicalRecord) bool {
		if ctx.Err() != nil {
			return false
		}
		if allow != nil && !allow(record.Edge) {
			return true
		}
		if !record.Factored {
			if targets[Key{BlobSHA: record.Edge.TargetBlob, SymbolOffset: record.Edge.TargetOffset}] {
				return emit(record.Source, record.Edge)
			}
			return true
		}
		selected, err := selectTargets(record.Targets)
		if err != nil {
			selectionErr = err
			return false
		}
		if selected.wide {
			return g.visitRecord(record, nil, func(edge Edge) bool {
				if ctx.Err() != nil {
					return false
				}
				if targets[Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}] {
					return emit(record.Source, edge)
				}
				return true
			})
		}
		indices := selected.indices
		emitIndex := func(i int) bool {
			if ctx.Err() != nil {
				return false
			}
			if i == record.Exclude {
				return true
			}
			target, _ := g.TargetAt(record.Targets, i)
			edge := record.Edge
			edge.TargetBlob = target.BlobSHA
			edge.TargetOffset = target.SymbolOffset
			return emit(record.Source, edge)
		}
		if record.MoveTarget < 0 {
			for _, i := range indices {
				if !emitIndex(i) {
					return false
				}
			}
		} else {
			boundary := sort.SearchInts(indices, record.MoveAfter+1)
			moved := sort.SearchInts(indices, record.MoveTarget)
			for _, i := range indices[:boundary] {
				if i != record.MoveTarget && !emitIndex(i) {
					return false
				}
			}
			if moved < len(indices) && indices[moved] == record.MoveTarget {
				if !emitIndex(record.MoveTarget) {
					return false
				}
			}
			for _, i := range indices[boundary:] {
				if !emitIndex(i) {
					return false
				}
			}
		}
		return true
	})
	if selectionErr != nil {
		return selectionErr
	}
	return ctx.Err()
}
func (g *Graph) logicalIndex(physical int) int {
	c := g.compact
	if c == nil {
		return physical
	}
	pos := sort.Search(c.groups, func(i int) bool { index, _ := g.groupAt(i); return index >= physical })
	if pos == 0 {
		return physical
	}
	index, ref := g.groupAt(pos - 1)
	count := g.TargetCount(ref.targets)
	if ref.exclude >= 0 {
		count--
	}
	return physical + c.logicalStarts[pos-1] - index + count - 1
}
func (g *Graph) compactEdgeAt(i int) (Edge, bool) {
	c := g.compact
	if g.data == nil || i < 0 || i >= c.logicalEdges {
		return Edge{}, false
	}
	pos := sort.Search(c.groups, func(j int) bool { return c.logicalStarts[j] > i })
	if pos == 0 {
		return g.decodeEdge(i), true
	}
	physical, ref := g.groupAt(pos - 1)
	first := c.logicalStarts[pos-1]
	count := g.TargetCount(ref.targets)
	if ref.exclude >= 0 {
		count--
	}
	if i >= first+count {
		return g.decodeEdge(physical + 1 + i - first - count), true
	}
	targetIndex := orderedTargetIndex(ref, i-first)
	target, _ := g.TargetAt(ref.targets, targetIndex)
	edge := g.decodeEdge(physical)
	edge.TargetBlob = target.BlobSHA
	edge.TargetOffset = target.SymbolOffset
	return edge, true
}
func (g *Graph) compactEdges(key Key) []Edge {
	node := g.nodeIndex(key)
	if node < 0 {
		return nil
	}
	_, _, count, _ := g.NodeAt(node)
	out := make([]Edge, 0, count)
	g.EachOutgoing(key, nil, func(edge Edge) bool { out = append(out, edge); return true })
	return out
}

func validMove(ref factoredRef, count int) bool {
	if ref.moveTarget == -1 && ref.moveAfter == -1 {
		return true
	}
	return ref.moveTarget >= 0 && ref.moveTarget < count && ref.moveAfter > ref.moveTarget && ref.moveAfter < count && ref.moveTarget != ref.exclude
}

// orderedTargetIndex maps a surviving logical rank to its canonical target
// index in constant time, accounting for the forward move and single exclusion.
func orderedTargetIndex(ref factoredRef, rank int) int {
	if ref.exclude >= 0 {
		position := ref.exclude
		if ref.moveTarget >= 0 && position > ref.moveTarget && position <= ref.moveAfter {
			position--
		}
		if rank >= position {
			rank++
		}
	}
	if ref.moveTarget >= 0 {
		if rank == ref.moveAfter {
			return ref.moveTarget
		}
		if rank >= ref.moveTarget && rank < ref.moveAfter {
			return rank + 1
		}
	}
	return rank
}

// EachRecordEdge expands one physical record in its exact logical order.
// The record must originate from this Graph's EachRecord visitor.
func (g *Graph) EachRecordEdge(record PhysicalRecord, fn func(Edge) bool) {
	if g == nil || g.data == nil || fn == nil {
		return
	}
	g.visitRecord(record, nil, fn)
}
