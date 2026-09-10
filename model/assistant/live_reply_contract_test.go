package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type replyContractSample struct {
	Arm       string                `json:"arm"`
	Case      string                `json:"case"`
	Repeat    int                   `json:"repeat"`
	Input     string                `json:"input"`
	Response  *llm.GenerateResponse `json:"response"`
	Bubbles   []string              `json:"bubbles"`
	Issues    []string              `json:"issues"`
	MaxRunes  int                   `json:"max_bubble_runes"`
	ElapsedMS int64                 `json:"elapsed_ms"`
	Error     string                `json:"error,omitempty"`
}

func replyContractRequest(array bool) llm.GenerateRequest {
	prompt := defaultSystemPrompt + "\n" + ReplyStyleHuman.prompt(true, personaVoice{}) + "\n" + ReplyStyleHuman.closingAnchor()
	// Preserve the earlier A/B experiment's prompt baseline.
	prompt = strings.ReplaceAll(prompt, replyDocumentDeliveryRule, "")
	prompt = strings.ReplaceAll(prompt, replyLineBreakChoiceRule, "")
	prompt = strings.ReplaceAll(prompt, replyDeliveryChoiceRule, "")
	field := "content"
	property := map[string]any{"type": "string", "description": "给用户看的最终自然语言回复，必填且不能为空。不要写成 JSON，也不要出现内部协议字段。"}
	if array {
		field = "messages"
		property = map[string]any{
			"type": "array", "items": map[string]any{"type": "string"},
			"description": "按发送顺序排列的完整消息，每个元素直接作为一条消息发送；数组和各元素均不能为空。",
		}
		prompt = strings.ReplaceAll(prompt, replySegmentationRule, "通过 agent.finalize 的 messages 数组提交回复，每个元素就是一次完整发言。元素内部的换行只负责排版，不会另发消息。不要输出分条标记，也不要把协议字段写进正文。")
		prompt = strings.ReplaceAll(prompt, replyBlankLineRule, "每条消息内部可以使用必要的段落、列表和代码换行，代码及引用原文保留格式。不要为了排版增加消息数组元素。")
		prompt = strings.ReplaceAll(prompt, "需要另发时写 "+notificationSplitMarker, "需要另发时另建 messages 元素")
	}
	// Keep example wording and boundaries identical, but encode each example
	// in its arm's protocol so legacy markers cannot contradict the array arm.
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "你：") {
			continue
		}
		answer := strings.TrimPrefix(line, "你：")
		var value any = answer
		if array {
			value = strings.Split(answer, notificationSplitMarker)
		}
		encoded, _ := json.Marshal(map[string]any{field: value})
		lines[i] = "你调用 agent.finalize：" + string(encoded)
	}
	prompt = strings.Join(lines, "\n")
	prompt += "\n每条消息表达完整，不拆散标题和正文，不逐句发送。"
	prompt += "\n本轮无需外部工具，请调用 agent.finalize，将完整回复写入 " + field + "。"
	return llm.GenerateRequest{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: prompt}},
		Tools: []llm.ToolDefinition{{
			Name: "agent.finalize", Description: "结束本轮并提交最终答复。", Strict: true,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{field: property}, "required": []string{field}, "additionalProperties": false},
		}},
		ToolChoice: "agent.finalize", MaxOutputTokens: 8192,
	}
}

func decodeReplyContract(response *llm.GenerateResponse, array bool) ([]string, error) {
	if response == nil || len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "agent.finalize" {
		return nil, fmt.Errorf("expected exactly one agent.finalize call")
	}
	args := response.ToolCalls[0].Arguments
	if len(args) != 1 {
		return nil, fmt.Errorf("expected exactly one response field")
	}
	if !array {
		content, ok := args["content"].(string)
		if !ok || strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("content is not a nonempty string")
		}
		return splitChatReply(content, chatSplitLimits{}), nil
	}
	raw, err := json.Marshal(args["messages"])
	if err != nil {
		return nil, err
	}
	var messages []string
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("messages is not an array of strings")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages is empty")
	}
	for _, message := range messages {
		if strings.TrimSpace(message) == "" {
			return nil, fmt.Errorf("messages contains an empty element")
		}
	}
	return messages, nil
}

func replyContractIssues(kind string, bubbles []string) []string {
	var issues []string
	for i, bubble := range bubbles {
		if strings.TrimSpace(bubble) == "" {
			issues = append(issues, fmt.Sprintf("bubble %d is empty", i+1))
		}
		if strings.Contains(bubble, notificationSplitMarker) {
			issues = append(issues, fmt.Sprintf("bubble %d leaks a split marker", i+1))
		}
		onlyHeadings := true
		for _, line := range strings.Split(strings.TrimSpace(bubble), "\n") {
			if strings.TrimSpace(line) != "" {
				onlyHeadings = onlyHeadings && isDocumentSectionLabel(line)
			}
		}
		if onlyHeadings && strings.TrimSpace(bubble) != "" {
			issues = append(issues, fmt.Sprintf("bubble %d has a title without body", i+1))
		}
	}
	switch kind {
	case "short":
		if len(bubbles) != 1 || bubbles[0] != "在的" {
			issues = append(issues, "short reply differs from the requested single message")
		}
	case "explicit":
		if len(bubbles) != 2 {
			issues = append(issues, "explicit two-message request did not produce two messages")
		}
	case "list":
		valid := false
		if len(bubbles) == 1 {
			doc := replyMarkdownParser.Parse(text.NewReader([]byte(bubbles[0])))
			list, ok := doc.FirstChild().(*ast.List)
			valid = ok && list.IsOrdered() && list.ChildCount() == 3 && list.NextSibling() == nil
		}
		if !valid {
			issues = append(issues, "requested three-item numbered list is not one complete message")
		}
	case "code":
		valid := false
		if len(bubbles) == 1 {
			source := []byte(bubbles[0])
			doc := replyMarkdownParser.Parse(text.NewReader(source))
			code, ok := doc.FirstChild().(*ast.FencedCodeBlock)
			valid = ok && code.NextSibling() == nil && string(code.Language(source)) == "python" && strings.TrimSpace(string(code.Lines().Value(source))) == "print(1)\nprint(2)"
		}
		if !valid {
			issues = append(issues, "requested Python code is not one intact code block")
		}
	}
	return issues
}

func TestReplyContractProbeValidation(t *testing.T) {
	if strings.Contains(replyContractRequest(true).Messages[0].Content, notificationSplitMarker) {
		t.Fatal("array prompt contains legacy split markers")
	}
	if !strings.Contains(replyContractRequest(false).Messages[0].Content, notificationSplitMarker) {
		t.Fatal("content prompt lost explicit marker guidance")
	}
	for _, invalid := range []any{nil, "text", []any{}, []any{""}, []any{"ok", 4}, []any{" "}} {
		response := &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{Name: "agent.finalize", Arguments: map[string]any{"messages": invalid}}}}
		if _, err := decodeReplyContract(response, true); err == nil {
			t.Fatalf("accepted invalid array: %#v", invalid)
		}
	}
	want := []string{"## Title\nBody", "```python\nprint(1)\nprint(2)\n```"}
	response := &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{Name: "agent.finalize", Arguments: map[string]any{"messages": want}}}}
	got, err := decodeReplyContract(response, true)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("array delivery changed content: %q, %v", got, err)
	}
	for kind, bubbles := range map[string][]string{
		"short": {"在的"}, "explicit": {"First", "Second"},
		"list": {"1. First\n2. Second\n3. Third"}, "code": {want[1]},
	} {
		if issues := replyContractIssues(kind, bubbles); len(issues) != 0 {
			t.Fatalf("valid %s failed: %v", kind, issues)
		}
		if issues := replyContractIssues(kind, []string{"## Orphan"}); len(issues) == 0 {
			t.Fatalf("invalid %s passed", kind)
		}
	}
}

// Final-turn-only A/B experiment. No production runner or channel is changed.
// Requests are interleaved, with alternating arm order and no repair retries.
func TestLiveReplyContractAB(t *testing.T) {
	client := liveLLMClient(t)
	cases := []struct{ name, input string }{
		{"short", "只回复：在的"},
		{"news", "我拿到 offer 了！就是要搬去一个完全不熟的城市，有点慌"},
		{"explicit", "先回应我终于修好 bug 这件事，再单独问我是哪一步出了问题，分成两次发言"},
		{"travel", "帮我做成都两天的详细行程，第一次去，不自驾，每天上午下午晚上怎么安排，交通和需要提前准备的事也写清楚"},
		{"list", "只给我三步整理桌面的方法，用编号列表，不加开头结尾"},
		{"code", "只输出 Python 代码块，包含两行：print(1) 和 print(2)"},
	}
	for _, array := range []bool{false, true} {
		config, err := json.Marshal(map[string]any{"array": array, "request": replyContractRequest(array), "model": os.Getenv("DIANA_TEST_LLM_MODEL")})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("AB_CONFIG %s", config)
	}
	for _, tc := range cases {
		for repeat := 1; repeat <= 3; repeat++ {
			for _, array := range []bool{repeat%2 == 0, repeat%2 != 0} {
				arm := "A_content"
				if array {
					arm = "B_messages"
				}
				t.Run(fmt.Sprintf("%s/%d/%s", tc.name, repeat, arm), func(t *testing.T) {
					req := replyContractRequest(array)
					req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: tc.input})
					ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
					defer cancel()
					start := time.Now()
					response, err := client.Generate(ctx, req)
					sample := replyContractSample{Arm: arm, Case: tc.name, Repeat: repeat, Input: tc.input, Response: response, ElapsedMS: time.Since(start).Milliseconds()}
					if err == nil {
						sample.Bubbles, err = decodeReplyContract(response, array)
					}
					if err != nil {
						sample.Error = strings.ReplaceAll(err.Error(), os.Getenv("DIANA_TEST_LLM_API_KEY"), "[redacted]")
					} else {
						sample.Issues = replyContractIssues(tc.name, sample.Bubbles)
						for _, bubble := range sample.Bubbles {
							sample.MaxRunes = max(sample.MaxRunes, len([]rune(bubble)))
						}
					}
					raw, marshalErr := json.Marshal(sample)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					t.Logf("AB_SAMPLE %s", raw)
					if sample.Error != "" || len(sample.Issues) > 0 {
						t.Errorf("contract failed: error=%s issues=%v", sample.Error, sample.Issues)
					}
				})
			}
		}
	}
}
