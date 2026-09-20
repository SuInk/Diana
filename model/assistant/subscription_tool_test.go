// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// subscriptionFakeBackend 记下自己收到的输入：合并之后最容易出错的不是转发本身，
// 而是转发时把 kind 一起塞给了下游，或者把不属于这个 kind 的字段带过去。
type subscriptionFakeBackend struct {
	name     string
	received []map[string]any
	items    []map[string]any
	err      error
}

func (b *subscriptionFakeBackend) Run(_ context.Context, input map[string]any) (string, error) {
	copied := map[string]any{}
	for key, value := range input {
		copied[key] = value
	}
	b.received = append(b.received, copied)
	if b.err != nil {
		return "", b.err
	}
	body, _ := json.Marshal(map[string]any{"ok": true, "action": "listed", "items": b.items})
	return string(body), nil
}

func subscriptionTestTool(github *subscriptionFakeBackend, schedule, rss *subscriptionFakeBackend) *dianaSubscriptionTool {
	return newDianaSubscriptionTool(
		subscriptionBackend{kind: subscriptionKindSchedule, label: "周期查询", operations: []string{"create", "list", "update", "cancel", "delete"}, delegate: schedule},
		subscriptionBackend{kind: subscriptionKindRSS, label: "RSS", operations: []string{"create", "list", "update", "cancel", "delete"}, delegate: rss},
		subscriptionBackend{kind: subscriptionKindGitHub, label: "GitHub 仓库", operations: []string{"create", "list", "update", "cancel", "delete", "run"}, delegate: subscriptionGitHubDelegateFake(github)},
	)
}

// 夹具版的 subscriptionGitHubDelegate：同样要把「没挂上」表达成 nil 接口。
func subscriptionGitHubDelegateFake(backend *subscriptionFakeBackend) interface {
	Run(context.Context, map[string]any) (string, error)
} {
	if backend == nil {
		return nil
	}
	return backend
}

func subscriptionRun(t *testing.T, tool *dianaSubscriptionTool, input map[string]any) dianaSubscriptionResult {
	t.Helper()
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var result dianaSubscriptionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return result
}

// github 订阅只挂给主人和仓库管理人员。合并之后不能让所有人都在 kind 枚举里看见
// 它——看得见模型就会去调，然后只能被拒绝，白费一轮。
func TestSubscriptionKindEnumOnlyListsAvailableKinds(t *testing.T) {
	withGitHub := subscriptionTestTool(&subscriptionFakeBackend{name: "github"}, &subscriptionFakeBackend{}, &subscriptionFakeBackend{})
	schema, _ := json.Marshal(withGitHub.InputSchema())
	if !strings.Contains(string(schema), subscriptionKindGitHub) {
		t.Fatalf("github kind missing while it is available: %s", schema)
	}

	withoutGitHub := subscriptionTestTool(nil, &subscriptionFakeBackend{}, &subscriptionFakeBackend{})
	if got := withoutGitHub.kinds(); len(got) != 2 || slicesContains(got, subscriptionKindGitHub) {
		t.Fatalf("kinds without github access = %#v", got)
	}
	// 硬调一次也要被挡住，并且说清为什么，而不是崩在 nil 上。
	result := subscriptionRun(t, withoutGitHub, map[string]any{"operation": "create", "kind": "github", "repository": "acme/demo"})
	if result.OK || !strings.Contains(result.Message, "不可用") {
		t.Fatalf("unavailable kind result = %#v", result)
	}
}

// 转发时不能把 kind 带给下游：那三个工具不认识这个字段。
func TestSubscriptionForwardsWithoutKind(t *testing.T) {
	schedule := &subscriptionFakeBackend{}
	tool := subscriptionTestTool(nil, schedule, &subscriptionFakeBackend{})
	subscriptionRun(t, tool, map[string]any{"operation": "create", "kind": "schedule", "interval": "6h", "query": "查最新公告"})
	if len(schedule.received) != 1 {
		t.Fatalf("schedule backend calls = %d", len(schedule.received))
	}
	forwarded := schedule.received[0]
	if _, present := forwarded["kind"]; present {
		t.Fatalf("kind leaked to the backend: %#v", forwarded)
	}
	if forwarded["operation"] != "create" || forwarded["query"] != "查最新公告" {
		t.Fatalf("forwarded input = %#v", forwarded)
	}
}

// list 不填 kind 一次列全部，每条带 kind；这是合并最主要的收益，以前要分三次调。
func TestSubscriptionListAllTagsEveryItemWithKind(t *testing.T) {
	schedule := &subscriptionFakeBackend{items: []map[string]any{{"id": "s1"}}}
	rss := &subscriptionFakeBackend{items: []map[string]any{{"id": "r1"}, {"id": "r2"}}}
	tool := subscriptionTestTool(nil, schedule, rss)

	result := subscriptionRun(t, tool, map[string]any{"operation": "list"})
	if !result.OK || len(result.Items) != 3 {
		t.Fatalf("list all = %#v", result)
	}
	kinds := map[string]int{}
	for _, item := range result.Items {
		kinds[subscriptionItemString(item, "kind")]++
	}
	if kinds[subscriptionKindSchedule] != 1 || kinds[subscriptionKindRSS] != 2 {
		t.Fatalf("kind tags = %#v", kinds)
	}
}

// 一种查失败不该把另外两种的结果一起吞掉，但也不能假装查全了。
func TestSubscriptionListAllReportsPartialFailure(t *testing.T) {
	schedule := &subscriptionFakeBackend{items: []map[string]any{{"id": "s1"}}}
	rss := &subscriptionFakeBackend{err: context.DeadlineExceeded}
	tool := subscriptionTestTool(nil, schedule, rss)

	result := subscriptionRun(t, tool, map[string]any{"operation": "list"})
	if !result.OK || len(result.Items) != 1 {
		t.Fatalf("partial list dropped the healthy kind: %#v", result)
	}
	if !strings.Contains(result.Message, "不完整") || !strings.Contains(result.Message, subscriptionKindRSS) {
		t.Fatalf("partial failure not reported: %q", result.Message)
	}
}

// run 只有 github 支持。对不支持的 kind 要说清它支持哪些，而不是静默当成别的操作。
func TestSubscriptionRejectsOperationOutsideTheKind(t *testing.T) {
	tool := subscriptionTestTool(nil, &subscriptionFakeBackend{}, &subscriptionFakeBackend{})
	result := subscriptionRun(t, tool, map[string]any{"operation": "run", "kind": "schedule", "id": "s1"})
	if result.OK || !strings.Contains(result.Message, "不支持") {
		t.Fatalf("unsupported operation result = %#v", result)
	}
}

// 除 list 之外都必须点名 kind，否则参数对不上哪一种订阅。
func TestSubscriptionRequiresKindForMutations(t *testing.T) {
	tool := subscriptionTestTool(nil, &subscriptionFakeBackend{}, &subscriptionFakeBackend{})
	result := subscriptionRun(t, tool, map[string]any{"operation": "create", "interval": "6h"})
	if result.OK || !strings.Contains(result.Message, "kind 必填") {
		t.Fatalf("missing kind result = %#v", result)
	}
}
