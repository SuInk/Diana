// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 拼出来的提示词必须和手写那版逐字一致：这次改的是「怎么得到这段文字」，
// 不是「说什么」。模型侧的行为不该跟着动。
func TestIdentityPrivacyPromptRendersUnchanged(t *testing.T) {
	const want = "【会话标识隐私代理】消息中的真实用户 ID、群 ID 和消息 ID 已由本地代理替换为不透明别名。" +
		"相同别名始终表示同一对象；im_owner、im_current_user、im_bot、im_user、im_group、im_message 前缀保留角色语义。" +
		"理解对话时按角色和昵称判断，不要猜测真实数字。调用工具或在回复中需要引用标识时，必须原样复制别名——" +
		"包括 [diana-reply:im_message_xxx]、[diana-at:im_user_xxx] 这类标记；本地代理会在执行工具或发送消息前自动恢复真实标识。"
	if llmIdentityPrivacyPrompt != want {
		t.Fatalf("提示词变了\n实际:%s\n期望:%s", llmIdentityPrivacyPrompt, want)
	}
}

// 提示词里列举的角色要覆盖 normalizeIdentityPrivacyRole 真会产出的每一种，
// 否则模型会碰到一个提示词没交代过的前缀。
func TestIdentityAliasRolesCoverNormalizedRoles(t *testing.T) {
	produced := map[string]bool{}
	for _, input := range []string{"owner", "current", "current_user", "sender", "bot", "self", "group", "", "whatever"} {
		produced[normalizeIdentityPrivacyRole(input)] = true
	}
	listed := map[string]bool{}
	for _, role := range identityAliasRoles {
		listed[role] = true
	}
	for role := range produced {
		if !listed[role] {
			t.Errorf("normalizeIdentityPrivacyRole 会产出 %q，但 identityAliasRoles 没列它", role)
		}
	}
	if !listed["message"] {
		t.Error("message 别名由 registerMessageID 单独产出，也要列进去")
	}
	for _, role := range identityAliasRoles {
		if !strings.Contains(llmIdentityPrivacyPrompt, identityAliasPrefix+role) {
			t.Errorf("提示词没提到 %s%s", identityAliasPrefix, role)
		}
	}
}

// 别名前缀改过一次（qq_ → im_），当时散落在注释里讲同一件事的地方没跟上，
// 于是照着注释找 bug 的人会被带到一个已经不存在的前缀上。
// 这条守着整个包：再改前缀时，任何写着旧前缀的地方都会当场红。
func TestNoStaleAliasPrefixInPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// 别名的形状是 <前缀><角色>_<摘要>，摘要是十六进制；文档里写占位符时用 xxx。
	// 要求跟着摘要才不会把 get_group_id、cross_group_context 这类普通标识符算进来。
	stale := regexp.MustCompile(`\b[a-z][a-z0-9]{1,9}_(?:` + strings.Join(identityAliasRoles, "|") + `)_(?:[0-9a-f]{6,}|xxx)\b`)
	current := identityAliasPrefix
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for index, line := range strings.Split(string(body), "\n") {
			for _, hit := range stale.FindAllString(line, -1) {
				// 只有当前前缀开头的才算数；其余是改名时漏掉的旧写法。
				if strings.HasPrefix(hit, current) {
					continue
				}
				t.Errorf("%s:%d 写着过期的别名前缀 %q，当前是 %q\n    %s", name, index+1, hit, current, strings.TrimSpace(line))
			}
		}
	}
}
