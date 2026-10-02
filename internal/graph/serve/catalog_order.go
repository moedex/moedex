package graphserve

import (
	"strconv"

	"moedex/internal/graph/diskgraph"
)

// lessGraphKeyID preserves the public string ID ordering (including decimal,
// rather than numeric, offsets), without constructing whole IDs for the usual
// distinct fixed-width Git identities. The fallback preserves arbitrary test
// keys and prefix cases too.
func lessGraphKeyID(a, b diskgraph.Key) bool {
	if a.BlobSHA == b.BlobSHA {
		if a.SymbolOffset == b.SymbolOffset {
			return false
		}
		return strconv.FormatUint(a.SymbolOffset, 10) < strconv.FormatUint(b.SymbolOffset, 10)
	}
	if len(a.BlobSHA) == len(b.BlobSHA) {
		return a.BlobSHA < b.BlobSHA
	}
	return keyID(a) < keyID(b)
}
