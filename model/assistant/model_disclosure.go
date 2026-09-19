// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// ModelDisclosure 决定谁能问出机器人背后挂的是哪个模型。群里随便一个人一问就
// 报出模型 ID 和供应商，对不少部署来说是不想公开的运维信息。
type ModelDisclosure string

const (
	// ModelDisclosureOwner 只对主人如实报模型，其他人一律不透露。主人总得能看、
	// 能改，所以没有「连主人也不告诉」这一档。
	ModelDisclosureOwner ModelDisclosure = "owner"
	// ModelDisclosureEveryone 谁问都如实报。
	ModelDisclosureEveryone ModelDisclosure = "everyone"
)

// normalizeModelDisclosure 没写或写错都按 owner 处理：默认收紧，想公开得显式打开。
func normalizeModelDisclosure(mode ModelDisclosure) ModelDisclosure {
	if ModelDisclosure(strings.ToLower(strings.TrimSpace(string(mode)))) == ModelDisclosureEveryone {
		return ModelDisclosureEveryone
	}
	return ModelDisclosureOwner
}

// modelDisclosedTo 判断本轮发言者能不能拿到模型信息。
func modelDisclosedTo(cfg BotConfig, owner bool) bool {
	return owner || normalizeModelDisclosure(cfg.ModelDisclosure) == ModelDisclosureEveryone
}
