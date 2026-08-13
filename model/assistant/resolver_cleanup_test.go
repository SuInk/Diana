package assistant

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupLocalMediaFilePreservesSharedTempDirectory(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	target := filepath.Join(tempRoot, "diana-agent-image-test.png")
	sentinel := filepath.Join(tempRoot, "keep.txt")
	if err := os.WriteFile(target, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	<-cleanupLocalMediaFilesLater([]string{target}, 0)

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("shared temp directory was damaged: %q, %v", got, err)
	}
}

func TestCleanupLocalMediaFileRemovesOnlyOwnedWorkDirectory(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	workDir, err := os.MkdirTemp(tempRoot, "diana-agent-image-")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workDir, "image.png")
	if err := os.WriteFile(target, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupLocalMediaFile(target)

	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("owned work directory still exists: %v", err)
	}
}

func TestCleanupLocalMediaFilePreservesUnownedParent(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "voice.wav")
	sentinel := filepath.Join(parent, "settings.json")
	sidecar := target + ".jpg"
	if err := os.WriteFile(target, []byte("voice"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupLocalMediaFile(target)

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("unregistered sidecar was removed: %v", err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("unowned parent was removed: %v", err)
	}
}

func TestCleanupLocalMediaFileRejectsOwnedNameSymlink(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	realDir := t.TempDir()
	sentinel := filepath.Join(realDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tempRoot, "diana-agent-image-linked")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	cleanupLocalMediaFile(filepath.Join(link, "missing.png"))

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
}
