// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
)

// LoadLocalMediaBaseURL 读 WebUI 保存的媒体回源基址。第二个返回值是库里有没有
// 这条记录；空串一律视为未配置（保存空串即清除，行为与删除一致）。
func (s *SQLiteStore) LoadLocalMediaBaseURL(ctx context.Context) (string, bool, error) {
	var baseURL string
	ok, err := s.loadJSON(ctx, "local_media_base_url", &baseURL)
	return baseURL, ok, err
}

func (s *SQLiteStore) SaveLocalMediaBaseURL(ctx context.Context, baseURL string) error {
	return s.saveJSON(ctx, "local_media_base_url", baseURL)
}
