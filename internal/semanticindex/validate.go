package semanticindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"moedex/internal/semantic"
	"unicode/utf8"
)

func (x *Index) validate(e Expected, max int64) error {
	bad := func() error { return fmt.Errorf("semanticindex: corrupt or unsupported index") }
	b := x.data
	if len(b) < legacyHeaderSize || int64(len(b)) > max || string(b[:8]) != "MDXSEM01" || le.Uint64(b[16:]) != uint64(len(b)) {
		return bad()
	}
	version := le.Uint64(b[8:])
	count, size := sectionCount, headerSize
	switch version {
	case 1:
		count, size = legacySectionCount, legacyHeaderSize
	case 2:
		count, size = domainSectionCount, legacyHeaderSize
	case 3:
		count, size = contractSectionCount, contractHeaderSize
	case 4:
		count, size = implementationSectionCount, implementationHeaderSize
	case 5:
		count, size = interfaceSectionCount, interfaceHeaderSize
	case Version:
	default:
		return bad()
	}
	if len(b) < size {
		return bad()
	}
	x.version = version
	ad, _ := hex.DecodeString(e.ArtifactSHA256)
	cp, _ := hex.DecodeString(e.CorpusFingerprint)
	if !bytes.Equal(b[56:88], ad) || !bytes.Equal(b[88:120], cp) {
		return fmt.Errorf("semanticindex: provenance mismatch")
	}
	hash := sha256.Sum256(b[size:])
	if !bytes.Equal(b[24:56], hash[:]) {
		return bad()
	}
	for _, v := range b[120+count*16 : size] {
		if v != 0 {
			return bad()
		}
	}
	end := uint64(size)
	for t := 0; t < count; t++ {
		o, n := le.Uint64(b[120+t*16:]), le.Uint64(b[128+t*16:])
		if o != end || o > uint64(len(b)) || n > (uint64(len(b))-o)/widths[t] {
			return bad()
		}
		x.sections[t] = section{o, n}
		end = o + n*widths[t]
	}
	if end != uint64(len(b)) || x.sections[strdir].n == 0 {
		return bad()
	}
	end = 0
	for i := uint64(0); i < x.sections[strdir].n; i++ {
		o, n := x.field(strdir, i, 0), x.field(strdir, i, 1)
		if o != end || o > x.sections[strdata].n || n > x.sections[strdata].n-o {
			return bad()
		}
		end = o + n
		s := x.textBytes(i)
		if !utf8.Valid(s) || bytes.IndexByte(s, 0) >= 0 {
			return bad()
		}
		if i == 0 && len(s) != 0 {
			return bad()
		}
		if i > 0 && bytes.Compare(x.textBytes(i-1), s) >= 0 {
			return bad()
		}
	}
	if end != x.sections[strdata].n {
		return bad()
	}
	stringCols := map[int][]uint64{snapshots: {0, 1, 2, 3, 4}, contexts: {0, 2, 3, 4, 5, 6}, sources: {0, 2, 3}, symbols: {0, 1, 2, 3, 4, 5}, facts: {0, 1, 6, 7, 8, 13, 14, 15}}
	prefixes := map[int]string{snapshots: "snapshot:", contexts: "context:", sources: "source:", symbols: "symbol:", facts: "occurrence:"}
	for t, cols := range stringCols {
		for i := uint64(0); i < x.sections[t].n; i++ {
			for _, c := range cols {
				if x.field(t, i, c) >= x.sections[strdir].n {
					return bad()
				}
			}
			id := x.textBytes(x.field(t, i, 0))
			prefix := []byte(prefixes[t])
			if !bytes.HasPrefix(id, prefix) || !hexBytes(id[len(prefix):], 64) {
				return bad()
			}
			if t == facts {
				id = x.textBytes(x.field(t, i, 1))
				if !bytes.HasPrefix(id, []byte("binding:")) || !hexBytes(id[len("binding:"):], 64) {
					return bad()
				}
			}
			if i > 0 && x.field(t, i-1, 0) >= x.field(t, i, 0) {
				return bad()
			}
		}
	}
	is := func(t int, i, c uint64, s string) bool { return bytes.Equal(x.textBytes(x.field(t, i, c)), []byte(s)) }
	for i := uint64(0); i < x.sections[snapshots].n; i++ {
		if len(x.textBytes(x.field(snapshots, i, 1))) == 0 || !hexBytes(x.textBytes(x.field(snapshots, i, 4)), 64) {
			return bad()
		}
	}
	for i := uint64(0); i < x.sections[contexts].n; i++ {
		if x.field(contexts, i, 1) >= x.sections[snapshots].n || !is(contexts, i, 6, "complete") || !pathBytes(x.textBytes(x.field(contexts, i, 2))) || !hexBytes(x.textBytes(x.field(contexts, i, 3)), 64) {
			return bad()
		}
	}
	for i := uint64(0); i < x.sections[sources].n; i++ {
		if x.field(sources, i, 1) >= x.sections[snapshots].n || x.field(sources, i, 5) > 1 || !pathBytes(x.textBytes(x.field(sources, i, 2))) || !hexBytes(x.textBytes(x.field(sources, i, 3)), 64) {
			return bad()
		}
	}
	for i := uint64(0); i < x.sections[symbols].n; i++ {
		if !(is(symbols, i, 2, "project") || is(symbols, i, 2, "assembly") || is(symbols, i, 2, "package")) || !(is(symbols, i, 5, "documentation_comment_id") || is(symbols, i, 5, "location_fallback") || (is(symbols, i, 5, "constructed_interface_method_v1") || is(symbols, i, 5, "constructed_interface_method_v2")) || is(symbols, i, 5, "constructed_named_type_v1")) {
			return bad()
		}
	}
	for i := uint64(0); i < x.sections[symbols].n; i++ {
		if is(symbols, i, 5, "constructed_named_type_v1") && semantic.ValidateConstructedNamedType(x.symbol(i).Key) != nil {
			return fmt.Errorf("semanticindex: invalid constructed type")
		}
		if (is(symbols, i, 5, "constructed_interface_method_v1") || is(symbols, i, 5, "constructed_interface_method_v2")) && semantic.ValidateConstructedInterfaceMethod(x.symbol(i).Key) != nil {
			return bad()
		}
	}
	// Memberships retain even source trees without occurrences.
	for i := uint64(0); i < x.sections[memberships].n; i++ {
		s, c := x.field(memberships, i, 0), x.field(memberships, i, 1)
		if s >= x.sections[sources].n || c >= x.sections[contexts].n || x.field(sources, s, 1) != x.field(contexts, c, 1) {
			return bad()
		}
		if i > 0 && !x.memberLess(i-1, i) {
			return bad()
		}
	}
	var ndefs uint64
	end = 0
	for i := uint64(0); i < x.sections[facts].n; i++ {
		s, c := x.field(facts, i, 2), x.field(facts, i, 3)
		if s >= x.sections[sources].n || c >= x.sections[contexts].n || x.field(sources, s, 1) != x.field(contexts, c, 1) || !x.hasMembership(s, c) {
			return bad()
		}
		off, n := x.field(facts, i, 4), x.field(facts, i, 5)
		size := x.field(sources, s, 4)
		if n == 0 || off > size || n > size-off {
			return bad()
		}
		decl := is(facts, i, 6, "declaration")
		if !decl && !is(facts, i, 6, "reference") {
			return bad()
		}
		declarationKind := is(facts, i, 7, "declaration") || is(facts, i, 7, "constructor_declaration")
		if decl != declarationKind || !(declarationKind || is(facts, i, 7, "name") || is(facts, i, 7, "invocation") || is(facts, i, 7, "constructor")) {
			return bad()
		}
		sym, enc, start, count := x.field(facts, i, 9), x.field(facts, i, 10), x.field(facts, i, 11), x.field(facts, i, 12)
		if sym > x.sections[symbols].n || enc > x.sections[symbols].n || start != end || start > x.sections[candidates].n || count > x.sections[candidates].n-start {
			return bad()
		}
		end = start + count
		resolved := is(facts, i, 8, "resolved")
		if resolved {
			if sym == 0 || count != 0 {
				return bad()
			}
			if decl {
				ndefs++
			}
		} else {
			if sym != 0 {
				return bad()
			}
			if is(facts, i, 8, "ambiguous") {
				if count < 2 {
					return bad()
				}
			} else if !is(facts, i, 8, "unresolved") && !is(facts, i, 8, "unsupported") {
				return bad()
			}
		}
		for j := start; j < end; j++ {
			v := x.field(candidates, j, 0)
			if v >= x.sections[symbols].n || (j > start && x.field(candidates, j-1, 0) >= v) {
				return bad()
			}
		}
		if x.field(facts, i, 14) != x.field(contexts, c, 4) || x.field(facts, i, 15) != x.field(contexts, c, 5) || len(x.textBytes(x.field(facts, i, 13))) == 0 {
			return bad()
		}
	}
	if end != x.sections[candidates].n || x.sections[positions].n != x.sections[facts].n || x.sections[definitions].n != ndefs {
		return bad()
	}
	for _, t := range []int{positions, definitions} {
		for i := uint64(0); i < x.sections[t].n; i++ {
			v := x.field(t, i, 0)
			if v >= x.sections[facts].n {
				return bad()
			}
			if t == definitions && !(is(facts, v, 6, "declaration") && is(facts, v, 8, "resolved")) {
				return bad()
			}
			if i > 0 && !x.less(x.field(t, i-1, 0), v, t == definitions) {
				return bad()
			}
		}
	}
	if err := x.validateDomainFacts(); err != nil {
		return err
	}
	if err := x.validateContractPostings(); err != nil {
		return err
	}
	if err := x.validateImplementationFacts(); err != nil {
		return err
	}
	if err := x.validateInterfacePostings(); err != nil {
		return err
	}
	return x.validateCallerPostings()
}

func (x *Index) memberKey(row uint64) [4]uint64 {
	s, c := x.field(memberships, row, 0), x.field(memberships, row, 1)
	return [4]uint64{x.field(snapshots, x.field(sources, s, 1), 1), x.field(sources, s, 2), c, s}
}
func lessKey(a, b [4]uint64) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
func (x *Index) memberLess(a, b uint64) bool { return lessKey(x.memberKey(a), x.memberKey(b)) }
func (x *Index) hasMembership(s, c uint64) bool {
	want := [4]uint64{x.field(snapshots, x.field(sources, s, 1), 1), x.field(sources, s, 2), c, s}
	lo, hi := uint64(0), x.sections[memberships].n
	for lo < hi {
		m := (lo + hi) / 2
		if lessKey(x.memberKey(m), want) {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo < x.sections[memberships].n && x.memberKey(lo) == want
}

func hexBytes(b []byte, n int) bool {
	if len(b) != n {
		return false
	}
	for _, c := range b {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func pathBytes(b []byte) bool {
	if len(b) == 0 || b[0] == '/' || b[len(b)-1] == '/' || bytes.ContainsAny(b, "\\:\x00") {
		return false
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '/')
		if i < 0 {
			i = len(b)
		}
		part := b[:i]
		if len(part) == 0 || bytes.Equal(part, []byte(".")) || bytes.Equal(part, []byte("..")) {
			return false
		}
		if i == len(b) {
			return true
		}
		b = b[i+1:]
	}
	return true
}
