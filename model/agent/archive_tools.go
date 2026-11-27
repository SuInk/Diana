// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

const (
	defaultArchiveMaxBytes int64 = 64 << 20
	defaultArchiveMaxFiles       = 10000
)

type ExtractArchiveTool struct {
	root      string
	protected protectedFiles
	maxBytes  int64
	maxFiles  int
	store     FileStore
}

func (t *ExtractArchiveTool) Name() string { return "extract_archive" }
func (t *ExtractArchiveTool) Description() string {
	return "将工作目录内的 ZIP、tar 或 tar.gz 归档解压到新建或空目录。禁止覆盖已有文件，拒绝不安全路径、链接和特殊条目；展开总量最多 64 MiB、10000 个条目。"
}
func (t *ExtractArchiveTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"archive_path", "destination"}, map[string]any{
		"archive_path": toolStringParam("工作目录内的归档相对路径（.zip、.tar、.tar.gz 或 .tgz）"),
		"destination":  toolStringParam("工作目录内的新建或空目录相对路径"),
	})
}

func archiveRelativePath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("unsafe archive path %q", name)
		}
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", fmt.Errorf("empty archive entry path %q", name)
	}
	return clean, nil
}

func (t *ExtractArchiveTool) Run(ctx context.Context, input map[string]any) (string, error) {
	archiveRel, destRel := stringFromInput(input, "archive_path"), stringFromInput(input, "destination")
	if archiveRel == "" || destRel == "" {
		return "", errors.New("archive_path and destination are required")
	}
	for _, rel := range []string{archiveRel, destRel} {
		if _, err := archiveRelativePath(rel); err != nil {
			return "", err
		}
	}
	store := toolFileStore(t.store, t.root, t.protected)
	archiveClean, err := store.Resolve(archiveRel)
	if err != nil {
		return "", err
	}
	destClean, err := store.Resolve(destRel)
	if err != nil {
		return "", err
	}
	if store.Blocked(archiveClean) {
		return "", errProtectedFile(archiveRel)
	}
	if store.Blocked(destClean) {
		return "", errProtectedFile(destRel)
	}
	// keep/ 的写入要登记归属和配额，解压绕不过那套账，直接不让落进去。
	if _, inKeep := keepLocation(destClean); inKeep {
		return "", errors.New("extract_archive cannot write into keep/; extract to tmp/ or outputs/ first")
	}
	if destClean == "." {
		return "", errNotEmptyDestination
	}
	info, err := store.Stat(ctx, archiveClean)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("archive must be a regular file")
	}
	source, err := store.Open(ctx, archiveClean)
	if err != nil {
		return "", err
	}
	defer source.Close()
	if err := emptyDestination(ctx, store, destClean); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(store.stageDir(), ".extract-archive-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	staged, err := os.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	defer staged.Close()
	maxBytes, maxFiles := t.maxBytes, t.maxFiles
	if maxBytes <= 0 {
		maxBytes = defaultArchiveMaxBytes
	}
	if maxFiles <= 0 {
		maxFiles = defaultArchiveMaxFiles
	}
	var total int64
	count, files := 0, 0
	seen := map[string]bool{}
	extract := func(name string, mode fs.FileMode, reader io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > maxFiles {
			return fmt.Errorf("archive exceeds %d entry limit", maxFiles)
		}
		clean, err := archiveRelativePath(name)
		if err != nil {
			return err
		}
		if seen[clean] {
			return fmt.Errorf("duplicate archive entry %q", name)
		}
		seen[clean] = true
		target, err := store.Resolve(joinStoreRel(destClean, clean))
		if err != nil {
			return err
		}
		for current := target; current != "."; current = path.Dir(current) {
			if store.Blocked(current) {
				return errProtectedFile(current)
			}
		}
		if mode.IsDir() {
			return staged.MkdirAll(clean, 0o755)
		}
		if !mode.IsRegular() {
			return fmt.Errorf("unsupported archive entry %q", name)
		}
		if err := staged.MkdirAll(path.Dir(clean), 0o755); err != nil {
			return err
		}
		f, err := staged.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(f, &archiveContextReader{ctx: ctx, reader: io.LimitReader(reader, maxBytes-total+1)})
		closeErr := f.Close()
		total += n
		if total > maxBytes {
			return fmt.Errorf("archive exceeds %d expanded byte limit", maxBytes)
		}
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		files++
		return nil
	}
	switch lower := strings.ToLower(archiveRel); {
	case strings.HasSuffix(lower, ".zip"):
		// 两种后端打开的文件都能随机读：宿主是 *os.File，容器是读进内存的 bytes.Reader。
		at, ok := source.(io.ReaderAt)
		if !ok {
			return "", errors.New("archive cannot be read at random offsets")
		}
		zr, err := zip.NewReader(at, info.Size())
		if err != nil {
			return "", err
		}
		for _, entry := range zr.File {
			if entry.Mode().IsDir() {
				err = extract(entry.Name, entry.Mode(), nil)
			} else {
				var r io.ReadCloser
				r, err = entry.Open()
				if err == nil {
					err = extract(entry.Name, entry.Mode(), r)
					closeErr := r.Close()
					if err == nil {
						err = closeErr
					}
				}
			}
			if err != nil {
				return "", err
			}
		}
	case strings.HasSuffix(lower, ".tar"), strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		var reader io.Reader = source
		var gz *gzip.Reader
		if !strings.HasSuffix(lower, ".tar") {
			gz, err = gzip.NewReader(source)
			if err != nil {
				return "", err
			}
			defer gz.Close()
			reader = gz
		}
		// Bound tar metadata and padding as well as extracted file contents.
		bounded := &archiveBudgetReader{reader: reader, remaining: maxBytes + int64(maxFiles)*4096 + 1024}
		tr := tar.NewReader(&archiveContextReader{ctx: ctx, reader: bounded})
		for {
			header, nextErr := tr.Next()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return "", nextErr
			}
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
				return "", fmt.Errorf("unsupported archive entry %q", header.Name)
			}
			if err := extract(header.Name, header.FileInfo().Mode(), tr); err != nil {
				return "", err
			}
		}
		if gz != nil {
			// tar EOF precedes gzip's trailer; drain it to verify the checksum too.
			if _, err := io.Copy(io.Discard, &archiveContextReader{ctx: ctx, reader: bounded}); err != nil {
				return "", err
			}
		}
	default:
		return "", errors.New("unsupported archive format; use zip, tar or tar.gz")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := store.ImportTree(ctx, destClean, stage); err != nil {
		return "", err
	}
	return marshalToolResult(map[string]any{"archive_path": archiveClean, "destination": destClean, "files": files, "entries": count, "bytes": total})
}

type archiveBudgetReader struct {
	reader    io.Reader
	remaining int64
}

func (r *archiveBudgetReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, errors.New("archive stream exceeds expanded byte budget")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
