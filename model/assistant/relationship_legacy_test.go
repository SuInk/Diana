// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyRomanceFieldsAreIgnored(t *testing.T) {
	var cfg BotConfig
	if err := json.Unmarshal([]byte(`{"owner_id":"owner","romance_enabled":true,"mood_enabled":true}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithDefaults()
	var payload ConfigPayload
	if err := json.Unmarshal([]byte(`{"owner_id":"owner","romance_enabled":true,"mood_enabled":true}`), &payload); err != nil {
		t.Fatal(err)
	}
	updated := ConfigFromPayload(payload, cfg)
	if !boolValue(updated.MoodEnabled, false) || updated.OwnerID != "owner" {
		t.Fatalf("existing settings changed: %#v", updated)
	}

	var profile UserMemoryProfile
	if err := json.Unmarshal([]byte(`{"user_id":"user","favorability":80,"message_count":60,"romance":{"active":true,"since":"2026-08-01T12:00:00Z"}}`), &profile); err != nil {
		t.Fatal(err)
	}
	policy := RelationshipPolicyForConfig(updated, profile, "user")
	if !reflect.DeepEqual(policy, RelationshipPolicyFor(profile, "owner", "user")) {
		t.Fatalf("legacy state changed relationship policy: %#v", policy)
	}
	for name, value := range map[string]any{
		"config": updated, "payload": PayloadFromConfig(updated),
		"snapshot": dianaBotConfigFromConfig(updated), "profile": profile,
	} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "romance") {
			t.Fatalf("%s still exposes removed fields: %s", name, body)
		}
	}
}

func TestRelationshipToolRejectsRemovedRomanceOperations(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	before := UserMemoryProfile{UserID: "user", Favorability: 80, MessageCount: 60}
	memory.profiles["user"] = before
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	tool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "user"})
	for _, operation := range []string{"romance_start", "romance_end"} {
		if _, err := tool.Run(context.Background(), map[string]any{"operation": operation}); err == nil {
			t.Fatalf("removed operation %q succeeded", operation)
		}
	}
	if !reflect.DeepEqual(memory.profiles["user"], before) || len(memory.favorabilityChanges["user"]) != 0 {
		t.Fatalf("removed operation changed user data: %#v", memory.profiles["user"])
	}
	schema, err := json.Marshal(tool.InputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(schema), "romance") || strings.Contains(tool.Description(), "romance") {
		t.Fatalf("removed operations still advertised: %s", schema)
	}
}
