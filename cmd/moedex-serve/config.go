package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Config file support. The "config" is a KEY=VALUE file in the same format as a
// systemd EnvironmentFile (and deploy/moedex-serve.env.example): one VAR=value
// per line, # comments and blank lines ignored, optional surrounding quotes. The
// keys are the same env vars the daemon already reads (MOEDEX_SHARD_DIR,
// MOEDEX_AUTH_TOKEN, MOEDEX_HTTP_ADDR, ...). This keeps the zero-dependency
// posture: no YAML/TOML, just stdlib. It is loaded BEFORE flag defaults are
// computed (see main), so layering is flag > file > env > built-in default.

// extractConfigPath finds the -config / --config value in args without running
// the flag parser, since the file must load before flag defaults read the env.
// Supports "-config PATH", "--config PATH", "-config=PATH", "--config=PATH".
func extractConfigPath(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-config" || a == "--config" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		for _, p := range []string{"-config=", "--config="} {
			if strings.HasPrefix(a, p) {
				return a[len(p):]
			}
		}
	}
	return ""
}

// loadEnvFile parses path and sets each KEY in the process environment,
// overriding any existing value (so the file ranks above the ambient env). A
// read or parse error is returned; callers treat it as fatal config.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	kv, err := parseEnvFile(f)
	if err != nil {
		return err
	}
	for k, v := range kv {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// parseEnvFile parses EnvironmentFile-style KEY=VALUE lines. It is separate from
// loadEnvFile so it can be tested without touching the process environment.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ") // tolerate shell-style export
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: not KEY=VALUE: %q", line, sc.Text())
		}
		key := strings.TrimSpace(s[:eq])
		if !validKey(key) {
			return nil, fmt.Errorf("line %d: invalid key %q", line, key)
		}
		val := strings.TrimSpace(s[eq+1:])
		// Strip a single matching pair of surrounding quotes.
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out, sc.Err()
}

// validKey accepts shell/env-style identifiers: [A-Za-z_][A-Za-z0-9_]*.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// envOr returns the env var value for key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envOrInt returns the env var value for key parsed as an int, or def when
// it is unset, empty, or not a valid integer.
func envOrInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
