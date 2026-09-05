// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

var mediaContentLocks [64]sync.Mutex
var mediaSourceFetches singleflight.Group

type mediaSourceRecord struct {
	File        string `json:"file"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
}

// Content objects are immutable and independent of message, platform and URL.
// The first writer chooses the extension; later identical bytes reuse its path.
func persistMediaContent(dir string, body []byte, contentType, fileName string) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("media body is empty")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	objectDir := mediaObjectDir(dir, hex.EncodeToString(sum[:]))
	lock := &mediaContentLocks[sum[0]%byte(len(mediaContentLocks))]
	lock.Lock()
	defer lock.Unlock()
	path, valid := findMediaObject(objectDir, hex.EncodeToString(sum[:]), int64(len(body)))
	if valid {
		touchMediaFile(path)
		return path, nil
	}
	if err := os.MkdirAll(objectDir, 0o700); err != nil {
		return "", err
	}
	// Repair a corrupt object in place, keeping all existing source references
	// valid even if the new download suggests a different filename extension.
	if path == "" {
		path = filepath.Join(objectDir, "media"+mediaContentExtension(body, contentType, fileName))
	}
	if err := writeAtomicMediaFile(path, body); err != nil {
		return "", err
	}
	return path, nil
}

func mediaObjectDir(dir, digest string) string {
	return filepath.Join(dir, "objects", digest[:2], digest)
}

func mediaContentExtension(body []byte, contentType, fileName string) string {
	sniffed := http.DetectContentType(body)
	if strings.HasPrefix(sniffed, "image/") {
		return imageExtension(sniffed, body)
	}
	if ext := filepath.Ext(safeHistoryMediaFileName(fileName)); ext != "" {
		return ext
	}
	if strings.HasPrefix(contentType, "image/") {
		return imageExtension(contentType, body)
	}
	extensions := map[string]string{"application/pdf": ".pdf", "video/mp4": ".mp4", "audio/ogg": ".ogg", "application/ogg": ".ogg", "audio/wave": ".wav", "audio/mpeg": ".mp3"}
	if ext := extensions[sniffed]; ext != "" {
		return ext
	}
	if ext := extensions[contentType]; ext != "" {
		return ext
	}
	return ".bin"
}

func findMediaObject(dir, digest string, size int64) (string, bool) {
	entries, _ := os.ReadDir(dir)
	candidate := ""
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "media.") || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if candidate == "" {
			candidate = path
		}
		if validMediaContent(path, digest, size) {
			return path, true
		}
	}
	return candidate, false
}

func validMediaContent(path, digest string, size int64) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size || size <= 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, size+1))
	return err == nil && n == size && hex.EncodeToString(hash.Sum(nil)) == digest
}

func touchMediaFile(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

func writeAtomicMediaFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if _, err := f.Write(body); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func cachedMediaSource(dir, key string, maxBytes int64) string {
	path, _ := cachedMediaSourceDetails(dir, key, maxBytes)
	return path
}

func cachedMediaSourceDetails(dir, key string, maxBytes int64) (string, string) {
	index := mediaSourceIndexPath(dir, key)
	f, err := os.Open(index)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	var record mediaSourceRecord
	if err := json.NewDecoder(io.LimitReader(f, 4096)).Decode(&record); err != nil || record.Size <= 0 || record.Size > maxBytes {
		return "", ""
	}
	parts := strings.Split(record.File, "/")
	if len(parts) != 4 || parts[0] != "objects" || len(parts[2]) != 64 || parts[1] != parts[2][:2] ||
		!strings.HasPrefix(parts[3], "media.") || filepath.Base(parts[3]) != parts[3] || strings.ContainsAny(parts[3], `\/`) {
		return "", ""
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", ""
	}
	path := filepath.Join(dir, filepath.FromSlash(record.File))
	if !validMediaContent(path, parts[2], record.Size) {
		return "", ""
	}
	touchMediaFile(path)
	touchMediaFile(index)
	return path, record.ContentType
}

func rememberMediaSource(dir, key, path, contentType string, size int64) error {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return err
	}
	record, err := json.Marshal(mediaSourceRecord{File: filepath.ToSlash(relative), Size: size, ContentType: contentType})
	if err != nil {
		return err
	}
	return writeAtomicMediaFile(mediaSourceIndexPath(dir, key), record)
}

func mediaSourceIndexPath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, ".sources", hex.EncodeToString(sum[:])+".json")
}

// Only hashes are persisted in the source index, never URLs or access tokens.
// Failed fetches are not cached, and a waiting caller can cancel independently.
func fetchMediaContent(ctx context.Context, dir, key string, maxBytes int64, fetch func(context.Context) ([]byte, string, string, error)) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path := cachedMediaSource(dir, key, maxBytes); path != "" {
		return path, nil
	}
	flightKey := fmt.Sprintf("%s:%d", mediaSourceIndexPath(dir, key), maxBytes)
	result := mediaSourceFetches.DoChan(flightKey, func() (any, error) {
		if path := cachedMediaSource(dir, key, maxBytes); path != "" {
			return path, nil
		}
		body, contentType, name, err := fetch(ctx)
		if err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(body) == 0 || int64(len(body)) > maxBytes {
			return "", fmt.Errorf("media exceeds size limit or is empty")
		}
		path, err := persistMediaContent(dir, body, contentType, name)
		if err != nil {
			return "", err
		}
		if err := rememberMediaSource(dir, key, path, contentType, int64(len(body))); err != nil {
			return "", err
		}
		trimMediaSourceIndex(dir)
		return path, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return "", result.Err
		}
		return result.Val.(string), nil
	}
}

// Signed URLs can change frequently. Bound this small optimization index;
// removing an index never deletes media referenced by message history.
func trimMediaSourceIndex(dir string) {
	const maxSources = 4096
	root := filepath.Join(dir, ".sources")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) <= maxSources {
		return
	}
	files := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && strings.HasSuffix(info.Name(), ".json") {
			files = append(files, info)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().Before(files[j].ModTime()) })
	for len(files) > maxSources {
		_ = os.Remove(filepath.Join(root, files[0].Name()))
		files = files[1:]
	}
}
