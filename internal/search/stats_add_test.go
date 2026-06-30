package search

import "testing"

// TestStatsAddSumsCandidateBlobs guards F-034: add() summed every Stats field
// except CandidateBlobs, a latent trap for any future caller that relies on
// add() to merge per-worker candidate-blob counts.
func TestStatsAddSumsCandidateBlobs(t *testing.T) {
	s := Stats{CandidateBlobs: 3}
	s.add(Stats{CandidateBlobs: 4})
	if s.CandidateBlobs != 7 {
		t.Errorf("CandidateBlobs = %d, want 7", s.CandidateBlobs)
	}
}
