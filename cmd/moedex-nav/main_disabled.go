//go:build !lsp

// Default build: moedex-nav is a Condition-1 spike behind `-tags lsp` (ADR 0017)
// and is not compiled into the pure-Go default. This stub keeps `go build ./...`
// green while telling anyone who runs the default binary how to get the real one.

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"moedex-nav: LSP navigation is an optional spike; rebuild with -tags lsp "+
			"(needs gopls on PATH). See docs/adr/0017-lsp-navigation-and-the-serena-boundary.md")
	os.Exit(1)
}
