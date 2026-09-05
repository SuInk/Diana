// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !linux

package hostinfo

import (
	"errors"
	"runtime"
)

// temperatures 在非 Linux 上没有免 root 的通用读法。
//
// macOS 的 powermetrics 需要 root，SMC 要走私有框架；Windows 的 WMI 温度类
// 大多数消费级主板根本不实现。与其半懂不懂地报一个数，不如如实说读不到。
func temperatures() ([]Reading, error) {
	return nil, errors.New("暂不支持在 " + runtime.GOOS + " 上读取温度（macOS 需要 root 权限，Windows 缺少通用接口）")
}

func powerDraw() ([]Reading, *Battery, error) {
	return nil, nil, errors.New("暂不支持在 " + runtime.GOOS + " 上读取功率")
}
