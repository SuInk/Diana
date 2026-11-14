// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/url"
	"strings"
)

// qqImageURLAppIDs 是 NTQQ 下载地址里表示图片的 appid：1406 私聊图片、1407 群图片。
// 语音、视频、文件走别的 appid，不当图片读。
var qqImageURLAppIDs = map[string]bool{"1406": true, "1407": true}

// isQQImageURL 认出 QQ 的图片下载地址。
func isQQImageURL(raw string) bool {
	if brokenMediaURL(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case host == "multimedia.nt.qq.com.cn":
		return qqImageURLAppIDs[parsed.Query().Get("appid")]
	case host == "qpic.cn" || strings.HasSuffix(host, ".qpic.cn"),
		host == "ugcimg.cn" || strings.HasSuffix(host, ".ugcimg.cn"):
		return true
	}
	return false
}

// promoteTextQQImageURLs 把正文里贴出来的 QQ 图片地址换成图片段。
//
// 有人（多半是别的机器人）把图片的下载地址当文字发出来时，模型只看到一串带
// rkey 的网址，链接解析还会把它当网页去抓。换成图片段后它就和普通发图一样：
// 走识图、进图片描述缓存，正文里也不再留这串一次性地址。
func promoteTextQQImageURLs(segments []MessageSegment) []MessageSegment {
	existing := map[string]bool{}
	for _, segment := range segments {
		if segment.Type == "image" {
			existing[strings.TrimSpace(segment.Data["url"])] = true
		}
	}
	var out []MessageSegment
	changed := false
	for index, segment := range segments {
		if segment.Type != "text" {
			if changed {
				out = append(out, segment)
			}
			continue
		}
		text := segment.Data["text"]
		var images []MessageSegment
		rewritten := urlPattern.ReplaceAllStringFunc(text, func(match string) string {
			link := normalizeResolverURL(match)
			if !isQQImageURL(link) {
				return match
			}
			if !existing[link] {
				existing[link] = true
				images = append(images, MessageSegment{Type: "image", Data: map[string]string{"url": link, "file": link}})
			}
			// 正则连带吃进去的句尾标点留在正文里，和 normalizeResolverURL 的裁剪一致。
			return match[len(strings.TrimRight(match, ".,;:!?)]}\\")):]
		})
		if rewritten == text {
			if changed {
				out = append(out, segment)
			}
			continue
		}
		if !changed {
			out = append(make([]MessageSegment, 0, len(segments)+len(images)), segments[:index]...)
			changed = true
		}
		if trimmed := strings.TrimSpace(rewritten); trimmed != "" {
			data := make(map[string]string, len(segment.Data))
			for key, value := range segment.Data {
				data[key] = value
			}
			data["text"] = trimmed
			out = append(out, MessageSegment{Type: "text", Data: data})
		}
		out = append(out, images...)
	}
	if !changed {
		return segments
	}
	return out
}
