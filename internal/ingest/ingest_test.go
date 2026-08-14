package ingest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo creates a temp dir, runs `git init`, writes the given files, and
// `git add`s them so they appear in `git ls-files -s`. files maps a relative
// path to its byte content. It returns the repo dir.
func gitRepo(t *testing.T, files map[string][]byte) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// Keep git from depending on the user's global config/identity.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	return dir
}

// byRel indexes a slice of Files by RelPath for convenient assertions.
func byRel(files []File) map[string]File {
	m := make(map[string]File, len(files))
	for _, f := range files {
		m[f.RelPath] = f
	}
	return m
}

func TestRepoReturnsTextFile(t *testing.T) {
	content := []byte("hello\nworld\n")
	dir := gitRepo(t, map[string][]byte{"main.go": content})

	files, err := Repo("myrepo", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	f, ok := m["main.go"]
	if !ok {
		t.Fatalf("main.go not returned; got %v", files)
	}
	if string(f.Content) != string(content) {
		t.Errorf("Content = %q, want %q", f.Content, content)
	}
	if f.Repo != "myrepo" {
		t.Errorf("Repo label = %q, want %q", f.Repo, "myrepo")
	}
	if f.AbsPath != filepath.Join(dir, "main.go") {
		t.Errorf("AbsPath = %q, want %q", f.AbsPath, filepath.Join(dir, "main.go"))
	}
	// git blob SHA-1 is 40 lowercase hex chars.
	if len(f.SHA) != 40 {
		t.Errorf("SHA = %q, want 40 hex chars", f.SHA)
	}
	for _, c := range f.SHA {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("SHA = %q contains non-hex char %q", f.SHA, c)
			break
		}
	}
}

func TestRepoSkipsBinary(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{
		"text.txt":   []byte("plain text"),
		"binary.dat": {0x00, 0x01, 0x02, 'a', 'b'}, // contains a NUL byte
	})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	if _, ok := m["binary.dat"]; ok {
		t.Errorf("binary.dat should be skipped (contains NUL), got it in results")
	}
	if _, ok := m["text.txt"]; !ok {
		t.Errorf("text.txt should be included")
	}
}

func TestRepoAIPrivacyGlobalRestrictedReadsNoContent(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{
		AIPrivacyFileName: []byte("global_privacy_level: 1\n"),
		"secret.txt":      []byte("never expose this marker\n"),
	})

	files, err := Repo("restricted", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("globally Restricted repo returned %d files: %v", len(files), files)
	}
}

func TestRepoAIPrivacyFiltersRestrictedOverridesBeforeRead(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{
		AIPrivacyFileName: []byte(`global_privacy_level: 3
privacy_levels:
  - path: /secrets/
    privacy_level: 1
  - path: /exact.txt
    privacy_level: 1
`),
		"public.txt":          []byte("searchable\n"),
		"exact.txt":           []byte("restricted exact file\n"),
		"secrets/token.txt":   []byte("restricted child\n"),
		"secrets-named.txt":   []byte("not beneath restricted folder\n"),
		"nested/ordinary.txt": []byte("also searchable\n"),
	})

	files, err := Repo("mixed", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	for _, restricted := range []string{AIPrivacyFileName, "exact.txt", "secrets/token.txt"} {
		if _, ok := m[restricted]; ok {
			t.Errorf("Restricted/bootstrap path should not be indexed: %s", restricted)
		}
	}
	for _, allowed := range []string{"public.txt", "secrets-named.txt", "nested/ordinary.txt"} {
		if _, ok := m[allowed]; !ok {
			t.Errorf("allowed path should be indexed: %s", allowed)
		}
	}
}

func TestRepoAIPrivacyMalformedPolicyFailsClosed(t *testing.T) {
	cases := map[string]string{
		"unknown field": `global_privacy_level: 3
unexpected: true
`,
		"more permissive global override": `global_privacy_level: 2
privacy_levels:
  - path: /public/
    privacy_level: 3
`,
		"more permissive ancestor override": `global_privacy_level: 3
privacy_levels:
  - path: /internal/
    privacy_level: 1
  - path: /internal/child/
    privacy_level: 2
`,
		"unsafe path": `global_privacy_level: 3
privacy_levels:
  - path: /safe/../escape/
    privacy_level: 1
`,
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			dir := gitRepo(t, map[string][]byte{
				AIPrivacyFileName: []byte(policy),
				"content.txt":     []byte("must not be read after a bad policy\n"),
			})
			if _, err := Repo("bad-policy", dir); err == nil || !IsPrivacyPolicyError(err) {
				t.Fatalf("Repo error = %v, want PrivacyPolicyError", err)
			}
		})
	}
}

func TestRepoAIPrivacyMissingOrEmptyDefaultsInternal(t *testing.T) {
	for name, files := range map[string]map[string][]byte{
		"missing": {"content.txt": []byte("default internal\n")},
		"empty":   {AIPrivacyFileName: nil, "content.txt": []byte("default internal\n")},
	} {
		t.Run(name, func(t *testing.T) {
			dir := gitRepo(t, files)
			got, err := Repo("default", dir)
			if err != nil {
				t.Fatalf("Repo: %v", err)
			}
			m := byRel(got)
			if _, ok := m["content.txt"]; !ok {
				t.Fatalf("missing/empty policy did not default to level 3: %v", got)
			}
			if _, ok := m[AIPrivacyFileName]; ok {
				t.Fatal("bootstrap policy file must not become searchable content")
			}
		})
	}
}

func TestRepoSkipsTrackedSymlinkContent(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{"target.txt": []byte("ordinary content\n")})
	if err := os.Symlink("target.txt", filepath.Join(dir, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "add", "alias.txt")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add symlink: %v\n%s", err, out)
	}

	files, err := Repo("symlink", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	if _, ok := m["alias.txt"]; ok {
		t.Fatal("tracked symlink must not be followed into searchable content")
	}
	if _, ok := m["target.txt"]; !ok {
		t.Fatal("ordinary tracked target should remain searchable")
	}
}

func TestCorpusAIPrivacyPoliciesParse(t *testing.T) {
	root := os.Getenv("MOEDEX_PRIVACY_CORPUS")
	if root == "" {
		t.Skip("set MOEDEX_PRIVACY_CORPUS to validate a real corpus without reading repository content")
	}
	repos, err := DiscoverRepos(root)
	if err != nil {
		t.Fatalf("discover corpus repositories: %v", err)
	}
	policyCount := 0
	for _, repo := range repos {
		policyPath := filepath.Join(repo, AIPrivacyFileName)
		if _, err := os.Lstat(policyPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("inspect privacy policy: %v", err)
		}
		policyCount++
		if _, err := loadPrivacyPolicy(repo); err != nil {
			t.Fatalf("validate privacy policy: %v", err)
		}
	}
	if policyCount == 0 {
		t.Fatal("corpus contains no .ai-privacy.yml files")
	}
	t.Logf("validated %d .ai-privacy.yml files", policyCount)
}

func TestCorpusAIPrivacyPublicationAudit(t *testing.T) {
	root := os.Getenv("MOEDEX_PRIVACY_CORPUS")
	casPath := os.Getenv("MOEDEX_PRIVACY_CAS_MANIFEST")
	servedPath := os.Getenv("MOEDEX_PRIVACY_SERVED_MANIFEST")
	if root == "" || casPath == "" || servedPath == "" {
		t.Skip("set MOEDEX_PRIVACY_CORPUS, MOEDEX_PRIVACY_CAS_MANIFEST, and MOEDEX_PRIVACY_SERVED_MANIFEST to audit a real publication")
	}

	type manifestRepo struct {
		ProjectID int64  `json:"project_id"`
		AIPrivacy string `json:"ai_privacy"`
		Files     []struct {
			Rel string `json:"rel"`
		} `json:"files"`
	}
	var cas struct {
		Repos []manifestRepo `json:"repos"`
	}
	casData, err := os.ReadFile(casPath)
	if err != nil {
		t.Fatalf("read CAS manifest: %v", err)
	}
	if err := json.Unmarshal(casData, &cas); err != nil {
		t.Fatalf("decode CAS manifest: %v", err)
	}

	var served struct {
		Heads []manifestRepo `json:"heads"`
	}
	servedData, err := os.ReadFile(servedPath)
	if err != nil {
		t.Fatalf("read served manifest: %v", err)
	}
	if err := json.Unmarshal(servedData, &served); err != nil {
		t.Fatalf("decode served manifest: %v", err)
	}

	byProjectID := func(entries []manifestRepo) map[int64]manifestRepo {
		out := make(map[int64]manifestRepo, len(entries))
		for _, entry := range entries {
			out[entry.ProjectID] = entry
		}
		return out
	}
	casByProjectID := byProjectID(cas.Repos)
	servedByProjectID := byProjectID(served.Heads)
	sources, err := DiscoverSources(root)
	if err != nil {
		t.Fatalf("discover corpus repositories: %v", err)
	}

	var policyCount, globalRestricted, restrictedOverrides, eligibleFileRefs int
	var missingCAS, missingServed, fingerprintMismatches, restrictedFileRefs int
	for _, source := range sources {
		if !source.Managed || source.ProjectID <= 0 {
			t.Fatal("publication privacy audit requires a managed corpus with stable project IDs")
		}
		repo := source.Dir
		policyPath := filepath.Join(repo, AIPrivacyFileName)
		if _, err := os.Lstat(policyPath); err == nil {
			policyCount++
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect privacy policy: %v", err)
		}
		policy, err := loadPrivacyPolicy(repo)
		if err != nil {
			t.Fatalf("validate privacy policy: %v", err)
		}
		if policy.globalLevel == restrictedAILevel {
			globalRestricted++
		}
		for _, override := range policy.overrides {
			if override.level == restrictedAILevel {
				restrictedOverrides++
			}
		}
		fingerprint := privacyPolicyFingerprint(policy)

		casRepo, ok := casByProjectID[source.ProjectID]
		if !ok {
			missingCAS++
		} else {
			if casRepo.AIPrivacy != fingerprint {
				fingerprintMismatches++
			}
			for _, file := range casRepo.Files {
				if policy.restricted(file.Rel) {
					restrictedFileRefs++
				} else {
					eligibleFileRefs++
				}
			}
		}
		servedRepo, ok := servedByProjectID[source.ProjectID]
		if !ok {
			missingServed++
		} else if servedRepo.AIPrivacy != fingerprint {
			fingerprintMismatches++
		}
	}

	if missingCAS != 0 || missingServed != 0 || fingerprintMismatches != 0 || restrictedFileRefs != 0 {
		t.Fatalf("publication privacy audit failed: missing_cas=%d missing_served=%d fingerprint_mismatches=%d restricted_file_refs=%d",
			missingCAS, missingServed, fingerprintMismatches, restrictedFileRefs)
	}
	t.Logf("audited repos=%d policies=%d global_level1=%d level1_overrides=%d eligible_file_refs=%d restricted_file_refs=%d",
		len(sources), policyCount, globalRestricted, restrictedOverrides, eligibleFileRefs, restrictedFileRefs)
}

func TestRepoStripsBOM(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	body := []byte("package main\n")
	dir := gitRepo(t, map[string][]byte{"bom.go": append(append([]byte{}, bom...), body...)})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	f, ok := m["bom.go"]
	if !ok {
		t.Fatalf("bom.go not returned")
	}
	if string(f.Content) != string(body) {
		t.Errorf("Content = %q, want BOM stripped to %q", f.Content, body)
	}
}

func TestRepoSkipsVCSInternalPaths(t *testing.T) {
	// A repo that has committed VCS-internal junk into git (e.g. an SVN working
	// copy's pristine base-file cache checked into a git mirror) must NOT have
	// those files ingested: a 40-hex .svn-base blob is not searchable code.
	// git ls-files only filters out the OUTER .git/ dir; nested VCS metadata that
	// was actually committed (.svn/, an inner .git/, .hg/, .bzr/) still appears,
	// so ingest must drop any path with such a segment.
	dir := gitRepo(t, map[string][]byte{
		// SVN pristine base-file cache — the exact symptom.
		".svn/pristine/ab/abcdef0123456789abcdef0123456789abcdef01.svn-base": []byte("0123456789abcdef0123456789abcdef01234567"),
		".svn/entries":               []byte("svn metadata"),
		"vendor/sub/.git/config":     []byte("[core]\n"), // committed nested git dir
		"nested/.hg/store/data.i":    []byte("hg internal"),
		"old/.bzr/checkout/dirstate": []byte("bzr internal"),
		// Legitimately-named files that merely CONTAIN a vcs token as a substring
		// (not a path segment) must be kept — no over-matching.
		"config/my.git.config": []byte("keep me"),
		"docs/.svnotes.md":     []byte("keep me too"),
		"src/main.go":          []byte("package main\n"),
	})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	for _, junk := range []string{
		".svn/pristine/ab/abcdef0123456789abcdef0123456789abcdef01.svn-base",
		".svn/entries",
		"vendor/sub/.git/config",
		"nested/.hg/store/data.i",
		"old/.bzr/checkout/dirstate",
	} {
		if _, ok := m[junk]; ok {
			t.Errorf("VCS-internal path should be skipped: %q", junk)
		}
	}
	for _, keep := range []string{"config/my.git.config", "docs/.svnotes.md", "src/main.go"} {
		if _, ok := m[keep]; !ok {
			t.Errorf("legitimate path should be kept: %q", keep)
		}
	}
}

func TestRepoSkipsFileMissingFromWorktree(t *testing.T) {
	dir := gitRepo(t, map[string][]byte{
		"keep.txt": []byte("kept"),
		"gone.txt": []byte("deleted from worktree"),
	})
	// Remove from the working tree but leave it tracked in the index.
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	if _, ok := m["gone.txt"]; ok {
		t.Errorf("gone.txt should be skipped (tracked but absent from worktree)")
	}
	if _, ok := m["keep.txt"]; !ok {
		t.Errorf("keep.txt should still be present")
	}
}

func TestRepoErrorsOnNonGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir() // not a git repo
	if _, err := Repo("r", dir); err == nil {
		t.Fatal("expected error for non-git directory, got nil")
	}
}

func TestRepoDedupsContentBySHANo(t *testing.T) {
	// Two paths with identical content share a git blob SHA. Repo returns one
	// File per tracked path (dedup is the index's job, not ingest's), but both
	// must carry the same SHA.
	same := []byte("identical contents\n")
	dir := gitRepo(t, map[string][]byte{
		"a.txt": same,
		"b.txt": same,
	})

	files, err := Repo("r", dir)
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	m := byRel(files)
	a, okA := m["a.txt"]
	b, okB := m["b.txt"]
	if !okA || !okB {
		t.Fatalf("expected both a.txt and b.txt, got %v", files)
	}
	if a.SHA != b.SHA {
		t.Errorf("identical content should share a SHA: a=%q b=%q", a.SHA, b.SHA)
	}
}
