// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// decisionOnlyClient 复刻 TypeSafe System One 的行为：不生成文本，只回答带类型的题。
type decisionOnlyClient struct {
	got *llm.GenerateRequest
}

func (c decisionOnlyClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	if req.Decision == nil || len(req.Decision.Questions) == 0 {
		return nil, llm.ErrDecisionRequired
	}
	if c.got != nil {
		*c.got = req
	}
	return &llm.GenerateResponse{Provider: llm.ProviderTypeSafe, Model: req.Model, Text: `{"addressed":true,"confidence":0.93}`}, nil
}

// 判断模型发一句 ping 必然被挡，而且是在出网之前——界面上看起来像"连不通"，
// 其实链路一次都没试过。连通测试要改成问它一道真题。
func TestLLMTestFallsBackToDecisionProbe(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderTypeSafe,
		APIKey:   "test-key",
		BaseURL:  "https://api.typesafe.ai",
		Model:    "jev-latest",
	})
	var got llm.GenerateRequest
	handler := NewLLMConfigHandlerWithFactory(store, func(llm.ProviderConfig) (llm.LLMClient, error) {
		return decisionOnlyClient{got: &got}, nil
	})
	router := testRouter(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader([]byte(`{"message":"美海在吗"}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got.Decision == nil || len(got.Decision.Questions) != 1 {
		t.Fatalf("没有改发判断题：%#v", got.Decision)
	}
	if question := got.Decision.Questions[0]; question.Kind != llm.DecisionNoul || question.Path == "" || question.ReasonPath == "" {
		t.Fatalf("判断题不完整：%#v", question)
	}
	// 用户填的那句话要进题目，否则测的不是他关心的那条消息。
	found := false
	for _, message := range got.Messages {
		if message.Role == llm.RoleUser && message.Content == "美海在吗" {
			found = true
		}
	}
	if !found {
		t.Fatalf("测试消息没带进判断题：%#v", got.Messages)
	}
	var resp llm.GenerateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Fatalf("response = %#v", resp)
	}
}

// 分组名是用户自己起的，线上那档就叫「生图」。要测的模型等于这套配置的生图模型
// 时必须按生图测，否则拿生图模型去跑文本测试，必然失败。
func TestLLMTestPicksImageModeByModelNotGroupName(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "saved-key",
		Model:      "gpt-5.6-sol",
		ImageModel: "gpt-image-2",
	})
	var gotRequest llm.ImageGenerateRequest
	handler := NewLLMConfigHandlerWithFactory(store, func(llm.ProviderConfig) (llm.LLMClient, error) {
		return fakeImageLLMClient{request: &gotRequest}, nil
	})
	router := testRouter(handler)

	body := []byte(`{"message":"画只猫","id":"` + store.Profiles().Profiles[0].ID + `","group":"生图","provider":"openai_compatible","model":"gpt-image-2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotRequest.Model != "gpt-image-2" || gotRequest.Prompt != "画只猫" {
		t.Fatalf("image request = %#v", gotRequest)
	}
}

// 文本模型不能被误判成生图：同一套配置里 model 和 image_model 不一样时照常走文本。
func TestLLMTestKeepsTextModeForTextModel(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "saved-key",
		Model:      "gpt-5.6-sol",
		ImageModel: "gpt-image-2",
	})
	handler := NewLLMConfigHandlerWithFactory(store, func(llm.ProviderConfig) (llm.LLMClient, error) {
		return fakeLLMClient{}, nil
	})
	router := testRouter(handler)

	body := []byte(`{"message":"hello","id":"` + store.Profiles().Profiles[0].ID + `","group":"生图","provider":"openai_compatible","model":"gpt-5.6-sol"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/llm/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
