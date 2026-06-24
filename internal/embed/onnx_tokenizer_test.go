package embed

import (
	"bytes"
	"os"
	"testing"
	"unicode/utf8"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

// This pins the fix for the in-process ONNX dense arm crashing on legacy
// Windows-1252 ColdFusion. The forked byte-level tokenizer panics on INVALID
// UTF-8 (it expands each invalid byte to a 3-byte U+FFFD and indexes out of range
// in normalizer.TransformRange); toTokenizerSafeText coerces input to valid UTF-8
// first. We exercise the REAL bundled tokenizer (loaded from disk, no ONNX runtime
// needed) so this regression test runs in the default `go test ./...`.

// win1252Inputs are the kinds of bytes that crashed the embedder on the real CF
// corpus: smart quote, nbsp, en-dash, and a lone trailing invalid byte.
var win1252Inputs = []struct{ name, s string }{
	{"smart quote", "don\x92t void the transaction"},
	{"nbsp", "a\xa0b"},
	{"en-dash", "2020\x962024"},
	{"trailing invalid", "<cfquery>SELECT 1</cfquery>\x92"},
	{"mixed cfml", "\t<!--- comment --->\n<cfset x = \x93hi\x94>"},
}

func loadBundledTokenizer(t *testing.T) *tokenizer.Tokenizer {
	t.Helper()
	data, err := os.ReadFile("onnxmodel/tokenizer.json")
	if err != nil {
		t.Fatalf("read bundled tokenizer.json: %v", err)
	}
	tk, err := pretrained.FromReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("load bundled tokenizer: %v", err)
	}
	return tk
}

func encodePanics(tk *tokenizer.Tokenizer, s string) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	_, _ = tk.Encode(tokenizer.NewSingleEncodeInput(tokenizer.NewRawInputSequence(s)), true)
	return false
}

// TestToTokenizerSafeTextProducesValidUTF8 checks the sanitizer's contract: the
// output is always valid UTF-8, and valid input (incl. multi-byte) is preserved
// unchanged.
func TestToTokenizerSafeTextProducesValidUTF8(t *testing.T) {
	for _, c := range win1252Inputs {
		got := toTokenizerSafeText(c.s)
		if !utf8.ValidString(got) {
			t.Errorf("%s: toTokenizerSafeText output is still invalid UTF-8: %q", c.name, got)
		}
	}
	// Valid UTF-8, including multi-byte runes, must pass through untouched.
	for _, valid := range []string{"plain ascii", "café — naïve", "emoji \U0001F600 end", "héllo\tworld"} {
		if got := toTokenizerSafeText(valid); got != valid {
			t.Errorf("valid UTF-8 was altered: %q -> %q", valid, got)
		}
	}
}

// TestSanitizedInputDoesNotPanicTokenizer is the load-bearing regression: the
// bundled byte-level tokenizer must encode the sanitized form of every Win1252
// input without panicking and yield at least one token.
func TestSanitizedInputDoesNotPanicTokenizer(t *testing.T) {
	tk := loadBundledTokenizer(t)
	for _, c := range win1252Inputs {
		safe := toTokenizerSafeText(c.s)
		if encodePanics(tk, safe) {
			t.Errorf("%s: tokenizer still panicked after sanitization (input=%q safe=%q)", c.name, c.s, safe)
			continue
		}
		enc, err := tk.Encode(tokenizer.NewSingleEncodeInput(tokenizer.NewRawInputSequence(safe)), true)
		if err != nil {
			t.Errorf("%s: encode returned error: %v", c.name, err)
			continue
		}
		if len(enc.Ids) == 0 {
			t.Errorf("%s: sanitized input produced 0 tokens (no dense signal)", c.name)
		}
	}
}
