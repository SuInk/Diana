// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"net/url"
	"path"
	"strings"
)

// dropBrokenDuplicateImages 去掉接入端多发的一份残缺图片段。
//
// SnowLuma（snowluma-voice v1.14.10）偶尔把同一张图在一条消息里发两遍：第一遍地址
// 完整（…/download?appid=1407&fileid=…&rkey=…），第二遍丢了路径和 fileid，变成
// https://multimedia.nt.qq.com.cn&rkey=…&spec=0，主机名都解析不了。11-06～11-11 生产
// 15 条消息全是这个样子，两遍的文件名前缀相同（扩展名有时一个 .jpeg 一个 .jpg）。
// 读第二遍必然失败；@ 机器人带图的那条因此整条回复失败，只发出一条错误说明。
//
// 只丢「地址残缺、且同一条消息里另有同一个文件」的那一段：图其实读得到，丢的只是
// 坏掉的重复。只有残缺那一份时原样保留，读取失败照常报出来，不把问题藏掉。
func dropBrokenDuplicateImages(segments []MessageSegment) []MessageSegment {
	healthy := map[string]bool{}
	for _, segment := range segments {
		if segment.Type != "image" || brokenMediaURL(segment.Data["url"]) {
			continue
		}
		if stem := imageFileStem(segment.Data["file"]); stem != "" {
			healthy[stem] = true
		}
	}
	if len(healthy) == 0 {
		return segments
	}
	out := segments[:0:0]
	for _, segment := range segments {
		if segment.Type == "image" && brokenMediaURL(segment.Data["url"]) && healthy[imageFileStem(segment.Data["file"])] {
			continue
		}
		out = append(out, segment)
	}
	return out
}

// brokenMediaURL 认出主机名后面直接跟了查询参数、没有路径的地址。空地址不算：
// 很多图片段本来就只给 file，靠 get_image 取。
func brokenMediaURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host == "" || strings.ContainsAny(parsed.Host, "&=?")
}

// imageFileStem 是图片段 file 去掉扩展名后的部分：同一张图两遍的扩展名可能一个
// .jpeg 一个 .jpg。
func imageFileStem(file string) string {
	file = strings.ToLower(strings.TrimSpace(file))
	if file == "" {
		return ""
	}
	return strings.TrimSuffix(file, path.Ext(file))
}
