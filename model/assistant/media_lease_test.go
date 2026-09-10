package assistant

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaLeaseRenewsOriginalLinkAndDefersCleanup(t *testing.T) {
	store := NewLocalMediaStore("http://localhost/media")
	now := time.Now()
	store.now = func() time.Time { return now }
	if err := store.SetIndexDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	link, ok := store.Share(path, 10*time.Minute)
	if !ok {
		t.Fatal("share")
	}
	refresh, release, known, err := store.acquireShare(link)
	if err != nil || !known {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Minute)
	cleanupLocalMediaFile(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("deleted during send")
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	token := filepath.Base(u.Path)
	w := httptest.NewRecorder()
	store.ServeToken(w, httptest.NewRequest("GET", link, nil), token)
	if w.Code != 200 || w.Body.String() != "image" {
		t.Fatalf("retry link: %d %s", w.Code, w.Body.String())
	}
	release()
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pending cleanup did not run")
	}
}

func TestMediaWaitDoesNotBlockTextAndCanCancel(t *testing.T) {
	r := NewRuntime(BotConfig{}, newScriptedBackoffChannel("123456"), NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456"}
	gate := r.groupOutboundDelivery(event, "media")
	gate.mu.Lock()
	defer gate.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := r.executeOutboundCall(ctx, event, "send_group_msg", func(context.Context) (map[string]any, error) { return map[string]any{"message_id": 1}, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(outboundMessageContext(context.Background(), OutgoingMessage{ImageURLs: []string{"image"}}), 20*time.Millisecond)
	defer cancel2()
	_, err = r.executeOutboundCall(ctx2, event, "send_group_msg", func(context.Context) (map[string]any, error) { t.Error("unexpected send"); return nil, nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
