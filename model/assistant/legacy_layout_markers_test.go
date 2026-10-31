package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestNormalizeDianaMarkupUpgradesLegacyBrackets(t *testing.T) {
	cases := map[string]string{
		"[diana-at:10002] 早":                   `<diana-at id="10002"/> 早`,
		"[diana-reply:ROBOT1.0_x.y-z]好":        `<diana-quote id="ROBOT1.0_x.y-z"/>好`,
		"甲[diana-msg]乙[diana-line]丙":           "甲<diana-message/>乙<diana-br/>丙",
		"[[DIANA_REPLY_SINGLE]]整条":             "<diana-single/>整条",
		"[[DIANA_REPLY_AUTO]]":                 "<diana-auto/>",
		"[[DIANA_LINES_PRESERVE]]":             "<diana-lines-preserve/>",
		"[[DIANA_LINES_COMPACT]]":              "<diana-lines-compact/>",
		"不聊了[[DIANA_IGNORE_CURRENT_USER_30M]]": "不聊了<diana-ignore-user/>",
		"不行[[DIANA_REFUSE_CURRENT]]":           "不行<diana-refuse/>",
	}
	for input, want := range cases {
		if got := normalizeDianaMarkup(input); got != want {
			t.Errorf("normalize %q = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeDianaMarkupStraightensLooseTags(t *testing.T) {
	cases := map[string]string{
		`<diana-at id="1"/>`:       `<diana-at id="1"/>`,
		`<diana-at id='1' />`:      `<diana-at id="1"/>`,
		`<diana-at id=1>`:          `<diana-at id="1"/>`,
		`< diana-at  id = "1" / >`: `<diana-at id="1"/>`,
		`<at id="1"/>`:             `<diana-at id="1"/>`,
		`<quote id="m-9"/>`:        `<diana-quote id="m-9"/>`,
		`<diana-reply id="m-9"/>`:  `<diana-quote id="m-9"/>`,
		`<diana-at id=\"1\"/>`:     `<diana-at id="1"/>`,
		"<diana-message>甲</diana-message><diana-message>乙</diana-message>": "<diana-message/>甲<diana-message/>乙",
		"<diana-br>":     "<diana-br/>",
		"<diana-single>": "<diana-single/>",
	}
	for input, want := range cases {
		if got := normalizeDianaMarkup(input); got != want {
			t.Errorf("normalize %q = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeDianaMarkupLeavesCodeAndForeignTagsAlone(t *testing.T) {
	for _, text := range []string{
		"写法是 `[diana-at:1]` 或 `<at id=\"1\"/>`",
		"```html\n<diana-br>\n[diana-msg]\n```",
		"List<String> 和 <br/> 都不是标记",
		"<diana-unknown/> 认不出的交给发送前清理",
	} {
		if got := normalizeDianaMarkup(text); got != text {
			t.Errorf("normalize %q changed to %q", text, got)
		}
	}
}

func TestPendingDianaMarkupTail(t *testing.T) {
	for text, want := range map[string]bool{
		"你好 <":                    true,
		"你好 <dia":                 true,
		"你好 <diana-at id=":        true,
		"你好 <at id=\"1":           true,
		"你好 <diana-at id=\"1\"/>": false,
		"x < y":                   false,
		"List<Str":                false,
	} {
		if got := pendingDianaMarkupTail(text); got != want {
			t.Errorf("pending(%q) = %v, want %v", text, got, want)
		}
	}
}

type fixedReplyProvider struct{ resp llm.GenerateResponse }

func (p fixedReplyProvider) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	resp := p.resp
	return &resp, nil
}

func TestDianaMarkupProviderNormalizesTextAndToolArguments(t *testing.T) {
	provider := &dianaMarkupProvider{provider: fixedReplyProvider{resp: llm.GenerateResponse{
		Text: "[diana-reply:55][diana-at:2] 好[diana-msg]第二条",
		ToolCalls: []llm.ToolCall{{Name: "send", Arguments: map[string]any{
			"message": `<at id='3'> 来了[diana-line]换行`,
			"nested":  []any{map[string]any{"text": "[[DIANA_REPLY_SINGLE]]"}},
		}}},
	}}}
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `<diana-quote id="55"/><diana-at id="2"/> 好<diana-message/>第二条` {
		t.Fatalf("text = %q", resp.Text)
	}
	args := resp.ToolCalls[0].Arguments
	if args["message"] != `<diana-at id="3"/> 来了<diana-br/>换行` {
		t.Fatalf("message argument = %q", args["message"])
	}
	if got := args["nested"].([]any)[0].(map[string]any)["text"]; got != "<diana-single/>" {
		t.Fatalf("nested argument = %q", got)
	}
}

func TestStripLeakedControlMarkersKeepsConsumableTagsAndForeignBrackets(t *testing.T) {
	got := stripLeakedControlMarkers(`好的<diana-unknown x="1"/> <diana-at id="1"/> List<String> <br/> 结束<diana-message/>下一条`)
	if strings.Contains(got, "diana-unknown") {
		t.Fatalf("unknown diana tag leaked: %q", got)
	}
	for _, keep := range []string{`<diana-at id="1"/>`, "<diana-message/>", "List<String>", "<br/>"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("%s must stay: %q", keep, got)
		}
	}
}

func TestUnescapeJSONHTMLEscapesKeepsJSONValid(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"mention": mentionMarkerFor("10002"), "note": `a<b & 字面反斜杠\\u003c`})
	got := unescapeJSONHTMLEscapes(string(payload))
	if !strings.Contains(got, `"mention":"<diana-at id=\"10002\"/>"`) {
		t.Fatalf("tag still escaped: %s", got)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("no longer JSON: %v\n%s", err, got)
	}
	if decoded["mention"] != mentionMarkerFor("10002") || decoded["note"] != `a<b & 字面反斜杠\\u003c` {
		t.Fatalf("decoded = %#v", decoded)
	}
}

type capturingMarkupProvider struct{ got llm.GenerateRequest }

func (p *capturingMarkupProvider) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.got = req
	return &llm.GenerateResponse{}, nil
}

func TestDianaMarkupProviderUnescapesToolResults(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"mention": mentionMarkerFor("1")})
	inner := &capturingMarkupProvider{}
	original := []llm.Message{{Role: llm.RoleTool, Content: string(payload)}}
	_, _ = (&dianaMarkupProvider{provider: inner}).Generate(context.Background(), llm.GenerateRequest{Messages: original})
	if inner.got.Messages[0].Content != `{"mention":"<diana-at id=\"1\"/>"}` {
		t.Fatalf("tool result sent = %s", inner.got.Messages[0].Content)
	}
	if original[0].Content != string(payload) {
		t.Fatal("caller's history must not be mutated")
	}
}
