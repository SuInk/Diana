package assistant

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupLocalMediaFilePreservesSharedTempDirectory(t *testing.T) {
	targetFile, err := os.CreateTemp("", "diana-cleanup-target-*.png")
	if err != nil {
		t.Fatal(err)
	}
	target := targetFile.Name()
	if _, err := targetFile.Write([]byte("image")); err != nil {
		t.Fatal(err)
	}
	if err := targetFile.Close(); err != nil {
		t.Fatal(err)
	}
	sentinelFile, err := os.CreateTemp("", "diana-cleanup-sentinel-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := sentinelFile.Name()
	if _, err := sentinelFile.Write([]byte("keep")); err != nil {
		t.Fatal(err)
	}
	if err := sentinelFile.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sentinel) })

	<-cleanupLocalMediaFilesLater([]string{target}, 0)

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
		t.Fatalf("shared temp directory was damaged: %q, %v", got, err)
	}
}

func TestCleanupLocalMediaFileRemovesOnlyOwnedWorkDirectory(t *testing.T) {
	workDir, err := os.MkdirTemp("", "diana-agent-image-")
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
	realDir := t.TempDir()
	sentinel := filepath.Join(realDir, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir, err := os.MkdirTemp("", "diana-cleanup-link-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkDir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(linkDir), "diana-agent-image-"+filepath.Base(linkDir))
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })

	cleanupLocalMediaFile(filepath.Join(link, "missing.png"))

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
}
