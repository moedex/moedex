package server

import (
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

func TestBuildGraphSidecarTypesPublishersAndConsumers(t *testing.T) {
	dir := t.TempDir()
	eventContent := "public record OrderSubmitted(int Id);\n"
	consumerContent := "public class OrderConsumer : IConsumer<OrderSubmitted>\n{\n}\n"
	publisherContent := "public class Publisher\n{\n    public void Send()\n    {\n        endpoint.Publish(new OrderSubmitted(1));\n    }\n}\n"
	ix := index.New()
	ix.AddFile("fixture", "event.cs", filepath.Join(dir, "event.cs"), "event-sha", []byte(eventContent))
	ix.AddFile("fixture", "consumer.cs", filepath.Join(dir, "consumer.cs"), "consumer-sha", []byte(consumerContent))
	ix.AddFile("fixture", "publisher.cs", filepath.Join(dir, "publisher.cs"), "publisher-sha", []byte(publisherContent))
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}

	path, err := BuildGraphSidecar(dir)
	if err != nil {
		t.Fatalf("BuildGraphSidecar: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	eventOffset := uint64(strings.Index(eventContent, "OrderSubmitted"))
	want := map[diskgraph.EdgeType]bool{
		diskgraph.EdgePublishes: false,
		diskgraph.EdgeConsumes:  false,
	}
	for _, source := range g.Keys() {
		for _, edge := range g.Edges(source) {
			if edge.TargetBlob == "event-sha" && edge.TargetOffset == eventOffset {
				if _, tracked := want[edge.Type]; tracked {
					want[edge.Type] = true
				}
			}
		}
	}
	for edgeType, found := range want {
		if !found {
			t.Errorf("graph sidecar lacks %s edge to OrderSubmitted", edgeType)
		}
	}
}
