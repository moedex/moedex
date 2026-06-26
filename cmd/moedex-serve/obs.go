package main

import (
	"expvar"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
)

// Observability is stdlib-only by design: expvar for monotone counters and a
// hand-rolled fixed-bucket histogram for latency, rendered as Prometheus text
// exposition v0.0.4 at scrape time. No prometheus/client_golang, no new module.

// histogram is a fixed-bucket cumulative latency histogram. Buckets are upper
// bounds in seconds; Prometheus semantics are cumulative ("le"), so each
// observation increments every bucket whose bound it does not exceed. count/sum
// back the _count and _sum series.
type histogram struct {
	bounds  []float64
	mu      sync.Mutex
	buckets []uint64
	count   uint64
	sum     float64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{bounds: bounds, buckets: make([]uint64, len(bounds))}
}

func (h *histogram) observe(v float64) {
	h.mu.Lock()
	for i, b := range h.bounds {
		if v <= b {
			h.buckets[i]++
		}
	}
	h.count++
	h.sum += v
	h.mu.Unlock()
}

// snapshot copies the histogram state under the lock so the scrape never races
// concurrent observations.
func (h *histogram) snapshot() (buckets []uint64, count uint64, sum float64) {
	h.mu.Lock()
	buckets = append([]uint64(nil), h.buckets...)
	count, sum = h.count, h.sum
	h.mu.Unlock()
	return
}

// metrics holds the daemon's counters and the request-latency histogram. The
// expvar.Map keys are stable label values (code class, reload result) so the
// scrape can expand them into labeled Prometheus series.
type metrics struct {
	requests *expvar.Map // by code class: "2xx" | "4xx" | "5xx"
	panics   *expvar.Int
	reloads  *expvar.Map // by result: "ok" | "fail"
	dur      *histogram
}

var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// newMetrics constructs the metric set and publishes it to the global expvar
// registry. expvar.NewMap/NewInt panic on a duplicate name, so this must run at
// most once per process — runHTTP calls it once. Tests use newUnpublishedMetrics.
func newMetrics() *metrics {
	return &metrics{
		requests: expvar.NewMap("moedex_http_requests_total"),
		panics:   expvar.NewInt("moedex_http_panics_total"),
		reloads:  expvar.NewMap("moedex_reloads_total"),
		dur:      newHistogram(defaultBuckets),
	}
}

// newUnpublishedMetrics builds a metric set with the same shape but NOT
// registered in the global expvar registry, so it can be constructed many times
// (e.g. once per test) without tripping expvar's duplicate-name panic.
func newUnpublishedMetrics() *metrics {
	return &metrics{
		requests: new(expvar.Map).Init(),
		panics:   new(expvar.Int),
		reloads:  new(expvar.Map).Init(),
		dur:      newHistogram(defaultBuckets),
	}
}

// observe records one finished request: its latency and its status-code class.
func (m *metrics) observe(code int, seconds float64) {
	m.requests.Add(codeClass(code), 1)
	m.dur.observe(seconds)
}

func (m *metrics) incPanic()             { m.panics.Add(1) }
func (m *metrics) incReload(result string) { m.reloads.Add(result, 1) }

// codeClass buckets an HTTP status into the Prometheus-conventional class label.
func codeClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 200 && code < 300:
		return "2xx"
	default:
		return "other"
	}
}

// metricsHandler renders the full text exposition. Corpus gauges are read at
// scrape time through the holder (acquire/release) so they reflect the live
// generation even across a hot swap.
func metricsHandler(holder *corpusHolder, m *metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		shards := snap.c.NumShards()
		blobs := snap.c.NumBlobs()
		snap.release()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		writeCounterMap(w, "moedex_http_requests_total",
			"Total HTTP requests by status-code class.", "code", m.requests)

		fmt.Fprintf(w, "# HELP moedex_http_panics_total Total handler panics recovered.\n")
		fmt.Fprintf(w, "# TYPE moedex_http_panics_total counter\n")
		fmt.Fprintf(w, "moedex_http_panics_total %d\n", m.panics.Value())

		writeCounterMap(w, "moedex_reloads_total",
			"Total shard reloads by result.", "result", m.reloads)

		// Latency histogram, cumulative buckets + sum + count.
		buckets, count, sum := m.dur.snapshot()
		fmt.Fprintf(w, "# HELP moedex_http_request_duration_seconds HTTP request latency.\n")
		fmt.Fprintf(w, "# TYPE moedex_http_request_duration_seconds histogram\n")
		for i, b := range m.dur.bounds {
			fmt.Fprintf(w, "moedex_http_request_duration_seconds_bucket{le=\"%s\"} %d\n",
				formatBound(b), buckets[i])
		}
		fmt.Fprintf(w, "moedex_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", count)
		fmt.Fprintf(w, "moedex_http_request_duration_seconds_sum %s\n", strconv.FormatFloat(sum, 'g', -1, 64))
		fmt.Fprintf(w, "moedex_http_request_duration_seconds_count %d\n", count)

		// Corpus gauges, read live at scrape time.
		fmt.Fprintf(w, "# HELP moedex_corpus_shards Mmap'd shards in the live corpus.\n")
		fmt.Fprintf(w, "# TYPE moedex_corpus_shards gauge\n")
		fmt.Fprintf(w, "moedex_corpus_shards %d\n", shards)
		fmt.Fprintf(w, "# HELP moedex_corpus_blobs Blobs in the live corpus.\n")
		fmt.Fprintf(w, "# TYPE moedex_corpus_blobs gauge\n")
		fmt.Fprintf(w, "moedex_corpus_blobs %d\n", blobs)
	}
}

// rankMetricsHandler renders the metrics exposition for the warm MCP-over-HTTP
// daemon: the same request/panic/reload families as the retrieval daemon, plus
// ranked-corpus gauges (blobs, BM25 docs, symbol blobs, dense chunks) read live
// through the rankHolder so they reflect the current generation across a hot swap.
func rankMetricsHandler(holder *rankHolder, m *metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := holder.acquire()
		blobs := snap.rc.NumBlobs()
		docs := snap.rc.NumDocs()
		symBlobs := snap.rc.NumSymbolBlobs()
		dense := snap.rc.DenseChunks()
		snap.release()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		writeCounterMap(w, "moedex_http_requests_total",
			"Total HTTP requests by status-code class.", "code", m.requests)

		fmt.Fprintf(w, "# HELP moedex_http_panics_total Total handler panics recovered.\n")
		fmt.Fprintf(w, "# TYPE moedex_http_panics_total counter\n")
		fmt.Fprintf(w, "moedex_http_panics_total %d\n", m.panics.Value())

		writeCounterMap(w, "moedex_reloads_total",
			"Total ranker reloads by result.", "result", m.reloads)

		buckets, count, sum := m.dur.snapshot()
		fmt.Fprintf(w, "# HELP moedex_http_request_duration_seconds HTTP request latency.\n")
		fmt.Fprintf(w, "# TYPE moedex_http_request_duration_seconds histogram\n")
		for i, b := range m.dur.bounds {
			fmt.Fprintf(w, "moedex_http_request_duration_seconds_bucket{le=\"%s\"} %d\n",
				formatBound(b), buckets[i])
		}
		fmt.Fprintf(w, "moedex_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", count)
		fmt.Fprintf(w, "moedex_http_request_duration_seconds_sum %s\n", strconv.FormatFloat(sum, 'g', -1, 64))
		fmt.Fprintf(w, "moedex_http_request_duration_seconds_count %d\n", count)

		writeGauge(w, "moedex_corpus_blobs", "Blobs in the live ranked corpus.", blobs)
		writeGauge(w, "moedex_corpus_docs", "BM25 documents in the live ranked corpus.", docs)
		writeGauge(w, "moedex_corpus_symbol_blobs", "Symbol-indexed blobs in the live ranked corpus.", symBlobs)
		writeGauge(w, "moedex_corpus_dense_chunks", "Dense embedding chunks in the live ranked corpus.", dense)
	}
}

// writeGauge emits one unlabeled gauge family.
func writeGauge(w http.ResponseWriter, name, help string, v int) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	fmt.Fprintf(w, "%s %d\n", name, v)
}

// writeCounterMap emits one labeled counter family from an expvar.Map, sorting
// keys so the exposition is deterministic.
func writeCounterMap(w http.ResponseWriter, name, help, label string, mp *expvar.Map) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s counter\n", name)
	var keys []string
	mp.Do(func(kv expvar.KeyValue) { keys = append(keys, kv.Key) })
	sort.Strings(keys)
	if len(keys) == 0 {
		// Emit nothing rather than a bare unlabeled series; a family with no
		// observed label values is valid (Prometheus tolerates zero series).
		return
	}
	for _, k := range keys {
		fmt.Fprintf(w, "%s{%s=\"%s\"} %s\n", name, label, k, mp.Get(k).String())
	}
}

// formatBound renders a bucket bound the way Prometheus clients do (e.g. "0.005",
// "1", "10") so dashboards match the conventional bucketing.
func formatBound(b float64) string {
	return strconv.FormatFloat(b, 'g', -1, 64)
}
