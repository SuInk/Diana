// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// Private trace bundles stay outside the repository. Replay only calls the LLM;
// it never executes tools, sends chat messages, or writes production state.
func TestLiveFollowUpProductionTraceReplay(t *testing.T) {
	path := os.Getenv("DIANA_FOLLOW_UP_REPLAY_BUNDLE")
	if path == "" {
		t.Skip("set DIANA_FOLLOW_UP_REPLAY_BUNDLE to a private trace bundle")
	}
	client := liveLLMClient(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		OldPrompt string
		Cases     []struct {
			Name         string
			Request      llm.GenerateRequest
			ForbiddenNew []string
		}
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal("invalid private replay bundle")
	}
	if bundle.OldPrompt == "" || len(bundle.Cases) == 0 {
		t.Fatal("replay requires an old prompt and captured requests")
	}
	providerFailed := false
	for _, tc := range bundle.Cases {
		for _, variant := range []string{"old", "new"} {
			t.Run(tc.Name+"/"+variant, func(t *testing.T) {
				req := tc.Request
				req.Messages = append([]llm.Message(nil), req.Messages...)
				if len(req.Tools) != 0 || len(req.Messages) == 0 {
					t.Fatal("expected a follow-up request without tools")
				}
				last := &req.Messages[len(req.Messages)-1]
				if last.Role != llm.RoleUser || !strings.Contains(last.Content, "你刚刚把下面这条内容") || strings.Count(last.Content, bundle.OldPrompt) != 1 {
					t.Fatal("captured request does not have one replaceable follow-up instruction")
				}
				if variant == "new" {
					last.Content = strings.Replace(last.Content, bundle.OldPrompt, promptFollowUp, 1)
				}
				// Diagnostic control only: compare the same trace without old chat turns.
				// This does not change the production follow-up context policy.
				if os.Getenv("DIANA_FOLLOW_UP_REPLAY_ISOLATE_HISTORY") == "1" {
					current := *last
					var isolated []llm.Message
					for _, message := range req.Messages[:len(req.Messages)-1] {
						if message.Role == llm.RoleSystem {
							isolated = append(isolated, message)
						}
					}
					req.Messages = append(isolated, current)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				resp, err := client.Generate(ctx, req)
				if err != nil {
					providerFailed = true
					t.Fatalf("replay request failed: %v", err)
				}
				if strings.TrimSpace(resp.Text) == "" || len(resp.ToolCalls) != 0 {
					t.Fatal("expected a nonempty textual follow-up without tool calls")
				}
				t.Logf("messages=%d reply=%s", len(req.Messages), resp.Text)
				if variant == "new" {
					compact := strings.Join(strings.Fields(strings.ToLower(resp.Text)), "")
					for _, stale := range tc.ForbiddenNew {
						if strings.Contains(compact, strings.ToLower(stale)) {
							t.Errorf("new prompt returned to a forbidden historical topic: %q", stale)
						}
					}
				}
			})
			if providerFailed {
				return // Do not repeatedly hit a failing or rate-limited provider.
			}
		}
	}
}
