package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMusicLinksOnOtherPlatformsNeverExposeCQ(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	for _, platform := range []string{PlatformQQOfficial, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		tools, err := plugin.AgentToolsForPlatform(platform, musicRequestSettings(server))
		if err != nil {
			t.Fatal(err)
		}
		tool := tools[0].(*dianaMusicTool)
		output, err := tool.Run(context.Background(), map[string]any{"query": "雾里"})
		if err != nil {
			t.Fatal(err)
		}
		reply, done := tool.TerminalResult(output)
		if !done || strings.Contains(reply, "[CQ:") || !strings.Contains(reply, "https://music.163.com/song?id=") {
			t.Fatalf("platform=%s reply=%q", platform, reply)
		}
	}
}

func TestMusicTelegramConvertsToMP3NotSilk(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("downloaded audio"))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://host.docker.internal/media/resolver/token"}
	plugin.SetLocalMediaSharer(sharer)
	calls := 0
	plugin.commandRunner = func(_ context.Context, command string, args ...string) ([]byte, error) {
		calls++
		if !strings.Contains(strings.Join(args, " "), "libmp3lame") || !strings.Contains(strings.Join(args, " "), "title=歌名") || !strings.Contains(strings.Join(args, " "), "artist=歌手") || command == "silk-encoder" {
			t.Fatal("Telegram used wrong encoder")
		}
		return nil, os.WriteFile(args[len(args)-1], []byte("ID3audio"), 0600)
	}
	cfg := musicConfigFromSettings(musicRequestSettings(server))
	cfg.SilkEncoder = "silk-encoder"
	reply, err := plugin.prepareSongVoice(context.Background(), cfg, song{Source: "netease", Name: "歌名", Artists: "歌手", PlayURL: server.URL + "/audio/1974443814.mp3"}, PlatformTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(sharer.paths) != 1 || !strings.HasSuffix(sharer.paths[0], ".telegram.mp3") || !strings.Contains(reply, "[CQ:record") {
		t.Fatalf("bad preparation: %q %#v", reply, sharer.paths)
	}
	t.Cleanup(func() { cleanupLocalMediaFile(sharer.paths[0]) })
}

func TestTelegramMusicUploadsLocalMediaInsteadOfCQOrInternalURL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(file, []byte("ID3audio"), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewLocalMediaStore("http://host.docker.internal:18080/media/resolver")
	shared, ok := store.Share(file, time.Hour)
	if !ok {
		t.Fatal("share failed")
	}
	var textCalls, audioCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/sendMessage") {
			textCalls++
			var payload map[string]any
			_ = json.NewDecoder(req.Body).Decode(&payload)
			if payload["text"] != "歌名" {
				t.Errorf("bad text: %#v", payload)
			}
		} else if strings.HasSuffix(req.URL.Path, "/sendAudio") {
			audioCalls++
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer req.MultipartForm.RemoveAll()
			if req.FormValue("chat_id") != "-1001" || req.FormValue("message_thread_id") != "7" || req.FormValue("reply_to_message_id") != "9" {
				t.Error("lost routing")
			}
			f, _, err := req.FormFile("audio")
			if err != nil {
				t.Error(err)
				return
			}
			defer f.Close()
			data, _ := io.ReadAll(f)
			if string(data) != "ID3audio" {
				t.Error("did not upload audio bytes")
			}
		} else {
			t.Errorf("unexpected API %s", req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer server.Close()
	channel := NewTelegramChannel(TelegramConfig{BotToken: "test-token", APIBaseURL: server.URL})
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, channel, NewPluginManager(), nil, nil, nil, nil)
	r.SetLocalMediaSharer(store)
	msg := OutgoingMessage{Platform: PlatformTelegram, GroupID: "-1001", MessageThreadID: "7", ReplyMessageID: "9", Text: "歌名\n[CQ:record,file=" + escapeCQParameter(shared) + "]"}
	prepared, err := r.prepareTelegramAudio(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.sendChannelWithRetry(context.Background(), prepared, 1); err != nil {
		t.Fatal(err)
	}
	if textCalls != 1 || audioCalls != 1 {
		t.Fatalf("text=%d audio=%d", textCalls, audioCalls)
	}
	for _, raw := range []string{"[CQ:record,file=/etc/passwd]", "[CQ:record,file=http://host.docker.internal/media/resolver/expired]"} {
		msg.Text = raw
		if _, err := r.prepareTelegramAudio(msg); err == nil {
			t.Fatal("unregistered audio accepted")
		}
		if _, err := channel.SendWithResult(context.Background(), msg); err == nil {
			t.Fatal("CQ leaked as text")
		}
	}
}

type retryMusicAudioChannel struct {
	recordingChannel
	audioAttempts int
}

func (c *retryMusicAudioChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if len(msg.AudioURLs) > 0 {
		c.audioAttempts++
		if c.audioAttempts == 1 {
			return nil, errors.New("temporary audio failure")
		}
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func TestTelegramAudioRetryDoesNotRepeatSongTitle(t *testing.T) {
	channel := &retryMusicAudioChannel{}
	r := NewRuntime(BotConfig{Platform: PlatformTelegram}, channel, NewPluginManager(), nil, nil, nil, nil)
	_, err := r.sendChannelWithRetry(context.Background(), OutgoingMessage{Platform: PlatformTelegram, UserID: "1", Text: "歌名", AudioURLs: []string{"song.mp3"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || channel.audioAttempts != 2 || sent[0].Text != "歌名" || len(sent[0].AudioURLs) != 0 || sent[1].Text != "" || len(sent[1].AudioURLs) != 1 {
		t.Fatalf("retry duplicated payload: %#v attempts=%d", sent, channel.audioAttempts)
	}
}

func TestTelegramMusicPreparationFailureIsNotPlaybackSuccess(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	plugin.commandRunner = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("ffmpeg unavailable") }
	tools, err := plugin.AgentToolsForPlatform(PlatformTelegram, musicRequestSettings(server))
	if err != nil {
		t.Fatal(err)
	}
	tool := tools[0].(*dianaMusicTool)
	output, err := tool.Run(context.Background(), map[string]any{"query": "雾里"})
	if err == nil || output != "" {
		t.Fatalf("failed audio was reported as available: output=%q err=%v", output, err)
	}
}

func TestMusicPluginManagerBindsPlatformPerTool(t *testing.T) {
	manager := NewPluginManager(NewMusicPlugin(nil))
	if _, err := manager.SetEnabled(musicPluginID, true); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{PlatformTelegram, PlatformOneBotV11, PlatformQQOfficial, PlatformDingTalk, PlatformFeishu, PlatformWeCom} {
		tools, err := manager.AgentToolsForPlatformWithGroupOverrides(platform, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range tools {
			if music, ok := tool.(*dianaMusicTool); ok {
				found = true
				if music.platform != platform {
					t.Fatalf("wrong platform %q", music.platform)
				}
			}
		}
		if !found {
			t.Fatalf("music tool unavailable on %s", platform)
		}
	}
}
