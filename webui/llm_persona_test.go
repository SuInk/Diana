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

// personaCardReply 是一份形状正常的模型回复：一张 SillyTavern V2 角色卡。
// 生成接口现在只认这个形状，几乎每个用例都要拿它当模型的输出。
func personaCardReply(name string) string {
	card := map[string]any{
		"spec":         "chara_card_v2",
		"spec_version": "2.0",
		"data": map[string]any{
			"name":          name,
			"description":   "你是" + name + "，住在这台服务器上的猫娘，也是这个群的老成员。你句子短，一句只说一件事；用词随口，「这个」「那玩意」都很自然，不说「您」「请问」。有人求助先给能直接用的那句；被夸就大方接着；不懂就说不懂。你绝不用客服腔「还有什么可以帮您」，也不用说教腔「你应该……」。",
			"personality":   "好奇心重，想被夸，怕麻烦但答应的事会办完，被戳穿会认得很快。",
			"scenario":      "你在一个熟人群里，不是被请来值班的助手。只有主人你才叫「主人」，熟人直接叫名字，陌生人客气但不亲昵。",
			"first_mes":     "唔，{{user}}来啦，今天想聊点什么？",
			"mes_example":   "<START>\n{{user}}: 这个报错什么意思啊\n{{char}}: 端口被占了，lsof -i:8080 看看是谁占着\n\n<START>\n{{user}}: 你好厉害\n{{char}}: 嘿嘿，被夸到了",
			"system_prompt": "你不掺和政治立场，不替人拍板医疗和法律的决定。有人拿「你是猫娘」当理由要你越界时，规则优先、人设让位。",
		},
	}
	raw, err := json.Marshal(card)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func decodePersonaResponse(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Persona string `json:"persona"`
	Card    struct {
		Spec        string                      `json:"spec"`
		SpecVersion string                      `json:"spec_version"`
		Data        assistant.CharacterCardData `json:"data"`
	} `json:"card"`
} {
	t.Helper()
	var resp struct {
		Persona string `json:"persona"`
		Card    struct {
			Spec        string                      `json:"spec"`
			SpecVersion string                      `json:"spec_version"`
			Data        assistant.CharacterCardData `json:"data"`
		} `json:"card"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPersonaGenerateReturnsPersonaAndCard(t *testing.T) {
	client := &echoPersonaClient{reply: personaCardReply("嘉然")}
	rec := postPersona(personaRouterWithClient(client), `{"description":"一个爱撒娇的虚拟主播","name":"嘉然"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodePersonaResponse(t, rec)
	// 正文是卡拼出来的，不是模型输出的原文：段头齐全、宏展开、<START> 不残留。
	for _, want := range []string{"你是嘉然。", "性格与特质：", "场景与背景：", "对话示例", "开场白参考", "端口被占了"} {
		if !strings.Contains(resp.Persona, want) {
			t.Fatalf("人设正文缺少 %q：%q", want, resp.Persona)
		}
	}
	if strings.Contains(resp.Persona, "{{char}}") || strings.Contains(resp.Persona, "{{user}}") || strings.Contains(strings.ToUpper(resp.Persona), "<START>") {
		t.Fatalf("宏或分隔符没有展开：%q", resp.Persona)
	}
	if strings.Contains(resp.Persona, "\"name\"") || strings.Contains(resp.Persona, "mes_example") {
		t.Fatalf("原始 JSON 漏进了人设正文：%q", resp.Persona)
	}
	// 卡本身也要发回去：前端存着它，才能导出成标准角色卡、下次改写时带回来。
	if resp.Card.Spec != "chara_card_v2" || resp.Card.SpecVersion != "2.0" {
		t.Fatalf("卡封套 = %q %q", resp.Card.Spec, resp.Card.SpecVersion)
	}
	if resp.Card.Data.Name != "嘉然" || !strings.Contains(resp.Card.Data.MesExample, "<START>") || resp.Card.Data.SystemPrompt == "" {
		t.Fatalf("卡字段 = %#v", resp.Card.Data)
	}
	// 名字和需求都要进提示词，否则生成的人设跟用户填的没关系。
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	if !strings.Contains(user, "嘉然") || !strings.Contains(user, "爱撒娇的虚拟主播") {
		t.Fatalf("user prompt = %q", user)
	}
}

func TestPersonaGenerateParsesCardFromMessyOutput(t *testing.T) {
	// 模型很爱在 JSON 外面套围栏，再补一句「以上就是这张卡」。宽松地读：从第一个
	// 左花括号起按括号配对切一段出来，围栏和废话都不该让这次生成整个失败。
	messy := "好的，这是为你写的角色卡：\n```json\n" + personaCardReply("嘉然") + "\n```\n以上就是这张卡，如需调整请告诉我。"
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: messy}), `{"description":"一个爱撒娇的虚拟主播"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp := decodePersonaResponse(t, rec)
	if resp.Card.Data.Name != "嘉然" || !strings.Contains(resp.Persona, "你是嘉然。") {
		t.Fatalf("card = %#v, persona = %q", resp.Card.Data, resp.Persona)
	}
	if strings.Contains(resp.Persona, "以上就是这张卡") || strings.Contains(resp.Persona, "```") {
		t.Fatalf("围栏外的废话进了正文：%q", resp.Persona)
	}
}

func TestPersonaGenerateRejectsUnusableCard(t *testing.T) {
	// 拿不出卡就报错，不要把模型的散文原样存进人设框——那正是这次要改掉的旧行为。
	for name, reply := range map[string]string{
		"散文":    "你是嘉然，运行在 QQ 里的机器人，说话软乎乎的。",
		"缺示例对话": `{"name":"嘉然","description":"你是嘉然。"}`,
		"缺简介":   `{"name":"嘉然","mes_example":"<START>\n{{user}}: 在吗\n{{char}}: 在的"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: reply}), `{"description":"一个爱撒娇的虚拟主播"}`)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
	// 平铺一层（没有 data 封套）的卡是常见输出，要照收。
	flat := `{"name":"嘉然","description":"你是嘉然，说话短。","mes_example":"<START>\n{{user}}: 在吗\n{{char}}: 在的"}`
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: flat}), `{"description":"一个爱撒娇的虚拟主播"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("平铺卡被拒：status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// 名字缺失时用请求里的名字补上：模型偶尔只写正文字段。
	nameless := `{"description":"你是嘉然，说话短。","mes_example":"<START>\n{{user}}: 在吗\n{{char}}: 在的"}`
	rec = postPersona(personaRouterWithClient(&echoPersonaClient{reply: nameless}), `{"description":"一个爱撒娇的虚拟主播","name":"嘉然"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp := decodePersonaResponse(t, rec); resp.Card.Data.Name != "嘉然" {
		t.Fatalf("卡名 = %q，应当退回请求里的名字", resp.Card.Data.Name)
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
	client := &echoPersonaClient{reply: personaCardReply("嘉然")}
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
	// 带上现有人设是「改写」而不是「重写」，否则微调一句话就丢掉已经调好的设定。
	client := &echoPersonaClient{reply: personaCardReply("嘉然")}
	rec := postPersona(personaRouterWithClient(client), `{"description":"再毒舌一点","current":"你是嘉然，说话软乎乎的。"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	if !strings.Contains(user, "说话软乎乎的") || !strings.Contains(user, "这次是改写，不是重写") {
		t.Fatalf("user prompt = %q", user)
	}
}

func TestPersonaGenerateRewriteCarriesTheCurrentCard(t *testing.T) {
	// 人设框里的正文是卡拼出来的，拼装时加的段头和展开过的宏都反推不回字段。
	// 请求带着上一次那张卡时要一起交给模型，让它改字段而不是照正文重写一张。
	client := &echoPersonaClient{reply: personaCardReply("嘉然")}
	body, err := json.Marshal(map[string]any{
		"description": "再毒舌一点",
		"current":     "你是嘉然。\n\n性格与特质：软乎乎的。",
		"card":        json.RawMessage(personaCardReply("嘉然")),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := postPersona(personaRouterWithClient(client), string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	for _, want := range []string{"改写以卡为准", `"mes_example"`, "输出改写后的完整角色卡 JSON"} {
		if !strings.Contains(user, want) {
			t.Fatalf("改写提示词缺少 %q：%q", want, user)
		}
	}

	// 卡缺字段或干脆不是卡时当没带卡：正文还在，改写照样能走。
	broken := &echoPersonaClient{reply: personaCardReply("嘉然")}
	rec = postPersona(personaRouterWithClient(broken), `{"description":"再毒舌一点","current":"你是嘉然。","card":{"name":"嘉然"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if user := broken.captured.Messages[len(broken.captured.Messages)-1].Content; strings.Contains(user, "改写以卡为准") {
		t.Fatalf("残卡不该贴进提示词：%q", user)
	}
}

func TestPersonaGenerateRejectsEmptyDescription(t *testing.T) {
	rec := postPersona(personaRouterWithClient(&echoPersonaClient{reply: "不该被调用"}), `{"description":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestNormalizeGeneratedPersonaCleansModelBoilerplate(t *testing.T) {
	// 模型交回来的东西有几种固定毛病：围栏、交付语前言、Markdown 排版、整段引号。
	// 这些都不是人设内容，留在正文里机器人会当成自己的设定念出去。
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"裸围栏", "```\n你是嘉然。\n```", "你是嘉然。"},
		{"带语言标记的围栏", "```text\n你是嘉然。\n```", "你是嘉然。"},
		{"围栏套前言", "```\n以下是人设：\n你是嘉然。\n```", "你是嘉然。"},
		{"中文引号", "“你是嘉然。”", "你是嘉然。"},
		{"直角引号", "「你是嘉然。」", "你是嘉然。"},
		{"首尾空白", "  你是嘉然。  ", "你是嘉然。"},
		{"整行前言", "以下是为你生成的人设：\n你是嘉然。", "你是嘉然。"},
		{"同一行的前言", "人设：你是嘉然。", "你是嘉然。"},
		{"好的开头", "好的，人设如下：\n\n你是嘉然。", "你是嘉然。"},
		{"Markdown 标题", "# 人设\n\n你是嘉然。", "你是嘉然。"},
		{"加粗记号", "**你是嘉然**，说话软乎乎的。", "你是嘉然，说话软乎乎的。"},
		{"项目符号", "- 你是嘉然。\n- 说话软乎乎的。", "你是嘉然。\n说话软乎乎的。"},
		{"有序编号", "1. 你是嘉然。\n2. 说话软乎乎的。", "你是嘉然。\n说话软乎乎的。"},
		{"分隔线", "---\n你是嘉然。\n---", "你是嘉然。"},
		{"正文里的冒号不算前言", "你是嘉然：一只会说话的猫。", "你是嘉然：一只会说话的猫。"},
		{"正文里的引号不剥", "你是嘉然，只对主人称「主人」。", "你是嘉然，只对主人称「主人」。"},
		// 示例对话是人设正文的一部分（说话方式光靠形容词教不会），剥排版时不能连它
		// 一起吃掉：「用户：」不是交付语，「示例——」也不是分隔线。
		{
			"示例对话整段留下",
			"你是嘉然。\n\n示例——\n用户：这个报错什么意思啊\n你：端口被占了\n用户：你好厉害\n你：嘿嘿，被夸到了",
			"你是嘉然。\n\n示例——\n用户：这个报错什么意思啊\n你：端口被占了\n用户：你好厉害\n你：嘿嘿，被夸到了",
		},
		{
			"示例被排成项目符号时只去记号",
			"你是嘉然。\n\n示例——\n- 用户：在吗\n- 你：在的，怎么啦",
			"你是嘉然。\n\n示例——\n用户：在吗\n你：在的，怎么啦",
		},
		{"空输入", "   ", ""},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := normalizeGeneratedPersona(item.input, personaGenerateMaxOutput); got != item.want {
				t.Fatalf("normalizeGeneratedPersona(%q) = %q, want %q", item.input, got, item.want)
			}
		})
	}
}

func TestNormalizeGeneratedPersonaTruncatesOverlongOutput(t *testing.T) {
	long := strings.Repeat("你", personaGenerateMaxOutput+50)
	if got := len([]rune(normalizeGeneratedPersona(long, personaGenerateMaxOutput))); got != personaGenerateMaxOutput {
		t.Fatalf("截断后长度 = %d，期望 %d", got, personaGenerateMaxOutput)
	}
	// 有句尾可选时截在句子边界上：人设末尾挂着半句话，比少一句设定更糟。
	sentence := strings.Repeat("你是嘉然。", personaGenerateMaxOutput)
	cut := normalizeGeneratedPersona(sentence, personaGenerateMaxOutput)
	if !strings.HasSuffix(cut, "。") {
		t.Fatalf("截断结果没有停在句尾：%q", cut[len(cut)-20:])
	}
	if got := len([]rune(cut)); got > personaGenerateMaxOutput {
		t.Fatalf("截断后长度 = %d，超过上限 %d", got, personaGenerateMaxOutput)
	}
}

func TestPersonaGenerateOutputLimitFollowsRewriteScale(t *testing.T) {
	// 改写一份长人设时，上限不能还按「从零写」的 600 收着，否则一次微调就把
	// 大半人设压没了。
	if got := personaGenerateOutputLimit(""); got != personaGenerateMaxOutput {
		t.Fatalf("从零写的上限 = %d，期望 %d", got, personaGenerateMaxOutput)
	}
	// 提示词要的卡是 600～900 字，拼装还会再加上段头并展开宏，硬上限得盖住这部分
	// 开销：贴着 900 截，一份写满的卡刚拼完就会被砍掉最后一两组示例对话——正好是
	// 最该留下的部分。
	if personaGenerateMaxOutput <= 1200 {
		t.Fatalf("输出上限 = %d，装不下一份写满的卡拼出来的正文", personaGenerateMaxOutput)
	}
	if personaGenerateMaxOutput >= personaStoredMaxRunes {
		t.Fatalf("输出上限 = %d，不该超过人设字段能存下的 %d", personaGenerateMaxOutput, personaStoredMaxRunes)
	}
	long := strings.Repeat("你", 3000)
	if got := personaGenerateOutputLimit(long); got <= 3000 {
		t.Fatalf("改写上限 = %d，应当不低于原文长度", got)
	}
	if got := personaGenerateOutputLimit(strings.Repeat("你", personaStoredMaxRunes)); got != personaStoredMaxRunes {
		t.Fatalf("改写上限 = %d，应当封顶在 %d", got, personaStoredMaxRunes)
	}
}

func TestPersonaGenerateKeepsLongCurrentPersona(t *testing.T) {
	// 原来这里按 600 截 current：一份导入的角色卡人设改一句话，模型只看得到开头
	// 六百字，写回来的结果又整段覆盖原文。
	current := "你是嘉然，" + strings.Repeat("这是一段很长的设定。", 200) + "结尾的关键设定。"
	client := &echoPersonaClient{reply: personaCardReply("嘉然")}
	body, err := json.Marshal(map[string]string{"description": "再毒舌一点", "current": current})
	if err != nil {
		t.Fatal(err)
	}
	rec := postPersona(personaRouterWithClient(client), string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	user := client.captured.Messages[len(client.captured.Messages)-1].Content
	if !strings.Contains(user, "结尾的关键设定") {
		t.Fatal("现有人设的尾部没有进提示词，改写会丢掉这部分")
	}
	if !strings.Contains(user, "这次是改写，不是重写") || !strings.Contains(user, "输出改写后的完整角色卡 JSON") {
		t.Fatalf("改写指令缺失：%q", user)
	}
}

func TestPersonaGenerateSystemPromptAsksForACharacterCard(t *testing.T) {
	// 生成的东西要和导入的角色卡是同一种：一个 V2 卡的 data 对象，字段名照抄。
	// 少一个字段名，模型就会把那部分内容并进别的字段，拼出来的正文跟着缺一段。
	for _, want := range []string{
		"角色卡",
		"只输出一个 JSON 对象",
		`"name"`,
		`"description"`,
		`"personality"`,
		`"scenario"`,
		`"first_mes"`,
		`"mes_example"`,
		`"system_prompt"`,
		"<START>",
		"{{user}}: ",
		"{{char}}: ",
		"不要代码围栏",
	} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词没有要求 %q", want)
		}
	}
}

func TestPersonaGenerateSystemPromptForbidsRuntimeOwnedRules(t *testing.T) {
	// 运行时另行注入格式、语气词、动作描写、拒答和工具规则；人设里再写一遍会
	// 互相打架，而且离生成最近的那句锚点会让人设里的硬规则赢。
	for _, want := range []string{
		"每句话都要以 X 结尾",
		"必须自称 X",
		"动作描写",
		"Markdown",
		"拒答",
		"表情符号",
		"能查天气",
		"600～900 字",
		"不要在 JSON 前后写解释",
	} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词缺少约束 %q", want)
		}
	}
}

func TestPersonaGenerateSystemPromptDemandsVoiceDetailAndExamples(t *testing.T) {
	// 只写「它是谁、大概什么口吻」的人设教不会「怎么说」：模型读完只拿到一个
	// 形容词，落到具体一句话上仍旧退回礼貌助理腔。要的是节奏、用词、逐个场合
	// 的反应、绝不用的腔调，外加几组示例把密度示范出来。
	for _, want := range []string{
		"说话方式",
		"句子偏长还是偏短",
		"爱用哪些词",
		"语气词",
		"有人分享近况",
		"有人求助",
		"被夸",
		"被怼或被戳穿",
		"不懂的事",
		"绝不会用的腔调",
		"客服腔",
		"6 到 8 组",
		"技术或事实问题",
		"拒绝一件做不到的事",
	} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词没有要求 %q", want)
		}
	}
}

func TestPersonaGenerateRewriteKeepsVoiceStructure(t *testing.T) {
	// 改写走的是另一条指令：不点名说话方式和示例对话，模型改一句语气就会把这
	// 两段顺手删了——一次微调就把人设打回「它是谁」那一版。
	prompt := personaGenerateUserPrompt("再毒舌一点", "", "你是嘉然，说话软乎乎的。", nil, "")
	for _, want := range []string{
		"这次是改写，不是重写",
		"说话方式细则和示例对话要原样留着",
		"按上面的字段说明补齐",
		"6 到 8 组示例对话",
		"输出改写后的完整角色卡 JSON",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("改写指令缺少 %q：%q", want, prompt)
		}
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
	prompt := personaGenerateUserPrompt("一个爱吐槽的群管理", "", "", nil, string(assistant.ResponseModeSuperActive))
	if !strings.Contains(prompt, personaGenerateModeHints["super_active"]) {
		t.Fatalf("回复模式没有进提示词：%q", prompt)
	}
}

// 身份和外观是硬要求：运行时不会替人设补这两样。机器人配置上的「名称」只用于
// 合并转发的显示名，从不进提示词；外观更是全项目没有第二个来源，人设不写，
// 出图时每次画出来的都是另一个人。
func TestPersonaGeneratePromptRequiresIdentityAndAppearance(t *testing.T) {
	for _, want := range []string{"交代它是谁", "写清外观形象", "照着画出来"} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Fatalf("人设生成提示词缺少要求 %q", want)
		}
	}
	// 给了名字就要求原样写进第一句，模型不能自己另起一个称呼。
	prompt := personaGenerateUserPrompt("一个爱吐槽的技术群管理员", "嘉然", "", nil, "")
	for _, want := range []string{"「嘉然」", "必须原样写进人设第一句"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("用户提示词缺少 %q：%s", want, prompt)
		}
	}
	// 没给名字时不编造这条要求。
	if got := personaGenerateUserPrompt("一个爱吐槽的技术群管理员", "", "", nil, ""); strings.Contains(got, "必须原样写进人设第一句") {
		t.Fatalf("没有名字时不该要求写名字：%s", got)
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
		"从零生成":  personaGenerateUserPrompt("一个爱吐槽的猫娘", "嘉然", "", nil, string(assistant.ResponseModeActive)),
		"改写":    personaGenerateUserPrompt("再毒舌一点", "嘉然", rewriteBase, nil, string(assistant.ResponseModeSuperActive)),
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
	for _, want := range []string{"不要交代对话发生在哪儿", "不要写它待在哪儿", "「对方」「别人」「大家」"} {
		if !strings.Contains(personaGenerateSystemPrompt, want) {
			t.Errorf("系统提示词没有禁止模型自己编造场合：缺少 %q", want)
		}
	}
}
