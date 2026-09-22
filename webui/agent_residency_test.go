package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

type residencyTestRuntime struct {
	BotRuntime
	profile  string
	id       string
	resident *bool
	saved    bool
	list     []string
	listSet  bool
}

func (r *residencyTestRuntime) AgentResidency(profileID string) ([]assistant.AgentResidencyEntry, bool) {
	r.profile = profileID
	return []assistant.AgentResidencyEntry{{ID: "tool:poke", Kind: "tool", Name: "poke", Default: true}}, true
}

func (r *residencyTestRuntime) SetAgentResidency(profileID, id string, resident *bool) error {
	r.profile, r.id, r.resident, r.saved = profileID, id, resident, true
	return nil
}

func (r *residencyTestRuntime) SaveAgentResidencyList(profileID string, ids []string) error {
	r.profile, r.list, r.listSet = profileID, ids, true
	return nil
}

func TestAgentResidencyListsAndSavesTiers(t *testing.T) {
	r := &residencyTestRuntime{BotRuntime: assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)}
	router := botTestRouter(NewBotHandler(context.Background(), r))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-residency?profile=bot-a", nil))
	if response.Code != http.StatusOK || r.profile != "bot-a" {
		t.Fatalf("list status %d profile %q", response.Code, r.profile)
	}
	var listed struct {
		Items  []assistant.AgentResidencyEntry `json:"items"`
		Listed bool                            `json:"listed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != "tool:poke" || !listed.Items[0].Default || !listed.Listed {
		t.Fatalf("items = %#v listed=%v", listed.Items, listed.Listed)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/agent-residency", strings.NewReader(`{"profile_id":"bot-a","id":"tool:poke","resident":false}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !r.saved || r.id != "tool:poke" || r.resident == nil || *r.resident {
		t.Fatalf("save status %d %+v", response.Code, r)
	}

	// 界面存的是整份名单：一次写完，不会留下「插件进去了、排除的那个还没拿掉」这种中间态。
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/assistant/agent-residency", strings.NewReader(`{"profile_id":"bot-a","ids":["tool:poke","mcp:gitea"]}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !r.listSet || strings.Join(r.list, ",") != "tool:poke,mcp:gitea" {
		t.Fatalf("list save status %d %+v", response.Code, r)
	}

	// 空名单是「一个都不常驻」，不是「没配过」：ids 给了空数组就不能被当成没传。
	r.list, r.listSet = nil, false
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/assistant/agent-residency", strings.NewReader(`{"profile_id":"bot-a","ids":[]}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !r.listSet || r.list == nil || len(r.list) != 0 {
		t.Fatalf("empty list did not reach the runtime: %d %+v", response.Code, r)
	}

	// 退回推荐名单要传 nil，才会把这台机器人的名单整个删掉、重新跟着版本走。
	r.list, r.listSet = []string{"x"}, false
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/assistant/agent-residency", strings.NewReader(`{"profile_id":"bot-a","reset":true}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !r.listSet || r.list != nil {
		t.Fatalf("reset did not clear the list: %d %+v", response.Code, r)
	}
}

func TestAgentResidencyRejectsUnavailableRuntime(t *testing.T) {
	r := assistant.NewRuntime(assistant.BotConfig{}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	router := botTestRouter(NewBotHandler(context.Background(), struct{ BotRuntime }{r}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/agent-residency", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", response.Code)
	}
}
