// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// 入群自动审批策略（/v2/groups/join_approval_strategy）：关联群里命中白名单 QQ 号的
// 入群申请由平台直接通过。一个机器人最多 20 个策略，机器人得是关联群的管理员才生效。
// 这是主人在控制台维护的配置，不给模型工具用，做成默认关闭的内置插件：策略存在
// 平台那边，插件只是遥控器，界面在插件设置里（QQJoinStrategyPanel.vue）。

const (
	qqJoinApprovalPluginID = "official.qq-join-approval"

	qqJoinStrategyPath          = "/v2/groups/join_approval_strategy"
	qqJoinStrategyGroupLimit    = 100
	qqJoinStrategyWhitelistOnce = 10000
	qqJoinStrategyRemarkLimit   = 255
	qqJoinStrategyPageLimit     = 50
)

// QQJoinStrategyRequest 是控制台发来的一次策略操作。Body 的字段名照平台文档原样透传。
type QQJoinStrategyRequest struct {
	Op         string         `json:"op"`
	StrategyID string         `json:"strategy_id,omitempty"`
	Body       map[string]any `json:"body,omitempty"`
}

// QQJoinStrategyChannel 由能管理入群自动审批策略的通道实现。
type QQJoinStrategyChannel interface {
	JoinApprovalStrategy(ctx context.Context, req QQJoinStrategyRequest) (map[string]any, error)
}

func validateQQJoinStrategyGroups(body map[string]any) error {
	openids, ids := directoryStrings(body["group_openids"]), directoryStrings(body["group_ids"])
	if len(openids) > 0 && len(ids) > 0 {
		return fmt.Errorf("group_openids 和 group_ids 只能二选一")
	}
	if len(openids) > qqJoinStrategyGroupLimit || len(ids) > qqJoinStrategyGroupLimit {
		return fmt.Errorf("一个策略最多关联 %d 个群", qqJoinStrategyGroupLimit)
	}
	return nil
}

func validateQQJoinStrategyCommon(body map[string]any) error {
	if v, ok := body["is_enable"]; ok && v != "on" && v != "off" {
		return fmt.Errorf("is_enable 只能是 on 或 off")
	}
	if utf8.RuneCountInString(stringFromAny(body["remark"])) > qqJoinStrategyRemarkLimit {
		return fmt.Errorf("备注最多 %d 个字", qqJoinStrategyRemarkLimit)
	}
	return nil
}

// JoinApprovalStrategy 执行一次策略操作：list/create/update/delete/execute/whitelist。
func (c *QQOfficialChannel) JoinApprovalStrategy(ctx context.Context, req QQJoinStrategyRequest) (map[string]any, error) {
	body := req.Body
	if body == nil {
		body = map[string]any{}
	}
	id := strings.TrimSpace(req.StrategyID)
	one := qqJoinStrategyPath + "/" + url.PathEscape(id)
	if req.Op != "list" && req.Op != "create" && id == "" {
		return nil, fmt.Errorf("缺少策略 ID")
	}
	switch req.Op {
	case "list":
		// 翻完全部页：一个机器人最多 20 个策略。
		var all []any
		cursor := ""
		for page := 0; page < 10; page++ {
			d, err := directoryData(c.CallAPI(ctx, "GET "+qqJoinStrategyPath, map[string]any{"cursor": cursor, "limit": qqJoinStrategyPageLimit}))
			if err != nil {
				return nil, err
			}
			for _, row := range directoryRows(d["strategies"]) {
				all = append(all, row)
			}
			next := stringFromAny(d["next_cursor"])
			if next == "" || next == cursor {
				break
			}
			cursor = next
		}
		if all == nil {
			all = []any{}
		}
		return map[string]any{"strategies": all}, nil
	case "create":
		if err := validateQQJoinStrategyGroups(body); err != nil {
			return nil, err
		}
		if err := validateQQJoinStrategyCommon(body); err != nil {
			return nil, err
		}
		return directoryData(c.CallAPI(ctx, "POST "+qqJoinStrategyPath, body))
	case "update":
		if err := validateQQJoinStrategyCommon(body); err != nil {
			return nil, err
		}
		if action, ok := body["group_action"].(map[string]any); ok {
			if op := action["op"]; op != "add" && op != "del" {
				return nil, fmt.Errorf("group_action.op 只能是 add 或 del")
			}
			if err := validateQQJoinStrategyGroups(action); err != nil {
				return nil, err
			}
		}
		return directoryData(c.CallAPI(ctx, "PATCH "+one, body))
	case "delete":
		return directoryData(c.CallAPI(ctx, "DELETE "+one, nil))
	case "execute":
		return directoryData(c.CallAPI(ctx, "POST "+one+"/execute", nil))
	case "whitelist":
		if op := body["op"]; op != "add" && op != "del" {
			return nil, fmt.Errorf("白名单操作只能是 add 或 del")
		}
		users := directoryStrings(body["whitelist_users"])
		if len(users) == 0 || len(users) > qqJoinStrategyWhitelistOnce {
			return nil, fmt.Errorf("一次提交的 QQ 号须在 1 到 %d 个之间", qqJoinStrategyWhitelistOnce)
		}
		return directoryData(c.CallAPI(ctx, "POST "+one+"/whitelist_users", map[string]any{"op": body["op"], "whitelist_users": users}))
	}
	return nil, fmt.Errorf("不支持的策略操作 %q", req.Op)
}

// QQJoinApprovalPlugin 只有清单：开关决定控制台能不能管理这台机器人的策略。
type QQJoinApprovalPlugin struct{}

func NewQQJoinApprovalPlugin() *QQJoinApprovalPlugin { return &QQJoinApprovalPlugin{} }

func (*QQJoinApprovalPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:              qqJoinApprovalPluginID,
		Name:            "QQ 入群自动审批",
		Version:         "0.1.0",
		Description:     "自动处理 QQ 群的入群申请：按验证消息、问答答案、昵称等规则直接通过或拒绝，也可以管理 QQ 开放平台的白名单 QQ 号策略。机器人需是群管理员；平台不提供申请人等级。",
		Official:        true,
		BuiltIn:         true,
		DefaultDisabled: true,
		Platforms:       []string{PlatformQQOfficial},
		Permissions:     []string{"qq_official:join_approval_strategy", "qq_official:join_request_approve"},
		Settings:        qqJoinApprovalSettings(),
	}
}

// Handle 不处理消息：审批由平台按策略完成。
func (*QQJoinApprovalPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

// QQOfficialJoinStrategy 供控制台按机器人管理入群自动审批策略。
func (r *Runtime) QQOfficialJoinStrategy(ctx context.Context, profileID string, req QQJoinStrategyRequest) (map[string]any, error) {
	if r.profileDisabled(profileID) || !r.plugins.EnabledWithOverrides(qqJoinApprovalPluginID, r.plugins.ProfileOverrides(profileID)) {
		return nil, fmt.Errorf("这台机器人没有启用「QQ 入群自动审批」插件")
	}
	channel, ok := channelFor[QQJoinStrategyChannel](r, profileID)
	if !ok {
		return nil, fmt.Errorf("只有已连接的 QQ 官方机器人能管理入群自动审批策略")
	}
	return channel.JoinApprovalStrategy(ctx, req)
}

// 入群自动审批插件的本地规则：平台策略只认白名单 QQ 号，这里按申请里能看到的字段判。
// 文档给的字段只有验证消息、问答、昵称、来源、是否机器人和安全提示，没有等级。
const (
	qqJoinSettingMode           = "mode"
	qqJoinSettingApproveWords   = "approve_keywords"
	qqJoinSettingNicknameWords  = "nickname_keywords"
	qqJoinSettingRejectWords    = "reject_keywords"
	qqJoinSettingRejectBots     = "reject_bots"
	qqJoinSettingRejectRisky    = "reject_risky"
	qqJoinSettingApproveInvited = "approve_invited"
	qqJoinSettingUnmatched      = "unmatched"
	qqJoinSettingRejectReason   = "reject_reason"

	qqJoinModeOff   = "off"
	qqJoinModeRules = "rules"
)

func qqJoinApprovalSettings() []PluginSettingSpec {
	scopes := []string{PluginSettingScopeBot, PluginSettingScopeGroup}
	return []PluginSettingSpec{
		{Key: qqJoinSettingMode, Label: "按规则审批", Description: "收到入群申请时按下面的规则自动通过或拒绝；关闭时申请只记成待办，由主人用 platform_requests 处理。平台白名单策略在下方单独管理，两者互不影响。", Type: PluginSettingTypeSelect, Default: qqJoinModeOff, Options: []PluginSettingOption{{Value: qqJoinModeOff, Label: "关闭"}, {Value: qqJoinModeRules, Label: "按规则"}}, Scopes: scopes},
		{Key: qqJoinSettingApproveWords, Label: "验证消息关键词", Description: "验证消息或问答答案里含其中任一词就通过，每行一个。", Type: PluginSettingTypeText, Default: "", Rows: 3, Scopes: scopes},
		{Key: qqJoinSettingNicknameWords, Label: "昵称关键词", Description: "申请人昵称含其中任一词就通过，每行一个。", Type: PluginSettingTypeText, Default: "", Rows: 2, Scopes: scopes},
		{Key: qqJoinSettingRejectWords, Label: "拒绝关键词", Description: "验证消息、问答答案或昵称含其中任一词就拒绝，优先于通过规则，每行一个。", Type: PluginSettingTypeText, Default: "", Rows: 2, Scopes: scopes},
		{Key: qqJoinSettingApproveInvited, Label: "群成员邀请的直接通过", Type: PluginSettingTypeBool, Default: false, Scopes: scopes},
		{Key: qqJoinSettingRejectBots, Label: "拒绝机器人账号", Type: PluginSettingTypeBool, Default: true, Scopes: scopes},
		{Key: qqJoinSettingRejectRisky, Label: "拒绝平台标记的可疑申请", Description: "平台给出 warning_tips（可疑）时拒绝。", Type: PluginSettingTypeBool, Default: false, Scopes: scopes},
		{Key: qqJoinSettingUnmatched, Label: "都没命中时", Type: PluginSettingTypeSelect, Default: "pending", Options: []PluginSettingOption{{Value: "pending", Label: "留给主人处理"}, {Value: "decline", Label: "拒绝"}}, Scopes: scopes},
		{Key: qqJoinSettingRejectReason, Label: "拒绝理由", Description: "拒绝时发给申请人，留空不带。", Type: PluginSettingTypeString, Default: "", Scopes: scopes},
	}
}

func qqJoinKeywords(text string) []string {
	var out []string
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' || r == '，' }) {
		if word := strings.ToLower(strings.TrimSpace(line)); word != "" {
			out = append(out, word)
		}
	}
	return out
}

func qqJoinContainsAny(text string, words []string) bool {
	text = strings.ToLower(text)
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// qqJoinDecision 按规则判一条申请：approve、decline，或空串表示留待主人处理。
func qqJoinDecision(raw qqOfficialGroupEvent, settings SettingValues) (string, string) {
	if settings.String(qqJoinSettingMode, qqJoinModeOff) != qqJoinModeRules {
		return "", ""
	}
	answers := []string{raw.VerifyInfo.VerifyMessage}
	for _, qa := range raw.VerifyInfo.ReviewQAList {
		answers = append(answers, qa.Answer)
	}
	message := strings.Join(answers, "\n")
	switch {
	case qqJoinContainsAny(message+"\n"+raw.Username, qqJoinKeywords(settings.String(qqJoinSettingRejectWords, ""))):
		return "decline", "命中拒绝关键词"
	case raw.Bot && settings.Bool(qqJoinSettingRejectBots, true):
		return "decline", "申请人是机器人账号"
	case raw.RiskTips == "warning_tips" && settings.Bool(qqJoinSettingRejectRisky, false):
		return "decline", "平台标记为可疑申请"
	case raw.ApplySource == "invited" && settings.Bool(qqJoinSettingApproveInvited, false):
		return "approve", "群成员邀请"
	case qqJoinContainsAny(message, qqJoinKeywords(settings.String(qqJoinSettingApproveWords, ""))):
		return "approve", "验证消息命中关键词"
	case qqJoinContainsAny(raw.Username, qqJoinKeywords(settings.String(qqJoinSettingNicknameWords, ""))):
		return "approve", "昵称命中关键词"
	case settings.String(qqJoinSettingUnmatched, "pending") == "decline":
		return "decline", "没有命中任何通过规则"
	}
	return "", ""
}

// autoReviewQQJoinRequest 在插件启用且规则给出结论时直接审批，返回是否已处理。
// 调接口失败就留成待办，不吞掉申请。
func (r *Runtime) autoReviewQQJoinRequest(ctx context.Context, event MessageEvent, record OneBotRequestRecord) bool {
	request := event.oneBotRequest
	if request == nil || request.qqJoin == nil || NormalizePlatformID(record.Platform) != PlatformQQOfficial {
		return false
	}
	_, settings, enabled := r.plugins.PluginWithSettingsForGroup(qqJoinApprovalPluginID, r.pluginOverridesForEvent(event), r.pluginSettingOverridesForEvent(event))
	if !enabled {
		return false
	}
	decision, why := qqJoinDecision(*request.qqJoin, settings)
	if decision == "" {
		return false
	}
	approve := decision == "approve"
	reason := ""
	if !approve {
		reason = truncateForChat(settings.String(qqJoinSettingRejectReason, ""), oneBotRequestTextLimit)
	}
	action, params, err := qqOfficialJoinRequestAction(record, approve, reason)
	if err == nil {
		_, err = r.callPlatformAPIForEvent(ctx, event, action, params)
	}
	outcome := EventRecord{
		At: r.clock(), Kind: EventKindRequest, Platform: event.Platform, ProfileID: event.ProfileID,
		UserID: event.UserID, GroupID: event.GroupID, MessageID: event.MessageID,
		Text: oneBotRequestSummary(request.RequestType, request.SubType), Handled: true,
	}
	if err != nil {
		outcome.Outcome, outcome.Decision, outcome.Reason = "qq_join_auto_review_failed", "pending", "自动审批调用失败，留给主人处理："+err.Error()
		r.record(outcome)
		return false
	}
	status := OneBotRequestApproved
	if !approve {
		status = OneBotRequestRejected
	}
	if store := r.oneBotRequestStore(); store != nil {
		_, _ = store.ResolveOneBotRequest(ctx, record.ProfileID, record.ID, status, "入群自动审批："+why, r.clock())
	}
	outcome.Outcome, outcome.Decision, outcome.Reason = "qq_join_auto_"+decision, decision, "入群自动审批："+why
	r.record(outcome)
	return true
}
