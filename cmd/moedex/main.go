package main

import (
	"context"
	"os"

	"moedex/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args))
}
