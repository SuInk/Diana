// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"path/filepath"
	"testing"
)

// Docker 只挂数据目录：ytb_cookies.txt 放在数据库旁边也要能找到，工作目录里的优先。
func TestDefaultYTDLPCookiesPathFallsBackToDataDir(t *testing.T) {
	workDir := t.TempDir()
	dataDir := t.TempDir()
	t.Chdir(workDir)
	t.Setenv("APP_DB_PATH", filepath.Join(dataDir, "diana.db"))
	if got := defaultYTDLPCookiesPath(); got != "" {
		t.Fatalf("no cookies: path = %q", got)
	}
	// Empty files are ignored, as before.
	if err := os.WriteFile(filepath.Join(dataDir, "ytb_cookies.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultYTDLPCookiesPath(); got != "" {
		t.Fatalf("empty cookies: path = %q", got)
	}
	inData := filepath.Join(dataDir, "ytb_cookies.txt")
	if err := os.WriteFile(inData, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultYTDLPCookiesPath(); got != inData {
		t.Fatalf("data dir: path = %q, want %q", got, inData)
	}
	if err := os.WriteFile(filepath.Join(workDir, "ytb_cookies.txt"), []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs("ytb_cookies.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := defaultYTDLPCookiesPath(); got != want {
		t.Fatalf("working directory: path = %q, want %q", got, want)
	}
}
