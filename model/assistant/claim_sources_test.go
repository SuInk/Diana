// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func TestReplyLinkPolicyStaysSilentOnDefaultAndOverridesOtherwise(t *testing.T) {
	plugins := NewDefaultPluginManager()
	runtime := &Runtime{plugins: plugins}
	event := MessageEvent{GroupID: "99"}
	if rule := runtime.replyLinkPolicyContext(event); rule != "" {
		t.Fatalf("默认档不该注入链接规则，避免和人设打架：%q", rule)
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingLinkPolicy: replyLinkPolicyAlways}); err != nil {
		t.Fatal(err)
	}
	if rule := runtime.replyLinkPolicyContext(event); !strings.Contains(rule, "最多一条") {
		t.Fatalf("附带链接档应给出明确规则：%q", rule)
	}
	if _, err := plugins.UpdateSettings(webSearchPluginID, map[string]any{webSearchSettingLinkPolicy: replyLinkPolicyNever}); err != nil {
		t.Fatal(err)
	}
	if rule := runtime.replyLinkPolicyContext(event); !strings.Contains(rule, "不要在回复里给出 URL") {
		t.Fatalf("禁链档应给出明确规则：%q", rule)
	}
}
