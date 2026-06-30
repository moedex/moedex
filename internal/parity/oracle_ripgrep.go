package parity

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

// ripgrep is the ground-truth oracle. It searches a materialized content mirror
// of exactly F (one file per fileID), so its scanned-file set IS F (AC-D1) and
// the bytes it scans are byte-identical to what moedex indexed (BOM stripped,
// no ignore/binary ambiguity).
type ripgrep struct {
	bin       string
	mirrorDir string
	threads   string // value for rg --threads
}

func newRipgrep(mirrorDir string, threads int) (*ripgrep, error) {
	bin, err := exec.LookPath("rg")
	if err != nil {
		return nil, err
	}
	t := "0"
	if threads > 0 {
		t = strconv.Itoa(threads)
	}
	return &ripgrep{bin: bin, mirrorDir: mirrorDir, threads: t}, nil
}

// args builds rg flags matched to a query's semantics. The base flags mirror the
// established parity-test invocation (no config, case-sensitive default,
// line-oriented, explicit scope). Per query:
//   - -F             fixed-string (literal) search
//   - -i / --case-sensitive
//   - --no-unicode   ASCII \w\d\s\b to match Go RE2 (only on regex w/o '.')
func (r *ripgrep) args(q Query) []string {
	a := []string{
		"--no-config", "--no-heading", "--color=never", "--with-filename",
		"-n", "--no-ignore", "--hidden", "--no-messages", "--threads", r.threads,
	}
	if q.IgnoreCase {
		a = append(a, "-i")
	} else {
		a = append(a, "--case-sensitive")
	}
	if q.Literal {
		a = append(a, "-F")
	}
	if q.NoUnicode {
		a = append(a, "--no-unicode")
	}
	a = append(a, "-e", q.Pattern, "--", r.mirrorDir)
	return a
}

// run executes ripgrep for a query and returns its match set over F, retrying a
// few times on transient failures (e.g. a fork/exec hiccup under memory
// pressure) since a single rg error otherwise fails the whole gate.
func (r *ripgrep) run(q Query, ft *FileTable) (MatchSet, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		set, err := r.runOnce(q, ft)
		if err == nil {
			return set, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// runOnce is a single ripgrep invocation. ripgrep exit code 1 (no matches) is
// not an error.
func (r *ripgrep) runOnce(q Query, ft *FileTable) (MatchSet, error) {
	cmd := exec.Command(r.bin, r.args(q)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var a accum
	br := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			parseRGLine(bytes.TrimSuffix(line, []byte{'\n'}), &a)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Drain and let Wait report the real failure.
			_, _ = io.Copy(io.Discard, br)
			break
		}
	}

	werr := cmd.Wait()
	if werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return a.finalize(), nil // no matches
		}
		return nil, fmt.Errorf("rg %q: %w (stderr: %s)", q.Pattern, werr, stderr.String())
	}
	return a.finalize(), nil
}

// parseRGLine parses one "path:line:content" record. Mirror paths contain no
// ':' so the first two colons delimit path and line number.
func parseRGLine(line []byte, a *accum) {
	c1 := bytes.IndexByte(line, ':')
	if c1 < 0 {
		return
	}
	rest := line[c1+1:]
	c2 := bytes.IndexByte(rest, ':')
	if c2 < 0 {
		return
	}
	n, err := strconv.Atoi(string(rest[:c2]))
	if err != nil {
		return
	}
	if id := mirrorPathToID(string(line[:c1])); id >= 0 {
		a.add(id, n)
	}
}
