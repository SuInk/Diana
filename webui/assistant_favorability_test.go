// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/SuInk/diana/model/assistant"
)

// 好感与画像接口：按结果筛选、非法结果值直接忽略、多于一页时给出下一页游标。
func TestRelationshipEvaluationsEndpoint(t *testing.T) {
	store, router := newAssistantUsersTestRouter(t)
	ctx := context.Background()
	for _, status := range []string{
		assistant.RelationshipEvaluationChanged,
		assistant.RelationshipEvaluationUnchanged,
		assistant.RelationshipEvaluationChanged,
		assistant.RelationshipEvaluationCapped,
	} {
		if err := store.RecordRelationshipEvaluation(ctx, assistant.RelationshipEvaluationRecord{
			BotProfileID: "bot-a", UserID: "10001", Status: status, Reason: "测试",
		}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(query string) relationshipEvaluationsResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/assistant/favorability/evaluations?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", query, rec.Code, rec.Body.String())
		}
		var body relationshipEvaluationsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	changed := get("profile=bot-a&status=changed,capped,drop_table")
	if len(changed.Evaluations) != 3 || changed.NextBeforeID != 0 {
		t.Fatalf("changed = %#v", changed)
	}
	if len(changed.PortraitFields) == 0 {
		t.Fatal("response must carry portrait field specs for the filter dialog")
	}
	// 不认识的取值当不限：不能报错，也不能把条件拼进 SQL。
	if loose := get("profile=bot-a&direction=sideways&chat=x&portrait_field=nope&portrait_source=x&min_confidence=7"); len(loose.Evaluations) != 4 {
		t.Fatalf("unknown filter values must be ignored: %#v", loose)
	}
	// 多选全取消：传了空值是一个都不要，不传才是不限。
	if none := get("profile=bot-a&status="); len(none.Evaluations) != 0 {
		t.Fatalf("empty status selection must match nothing: %#v", none)
	}
	first := get("profile=bot-a&limit=2")
	if len(first.Evaluations) != 2 || first.NextBeforeID != first.Evaluations[1].ID {
		t.Fatalf("first page = %#v", first)
	}
	second := get("profile=bot-a&limit=2&before_id=" + strconv.FormatInt(first.NextBeforeID, 10))
	if len(second.Evaluations) != 2 || second.NextBeforeID != 0 || second.Evaluations[0].ID >= first.NextBeforeID {
		t.Fatalf("second page = %#v", second)
	}
}
