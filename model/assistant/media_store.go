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
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// 来源索引避免重复下载，内容 SHA-256 避免不同 URL 或生图入口落盘多份。
type MediaStore struct {
	dir        string
	maxBytes   int64
	cacheBytes int64
	client     *http.Client
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

	path, err := fetchMediaContent(ctx, s.dir, "image:"+rawURL, min(s.maxBytes, s.cacheBytes), func(ctx context.Context) ([]byte, string, string, error) {
		// Preserve legacy cached paths while avoiding another network download.
		if f, err := os.Open(s.pathFor(rawURL)); err == nil {
			defer f.Close()
			if body, err := io.ReadAll(io.LimitReader(f, s.maxBytes+1)); err == nil && len(body) > 0 && int64(len(body)) <= s.maxBytes {
				return body, http.DetectContentType(body), "", nil
			}
		}
		body, contentType, err := s.download(ctx, rawURL)
		return body, contentType, "", err
	})
	if err != nil {
		return "", err
	}
	s.evictOverCap(path)
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
	if int64(len(body)) > s.cacheBytes {
		return "", fmt.Errorf("assistant: image exceeds media cache capacity")
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("assistant: unsupported image content type %q", contentType)
	}
	path, err := persistMediaContent(s.dir, body, contentType, "")
	if err != nil {
		return "", err
	}
	s.evictOverCap(path)
	return path, nil
}

func (s *MediaStore) download(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	// 部分图床按 UA 拒绝空客户端。
	req.Header.Set("User-Agent", resolverUserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("assistant: fetch media: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("assistant: fetch media %s: HTTP %d", redactURLQuery(rawURL), resp.StatusCode)
	}
	limit := min(s.maxBytes, s.cacheBytes)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("assistant: read media: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, "", fmt.Errorf("assistant: 媒体超过 %dMB 上限", limit>>20)
	}
	if len(body) == 0 {
		return nil, "", fmt.Errorf("assistant: empty media body")
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// pathFor locates legacy URL-keyed cache files for reuse without downloading.
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
func (s *MediaStore) evictOverCap(keep string) {
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		size int64
		used time.Time
	}
	files := make([]item, 0)
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			objects := filepath.Join(root, "objects")
			if path != root && path != objects && !strings.HasPrefix(path, objects+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".partial-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, item{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= s.cacheBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	for _, file := range files {
		if total <= s.cacheBytes {
			return
		}
		if file.path == keep {
			continue
		}
		if os.Remove(file.path) == nil {
			total -= file.size
			if strings.HasPrefix(file.path, filepath.Join(root, "objects")+string(filepath.Separator)) {
				_ = os.Remove(filepath.Dir(file.path))
			}
		}
	}
}
