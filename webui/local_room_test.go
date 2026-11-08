// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// fixedPlanProvider 对所有调用都回同一段文字。
type fixedPlanProvider struct{ text string }

func (p fixedPlanProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "test", Text: p.text}, nil
}

func newLocalRoomTest(t *testing.T, plan string) (*gin.Engine, *BotHandler, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	template := assistant.BotConfig{ID: "main", Name: "嘉然", Platform: assistant.PlatformLocal, Enabled: true, OwnerID: "10001", ModelRoles: map[string]assistant.ModelRole{"chat": {ProfileID: "fast", Model: "fast-model"}}}
	r := assistant.NewRuntime(template, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, func() (assistant.LLMProvider, error) {
		return fixedPlanProvider{text: plan}, nil
	})
	h := NewBotHandlerWithFactory(context.Background(), r, func(cfg assistant.BotConfig) assistant.Channel {
		return assistant.NewChannelForConfig(cfg)
	})
	h.SetProfileStore(NewMemoryBotProfileStoreFromSet(assistant.ProfileSet{Profiles: []assistant.BotConfig{template}}))
	m := NewAuthManager(&memoryAuthStore{})
	if _, err := m.Bootstrap("", "test-password"); err != nil {
		t.Fatal(err)
	}
	token, err := m.IssueSession()
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(m.Middleware())
	h.Register(router)
	return router, h, token
}

// 一句话建群：建出带人设的内置机器人（模型分配照模板），再建群拉进来。
func TestLocalRoomPlanCreatesBotsAndRoom(t *testing.T) {
	plan := `{"room_name":"结束乐队","bots":[{"name":"虹夏","persona":"你是乐队鼓手，开朗爱操心"},{"name":"凉","persona":"你是贝斯手，话少爱省钱"}]}`
	router, h, token := newLocalRoomTest(t, plan)
	if w := adminChatRequest(router, "POST", "/api/assistant/rooms/plan", "", `{"text":"建两个机器人"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("没登录也能建群：%d", w.Code)
	}
	w := adminChatRequest(router, "POST", "/api/assistant/rooms/plan", token, `{"text":"建两个机器人：虹夏和凉，组个乐队群"}`)
	var body struct {
		Room assistant.LocalRoom `json:"room"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("plan: %d %s", w.Code, w.Body.String())
	}
	if body.Room.Name != "结束乐队" || len(body.Room.Members) != 2 {
		t.Fatalf("room = %#v", body.Room)
	}
	created := map[string]assistant.BotConfig{}
	for _, profile := range h.profiles.Profiles().Profiles {
		created[profile.ID] = profile
	}
	for _, id := range body.Room.Members {
		bot, ok := created[id]
		if !ok {
			t.Fatalf("成员 %s 没建出来", id)
		}
		if bot.Platform != assistant.PlatformLocal || bot.SystemPrompt == "" || bot.ModelRoles["chat"].Model != "fast-model" || bot.OwnerID != "" {
			t.Fatalf("新机器人不对：%#v", bot)
		}
	}
	w = adminChatRequest(router, "GET", "/api/assistant/rooms", token, "")
	var list struct {
		Rooms []assistant.LocalRoom `json:"rooms"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Rooms) != 1 {
		t.Fatalf("rooms: %d %s", w.Code, w.Body.String())
	}
}

func TestLocalRoomCrudAndStop(t *testing.T) {
	router, _, token := newLocalRoomTest(t, "")
	w := adminChatRequest(router, "POST", "/api/assistant/rooms", token, `{"name":"测试群","members":["main"],"max_bot_turns":3}`)
	var body struct {
		Room assistant.LocalRoom `json:"room"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Room.MaxBotTurns != 3 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id := body.Room.ID
	if w := adminChatRequest(router, "PUT", "/api/assistant/rooms/"+id, token, `{"name":"改名了","members":["main"]}`); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/rooms/"+id+"/stop", token, ""); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	if w := adminChatRequest(router, "POST", "/api/assistant/rooms", token, `{"name":"空群","members":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("没有成员的群不该建出来：%d", w.Code)
	}
	if w := adminChatRequest(router, "DELETE", "/api/assistant/rooms/"+id, token, ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := adminChatRequest(router, "GET", "/api/assistant/rooms/"+id, token, ""); w.Code != http.StatusNotFound {
		t.Fatalf("解散后还能读：%d", w.Code)
	}
}
