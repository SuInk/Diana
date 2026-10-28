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
	}
	return link
}

var outgoingCQRecord = regexp.MustCompile(`\[CQ:record(?:,[^\]]*)?\]`)

// prepareChannelAudio 把正文里的 [CQ:record] 换成本机音频文件，交给不认 CQ 码的
// 渠道（Telegram、QQ 官方）按各自的方式发：语音合成和点歌产出的都是这种 CQ 码。
//
// Only resolve files already registered by the media sharer. Never let a model
// supply an arbitrary local path through a CQ string and upload it.
func (r *Runtime) prepareChannelAudio(msg OutgoingMessage) (OutgoingMessage, error) {
	platform := NormalizePlatformID(msg.Platform)
	if (platform != PlatformTelegram && platform != PlatformQQOfficial) || msg.LiteralText {
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
			audioErr = fmt.Errorf("%s: 音频共享文件不可用", platform)
			return ""
		}
		path, ok := resolver.ResolveSharedPath(segments[0].Data["file"])
		if !ok {
			audioErr = fmt.Errorf("%s: 音频共享文件已失效或不属于本机媒体库", platform)
			return ""
		}
		msg.AudioURLs = append(msg.AudioURLs, path)
		return ""
	})
	msg.Text = strings.TrimSpace(msg.Text)
	return msg, audioErr
}
