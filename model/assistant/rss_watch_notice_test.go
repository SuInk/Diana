package assistant

import (
	"strings"
	"testing"
)

// 抬头要说清「哪个平台、哪个号」：平台一直存在 FeedSource 里，以前没写出来，
// 推特订阅也显示成「RSS 订阅」。
func TestRSSWatchNoticeHeaderNamesPlatformAndAccount(t *testing.T) {
	for _, tc := range []struct {
		name     string
		item     Reminder
		feedName string
		want     string
	}{
		{
			name:     "推特带显示名",
			item:     Reminder{FeedSource: "twitter", FeedHandle: "thsottiaux"},
			feedName: "Thomas Sottiaux",
			want:     "Twitter Thomas Sottiaux @thsottiaux",
		},
		{
			// RSSHub、Nitter 的标题格式各不相同，标题里已经带 @handle 时不重复。
			name:     "标题已含 handle",
			item:     Reminder{FeedSource: "twitter", FeedHandle: "thsottiaux"},
			feedName: "Thomas Sottiaux / @thsottiaux",
			want:     "Twitter Thomas Sottiaux / @thsottiaux",
		},
		{
			name: "推特没有标题",
			item: Reminder{FeedSource: "twitter", FeedHandle: "thsottiaux"},
			want: "Twitter @thsottiaux",
		},
		{
			name:     "普通 RSS 用 Feed 标题",
			item:     Reminder{FeedSource: "rss", FeedURL: "https://example.test/feed.xml"},
			feedName: "少数派",
			want:     "RSS 少数派",
		},
		{
			name: "连标题都没有就退回地址",
			item: Reminder{FeedSource: "rss", FeedURL: "https://example.test/feed.xml"},
			want: "RSS https://example.test/feed.xml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rssWatchNoticeHeader(tc.item, tc.feedName); got != tc.want {
				t.Fatalf("header = %q, want %q", got, tc.want)
			}
		})
	}
}

// 整条通知只发一条：抬头、正文、订阅 ID 之间不能再有「换一条消息发」的标记。
func TestRSSWatchNoticeIsASingleMessage(t *testing.T) {
	notice := "Twitter @thsottiaux\n博主发布了消息\n订阅 7c53d9d7"
	if strings.Contains(notice, notificationSplitMarker) {
		t.Fatal("通知里不该再有分条标记")
	}
	if chunks := splitReply(notice, notificationChunkSize); len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
}
