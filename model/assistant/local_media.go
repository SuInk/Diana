// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"log"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type LocalMediaSharer interface {
	Share(path string, ttl time.Duration) (string, bool)
}

type LocalMediaPathResolver interface {
	ResolveSharedPath(value string) (string, bool)
}

type LocalMediaStore struct {
	mu             sync.RWMutex
	baseURL        string
	basePath       string
	originProvider func() string
	items          map[string]localMediaItem
	indexDir       string
	now            func() time.Time
}

// SetIndexDir preserves unexpired share mappings across process restarts.
func (s *LocalMediaStore) SetIndexDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var item localMediaItem
		path := filepath.Join(dir, entry.Name())
		if body, err := os.ReadFile(path); err == nil && json.Unmarshal(body, &item) == nil && s.now().After(item.ExpiresAt) {
			_ = os.Remove(path)
		}
	}
	s.mu.Lock()
	s.indexDir = dir
	s.mu.Unlock()
	return nil
}

type localMediaItem struct {
	Path      string
	Name      string
	ExpiresAt time.Time
}

func NewLocalMediaStore(baseURL string) *LocalMediaStore {
	store := &LocalMediaStore{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		items:   map[string]localMediaItem{},
		now:     time.Now,
	}
	if parsed, err := neturl.Parse(store.baseURL); err == nil {
		store.basePath = strings.TrimRight(parsed.EscapedPath(), "/")
	}
	return store
}

// SetOriginProvider 注册“当前应使用的服务地址”回调。桥端（可能在容器或
// 另一台机器上）能用某个地址完成反向 ws 握手，就一定也能用同一地址回源
// 取媒体，所以按连接握手 Host 动态拼 URL 可以让用户只配置 ws 地址。
// 回调返回空串时退回构造时的静态基址。
func (s *LocalMediaStore) SetOriginProvider(provider func() string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.originProvider = provider
	s.mu.Unlock()
}

func (s *LocalMediaStore) shareBaseURL() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	provider := s.originProvider
	s.mu.RUnlock()
	if provider != nil {
		if origin := strings.TrimRight(strings.TrimSpace(provider()), "/"); origin != "" {
			return origin + s.basePath
		}
	}
	return s.baseURL
}

func (s *LocalMediaStore) Share(path string, ttl time.Duration) (string, bool) {
	baseURL := s.shareBaseURL()
	if baseURL == "" {
		return "", false
	}
	path = strings.TrimSpace(strings.TrimPrefix(path, "file://"))
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return "", false
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	now := s.now()
	token := uuid.NewString()

	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	item := localMediaItem{
		Path:      path,
		Name:      filepath.Base(path),
		ExpiresAt: now.Add(ttl),
	}
	if s.indexDir != "" {
		if err := s.persistItemLocked(token, item); err != nil {
			s.mu.Unlock()
			log.Printf("diana persist local media share failed: %v", err)
			return "", false
		}
	}
	s.items[token] = item
	s.mu.Unlock()

	return baseURL + "/" + neturl.PathEscape(token), true
}

func (s *LocalMediaStore) ResolveSharedPath(value string) (string, bool) {
	if s == nil || s.baseURL == "" {
		return "", false
	}
	shared, err := neturl.Parse(strings.TrimSpace(value))
	// 分享 URL 的主机名会随桥的握手地址变化（见 SetOriginProvider），这里
	// 只按路径前缀 + token 匹配；token 是一次性的 UUID，本身就是凭据。
	if err != nil || (shared.Scheme != "" && !strings.EqualFold(shared.Scheme, "http") && !strings.EqualFold(shared.Scheme, "https")) {
		return "", false
	}
	prefix := s.basePath + "/"
	sharedPath := shared.EscapedPath()
	if !strings.HasPrefix(sharedPath, prefix) {
		return "", false
	}
	escapedToken := strings.TrimPrefix(sharedPath, prefix)
	if escapedToken == "" || strings.Contains(escapedToken, "/") {
		return "", false
	}
	token, err := neturl.PathUnescape(escapedToken)
	if err != nil {
		return "", false
	}
	item, ok := s.lookup(token)
	if !ok {
		return "", false
	}
	return item.Path, true
}

func (s *LocalMediaStore) ServeToken(w http.ResponseWriter, r *http.Request, token string) {
	item, ok := s.lookup(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(item.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() || info.Size() == 0 {
		http.NotFound(w, r)
		return
	}
	if disposition := mime.FormatMediaType("inline", map[string]string{"filename": item.Name}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, item.Name, info.ModTime(), file)
}

func (s *LocalMediaStore) lookup(token string) (localMediaItem, bool) {
	if s == nil {
		return localMediaItem{}, false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return localMediaItem{}, false
	}
	now := s.now()
	s.mu.RLock()
	item, ok := s.items[token]
	dir := s.indexDir
	s.mu.RUnlock()
	if !ok && dir != "" {
		// Tokens are opaque UUIDs, never filesystem paths supplied by a message.
		parsed, err := uuid.Parse(token)
		if err != nil || parsed.String() != token {
			return localMediaItem{}, false
		}
		body, err := os.ReadFile(filepath.Join(dir, token+".json"))
		if err == nil && json.Unmarshal(body, &item) == nil {
			ok = true
		}
	}
	if !ok {
		return localMediaItem{}, false
	}
	if now.After(item.ExpiresAt) {
		if dir != "" {
			_ = os.Remove(filepath.Join(dir, token+".json"))
		}
		s.mu.Lock()
		if current, ok := s.items[token]; ok && now.After(current.ExpiresAt) {
			delete(s.items, token)
		}
		s.mu.Unlock()
		return localMediaItem{}, false
	}
	return item, true
}

func (s *LocalMediaStore) persistItemLocked(token string, item localMediaItem) error {
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.indexDir, ".share-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(s.indexDir, token+".json"))
}

func (s *LocalMediaStore) cleanupExpiredLocked(now time.Time) {
	for token, item := range s.items {
		if now.After(item.ExpiresAt) {
			delete(s.items, token)
			if s.indexDir != "" {
				_ = os.Remove(filepath.Join(s.indexDir, token+".json"))
			}
		}
	}
}
