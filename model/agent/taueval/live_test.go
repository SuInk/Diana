// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package taueval

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveTau 跑 DIANA_TAU_CASES 指向的用例（文件或目录）。真实用例含群聊原文，放在仓库外。
// 被测、用户模拟器与评审默认同一模型；DIANA_TAU_USER_MODEL、DIANA_TAU_JUDGE_MODEL 可分别另指定。
func TestLiveTau(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DIANA_TAU_CASES"))
	if os.Getenv("DIANA_LIVE_LLM") != "1" || path == "" {
		t.Skip("set DIANA_LIVE_LLM=1, DIANA_TEST_LLM_* and DIANA_TAU_CASES")
	}
	cases, err := LoadCases(path)
	if err != nil {
		t.Fatal(err)
	}
	trials, _ := strconv.Atoi(os.Getenv("DIANA_TAU_TRIALS"))
	parallel, _ := strconv.Atoi(os.Getenv("DIANA_TAU_PARALLEL"))
	model := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL"))
	opts := Options{Agent: liveClient(t, model), Trials: trials, Parallel: parallel, Logf: t.Logf}
	if userModel := strings.TrimSpace(os.Getenv("DIANA_TAU_USER_MODEL")); userModel != "" {
		opts.User = liveClient(t, userModel)
	}
	if judgeModel := strings.TrimSpace(os.Getenv("DIANA_TAU_JUDGE_MODEL")); judgeModel != "" {
		opts.Judge = liveClient(t, judgeModel)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	report := Evaluate(ctx, opts, cases)
	for _, c := range report.Cases {
		t.Logf("%s: %d/%d", c.Case, c.Successes, c.Trials)
	}
	for k, v := range report.PassHatK {
		t.Logf("pass^%d = %.3f", k+1, v)
	}
	for category, curve := range report.Categories {
		t.Logf("[%s] pass^1 = %.3f pass^%d = %.3f", category, curve[0], len(curve), curve[len(curve)-1])
	}
	if out := os.Getenv("DIANA_TAU_REPORT"); out != "" {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func liveClient(t *testing.T, model string) llm.LLMClient {
	t.Helper()
	config := llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")),
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    model,
		APIStyle: llm.APIStyle(strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_STYLE"))),
		Timeout:  120 * time.Second,
	}
	if provider := strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_PROVIDER")); provider != "" {
		config.Provider = llm.Provider(provider)
	}
	client, err := llm.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
