package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type followUpControlChannel struct {
	recordingChannel
	sendError error
	noAck     bool
}

func (c *followUpControlChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if c.sendError != nil {
		return nil, c.sendError
	}
	if err := c.recordingChannel.Send(ctx, msg); err != nil {
		return nil, err
	}
	if c.noAck {
		return nil, nil
	}
	return map[string]any{"message_id": "follow-up-id"}, nil
}

func TestFollowUpStripsControlMarkersAndCountsOnlyAcknowledgedRefusal(t *testing.T) {
	for _, kind := range []followUpKind{followUpKindPlugin, followUpKindRepositoryWatch} {
		for _, mode := range []string{"ack", "failed", "no_ack"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				channel := &followUpControlChannel{noAck: mode == "no_ack"}
				if mode == "failed" {
					channel.sendError = errors.New("send failed")
				}
				r := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner", SendRetryAttempts: 1}, channel, NewPluginManager(), nil, nil, nil, nil)
				event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "user", MessageID: "source"}
				err := r.sendFollowUp(context.Background(), kind, event, "这类新闻我就不展开聊了喵……"+replyRefusalMarker+replyRefusalMarker)
				if (err != nil) != (mode == "failed") {
					t.Fatalf("err=%v", err)
				}
				for _, msg := range channel.sentSnapshot() {
					if strings.Contains(msg.Text, "[[DIANA_") {
						t.Fatalf("marker leaked: %s", msg.Text)
					}
				}
				want := 0
				if mode == "ack" {
					want = 1
				}
				if got := len(r.replyRefusalByUser["user"].Hits); got != want {
					t.Fatalf("refusal count=%d want=%d", got, want)
				}
				if mode == "ack" {
					if err := r.sendFollowUp(context.Background(), kind, event, "仍不展开。"+replyRefusalMarker); err != nil {
						t.Fatal(err)
					}
					if len(r.replyRefusalByUser["user"].Hits) != 1 {
						t.Fatal("same source counted twice")
					}
				}
			})
		}
	}
}

func TestFollowUpMarkerOnlyAndPlainReply(t *testing.T) {
	channel := &followUpControlChannel{}
	r := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "user", MessageID: "plain"}
	if err := r.sendFollowUp(context.Background(), followUpKindPlugin, event, "正常跟评"); err != nil {
		t.Fatal(err)
	}
	if len(r.replyRefusalByUser["user"].Hits) != 0 {
		t.Fatal("plain follow-up counted as refusal")
	}
	event.MessageID = "marker-only"
	if err := r.sendFollowUp(context.Background(), followUpKindPlugin, event, replyRefusalMarker); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 2 || strings.TrimSpace(sent[1].Text) == "" || strings.Contains(sent[1].Text, replyRefusalMarker) {
		t.Fatalf("sent=%+v", sent)
	}
	if len(r.replyRefusalByUser["user"].Hits) != 1 {
		t.Fatal("visible marker-only refusal not counted")
	}
}

func TestFollowUpKeepsSingleDeliveryAndCleansHistory(t *testing.T) {
	for _, marked := range []bool{false, true} {
		channel := &followUpControlChannel{}
		r := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "owner"}, channel, NewPluginManager(), nil, nil, nil, nil)
		event := MessageEvent{Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g", UserID: "user", MessageID: "source", replyDeliveryMode: replyDeliverySingle}
		text := "这次不展开。" + notificationSplitMarker + "换个话题吧。" + replyRefusalMarker
		if marked {
			text = replySingleMarker + text
			event.replyDeliveryMode = ""
		}
		if err := r.sendFollowUp(context.Background(), followUpKindPlugin, event, text); err != nil {
			t.Fatal(err)
		}
		if len(channel.sentSnapshot()) != 1 {
			t.Fatal("single-message preference lost")
		}
		for _, history := range r.history {
			for _, message := range history {
				if strings.Contains(historyPlainText(message), "[[DIANA_") {
					t.Fatal("control marker persisted into history")
				}
			}
		}
	}
}
