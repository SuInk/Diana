// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
)

func identityLinkTestAccount(profile, platform, user string) assistant.IdentityAccount {
	return assistant.IdentityAccount{ProfileID: profile, Platform: platform, UserID: user}
}

func TestIdentityLinksLinkListLookupAndUnlink(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity-links.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	qq := identityLinkTestAccount("qq", assistant.PlatformOneBotV11, "10001")
	tg := identityLinkTestAccount("tg", assistant.PlatformTelegram, "30003")
	tg.Username = "@alice_tg"
	group, err := store.LinkIdentityAccounts(ctx, qq, tg)
	if err != nil || len(group) != 2 || group[0].PersonID == "" || group[0].PersonID != group[1].PersonID {
		t.Fatalf("link group=%#v err=%v", group, err)
	}
	person := group[0].PersonID

	// 第三个账号并进已有的那个人；关联持久化，重开库仍在。
	wx := identityLinkTestAccount("wx", assistant.PlatformWeixin, "wxid-55501")
	if _, err := store.LinkIdentityAccounts(ctx, wx, tg); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	all, err := store.ListIdentityLinks(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("list=%#v err=%v", all, err)
	}
	for _, item := range all {
		if item.PersonID != person {
			t.Fatalf("account joined a different person: %#v", item)
		}
		if item.UserID == "30003" && item.Username != "alice_tg" {
			t.Fatalf("username not stored: %#v", item)
		}
	}
	linked, err := store.LinkedIdentityAccounts(ctx, qq)
	if err != nil || len(linked) != 3 {
		t.Fatalf("linked=%#v err=%v", linked, err)
	}
	if none, err := store.LinkedIdentityAccounts(ctx, identityLinkTestAccount("qq", assistant.PlatformOneBotV11, "19999")); err != nil || len(none) != 0 {
		t.Fatalf("unlinked account lookup=%#v err=%v", none, err)
	}

	// 两个账号已分属不同的人时不自动合并。
	other := identityLinkTestAccount("qq", assistant.PlatformOneBotV11, "10002")
	otherTG := identityLinkTestAccount("tg", assistant.PlatformTelegram, "30004")
	if _, err := store.LinkIdentityAccounts(ctx, other, otherTG); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkIdentityAccounts(ctx, qq, other); !errors.Is(err, assistant.ErrIdentityLinkConflict) {
		t.Fatalf("conflicting link err=%v, want ErrIdentityLinkConflict", err)
	}

	// 解除一个账号后同组其余账号仍在；剩一个时整组删掉。
	if removed, err := store.UnlinkIdentityAccount(ctx, wx); err != nil || !removed {
		t.Fatalf("unlink removed=%v err=%v", removed, err)
	}
	if linked, _ := store.LinkedIdentityAccounts(ctx, qq); len(linked) != 2 {
		t.Fatalf("after unlink linked=%#v", linked)
	}
	if removed, err := store.UnlinkIdentityAccount(ctx, tg); err != nil || !removed {
		t.Fatalf("unlink removed=%v err=%v", removed, err)
	}
	if linked, _ := store.LinkedIdentityAccounts(ctx, qq); len(linked) != 0 {
		t.Fatalf("lone account should be dropped, got %#v", linked)
	}
	if removed, err := store.UnlinkIdentityAccount(ctx, qq); err != nil || removed {
		t.Fatalf("unlinking an unlinked account removed=%v err=%v", removed, err)
	}
}

func TestIdentityLinksGroupLimit(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "identity-links-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	anchor := identityLinkTestAccount("tg", assistant.PlatformTelegram, "30001")
	for i := 1; i < assistant.IdentityLinkGroupLimit; i++ {
		account := identityLinkTestAccount("qq", assistant.PlatformOneBotV11, "1"+string(rune('0'+i/10))+string(rune('0'+i%10))+"01")
		if _, err := store.LinkIdentityAccounts(ctx, anchor, account); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
	}
	extra := identityLinkTestAccount("wx", assistant.PlatformWeixin, "wxid-extra")
	if _, err := store.LinkIdentityAccounts(ctx, anchor, extra); !errors.Is(err, assistant.ErrIdentityLinkGroupFull) {
		t.Fatalf("over-limit link err=%v, want ErrIdentityLinkGroupFull", err)
	}
}

func TestStructuredMemoryLinkedSubjectsReadOnlyPersonalMemories(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "identity-links-memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	write := func(session, subject string, kind assistant.MemoryKind, visibility assistant.MemoryVisibility, key, content string) {
		t.Helper()
		_, err := store.ApplyMemoryCandidates(ctx, assistant.MemoryWriteRequest{
			SubjectUserID: subject, SubjectName: "Alice", Session: session, EventKind: assistant.EventKindGroup, GroupID: "77001",
			SourceMessageID: "m-" + key, SourceEventTime: time.Now().Add(-time.Hour),
			Candidates: []assistant.MemoryCandidate{{
				Key: key, Kind: kind, Topic: "偏好", Content: content, Confidence: 0.95, Importance: 0.9,
				Visibility: visibility, SourceType: assistant.MemorySourceExplicit,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 私聊里写的记忆一律是会话可见，visibility=user 只来自群聊。
	write("qq:group:77001", "10001", assistant.MemoryKindPreference, assistant.MemoryVisibilityUser, "drink", "喜欢喝乌龙茶")
	write("qq:group:77001", "10001", assistant.MemoryKindPreference, assistant.MemoryVisibilitySession, "session-only", "这条只属于 QQ 群会话")
	write("qq:group:77001", "10009", assistant.MemoryKindPreference, assistant.MemoryVisibilityUser, "stranger", "别人的偏好")

	query := assistant.StructuredMemoryQuery{SubjectUserID: "30003", Session: "tg:private:30003", MaxCandidates: 20}
	items, err := store.ListStructuredMemories(ctx, query)
	if err != nil || len(items) != 0 {
		t.Fatalf("without links items=%#v err=%v", items, err)
	}
	query.LinkedSubjects = []assistant.StructuredMemorySubject{{Namespace: "qq:", UserID: "10001"}}
	items, err = store.ListStructuredMemories(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "喜欢喝乌龙茶" {
		t.Fatalf("linked read should return only the linked account's user-visible memory, got %#v", items)
	}
}
