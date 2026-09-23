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
	first := get("profile=bot-a&limit=2")
	if len(first.Evaluations) != 2 || first.NextBeforeID != first.Evaluations[1].ID {
		t.Fatalf("first page = %#v", first)
	}
	second := get("profile=bot-a&limit=2&before_id=" + strconv.FormatInt(first.NextBeforeID, 10))
	if len(second.Evaluations) != 2 || second.NextBeforeID != 0 || second.Evaluations[0].ID >= first.NextBeforeID {
		t.Fatalf("second page = %#v", second)
	}
}
