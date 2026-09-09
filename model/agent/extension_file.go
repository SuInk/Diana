package agent

import (
	"os"
	"path/filepath"
	"runtime"
)

func saveExtensionFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".extension-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		return err
	}
	backup, createErr := os.CreateTemp(filepath.Dir(path), ".extension-state-backup-*")
	if createErr != nil {
		return err
	}
	backup.Close()
	os.Remove(backup.Name())
	if moveErr := os.Rename(path, backup.Name()); moveErr != nil {
		return err
	}
	if moveErr := os.Rename(f.Name(), path); moveErr != nil {
		_ = os.Rename(backup.Name(), path)
		return moveErr
	}
	_ = os.Remove(backup.Name())
	return nil
}
