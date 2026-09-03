// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryMediaStoreClassifiesEveryPlatformByDateAndType(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", root)
	at := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local).Unix()
	for _, platform := range SupportedPlatforms() {
		path, err := writeHistoryMedia(historyMediaWriteRequest{
			Platform: platform.ID, EventTime: at, TargetKind: string(EventKindGroup),
			GroupID: "group-1", MessageID: "message-1", Category: "file",
			Source: "source-1", FileName: "report.pdf", Body: []byte("payload"), ContentType: "application/pdf",
		})
		if err != nil {
			t.Fatalf("%s: %v", platform.ID, err)
		}
		want := filepath.Join(root, safeHistoryPart(platform.ID), "2026-09-03", "file", "group_group-1", "message-1")
		if filepath.Dir(path) != want || !strings.HasSuffix(path, "-report.pdf") {
			t.Fatalf("%s path = %q, want dir %q", platform.ID, path, want)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s file mode = %v err=%v", platform.ID, info, err)
		}
	}
}

func TestHistoryImageUsesSharedClassifiedStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", root)
	path, err := writeHistoryImage(PlatformOneBotV11, time.Date(2026, time.September, 3, 1, 0, 0, 0, time.Local).Unix(), "private", "", "user-1", "message-1", "inline", tinyJPEGBytes(t), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, PlatformOneBotV11, "2026-09-03", "image", "private_user-1", "message-1")
	if filepath.Dir(path) != want {
		t.Fatalf("image path = %q, want dir %q", path, want)
	}
}
