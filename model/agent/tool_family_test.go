// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// familyTestTool 记下每次收到的参数，用来核对族把调用换回成员时交过去的是什么。
type familyTestTool struct {
	name        string
	description string
	schema      map[string]any
	inputs      []map[string]any
}

func (t *familyTestTool) Name() string                { return t.name }
func (t *familyTestTool) Description() string         { return t.description }
func (t *familyTestTool) InputSchema() map[string]any { return t.schema }
func (t *familyTestTool) RepeatableCalls() bool       { return true }
func (t *familyTestTool) Run(_ context.Context, input map[string]any) (string, error) {
	t.inputs = append(t.inputs, input)
	return `{"ok":true}`, nil
}

type familyTestTools struct {
	open, click, tabs *familyTestTool
}

func familyTestRegistry() (*ToolRegistry, familyTestTools) {
	tools := familyTestTools{
		open: &familyTestTool{name: "page_open", description: "打开网页。", schema: toolObjectSchema([]string{"url"}, map[string]any{
			"url": toolStringParam("页面地址"), "tab_id": toolStringParam("标签页 ID"),
		})},
		click: &familyTestTool{name: "page_click", description: "点击页面元素。", schema: toolObjectSchema(nil, map[string]any{
			"selector": toolStringParam("CSS 选择器"), "tab_id": toolStringParam("标签页 ID"),
		})},
		tabs: &familyTestTool{name: "page_tabs", description: "管理标签页。", schema: toolObjectSchema([]string{"action"}, map[string]any{
			"action": toolEnumParam("", "list", "close"), "tab_id": toolStringParam("要关的标签页"),
		})},
	}
	registry := NewToolRegistry(&countingTool{name: "common"}, tools.open, tools.click, tools.tabs)
	registry.RegisterFamily(ToolFamily{
		Name:        "page",
		Description: "操作网页：打开、点击、管理标签页。",
		Members: []ToolFamilyMember{
			{Action: "open", Tool: "page_open"},
			{Action: "click", Tool: "page_click"},
			{Action: "tabs_list", Tool: "page_tabs", Fixed: map[string]any{"action": "list"}, Summary: "列出标签页。"},
			{Action: "tabs_close", Tool: "page_tabs", Fixed: map[string]any{"action": "close"}, Summary: "关闭标签页。"},
		},
	})
	return registry, tools
}

func familyActions(t *testing.T, registry *ToolRegistry, name string) []any {
	t.Helper()
	tool, ok := registry.Get(name)
	if !ok {
		return nil
	}
	schema := tool.(ToolInputSchema).InputSchema()
	return schema["properties"].(map[string]any)[toolFamilyActionField].(map[string]any)["enum"].([]any)
}

// 模型看到的目录里成员收成一个族名；权限、配置按成员名取的 Names 不变。
func TestToolFamilyIsPresentedAsOneTool(t *testing.T) {
	registry, _ := familyTestRegistry()
	if got := registry.PresentedNames(); !reflect.DeepEqual(got, []string{"common", "page"}) {
		t.Fatalf("presented names = %v", got)
	}
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"common", "page_click", "page_open", "page_tabs"}) {
		t.Fatalf("member names must stay visible to permission code: %v", got)
	}
	catalog := registry.SystemPromptCatalog()
	if strings.Count(catalog, "\n") != 1 || !strings.Contains(catalog, "- page: 操作网页") || strings.Contains(catalog, "page_open") {
		t.Fatalf("catalog = %q", catalog)
	}
	var definitions []string
	for _, definition := range registry.Definitions() {
		definitions = append(definitions, definition.Name)
	}
	if !reflect.DeepEqual(definitions, []string{"common", "page"}) {
		t.Fatalf("definitions = %v", definitions)
	}
	tool, _ := registry.Get("page")
	description := tool.Description()
	for _, want := range []string{"- open（必填 url）：打开网页。", "- tabs_close：关闭标签页。"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description lacks %q:\n%s", want, description)
		}
	}
	schema := tool.(ToolInputSchema).InputSchema()
	properties := schema["properties"].(map[string]any)
	// 成员自己的 action 已经摊平成族的动作，不能再作为参数露出来顶掉族的 action。
	if enum := properties[toolFamilyActionField].(map[string]any)["enum"].([]any); !reflect.DeepEqual(enum, []any{"open", "click", "tabs_list", "tabs_close"}) {
		t.Fatalf("action enum = %v", enum)
	}
	tabID := properties["tab_id"].(map[string]any)["description"].(string)
	if !strings.Contains(tabID, "open/click：标签页 ID") || !strings.Contains(tabID, "tabs_list/tabs_close：要关的标签页") {
		t.Fatalf("shared parameter must say which actions mean what: %q", tabID)
	}
}

// 权限照旧按成员裁：摘掉一个成员，族的 action 里就没有它；一个都不剩，族也不存在，
// 而且按「没权限」回话，不说成「不存在」。
func TestToolFamilyActionsFollowMemberPermissions(t *testing.T) {
	registry, _ := familyTestRegistry()
	registry.Remove("page_tabs")
	if got := familyActions(t, registry, "page"); !reflect.DeepEqual(got, []any{"open", "click"}) {
		t.Fatalf("actions after removing a member = %v", got)
	}

	registry, _ = familyTestRegistry()
	registry.Retain(map[string]bool{"common": true, "page_open": true})
	if got := familyActions(t, registry, "page"); !reflect.DeepEqual(got, []any{"open"}) {
		t.Fatalf("actions after Retain = %v", got)
	}
	if _, _, err := registry.resolveFamilyCall("page", map[string]any{"action": "click"}); err == nil || !strings.Contains(err.Error(), "当前可选：open") {
		t.Fatalf("a trimmed action must be refused with the available ones: %v", err)
	}

	registry, _ = familyTestRegistry()
	registry.Retain(map[string]bool{"common": true})
	if _, ok := registry.Get("page"); ok {
		t.Fatal("a family without members must not exist")
	}
	if !registry.PolicyDenied("page") {
		t.Fatal("a family whose members were all trimmed by permissions is denied, not missing")
	}
	if got := registry.PresentedNames(); !reflect.DeepEqual(got, []string{"common"}) {
		t.Fatalf("presented names = %v", got)
	}
}

// 调用族时，Runner 执行的是成员工具，交过去的是成员自己的参数：没有族的 action，
// 没有别的动作的参数，摊平的子动作补回成员的 action。
func TestRunnerDispatchesFamilyCallToMember(t *testing.T) {
	for _, native := range []bool{false, true} {
		registry, tools := familyTestRegistry()
		client := &dispatchTestClient{replies: []*llm.GenerateResponse{
			executeReply(native, "page", map[string]any{"action": "open", "url": "https://example.com", "selector": nil}),
			executeReply(native, "page", map[string]any{"action": "tabs_close", "tab_id": "t1"}),
			// 合并前的成员名、说明文字里「族名 的 动作」的写法照样认。
			executeReply(native, "page_click", map[string]any{"selector": "#a"}),
			executeReply(native, "page 的 click", map[string]any{"selector": "#b"}),
		}}
		runner, err := NewRunner(client, Config{MaxSteps: 8, CoreTools: []string{"common"}}, registry)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "打开再点一下"}}})
		if err != nil {
			t.Fatal(err)
		}
		var ran []string
		for _, step := range result.Steps {
			if step.Skipped || step.Error != "" {
				t.Fatalf("native=%v step failed: %#v", native, step)
			}
			ran = append(ran, step.Tool)
		}
		if !reflect.DeepEqual(ran, []string{"page_open", "page_tabs", "page_click", "page_click"}) {
			t.Fatalf("native=%v steps ran as %v", native, ran)
		}
		if !reflect.DeepEqual(tools.open.inputs, []map[string]any{{"url": "https://example.com"}}) {
			t.Fatalf("native=%v open input = %#v", native, tools.open.inputs)
		}
		if !reflect.DeepEqual(tools.tabs.inputs, []map[string]any{{"action": "close", "tab_id": "t1"}}) {
			t.Fatalf("native=%v tabs input = %#v", native, tools.tabs.inputs)
		}
		if !reflect.DeepEqual(tools.click.inputs, []map[string]any{{"selector": "#a"}, {"selector": "#b"}}) {
			t.Fatalf("native=%v click inputs = %#v", native, tools.click.inputs)
		}
	}
}

// 参数不合成员自己的 schema 时不执行，报错里说清是哪个动作缺什么。
func TestToolFamilyValidatesAgainstMemberSchema(t *testing.T) {
	registry, tools := familyTestRegistry()
	_, _, err := registry.resolveFamilyCall("page", map[string]any{"action": "open"})
	if err == nil || !strings.Contains(err.Error(), "page 的 action=open") || !strings.Contains(err.Error(), "url") {
		t.Fatalf("missing required member parameter must be reported: %v", err)
	}
	if len(tools.open.inputs) != 0 {
		t.Fatal("the member ran despite invalid input")
	}
	if _, _, err := registry.resolveFamilyCall("page", map[string]any{}); err == nil || !strings.Contains(err.Error(), "可选：open、click、tabs_list、tabs_close") {
		t.Fatalf("missing action must list the choices: %v", err)
	}
}

// 照旧名调用带子动作的成员（标签页的 close）时，换成摊平后的那个动作。
func TestNormalizeFamilyCallMapsMemberSubAction(t *testing.T) {
	registry, _ := familyTestRegistry()
	name, input := registry.normalizeFamilyCall("page_tabs", map[string]any{"action": "close", "tab_id": "t1"})
	if name != "page" || !reflect.DeepEqual(input, map[string]any{"action": "tabs_close", "tab_id": "t1"}) {
		t.Fatalf("normalized to %s %#v", name, input)
	}
	// 子动作对不上任何一个：不乱猜，按成员原名交回去。
	name, input = registry.normalizeFamilyCall("page_tabs", map[string]any{"action": "pin"})
	if name != "page_tabs" || input["action"] != "pin" {
		t.Fatalf("unknown sub-action was rewritten: %s %#v", name, input)
	}
	// 不认识的名字原样返回。
	if name, _ := registry.normalizeFamilyCall("page 的 fly", nil); name != "page 的 fly" {
		t.Fatalf("unknown action was rewritten: %s", name)
	}
}

// 合并前各群会话里记着的是成员名：恢复加载状态时换成族的契约，只出现一次。
func TestDeferredLoaderRestoresMemberNamesAsFamily(t *testing.T) {
	registry, _ := familyTestRegistry()
	loader := newDeferredToolLoader(registry, []string{"common"}, nil, nil)
	loader.restore([]string{"page_open", "page_click", "page"})
	if !reflect.DeepEqual(loader.order, []string{"page"}) {
		t.Fatalf("restored as %v", loader.order)
	}
	var contracts []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(loader.loadedContracts(true)), &contracts); err != nil || len(contracts) != 1 || contracts[0].Name != "page" {
		t.Fatalf("contracts = %s (%v)", loader.loadedContracts(true), err)
	}
	if catalog := loader.catalog(); strings.Contains(catalog, "page_open") || !strings.Contains(catalog, "- page: ") {
		t.Fatalf("deferred catalog = %q", catalog)
	}
}

// 浏览器族：十几个 browser_* 在目录里只剩 browser 一行；个人浏览器只有其中几个动作。
func TestBrowserToolsArePresentedAsOneFamily(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterBrowserTools(t.TempDir(), Config{}.WithDefaults())
	if got := registry.PresentedNames(); !reflect.DeepEqual(got, []string{BrowserToolFamilyName}) {
		t.Fatalf("presented names = %v", got)
	}
	if len(registry.Names()) < 12 {
		t.Fatalf("members must stay registered under their own names: %v", registry.Names())
	}
	actions := familyActions(t, registry, BrowserToolFamilyName)
	for _, want := range []any{"open", "text", "click", "type", "screenshot", "tabs_list", "tabs_close", "back", "reload", "eval"} {
		found := false
		for _, action := range actions {
			found = found || action == want
		}
		if !found {
			t.Fatalf("browser lacks action %v: %v", want, actions)
		}
	}
	name, input, err := registry.resolveFamilyCall(BrowserToolFamilyName, map[string]any{"action": "back"})
	if err != nil || name != "browser_navigate" || !reflect.DeepEqual(input, map[string]any{"action": "back"}) {
		t.Fatalf("back resolved to %s %#v (%v)", name, input, err)
	}

	// 安全模式摘掉脚本执行后，eval 这个动作就没有了，其他照常。
	registry.Remove("browser_eval")
	for _, action := range familyActions(t, registry, BrowserToolFamilyName) {
		if action == "eval" {
			t.Fatal("eval must disappear with its member")
		}
	}
}
