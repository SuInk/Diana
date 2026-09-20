// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestPurgeRefusedImageDescriptionsKeepsRealDescriptions(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	for _, record := range []assistant.ImageDescriptionRecord{
		{ContentSHA256: "aa", Description: "未收到图片内容，无法生成描述。请重新发送需要记录的图片。", Source: "vision", Version: "recall-image-v1"},
		{ContentSHA256: "bb", Description: "一张手机群聊界面截图，顶部状态栏显示电量 64%。", Source: "vision", Version: "recall-image-v1"},
	} {
		if err := store.SaveImageDescription(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveImageRecognition(ctx, assistant.ImageRecognitionRecord{
		CacheKey: "describe:cc", ContentSHA256: "cc", Kind: "describe", Backend: "llm",
		Text: "没有收到图片，请重新发送。", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	purged, err := store.PurgeRefusedImageDescriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 2 {
		t.Fatalf("purged = %d, want 2", purged)
	}
	if _, ok, err := store.GetImageDescription(ctx, "aa"); err != nil || ok {
		t.Fatalf("refused description survived: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.LoadImageRecognition(ctx, "describe:cc"); err != nil || ok {
		t.Fatalf("refused recognition survived: ok=%v err=%v", ok, err)
	}
	record, ok, err := store.GetImageDescription(ctx, "bb")
	if err != nil || !ok {
		t.Fatalf("real description was deleted: ok=%v err=%v", ok, err)
	}
	if record.Description == "" {
		t.Fatalf("record = %#v", record)
	}
}

// 清理只跑一次：标记写进 app_state 之后，后来正常写入的描述不会被再扫一遍。
func TestPurgeRefusedImageDescriptionsRunsOnce(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	if _, err := store.PurgeRefusedImageDescriptions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveImageDescription(ctx, assistant.ImageDescriptionRecord{
		ContentSHA256: "dd", Description: "未收到图片，无法生成描述。", Source: "vision", Version: "recall-image-v1",
	}); err != nil {
		t.Fatal(err)
	}
	purged, err := store.PurgeRefusedImageDescriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Fatalf("purged = %d, want 0 on a second run", purged)
	}
	if _, ok, err := store.GetImageDescription(ctx, "dd"); err != nil || !ok {
		t.Fatalf("second run should not rescan: ok=%v err=%v", ok, err)
	}
}
