// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
)

const (
	defaultMediaCacheDir = "data/media"
	// 单张图片上限。聊天里的图片很少超过这个体积，超了多半不是正常图片。
	defaultMediaMaxBytes = 10 << 20
	// 缓存目录总量上限，超出后按最后使用时间淘汰最旧的文件。
	defaultMediaCacheBytes = 512 << 20
	mediaFetchTimeout      = 20 * time.Second
)

// MediaStore 把图片持久化到本地，后续处理一律读本地文件。
//
// 为什么不直接把图片 URL 透传给 LLM：聊天平台的图片地址通常是短时效的，
// 有的实现还带 rkey 校验，境外或中转的模型服务多半拉不到，表现就是识图静默
// 失败。下载到本地后按 base64 提交，链路不再依赖服务商能否访问该地址。
//
// 按来源 URL 去重是必须的：历史上下文每轮都会重放同一批图片，不缓存的话
// 每次生成回复都要把这些图片重新下载一遍。
type MediaStore struct {
	dir        string
	maxBytes   int64
	cacheBytes int64
	client     *http.Client

	mu       sync.Mutex
	inFlight map[string]chan struct{}
}

// NewMediaStore 创建媒体存储。dir 为空时使用默认目录。
func NewMediaStore(dir string) *MediaStore {
	if strings.TrimSpace(dir) == "" {
		dir = defaultMediaCacheDir
	}
	return &MediaStore{
		dir:        dir,
		maxBytes:   defaultMediaMaxBytes,
		cacheBytes: defaultMediaCacheBytes,
		client:     &http.Client{Timeout: mediaFetchTimeout},
		inFlight:   map[string]chan struct{}{},
	}
}

// SetLimits 覆盖单文件与总量上限，0 表示保持默认。
func (s *MediaStore) SetLimits(maxBytes, cacheBytes int64) {
	if maxBytes > 0 {
		s.maxBytes = maxBytes
	}
	if cacheBytes > 0 {
		s.cacheBytes = cacheBytes
	}
}

// Dir 返回持久化目录，便于外部排查。
func (s *MediaStore) Dir() string {
	return s.dir
}

// Fetch 下载并持久化一个远程地址，返回本地路径。
// 已经缓存过的地址直接返回，不再发起网络请求。
func (s *MediaStore) Fetch(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("assistant: empty media url")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("assistant: unsupported media scheme")
	}

	path := s.pathFor(rawURL)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		// 命中缓存：刷新访问时间，淘汰时按最近使用排序。
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}

	// 同一地址并发只下载一次，其余等待复用结果。
	wait, leader := s.acquire(rawURL)
	if !leader {
		<-wait
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path, nil
		}
		return "", fmt.Errorf("assistant: concurrent media fetch failed for %s", redactURLQuery(rawURL))
	}
	defer s.release(rawURL)

	if err := s.download(ctx, rawURL, path); err != nil {
		return "", err
	}
	s.evictOverCap()
	return path, nil
}

// StoreImage 按内容哈希持久化已有图片字节。生图结果与入站图片共用同一个
// 容量上限和淘汰机制，避免再为 data URL 创建无所有权的系统临时文件。
func (s *MediaStore) StoreImage(body []byte, contentType string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("assistant: media store is not configured")
	}
	if len(body) == 0 {
		return "", fmt.Errorf("assistant: empty media body")
	}
	if int64(len(body)) > s.maxBytes {
		return "", fmt.Errorf("assistant: 媒体超过 %dMB 上限", s.maxBytes>>20)
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("assistant: unsupported image content type %q", contentType)
	}
	cacheDir, err := filepath.Abs(s.dir)
	if err != nil {
		return "", fmt.Errorf("assistant: resolve media dir: %w", err)
	}

	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	path := filepath.Join(cacheDir, "generated-"+digest+imageExtension(contentType, body))
	cacheKey := "generated:" + filepath.Base(path)
	wait, leader := s.acquire(cacheKey)
	if !leader {
		<-wait
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path, nil
		}
		return "", fmt.Errorf("assistant: concurrent media store failed")
	}
	defer s.release(cacheKey)

	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("assistant: create media dir: %w", err)
	}
	tmp, err := os.CreateTemp(cacheDir, ".partial-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(body); err != nil {
		return "", fmt.Errorf("assistant: save media: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("assistant: save media: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("assistant: persist media: %w", err)
	}
	s.evictOverCap()
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return "", fmt.Errorf("assistant: persisted media was evicted")
	}
	return path, nil
}

func (s *MediaStore) acquire(key string) (chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.inFlight[key]; ok {
		return ch, false
	}
	ch := make(chan struct{})
	s.inFlight[key] = ch
	return ch, true
}

func (s *MediaStore) release(key string) {
	s.mu.Lock()
	ch, ok := s.inFlight[key]
	delete(s.inFlight, key)
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (s *MediaStore) download(ctx context.Context, rawURL, dest string) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("assistant: create media dir: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	// 部分图床按 UA 拒绝空客户端。
	req.Header.Set("User-Agent", resolverUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("assistant: fetch media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("assistant: fetch media %s: HTTP %d", redactURLQuery(rawURL), resp.StatusCode)
	}

	// 先写临时文件再改名，避免下载中断留下半截文件被当作缓存命中。
	tmp, err := os.CreateTemp(s.dir, ".partial-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	// 多读 1 字节用于判断是否超限。
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, s.maxBytes+1))
	if err != nil {
		return fmt.Errorf("assistant: save media: %w", err)
	}
	if written > s.maxBytes {
		return fmt.Errorf("assistant: 媒体超过 %dMB 上限", s.maxBytes>>20)
	}
	if written == 0 {
		return fmt.Errorf("assistant: empty media body")
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

// pathFor 用来源地址的哈希作为文件名：同一地址稳定命中，不同地址不冲突。
func (s *MediaStore) pathFor(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:]))
}

// DataURL 把持久化文件读成 data: URL，供 LLM 以 base64 方式提交。
func (s *MediaStore) DataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("assistant: empty media file")
	}
	mediaType := http.DetectContentType(data)
	if !strings.HasPrefix(mediaType, "image/") {
		return "", fmt.Errorf("assistant: %s is not an image", filepath.Base(path))
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// evictOverCap 目录总量超限时按最后访问时间淘汰最旧的文件。
// 持久化不等于无限增长，否则长期运行会把磁盘塞满。
func (s *MediaStore) evictOverCap() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		size int64
		used time.Time
	}
	files := make([]item, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".partial-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, item{filepath.Join(s.dir, entry.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= s.cacheBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	for _, file := range files {
		if total <= s.cacheBytes {
			return
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}
