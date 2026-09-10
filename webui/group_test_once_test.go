package webui

import (
	"net/http"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

func TestGroupTestOneShotPreservesImageLink(t *testing.T) {
	channel := &restoredControlChannel{responses: map[string]map[string]any{"send_group_msg": {"message_id": 42}}}
	router := botTestRouter(restoredControlHandler(channel))
	response := performJSONRequest(router, http.MethodPost, "/api/assistant/group-test", `{"group_id":"765205730","one_shot":true,"message":"[CQ:image,file=http://example.test/media/token]"}`)
	if response.Code != 200 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	if len(channel.calls) != 1 || channel.calls[0].action != "send_group_msg" {
		t.Fatalf("calls: %+v", channel.calls)
	}
	segments, ok := channel.calls[0].params["message"].([]assistant.MessageSegment)
	if !ok || len(segments) != 1 || segments[0].Type != "image" || segments[0].Data["file"] != "http://example.test/media/token" {
		t.Fatalf("image link changed: %+v", segments)
	}
	response = performJSONRequest(router, http.MethodPost, "/api/assistant/group-test", `{"group_id":"bad","one_shot":true,"message":"test"}`)
	if response.Code != 400 || len(channel.calls) != 1 {
		t.Fatal("invalid group reached platform")
	}
}
