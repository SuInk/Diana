// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveReplyEchoReplay 重放生产 debug-traces 里主回复的第一步请求，看回复是不是以对方
// 刚说的那句开头、或者把自己发过的话原样再发（见 replyTailReminder）。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_ECHO_REPLAY=<…-reply.json 所在目录> go test ./model/assistant -run TestLiveReplyEchoReplay
//
// DIANA_ECHO_VARIANTS 逗号分隔：
//   - baseline  原样重放（v0.9.11 录下的请求，【输出前检查】只在系统头部）
//   - tail      请求最后接上当前的【输出前检查】
//   - tail_rule 同 tail，但不带「别复述原话」这一条，量这条本身有没有用
//   - tail_1602 同 tail，但不带括号使用：#1602 那版尾部提醒
//   - deliver   同 tail，并把录下请求里按需目录只列名字的 send_file 换成完整一行（replyFeaturedTools）
//
// 交付类统计：file 是调了 send_file（tools_execute、codemode 或 tool_search 取它的定义），
// paste 是把 <svg 这类源码直接贴进正文，claim 是没调 send_file 却说「发好了」「发了」。
// 另外数括号：aside 是整条或句中出现成对括号的旁白（「（盯）」「终于下班了（累死）」），
// trail 是句尾单独挂一个「（」。
// swe-2 是免费额度、和生产共用一个号：串行跑，一批别超过 20 次。
func TestLiveReplyEchoReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_ECHO_REPLAY"))
	if dir == "" {
		t.Skip("set DIANA_ECHO_REPLAY to a directory of recorded reply requests")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(paths)
	if only := strings.TrimSpace(os.Getenv("DIANA_ECHO_ONLY")); only != "" {
		var kept []string
		for _, path := range paths {
			if strings.Contains(filepath.Base(path), only) {
				kept = append(kept, path)
			}
		}
		paths = kept
	}
	client := liveLLMClient(t)
	runs := envInt("DIANA_ECHO_RUNS", 1)
	variants := strings.Split(firstNonEmpty(os.Getenv("DIANA_ECHO_VARIANTS"), "baseline,tail"), ",")
	group := MessageEvent{Kind: EventKindGroup}
	full := replyTailReminder(BotConfig{}, group)
	withoutRule := strings.Replace(full, "\n"+promptReplyNoEchoSpec.Default, "", 1)
	withoutBracket := strings.Replace(full, "\n"+promptReplyUnclosedBracketSpec.Default, "", 1)
	fileTool := &dianaFileDeliveryTool{}
	fileLine := "- " + dianaFileDeliveryToolName + "(filename: string, content?: string, path?: string, preview?: boolean): " + fileTool.Description() + "\n"
	claim := regexp.MustCompile(`发好了|已经发了|发出来了|发在群里了|文件发了|文件来了`)
	aside := regexp.MustCompile(`[（(][^（()）]{1,20}[）)]`)
	quote := regexp.MustCompile(`<diana-quote[^>]*/>|<diana-at[^>]*/>`)
	speaker := regexp.MustCompile(`^\[[^\]]*\]\s*[^:：]*[:：]\s*`)

	type tally struct{ runs, echo, repeat, silent, aside, trail, file, paste, claim int }
	totals := map[string]*tally{}
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
		base := envelope.Metadata.Request
		current := strings.TrimSpace(speaker.ReplaceAllString(base.Messages[len(base.Messages)-1].Content, ""))
		// 自己发过的话：请求里所有 send 调用的正文。
		sent := map[string]bool{}
		for _, message := range base.Messages {
			for _, call := range message.ToolCalls {
				if text, _ := call.Arguments["text"].(string); call.Name == "send" && strings.TrimSpace(text) != "" {
					sent[strings.TrimSpace(quote.ReplaceAllString(text, ""))] = true
				}
			}
		}
		for _, variant := range variants {
			req := base
			req.Messages = append([]llm.Message(nil), base.Messages...)
			switch variant {
			case "tail":
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Content: full, Priority: llm.MessagePrioritySystem})
			case "tail_rule":
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Content: withoutRule, Priority: llm.MessagePrioritySystem})
			case "deliver":
				req.Messages[0].Content = featureCatalogTool(req.Messages[0].Content, dianaFileDeliveryToolName, fileLine)
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Content: full, Priority: llm.MessagePrioritySystem})
			case "tail_1602":
				req.Messages = append(req.Messages, llm.Message{Role: llm.RoleSystem, Content: withoutBracket, Priority: llm.MessagePrioritySystem})
			}
			for run := 0; run < runs; run++ {
				resp, err := client.Generate(context.Background(), req)
				if err != nil {
					t.Logf("%s %s run %d: error %s", variant, filepath.Base(path), run, truncateRunes(err.Error(), 200))
					if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate") {
						t.Fatal("rate limited: stop before production swe-2 runs dry")
					}
					continue
				}
				text := strings.TrimSpace(resp.Text)
				var calls []string
				for _, call := range resp.ToolCalls {
					name := call.Name
					if raw, _ := json.Marshal(call.Arguments); call.Name != "send" && strings.Contains(string(raw), dianaFileDeliveryToolName) {
						name += "(" + dianaFileDeliveryToolName + ")"
					}
					if call.Name == "send" {
						text, _ = call.Arguments["text"].(string)
						if silent, _ := call.Arguments["silent"].(bool); silent {
							name += "(silent)"
						}
						if sticker, _ := call.Arguments["sticker"].(string); sticker != "" {
							name += "(sticker:" + sticker + ")"
						}
					}
					calls = append(calls, name)
				}
				clean := strings.TrimSpace(quote.ReplaceAllString(text, ""))
				counts := totals[variant]
				if counts == nil {
					counts = &tally{}
					totals[variant] = counts
				}
				counts.runs++
				echo := len([]rune(current)) >= 4 && strings.HasPrefix(clean, string([]rune(current)[:min(8, len([]rune(current)))]))
				repeat := clean != "" && sent[clean]
				if echo {
					counts.echo++
				}
				if repeat {
					counts.repeat++
				}
				if clean == "" {
					counts.silent++
				}
				usedFile := strings.Contains(strings.Join(calls, ","), dianaFileDeliveryToolName)
				pasted := strings.Contains(clean, "<svg") || strings.Contains(clean, "</svg>")
				claimed := !usedFile && claim.MatchString(clean)
				if usedFile {
					counts.file++
				}
				if pasted {
					counts.paste++
				}
				if claimed {
					counts.claim++
				}
				hasAside := aside.MatchString(clean)
				hasTrail := strings.HasSuffix(clean, "（") || strings.HasSuffix(clean, "(")
				if hasAside {
					counts.aside++
				}
				if hasTrail {
					counts.trail++
				}
				t.Logf("%-9s %-34s echo=%-5v repeat=%-5v aside=%-5v trail=%-5v file=%-5v paste=%-5v claim=%-5v calls=%s text=%s", variant, filepath.Base(path), echo, repeat, hasAside, hasTrail, usedFile, pasted, claimed, strings.Join(calls, ","), truncateRunes(clean, 90))
			}
		}
	}
	for _, variant := range variants {
		if counts := totals[variant]; counts != nil {
			t.Logf("SUMMARY %-9s runs=%d echo=%d repeat=%d aside=%d trail=%d silent_or_tool=%d file=%d paste=%d claim=%d", variant, counts.runs, counts.echo, counts.repeat, counts.aside, counts.trail, counts.silent, counts.file, counts.paste, counts.claim)
		}
	}
}

// featureCatalogTool 把按需目录最后一行「只列名字」里的 name 挪出来，写成完整一行。
func featureCatalogTool(system, name, line string) string {
	const unlisted = "- 另外还有（只列名字"
	at := strings.Index(system, unlisted)
	if at < 0 {
		return system
	}
	end := strings.Index(system[at:], "\n")
	if end < 0 {
		end = len(system) - at
	}
	listing := system[at : at+end]
	trimmed := strings.Replace(strings.Replace(listing, "、"+name, "", 1), name+"、", "", 1)
	return system[:at] + line + trimmed + system[at+end:]
}
