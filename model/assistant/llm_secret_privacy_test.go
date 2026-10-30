// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 密钥用拼接造出来，免得仓库里出现能被密钥扫描当真的整串；证件号、卡号、手机号
// 都是按校验规则合成的。
var (
	testSecretKey = "sk-" + "proj-" + strings.Repeat("Ab3_xY9-", 19) + "upkA"
	testIDCard    = "110105199003071239"
	testBankCard  = "6222021000000005"
	testPhone     = "13912340000"
)

// recordingSecretProvider 记下发给模型的请求，并把请求里的第一个占位符抄进回复，
// 模拟模型照抄占位符调用工具。
type recordingSecretProvider struct {
	requests []llm.GenerateRequest
	reply    func(request string) *llm.GenerateResponse
}

func (p *recordingSecretProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	var all strings.Builder
	for _, message := range req.Messages {
		all.WriteString(message.Content)
		all.WriteString("\n")
	}
	return p.reply(all.String()), nil
}

func newTestSecretPrivacyProvider(inner LLMProvider, local map[string]bool) *secretPrivacyProvider {
	return &secretPrivacyProvider{provider: inner, salt: "test-salt", local: local, known: &secretContextRegistry{}}
}

// 线上：模型第二次重抄 164 位密钥时漏了一个字符，接口回 401。换成占位符后模型只抄
// 占位符，执行前换回的必须是完整原文；回复正文里只给掩码。
func TestSecretPrivacyRestoresExactValueForToolsAndMasksReply(t *testing.T) {
	inner := &recordingSecretProvider{reply: func(request string) *llm.GenerateResponse {
		placeholder := secretmask.PlaceholderPattern.FindString(request)
		return &llm.GenerateResponse{
			Text: "已用 " + placeholder + " 测试",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Name: "host_access",
				Arguments: map[string]any{
					"command": "/usr/bin/curl",
					"args":    []any{"-H", "Authorization: Bearer " + placeholder, "https://api.openai.com/v1/models"},
				},
			}},
		}
	}}
	provider := newTestSecretPrivacyProvider(inner, nil)
	response, err := provider.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "你是助手"},
		{Role: llm.RoleUser, Content: testSecretKey + " 帮忙测试下这个 apikey"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sent := inner.requests[0]
	for _, message := range sent.Messages {
		if strings.Contains(message.Content, testSecretKey) {
			t.Fatalf("key reached the model: %q", message.Content)
		}
	}
	if !strings.Contains(sent.Messages[0].Content, "【敏感信息隐私代理】") {
		t.Fatalf("privacy prompt missing from system message: %q", sent.Messages[0].Content)
	}
	args := response.ToolCalls[0].Arguments["args"].([]any)
	if args[1] != "Authorization: Bearer "+testSecretKey {
		t.Fatalf("tool argument not restored to the exact key: %q", args[1])
	}
	if strings.Contains(response.Text, testSecretKey) || !strings.Contains(response.Text, secretmask.Mask(testSecretKey)) {
		t.Fatalf("reply should carry the masked key only: %q", response.Text)
	}
}

// 同一个值换哪都是同一个占位符，历史逐字不变，前缀缓存才能命中。
func TestSecretPrivacyPlaceholderIsStable(t *testing.T) {
	first := newSecretPrivacyScope("test-salt", nil, nil).protectText("key " + testSecretKey)
	second := newSecretPrivacyScope("test-salt", nil, nil).protectText("又发了一次 " + testSecretKey)
	placeholder := secretmask.PlaceholderPattern.FindString(first)
	if placeholder == "" || !strings.Contains(second, placeholder) {
		t.Fatalf("placeholder changed between scopes: %q vs %q", first, second)
	}
	if !strings.HasPrefix(placeholder, secretmask.PlaceholderPrefix+"api_key_") {
		t.Fatalf("unexpected placeholder %q", placeholder)
	}
}

func TestSecretPrivacyFindsPersonalDataAndSkipsLookalikes(t *testing.T) {
	scope := newSecretPrivacyScope("test-salt", nil, nil)
	text := strings.Join([]string{
		"身份证 " + testIDCard,
		"卡号 6222 0210 0000 0005",
		"电话 " + testPhone,
		"邮箱 alice.w@mail.test.cn",
		// 下面这些不该换。
		"订单 110105199003071230", // 校验位不对
		"卡 6222021000000006",    // Luhn 不过
		"max_tokens: 4096",
		"git@github.com:SuInk/diana.git",
		"群 1234567890",
	}, "\n")
	protected := scope.protectText(text)
	for _, kind := range []string{"id_card", "bank_card", "phone", "email"} {
		if !strings.Contains(protected, secretmask.PlaceholderPrefix+kind+"_") {
			t.Errorf("%s not masked:\n%s", kind, protected)
		}
	}
	for _, keep := range []string{"110105199003071230", "6222021000000006", "max_tokens: 4096", "git@github.com", "1234567890"} {
		if !strings.Contains(protected, keep) {
			t.Errorf("%q should stay as written:\n%s", keep, protected)
		}
	}
}

// 「密码是 xxx」只能靠上下文认出来；同一个值出现在别处（工具输出、下一条命令）
// 也得换成同一个占位符。
func TestSecretPrivacyMasksContextSecretEverywhereInRequest(t *testing.T) {
	scope := newSecretPrivacyScope("test-salt", nil, &secretContextRegistry{})
	protected := scope.protectRequest(llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "数据库密码是 Hunter2pass"},
		{Role: llm.RoleTool, Content: "mysql -pHunter2pass 连接成功；Hunter2pass"},
	}})
	for _, message := range protected.Messages {
		if strings.Contains(message.Content, "Hunter2pass ") || strings.HasSuffix(message.Content, "Hunter2pass") {
			t.Fatalf("context secret leaked: %q", message.Content)
		}
	}
	// 进程级名单记住了它：下一次请求里它单独出现也认得。
	next := newSecretPrivacyScope("test-salt", nil, scope.known).protectText("结果：Hunter2pass")
	if strings.Contains(next, "Hunter2pass") {
		t.Fatalf("remembered secret not masked in a later request: %q", next)
	}
}

// Agent 回放上一轮的工具调用时，参数已经换回了原值，重新发给模型前要再换回去。
func TestSecretPrivacyRemasksReplayedToolCalls(t *testing.T) {
	scope := newSecretPrivacyScope("test-salt", nil, nil)
	protected := scope.protectRequest(llm.GenerateRequest{Messages: []llm.Message{{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "host_access", Arguments: map[string]any{
			"args": []any{"-H", "Authorization: Bearer " + testSecretKey},
		}}},
	}}})
	arg := protected.Messages[1].ToolCalls[0].Arguments["args"].([]any)[1].(string)
	if strings.Contains(arg, testSecretKey) || !secretmask.PlaceholderPattern.MatchString(arg) {
		t.Fatalf("replayed tool call not masked: %q", arg)
	}
}

// 说给人看的话：个人信息本会话里出现过的给原文，别处带进来的给掩码；send 工具的
// 参数按回复处理，其余工具一律换回原值。
func TestSecretPrivacyReplyRestoresOnlyLocalPersonalData(t *testing.T) {
	scope := newSecretPrivacyScope("test-salt", map[string]bool{testPhone: true}, nil)
	protected := scope.protectText("本群 " + testPhone + "，记忆里 " + testIDCard)
	reply := scope.restoreForReply(protected)
	if !strings.Contains(reply, testPhone) {
		t.Fatalf("local phone should be restored: %q", reply)
	}
	if strings.Contains(reply, testIDCard) || !strings.Contains(reply, "110"+secretmask.Marker+"1239") {
		t.Fatalf("foreign id card should be masked: %q", reply)
	}
	calls := scope.restoreToolCalls([]llm.ToolCall{
		{Name: agent.FinalizeToolName, Arguments: map[string]any{"text": protected}},
		{Name: "reminder", Arguments: map[string]any{"note": protected}},
	})
	if text := calls[0].Arguments["text"].(string); strings.Contains(text, testIDCard) {
		t.Fatalf("send text should follow the reply policy: %q", text)
	}
	if note := calls[1].Arguments["note"].(string); !strings.Contains(note, testIDCard) {
		t.Fatalf("other tools get the original value: %q", note)
	}
}

func TestWithSecretPrivacyLocalCollectsConversationValues(t *testing.T) {
	ctx := withSecretPrivacyLocal(context.Background(),
		MessageEvent{RawMessage: "我手机 " + testPhone},
		[]MessageEvent{{Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "卡 " + testBankCard}}}}},
	)
	local := secretPrivacyLocalFromContext(ctx)
	if !local[testPhone] || !local[testBankCard] {
		t.Fatalf("local values = %v", local)
	}
}

func TestMaskSecretsForTraceUsesModelPlaceholders(t *testing.T) {
	runtime := NewRuntime(
		BotConfig{LLMSecretMaskingEnabled: boolPointer(true)},
		nilChannel{}, NewPluginManager(), nil, nil, nil,
		func() (LLMProvider, error) { return &sequenceLLMProvider{}, nil },
	)
	input, output := runtime.maskSecretsForTrace(context.Background(),
		map[string]any{"args": []any{"-H", "Authorization: Bearer " + testSecretKey}},
		"HTTP 200 for "+testSecretKey,
	)
	arg := input["args"].([]any)[1].(string)
	if strings.Contains(arg, testSecretKey) || strings.Contains(output, testSecretKey) {
		t.Fatalf("trace kept the key: %q / %q", arg, output)
	}
	if !strings.HasPrefix(arg, "Authorization: Bearer "+secretmask.PlaceholderPrefix+"api_key_") {
		t.Fatalf("auth scheme should stay readable: %q", arg)
	}
	if secretmask.PlaceholderPattern.FindString(arg) != secretmask.PlaceholderPattern.FindString(output) {
		t.Fatalf("trace placeholders differ: %q / %q", arg, output)
	}
}

// 记忆提取这类输出要存起来再用，换回原值；按回复规则给掩码的话，记忆里存的就是
// 139****0000，以后问起来答不上。
func TestSecretPrivacyDataPurposeRestoresFullText(t *testing.T) {
	inner := &recordingSecretProvider{reply: func(request string) *llm.GenerateResponse {
		return &llm.GenerateResponse{Text: "用户手机号是 " + secretmask.PlaceholderPattern.FindString(request)}
	}}
	provider := newTestSecretPrivacyProvider(inner, nil)
	ctx := withLLMUsagePurpose(withLLMUsageContext(context.Background(), MessageEvent{}), PurposeMemoryExtract)
	response, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "我手机 " + testPhone}}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "用户手机号是 "+testPhone {
		t.Fatalf("memory output should keep the original value: %q", response.Text)
	}
}

// api_key=sk-… 这一段同时被键值规则和令牌规则认出来；类别必须每次一样，不然占位符
// 跟着变，历史缓存就断了。逗号隔开的两个证件号都要认出来。
func TestSecretPrivacyKindIsDeterministicAndAdjacentValuesFound(t *testing.T) {
	text := "api_key=" + testSecretKey + "\n证件 " + testIDCard + ",440304200111294567"
	want := newSecretPrivacyScope("test-salt", nil, nil).protectText(text)
	for i := 0; i < 20; i++ {
		if got := newSecretPrivacyScope("test-salt", nil, nil).protectText(text); got != want {
			t.Fatalf("placeholders changed between runs:\n%s\n%s", want, got)
		}
	}
	if !strings.Contains(want, secretmask.PlaceholderPrefix+"api_key_") {
		t.Fatalf("token should keep its own kind: %q", want)
	}
	if strings.Contains(want, testIDCard) || strings.Contains(want, "440304200111294567") {
		t.Fatalf("adjacent id cards not both masked: %q", want)
	}
}
