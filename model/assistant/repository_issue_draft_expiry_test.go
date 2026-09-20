// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 过了有效期的草稿不能再用确认码提交：确认码只有 6 位，草稿无限期有效意味着
// 它永远可以被翻出来执行。
func TestExpiredDraftCannotBeApproved(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
		repositoryPublishSettingTimeout:   5,
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	toolFor := func(rawMessage string) *dianaGitHubTool {
		return newDianaGitHubTool(runtime,
			MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: rawMessage},
			plugin, settings)
	}

	drafted := runRepositoryPublishToolOnce(t, toolFor("给 acme/demo 提个 issue，说搜索结果排序不对"), map[string]any{
		"operation":  "create",
		"repository": "acme/demo",
		"title":      "搜索结果排序不对",
		"body":       "关键词命中的条目排在后面。",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}
	draftID := drafted.Draft.ID
	code := repositoryIssueConfirmationCode(draftID)

	expireRepositoryIssueDraft(t, plugin, draftID)

	approved := runRepositoryPublishToolOnce(t, toolFor("确认 "+code), map[string]any{
		"operation": "approve",
		"draft_id":  draftID,
	})
	if approved.OK || approved.FailureCode != "draft_expired" {
		t.Fatalf("过期草稿不应当被批准：%#v", approved)
	}
	if len(github.requests) != 0 {
		t.Fatalf("过期草稿碰了 GitHub：%#v", github.requests)
	}

	// 后台还原会换一个确认码：旧码在群里公开过，又躺了至少七天。
	restoredDraft, err := plugin.RestoreDraft(context.Background(), draftID)
	if err != nil {
		t.Fatalf("还原草稿失败：%v", err)
	}
	newCode := draftConfirmationCode(restoredDraft)
	if newCode == "" || newCode == code {
		t.Fatalf("还原后应当换一个确认码：旧 %q 新 %q", code, newCode)
	}
	stale := runRepositoryPublishToolOnce(t, toolFor("确认 "+code), map[string]any{
		"operation": "approve",
		"draft_id":  draftID,
	})
	if stale.OK {
		t.Fatalf("旧确认码不该还能批准：%#v", stale)
	}
	restored := runRepositoryPublishToolOnce(t, toolFor("确认 "+newCode), map[string]any{
		"operation": "approve",
		"draft_id":  draftID,
	})
	if !restored.OK {
		t.Fatalf("还原后用新确认码应当可以批准：%#v", restored)
	}
}

// 后台发布不需要确认码：WebUI 的调用者已经登录过控制台。还原之后没人往群里
// 播报新码，没有这条路径草稿就只能干等着。
func TestPublishDraftFromWebSkipsConfirmationCode(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
		repositoryPublishSettingTimeout:   5,
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaGitHubTool(runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: "给 acme/demo 提个 issue，说导航栏在窄屏会错位"},
		plugin, settings)
	drafted := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation":  "create",
		"repository": "acme/demo",
		"title":      "导航栏在窄屏会错位",
		"body":       "宽度小于 400px 时菜单换行。",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}

	ctx := context.Background()
	result, err := plugin.PublishDraftFromWeb(ctx, settings, drafted.Draft.ID)
	if err != nil {
		t.Fatalf("后台发布失败：%v", err)
	}
	if !result.OK || result.Issue == nil {
		t.Fatalf("后台发布应当写入 GitHub：%#v", result)
	}
	// 草稿要标记成已创建，否则群里还能拿确认码再写一次。
	stored, ok, err := plugin.draftByID(ctx, drafted.Draft.ID)
	if err != nil || !ok || stored.Status != "created" {
		t.Fatalf("草稿状态没有更新：status=%q ok=%v err=%v", stored.Status, ok, err)
	}
	if _, err := plugin.PublishDraftFromWeb(ctx, settings, drafted.Draft.ID); err == nil {
		t.Fatal("已处理的草稿不该能再次发布")
	}
	if _, err := plugin.PublishDraftFromWeb(ctx, settings, "不存在"); err == nil {
		t.Fatal("不存在的草稿应当报错")
	}
}

// 过期只约束群里那条确认码链路；后台是登录过的管理员，照样能直接提交。
func TestPublishDraftFromWebWorksOnExpiredDraft(t *testing.T) {
	github := newRepositoryPublishTestGitHub()
	server := httptest.NewServer(http.HandlerFunc(github.handler))
	defer server.Close()

	plugin := newRepositoryPublishPlugin(server.Client(), server.URL)
	settings := SettingValues{
		repositoryPublishSettingToken:     repositoryPublishTestToken,
		repositoryPublishSettingAllowlist: "acme/demo",
		repositoryPublishSettingTimeout:   5,
	}
	runtime := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaGitHubTool(runtime,
		MessageEvent{Kind: EventKindPrivate, UserID: "owner", RawMessage: "给 acme/demo 提个 issue，说日志页筛选会丢状态"},
		plugin, settings)
	drafted := runRepositoryPublishToolOnce(t, tool, map[string]any{
		"operation":  "create",
		"repository": "acme/demo",
		"title":      "日志页筛选会丢状态",
		"body":       "翻页之后筛选条件被重置。",
	})
	if drafted.Draft == nil {
		t.Fatalf("没有生成草稿：%#v", drafted)
	}
	expireRepositoryIssueDraft(t, plugin, drafted.Draft.ID)

	result, err := plugin.PublishDraftFromWeb(context.Background(), settings, drafted.Draft.ID)
	if err != nil || !result.OK {
		t.Fatalf("过期草稿在后台应当可以直接提交：err=%v result=%#v", err, result)
	}
}

func TestRepositoryIssueDraftPurgeCutoff(t *testing.T) {
	now := time.Now()
	cutoff := RepositoryIssueDraftPurgeCutoff(now)
	// 刚过期的草稿还要留 30 天，不能立刻删。
	justExpired := now.Add(-repositoryIssueDraftTTL - time.Hour)
	if !justExpired.After(cutoff) {
		t.Fatalf("刚过期的草稿被判成该删：cutoff=%s created=%s", cutoff, justExpired)
	}
	// 过期满 30 天的才删。
	longGone := now.Add(-repositoryIssueDraftTTL - repositoryIssueDraftPurgeAfter - time.Hour)
	if !longGone.Before(cutoff) {
		t.Fatalf("过期超过保留期的草稿没被判成该删：cutoff=%s created=%s", cutoff, longGone)
	}
}

func TestDraftExpiryFallsBackToCreatedAt(t *testing.T) {
	// 升级前存下的草稿没有 expires_at，按创建时间加有效期折算，不能一律当成没过期。
	old := RepositoryIssueDraft{Status: "pending", CreatedAt: time.Now().Add(-8 * 24 * time.Hour)}
	if !old.Expired(time.Now()) {
		t.Fatal("历史草稿超过有效期后应当算过期")
	}
	fresh := RepositoryIssueDraft{Status: "pending", CreatedAt: time.Now().Add(-time.Hour)}
	if fresh.Expired(time.Now()) {
		t.Fatal("刚建的历史草稿不该算过期")
	}
	// 终态草稿是记录，不参与过期。
	done := RepositoryIssueDraft{Status: "created", CreatedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if done.Expired(time.Now()) {
		t.Fatal("已创建的草稿不该算过期")
	}
}

func expireRepositoryIssueDraft(t *testing.T, plugin *RepositoryPublishPlugin, id string) {
	t.Helper()
	ctx := context.Background()
	draft, ok, err := plugin.findResolvedDraft(ctx, "private:owner", id)
	if err != nil || !ok {
		t.Fatalf("读取草稿失败：ok=%v err=%v", ok, err)
	}
	draft.ExpiresAt = time.Now().Add(-time.Minute)
	if err := plugin.updateDraft(ctx, draft); err != nil {
		t.Fatalf("写回草稿失败：%v", err)
	}
}

// 后台可以改草稿内容，但已经写进 GitHub 的不能改：改了也不会同步到线上。
func TestEditDraftFromWeb(t *testing.T) {
	plugin := newRepositoryPublishPlugin(http.DefaultClient, "https://example.invalid")
	ctx := context.Background()
	draft, err := plugin.saveDraft(ctx, repositoryIssueDraft{
		GroupID: "100", Repository: "acme/demo", RequesterID: "u1",
		Input: map[string]any{"title": "原标题", "body": "原正文", "labels": []string{"bug"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := plugin.EditDraftFromWeb(ctx, draft.ID, "新标题", "新正文", []string{"enhancement", " webui ", ""})
	if err != nil {
		t.Fatal(err)
	}
	if configToolString(edited.Input, "title") != "新标题" || configToolString(edited.Input, "body") != "新正文" {
		t.Fatalf("标题正文没改到：%#v", edited.Input)
	}
	labels, _, _, _ := repositoryIssueStringList(edited.Input, "labels", 20)
	if len(labels) != 2 || labels[0] != "enhancement" || labels[1] != "webui" {
		t.Fatalf("标签没有去空白和空项：%#v", labels)
	}
	// 正文清空是有效改动，不能当成「没填就沿用」。
	cleared, err := plugin.EditDraftFromWeb(ctx, draft.ID, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if configToolString(cleared.Input, "body") != "" {
		t.Fatalf("正文没有被清空：%#v", cleared.Input)
	}
	if _, ok := cleared.Input["labels"]; ok {
		t.Fatalf("标签清空后不该留下字段：%#v", cleared.Input)
	}
	if configToolString(cleared.Input, "title") != "新标题" {
		t.Fatalf("标题留空应当沿用原值：%#v", cleared.Input)
	}

	done := cleared
	done.Status = "created"
	if err := plugin.updateDraft(ctx, done); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.EditDraftFromWeb(ctx, draft.ID, "再改一次", "", nil); err == nil {
		t.Fatal("已经写进 GitHub 的草稿不该能改")
	}
}

// 已取消的草稿可以还原回待审批；已创建的不行——再提交一次就是重复建 Issue。
func TestRestoreDraftScope(t *testing.T) {
	plugin := newRepositoryPublishPlugin(http.DefaultClient, "https://example.invalid")
	ctx := context.Background()
	draft, err := plugin.saveDraft(ctx, repositoryIssueDraft{GroupID: "100", Repository: "acme/demo", RequesterID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	draft.Status = "cancelled"
	draft.ResolvedBy = "u2"
	if err := plugin.updateDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	restored, err := plugin.RestoreDraft(ctx, draft.ID)
	if err != nil {
		t.Fatalf("已取消的草稿应当可以还原：%v", err)
	}
	if restored.Status != "pending" || restored.ResolvedBy != "" || restored.Expired(time.Now()) {
		t.Fatalf("还原后应当回到待审批并重新计时：%#v", restored)
	}

	restored.Status = "created"
	if err := plugin.updateDraft(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.RestoreDraft(ctx, restored.ID); err == nil {
		t.Fatal("已创建的草稿不该能还原，否则等于重复建 Issue")
	}
	if _, err := plugin.RestoreDraft(ctx, "不存在"); err == nil {
		t.Fatal("不存在的草稿应当报错")
	}
}

// 删除只删记录，不碰 GitHub。
func TestDeleteDraft(t *testing.T) {
	plugin := newRepositoryPublishPlugin(http.DefaultClient, "https://example.invalid")
	ctx := context.Background()
	draft, err := plugin.saveDraft(ctx, repositoryIssueDraft{GroupID: "100", Repository: "acme/demo", RequesterID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.DeleteDraft(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := plugin.draftByID(ctx, draft.ID); err != nil || ok {
		t.Fatalf("草稿没被删掉：ok=%v err=%v", ok, err)
	}
	if err := plugin.DeleteDraft(ctx, draft.ID); err == nil {
		t.Fatal("删不存在的草稿应当报错")
	}
}
