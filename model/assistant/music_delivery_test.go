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
	for _, platform := range []string{PlatformWeixin} {
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
		if !strings.Contains(strings.Join(args, " "), "libmp3lame -q:a 0") || !strings.Contains(strings.Join(args, " "), "title=歌名") || !strings.Contains(strings.Join(args, " "), "artist=歌手") || command == "silk-encoder" {
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
			if req.FormValue("chat_id") != "-1001" || req.FormValue("message_thread_id") != "7" || telegramTestReplyID(t, map[string]any{"reply_parameters": req.FormValue("reply_parameters")}) != "9" {
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
	prepared, err := r.prepareChannelAudio(msg)
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
		if _, err := r.prepareChannelAudio(msg); err == nil {
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
	if _, err := manager.SetEnabledForProfile(musicPluginID, "qq", true); err != nil {
		t.Fatal(err)
	}
	overrides := manager.ProfileOverrides("qq")
	for _, platform := range []string{PlatformTelegram, PlatformOneBotV11, PlatformQQOfficial} {
		tools, err := manager.AgentToolsForPlatformWithGroupOverrides(platform, overrides, nil)
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

func TestMusicSilkEncodingPreservesWidebandAtHigherBitrate(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	cfg := musicConfigFromSettings(SettingValues{musicSettingSilkEncoder: "silk-encoder"})
	path := filepath.Join(t.TempDir(), "music.mp3")
	if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	plugin.commandRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		joined := strings.Join(args, " ")
		if name == cfg.FFmpegPath {
			if !strings.Contains(joined, "-ar 24000 -ac 1 -f s16le") {
				t.Fatalf("PCM arguments: %s", joined)
			}
			return nil, os.WriteFile(path+".pcm", []byte("pcm"), 0600)
		}
		if name != "silk-encoder" || !strings.Contains(joined, "-rate 40000") || !strings.Contains(joined, "-Fs_maxInternal 24000") || !strings.Contains(joined, "-complexity 2") {
			t.Fatalf("Silk arguments: %s", joined)
		}
		return nil, os.WriteFile(path+".silk", []byte("\x02#!SILK_V3encoded"), 0600)
	}
	got, err := plugin.encodeSilkIfConfigured(context.Background(), cfg, path)
	if err != nil || got != path+".silk" || calls != 2 {
		t.Fatalf("got %s, %v calls=%d", got, err, calls)
	}
	if _, err := os.Stat(path + ".pcm"); !os.IsNotExist(err) {
		t.Fatal("temporary PCM not removed")
	}
	cfg.SilkEncoder = ""
	got, err = plugin.encodeSilkIfConfigured(context.Background(), cfg, path)
	if err != nil || got != path || calls != 2 {
		t.Fatal("unconfigured encoder must preserve source")
	}
}

// QQ 官方渠道不认 CQ 码：语音合成交来的 [CQ:record] 要在发送前换成本机音频文件，
// 由渠道走富媒体上传（file_type=3）发成语音条。
func TestQQOfficialAudioConvertsCQRecordToLocalFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "voice.wav")
	if err := os.WriteFile(file, []byte("RIFFaudio"), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewLocalMediaStore("http://host.docker.internal:18080/media/resolver")
	shared, ok := store.Share(file, time.Hour)
	if !ok {
		t.Fatal("share failed")
	}
	r := NewRuntime(BotConfig{Platform: PlatformQQOfficial}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetLocalMediaSharer(store)
	msg := OutgoingMessage{Platform: PlatformQQOfficial, UserID: "openid-1", Text: "[CQ:record,file=" + escapeCQParameter(shared) + "]"}
	prepared, err := r.prepareChannelAudio(msg)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Text != "" || len(prepared.AudioURLs) != 1 || prepared.AudioURLs[0] != file {
		t.Fatalf("prepared = %#v", prepared)
	}
	msg.Text = "[CQ:record,file=/etc/passwd]"
	if _, err := r.prepareChannelAudio(msg); err == nil {
		t.Fatal("unregistered audio accepted")
	}
	// OneBot 照旧原样发 CQ 码。
	onebot := OutgoingMessage{Platform: "onebot-v11", Text: "[CQ:record,file=" + escapeCQParameter(shared) + "]"}
	if got, _ := r.prepareChannelAudio(onebot); got.Text != onebot.Text || len(got.AudioURLs) != 0 {
		t.Fatalf("onebot message changed: %#v", got)
	}
}

// QQ 官方机器人点歌发语音：工具产出 [CQ:record]，由通道换成本机文件按语音上传，
// 不再退回一行歌曲链接。
func TestMusicToolSendsVoiceOnQQOfficial(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	plugin.SetLocalMediaSharer(&recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"})
	tools, err := plugin.AgentToolsForPlatform(PlatformQQOfficial, musicRequestSettings(server))
	if err != nil {
		t.Fatal(err)
	}
	tool := tools[0].(*dianaMusicTool)
	output, err := tool.Run(context.Background(), map[string]any{"query": "雾里"})
	if err != nil {
		t.Fatal(err)
	}
	reply, done := tool.TerminalResult(output)
	if !done || !strings.Contains(reply, "[CQ:record") || strings.Contains(reply, "https://music.163.com/song?id=") {
		t.Fatalf("qq official reply=%q", reply)
	}
}
