// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
)

// logStdoutEnv 决定写日志文件时标准输出还要不要同时复制一份。
//
// 不设时自动判断：Docker 靠 docker logs 看日志、终端里运行要看得见输出、systemd
// 的 journal 自带轮转，这三种照旧复制；其余情况（launchd 的 StandardOutPath、nohup
// 重定向到文件）标准输出只留启动阶段那几行。launchd 的输出文件没人轮转，以前和
// 程序自己写的日志一字不差、只增不减，涨到几十 MB 也没人发现。
const logStdoutEnv = "DIANA_LOG_STDOUT"

// logStdoutMode 是日志文件之外标准输出的去向。
type logStdoutMode int

const (
	// logStdoutStartup 只在启动阶段复制，服务开始监听后停止。
	logStdoutStartup logStdoutMode = iota
	// logStdoutAlways 全程复制。
	logStdoutAlways
)

func (m logStdoutMode) String() string {
	if m == logStdoutAlways {
		return "always"
	}
	return "startup"
}

// stdoutFacts 是判断标准输出去向用到的环境事实，单独拎出来方便测试。
type stdoutFacts struct {
	env      string
	docker   bool
	terminal bool
	journal  bool
}

func currentStdoutFacts() stdoutFacts {
	return stdoutFacts{
		env:      os.Getenv(logStdoutEnv),
		docker:   dockerDeployment(),
		terminal: stdoutIsTerminal(),
		journal:  stdoutIsJournal(),
	}
}

// resolveLogStdoutMode 按环境变量、部署方式和标准输出实际连着什么决定复制策略。
// 环境变量写了认得的值就以它为准；写错了按没写处理，免得一个拼写错误让 Docker
// 的 docker logs 看不到日志。
func resolveLogStdoutMode(facts stdoutFacts) logStdoutMode {
	switch strings.ToLower(strings.TrimSpace(facts.env)) {
	case "1", "true", "yes", "on", "always":
		return logStdoutAlways
	case "0", "false", "no", "off", "startup":
		return logStdoutStartup
	}
	if facts.docker || facts.terminal || facts.journal {
		return logStdoutAlways
	}
	return logStdoutStartup
}

// stdoutIsTerminal 报告标准输出是不是字符设备（终端、Windows 控制台，也包括
// /dev/null）。/dev/null 算进来无妨：复制过去也不占地方。
func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// logOutput 先写日志文件，再按需复制到标准输出。
//
// 以前用 io.MultiWriter(os.Stdout, file)：标准输出排在前面，它一出错（管道被关、
// launchd 的输出文件所在磁盘写满）整行就连文件都不写了。现在文件优先，标准输出
// 写失败只丢复制那一份。
type logOutput struct {
	file   io.Writer
	stdout io.Writer
	mirror atomic.Bool
}

func newLogOutput(file, stdout io.Writer) *logOutput {
	output := &logOutput{file: file, stdout: stdout}
	output.mirror.Store(true)
	return output
}

func (o *logOutput) Write(p []byte) (int, error) {
	// 访问日志跳过某条请求时 gin 仍会写一个空串，不必为它碰文件和标准输出。
	if len(p) == 0 {
		return 0, nil
	}
	n, err := o.file.Write(p)
	if o.mirror.Load() {
		_, _ = o.stdout.Write(p)
	}
	return n, err
}

// stopMirror 停止复制到标准输出，并在标准输出上留一句去向说明。重复调用无效果。
func (o *logOutput) stopMirror(logPath string) {
	if o.mirror.CompareAndSwap(true, false) {
		_, _ = fmt.Fprintf(o.stdout, "diana: startup finished; further logs go to %s only (set %s=1 to keep copying them here)\n", logPath, logStdoutEnv)
	}
}
