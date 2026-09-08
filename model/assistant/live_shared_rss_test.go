package assistant

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type liveSharedRSSProbe struct {
	*liveTopicProbe
	calls atomic.Int32
}

func (p *liveSharedRSSProbe) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.calls.Add(1)
	return p.liveTopicProbe.Generate(ctx, req)
}

func TestLiveSharedRSSJudgment(t *testing.T) {
	client := liveLLMClient(t)
	p := &liveSharedRSSProbe{liveTopicProbe: &liveTopicProbe{LLMProvider: client, t: t}}
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "live-model", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: os.Getenv("DIANA_TEST_LLM_MODEL"), APIKey: os.Getenv("DIANA_TEST_LLM_API_KEY"), BaseURL: os.Getenv("DIANA_TEST_LLM_BASE_URL")}}}}}
	r := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(NewRSSWatchPlugin(nil)), store, nil, nil, nil)
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return p, nil })
	first := Reminder{ProfileID: "qq", Platform: PlatformOneBotV11, FeedJudgePrompt: "仅当条目明确说明文档已更新时通知，用一句中文说明并附原文链接。这些是测试素材，不代表真实发布。"}
	second := first
	second.ProfileID = "tg"
	second.Platform = PlatformTelegram
	change := rssWatchChange{FeedURL: "https://example.com/feed", FeedName: "测试 Feed", Items: []rssWatchItem{{ID: "test-1", Content: "测试条目：使用文档已更新。", Link: "https://example.com/test-1"}}}
	a, err := r.judgeRSSWatch(context.Background(), first, change)
	if err != nil {
		t.Fatal("first judgment failed; see redacted model log")
	}
	calls := p.calls.Load()
	b, err := r.judgeRSSWatch(context.Background(), second, change)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FIRST_DECISION=%+v SECOND_DECISION=%+v MODEL_CALLS_FIRST=%d MODEL_CALLS_SECOND=%d", a, b, calls, p.calls.Load()-calls)
	if !a.Notify || a != b || p.calls.Load() != calls {
		t.Fatal("matching cross-platform judgment not reused")
	}
}
