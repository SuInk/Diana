// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import "strings"

func memoryProfileSessionPrefixes(profileID string) (string, string) {
	prefix := strings.TrimSpace(profileID)
	if prefix != "" {
		prefix += ":"
	}
	return prefix + "group:", prefix + "private:"
}

func memorySessionNamespace(session string) string {
	session = strings.TrimSpace(session)
	for _, marker := range []string{":group:", ":private:"} {
		if index := strings.LastIndex(session, marker); index >= 0 {
			return session[:index+1]
		}
	}
	return ""
}

// groupEventSessions 是某台机器人在一个群里的聊天记录会话键：带命名空间的当前写法，
// 加上命名空间出现前（#591 之前）的旧写法。同一个群里有两台机器人时，每条消息会被
// 各记一份，只按群号查就会把条数算成两倍。固定两个，查询里写 session IN (?, ?)；
// 没有命名空间时两个相同。
func groupEventSessions(namespace, groupID string) (string, string) {
	legacy := "group:" + strings.TrimSpace(groupID)
	if namespace = strings.TrimSpace(namespace); namespace == "" {
		return legacy, legacy
	}
	return namespace + ":" + legacy, legacy
}
