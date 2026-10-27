// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// QQ 官方机器人的原生 Markdown 认一种图集节点：一串 mqqapi://markdown/node 空链接，
// 客户端把它们渲染成九宫格。官方文档没写，是群友实测出来的写法。一次发好几张图
// （抖音图集、搜图结果）时合成一条，比逐张上传刷屏好得多，也省被动回复的条数。
//
// 节点里填的是图片地址，由 QQ 自己去拉，所以只有公网 http(s) 地址能用；本机分享的
// 127.0.0.1、内网地址和 data: 图照旧逐张上传。
//
// 实测普通账号会被拒（40034028「请求参数不允许包含url mqqapi://markdown/node」），
// 应该要额外开通；拒过一次通道就记住，不再每次多图都先白发一条。
const (
	qqGalleryRefusedErrorCode = "40034028"

	qqGalleryMinImages   = 2
	qqGalleryMaxImages   = 20
	qqGalleryItemsPerRow = 3
	qqGalleryNodeID      = "dianaPics"
)

// qqOfficialGalleryMarkdown 把一组图片地址拼成图集节点；有任何一张用不了就返回
// false，整组退回逐张上传，不拆成半边图集半边单图。
func qqOfficialGalleryMarkdown(images []string) (string, bool) {
	if len(images) < qqGalleryMinImages || len(images) > qqGalleryMaxImages {
		return "", false
	}
	for _, image := range images {
		if !qqGalleryURLUsable(image) {
			return "", false
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[](mqqapi://markdown/node?nodeType=replace&nodeID=%s)", qqGalleryNodeID)
	fmt.Fprintf(&b, "[](mqqapi://markdown/node?nodeType=multiPic&nodeID=%s&state=start&itemsPerRow=%d&itemsNum=%d)", qqGalleryNodeID, qqGalleryItemsPerRow, len(images))
	for index, image := range images {
		fmt.Fprintf(&b, "[](mqqapi://markdown/node-data?nodeID=%s&index=%d&picUrl=%s)", qqGalleryNodeID, index, qqGalleryPicURL(image))
	}
	fmt.Fprintf(&b, "[](mqqapi://markdown/node?nodeType=multiPic&nodeID=%s&state=end)", qqGalleryNodeID)
	return b.String(), true
}

// qqGalleryPicURL 把图片地址放进节点参数。地址自己的 ? 和 & 会和节点参数混在一起，
// 必须编码。
func qqGalleryPicURL(image string) string {
	return url.QueryEscape(strings.TrimSpace(image))
}

// qqGalleryURLUsable 判断 QQ 能不能自己拉到这张图：公网 http(s) 地址，主机不是
// localhost、回环或内网 IP。markdown 链接里不能有圆括号和空白，有就不用图集。
func qqGalleryURLUsable(image string) bool {
	image = strings.TrimSpace(image)
	if strings.ContainsAny(image, " ()\n\t") {
		return false
	}
	parsed, err := url.Parse(image)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
	}
	return strings.Contains(host, ".")
}
