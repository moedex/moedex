//go:build onnx

// This file is compiled only with `-tags onnx`. It embeds a code-trained
// sentence embedder — st-codesearch-distilroberta-base (CodeSearchNet-trained,
// 768-d), int8-quantized to ~78MB — and its tokenizer directly in the binary and
// runs inference via the ONNX Runtime Go binding, with no external embedding
// service. The default build excludes this file (see onnx_disabled.go), so the
// core keeps zero ML deps.
//
// This model was chosen over general-text all-MiniLM after a head-to-head on the
// gold corpus (see eval.TestCorpusCodeModelMeasurement): the code-trained model
// won in the RRF hybrid where general-text all-MiniLM was ~neutral on code. On the
// reconciled two-annotator gold set the full hybrid (lexical+dense+symbol) reaches
// Recall@5 0.87 / NDCG 0.72 vs lexical 0.71 / 0.65 — the dense arm's value is
// RECALL (it recovers files lexical misses), not top-rank precision. int8
// quantization held quality (no regression vs fp32).
//
// The model is a raw sentence-transformers encoder: it outputs token embeddings
// (last_hidden_state, [batch, seq, dim]). We reproduce the sentence-transformers
// pooling here — attention-masked mean over tokens, then L2 normalization — to get
// one vector per input. The ONNX Runtime SHARED LIBRARY must be present at run
// time (pass its path to NewONNXEmbedder or set ONNXRUNTIME_LIB_PATH).
//
// NewONNXEmbedderFromFiles loads an arbitrary HF encoder export (any input set,
// any hidden dim) from disk — used to A/B a code-trained model against the
// bundled all-MiniLM without re-embedding a second model in the binary.

package embed

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log"
	"math"
	"os"
	"sync"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

//go:embed onnxmodel/model.onnx
var onnxModelData []byte

//go:embed onnxmodel/tokenizer.json
var onnxTokenizerData []byte

// Bundled st-codesearch-distilroberta spec: RoBERTa-style 2 inputs (NO
// token_type_ids), 768-d, 256-token cap.
var bundledInputNames = []string{"input_ids", "attention_mask"}

const (
	bundledDim    = 768
	bundledMaxSeq = 256
	outputName    = "last_hidden_state"
)

// ONNXEmbedder runs a HuggingFace encoder export in-process and pools its token
// embeddings into one vector per input. It satisfies Embedder. ONNX Runtime is
// not documented as safe for concurrent Run on one session, so Embed serializes
// inference under a mutex; fine for moedex (batched one-shot at build; cheap
// per-query).
type ONNXEmbedder struct {
	mu         sync.Mutex
	tk         tokenizer.Tokenizer
	session    *ort.DynamicAdvancedSession
	inputNames []string // model input order; may omit token_type_ids (RoBERTa)
	dim        int
	maxSeq     int
}

// NewONNXEmbedder loads the bundled code embedder (embedded in the binary).
// runtimePath points at the ONNX Runtime shared library; empty falls back to
// ONNXRUNTIME_LIB_PATH or the binding's own discovery.
func NewONNXEmbedder(runtimePath string) (*ONNXEmbedder, error) {
	tk, err := pretrained.FromReader(bytes.NewReader(onnxTokenizerData))
	if err != nil {
		return nil, fmt.Errorf("embed/onnx: load tokenizer: %w", err)
	}
	if err := initRuntime(runtimePath); err != nil {
		return nil, err
	}
	session, err := ort.NewDynamicAdvancedSessionWithONNXData(onnxModelData, bundledInputNames, []string{outputName}, nil)
	if err != nil {
		_ = ort.DestroyEnvironment()
		return nil, fmt.Errorf("embed/onnx: create session: %w", err)
	}
	return &ONNXEmbedder{tk: *tk, session: session, inputNames: bundledInputNames, dim: bundledDim, maxSeq: bundledMaxSeq}, nil
}

// NewONNXEmbedderFromFiles loads an encoder ONNX + tokenizer.json from disk.
// inputNames is the model's input order (e.g. ["input_ids","attention_mask"] for
// RoBERTa, which has no token_type_ids); dim is the hidden size; maxSeq caps the
// sequence length. The output must be last_hidden_state [batch, seq, dim].
func NewONNXEmbedderFromFiles(runtimePath, modelPath, tokenizerPath string, inputNames []string, dim, maxSeq int) (*ONNXEmbedder, error) {
	tkData, err := os.ReadFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("embed/onnx: read tokenizer %s: %w", tokenizerPath, err)
	}
	tk, err := pretrained.FromReader(bytes.NewReader(tkData))
	if err != nil {
		return nil, fmt.Errorf("embed/onnx: load tokenizer: %w", err)
	}
	if err := initRuntime(runtimePath); err != nil {
		return nil, err
	}
	if maxSeq <= 0 {
		maxSeq = bundledMaxSeq
	}
	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, []string{outputName}, nil)
	if err != nil {
		_ = ort.DestroyEnvironment()
		return nil, fmt.Errorf("embed/onnx: create session from %s: %w", modelPath, err)
	}
	return &ONNXEmbedder{tk: *tk, session: session, inputNames: inputNames, dim: dim, maxSeq: maxSeq}, nil
}

func initRuntime(runtimePath string) error {
	if runtimePath != "" {
		ort.SetSharedLibraryPath(runtimePath)
	} else if p, ok := os.LookupEnv("ONNXRUNTIME_LIB_PATH"); ok {
		ort.SetSharedLibraryPath(p)
	}
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("embed/onnx: init runtime: %w", err)
	}
	return nil
}

// Embed implements Embedder: tokenize, run the encoder, masked-mean-pool, and L2
// normalize each input into a dim-d vector.
func (e *ONNXEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Tokenize each input sequentially rather than via EncodeBatch. EncodeBatch
	// tokenizes in concurrent goroutines, and the byte-level pre-tokenizer in the
	// forked tokenizer can PANIC on certain real source chunks (an
	// index-out-of-range in normalizer.TransformRange). A panic in a goroutine
	// EncodeBatch spawns is unrecoverable and crashes the whole process mid-build /
	// mid-query. Running each Encode in our own goroutine lets us recover a bad
	// chunk and substitute an empty encoding — which mean-pools to a zero vector, so
	// that chunk simply carries no dense signal — instead of taking the build or the
	// daemon down. We must return one vector per input, so a failed chunk yields a
	// placeholder, never a gap.
	encs := make([]tokenizer.Encoding, len(texts))
	skipped := 0
	var firstBad string
	for i, s := range texts {
		enc, perr := encodeRecover(&e.tk, s)
		if perr != nil {
			if skipped == 0 {
				firstBad = s
			}
			skipped++
			continue // leave encs[i] as the zero Encoding (empty -> zero vector)
		}
		encs[i] = *enc
	}
	if skipped > 0 {
		sample := firstBad
		if len(sample) > 80 {
			sample = sample[:80]
		}
		log.Printf("embed/onnx: %d/%d chunks panicked in the byte-level tokenizer; "+
			"substituting empty embeddings. first sample: %q", skipped, len(texts), sample)
	}

	// Pad/truncate every encoding to a common sequence length so the batch is a
	// dense [batch, seqLen] tensor. Padding positions get mask 0 and drop out of
	// the mean pool.
	seqLen := 0
	for _, en := range encs {
		if n := len(en.Ids); n > seqLen {
			seqLen = n
		}
	}
	if seqLen > e.maxSeq {
		seqLen = e.maxSeq
	}
	if seqLen == 0 {
		seqLen = 1
	}

	batch := len(encs)
	ids := make([]int64, batch*seqLen)
	mask := make([]int64, batch*seqLen)
	types := make([]int64, batch*seqLen)
	for b, en := range encs {
		n := len(en.Ids)
		if n > seqLen {
			n = seqLen
		}
		base := b * seqLen
		for i := 0; i < n; i++ {
			ids[base+i] = int64(en.Ids[i])
			mask[base+i] = int64(en.AttentionMask[i])
			types[base+i] = int64(en.TypeIds[i])
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	shape := ort.NewShape(int64(batch), int64(seqLen))
	// Build only the tensors this model declares, in declared order.
	feed := map[string][]int64{"input_ids": ids, "attention_mask": mask, "token_type_ids": types}
	inputTensors := make([]ort.Value, 0, len(e.inputNames))
	for _, name := range e.inputNames {
		data, ok := feed[name]
		if !ok {
			return nil, fmt.Errorf("embed/onnx: unsupported model input %q", name)
		}
		t, err := ort.NewTensor(shape, data)
		if err != nil {
			return nil, fmt.Errorf("embed/onnx: %s tensor: %w", name, err)
		}
		defer t.Destroy()
		inputTensors = append(inputTensors, t)
	}

	outShape := ort.NewShape(int64(batch), int64(seqLen), int64(e.dim))
	outT, err := ort.NewEmptyTensor[float32](outShape)
	if err != nil {
		return nil, fmt.Errorf("embed/onnx: output tensor: %w", err)
	}
	defer outT.Destroy()

	if err := e.session.Run(inputTensors, []ort.Value{outT}); err != nil {
		return nil, fmt.Errorf("embed/onnx: run: %w", err)
	}

	hidden := outT.GetData() // flat [batch * seqLen * dim]
	out := make([]Vector, batch)
	for b := 0; b < batch; b++ {
		out[b] = meanPool(hidden, mask, b, seqLen, e.dim)
	}
	return out, nil
}

// encodeRecover runs a single Encode, converting a tokenizer PANIC into an error.
// The forked byte-level pre-tokenizer indexes out of range on some inputs; because
// Encode runs synchronously in the caller's goroutine (unlike EncodeBatch's
// workers), a deferred recover here contains the panic so the caller can fall back
// to an empty encoding rather than crashing the process.
func encodeRecover(tk *tokenizer.Tokenizer, s string) (enc *tokenizer.Encoding, err error) {
	defer func() {
		if r := recover(); r != nil {
			enc, err = nil, fmt.Errorf("tokenizer panic: %v", r)
		}
	}()
	return tk.Encode(tokenizer.NewSingleEncodeInput(tokenizer.NewRawInputSequence(s)), true)
}

// meanPool computes the attention-masked mean of token embeddings for batch item
// b, then L2-normalizes — the sentence-transformers pooling.
func meanPool(hidden []float32, mask []int64, b, seqLen, dim int) Vector {
	v := make(Vector, dim)
	var count float64
	for i := 0; i < seqLen; i++ {
		if mask[b*seqLen+i] == 0 {
			continue
		}
		count++
		off := (b*seqLen + i) * dim
		for h := 0; h < dim; h++ {
			v[h] += hidden[off+h]
		}
	}
	if count == 0 {
		count = 1
	}
	inv := float32(1.0 / count)
	var norm float64
	for h := 0; h < dim; h++ {
		v[h] *= inv
		norm += float64(v[h]) * float64(v[h])
	}
	if norm > 0 {
		s := float32(1.0 / math.Sqrt(norm))
		for h := 0; h < dim; h++ {
			v[h] *= s
		}
	}
	return v
}

// Dim implements Embedder.
func (e *ONNXEmbedder) Dim() int { return e.dim }

// Close releases the session and runtime environment.
func (e *ONNXEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
	return ort.DestroyEnvironment()
}
