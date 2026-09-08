package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestParticipationHTTPSaveAndRestart(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPersistentBotProfileStore(ctx, db, assistant.DefaultBotConfig())
	if err != nil {
		t.Fatal(err)
	}
	r := assistant.NewRuntime(assistant.DefaultBotConfig(), fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandlerWithFactory(ctx, r, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	h.SetProfileStore(store)
	router := botTestRouter(h)
	current, _ := store.Profiles().Current()
	body := assistant.PayloadFromConfig(current)
	body.Enabled = false
	body.Participation = &assistant.ParticipationPreferences{Desire: 87, Social: 99, Followup: 61, Restraint: 12, Information: 0, CooldownSeconds: 45}
	encoded, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/assistant/config", strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	var response assistant.ConfigPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Participation == nil || *response.Participation != *body.Participation {
		t.Fatalf("response: %+v", response.Participation)
	}
	reopened, err := NewPersistentBotProfileStore(ctx, db, assistant.DefaultBotConfig())
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := reopened.Profiles().Current()
	if saved.Participation == nil || *saved.Participation != *body.Participation {
		t.Fatalf("restart: %+v", saved.Participation)
	}
}
