// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"
)

func TestReplyToneLibrarySaveUpdateDelete(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	set, saved, err := (ReplyToneSet{}).Save(ReplyTonePreset{Name: "群聊短句", Text: " 简短一点\r\n少用句号 "}, now)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Text != "简短一点\n少用句号" || len(set.Presets) != 1 {
		t.Fatalf("saved=%#v set=%#v", saved, set)
	}
	set, updated, err := set.Save(ReplyTonePreset{ID: saved.ID, Name: "熟人语气", Text: "自然接话"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Presets) != 1 || updated.Name != "熟人语气" || set.Presets[0].Text != "自然接话" {
		t.Fatalf("updated=%#v set=%#v", updated, set)
	}
	if got := set.Delete(saved.ID); len(got.Presets) != 0 {
		t.Fatalf("delete=%#v", got)
	}
}

func TestReplyToneLibraryKeepsBuiltinReadOnly(t *testing.T) {
	builtins := BuiltinReplyTones()
	if len(builtins) != 1 || !builtins[0].Builtin || builtins[0].Text == "" {
		t.Fatalf("builtins=%#v", builtins)
	}
	if _, _, err := (ReplyToneSet{}).Save(ReplyTonePreset{ID: builtins[0].ID, Name: "改内置", Text: "不该成功"}, time.Now()); err == nil {
		t.Fatal("saving a builtin reply tone should fail")
	}
}
