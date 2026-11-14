// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
)

type testPageResult struct {
	Items []string `json:"items"`
	toolResultPage
}

func marshalTestPage(t *testing.T, ctx context.Context, items []string, offset, limit int) testPageResult {
	t.Helper()
	raw, err := marshalToolResultPage(ctx, len(items), offset, limit, func(n int, page toolResultPage) any {
		return testPageResult{Items: items[page.Offset : page.Offset+n], toolResultPage: page}
	})
	if err != nil {
		t.Fatal(err)
	}
	if budget := agent.ToolOutputBudget(ctx); budget > 0 && utf8.RuneCountInString(raw) > budget {
		t.Fatalf("page exceeds budget %d: %d", budget, utf8.RuneCountInString(raw))
	}
	var page testPageResult
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestMarshalToolResultPageHonoursLimitWithoutCap(t *testing.T) {
	items := make([]string, 300)
	for i := range items {
		items[i] = "x"
	}
	page := marshalTestPage(t, context.Background(), items, 0, 0)
	if len(page.Items) != 300 || page.HasMore || page.PageNote != "" {
		t.Fatalf("unlimited page should return everything: %+v", page.toolResultPage)
	}
	page = marshalTestPage(t, context.Background(), items, 0, 120)
	if len(page.Items) != 120 || page.Total != 300 || page.RemainingCount != 180 || page.NextOffset != 120 || !strings.Contains(page.PageNote, "offset=120") {
		t.Fatalf("limited page = %+v", page.toolResultPage)
	}
}

func TestMarshalToolResultPageSplitsWholeItemsAtBudget(t *testing.T) {
	ctx := agent.WithToolOutputBudget(context.Background(), 1000)
	items := make([]string, 10)
	for i := range items {
		items[i] = strings.Repeat("字", 200)
	}
	var seen []string
	offset := 0
	for range 20 {
		page := marshalTestPage(t, ctx, items, offset, 0)
		for _, item := range page.Items {
			if item != items[0] {
				t.Fatal("item was shortened")
			}
		}
		seen = append(seen, page.Items...)
		if !page.HasMore {
			break
		}
		if page.ReturnedCount == 0 || page.NextOffset != offset+page.ReturnedCount || !strings.Contains(page.PageNote, "单次结果上限") {
			t.Fatalf("bad continuation: %+v", page.toolResultPage)
		}
		offset = page.NextOffset
	}
	if len(seen) != len(items) {
		t.Fatalf("paging lost items: got %d of %d", len(seen), len(items))
	}
}

func TestMarshalToolResultPageOffsetPastEnd(t *testing.T) {
	page := marshalTestPage(t, context.Background(), []string{"a", "b"}, 5, 0)
	if len(page.Items) != 0 || page.HasMore || page.Offset != 2 || !strings.Contains(page.PageNote, "超出末尾") {
		t.Fatalf("past-end page = %+v", page)
	}
}
