package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestSessionToolDefinitionsStayStableAfterLoading(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}, &countingTool{name: "admin"})
	loader := newDeferredToolLoader(registry, []string{"common"})
	definitions := append(registry.Definitions(), finalizeToolDefinition(false))
	first := loader.filter(definitions)
	if _, err := loader.Run(t.Context(), map[string]any{"names": []any{"rare", "admin"}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, loader.filter(definitions)) {
		t.Fatal("loading rewrote native declarations")
	}
	member := newDeferredToolLoader(NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}), []string{"common"})
	if _, err := member.Run(t.Context(), map[string]any{"names": []any{"admin"}}); err == nil {
		t.Fatal("unauthorized contract loaded")
	}
}

func TestRunnerDoesNotCarryLoadedContractsAcrossRuns(t *testing.T) {
	rare := &countingTool{name: "rare"}
	firstClient := &deferredLoadClient{}
	runner, err := NewRunner(firstClient, Config{MaxSteps: 4, CoreTools: []string{"common"}}, NewToolRegistry(&countingTool{name: "common"}, rare))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}}
	if _, err = runner.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	secondClient := &dispatchTestClient{replies: []*llm.GenerateResponse{dispatchReply(true, "rare", map[string]any{}), executeReply(true, "rare", map[string]any{})}}
	runner.client = secondClient
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if rare.calls != 2 || len(result.Steps) != 2 || !result.Steps[0].Skipped || result.Steps[1].Skipped {
		t.Fatalf("state leaked across runs: calls=%d steps=%#v", rare.calls, result.Steps)
	}
	for _, message := range secondClient.requests[0].Messages {
		if strings.Contains(message.Content, `"inputSchema"`) {
			t.Fatal("old contract was automatically injected")
		}
	}
	if !reflect.DeepEqual(firstClient.requests[0].Tools, secondClient.requests[0].Tools) {
		t.Fatal("native definitions changed across runs")
	}
}

func TestFinalizeSchemaIsStableWhenTaskOrEvidenceStateChanges(t *testing.T) {
	a, _ := json.Marshal(finalizeToolDefinition(false))
	b, _ := json.Marshal(finalizeToolDefinition(true))
	if string(a) != string(b) {
		t.Fatal("live state changed finalize schema")
	}
}
