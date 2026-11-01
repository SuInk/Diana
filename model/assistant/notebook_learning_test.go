// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// Private production fixtures and model credentials never enter the repository.
func TestNotebookLearningReplaySchema(t *testing.T) {
	path := os.Getenv("DIANA_NOTEBOOK_SCHEMA_OUT")
	if path == "" {
		t.Skip("set DIANA_NOTEBOOK_SCHEMA_OUT to export the real replay prompt and tool")
	}
	raw, err := json.Marshal(map[string]any{"prompt": memoryGateSystemPrompt + slangGateRule + notebookLearningRule, "tool": memoryGateSubmitToolFor(true)})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func TestNotebookLearningCapturedReplay(t *testing.T) {
	path := os.Getenv("DIANA_NOTEBOOK_REPLAY_IN")
	if path == "" {
		t.Skip("set DIANA_NOTEBOOK_REPLAY_IN to a captured model result")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Payload memoryGatePayload `json:"payload"`
		Verdict json.RawMessage   `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	if len(replay.Payload.CurrentBatch) == 0 {
		t.Fatal("missing current batch")
	}
	last := replay.Payload.CurrentBatch[len(replay.Payload.CurrentBatch)-1]
	event := MessageEvent{ProfileID: "replay-tg", ContextNamespace: "replay-tg", Kind: EventKindGroup, GroupID: last.GroupID, UserID: last.UserID, MessageID: last.MessageID}
	store := newMemoryNotebookStore()
	runtime := newNotebookRuntime(t, store)
	_, ai, err := parseMemoryGateResponse(string(replay.Verdict))
	if err != nil {
		t.Fatal(err)
	}
	confirmed := runtime.learnNotebookDefinitions(context.Background(), event, replay.Payload, string(replay.Verdict), ai)
	if !confirmed["uuu"] {
		t.Fatal("production explanation was not accepted")
	}
	if block := runtime.notebookContextForRouting(context.Background(), event, "uuu默认是gnome吗"); !strings.Contains(block, "Ubuntu") {
		t.Fatalf("missing meaning: %s", block)
	}
}

func notebookLearningFixture() (MessageEvent, memoryGatePayload, notebookLearningCandidate) {
	event := notebookTestEvent("human", "UbUntU")
	event.MessageID = "explanation"
	use := memoryGateEventFromMessage(event, "计算机科学导论让我安装uuu")
	use.MessageID = "use"
	explanation := memoryGateEventFromMessage(event, "UbUntU")
	return event, memoryGatePayload{CurrentBatch: []memoryGateEvent{use, explanation}}, notebookLearningCandidate{
		Term: "uuu", Meaning: "本群对 Ubuntu 的简称", EvidenceMessageIDs: []string{"use"}, ConfirmationMessageID: "explanation",
	}
}

func notebookLearningJSON(t *testing.T, candidate notebookLearningCandidate) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"memories": []any{}, "notebook_candidates": []notebookLearningCandidate{candidate}, "unknown_terms": []map[string]any{{"term": "uuu", "source_index": 0}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNotebookLearningValidatesProvenance(t *testing.T) {
	for _, name := range []string{"valid", "unknown evidence", "unknown confirmation", "bot confirmation", "other speaker", "other group", "only bot usage", "invented term", "empty meaning", "oversized meaning", "history confirmation", "missing profile", "disabled", "private", "likely AI"} {
		t.Run(name, func(t *testing.T) {
			event, payload, candidate := notebookLearningFixture()
			cfg := BotConfig{}
			var ai *memorySpeakerAI
			switch name {
			case "unknown evidence":
				candidate.EvidenceMessageIDs = []string{"missing"}
			case "unknown confirmation":
				candidate.ConfirmationMessageID = "missing"
			case "bot confirmation":
				payload.CurrentBatch[1].FromBot = true
			case "other speaker":
				payload.CurrentBatch[1].UserID = "other"
			case "other group":
				payload.CurrentBatch[1].GroupID = "other"
			case "only bot usage":
				payload.CurrentBatch[0].FromBot = true
			case "invented term":
				candidate.Term = "xxx"
			case "empty meaning":
				candidate.Meaning = " "
			case "oversized meaning":
				candidate.Meaning = strings.Repeat("字", 121)
			case "history confirmation":
				payload.RecentMessages = payload.CurrentBatch[1:]
				payload.CurrentBatch = payload.CurrentBatch[:1]
			case "missing profile":
				event.ProfileID = ""
			case "disabled":
				cfg.SlangLearningEnabled = boolPointer(false)
			case "private":
				event.Kind = EventKindPrivate
			case "likely AI":
				ai = &memorySpeakerAI{Likelihood: 0.99}
			}
			store := newMemoryNotebookStore()
			runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetNotebookStore(store)
			got := runtime.learnNotebookDefinitions(context.Background(), event, payload, notebookLearningJSON(t, candidate), ai)
			want := 0
			if name == "valid" {
				want = 1
			}
			if len(got) != want || len(store.entries) != want {
				t.Fatalf("confirmed=%v entries=%v", got, store.entries)
			}
		})
	}
}

func TestNotebookLearningFromMemoryJobWithoutMemories(t *testing.T) {
	event, _, candidate := notebookLearningFixture()
	provider := &capturingLLMProvider{reply: notebookLearningJSON(t, candidate)}
	profiles := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{{ID: "default", Group: "default", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "m"}}}}}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), profiles, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return provider, nil })
	store := newSlangNotebookStore()
	runtime.SetNotebookStore(store)
	memory := &testStructuredMemoryStore{}
	runtime.SetStructuredMemoryStore(memory)
	use := event
	use.MessageID = "use"
	use.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "计算机科学导论让我安装uuu"}}}
	jobs := []MemoryJobPayload{
		{Kind: MemoryJobEvent, Session: sessionKey(event), Event: use},
		{Kind: MemoryJobEvent, Session: sessionKey(event), Event: event},
	}
	for i := 0; i < 2; i++ {
		if err := runtime.processEventMemoryJobs(context.Background(), memory, jobs); err != nil {
			t.Fatal(err)
		}
	}
	entry, found, err := store.NotebookEntryDetail(context.Background(), notebookGlobalScope(event.ProfileID), "uuu")
	if err != nil || !found || entry.Version != 1 || entry.AuthorUserID != SlangAutoEditorID {
		t.Fatalf("entry=%+v found=%v err=%v", entry, found, err)
	}
	if store.candidate(event.ProfileID, event.GroupID, "uuu").Count != 0 {
		t.Fatal("confirmed word must not be researched")
	}
	block := runtime.notebookContextForRouting(context.Background(), event, "uuu默认是gnome吗")
	if !strings.Contains(block, "Ubuntu") {
		t.Fatalf("missing learned definition: %s", block)
	}
	event.GroupID = "another-group"
	if block := runtime.notebookContextForRouting(context.Background(), event, "uuu默认是gnome吗"); !strings.Contains(block, "Ubuntu") {
		t.Fatalf("missing shared meaning: %s", block)
	}
}

func TestNotebookLearningRespectsSharedScope(t *testing.T) {
	for _, shared := range []bool{true, false} {
		event, payload, candidate := notebookLearningFixture()
		event.ContextNamespace = event.ProfileID
		store := newMemoryNotebookStore()
		runtime := NewRuntime(BotConfig{NotebookSharedScopeEnabled: boolPointer(shared)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		runtime.SetNotebookStore(store)
		runtime.learnNotebookDefinitions(context.Background(), event, payload, notebookLearningJSON(t, candidate), nil)
		for _, target := range []string{"source", "other-group", "private", "other-profile"} {
			read := event
			switch target {
			case "other-group":
				read.GroupID = "other"
			case "private":
				read.Kind, read.GroupID = EventKindPrivate, ""
			case "other-profile":
				read.ProfileID, read.ContextNamespace = "another-bot", "another-bot"
			}
			got := strings.Contains(runtime.notebookContextForRouting(context.Background(), read, "uuu默认是gnome吗"), "Ubuntu")
			want := target == "source" || (shared && target != "other-profile")
			if got != want {
				t.Fatalf("shared=%v target=%s hit=%v want=%v", shared, target, got, want)
			}
		}
	}
}
