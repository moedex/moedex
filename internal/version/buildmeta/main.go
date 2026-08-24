package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"moedex/internal/version/sourcehash"
)

func main() {
	checkClean := flag.Bool("check-clean", false, "fail unless the Git worktree is clean")
	flag.Parse()
	ctx := context.Background()
	if *checkClean {
		if err := sourcehash.CheckClean(ctx, "."); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	digest, err := sourcehash.Digest(ctx, ".")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(digest)
}
