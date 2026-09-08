package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

type imageProvenanceTestStore struct{ records map[string]ImageModelRecord }

func (*imageProvenanceTestStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	return nil
}
func (*imageProvenanceTestStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}
func (s *imageProvenanceTestStore) SaveImageModelRecord(_ context.Context, scope string, record ImageModelRecord) error {
	s.records[scope+record.MessageID] = record
	return nil
}
func (s *imageProvenanceTestStore) LoadImageModelRecord(_ context.Context, scope, id string) (ImageModelRecord, bool, error) {
	r, ok := s.records[scope+id]
	return r, ok, nil
}

type imageProvenanceChannel struct {
	recordingChannel
	calls  int
	failAt int
}

func (c *imageProvenanceChannel) SendWithResult(_ context.Context, _ OutgoingMessage) (map[string]any, error) {
	c.calls++
	if c.calls == c.failAt {
		return nil, fmt.Errorf("test send failed")
	}
	return map[string]any{"message_id": fmt.Sprint(c.calls)}, nil
}

func TestImageModelsFollowTelegramImageReceiptsAndIgnoreFailedSends(t *testing.T) {
	store := &imageProvenanceTestStore{records: map[string]ImageModelRecord{}}
	channel := &imageProvenanceChannel{failAt: 4}
	r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(store)
	event := MessageEvent{Platform: PlatformTelegram, ProfileID: "bot-a", Kind: EventKindGroup, GroupID: "same", UserID: "user"}
	msg := OutgoingMessage{Platform: PlatformTelegram, Text: "caption", ImageURLs: []string{"a", "b", "c"}, GeneratedImageModels: []GeneratedImageModel{{ModelID: "first"}, {ModelID: "fallback"}, {ModelID: "never-sent"}}}
	if _, err := r.sendChannelWithRetry(context.Background(), msg, 1, event); err == nil {
		t.Fatal("expected third image send failure")
	}
	if len(store.records) != 2 {
		t.Fatalf("caption or failed image was attributed: %+v", store.records)
	}
	for id, want := range map[string]string{"2": "first", "3": "fallback"} {
		event.Quoted = &QuotedMessage{MessageID: id}
		tool := newDianaRuntimeModelTool(newRuntimeAgentLLMProvider(r, context.Background()), event)
		body, err := tool.Run(context.Background(), map[string]any{"group": "history"})
		if err != nil || !strings.Contains(body, `"model_id":"`+want+`"`) {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
	event.ProfileID = "bot-b"
	body, err := newDianaRuntimeModelTool(newRuntimeAgentLLMProvider(r, context.Background()), event).Run(context.Background(), map[string]any{"group": "history", "message_id": "2"})
	if err != nil || !strings.Contains(body, `"found":false`) {
		t.Fatalf("cross-bot provenance leak: %s %v", body, err)
	}
}

func TestGeneratedImageProvenanceUsesSuccessfulFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model == "primary-image" {
			http.Error(w, "failed", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"url":"https://images.invalid/generated.png"}]}`))
	}))
	defer server.Close()
	profiles := []llm.Profile{}
	for _, model := range []string{"primary-image", "fallback-image"} {
		profiles = append(profiles, llm.Profile{ID: model, Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "private", BaseURL: server.URL, Model: "chat", ImageModel: model}})
	}
	r := NewRuntime(BotConfig{ModelRoles: map[string]ModelRole{"chat": {Group: llm.GroupChat, Model: "chat"}}}, nilChannel{}, NewPluginManager(), &stubLLMProfileStore{set: llm.ProfileSet{Profiles: profiles}}, nil, nil, nil)
	tool := &dianaImageTool{runtime: r, event: MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g"}}
	output, err := tool.execute(context.Background(), dianaImageToolRequest{Operation: "generate", Prompt: "test image"}, PluginTaskServices{})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Models) != 1 || output.Models[0].ModelID != "fallback-image" || output.Models[0].Operation != "generate" {
		t.Fatalf("wrong actual provenance: %+v", output.Models)
	}
}
