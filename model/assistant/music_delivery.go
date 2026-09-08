package assistant

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

func musicLinkReply(item song) string {
	link := musicPageURL(item)
	if link == "" {
		return item.Title() + "\n当前曲库未提供可分享的歌曲页面"
	}
	return "🎵 " + item.Title() + "\n" + link
}

func musicPageURL(item song) string {
	var link string
	switch item.Source {
	case "netease":
		link = "https://music.163.com/song?id=" + url.QueryEscape(item.ID)
	case "qq":
		link = "https://y.qq.com/n/ryqq/songDetail/" + url.PathEscape(item.ID)
	case "kugou":
		hash, album := kugouSplitSongID(item.ID)
		link = "https://www.kugou.com/song/#" + url.Values{"hash": {hash}, "album_id": {album}}.Encode()
	}
	return link
}

var outgoingCQRecord = regexp.MustCompile(`\[CQ:record(?:,[^\]]*)?\]`)

// Only resolve files already registered by the media sharer. Never let a model
// supply an arbitrary local path through a CQ string and upload it to Telegram.
func (r *Runtime) prepareTelegramAudio(msg OutgoingMessage) (OutgoingMessage, error) {
	if NormalizePlatformID(msg.Platform) != PlatformTelegram {
		return msg, nil
	}
	r.mu.RLock()
	resolver, _ := r.localMedia.(LocalMediaPathResolver)
	r.mu.RUnlock()
	var audioErr error
	msg.AudioURLs = append([]string(nil), msg.AudioURLs...)
	msg.Text = outgoingCQRecord.ReplaceAllStringFunc(msg.Text, func(code string) string {
		segments := CQToSegments(code)
		if len(segments) != 1 || resolver == nil {
			audioErr = fmt.Errorf("telegram: 音频共享文件不可用")
			return ""
		}
		path, ok := resolver.ResolveSharedPath(segments[0].Data["file"])
		if !ok {
			audioErr = fmt.Errorf("telegram: 音频共享文件已失效或不属于本机媒体库")
			return ""
		}
		msg.AudioURLs = append(msg.AudioURLs, path)
		return ""
	})
	msg.Text = strings.TrimSpace(msg.Text)
	return msg, audioErr
}
