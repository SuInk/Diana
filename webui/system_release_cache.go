// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const releaseCacheTTL = 30 * time.Minute

// ReleaseCacheStore persists the shared GitHub Release cache across restarts.
type ReleaseCacheStore interface {
	LoadReleaseCache(context.Context) ([]byte, bool, error)
	SaveReleaseCache(context.Context, []byte) error
}

type persistedReleaseCache struct {
	Key              string                  `json:"key,omitempty"`
	ETag             string                  `json:"etag,omitempty"`
	Releases         []persistedReleaseEntry `json:"releases,omitempty"`
	FetchedAt        time.Time               `json:"fetched_at,omitempty"`
	RateLimitResetAt time.Time               `json:"rate_limit_reset_at,omitempty"`
}

type persistedReleaseEntry struct {
	Tag               string         `json:"tag"`
	Name              string         `json:"name,omitempty"`
	Notes             string         `json:"notes,omitempty"`
	Prerelease        bool           `json:"prerelease,omitempty"`
	Date              time.Time      `json:"date,omitempty"`
	URL               string         `json:"url,omitempty"`
	ChecksumAvailable bool           `json:"checksum_available"`
	ChecksumURL       string         `json:"checksum_url,omitempty"`
	Assets            []ReleaseAsset `json:"assets,omitempty"`
}

func persistedReleaseFromEntry(entry ReleaseEntry) persistedReleaseEntry {
	return persistedReleaseEntry{
		Tag:               entry.Tag,
		Name:              entry.Name,
		Notes:             entry.Notes,
		Prerelease:        entry.Prerelease,
		Date:              entry.Date,
		URL:               entry.URL,
		ChecksumAvailable: entry.ChecksumAvailable,
		ChecksumURL:       entry.ChecksumURL,
		Assets:            append([]ReleaseAsset(nil), entry.Assets...),
	}
}

func (entry persistedReleaseEntry) releaseEntry() ReleaseEntry {
	return ReleaseEntry{
		Tag:               entry.Tag,
		Name:              entry.Name,
		Notes:             entry.Notes,
		Prerelease:        entry.Prerelease,
		Date:              entry.Date,
		URL:               entry.URL,
		ChecksumAvailable: entry.ChecksumAvailable,
		ChecksumURL:       entry.ChecksumURL,
		Assets:            append([]ReleaseAsset(nil), entry.Assets...),
	}
}

func releaseCacheEntries(entries []ReleaseEntry) []persistedReleaseEntry {
	persisted := make([]persistedReleaseEntry, len(entries))
	for index, entry := range entries {
		persisted[index] = persistedReleaseFromEntry(entry)
	}
	return persisted
}

func releaseEntriesFromCache(entries []persistedReleaseEntry) []ReleaseEntry {
	releases := make([]ReleaseEntry, len(entries))
	for index, entry := range entries {
		releases[index] = entry.releaseEntry()
	}
	return releases
}

func (h *SystemUpdateHandler) SetReleaseCacheStore(ctx context.Context, store ReleaseCacheStore) error {
	h.releaseCacheStore = store
	if store == nil {
		return nil
	}
	payload, ok, err := store.LoadReleaseCache(ctx)
	if err != nil || !ok {
		return err
	}
	var cached persistedReleaseCache
	if err := json.Unmarshal(payload, &cached); err != nil {
		return fmt.Errorf("decode system release cache: %w", err)
	}
	h.releaseCacheMu.Lock()
	h.releaseCache = cached
	h.releaseCacheMu.Unlock()
	return nil
}

func (h *SystemUpdateHandler) githubReleases(ctx context.Context, owner, repo string, limit int) ([]ReleaseEntry, error) {
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	key := fmt.Sprintf("%s/%s?per_page=%d", strings.TrimSpace(owner), strings.TrimSpace(repo), limit)
	force, _ := ctx.Value(releaseRefreshContextKey{}).(bool)
	now := h.currentTime()
	if releases, ok := h.cachedReleases(key, now, false); ok && !force {
		return releases, nil
	}
	if resetAt, limited := h.activeGitHubRateLimit(now); limited {
		if releases, ok := h.cachedReleases(key, now, true); ok {
			return releases, nil
		}
		if strings.EqualFold(owner, defaultReleaseOwner) && strings.EqualFold(repo, defaultReleaseRepo) {
			if fallback, err := h.fetchStaticReleaseManifest(ctx); err == nil && len(fallback) > 0 {
				return fallback, nil
			}
		}
		return nil, &githubRateLimitError{StatusCode: 403, ResetAt: resetAt}
	}

	flightKey := key
	if force {
		flightKey += "#refresh"
	}
	result := h.releaseFetch.DoChan(flightKey, func() (any, error) {
		now := h.currentTime()
		if releases, ok := h.cachedReleases(key, now, false); ok && !force {
			return releases, nil
		}
		if resetAt, limited := h.activeGitHubRateLimit(now); limited {
			if releases, ok := h.cachedReleases(key, now, true); ok {
				return releases, nil
			}
			if strings.EqualFold(owner, defaultReleaseOwner) && strings.EqualFold(repo, defaultReleaseRepo) {
				if fallback, err := h.fetchStaticReleaseManifest(ctx); err == nil && len(fallback) > 0 {
					return fallback, nil
				}
			}
			return nil, &githubRateLimitError{StatusCode: 403, ResetAt: resetAt}
		}

		fetchTimeout := h.httpClient.Timeout
		if fetchTimeout <= 0 {
			fetchTimeout = 10 * time.Second
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()
		h.releaseCacheMu.RLock()
		etag := ""
		if h.releaseCache.Key == key {
			etag = h.releaseCache.ETag
		}
		h.releaseCacheMu.RUnlock()
		fetched, err := fetchGitHubReleases(fetchCtx, h.httpClient, h.githubAPIBase, owner, repo, limit, h.currentGitHubToken(), etag)
		if err != nil {
			var rateLimitErr *githubRateLimitError
			if errors.As(err, &rateLimitErr) {
				h.rememberGitHubRateLimit(fetchCtx, rateLimitErr.ResetAt)
				if stale, ok := h.cachedReleases(key, now, true); ok {
					return stale, nil
				}
			}
			if strings.EqualFold(owner, defaultReleaseOwner) && strings.EqualFold(repo, defaultReleaseRepo) {
				if fallback, fallbackErr := h.fetchStaticReleaseManifest(fetchCtx); fallbackErr == nil && len(fallback) > 0 {
					h.saveReleaseCache(fetchCtx, persistedReleaseCache{Key: key, Releases: releaseCacheEntries(fallback), FetchedAt: now})
					return fallback, nil
				}
			}
			return nil, err
		}
		if fetched.NotModified {
			h.releaseCacheMu.RLock()
			cached := h.releaseCache
			h.releaseCacheMu.RUnlock()
			cached.FetchedAt = now
			if fetched.ETag != "" {
				cached.ETag = fetched.ETag
			}
			cached.RateLimitResetAt = time.Time{}
			h.saveReleaseCache(fetchCtx, cached)
			return releaseEntriesFromCache(cached.Releases), nil
		}
		releases := fetched.Releases
		h.saveReleaseCache(fetchCtx, persistedReleaseCache{
			Key:       key,
			ETag:      fetched.ETag,
			Releases:  releaseCacheEntries(releases),
			FetchedAt: now,
		})
		return cloneReleaseEntries(releases), nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case fetched := <-result:
		if fetched.Err != nil {
			return nil, fetched.Err
		}
		releases, ok := fetched.Val.([]ReleaseEntry)
		if !ok {
			return nil, errors.New("GitHub Release 缓存结果类型无效")
		}
		return cloneReleaseEntries(releases), nil
	}
}

func (h *SystemUpdateHandler) fetchStaticReleaseManifest(ctx context.Context) ([]ReleaseEntry, error) {
	url := strings.TrimSpace(h.staticReleaseURL)
	if url == "" {
		return nil, errors.New("静态 Release 清单未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "diana-webui")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("静态 Release 清单 HTTP %d", resp.StatusCode)
	}
	var manifest struct {
		Releases []persistedReleaseEntry `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return releaseEntriesFromCache(manifest.Releases), nil
}

func (h *SystemUpdateHandler) cachedReleases(key string, now time.Time, allowStale bool) ([]ReleaseEntry, bool) {
	h.releaseCacheMu.RLock()
	cached := h.releaseCache
	h.releaseCacheMu.RUnlock()
	if cached.Key != key || cached.FetchedAt.IsZero() {
		return nil, false
	}
	if !allowStale && !now.Before(cached.FetchedAt.Add(releaseCacheTTL)) {
		return nil, false
	}
	return releaseEntriesFromCache(cached.Releases), true
}

func (h *SystemUpdateHandler) activeGitHubRateLimit(now time.Time) (time.Time, bool) {
	h.releaseCacheMu.RLock()
	resetAt := h.releaseCache.RateLimitResetAt
	h.releaseCacheMu.RUnlock()
	return resetAt, !resetAt.IsZero() && now.Before(resetAt)
}

func (h *SystemUpdateHandler) rememberGitHubRateLimit(ctx context.Context, resetAt time.Time) {
	if resetAt.IsZero() {
		return
	}
	h.releaseCacheMu.Lock()
	cached := h.releaseCache
	if resetAt.After(cached.RateLimitResetAt) {
		cached.RateLimitResetAt = resetAt
		h.releaseCache = cached
	}
	h.releaseCacheMu.Unlock()
	h.persistReleaseCache(ctx, cached)
}

func (h *SystemUpdateHandler) saveReleaseCache(ctx context.Context, cached persistedReleaseCache) {
	h.releaseCacheMu.Lock()
	h.releaseCache = cached
	h.releaseCacheMu.Unlock()
	h.persistReleaseCache(ctx, cached)
}

func (h *SystemUpdateHandler) persistReleaseCache(ctx context.Context, cached persistedReleaseCache) {
	if h.releaseCacheStore == nil {
		return
	}
	payload, err := json.Marshal(cached)
	if err != nil {
		return
	}
	h.releaseCachePersistMu.Lock()
	defer h.releaseCachePersistMu.Unlock()
	_ = h.releaseCacheStore.SaveReleaseCache(ctx, payload)
}

func (h *SystemUpdateHandler) currentTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func cloneReleaseEntries(entries []ReleaseEntry) []ReleaseEntry {
	cloned := append([]ReleaseEntry(nil), entries...)
	for index := range cloned {
		cloned[index].Assets = append([]ReleaseAsset(nil), cloned[index].Assets...)
	}
	return cloned
}
