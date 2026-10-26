package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFrontendDistDirFindsPackageFrontendNextToExecutable(t *testing.T) {
	// 在包目录以外直接执行解压出来的二进制，也要用上包里自带的前端。
	t.Chdir(t.TempDir())
	packageDir := t.TempDir()
	executable := filepath.Join(packageDir, "diana-webui")
	current := filepath.Join(packageDir, "frontend", "dist")
	legacy := filepath.Join(packageDir, "frontend-next", "dist")

	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := frontendDistDirFor("", executable); got != legacy {
		t.Fatalf("legacy package: got %s, want %s", got, legacy)
	}
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := frontendDistDirFor("", executable); got != current {
		t.Fatalf("current package: got %s, want %s", got, current)
	}
	custom := filepath.Join(t.TempDir(), "custom")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := frontendDistDirFor(custom, executable); got != custom {
		t.Fatalf("explicit setting: got %s, want %s", got, custom)
	}
}
