// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryMediaStoreDeduplicatesAcrossPlatformsAndMessages(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", root)
	at := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local).Unix()
	var first string
	for i, platform := range SupportedPlatforms() {
		path, err := writeHistoryMedia(historyMediaWriteRequest{
			Platform: platform.ID, EventTime: at + int64(i)*86400, TargetKind: string(EventKindGroup),
			GroupID: platform.ID, MessageID: platform.ID, Category: "file",
			Source: "source-1", FileName: "report.pdf", Body: []byte("payload"), ContentType: "application/pdf",
		})
		if err != nil {
			t.Fatalf("%s: %v", platform.ID, err)
		}
		if first == "" {
			first = path
		}
		if path != first || filepath.Base(filepath.Dir(path)) != imageBytesSHA256([]byte("payload")) || filepath.Ext(path) != ".pdf" {
			t.Fatalf("%s path = %q, first = %q", platform.ID, path, first)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s file mode = %v err=%v", platform.ID, info, err)
		}
	}
}

func TestHistoryImageUsesSharedContentStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", root)
	path, err := writeHistoryImage(PlatformOneBotV11, time.Date(2026, time.September, 3, 1, 0, 0, 0, time.Local).Unix(), "private", "", "user-1", "message-1", "inline", tinyJPEGBytes(t), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	digest := imageBytesSHA256(tinyJPEGBytes(t))
	want := filepath.Join(root, "objects", digest[:2], digest)
	if filepath.Dir(path) != want {
		t.Fatalf("image path = %q, want dir %q", path, want)
	}
}
