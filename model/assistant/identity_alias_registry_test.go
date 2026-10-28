// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 只追加续接把前几轮的请求原样带着走。上一轮登记过的号码这一轮不在历史窗口里，
// 也必须换成同一个别名，否则前缀从那里断开、真实号码漏给上游。
func TestIdentityAliasesStayStableAfterLeavingWindow(t *testing.T) {
	enabled := true
	runtime := NewRuntime(BotConfig{LLMIdentityMaskingEnabled: &enabled}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	base := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "2000002", GroupID: "5000005", Time: 1}

	first := base
	first.UserID, first.MessageID = "3000003", "-77700001"
	scope1 := identityPrivacyScopeFromContext(runtime.withIdentityPrivacyContext(context.Background(), first, nil))
	line := "[跨群历史] 远野（3000003）: 看 message_id=-77700001，价格 1999 元"
	before := scope1.protectText(line)
	if strings.Contains(before, "3000003") || strings.Contains(before, "77700001") || !strings.Contains(before, "1999") {
		t.Fatalf("第一轮脱敏不对: %s", before)
	}

	// 下一轮换了发言者，那个号码和消息都不在本轮窗口里；别的群也一样认得。
	for _, groupID := range []string{"5000005", "6000006"} {
		next := base
		next.GroupID, next.UserID, next.MessageID = groupID, "4000004", "77700002"
		scope := identityPrivacyScopeFromContext(runtime.withIdentityPrivacyContext(context.Background(), next, nil))
		if after := scope.protectText(line); after != before {
			t.Fatalf("群 %s 同一行脱敏结果变了:\n%s\n%s", groupID, before, after)
		}
		// 模型照抄别名调工具，要还原得回真实号码。
		alias := scope1.register("3000003", "user")
		if got := scope.restoreText(alias); got != "3000003" {
			t.Fatalf("别名还原失败: %q", got)
		}
	}
}

// 短号码不进全局名单：Telegram 六位递增的消息 ID 收进来，正文里同样的数字也会被换掉。
func TestIdentityAliasRegistrySkipsShortNumbers(t *testing.T) {
	var registry identityAliasRegistry
	registry.remember("265075", "message")
	registry.remember("12345678", "user")
	if _, ok := registry.role("265075"); ok {
		t.Fatal("六位号码不该进全局名单")
	}
	if role, ok := registry.role("12345678"); !ok || role != "user" {
		t.Fatalf("八位号码应当记住: %q %v", role, ok)
	}
}
