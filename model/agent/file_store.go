// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"
)

// FileStore 是文件工具读写的工作区。
//
// 文件工具以前直接对宿主工作目录调 os.*。工作区要并进工作区容器（每个会话范围一个
// 目录，见 WorkBox），同一套工具得既能读写宿主目录、也能读写容器里的范围目录，所以
// 工具只经由这个接口碰文件：宿主后端（hostFileStore）原样包着 safePath、os.* 和受保护
// 文件名单；容器后端（boxFileStore）以范围 uid 在容器里执行。
//
// 路径约定：工具先用 Resolve 把模型给的路径变成工作区内的相对路径（"." 是根），之后
// 所有方法都只收这种相对路径、用 / 分隔。
type FileStore interface {
	// Resolve 把模型给的路径变成工作区内的相对路径；跑出工作区的报错。
	Resolve(raw string) (string, error)
	// Stat 跟着软链接取信息；不存在时错误满足 errors.Is(err, fs.ErrNotExist)。
	Stat(ctx context.Context, rel string) (fs.FileInfo, error)
	// ReadDir 列出一层，不跟进子目录。
	ReadDir(ctx context.Context, rel string) ([]fs.FileInfo, error)
	// Open 打开一个普通文件读。
	Open(ctx context.Context, rel string) (io.ReadSeekCloser, error)
	// LocalTree 给整棵子树一个宿主机上的只读视图，grep、find_files 在上面遍历。宿主后端
	// 就是原目录；容器后端一次拷出来放进临时目录，release 时删掉。调用方不得写它。
	LocalTree(ctx context.Context, rel string) (dir string, release func(), err error)
	// Blocked 报告 rel 是不是受保护的运行时文件，文件工具不得读写或列出它。
	Blocked(rel string) bool
}

// toolFileStore 返回工具该用的工作区：设了 store 用它，否则是 root 上的宿主目录。
func toolFileStore(store FileStore, root string, protected protectedFiles) FileStore {
	if store != nil {
		return store
	}
	return newHostFileStore(root, protected)
}

// hostFileStore 是宿主工作目录。
type hostFileStore struct {
	root      string
	protected protectedFiles
}

func newHostFileStore(root string, protected protectedFiles) *hostFileStore {
	return &hostFileStore{root: root, protected: protected}
}

func (s *hostFileStore) Resolve(raw string) (string, error) {
	full, err := safePath(s.root, raw)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.root, full)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func (s *hostFileStore) full(rel string) string {
	return filepath.Join(s.root, filepath.FromSlash(rel))
}

func (s *hostFileStore) Stat(_ context.Context, rel string) (fs.FileInfo, error) {
	return os.Stat(s.full(rel))
}

func (s *hostFileStore) ReadDir(_ context.Context, rel string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(s.full(rel))
	if err != nil {
		return nil, err
	}
	infos := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			// 列的时候文件刚被删掉：只留名字和类型，大小留空。
			info = storeFileInfo{name: entry.Name(), mode: entry.Type()}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func (s *hostFileStore) Open(_ context.Context, rel string) (io.ReadSeekCloser, error) {
	return os.Open(s.full(rel))
}

func (s *hostFileStore) LocalTree(_ context.Context, rel string) (string, func(), error) {
	return s.full(rel), func() {}, nil
}

func (s *hostFileStore) Blocked(rel string) bool {
	return s.protected.blocked(s.full(rel))
}

// storeFileInfo 是容器后端按 stat/find 输出拼出来的 fs.FileInfo。
type storeFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func (i storeFileInfo) Name() string       { return i.name }
func (i storeFileInfo) Size() int64        { return i.size }
func (i storeFileInfo) Mode() fs.FileMode  { return i.mode }
func (i storeFileInfo) ModTime() time.Time { return i.modTime }
func (i storeFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i storeFileInfo) Sys() any           { return nil }

// joinStoreRel 把子树里的相对路径接回工作区相对路径。
func joinStoreRel(base, rel string) string {
	if base == "." || base == "" {
		return rel
	}
	return path.Join(base, rel)
}
