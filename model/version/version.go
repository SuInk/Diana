// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package version 统一解析 Diana 运行时版本号，保证自更新始终能做语义化比较。
package version

import (
	_ "embed"
	"strconv"
	"strings"
)

// sourceVersion 是源码树声明的版本基线，发布新版本时随 tag 一起更新。
//
//go:embed VERSION
var sourceVersion string

// devSuffix 标记版本号来自源码基线而不是正式 Release 构建。
const devSuffix = "-dev"

// Source 返回源码树声明的版本基线。
func Source() string {
	return strings.TrimSpace(sourceVersion)
}

// IsSemantic 判断版本号是否是可比较的 vX.Y.Z 语义化版本，可带 +/- 后缀。
func IsSemantic(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		return false
	}
	value = value[1:]
	if index := strings.IndexAny(value, "+-"); index >= 0 {
		if index == len(value)-1 {
			return false
		}
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return false
		}
	}
	return true
}

// Resolve 归一化构建期通过 -ldflags 注入的版本号。
// Release 构建注入语义化 tag 时原样返回；本地 go build 没有注入（dev）或者
// CI 只注入提交短号时，回落到源码基线并带上 -dev 后缀，
// 避免运行中的二进制报出无法比较的版本号、导致更新检查永远不判定为可更新。
func Resolve(injected string) string {
	injected = strings.TrimSpace(injected)
	if IsSemantic(injected) {
		return injected
	}
	source := Source()
	if !IsSemantic(source) {
		if injected == "" {
			return "dev"
		}
		return injected
	}
	if injected == "" || strings.EqualFold(injected, "dev") {
		return source + devSuffix
	}
	return source + devSuffix + "+" + sanitizeMetadata(injected)
}

// sanitizeMetadata 只保留语义化版本后缀允许的字符，避免版本号带上奇怪内容。
func sanitizeMetadata(value string) string {
	var builder strings.Builder
	for _, item := range value {
		switch {
		case item >= '0' && item <= '9', item >= 'a' && item <= 'z', item >= 'A' && item <= 'Z', item == '.', item == '-':
			builder.WriteRune(item)
		default:
			builder.WriteRune('-')
		}
	}
	return builder.String()
}
