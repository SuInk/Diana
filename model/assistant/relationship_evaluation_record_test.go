// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingEvaluationMemory 在内存画像之外记下每一次评估。
type recordingEvaluationMemory struct {
	*memoryUserMemoryStore
	mu      sync.Mutex
	records []RelationshipEvaluationRecord
}

func (s *recordingEvaluationMemory) RecordRelationshipEvaluation(_ context.Context, record RelationshipEvaluationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *recordingEvaluationMemory) snapshot() []RelationshipEvaluationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RelationshipEvaluationRecord(nil), s.records...)
}

func evaluationTestEvent() MessageEvent {
	return MessageEvent{
		Kind:       EventKindGroup,
		ProfileID:  "bot-a",
		GroupID:    "group",
		UserID:     "user",
		MessageID:  "message",
		SenderName: "Alice",
		RawMessage: "谢谢你一直帮我",
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": "谢谢你一直帮我"}}},
	}
}

func runEvaluationForTest(t *testing.T, reply string, score int) RelationshipEvaluationRecord {
	t.Helper()
	provider := &capturingLLMProvider{reply: reply}
	memory := &recordingEvaluationMemory{memoryUserMemoryStore: newMemoryUserMemoryStore()}
	memory.profiles["user"] = UserMemoryProfile{UserID: "user", Favorability: score}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetUserMemoryStore(memory)
	event := evaluationTestEvent()
	<-runtime.enqueueRelationshipEvaluation(event, PlainText(event.Segments))
	records := memory.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	return records[0]
}

// 每一次后台评估都要留下记录，不只是分数真的变了的那些。
func TestRelationshipEvaluationRecordsEveryOutcome(t *testing.T) {
	changed := runEvaluationForTest(t, `{"should_update":true,"delta":2,"confidence":0.9,"reason":"真诚感谢"}`, 10)
	if changed.Status != RelationshipEvaluationChanged || changed.ProposedDelta != 2 || changed.AppliedDelta != 2 ||
		changed.BeforeScore != 10 || changed.AfterScore != 12 || changed.Reason != "真诚感谢" || changed.Model != "test" {
		t.Fatalf("changed = %#v", changed)
	}
	if changed.BotProfileID != "bot-a" || changed.GroupID != "group" || changed.MessageID != "message" ||
		changed.SenderName != "Alice" || changed.MessageText != "谢谢你一直帮我" {
		t.Fatalf("changed identity = %#v", changed)
	}

	low := runEvaluationForTest(t, `{"should_update":true,"delta":3,"confidence":0.5,"reason":"不太确定"}`, 10)
	if low.Status != RelationshipEvaluationLowConfidence || low.ProposedDelta != 3 || low.AppliedDelta != 0 || low.AfterScore != 10 {
		t.Fatalf("low confidence = %#v", low)
	}

	unchanged := runEvaluationForTest(t, `{"should_update":false,"delta":0,"confidence":0.95,"reason":"普通闲聊"}`, 10)
	if unchanged.Status != RelationshipEvaluationUnchanged || unchanged.AppliedDelta != 0 {
		t.Fatalf("unchanged = %#v", unchanged)
	}

	failed := runEvaluationForTest(t, `不是 JSON`, 10)
	if failed.Status != RelationshipEvaluationFailed || failed.Error == "" || failed.BeforeScore != 10 {
		t.Fatalf("failed = %#v", failed)
	}
}

// 同一次评估记下的画像跟着这条记录走，分数没动也要看得到。
func TestRelationshipEvaluationRecordsPortrait(t *testing.T) {
	record := runEvaluationForTest(t, `{"should_update":false,"delta":0,"confidence":0.95,"reason":"自我介绍",`+
		`"portrait":[{"field":"occupation","value":"程序员","source":"stated","confidence":0.9},`+
		`{"field":"hobbies","value":"可能喜欢猫","source":"inferred","confidence":0.5}]}`, 10)
	if record.Status != RelationshipEvaluationUnchanged || len(record.Portrait) != 1 {
		t.Fatalf("record = %#v", record)
	}
	if got := record.Portrait[0]; got.Field != "occupation" || got.Value != "程序员" || got.Label == "" || got.Source != "stated" {
		t.Fatalf("portrait = %#v", got)
	}
}

// 后台评估排满时这一轮跳过，不拖慢回复，但也得有一条记录。
func TestRelationshipEvaluationRecordsSaturationSkip(t *testing.T) {
	memory := &recordingEvaluationMemory{memoryUserMemoryStore: newMemoryUserMemoryStore()}
	runtime := NewRuntime(BotConfig{BotAccount: "bot"}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return &capturingLLMProvider{reply: `{"should_update":true,"delta":1,"confidence":0.9,"reason":"x"}`}, nil
	})
	runtime.SetUserMemoryStore(memory)
	logs := &captureAppLogs{}
	runtime.SetAppLogWriter(logs)
	for range cap(runtime.relationshipEvalSem) {
		runtime.relationshipEvalSem <- struct{}{}
	}
	event := evaluationTestEvent()
	<-runtime.enqueueRelationshipEvaluation(event, PlainText(event.Segments))
	deadline := time.Now().Add(2 * time.Second)
	for (len(memory.snapshot()) == 0 || !hasAppLogAction(logs.entriesSnapshot(), "relationship_evaluation_skipped")) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	records := memory.snapshot()
	if len(records) != 1 || records[0].Status != RelationshipEvaluationSkipped || records[0].Error == "" || records[0].UserID != "user" {
		t.Fatalf("records = %#v", records)
	}
	if !hasAppLogAction(logs.entriesSnapshot(), "relationship_evaluation_skipped") {
		t.Fatalf("skip not in the run log: %v", appLogActions(logs.entriesSnapshot()))
	}
}

// 分数顶到上下限时，模型给的幅度没全部生效，要能看出是被截了；并发写造成的
// 前后差异不算截断。
func TestRelationshipEvaluationStatusDetectsCap(t *testing.T) {
	decision := relationshipEvaluationDecision{ShouldUpdate: true, Delta: 3, Confidence: 0.9}
	if got := relationshipEvaluationStatus(decision, UserMemoryProfile{Favorability: 199}, UserMemoryProfile{Favorability: 200}); got != RelationshipEvaluationCapped {
		t.Fatalf("capped status = %q", got)
	}
	if got := relationshipEvaluationStatus(decision, UserMemoryProfile{Favorability: 50}, UserMemoryProfile{Favorability: 55}); got != RelationshipEvaluationChanged {
		t.Fatalf("concurrent write status = %q", got)
	}
	down := relationshipEvaluationDecision{ShouldUpdate: true, Delta: -2, Confidence: 0.9}
	if got := relationshipEvaluationStatus(down, UserMemoryProfile{Favorability: -100}, UserMemoryProfile{Favorability: -100}); got != RelationshipEvaluationCapped {
		t.Fatalf("floor status = %q", got)
	}
}

// 运行日志里每次评估以前都是同一句「模型已完成关系与画像评估」；现在要看得出是谁、
// 加减了多少、为什么没加上，记下了什么画像。
func TestRelationshipEvaluationLogMessage(t *testing.T) {
	event := MessageEvent{UserID: "10001", SenderName: "小林"}
	cases := []struct {
		name     string
		before   int
		after    int
		decision relationshipEvaluationDecision
		status   string
		portrait []string
		want     string
	}{
		{"changed", 10, 12, relationshipEvaluationDecision{ShouldUpdate: true, Delta: 2, Confidence: 0.9}, RelationshipEvaluationChanged, nil, "小林：好感度 +2（10 → 12）"},
		{"down", 5, 2, relationshipEvaluationDecision{ShouldUpdate: true, Delta: -3, Confidence: 0.9}, RelationshipEvaluationChanged, nil, "小林：好感度 -3（5 → 2）"},
		{"capped", 200, 200, relationshipEvaluationDecision{ShouldUpdate: true, Delta: 2, Confidence: 0.9}, RelationshipEvaluationCapped, nil, "小林：好感度已到头（200），模型给的 +2 没加上"},
		{"low confidence", 25, 25, relationshipEvaluationDecision{ShouldUpdate: true, Delta: -1, Confidence: 0.55}, RelationshipEvaluationLowConfidence, nil, "小林：模型想 -1，但把握不够（55%），好感度不变"},
		{"portrait only", 12, 12, relationshipEvaluationDecision{Confidence: 0.97}, RelationshipEvaluationUnchanged, []string{"居住地点 杭州", "兴趣爱好 爬山"}, "小林：好感度不变；记下画像：居住地点 杭州、兴趣爱好 爬山"},
	}
	for _, tc := range cases {
		got := relationshipEvaluationLogMessage(event, UserMemoryProfile{Favorability: tc.before}, UserMemoryProfile{Favorability: tc.after}, tc.decision, tc.status, tc.portrait)
		if got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}
