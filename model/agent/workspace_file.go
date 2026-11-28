package agent

import (
	"context"
	"fmt"
	"io"
)

// ReadStoreFile 从工作区读一个有大小上限的普通文件，返回它在工作区内的相对路径和内容。
// 路径和 read_file 接受同样的写法（宿主后端先过 NormalizeWorkspacePath，容器后端也认
// /work/… 的绝对路径）；受保护的运行时配置读不到，指向工作区外的软链接也读不到。
func ReadStoreFile(ctx context.Context, store FileStore, raw string, limit int64) (string, []byte, error) {
	if limit <= 0 {
		return "", nil, fmt.Errorf("read limit must be positive")
	}
	rel, err := store.Resolve(raw)
	if err != nil {
		return "", nil, err
	}
	if rel == "." {
		return "", nil, fmt.Errorf("%w（没有给出文件名）", ErrWorkspacePath)
	}
	if store.Blocked(rel) {
		return "", nil, errProtectedFile(raw)
	}
	info, err := store.Stat(ctx, rel)
	if err != nil {
		return "", nil, storeMissingFileError(ctx, store, rel, err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%s 是目录，不是文件；用 list_files 看里面有什么", raw)
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return "", nil, fmt.Errorf("file must be regular and at most %d bytes", limit)
	}
	file, err := store.Open(ctx, rel)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(data)) > limit {
		return "", nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return rel, data, nil
}
