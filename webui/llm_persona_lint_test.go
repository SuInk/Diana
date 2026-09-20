// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func postPersonaLint(router *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/llm/persona/lint", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeLintFindings(t *testing.T, rec *httptest.ResponseRecorder) []personaLintFinding {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Findings []personaLintFinding `json:"findings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v：%s", err, rec.Body.String())
	}
	return body.Findings
}

// 这条检查存在的理由就是正则那版认字面：多一个字、换一种说法就漏。接口要能把
// 「每一句结尾都来个喵」这种正则够不着的写法报出来，不然换成 LLM 毫无意义。
func TestPersonaLintReportsParaphrasedRules(t *testing.T) {
	client := &echoPersonaClient{reply: `{"findings":[{"code":"sentence-enders","match":"每一句结尾都来个喵","message":"句尾语气词由界面上的设置项控制，正文里写死会和它打架。"}]}`}
	router := personaRouterWithClient(client)
	rec := postPersonaLint(router, `{"text":"你是一只猫娘。说话方式：每一句结尾都来个喵，听着才软。"}`)
	findings := decodeLintFindings(t, rec)
	if len(findings) != 1 || findings[0].Code != "sentence-enders" {
		t.Fatalf("应当报出一条句尾语气词冲突，实际 %+v", findings)
	}
	if findings[0].Match != "每一句结尾都来个喵" {
		t.Fatalf("match 要原样回传便于定位，实际 %q", findings[0].Match)
	}
	// 开关的当前值要随请求交上去，模型判断「是不是在抢同一件事」靠它。
	user := client.captured.Messages[1].Content
	for _, want := range []string{"自称：", "句尾语气词：", "动作描写：关"} {
		if !strings.Contains(user, want) {
			t.Fatalf("用户提示词里缺少 %q：%s", want, user)
		}
	}
}

// 模型很爱把原话「顺手改通顺」再引用。定位不到的提示比没有提示更烦人：用户拿着
// 那句话在输入框里搜，什么也搜不着。对不上原文的一律丢掉。
func TestPersonaLintDropsFindingsThatDoNotQuoteTheText(t *testing.T) {
	client := &echoPersonaClient{reply: `{"findings":[
		{"code":"formatting","match":"请不要使用 Markdown 语法","message":"格式由回复设置控制。"},
		{"code":"venue","match":"你住在这个群里","message":"场合由运行时注入。"}
	]}`}
	router := personaRouterWithClient(client)
	rec := postPersonaLint(router, `{"text":"你是个机器人。你住在这个群里，大家都认识你。"}`)
	findings := decodeLintFindings(t, rec)
	if len(findings) != 1 || findings[0].Code != "venue" {
		t.Fatalf("只有能对上原文的那条该留下，实际 %+v", findings)
	}
}

// 动作描写开关开着时，正文里写括号动作是对的，不该报。
func TestPersonaLintSkipsActionFindingsWhileTheToggleIsOn(t *testing.T) {
	reply := `{"findings":[{"code":"action-description","match":"（歪头）","message":"括号动作由动作描写开关控制。"}]}`
	text := `{"text":"你说话时爱写 （歪头） 这样的小动作。","action_description_enabled":%s}`

	router := personaRouterWithClient(&echoPersonaClient{reply: reply})
	if findings := decodeLintFindings(t, postPersonaLint(router, strings.Replace(text, "%s", "true", 1))); len(findings) != 0 {
		t.Fatalf("开关开着时不该报动作描写，实际 %+v", findings)
	}
	router = personaRouterWithClient(&echoPersonaClient{reply: reply})
	if findings := decodeLintFindings(t, postPersonaLint(router, strings.Replace(text, "%s", "false", 1))); len(findings) != 1 {
		t.Fatalf("开关关着时该报动作描写，实际 %+v", findings)
	}
}

// 同一类只留一条：同一件事报三遍会把真正不同的那几条挤下去。自创的分类也一并丢掉，
// 前端按 code 归类显示，认不得的 code 没有地方能落。
func TestPersonaLintKeepsOneFindingPerCodeAndDropsUnknownCodes(t *testing.T) {
	client := &echoPersonaClient{reply: `{"findings":[
		{"code":"formatting","match":"不要用 Markdown","message":"第一条。"},
		{"code":"formatting","match":"每条不超过 50 字","message":"第二条。"},
		{"code":"vibes","match":"你很可爱","message":"我觉得写得不好。"}
	]}`}
	router := personaRouterWithClient(client)
	rec := postPersonaLint(router, `{"text":"你很可爱。不要用 Markdown，每条不超过 50 字。"}`)
	findings := decodeLintFindings(t, rec)
	if len(findings) != 1 || findings[0].Message != "第一条。" {
		t.Fatalf("同一类只留第一条、未知分类要丢掉，实际 %+v", findings)
	}
}

// 没毛病的正文要安静。一个见谁咬谁的检查器比漏报更糟：用户会先学会无视提示，
// 再也不看真正写错的那次。
func TestPersonaLintStaysQuietOnCleanText(t *testing.T) {
	router := personaRouterWithClient(&echoPersonaClient{reply: `{"findings":[]}`})
	rec := postPersonaLint(router, `{"text":"你叫 Diana，是个机器人。句子短，一条只说一件事。"}`)
	if findings := decodeLintFindings(t, rec); len(findings) != 0 {
		t.Fatalf("干净的正文不该报，实际 %+v", findings)
	}
}

// 空正文不值得花一次模型往返。
func TestPersonaLintRejectsEmptyText(t *testing.T) {
	client := &echoPersonaClient{reply: `{"findings":[]}`}
	router := personaRouterWithClient(client)
	if rec := postPersonaLint(router, `{"text":"   "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("空正文该回 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if client.captured.Messages != nil {
		t.Fatal("空正文不该发起模型请求")
	}
}

// 模型没吐 JSON 时要明确失败，而不是当成「没毛病」——前端会把空结果显示成
// 「没发现问题」，把一次失败说成一次通过是最糟的结果。
func TestPersonaLintFailsWhenModelReturnsNoJSON(t *testing.T) {
	router := personaRouterWithClient(&echoPersonaClient{reply: "我觉得这段人设写得挺好的，没什么问题。"})
	rec := postPersonaLint(router, `{"text":"你叫 Diana，是个机器人。"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("模型没返回 JSON 时该回 502，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
