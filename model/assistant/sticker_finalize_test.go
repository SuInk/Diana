// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// stickerFinalizeLLMProvider 在收尾时按需填 sticker；sawField 记下收尾工具是否带了这个字段。
type stickerFinalizeLLMProvider struct {
	capturingLLMProvider
	sticker        string
	personaVerdict string
	personaCheck   func(context.Context) error
	silent         bool
	order          string
	sawField       bool
	sawOrder       bool
}

func (p *stickerFinalizeLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "合不合你的人设") {
			if p.personaCheck != nil {
				if err := p.personaCheck(ctx); err != nil {
					return nil, err
				}
			}
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
		if order, ok := properties[stickerOrderFieldName].(map[string]any); ok {
			// 两个值要在 schema 里对等列出，不能靠留空当默认。
			enum, _ := order["enum"].([]string)
			p.sawOrder = len(enum) == 2 && enum[0] == stickerOrderBefore && enum[1] == stickerOrderAfter
		}
		arguments := map[string]any{"content": response.Text}
		if p.silent {
			arguments = map[string]any{"content": "", "silent": true, "silent_reason": "一张图就够了"}
		}
		if p.order != "" {
			arguments[stickerOrderFieldName] = p.order
		}
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
		rt := NewRuntime(BotConfig{AgentEnabled: true, StickerReplyOrder: StickerReplyOrderAfter}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
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
			if len(sent) != 1 || sentStickerPath(sent[0]) != "" {
				t.Fatalf("no keywords: sent = %#v", sent)
			}
			continue
		}
		if len(sent) != 2 || sent[0].Text == "" || sentStickerPath(sent[1]) != path {
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
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "a", EventTime: 2, Summary: "动画表情", Path: crude, ContentSHA256: imageBytesSHA256([]byte("crude")), Tagged: true, Description: "大叔猥琐地说“晚安宝贝来我被窝”"},
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "b", EventTime: 1, Summary: "动画表情", Path: cute, ContentSHA256: imageBytesSHA256([]byte("cute")), Tagged: true, Description: "小猫抱着枕头说“晚安”"},
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
	if got := channel.sentSnapshot(); len(got) != 1 || sentStickerPath(got[0]) != cute {
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

// 没按多帧标注过的候选（以前只看第一帧的 GIF），自动配图前先当场重看；重看后关键词对不上就不发。
func TestStickerSendBestMatchReannotatesStaleCandidate(t *testing.T) {
	path, hash := writeRecallImageFixture(t)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", MessageID: "m"}
	store := &stickerPersonaTestStore{stickerAssetTestStore: stickerAssetTestStore{stickerHistoryStore: stickerHistoryStore{events: map[string][]MessageEvent{}}, assets: []StickerAsset{
		{Session: sessionKey(event), Kind: EventKindGroup, GroupID: "g", MessageID: "a", EventTime: 1, Summary: "动画表情", Path: path, ContentSHA256: hash, Description: "人物侧卧在床上闭着眼睛，像在睡觉"},
	}}, verdicts: map[string]bool{}}
	channel := &recordingChannel{}
	vision := &stickerReannotateProvider{annotation: "角色趴在床上一直扭动身体，带点暗示。 标签：趴着、扭动、床"}
	rt := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return vision, nil })
	rt.SetMessageHistoryStore(store)
	tool := newDianaStickerTool(rt, event, nil)
	sent, err := tool.sendBestMatch(context.Background(), "晚安 睡觉")
	if err != nil || sent || len(channel.sentSnapshot()) != 0 {
		t.Fatalf("stale sticker sent after re-annotation: sent=%v err=%v", sent, err)
	}
	if tags, ok := store.taggedSnapshot(hash); !ok || strings.Join(tags, "|") != "趴着|扭动|床" {
		t.Fatalf("re-annotation not saved: %v %v", tags, ok)
	}
	if vision.annotations != 1 {
		t.Fatalf("annotations = %d", vision.annotations)
	}
}

// stickerReannotateProvider 对表情包标注请求回固定标注，对人设判断回「会」。
type stickerReannotateProvider struct {
	annotation  string
	annotations int
}

func (p *stickerReannotateProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	last := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(last, "合不合你的人设") {
		return &llm.GenerateResponse{Text: "会"}, nil
	}
	p.annotations++
	return &llm.GenerateResponse{Text: p.annotation}, nil
}

// 一张图就够、不想说话：静默收尾加 sticker 只回一张表情包；挑不到就什么都不发，也不补兜底文字。
func TestReplySilentFinalizeSendsStickerOnly(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "night.gif")
	body := []byte("night-sticker")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, configOrder := range []StickerReplyOrder{StickerReplyOrderBefore, StickerReplyOrderAfter, StickerReplyOrderAuto} {
		for _, keywords := range []string{"晚安 摸头", "斗图 翻白眼"} {
			channel := &recordingChannel{}
			provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "晚安"}, sticker: keywords, silent: true, personaVerdict: "会"}
			rt := NewRuntime(BotConfig{AgentEnabled: true, StickerReplyOrder: configOrder}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "night-" + keywords, RawMessage: "晚安啦"}
			rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{
				Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1,
				Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[晚安]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}},
			}}}})
			_, err := rt.replyTo(context.Background(), event, event.RawMessage)
			var silent *modelSilentFinishError
			if !errors.As(err, &silent) {
				t.Fatalf("%s: err = %v, want silent finish", keywords, err)
			}
			sent := channel.sentSnapshot()
			if keywords == "晚安 摸头" {
				if len(sent) != 1 || sent[0].Text != "" || sentStickerPath(sent[0]) != path {
					t.Fatalf("sticker-only: sent = %#v", sent)
				}
				continue
			}
			if len(sent) != 0 {
				t.Fatalf("no match must stay silent: sent = %#v", sent)
			}
		}
	}
}

// 固定配置覆盖模型选择；auto 保留模型缺省 after 的行为。
func TestReplyFinalizeStickerOrderFollowsModelChoice(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "shock.gif")
	body := []byte("shock-sticker")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, configOrder := range []StickerReplyOrder{"", "invalid", StickerReplyOrderBefore, StickerReplyOrderAfter, StickerReplyOrderAuto} {
		for _, order := range []string{"before", "after", "", "invalid"} {
			channel := &recordingChannel{}
			provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "真的假的，你居然一次过了"}, sticker: "震惊 瞪眼", order: order, personaVerdict: "会"}
			rt := NewRuntime(BotConfig{AgentEnabled: true, StickerReplyOrder: configOrder}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "order-" + order, RawMessage: "我驾照一次过了"}
			rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{
				Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1,
				Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[震惊]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}},
			}}}})
			if _, err := rt.replyTo(context.Background(), event, event.RawMessage); err != nil {
				t.Fatal(err)
			}
			if !provider.sawOrder {
				t.Fatal("agent_finalize did not offer sticker_order as a before/after enum")
			}
			sent := channel.sentSnapshot()
			if len(sent) != 2 {
				t.Fatalf("order=%q sent = %#v", order, sent)
			}
			stickerIndex, textIndex := 1, 0
			if configOrder != StickerReplyOrderAfter && (configOrder != StickerReplyOrderAuto || order == "before") {
				stickerIndex, textIndex = 0, 1
			}
			if sentStickerPath(sent[stickerIndex]) != path || sent[textIndex].Text == "" {
				t.Fatalf("order=%q sent = %#v", order, sent)
			}
		}
	}
}

func TestStickerReplyOrderConfig(t *testing.T) {
	if DefaultBotConfig().StickerReplyOrder != StickerReplyOrderBefore {
		t.Fatal("default must be before")
	}
	for _, order := range []StickerReplyOrder{"", "invalid", " BEFORE ", StickerReplyOrderBefore, StickerReplyOrderAfter, StickerReplyOrderAuto} {
		want := order
		if order != StickerReplyOrderAfter && order != StickerReplyOrderAuto {
			want = StickerReplyOrderBefore
		}
		cfg := BotConfig{ID: "a", StickerReplyOrder: order}.WithDefaults()
		if cfg.StickerReplyOrder != want {
			t.Fatalf("%q: got %q want %q", order, cfg.StickerReplyOrder, want)
		}
		payload := PayloadFromConfig(cfg)
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ConfigPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if got := ConfigFromPayload(decoded, BotConfig{}).StickerReplyOrder; got != want {
			t.Fatalf("roundtrip %q: %q", order, got)
		}
		if got := ConfigFromPayload(ConfigPayload{StickerReplyOrder: order}, BotConfig{StickerReplyOrder: StickerReplyOrderAfter}).StickerReplyOrder; got != want {
			t.Fatalf("payload %q: %q", order, got)
		}
	}
	var legacy BotConfig
	if err := json.Unmarshal([]byte(`{"id":"legacy"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.WithDefaults().StickerReplyOrder != StickerReplyOrderBefore {
		t.Fatal("legacy config must default before")
	}
	set := ProfileSet{Profiles: []BotConfig{{ID: "a", StickerReplyOrder: StickerReplyOrderBefore}, {ID: "b", StickerReplyOrder: StickerReplyOrderAfter}, {ID: "c", StickerReplyOrder: StickerReplyOrderAuto}}}.WithDefaults()
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var restored ProfileSet
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	payload := PayloadFromProfileSet(restored, "b")
	if payload.StickerReplyOrder != StickerReplyOrderAfter {
		t.Fatal("wrong focus profile")
	}
	for i, cfg := range set.Profiles {
		if payload.Profiles[i].StickerReplyOrder != cfg.StickerReplyOrder {
			t.Fatal("profile order leaked")
		}
	}
}

func TestStickerReplyOrderProfilePrompt(t *testing.T) {
	a := BotConfig{ID: "a", StickerReplyOrder: StickerReplyOrderBefore}
	b := BotConfig{ID: "b", StickerReplyOrder: StickerReplyOrderAfter}
	c := BotConfig{ID: "c", StickerReplyOrder: StickerReplyOrderAuto}
	rt := twoBotRuntime(t, a, b)
	rt.SetProfiles(ProfileSet{Profiles: []BotConfig{a, b, c}})
	for _, cfg := range []BotConfig{a, b, c} {
		event := MessageEvent{ProfileID: cfg.ID, Kind: EventKindPrivate, UserID: "10001"}
		if got := rt.effectiveConfigForEvent(event).StickerReplyOrder; got != cfg.StickerReplyOrder {
			t.Fatalf("profile %s = %q", cfg.ID, got)
		}
		prompt := rt.systemPromptWithRelationshipAndAgent(event, nil, false, RelationshipPolicy{}, true)
		if cfg.StickerReplyOrder == StickerReplyOrderAuto {
			if strings.Contains(prompt, "表情包顺序已固定为") || !strings.Contains(prompt, "对别人这句话的反应") || !strings.Contains(prompt, stickerReplyOrderPrompt(StickerReplyOrderAuto)) {
				t.Fatal("auto guidance changed")
			}
		} else if !strings.Contains(prompt, stickerReplyOrderPrompt(cfg.StickerReplyOrder)) {
			t.Fatalf("profile %s missing fixed rule", cfg.ID)
		}
	}
}

func TestReplyFinalizeStickerUnavailableDoesNotBlockText(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "shock.gif")
	body := []byte("shock-sticker")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, order := range []StickerReplyOrder{StickerReplyOrderBefore, StickerReplyOrderAfter, StickerReplyOrderAuto} {
		for _, scenario := range []string{"failure", "disabled", "no_match", "cancel"} {
			t.Run(string(order)+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				channel := &stickerFailChannel{fail: scenario == "failure"}
				if scenario == "cancel" {
					channel.cancel = cancel
				}
				keywords := "震惊 瞪眼"
				if scenario == "no_match" {
					keywords = "晚安 摸头"
				}
				provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "真的假的，你居然一次过了"}, sticker: keywords, order: "before", personaVerdict: "会"}
				plugins := NewDefaultPluginManager()
				if scenario == "disabled" {
					if _, err := plugins.SetEnabledForProfile(stickerPluginID, "test-bot", false); err != nil {
						t.Fatal(err)
					}
				}
				rt := NewRuntime(BotConfig{ID: "test-bot", AgentEnabled: true, StickerReplyOrder: order}.WithDefaults(), channel, plugins, nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
				event := MessageEvent{ProfileID: "test-bot", Kind: EventKindPrivate, UserID: "10001", MessageID: "unavailable", RawMessage: "我驾照一次过了"}
				rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[震惊]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}}}}}})
				_, err := rt.replyTo(ctx, event, event.RawMessage)
				if scenario == "cancel" && order != StickerReplyOrderAfter {
					if !errors.Is(err, context.Canceled) || len(channel.sentSnapshot()) != 0 {
						t.Fatalf("cancel: err=%v sent=%#v", err, channel.sentSnapshot())
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				sent := channel.sentSnapshot()
				if len(sent) != 1 || sent[0].Text == "" || sentStickerPath(sent[0]) != "" {
					t.Fatalf("sent=%#v", sent)
				}
			})
		}
	}
}

func TestReplyFinalizeStickerBeforeBudget(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "shock.gif")
	body := []byte("shock-sticker")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"child_timeout", "reserved_deadline", "insufficient_budget", "parent_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			timeout := 20 * time.Second
			if scenario == "reserved_deadline" {
				timeout = stickerReplySendReserve + time.Second
			} else if scenario == "insufficient_budget" {
				timeout = stickerReplySendReserve
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			channel := &recordingChannel{}
			var checked atomic.Bool
			provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "真的假的，你居然一次过了"}, sticker: "震惊 瞪眼", order: "after"}
			provider.personaCheck = func(checkCtx context.Context) error {
				checked.Store(true)
				deadline, ok := checkCtx.Deadline()
				parentDeadline, _ := ctx.Deadline()
				if !ok || time.Until(deadline) > stickerBeforeReplyTimeout || parentDeadline.Sub(deadline) < stickerReplySendReserve {
					t.Errorf("invalid sticker deadline: %v parent: %v", deadline, parentDeadline)
				}
				if scenario == "parent_cancel" {
					cancel()
				}
				<-checkCtx.Done()
				return checkCtx.Err()
			}
			rt := NewRuntime(BotConfig{AgentEnabled: true, StickerReplyOrder: StickerReplyOrderBefore}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "budget", RawMessage: "我驾照一次过了"}
			rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[震惊]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}}}}}})
			_, err := rt.replyTo(ctx, event, event.RawMessage)
			if checked.Load() != (scenario != "insufficient_budget") {
				t.Fatalf("persona checked = %v", checked.Load())
			}
			sent := channel.sentSnapshot()
			if scenario == "parent_cancel" {
				if !errors.Is(err, context.Canceled) || len(sent) != 0 {
					t.Fatalf("parent cancellation: err=%v sent=%#v", err, sent)
				}
				return
			}
			if err != nil || ctx.Err() != nil || len(sent) != 1 || sent[0].Text == "" || sentStickerPath(sent[0]) != "" {
				t.Fatalf("text should survive optional sticker timeout: err=%v parent=%v sent=%#v", err, ctx.Err(), sent)
			}
		})
	}
}

func TestReplyFinalizeStickerPreparationDoesNotHoldText(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "shock.gif")
	body := []byte("shock-sticker")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"ignores_cancel", "parent_cancel", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), stickerReplySendReserve+time.Second)
			defer cancel()
			entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			channel := &recordingChannel{}
			provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "真的假的，你居然一次过了"}, sticker: "震惊", personaVerdict: "会"}
			provider.personaCheck = func(context.Context) error {
				defer close(exited)
				close(entered)
				if scenario == "panic" {
					panic("sticker preparation test")
				}
				<-release // 故意不理会 context，模拟不合作的审核客户端。
				return nil
			}
			rt := NewRuntime(BotConfig{AgentEnabled: true}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "blocked", RawMessage: "我驾照一次过了"}
			rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[震惊]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}}}}}})
			done := make(chan error, 1)
			go func() {
				_, err := rt.replyTo(ctx, event, event.RawMessage)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("preparation did not start")
			}
			if scenario == "parent_cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if scenario == "parent_cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel error = %v", err)
					}
				} else if err != nil || ctx.Err() != nil {
					t.Fatalf("reply should finish with live parent: %v / %v", err, ctx.Err())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("preparation blocked reply")
			}
			want := 1
			if scenario == "parent_cancel" {
				want = 0
			}
			if sent := channel.sentSnapshot(); len(sent) != want || (want == 1 && (sent[0].Text == "" || sentStickerPath(sent[0]) != "")) {
				t.Fatalf("sent before releasing worker = %#v", sent)
			}
			if scenario != "panic" {
				release <- struct{}{}
			}
			<-exited
			if sent := channel.sentSnapshot(); len(sent) != want {
				t.Fatalf("late preparation sent an extra message: %#v", sent)
			}
		})
	}
}

func TestStickerDeliverPreservesUncertainQuota(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "sticker.gif")
	if err := os.WriteFile(path, []byte("sticker"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, sendErr := range []error{ErrOutboundOutcomeUnknown, errOutboundOutcomeUnconfirmed, errors.New("definite failure")} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			channel := &stickerDeliveryBoundaryChannel{sendErr: sendErr}
			rt := NewRuntime(BotConfig{}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, nil)
			tool := newDianaStickerTool(rt, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := tool.deliver(ctx, stickerCandidate{Path: path}); err == nil {
				t.Fatal("delivery error lost")
			}
			wantReserved := sendErr == ErrOutboundOutcomeUnknown || sendErr == errOutboundOutcomeUnconfirmed
			if reserved := tool.sendLimitReason(time.Now()) != ""; reserved != wantReserved {
				t.Fatalf("reserved=%v want=%v", reserved, wantReserved)
			}
		})
	}
}

func TestStickerPreparationDeadlineDoesNotCancelDelivery(t *testing.T) {
	withFastSendTiming(t)
	path := filepath.Join(t.TempDir(), "shock.gif")
	body := []byte("shock-sticker")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stickerReplySendReserve+time.Second)
	defer cancel()
	var preparationDeadline time.Time
	channel := &stickerDeliveryBoundaryChannel{}
	channel.onSticker = func(sendCtx context.Context) error {
		deadline, _ := sendCtx.Deadline()
		parentDeadline, _ := ctx.Deadline()
		if !deadline.Equal(parentDeadline) {
			t.Errorf("delivery deadline=%v want parent=%v", deadline, parentDeadline)
		}
		timer := time.NewTimer(time.Until(preparationDeadline) + 30*time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			return sendCtx.Err()
		case <-sendCtx.Done():
			return sendCtx.Err()
		}
	}
	provider := &stickerFinalizeLLMProvider{capturingLLMProvider: capturingLLMProvider{reply: "真的假的，你居然一次过了"}, sticker: "震惊", personaVerdict: "会"}
	provider.personaCheck = func(checkCtx context.Context) error {
		preparationDeadline, _ = checkCtx.Deadline()
		return nil
	}
	rt := NewRuntime(BotConfig{AgentEnabled: true}.WithDefaults(), channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10001", MessageID: "delivery-budget", RawMessage: "我驾照一次过了"}
	rt.SetMessageHistoryStore(&stickerHistoryStore{events: map[string][]MessageEvent{sessionKey(event): {{Kind: EventKindPrivate, UserID: "10001", MessageID: "s", Time: 1, Segments: []MessageSegment{{Type: "image", Data: map[string]string{"summary": "[震惊]", "cached_file": path, imageContentSHA256Key: imageBytesSHA256(body)}}}}}}})
	if _, err := rt.replyTo(ctx, event, event.RawMessage); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 || sentStickerPath(sent[0]) == "" || sent[1].Text == "" {
		t.Fatalf("delivery must finish before text: %#v", sent)
	}
}

type stickerDeliveryBoundaryChannel struct {
	recordingChannel
	sendErr   error
	onSticker func(context.Context) error
}

func (c *stickerDeliveryBoundaryChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if sentStickerPath(msg) != "" {
		if c.sendErr != nil {
			return nil, c.sendErr
		}
		if c.onSticker != nil {
			if err := c.onSticker(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err := c.recordingChannel.Send(ctx, msg); err != nil {
		return nil, err
	}
	return map[string]any{"message_id": "sent"}, nil
}

type stickerFailChannel struct {
	recordingChannel
	fail   bool
	cancel context.CancelFunc
}

func (c *stickerFailChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	if sentStickerPath(msg) != "" {
		if c.cancel != nil {
			c.cancel()
			return context.Canceled
		}
		if c.fail {
			return errors.New("sticker unavailable")
		}
	}
	return c.recordingChannel.Send(ctx, msg)
}
func (c *stickerFailChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if err := c.Send(ctx, msg); err != nil {
		return nil, err
	}
	return map[string]any{"message_id": "sent"}, nil
}
