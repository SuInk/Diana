package assistant

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRoomRenderer() RoomSnapshotRenderer {
	files := http.Dir("../../frontend/public")
	return NewRoomSnapshotRenderer(func(name string) (io.ReadCloser, error) { return files.Open(name) })
}
func TestRoomSnapshotRenderer(t *testing.T) {
	render := testRoomRenderer()
	var previous []byte
	for _, pose := range []string{"rest", "phone", "desk"} {
		data, err := render(context.Background(), RoomState{Pose: pose, Phase: "idle", Revision: 1})
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 960 || img.Bounds().Dy() != 672 {
			t.Fatal(img.Bounds())
		}
		if bytes.Equal(data, previous) {
			t.Fatal("poses rendered identically")
		}
		previous = data
		if dir := os.Getenv("DIANA_TEST_ROOM_IMAGE_OUT"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, pose+".png"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := render(ctx, RoomState{Pose: "rest"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := render(context.Background(), RoomState{Pose: "bad"}); err == nil {
		t.Fatal("invalid pose")
	}
	broken := NewRoomSnapshotRenderer(func(string) (io.ReadCloser, error) { return nil, errors.New("missing asset") })
	if _, err := broken(context.Background(), RoomState{Pose: "rest"}); err == nil {
		t.Fatal("missing assets accepted")
	}
}
func TestRoomSnapshotAndReportDelivery(t *testing.T) {
	r, _, _ := roomTestRuntime()
	ch := &recordingChannel{}
	r.channel = ch
	r.SetRoomSnapshotRenderer(testRoomRenderer())
	event := MessageEvent{ProfileID: "one", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "group-one", UserID: "user-one"}
	tool := &dianaRoomTool{runtime: r, event: event}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "get"}); err != nil {
		t.Fatal(err)
	}
	if len(ch.sent) != 0 {
		t.Fatal("get sent unsolicited message")
	}
	for _, op := range []string{"report", "snapshot"} {
		output, err := tool.Run(context.Background(), map[string]any{"operation": op})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output, `"sent":true`) {
			t.Fatal(output)
		}
	}
	if len(ch.sent) != 2 || !strings.Contains(ch.sent[0].Text, "小窝状态") || len(ch.sent[1].ImageURLs) != 1 {
		t.Fatalf("sent: %+v", ch.sent)
	}
	for _, msg := range ch.sent {
		if msg.ProfileID != "one" || msg.GroupID != "group-one" {
			t.Fatalf("wrong target: %+v", msg)
		}
	}
	ch.sendErr = errors.New("offline")
	if output, err := tool.Run(context.Background(), map[string]any{"operation": "snapshot"}); err == nil || strings.Contains(output, `"sent":true`) {
		t.Fatal("failed send reported success")
	}
}
func TestRoomSnapshotRejectsLateOrUnscopedSends(t *testing.T) {
	r, cfg, _ := roomTestRuntime()
	ch := &recordingChannel{}
	r.channel = ch
	tool := &dianaRoomTool{runtime: r, event: MessageEvent{ProfileID: "one", Kind: EventKindPrivate, UserID: "user"}}
	for _, input := range []map[string]any{
		{"operation": "snapshot", "pose": "desk"}, {"operation": "snapshot", "group_id": "other"}, {"operation": "snapshot", "path": "/etc/passwd"},
	} {
		if _, err := tool.Run(context.Background(), input); err == nil {
			t.Fatal(input)
		}
	}
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "snapshot"}); err == nil {
		t.Fatal("missing renderer")
	}
	ctx, finish := r.beginRoomReply(context.Background(), cfg, tool.event)
	r.SetRoomSnapshotRenderer(func(context.Context, RoomState) ([]byte, error) { finish(nil); return []byte("test"), nil })
	if _, err := tool.Run(ctx, map[string]any{"operation": "snapshot"}); err == nil {
		t.Fatal("late render sent")
	}
	r.SetRoomSnapshotRenderer(func(context.Context, RoomState) ([]byte, error) {
		cfg.OwnSpaceEnabled = boolPointer(false)
		r.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
		return []byte("test"), nil
	})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "snapshot"}); err == nil {
		t.Fatal("disabled during rendering sent")
	}
	if len(ch.sent) != 0 {
		t.Fatal("unexpected send")
	}
}
