// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	dianaOneBotRequestsToolName = "platform_requests"
	oneBotRequestListLimit      = 20
	oneBotRequestTextLimit      = 200
)

type OneBotRequestStatus string

const (
	OneBotRequestPending  OneBotRequestStatus = "pending"
	OneBotRequestApproved OneBotRequestStatus = "approved"
	OneBotRequestRejected OneBotRequestStatus = "rejected"
)

// OneBotRequestEvent lives only between the transport decoder and Runtime.
// Flag is deliberately kept out of MessageEvent JSON so debug/event history can
// never expose the credential-like token required to approve the request.
type OneBotRequestEvent struct {
	RequestType string
	SubType     string
	UserID      string
	GroupID     string
	Comment     string
	Flag        string
}

type OneBotRequestRecord struct {
	ID          string              `json:"id"`
	ProfileID   string              `json:"profile_id,omitempty"`
	Platform    string              `json:"platform"`
	SelfID      string              `json:"self_id,omitempty"`
	RequestType string              `json:"request_type"`
	SubType     string              `json:"sub_type,omitempty"`
	UserID      string              `json:"user_id,omitempty"`
	GroupID     string              `json:"group_id,omitempty"`
	Comment     string              `json:"comment,omitempty"`
	Flag        string              `json:"-"`
	Status      OneBotRequestStatus `json:"status"`
	Reason      string              `json:"reason,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	DecidedAt   time.Time           `json:"decided_at,omitempty"`
}

type OneBotRequestStore interface {
	SaveOneBotRequest(context.Context, OneBotRequestRecord) (OneBotRequestRecord, bool, error)
	ListOneBotRequests(ctx context.Context, profileID string, status OneBotRequestStatus, limit int) ([]OneBotRequestRecord, error)
	GetOneBotRequest(ctx context.Context, profileID, id string) (OneBotRequestRecord, bool, error)
	ResolveOneBotRequest(ctx context.Context, profileID, id string, status OneBotRequestStatus, reason string, decidedAt time.Time) (OneBotRequestRecord, error)
}

func oneBotRequestMessageEvent(envelope oneBotEnvelope) MessageEvent {
	requestType := strings.ToLower(strings.TrimSpace(envelope.RequestType))
	subType := strings.ToLower(strings.TrimSpace(envelope.SubType))
	userID := stringifyID(envelope.UserID)
	groupID := stringifyID(envelope.GroupID)
	flag := strings.TrimSpace(envelope.Flag)
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{
		stringifyID(envelope.SelfID), requestType, subType, userID, groupID, flag,
	}, "\x00")))
	messageID := fmt.Sprintf("request-%x", fingerprint[:12])
	data := map[string]string{
		"request_type": requestType,
		"sub_type":     subType,
	}
	if userID != "" {
		data["user_id"] = userID
	}
	if groupID != "" {
		data["group_id"] = groupID
	}
	if comment := strings.TrimSpace(envelope.Comment); comment != "" {
		data["comment"] = comment
	}
	return MessageEvent{
		Kind:        EventKindRequest,
		SubType:     firstNonEmpty(subType, requestType),
		Time:        envelope.Time,
		SelfID:      stringifyID(envelope.SelfID),
		UserID:      userID,
		GroupID:     groupID,
		MessageID:   messageID,
		MessageType: requestType,
		RawMessage:  oneBotRequestSummary(requestType, subType),
		Segments:    []MessageSegment{{Type: "request", Data: data}},
		oneBotRequest: &OneBotRequestEvent{
			RequestType: requestType,
			SubType:     subType,
			UserID:      userID,
			GroupID:     groupID,
			Comment:     strings.TrimSpace(envelope.Comment),
			Flag:        flag,
		},
	}
}

func oneBotRequestSummary(requestType, subType string) string {
	switch requestType {
	case "friend":
		return "[request] friend"
	case "group":
		if subType == "invite" {
			return "[request] group invite"
		}
		return "[request] group add"
	default:
		return "[request] " + strings.TrimSpace(requestType)
	}
}

func (r *Runtime) SetOneBotRequestStore(store OneBotRequestStore) {
	r.mu.Lock()
	r.oneBotRequests = store
	r.mu.Unlock()
}

func (r *Runtime) oneBotRequestStore() OneBotRequestStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.oneBotRequests
}

func (r *Runtime) handleOneBotRequest(ctx context.Context, event MessageEvent) error {
	request := event.oneBotRequest
	store := r.oneBotRequestStore()
	if request == nil || store == nil || strings.TrimSpace(request.Flag) == "" {
		return nil
	}
	now := r.clock()
	record, inserted, err := store.SaveOneBotRequest(ctx, OneBotRequestRecord{
		ProfileID: strings.TrimSpace(event.ProfileID), Platform: firstNonEmpty(strings.TrimSpace(event.Platform), PlatformOneBotV11),
		SelfID: event.SelfID, RequestType: request.RequestType, SubType: request.SubType,
		UserID: request.UserID, GroupID: request.GroupID, Comment: request.Comment, Flag: request.Flag,
		Status: OneBotRequestPending, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("persist platform request: %w", err)
	}
	if !inserted {
		return nil
	}
	r.record(EventRecord{
		At: now, Kind: EventKindRequest, Platform: event.Platform, ProfileID: event.ProfileID,
		UserID: event.UserID, GroupID: event.GroupID, MessageID: event.MessageID,
		Text: oneBotRequestSummary(request.RequestType, request.SubType), Handled: true,
		Outcome: "onebot_request_pending", Decision: "pending", Reason: "好友或入群请求已持久化并等待主人处理",
	})
	// 群申请和邀请只留待主动查询，不再私聊推送给主人。
	if request.RequestType == "group" {
		return nil
	}
	cfg := r.effectiveConfigForEvent(event)
	ownerID := strings.TrimSpace(cfg.OwnerID)
	if ownerID == "" {
		return nil
	}
	notifyEvent := MessageEvent{
		ProfileID: event.ProfileID, Platform: event.Platform, ContextNamespace: event.ContextNamespace,
		Kind: EventKindPrivate, SelfID: event.SelfID, UserID: ownerID, Time: now.Unix(),
	}
	if err := r.sendNotification(ctx, notifyEvent, oneBotRequestOwnerNotice(record)); err != nil {
		// 请求已经安全落库，通知失败不能把同一平台事件变成未处理；主人之后仍可
		// 通过 platform_requests list 找到它。
		log.Printf("diana OneBot request owner notification failed: type=%s sub_type=%s request_id=%s: %v", record.RequestType, record.SubType, record.ID, err)
		return nil
	}
	return nil
}

func oneBotRequestOwnerNotice(item OneBotRequestRecord) string {
	label := "OneBot 请求"
	switch {
	case item.RequestType == "friend":
		label = "好友请求"
	case item.RequestType == "group" && item.SubType == "invite":
		label = "机器人群邀请"
	case item.RequestType == "group":
		label = "成员入群申请"
	}
	lines := []string{fmt.Sprintf("收到%s，等待处理", label), "请求编号：" + item.ID}
	if item.GroupID != "" {
		lines = append(lines, "群："+item.GroupID)
	}
	if item.UserID != "" {
		lines = append(lines, "发起人："+item.UserID)
	}
	if comment := truncateForChat(item.Comment, oneBotRequestTextLimit); comment != "" {
		lines = append(lines, "附言："+comment)
	}
	lines = append(lines, "回复“同意请求 "+item.ID+"”或“拒绝请求 "+item.ID+"，原因……”即可处理")
	return strings.Join(lines, "\n")
}

// platformSupportsJoinRequests 判断平台有没有可审批的好友/入群请求。QQ 官方机器人
// 当上 QQ 群管理员后能收到并审批入群申请（GROUP_JOIN_REQUEST）。
func platformSupportsJoinRequests(platform string) bool {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram, PlatformQQOfficial:
		return true
	}
	return false
}

type dianaOneBotRequestsTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaOneBotRequestsTool(runtime *Runtime, event MessageEvent) *dianaOneBotRequestsTool {
	return &dianaOneBotRequestsTool{runtime: runtime, event: event}
}

func (*dianaOneBotRequestsTool) Name() string { return dianaOneBotRequestsToolName }

func (*dianaOneBotRequestsTool) Description() string {
	return "列出并处理好友请求、入群申请和群邀请（QQ 的 OneBot 与 Telegram 审核入群），仅主人。批准或拒绝须对应用户明确的决定。flag 由工具内部保存，不要向用户索要或展示。"
}

func (*dianaOneBotRequestsTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("list 查看待处理请求（QQ 会先从平台补拉漏掉的入群申请和被折叠的好友申请）；sync 只补拉", "list", "sync", "approve", "reject"),
		"id":        toolStringParam("approve/reject 必填：请求编号，来自通知或 list"),
		"reason":    toolStringParam("拒绝群申请/邀请的原因，≤200 字"),
		"remark":    toolStringParam("同意好友时的备注，≤200 字"),
	})
}

func (t *dianaOneBotRequestsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.runtime.oneBotRequestStore() == nil {
		return "", fmt.Errorf("好友与入群请求存储未配置")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只有机器人主人可以处理好友或群请求")
	}
	profileID := strings.TrimSpace(t.event.ProfileID)
	operation := t.CanonicalOperation(input)
	store := t.runtime.oneBotRequestStore()
	if operation == "list" || operation == "sync" {
		message := ""
		syncPending := t.runtime.syncOneBotPendingRequests
		canSync := IsOneBotPlatform(t.runtime.currentPlatform(t.event))
		if NormalizePlatformID(t.runtime.currentPlatform(t.event)) == PlatformQQOfficial && t.event.PlatformScope == "qq_group" {
			syncPending, canSync = t.runtime.syncQQOfficialJoinRequests, true
		}
		if canSync {
			found, syncErr := syncPending(ctx, t.event)
			switch {
			case syncErr != nil && operation == "sync":
				return "", fmt.Errorf("从平台补拉请求失败：%w", syncErr)
			case syncErr != nil:
				// 补拉是兜底，失败不影响列出已经存下的请求。
				message = "没能从平台补拉漏掉的请求（" + syncErr.Error() + "），下面只有收到过推送的。"
			case found > 0:
				message = fmt.Sprintf("从平台补拉到 %d 条之前没收到推送的请求。", found)
			}
		} else if operation == "sync" {
			return "", fmt.Errorf("当前平台没有可补拉的请求列表，入群申请只能靠平台推送")
		}
		items, err := store.ListOneBotRequests(ctx, profileID, OneBotRequestPending, oneBotRequestListLimit)
		if err != nil {
			return "", err
		}
		return marshalOneBotRequestToolResult(operation, items, message)
	}
	if operation != "approve" && operation != "reject" {
		return "", fmt.Errorf("不支持的 operation %q", operation)
	}
	id := strings.TrimSpace(configToolString(input, "id"))
	if id == "" {
		return "", fmt.Errorf("%s 必须提供请求编号 id", operation)
	}
	item, found, err := store.GetOneBotRequest(ctx, profileID, id)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("没有找到请求 %s", id)
	}
	if item.Status != OneBotRequestPending {
		return marshalOneBotRequestToolResult(operation, []OneBotRequestRecord{item}, "请求已经处理过，没有重复调用平台接口")
	}
	approve := operation == "approve"
	action, params, err := oneBotRequestAction(item, approve, input)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	requestEvent := MessageEvent{ProfileID: item.ProfileID, Platform: item.Platform}
	var callErr error
	if IsOneBotPlatform(item.Platform) {
		_, callErr = t.runtime.callOneBotAPIForEvent(callCtx, requestEvent, action, params)
	} else {
		_, callErr = t.runtime.callPlatformAPIForEvent(callCtx, requestEvent, action, params)
	}
	cancel()
	if callErr != nil {
		return "", fmt.Errorf("请求处理失败: %w", callErr)
	}
	status := OneBotRequestApproved
	if !approve {
		status = OneBotRequestRejected
	}
	reason := truncateForChat(configToolString(input, "reason"), oneBotRequestTextLimit)
	resolved, err := store.ResolveOneBotRequest(ctx, profileID, id, status, reason, t.runtime.clock())
	if err != nil {
		return "", err
	}
	// 好友请求通过之后补发欠着的私聊。friend_add 通知也会做同一件事，但不是每个
	// OneBot 实现都报那条通知；取出即删除，两条路谁先到都只发一次。
	if approve && item.RequestType == "friend" {
		t.runtime.flushPendingDirectMessages(ctx, MessageEvent{
			ProfileID: item.ProfileID, Platform: item.Platform,
			SelfID: item.SelfID, UserID: item.UserID,
		})
	}
	return marshalOneBotRequestToolResult(operation, []OneBotRequestRecord{resolved}, "")
}

func oneBotRequestAction(item OneBotRequestRecord, approve bool, input map[string]any) (string, map[string]any, error) {
	if NormalizePlatformID(item.Platform) == PlatformTelegram {
		// Telegram 只有审核入群这一种请求，拒绝不带原因。
		chatID, userID, ok := parseTelegramJoinRequestFlag(item.Flag)
		if !ok {
			return "", nil, fmt.Errorf("请求 %s 的 Telegram 句柄无效", item.ID)
		}
		action := "approveChatJoinRequest"
		if !approve {
			action = "declineChatJoinRequest"
		}
		return action, map[string]any{"chat_id": chatID, "user_id": oneBotIDParam(userID)}, nil
	}
	if NormalizePlatformID(item.Platform) == PlatformQQOfficial {
		reason := ""
		if !approve {
			reason = truncateForChat(configToolString(input, "reason"), oneBotRequestTextLimit)
		}
		return qqOfficialJoinRequestAction(item, approve, reason)
	}
	params := map[string]any{"flag": item.Flag, "approve": approve}
	switch item.RequestType {
	case "friend":
		// 被 QQ 折叠的可疑好友申请走另一组接口，flag 是申请人的 uid，不能带备注。
		if item.SubType == oneBotRequestSubTypeDoubt {
			params["flag"] = doubtRequestUID(item.Flag)
			return "set_doubt_friends_add_request", params, nil
		}
		if approve {
			if remark := truncateForChat(configToolString(input, "remark"), oneBotRequestTextLimit); remark != "" {
				params["remark"] = remark
			}
		}
		return "set_friend_add_request", params, nil
	case "group":
		params["sub_type"] = item.SubType
		if !approve {
			if reason := truncateForChat(configToolString(input, "reason"), oneBotRequestTextLimit); reason != "" {
				params["reason"] = reason
			}
		}
		return "set_group_add_request", params, nil
	default:
		return "", nil, fmt.Errorf("不支持的 OneBot request_type %q", item.RequestType)
	}
}

func marshalOneBotRequestToolResult(operation string, items []OneBotRequestRecord, message string) (string, error) {
	type view struct {
		ID          string              `json:"id"`
		RequestType string              `json:"request_type"`
		SubType     string              `json:"sub_type,omitempty"`
		UserID      string              `json:"user_id,omitempty"`
		GroupID     string              `json:"group_id,omitempty"`
		Comment     string              `json:"comment,omitempty"`
		Status      OneBotRequestStatus `json:"status"`
		CreatedAt   string              `json:"created_at"`
	}
	result := struct {
		OK        bool   `json:"ok"`
		Operation string `json:"operation"`
		Message   string `json:"message,omitempty"`
		Items     []view `json:"items"`
	}{OK: true, Operation: operation, Message: message, Items: make([]view, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, view{
			ID: item.ID, RequestType: item.RequestType, SubType: item.SubType,
			UserID: item.UserID, GroupID: item.GroupID, Comment: truncateForChat(item.Comment, oneBotRequestTextLimit),
			Status: item.Status, CreatedAt: item.CreatedAt.Format(time.RFC3339),
		})
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

// CanonicalOperation 是 Run 实际执行的操作，按操作拦截时用同一套换算。
func (*dianaOneBotRequestsTool) CanonicalOperation(input map[string]any) string {
	return strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
}
