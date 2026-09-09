package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadWorkspaceFileBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadWorkspaceFile(root, "ok.txt", 5)
	if err != nil || string(data) != "hello" {
		t.Fatalf("data=%q error=%v", data, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", filepath.Join(outside, "secret"), "escape/secret", "."} {
		if _, err := ReadWorkspaceFile(root, path, 32); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if _, err := ReadWorkspaceFile(root, "ok.txt", 4); err == nil {
		t.Fatal("size limit ignored")
	}
}
