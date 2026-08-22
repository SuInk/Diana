// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	// 分词词典是后台异步加载的,不等它就绪的话,选词结果会随测试时序漂移:
	// 同一条用例可能这次用上词典词、下次只有 n-gram。统一等到就绪,
	// 测试面对的就是线上稳态。
	awaitCJKSegmenter()
	os.Exit(m.Run())
}
