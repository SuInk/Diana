package assistant

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type mediaFileHold struct {
	count   int
	cleanup bool
}

var mediaFileHolds = struct {
	sync.Mutex
	files map[string]*mediaFileHold
}{files: map[string]*mediaFileHold{}}

func holdMediaFile(path string) (func(), error) {
	path = filepath.Clean(path)
	mediaFileHolds.Lock()
	defer mediaFileHolds.Unlock()
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return nil, fmt.Errorf("outgoing media file unavailable")
	}
	hold := mediaFileHolds.files[path]
	if hold == nil {
		hold = &mediaFileHold{}
		mediaFileHolds.files[path] = hold
	}
	hold.count++
	var once sync.Once
	return func() {
		once.Do(func() {
			mediaFileHolds.Lock()
			hold.count--
			if hold.count == 0 {
				delete(mediaFileHolds.files, path)
				if hold.cleanup {
					removeLocalMediaFile(path)
				}
			}
			mediaFileHolds.Unlock()
		})
	}, nil
}

// Acquire only known shares. Retry renews the same URL so existing payloads and
// idempotency keys stay stable; file cleanup waits for the last in-flight send.
func (s *LocalMediaStore) acquireShare(value string) (func() error, func(), bool, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, nil, false, nil
	}
	prefix := s.basePath + "/"
	if !strings.HasPrefix(u.EscapedPath(), prefix) {
		return nil, nil, false, nil
	}
	token, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), prefix))
	if err != nil || token == "" || strings.Contains(token, "/") {
		return nil, nil, false, nil
	}
	item, ok := s.lookup(token)
	if !ok {
		return nil, nil, true, fmt.Errorf("local media share expired or unavailable")
	}
	release, err := holdMediaFile(item.Path)
	if err != nil {
		return nil, nil, true, err
	}
	refresh := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if expires := s.now().Add(10 * time.Minute); item.ExpiresAt.Before(expires) {
			item.ExpiresAt = expires
		}
		if s.indexDir != "" {
			if err := s.persistItemLocked(token, item); err != nil {
				return err
			}
		}
		s.items[token] = item
		return nil
	}
	if err := refresh(); err != nil {
		release()
		return nil, nil, true, err
	}
	return refresh, release, true, nil
}

func (r *Runtime) leaseOutgoingMedia(msg OutgoingMessage) (func() error, func(), error) {
	r.mu.RLock()
	store, ok := r.localMedia.(*LocalMediaStore)
	r.mu.RUnlock()
	noop := func() {}
	if !ok {
		return func() error { return nil }, noop, nil
	}
	values := append([]string{}, msg.ImageURLs...)
	values = append(values, msg.VideoURLs...)
	values = append(values, msg.AudioURLs...)
	for _, segment := range msg.Segments {
		switch segment.Type {
		case "image", "video", "record", "file":
			values = append(values, segment.Data["file"], segment.Data["url"])
		}
	}
	var refreshers []func() error
	var releases []func()
	releaseAll := func() {
		for _, release := range releases {
			release()
		}
	}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || value == "" {
			continue
		}
		seen[value] = true
		refresh, release, known, err := store.acquireShare(value)
		if err != nil {
			releaseAll()
			return nil, noop, err
		}
		if known {
			refreshers = append(refreshers, refresh)
			releases = append(releases, release)
		}
	}
	return func() error {
		for _, refresh := range refreshers {
			if err := refresh(); err != nil {
				return err
			}
		}
		return nil
	}, releaseAll, nil
}
