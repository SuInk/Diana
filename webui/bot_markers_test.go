package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

func TestBotMarkersPersistenceKeepsActiveProfile(t *testing.T) {
	ctx := context.Background()
	db, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{{ID: "a", OwnerID: "900"}, {ID: "b", OwnerID: "901"}}}); err != nil {
		t.Fatal(err)
	}
	if err = NewRuntimePersistor(s).SaveMarkedBotIDs("b", []string{"200"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPersistentBotProfileStore(ctx, db, assistant.BotConfig{})
	if err != nil {
		t.Fatal(err)
	}
	set := reopened.Profiles()
	if set.ActiveID != "a" {
		t.Fatal("mark switched active profile")
	}
	for _, p := range set.Profiles {
		if slices.Contains(p.MarkedBotIDs, "200") != (p.ID == "b") {
			t.Fatalf("wrong scope: %+v", p.MarkedBotIDs)
		}
	}
	if err = s.SaveMarkedBotIDs("missing", []string{"200"}); err == nil {
		t.Fatal("missing profile accepted")
	}
}

func TestBotMarkersGroupAdminCannotChangeOwnerList(t *testing.T) {
	ctx := context.Background()
	r := assistant.NewRuntime(assistant.BotConfig{ID: "a", OwnerID: "900"}, fakeChannel{}, assistant.NewPluginManager(), nil, nil, nil, nil)
	h := NewBotHandler(ctx, r)
	profiles := NewMemoryBotProfileStore(r.Config())
	if err := profiles.SaveProfiles(assistant.ProfileSet{ActiveID: "a", Profiles: []assistant.BotConfig{{ID: "a", OwnerID: "900"}}}); err != nil {
		t.Fatal(err)
	}
	h.SetProfileStore(profiles)
	groups := NewMemoryBotGroupConfigStore()
	h.SetGroupConfigStore(groups)
	if _, err := groups.SaveGroupConfig(assistant.GroupConfig{BotProfileID: "a", GroupID: "100", MarkedBotIDs: []string{"200"}}, r.Config()); err != nil {
		t.Fatal(err)
	}
	router := botTestRouter(h)
	for _, tc := range []struct {
		user, body string
		status     int
	}{{"901", `{"config":{"marked_bot_ids":[]}}`, 403}, {"901", `{"config":{"system_prompt":"ordinary change"}}`, 200}, {"900", `{"config":{"marked_bot_ids":["201"]}}`, 200}} {
		code, _, err := h.groupAdmin.CreateChallenge("100", tc.user, "a")
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := h.groupAdmin.Verify("100", tc.user, code, "a")
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/assistant/group-admin/config", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Diana-Group-Token", token)
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.user, rec.Code, rec.Body.String())
		}
		saved, _ := groups.ConfigForGroup("a", "100")
		want := "200"
		if tc.user == "900" {
			want = "201"
		}
		if !slices.Equal(saved.MarkedBotIDs, []string{want}) {
			t.Fatalf("markers=%v", saved.MarkedBotIDs)
		}
	}
}
