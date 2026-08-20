//go:build onnx

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"moedex/internal/embed"
	"moedex/internal/server"
)

func buildSnapshotDense(dir string) error {
	intraThreads, err := graphIntEnv("MOEDEX_ONNX_INTRA_OP_THREADS", 0)
	if err != nil {
		return err
	}
	interThreads, err := graphIntEnv("MOEDEX_ONNX_INTER_OP_THREADS", 0)
	if err != nil {
		return err
	}
	embedder, err := embed.NewONNXEmbedderWithOptions(os.Getenv("ONNXRUNTIME_LIB_PATH"), embed.ONNXOptions{
		IntraOpThreads: intraThreads,
		InterOpThreads: interThreads,
	})
	if err != nil {
		return err
	}
	defer embedder.Close()
	started := time.Now()
	last := time.Time{}
	_, err = server.RefreshEmbeddings(context.Background(), dir, server.RankConfig{
		Emb:        embedder,
		EmbedModel: "st-codesearch-distilroberta-onnx",
		StorePath:  filepath.Join(dir, "corpus-embeddings.store"),
		EmbeddingProgress: func(progress embed.BuildProgress) {
			now := time.Now()
			if progress.Embedded != 0 && progress.Embedded != progress.Total && now.Sub(last) < 10*time.Second {
				return
			}
			last = now
			fmt.Fprintf(os.Stderr, "moedex-index: snapshot dense progress %d/%d (%.1f%%), elapsed=%s\n",
				progress.Embedded, progress.Total, snapshotPercent(progress.Embedded, progress.Total), now.Sub(started).Round(time.Second))
		},
	})
	return err
}

func snapshotPercent(done, total int) float64 {
	if total == 0 {
		return 100
	}
	return 100 * float64(done) / float64(total)
}
