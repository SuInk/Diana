// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/llm"
)

// 号码表边见边学：上一轮按原号发出的摘要，这一轮认识了那个号也要原样发，只追加的
// 前缀才不断；新正文照本轮的号码表换别名，旧正文里的别名照样还原得回来。
func TestIdentityPrivacySendsSentTextAsItWent(t *testing.T) {
	registry := &identityAliasRegistry{}
	memo := &privacySentMemo[map[string]string]{}
	newScope := func() *identityPrivacyScope {
		scope := newIdentityPrivacyScopeWithSalt("salt")
		scope.known = registry
		return scope
	}
	summary := "摘要：老群友 4671742 问过蟹棒，8800123 也在"
	first := llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "头部"}, {Role: llm.RoleUser, Content: summary}}}

	scope := newScope()
	scope.register("8800123", "user")
	sent := scope.protectRequestSent(first, memo, "group:30001")
	firstSent := sent.Messages[1].Content
	if !strings.Contains(firstSent, "4671742") || strings.Contains(firstSent, "8800123") {
		t.Fatalf("test setup: only the registered number is aliased the first time: %q", firstSent)
	}
	alias := scope.realToAlias["8800123"]

	// 下一轮 4671742 出现在结构化字段里，号码表认识它了。
	scope = newScope()
	scope.register("4671742", "user")
	if again := scope.protectText(summary); again == firstSent {
		t.Fatal("test setup: without the memo the summary would be re-aliased")
	}
	second := first
	second.Messages = append(append([]llm.Message(nil), first.Messages...), llm.Message{Role: llm.RoleUser, Content: "[12:00] 4671742: 又来了"})
	sent = scope.protectRequestSent(second, memo, "group:30001")
	if sent.Messages[1].Content != firstSent {
		t.Fatalf("sent text changed:\nbefore: %q\nnow:    %q", firstSent, sent.Messages[1].Content)
	}
	if strings.Contains(sent.Messages[2].Content, "4671742") {
		t.Fatalf("new text must use this turn's aliases: %q", sent.Messages[2].Content)
	}
	if got := scope.restoreText(alias); got != "8800123" {
		t.Fatalf("alias in the reused text must restore, got %q", got)
	}

	// 别的会话照本轮的规则换。
	other := newScope().protectRequestSent(first, memo, "group:30002")
	if strings.Contains(other.Messages[1].Content, "4671742") {
		t.Fatalf("another conversation must not reuse this one's text: %q", other.Messages[1].Content)
	}
}

// 靠上下文认出的密钥要等后面的消息说出来才知道：已经发过的正文不因此改写，新正文照换，
// 旧正文里的占位符照样还原。
func TestSecretPrivacySendsSentTextAsItWent(t *testing.T) {
	memo := &privacySentMemo[map[string]secretPrivacyEntry]{}
	known := &secretContextRegistry{}
	newScope := func() *secretPrivacyScope {
		scope := newSecretPrivacyScope("test-salt", nil, known)
		scope.sent, scope.route = memo, "group:30001"
		return scope
	}
	earlier := llm.Message{Role: llm.RoleUser, Content: "昨天连上了 mysql -pHunter2pass，key " + testSecretKey}
	first := llm.GenerateRequest{Messages: []llm.Message{earlier}}
	scope := newScope()
	firstSent := scope.protectRequest(first).Messages[1].Content
	if !strings.Contains(firstSent, "Hunter2pass") || strings.Contains(firstSent, testSecretKey) {
		t.Fatalf("test setup: the context secret is not known yet: %q", firstSent)
	}
	placeholder := secretmask.PlaceholderPattern.FindString(firstSent)

	second := llm.GenerateRequest{Messages: []llm.Message{earlier, {Role: llm.RoleUser, Content: "数据库密码是 Hunter2pass"}}}
	if again := newSecretPrivacyScope("test-salt", nil, known).protectRequest(second).Messages[1].Content; again == firstSent {
		t.Fatal("test setup: without the memo the earlier text would be re-masked")
	}
	scope = newScope()
	sent := scope.protectRequest(second)
	if sent.Messages[1].Content != firstSent {
		t.Fatalf("sent text changed:\nbefore: %q\nnow:    %q", firstSent, sent.Messages[1].Content)
	}
	if strings.Contains(sent.Messages[2].Content, "Hunter2pass") {
		t.Fatalf("new text must be masked: %q", sent.Messages[2].Content)
	}
	if got := scope.restoreForTool(placeholder); got != testSecretKey {
		t.Fatalf("placeholder in the reused text must restore, got %q", got)
	}
}
