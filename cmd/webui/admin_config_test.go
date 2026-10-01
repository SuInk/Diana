// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
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
	if perm := info.Mode().Perm(); perm != 0o600 && !strings.EqualFold(os.Getenv("GOOS"), "windows") {
		t.Fatalf("permissions changed to %o", perm)
	}
}
