// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"github.com/SuInk/diana/model/assistant"
)

func (s *SQLiteStore) LoadMediaDownloadCachePolicy(ctx context.Context) (assistant.MediaDownloadCachePolicy, bool, error) {
	var policy assistant.MediaDownloadCachePolicy
	ok, err := s.loadJSON(ctx, "media_download_cache_policy", &policy)
	return policy, ok, err
}

func (s *SQLiteStore) SaveMediaDownloadCachePolicy(ctx context.Context, policy assistant.MediaDownloadCachePolicy) error {
	return s.saveJSON(ctx, "media_download_cache_policy", policy)
}
