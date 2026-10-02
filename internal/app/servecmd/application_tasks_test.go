package servecmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/semantic"
)

type applicationTaskDefinition struct {
	Project   string `json:"project"`
	Path      string `json:"path"`
	Offset    uint64 `json:"byte_offset"`
	Length    uint64 `json:"byte_length"`
	RawSHA256 string `json:"raw_sha256"`
}
type applicationTask struct {
	ID          string                      `json:"id"`
	Prompt      string                      `json:"prompt"`
	Target      semantic.SymbolKey          `json:"target"`
	Expected    []string                    `json:"expected_case_ids"`
	Gaps        []string                    `json:"known_coverage_gap_case_ids"`
	Definitions []applicationTaskDefinition `json:"expected_definitions"`
}
type applicationTaskGold struct {
	PredecessorSHA256 string            `json:"predecessor_sha256,omitempty"`
	Version           int               `json:"version"`
	SourceGoldSHA256  string            `json:"source_gold_sha256"`
	Tasks             []applicationTask `json:"tasks"`
	PairedProtocol    struct {
		Limits struct {
			Calls       int `json:"max_tool_calls"`
			Bytes       int `json:"max_response_bytes"`
			Evidence    int `json:"evidence_limit"`
			Definitions int `json:"definition_limit"`
		} `json:"limits"`
	} `json:"paired_protocol"`
}

func readApplicationTasks(t *testing.T) (applicationTaskGold, applicationGold) {
	t.Helper()
	base := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "application-outbox-20261001")
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	taskName, sourceName, taskSHA := "impact-task-gold-v1.json", "source-gold-v2.json", "12742046b0c89b8cd2e85de55e9fb76390c6679d3d006c6ab0219bcef3467d21"
	if version := os.Getenv("MOEDEX_APPLICATION_TASK_VERSION"); version == "2" {
		taskName, sourceName, taskSHA = "impact-task-gold-v2.json", "source-gold-v4.json", "04ae12b87c1819548380387b0f46202dba1a1dc6e8890d42efe680cd082b7f13"
	} else if version != "" && version != "1" {
		t.Fatal("unknown task gold version", version)
	}
	raw := read(taskName)
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != taskSHA {
		t.Fatal("frozen task expectations changed")
	}
	var tasks applicationTaskGold
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatal(err)
	}
	raw = read(sourceName)
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != tasks.SourceGoldSHA256 {
		t.Fatal("task source gold changed")
	}
	var gold applicationGold
	if err := json.Unmarshal(raw, &gold); err != nil {
		t.Fatal(err)
	}
	return tasks, gold
}

func TestApplicationTasksAreFrozenSourceExpectations(t *testing.T) {
	tasks, gold := readApplicationTasks(t)
	if (tasks.Version != 1 && tasks.Version != 2) || len(tasks.Tasks) != 4 || tasks.PairedProtocol.Limits.Calls != 12 || tasks.PairedProtocol.Limits.Bytes != 65536 {
		t.Fatal("unexpected task protocol")
	}
	cases := map[string]applicationCase{}
	for _, c := range gold.Cases {
		cases[c.ID] = c
	}
	ids := map[string]bool{}
	for _, task := range tasks.Tasks {
		if task.ID == "" || ids[task.ID] || task.Prompt == "" || len(task.Expected) == 0 || len(task.Definitions) == 0 {
			t.Fatal("invalid task", task.ID)
		}
		ids[task.ID] = true
		for _, id := range task.Expected {
			c, ok := cases[id]
			if !ok || c.Classification != "supported" || c.Target != task.Target {
				t.Fatal("unsupported/foreign task expectation", id)
			}
		}
		for _, id := range task.Gaps {
			c, ok := cases[id]
			if !ok || c.Classification != "unsupported" {
				t.Fatal("gap promoted implicitly", id)
			}
		}
		for _, d := range task.Definitions {
			if d.RawSHA256 == "" || gold.ReviewedClosure.SourceHashes[d.Path] != d.RawSHA256 || d.Length == 0 {
				t.Fatal("definition outside frozen source", d.Path)
			}
		}
	}
}

func TestApplicationTaskSuccessorPreservesProtocol(t *testing.T) {
	t.Setenv("MOEDEX_APPLICATION_TASK_VERSION", "1")
	prior, _ := readApplicationTasks(t)
	t.Setenv("MOEDEX_APPLICATION_TASK_VERSION", "2")
	next, gold := readApplicationTasks(t)
	if next.Version != 2 || gold.Version != 4 || next.PredecessorSHA256 != "12742046b0c89b8cd2e85de55e9fb76390c6679d3d006c6ab0219bcef3467d21" || !reflect.DeepEqual(next.PairedProtocol, prior.PairedProtocol) {
		t.Fatal("task protocol drift")
	}
	for i, c := range next.Tasks {
		old := prior.Tasks[i]
		if c.ID != old.ID || c.Target != old.Target || !reflect.DeepEqual(c.Definitions, old.Definitions) {
			t.Fatal("task source identity drift")
		}
		if c.ID == "submitted-message" {
			old.Expected = append(old.Expected, "saga-event")
			old.Gaps = []string{"controller-wrapper"}
			old.Prompt = c.Prompt
		}
		if c.ID == "attendee-message" {
			old.Expected = append(old.Expected, "fluent-publish-AddEventAttendee")
			old.Gaps = []string{}
			old.Prompt = c.Prompt
		}
		if !reflect.DeepEqual(c, old) {
			t.Fatal("unrelated task change", c.ID)
		}
	}
}
