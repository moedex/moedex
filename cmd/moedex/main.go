// Command moedex is a slice-1 poking tool: index one git repo in memory and run
// a literal or regex query, printing path:line results.
//
// Usage:
//
//	moedex -repo DIR [-regex] PATTERN
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/search"
	"moedex/internal/version"
)

func main() {
	repo := flag.String("repo", ".", "path to a git repo to index")
	isRegex := flag.Bool("regex", false, "treat PATTERN as a regular expression")
	showVersion := flag.Bool("version", false, "print build identity and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Line("moedex", false))
		return
	}
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: moedex -repo DIR [-regex] PATTERN")
		os.Exit(2)
	}
	pattern := flag.Arg(0)

	files, err := ingest.Repo(*repo, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
	ix := index.New()
	for _, f := range files {
		ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
	}
	fmt.Fprintf(os.Stderr, "indexed %d files into %d blobs\n", len(files), ix.NumBlobs())

	var matches []search.Match
	if *isRegex {
		matches, err = search.Regex(context.Background(), ix, pattern)
		if err != nil {
			fmt.Fprintln(os.Stderr, "regex:", err)
			os.Exit(1)
		}
	} else {
		matches, err = search.Literal(context.Background(), ix, pattern)
		if err != nil {
			fmt.Fprintln(os.Stderr, "literal:", err)
			os.Exit(1)
		}
	}

	for _, m := range matches {
		fmt.Printf("%s:%d\n", m.RelPath, m.Line)
	}
	fmt.Fprintf(os.Stderr, "%d matching lines\n", len(matches))
}
