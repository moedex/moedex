package semanticindex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"moedex/internal/semantic"
)

const maxDomainJSONBytes = 64 << 10

// Domain payloads are one bounded canonical JSON array per fact. The directory
// is ordered by fact row, so a position lookup never scans unrelated domains.
func (x *Index) domainPayload(fact uint64) []byte {
	i := sort.Search(int(x.sections[domainFacts].n), func(i int) bool { return x.field(domainFacts, uint64(i), 0) >= fact })
	if uint64(i) == x.sections[domainFacts].n || x.field(domainFacts, uint64(i), 0) != fact {
		return nil
	}
	return x.textBytes(x.field(domainFacts, uint64(i), 1))
}
func (x *Index) domainSymbolRow(id string) (uint64, bool) {
	sid, ok := x.strID(id)
	if !ok {
		return 0, false
	}
	row := sort.Search(int(x.sections[symbols].n), func(i int) bool { return x.field(symbols, uint64(i), 0) >= sid })
	return uint64(row), uint64(row) < x.sections[symbols].n && x.field(symbols, uint64(row), 0) == sid
}
func (x *Index) validateDomainFacts() error {
	bad := func() error { return fmt.Errorf("semanticindex: invalid domain facts") }
	// Interned payloads and symbols can be shared by many occurrences. Validate
	// each unique payload and (payload, API) pair once, bounding startup work by
	// the stored directory and unique content instead of repeatedly parsing it.
	payloads := map[uint64][]semantic.DomainFact{}
	decodedSymbols := map[uint64]semantic.Symbol{}
	symbolAt := func(row uint64) semantic.Symbol {
		if s, ok := decodedSymbols[row]; ok {
			return s
		}
		s := x.symbol(row)
		decodedSymbols[row] = s
		return s
	}

	validatedAPI := map[[2]uint64]bool{}
	for i := uint64(0); i < x.sections[domainFacts].n; i++ {
		fact, sid := x.field(domainFacts, i, 0), x.field(domainFacts, i, 1)
		if fact >= x.sections[facts].n || sid >= x.sections[strdir].n || (i > 0 && x.field(domainFacts, i-1, 0) >= fact) {
			return bad()
		}
		is := func(col uint64, value string) bool {
			return bytes.Equal(x.textBytes(x.field(facts, fact, col)), []byte(value))
		}
		if !is(8, "resolved") || !is(6, "reference") || !is(13, "roslyn-semantic-model") || !is(14, "msbuild-roslyn") {
			return bad()
		}
		domains, ok := payloads[sid]
		if !ok {
			raw := x.textBytes(sid)
			if len(raw) == 0 || len(raw) > maxDomainJSONBytes {
				return bad()
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&domains); err != nil || len(domains) == 0 {
				return bad()
			}
			if err := semantic.ValidateDomainFacts(domains); err != nil {
				return bad()
			}
			canonical, err := json.Marshal(domains)
			if err != nil || !bytes.Equal(raw, canonical) {
				return bad()
			}
			for _, f := range domains {
				var targets []semantic.Symbol
				for _, target := range f.Targets {
					row, found := x.domainSymbolRow(target.SymbolID)
					if !found {
						return bad()
					}
					targets = append(targets, symbolAt(row))
					if semantic.ValidateDomainFactTarget(f, target.Role, symbolAt(row)) != nil {
						return bad()
					}
				}
				if semantic.ValidateDomainTargetCorrespondence(f, targets) != nil {
					return bad()
				}
			}
			payloads[sid] = domains
		}
		for _, domain := range domains {
			if semantic.ValidateDomainExtractor(domain, x.text(x.field(facts, fact, 15))) != nil {
				return bad()
			}
		}
		api := x.field(facts, fact, 9) - 1 // Core validation requires a resolved symbol.
		pair := [2]uint64{sid, api}
		if !validatedAPI[pair] {
			for _, f := range domains {
				if semantic.ValidateDomainAPI(f, symbolAt(api)) != nil {
					return bad()
				}
			}
			validatedAPI[pair] = true
		}
	}
	return nil
}
func (x *Index) domainResultCost(fact uint64) uint64 {
	raw := x.domainPayload(fact)
	if len(raw) == 0 {
		return 0
	}
	var domains []semantic.DomainFact
	_ = json.Unmarshal(raw, &domains) // Open validated the immutable payload.
	cost := uint64(len(raw))*2 + 1024
	for _, f := range domains {
		for _, target := range f.Targets {
			row, _ := x.domainSymbolRow(target.SymbolID)
			cost += 2 * x.rowCost(symbols, row, 0, 1, 2, 3, 4, 5)
		}
	}
	return cost
}
func (x *Index) resultDomainFacts(fact uint64) ([]semantic.DomainFact, []semantic.Symbol) {
	raw := x.domainPayload(fact)
	if len(raw) == 0 {
		return nil, nil
	}
	var domains []semantic.DomainFact
	_ = json.Unmarshal(raw, &domains)
	var targets []semantic.Symbol
	seen := map[string]bool{}
	for _, f := range domains {
		for _, target := range f.Targets {
			if !seen[target.SymbolID] {
				row, _ := x.domainSymbolRow(target.SymbolID)
				targets = append(targets, x.symbol(row))
				seen[target.SymbolID] = true
			}
		}
	}
	return domains, targets
}
