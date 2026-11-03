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

// musicSendsCard 判断这次该不该发音乐卡片：只有 OneBot 认 music 段，Telegram 和
// QQ 官方照旧发音频文件。
func musicSendsCard(cfg musicConfig, platform string) bool {
	return cfg.SendAsCard && IsOneBotPlatform(platform)
}

// musicCardCode 拼一张自定义音乐卡片。用 custom 而不是 type=qq/163 加歌曲 ID 的
// 平台卡片：平台卡片由签名服务自己去取播放地址，不带我们的会员 Cookie，会员歌
// 只能试听；custom 直接塞已经取到的地址，填了会员 Cookie 就是整首、原音质。
// 代价是地址来自曲库 CDN，会过期（网易云官方接口给的通常只有二十来分钟）。
//
// 签名在 OneBot 端做（SnowLuma 的 musicSignUrl），这里只给字段；签不出来时
// SnowLuma 会退回一张未签名的本地卡片。
func musicCardCode(item song) string {
	fields := []string{
		"type=custom",
		"url=" + escapeCQParameter(musicCardURL(firstNonEmpty(musicPageURL(item), item.PlayURL))),
		"audio=" + escapeCQParameter(musicCardURL(item.PlayURL)),
		"title=" + escapeCQParameter(firstNonEmpty(strings.TrimSpace(item.Name), item.Title())),
	}
	if artists := strings.TrimSpace(item.Artists); artists != "" {
		fields = append(fields, "content="+escapeCQParameter(artists))
	}
	return "[CQ:music," + strings.Join(fields, ",") + "]"
}

// musicCardURL 把地址里的 + 写成 %2B。签名服务（SnowLuma 默认用的 xingzhige）会按
// 表单规则解码字段，原样的 + 变成空格：网易云 CDN 地址的 vuutv 参数是 base64，
// 一首歌常有好几个 +，签出来的 musicUrl 就成了带裸空格的坏地址。%2B 经它解码后
// 正好还原成 +，CDN 两种写法都认。2026-11-03 在 miku 上实测。
func musicCardURL(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), "+", "%2B")
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
