package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestChatModelRouteControls(t *testing.T) {
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{ProfileID: "one", Model: "shared", ReasoningEffort: "high", Weight: 2, Fallbacks: []ModelRole{{ProfileID: "two", Model: "other", ReasoningEffort: "low", Weight: 3, Standby: true}}})
	tool := newDianaLLMConfigTool(r, event)
	for _, operation := range []string{"disable", "enable"} {
		for _, target := range []struct {
			id, model string
			index     int
		}{{"one", "shared", 0}, {"two", "other", 1}} {
			reply, err := tool.Run(context.Background(), map[string]any{"operation": operation, "provider_id": target.id, "model": target.model})
			if err != nil || !strings.Contains(reply, `"tested":false`) {
				t.Fatalf("reply=%s err=%v", reply, err)
			}
			role := saver.configs["b"].ModelRoles["chat"]
			routes := []ModelRole{role, role.Fallbacks[0]}
			if routes[target.index].Disabled != (operation == "disable") {
				t.Fatal(routes)
			}
			if role.ReasoningEffort != "high" || role.Fallbacks[0].ReasoningEffort != "low" || role.Fallbacks[0].Weight != 3 || !role.Fallbacks[0].Standby {
				t.Fatal(role)
			}
			if !reflect.DeepEqual(role, r.effectiveConfigForEvent(event).ModelRoles["chat"]) {
				t.Fatal("runtime differs")
			}
		}
	}
	for _, strategy := range []string{"smart", "failover", "round_robin", "weighted"} {
		if _, err := tool.Run(context.Background(), map[string]any{"operation": "set_strategy", "routing_strategy": strategy}); err != nil {
			t.Fatal(err)
		}
		role := saver.configs["b"].ModelRoles["chat"]
		if role.RoutingStrategy != strategy || role.Model != "shared" || role.Fallbacks[0].Model != "other" || role.Weight != 2 {
			t.Fatal(role)
		}
	}
	if saver.configs["a"].ModelRoles["chat"].Disabled {
		t.Fatal("other bot changed")
	}
}

func TestChatModelRouteControlsRejectWithoutMutation(t *testing.T) {
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{ProfileID: "one", Model: "shared", Fallbacks: []ModelRole{{ProfileID: "two", Model: "shared"}}})
	tool := newDianaLLMConfigTool(r, event)
	for _, input := range []map[string]any{
		{"operation": "disable", "model": "shared"},
		{"operation": "disable", "model": "missing"},
		{"operation": "disable", "model": "shared", "provider_id": "missing"},
		{"operation": "disable", "model": "shared", "provider_id": "one", "group": "x"},
		{"operation": "enable"},
		{"operation": "disable", "model": "shared", "scope": "group"},
		{"operation": "set_strategy", "routing_strategy": "unknown"},
		{"operation": "set_strategy", "routing_strategy": "smart", "role": "image"},
		{"operation": "set_strategy", "routing_strategy": "weighted", "scope": "group"},
		{"operation": "set_strategy", "routing_strategy": "weighted", "model": "shared"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	if saver.calls != 0 {
		t.Fatal("invalid operations persisted")
	}
	input := map[string]any{"operation": "disable", "model": "shared", "provider_id": "one"}
	saver.err = errors.New("disk full")
	if _, err := tool.Run(context.Background(), input); err == nil {
		t.Fatal("save error hidden")
	}
	if r.effectiveConfigForEvent(event).ModelRoles["chat"].Disabled {
		t.Fatal("failed save changed runtime")
	}
	saver.err = nil
	event.UserID = "stranger"
	if _, err := newDianaLLMConfigTool(r, event).Run(context.Background(), input); err == nil {
		t.Fatal("unauthorized write")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Run(ctx, input); err == nil {
		t.Fatal("cancelled write")
	}
	if saver.calls != 0 {
		t.Fatal("failed operations persisted")
	}
}

func TestChatModelGroupRouteAndInheritedControls(t *testing.T) {
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{Group: "shared", Model: "m", Fallbacks: []ModelRole{{ProfileID: "one", Model: "m"}}})
	tool := newDianaLLMConfigTool(r, event)
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "disable", "group": "shared", "model": "m"}); err != nil {
		t.Fatal(err)
	}
	role := saver.configs["b"].ModelRoles["chat"]
	if !role.Disabled || role.Fallbacks[0].Disabled {
		t.Fatal(role)
	}
	bot := saver.configs["b"]
	bot.ModelRoles["vision"] = ModelRole{FollowChat: true}
	saver.configs["b"] = bot
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{saver.configs["a"], bot}})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "set_strategy", "role": "vision", "routing_strategy": "weighted"}); err == nil {
		t.Fatal("silently changed followed role")
	}
	off := false
	bot.OwnerLLMConfigEnabled = &off
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{saver.configs["a"], bot}})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "enable", "group": "shared", "model": "m"}); err == nil {
		t.Fatal("ignored config switch")
	}
}
