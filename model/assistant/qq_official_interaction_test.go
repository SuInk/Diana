package assistant

import (
	"encoding/json"
	"testing"
)

func TestQQOfficialEventFromInteractionUsesFullEventID(t *testing.T) {
	raw := `{"id":"abc","chat_type":1,"group_openid":"G1","group_member_openid":"U1","data":{"resolved":{"button_data":"/签到","button_id":"1-1"}}}`
	var interaction qqOfficialInteraction
	if err := json.Unmarshal([]byte(raw), &interaction); err != nil {
		t.Fatal(err)
	}
	eventID := "INTERACTION_CREATE:b395ee17-c4a5-4bcf-8cf6-c87678d07601"
	event, ok := qqOfficialEventFromInteraction(eventID, interaction, "self")
	if !ok {
		t.Fatal("interaction dropped")
	}
	if event.MessageID != eventID || event.GroupID != "G1" || event.UserID != "U1" || event.RawMessage != "/签到" || !event.ToMe {
		t.Fatalf("unexpected event: %+v", event)
	}
	if _, ok := qqOfficialEventFromInteraction("abc", interaction, "self"); ok {
		t.Fatal("bare d.id must not be used as event_id")
	}
}

func TestQQGatewayPayloadKeepsOuterID(t *testing.T) {
	var payload qqGatewayPayload
	raw := `{"op":0,"s":3,"t":"INTERACTION_CREATE","id":"INTERACTION_CREATE:x-1","d":{"id":"x"}}`
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID != "INTERACTION_CREATE:x-1" {
		t.Fatalf("payload id = %q", payload.ID)
	}
}
