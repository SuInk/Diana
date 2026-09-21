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
}

func (r *residencyTestRuntime) AgentResidency(profileID string) []assistant.AgentResidencyEntry {
	r.profile = profileID
	return []assistant.AgentResidencyEntry{{ID: "tool:poke", Kind: "tool", Name: "poke", Default: true}}
}

func (r *residencyTestRuntime) SetAgentResidency(profileID, id string, resident *bool) error {
	r.profile, r.id, r.resident, r.saved = profileID, id, resident, true
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
		Items []assistant.AgentResidencyEntry `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != "tool:poke" || !listed.Items[0].Default {
		t.Fatalf("items = %#v", listed.Items)
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/assistant/agent-residency", strings.NewReader(`{"profile_id":"bot-a","id":"tool:poke","resident":false}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !r.saved || r.id != "tool:poke" || r.resident == nil || *r.resident {
		t.Fatalf("save status %d %+v", response.Code, r)
	}

	// 不带 resident 就是「跟随默认」，必须原样传成 nil，否则默认档会被存成按需。
	r.resident, r.saved = nil, false
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/assistant/agent-residency", strings.NewReader(`{"profile_id":"bot-a","id":"tool:poke"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !r.saved || r.resident != nil {
		t.Fatalf("clearing the tier did not reach the runtime: %d %+v", response.Code, r)
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
