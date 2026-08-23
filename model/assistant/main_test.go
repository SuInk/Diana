// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	// Agent 的工作目录跟着数据库位置走。测试里把它指到临时目录，免得用例往
	// 开发机的真实缓存目录写文件。
	workspaceRoot, err := os.MkdirTemp("", "diana-assistant-test-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("APP_DB_PATH", filepath.Join(workspaceRoot, "app.db"))
	// 分词词典是后台异步加载的,不等它就绪的话,选词结果会随测试时序漂移:
	// 同一条用例可能这次用上词典词、下次只有 n-gram。统一开启并等到就绪,
	// 测试面对的就是开着词典分词的线上稳态。
	applyCJKSegmentConfig(BotConfig{DictSegmentEnabled: boolPointer(true)})
	if !awaitCJKSegmenter() {
		// 加载失败时选词会静默退回 n-gram,同一段中文被切成不同的词,依赖检索
		// 打分的用例就会莫名其妙地翻脸(例如摘要排到情景后面)。以前这里忽略返回值,
		// 于是「词典没装上」表现成一条排序断言失败,查起来完全不着边。
		fmt.Fprintln(os.Stderr, "分词词典加载失败,检索相关用例的结果不可信:", cjkSegmentErr)
		_ = os.RemoveAll(workspaceRoot)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(workspaceRoot)
	os.Exit(code)
}
