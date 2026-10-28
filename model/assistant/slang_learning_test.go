// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// slangNotebookStore 是带候选表的内存笔记本：黑话学习按类型断言从笔记本存储上取候选表。
type slangNotebookStore struct {
	*memoryNotebookStore
	mu         sync.Mutex
	candidates map[string]*SlangCandidate
	researched map[string]time.Time
}

func newSlangNotebookStore() *slangNotebookStore {
	return &slangNotebookStore{memoryNotebookStore: newMemoryNotebookStore(), candidates: map[string]*SlangCandidate{}, researched: map[string]time.Time{}}
}

func slangKey(profile, group, term string) string {
	return profile + "|" + group + "|" + NormalizeNotebookTitle(term)
}

func (s *slangNotebookStore) ObserveSlangCandidate(_ context.Context, observation SlangObservation) (SlangCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := slangKey(observation.ProfileID, observation.GroupID, observation.Term)
	candidate := s.candidates[key]
	if candidate == nil {
		candidate = &SlangCandidate{ProfileID: observation.ProfileID, GroupID: observation.GroupID, Term: observation.Term,
			Status: SlangCandidatePending, NextThreshold: slangResearchFirstThreshold}
		s.candidates[key] = candidate
	}
	candidate.Count++
	found := false
	for _, speaker := range candidate.Speakers {
		found = found || speaker == observation.SpeakerID
	}
	if !found {
		candidate.Speakers = append(candidate.Speakers, observation.SpeakerID)
	}
	candidate.Evidence = append(candidate.Evidence, observation.Evidence)
	return *candidate, nil
}

func (s *slangNotebookStore) UpdateSlangCandidate(_ context.Context, profileID, groupID, term string, status SlangCandidateStatus, next int, research bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := slangKey(profileID, groupID, term)
	if candidate := s.candidates[key]; candidate != nil {
		candidate.Status, candidate.NextThreshold = status, next
	}
	if research {
		s.researched[key] = now
	}
	return nil
}

func (s *slangNotebookStore) CountSlangResearch(_ context.Context, profileID string, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for key, at := range s.researched {
		if strings.HasPrefix(key, profileID+"|") && !at.Before(since) {
			count++
		}
	}
	return count, nil
}

func (s *slangNotebookStore) candidate(profile, group, term string) SlangCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if candidate := s.candidates[slangKey(profile, group, term)]; candidate != nil {
		return *candidate
	}
	return SlangCandidate{}
}

type fakeSlangSearch struct {
	result string
	calls  int
}

func (f *fakeSlangSearch) Name() string        { return "web_search" }
func (f *fakeSlangSearch) Description() string { return "fake" }
func (f *fakeSlangSearch) Run(context.Context, map[string]any) (string, error) {
	f.calls++
	return f.result, nil
}

func slangGroupEvent(user, text string) MessageEvent {
	return MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "10001", UserID: user, SenderName: "群友" + user, MessageID: "m-" + user, Time: 1700000000}
}

func TestParseSlangGateTermsNormalizesAndLimits(t *testing.T) {
	raw := `{"memories":[],"unknown_terms":[{"term":" 「开盒」 "},{"term":"开盒"},{"term":"x"},{"term":"一个超过八个字的长说法啊啊"},{"term":"yyds","source_index":1},{"term":"视奸号"},{"term":"多余"}]}`
	got := parseSlangGateTerms(raw)
	var terms []string
	for _, item := range got {
		terms = append(terms, item.Term)
	}
	if want := []string{"开盒", "yyds", "视奸号"}; !reflect.DeepEqual(terms, want) {
		t.Fatalf("terms = %v, want %v", terms, want)
	}
	if got[1].SourceIndex == nil || *got[1].SourceIndex != 1 {
		t.Fatalf("source index lost: %+v", got[1])
	}
	if parseSlangGateTerms("not json") != nil {
		t.Fatal("garbage should yield no terms")
	}
}

// 关着的机器人门控工具一字不变，缓存前缀不受影响。
func TestMemoryGateToolUnchangedWhenSlangDisabled(t *testing.T) {
	if !reflect.DeepEqual(memoryGateSubmitToolFor(false), memoryGateSubmitTool()) {
		t.Fatal("disabled slang learning must not change the gate tool")
	}
	props := asSchemaMap(memoryGateSubmitToolFor(true).Parameters["properties"])
	if _, ok := props["unknown_terms"]; !ok {
		t.Fatal("enabled slang learning should add unknown_terms")
	}
	if _, ok := asSchemaMap(memoryGateSubmitTool().Parameters["properties"])["unknown_terms"]; ok {
		t.Fatal("enabling must not mutate the shared base tool")
	}
}

func TestSlangLearningOnlyForEnabledGroups(t *testing.T) {
	on := BotConfig{SlangLearningEnabled: boolPointer(true)}
	if !slangLearningActive(on, slangGroupEvent("1", "")) {
		t.Fatal("enabled group should learn")
	}
	if slangLearningActive(BotConfig{}, slangGroupEvent("1", "")) {
		t.Fatal("default must be off")
	}
	private := slangGroupEvent("1", "")
	private.Kind, private.GroupID = EventKindPrivate, ""
	if slangLearningActive(on, private) {
		t.Fatal("private chats have no group context to learn from")
	}
}

// 门控标出的词要原样出现在消息里，已在笔记本（含作废的）的词不再攒。
func TestObserveSlangTermsSkipsInventedAndKnownTerms(t *testing.T) {
	store := newSlangNotebookStore()
	runtime := NewRuntime(BotConfig{SlangLearningEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetNotebookStore(store)
	event := slangGroupEvent("1", "")
	scope := notebookGlobalScope(event.ProfileID)
	if _, _, err := store.UpsertNotebookEntry(context.Background(), NotebookUpsertRequest{ScopeKey: scope, Term: "开摆", Meaning: "放弃挣扎"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpsertNotebookEntry(context.Background(), NotebookUpsertRequest{ScopeKey: scope, Term: "破防", Meaning: "被戳中"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.DeleteNotebookEntry(context.Background(), scope, "破防", "owner", "主人", "不要", time.Now()); err != nil {
		t.Fatal(err)
	}
	text := "今天又开摆了，破防，被开盒"
	runtime.observeSlangTerms(context.Background(), []MessageEvent{event}, []string{text},
		[]slangGateTerm{{Term: "开摆"}, {Term: "破防"}, {Term: "开盒"}, {Term: "视奸号"}})
	if got := store.candidate("bot-a", "10001", "开盒"); got.Count != 1 {
		t.Fatalf("开盒 should be observed once, got %+v", got)
	}
	for _, term := range []string{"开摆", "破防", "视奸号"} {
		if got := store.candidate("bot-a", "10001", term); got.Count != 0 {
			t.Fatalf("%s should be skipped, got %+v", term, got)
		}
	}
}

func TestSlangCandidateReadyNeedsCountAndSpeakers(t *testing.T) {
	base := SlangCandidate{Status: SlangCandidatePending, NextThreshold: 3}
	cases := []struct {
		count    int
		speakers []string
		want     bool
	}{
		{2, []string{"a", "b"}, false},
		{3, []string{"a"}, false},
		{3, []string{"a", "b"}, true},
	}
	for _, tc := range cases {
		candidate := base
		candidate.Count, candidate.Speakers = tc.count, tc.speakers
		if got := slangCandidateReady(candidate); got != tc.want {
			t.Fatalf("count=%d speakers=%v ready=%v want %v", tc.count, tc.speakers, got, tc.want)
		}
	}
	learned := base
	learned.Count, learned.Speakers, learned.Status = 9, []string{"a", "b"}, SlangCandidateLearned
	if slangCandidateReady(learned) {
		t.Fatal("learned candidates must not be researched again")
	}
}

func slangResearchRuntime(t *testing.T, reply string) (*Runtime, *slangNotebookStore, *capturingLLMProvider) {
	t.Helper()
	store := newSlangNotebookStore()
	provider := &capturingLLMProvider{reply: reply}
	profiles := &stubLLMProfileStore{set: llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "main", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "main"}},
	}}}
	runtime := NewRuntime(BotConfig{SlangLearningEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), profiles, nil, nil, nil)
	runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return provider, nil })
	runtime.SetNotebookStore(store)
	return runtime, store, provider
}

func observedSlangCandidate(t *testing.T, store *slangNotebookStore, term string, speakers ...string) SlangCandidate {
	t.Helper()
	var candidate SlangCandidate
	for _, speaker := range speakers {
		var err error
		candidate, err = store.ObserveSlangCandidate(context.Background(), SlangObservation{
			ProfileID: "bot-a", GroupID: "10001", Term: term, SpeakerID: speaker,
			Evidence: SlangEvidence{Sender: speaker, Text: "被" + term + "了"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return candidate
}

func TestSlangResearchWritesConfirmedTermToNotebook(t *testing.T) {
	runtime, store, provider := slangResearchRuntime(t, `{"confirmed":true,"meaning":"把别人的隐私信息扒出来公开","example":"小号被开盒了","sources":["https://example.com/a"]}`)
	candidate := observedSlangCandidate(t, store, "开盒", "1", "2", "1")
	event := slangGroupEvent("1", "")
	search := &fakeSlangSearch{result: "开盒：网络用语，指公开他人隐私"}
	if err := runtime.researchSlangCandidateWith(context.Background(), event, runtime.effectiveConfigForEvent(event), candidate, search, time.Now()); err != nil {
		t.Fatal(err)
	}
	if search.calls != 1 || provider.calls != 1 {
		t.Fatalf("search calls=%d judge calls=%d", search.calls, provider.calls)
	}
	entry, found, _ := store.NotebookEntryDetail(context.Background(), notebookGlobalScope("bot-a"), "开盒")
	if !found || entry.Meaning != "把别人的隐私信息扒出来公开" || entry.EditorUserID != SlangAutoEditorID || !strings.Contains(entry.Note, "https://example.com/a") {
		t.Fatalf("notebook entry = %+v found=%v", entry, found)
	}
	if got := store.candidate("bot-a", "10001", "开盒"); got.Status != SlangCandidateLearned {
		t.Fatalf("candidate status = %s", got.Status)
	}
	if !strings.Contains(provider.request.Messages[1].Content, "开盒：网络用语") {
		t.Fatal("judge should see the search results")
	}
}

// 没查实不写笔记本，门槛翻倍等下次；翻过上限就不再查。
func TestSlangResearchDefersUnconfirmedTerm(t *testing.T) {
	runtime, store, _ := slangResearchRuntime(t, `{"confirmed":false,"meaning":"","sources":[]}`)
	candidate := observedSlangCandidate(t, store, "查医生", "1", "2", "3")
	event := slangGroupEvent("1", "")
	if err := runtime.researchSlangCandidateWith(context.Background(), event, runtime.effectiveConfigForEvent(event), candidate, &fakeSlangSearch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.NotebookEntryDetail(context.Background(), notebookGlobalScope("bot-a"), "查医生"); found {
		t.Fatal("unconfirmed term must not be written")
	}
	got := store.candidate("bot-a", "10001", "查医生")
	if got.Status != SlangCandidatePending || got.NextThreshold != 6 {
		t.Fatalf("candidate = %+v, want pending with threshold 6", got)
	}
	candidate.NextThreshold = slangResearchMaxThreshold
	if err := runtime.researchSlangCandidateWith(context.Background(), event, runtime.effectiveConfigForEvent(event), candidate, &fakeSlangSearch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := store.candidate("bot-a", "10001", "查医生"); got.Status != SlangCandidateUnresolved {
		t.Fatalf("candidate status = %s, want unresolved", got.Status)
	}
}

// 查证期间有人手动记过（或作废过）这个词，以人为准，不覆盖。
func TestSlangResearchDoesNotOverwriteManualEntry(t *testing.T) {
	runtime, store, provider := slangResearchRuntime(t, `{"confirmed":true,"meaning":"自动查到的意思"}`)
	candidate := observedSlangCandidate(t, store, "开盒", "1", "2", "3")
	if _, _, err := store.UpsertNotebookEntry(context.Background(), NotebookUpsertRequest{ScopeKey: notebookGlobalScope("bot-a"), Term: "开盒", Meaning: "主人写的意思"}); err != nil {
		t.Fatal(err)
	}
	event := slangGroupEvent("1", "")
	if err := runtime.researchSlangCandidateWith(context.Background(), event, runtime.effectiveConfigForEvent(event), candidate, &fakeSlangSearch{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	entry, _, _ := store.NotebookEntryDetail(context.Background(), notebookGlobalScope("bot-a"), "开盒")
	if entry.Meaning != "主人写的意思" || provider.calls != 0 {
		t.Fatalf("manual entry overwritten or judged anyway: %+v calls=%d", entry, provider.calls)
	}
	if got := store.candidate("bot-a", "10001", "开盒"); got.Status != SlangCandidateKnown {
		t.Fatalf("candidate status = %s, want known", got.Status)
	}
}

func TestSlangResearchRespectsDailyLimit(t *testing.T) {
	runtime, store, provider := slangResearchRuntime(t, `{"confirmed":true,"meaning":"x"}`)
	now := time.Now()
	for index := 0; index < slangResearchDailyLimit; index++ {
		store.researched[slangKey("bot-a", "10001", "词"+string(rune('a'+index)))] = now
	}
	candidate := observedSlangCandidate(t, store, "开盒", "1", "2", "3")
	if err := runtime.researchSlangCandidate(context.Background(), slangGroupEvent("1", ""), candidate); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatal("daily limit reached; research must not run")
	}
}

func runSlangGate(t *testing.T, enabled bool) (*capturingLLMProvider, *slangNotebookStore) {
	t.Helper()
	memory := &testStructuredMemoryStore{}
	provider := &capturingLLMProvider{reply: `{"memories":[],"unknown_terms":[{"term":"开盒"},{"term":"编出来的词"}]}`}
	cfg := BotConfig{}
	if enabled {
		cfg.SlangLearningEnabled = boolPointer(true)
	}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	runtime.SetStructuredMemoryStore(memory)
	notebook := newSlangNotebookStore()
	runtime.SetNotebookStore(notebook)
	err := runtime.processEventMemoryJobs(context.Background(), memory, []MemoryJobPayload{{
		Kind: MemoryJobEvent, Session: "group:10001", Event: MessageEvent{
			Kind: EventKindGroup, GroupID: "10001", UserID: "u1", MessageID: "m1",
			Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "小号冲浪被当场开盒了"}}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return provider, notebook
}

func TestMemoryGateCarriesSlangFieldOnlyWhenEnabled(t *testing.T) {
	provider, notebook := runSlangGate(t, false)
	if strings.Contains(provider.request.Messages[0].Content, "unknown_terms") {
		t.Fatal("disabled bot's gate prompt must not mention unknown_terms")
	}
	if _, ok := asSchemaMap(provider.request.Tools[0].Parameters["properties"])["unknown_terms"]; ok {
		t.Fatal("disabled bot's gate tool must not carry unknown_terms")
	}
	if got := notebook.candidate("", "10001", "开盒"); got.Count != 0 {
		t.Fatal("disabled bot must not record candidates")
	}

	provider, notebook = runSlangGate(t, true)
	if !strings.HasSuffix(provider.request.Messages[0].Content, slangGateRule) {
		t.Fatal("enabled bot's gate prompt should end with the slang rule")
	}
	if got := notebook.candidate("", "10001", "开盒"); got.Count != 1 || len(got.Evidence) != 1 {
		t.Fatalf("开盒 should be recorded with evidence, got %+v", got)
	}
	if got := notebook.candidate("", "10001", "编出来的词"); got.Count != 0 {
		t.Fatal("a term absent from the message must be dropped")
	}
}
