package assistant

import "testing"

func TestDianaBotConfigSnapshotIncludesRestoredBehavior(t *testing.T) {
	disabled := false
	cfg := BotConfig{
		OwnerLoginEnabled:            true,
		BotReplyLoopDetectionEnabled: &disabled,
		GroupAdmission:               GroupAdmission{Mode: GroupAdmissionWhitelist, AllowedGroups: []string{"10001"}},
		ReplyGate:                    &ReplyGate{MinGroupLevel: 12},
		ReplyReferenceEnabled:        &disabled,
		RecallReplyAutoDeleteEnabled: &disabled,
		ModelRoles: map[string]ModelRole{
			"chat": {ProfileID: "profile-a", Model: "model-a"},
		},
		ReplyRules: []ReplyRule{{ID: "rule-a", Enabled: true, Prompt: "voice", Action: ReplyRuleActionVoice}},
	}.WithDefaults()

	snapshot := dianaBotConfigFromConfig(cfg)
	if !snapshot.OwnerLoginEnabled {
		t.Fatal("owner login setting was omitted")
	}
	if snapshot.GroupAdmission.Mode != GroupAdmissionWhitelist || len(snapshot.GroupAdmission.AllowedGroups) != 1 {
		t.Fatalf("group admission = %#v", snapshot.GroupAdmission)
	}
	if snapshot.ReplyGate == nil || snapshot.ReplyGate.MinGroupLevel != 12 {
		t.Fatalf("reply gate = %#v", snapshot.ReplyGate)
	}
	if snapshot.ReplyReferenceEnabled || snapshot.RecallReplyAutoDeleteEnabled || snapshot.BotReplyLoopDetectionEnabled {
		t.Fatalf("explicitly disabled behavior was not preserved: %#v", snapshot)
	}
	if snapshot.ModelRoles["chat"].Model != "model-a" || len(snapshot.ReplyRules) != 1 {
		t.Fatalf("model roles or reply rules missing: %#v", snapshot)
	}
	if snapshot.PassiveRouterPromptChars == 0 || snapshot.PromptPlaintextRulesChars == 0 {
		t.Fatalf("restored prompt metadata missing: %#v", snapshot)
	}
}
