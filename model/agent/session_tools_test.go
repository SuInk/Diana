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
	loader := newDeferredToolLoader(registry, []string{"common"}, nil, nil)
	definitions := append(registry.Definitions(), finalizeToolDefinition(false))
	first := loader.filter(definitions)
	loader.restore([]string{"rare", "admin", "rare", "gone"})
	next := loader.filter(definitions)
	if !reflect.DeepEqual(first, next) {
		t.Fatal("loading rewrote provider tool definitions")
	}
	if got := toolDefinitionNames(next); !reflect.DeepEqual(got, []string{"common", finalizeToolName, ToolsLoadToolName, ToolsExecuteToolName}) {
		t.Fatal(got)
	}
	contracts := loader.loadedContracts(true, false)
	if !strings.Contains(contracts, `"name":"rare"`) || !strings.Contains(contracts, `"name":"admin"`) {
		t.Fatalf("restored contracts = %s", contracts)
	}
	member := newDeferredToolLoader(NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}), []string{"common"}, nil, nil)
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
	messages := secondClient.requests[0].Messages
	if strings.Contains(messages[0].Content, `"name":"rare"`) {
		t.Fatal("restored schema leaked into stable prompt")
	}
	found := false
	for _, message := range messages[1 : len(messages)-1] {
		if strings.Contains(message.Content, `"name":"rare"`) && strings.Contains(message.Content, `"inputSchema":`) {
			found = true
			if message.Role != llm.RoleUser || message.Priority != llm.MessagePrioritySystem {
				t.Fatalf("restored contract must stay inline and retain system budget priority: %#v", message)
			}
		}
	}
	if !found {
		t.Fatal("restored schema missing from volatile tail")
	}
}

func TestFinalizeSchemaIsStableWhenTaskOrEvidenceStateChanges(t *testing.T) {
	a, _ := json.Marshal(finalizeToolDefinition(false))
	b, _ := json.Marshal(finalizeToolDefinition(true))
	if string(a) != string(b) {
		t.Fatal("live state changed finalize schema")
	}
}

type longDescriptionTool struct{ countingTool }

func (*longDescriptionTool) Description() string {
	return strings.Repeat("这一段描述很长，目录那一行会把它截短。", 20)
}

// 目录那一行已经是完整描述时，契约只带 inputSchema；目录截短过的、或者调用方换了系统
// 提示词（公共目录不在请求里）时，契约照旧带完整描述。
func TestLoadedContractsSkipDescriptionTheCatalogAlreadyShows(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"}, &longDescriptionTool{countingTool{name: "long"}})
	loader := newDeferredToolLoader(registry, []string{"common"}, nil, nil)
	loader.restore([]string{"rare", "long"})
	if !strings.Contains(loader.catalog(), "- rare") || !strings.Contains(loader.catalog(), ": test tool") {
		t.Fatalf("catalog = %s", loader.catalog())
	}
	descriptions := func(catalogShown bool) map[string]string {
		var contracts []struct {
			Name        string         `json:"name"`
			Description *string        `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		if err := json.Unmarshal([]byte(loader.loadedContracts(true, catalogShown)), &contracts); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, contract := range contracts {
			if contract.InputSchema == nil {
				t.Fatalf("contract %s lost its inputSchema", contract.Name)
			}
			if contract.Description != nil {
				out[contract.Name] = *contract.Description
			}
		}
		return out
	}
	got := descriptions(true)
	if _, ok := got["rare"]; ok {
		t.Fatalf("description already in the catalog was repeated: %v", got)
	}
	if got["long"] != (&longDescriptionTool{}).Description() {
		t.Fatalf("truncated catalog line must keep the full description: %v", got)
	}
	if got := descriptions(false); got["rare"] != "test tool" {
		t.Fatalf("without the catalog the contract must carry the description: %v", got)
	}
}
