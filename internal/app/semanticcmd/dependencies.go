package semanticcmd

import (
	"context"
	"moedex/internal/semanticrun"
)

// PackDependencies validates and copies an explicitly supplied extracted cache.
// No ambient NuGet cache or network source is consulted.
func PackDependencies(ctx context.Context, packages, output string) (semanticrun.DependencyPackSummary, error) {
	return semanticrun.PackDependencyBundle(ctx, packages, output)
}

func PackDependenciesWithLimit(ctx context.Context, packages, output string, maxBytes int64) (semanticrun.DependencyPackSummary, error) {
	return semanticrun.PackDependencyBundleWithLimit(ctx, packages, output, maxBytes)
}
