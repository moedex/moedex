package semanticindex

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"moedex/internal/mmapslice"
	"moedex/internal/semantic"
)

const legacyHeaderSize = 320
const contractHeaderSize = 336
const implementationHeaderSize = 384
const headerSize = 400
const legacySectionCount = 11
const domainSectionCount = 12
const contractSectionCount = 13
const implementationSectionCount = 16
const sectionCount = 17
const (
	strdir = iota
	strdata
	snapshots
	contexts
	sources
	symbols
	facts
	candidates
	positions
	definitions
	memberships
	domainFacts
	contractPostings
	implementationFacts
	invocationPostings
	implementationPostings
	interfacePostings
)

var widths = [sectionCount]uint64{16, 1, 40, 56, 48, 48, 128, 8, 8, 8, 16, 16, 32, 16, 8, 24, 24}
var le = binary.LittleEndian

type section struct{ off, n uint64 }
type Index struct {
	mapping  *mmapslice.Mapping
	data     []byte
	sections [sectionCount]section
	version  uint64
}

func (x *Index) field(s int, row, col uint64) uint64 {
	return le.Uint64(x.data[x.sections[s].off+row*widths[s]+col*8:])
}
func (x *Index) textBytes(id uint64) []byte {
	o, n := x.field(strdir, id, 0), x.field(strdir, id, 1)
	return x.data[x.sections[strdata].off+o : x.sections[strdata].off+o+n]
}
func (x *Index) text(id uint64) string { return string(x.textBytes(id)) }
func validDigest(s string) bool {
	v, e := hex.DecodeString(s)
	return e == nil && len(v) == 32 && s == strings.ToLower(s)
}

// Build writes a new, independent immutable file. JSON artifact decoding belongs
// exclusively to the offline caller; no audit captures are stored in this index.
func Build(path string, a *semantic.Artifact, p Provenance) error {
	if e := a.Validate(); e != nil {
		return e
	}
	if !a.Complete() {
		return fmt.Errorf("semanticindex: incomplete artifact")
	}
	if !validDigest(p.ArtifactSHA256) || !validDigest(p.CorpusFingerprint) {
		return fmt.Errorf("semanticindex: invalid provenance")
	}
	ss := append([]semantic.SourceSnapshot(nil), a.Snapshots...)
	cc := append([]semantic.BuildContext(nil), a.Contexts...)
	ff := append([]semantic.Source(nil), a.Sources...)
	sy := append([]semantic.Symbol(nil), a.Symbols...)
	oo := append([]semantic.Occurrence(nil), a.Occurrences...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })
	sort.Slice(cc, func(i, j int) bool { return cc[i].ID < cc[j].ID })
	sort.Slice(ff, func(i, j int) bool { return ff[i].ID < ff[j].ID })
	sort.Slice(sy, func(i, j int) bool { return sy[i].ID < sy[j].ID })
	sort.Slice(oo, func(i, j int) bool { return oo[i].ID < oo[j].ID })
	set := map[string]bool{"": true}
	add := func(v ...string) {
		for _, s := range v {
			set[s] = true
		}
	}
	for _, s := range ss {
		add(s.ID, s.Repo, s.ProjectID, s.Commit, s.InputFingerprint)
	}
	for _, c := range cc {
		add(c.ID, c.Project, c.InputFingerprint, c.Extractor, c.ExtractorVersion, c.Status)
	}
	for _, s := range ff {
		add(s.ID, s.Path, s.RawSHA256)
	}
	for _, s := range sy {
		add(s.ID, s.Key.Language, s.Key.NamespaceKind, s.Key.Namespace, s.Key.Descriptor, s.Key.DescriptorKind)
	}
	bindings := map[string]semantic.Binding{}
	domainJSON := map[string]string{}
	implementationJSON := map[string]string{}
	for _, b := range a.Bindings {
		bindings[b.OccurrenceID] = b
		if len(b.ImplementationFacts) > 0 {
			raw, err := json.Marshal(b.ImplementationFacts)
			if err != nil {
				return err
			}
			if len(raw) > maxDomainJSONBytes {
				return fmt.Errorf("semanticindex: implementation facts exceed limit")
			}
			implementationJSON[b.OccurrenceID] = string(raw)
			add(string(raw))
		}
		if len(b.DomainFacts) > 0 {
			raw, err := json.Marshal(b.DomainFacts)
			if err != nil {
				return err
			}
			if len(raw) > maxDomainJSONBytes {
				return fmt.Errorf("semanticindex: domain facts exceed limit")
			}
			domainJSON[b.OccurrenceID] = string(raw)
			add(string(raw))
		}
		add(b.ID, b.Method, b.Extractor, b.ExtractorVersion, b.Status)
	}
	for _, o := range oo {
		add(o.ID, o.Role, o.Kind)
	}
	strs := make([]string, 0, len(set))
	for s := range set {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return fmt.Errorf("semanticindex: invalid UTF8 string")
		}
		strs = append(strs, s)
	}
	sort.Strings(strs)
	ids := map[string]uint64{}
	for i, s := range strs {
		ids[s] = uint64(i)
	}
	var tables [sectionCount][]byte
	push := func(t int, v ...uint64) {
		for _, n := range v {
			tables[t] = le.AppendUint64(tables[t], n)
		}
	}
	for _, s := range strs {
		push(strdir, uint64(len(tables[strdata])), uint64(len(s)))
		tables[strdata] = append(tables[strdata], s...)
	}
	sm, cm, fm, ym := map[string]uint64{}, map[string]uint64{}, map[string]uint64{}, map[string]uint64{}
	for i, s := range ss {
		sm[s.ID] = uint64(i)
		push(snapshots, ids[s.ID], ids[s.Repo], ids[s.ProjectID], ids[s.Commit], ids[s.InputFingerprint])
	}
	for i, c := range cc {
		cm[c.ID] = uint64(i)
		push(contexts, ids[c.ID], sm[c.SnapshotID], ids[c.Project], ids[c.InputFingerprint], ids[c.Extractor], ids[c.ExtractorVersion], ids[c.Status])
	}
	for i, s := range ff {
		fm[s.ID] = uint64(i)
		g := uint64(0)
		if s.Generated {
			g = 1
		}
		push(sources, ids[s.ID], sm[s.SnapshotID], ids[s.Path], ids[s.RawSHA256], s.ByteSize, g)
	}
	for i, s := range sy {
		ym[s.ID] = uint64(i)
		k := s.Key
		push(symbols, ids[s.ID], ids[k.Language], ids[k.NamespaceKind], ids[k.Namespace], ids[k.Descriptor], ids[k.DescriptorKind])
	}
	var reverse [][4]uint64
	var invocations []uint64
	var implementations [][3]uint64
	var interfaces [][3]uint64
	for factRow, o := range oo {
		b := bindings[o.ID]
		if raw, ok := implementationJSON[o.ID]; ok {
			push(implementationFacts, uint64(factRow), ids[raw])
			for ordinal := range b.ImplementationFacts {
				implementations = append(implementations, [3]uint64{ym[b.SymbolID], uint64(factRow), uint64(ordinal)})
				interfaces = append(interfaces, [3]uint64{ym[b.ImplementationFacts[ordinal].InterfaceSymbolID], uint64(factRow), uint64(ordinal)})
			}
		}
		if o.Role == "reference" && o.Kind == "invocation" && b.Status == "resolved" && b.SymbolID != "" && b.EnclosingSymbolID != "" {
			invocations = append(invocations, uint64(factRow))
		}
		for di, domain := range b.DomainFacts {
			for ti, target := range domain.Targets {
				reverse = append(reverse, [4]uint64{ym[target.SymbolID], uint64(factRow), uint64(di), uint64(ti)})
			}
		}
		if raw, ok := domainJSON[o.ID]; ok {
			push(domainFacts, uint64(factRow), ids[raw])
		}
		sym, enc := uint64(0), uint64(0)
		if b.SymbolID != "" {
			sym = ym[b.SymbolID] + 1
		}
		if b.EnclosingSymbolID != "" {
			enc = ym[b.EnclosingSymbolID] + 1
		}
		start := uint64(len(tables[candidates]) / 8)
		for _, s := range b.CandidateSymbolIDs {
			push(candidates, ym[s])
		}
		push(facts, ids[o.ID], ids[b.ID], fm[o.SourceID], cm[o.ContextID], o.Offset, o.Length, ids[o.Role], ids[o.Kind], ids[b.Status], sym, enc, start, uint64(len(b.CandidateSymbolIDs)), ids[b.Method], ids[b.Extractor], ids[b.ExtractorVersion])
	}
	sort.Slice(reverse, func(i, j int) bool {
		a, b := reverse[i], reverse[j]
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		ca, cb := cm[oo[a[1]].ContextID], cm[oo[b[1]].ContextID]
		if ca != cb {
			return ca < cb
		}
		return lessContractPosting(a, b)
	})
	for _, row := range reverse {
		push(contractPostings, row[:]...)
	}
	var members [][2]uint64
	for _, c := range cc {
		for _, sid := range c.SourceIDs {
			members = append(members, [2]uint64{fm[sid], cm[c.ID]})
		}
	}
	sort.Slice(members, func(i, j int) bool {
		a, b := members[i], members[j]
		sa, sb := ff[a[0]], ff[b[0]]
		ra, rb := ss[sm[sa.SnapshotID]].Repo, ss[sm[sb.SnapshotID]].Repo
		if ra != rb {
			return ra < rb
		}
		if sa.Path != sb.Path {
			return sa.Path < sb.Path
		}
		if a[1] != b[1] {
			return a[1] < b[1]
		}
		return a[0] < b[0]
	})
	for _, m := range members {
		push(memberships, m[0], m[1])
	}
	// Construct a temporary view for the exact same comparator used by readers.
	x := &Index{data: make([]byte, headerSize)}
	for t, b := range tables {
		x.sections[t] = section{uint64(len(x.data)), uint64(len(b)) / widths[t]}
		x.data = append(x.data, b...)
	}
	sort.Slice(invocations, func(i, j int) bool { return less3(x.invocationKey(invocations[i]), x.invocationKey(invocations[j])) })
	for _, row := range invocations {
		push(invocationPostings, row)
	}
	sort.Slice(implementations, func(i, j int) bool {
		return lessContractPosting(x.implementationKey(implementations[i]), x.implementationKey(implementations[j]))
	})
	for _, row := range implementations {
		push(implementationPostings, row[:]...)
	}
	sort.Slice(interfaces, func(i, j int) bool {
		return lessContractPosting(x.implementationKey(interfaces[i]), x.implementationKey(interfaces[j]))
	})
	for _, row := range interfaces {
		push(interfacePostings, row[:]...)
	}
	pos := make([]uint64, len(oo))
	var defs []uint64
	for i, o := range oo {
		pos[i] = uint64(i)
		if o.Role == "declaration" && bindings[o.ID].Status == "resolved" {
			defs = append(defs, uint64(i))
		}
	}
	sort.Slice(pos, func(i, j int) bool { return x.less(pos[i], pos[j], false) })
	sort.Slice(defs, func(i, j int) bool { return x.less(defs[i], defs[j], true) })
	for _, i := range pos {
		push(positions, i)
	}
	for _, i := range defs {
		push(definitions, i)
	}
	data := make([]byte, headerSize)
	copy(data, "MDXSEM01")
	le.PutUint64(data[8:], Version)
	ad, _ := hex.DecodeString(p.ArtifactSHA256)
	cp, _ := hex.DecodeString(p.CorpusFingerprint)
	copy(data[56:], ad)
	copy(data[88:], cp)
	for t, b := range tables {
		le.PutUint64(data[120+t*16:], uint64(len(data)))
		le.PutUint64(data[128+t*16:], uint64(len(b))/widths[t])
		data = append(data, b...)
	}
	if int64(len(data)) > MaxBytes {
		return fmt.Errorf("semanticindex: exceeds byte limit")
	}
	le.PutUint64(data[16:], uint64(len(data)))
	hash := sha256.Sum256(data[headerSize:])
	copy(data[24:], hash[:])
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".semantic-index-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Link(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// Open maps immutable snapshot bytes. Expected provenance is mandatory. Caller
// owns the serving lease and must not Close while another call is in progress.
func Open(path string, e Expected, l Limits) (*Index, error) {
	if !validDigest(e.ArtifactSHA256) || !validDigest(e.CorpusFingerprint) {
		return nil, fmt.Errorf("semanticindex: expected provenance required")
	}
	max := l.MaxBytes
	if max == 0 {
		max = MaxBytes
	}
	if max < 1 || max > MaxBytes {
		return nil, fmt.Errorf("semanticindex: invalid limit")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() < legacyHeaderSize || st.Size() > max {
		return nil, fmt.Errorf("semanticindex: invalid file size/type")
	}
	m, err := mmapslice.Open(path)
	if err != nil {
		return nil, err
	}
	x := &Index{mapping: m, data: m.Bytes()}
	if err = x.validate(e, max); err != nil {
		m.Close()
		return nil, err
	}
	return x, nil
}
func (x *Index) Close() error {
	if x == nil {
		return nil
	}
	x.data = nil
	if x.mapping == nil {
		return nil
	}
	e := x.mapping.Close()
	x.mapping = nil
	return e
}

func (x *Index) less(a, b uint64, defs bool) bool {
	cmp := func(u, v uint64) int {
		if u < v {
			return -1
		}
		if u > v {
			return 1
		}
		return 0
	}
	sa, sb := x.field(facts, a, 2), x.field(facts, b, 2)
	ca, cb := x.field(facts, a, 3), x.field(facts, b, 3)
	ra, rb := x.field(snapshots, x.field(sources, sa, 1), 1), x.field(snapshots, x.field(sources, sb, 1), 1)
	if defs {
		if c := cmp(x.field(facts, a, 9), x.field(facts, b, 9)); c != 0 {
			return c < 0
		}
	}
	for _, pair := range [][2]uint64{{ra, rb}, {x.field(sources, sa, 2), x.field(sources, sb, 2)}, {x.field(facts, a, 4), x.field(facts, b, 4)}, {ca, cb}, {x.field(facts, a, 0), x.field(facts, b, 0)}} {
		if c := cmp(pair[0], pair[1]); c != 0 {
			return c < 0
		}
	}
	return false
}
