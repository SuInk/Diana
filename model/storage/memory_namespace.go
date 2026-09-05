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
