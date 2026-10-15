package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRSSWatchTwitterFeedKeepsPhotoURLs(t *testing.T) {
	body, err := os.ReadFile("testdata/fxtwitter_animecorner.json")
	if err != nil {
		t.Fatal(err)
	}
	feed, err := parseTwitterStatusesFeed(body)
	if err != nil {
		t.Fatal(err)
	}
	var keyVisual rssWatchItem
	for _, item := range feed.Items {
		if item.ID == "2110448153912840594" {
			keyVisual = item
		}
	}
	if len(keyVisual.Images) != 1 || !strings.HasPrefix(keyVisual.Images[0], "https://pbs.twimg.com/media/") {
		t.Fatalf("key visual images = %#v", keyVisual.Images)
	}
}

func TestRSSWatchReplyImagesFollowLinkedItems(t *testing.T) {
	items := []rssWatchItem{
		{ID: "2110448153912840594", Link: "https://x.com/animecorner_ac/status/2110448153912840594", Images: []string{"https://pbs.twimg.com/media/A.jpg?name=orig"}},
		{ID: "2110447009954242886", Link: "https://x.com/animecorner_ac/status/2110447009954242886"},
		{ID: "2110389043280372028", Link: "https://x.com/animecorner_ac/status/2110389043280372028", Images: []string{"https://pbs.twimg.com/media/B.jpg", "https://pbs.twimg.com/media/C.jpg", "https://pbs.twimg.com/media/D.jpg"}},
	}
	reply := "赛博朋克：边缘行者 2 主视觉公布 https://x.com/animecorner_ac/status/2110448153912840594"
	got := rssWatchReplyImages(reply, items)
	if got != "\n[CQ:image,file=https://pbs.twimg.com/media/A.jpg?name=orig]" {
		t.Fatalf("images = %q", got)
	}
	// 推特链接被改写成 twitter.com 也认得出：按 ID 匹配。每条最多两张。
	got = rssWatchReplyImages("黑色五叶草 https://twitter.com/i/status/2110389043280372028", items)
	if strings.Count(got, "[CQ:image") != 2 {
		t.Fatalf("per-item cap: %q", got)
	}
	if rssWatchReplyImages("没有链接的多条汇总", items) != "" {
		t.Fatal("unmatched multi-item batch must not attach images")
	}
	if strings.Count(rssWatchReplyImages("只有一条", items[:1]), "[CQ:image") != 1 {
		t.Fatal("single-item batch should attach its images")
	}
}

func TestRSSWatchReplyImagesRejectsForeignHostsAndEscapes(t *testing.T) {
	items := []rssWatchItem{{ID: "1234567890", Images: []string{
		"https://evil.example/x.jpg",
		"http://pbs.twimg.com/media/plain.jpg",
		"https://pbs.twimg.com/media/X.jpg?a=1&b=2,],[CQ:at,qq=all]",
	}}}
	got := rssWatchReplyImages("1234567890", items)
	if strings.Count(got, "[CQ:") != 1 || strings.Contains(got, "evil") || strings.Contains(got, "qq=all]") {
		t.Fatalf("unsafe output %q", got)
	}
	segments := TextToOneBotSegments("正文" + got)
	images := 0
	for _, segment := range segments {
		if segment.Type == "at" {
			t.Fatalf("injected mention: %#v", segments)
		}
		if segment.Type == "image" {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("segments = %#v", segments)
	}
}

func TestRSSWatchNoticeWithImageBecomesImageSegment(t *testing.T) {
	text := "Twitter @animecorner_ac\n赛博朋克：边缘行者 2 主视觉公布\n订阅 ec02c05a" +
		rssWatchReplyImages("x", []rssWatchItem{{ID: "2110448153912840594", Images: []string{"https://pbs.twimg.com/media/HUnUbt9XgAA-hEo.jpg?name=orig"}}})
	var found bool
	for _, chunk := range splitReply(text, notificationChunkSize) {
		for _, segment := range sendableOneBotSegments(TextToOneBotSegments(chunk)) {
			if segment.Type == "image" && segment.Data["file"] == "https://pbs.twimg.com/media/HUnUbt9XgAA-hEo.jpg?name=orig" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("image segment not produced")
	}
}

func TestRSSWatchReplyImagesMatchLinksQuotedFromContent(t *testing.T) {
	items := []rssWatchItem{
		{ID: "1", Content: "Kaze Tsugu movie! https://atani.me/kaze-tsugu-theatrical-new-visual", Images: []string{"https://pbs.twimg.com/media/K.jpg"}},
		{ID: "2", Content: "Yumeconne anime https://atani.me/yumeconne-anime.", Images: []string{"https://pbs.twimg.com/media/Y.jpg"}},
		{ID: "3", Content: "Merch https://atani.me/merch", Images: []string{"https://pbs.twimg.com/media/M.jpg"}},
	}
	reply := "《风之继承》剧场版 https://atani.me/kaze-tsugu-theatrical-new-visual\n《梦之连结》TV 动画化 https://atani.me/yumeconne-anime"
	got := rssWatchReplyImages(reply, items)
	if !strings.Contains(got, "K.jpg") || !strings.Contains(got, "Y.jpg") || strings.Contains(got, "M.jpg") {
		t.Fatalf("images = %q", got)
	}
}

func TestRSSWatchImagesAvoidAmbiguousCitations(t *testing.T) {
	items := []rssWatchItem{
		{ID: "123456789", Link: "https://x.com/a/status/123456789", Content: "https://news.example/shared https://news.example/story", Images: []string{"https://pbs.twimg.com/media/A.jpg"}},
		{ID: "987654321", Link: "https://x.com/b/status/987654321", Content: "https://news.example/shared", Images: []string{"https://pbs.twimg.com/media/B.jpg"}},
	}
	for _, reply := range []string{"https://news.example/shared", "https://news.example/story-other", "https://x.com/a/status/1234567890", "https://evil.example/a/status/123456789", "123456789"} {
		if got := rssWatchReplyImages(reply, items); got != "" {
			t.Errorf("%q attached %q", reply, got)
		}
	}
	if got := rssWatchReplyImages("https://twitter.com/i/status/123456789", items); !strings.Contains(got, "A.jpg") || strings.Contains(got, "B.jpg") {
		t.Fatal(got)
	}
}

func TestRSSWatchImagesDeduplicateAndCap(t *testing.T) {
	items := []rssWatchItem{
		{Link: "https://x.com/a/status/1", Images: []string{"https://pbs.twimg.com/media/A.jpg", "https://pbs.twimg.com/media/A.jpg", "https://pbs.twimg.com/media/B.jpg", "https://pbs.twimg.com/media/C.jpg"}},
		{Link: "https://x.com/a/status/2", Images: []string{"https://pbs.twimg.com/media/A.jpg", "https://pbs.twimg.com/media/D.jpg", "https://pbs.twimg.com/media/E.jpg"}},
		{Link: "https://x.com/a/status/3", Images: []string{"https://pbs.twimg.com/media/F.jpg"}},
	}
	got := rssWatchReplyImages("https://x.com/a/status/1 https://x.com/a/status/2 https://x.com/a/status/3", items)
	if strings.Count(got, "[CQ:image") != 4 || strings.Count(got, "A.jpg") != 1 || strings.Contains(got, "C.jpg") || strings.Contains(got, "F.jpg") {
		t.Fatal(got)
	}
	for _, raw := range []string{"https://pbs.twimg.com:8443/a", "https://user@pbs.twimg.com/a", "https://pbs.twimg.com.evil.example/a"} {
		if rssWatchImageAllowed(raw) {
			t.Errorf("allowed %q", raw)
		}
	}
}

func TestRSSWatchImagesExcludedFromJudge(t *testing.T) {
	payload, err := json.Marshal(rssWatchItem{ID: "1", Images: []string{"https://pbs.twimg.com/media/private.jpg"}})
	if err != nil || strings.Contains(string(payload), "pbs.twimg.com") {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
}

func TestRSSWatchImageNoticeDelivery(t *testing.T) {
	for _, platform := range []string{PlatformOneBotV11, PlatformTelegram} {
		t.Run(platform, func(t *testing.T) {
			channel := &recordingChannel{}
			cfg := BotConfig{ID: "a", Platform: platform, SendRetryAttempts: 1}
			r := NewRuntime(cfg, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Platform: platform, ProfileID: "a", UserID: "100"}
			notice := "New visual" + rssWatchReplyImages("x", []rssWatchItem{{Images: []string{"https://pbs.twimg.com/media/A.jpg"}}})
			if err := r.sendSubscriberNotice(withSubscriptionPush(context.Background(), subscriptionPushRSSWatch), event, notice); err != nil {
				t.Fatal(err)
			}
			textFound, imageFound := false, false
			for _, msg := range channel.sentSnapshot() {
				textFound = textFound || strings.Contains(msg.Text, "New visual")
				for _, raw := range msg.ImageURLs {
					imageFound = imageFound || raw == "https://pbs.twimg.com/media/A.jpg"
				}
			}
			if !textFound || !imageFound {
				t.Fatalf("missing text or image: %#v", channel.sentSnapshot())
			}
		})
	}
}

type rssImageFailureChannel struct{ recordingChannel }

func (c *rssImageFailureChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if len(msg.ImageURLs) > 0 {
		return nil, errors.New("photo unavailable")
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func TestRSSWatchTelegramImageFailureKeepsTextAndReportsError(t *testing.T) {
	channel := &rssImageFailureChannel{}
	r := NewRuntime(BotConfig{ID: "a", Platform: PlatformTelegram, SendRetryAttempts: 1}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Platform: PlatformTelegram, ProfileID: "a", UserID: "100"}
	notice := "New visual" + rssWatchReplyImages("x", []rssWatchItem{{Images: []string{"https://pbs.twimg.com/media/A.jpg"}}})
	err := r.sendSubscriberNotice(withSubscriptionPush(context.Background(), subscriptionPushRSSWatch), event, notice)
	if err == nil {
		t.Fatal("image failure was hidden")
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].Text != "New visual" {
		t.Fatalf("text delivery = %#v", sent)
	}
}

func TestRSSWatchRepostsAndQuotesKeepPhotos(t *testing.T) {
	body := []byte(`{"code":200,"results":[
  {"id":"11111111","url":"https://x.com/original/status/11111111","text":"Original visual","reposted_by":{"screen_name":"subscriber"},"media":{"photos":[{"url":"https://pbs.twimg.com/media/repost.jpg"}]}},
  {"id":"22222222","url":"https://x.com/commenter/status/22222222","text":"Look at this","quote":{"url":"https://x.com/artist/status/33333333","raw_text":{"text":"Quoted visual"},"author":{"screen_name":"artist"},"media":{"photos":[{"url":"https://pbs.twimg.com/media/quote.jpg"}]},"quote":{"text":"Nested","media":{"photos":[{"url":"https://pbs.twimg.com/media/nested.jpg"}]}}}},
  {"id":"44444444","text":"Own and quoted photos","media":{"photos":[{"url":"https://pbs.twimg.com/media/own.jpg"}]},"quote":{"url":"https://x.com/artist/status/55555555","text":"Quoted photos","media":{"photos":[{"url":"https://pbs.twimg.com/media/own.jpg"},{"url":"https://pbs.twimg.com/media/quote.jpg"},{"url":"https://pbs.twimg.com/media/extra.jpg"}]}}},
  {"id":"66666666","text":"Unavailable quote","quote":null}
 ]}`)
	feed, err := parseTwitterStatusesFeed(body)
	if err != nil || len(feed.Items) != 4 {
		t.Fatalf("feed=%#v err=%v", feed, err)
	}
	if got := rssWatchReplyImages("https://x.com/original/status/11111111", feed.Items); !strings.Contains(got, "repost.jpg") || strings.Contains(got, "quote.jpg") {
		t.Fatal(got)
	}
	quoted := feed.Items[1]
	if !strings.Contains(quoted.Content, "引用推文 @artist\nQuoted visual\nhttps://x.com/artist/status/33333333") || strings.Contains(quoted.Content, "Nested") {
		t.Fatal(quoted.Content)
	}
	if len(quoted.Images) != 1 || !strings.Contains(quoted.Images[0], "quote.jpg") {
		t.Fatal(quoted.Images)
	}
	if got := rssWatchReplyImages("https://x.com/artist/status/33333333", feed.Items); !strings.Contains(got, "quote.jpg") || strings.Contains(got, "repost.jpg") {
		t.Fatal(got)
	}
	got := rssWatchReplyImages("single item", feed.Items[2:3])
	if strings.Count(got, "[CQ:image") != 2 || !strings.Contains(got, "own.jpg") || !strings.Contains(got, "quote.jpg") || strings.Contains(got, "extra.jpg") {
		t.Fatal(got)
	}
	if len(feed.Items[3].Images) != 0 {
		t.Fatal(feed.Items[3].Images)
	}
}
