package semanticrun

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publicFixture(t *testing.T) (GitProjectionOptions, string) {
	t.Helper()
	repo := t.TempDir()
	for name, content := range map[string]string{"A.cs": "class A {}", "hidden.cs": "class Hidden {}", "version.txt": "$Format:%H$", ".gitattributes": "hidden.cs export-ignore\nversion.txt export-subst\n"} {
		if e := os.WriteFile(filepath.Join(repo, name), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	gitTest(t, repo, "init", "-q")
	gitTest(t, repo, "config", "user.name", "Test")
	gitTest(t, repo, "config", "user.email", "test@localhost")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "public fixture")
	origin := "https://github.com/example/oracle.git"
	gitTest(t, repo, "remote", "add", "origin", origin)
	return GitProjectionOptions{Checkout: repo, Repo: "example/oracle", Commit: gitTest(t, repo, "rev-parse", "HEAD"), Origin: origin, TempParent: t.TempDir()}, repo
}

func TestPublicGitProjectionPreservesHonestIdentity(t *testing.T) {
	o, repo := publicFixture(t)
	os.WriteFile(filepath.Join(repo, "untracked.cs"), []byte("ignored"), 0600)
	o.MaxBytes = 512 << 20
	p, e := CreateGitProjection(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if p.ProjectID != 0 || p.managedRoot != "" || p.Origin != o.Origin || p.Commit != o.Commit || p.Repo != o.Repo {
		t.Fatalf("identity %#v", p)
	}
	for name, want := range map[string]string{"A.cs": "class A {}", "hidden.cs": "class Hidden {}", "version.txt": "$Format:%H$"} {
		got, e := os.ReadFile(filepath.Join(p.Root, name))
		if e != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, e)
		}
	}
	if _, e := os.Stat(filepath.Join(p.Root, "untracked.cs")); !os.IsNotExist(e) {
		t.Fatal("untracked input copied")
	}
	os.Mkdir(filepath.Join(p.Root, "obj"), 0700)
	os.WriteFile(filepath.Join(p.Root, "obj/generated.cs"), []byte("generated"), 0600)
	if e = p.Verify(context.Background()); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(repo, "A.cs"), []byte("class B {}"), 0600)
	if p.Verify(context.Background()) == nil {
		t.Fatal("canonical raw-byte drift accepted")
	}
}
func TestPublicGitProjectionRejectsBadSelection(t *testing.T) {
	for _, kind := range []string{"origin", "commit", "short", "dirty", "inside", "symlink", "limit", "userinfo", "control"} {
		t.Run(kind, func(t *testing.T) {
			o, repo := publicFixture(t)
			switch kind {
			case "origin":
				o.Origin = "https://github.com/other/repo.git"
			case "commit":
				o.Commit = strings.Repeat("a", 40)
			case "short":
				o.Commit = o.Commit[:8]
			case "dirty":
				os.WriteFile(filepath.Join(repo, "A.cs"), []byte("class B {}"), 0600)
			case "inside":
				o.TempParent = repo
			case "symlink":
				os.Symlink("A.cs", filepath.Join(repo, "link.cs"))
				gitTest(t, repo, "add", "link.cs")
				gitTest(t, repo, "commit", "-qm", "link")
				o.Commit = gitTest(t, repo, "rev-parse", "HEAD")
			case "limit":
				o.MaxBytes = 1
			case "userinfo":
				o.Origin = "https://secret@github.com/example/oracle.git"
			case "control":
				o.Origin = "https://github.com/example/\toracle.git"
			}
			if p, e := CreateGitProjection(context.Background(), o); e == nil {
				p.Close()
				t.Fatal("invalid public projection admitted")
			}
			if kind != "inside" {
				entries, _ := os.ReadDir(o.TempParent)
				if len(entries) > 0 {
					t.Fatal("failed projection retained")
				}
			}
		})
	}
}
func TestPublicGitProjectionVerifyOriginAndHead(t *testing.T) {
	for _, kind := range []string{"origin", "head"} {
		t.Run(kind, func(t *testing.T) {
			o, repo := publicFixture(t)
			p, e := CreateGitProjection(context.Background(), o)
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			if kind == "origin" {
				gitTest(t, repo, "remote", "set-url", "origin", "https://github.com/changed/repo.git")
			} else {
				gitTest(t, repo, "commit", "--allow-empty", "-qm", "new commit")
			}
			if p.Verify(context.Background()) == nil {
				t.Fatal("public identity drift accepted")
			}
		})
	}
}

func TestPublicGitProjectionRejectsMalformedOriginsBeforeGit(t *testing.T) {
	for _, origin := range []string{"https://github.com/", "https://github.com/repo?", "https://github.com/repo#", "https://github.com/has space/repo"} {
		_, e := CreateGitProjection(context.Background(), GitProjectionOptions{Checkout: "nonexistent", Repo: "roslyn", Commit: strings.Repeat("a", 40), Origin: origin, TempParent: t.TempDir()})
		if e == nil || !strings.Contains(e.Error(), "HTTPS origin") {
			t.Fatalf("origin %q was not rejected before filesystem/Git work: %v", origin, e)
		}
	}
}

var publicGitCheckout = flag.String("public-git-checkout", "", "pinned Roslyn checkout for explicit projection integration test")

// Deliberately opt-in: reads/copies the pinned public corpus, never fetches it.
func TestPublicGitProjectionActualRoslyn(t *testing.T) {
	checkout := *publicGitCheckout
	if checkout == "" {
		t.Skip("pass -args -public-git-checkout PATH for pinned Roslyn projection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, e := CreateGitProjection(ctx, GitProjectionOptions{Checkout: checkout, Repo: "roslyn", Commit: "36d26c5466e4d25940657ccb8d5b9557ccaf7be1", Origin: "https://github.com/dotnet/roslyn.git", TempParent: t.TempDir(), MaxBytes: 512 << 20})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	var total int64
	for _, file := range p.inventory {
		total += file.size
	}
	if len(p.inventory) != 35115 || total != 456238809 || p.ProjectID != 0 {
		t.Fatalf("inventory files=%d bytes=%d projectID=%d", len(p.inventory), total, p.ProjectID)
	}
	if e = p.Verify(ctx); e != nil {
		t.Fatal(e)
	}
	t.Logf("verified public projection: %d files, %d bytes; origin=%s commit=%s projectID omitted", len(p.inventory), total, p.Origin, p.Commit)
}
