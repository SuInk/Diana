// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

func TestDianaImageAgentToolGeneratesFromResolvedPrompt(t *testing.T) {
	var submittedPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		submittedPrompt = body.Prompt
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"c2VhcmNoLWRlcml2ZWQtaW1hZ2U="}]}`))
	}))
	defer server.Close()

	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info": {
			"group_id":   "20005",
			"group_name": "测试群",
		},
		"get_group_member_info": {
			"group_id": "20005",
			"user_id":  "10001",
			"nickname": "Alice",
		},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), store, nil, nil, nil)
	mediaCache := mediaStore(t)
	runtime.SetMediaStore(mediaCache)
	sharer := &recordingLocalMediaSharer{url: server.URL + "/media"}
	runtime.SetLocalMediaSharer(sharer)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "agent-image"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := newDianaImageTool(runtime, event, policy)

	raw, err := tool.Run(context.Background(), map[string]any{
		"prompt":  "官方检索结果确认主色为靛蓝 #4B0082 与金色 #FFD700；据此创作一张平面海报。",
		"caption": "按检索结果画好了。",
		"size":    "1024x1024",
	})
	if err != nil {
		t.Fatal(err)
	}
	var queued dianaImageToolResult
	if err := json.Unmarshal([]byte(raw), &queued); err != nil {
		t.Fatalf("queued result = %q: %v", raw, err)
	}
	if !queued.OK || !queued.Queued || !strings.HasPrefix(queued.TaskID, "img-") || queued.Action != "generate" {
		t.Fatalf("queued result = %#v", queued)
	}
	if _, ok := tool.(agent.TerminalResultTool); ok {
		t.Fatal("diana.image must let the agent continue to its final text reply")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return runtime.activeSubagentTaskCount() == 0
	})
	if !strings.Contains(submittedPrompt, "#4B0082") || !strings.Contains(submittedPrompt, "#FFD700") || !strings.Contains(submittedPrompt, "群聊：测试群") {
		t.Fatalf("submitted prompt = %q", submittedPrompt)
	}
	sharedPaths := sharer.pathsSnapshot()
	if len(sharedPaths) != 1 {
		t.Fatalf("shared paths = %#v", sharedPaths)
	}
	if filepath.Dir(sharedPaths[0]) != mediaCache.Dir() {
		t.Fatalf("generated image did not use media cache: %q", sharedPaths[0])
	}
	data, err := os.ReadFile(sharedPaths[0])
	if err != nil || string(data) != "search-derived-image" {
		t.Fatalf("shared image = %q, err = %v", data, err)
	}
	// 第一条是运行时替模型发的「开始处理」，第二条才是图片结果。开场白以前留给模型
	// 自己在 final 里写，写不写、写成什么样都不保证。
	sent := channel.sentSnapshot()
	if len(sent) != 2 {
		t.Fatalf("sent = %#v", sent)
	}
	if !strings.Contains(sent[0].Text, "开始生成图片") || strings.Contains(sent[0].Text, "任务编号") {
		t.Fatalf("start announcement = %#v", sent[0])
	}
	if sent[1].Text != "按检索结果画好了。" || len(sent[1].ImageURLs) != 1 || sent[1].ImageURLs[0] != sharer.url {
		t.Fatalf("sent = %#v", sent)
	}
	var loggedPrompt string
	imageLogFound := false
	entries := logs.entriesSnapshot()
	for _, entry := range entries {
		if entry.Action == "chatbot.image.generate" {
			loggedPrompt, _ = entry.Metadata["prompt"].(string)
			imageLogFound = true
			break
		}
	}
	if !imageLogFound {
		t.Fatalf("logs = %#v", entries)
	}
	if !strings.Contains(loggedPrompt, "#4B0082") || !strings.Contains(loggedPrompt, "群聊：测试群") {
		t.Fatalf("logged prompt = %q", loggedPrompt)
	}
}

func TestDianaImageAgentToolEnforcesRelationshipPermissions(t *testing.T) {
	initial := RelationshipPolicyFor(UserMemoryProfile{}, "owner", "user")
	if !initial.allowedAgentToolNames()[dianaImageToolName] {
		t.Fatalf("initial allowlist hid permission-checked image tool: %#v", initial.allowedAgentToolNames())
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaImageTool(runtime, MessageEvent{UserID: "user"}, initial)
	for _, operation := range []string{"generate", "edit"} {
		_, err := tool.Run(context.Background(), map[string]any{
			"operation": operation,
			"prompt":    "测试",
		})
		if err == nil || strings.Contains(err.Error(), "好感度不足") {
			t.Fatalf("%s error = %v", operation, err)
		}
	}
	hostile := RelationshipPolicyFor(UserMemoryProfile{Favorability: -20}, "owner", "user")
	if !hostile.allowedAgentToolNames()[dianaImageToolName] {
		t.Fatalf("hostile allowlist hid permission-checked image tool: %#v", hostile.allowedAgentToolNames())
	}
}

// 用户只说了「生成图片」，模型没有别的可回时，开场白就是这一轮的回复——
// 而不是「我这边没有生成有效回复」，更不是一条都不发。
func TestImageAnnouncementBecomesReplyWhenModelSaysNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"YWdlbnQtaW1hZ2U="}]}`))
	}))
	defer server.Close()

	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		`{"action":"tool","tool":"diana.image","input":{"operation":"generate","prompt":"一只在窗台上打盹的猫"}}`,
		`{"action":"final","task_state":"pending","content":""}`,
	}}
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		OwnerID:       "owner",
		AgentEnabled:  true,
		AgentMaxSteps: 4,
	}, channel, NewPluginManager(), store, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: server.URL + "/media"})
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "owner",
		MessageID:  "image-only",
		RawMessage: "生成图片",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "生成图片"}}},
	}

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "开始生成图片") {
		t.Fatalf("reply = %q", reply)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return runtime.activeSubagentTaskCount() == 0
	})
	sent := channel.sentSnapshot()
	texts := 0
	for _, message := range sent {
		if len(message.ImageURLs) == 0 {
			texts++
		}
	}
	if texts != 1 {
		t.Fatalf("发图前的文字应当恰好一条: %#v", sent)
	}
}

func TestRuntimeAgentSearchesBeforeGeneratingImage(t *testing.T) {
	var submittedPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		submittedPrompt = body.Prompt
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"YWdlbnQtaW1hZ2U="}]}`))
	}))
	defer server.Close()

	search := &recordingAgentSearchTool{result: "检索确认：官方资料使用靛蓝 #4B0082 与金色 #FFD700。"}
	plugins := NewPluginManager(&agentImageSearchPlugin{tool: search})
	provider := &sequenceLLMProvider{replies: []string{
		`{"action":"none","prompt":""}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"官方主题配色"}}`,
		`{"action":"tool","tool":"diana.image","input":{"operation":"generate","prompt":"根据已核验的官方资料创作平面海报，主色严格使用靛蓝 #4B0082 与金色 #FFD700，简洁几何构图，不添加文字。","caption":"按查到的官方配色画好了。"}}`,
		`{"action":"final","task_state":"pending","content":"文字说明先发给你，图片完成后会自动补上。"}`,
	}}
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{
		OwnerID:       "owner",
		AgentEnabled:  true,
		AgentMaxSteps: 4,
	}, channel, plugins, store, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	mediaCache := mediaStore(t)
	runtime.SetMediaStore(mediaCache)
	sharer := &recordingLocalMediaSharer{url: server.URL + "/media"}
	runtime.SetLocalMediaSharer(sharer)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	event := MessageEvent{
		Kind:       EventKindPrivate,
		UserID:     "owner",
		MessageID:  "search-then-image",
		RawMessage: "先搜索官方主题配色，核验后根据结果生成一张海报",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "先搜索官方主题配色，核验后根据结果生成一张海报"}}},
	}

	reply, err := runtime.replyTo(context.Background(), event, event.RawMessage)
	if err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return runtime.activeSubagentTaskCount() == 0
	})
	if search.calls != 1 {
		t.Fatalf("search calls = %d", search.calls)
	}
	requests := provider.requestsSnapshot()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d", len(requests))
	}
	if !requestMessagesContain(requests[2].Messages, search.result) {
		t.Fatalf("image tool decision did not receive search result: %#v", requests[2].Messages)
	}
	if !requestMessagesContain(requests[1].Messages, "先完成搜索和必要的网页核验") || !requestMessagesContain(requests[1].Messages, dianaImageToolName) {
		t.Fatalf("agent prompt does not enforce search-before-image: %#v", requests[1].Messages)
	}
	if !strings.Contains(submittedPrompt, "#4B0082") || !strings.Contains(submittedPrompt, "#FFD700") {
		t.Fatalf("submitted prompt = %q", submittedPrompt)
	}
	if reply != "文字说明先发给你，图片完成后会自动补上" {
		t.Fatalf("reply = %q", reply)
	}
	sent := channel.sentSnapshot()
	// 发图之前只该有一条文字。模型自己交代了这一轮，运行时的开场白就不再补发——
	// 以前两条都发，用户连着看到两句几乎一样的话。
	if len(sent) != 2 {
		t.Fatalf("sent = %#v", sent)
	}
	for _, message := range sent {
		if strings.Contains(message.Text, "开始生成图片") {
			t.Fatalf("模型已经说过了，不该再补一条开场白: %#v", sent)
		}
	}
	textFound := false
	imageFound := false
	for _, message := range sent {
		if message.Text == reply && len(message.ImageURLs) == 0 {
			textFound = true
		}
		if message.Text == "按查到的官方配色画好了。" && len(message.ImageURLs) == 1 && message.ImageURLs[0] == sharer.url {
			imageFound = true
		}
	}
	if !textFound || !imageFound {
		t.Fatalf("sent = %#v", sent)
	}
	sharedPaths := sharer.pathsSnapshot()
	if len(sharedPaths) != 1 {
		t.Fatalf("shared paths = %#v", sharedPaths)
	}
	if filepath.Dir(sharedPaths[0]) != mediaCache.Dir() {
		t.Fatalf("generated image did not use media cache: %q", sharedPaths[0])
	}
	wantTargets := map[string]bool{"web_search.search": false, dianaImageToolName: false}
	imageLogFound := false
	entries := logs.entriesSnapshot()
	for _, entry := range entries {
		if entry.Action == "chatbot.agent_tool" {
			if _, ok := wantTargets[entry.Target]; ok {
				wantTargets[entry.Target] = true
			}
		}
		if entry.Action == "chatbot.image.generate" {
			imageLogFound = true
		}
	}
	if !wantTargets["web_search.search"] || !wantTargets[dianaImageToolName] || !imageLogFound {
		t.Fatalf("logs = %#v", entries)
	}
}

type recordingAgentSearchTool struct {
	result string
	calls  int
}

func (t *recordingAgentSearchTool) Name() string { return "web_search.search" }

func (t *recordingAgentSearchTool) Description() string {
	return `测试搜索工具。input: {"query":"搜索词"}`
}

func (t *recordingAgentSearchTool) Run(context.Context, map[string]any) (string, error) {
	t.calls++
	return t.result, nil
}

type agentImageSearchPlugin struct {
	tool agent.Tool
}

func (p *agentImageSearchPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "test.agent-image-search", Name: "Agent image search test", BuiltIn: true}
}

func (p *agentImageSearchPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (p *agentImageSearchPlugin) AgentTools() []agent.Tool {
	if p == nil || p.tool == nil {
		return nil
	}
	return []agent.Tool{p.tool}
}

var _ agent.Tool = (*recordingAgentSearchTool)(nil)
var _ Plugin = (*agentImageSearchPlugin)(nil)
var _ AgentToolProviderPlugin = (*agentImageSearchPlugin)(nil)

func writeTestPNG(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nmock-image"))
}

// source_mode="each" 对每张参考图各做一次编辑，产出多张一起发。以前所有参考图被
// 塞进同一次请求（语义是合成一张），「把每个人的头像都改一下」这类请求做不出来。
func TestDianaImageToolEditsEachSourceSeparately(t *testing.T) {
	var (
		editMu    sync.Mutex
		editCalls [][]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if r.URL.Path != "/v1/images/edits" {
			t.Errorf("path = %q", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		names := make([]string, 0, 2)
		for _, headers := range r.MultipartForm.File {
			for _, header := range headers {
				names = append(names, header.Filename)
			}
		}
		editMu.Lock()
		editCalls = append(editCalls, names)
		editMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"ZWRpdGVkLWltYWdl"}]}`))
	}))
	defer server.Close()

	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: server.URL + "/media"})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "batch-edit",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": server.URL + "/media?a"}},
			{Type: "image", Data: map[string]string{"url": server.URL + "/media?b"}},
			{Type: "text", Data: map[string]string{"text": "这两张都改成赛博风"}},
		},
	}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := newDianaImageTool(runtime, event, policy)

	raw, err := tool.Run(context.Background(), map[string]any{
		"operation":   "edit",
		"prompt":      "改成赛博朋克风格，保持人物身份特征",
		"source_mode": "each",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaImageToolResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.TaskID == "" || !result.Announced {
		t.Fatalf("result = %#v", result)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		editMu.Lock()
		defer editMu.Unlock()
		return len(editCalls) == 2
	})

	editMu.Lock()
	calls := append([][]string(nil), editCalls...)
	editMu.Unlock()
	for _, names := range calls {
		if len(names) != 1 {
			t.Fatalf("each 模式下每次请求只应带一张参考图：%#v", calls)
		}
	}

	// 逐张模式改为完成一张发一张:应出现两条各带一张图的消息,而不是攒到
	// 最后一条消息里带两张图。
	waitForCondition(t, 5*time.Second, func() bool {
		count := 0
		for _, message := range channel.sentSnapshot() {
			if len(message.ImageURLs) == 1 {
				count++
			}
		}
		return count == 2
	})

	sent := channel.sentSnapshot()
	if !strings.Contains(sent[0].Text, "逐张编辑图片") {
		t.Fatalf("start announcement = %#v", sent[0])
	}
	firstImage := -1
	for index, message := range sent {
		if len(message.ImageURLs) == 2 {
			t.Fatalf("图片不该攒到一条消息里发:%#v", message)
		}
		if len(message.ImageURLs) == 1 && firstImage < 0 {
			firstImage = index
		}
		// 编号是内部标识,处理过程中的任何消息都不该带。
		if strings.Contains(message.Text, "任务 img-") || strings.Contains(message.Text, "任务编号") {
			t.Fatalf("消息里不该出现任务编号:%#v", message)
		}
	}
	if firstImage < 0 {
		t.Fatal("没有逐张发出的图片消息")
	}
	if sent[firstImage].ReplyMessageID != "batch-edit" {
		t.Fatalf("第一张应引用原消息:%#v", sent[firstImage])
	}
	// 全部成功时安静收尾:最后不再补一条「共 N 张」汇总。
	for _, message := range sent {
		if strings.Contains(message.Text, "共 2 张") {
			t.Fatalf("不该再有汇总消息:%#v", message)
		}
	}
}

// 标注数量和来源对不上时整组丢弃——错位的标注会把 A 的头像标成 B,
// 比没有标注更糟。
func TestDianaImageToolDiscardsMismatchedSourceLabels(t *testing.T) {
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible, APIKey: "secret", Model: "gpt-test", ImageModel: "gpt-image-2",
	})}, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "m1"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := &dianaImageTool{runtime: runtime, event: event, relationship: policy}

	request, err := tool.prepareRequest(map[string]any{
		"operation":        "edit",
		"prompt":           "加上旗帜元素",
		"source_mode":      "each",
		"identity_sources": []any{"member:10001", "member:10002"},
		"source_labels":    []any{"只有一个标注"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.SourceLabels != nil {
		t.Fatalf("数量不匹配的标注应整组丢弃:%#v", request.SourceLabels)
	}

	request, err = tool.prepareRequest(map[string]any{
		"operation":        "edit",
		"prompt":           "加上旗帜元素",
		"source_mode":      "each",
		"identity_sources": []any{"member:10001", "member:10002"},
		"source_labels":    []any{"Winter 的头像", "美海的头像"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.SourceLabels) != 2 || request.SourceLabels[0] != "Winter 的头像" {
		t.Fatalf("数量匹配的标注应保留:%#v", request.SourceLabels)
	}
}

// 默认仍是 combine：多张参考图交给同一次编辑，合成一张。逐张模式是新增能力，
// 不是把原来的行为改掉。
func TestDianaImageToolCombinesSourcesByDefault(t *testing.T) {
	var (
		editMu    sync.Mutex
		editCalls [][]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		names := make([]string, 0, 2)
		for _, headers := range r.MultipartForm.File {
			for _, header := range headers {
				names = append(names, header.Filename)
			}
		}
		editMu.Lock()
		editCalls = append(editCalls, names)
		editMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"ZWRpdGVkLWltYWdl"}]}`))
	}))
	defer server.Close()

	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: server.URL + "/media"})
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20005", UserID: "10001", MessageID: "combine-edit",
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": server.URL + "/media?a"}},
			{Type: "image", Data: map[string]string{"url": server.URL + "/media?b"}},
			{Type: "text", Data: map[string]string{"text": "把这两张合成一张"}},
		},
	}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "owner", event.UserID)
	tool := newDianaImageTool(runtime, event, policy)

	if _, err := tool.Run(context.Background(), map[string]any{
		"operation": "edit",
		"prompt":    "把两张图合成一张海报",
	}); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, 5*time.Second, func() bool {
		editMu.Lock()
		defer editMu.Unlock()
		return len(editCalls) == 1
	})
	editMu.Lock()
	calls := append([][]string(nil), editCalls...)
	editMu.Unlock()
	if len(calls) != 1 || len(calls[0]) != 2 {
		t.Fatalf("combine 模式应当把两张参考图交给同一次请求：%#v", calls)
	}
}
