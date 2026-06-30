package main

import "testing"

// TestBearerTokenMatches locks down the correctness of the constant-time
// comparison used by withAuth: it must accept only an exact "Bearer <token>"
// match and reject everything else (wrong token, wrong/missing prefix,
// length mismatches), the same cases the old `!=` comparison handled.
func TestBearerTokenMatches(t *testing.T) {
	const token = "s3cret-bearer"
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"exact match", "Bearer s3cret-bearer", true},
		{"wrong token same length", "Bearer x3cret-bearer", false},
		{"wrong token shorter", "Bearer short", false},
		{"wrong token longer", "Bearer s3cret-bearer-extra", false},
		{"empty header", "", false},
		{"missing prefix", "s3cret-bearer", false},
		{"wrong scheme", "Basic s3cret-bearer", false},
		{"prefix only no token", "Bearer ", false},
		{"case-sensitive token", "Bearer S3CRET-BEARER", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bearerTokenMatches(tt.header, token); got != tt.want {
				t.Errorf("bearerTokenMatches(%q, %q) = %v, want %v", tt.header, token, got, tt.want)
			}
		})
	}
}
