package assistant

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const missingImageSourceError = `message element "image" requires a file/url source or a complete fast-upload fingerprint`

type imageRejectingForwardChannel struct {
	*recordingChannel
	customAttempts     int
	recoverCustomAfter int
	rejectStaging      bool
	imageBody          []byte
	fetchedImages      int
}

func (c *imageRejectingForwardChannel) OutboundBackoffEnabled() bool { return true }
func (c *imageRejectingForwardChannel) Status() ChannelStatus {
	return ChannelStatus{Connected: true, SelfID: "42"}
}
func (c *imageRejectingForwardChannel) CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	if action == "send_group_forward_msg" {
		if nodes, ok := params["messages"].([]map[string]any); ok && len(nodes) > 0 {
			if data, ok := nodes[0]["data"].(map[string]any); ok && data["content"] != nil {
				c.customAttempts++
				if c.recoverCustomAfter > 0 && c.customAttempts >= c.recoverCustomAfter {
					return c.recordingChannel.CallAPI(ctx, action, params)
				}
				return nil, errors.New(missingImageSourceError)
			}
		}
	}
	if action == "send_private_msg" && c.rejectStaging {
		if segments, ok := params["message"].([]map[string]any); ok {
			for _, segment := range segments {
				if segment["type"] == "image" {
					return nil, errors.New(missingImageSourceError)
				}
			}
		}
	}
	if action == "send_private_msg" && c.imageBody != nil {
		for _, segment := range params["message"].([]map[string]any) {
			if segment["type"] != "image" {
				continue
			}
			source := segmentDataString(segment["data"], "file")
			if !strings.HasPrefix(source, "http://") {
				return nil, errors.New("bridge received a host-only path")
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
			if err != nil {
				return nil, err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return nil, err
			}
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Equal(body, c.imageBody) {
				return nil, errors.New("bridge could not fetch shared image bytes")
			}
			c.fetchedImages++
		}
	}
	return c.recordingChannel.CallAPI(ctx, action, params)
}

func TestImageForwardRejectionUsesFallbackWithoutGroupCooldown(t *testing.T) {
	withFastSendTiming(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("test image body"))
	}))
	defer server.Close()
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "staged", true: "direct"}[direct], func(t *testing.T) {
			base := resolverForwardChannel()
			channel := &imageRejectingForwardChannel{recordingChannel: base, rejectStaging: direct}
			r := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "42", MessageID: "image-forward"}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			response := resolverForwardTestResponse()
			response.ImageURLs = []string{server.URL + "/1.png", server.URL + "/2.png"}
			response.ForwardMessages[1].ImageURLs = response.ImageURLs[:1]
			response.ForwardMessages[2].ImageURLs = response.ImageURLs[1:]
			if err := r.sendForwardPluginResponse(ctx, event, response, r.Config()); err != nil {
				t.Fatalf("fallback failed: %v", err)
			}
			if channel.customAttempts != outboundPayloadMaxAttempts {
				t.Fatalf("unchanged invalid payload retried %d times", channel.customAttempts)
			}
			if direct {
				images := 0
				for _, msg := range base.sentSnapshot() {
					images += len(msg.ImageURLs)
				}
				if images != 2 {
					t.Fatalf("direct fallback lost images: %d", images)
				}
			} else {
				images := 0
				for _, call := range base.calls {
					if call.action != "send_private_msg" {
						continue
					}
					for _, segment := range call.params["message"].([]map[string]any) {
						if segment["type"] == "image" {
							if segmentDataString(segment["data"], "file") == "" {
								t.Fatal("staged image has no source")
							}
							images++
						}
					}
				}
				if images != 2 || len(base.sentSnapshot()) != 0 {
					t.Fatalf("staged fallback lost images: %d", images)
				}
			}
			gate := r.groupOutboundDelivery(event)
			if gate.failures != 0 || !gate.dropUntil.IsZero() || !gate.nextAttempt.IsZero() {
				t.Fatal("payload rejection poisoned group delivery state")
			}
			if err := r.sendOutgoing(ctx, event, OutgoingMessage{Text: "next valid message"}); err != nil {
				t.Fatalf("later valid message blocked: %v", err)
			}
		})
	}
}

func TestInvalidImagePayloadRetriesThenLeavesGroupAvailable(t *testing.T) {
	withFastSendTiming(t)
	channel := newScriptedBackoffChannel()
	channel.attemptErrors = []error{errors.New(missingImageSourceError), errors.New(missingImageSourceError), errors.New(missingImageSourceError), nil}
	r := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", MessageID: "bad-image"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.sendOutgoing(ctx, event, OutgoingMessage{ImageURLs: []string{"invalid"}}); err == nil || errors.Is(err, errOutboundDeliveryDropped) {
		t.Fatalf("expected bounded payload rejection, got %v", err)
	}
	if err := r.sendOutgoing(ctx, event, OutgoingMessage{Text: "healthy"}); err != nil {
		t.Fatal(err)
	}
	if got := len(channel.attemptTexts(event.GroupID)); got != 4 {
		t.Fatalf("send attempts=%d", got)
	}
}

func TestForwardSafetyRejectionDoesNotTryOtherDeliveryForms(t *testing.T) {
	channel := resolverForwardChannel()
	provider := &qualityTestProvider{reply: `{"should_send":true,"confidence":1,"reason":"test","account_safe":false,"account_risk":"other","account_risk_reason":"test block"}`}
	r := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "42", MessageID: "blocked-forward"}
	err := r.sendForwardPluginResponse(context.Background(), event, resolverForwardTestResponse(), r.Config())
	var blocked *replyAccountSafetyRejectedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected safety rejection, got %v", err)
	}
	if countRecordingSends(channel) != 0 {
		t.Fatal("safety rejection bypassed through fallback")
	}
}

func TestImageSourcesKeepRemoteAndInlineFormats(t *testing.T) {
	for input, want := range map[string]string{
		"file:///tmp/image.jpg":         "file:///tmp/image.jpg",
		"https://example.com/image.jpg": "https://example.com/image.jpg",
		"data:image/png;base64,AAAA":    "base64://AAAA",
	} {
		if got := imageFileForOutgoingSegment(input); got != want {
			t.Fatalf("source=%q got=%q want=%q", input, got, want)
		}
	}
	if isOutboundPayloadRejection(errors.Join(context.DeadlineExceeded, errors.New(missingImageSourceError))) {
		t.Fatal("unknown delivery outcome treated as safe to retry through fallback")
	}
}

func TestForwardStagingFetchesHostImagesOverHTTP(t *testing.T) {
	withFastSendTiming(t)
	imageBody := []byte("image bytes available only through the media endpoint")
	file := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(file, imageBody, 0600); err != nil {
		t.Fatal(err)
	}
	var store *LocalMediaStore
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		store.ServeToken(w, request, path.Base(request.URL.Path))
	}))
	defer server.Close()
	store = NewLocalMediaStore(server.URL + "/media/resolver")
	channel := &imageRejectingForwardChannel{recordingChannel: resolverForwardChannel(), imageBody: imageBody}
	r := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	r.SetLocalMediaSharer(store)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "42", MessageID: "host-image"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.sendRealForwardMessages(ctx, event, []OutgoingMessage{{ImageURLs: []string{file}}}, r.Config())
	if err != nil {
		t.Fatal(err)
	}
	if channel.customAttempts != outboundPayloadMaxAttempts || channel.fetchedImages != 1 {
		t.Fatalf("custom=%d fetched=%d", channel.customAttempts, channel.fetchedImages)
	}
}

func TestImageForwardRecoversWithinRetryBudget(t *testing.T) {
	withFastSendTiming(t)
	for _, succeedsAt := range []int{2, 3} {
		channel := &imageRejectingForwardChannel{recordingChannel: resolverForwardChannel(), recoverCustomAfter: succeedsAt}
		r := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
		event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", SelfID: "42", MessageID: "retry-image"}
		id, err := r.sendRealForwardMessages(context.Background(), event, resolverForwardTestResponse().ForwardMessages, r.Config())
		if err != nil || id == "" || channel.customAttempts != succeedsAt {
			t.Fatalf("succeedsAt=%d attempts=%d id=%q err=%v", succeedsAt, channel.customAttempts, id, err)
		}
		if countRecordingCalls(channel.recordingChannel, "send_private_msg") != 0 || len(channel.sentSnapshot()) != 0 {
			t.Fatal("successful retry also triggered fallback")
		}
	}
}

func TestImageRetryCancellationDoesNotPoisonGroup(t *testing.T) {
	r := NewRuntime(BotConfig{}, newScriptedBackoffChannel(), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	_, err := r.executeOutboundCall(ctx, event, "send_group_forward_msg", func(context.Context) (map[string]any, error) {
		attempts++
		cancel()
		return nil, errors.New(missingImageSourceError)
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	gate := r.groupOutboundDelivery(event)
	if gate.failures != 0 || !gate.dropUntil.IsZero() {
		t.Fatal("cancelled image retry locked group")
	}
}
