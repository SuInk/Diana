// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// 订阅工具：把 schedule、rss、github_watch 三个工具合成一个。
//
// 这三样在存储层本来就是一张表：Reminder.Kind 取 query / rss_watch /
// repository_watch，字段摊平在同一个结构里，tasks 工具也早就用一个 kind 字段把它们
// 一起列出来。分裂只发生在工具层——模型要先猜自己该调哪一个，猜错就白费一轮；而
// 「列出来用 A 工具、改用 B 工具」本身也很别扭。
//
// 合并的代价是判别联合：kind 选错，参数就对不上。压这个风险靠两件事，都不是靠在
// 描述里多写几句求模型小心：
//
//  1. 名字不带点号，描述第一句就说「这是一个工具，不是三个」。这条抄 github 工具，
//     那边的注释记过原因：点号命名会让弱模型把取值脑补成子工具名。
//  2. kind 的枚举按本次会话实际可用的种类动态生成。github 订阅本来就只挂给主人和
//     仓库管理人员，合并之后不能让所有人都在枚举里看见它——看得见就会去调，然后
//     只能被拒绝。跨会话发送那里记过同一条教训。
const dianaSubscriptionToolName = "subscription"

const (
	subscriptionKindSchedule = "schedule"
	subscriptionKindRSS      = "rss"
	subscriptionKindGitHub   = "github"
)

// subscriptionBackend 是被合并的某一种订阅。delegate 就是原来那个工具，合并只换了
// 入口，没有重写各自的校验、额度和权限——那三套逻辑各有各的测试，不该借这次改动
// 一起翻新。
type subscriptionBackend struct {
	kind       string
	label      string
	operations []string
	delegate   interface {
		Run(context.Context, map[string]any) (string, error)
	}
}

type dianaSubscriptionTool struct {
	backends []subscriptionBackend
}

func newDianaSubscriptionTool(backends ...subscriptionBackend) *dianaSubscriptionTool {
	available := make([]subscriptionBackend, 0, len(backends))
	for _, backend := range backends {
		if backend.delegate != nil {
			available = append(available, backend)
		}
	}
	if len(available) == 0 {
		return nil
	}
	return &dianaSubscriptionTool{backends: available}
}

func (t *dianaSubscriptionTool) Name() string { return dianaSubscriptionToolName }

func (t *dianaSubscriptionTool) kinds() []string {
	kinds := make([]string, 0, len(t.backends))
	for _, backend := range t.backends {
		kinds = append(kinds, backend.kind)
	}
	return kinds
}

func (t *dianaSubscriptionTool) backend(kind string) (subscriptionBackend, bool) {
	for _, candidate := range t.backends {
		if candidate.kind == kind {
			return candidate, true
		}
	}
	return subscriptionBackend{}, false
}

func (t *dianaSubscriptionTool) Description() string {
	labels := make([]string, 0, len(t.backends))
	for _, backend := range t.backends {
		labels = append(labels, backend.kind+"="+backend.label)
	}
	return `这是一个工具，不是一组子工具：kind 和 operation 都是参数的取值，调用时工具名永远是 subscription。` +
		`管理持久化订阅：` + strings.Join(labels, "；") + `。` +
		`先用 kind 选订阅种类，再用 operation 选动作；每个字段的说明里写了它属于哪个 kind，不属于当前 kind 的字段不要填。` +
		`operation=list 不填 kind 就一次列出全部种类，每条带 kind 字段——用户问「我有哪些订阅」时这样调，不要逐个 kind 试。` +
		`cancel 只停止并保留记录，delete 才彻底删除。只执行一次的提醒不在本工具，改用 reminder；` +
		`「每天八点」「每周日 22:00」这类重复的提醒属于 kind=schedule，用 at 定首次时间、interval 定间隔。`
}

func (t *dianaSubscriptionTool) InputSchema() map[string]any {
	operations := map[string]bool{}
	for _, backend := range t.backends {
		for _, operation := range backend.operations {
			operations[operation] = true
		}
	}
	ordered := make([]string, 0, len(operations))
	for _, operation := range []string{"create", "list", "update", "cancel", "delete", "run"} {
		if operations[operation] {
			ordered = append(ordered, operation)
		}
	}
	schema := map[string]any{
		"kind": toolEnumParam("订阅种类。create、update、cancel、delete、run 必填；list 省略表示列出全部种类。",
			t.kinds()...),
		"operation": toolEnumParam("要执行的操作。cancel 只停止并保留记录，delete 才彻底删除；run 是立刻检查一次，只有 kind=github 支持。",
			ordered...),
		"id": toolStringParam("要操作的订阅 ID；update、cancel、delete、run 必填，可先用 list 查到。"),
		"interval": toolStringParam("重复间隔，单位 " + durationUnitsHint + "。" +
			"kind=schedule 必填：每天 1d、每周 1w、每月 1mo、每年 1y，固定时间点另用 at 指定；kind=rss 是检查 Feed 的间隔，例如 15m，省略按默认间隔处理。" +
			"各 kind 的上下限不同，填错会返回具体数值。"),
	}
	for key, value := range subscriptionKindFields() {
		schema[key] = value
	}
	return toolObjectSchema([]string{"operation"}, schema)
}

// subscriptionKindFields 是各 kind 专属的参数。说明一律以「kind=x 专用」开头：
// 合并之后模型同时看得见三套字段，不标清楚归属就会串。
func subscriptionKindFields() map[string]any {
	return map[string]any{
		"query":     toolStringParam("kind=schedule 专用：" + scheduleQueryDescription),
		"at":        toolStringParam("kind=schedule 专用：" + scheduleAtDescription),
		"month_day": toolIntParam("kind=schedule 专用："+scheduleMonthDayDescription, -31, 31),
		"weekday":   toolEnumParam("kind=schedule 专用："+scheduleWeekdayDescription, scheduleWeekdayOrder...),
		"week":      toolIntParam("kind=schedule 专用："+scheduleWeekDescription, -5, 5),
		"items": toolItemsParam("kind=schedule 专用：一次创建多个订阅，只在 create 时有效，最多 "+itoa(maximumTasksPerToolCall)+" 项。",
			maximumTasksPerToolCall, []string{"interval", "query"}, map[string]any{
				"interval":  toolStringParam("重复间隔：每天 1d、每周 1w、每月 1mo、每年 1y；m 是分钟，月写 mo。"),
				"query":     toolStringParam("每次触发时要查的内容，或到点要提醒的内容。"),
				"at":        toolStringParam("首次触发时间，RFC3339；有固定时间点时必须传。"),
				"month_day": toolIntParam("按月重复时落在第几天，-1 是最后一天；和 weekday 二选一。", -31, 31),
				"weekday":   toolEnumParam("按月重复时配合 week 表示第几个星期几。", scheduleWeekdayOrder...),
				"week":      toolIntParam("配合 weekday：1~5 从月初数，-1 是最后一个。", -5, 5),
			}),
		"target_user_id":  toolStringParam("kind=schedule 专用：代其他用户管理时的目标账号，仅机器人主人可用；创建仍占目标用户的额度。"),
		"twitter_handle":  toolStringParam("kind=rss 专用：要关注的单个 X (Twitter) 用户名，不带 @。盯多个人用 twitter_handles。"),
		"twitter_handles": toolStringArrayParam("kind=rss 专用：要关注的多个 X (Twitter) 用户名，共用同一套 judge_prompt。最多 " + itoa(maximumRSSWatchSources) + " 个来源（和 feed_urls 合计）。"),
		"feed_url":        toolStringParam("kind=rss 专用：要关注的单个 RSS/Atom feed 地址。盯多个 Feed 用 feed_urls。"),
		"feed_urls":       toolStringArrayParam("kind=rss 专用：要关注的多个 RSS/Atom feed 地址，共用同一套 judge_prompt。"),
		"judge_prompt":    toolStringParam("kind=rss 专用：判断条件，写清楚什么样的新条目才值得通知、通知时要说什么。最多 " + itoa(maximumRSSJudgeRunes) + " 个字符。"),
		"repository":      toolStringParam("kind=github 专用：仓库，写成 owner/repo 或 GitHub 链接；create 必填。"),
		"branch":          toolStringParam("kind=github 专用：要盯的分支，留空是默认分支。"),
		"watch": toolEnumArrayParam("kind=github 专用：要监控的类型，可多选。create 省略按全部处理；update 省略表示不改。",
			"commits", "pull_requests", "issues", "releases", "stars"),
		"pull_request_events": toolEnumArrayParam("kind=github 专用：PR 只收这几种动态；省略表示新订阅默认全选，空数组表示全不选。", repositoryWatchPullEventKinds...),
		"issue_events":        toolEnumArrayParam("kind=github 专用：Issue 只收这几种动态；省略表示新订阅默认全选，空数组表示全不选。", repositoryWatchIssueEventKinds...),
		"release_kinds": toolEnumArrayParam("kind=github 专用：Release 只收这几种版本；省略表示新订阅默认全选，空数组表示全不选。草稿任何时候都不推送。",
			repositoryWatchReleaseKindList...),
	}
}

type dianaSubscriptionResult struct {
	OK      bool             `json:"ok"`
	Action  string           `json:"action"`
	Kind    string           `json:"kind,omitempty"`
	Message string           `json:"message,omitempty"`
	Items   []map[string]any `json:"items,omitempty"`
}

func (t *dianaSubscriptionTool) fail(message string) (string, error) {
	body, err := json.Marshal(dianaSubscriptionResult{Action: "failed", Message: message})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaSubscriptionTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || len(t.backends) == 0 {
		return "", fmt.Errorf("当前没有可用的订阅种类")
	}
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "list"
	}
	kind := strings.ToLower(strings.TrimSpace(configToolString(input, "kind")))

	if operation == "list" && kind == "" {
		return t.listAll(ctx, input)
	}
	if kind == "" {
		return t.fail("kind 必填：" + operation + " 要先说明操作哪一种订阅，可选 " + strings.Join(t.kinds(), "、") + "。")
	}
	backend, ok := t.backend(kind)
	if !ok {
		return t.fail("kind=" + kind + " 在当前会话不可用。可选 " + strings.Join(t.kinds(), "、") +
			"。github 订阅只对机器人主人和该仓库的管理人员开放。")
	}
	if !slicesContains(backend.operations, operation) {
		return t.fail("kind=" + kind + " 不支持 operation=" + operation + "，它支持 " + strings.Join(backend.operations, "、") + "。")
	}
	// 转发前把 kind 摘掉：下游那三个工具不认识这个字段，留着只会让它们的参数校验
	// 多一个不认识的键。
	forwarded := make(map[string]any, len(input))
	for key, value := range input {
		if key == "kind" {
			continue
		}
		forwarded[key] = value
	}
	forwarded["operation"] = operation
	return backend.delegate.Run(ctx, forwarded)
}

// listAll 把各 kind 的 list 结果摊成一条扁平列表，每条打上 kind。
//
// 不自己去查存储：每种订阅的 list 各有各的可见范围（主人能看全部、普通用户只看
// 自己、github 还要按仓库授权过滤），绕过它们等于把那三套过滤重写一遍。
func (t *dianaSubscriptionTool) listAll(ctx context.Context, input map[string]any) (string, error) {
	result := dianaSubscriptionResult{OK: true, Action: "listed"}
	failures := make([]string, 0, len(t.backends))
	for _, backend := range t.backends {
		forwarded := map[string]any{"operation": "list"}
		for _, key := range []string{"scope", "target_user_id"} {
			if value, present := input[key]; present {
				forwarded[key] = value
			}
		}
		raw, err := backend.delegate.Run(ctx, forwarded)
		if err != nil {
			failures = append(failures, backend.kind+"："+err.Error())
			continue
		}
		var envelope struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			failures = append(failures, backend.kind+"：返回格式无法解析")
			continue
		}
		for _, item := range envelope.Items {
			if item == nil {
				continue
			}
			item["kind"] = backend.kind
			result.Items = append(result.Items, item)
		}
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		return subscriptionItemString(result.Items[i], "kind") < subscriptionItemString(result.Items[j], "kind")
	})
	switch {
	case len(failures) == len(t.backends):
		return t.fail("全部订阅种类都查询失败：" + strings.Join(failures, "；"))
	case len(failures) > 0:
		// 一种查失败不该把另外两种的结果一起吞掉，但也不能假装查全了。
		result.Message = fmt.Sprintf("共 %d 条订阅；以下种类查询失败，结果不完整：%s", len(result.Items), strings.Join(failures, "；"))
	default:
		result.Message = fmt.Sprintf("共 %d 条订阅，每条的 kind 字段说明它属于哪一种。", len(result.Items))
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func subscriptionItemString(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// subscriptionGitHubDelegate 把「没挂上」表达成 nil 接口。
//
// 直接把 *dianaRepositoryWatchTool(nil) 塞进接口字段会得到一个非 nil 的接口值，
// newDianaSubscriptionTool 就会把它当成可用的种类收进 backends，kind 枚举里于是
// 多出一个调用必崩的 github。
func subscriptionGitHubDelegate(tool *dianaRepositoryWatchTool) interface {
	Run(context.Context, map[string]any) (string, error)
} {
	if tool == nil {
		return nil
	}
	return tool
}

// CanonicalOperation 是这次调用实际会执行的操作：没写 operation 按 list 算，具体换算
// 交给对应 kind 的工具（提醒和订阅的 _elsewhere 由它们判断），和 Run 转发时一致。
func (t *dianaSubscriptionTool) CanonicalOperation(input map[string]any) string {
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	if operation == "" {
		operation = "list"
	}
	kind := strings.ToLower(strings.TrimSpace(configToolString(input, "kind")))
	backend, ok := t.backend(kind)
	if !ok {
		return operation
	}
	canonical, ok := backend.delegate.(agent.CanonicalOperationTool)
	if !ok {
		return operation
	}
	forwarded := make(map[string]any, len(input))
	for key, value := range input {
		forwarded[key] = value
	}
	forwarded["operation"] = operation
	return canonical.CanonicalOperation(forwarded)
}
