// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package qqcall

import (
	"strings"
	"unicode"
)

// NameMatches 判断来电窗口上的昵称是不是这些名字里的一个（好友备注、QQ 昵称）。
// 只认整串相等：主人才放行，「像」不算。OCR 常见的大小写和空白差异不计。
func NameMatches(seen string, names ...string) bool {
	seen = normalizeName(seen)
	if seen == "" {
		return false
	}
	for _, name := range names {
		if n := normalizeName(name); n != "" && n == seen {
			return true
		}
	}
	return false
}

func normalizeName(s string) string {
	return strings.ToLower(stripSpace(s))
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
