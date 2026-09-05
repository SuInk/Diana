// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSQLiteStoreListUserMemoriesPagingAndSearch(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for i := 0; i < 5; i++ {
		event := assistant.MessageEvent{
			Kind:       assistant.EventKindGroup,
			GroupID:    "20001",
			UserID:     fmt.Sprintf("1000%d", i),
			SenderName: fmt.Sprintf("成员%d", i),
			MessageID:  fmt.Sprintf("m%d", i),
			RawMessage: fmt.Sprintf("第 %d 条发言内容", i),
			Time:       1_700_000_000 + int64(i),
		}
		if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{}); err != nil {
			t.Fatal(err)
		}
	}

	profiles, total, err := store.ListUserMemories(ctx, "", "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(profiles) != 2 {
		t.Fatalf("total=%d page=%d, want 5/2", total, len(profiles))
	}

	page2, _, err := store.ListUserMemories(ctx, "", "", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].UserID == profiles[0].UserID {
		t.Fatalf("page2=%#v overlaps page1=%#v", page2, profiles)
	}

	matched, total, err := store.ListUserMemories(ctx, "", "成员3", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(matched) != 1 || matched[0].DisplayName != "成员3" {
		t.Fatalf("matched=%#v total=%d, want only 成员3", matched, total)
	}

	// LIKE 通配符按字面处理，不能变成全量匹配。
	wild, total, err := store.ListUserMemories(ctx, "", "%", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(wild) != 0 {
		t.Fatalf("wildcard query matched %d rows, want 0", total)
	}
}

// 人员画像按机器人各记一份：同一个人在两台机器人那儿是两段关系，好感度和记忆
// 都不该串。
func TestUserMemoryIsScopedPerBotProfile(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "user-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	onebot := assistant.MessageEvent{Kind: assistant.EventKindGroup, ProfileID: "bot-onebot", GroupID: "g1", UserID: "u1", SenderName: "青禾"}
	telegram := assistant.MessageEvent{Kind: assistant.EventKindPrivate, ProfileID: "bot-telegram", UserID: "u1", SenderName: "青禾"}
	// 单次增量上限是 ±3，两边取不同的合法值，串台时一眼看得出来。
	if _, err := store.UpdateUserMemory(ctx, onebot, assistant.UserMemoryUpdate{FavorabilityDelta: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateUserMemory(ctx, telegram, assistant.UserMemoryUpdate{FavorabilityDelta: 1}); err != nil {
		t.Fatal(err)
	}

	first, ok, err := store.GetUserMemory(ctx, "bot-onebot", "u1")
	if err != nil || !ok {
		t.Fatalf("onebot profile ok=%v err=%v", ok, err)
	}
	second, ok, err := store.GetUserMemory(ctx, "bot-telegram", "u1")
	if err != nil || !ok {
		t.Fatalf("telegram profile ok=%v err=%v", ok, err)
	}
	if first.Favorability == second.Favorability {
		t.Fatalf("两台机器人拿到了同一份好感度: %d / %d", first.Favorability, second.Favorability)
	}

	scoped, total, err := store.ListUserMemories(ctx, "bot-telegram", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(scoped) != 1 {
		t.Fatalf("scoped list total=%d len=%d", total, len(scoped))
	}
	all, totalAll, err := store.ListUserMemories(ctx, "", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if totalAll != 2 || len(all) != 2 {
		t.Fatalf("unscoped list total=%d len=%d", totalAll, len(all))
	}
}

// 老库升级：已有画像归给迁移时的当前配置档，其余机器人从空白开始——不复制，
// 否则等于凭空给每台都编造一段它没经历过的关系。
func TestUserMemoryMigrationAssignsExistingDataToCurrentBot(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "user-migrate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.SaveBotProfiles(ctx, assistant.ProfileSet{
		ActiveID: "bot-onebot",
		Profiles: []assistant.BotConfig{{ID: "bot-onebot", Name: "OneBot"}, {ID: "bot-telegram", Name: "Telegram"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE user_profiles`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
CREATE TABLE user_profiles (
  user_id TEXT PRIMARY KEY,
  display_name TEXT,
  favorability INTEGER NOT NULL,
  message_count INTEGER NOT NULL,
  memories TEXT NOT NULL,
  last_seen_at TEXT,
  updated_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO user_profiles (user_id, display_name, favorability, message_count, memories, last_seen_at, updated_at)
VALUES ('u1', '老用户', 42, 7, '[]', '', '2026-08-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateUserProfilesToBotScope(); err != nil {
		t.Fatal(err)
	}
	// 真实启动顺序里恋爱列的补列跑在作用域迁移之后，这里照做。
	if err := store.addUserProfileRomanceColumn(); err != nil {
		t.Fatal(err)
	}

	owned, ok, err := store.GetUserMemory(ctx, "bot-onebot", "u1")
	if err != nil || !ok || owned.Favorability != 42 {
		t.Fatalf("当前档没有继承历史画像: ok=%v profile=%#v err=%v", ok, owned, err)
	}
	if _, ok, err := store.GetUserMemory(ctx, "bot-telegram", "u1"); err != nil || ok {
		t.Fatalf("另一台机器人不该凭空拿到这段关系: ok=%v err=%v", ok, err)
	}
}

// 列表排序由 SQL 做：前端只能看到当前页，页内排序等于排了个假的。空的活跃时间
// 不管正序倒序都得沉底，否则「最早活跃」榜首全是从没说过话的人。
func TestSQLiteStoreListUserMemoriesSorting(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sort.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	seed := func(userID string, favorability int, messages int, at int64) {
		for i := 0; i < messages; i++ {
			event := assistant.MessageEvent{
				Kind:       assistant.EventKindGroup,
				GroupID:    "20001",
				UserID:     userID,
				SenderName: userID,
				MessageID:  fmt.Sprintf("%s-m%d", userID, i),
				RawMessage: fmt.Sprintf("%s 的第 %d 条发言", userID, i),
				Time:       at,
			}
			if _, err := store.UpdateUserMemory(ctx, event, assistant.UserMemoryUpdate{}); err != nil {
				t.Fatal(err)
			}
		}
		score := favorability
		admin := assistant.MessageEvent{Kind: assistant.EventKindGroup, GroupID: "20001", UserID: userID, SenderName: userID}
		if _, err := store.UpdateUserMemory(ctx, admin, assistant.UserMemoryUpdate{SetFavorability: &score, Administrative: true}); err != nil {
			t.Fatal(err)
		}
	}
	seed("u-chatty", 10, 3, 1_700_000_100)
	seed("u-liked", 50, 1, 1_700_000_050)
	seed("u-silent", 30, 0, 0)

	ids := func(sort, order string) []string {
		profiles, _, err := store.ListUserMemoriesSorted(ctx, "", "", sort, order, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(profiles))
		for _, profile := range profiles {
			out = append(out, profile.UserID)
		}
		return out
	}

	if got := ids("favorability", "desc"); !reflect.DeepEqual(got, []string{"u-liked", "u-silent", "u-chatty"}) {
		t.Fatalf("好感度倒序 = %v", got)
	}
	if got := ids("favorability", "asc"); !reflect.DeepEqual(got, []string{"u-chatty", "u-silent", "u-liked"}) {
		t.Fatalf("好感度正序 = %v", got)
	}
	if got := ids("messages", "desc"); !reflect.DeepEqual(got, []string{"u-chatty", "u-liked", "u-silent"}) {
		t.Fatalf("消息数倒序 = %v", got)
	}
	if got := ids("last_seen", "desc"); !reflect.DeepEqual(got, []string{"u-chatty", "u-liked", "u-silent"}) {
		t.Fatalf("最近活跃倒序 = %v", got)
	}
	if got := ids("last_seen", "asc"); !reflect.DeepEqual(got, []string{"u-liked", "u-chatty", "u-silent"}) {
		t.Fatalf("最早活跃正序 = %v", got)
	}
	// 不认识的排序键回落到「最近更新 · 倒序」，不能报错也不能把参数拼进 SQL。
	injected := ids("favorability; DROP TABLE user_profiles", "desc")
	if len(injected) != 3 {
		t.Fatalf("非法排序键没有回落到默认排序: %v", injected)
	}
	if sort, order := NormalizeUserMemorySort("FAVORABILITY", "ASC"); sort != "favorability" || order != "asc" {
		t.Fatalf("NormalizeUserMemorySort 大小写不敏感失败: %s/%s", sort, order)
	}
}
