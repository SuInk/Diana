package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestSessionToolDefinitionsAppendAndRestoreOnlyAllowedTools(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}, &countingTool{name: "admin"})
	loader := newDeferredToolLoader(registry, []string{"common"})
	definitions := append(registry.Definitions(), finalizeToolDefinition(nil, false))
	first := loader.filter(definitions)
	loader.restore([]string{"rare", "admin", "rare", "gone"})
	next := loader.filter(definitions)
	if !reflect.DeepEqual(first, next) {
		t.Fatal("loading rewrote provider tool definitions")
	}
	if got := toolDefinitionNames(next); !reflect.DeepEqual(got, []string{"common", finalizeToolName, ToolsLoadToolName, ToolsExecuteToolName}) {
		t.Fatal(got)
	}
	contracts := loader.loadedContracts()
	if !strings.Contains(contracts, `"name":"rare"`) || !strings.Contains(contracts, `"name":"admin"`) {
		t.Fatalf("restored contracts = %s", contracts)
	}
	member := newDeferredToolLoader(NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}), []string{"common"})
	member.restore(loader.order)
	if !reflect.DeepEqual(member.order, []string{"rare"}) {
		t.Fatalf("permissions bypassed: %v", member.order)
	}
}

func TestRunnerRestoresDiscoveredToolsOnNewRun(t *testing.T) {
	var loaded []string
	newRunner := func(client llm.LLMClient, rare *countingTool) *Runner {
		runner, err := NewRunner(client, Config{MaxSteps: 4, CoreTools: []string{"common"}}, NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}))
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	firstClient := &deferredLoadClient{}
	request := Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "test"}}, ToolsLoaded: func(names []string) { loaded = names }}
	if _, err := newRunner(firstClient, &countingTool{name: "rare"}).Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, []string{"rare"}) {
		t.Fatal(loaded)
	}
	previous, _ := json.Marshal(firstClient.requests[1].Tools)
	request.LoadedTools = loaded
	rare := &countingTool{name: "rare"}
	secondClient := &dispatchTestClient{replies: []*llm.GenerateResponse{executeReply(true, "rare", map[string]any{})}}
	runner, err := NewRunner(secondClient, Config{MaxSteps: 4, CoreTools: []string{"common"}}, NewToolRegistry(&countingTool{name: "common"}, rare))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if rare.calls != 1 {
		t.Fatalf("restored tool calls = %d", rare.calls)
	}
	next, _ := json.Marshal(secondClient.requests[0].Tools)
	if string(previous) != string(next) {
		t.Fatal("restoring a tool rewrote provider definitions")
	}
	if system := secondClient.requests[0].Messages[0].Content; !strings.Contains(system, `"name":"rare"`) {
		t.Fatalf("restored schema missing from stable prompt: %s", system)
	}
}

func TestFinalizeSchemaIsStableWhenTaskOrEvidenceStateChanges(t *testing.T) {
	a, _ := json.Marshal(finalizeToolDefinition(nil, false))
	b, _ := json.Marshal(finalizeToolDefinition(&claimEvidenceLedger{active: true}, true))
	if string(a) != string(b) {
		t.Fatal("live state changed finalize schema")
	}
}
