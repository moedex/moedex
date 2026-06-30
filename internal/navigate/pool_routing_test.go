//go:build lsp

package navigate

import "testing"

// TestRoutingLang_PureLogic pins how the file-routed convenience queries
// (Definition/References/Implementations/SetOverlay/DropOverlay/NotifyChanged)
// resolve the language used for BOTH the pool key and the per-spawn
// Config.Language. A Pool configured with a pinned Config.Language (forced
// -lang mode, cmd/moedex-nav's buildConfig) must route EVERY file to that one
// language regardless of the file's own extension — otherwise NavigatorFor's
// "cfg.Language = lang" (pool.go) clobbers the forced language with the file's
// extension-derived one on the very first mismatched file, silently defeating
// -lang. Auto mode (neither Server nor Language set) must still derive the
// language from the file extension. Legacy override mode (Server set) is
// unaffected by this change: lang still comes from the file extension, per the
// documented "lang selects the pool key but not the command" contract — Server
// alone pins the launched command for every key.
func TestRoutingLang_PureLogic(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		file string
		want string
	}{
		{name: "forced language overrides mismatched extension", cfg: Config{Language: "python"}, file: "x.go", want: "python"},
		{name: "forced language matches extension", cfg: Config{Language: "go"}, file: "x.go", want: "go"},
		{name: "auto mode uses file extension", cfg: Config{}, file: "x.py", want: "python"},
		{name: "auto mode unknown extension defaults to go", cfg: Config{}, file: "x.txt", want: "go"},
		{name: "legacy override mode still uses file extension for keying", cfg: Config{Server: "my-langserver", Language: "python"}, file: "x.go", want: "go"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pool{cfg: tc.cfg}
			fileLang, _ := workspaceRoot(tc.file)
			if got := p.routingLang(fileLang); got != tc.want {
				t.Errorf("routingLang(%q) = %q, want %q", fileLang, got, tc.want)
			}
		})
	}
}
