package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVoiceTTSPluginRegisteredWithPresets(t *testing.T) {
	state, ok := NewDefaultPluginManager().Get(voiceTTSPluginID)
	if !ok || !state.Installed || !state.Enabled {
		t.Fatalf("state=%#v ok=%v", state, ok)
	}
	if len(state.Manifest.Settings) == 0 {
		t.Fatal("voice plugin settings are missing")
	}
}

func TestVoiceTTSPluginSynthesizesExplicitCommand(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(testVoiceWAV())
	}))
	defer server.Close()

	plugin := NewVoiceTTSPlugin(server.Client())
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text: "语音说：晚安",
		Settings: SettingValues{
			voiceTTSSettingPreset:   voiceTTSPresetCustom,
			voiceTTSSettingEndpoint: server.URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !resp.Handled || !strings.HasPrefix(resp.Reply, "[CQ:record,file=base64://") {
		t.Fatalf("response=%#v", resp)
	}
	if requestBody["text"] != "晚安" || requestBody["media_type"] != "wav" {
		t.Fatalf("request=%#v", requestBody)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(resp.Reply, "[CQ:record,file=base64://"), "]")
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		t.Fatalf("invalid base64 record: %v", err)
	}
	segments := buildOutgoingSegments(OutgoingMessage{Text: resp.Reply})
	if len(segments) != 1 || segments[0]["type"] != "record" {
		t.Fatalf("onebot segments=%#v", segments)
	}
}

func TestVoiceTTSPluginIgnoresNormalChat(t *testing.T) {
	resp, err := NewVoiceTTSPlugin(nil).Handle(context.Background(), PluginRequest{Text: "今天天气怎么样"})
	if err != nil || resp != nil {
		t.Fatalf("response=%#v err=%v", resp, err)
	}
}

func testVoiceWAV() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 4, 0, 0, 0,
		'W', 'A', 'V', 'E',
	}
}
