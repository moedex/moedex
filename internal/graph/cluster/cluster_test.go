package cluster

import (
	"reflect"
	"testing"
)

func TestDetectFindsTwoObviousCommunities(t *testing.T) {
	nodes := []Node{
		{ID: "a1", Repo: "accounts"}, {ID: "a2", Repo: "accounts"}, {ID: "a3", Repo: "shared"},
		{ID: "b1", Namespace: "platform/billing"}, {ID: "b2", Namespace: "platform/billing"}, {ID: "b3", Repo: "shared"},
	}
	edges := []Edge{
		{Source: "a1", Target: "a2", Weight: 1},
		{Source: "a2", Target: "a3", Weight: 1},
		{Source: "a3", Target: "a1", Weight: 1},
		{Source: "b1", Target: "b2", Weight: 1},
		{Source: "b2", Target: "b3", Weight: 1},
		{Source: "b3", Target: "b1", Weight: 1},
		{Source: "a3", Target: "b3", Weight: 0.05},
	}

	got := Detect(nodes, edges)
	if len(got) != 2 {
		t.Fatalf("Detect returned %d clusters, want 2: %#v", len(got), got)
	}
	if ids := memberIDs(got[0]); !reflect.DeepEqual(ids, []string{"a1", "a2", "a3"}) {
		t.Errorf("first community = %v, want [a1 a2 a3]", ids)
	}
	if ids := memberIDs(got[1]); !reflect.DeepEqual(ids, []string{"b1", "b2", "b3"}) {
		t.Errorf("second community = %v, want [b1 b2 b3]", ids)
	}
	if got[0].Label != "accounts" || got[1].Label != "platform/billing" {
		t.Errorf("labels = %q, %q, want accounts and platform/billing", got[0].Label, got[1].Label)
	}
	for _, community := range got {
		if community.MemberCount != len(community.Members) {
			t.Errorf("cluster %d member_count = %d, members = %d", community.ClusterID, community.MemberCount, len(community.Members))
		}
	}
}

func TestDetectKeepsIsolatedNodesAsSingletons(t *testing.T) {
	got := Detect([]Node{{ID: "connected-a"}, {ID: "connected-b"}, {ID: "isolated", Repo: "lonely"}}, []Edge{
		{Source: "connected-a", Target: "connected-b", Weight: 1},
	})
	if len(got) != 2 {
		t.Fatalf("Detect returned %d clusters, want connected pair plus singleton: %#v", len(got), got)
	}
	var singleton *Cluster
	for i := range got {
		if got[i].MemberCount == 1 && got[i].Members[0].ID == "isolated" {
			singleton = &got[i]
		}
	}
	if singleton == nil {
		t.Fatalf("isolated node was not retained as a singleton: %#v", got)
	}
	if singleton.Label != "lonely" {
		t.Errorf("singleton label = %q, want lonely", singleton.Label)
	}
}

func TestDetectIsDeterministicAcrossInputOrder(t *testing.T) {
	nodes := []Node{{ID: "c", Repo: "z"}, {ID: "a", Repo: "a"}, {ID: "b", Repo: "a"}}
	edges := []Edge{{Source: "b", Target: "c", Weight: 1}, {Source: "a", Target: "b", Weight: 1}}
	want := Detect(nodes, edges)
	got := Detect([]Node{nodes[2], nodes[0], nodes[1]}, []Edge{edges[1], edges[0]})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input order changed result:\n got  %#v\n want %#v", got, want)
	}
}

func memberIDs(community Cluster) []string {
	ids := make([]string, len(community.Members))
	for i, member := range community.Members {
		ids[i] = member.ID
	}
	return ids
}
