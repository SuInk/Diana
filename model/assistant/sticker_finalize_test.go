// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// stickerFinalizeLLMProvider 在收尾时按需填 sticker；sawField 记下收尾工具是否带了这个字段。
type stickerFinalizeLLMProvider struct {
	capturingLLMProvider
	sticker        string
	personaVerdict string
	sawField       bool
}

func (p *stickerFinalizeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "合不合你的人设") {
			return &llm.GenerateResponse{Text: p.personaVerdict}, nil
		}
	}
	response, err := p.capturingLLMProvider.Generate(ctx, req)
	if err != nil || response.Text != p.reply {
		return response, err
	}
	for _, tool := range req.Tools {
		if tool.Name != "agent_finalize" {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		_, p.sawField = properties[stickerFinalizeFieldName]
		arguments := map[string]any{"content": response.Text}
		if p.sticker != "" {
			arguments[stickerFinalizeFieldName] = p.sticker
		}
		response.ToolCalls = []llm.ToolCall{{ID: "finalize", Name: tool.Name, Arguments: arguments}}
		response.Text = ""
	}
	return response, nil
}

// 模型收尾时填了关键词：正文先发，紧跟一张命中的表情包；没填就只发正文。
func TestReplyFinalizeStickerFollowsText(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "smug.gif")
	body := []byte("smug-sticker")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sticker := range []string{"得意 叉腰", ""} {
		channel := &recordingChannel{}
		provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "嘿嘿，被你发现了"}, sticker: sticker, personaVerdict: "会"}
		rt := NewRuntime(BotConfig{AgentEnabled: true}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
		event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "tease", RawMessage: "你是不是偷吃了"}
		rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{
			Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1,
			Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[得意]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}},
		}}}})
		if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
			t.Fatal(err)
		}
		if !provider.sawField {
			t.Fatal("agent_finalize did not offer the sticker field")
		}
		sent := channel.sentSnapshot()
		if sticker == "" {
			if len(sent) != 1 || len(sent[0].ImageURLs) != 0 {
				t.Fatalf("no keywords: sent = %#v", sent)
			}
			continue
		}
		if len(sent) != 2 || sent[0].Text == "" || len(sent[1].ImageURLs) != 1 || sent[1].ImageURLs[0] != path {
			t.Fatalf("with keywords: sent = %#v", sent)
		}
	}
}

// 自动配图只发关键词命中的；库里没有命中时宁可不发，也不拿随机补位的顶上。
func TestStickerSendBestMatchRequiresKeywordHit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cat.gif")
	body := []byte("cat")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m"}
	channel := &recordingChannel{}
	provider := &stickerFinalizeLLMProvider{personaVerdict: "会"}
	rt := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{
		Kind: EventKindGroup, GroupID: "g", MessageID: "s", Time: 1,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[猫猫翻白眼]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}},
	}}}})
	tool := newDianaStickerTool(rt, event, nil)
	if sent, err := tool.sendBestMatch(context.Background(), "晚安 摸头"); err != nil || sent || len(channel.sentSnapshot()) != 0 {
		t.Fatalf("unmatched keywords sent=%v err=%v", sent, err)
	}
	if sent, err := tool.sendBestMatch(context.Background(), "翻白眼 无语"); err != nil || !sent || len(channel.sentSnapshot()) != 1 {
		t.Fatalf("matched keywords sent=%v err=%v", sent, err)
	}
	// 同一轮已经发过一张，单轮上限默认 1。
	if sent, _ := tool.sendBestMatch(context.Background(), "翻白眼"); sent || len(channel.sentSnapshot()) != 1 {
		t.Fatal("second sticker in the same turn was sent")
	}
}

// 自动配图前要过人设这一关：判定「不会」的不发，并缓存结果，下次检索直接跳过、不再问模型。
func TestStickerSendBestMatchChecksPersona(t *testing.T) {
	dir := t.TempDir()
	crude, cute := filepath.Join(dir, "crude.gif"), filepath.Join(dir, "cute.gif")
	for path, body := range map[string]string{crude: "crude", cute: "cute"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m"}
	store := &stickerPersonaTestStore{stickerAssetTestStore: stickerAssetTestStore{stickerHistoryStore: stickerHistoryStore{events: map[string][]MessageEvent{}}, assets: []StickerAsset{
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "a", EventTime: 2, Summary: "动画表情", Path: crude, ContentSHA256: imageBytesSHA256([]byte("crude")), Description: "大叔猥琐地说“晚安宝贝来我被窝”"},
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "b", EventTime: 1, Summary: "动画表情", Path: cute, ContentSHA256: imageBytesSHA256([]byte("cute")), Description: "小猫抱着枕头说“晚安”"},
	}}, verdicts: map[string]bool{}}
	channel := &recordingChannel{}
	judge := &stickerPersonaJudge{}
	rt := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return judge, nil })
	rt.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(rt, event, SettingValues{stickerSettingTurnLimit: 5})
	sent, err := tool.sendBestMatch(context.Background(), "晚安 被窝")
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if got := channel.sentSnapshot(); len(got) != 1 || got[0].ImageURLs[0] != cute {
		t.Fatalf("sent = %#v", got)
	}
	if fit, judged := store.verdicts[imageBytesSHA256([]byte("crude"))]; !judged || fit {
		t.Fatalf("crude verdict not cached: %#v", store.verdicts)
	}
	calls := judge.calls
	candidates, err := tool.candidates(context.Background(), "晚安 被窝")
	if err != nil || len(candidates) != 1 || candidates[0].Path != cute || judge.calls != calls {
		t.Fatalf("known-unfit sticker not dropped from search: %d candidates, calls %d→%d", len(candidates), calls, judge.calls)
	}
}

type stickerPersonaTestStore struct {
	stickerAssetTestStore
	verdicts map[string]bool
}

func (s *stickerPersonaTestStore) StickerPersonaFit(_ context.Context, _ string, hashes []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for _, hash := range hashes {
		if fit, ok := s.verdicts[hash]; ok {
			out[hash] = fit
		}
	}
	return out, nil
}

func (s *stickerPersonaTestStore) SaveStickerPersonaFit(_ context.Context, _ string, hash string, fit bool, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdicts[hash] = fit
	return nil
}

// stickerPersonaJudge 对简介里是大叔口吻的那张答「不会」，其余答「会」。
type stickerPersonaJudge struct{ calls int }

func (j *stickerPersonaJudge) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	j.calls++
	last := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(last, "大叔猥琐地说") {
		return &llm.GenerateResponse{Text: "不会：大叔口吻，不像我"}, nil
	}
	return &llm.GenerateResponse{Text: "会"}, nil
}

func TestParseStickerPersonaVerdict(t *testing.T) {
	for raw, want := range map[string]bool{"会": true, "「会」": true, "会，挺可爱的": true, "不会：太低俗": false, "不合适": false, "": false} {
		if got, _ := parseStickerPersonaVerdict(raw); got != want {
			t.Errorf("%q → %v, want %v", raw, got, want)
		}
	}
}
