package indexcmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"moedex/internal/navigate"
)

func TestCheckNavServersWarnsWhenCFMLCannotStart(t *testing.T) {
	d := &doctorReport{}
	checkNavServersWith(
		d,
		[]navigate.LangSpec{{Language: "cfml", Command: "cflsp", Args: []string{"--stdio"}}},
		func(string) (string, error) { return "/fake/cflsp", nil },
		func(path string, args []string) error {
			if path != "/fake/cflsp" || len(args) != 1 || args[0] != "--stdio" {
				t.Fatalf("probe called with path=%q args=%q", path, args)
			}
			return errors.New("exited during startup: managed server.js missing")
		},
	)

	if d.warn != 1 || len(d.lines) != 1 {
		t.Fatalf("report = %+v, want one warning", d)
	}
	if got := d.lines[0]; got.lvl != lvlWarn || !strings.Contains(got.msg, "failed startup") || !strings.Contains(got.msg, "server.js missing") {
		t.Fatalf("line = %+v, want actionable CFML startup warning", got)
	}
}

func TestCheckNavServersAcceptsCFMLThatSurvivesStartup(t *testing.T) {
	d := &doctorReport{}
	checkNavServersWith(
		d,
		[]navigate.LangSpec{{Language: "cfml", Command: "cflsp", Args: []string{"--stdio"}}},
		func(string) (string, error) { return "/fake/cflsp", nil },
		func(string, []string) error { return nil },
	)

	if d.warn != 0 || len(d.lines) != 1 || d.lines[0].lvl != lvlOK {
		t.Fatalf("report = %+v, want one healthy line", d)
	}
}

func TestProbeLSPStartupDistinguishesImmediateExitFromLiveServer(t *testing.T) {
	exitArgs := []string{"-test.run=^TestLSPStartupHelperProcess$", "--", "lsp-probe-helper=exit"}
	err := probeLSPStartup(os.Args[0], exitArgs, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "managed server.js missing") {
		t.Fatalf("immediate exit error = %v, want captured startup stderr", err)
	}

	liveArgs := []string{"-test.run=^TestLSPStartupHelperProcess$", "--", "lsp-probe-helper=live"}
	if err := probeLSPStartup(os.Args[0], liveArgs, 100*time.Millisecond); err != nil {
		t.Fatalf("live server rejected: %v", err)
	}
}

func TestLSPStartupHelperProcess(t *testing.T) {
	var mode string
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "lsp-probe-helper=") {
			mode = strings.TrimPrefix(arg, "lsp-probe-helper=")
		}
	}
	switch mode {
	case "":
		return
	case "exit":
		fmt.Fprintln(os.Stderr, "managed server.js missing")
		os.Exit(23)
	case "live":
		_, _ = io.Copy(io.Discard, os.Stdin)
	default:
		os.Exit(24)
	}
}
