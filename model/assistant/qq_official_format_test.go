package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseQQFormatValue(t *testing.T) {
	name, patch, err := parseQQFormatValue(`小按钮={"keyboard":{"content":{"style":{"font_size":"small"}}},"msg_id":"x","markdown":{"content":"换掉"}}`)
	if err != nil || name != "小按钮" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if _, has := patch["msg_id"]; has {
		t.Fatalf("msg_id 不该留下: %#v", patch)
	}
	if _, has := patch["markdown"]; has {
		t.Fatalf("只有 content 的 markdown 应整个拿掉: %#v", patch)
	}
	if name, patch, err := parseQQFormatValue("小按钮"); err != nil || name != "小按钮" || patch != nil {
		t.Fatalf("模板名: %q %#v %v", name, patch, err)
	}
	if _, _, err := parseQQFormatValue(`{"msg_seq":3}`); err == nil {
		t.Fatal("没有可用字段应报错")
	}
	if _, _, err := parseQQFormatValue(`{"keyboard":`); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestApplyQQFormatPatchMergesIntoKeyboard(t *testing.T) {
	body := map[string]any{"msg_type": 2, "keyboard": qqOfficialKeyboard([][]OutgoingButton{{{Label: "A", Data: "A"}}})}
	merged := applyQQFormatPatch(body, map[string]any{"keyboard": map[string]any{"content": map[string]any{"style": map[string]any{"font_size": "small"}}}})
	content := merged["keyboard"].(map[string]any)["content"].(map[string]any)
	if content["rows"] == nil || content["style"].(map[string]any)["font_size"] != "small" {
		t.Fatalf("merged = %#v", merged)
	}
	if _, has := body["keyboard"].(map[string]any)["content"].(map[string]any)["style"]; has {
		t.Fatal("原请求体不该被改动")
	}
	// 没有按钮时只调样式的 keyboard 补丁不能凭空造出空键盘。
	plain := applyQQFormatPatch(map[string]any{"msg_type": 2}, map[string]any{"keyboard": map[string]any{"content": map[string]any{"style": map[string]any{}}}})
	if _, has := plain["keyboard"]; has {
		t.Fatalf("plain = %#v", plain)
	}
}

func TestQQOfficialSendsCustomFormat(t *testing.T) {
	api := newQQFakeAPI(t)
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "想听哪个", ReplyMessageID: "in1",
		Buttons:  [][]OutgoingButton{{{Label: "A", Data: "A"}}},
		QQFormat: &QQFormat{Patch: map[string]any{"keyboard": map[string]any{"content": map[string]any{"style": map[string]any{"font_size": "small"}}}}},
	}); err != nil {
		t.Fatal(err)
	}
	content := api.messages[0]["keyboard"].(map[string]any)["content"].(map[string]any)
	if content["style"].(map[string]any)["font_size"] != "small" {
		t.Fatalf("message = %#v", api.messages[0])
	}
}

// 自定义格式被拒时退回默认格式，继续走按钮降级，回复照样发出去，并给模板记一次失败。
func TestQQOfficialFallsBackWhenCustomFormatRejected(t *testing.T) {
	api := newQQFakeAPI(t)
	api.rejectKeyboard = true
	var reports []bool
	if err := api.channel().Send(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "选一个", ReplyMessageID: "in1",
		Buttons: [][]OutgoingButton{{{Label: "A", Data: "A"}}},
		QQFormat: &QQFormat{Name: "小按钮", Patch: map[string]any{"keyboard": map[string]any{"content": map[string]any{"style": map[string]any{"font_size": "small"}}}},
			Report: func(ok bool, _ error) { reports = append(reports, ok) }},
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || len(reports) != 1 || reports[0] {
		t.Fatalf("messages=%d reports=%v", len(api.messages), reports)
	}
}

type memoryQQFormatStore struct{ data map[string][]byte }

func (s *memoryQQFormatStore) LoadQQFormatTemplates(_ context.Context, profileID string) ([]byte, error) {
	return s.data[profileID], nil
}

func (s *memoryQQFormatStore) SaveQQFormatTemplates(_ context.Context, profileID string, data []byte) error {
	s.data[profileID] = data
	return nil
}

func TestQQFormatTemplateSaveReuseAndDisable(t *testing.T) {
	r := &Runtime{}
	r.SetQQFormatTemplateStore(&memoryQQFormatStore{data: map[string][]byte{}})
	ctx := context.Background()
	event := MessageEvent{ProfileID: "p1"}
	saved := r.resolveQQFormat(ctx, event, `小按钮={"keyboard":{"content":{"style":{"font_size":"small"}}}}`)
	if saved == nil || saved.Name != "小按钮" {
		t.Fatalf("saved = %#v", saved)
	}
	reused := r.resolveQQFormat(ctx, event, "小按钮")
	if reused == nil || reused.Patch["keyboard"] == nil {
		t.Fatalf("reused = %#v", reused)
	}
	if r.resolveQQFormat(ctx, MessageEvent{ProfileID: "p2"}, "小按钮") != nil {
		t.Fatal("模板按机器人档案隔离")
	}
	for i := 0; i < qqFormatDisableAfterFails; i++ {
		reused.Report(false, nil)
	}
	if r.resolveQQFormat(ctx, event, "小按钮") != nil {
		t.Fatal("连续被拒后模板应停用")
	}
	templates, _ := r.loadQQFormatTemplates(ctx, "p1")
	if field := qqFormatFinalizeField(templates); strings.Contains(field.Description, "已存模板：") {
		t.Fatalf("停用的模板不该出现在说明里: %s", field.Description)
	}
	// 重写同名模板清掉停用标记。
	r.upsertQQFormatTemplate(ctx, "p1", "小按钮", reused.Patch, time.Now())
	if r.resolveQQFormat(ctx, event, "小按钮") == nil {
		t.Fatal("重写后应恢复可用")
	}
}
