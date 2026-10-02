package diskgraph

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/graph"
)

func factoredPrototype(source Key, name string, at uint64) Edge {
	return Edge{Type: EdgeCalls, Confidence: graph.Pattern, Name: name, Generation: 7, Evidence: graph.Evidence{BlobSHA: source.BlobSHA, ByteOffset: at, ByteLength: 3}}
}
func openBuilt(t *testing.T, b *Builder) *Graph {
	t.Helper()
	path := filepath.Join(t.TempDir(), "graph")
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}
func assertLogicalEqual(t *testing.T, got, want *Graph) {
	t.Helper()
	if got.NumNodes() != want.NumNodes() || got.NumEdges() != want.NumEdges() {
		t.Fatalf("counts got %d/%d want %d/%d", got.NumNodes(), got.NumEdges(), want.NumNodes(), want.NumEdges())
	}
	if !reflect.DeepEqual(got.Keys(), want.Keys()) || !reflect.DeepEqual(got.Names(), want.Names()) {
		t.Fatal("keys/names differ")
	}
	for node := 0; node < want.NumNodes(); node++ {
		key, first, count, ok := got.NodeAt(node)
		wk, wf, wc, wo := want.NodeAt(node)
		if key != wk || first != wf || count != wc || ok != wo {
			t.Fatalf("NodeAt(%d): %v %d %d versus %v %d %d", node, key, first, count, wk, wf, wc)
		}
		if a, b := got.Edges(key), want.Edges(key); !reflect.DeepEqual(a, b) {
			t.Fatalf("node %v edges differ\ngot %#v\nwant %#v", key, a, b)
		}
		if !reflect.DeepEqual(got.Load(key.BlobSHA, key.SymbolOffset), want.Load(key.BlobSHA, key.SymbolOffset)) {
			t.Fatal("Load differs")
		}
	}
	for i := -1; i <= want.NumEdges(); i++ {
		a, ao := got.EdgeAt(i)
		b, bo := want.EdgeAt(i)
		if a != b || ao != bo || got.EdgeName(i) != want.EdgeName(i) {
			t.Fatalf("EdgeAt/name(%d) differs", i)
		}
	}
	type record struct {
		source Key
		edge   Edge
	}
	var a, b []record
	got.EachEdge(func(k Key, e Edge) bool { a = append(a, record{k, e}); return true })
	want.EachEdge(func(k Key, e Edge) bool { b = append(b, record{k, e}); return true })
	if !reflect.DeepEqual(a, b) {
		t.Fatal("EachEdge order differs")
	}
}

func mixedFactoredFixture(t *testing.T) (*Builder, *Builder, []Key) {
	t.Helper()
	compact, expanded := NewBuilder(), NewBuilder()
	compact.SetGeneration(7)
	expanded.SetGeneration(7)
	compact.AddCorpusEntry("roster\x00version=4")
	expanded.AddCorpusEntry("roster\x00version=4")
	targets := []Key{{"z", 30}, {"b", 20}, {"a", 10}, {"b", 25}}
	id, err := compact.AddTargetSet(targets)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := compact.AddTargetSet(targets); err != nil || again != id {
		t.Fatal("target set not interned")
	}
	// Interleave source nodes and physical record kinds. On disk source nodes
	// sort, while every node's explicit/group record order remains unchanged.
	for _, source := range []Key{{"c", 9}, {"a", 3}, {"c", 2}} {
		for step := 0; step < 5; step++ {
			e := factoredPrototype(source, fmt.Sprintf("Name%d", step), uint64(step+50))
			if step%2 == 0 {
				e.TargetBlob = targets[step%len(targets)].BlobSHA
				e.TargetOffset = targets[step%len(targets)].SymbolOffset
				if err := compact.AddEdge(source, e); err != nil {
					t.Fatal(err)
				}
				expanded.AddEdge(source, e)
				continue
			}
			exclude := -1
			if step == 3 {
				exclude = 1
			}
			if err := compact.AddFactoredSource(source, e, id, exclude); err != nil {
				t.Fatal(err)
			}
			for i, target := range targets {
				if i == exclude {
					continue
				}
				x := e
				x.TargetBlob = target.BlobSHA
				x.TargetOffset = target.SymbolOffset
				expanded.AddEdge(source, x)
			}
		}
	}
	// Fully excluded groups create an isolated node, just as AddNode does.
	single, _ := compact.AddTargetSet([]Key{{"only", 5}})
	isolated := Key{"isolated", 1}
	if err := compact.AddFactoredSource(isolated, factoredPrototype(isolated, "none", 1), single, 0); err != nil {
		t.Fatal(err)
	}
	expanded.AddNode(isolated)
	return compact, expanded, targets
}

func TestFactoredRoundTripExactLogicalOrder(t *testing.T) {
	compact, expanded, _ := mixedFactoredFixture(t)
	if compact.NumEdges() != expanded.NumEdges() {
		t.Fatal("builder logical count differs")
	}
	got, want := openBuilt(t, compact), openBuilt(t, expanded)
	assertLogicalEqual(t, got, want)
	if got.Generation() != 7 || !reflect.DeepEqual(got.CorpusEntrySet(), want.CorpusEntrySet()) {
		t.Fatal("generation/roster changed")
	}
	if got.NumRecords() != 15 || got.NumTargetSets() != 2 {
		t.Fatalf("physical counts %d/%d", got.NumRecords(), got.NumTargetSets())
	}
	explicit, groups := 0, 0
	got.EachExplicitEdge(func(Key, Edge) bool { explicit++; return true })
	got.EachFactoredSource(func(f FactoredSource) bool {
		groups++
		if f.Prototype.TargetBlob != "" || f.Prototype.TargetOffset != 0 {
			t.Fatal("group exposes witness as target")
		}
		return true
	})
	if explicit != 9 || groups != 6 {
		t.Fatalf("physical kinds %d/%d", explicit, groups)
	}
	if allocs := testing.AllocsPerRun(10, func() { got.EachEdge(func(Key, Edge) bool { return true }) }); allocs != 0 {
		t.Fatalf("logical streaming allocated %v", allocs)
	}
	if got.BuildID() == want.BuildID() {
		t.Fatal("BuildID omitted compact representation")
	}
	// Identical builders produce identical bytes despite their temporary paths.
	other := openBuilt(t, compact)
	if other.BuildID() != got.BuildID() {
		t.Fatal("nondeterministic compact save")
	}
}

func TestFactoredVisitorsSelectBeforeExpansion(t *testing.T) {
	b, expanded, targets := mixedFactoredFixture(t)
	g, want := openBuilt(t, b), openBuilt(t, expanded)
	allow := func(e Edge) bool { return e.Name != "Name3" }
	for _, key := range want.Keys() {
		var got, expected []Edge
		g.EachOutgoing(key, allow, func(e Edge) bool { got = append(got, e); return true })
		for _, e := range want.Edges(key) {
			if allow(e) {
				expected = append(expected, e)
			}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("outgoing %v differs", key)
		}
	}
	wanted := map[Key]bool{targets[0]: true, targets[1]: true, targets[2]: false}
	type visit struct {
		source Key
		edge   Edge
	}
	var got, expected []visit
	g.EachIncoming(wanted, allow, func(k Key, e Edge) bool { got = append(got, visit{k, e}); return true })
	want.EachEdge(func(k Key, e Edge) bool {
		if allow(e) && wanted[Key{e.TargetBlob, e.TargetOffset}] {
			expected = append(expected, visit{k, e})
		}
		return true
	})
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("incoming differs\n%#v\n%#v", got, expected)
	}
	stopped := 0
	g.EachIncoming(wanted, nil, func(Key, Edge) bool { stopped++; return false })
	if stopped != 1 {
		t.Fatal("incoming did not stop")
	}
	stopped = 0
	g.EachOutgoing(want.Keys()[0], nil, func(Edge) bool { stopped++; return false })
	if stopped != 1 {
		t.Fatal("outgoing did not stop")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.EachIncomingContext(ctx, wanted, nil, func(Key, Edge) bool { t.Fatal("emitted after cancellation"); return true }); err != context.Canceled {
		t.Fatal(err)
	}
	if err := g.EachOutgoingContext(ctx, want.Keys()[0], nil, func(Edge) bool { return true }); err != context.Canceled {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	checked := 0
	err := g.EachIncomingContext(ctx, wanted, func(Edge) bool { checked++; cancel(); return false }, func(Key, Edge) bool { t.Fatal("filtered record emitted"); return true })
	if err != context.Canceled || checked != 1 {
		t.Fatalf("nonmatching cancellation: %v/%d", err, checked)
	}
	ctx, cancel = context.WithCancel(context.Background())
	checked = 0
	err = g.EachOutgoingContext(ctx, want.Keys()[0], func(Edge) bool { checked++; cancel(); return false }, func(Edge) bool { return true })
	if err != context.Canceled || checked != 1 {
		t.Fatalf("outgoing filtered cancellation: %v/%d", err, checked)
	}
}

func TestFactoredPhysicalCarryPreservesLogicalGraph(t *testing.T) {
	b, _, _ := mixedFactoredFixture(t)
	original := openBuilt(t, b)
	carry := NewBuilder()
	ids := map[TargetSetID]TargetSetID{}
	for _, key := range original.Keys() {
		carry.AddNode(key)
	}
	original.EachRecord(func(record PhysicalRecord) bool {
		if !record.Factored {
			if err := carry.AddEdge(record.Source, record.Edge); err != nil {
				t.Fatal(err)
			}
			return true
		}
		id, ok := ids[record.Targets]
		if !ok {
			var keys []Key
			original.EachTarget(record.Targets, func(k Key) bool { keys = append(keys, k); return true })
			var err error
			id, err = carry.AddTargetSet(keys)
			if err != nil {
				t.Fatal(err)
			}
			ids[record.Targets] = id
		}
		if err := carry.AddFactoredSource(record.Source, record.Edge, id, record.Exclude); err != nil {
			t.Fatal(err)
		}
		return true
	})
	assertLogicalEqual(t, openBuilt(t, carry), original)
}

func TestFactoredUpgradeMatchesExpandedFirstRelationship(t *testing.T) {
	for exclude := -1; exclude < 4; exclude++ {
		for upgraded := 0; upgraded < 4; upgraded++ {
			t.Run(fmt.Sprintf("exclude%d/upgrade%d", exclude, upgraded), func(t *testing.T) {
				compact, expanded := NewBuilder(), NewBuilder()
				source := Key{"source", 1}
				targets := []Key{{"a", 1}, {"b", 2}, {"c", 3}, {"d", 4}}
				id, _ := compact.AddTargetSet(targets)
				before := factoredPrototype(source, "before", 50)
				before.TargetBlob = "before-target"
				compact.AddEdge(source, before)
				expanded.AddEdge(source, before)
				prototype := factoredPrototype(source, "group", 60)
				compact.AddFactoredSource(source, prototype, id, exclude)
				for i, key := range targets {
					if i == exclude {
						continue
					}
					e := prototype
					e.TargetBlob = key.BlobSHA
					e.TargetOffset = key.SymbolOffset
					expanded.AddEdge(source, e)
				}
				after := factoredPrototype(source, "after", 70)
				compact.AddFactoredSource(source, after, id, -1)
				for _, key := range targets {
					e := after
					e.TargetBlob = key.BlobSHA
					e.TargetOffset = key.SymbolOffset
					expanded.AddEdge(source, e)
				}
				edge := prototype
				edge.Confidence = graph.Proven
				edge.TargetBlob = targets[upgraded].BlobSHA
				edge.TargetOffset = targets[upgraded].SymbolOffset
				edge.Generation = 9
				a, err := compact.AddOrUpgradeEdge(source, edge)
				if err != nil {
					t.Fatal(err)
				}
				w, err := expanded.AddOrUpgradeEdge(source, edge)
				if err != nil || a != w {
					t.Fatalf("upgrade append %v/%v err%v", a, w, err)
				}
				if compact.NumEdges() != expanded.NumEdges() {
					t.Fatal("upgrade count changed")
				}
				assertLogicalEqual(t, openBuilt(t, compact), openBuilt(t, expanded))
			})
		}
	}
}

func TestFactoredStorageScalesWithSourcesAndTargets(t *testing.T) {
	b := NewBuilder()
	targets := make([]Key, 1000)
	for i := range targets {
		targets[i] = Key{"target", uint64(i)}
	}
	id, err := b.AddTargetSet(targets)
	if err != nil {
		t.Fatal(err)
	}
	const sources = 10000
	for i := 0; i < sources; i++ {
		key := Key{"source", uint64(i)}
		if err := b.AddFactoredSource(key, factoredPrototype(key, "Shared", uint64(i)), id, -1); err != nil {
			t.Fatal(err)
		}
	}
	g := openBuilt(t, b)
	if g.NumEdges() != sources*len(targets) || g.NumRecords() != sources || len(g.compact.logicalStarts) != sources {
		t.Fatal("logical/physical storage counts wrong")
	}
	if len(g.mapping) > 2<<20 {
		t.Fatalf("compact file grew with Cartesian product: %d bytes", len(g.mapping))
	}
	n := 0
	g.EachIncoming(map[Key]bool{targets[800]: true}, nil, func(Key, Edge) bool { n++; return true })
	if n != sources {
		t.Fatalf("incoming matches %d", n)
	}
	n = 0
	g.EachOutgoing(Key{"source", 10}, nil, func(Edge) bool { n++; return n < 3 })
	if n != 3 {
		t.Fatal("bounded expansion failed")
	}
}

func TestFactoredInvalidInputAndCorruption(t *testing.T) {
	b, _, _ := mixedFactoredFixture(t)
	if _, err := b.AddTargetSet(nil); err == nil {
		t.Fatal("accepted empty target set")
	}
	if _, err := b.AddTargetSet([]Key{{"a", 1}, {"a", 1}}); err == nil {
		t.Fatal("accepted duplicate target")
	}
	if _, err := b.AddTargetSet([]Key{{"", 1}}); err == nil {
		t.Fatal("accepted missing blob")
	}
	source := Key{"source", 1}
	prototype := factoredPrototype(source, "Name", 1)
	for _, tc := range []struct {
		id      TargetSetID
		exclude int
	}{{100, -1}, {0, -2}, {0, 100}} {
		if err := b.AddFactoredSource(source, prototype, tc.id, tc.exclude); err == nil {
			t.Fatal("accepted invalid group")
		}
	}
	g := openBuilt(t, b)
	raw := append([]byte(nil), g.mapping...)
	off := func(field int) int { return int(binary.LittleEndian.Uint64(raw[field : field+8])) }
	setOff, targetOff, groupOff := off(40), off(56), off(72)
	mutations := map[string]func([]byte){
		"truncated":          func(data []byte) { binary.LittleEndian.PutUint64(data[16:24], uint64(len(data)+1)) },
		"reserved":           func(data []byte) { data[100] = 1 },
		"set count overflow": func(data []byte) { binary.LittleEndian.PutUint64(data[48:56], ^uint64(0)) },
		"set range":          func(data []byte) { binary.LittleEndian.PutUint64(data[setOff:setOff+8], 1) },
		"set length":         func(data []byte) { binary.LittleEndian.PutUint64(data[setOff+8:setOff+16], ^uint64(0)) },
		"target blob":        func(data []byte) { binary.LittleEndian.PutUint32(data[targetOff:targetOff+4], ^uint32(0)) },
		"target reserved":    func(data []byte) { data[targetOff+4] = 1 },
		"duplicate target": func(data []byte) {
			copy(data[targetOff+targetSize:targetOff+2*targetSize], data[targetOff:targetOff+targetSize])
		},
		"group physical":  func(data []byte) { binary.LittleEndian.PutUint64(data[groupOff:groupOff+8], ^uint64(0)) },
		"group order":     func(data []byte) { copy(data[groupOff+groupSize:groupOff+groupSize+8], data[groupOff:groupOff+8]) },
		"group set":       func(data []byte) { binary.LittleEndian.PutUint64(data[groupOff+8:groupOff+16], 1000) },
		"group exclusion": func(data []byte) { binary.LittleEndian.PutUint64(data[groupOff+16:groupOff+24], 1000) },
		"logical count":   func(data []byte) { binary.LittleEndian.PutUint64(data[88:96], 1) },
		"witness":         func(data []byte) { data[targetOff+8] ^= 1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), raw...)
			mutate(data)
			if _, err := parse(data); err == nil {
				t.Fatal("accepted corruption")
			}
		})
	}
	// Every truncation must fail without slicing beyond the supplied bytes.
	for n := 0; n < len(raw); n++ {
		if _, err := parse(raw[:n]); err == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	path := filepath.Join(t.TempDir(), "graph")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if opened.NumRecords() != 0 || opened.NumTargetSets() != 0 || opened.Edges(source) != nil {
		t.Fatal("closed graph exposes data")
	}
}

func TestFactoredMovesAndExclusionsPreserveExactOrder(t *testing.T) {
	compact, expanded := NewBuilder(), NewBuilder()
	targets := []Key{{"a", 0}, {"b", 1}, {"c", 2}, {"d", 3}, {"e", 4}}
	id, _ := compact.AddTargetSet(targets)
	for moved := 0; moved < len(targets)-1; moved++ {
		for after := moved + 1; after < len(targets); after++ {
			for exclude := -1; exclude < len(targets); exclude++ {
				if exclude == moved {
					continue
				}
				source := Key{fmt.Sprintf("source-%d-%d-%d", moved, after, exclude), 0}
				prototype := factoredPrototype(source, "Moved", 20)
				if err := compact.AddFactoredSourceOrdered(source, prototype, id, exclude, moved, after); err != nil {
					t.Fatal(err)
				}
				// Independent insertion-based oracle: remove and reinsert the moved target,
				// then filter the excluded canonical key. No production rank helper used.
				order := append([]Key{}, targets[:moved]...)
				order = append(order, targets[moved+1:after+1]...)
				order = append(order, targets[moved])
				order = append(order, targets[after+1:]...)
				for _, key := range order {
					if exclude >= 0 && key == targets[exclude] {
						continue
					}
					edge := prototype
					edge.TargetBlob = key.BlobSHA
					edge.TargetOffset = key.SymbolOffset
					expanded.AddEdge(source, edge)
				}
			}
		}
	}
	g, want := openBuilt(t, compact), openBuilt(t, expanded)
	assertLogicalEqual(t, g, want)
	for mask := 0; mask < 1<<len(targets); mask++ {
		selected := make(map[Key]bool)
		for i, key := range targets {
			selected[key] = mask&(1<<i) != 0
		}
		type item struct {
			source Key
			edge   Edge
		}
		var got, expected []item
		g.EachIncoming(selected, nil, func(source Key, edge Edge) bool { got = append(got, item{source, edge}); return true })
		want.EachEdge(func(source Key, edge Edge) bool {
			if selected[Key{edge.TargetBlob, edge.TargetOffset}] {
				expected = append(expected, item{source, edge})
			}
			return true
		})
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("incoming moved order mismatch for subset %d", mask)
		}
	}
	g.EachRecord(func(record PhysicalRecord) bool {
		var got []Edge
		g.EachRecordEdge(record, func(e Edge) bool { got = append(got, e); return true })
		if !reflect.DeepEqual(got, want.Edges(record.Source)) {
			t.Fatal("per-record expansion order differs")
		}
		return true
	})
	// Upgrade the moved target while keeping every other logical edge in place.
	for _, source := range g.Keys() {
		edges := want.Edges(source)
		edge := edges[len(edges)/2]
		edge.Confidence = graph.Proven
		edge.Generation = 8
		a, err := compact.AddOrUpgradeEdge(source, edge)
		if err != nil {
			t.Fatal(err)
		}
		b, err := expanded.AddOrUpgradeEdge(source, edge)
		if err != nil || a != b {
			t.Fatal("moved upgrade mismatch", err)
		}
	}
	assertLogicalEqual(t, openBuilt(t, compact), openBuilt(t, expanded))
	for _, move := range [][2]int{{0, -1}, {-1, 0}, {2, 1}, {2, 2}, {0, 5}, {5, 6}} {
		if err := compact.AddFactoredSourceOrdered(Key{"invalid", 0}, factoredPrototype(Key{"invalid", 0}, "bad", 0), id, -1, move[0], move[1]); err == nil {
			t.Fatal("accepted invalid move", move)
		}
	}
	raw := append([]byte(nil), g.mapping...)
	groupOff := int(binary.LittleEndian.Uint64(raw[72:80]))
	binary.LittleEndian.PutUint64(raw[groupOff+24:groupOff+32], uint64(len(targets)))
	if _, err := parse(raw); err == nil {
		t.Fatal("accepted corrupt moved-target index")
	}
}

func TestFactoredIncomingWideSelectionStreamsExactly(t *testing.T) {
	b := NewBuilder()
	targets := make([]Key, 65538)
	selected := make(map[Key]bool, len(targets))
	for i := range targets {
		targets[i] = Key{"target", uint64(i)}
		selected[targets[i]] = true
	}
	id, err := b.AddTargetSet(targets)
	if err != nil {
		t.Fatal(err)
	}
	source := Key{"source", 0}
	if err := b.AddFactoredSourceOrdered(source, factoredPrototype(source, "Wide", 0), id, 3, 0, 5); err != nil {
		t.Fatal(err)
	}
	g := openBuilt(t, b)
	var got []uint64
	g.EachIncoming(selected, nil, func(_ Key, edge Edge) bool { got = append(got, edge.TargetOffset); return len(got) < 6 })
	if want := []uint64{1, 2, 4, 5, 0, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wide ordered stream %v, want %v", got, want)
	}
}
