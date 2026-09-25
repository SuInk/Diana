package agent

import (
	"fmt"
	"io"
	"os"
)

// ReadWorkspaceFile reads a bounded regular file without following links outside root.
// 路径先过 NormalizeWorkspacePath，和 read_file 接受同样的写法；真正打开仍走 os.OpenRoot，
// 指向工作目录外面的软链接照旧打不开。
func ReadWorkspaceFile(root, path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("read limit must be positive")
	}
	raw := path
	path, err := NormalizeWorkspacePath(root, raw)
	if err != nil {
		return nil, err
	}
	if path == "." {
		return nil, fmt.Errorf("%w（没有给出文件名）", ErrWorkspacePath)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	info, err := dir.Stat(path)
	if err != nil {
		return nil, workspaceNotFound(raw, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s 是目录，不是文件；用 list_files 看里面有什么", raw)
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
