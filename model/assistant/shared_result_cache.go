package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const sharedCacheMaxEntries = 64
const sharedCacheMaxBytes = 16 << 20
const sharedCacheMaxEntryBytes = 4 << 20

type sharedCacheEntry struct {
	data    []byte
	created time.Time
	expires time.Time
}

// Cached JSON owns its memory. Every reader gets a fresh decoded value, so
// per-subscription cursor filtering and platform rendering cannot mutate it.
type sharedResultCache[T any] struct {
	mu       sync.Mutex
	entries  map[string]*sharedCacheEntry
	bytes    int
	revision uint64
	flights  singleflight.Group
}

func sharedResultKey(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (c *sharedResultCache[T]) get(key string, ttl time.Duration, valid func(T) bool) (T, bool) {
	c.mu.Lock()
	entry := c.entries[key]
	c.mu.Unlock()
	var value T
	if entry == nil {
		return value, false
	}
	if time.Now().Before(entry.expires) && time.Since(entry.created) < ttl && json.Unmarshal(entry.data, &value) == nil && (valid == nil || valid(value)) {
		return value, true
	}
	c.mu.Lock()
	if c.entries[key] == entry {
		delete(c.entries, key)
		c.bytes -= len(entry.data)
	}
	c.mu.Unlock()
	return value, false
}

func (c *sharedResultCache[T]) put(key string, data []byte, ttl time.Duration, revision uint64) {
	if len(data) > sharedCacheMaxEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision != c.revision {
		return
	}
	if c.entries == nil {
		c.entries = map[string]*sharedCacheEntry{}
	}
	now := time.Now()
	for k, entry := range c.entries {
		if k == key || !now.Before(entry.expires) {
			delete(c.entries, k)
			c.bytes -= len(entry.data)
		}
	}
	for len(c.entries) >= sharedCacheMaxEntries || c.bytes+len(data) > sharedCacheMaxBytes {
		var oldestKey string
		var oldest *sharedCacheEntry
		for k, entry := range c.entries {
			if oldest == nil || entry.created.Before(oldest.created) {
				oldestKey, oldest = k, entry
			}
		}
		if oldest == nil {
			break
		}
		delete(c.entries, oldestKey)
		c.bytes -= len(oldest.data)
	}
	c.entries[key] = &sharedCacheEntry{data: data, created: now, expires: now.Add(ttl)}
	c.bytes += len(data)
}

// Fresh baselines invalidate in-flight writers as well as completed entries.
func (c *sharedResultCache[T]) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revision++
	c.entries = nil
	c.bytes = 0
}

func (c *sharedResultCache[T]) load(ctx context.Context, key string, ttl, budget time.Duration, valid func(T) bool, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if key == "" || ttl <= 0 {
		return fetch(ctx)
	}
	if value, ok := c.get(key, ttl, valid); ok {
		return value, nil
	}
	c.mu.Lock()
	revision := c.revision
	c.mu.Unlock()
	flightKey := fmt.Sprintf("%s|%d|%d|%d", key, ttl, budget, revision)
	result := c.flights.DoChan(flightKey, func() (out any, err error) {
		defer func() {
			if failure := recover(); failure != nil {
				err = fmt.Errorf("shared source loader panicked: %v", failure)
			}
		}()
		if value, ok := c.get(key, ttl, valid); ok {
			return json.Marshal(value)
		}
		// A single caller's cancellation must not abort other subscribers.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		defer cancel()
		value, err := fetch(loadCtx)
		if err != nil {
			return nil, err
		}
		if err := loadCtx.Err(); err != nil {
			return nil, err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if valid == nil || valid(value) {
			c.put(key, data, ttl, revision)
		}
		return data, nil
	})
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case result := <-result:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if result.Err != nil {
			return zero, result.Err
		}
		err := json.Unmarshal(result.Val.([]byte), &zero)
		return zero, err
	}
}
