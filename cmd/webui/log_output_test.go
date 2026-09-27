// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestResolveLogStdoutMode(t *testing.T) {
	cases := []struct {
		name  string
		facts stdoutFacts
		want  logStdoutMode
	}{
		// launchd 的 StandardOutPath、nohup 重定向：标准输出是普通文件，没人轮转。
		{name: "redirected to file", facts: stdoutFacts{}, want: logStdoutStartup},
		// Docker 靠 docker logs 看日志，标准输出同样不是终端，必须照旧全程复制。
		{name: "docker", facts: stdoutFacts{docker: true}, want: logStdoutAlways},
		{name: "terminal", facts: stdoutFacts{terminal: true}, want: logStdoutAlways},
		{name: "systemd journal", facts: stdoutFacts{journal: true}, want: logStdoutAlways},
		{name: "env forces copy", facts: stdoutFacts{env: "1"}, want: logStdoutAlways},
		{name: "env forces copy case-insensitive", facts: stdoutFacts{env: " Always "}, want: logStdoutAlways},
		{name: "env turns docker copy off", facts: stdoutFacts{env: "off", docker: true}, want: logStdoutStartup},
		{name: "env turns terminal copy off", facts: stdoutFacts{env: "0", terminal: true}, want: logStdoutStartup},
		// 拼错的值按没写处理，不能让 Docker 因为一个手误看不到日志。
		{name: "unknown env value falls back", facts: stdoutFacts{env: "maybe", docker: true}, want: logStdoutAlways},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveLogStdoutMode(tc.facts); got != tc.want {
				t.Fatalf("mode = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLogOutputStopsMirrorAfterStartup(t *testing.T) {
	var file, stdout bytes.Buffer
	output := newLogOutput(&file, &stdout)
	if _, err := output.Write([]byte("config loaded\n")); err != nil {
		t.Fatal(err)
	}
	output.stopMirror("/var/log/diana.log")
	output.stopMirror("/var/log/diana.log")
	if _, err := output.Write([]byte("GET /api/logs\n")); err != nil {
		t.Fatal(err)
	}
	if file.String() != "config loaded\nGET /api/logs\n" {
		t.Fatalf("file = %q", file.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || lines[0] != "config loaded" || !strings.Contains(lines[1], "further logs go to /var/log/diana.log only") || !strings.Contains(lines[1], logStdoutEnv+"=1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// 标准输出坏了（管道被关、输出文件所在磁盘满）不能连累日志文件。
func TestLogOutputWritesFileEvenWhenStdoutFails(t *testing.T) {
	var file bytes.Buffer
	output := newLogOutput(&file, failingWriter{})
	n, err := output.Write([]byte("line\n"))
	if err != nil || n != 5 || file.String() != "line\n" {
		t.Fatalf("n=%d err=%v file=%q", n, err, file.String())
	}
}

func TestLogOutputIgnoresEmptyWrites(t *testing.T) {
	var file, stdout bytes.Buffer
	output := newLogOutput(&file, &stdout)
	if n, err := output.Write(nil); n != 0 || err != nil || file.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("n=%d err=%v file=%q stdout=%q", n, err, file.String(), stdout.String())
	}
}
