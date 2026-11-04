// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 生产事故：swe-2 在多机器人群里没调 send，直接用纯文本写出「silent=true，理由」，
// 原路径按 plain_text 收尾，把内部理由当正文发到了群里。
func TestRunnerTreatsPlainTextSilentDirectiveAsSilent(t *testing.T) {
	client := &scriptedClient{responses: []string{"silent=true，这是在问 Mio 不是我（"}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "在看啥宝宝"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Silent || resp.Text != "" || resp.SilentReason != "这是在问 Mio 不是我（" {
		t.Fatalf("resp = Silent:%v Text:%q SilentReason:%q", resp.Silent, resp.Text, resp.SilentReason)
	}
}

func TestSilentDirectiveFromText(t *testing.T) {
	for text, want := range map[string]string{
		"silent=true，这是在问 Mio 不是我（":        "这是在问 Mio 不是我（",
		"silent=true，Mio 是在跟小汐说话，重启的也不是我（": "Mio 是在跟小汐说话，重启的也不是我（",
		"  silent = true , 不是对我说的":         "不是对我说的",
		"Silent: TRUE。还是在跟小汐说话":            "还是在跟小汐说话",
		"silent：true（不是叫我）":                "不是叫我）",
		"silent=true":                      "",
		"silent=true\n这句是 Mio 在使唤小汐":       "这句是 Mio 在使唤小汐",
	} {
		reason, ok := SilentDirectiveFromText(text)
		if !ok || reason != want {
			t.Errorf("SilentDirectiveFromText(%q) = %q, %v; want %q, true", text, reason, ok, want)
		}
	}
	// 正文里讲这个参数、静默为 false、或开头不是指令时照常发出。
	for _, text := range []string{
		"silent",
		"silent=false，我来回一句",
		"silent=true 是 send 的参数，填了就不发消息",
		"silent=truely",
		"请在回复里加上 silent=true",
		"silently=true，",
		`{"silent": true}`,
		"",
	} {
		if reason, ok := SilentDirectiveFromText(text); ok {
			t.Errorf("SilentDirectiveFromText(%q) = %q, true; want false", text, reason)
		}
	}
}
