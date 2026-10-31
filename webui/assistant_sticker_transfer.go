// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"

	"github.com/gin-gonic/gin"
)

const (
	stickerPackManifestName = "manifest.json"
	stickerPackFormat       = "diana-stickers"
	stickerPackVersion      = 1
	// 导入包整体和单张图的上限：表情包一般几十 KB 到几 MB，几千张的包也在这个范围内。
	stickerPackMaxUpload  = 1 << 30
	stickerPackMaxImage   = 20 << 20
	stickerPackMaxEntries = 20000
)

// stickerPackManifest 是导出包里的 manifest.json。图片放在 images/ 下，文件名是哈希加扩展名。
type stickerPackManifest struct {
	Format     string                    `json:"format"`
	Version    int                       `json:"version"`
	ExportedAt time.Time                 `json:"exported_at"`
	Stickers   []stickerPackManifestItem `json:"stickers"`
}

type stickerPackManifestItem struct {
	File string `json:"file"`
	storage.StickerTransferItem
}

// StickerImportResult 是一次导入的结果。Imported 按「图 × 机器人」计，Duplicate 是这台机器人已经导入过的。
type StickerImportResult struct {
	Images    int      `json:"images"`
	Imported  int      `json:"imported"`
	Duplicate int      `json:"duplicate"`
	Skipped   int      `json:"skipped"`
	Profiles  []string `json:"profiles"`
	Errors    []string `json:"errors,omitempty"`
}

// exportStickers 按当前筛选条件把表情包原图和标注打成 zip 下载。文件已经被清理掉的图跳过。
func (h *BotHandler) exportStickers(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	query := stickerLibraryQueryFrom(c)
	query.Limit, query.Offset = 0, 0
	items, err := h.sqlite.ListStickerTransferItems(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取表情包池失败"})
		return
	}
	if len(items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "没有可以导出的表情包"})
		return
	}
	now := time.Now()
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="diana-stickers-%s.zip"`, now.Format("20060102-150405")))
	c.Status(http.StatusOK)
	archive := zip.NewWriter(c.Writer)
	manifest := stickerPackManifest{Format: stickerPackFormat, Version: stickerPackVersion, ExportedAt: now.UTC(), Stickers: []stickerPackManifestItem{}}
	for _, item := range items {
		name, err := writeStickerPackImage(archive, item)
		if err != nil {
			continue
		}
		manifest.Stickers = append(manifest.Stickers, stickerPackManifestItem{File: name, StickerTransferItem: item})
	}
	// 响应头已经发出，中途出错只能截断下载；导入时没有清单的包会被当成坏包。
	writer, err := archive.Create(stickerPackManifestName)
	if err == nil {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(manifest)
	}
	if err == nil {
		err = archive.Close()
	}
	if err != nil {
		_ = c.Error(err)
	}
}

func writeStickerPackImage(archive *zip.Writer, item storage.StickerTransferItem) (string, error) {
	file, err := os.Open(item.Path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	name := "images/" + item.Hash + stickerImageExtension(item.MIME, head[:n])
	// 图片本身已经压缩过，原样存进去，省得白算一遍 deflate。
	writer, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Now()})
	if err != nil {
		return "", err
	}
	_, err = io.Copy(writer, file)
	return name, err
}

// importStickers 导入表情包 zip：Diana 导出的包连标注一起导入；别处打的包只要是一堆图片也认，
// 文件名当作表情名。指定了机器人只导给它，没指定导给全部机器人。
func (h *BotHandler) importStickers(c *gin.Context) {
	if h.sqlite == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "事件存储未配置"})
		return
	}
	profiles := h.stickerImportProfiles(botProfileScope(c))
	if len(profiles) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还没有机器人，先添加机器人再导入"})
		return
	}
	// 请求体就是 zip 本身。zip 要随机读，先落到临时文件，大包也不占内存。
	temp, err := os.CreateTemp("", "diana-stickers-*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建临时文件失败"})
		return
	}
	defer func() {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
	}()
	size, err := io.Copy(temp, http.MaxBytesReader(c.Writer, c.Request.Body, stickerPackMaxUpload))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "上传失败，压缩包不能超过 1 GB"})
		return
	}
	archive, err := zip.NewReader(temp, size)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不是有效的 zip 文件"})
		return
	}
	dir, err := assistant.StickerImportDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建表情包目录失败"})
		return
	}
	result, err := h.importStickerPack(c, archive, dir, profiles)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *BotHandler) stickerImportProfiles(scope string) []string {
	if scope != "" {
		return []string{scope}
	}
	if h.profiles == nil {
		return nil
	}
	var ids []string
	for _, profile := range h.profiles.Profiles().Profiles {
		if id := strings.TrimSpace(profile.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *BotHandler) importStickerPack(c *gin.Context, archive *zip.Reader, dir string, profiles []string) (StickerImportResult, error) {
	result := StickerImportResult{Profiles: profiles}
	if len(archive.File) > stickerPackMaxEntries {
		return result, fmt.Errorf("包里文件太多（超过 %d 个）", stickerPackMaxEntries)
	}
	byName := map[string]*zip.File{}
	for _, entry := range archive.File {
		byName[entry.Name] = entry
	}
	// 有 Diana 的清单就按清单导，标注一起带上；没有就把包里的图片都当表情包。
	var entries []stickerPackManifestItem
	if manifestFile := byName[stickerPackManifestName]; manifestFile != nil {
		body, err := readStickerPackEntry(manifestFile, 64<<20)
		if err != nil {
			return result, fmt.Errorf("读取 manifest.json 失败: %w", err)
		}
		var manifest stickerPackManifest
		if err := json.Unmarshal(body, &manifest); err != nil || manifest.Format != stickerPackFormat {
			return result, errors.New("manifest.json 不是 Diana 导出的表情包清单")
		}
		if manifest.Version > stickerPackVersion {
			return result, errors.New("这个表情包是新版 Diana 导出的，请先升级再导入")
		}
		entries = manifest.Stickers
	} else {
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() || strings.HasPrefix(path.Base(entry.Name), ".") || strings.HasPrefix(entry.Name, "__MACOSX/") {
				continue
			}
			stem := strings.TrimSuffix(path.Base(entry.Name), path.Ext(entry.Name))
			entries = append(entries, stickerPackManifestItem{File: entry.Name, StickerTransferItem: storage.StickerTransferItem{Summary: stem}})
		}
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		zipFile := byName[entry.File]
		if zipFile == nil {
			result.Skipped++
			continue
		}
		body, err := readStickerPackEntry(zipFile, stickerPackMaxImage)
		if err != nil {
			result.Skipped++
			result.addError(fmt.Sprintf("%s：%v", entry.File, err))
			continue
		}
		mime := http.DetectContentType(body)
		if !strings.HasPrefix(mime, "image/") {
			result.Skipped++
			continue
		}
		// 以图片内容重算哈希，清单里写的不作数，免得一张图冒用另一张的标注。
		digest := sha256.Sum256(body)
		hash := hex.EncodeToString(digest[:])
		if !strings.EqualFold(strings.TrimSpace(entry.Hash), hash) {
			entry.Tagged, entry.Gist, entry.Tags, entry.Category, entry.TagVersion, entry.Description = false, "", nil, "", "", ""
		}
		if seen[hash] {
			continue
		}
		seen[hash] = true
		entry.Hash, entry.MIME = hash, mime
		if looksLikeHash(entry.Summary) {
			entry.Summary = ""
		}
		filePath := filepath.Join(dir, hash+stickerImageExtension(mime, body))
		if err := writeStickerImportFile(filePath, body); err != nil {
			return result, fmt.Errorf("保存表情包失败: %w", err)
		}
		result.Images++
		for _, profile := range profiles {
			added, err := h.sqlite.ImportStickerAsset(c.Request.Context(), profile, filePath, entry.StickerTransferItem)
			if err != nil {
				return result, fmt.Errorf("写入表情包池失败: %w", err)
			}
			if added {
				result.Imported++
			} else {
				result.Duplicate++
			}
		}
	}
	if result.Images == 0 {
		return result, errors.New("包里没有可以导入的图片")
	}
	return result, nil
}

func (r *StickerImportResult) addError(message string) {
	if len(r.Errors) < 20 {
		r.Errors = append(r.Errors, message)
	}
}

func readStickerPackEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("超过 %d MB", limit>>20)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	// 头里写的大小可以造假，读的时候再限一次。
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("超过 %d MB", limit>>20)
	}
	return body, nil
}

// writeStickerImportFile 按内容寻址落盘，同一张图已经在就不再写。先写临时文件再改名，导到一半断了也不留半张图。
func writeStickerImportFile(filePath string, body []byte) error {
	if existing, err := os.ReadFile(filePath); err == nil && bytes.Equal(existing, body) {
		return nil
	}
	temp, err := os.CreateTemp(filepath.Dir(filePath), ".partial-*")
	if err != nil {
		return err
	}
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(temp.Name())
		return err
	}
	return os.Rename(temp.Name(), filePath)
}

func stickerImageExtension(mime string, head []byte) string {
	if mime == "" || !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(head)
	}
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	}
	return ".img"
}

// looksLikeHash 认出导出包里那种「哈希.png」的文件名，它们当不了表情名。
func looksLikeHash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 16 {
		return false
	}
	return strings.IndexFunc(strings.ToLower(value), func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) < 0
}
