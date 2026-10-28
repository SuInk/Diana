// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package runtimeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testFields() []Field {
	return []Field{
		{Key: "server.port", Label: "端口", Kind: KindPort, Default: "18080", Editable: true, Risky: true},
		{Key: "server.trusted_proxies", Label: "代理", Kind: KindList, Editable: true},
		{Key: "storage.log_retention_days", Label: "日志", Kind: KindInt, Default: "0", Editable: true, BotEditable: true, Min: Int64(-1), Max: Int64(36500)},
		{Key: "update.release_enabled", Label: "更新", Kind: KindBool, Default: "true", Editable: true},
		{Key: "admin.password", Label: "密码", Kind: KindString, Secret: true},
	}
}

const sample = `# 顶部注释
server:
  port: "18081" # 端口注释
storage:
  log_retention_days: 7
admin:
  password: hunter2
bot:
  owner_id: "10001"
`

func newTestManager(t *testing.T, data string) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if data != "" {
		if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(Options{Path: path, StartupData: []byte(data), Args: []string{"--config", path, "--token=abc"}, Fields: testFields(),
		Env: []EnvVar{{Name: "A_PLAIN"}, {Name: "A_SECRET", Secret: true}, {Name: "A_UNSET"}},
		LookupEnv: func(name string) (string, bool) {
			return map[string]string{"A_PLAIN": "x", "A_SECRET": "s"}[name], name != "A_UNSET"
		}})
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

func itemByKey(t *testing.T, snap Snapshot, key string) Item {
	t.Helper()
	for _, item := range snap.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("missing %s", key)
	return Item{}
}

func TestSnapshotSourcesAndSecrets(t *testing.T) {
	m, _ := newTestManager(t, sample)
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if port := itemByKey(t, snap, "server.port"); port.Value != "18081" || port.Source != SourceFile || port.Pending {
		t.Fatalf("port = %+v", port)
	}
	if rel := itemByKey(t, snap, "update.release_enabled"); rel.Value != "true" || rel.Source != SourceDefault {
		t.Fatalf("release = %+v", rel)
	}
	if pw := itemByKey(t, snap, "admin.password"); pw.Value != "已设置" || strings.Contains(pw.FileValue, "hunter2") {
		t.Fatalf("password leaked: %+v", pw)
	}
	if snap.Args[2] != "--token=***" {
		t.Fatalf("args = %v", snap.Args)
	}
	for _, env := range snap.Env {
		switch env.Name {
		case "A_PLAIN":
			if !env.Set || env.Value != "x" {
				t.Fatalf("plain = %+v", env)
			}
		case "A_SECRET":
			if !env.Set || env.Value != "" {
				t.Fatalf("secret = %+v", env)
			}
		case "A_UNSET":
			if env.Set {
				t.Fatalf("unset = %+v", env)
			}
		}
	}
}

func TestUpdateKeepsCommentsAndMarksPending(t *testing.T) {
	m, path := newTestManager(t, sample)
	snap, err := m.Update(map[string]string{
		"storage.log_retention_days": "30",
		"server.trusted_proxies":     "10.0.0.0/8, 127.0.0.1",
	}, ActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	logs := itemByKey(t, snap, "storage.log_retention_days")
	if logs.Value != "7" || logs.FileValue != "30" || !logs.Pending || !snap.RestartNeeded {
		t.Fatalf("logs = %+v", logs)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"# 顶部注释", "# 端口注释", "log_retention_days: 30", "- 10.0.0.0/8", "owner_id: \"10001\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	// 清空回到默认值。
	snap, err = m.Update(map[string]string{"storage.log_retention_days": ""}, ActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if logs := itemByKey(t, snap, "storage.log_retention_days"); logs.FileValue != "0" {
		t.Fatalf("after clear = %+v", logs)
	}
}

func TestUpdateRejectsWithoutWriting(t *testing.T) {
	m, path := newTestManager(t, sample)
	cases := []struct {
		values map[string]string
		actor  Actor
		perm   bool
	}{
		{map[string]string{"admin.password": "x"}, ActorAdmin, true},
		{map[string]string{"server.port": "18082"}, ActorBot, true},
		{map[string]string{"server.port": "70000"}, ActorAdmin, false},
		{map[string]string{"server.port": "abc"}, ActorAdmin, false},
		{map[string]string{"server.trusted_proxies": "1.1.1.1"}, ActorBot, true},
		{map[string]string{"storage.log_retention_days": "-2"}, ActorAdmin, false},
		{map[string]string{"storage.log_retention_days": "abc", "server.trusted_proxies": "1.1.1.1"}, ActorAdmin, false},
		{map[string]string{"nope": "1"}, ActorAdmin, false},
	}
	for _, tc := range cases {
		_, err := m.Update(tc.values, tc.actor)
		if err == nil {
			t.Fatalf("%v accepted", tc.values)
		}
		if errors.Is(err, ErrNotEditable) != tc.perm {
			t.Fatalf("%v err = %v", tc.values, err)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != sample {
		t.Fatalf("file changed:\n%s", data)
	}
}

func TestUpdatePortWritesQuotedString(t *testing.T) {
	m, path := newTestManager(t, sample)
	snap, err := m.Update(map[string]string{"server.port": " 18082 "}, ActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if port := itemByKey(t, snap, "server.port"); port.Value != "18081" || port.FileValue != "18082" || !port.Pending {
		t.Fatalf("port = %+v", port)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `port: "18082" # 端口注释`) {
		t.Fatalf("port not written as quoted string:\n%s", data)
	}
}

func TestUpdateCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "config.yaml")
	m, err := New(Options{CreatePath: path, Fields: testFields()})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := m.Update(map[string]string{"update.release_enabled": "false"}, ActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.ConfigExists || itemByKey(t, snap, "update.release_enabled").FileValue != "false" {
		t.Fatalf("snap = %+v", snap)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestBotEditableMustBeEditable(t *testing.T) {
	if _, err := New(Options{Fields: []Field{{Key: "a.b", BotEditable: true}}}); err == nil {
		t.Fatal("expected error")
	}
}
