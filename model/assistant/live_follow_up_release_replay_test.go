// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveFollowUpReleaseReplay 拿真实 Release 更新说明对比仓库订阅跟评：
// old 去掉「版本发布」那段要求（参考资料改个段名就不触发），new 走现在的完整路径。
// 线上 v0.9.7 推送后跟评只有一句、只挑了一项，这里看改完是不是在介绍版本。
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=... DIANA_TEST_LLM_MODEL=gpt-6.1-terra \
//	DIANA_REPLAY_PROFILE=profile.json DIANA_REPLAY_RELEASE_DIR=dir（里面放 vX.Y.Z.md） \
//	DIANA_REPLAY_OUT=out.jsonl go test ./model/assistant -run TestLiveFollowUpReleaseReplay -v
func TestLiveFollowUpReleaseReplay(t *testing.T) {
	profilePath := os.Getenv("DIANA_REPLAY_PROFILE")
	releaseDir := os.Getenv("DIANA_REPLAY_RELEASE_DIR")
	if profilePath == "" || releaseDir == "" {
		t.Skip("set DIANA_REPLAY_PROFILE and DIANA_REPLAY_RELEASE_DIR")
	}
	client := liveLLMClient(t)
	rawProfile, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(releaseDir, "v*.md"))
	if len(files) == 0 {
		t.Fatal("no release notes")
	}
	samples := 3
	type row struct {
		Arm, Tag, Text, Error string
		Runes                 int
	}
	var (
		mu   sync.Mutex
		rows []row
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 4)
	)
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		tag := strings.TrimSuffix(filepath.Base(file), ".md")
		notice := "GitHub 动态：SuInk/Diana\nRelease " + tag + "\n发布于 2026-11-05 12:18:35\nhttps://github.com/SuInk/Diana/releases/tag/" + tag
		for _, arm := range []string{"old", "new"} {
			reference := "Release " + tag + " 更新说明：\n" + strings.TrimSpace(string(body))
			if arm == "old" {
				reference = "Release " + tag + " 说明：\n" + strings.TrimSpace(string(body))
			}
			for i := 0; i < samples; i++ {
				wg.Add(1)
				go func(arm, tag, reference string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					var cfg BotConfig
					if err := json.Unmarshal(rawProfile, &cfg); err != nil {
						t.Error(err)
						return
					}
					// 回放只看措辞：工具要连真实插件和凭据，这里不装。
					cfg.AgentEnabled = false
					cfg = cfg.WithDefaults()
					runtime := NewRuntime(cfg, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
					runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
					source := MessageEvent{Platform: cfg.Platform, Kind: EventKindGroup, GroupID: "10001", SelfID: cfg.BotAccount, ProfileID: cfg.ID}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					text := runtime.followUpCommentWithReference(ctx, followUpKindRepositoryWatch, source, notice, reference)
					cancel()
					mu.Lock()
					rows = append(rows, row{Arm: arm, Tag: tag, Text: text, Runes: len([]rune(text))})
					mu.Unlock()
				}(arm, tag, reference)
			}
		}
	}
	wg.Wait()
	if path := os.Getenv("DIANA_REPLAY_OUT"); path != "" {
		out, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range rows {
			line, _ := json.Marshal(item)
			_, _ = out.Write(append(line, '\n'))
		}
		_ = out.Close()
	}
	for _, item := range rows {
		t.Logf("[%s %s] %d 字：%s", item.Arm, item.Tag, item.Runes, item.Text)
	}
}
