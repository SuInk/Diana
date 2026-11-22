package assistant

import (
	"context"
	"os"
	"testing"
)

// 引用机器人自己发的图时，那条图常常不在历史库里（图文连发被 Telegram 拆成两条、
// 只记了第一条）。引用里的 file_id 要在收消息时落成本地文件，history_media 才能
// 直接用当前这条引用取到原图，而不是报「当前会话中找不到这条消息」。
func TestTelegramQuotedMediaIsDownloadedForHistoryMedia(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	api := newFakeTelegramAPI(t, map[string]any{
		"getFile": map[string]any{"file_path": "photos/quoted.jpg"}, "download:photos/quoted.jpg": []byte("quoted image bytes"),
	})
	msg := &telegramMessage{
		MessageID: 20406,
		Chat:      &telegramChat{ID: -10044, Type: "supergroup"},
		From:      &telegramUser{ID: 55071, FirstName: "Kongtou"},
		Text:      "这个是什么工具",
		ReplyTo: &telegramMessage{
			MessageID: 19577,
			Chat:      &telegramChat{ID: -10044, Type: "supergroup"},
			From:      &telegramUser{ID: 89168, FirstName: "Diana", IsBot: true},
			Photo:     []telegramPhoto{{FileID: "quoted-photo", FileUniqueID: "quoted-unique", FileSize: 18}},
		},
	}
	event := api.channel().resolveIncomingMedia(context.Background(), telegramMessageToEvent(msg, "89168", "dianabot"), msg)
	if event.Quoted == nil || len(event.Quoted.Segments) != 1 {
		t.Fatalf("quoted = %#v", event.Quoted)
	}
	cached := event.Quoted.Segments[0].Data["cached_file"]
	if body, err := os.ReadFile(cached); err != nil || string(body) != "quoted image bytes" {
		t.Fatalf("quoted media not cached: path=%q err=%v", cached, err)
	}

	tool := &dianaHistoryImagesTool{runtime: &Runtime{}, event: event}
	source, found, _ := tool.findSourceEvent(context.Background(), "19577")
	if !found || len(historicalToolImageRefs(source)) != 1 {
		t.Fatalf("history_media cannot use the quote: found=%v source=%#v", found, source)
	}
}
