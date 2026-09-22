// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// RepositoryDisclosure 决定谁能问出这台机器人跑的是哪个开源项目。地址本身是公开
// 的，但「这个群里的机器人就是那个项目」这件事不是：知道地址就知道去哪看默认提
// 示词、默认人设和全部工具实现，私下部署的人未必愿意让群里随便一个人顺着链接摸
// 过来。
type RepositoryDisclosure string

const (
	// RepositoryDisclosureOwner 只对主人报项目地址。主人在控制台本来就看得到，
	// 所以没有「连主人也不告诉」这一档。
	RepositoryDisclosureOwner RepositoryDisclosure = "owner"
	// RepositoryDisclosureEveryone 谁问都给。
	RepositoryDisclosureEveryone RepositoryDisclosure = "everyone"
)

// normalizeRepositoryDisclosure 没写或写错都按 owner 处理：和模型披露一样默认收紧，
// 想公开得显式打开。
func normalizeRepositoryDisclosure(mode RepositoryDisclosure) RepositoryDisclosure {
	if RepositoryDisclosure(strings.ToLower(strings.TrimSpace(string(mode)))) == RepositoryDisclosureEveryone {
		return RepositoryDisclosureEveryone
	}
	return RepositoryDisclosureOwner
}

// repositoryDisclosedTo 判断本轮发言者能不能拿到项目地址。
func repositoryDisclosedTo(cfg BotConfig, owner bool) bool {
	return owner || normalizeRepositoryDisclosure(cfg.RepositoryDisclosure) == RepositoryDisclosureEveryone
}
