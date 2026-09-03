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
	dianaOneBotRequestsToolName = "diana.onebot_requests"
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
		return fmt.Errorf("persist OneBot request: %w", err)
	}
	if !inserted {
		return nil
	}
	r.record(EventRecord{
		At: now, Kind: EventKindRequest, Platform: event.Platform, ProfileID: event.ProfileID,
		UserID: event.UserID, GroupID: event.GroupID, MessageID: event.MessageID,
		Text: oneBotRequestSummary(request.RequestType, request.SubType), Handled: true,
		Outcome: "onebot_request_pending", Decision: "pending", Reason: "OneBot 请求已持久化并等待主人处理",
	})
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
		// 通过 diana.onebot_requests list 找到它。
		log.Printf("chatbot OneBot request owner notification failed: type=%s sub_type=%s request_id=%s: %v", record.RequestType, record.SubType, record.ID, err)
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

type dianaOneBotRequestsTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaOneBotRequestsTool(runtime *Runtime, event MessageEvent) *dianaOneBotRequestsTool {
	return &dianaOneBotRequestsTool{runtime: runtime, event: event}
}

func (*dianaOneBotRequestsTool) Name() string { return dianaOneBotRequestsToolName }

func (*dianaOneBotRequestsTool) Description() string {
	return "列出并处理 OneBot v11 好友请求、成员入群申请和机器人群邀请。只对机器人主人开放；批准或拒绝必须对应当前用户明确表达的决定。工具使用内部保存的 flag，不要向用户索要或展示 flag。"
}

func (*dianaOneBotRequestsTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("操作：list 查看待处理请求；approve 同意；reject 拒绝。", "list", "approve", "reject"),
		"id":        toolStringParam("approve/reject 必填的请求编号；来自主人通知或 list 结果。"),
		"reason":    toolStringParam("拒绝群申请或群邀请时的简短原因，最多 200 字。"),
		"remark":    toolStringParam("同意好友请求时设置的备注，最多 200 字。"),
	})
}

func (t *dianaOneBotRequestsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.runtime.oneBotRequestStore() == nil {
		return "", fmt.Errorf("OneBot 请求存储未配置")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("只有机器人主人可以处理好友或群请求")
	}
	profileID := strings.TrimSpace(t.event.ProfileID)
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	store := t.runtime.oneBotRequestStore()
	if operation == "list" {
		items, err := store.ListOneBotRequests(ctx, profileID, OneBotRequestPending, oneBotRequestListLimit)
		if err != nil {
			return "", err
		}
		return marshalOneBotRequestToolResult(operation, items, "")
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
		return marshalOneBotRequestToolResult(operation, []OneBotRequestRecord{item}, "请求已经处理过，没有重复调用 OneBot")
	}
	approve := operation == "approve"
	action, params, err := oneBotRequestAction(item, approve, input)
	if err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, callErr := t.runtime.callOneBotAPIForEvent(callCtx, MessageEvent{ProfileID: item.ProfileID, Platform: item.Platform}, action, params)
	cancel()
	if callErr != nil {
		return "", fmt.Errorf("OneBot 请求处理失败: %w", callErr)
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
	return marshalOneBotRequestToolResult(operation, []OneBotRequestRecord{resolved}, "")
}

func oneBotRequestAction(item OneBotRequestRecord, approve bool, input map[string]any) (string, map[string]any, error) {
	params := map[string]any{"flag": item.Flag, "approve": approve}
	switch item.RequestType {
	case "friend":
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
