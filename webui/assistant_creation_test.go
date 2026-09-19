package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestNewBotUsesFreshDefaultsAndDoesNotInheritSecrets(t *testing.T) {
	existing := assistant.DefaultBotConfig()
	existing.ID = "original"
	existing.Name = "Existing bot"
	existing.OwnerID = "12345"
	existing.SystemPrompt = "Existing custom persona"
	existing.OneBotAccessToken = "original-onebot-token"
	existing.OneBotHTTPSecret = "original-http-secret"
	existing.TelegramBotToken = "original-telegram-token"
	existing.NoneBotBridgeToken = "original-bridge-token"
	existing.QQAppSecret = "original-qq-secret"
	existing.DingTalkClientSecret = "original-dingtalk-secret"
	existing.FeishuAppSecret = "original-feishu-secret"
	existing.WeComSecret = "original-wecom-secret"
	existing.GroupTriggers = []string{"old trigger"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := assistant.NewRuntime(existing, fakeChannel{}, assistant.NewDefaultPluginManager(), nil, nil, nil, nil)
	handler := NewBotHandlerWithFactory(ctx, runtime, func(assistant.BotConfig) assistant.Channel { return fakeChannel{} })
	existing = handler.profiles.Profiles().Profiles[0]
	router := botTestRouter(handler)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/assistant/config/defaults?platform=telegram", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("defaults status=%d body=%s", response.Code, response.Body.String())
	}
	var draft assistant.ConfigPayload
	if err := json.Unmarshal(response.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if !draft.Enabled || !draft.OwnerLoginEnabled {
		t.Fatal("new bot switches must default on")
	}
	if draft.Platform != "telegram" || draft.ID != "" || draft.OwnerID != "" || draft.SystemPrompt == existing.SystemPrompt || draft.TelegramBotTokenConfigured || draft.OneBotAccessTokenConfigured || draft.OneBotHTTPSecretConfigured {
		t.Fatalf("defaults contain existing profile settings: %+v", draft)
	}
	if draft.GroupTriggers[0] == "old trigger" {
		t.Fatal("new bot inherited triggers")
	}
	if len(handler.profiles.Profiles().Profiles) != 1 {
		t.Fatal("reading defaults changed the stored profiles")
	}

	// Explicitly turning both switches off must be honored. Even a supplied
	// existing ID cannot turn the creation endpoint into an edit operation.
	draft.ID = existing.ID
	draft.Enabled, draft.OwnerLoginEnabled = false, false
	draft.Name = "Independent bot"
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/assistant/config/new", bytes.NewReader(raw)))
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	set := handler.profiles.Profiles()
	if len(set.Profiles) != 2 {
		t.Fatalf("creation did not add a profile: %d", len(set.Profiles))
	}
	// 新建的机器人追加在末尾。
	fresh := set.Profiles[len(set.Profiles)-1]
	if fresh.ID == existing.ID || fresh.Enabled || fresh.OwnerLoginEnabled {
		t.Fatalf("incorrect new identity or switches: %s", fresh.ID)
	}
	for field, value := range map[string]string{
		"onebot": fresh.OneBotAccessToken, "http": fresh.OneBotHTTPSecret, "telegram": fresh.TelegramBotToken, "bridge": fresh.NoneBotBridgeToken,
		"qq": fresh.QQAppSecret, "dingtalk": fresh.DingTalkClientSecret, "feishu": fresh.FeishuAppSecret, "wecom": fresh.WeComSecret,
	} {
		if value != "" {
			t.Errorf("new profile inherited %s credential", field)
		}
	}
	original, ok := set.ConfigForProfile(existing.ID)
	if !ok || original.OneBotAccessToken != existing.OneBotAccessToken || original.SystemPrompt != existing.SystemPrompt || original.Enabled || original.OwnerLoginEnabled {
		t.Fatal("existing profile was changed")
	}
}

func TestUnknownBotIDDoesNotReuseCurrentSecrets(t *testing.T) {
	original := assistant.DefaultBotConfig()
	original.OneBotAccessToken = "private-token"
	set := assistant.NewProfileSet(original)
	cfg := existingBotProfileConfig(set, assistant.ConfigPayload{ID: "another-bot"})
	if cfg.OneBotAccessToken != "" {
		t.Fatal("unknown ID inherited current credential")
	}
	// Legacy edit requests without an ID still preserve the current credentials.
	cfg = existingBotProfileConfig(set, assistant.ConfigPayload{})
	if cfg.OneBotAccessToken != "private-token" {
		t.Fatal("legacy editing lost credentials")
	}
}
