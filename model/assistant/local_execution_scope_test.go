// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestLocalExecutionCapabilitiesRemainRequestScoped(t *testing.T) {
	t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "app.db"))
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	defer runtime.closeAgentRegistryCache()
	cfg := DefaultBotConfig()
	cfg.AgentCommandAllowlist = []string{"node"}
	cfg.AgentFileWriteEnabled = true
	cfg.AgentCommandSandbox = "off"
	owner, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, name := range []string{"run_command", "extract_archive"} {
		if _, ok := owner.Get(name); !ok {
			t.Fatalf("owner missing %s with coding plugin disabled", name)
		}
	}
	cfg.AgentCommandAllowlist = nil
	cfg.AgentFileWriteEnabled = false
	closed, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), MessageEvent{Kind: EventKindPrivate, UserID: "owner"}, RelationshipPolicy{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closed.Close()
	for _, name := range []string{"run_command", "extract_archive"} {
		if _, ok := closed.Get(name); ok {
			t.Fatalf("disabled bot borrowed %s", name)
		}
	}
	list, ok := owner.Get("list_capabilities")
	if !ok {
		t.Fatal("owner missing capabilities")
	}
	raw, err := list.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if len(report["execution"]) == 0 {
		t.Fatalf("execution report missing: %s", raw)
	}
	member, err := runtime.newAgentRegistry(context.Background(), cfg.WithDefaults(), MessageEvent{Kind: EventKindPrivate, UserID: "member"}, RelationshipPolicy{Score: 60})
	if err != nil {
		t.Fatal(err)
	}
	defer member.Close()
	for _, name := range []string{"run_command", "extract_archive", "list_capabilities"} {
		if _, ok := member.Get(name); ok {
			t.Fatalf("member acquired %s", name)
		}
	}
}
