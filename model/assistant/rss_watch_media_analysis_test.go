package assistant

import (
	"bytes"
	"context"
	"fmt"
	"github.com/SuInk/diana/model/llm"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rssMediaProbe struct {
	images atomic.Int32
	judges atomic.Int32
}

func (p *rssMediaProbe) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	for _, m := range req.Messages {
		for _, part := range m.Parts {
			if part.Type == llm.ContentPartImageURL {
				p.images.Add(1)
				return &llm.GenerateResponse{Text: "画面中有一只红色的猫。"}, nil
			}
		}
	}
	p.judges.Add(1)
	text := fmt.Sprint(req.Messages)
	if !strings.Contains(text, "红色的猫") {
		return &llm.GenerateResponse{Text: `{"notify":false,"reply":""}`}, nil
	}
	return &llm.GenerateResponse{Text: `{"notify":true,"reply":"图片中有红色的猫 https://x.com/a/status/1"}`}, nil
}

func rssAnalysisPNG(t *testing.T, palette ...color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	ink := color.RGBA{255, 0, 0, 255}
	if len(palette) > 0 {
		ink = palette[0]
	}
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.Set(x, y, ink)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func rssMediaRuntime(t *testing.T, p *RSSWatchPlugin, provider LLMProvider) *Runtime {
	t.Helper()
	store := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "model", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "vision-test", APIKey: "test-key"}}}}}
	r := NewRuntime(BotConfig{}, &recordingChannel{}, NewPluginManager(p), store, nil, nil, nil)
	r.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return provider, nil })
	return r
}
func rssMediaClient(body []byte, mime string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
}
func TestRSSMediaAnalysisReachesJudgeAndCaches(t *testing.T) {
	p := NewRSSWatchPlugin(rssMediaClient(rssAnalysisPNG(t), "image/png"))
	provider := &rssMediaProbe{}
	r := rssMediaRuntime(t, p, provider)
	item := Reminder{FeedRuleMode: "llm", FeedJudgePrompt: "图片有猫才通知", FeedMediaMode: "images"}
	change := rssWatchChange{Items: []rssWatchItem{{ID: "1", Content: "无图片答案的正文", Link: "https://x.com/a/status/1", Images: []string{"https://pbs.twimg.com/media/sample.png"}}}}
	for i := 0; i < 2; i++ {
		enriched, err := r.prepareRSSMedia(context.Background(), item, change, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := r.judgeRSSWatch(context.Background(), item, enriched)
		if err != nil || !decision.Notify {
			t.Fatalf("decision=%+v err=%v", decision, err)
		}
	}
	if provider.images.Load() != 1 || provider.judges.Load() != 1 {
		t.Fatalf("media=%d judges=%d", provider.images.Load(), provider.judges.Load())
	}
	if strings.Contains(change.Items[0].Content, "红色") {
		t.Fatal("mutated feed")
	}
	item.FeedMediaMode = "text"
	enriched, err := r.prepareRSSMedia(context.Background(), item, change, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := r.judgeRSSWatch(context.Background(), item, enriched)
	if err != nil || decision.Notify {
		t.Fatal("text only inferred media")
	}
}
func TestRSSMediaFailureAndMissingFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, err := normalizeRSSMedia("video", "retry", true); err == nil {
		t.Fatal("accepted missing dependency")
	}
	if _, _, err := normalizeRSSMedia("video", "text", true); err != nil {
		t.Fatal(err)
	}
	p := NewRSSWatchPlugin(rssMediaClient([]byte(`{"code":200,"results":[{"id":"1","text":"普通正文","media":{"videos":[{"url":"https://video.twimg.com/a.mp4"}]}}]}`), "application/json"))
	r := rssMediaRuntime(t, p, &rssMediaProbe{})
	source := ReminderFeedSource{FeedURL: "https://example.com/feed", LastItemID: "0"}
	item := Reminder{FeedRuleMode: "llm", FeedJudgePrompt: "有猫才通知", FeedMediaMode: "video"}
	_, err := r.checkRSSWatchSource(context.Background(), item, &source, p, nil)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") || source.LastItemID != "0" {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	item.FeedMediaFailure = "text"
	_, err = r.checkRSSWatchSource(context.Background(), item, &source, p, nil)
	if err != nil || source.LastItemID != "1" {
		t.Fatalf("fallback source=%+v err=%v", source, err)
	}
}
func TestRSSMediaVideoFramesAndSilentAudio(t *testing.T) {
	body, err := os.ReadFile(createTimelineVideo(t))
	if err != nil {
		t.Fatal(err)
	}
	p := NewRSSWatchPlugin(rssMediaClient(body, "video/mp4"))
	r := rssMediaRuntime(t, p, &rssMediaProbe{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	frames, transcript, err := r.rssVideoAnalysis(ctx, "https://video.twimg.com/sample.mp4", true, p)
	if err != nil || len(frames) < 4 || transcript != "" {
		t.Fatalf("frames=%d transcript=%q err=%v", len(frames), transcript, err)
	}
	for _, frame := range frames {
		if !strings.HasPrefix(frame, "data:image/") {
			t.Fatal("frame not materialized")
		}
	}
}
func TestLiveRSSMediaVisualJudgment(t *testing.T) {
	client := liveLLMClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, mode := range []string{"images", "images_negative", "video"} {
		t.Run(mode, func(t *testing.T) {
			body, mime := rssAnalysisPNG(t), "image/png"
			mediaMode := mode
			if mode == "images_negative" {
				body = rssAnalysisPNG(t, color.RGBA{0, 0, 255, 255})
				mediaMode = "images"
			}
			entry := rssWatchItem{ID: "1", Content: "测试素材", Link: "https://example.com/sample", Images: []string{"https://pbs.twimg.com/media/sample.png"}}
			rule := "仅当图片主色为红色时通知，回复说明看到的颜色并附链接。"
			if mode == "video" {
				var err error
				body, err = os.ReadFile(createTimelineVideo(t))
				if err != nil {
					t.Fatal(err)
				}
				mime = "video/mp4"
				entry.Images = nil
				entry.Videos = []rssWatchVideo{{URL: "https://video.twimg.com/sample.mp4"}}
				rule = "仅当视频中先出现红色画面，随后出现蓝色画面时通知，回复说明顺序并附链接。"
			}
			p := NewRSSWatchPlugin(rssMediaClient(body, mime))
			r := rssMediaRuntime(t, p, client)
			item := Reminder{FeedRuleMode: "llm", FeedJudgePrompt: rule, FeedMediaMode: mediaMode}
			change := rssWatchChange{Items: []rssWatchItem{entry}}
			enriched, err := r.prepareRSSMedia(ctx, item, change, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := r.judgeRSSWatch(ctx, item, enriched)
			if err != nil || decision.Notify != (mode != "images_negative") {
				t.Fatalf("decision=%+v err=%v analysis=%s", decision, err, enriched.Items[0].Content)
			}
			t.Logf("mode=%s notify=%v reply=%s", mode, decision.Notify, decision.Reply)
		})
	}
}

func TestRSSMediaVideoAudioExtractionAndTranscription(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	path := filepath.Join(t.TempDir(), "audio-video.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=black:size=160x90:rate=5:duration=2", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if err := req.ParseMultipartForm(20 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer req.MultipartForm.RemoveAll()
		f, _, err := req.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		if len(b) < 44 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
			t.Error("missing actual extracted WAV")
		}
		fmt.Fprint(w, `{"text":"音频中的秘密词是橘子"}`)
	}))
	defer server.Close()
	r := mediaSlotTestRuntime(t, map[string]ModelRole{"stt": {ProfileID: "p1", Model: "whisper-1"}}, mediaSlotProfile("p1", server.URL))
	p := NewRSSWatchPlugin(rssMediaClient(body, "video/mp4"))
	frames, transcript, err := r.rssVideoAnalysis(context.Background(), "https://video.twimg.com/audio.mp4", true, p)
	if err != nil || len(frames) == 0 || !strings.Contains(transcript, "橘子") || calls.Load() != 1 {
		t.Fatalf("frames=%d transcript=%q calls=%d err=%v", len(frames), transcript, calls.Load(), err)
	}
}
