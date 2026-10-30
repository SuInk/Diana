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
// 这是主人在控制台维护的配置，不给模型工具用。

const (
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

// QQOfficialJoinStrategy 供控制台按机器人管理入群自动审批策略。
func (r *Runtime) QQOfficialJoinStrategy(ctx context.Context, profileID string, req QQJoinStrategyRequest) (map[string]any, error) {
	channel, ok := channelFor[QQJoinStrategyChannel](r, profileID)
	if !ok {
		return nil, fmt.Errorf("只有已连接的 QQ 官方机器人能管理入群自动审批策略")
	}
	return channel.JoinApprovalStrategy(ctx, req)
}
