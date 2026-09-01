// Command scale indexes every git repo under a root directory into one
// in-memory index and reports size/throughput stats. Used to find where the
// slice-1 in-memory engine starts to strain.
//
// Usage: scale ROOT [sampleRegex]
//
// Environment knobs:
//
//	MOEDEX_MMAP=1            persist, reload with postings mmap'd, report heap delta.
//	MOEDEX_SELECTIVE=<frac>  opt-in FREE-style selective index: keep a trigram only
//	                         if it occurs in at most <frac> of blobs (e.g. 0.5). The
//	                         all-trigram path is the default (knob unset). Parity is
//	                         preserved by the IndexedGram force-scan fallback.
package scalecmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/search"
	"moedex/internal/version"
)

// versionRequested reports whether the first scale argument asks to print the
// build identity instead of running a scale pass against a corpus root.
func versionRequested(args []string) bool {
	return len(args) > 0 && (args[0] == "-version" || args[0] == "--version")
}

func Main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scale ROOT [sampleRegex]")
		os.Exit(2)
	}
	if versionRequested(os.Args[1:]) {
		fmt.Println(version.Line("scale", false))
		return
	}
	root := os.Args[1]
	sample := ""
	if len(os.Args) > 2 {
		sample = os.Args[2]
	}
	if err := run(root, sample); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root, sample string) error {
	// Optional selective build (MOEDEX_SELECTIVE=<frac>). nil selector => default
	// all-trigram path, so existing scale runs are unchanged.
	var sel index.GramSelector
	if v := os.Getenv("MOEDEX_SELECTIVE"); v != "" {
		frac, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("bad MOEDEX_SELECTIVE=%q: %w", v, err)
		}
		sel = index.FrequencyThresholdSelector{MaxDocFraction: frac}
		fmt.Printf("selective build: %s\n", sel.Describe())
	}

	repos, err := ingest.DiscoverRepos(root)
	if err != nil {
		return fmt.Errorf("discover repos under %s: %w", root, err)
	}
	for _, repo := range repos {
		if _, err := ingest.AIPrivacyFingerprint(repo); err != nil {
			return fmt.Errorf("privacy preflight: %w", err)
		}
	}
	fmt.Printf("found %d git repos under %s\n", len(repos), root)

	sb := index.NewBuildTarget(sel)
	var totalFiles int
	var totalBytes int64
	start := time.Now()
	for _, r := range repos {
		files, err := ingest.Repo(filepath.Base(r), r)
		if err != nil {
			if ingest.IsPrivacyPolicyError(err) {
				return fmt.Errorf("privacy preflight: %w", err)
			}
			fmt.Fprintf(os.Stderr, "  skip %s: %v\n", r, err)
			continue
		}
		for _, f := range files {
			sb.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			totalFiles++
			totalBytes += int64(len(f.Content))
		}
	}
	ix := sb.Finalize()
	buildDur := time.Since(start)

	var postings int64
	for _, t := range ix.Trigrams() {
		postings += int64(len(ix.Postings(t)))
	}

	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)

	fmt.Printf("\n=== build ===\n")
	fmt.Printf("text files indexed : %d\n", totalFiles)
	fmt.Printf("unique blobs       : %d  (dedup: %.1f%% of files were duplicate content)\n",
		ix.NumBlobs(), 100*float64(totalFiles-ix.NumBlobs())/float64(max(totalFiles, 1)))
	fmt.Printf("content bytes       : %.1f MB\n", float64(totalBytes)/1e6)
	fmt.Printf("distinct trigrams  : %d\n", len(ix.Trigrams()))
	fmt.Printf("total postings     : %d\n", postings)
	fmt.Printf("build time         : %s  (%.1f MB/s)\n", buildDur.Round(time.Millisecond),
		float64(totalBytes)/1e6/buildDur.Seconds())
	fmt.Printf("heap in use        : %.1f MB  (%.2fx content)\n",
		float64(ms.HeapAlloc)/1e6, float64(ms.HeapAlloc)/float64(max64(totalBytes, 1)))

	if sample != "" {
		fmt.Printf("\n=== sample regex query: %q ===\n", sample)
		qStart := time.Now()
		m, err := search.Regex(context.Background(), ix, sample)
		if err != nil {
			return fmt.Errorf("query error: %w", err)
		}
		fmt.Printf("%d matching lines in %s\n", len(m), time.Since(qStart).Round(time.Microsecond))
	}

	// MOEDEX_MMAP=1: persist, drop the in-RAM index, reload with postings mmap'd,
	// and report the heap difference — the slice-3 memory-wall result.
	if os.Getenv("MOEDEX_MMAP") == "1" {
		tmp, err := os.CreateTemp("", "moedex-*.idx")
		if err != nil {
			fmt.Fprintln(os.Stderr, "tempfile:", err)
			return nil
		}
		tmp.Close()
		defer os.Remove(tmp.Name())

		if err := diskstore.Save(ix, tmp.Name()); err != nil {
			fmt.Fprintln(os.Stderr, "save:", err)
			return nil
		}
		fi, _ := os.Stat(tmp.Name())
		ix = nil // let the in-RAM index be collected before we measure
		runtime.GC()

		loaded, closer, err := diskstore.LoadMmap(tmp.Name())
		if err != nil {
			fmt.Fprintln(os.Stderr, "mmap load:", err)
			return nil
		}
		defer closer.Close()

		var lms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&lms)

		fmt.Printf("\n=== mmap-loaded ===\n")
		fmt.Printf("on-disk index size : %.1f MB\n", float64(fi.Size())/1e6)
		fmt.Printf("heap in use        : %.1f MB  (%.2fx content; was %.2fx in-RAM)\n",
			float64(lms.HeapAlloc)/1e6,
			float64(lms.HeapAlloc)/float64(max64(totalBytes, 1)),
			float64(ms.HeapAlloc)/float64(max64(totalBytes, 1)))
		if sample != "" {
			qStart := time.Now()
			m, err := search.Regex(context.Background(), loaded, sample)
			if err == nil {
				fmt.Printf("sample query       : %d lines in %s (off mmap)\n",
					len(m), time.Since(qStart).Round(time.Microsecond))
			}
		}
	}
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
