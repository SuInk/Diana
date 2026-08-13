package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// TestParseActionAcceptsFencedJSON 验证对应功能场景。
func TestParseActionAcceptsFencedJSON(t *testing.T) {
	action, ok := parseAction("```json\n{\"action\":\"tool\",\"tool\":\"read_file\",\"input\":{\"path\":\"README.md\"}}\n```")
	if !ok {
		t.Fatal("expected JSON action")
	}
	if action.Action != "tool" || action.Tool != "read_file" || action.Input["path"] != "README.md" {
		t.Fatalf("action = %#v", action)
	}
}

// TestParseActionAcceptsFunctionCallJSON 验证兼容 Responses API function_call 形状。
func TestParseActionAcceptsFunctionCallJSON(t *testing.T) {
	action, ok := parseAction(`{"type":"function_call","name":"mcp__demo__echo","arguments":"{\"text\":\"hello\"}"}`)
	if !ok {
		t.Fatal("expected JSON action")
	}
	if action.Action != "tool" || action.Tool != "mcp__demo__echo" || action.Input["text"] != "hello" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseActionAcceptsBareToolJSON(t *testing.T) {
	action, ok := parseAction(`{"tool":"diana.reminder","arguments":{"operation":"create","delay":"1m","message":"睡觉"}}`)
	if !ok {
		t.Fatal("expected bare tool JSON action")
	}
	if action.Action != "tool" || action.Tool != "diana.reminder" || action.Input["delay"] != "1m" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseActionAcceptsFinalWithRawNewlines(t *testing.T) {
	action, ok := parseAction("{\"action\":\"final\",\"content\":\"第一行\n第二行\"}")
	if !ok {
		t.Fatal("expected lenient final action")
	}
	if action.Action != "final" || action.Content != "第一行\n第二行" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseActionRestoresDoubleEscapedFinalNewlines(t *testing.T) {
	action, ok := parseAction(`{"action":"final","content":"第一段\\n\\n第二段"}`)
	if !ok {
		t.Fatal("expected final action")
	}
	if action.Action != "final" || action.Content != "第一段\n\n第二段" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseActionRestoresMixedFinalNewlines(t *testing.T) {
	action, ok := parseAction("{\"action\":\"final\",\"content\":\"第一段\\\\n\\\\n第二段\n第三段\"}")
	if !ok {
		t.Fatal("expected lenient final action")
	}
	if action.Action != "final" || action.Content != "第一段\n\n第二段\n第三段" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseActionDoesNotRestoreToolInputNewlines(t *testing.T) {
	action, ok := parseAction(`{"action":"tool","tool":"demo","input":{"text":"第一段\\n第二段"}}`)
	if !ok {
		t.Fatal("expected tool action")
	}
	if action.Action != "tool" || action.Input["text"] != `第一段\n第二段` {
		t.Fatalf("action = %#v", action)
	}
}

// TestRunnerCallsToolAndReturnsFinal 验证对应功能场景。
func TestRunnerCallsToolAndReturnsFinal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TZ", "UTC")
	writeTestFile(t, dir, "note.txt", "hello from file")

	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"read_file","input":{"path":"note.txt"}}`,
		`{"action":"final","content":"文件里写着 hello from file"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: dir, MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读一下 note.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "文件里写着 hello from file" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Tool != "read_file" {
		t.Fatalf("Steps = %#v", resp.Steps)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	foundToolResult := false
	for _, msg := range client.requests[1].Messages {
		if strings.Contains(msg.Content, "hello from file") {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Fatalf("second request did not include tool result: %#v", client.requests[1].Messages)
	}
}

func TestRunnerCarriesRichToolResultPartsIntoNextModelTurn(t *testing.T) {
	tool := &richResultTestTool{imageURL: "data:image/png;base64,YQ=="}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"history_images","input":{"message_ids":["m1"]}}`,
		`{"action":"final","content":"看完了"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "看原图"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "看完了" || len(client.requests) != 2 {
		t.Fatalf("response=%#v requests=%d", resp, len(client.requests))
	}
	var found bool
	for _, message := range client.requests[1].Messages {
		if !strings.Contains(message.Content, `"loaded":1`) {
			continue
		}
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL == tool.imageURL {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("second request did not include rich image result: %#v", client.requests[1].Messages)
	}
}

func TestRunnerPreservesCompleteToolErrors(t *testing.T) {
	longError := strings.Repeat("tool-error-segment-", 180) + "tail-marker"
	tool := &errorTestTool{message: longError}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"error","input":{}}`,
		`{"action":"final","content":"reported"}`,
	}}
	var completedEvent RunEvent
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "执行"}},
		Observer: func(_ context.Context, event RunEvent) {
			if event.Phase == RunPhaseToolCompleted {
				completedEvent = event
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Error != longError {
		t.Fatalf("tool error was truncated: %#v", resp.Steps)
	}
	if completedEvent.Error != longError {
		t.Fatalf("observer error length = %d, want %d", len(completedEvent.Error), len(longError))
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}
	foundCompleteError := false
	for _, message := range client.requests[1].Messages {
		if strings.Contains(message.Content, longError) {
			foundCompleteError = true
			break
		}
	}
	if !foundCompleteError {
		t.Fatal("the next model turn did not receive the complete tool error")
	}
}

// TestRunnerFinalizesAfterToolBudgetExhausted 验证最后一次工具结果始终有额外收尾机会。
func TestRunnerFinalizesAfterToolBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "note.txt", "hello from file")

	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"read_file","input":{"path":"note.txt"}}`,
		`{"action":"final","content":"文件内容是 hello from file"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: dir, MaxSteps: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读一下 note.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "文件内容是 hello from file" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Tool != "read_file" {
		t.Fatalf("Steps = %#v", resp.Steps)
	}
	if len(client.requests) != 2 || !strings.Contains(client.requests[1].Messages[len(client.requests[1].Messages)-1].Content, "禁止再调用任何工具") {
		t.Fatalf("finalization request = %#v", client.requests)
	}
}

func TestRunnerDoesNotLeakEmptyFinalEnvelope(t *testing.T) {
	client := &scriptedClient{responses: []string{`{"action":"final","content":""}`}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir()}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "你好"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestRunnerDoesNotLeakMalformedToolEnvelopeAfterMaxSteps(t *testing.T) {
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"read_file","input":{"path":"broken.txt}`,
		`{"action":"final","content":"工具请求格式有误，无法读取文件。"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 1}, NewToolRegistry())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读取"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "工具请求格式有误，无法读取文件。" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestRunnerDoesNotExecuteToolRequestedDuringFinalization(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "note.txt", "hello")
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"read_file","input":{"path":"note.txt"}}`,
		`{"action":"tool","tool":"read_file","input":{"path":"note.txt"}}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: dir, MaxSteps: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "读取"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("finalization executed another tool: %#v", resp.Steps)
	}
	if !strings.Contains(resp.Text, "已经执行过的操作不会重复执行") {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestRunnerStopsAfterTerminalTool(t *testing.T) {
	tool := &terminalTestTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"terminal","input":{}}`,
		`{"action":"final","content":"should not be requested"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir()}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "执行"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "任务已启动" || len(client.requests) != 1 || len(resp.Steps) != 1 {
		t.Fatalf("resp = %#v requests = %d", resp, len(client.requests))
	}
}

// TestRunnerPromptIncludesSkills 验证 Agent prompt 只暴露 skills 清单和读取工具。
func TestRunnerPromptIncludesSkills(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetSkills([]SkillMetadata{{Name: "demo-skill", Description: "Use demo.", Path: "/tmp/demo/SKILL.md"}})
	runner := &Runner{cfg: Config{SkillsListBudget: 8000}.WithDefaults(), registry: registry}
	protocol := runner.systemPrompt()
	hint := runner.explicitSkillPrompt(Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "请用 $demo-skill"}}})
	if !strings.Contains(protocol, "demo-skill") || strings.Contains(protocol, "Explicitly Mentioned Skills") {
		t.Fatalf("stable protocol = %s", protocol)
	}
	if !strings.Contains(hint, "demo-skill") || !strings.Contains(hint, "Explicitly Mentioned Skills") {
		t.Fatalf("request hint = %s", hint)
	}
}

func TestRunnerKeepsTrustedRuntimeClockOutsideStableProtocol(t *testing.T) {
	client := &scriptedClient{}
	runner := &Runner{client: client, cfg: Config{}.WithDefaults(), registry: NewToolRegistry()}
	if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "现在几点"}}}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || len(client.requests[0].Messages) < 3 {
		t.Fatalf("requests = %#v", client.requests)
	}
	protocol := client.requests[0].Messages[0].Content
	clock := client.requests[0].Messages[1].Content
	if strings.Contains(protocol, "当前运行时钟") {
		t.Fatalf("volatile clock leaked into stable protocol: %s", protocol)
	}
	if !strings.Contains(clock, "当前运行时钟") || !strings.Contains(clock, time.Now().Format("2006-01-02")) || !strings.Contains(clock, "不要声称无法访问实时时钟") {
		t.Fatalf("clock message = %q", clock)
	}
}

func TestRunnerAppendsVolatileClockAfterCallerPrompt(t *testing.T) {
	client := &scriptedClient{}
	runner := &Runner{client: client, cfg: Config{}.WithDefaults(), registry: NewToolRegistry()}
	caller := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设提示词"},
		{Role: llm.RoleUser, Content: "【历史参考消息】旧问题"},
		{Role: llm.RoleUser, Content: "【当前需要回复的消息】现在几点"},
	}
	if _, err := runner.Run(context.Background(), Request{Messages: caller}); err != nil {
		t.Fatal(err)
	}
	messages := client.requests[0].Messages
	// 时钟必须排在调用方的稳定提示词和历史之后，否则前缀缓存从第二条起就断了。
	if !strings.Contains(messages[1].Content, "人设提示词") || !strings.Contains(messages[2].Content, "【历史参考消息】") {
		t.Fatalf("caller prompt was displaced: %#v", messages)
	}
	if !strings.Contains(messages[3].Content, RuntimeClockMarker) {
		t.Fatalf("clock is not in the trailing block: %#v", messages)
	}
	// 当前消息必须仍是最后一条，下游按最后下标判定当前轮。
	if !strings.Contains(messages[len(messages)-1].Content, "【当前需要回复的消息】") {
		t.Fatalf("current turn is no longer last: %#v", messages)
	}
}

func TestRunnerSkipsClockWhenCallerAlreadyProvidesOne(t *testing.T) {
	client := &scriptedClient{}
	runner := &Runner{client: client, cfg: Config{}.WithDefaults(), registry: NewToolRegistry()}
	caller := []llm.Message{
		{Role: llm.RoleSystem, Content: RuntimeClockMarker + "2026-08-12 23:00:00（时区 CST，UTC+08:00）"},
		{Role: llm.RoleUser, Content: "现在几点"},
	}
	if _, err := runner.Run(context.Background(), Request{Messages: caller}); err != nil {
		t.Fatal(err)
	}
	clocks := 0
	for _, message := range client.requests[0].Messages {
		if strings.Contains(message.Content, RuntimeClockMarker) {
			clocks++
		}
	}
	if clocks != 1 {
		t.Fatalf("clock injected %d times, want 1: %#v", clocks, client.requests[0].Messages)
	}
}

func TestRunnerPromptExplainsBoundedIterativeWebSearch(t *testing.T) {
	runner := &Runner{cfg: Config{MaxSteps: 8}.WithDefaults(), registry: NewToolRegistry(&countingWebSearchTool{})}
	prompt := runner.systemPrompt()
	for _, expected := range []string{"可以根据首轮结果改写 query 后继续搜索", "最多调用 3 次", "总计 8 个工具步骤", "不要把完整聊天记录塞进 query", "优先核对官方或法定披露来源"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q: %s", expected, prompt)
		}
	}
}

func TestRunnerPromptOmitsRulesForUnselectedTools(t *testing.T) {
	runner := &Runner{cfg: Config{MaxSteps: 8}.WithDefaults(), registry: NewToolRegistry(&countingWebSearchTool{})}
	prompt := runner.systemPrompt()
	for _, unexpected := range []string{"diana.reminder", "diana.schedule", "diana.image", "browser_open", "skills.read"} {
		if strings.Contains(prompt, unexpected) {
			t.Fatalf("prompt unexpectedly contains unselected tool %q: %s", unexpected, prompt)
		}
	}
}

func TestRunnerOnlyPassesQueryToWebSearch(t *testing.T) {
	tool := &countingWebSearchTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"长鑫存储 IPO 时间","num_results":10,"chat_history":"irrelevant"}}`,
		`{"action":"final","content":"done"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir()}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查一下"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(tool.lastInput) != 1 || tool.lastInput["query"] != "长鑫存储 IPO 时间" {
		t.Fatalf("web search input = %#v, want query only", tool.lastInput)
	}
	if len(resp.Steps) != 1 || len(resp.Steps[0].Input) != 1 {
		t.Fatalf("steps = %#v, want sanitized input", resp.Steps)
	}
}

func TestRunnerEnforcesPerRunWebSearchLimit(t *testing.T) {
	tool := &countingWebSearchTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"web_search.search","input":{"query":"first"}}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"second"}}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"third"}}`,
		`{"action":"tool","tool":"web_search.search","input":{"query":"fourth"}}`,
		`{"action":"final","content":"根据前三次搜索结果回答"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 5}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "查询"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != maxWebSearchCallsPerAgentRun {
		t.Fatalf("search calls = %d, want %d", tool.calls, maxWebSearchCallsPerAgentRun)
	}
	if len(resp.Steps) != 4 || !strings.Contains(resp.Steps[3].Error, "最多执行 3 次联网搜索") {
		t.Fatalf("steps = %#v", resp.Steps)
	}
	if resp.Text != "根据前三次搜索结果回答" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestRunnerProtocolRepairDoesNotConsumeToolBudget(t *testing.T) {
	tool := &countingTool{name: "demo"}
	client := &scriptedClient{responses: []string{
		`{"action":"none"}`,
		`{"action":"tool","tool":"demo","input":{"value":"ok"}}`,
		`{"action":"final","content":"done"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 1}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "执行"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 || len(resp.Steps) != 1 || resp.Steps[0].Skipped {
		t.Fatalf("tool calls=%d steps=%#v", tool.calls, resp.Steps)
	}
	if resp.Text != "done" || resp.ModelTurns != 3 || resp.FinishReason != "tool_budget_exhausted" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestRunnerSkipsConsecutiveDuplicateToolCall(t *testing.T) {
	tool := &countingTool{name: "demo"}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"demo","input":{"value":"same"}}`,
		`{"action":"tool","tool":"demo","input":{"value":"same"}}`,
		`{"action":"final","content":"used the first result"}`,
	}}
	var events []RunEvent
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 2}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "执行"}},
		TraceID:  "trace-test",
		Observer: func(_ context.Context, event RunEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 || len(resp.Steps) != 2 || !resp.Steps[1].Skipped {
		t.Fatalf("tool calls=%d steps=%#v", tool.calls, resp.Steps)
	}
	if resp.TraceID != "trace-test" || resp.Text != "used the first result" {
		t.Fatalf("response = %#v", resp)
	}
	phases := make([]RunPhase, 0, len(events))
	for _, event := range events {
		if event.TraceID != "trace-test" {
			t.Fatalf("event trace = %q", event.TraceID)
		}
		phases = append(phases, event.Phase)
	}
	for _, want := range []RunPhase{RunPhaseStarted, RunPhaseToolStarted, RunPhaseToolCompleted, RunPhaseProtocolRepair, RunPhaseCompleted} {
		if !containsRunPhase(phases, want) {
			t.Fatalf("phases=%#v, missing %q", phases, want)
		}
	}
}

func TestRunnerBoundsEveryToolCall(t *testing.T) {
	tool := &blockingTool{}
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"blocking","input":{}}`,
		`{"action":"final","content":"tool timed out"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 1, ToolTimeoutMS: 10}, NewToolRegistry(tool))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "执行"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 || !strings.Contains(resp.Steps[0].Error, "工具执行超时") {
		t.Fatalf("steps = %#v", resp.Steps)
	}
	if resp.Text != "tool timed out" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func containsRunPhase(phases []RunPhase, wanted RunPhase) bool {
	for _, phase := range phases {
		if phase == wanted {
			return true
		}
	}
	return false
}

type scriptedClient struct {
	responses []string
	requests  []llm.GenerateRequest
}

type terminalTestTool struct{}

type countingWebSearchTool struct {
	calls     int
	lastInput map[string]any
}

type countingTool struct {
	name  string
	calls int
}

type blockingTool struct{}

type errorTestTool struct {
	message string
}

type richResultTestTool struct {
	imageURL string
}

func (*countingWebSearchTool) Name() string        { return webSearchToolName }
func (*countingWebSearchTool) Description() string { return "test web search tool" }
func (t *countingWebSearchTool) Run(_ context.Context, input map[string]any) (string, error) {
	t.calls++
	t.lastInput = input
	return `{"status":"ok"}`, nil
}

func (t *countingTool) Name() string      { return t.name }
func (*countingTool) Description() string { return "test tool" }
func (t *countingTool) Run(context.Context, map[string]any) (string, error) {
	t.calls++
	return `{"status":"ok"}`, nil
}

func (*blockingTool) Name() string        { return "blocking" }
func (*blockingTool) Description() string { return "waits for context cancellation" }
func (*blockingTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (*errorTestTool) Name() string        { return "error" }
func (*errorTestTool) Description() string { return "returns a test error" }
func (t *errorTestTool) Run(context.Context, map[string]any) (string, error) {
	return "", errors.New(t.message)
}

func (*richResultTestTool) Name() string        { return "history_images" }
func (*richResultTestTool) Description() string { return "loads historical images" }
func (*richResultTestTool) Run(context.Context, map[string]any) (string, error) {
	return `{"loaded":1}`, nil
}
func (t *richResultTestTool) ToolResultParts(string) []llm.ContentPart {
	return []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: t.imageURL, Detail: "auto"}}
}

func (*terminalTestTool) Name() string        { return "terminal" }
func (*terminalTestTool) Description() string { return "test terminal tool" }
func (*terminalTestTool) Run(context.Context, map[string]any) (string, error) {
	return `{"message":"任务已启动"}`, nil
}
func (*terminalTestTool) TerminalResult(string) (string, bool) { return "任务已启动", true }

// Generate 调用当前模型 provider 生成回复。
func (c *scriptedClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.responses) == 0 {
		return &llm.GenerateResponse{Text: `{"action":"final","content":"done"}`}, nil
	}
	next := c.responses[0]
	c.responses = c.responses[1:]
	return &llm.GenerateResponse{Text: next}, nil
}
