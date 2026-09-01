package eval

import (
	"context"
	"hash/fnv"
	"strings"

	"moedex/internal/embed"
	"moedex/internal/tokenindex"
)

// conceptEmbedder is a deterministic, network-free embed.Embedder for the eval
// harness. It exists so the dense/hybrid arm can be measured hermetically in CI
// without a local embedding server.
//
// It is NOT a hashed bag-of-words (that would make the dense arm a slower copy
// of the lexical arm and prove nothing). Instead it projects text onto a small
// fixed set of CONCEPT axes via a hand-authored lexicon: tokens that are
// semantically related — but lexically distinct — map to the same axis. So
// "reverse"/"reversal" and "refund"/"chargeback" all load the "refund" axis, and
// a query "reverse a payment" lands near refund content whose trigrams it shares
// none of. That is the one thing the dense arm can do that BM25 cannot, and it is
// what the lexical-vs-dense-vs-hybrid comparison is meant to expose.
//
// Tokens not in the lexicon still contribute (hashed into the tail axes) so that
// unrelated documents do not all collapse to the zero vector and tie. The result
// is a low-dimensional vector where cosine similarity tracks shared CONCEPTS, not
// shared substrings.
type conceptEmbedder struct {
	axes    []string       // concept axis names, index = axis id
	lexicon map[string]int // token -> concept axis id
	tailDim int            // extra hashed axes for out-of-lexicon tokens
}

// conceptAxes is the hand-authored concept taxonomy. Each axis lists the tokens
// (already lowercased; identifier-split where relevant) that load it. The labels
// are chosen so that a query phrased with a synonym maps to the same axis as the
// implementing code's own vocabulary — the semantic bridge lexical search lacks.
var conceptAxes = map[string][]string{
	// "refund / reverse a captured charge" concept.
	"refund": {"refund", "reverse", "reversal", "reverses", "chargeback", "reimburse"},
	// "charge / capture a payment" concept.
	"charge": {"charge", "capture", "captured", "payment", "pay", "billing"},
	// "order" concept.
	"order": {"order", "orders", "purchase", "transaction"},
	// "ssl certificate issuance" concept.
	"ssl": {"ssl", "certificate", "cert", "tls", "issue", "issuance"},
	// "search / index" concept.
	"index": {"index", "trigram", "search", "lookup"},
	// "ci / build pipeline" concept.
	"build": {"build", "deploy", "pipeline", "stage", "compile"},
	// "authentication / login" concept (used by a dense-favoring query).
	"auth": {"auth", "authenticate", "login", "credential", "credentials", "password", "signin"},
}

// newConceptEmbedder builds the embedder with tailDim hashed fallback axes on top
// of the concept axes. tailDim>=1 keeps out-of-lexicon-only texts non-zero.
func newConceptEmbedder(tailDim int) *conceptEmbedder {
	ce := &conceptEmbedder{
		lexicon: map[string]int{},
		tailDim: tailDim,
	}
	// Deterministic axis order: sort the concept names so axis ids are stable.
	names := make([]string, 0, len(conceptAxes))
	for name := range conceptAxes {
		names = append(names, name)
	}
	// simple insertion sort to avoid importing sort just for this
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for id, name := range names {
		ce.axes = append(ce.axes, name)
		for _, tok := range conceptAxes[name] {
			ce.lexicon[tok] = id
		}
	}
	return ce
}

func (c *conceptEmbedder) Dim() int { return len(c.axes) + c.tailDim }

// Embed projects each text onto the concept axes. We tokenize with the SAME
// tokenizer the ranker uses (tokenindex.Tokenize) so identifier splitting matches
// — e.g. "RefundOrder" -> refund, order, refundorder — and concept tokens are
// found inside CamelCase names. Concept-lexicon tokens load their axis; remaining
// tokens hash into the tail axes so unrelated docs stay distinguishable.
func (c *conceptEmbedder) Embed(_ context.Context, texts []string) ([]embed.Vector, error) {
	dim := c.Dim()
	out := make([]embed.Vector, len(texts))
	for i, t := range texts {
		v := make(embed.Vector, dim)
		toks := tokenindex.Tokenize([]byte(t))
		// Fall back to whitespace fields for query strings that the trigram
		// tokenizer might leave joined; union both so concept words are caught.
		toks = append(toks, strings.Fields(strings.ToLower(t))...)
		for _, tok := range toks {
			if axis, ok := c.lexicon[tok]; ok {
				v[axis] += 2 // concept tokens weigh more than incidental hashing
				continue
			}
			if c.tailDim > 0 {
				h := fnv.New32a()
				h.Write([]byte(tok))
				v[len(c.axes)+int(h.Sum32()%uint32(c.tailDim))]++
			}
		}
		out[i] = v
	}
	return out, nil
}
