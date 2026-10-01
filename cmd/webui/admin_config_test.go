// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetAdminCredentialsYAML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "replace existing keys keeps comments and other sections",
			in:   "# 顶部注释\nserver:\n  port: \"18080\"\nadmin:\n  # 账号说明\n  username: \"old\" # 行尾注释\n  password: 'old-pass'\nupdate:\n  root: \".\"\n",
			want: "# 顶部注释\nserver:\n  port: \"18080\"\nadmin:\n  # 账号说明\n  username: \"new\" # 行尾注释\n  password: \"p: #'\\\"\\\\x\"\nupdate:\n  root: \".\"\n",
		},
		{
			name: "missing password key is inserted under admin",
			in:   "admin:\n    username: old\nbot: {}\n",
			want: "admin:\n    username: \"new\"\n    password: \"p: #'\\\"\\\\x\"\nbot: {}\n",
		},
		{
			name: "empty admin section",
			in:   "admin:\nupdate:\n  root: .\n",
			want: "admin:\n  username: \"new\"\n  password: \"p: #'\\\"\\\\x\"\nupdate:\n  root: .\n",
		},
		{
			name: "no admin section",
			in:   "server:\n  port: \"18080\"",
			want: "server:\n  port: \"18080\"\nadmin:\n  username: \"new\"\n  password: \"p: #'\\\"\\\\x\"\n",
		},
		{
			name: "empty file",
			in:   "",
			want: "admin:\n  username: \"new\"\n  password: \"p: #'\\\"\\\\x\"\n",
		},
		{
			name: "crlf line endings",
			in:   "admin:\r\n  username: old\r\n  password: old-pass\r\n",
			want: "admin:\r\n  username: \"new\"\r\n  password: \"p: #'\\\"\\\\x\"\r\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setAdminCredentialsYAML([]byte(tt.in), "new", `p: #'"\x`)
			if err != nil {
				t.Fatalf("setAdminCredentialsYAML() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestSetAdminCredentialsYAMLRejectsUnsupportedLayouts(t *testing.T) {
	for _, in := range []string{
		"admin: {username: a, password: b}\n",
		"admin:\n  username: a\n  password: |\n    multi\n",
		"- not a mapping\n",
	} {
		if _, err := setAdminCredentialsYAML([]byte(in), "new", "new-password"); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestWriteAdminCredentialsKeepsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("admin:\n  username: a\n  password: old-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminCredentials(path, "owner", "new-password"); err != nil {
		t.Fatalf("writeAdminCredentials() error = %v", err)
	}
	cfg, err := loadAppConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Username != "owner" || cfg.Admin.Password != "new-password" {
		t.Fatalf("config admin = %+v", cfg.Admin)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("permissions changed to %o", perm)
	}
}

func TestEnsureDataDirConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dbPath := filepath.Join(dir, "data", "diana.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// --config / DIANA_CONFIG 指向别处、数据库不在默认数据目录时都不生成。
	if path, err := ensureDataDirConfig(true, dbPath, "admin", "generated-pass"); err != nil || path != "" {
		t.Fatalf("explicit config path: path=%q err=%v", path, err)
	}
	if path, err := ensureDataDirConfig(false, filepath.Join(dir, "elsewhere", "diana.db"), "admin", "generated-pass"); err != nil || path != "" {
		t.Fatalf("custom db dir: path=%q err=%v", path, err)
	}

	path, err := ensureDataDirConfig(false, dbPath, "diana#abc", "generated-pass")
	if err != nil || path == "" {
		t.Fatalf("ensureDataDirConfig() path=%q err=%v", path, err)
	}
	if found := resolveConfigPath(nil); found != dataDirConfigPath {
		t.Fatalf("generated config is not found on next start: %q", found)
	}
	cfg, err := loadAppConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Username != "diana#abc" || cfg.Admin.Password != "generated-pass" {
		t.Fatalf("generated admin = %+v", cfg.Admin)
	}
	if info, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("generated config permissions = %o", info.Mode().Perm())
	}
	// 已经有了就不覆盖。
	if again, err := ensureDataDirConfig(false, dbPath, "other", "other-pass"); err != nil || again != "" {
		t.Fatalf("existing config overwritten: path=%q err=%v", again, err)
	}

	// 旧部署：只知道账号，密码留空，写回后就能改。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDataDirConfig(false, dbPath, "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := writeAdminCredentials(path, "admin", "reset-password"); err != nil {
		t.Fatalf("fill password in generated config: %v", err)
	}
	cfg, err = loadAppConfig(path)
	if err != nil || cfg.Admin.Password != "reset-password" {
		t.Fatalf("filled config admin = %+v err=%v", cfg.Admin, err)
	}
}
