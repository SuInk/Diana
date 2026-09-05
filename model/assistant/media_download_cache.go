// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
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

const mediaDownloadCacheMaxAge = 7 * 24 * time.Hour

type MediaDownloadCachePolicy struct {
	RetentionDays int   `json:"retention_days"`
	MaxMB         int64 `json:"max_mb"`
}

func (p MediaDownloadCachePolicy) Validate() error {
	if p.RetentionDays < -1 || p.RetentionDays > 36500 || p.MaxMB < 0 || p.MaxMB > 1<<20 {
		return fmt.Errorf("缓存保留天数必须为 -1 到 36500，容量必须为 0 到 1048576 MiB")
	}
	return nil
}

func (p MediaDownloadCachePolicy) WithDefaults() MediaDownloadCachePolicy {
	if p.RetentionDays == 0 {
		p.RetentionDays = 7
	}
	return p
}

func CurrentMediaDownloadCachePolicy() MediaDownloadCachePolicy {
	mediaDownloadUsers.Lock()
	defer mediaDownloadUsers.Unlock()
	days := -1
	if mediaDownloadUsers.maxAge > 0 {
		days = int(mediaDownloadUsers.maxAge / (24 * time.Hour))
	}
	return MediaDownloadCachePolicy{RetentionDays: days, MaxMB: mediaDownloadUsers.maxBytes >> 20}
}

var mediaDownloadFlights singleflight.Group
var mediaDownloadUsers = struct {
	sync.Mutex
	active   map[string]int
	maxBytes int64
	maxAge   time.Duration
}{active: make(map[string]int), maxAge: mediaDownloadCacheMaxAge}

// ConfigureMediaDownloadCache applies the process-wide download-cache policy.
// Zero days selects seven days, -1 disables expiry; zero bytes disables the cap.
func ConfigureMediaDownloadCache(retentionDays int, maxBytes int64) error {
	if retentionDays < -1 || retentionDays > 36500 || maxBytes < 0 {
		return fmt.Errorf("invalid download cache retention or capacity")
	}
	maxAge := mediaDownloadCacheMaxAge
	if retentionDays < 0 {
		maxAge = 0
	} else if retentionDays > 0 {
		maxAge = time.Duration(retentionDays) * 24 * time.Hour
	}
	mediaDownloadUsers.Lock()
	defer mediaDownloadUsers.Unlock()
	mediaDownloadUsers.maxAge = maxAge
	mediaDownloadUsers.maxBytes = maxBytes
	return nil
}

// CleanupMediaDownloadCache is also called by daily maintenance when no new
// downloads arrive. Active readers defer cleanup until the final lease ends.
func CleanupMediaDownloadCache() error {
	dir, err := mediaDownloadCacheDir()
	if err != nil {
		return err
	}
	mediaDownloadUsers.Lock()
	defer mediaDownloadUsers.Unlock()
	if mediaDownloadUsers.active[dir] == 0 {
		pruneMediaDownloadCache(dir, mediaDownloadUsers.maxBytes, mediaDownloadUsers.maxAge, time.Now())
	}
	return nil
}

type cachedMediaDownload struct {
	path, contentType string
}

func mediaDownloadCacheDir() (string, error) {
	dir, err := historyMediaDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(dir, "download-cache"))
}

// Readers hold a lease until parsing finishes. Eviction never removes an active
// reader's file, and canceled callers cannot evict a still-running fetch.
func holdMediaDownloadCache(dir string) func() {
	mediaDownloadUsers.Lock()
	mediaDownloadUsers.active[dir]++
	mediaDownloadUsers.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mediaDownloadUsers.Lock()
			defer mediaDownloadUsers.Unlock()
			mediaDownloadUsers.active[dir]--
			if mediaDownloadUsers.active[dir] == 0 {
				delete(mediaDownloadUsers.active, dir)
				pruneMediaDownloadCache(dir, mediaDownloadUsers.maxBytes, mediaDownloadUsers.maxAge, time.Now())
			}
		})
	}
}

// Trust domains get separate source indices, while identical verified bytes
// share storage. A trusted adapter URL must not bypass public HTTP policy.
func acquireMediaDownload(ctx context.Context, client *http.Client, source, name, digest, scope string, maxBytes int64) (string, string, func(), error) {
	dir, err := mediaDownloadCacheDir()
	if err != nil {
		return "", "", func() {}, err
	}
	release := holdMediaDownloadCache(dir)
	urlKey := scope + ":url:" + source
	key := urlKey
	if digest != "" {
		key = scope + ":md5:" + digest
	}
	loadURL := func(ctx context.Context) (cachedMediaDownload, error) {
		return fetchMediaDownload(ctx, dir, urlKey, maxBytes, func() (cachedMediaDownload, error) {
			return streamMediaDownload(ctx, client, dir, source, name, maxBytes)
		})
	}
	var result cachedMediaDownload
	if key == urlKey {
		result, err = loadURL(ctx)
	} else {
		result, err = fetchMediaDownload(ctx, dir, key, maxBytes, func() (cachedMediaDownload, error) {
			result, err := loadURL(ctx)
			if err != nil {
				return result, err
			}
			f, err := os.Open(result.path)
			if err != nil {
				return result, err
			}
			defer f.Close()
			hash := md5.New()
			_, err = io.Copy(hash, io.LimitReader(f, maxBytes+1))
			if err != nil {
				return result, err
			}
			if hex.EncodeToString(hash.Sum(nil)) != digest {
				return result, errMediaIdentityMismatch
			}
			return result, nil
		})
		if err == errMediaIdentityMismatch {
			result, err = loadURL(ctx)
		}
	}
	if err != nil {
		release()
		return "", "", func() {}, err
	}
	return result.path, result.contentType, release, nil
}

func fetchMediaDownload(ctx context.Context, dir, key string, maxBytes int64, load func() (cachedMediaDownload, error)) (cachedMediaDownload, error) {
	if err := ctx.Err(); err != nil {
		return cachedMediaDownload{}, err
	}
	if path, mime := cachedMediaSourceDetails(dir, key, maxBytes); path != "" {
		return cachedMediaDownload{path, mime}, nil
	}
	result := mediaDownloadFlights.DoChan(fmt.Sprintf("%s:%d", mediaSourceIndexPath(dir, key), maxBytes), func() (any, error) {
		release := holdMediaDownloadCache(dir)
		defer release()
		if path, mime := cachedMediaSourceDetails(dir, key, maxBytes); path != "" {
			return cachedMediaDownload{path, mime}, nil
		}
		file, err := load()
		if err != nil {
			return file, err
		}
		if err := ctx.Err(); err != nil {
			return file, err
		}
		info, err := os.Stat(file.path)
		if err != nil {
			return file, err
		}
		err = rememberMediaSource(dir, key, file.path, file.contentType, info.Size())
		return file, err
	})
	select {
	case <-ctx.Done():
		return cachedMediaDownload{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return cachedMediaDownload{}, result.Err
		}
		return result.Val.(cachedMediaDownload), nil
	}
}

func streamMediaDownload(ctx context.Context, client *http.Client, dir, source, name string, maxBytes int64) (cachedMediaDownload, error) {
	if maxBytes <= 0 {
		return cachedMediaDownload{}, fmt.Errorf("invalid media size limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return cachedMediaDownload{}, err
	}
	req.Header.Set("User-Agent", "Diana/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return cachedMediaDownload{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cachedMediaDownload{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return cachedMediaDownload{}, fmt.Errorf("media exceeds file parser limit: %d > %d bytes", resp.ContentLength, maxBytes)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cachedMediaDownload{}, err
	}
	f, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return cachedMediaDownload{}, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return cachedMediaDownload{}, err
	}
	if written <= 0 || written > maxBytes {
		return cachedMediaDownload{}, fmt.Errorf("media empty or exceeds file parser limit: %d > %d bytes", written, maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return cachedMediaDownload{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return cachedMediaDownload{}, err
	}
	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return cachedMediaDownload{}, err
	}
	if err := f.Close(); err != nil {
		return cachedMediaDownload{}, err
	}
	sum := hash.Sum(nil)
	digest := hex.EncodeToString(sum)
	objectDir := mediaObjectDir(dir, digest)
	lock := &mediaContentLocks[int(sum[0])%len(mediaContentLocks)]
	lock.Lock()
	defer lock.Unlock()
	path, valid := findMediaObject(objectDir, digest, written)
	if !valid {
		if err := os.MkdirAll(objectDir, 0o700); err != nil {
			return cachedMediaDownload{}, err
		}
		if path == "" {
			path = filepath.Join(objectDir, "media"+mediaContentExtension(header[:n], resp.Header.Get("Content-Type"), name))
		}
		if err := os.Rename(f.Name(), path); err != nil {
			return cachedMediaDownload{}, err
		}
	}
	touchMediaFile(path)
	return cachedMediaDownload{path, resp.Header.Get("Content-Type")}, nil
}

func pruneMediaDownloadCache(dir string, maxBytes int64, maxAge time.Duration, now time.Time) {
	if maxAge <= 0 && maxBytes <= 0 {
		// Source metadata is bounded independently; trimming it never deletes
		// downloaded files, including abandoned transfers in never-clean mode.
		trimMediaSourceIndex(dir)
		return
	}
	// Abandoned transfers from a previous process are never content objects.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), ".partial-") {
				continue
			}
			if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > mediaDownloadCacheMaxAge {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
	type entry struct {
		path string
		info os.FileInfo
	}
	var files []entry
	var total int64
	root := filepath.Join(dir, "objects")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() || !strings.HasPrefix(d.Name(), "media.") {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			files = append(files, entry{path, info})
			total += info.Size()
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].info.ModTime().Before(files[j].info.ModTime()) })
	for _, file := range files {
		expired := maxAge > 0 && now.Sub(file.info.ModTime()) > maxAge
		overCapacity := maxBytes > 0 && total > maxBytes
		if !expired && !overCapacity {
			continue
		}
		if os.Remove(file.path) == nil {
			total -= file.info.Size()
			_ = os.Remove(filepath.Dir(file.path))
			_ = os.Remove(filepath.Dir(filepath.Dir(file.path)))
		}
	}
	trimMediaSourceIndex(dir)
}
