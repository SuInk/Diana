// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"strings"
	"time"
)

// QQ 的头像 CDN 回 Cache-Control: max-age=2592000——整整 30 天。地址本身是固定的
// （p.qlogo.cn/gh/<群号>/<群号>/640、q1.qlogo.cn/g?nk=<QQ号>），所以换了头像之后
// 控制台里那张旧图能挂一个月，看着就像「头像从来不更新」。
//
// 第三方 CDN 的响应头改不了，唯一的办法是换地址。挂一个按小时变的参数：同一小时
// 内浏览器照常吃缓存，跨小时就是一条新地址，必须重新取。实测带上多余参数不影响
// 取图（两种地址都照常 200）。
//
// 用小时不用天：改完头像等一天才看得到，和不更新没什么区别。
func freshAvatarURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "http") {
		return raw
	}
	separator := "?"
	if strings.Contains(raw, "?") {
		separator = "&"
	}
	return raw + separator + "t=" + time.Now().UTC().Format("2006010215")
}
