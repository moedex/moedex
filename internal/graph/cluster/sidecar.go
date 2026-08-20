package cluster

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	SidecarVersion  = 1
	StatusAvailable = "available"
	StatusOverCap   = "over_cap"
)

// Sidecar is the generation-bound, independently versioned cluster artifact.
// Over-cap builds persist the envelope and counts but never partial communities.
type Sidecar struct {
	Version       int       `json:"version"`
	Generation    uint64    `json:"generation"`
	Status        string    `json:"status"`
	EligibleNodes int       `json:"eligible_nodes"`
	EligibleEdges int       `json:"eligible_edges"`
	Cap           int       `json:"cap"`
	BuildMillis   int64     `json:"build_millis"`
	Clusters      []Cluster `json:"clusters"`
}

type BuildReport struct {
	Status        string `json:"status"`
	EligibleNodes int    `json:"eligible_nodes"`
	EligibleEdges int    `json:"eligible_edges"`
	Cap           int    `json:"cap"`
	Clusters      int    `json:"clusters"`
	BuildMillis   int64  `json:"build_millis"`
}

func (s Sidecar) Report() BuildReport {
	return BuildReport{
		Status: s.Status, EligibleNodes: s.EligibleNodes, EligibleEdges: s.EligibleEdges,
		Cap: s.Cap, Clusters: len(s.Clusters), BuildMillis: s.BuildMillis,
	}
}

func Save(path string, sidecar Sidecar) error {
	if sidecar.Clusters == nil {
		sidecar.Clusters = []Cluster{}
	}
	if err := validateSidecar(sidecar); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".clusters-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	if err := enc.Encode(sidecar); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func Load(path string, generation uint64) (*Sidecar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var sidecar Sidecar
	if err := dec.Decode(&sidecar); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("cluster: sidecar contains trailing JSON")
		}
		return nil, err
	}
	if sidecar.Version != SidecarVersion {
		return nil, fmt.Errorf("cluster: sidecar version %d is unsupported (want %d)", sidecar.Version, SidecarVersion)
	}
	if sidecar.Generation != generation {
		return nil, fmt.Errorf("cluster: stale sidecar generation %d (graph generation %d)", sidecar.Generation, generation)
	}
	if sidecar.Clusters == nil {
		sidecar.Clusters = []Cluster{}
	}
	if err := validateSidecar(sidecar); err != nil {
		return nil, err
	}
	return &sidecar, nil
}

func validateSidecar(sidecar Sidecar) error {
	if sidecar.Version != SidecarVersion {
		return fmt.Errorf("cluster: sidecar version %d is unsupported (want %d)", sidecar.Version, SidecarVersion)
	}
	if sidecar.Status != StatusAvailable && sidecar.Status != StatusOverCap {
		return fmt.Errorf("cluster: invalid sidecar status %q", sidecar.Status)
	}
	if sidecar.EligibleNodes < 0 || sidecar.EligibleEdges < 0 || sidecar.Cap < 1 || sidecar.BuildMillis < 0 {
		return fmt.Errorf("cluster: invalid negative counts or non-positive cap")
	}
	if sidecar.Status == StatusOverCap {
		if sidecar.EligibleNodes <= sidecar.Cap {
			return fmt.Errorf("cluster: over_cap status has %d eligible nodes within cap %d", sidecar.EligibleNodes, sidecar.Cap)
		}
		if len(sidecar.Clusters) != 0 {
			return fmt.Errorf("cluster: over_cap sidecar must not contain partial communities")
		}
		return nil
	}
	if sidecar.EligibleNodes > sidecar.Cap {
		return fmt.Errorf("cluster: available sidecar has %d eligible nodes above cap %d", sidecar.EligibleNodes, sidecar.Cap)
	}
	totalMembers := 0
	for i, community := range sidecar.Clusters {
		if community.ClusterID != i+1 {
			return fmt.Errorf("cluster: community %d has non-deterministic cluster_id %d", i, community.ClusterID)
		}
		if community.MemberCount < 1 || community.MemberCount != len(community.Members) {
			return fmt.Errorf("cluster: cluster %d member_count %d does not match %d members", community.ClusterID, community.MemberCount, len(community.Members))
		}
		if community.Label == "" {
			return fmt.Errorf("cluster: cluster %d has no label", community.ClusterID)
		}
		totalMembers += community.MemberCount
		for j, member := range community.Members {
			if member.ID == "" {
				return fmt.Errorf("cluster: cluster %d member %d has no ID", community.ClusterID, j)
			}
			if j > 0 && community.Members[j-1].ID >= member.ID {
				return fmt.Errorf("cluster: cluster %d members are not deterministically ordered", community.ClusterID)
			}
		}
	}
	if totalMembers != sidecar.EligibleNodes {
		return fmt.Errorf("cluster: %d clustered members do not match %d eligible nodes", totalMembers, sidecar.EligibleNodes)
	}
	return nil
}
