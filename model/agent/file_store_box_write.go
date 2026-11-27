// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// 容器后端的写入。脚本都以范围 uid 运行，碰不到别的范围；每个目标再用 realpath 确认
// 落在范围目录（脚本里的 $R）内，挡住指向范围外（比如容器的 /tmp）的软链接。
//
// 退出码约定接着 boxResolveScript：3 不存在、4 落到范围外、5 类型不对、6 超过大小上限、
// 7 目标已存在、8 解压目标不是空目录、75 范围目录超限（stdout 是已用 KiB）。

// boxParentScript 取 $1 上一级的真实路径，确认在范围目录 $R 内，结果在 $a（最后一段
// 不跟软链接）。
const boxParentScript = `d=$(realpath -e -- "$(dirname -- "$1")" 2>/dev/null) || exit 3
case "$d" in "$R"|"$R"/*) ;; *) exit 4;; esac
a="$d/$(basename -- "$1")"
`

// boxQuotaScript 在写入 $N 字节之前看范围目录还剩多少；够的话用 ulimit -f 把这次写入
// 限在剩余空间内。$L 是上限 KiB。
const boxQuotaScript = `used=$(du -sxk "$R" 2>/dev/null | cut -f1)
if [ $(( ${used:-0} + $N / 1024 )) -ge "$L" ]; then echo "${used:-0}"; exit 75; fi
ulimit -f $(( ($L - ${used:-0}) * 2 ))
`

// boxLstatScript：$1 目标。
const boxLstatScript = `R=$2
` + boxParentScript + `[ -e "$a" ] || [ -L "$a" ] || exit 3
stat -c '%F|%s|%.9Y' -- "$a" | tr -d '\n'
printf '|%s' "$(basename -- "$1")"`

// boxHeadScript：$1 目标、$2 范围目录、$3 字节数。
const boxHeadScript = boxResolveScript + `[ -f "$p" ] || exit 5
head -c "$3" -- "$p"`

// boxStoreWriteScript：$1 目标、$2 范围目录、$3 上限 KiB、$4 字节数、$5 为 1 时不覆盖。
// 内容从 stdin 来。
const boxStoreWriteScript = `R=$2 L=$3 N=$4
p=$(realpath -m -- "$1") || exit 4
case "$p" in "$R"/*) ;; *) exit 4;; esac
[ -d "$p" ] && exit 5
if [ "$5" = 1 ] && [ -e "$p" ]; then exit 7; fi
mkdir -p -- "$(dirname -- "$p")" || exit 1
q=$(realpath -e -- "$(dirname -- "$p")") || exit 4
case "$q" in "$R"|"$R"/*) ;; *) exit 4;; esac
` + boxQuotaScript + `[ "$5" = 1 ] && set -C
cat > "$p"`

// boxMkdirScript：$1 目标、$2 范围目录。
const boxMkdirScript = `R=$2
p=$(realpath -m -- "$1") || exit 4
case "$p" in "$R"|"$R"/*) ;; *) exit 4;; esac
[ -e "$p" ] && [ ! -d "$p" ] && exit 5
mkdir -p -- "$p"`

// boxRenameScript：$1 来源、$2 目标、$3 范围目录。两边最后一段都不跟软链接。
const boxRenameScript = `R=$3
` + boxParentScript + `[ -e "$a" ] || [ -L "$a" ] || exit 3
from=$a
set -- "$2"
` + boxParentScript + `mv -T -f -- "$from" "$a"`

// boxCopyScript：$1 来源、$2 目标、$3 范围目录、$4 单文件上限字节、$5 为 1 时覆盖、
// $6 范围上限 KiB。成功时 stdout 是复制的字节数。
const boxCopyScript = `R=$3 L=$6
src=$(realpath -e -- "$1" 2>/dev/null) || exit 3
case "$src" in "$R"/*) ;; *) exit 4;; esac
[ -f "$src" ] || exit 5
N=$(stat -c %s -- "$src") || exit 5
[ "$N" -le "$4" ] || exit 6
dst=$(realpath -m -- "$2") || exit 4
case "$dst" in "$R"/*) ;; *) exit 4;; esac
[ -d "$dst" ] && exit 5
if [ "$5" != 1 ] && { [ -e "$dst" ] || [ -L "$2" ]; }; then exit 7; fi
q=$(realpath -e -- "$(dirname -- "$dst")" 2>/dev/null) || exit 3
case "$q" in "$R"|"$R"/*) ;; *) exit 4;; esac
` + boxQuotaScript + `[ "$5" = 1 ] || set -C
cat -- "$src" > "$dst" || { rm -f -- "$dst"; exit 1; }
printf '%s' "$N"`

// boxUsageScript：$1 目标、$2 范围目录。每个普通文件一行字节数，不存在时什么也不打。
const boxUsageScript = `p=$(realpath -e -- "$1" 2>/dev/null) || exit 0
case "$p" in "$2"|"$2"/*) ;; *) exit 0;; esac
find "$p" -type f -printf '%s\n'`

// boxImportScript：$1 目标目录、$2 范围目录、$3 上限 KiB、$4 字节数；tar 从 stdin 来。
// 目标必须不存在或是空目录；解包失败时把放进去的东西清掉。
const boxImportScript = `R=$2 L=$3 N=$4
[ -L "$1" ] && exit 8
p=$(realpath -m -- "$1") || exit 4
case "$p" in "$R"/*) ;; *) exit 4;; esac
made=0
if [ -e "$p" ]; then
  [ -d "$p" ] || exit 8
  [ -z "$(ls -A -- "$p")" ] || exit 8
else
  mkdir -p -- "$p" || exit 1
  made=1
fi
q=$(realpath -e -- "$p") || exit 4
case "$q" in "$R"/*) ;; *) exit 4;; esac
used=$(du -sxk "$R" 2>/dev/null | cut -f1)
if [ $(( ${used:-0} + $N / 1024 )) -ge "$L" ]; then
  [ $made = 1 ] && rmdir -- "$q"
  echo "${used:-0}"; exit 75
fi
ulimit -f $(( ($L - ${used:-0}) * 2 ))
if ! tar -x -k --no-same-owner --no-same-permissions -f - -C "$q"; then
  if [ $made = 1 ]; then rm -rf -- "$q"; else find "$q" -mindepth 1 -delete; fi
  exit 1
fi`

func (s *boxFileStore) runWrite(ctx context.Context, op, rel, script string, stdin io.Reader, args ...string) (dockerResult, error) {
	b := s.box
	defer b.use()()
	if err := b.Ensure(ctx); err != nil {
		return dockerResult{}, err
	}
	callArgs := append(b.execArgs(stdin != nil), "/bin/sh", "-c", script, "sh")
	callArgs = append(callArgs, args...)
	res := b.host.docker(ctx, dockerCall{args: callArgs, stdin: stdin, timeout: boxControlTimeout})
	if res.code == 0 {
		return res, nil
	}
	if err := boxDiskRefusal(res, b.diskLimitBytes()); err != nil {
		return res, err
	}
	switch res.code {
	case 5:
		return res, &fs.PathError{Op: op, Path: rel, Err: errors.New("类型不对：是目录，或者不是普通文件")}
	case 6:
		return res, &fs.PathError{Op: op, Path: rel, Err: errors.New("超过大小上限")}
	case 7:
		return res, &fs.PathError{Op: op, Path: rel, Err: fs.ErrExist}
	case 8:
		return res, errNotEmptyDestination
	}
	return res, boxStoreError(op, rel, res)
}

func (s *boxFileStore) limitKiB() string {
	return strconv.FormatInt(max(s.box.diskLimitBytes()>>10, 1), 10)
}

func (s *boxFileStore) Lstat(ctx context.Context, rel string) (fs.FileInfo, error) {
	if rel == "." || rel == "" {
		return s.Stat(ctx, rel)
	}
	res, err := s.runWrite(ctx, "lstat", rel, boxLstatScript, nil, s.abs(rel), s.box.Root())
	if err != nil {
		return nil, err
	}
	return parseBoxStat(string(res.stdout))
}

func (s *boxFileStore) Head(ctx context.Context, rel string, n int) ([]byte, error) {
	res, err := s.runWrite(ctx, "read", rel, boxHeadScript, nil, s.abs(rel), s.box.Root(), strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	return res.stdout, nil
}

func (s *boxFileStore) WriteFile(ctx context.Context, rel string, data []byte, exclusive bool) error {
	flag := "0"
	if exclusive {
		flag = "1"
	}
	_, err := s.runWrite(ctx, "write", rel, boxStoreWriteScript, bytes.NewReader(data),
		s.abs(rel), s.box.Root(), s.limitKiB(), strconv.Itoa(len(data)), flag)
	return err
}

func (s *boxFileStore) MkdirAll(ctx context.Context, rel string) error {
	_, err := s.runWrite(ctx, "mkdir", rel, boxMkdirScript, nil, s.abs(rel), s.box.Root())
	return err
}

func (s *boxFileStore) Rename(ctx context.Context, from, to string) error {
	_, err := s.runWrite(ctx, "rename", from, boxRenameScript, nil, s.abs(from), s.abs(to), s.box.Root())
	return err
}

func (s *boxFileStore) CopyFile(ctx context.Context, from, to string, overwrite bool, maxBytes int64) (int64, error) {
	flag := "0"
	if overwrite {
		flag = "1"
	}
	res, err := s.runWrite(ctx, "copy", from, boxCopyScript, nil,
		s.abs(from), s.abs(to), s.box.Root(), strconv.FormatInt(maxBytes, 10), flag, s.limitKiB())
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(res.stdout)), 10, 64)
}

func (s *boxFileStore) Usage(ctx context.Context, rel string) int64 {
	res, err := s.runWrite(ctx, "usage", rel, boxUsageScript, nil, s.abs(rel), s.box.Root())
	if err != nil {
		return 0
	}
	var total int64
	for _, line := range strings.Fields(string(res.stdout)) {
		if n, err := strconv.ParseInt(line, 10, 64); err == nil {
			total += n
		}
	}
	return total
}

// ImportTree 把 dir 打成 tar 流进容器里解开。dir 是解压归档时自己建的中间目录，只放
// 目录和普通文件；这里照样只收这两种，别的跳过。
func (s *boxFileStore) ImportTree(ctx context.Context, rel, dir string) error {
	var size int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(writeBoxImportTar(writer, dir)) }()
	_, err = s.runWrite(ctx, "import", rel, boxImportScript, reader,
		s.abs(rel), s.box.Root(), s.limitKiB(), strconv.FormatInt(size, 10))
	_ = reader.Close()
	return err
}

func writeBoxImportTar(w io.Writer, dir string) error {
	archive := tar.NewWriter(w)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return archive.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: info.ModTime()})
		case entry.Type().IsRegular():
			if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
			file, err := root.Open(name)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(archive, file)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return archive.Close()
}

func (s *boxFileStore) BlockedWithin(string) bool { return false }

func (s *boxFileStore) lockKey(rel string) string {
	return boxContainerName + ":" + s.abs(rel)
}

// keepStateDir 放在宿主状态目录下按范围分开：同一台机器人的群和私聊是两个范围，各有
// 各的 keep/<机器人>/，索引也得各记各的。
func (s *boxFileStore) keepStateDir() string {
	dir := boxStateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "box-state", path.Base(s.box.Root()))
}

func (s *boxFileStore) stageDir() string { return "" }
