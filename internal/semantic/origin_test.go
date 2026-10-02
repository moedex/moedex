package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotOriginAuditIdentity(t *testing.T) {
	s := SourceSnapshot{Repo: "roslyn", Commit: strings.Repeat("a", 40), InputFingerprint: sum("roster")}
	legacy := s.ComputeID()
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "origin") {
		t.Fatal("absent origin changed legacy serialization")
	}
	s.Origin = "https://github.com/dotnet/roslyn.git"
	if s.ComputeID() == legacy {
		t.Fatal("origin omitted from snapshot identity")
	}
	for _, origin := range []string{"", s.Origin} {
		if !validOrigin(origin) {
			t.Fatalf("valid origin rejected: %q", origin)
		}
	}
	for _, origin := range []string{"http://github.com/dotnet/roslyn", "https://user:secret@github.com/dotnet/roslyn", "https://github.com/dotnet/roslyn?token=secret", "https://github.com/dotnet/roslyn#x", "https://github.com", "https://github.com/a\\b", "https://github.com/a\x01b", strings.Repeat("a", 2049)} {
		if validOrigin(origin) {
			t.Fatalf("unsafe audit origin accepted: %q", origin)
		}
		a := fixture()
		a.Snapshots[0].Origin = origin
		a.Snapshots[0].ID = a.Snapshots[0].ComputeID()
		if a.Validate() == nil {
			t.Fatal("invalid origin artifact accepted")
		}
	}
}
