package webui

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSubscriptionTargetsKeepEachRobotAndRejectUnknownRobots(t *testing.T) {
	r := assistant.NewRuntime(assistant.BotConfig{ID: "qq"}, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(context.Background(), r)
	profiles := NewMemoryBotProfileStore(r.Config())
	if err := profiles.SaveProfiles(assistant.ProfileSet{ActiveID: "qq", Profiles: []assistant.BotConfig{{ID: "qq", Platform: assistant.PlatformOneBotV11}, {ID: "tg", Platform: assistant.PlatformTelegram}}}); err != nil {
		t.Fatal(err)
	}
	h.SetProfileStore(profiles)
	input := []repositoryWatchTargetPayload{{ProfileID: "qq", Destination: "group", GroupID: "100"}, {ProfileID: "tg", Destination: "private", UserID: "200"}}
	targets, err := h.subscriptionTargets(input, assistant.BotConfig{})
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets=%v err=%v", targets, err)
	}
	if targets[0].Platform != assistant.PlatformOneBotV11 || targets[1].Platform != assistant.PlatformTelegram || targets[1].ProfileID != "tg" || targets[1].ContextNamespace != "tg" {
		t.Fatal("targets inherited another robot's platform or namespace")
	}
	data, _ := json.Marshal(targets)
	returned := reminderDeliveryTargetsForWeb(assistant.Reminder{NotificationTargetsJSON: string(data)})
	if len(returned) != 2 || returned[0].ProfileID != "qq" || returned[1].ProfileID != "tg" || returned[1].UserID != "200" {
		t.Fatal("editing round trip lost a robot or target")
	}
	input[1].ProfileID = "missing"
	if _, err := h.subscriptionTargets(input, assistant.BotConfig{ID: "qq"}); err == nil {
		t.Fatal("unknown robot fell back to the first target's robot")
	}
}
