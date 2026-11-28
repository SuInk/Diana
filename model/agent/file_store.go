// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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

	// Lstat 和 Stat 一样，但最后一段是软链接时取链接本身。
	Lstat(ctx context.Context, rel string) (fs.FileInfo, error)
	// Head 读普通文件开头至多 n 字节，认类型、读图片尺寸用。
	Head(ctx context.Context, rel string, n int) ([]byte, error)
	// WriteFile 整体写入一个文件，父目录自动创建。exclusive 时目标已存在就报
	// fs.ErrExist；否则覆盖，已有文件的权限位不变。
	WriteFile(ctx context.Context, rel string, data []byte, exclusive bool) error
	// MkdirAll 建目录，连同缺的上级。
	MkdirAll(ctx context.Context, rel string) error
	// Rename 挪动或改名；最后一段是软链接时挪的是链接本身。目标的上级目录要已存在。
	Rename(ctx context.Context, from, to string) error
	// CopyFile 复制一个普通文件，超过 maxBytes 报错且不留半截。overwrite 为 false 时
	// 目标已存在就报 fs.ErrExist。
	CopyFile(ctx context.Context, from, to string, overwrite bool, maxBytes int64) (int64, error)
	// Usage 是 rel 下普通文件的总字节数（不算软链接），rel 是文件时就是它的大小；
	// 不存在时为 0。长期区配额按它算。
	Usage(ctx context.Context, rel string) int64
	// ImportTree 把宿主机上 dir 里的目录和普通文件放进工作区的 rel 下。rel 必须不存在
	// 或是空目录，已有同名文件不覆盖；失败时把这次放进去的东西都撤掉。
	ImportTree(ctx context.Context, rel, dir string) error
	// BlockedWithin 报告 rel 本身或它下面有没有受保护的运行时文件：整个目录挪走或删掉
	// 会把里面的凭据一起带走。
	BlockedWithin(rel string) bool

	// existing 返回 rels 里还存在（含悬空软链接）的那些，一次问完：容器后端逐个问要逐个
	// docker exec。
	existing(ctx context.Context, rels []string) map[string]bool

	// lockKey 是 rel 在进程内锁表里的名字：同一个工作区的同一个路径得到同一个名字。
	lockKey(rel string) string
	// keepStateDir 是长期区索引所在的宿主目录（索引在它的 keep-index/ 下），空表示
	// 不记索引。
	keepStateDir() string
	// stageDir 是解压归档时放中间文件的宿主目录，空表示系统临时目录。
	stageDir() string
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

// HostFileStore 是 cfg.WorkDir 上的宿主工作目录，受保护文件名单按 cfg 算（MCP 配置
// 可能被机器人配置指到别处，名单要跟着它走）。
func HostFileStore(cfg Config) FileStore {
	root, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		root = cfg.WorkDir
	}
	// 新装还没有工作目录时先建出来：Resolve 要解开工作目录本身的软链接，目录不在就报错。
	_ = os.MkdirAll(root, 0o755)
	return newHostFileStore(root, agentProtectedFiles(cfg))
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

func (s *hostFileStore) BlockedWithin(rel string) bool {
	return s.protected.containsWithin(s.full(rel))
}

func (s *hostFileStore) lockKey(rel string) string { return s.full(rel) }

func (s *hostFileStore) existing(_ context.Context, rels []string) map[string]bool {
	found := map[string]bool{}
	_ = s.withRoot(func(root *os.Root) error {
		for _, rel := range rels {
			if _, err := root.Lstat(filepath.FromSlash(rel)); err == nil {
				found[rel] = true
			}
		}
		return nil
	})
	return found
}

func (s *hostFileStore) keepStateDir() string { return DianaStateDir(s.root) }

func (s *hostFileStore) stageDir() string { return s.root }

// 写入都经 os.Root：中途有软链接指到工作目录外面时，改名、建目录、写文件都会被拒，
// 不只靠 Resolve 时的那一次检查。
func (s *hostFileStore) withRoot(fn func(root *os.Root) error) error {
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return err
	}
	defer root.Close()
	return fn(root)
}

func (s *hostFileStore) Lstat(_ context.Context, rel string) (info fs.FileInfo, err error) {
	err = s.withRoot(func(root *os.Root) error {
		info, err = root.Lstat(filepath.FromSlash(rel))
		return err
	})
	return info, err
}

func (s *hostFileStore) Head(_ context.Context, rel string, n int) (head []byte, err error) {
	err = s.withRoot(func(root *os.Root) error {
		file, err := root.Open(filepath.FromSlash(rel))
		if err != nil {
			return err
		}
		defer file.Close()
		head, err = io.ReadAll(io.LimitReader(file, int64(n)))
		return err
	})
	return head, err
}

func (s *hostFileStore) WriteFile(_ context.Context, rel string, data []byte, exclusive bool) error {
	return s.withRoot(func(root *os.Root) error {
		local := filepath.FromSlash(rel)
		if parent := filepath.Dir(local); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		if exclusive {
			flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
		}
		file, err := root.OpenFile(local, flags, 0o644)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		return err
	})
}

func (s *hostFileStore) MkdirAll(_ context.Context, rel string) error {
	return s.withRoot(func(root *os.Root) error {
		return root.MkdirAll(filepath.FromSlash(rel), 0o755)
	})
}

func (s *hostFileStore) Rename(_ context.Context, from, to string) error {
	return s.withRoot(func(root *os.Root) error {
		return root.Rename(filepath.FromSlash(from), filepath.FromSlash(to))
	})
}

func (s *hostFileStore) CopyFile(_ context.Context, from, to string, overwrite bool, maxBytes int64) (written int64, err error) {
	err = s.withRoot(func(root *os.Root) error {
		written, err = copyWithinRoot(root, filepath.FromSlash(from), filepath.FromSlash(to), overwrite, maxBytes)
		return err
	})
	return written, err
}

func (s *hostFileStore) Usage(_ context.Context, rel string) int64 {
	target := s.full(rel)
	info, err := os.Lstat(target)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(target, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ImportTree 逐个建目录、以 O_EXCL 建文件，记下自己建了什么，中途失败按相反顺序删掉。
func (s *hostFileStore) ImportTree(ctx context.Context, rel, dir string) error {
	return s.withRoot(func(work *os.Root) error {
		if err := emptyDestination(ctx, s, rel); err != nil {
			return err
		}
		staged, err := os.OpenRoot(dir)
		if err != nil {
			return err
		}
		defer staged.Close()
		var created []string
		published := false
		defer func() {
			if !published {
				for i := len(created) - 1; i >= 0; i-- {
					_ = work.Remove(created[i])
				}
			}
		}()
		mkdir := func(target string) error {
			current := ""
			for _, part := range strings.Split(path.Clean(target), "/") {
				current = path.Join(current, part)
				if err := work.Mkdir(filepath.FromSlash(current), 0o755); err == nil {
					created = append(created, filepath.FromSlash(current))
				} else if !errors.Is(err, fs.ErrExist) {
					return err
				}
				stat, err := work.Lstat(filepath.FromSlash(current))
				if err != nil {
					return err
				}
				if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("destination component %q is not a directory", current)
				}
			}
			return nil
		}
		if err := mkdir(rel); err != nil {
			return err
		}
		err = fs.WalkDir(staged.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if name == "." {
				return nil
			}
			target := joinStoreRel(rel, name)
			if entry.IsDir() {
				return mkdir(target)
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("unsupported archive entry %q", name)
			}
			if s.Blocked(target) {
				return errProtectedFile(target)
			}
			in, err := staged.Open(filepath.FromSlash(name))
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := work.OpenFile(filepath.FromSlash(target), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			created = append(created, filepath.FromSlash(target))
			_, err = io.Copy(out, &archiveContextReader{ctx: ctx, reader: in})
			if closeErr := out.Close(); err == nil {
				err = closeErr
			}
			return err
		})
		if err != nil {
			return err
		}
		published = true
		return nil
	})
}

// errNotEmptyDestination 是解压目标不是新目录也不是空目录。
var errNotEmptyDestination = errors.New("destination must be a new or empty directory")

// emptyDestination 确认 rel 不存在，或是一个空目录（不是指向目录的软链接）。
func emptyDestination(ctx context.Context, store FileStore, rel string) error {
	info, err := store.Lstat(ctx, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errNotEmptyDestination
	}
	entries, err := store.ReadDir(ctx, rel)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return errNotEmptyDestination
	}
	return nil
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
