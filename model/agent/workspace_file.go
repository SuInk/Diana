package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReadWorkspaceFile reads a bounded regular file without following links outside root.
func ReadWorkspaceFile(root, path string, limit int64) ([]byte, error) {
	if !filepath.IsLocal(path) || limit <= 0 {
		return nil, fmt.Errorf("a relative workspace path and positive limit are required")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	info, err := dir.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("file must be regular and at most %d bytes", limit)
	}
	file, err := dir.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}
