package sourcescope

import (
	"moedex/internal/index"
	"testing"
)

func TestScopeValidationAndLiteralMatching(t *testing.T) {
	for _, scope := range []Scope{{Language: "Go"}, {Language: "unknown"}, {PathPrefix: "/src"}, {PathPrefix: "../src"}, {PathPrefix: "src/../x"}, {PathPrefix: "./src"}, {PathPrefix: "src//x"}, {PathPrefix: "src//"}, {PathPrefix: "C:/src"}, {PathPrefix: `src\foo`}, {PathPrefix: "src/*"}, {PathPrefix: "src/?"}, {PathPrefix: "src/[ab]"}, {Repo: "bad\nrepo"}} {
		if err := scope.Validate(); err == nil {
			t.Errorf("accepted invalid scope %+v", scope)
		}
	}
	scope := Scope{Repo: "MyRepo", PathPrefix: "src/api/", Language: "typescript"}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file index.FileRef
		want bool
	}{
		{index.FileRef{Repo: "MyRepo", RelPath: "src/api/client.ts"}, true},
		{index.FileRef{Repo: "MyRepo", RelPath: "src/api/client.TSX"}, true},
		{index.FileRef{Repo: "myrepo", RelPath: "src/api/client.ts"}, false},
		{index.FileRef{Repo: "MyRepo", RelPath: "src/apis/client.ts"}, false},
		{index.FileRef{Repo: "MyRepo", RelPath: "src/api/client.js"}, false},
		{index.FileRef{Repo: "MyRepo", RelPath: "elsewhere/client.ts", AbsPath: "/src/api/client.ts"}, false},
	} {
		if got := scope.Match(tc.file); got != tc.want {
			t.Errorf("Match(%+v)=%v, want %v", tc.file, got, tc.want)
		}
	}
	for _, path := range []string{"src", "src/file.go"} {
		if !(Scope{PathPrefix: "src"}).Match(index.FileRef{RelPath: path}) {
			t.Errorf("segment prefix missed %s", path)
		}
	}
	if !(Scope{}).Empty() || (Scope{Repo: "x"}).Empty() {
		t.Fatal("incorrect Empty result")
	}
	if !(Scope{}).Match(index.FileRef{}) {
		t.Fatal("empty scope must match all locations")
	}
}
