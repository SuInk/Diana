// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// crossGroupTimelineStore 在检索桩上加一份按会话存的时间线，供取原群前后文。
type crossGroupTimelineStore struct {
	*crossGroupHistoryStore
	timeline map[string][]MessageEvent
}

func (s *crossGroupTimelineStore) ListMessageEventsBetween(_ context.Context, session string, fromTime, throughTime int64) ([]MessageEvent, error) {
	var out []MessageEvent
	for _, item := range s.timeline[session] {
		if item.Time >= fromTime && item.Time <= throughTime {
			out = append(out, item)
		}
	}
	return out, nil
}

// 线上那次：「我又忘了」捞到另一个群的「我又忘了滚木啥意思了」。联想照常带过来，
// 但附上它在原群的前后文和距今多久，模型能看出那边在聊滚木、不是这边的事。
// 前后文里不在本群的人只留占位，不把他的话带进来。
func TestCrossGroupContextCarriesSourceNeighborsAndAge(t *testing.T) {
	const now = int64(100000)
	channel := &crossGroupMembershipChannel{allowed: map[string]bool{
		"current|speaker": true, "current|haruka": true, "current|yuanye": true,
	}}
	source := crossGroupTestEvent(now-38*60, "shared", "speaker", "src", "我又忘了滚木啥意思了")
	store := &crossGroupTimelineStore{
		crossGroupHistoryStore: &crossGroupHistoryStore{candidates: []MessageEvent{source}},
		timeline: map[string][]MessageEvent{"bot-a:group:shared": {
			crossGroupTestEvent(source.Time-300, "shared", "stranger", "n1", "小尘又卡了，私下说一句我最近在吃药"),
			crossGroupTestEvent(source.Time-120, "shared", "haruka", "n2", "它现在躺得比皇室战争那根滚木还平"),
			source,
			crossGroupTestEvent(source.Time+60, "shared", "yuanye", "n3", "有个滚木"),
			crossGroupTestEvent(source.Time+7200, "shared", "yuanye", "far", "窗口之外的话"),
		}},
	}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	current := crossGroupTestEvent(now, "current", "speaker", "cur", "我又忘了")
	current.ContextNamespace = "bot-a"

	var cross MessageEvent
	for _, item := range runtime.contextHistory(current) {
		if item.crossGroupContext {
			cross = item
		}
	}
	if !cross.crossGroupContext {
		t.Fatal("跨群联想没有照常带上相关消息")
	}
	text := historyPlainText(cross)
	for _, want := range []string{"我又忘了滚木啥意思了", "〔原群前后文：", "之前「不在本群的人：（内容略）」「haruka：它现在躺得比皇室战争那根滚木还平」", "之后「yuanye：有个滚木」"} {
		if !strings.Contains(text, want) {
			t.Fatalf("跨群参考缺少 %q：%q", want, text)
		}
	}
	for _, leaked := range []string{"吃药", "窗口之外"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("跨群参考带出了不该带的内容 %q：%q", leaked, text)
		}
	}
	if prompt := historyPromptTextAt(cross, current.Time); !strings.Contains(prompt, "，约38分钟前] ") || strings.Contains(prompt, "shared") {
		t.Fatalf("跨群标记没有标出距今多久或暴露了来源群：%q", prompt)
	}
}

func TestCrossGroupAgeText(t *testing.T) {
	for seconds, want := range map[int64]string{30: "1分钟", 38 * 60: "38分钟", 5 * 3600: "5小时", 47 * 3600: "47小时", 11 * 86400: "11天"} {
		if got := crossGroupAgeText(seconds); got != want {
			t.Fatalf("crossGroupAgeText(%d) = %q, want %q", seconds, got, want)
		}
	}
}

// crossGroupPromptRecorder 记下每一次模型请求，回复固定文本。
type crossGroupPromptRecorder struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
}

func (p *crossGroupPromptRecorder) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return &llm.GenerateResponse{Text: "修好了"}, nil
}

func (p *crossGroupPromptRecorder) joined() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var builder strings.Builder
	for _, req := range p.requests {
		for _, message := range req.Messages {
			builder.WriteString(message.Content)
			for _, part := range message.Parts {
				builder.WriteString(part.Text)
			}
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// 检索挪到确定回复之后，整条链路上跨群联想要照常进到回复模型的提示词里。
func TestCrossGroupContextReachesReplyPromptAfterRouting(t *testing.T) {
	channel := &crossGroupMembershipChannel{allowed: map[string]bool{"current|speaker": true}}
	store := &crossGroupHistoryStore{candidates: []MessageEvent{
		crossGroupTestEvent(190, "shared", "speaker", "related", "图片缓存修复已经部署到测试机器"),
	}}
	provider := &crossGroupPromptRecorder{}
	runtime := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true), AgentEnabled: true, GroupTriggers: []string{"Diana"}}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMessageHistoryStore(store)
	current := crossGroupTestEvent(200, "current", "speaker", "current", "Diana 图片缓存修复现在怎么样")
	current.RawMessage = "Diana 图片缓存修复现在怎么样"
	runtime.remember(current)
	prepared, text, handled, outcome := runtime.prepareMessageEvent(context.Background(), current)
	if !handled {
		t.Fatalf("点名机器人的消息应进回复，outcome = %s", outcome)
	}
	if store.searchCalls != 0 {
		t.Fatalf("路由阶段不该检索跨群，实际 %d 次", store.searchCalls)
	}
	if _, err := runtime.replyTo(context.Background(), prepared, text); err != nil {
		t.Fatal(err)
	}
	prompt := provider.joined()
	if !strings.Contains(prompt, "[跨群历史 ") || !strings.Contains(prompt, "图片缓存修复已经部署到测试机器") {
		t.Fatalf("回复提示词里没有跨群联想：searches=%d\n%s", store.searchCalls, prompt)
	}
	if store.searchCalls != 1 {
		t.Fatalf("一轮回复应检索跨群一次，实际 %d 次", store.searchCalls)
	}
}
