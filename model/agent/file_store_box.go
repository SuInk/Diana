// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// boxFileStore 是工作区容器里的一个会话范围目录。所有操作都以范围 uid 执行，本来就读
// 不到别的范围；脚本里再用 realpath 确认目标仍在范围目录内，挡住指向范围外的软链接。
type boxFileStore struct {
	box *WorkBox
}

// NewBoxFileStore 返回 box 这个会话范围的文件工作区。
func NewBoxFileStore(box *WorkBox) FileStore {
	return &boxFileStore{box: box}
}

// BoxTreeMaxBytes 是 LocalTree 一次最多拷出多少字节。grep、find_files 要遍历整棵子树，
// 超过这个量请模型缩小 path。
const BoxTreeMaxBytes = 256 << 20

func (s *boxFileStore) Resolve(raw string) (string, error) {
	abs, err := s.box.ResolvePath(raw)
	if err != nil {
		return "", err
	}
	root := s.box.Root()
	if abs == root {
		return ".", nil
	}
	return strings.TrimPrefix(abs, root+"/"), nil
}

func (s *boxFileStore) abs(rel string) string {
	if rel == "." || rel == "" {
		return s.box.Root()
	}
	return s.box.Root() + "/" + rel
}

// boxResolveScript 是各脚本开头的公共一段：$1 是目标、$2 是范围目录。取真实路径（跟着
// 软链接），不存在退出 3，落到范围外退出 4；结果在 $p。
const boxResolveScript = `p=$(realpath -e -- "$1" 2>/dev/null) || exit 3
case "$p" in "$2"|"$2"/*) ;; *) exit 4;; esac
`

// boxStatScript 输出「类型|字节|修改时间（秒，带小数）|名字」。
const boxStatScript = boxResolveScript + `stat -c '%F|%s|%.9Y' -- "$p" | tr -d '\n'
printf '|%s' "$(basename -- "$1")"`

// boxReadDirScript 每项输出「类型字母\t字节\t修改时间\t名字」，以 NUL 结尾（名字里可能有换行）。
const boxReadDirScript = boxResolveScript + `[ -d "$p" ] || exit 5
find "$p" -mindepth 1 -maxdepth 1 -printf '%y\t%s\t%T@\t%f\0'`

// boxTreeScript 把子树打成 tar 流。跳过 .git：体积大、是二进制打包对象，遍历它没有意义。
const boxTreeScript = boxResolveScript + `[ -d "$p" ] || exit 5
cd -- "$p" && tar --exclude=.git -cf - .`

func (s *boxFileStore) run(ctx context.Context, script string, stdout io.Writer, args ...string) dockerResult {
	b := s.box
	callArgs := append(b.execArgs(false), "/bin/sh", "-c", script, "sh")
	callArgs = append(callArgs, args...)
	return b.host.docker(ctx, dockerCall{args: callArgs, stdout: stdout, timeout: boxControlTimeout})
}

// boxStoreError 把脚本退出码变成和 os.* 一样的错误，工具对两种后端给同样的提示。
func boxStoreError(op, rel string, res dockerResult) error {
	switch res.code {
	case 3:
		return &fs.PathError{Op: op, Path: rel, Err: fs.ErrNotExist}
	case 4:
		return &fs.PathError{Op: op, Path: rel, Err: fs.ErrPermission}
	case 5:
		return &fs.PathError{Op: op, Path: rel, Err: errors.New("不是目录")}
	}
	return boxErrorf("工作区%s失败：%s", op, strings.TrimSpace(res.stderr))
}

func (s *boxFileStore) Stat(ctx context.Context, rel string) (fs.FileInfo, error) {
	defer s.box.use()()
	if err := s.box.Ensure(ctx); err != nil {
		return nil, err
	}
	res := s.run(ctx, boxStatScript, nil, s.abs(rel), s.box.Root())
	if res.code != 0 {
		return nil, boxStoreError("stat", rel, res)
	}
	return parseBoxStat(string(res.stdout))
}

func parseBoxStat(out string) (fs.FileInfo, error) {
	fields := strings.SplitN(out, "|", 4)
	if len(fields) != 4 {
		return nil, boxErrorf("工作区 stat 输出格式不对：%q", out)
	}
	size, _ := strconv.ParseInt(fields[1], 10, 64)
	var mode fs.FileMode
	switch fields[0] {
	case "regular file", "regular empty file":
	case "directory":
		mode = fs.ModeDir
	case "symbolic link":
		mode = fs.ModeSymlink
	default:
		mode = fs.ModeIrregular
	}
	return storeFileInfo{name: fields[3], size: size, mode: mode, modTime: parseBoxTime(fields[2])}, nil
}

func parseBoxTime(value string) time.Time {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return time.Time{}
	}
	whole := math.Floor(seconds)
	return time.Unix(int64(whole), int64((seconds-whole)*1e9))
}

func (s *boxFileStore) ReadDir(ctx context.Context, rel string) ([]fs.FileInfo, error) {
	defer s.box.use()()
	if err := s.box.Ensure(ctx); err != nil {
		return nil, err
	}
	res := s.run(ctx, boxReadDirScript, nil, s.abs(rel), s.box.Root())
	if res.code != 0 {
		return nil, boxStoreError("readdir", rel, res)
	}
	var infos []fs.FileInfo
	for _, record := range strings.Split(string(res.stdout), "\x00") {
		fields := strings.SplitN(record, "\t", 4)
		if len(fields) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		var mode fs.FileMode
		switch fields[0] {
		case "f":
		case "d":
			mode = fs.ModeDir
		case "l":
			mode = fs.ModeSymlink
		default:
			mode = fs.ModeIrregular
		}
		infos = append(infos, storeFileInfo{name: fields[3], size: size, mode: mode, modTime: parseBoxTime(fields[2])})
	}
	return infos, nil
}

// boxReadSeeker 是读进内存的文件。容器里的文件只能整份流出来，read_file 又要回头重读，
// 所以按 BoxCopyOutMaxBytes 读进来再按需 Seek。
type boxReadSeeker struct{ *bytes.Reader }

func (boxReadSeeker) Close() error { return nil }

func (s *boxFileStore) Open(ctx context.Context, rel string) (io.ReadSeekCloser, error) {
	_, data, err := s.box.CopyOut(ctx, s.abs(rel), BoxCopyOutMaxBytes)
	if err != nil {
		return nil, err
	}
	return boxReadSeeker{bytes.NewReader(data)}, nil
}

func (s *boxFileStore) LocalTree(ctx context.Context, rel string) (string, func(), error) {
	defer s.box.use()()
	if err := s.box.Ensure(ctx); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "diana-box-tree-")
	if err != nil {
		return "", nil, err
	}
	release := func() { _ = os.RemoveAll(dir) }
	var archive bytes.Buffer
	limited := &limitedWriter{w: &archive, n: BoxTreeMaxBytes}
	res := s.run(ctx, boxTreeScript, limited, s.abs(rel), s.box.Root())
	if limited.overflow {
		release()
		return "", nil, boxErrorf("%s 下的文件超过 %d MB，缩小 path 再查", rel, BoxTreeMaxBytes>>20)
	}
	if res.code != 0 {
		release()
		return "", nil, boxStoreError("遍历", rel, res)
	}
	if err := extractBoxTree(&archive, dir); err != nil {
		release()
		return "", nil, err
	}
	return dir, release, nil
}

// extractBoxTree 把容器里打出来的 tar 解到 dir。只认目录和普通文件：软链接、设备文件
// 一律丢掉，名字必须是 dir 下的本地路径，解出来的东西不会指向 dir 外面。
func extractBoxTree(archive io.Reader, dir string) error {
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return boxErrorf("工作区遍历结果解不开：%v", err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." {
			continue
		}
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := writeBoxTreeFile(target, reader, header); err != nil {
				return err
			}
		}
	}
}

func writeBoxTreeFile(target string, reader io.Reader, header *tar.Header) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("工作区遍历结果写不进临时目录：%w", err)
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	// 保留修改时间：find_files 之后按时间排序、过期清理都会看它。
	return os.Chtimes(target, header.ModTime, header.ModTime)
}

func (s *boxFileStore) Blocked(string) bool {
	// 容器里只有用户放进来的东西，没有运行时机密。
	return false
}
