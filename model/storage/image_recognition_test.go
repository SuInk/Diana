// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestSQLiteStorePersistsImageRecognitionByCacheKey(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	ocr := assistant.ImageRecognitionRecord{
		CacheKey: "key-ocr", ContentSHA256: "ABCDEF", Kind: "ocr",
		Backend: "local", Model: "", Text: "图上的字", CreatedAt: 1000,
	}
	describe := assistant.ImageRecognitionRecord{
		CacheKey: "key-describe", ContentSHA256: "ABCDEF", Kind: "describe",
		Backend: "llm", Model: "gpt-4o-mini", Text: "一只在叫的狗", CreatedAt: 1001,
	}
	for _, record := range []assistant.ImageRecognitionRecord{ocr, describe} {
		if err := store.SaveImageRecognition(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	// 同一张图的转写和描述各存一份，互不覆盖。
	got, ok, err := store.LoadImageRecognition(ctx, "key-ocr")
	if err != nil || !ok || got.Text != "图上的字" || got.Kind != "ocr" || got.ContentSHA256 != "abcdef" {
		t.Fatalf("ocr record = %#v ok=%v err=%v", got, ok, err)
	}
	got, ok, err = store.LoadImageRecognition(ctx, "key-describe")
	if err != nil || !ok || got.Text != "一只在叫的狗" || got.Model != "gpt-4o-mini" {
		t.Fatalf("describe record = %#v ok=%v err=%v", got, ok, err)
	}

	if _, ok, err = store.LoadImageRecognition(ctx, "key-missing"); ok || err != nil {
		t.Fatalf("missing key ok=%v err=%v", ok, err)
	}

	// 重新识别同一套配置时覆盖旧结果，不产生第二行。
	ocr.Text = "重新识别的字"
	ocr.CreatedAt = 2000
	if err := store.SaveImageRecognition(ctx, ocr); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.LoadImageRecognition(ctx, "key-ocr")
	if got.Text != "重新识别的字" || got.CreatedAt != 2000 {
		t.Fatalf("updated record = %#v", got)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM image_recognitions`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}
}

func TestSQLiteStoreIgnoresEmptyImageRecognition(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveImageRecognition(context.Background(), assistant.ImageRecognitionRecord{
		CacheKey: "key", ContentSHA256: "abc", Kind: "ocr", Backend: "llm", Text: "   ",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.LoadImageRecognition(context.Background(), "key"); ok {
		t.Fatal("empty text should not be cached")
	}
}
