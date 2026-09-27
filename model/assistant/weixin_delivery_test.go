// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// 对方没发过消息就没有 context_token，iLink 发不出去。提醒、主人通知这类主动
// 消息要拿到一句说得清的错误，而不是去打接口再收一个笼统的失败。
func TestWeixinSendWithoutContextTokenExplains(t *testing.T) {
	fake := newWeixinFakeServer(t)
	channel := newTestWeixinChannel(fake, t.TempDir())
	err := channel.Send(context.Background(), OutgoingMessage{UserID: "stranger@im.wechat", Text: "提醒你开会"})
	if !errors.Is(err, ErrWeixinNoContext) || !strings.Contains(err.Error(), "先给机器人发一条消息") {
		t.Fatalf("send without context = %v, want ErrWeixinNoContext", err)
	}
	if len(fake.sent()) != 0 {
		t.Fatal("sendmessage was called without a context_token")
	}
}

// 同一联系人先发图再发字：图片下载再慢，交给运行时的顺序也不能反过来；
// 另一个联系人不用排在后面等。
func TestWeixinKeepsPerUserOrderWhileOtherUsersRunInParallel(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	fake := newWeixinFakeServer(t)
	key := []byte("0123456789abcdef")
	cipher, err := weixinEncryptAESECB(testPNG(t), key)
	if err != nil {
		t.Fatal(err)
	}
	fake.cdnBody = cipher
	fake.cdnDelay = 300 * time.Millisecond
	fake.push(`{"ret":0,"get_updates_buf":"c1","msgs":[` +
		`{"message_id":1,"from_user_id":"alice@im.wechat","item_list":[{"type":2,"image_item":{"aeskey":"` + hex.EncodeToString(key) +
		`","media":{"full_url":"` + fake.server.URL + `/cdn/download"}}}]},` +
		`{"message_id":2,"from_user_id":"alice@im.wechat","item_list":[{"type":1,"text_item":{"text":"看上面那张"}}]},` +
		`{"message_id":3,"from_user_id":"bob@im.wechat","item_list":[{"type":1,"text_item":{"text":"我先到"}}]}]}`)
	channel := newTestWeixinChannel(fake, t.TempDir())
	var mu sync.Mutex
	var order []string
	runWeixin(t, channel, func(_ context.Context, event MessageEvent) error {
		mu.Lock()
		order = append(order, event.MessageID)
		mu.Unlock()
		return nil
	})
	waitWeixin(t, "all three delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) == 3
	})
	mu.Lock()
	defer mu.Unlock()
	if order[0] != "3" {
		t.Fatalf("order = %v: bob waited behind alice's slow image", order)
	}
	if order[1] != "1" || order[2] != "2" {
		t.Fatalf("order = %v: alice's text overtook her image", order)
	}
}

// 游标只在这批消息交给 handler 之后才落盘：handler 还卡着时进程退出，重启要能
// 从旧游标把这批消息再收一遍。
func TestWeixinPersistsCursorOnlyAfterHandlerTookTheBatch(t *testing.T) {
	fake := newWeixinFakeServer(t)
	fake.push(`{"ret":0,"get_updates_buf":"cursor-after-batch","msgs":[` +
		`{"message_id":1,"from_user_id":"alice@im.wechat","item_list":[{"type":1,"text_item":{"text":"hi"}}]}]}`)
	channel := newTestWeixinChannel(fake, t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runWeixin(t, channel, func(context.Context, MessageEvent) error {
		close(entered)
		<-release
		return nil
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler was never called")
	}
	time.Sleep(100 * time.Millisecond)
	if raw, err := os.ReadFile(channel.statePath()); err == nil && strings.Contains(string(raw), "cursor-after-batch") {
		t.Fatal("cursor was persisted while the batch was still being handled")
	}
	fake.mu.Lock()
	polls := len(fake.cursors)
	fake.mu.Unlock()
	if polls != 1 {
		t.Fatalf("polled %d times while the batch was in flight, want 1", polls)
	}
	close(release)
	waitWeixin(t, "cursor persisted after handoff", func() bool {
		raw, err := os.ReadFile(channel.statePath())
		return err == nil && strings.Contains(string(raw), "cursor-after-batch")
	})
}
