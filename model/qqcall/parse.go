// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package qqcall

import (
	"regexp"
	"strings"
	"unicode"
)

// 接通那一刻 QQ 会推一条 msgType 529/11 的私聊，SnowLuma 解不出内容，只在日志里
// 记一行 WARN，from 就是对方的 QQ 号——不管是谁打给谁。响铃时什么都不推，所以
// 来电人的 QQ 号只能等接通后从这里拿。
var connectPushLine = regexp.MustCompile(`\bfrom=(\d{5,12})\b[^)]*\bmsgType=529/11\b`)

// PeerFromLogLine 从 SnowLuma 的一行日志里读出接通推送的对方 QQ 号，不是这种行时返回空。
func PeerFromLogLine(line string) string {
	if !strings.Contains(line, "529/11") {
		return ""
	}
	if m := connectPushLine.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// 来电窗口在昵称下面写着「邀请你语音通话」；自己拨出去的窗口没有这句。
const incomingMarker = "邀请你"

// ParseCallerText 把来电窗口那块区域的 OCR 结果拆成昵称，并判断是不是打进来的电话。
// OCR 会把一行字拆出空格，昵称取第一行非空文字，去掉空白。
func ParseCallerText(text string) (name string, incoming bool) {
	compact := stripSpace(text)
	incoming = strings.Contains(compact, incomingMarker)
	for _, line := range strings.Split(text, "\n") {
		line = stripSpace(line)
		if line == "" || strings.Contains(line, incomingMarker) {
			continue
		}
		return line, incoming
	}
	return "", incoming
}

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
