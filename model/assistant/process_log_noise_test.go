// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// captureProcessLog 把标准 log 的输出接到缓冲区，用例结束后还原。
func captureProcessLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	return &buffer
}

func TestRepositoryWatchCursorChangesListsOnlyChangedFields(t *testing.T) {
	at := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
	before := repositoryWatchCursorFields{
		commit: "abc", pullRequest: "2026-09-27T08:00:00Z#10", issue: "2026-09-27T08:00:00Z#11",
		release: "v1.0.0", releaseAt: at, releaseID: 7, star: "s1", starAt: at,
	}
	if got := repositoryWatchCursorChanges(before, before); got != "" {
		t.Fatalf("unchanged cursors reported: %q", got)
	}
	// 同一时刻换了时区不算变化。
	shifted := before
	shifted.releaseAt = at.In(time.FixedZone("CST", 8*3600))
	if got := repositoryWatchCursorChanges(before, shifted); got != "" {
		t.Fatalf("timezone-only change reported: %q", got)
	}
	after := before
	after.issue = "2026-09-27T09:00:00Z#12"
	after.starAt = at.Add(time.Minute)
	got := repositoryWatchCursorChanges(before, after)
	want := `issue="2026-09-27T08:00:00Z#11"->"2026-09-27T09:00:00Z#12" star_at=2026-09-27T08:00:00Z->2026-09-27T08:01:00Z`
	if got != want {
		t.Fatalf("changes = %q, want %q", got, want)
	}
	for _, unchanged := range []string{"commit=", "pr=", "release=", "release_id=", "star="} {
		if strings.Contains(got, unchanged) {
			t.Fatalf("unchanged field %q leaked into %q", unchanged, got)
		}
	}
}

func TestStoreRepositoryWatchProgressLogsOnlyWhenCursorMoves(t *testing.T) {
	store := &stubReminderStore{items: []Reminder{{
		ID: "watch-1", Kind: ReminderKindRepositoryWatch, Repository: "SuInk/Diana", IntervalSeconds: 60,
		WatchIssues: true, LastIssueCursor: "2026-09-27T08:00:00Z#40",
	}}}
	runtime := &Runtime{reminders: store}
	output := captureProcessLog(t)

	if err := runtime.storeRepositoryWatchProgress("watch-1", repositoryWatchSnapshot{IssueCursor: "2026-09-27T08:00:00Z#40", CheckedAt: time.Now()}, "", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "cursor saved") {
		t.Fatalf("unchanged cursor logged: %s", output.String())
	}
	if err := runtime.storeRepositoryWatchProgress("watch-1", repositoryWatchSnapshot{IssueCursor: "2026-09-27T09:00:00Z#41", CheckedAt: time.Now()}, "", ""); err != nil {
		t.Fatal(err)
	}
	line := output.String()
	if !strings.Contains(line, `cursor saved: id=watch-1 repository="SuInk/Diana" issue="2026-09-27T08:00:00Z#40"->"2026-09-27T09:00:00Z#41"`) || strings.Contains(line, "pr=") {
		t.Fatalf("cursor log = %q", line)
	}
}

// 工具调用开始那一行不再写进进程日志，其余阶段照写；操作日志里每个阶段都还在。
func TestAgentProgressProcessLogSkipsToolStarted(t *testing.T) {
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	observer := runtime.agentRunObserver(MessageEvent{Kind: EventKindGroup, GroupID: "123", UserID: "10001", MessageID: "m-progress"})
	output := captureProcessLog(t)

	phases := []agent.RunPhase{
		agent.RunPhaseStarted, agent.RunPhaseModelCompleted, agent.RunPhaseToolStarted,
		agent.RunPhaseToolCompleted, agent.RunPhaseCompleted,
	}
	for _, phase := range phases {
		observer(context.Background(), agent.RunEvent{TraceID: "chat-m-progress", Phase: phase, Tool: "web_search", ModelTurn: 1, ToolCall: 1})
	}
	text := output.String()
	if strings.Contains(text, "phase=tool_started") {
		t.Fatalf("tool_started still in process log:\n%s", text)
	}
	for _, phase := range []string{"started", "model_completed", "tool_completed", "completed"} {
		if !strings.Contains(text, "phase="+phase+" ") {
			t.Fatalf("phase %s missing from process log:\n%s", phase, text)
		}
	}
	started := 0
	for _, entry := range logs.entriesSnapshot() {
		if entry.Metadata["phase"] == agent.RunPhaseToolStarted {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("operation log tool_started entries = %d, want 1", started)
	}
}
