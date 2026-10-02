package semanticimport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"moedex/internal/semantic"
)

// Revalidate rechecks recorded source and source-scoped build inputs against
// confined roots keyed by SourceSnapshot.ID. It does not evaluate MSBuild,
// discover new inputs, attest SDK/external files, or authorize publication.
func Revalidate(ctx context.Context, a *semantic.Artifact, roots map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if !a.Complete() {
		return fmt.Errorf("semantic revalidate: incomplete artifact")
	}
	opened := map[string]*os.Root{}
	defer func() {
		for _, r := range opened {
			r.Close()
		}
	}()
	snapshots := map[string]semantic.SourceSnapshot{}
	for _, s := range a.Snapshots {
		p := roots[s.ID]
		if p == "" {
			return fmt.Errorf("semantic revalidate: missing snapshot root")
		}
		r, e := os.OpenRoot(p)
		if e != nil {
			return e
		}
		opened[s.ID] = r
		snapshots[s.ID] = s
	}
	sources := map[string]semantic.Source{}
	var sourceBytes int64
	for _, s := range a.Sources {
		if e := ctx.Err(); e != nil {
			return e
		}
		row := sourceRecord{Path: s.Path, SHA: s.RawSHA256, ByteSize: s.ByteSize, Generated: s.Generated}
		if s.Generated {
			encoded := base64.StdEncoding.EncodeToString(s.Content)
			row.ContentBase64 = &encoded
		}
		raw, e := readSource(opened[s.SnapshotID], row, DefaultMaxBytes-sourceBytes)
		if e != nil {
			return e
		}
		sourceBytes += int64(len(raw))
		sources[s.ID] = s
	}
	contexts := map[string]semantic.BuildContext{}
	for _, c := range a.Contexts {
		contexts[c.ID] = c
	}
	seenInputs := map[string]map[string]string{}
	var inputBytes int64
	for _, c := range a.Contexts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.Extractor != "msbuild-roslyn" || (c.ExtractorVersion != "1" && c.ExtractorVersion != "2" && c.ExtractorVersion != "3" && c.ExtractorVersion != "4" && c.ExtractorVersion != "5" && c.ExtractorVersion != "6" && c.ExtractorVersion != "7" && c.ExtractorVersion != "8" && c.ExtractorVersion != "9" && (c.ExtractorVersion != "10" && (c.ExtractorVersion != "11" && (c.ExtractorVersion != "12" && (c.ExtractorVersion != "13" && (c.ExtractorVersion != "14" && (c.ExtractorVersion != "15" && (c.ExtractorVersion != "16" && (c.ExtractorVersion != "17" && (c.ExtractorVersion != "18" && c.ExtractorVersion != "19")))))))))) {
			return fmt.Errorf("semantic revalidate: unsupported extractor")
		}
		var capture struct {
			Project          string             `json:"project"`
			Repo             string             `json:"repo"`
			Extractor        string             `json:"extractor"`
			ExtractorVersion string             `json:"extractor_version"`
			CompilerVersion  string             `json:"compiler_version"`
			Sources          []sourceRecord     `json:"sources"`
			Dependencies     []dependencyRecord `json:"project_references"`
			ProjectFile      struct {
				Path  string `json:"path"`
				Scope string `json:"scope"`
				SHA   string `json:"sha256"`
			} `json:"project_file"`
		}
		if e := json.Unmarshal([]byte(c.Capture), &capture); e != nil {
			return e
		}
		if capture.Project != c.Project || capture.Repo != snapshots[c.SnapshotID].Repo || capture.Extractor != c.Extractor || capture.ExtractorVersion != c.ExtractorVersion || capture.CompilerVersion == "" || capture.ProjectFile.Path != c.Project || capture.ProjectFile.Scope != "source" || capture.ProjectFile.SHA == "" {
			return fmt.Errorf("semantic revalidate: contradictory capture identity")
		}
		if len(capture.Sources) != len(c.SourceIDs) || len(capture.Dependencies) != len(c.DependencyContextIDs) {
			return fmt.Errorf("semantic revalidate: contradictory capture membership")
		}
		members := map[string]semantic.Source{}
		for _, id := range c.SourceIDs {
			s := sources[id]
			members[s.Path] = s
		}
		for _, s := range capture.Sources {
			want, ok := members[s.Path]
			if !ok || want.RawSHA256 != s.SHA || want.ByteSize != s.ByteSize || want.Generated != s.Generated {
				return fmt.Errorf("semantic revalidate: contradictory capture source")
			}
			delete(members, s.Path)
		}
		dependencies := map[string]string{}
		for _, id := range c.DependencyContextIDs {
			d := contexts[id]
			if _, ok := dependencies[d.Project]; ok {
				return fmt.Errorf("semantic revalidate: duplicate dependency project")
			}
			dependencies[d.Project] = d.InputFingerprint
		}
		for _, d := range capture.Dependencies {
			want, ok := dependencies[d.Project]
			if !ok || want != d.Context {
				return fmt.Errorf("semantic revalidate: contradictory capture dependency")
			}
			delete(dependencies, d.Project)
		}
		seen := seenInputs[c.SnapshotID]
		if seen == nil {
			seen = map[string]string{}
			seenInputs[c.SnapshotID] = seen
		}
		if e := verifyInputs(ctx, opened[c.SnapshotID], c.Capture, DefaultMaxBytes, seen, &inputBytes); e != nil {
			return e
		}
	}
	return nil
}
