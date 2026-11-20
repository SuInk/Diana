// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveFinalSendReplay 重放生产 debug-traces 里的主回复首步，看「send 只能收尾 +
// 收尾前复核说了却没做」下模型怎么走：直接调工具、正常回复、静默，还是说了没做被退回。
// 默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_FINAL_SEND_REPLAY_DIR=<放 *-reply.json 的目录> \
//	  scripts/test-serial.sh ./model/agent -run TestLiveFinalSendReplay -v
//
// 录下的请求是旧协议（send 带 continue、系统提示里有中途发言那条规则），这里把 send
// 定义换成当前代码的、删掉那条规则再发。
func TestLiveFinalSendReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_FINAL_SEND_REPLAY_DIR"))
	if dir == "" {
		t.Skip("set DIANA_FINAL_SEND_REPLAY_DIR to a directory of recorded reply requests")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*-reply.json"))
	sort.Strings(paths)
	client := liveAgentClient(t)
	runner := &Runner{client: client}
	describe := func(resp *llm.GenerateResponse) string {
		var calls []string
		for _, call := range resp.ToolCalls {
			name := call.Name
			if inner, _ := call.Arguments["name"].(string); inner != "" {
				name += ":" + inner
			}
			if call.Name == finalizeToolName {
				if silent, _ := call.Arguments["silent"].(bool); silent || sendText(call) == "" {
					reason, _ := call.Arguments["silent_reason"].(string)
					name += "(silent:" + truncateForLog(reason, 80) + ")"
				} else {
					name += "「" + truncateForLog(sendText(call), 70) + "」"
				}
			}
			calls = append(calls, name)
		}
		if len(calls) == 0 {
			return "纯文本「" + truncateForLog(resp.Text, 70) + "」"
		}
		return strings.Join(calls, " + ")
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Metadata struct {
				Request llm.GenerateRequest `json:"request"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Metadata.Request.Messages) == 0 {
			t.Fatalf("%s is not a recorded reply request: %v", path, err)
		}
		req := envelope.Metadata.Request
		for index, tool := range req.Tools {
			if tool.Name == finalizeToolName {
				req.Tools[index] = finalizeToolDefinition(false)
			}
		}
		for index, message := range req.Messages {
			if message.Role == llm.RoleSystem {
				req.Messages[index].Content = removeLine(message.Content, "- 要先查、先做几步才能回答时，可以在调用工具的同一步里 send")
			}
		}
		name := filepath.Base(path)
		first, err := client.Generate(context.Background(), req)
		if err != nil {
			t.Logf("%s ERROR %v", name, err)
			if strings.Contains(err.Error(), "429") {
				t.Fatal("rate limited, stop")
			}
			continue
		}
		line := name + " | 第一步 " + describe(first)
		if onlySendCalls(first.ToolCalls) {
			action := finalizeAction(first.ToolCalls, first.Text)
			if !action.Silent {
				unfinished := runner.reviewUnfinishedClaim(context.Background(), req.Messages, action.Content, nil)
				line += " | 复核「" + unfinished + "」"
				if unfinished != "" {
					req.Messages = appendToolRepair(req.Messages, first, first.Text, unfinishedClaimRepair(unfinished))
					second, err := client.Generate(context.Background(), req)
					if err != nil {
						line += " | 第二步 ERROR " + err.Error()
					} else {
						line += " | 第二步 " + describe(second)
					}
				}
			}
		}
		t.Log(line)
	}
}

func removeLine(text, prefix string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), prefix) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func truncateForLog(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return fmt.Sprintf("%s…", string(runes[:limit]))
}
