// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/llm"

	"github.com/gin-gonic/gin"
)

// echoPersonaClient 把收到的请求原样交还，便于断言提示词内容。
type echoPersonaClient struct {
	reply    string
	captured llm.GenerateRequest
}

func (client *echoPersonaClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	client.captured = req
	return &llm.GenerateResponse{Provider: llm.ProviderOpenAICompatible, Model: "gpt-test", Text: client.reply}, nil
}

func personaRouterWithClient(client llm.LLMClient) *gin.Engine {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{
		Provider: llm.ProviderOpenAICompatible,
		APIKey:   "test-key",
		Model:    "gpt-test",
	})
	return testRouter(NewLLMConfigHandlerWithFactory(store, func(llm.ProviderConfig) (llm.LLMClient, error) {
		return client, nil
	}))
}

func postPersona(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/llm/persona", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// soulReply 是一份形状正常的模型回复：一份 SOUL.md。
func soulReply(name string) string {
	return "# " + name + "\n\n## 概述\n\n" + name + "住在这台服务器上。我们希望她像一个熟人。\n\n## " + name + "的本性\n\n她说话短，粉色头发。"
}

func decodePersonaResponse(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Persona string `json:"persona"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp.Persona
}

func TestPersonaGenerateReturnsSoulMarkdown(t *testing.T) {
	client := &echoPersonaClient{reply: soulReply("嘉然")}
	rec := postPersona(personaRouterWithClient(client), `{"description":"一个爱撒娇的虚拟主播","name":"嘉然"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// SOUL.md 的标题是结构，原样保留。
	if persona := decodePersonaResponse(t, rec); persona != soulReply("嘉然") {
		t.Fatalf("persona = %q", persona)
	}
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	if !strings.Contains(user, "嘉然") || !strings.Contains(user, "爱撒娇的虚拟主播") {
		t.Fatalf("user prompt = %q", user)
	}
}

func TestPersonaGenerateStripsFenceAndPreamble(t *testing.T) {
	messy := "好的，以下是为你写的 SOUL.md：\n```markdown\n" + soulReply("嘉然") + "\n```"
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: messy}), `{"description":"一个爱撒娇的虚拟主播"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if persona := decodePersonaResponse(t, rec); persona != soulReply("嘉然") {
		t.Fatalf("persona = %q", persona)
	}
}

func TestPersonaGenerateRejectsEmptyOutput(t *testing.T) {
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: "  "}), `{"description":"一个爱撒娇的虚拟主播"}`)
	if rec.Code != statusUpstreamFailed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPersonaGenerateUsesRequestedChatProfile(t *testing.T) {
	store := NewMemoryLLMProfileStore(llm.ProviderConfig{})
	if err := store.SaveProfiles(llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "chat-default", Models: []llm.ModelInfo{{ID: "chat-default"}, {ID: "chat-selected"}}}},
			{ID: "image-p", Name: "生图", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "gpt-image-2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	client := &echoPersonaClient{reply: soulReply("嘉然")}
	selected := ""
	handler := NewLLMConfigHandlerWithFactory(store, func(cfg llm.ProviderConfig) (llm.LLMClient, error) {
		selected = cfg.Model
		return client, nil
	})
	rec := postPersona(testRouter(handler), `{"description":"自然一点","profile_id":"chat-p","model":"chat-selected"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if selected != "chat-selected" {
		t.Fatalf("persona selected %q, want chat-selected", selected)
	}
}

func TestPersonaGenerateNeverFallsBackToActiveImageProfile(t *testing.T) {
	set := llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "chat-p", Name: "对话", Group: llm.GroupChat, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "chat-model"}},
			{ID: "image-p", Name: "生图", Group: llm.GroupImage, Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "gpt-image-2"}},
		},
	}
	cfg, err := personaProviderConfig(set, personaGeneratePayload{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "chat-model" {
		t.Fatalf("persona fallback selected %q, want chat-model", cfg.Model)
	}
	if _, err := personaProviderConfig(set, personaGeneratePayload{ProfileID: "image-p"}); err == nil {
		t.Fatal("persona accepted an image-only profile for text generation")
	}
}

func TestPersonaGenerateRewritesFromCurrentPersona(t *testing.T) {
	// 带上现有 SOUL.md 是「改写」而不是「重写」，否则微调一句话就丢掉已经调好的设定。
	// 原来这里按 600 截 current：长人设改一句话，模型只看得到开头，写回来又整段覆盖原文。
	current := "# 嘉然\n\n" + strings.Repeat("这是一段很长的设定。", 200) + "结尾的关键设定。"
	client := &echoPersonaClient{reply: soulReply("嘉然")}
	body, err := json.Marshal(map[string]string{"description": "再毒舌一点", "current": current})
	if err != nil {
		t.Fatal(err)
	}
	rec := postPersona(personaRouterWithClient(client), string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	for _, want := range []string{"结尾的关键设定", "这次是改写，不是重写", "输出改写后的完整全文", "原有的设定一条都不要丢"} {
		if !strings.Contains(user, want) {
			t.Fatalf("改写提示词缺少 %q：%q", want, user)
		}
	}
}

func TestPersonaGenerateRejectsEmptyDescription(t *testing.T) {
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: "不该被调用"}), `{"description":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 生成的写法是 Diana 的 SOUL.md 写法：讲理由、写一个人、身份要稳、老实不讨好。
func TestPersonaGenerateSystemPromptFollowsConstitutionStyle(t *testing.T) {
	for _, want := range []string{
		"SOUL.md",
		"讲理由，不列规则",
		"写一个人，不写一张清单",
		"「我们」",
		"身份要稳",
		"不是一套戏服",
		"整体的权衡，不是机械排序",
		"## 概述",
		"## 核心价值",
		"的本性",
		"## 结语",
		"照着画出来",
		"不要代码围栏",
	} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词缺少 %q", want)
		}
	}
}

// 运行时另行注入格式、工具和上下文；SOUL.md 里再写一遍会两边打架。示例对话也不要：
// 模型会把它照抄成模板。
func TestPersonaGenerateSystemPromptForbidsRuntimeOwnedRules(t *testing.T) {
	for _, want := range []string{"输出格式与排版", "能力与工具", "运行时上下文", "示例对话"} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词缺少约束 %q", want)
		}
	}
}

func TestPersonaGeneratePromptUsesGivenName(t *testing.T) {
	prompt := personaGenerateUserPrompt("一个爱吐槽的技术群管理员", "嘉然", "", "")
	if !strings.Contains(prompt, "「嘉然」") || !strings.Contains(prompt, "一级标题") {
		t.Fatalf("用户提示词没要求用给定的名字：%s", prompt)
	}
	if got := personaGenerateUserPrompt("一个爱吐槽的技术群管理员", "", "", ""); strings.Contains(got, "一级标题") {
		t.Fatalf("没有名字时不该要求写名字：%s", got)
	}
}

func TestPersonaGenerateModeHintsCoverEveryResponseMode(t *testing.T) {
	// custom 是用户自己拧过的细项，没有一句话能概括；其余每档都要有对应说法，
	// 否则选了「超级活跃」生成出来的人设还是个闷葫芦。
	for _, mode := range []assistant.ResponseMode{
		assistant.ResponseModeQuiet,
		assistant.ResponseModeAssistant,
		assistant.ResponseModeStandard,
		assistant.ResponseModeActive,
		assistant.ResponseModeSuperActive,
	} {
		if personaGenerateModeHints[string(mode)] == "" {
			t.Errorf("回复模式 %q 没有对应的人设分寸说明", mode)
		}
	}
	if personaGenerateModeHints[string(assistant.ResponseModeCustom)] != "" {
		t.Error("custom 档不该有固定说法")
	}
	prompt := personaGenerateUserPrompt("一个爱吐槽的群管理", "", "", string(assistant.ResponseModeSuperActive))
	if !strings.Contains(prompt, personaGenerateModeHints["super_active"]) {
		t.Fatalf("回复模式没有进提示词：%q", prompt)
	}
}

// personaVenueForbiddenTerms 是人设生成提示词里一个都不能出现的词。
//
// 前一半是平台名，后一半是场合词，禁的是同一件事：一张人设卡存在共享人设库里，
// 会被应用到任意一个机器人配置上，而同一个机器人既在多人会话里说话，也在一对一
// 私聊里说话。提示词里写「运行在 QQ 群里」，这张卡换到 Telegram 上就是错的；写
// 「你活在群里」，这张卡被私聊用到时同样是错的。人设只管「它是谁、怎么说话」，
// 「在哪儿说、对着谁说」是运行时上下文（promptGroupScope、发言者模板、
// platformOutputRulesForConfig），运行时会自己补，人设抢着写只会写错。
// 同一条道理见 assistant.identityAliasPrefix 那段注释。
var personaVenueForbiddenTerms = []string{
	"QQ", "Telegram", "飞书", "企业微信", "微信", "OneBot",
	"群里", "群聊", "本群", "群友",
}

func TestPersonaGeneratePromptAssumesNoPlatformOrVenue(t *testing.T) {
	// 用户自己的需求描述会原样进提示词，这里给的都是中立措辞，命中的只会是
	// 我们自己写死的那部分。
	rewriteBase := "你是嘉然，说话软乎乎的。"
	prompts := map[string]string{
		"系统提示词": personaGenerateSystemPrompt,
		"从零生成":  personaGenerateUserPrompt("一个爱吐槽的猫娘", "嘉然", "", string(assistant.ResponseModeActive)),
		"改写":    personaGenerateUserPrompt("再毒舌一点", "嘉然", rewriteBase, string(assistant.ResponseModeSuperActive)),
	}
	for label, prompt := range prompts {
		for _, term := range personaVenueForbiddenTerms {
			if strings.Contains(prompt, term) {
				t.Errorf("%s里出现了 %q：人设要能跨配置、跨平台、跨群聊与私聊复用，"+
					"提示词不能替角色认定它在哪个平台、什么场合说话——那是运行时注入的上下文。\n%s",
					label, term, prompt)
			}
		}
	}
	// 回复模式说的是搭话分寸，不是「在哪儿搭话」，每一档都要经得起同样的检查。
	for mode, hint := range personaGenerateModeHints {
		for _, term := range personaVenueForbiddenTerms {
			if strings.Contains(hint, term) {
				t.Errorf("回复模式 %q 的说法里出现了 %q：分寸要写成怎么搭话，不要绑定场合", mode, term)
			}
		}
	}
	// 光是自己不写还不够，还要拦住模型顺手补一句「你住在某个群里」。
	for _, want := range []string{"说话的场合：", "「对方」「别人」「大家」"} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词没有禁止模型自己编造场合：缺少 %q", want)
		}
	}
}
