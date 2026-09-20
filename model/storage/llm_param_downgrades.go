// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"

	"github.com/SuInk/diana/model/llm"
)

// llmParamDowngradesKey 存「某个端点的某个模型拒过哪些请求字段」。结论自带
// 时间戳，由 llm 包按保质期过滤，这里只负责原样存取。
const llmParamDowngradesKey = "llm_param_downgrades"

func (s *SQLiteStore) LoadLLMParamDowngrades(ctx context.Context) ([]llm.DowngradeRecord, error) {
	var records []llm.DowngradeRecord
	ok, err := s.loadJSON(ctx, llmParamDowngradesKey, &records)
	if err != nil || !ok {
		return nil, err
	}
	return records, nil
}

func (s *SQLiteStore) SaveLLMParamDowngrades(ctx context.Context, records []llm.DowngradeRecord) error {
	return s.saveJSON(ctx, llmParamDowngradesKey, records)
}
