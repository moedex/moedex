//go:build lsp

package navigate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodeLocationQueryResult_Statuses(t *testing.T) {
	tests := []struct {
		name       string
		raw        json.RawMessage
		queryErr   error
		wantStatus LocationQueryStatus
		wantLocs   int
		wantErr    bool
	}{
		{
			name:       "resolved array",
			raw:        json.RawMessage(`[{"uri":"file:///tmp/a.go","range":{"start":{"line":2,"character":3},"end":{"line":2,"character":4}}}]`),
			wantStatus: LocationQueryResolved,
			wantLocs:   1,
		},
		{
			name:       "resolved single definition",
			raw:        json.RawMessage(`{"uri":"file:///tmp/a.go","range":{"start":{"line":2,"character":3},"end":{"line":2,"character":4}}}`),
			wantStatus: LocationQueryResolved,
			wantLocs:   1,
		},
		{name: "null is successful empty", raw: json.RawMessage(`null`), wantStatus: LocationQueryReadyEmpty},
		{name: "whitespace null is successful empty", raw: json.RawMessage(" \n null \t"), wantStatus: LocationQueryReadyEmpty},
		{name: "empty array is successful empty", raw: json.RawMessage(`[]`), wantStatus: LocationQueryReadyEmpty},
		{
			name:       "method not found is unsupported",
			queryErr:   &rpcError{Code: codeMethodNotFound, Message: "not implemented"},
			wantStatus: LocationQueryUnsupported,
		},
		{
			name:       "request failure is unavailable",
			queryErr:   context.DeadlineExceeded,
			wantStatus: LocationQueryUnavailable,
			wantErr:    true,
		},
		{
			name:       "malformed response is unavailable",
			raw:        json.RawMessage(`{"unexpected":true`),
			wantStatus: LocationQueryUnavailable,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeLocationQueryResult(tt.raw, tt.queryErr, "textDocument/definition")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tt.wantErr)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if len(got.Locations) != tt.wantLocs {
				t.Errorf("locations = %d, want %d: %+v", len(got.Locations), tt.wantLocs, got.Locations)
			}
		})
	}
}

func TestPoolDefinitionDetailed_UnavailablePreservesFailure(t *testing.T) {
	pool := NewPool(Config{})
	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	result, err := pool.DefinitionDetailed(context.Background(), Pos{File: "/tmp/a.go", Line: 1, Col: 1})
	if err == nil {
		t.Fatal("DefinitionDetailed on a closed pool returned nil error")
	}
	if result.Status != LocationQueryUnavailable {
		t.Errorf("status = %q, want %q", result.Status, LocationQueryUnavailable)
	}

	// The original method remains a compatibility wrapper: it still surfaces
	// the same pool failure rather than converting it to a successful empty.
	locs, legacyErr := pool.Definition(context.Background(), Pos{File: "/tmp/a.go", Line: 1, Col: 1})
	if legacyErr == nil || locs != nil {
		t.Errorf("legacy Definition = (%+v, %v), want (nil, error)", locs, legacyErr)
	}
}

func TestLegacyLocations_PreservesCompatibility(t *testing.T) {
	want := []Location{{File: "/tmp/a.go", Start: Pos{File: "/tmp/a.go", Line: 3, Col: 4}}}
	got, err := legacyLocations(LocationQueryResult{Status: LocationQueryResolved, Locations: want}, nil)
	if err != nil || len(got) != 1 || got[0].File != want[0].File {
		t.Fatalf("resolved compatibility = (%+v, %v), want locations unchanged", got, err)
	}

	for _, status := range []LocationQueryStatus{LocationQueryReadyEmpty, LocationQueryUnsupported} {
		got, err = legacyLocations(LocationQueryResult{Status: status}, nil)
		if err != nil || got != nil {
			t.Errorf("%s compatibility = (%+v, %v), want (nil, nil)", status, got, err)
		}
	}

	wantErr := errors.New("server unavailable")
	got, err = legacyLocations(LocationQueryResult{Status: LocationQueryUnavailable}, wantErr)
	if got != nil || !errors.Is(err, wantErr) {
		t.Errorf("unavailable compatibility = (%+v, %v), want (nil, %v)", got, err, wantErr)
	}
}
