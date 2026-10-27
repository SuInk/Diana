// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 压缩包安装：没法或不想放 GitHub 的插件（内网、私有、离线部署），直接上传
// zip / tar.gz。上传的包先转成与 GitHub 归档同形的 tar.gz，后面的清单校验、
// 白名单落盘、bundle 检查全部复用仓库安装那一套，不另起一条宽松的路。

// RepoPluginMaxUploadBytes 是上传压缩包的体积上限，与仓库归档下载上限一致。
const RepoPluginMaxUploadBytes = repoPluginMaxTotalBytes

var (
	// ErrRepoPluginArchive 表示上传的文件不是可识别的插件压缩包。
	ErrRepoPluginArchive = errors.New("diana: 只支持 .zip 或 .tar.gz 格式的插件压缩包")
	// ErrRepoPluginArchiveChanged 表示安装时上传的文件与预览时的不是同一个。
	ErrRepoPluginArchiveChanged = errors.New("diana: 上传的文件与预览时不是同一个，请重新预览确认后再安装")
)

// uploadedArchiveRoot 是转换后归档的第一层目录名；stripArchiveRoot 会把它剥掉。
const uploadedArchiveRoot = "plugin"

type uploadedArchiveEntry struct {
	name string
	body []byte
}

// PreviewArchive 校验上传的压缩包，返回与仓库安装同样的确认框信息。
// Commit 是压缩包的 SHA-256，安装时带回核对，保证装的就是预览过的那个文件。
func (i *RepoPluginInstaller) PreviewArchive(name string, data []byte) (RepoPluginPreview, error) {
	snapshot, err := loadArchiveSnapshot(name, data)
	if err != nil {
		return RepoPluginPreview{}, err
	}
	preview := snapshot.preview()
	preview.Risk.Warnings = append(preview.Risk.Warnings, "压缩包的来源无法由 Diana 核实，请确认它来自你信任的作者；之后要更新，需要重新上传新版压缩包。")
	return preview, nil
}

// InstallArchiveGuarded 安装上传的压缩包。expectedDigest 必须等于预览时返回的 SHA-256。
func (i *RepoPluginInstaller) InstallArchiveGuarded(name string, data []byte, expectedDigest string, guard func(PluginManifest) error) (*RepoPlugin, RepoPluginSource, error) {
	snapshot, err := loadArchiveSnapshot(name, data)
	if err != nil {
		return nil, RepoPluginSource{}, err
	}
	if strings.ToLower(strings.TrimSpace(expectedDigest)) != snapshot.commit {
		return nil, RepoPluginSource{}, ErrRepoPluginArchiveChanged
	}
	return i.installSnapshot(snapshot, guard)
}

func loadArchiveSnapshot(name string, data []byte) (repoPluginSnapshot, error) {
	name = cleanUploadedArchiveName(name)
	sum := sha256.Sum256(data)
	normalized, err := normalizeUploadedPluginArchive(data)
	if err != nil {
		return repoPluginSnapshot{}, err
	}
	origin := RepoPluginSource{Archive: name}.Origin()
	snapshot, err := newRepoPluginSnapshot(normalized, origin, hex.EncodeToString(sum[:]))
	if err != nil {
		return repoPluginSnapshot{}, err
	}
	snapshot.archiveName = name
	return snapshot, nil
}

// cleanUploadedArchiveName 只留文件名本身，去掉浏览器可能带上的路径，并限制长度。
func cleanUploadedArchiveName(name string) string {
	name = strings.TrimSpace(path.Base(filepath.ToSlash(strings.TrimSpace(name))))
	if name == "" || name == "." || name == "/" {
		return "plugin.zip"
	}
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
	}
	return name
}

// normalizeUploadedPluginArchive 把 zip / tar.gz 转成第一层目录为 plugin/ 的 tar.gz。
// 清单可以在包的根上，也可以在唯一的顶层目录里（直接压缩插件文件夹的常见形态）。
func normalizeUploadedPluginArchive(data []byte) ([]byte, error) {
	var entries []uploadedArchiveEntry
	var err error
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")):
		entries, err = readUploadedZip(data)
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		entries, err = readUploadedTarGz(data)
	default:
		return nil, ErrRepoPluginArchive
	}
	if err != nil {
		return nil, err
	}
	prefix, err := uploadedArchivePrefix(entries)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		rel := strings.TrimPrefix(entry.name, prefix)
		if rel == "" {
			continue
		}
		header := &tar.Header{Name: uploadedArchiveRoot + "/" + rel, Mode: 0o644, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(entry.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// uploadedArchivePrefix 找清单所在的目录前缀：根上有清单返回空串；所有文件都在同一个
// 顶层目录里、且清单在那一层，返回 "目录/"；其余情况认为不是插件包。
func uploadedArchivePrefix(entries []uploadedArchiveEntry) (string, error) {
	top := ""
	shared := true
	for _, entry := range entries {
		if entry.name == RepoPluginManifestFile {
			return "", nil
		}
		first, _, nested := strings.Cut(entry.name, "/")
		if !nested || (top != "" && first != top) {
			shared = false
		}
		top = first
	}
	if shared && top != "" {
		for _, entry := range entries {
			if entry.name == top+"/"+RepoPluginManifestFile {
				return top + "/", nil
			}
		}
	}
	return "", fmt.Errorf("%w: 压缩包根目录（或唯一的顶层文件夹）里没有 %s", ErrRepoPluginManifest, RepoPluginManifestFile)
}

// cleanUploadedEntryName 统一路径分隔符并丢掉系统垃圾文件；返回空串表示跳过。
// 越路径的条目在这里直接丢弃，转换后的归档里不会出现 ../。
func cleanUploadedEntryName(name string) string {
	name = strings.TrimLeft(strings.ReplaceAll(name, "\\", "/"), "/")
	name = path.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return ""
	}
	if name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
		return ""
	}
	return name
}

// uploadedArchiveBudget 统计解出来的文件数与总体积。压缩包可以很小、解开很大，
// 所以按实际读出的字节计数，而不是信头里写的大小。
type uploadedArchiveBudget struct {
	files int
	bytes int64
}

func (b *uploadedArchiveBudget) read(name string, r io.Reader) ([]byte, error) {
	b.files++
	if b.files > repoPluginMaxFiles {
		return nil, fmt.Errorf("diana: 插件压缩包文件数超过上限 %d", repoPluginMaxFiles)
	}
	remaining := int64(repoPluginMaxTotalBytes) - b.bytes
	body, err := io.ReadAll(io.LimitReader(r, remaining+1))
	if err != nil {
		return nil, fmt.Errorf("diana: 读取压缩包里的 %s 失败: %w", name, err)
	}
	b.bytes += int64(len(body))
	if b.bytes > repoPluginMaxTotalBytes {
		return nil, fmt.Errorf("diana: 插件压缩包解开后超过上限 %dMB", repoPluginMaxTotalBytes>>20)
	}
	return body, nil
}

func readUploadedZip(data []byte) ([]uploadedArchiveEntry, error) {
	// 含 ../ 的条目在 GODEBUG=zipinsecurepath=0 下会让 NewReader 报 ErrInsecurePath，
	// 但 reader 仍可用；这类条目由 cleanUploadedEntryName 丢掉，不必整包拒绝。
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("%w: %v", ErrRepoPluginArchive, err)
	}
	var budget uploadedArchiveBudget
	var entries []uploadedArchiveEntry
	for _, file := range reader.File {
		// 符号链接和目录都不落盘；zip 里的符号链接 Mode 不是常规文件。
		if !file.Mode().IsRegular() {
			continue
		}
		name := cleanUploadedEntryName(file.Name)
		if name == "" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrRepoPluginArchive, file.Name, err)
		}
		body, err := budget.read(name, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		entries = append(entries, uploadedArchiveEntry{name: name, body: body})
	}
	return entries, nil
}

func readUploadedTarGz(data []byte) ([]uploadedArchiveEntry, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRepoPluginArchive, err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var budget uploadedArchiveBudget
	var entries []uploadedArchiveEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRepoPluginArchive, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := cleanUploadedEntryName(header.Name)
		if name == "" {
			continue
		}
		body, err := budget.read(name, reader)
		if err != nil {
			return nil, err
		}
		entries = append(entries, uploadedArchiveEntry{name: name, body: body})
	}
}
