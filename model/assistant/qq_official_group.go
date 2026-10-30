// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// QQ 群（group_openid）这一侧的群管接口，字段与限制照开放平台 v2 文档：
//
//   - /info、/bot_state 所有机器人可用；
//   - 成员列表、成员详情、批量移除、群黑名单只对白名单机器人开放，没权限回 11253；
//   - 入群申请与禁言要求机器人是群管理员。
//
// 频道（guild）那一套在 qq_official_governance.go / qq_official_directory.go。

const (
	// qqGroupNoPermissionCode 是「应用无接口访问权限」：白名单接口没开通时返回。
	qqGroupNoPermissionCode = "11253"
	// qqGroupMembersPageSize 是成员列表单页上限，返回不足一页也不代表末页，以 next_cursor 为准。
	qqGroupMembersPageSize = 30
	// qqGroupBatchLimit 是批量移除、黑名单、禁言单次最多处理的成员数。
	qqGroupBatchLimit = 20
	// qqGroupMuteMax 是单次禁言最长时长。
	qqGroupMuteMax = 30 * 24 * time.Hour
	// qqGroupMembersMaxPages 限制一次名单拉取的页数：30 条一页，150 页够 4500 人。
	qqGroupMembersMaxPages = 150

	qqOfficialJoinRequestFlagPrefix = "qqofficial:"
)

// errQQGroupNoPermission 标出白名单接口没开通，调用方据此回落而不是当成故障。
var errQQGroupNoPermission = errors.New("QQ 群成员与群管接口仅对白名单机器人开放（错误码 11253）")

// qqGroupAPIError 把 11253 统一成 errQQGroupNoPermission。平台回 4xx 时错误正文里带
// {"code":11253,...}，回 200 时 code 在 JSON 里，两种都认。
func qqGroupAPIError(data map[string]any, err error) (map[string]any, error) {
	if err != nil {
		if strings.Contains(err.Error(), qqGroupNoPermissionCode) {
			return nil, fmt.Errorf("%w：%v", errQQGroupNoPermission, err)
		}
		return nil, err
	}
	if stringFromAny(data["code"]) == qqGroupNoPermissionCode {
		return nil, errQQGroupNoPermission
	}
	return directoryData(data, nil)
}

func qqGroupPath(gid string) string {
	return "/v2/groups/" + url.PathEscape(strings.TrimSpace(gid))
}

// isQQGroupDirectory 判断目录查询的对象是不是 QQ 群：当前消息就来自 QQ 群，或者
// 没有事件上下文（控制台）而且这个 ID 不是见过的子频道。
func (c *QQOfficialChannel) isQQGroupDirectory(ctx context.Context, id string) bool {
	e := directoryEvent(ctx)
	switch e.PlatformScope {
	case "qq_group":
		return true
	case "qq_guild":
		return false
	}
	c.mu.RLock()
	guild := c.guildChannels[id]
	c.mu.RUnlock()
	return guild == ""
}

func (c *QQOfficialChannel) qqGroupInfo(ctx context.Context, gid string) (GroupInfo, error) {
	d, err := qqGroupAPIError(c.CallAPI(ctx, "GET "+qqGroupPath(gid)+"/info", nil))
	if err != nil {
		return GroupInfo{}, err
	}
	return GroupInfo{
		GroupID:          gid,
		GroupName:        stringFromAny(d["group_name"]),
		Description:      stringFromAny(d["group_finger_memo"]),
		Category:         stringFromAny(d["group_class_text"]),
		Tags:             directoryStrings(d["group_tags"]),
		MemberCount:      directoryInt(d["group_member_num"]),
		MemberCountKnown: d["group_member_num"] != nil,
	}, nil
}

// qqGroupBotState 是 /bot_state 的返回：机器人在群里的身份和收消息设置。
type qqGroupBotState struct {
	MemberOpenID      string `json:"member_openid"`
	JoinedAt          string `json:"joined_at"`
	AllowProactiveMsg bool   `json:"allow_proactive_msg"`
	RecvMsgSetting    string `json:"recv_msg_setting"`
	MemberRole        string `json:"member_role"`
}

func (c *QQOfficialChannel) qqGroupBotState(ctx context.Context, gid string) (qqGroupBotState, error) {
	d, err := qqGroupAPIError(c.CallAPI(ctx, "GET "+qqGroupPath(gid)+"/bot_state", nil))
	if err != nil {
		return qqGroupBotState{}, err
	}
	var state qqGroupBotState
	encoded, _ := json.Marshal(d)
	if err := json.Unmarshal(encoded, &state); err != nil {
		return qqGroupBotState{}, fmt.Errorf("机器人群内状态返回格式无法识别")
	}
	c.mu.Lock()
	if c.groupBotOpenIDs == nil {
		c.groupBotOpenIDs = map[string]string{}
	}
	if state.MemberOpenID != "" {
		c.groupBotOpenIDs[gid] = state.MemberOpenID
	}
	c.mu.Unlock()
	return state, nil
}

// isQQGroupSelf 认出群里的机器人自己：网关 READY 给的 ID、AppID，或 bot_state 回过的群内 openid。
func (c *QQOfficialChannel) isQQGroupSelf(gid, uid string) bool {
	c.mu.RLock()
	appID := c.cfg.AppID
	openID := c.groupBotOpenIDs[gid]
	c.mu.RUnlock()
	return uid == appID || uid == c.Status().SelfID || (openID != "" && uid == openID)
}

func qqGroupMemberInfo(gid string, row map[string]any) OneBotGroupMemberInfo {
	uid := stringFromAny(row["member_openid"])
	bot, _ := row["bot"].(bool)
	return OneBotGroupMemberInfo{
		GroupID: gid, UserID: uid, Nickname: stringFromAny(row["username"]),
		Role: firstNonEmpty(stringFromAny(row["member_role"]), "member"), IsBot: bot, MembershipVerified: uid != "",
	}
}

// qqGroupMember 查单个成员。问的是机器人自己时走人人可用的 /bot_state，门禁里
// 「机器人是不是管理员」就不依赖白名单接口。
func (c *QQOfficialChannel) qqGroupMember(ctx context.Context, gid, uid string) (OneBotGroupMemberInfo, error) {
	if c.isQQGroupSelf(gid, uid) {
		state, err := c.qqGroupBotState(ctx, gid)
		if err != nil {
			return OneBotGroupMemberInfo{}, err
		}
		return OneBotGroupMemberInfo{GroupID: gid, UserID: uid, Role: firstNonEmpty(state.MemberRole, "member"), IsBot: true, MembershipVerified: true}, nil
	}
	d, err := qqGroupAPIError(c.CallAPI(ctx, "GET "+qqGroupPath(gid)+"/members/"+url.PathEscape(uid), nil))
	if err != nil {
		// 没开白名单就查不到别人的身份；返回错误让门禁按「无法确认」拒绝，不能猜成普通成员。
		return OneBotGroupMemberInfo{}, err
	}
	member := qqGroupMemberInfo(gid, d)
	if member.UserID != uid {
		return OneBotGroupMemberInfo{}, fmt.Errorf("群成员响应账号不匹配")
	}
	return member, nil
}

// qqGroupMembers 按 cursor 翻完成员列表。没开白名单时不报错，回一份空名单加说明，
// 让上层照常往下走。
func (c *QQOfficialChannel) qqGroupMembers(ctx context.Context, gid string) (GroupMemberDirectory, error) {
	result := GroupMemberDirectory{Source: "qq_official_group_members"}
	if info, err := c.qqGroupInfo(ctx, gid); err == nil {
		result.Total, result.TotalKnown = info.MemberCount, info.MemberCountKnown
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < qqGroupMembersMaxPages; page++ {
		d, err := qqGroupAPIError(c.CallAPI(ctx, "GET "+qqGroupPath(gid)+"/members", map[string]any{"cursor": cursor}))
		if errors.Is(err, errQQGroupNoPermission) {
			result.Source = "qq_official_group_members_unavailable"
			result.Warnings = append(result.Warnings, "QQ 群成员列表接口仅对白名单机器人开放，当前只能使用已收到消息里的账号信息")
			return result, nil
		}
		if err != nil {
			return result, err
		}
		for _, row := range directoryRows(d["members"]) {
			member := qqGroupMemberInfo(gid, row)
			if member.UserID == "" || seen[member.UserID] {
				continue
			}
			seen[member.UserID] = true
			result.Members = append(result.Members, member)
		}
		next := stringFromAny(d["next_cursor"])
		if next == "" {
			result.Complete = true
			return result, nil
		}
		if next == cursor {
			return result, fmt.Errorf("群成员分页无进展")
		}
		cursor = next
	}
	result.Warnings = append(result.Warnings, "达到本次群成员分页上限")
	return result, nil
}

// qqGroupRemoveMembers 批量移除成员，可选同时拉黑。
func qqGroupRemoveMembers(ctx context.Context, call oneBotAPICaller, gid string, members []string, blacklist bool) (map[string]any, error) {
	if len(members) == 0 || len(members) > qqGroupBatchLimit {
		return nil, fmt.Errorf("一次移除的成员数须在 1 到 %d 之间", qqGroupBatchLimit)
	}
	d, err := qqGroupAPIError(call(ctx, "POST "+qqGroupPath(gid)+"/batch_remove_members", map[string]any{
		"member_openids": members, "add_to_member_blacklist": blacklist,
	}))
	if err != nil {
		return nil, err
	}
	if result := stringFromAny(d["remove_members_result"]); result != "" && result != "success" {
		return d, fmt.Errorf("移除成员未成功：%s", result)
	}
	return d, nil
}

// QQGroupBlacklistEntry 是群黑名单里的一个人。
type QQGroupBlacklistEntry struct {
	MemberOpenID string `json:"member_openid"`
	UnionOpenID  string `json:"union_openid,omitempty"`
	Username     string `json:"username,omitempty"`
	BannedAt     string `json:"banned_at,omitempty"`
	Bot          bool   `json:"bot,omitempty"`
}

// QQGroupBlacklist 翻完群黑名单。
func (c *QQOfficialChannel) QQGroupBlacklist(ctx context.Context, gid string) ([]QQGroupBlacklistEntry, error) {
	var out []QQGroupBlacklistEntry
	cursor := ""
	for page := 0; page < qqGroupMembersMaxPages; page++ {
		d, err := qqGroupAPIError(c.CallAPI(ctx, "GET "+qqGroupPath(gid)+"/member_blacklist", map[string]any{"cursor": cursor, "limit": 100}))
		if err != nil {
			return out, err
		}
		for _, row := range directoryRows(d["users"]) {
			bot, _ := row["bot"].(bool)
			out = append(out, QQGroupBlacklistEntry{
				MemberOpenID: stringFromAny(row["member_openid"]), UnionOpenID: stringFromAny(row["union_openid"]),
				Username: stringFromAny(row["username"]), BannedAt: stringFromAny(row["banned_at"]), Bot: bot,
			})
		}
		next := stringFromAny(d["next_cursor"])
		if next == "" || next == cursor {
			return out, nil
		}
		cursor = next
	}
	return out, nil
}

// SetQQGroupBlacklist 加入（add）或移出（del）群黑名单，返回操作失败的 openid。
// 平台规定目标还在群里时不能 add。
func (c *QQOfficialChannel) SetQQGroupBlacklist(ctx context.Context, gid, op string, members []string) ([]string, error) {
	if op != "add" && op != "del" {
		return nil, fmt.Errorf("群黑名单操作只能是 add 或 del")
	}
	if len(members) == 0 || len(members) > qqGroupBatchLimit {
		return nil, fmt.Errorf("一次操作的成员数须在 1 到 %d 之间", qqGroupBatchLimit)
	}
	d, err := qqGroupAPIError(c.CallAPI(ctx, "POST "+qqGroupPath(gid)+"/member_blacklist", map[string]any{"op": op, "member_openids": members}))
	if err != nil {
		return nil, err
	}
	return directoryStrings(d["fail_openids"]), nil
}

// qqGroupMutedMembers 查当前处于禁言中的成员（不含已过期记录）。
func qqGroupMutedMembers(ctx context.Context, call oneBotAPICaller, gid string) (map[string]bool, error) {
	d, err := qqGroupAPIError(call(ctx, "GET "+qqGroupPath(gid)+"/restrict_chat_setting", nil))
	if err != nil {
		return nil, err
	}
	muted := map[string]bool{}
	for _, row := range directoryRows(d["members"]) {
		if id := stringFromAny(row["member_openid"]); id != "" {
			muted[id] = true
		}
	}
	return muted, nil
}

// qqGroupMute 设置或解除单个成员禁言。duration<=0 表示解除；已在禁言中的改用
// update，平台把 add/update 分成两种动作。
func qqGroupMute(ctx context.Context, call oneBotAPICaller, gid, member string, duration time.Duration, now time.Time) (map[string]any, error) {
	entry := map[string]any{"member_openid": member}
	if duration <= 0 {
		entry["op"], entry["mute_expire_at"] = "del", ""
	} else {
		if duration > qqGroupMuteMax {
			duration = qqGroupMuteMax
		}
		op := "add"
		if muted, err := qqGroupMutedMembers(ctx, call, gid); err == nil && muted[member] {
			op = "update"
		}
		entry["op"], entry["mute_expire_at"] = op, now.Add(duration).Format(time.RFC3339)
	}
	return qqGroupAPIError(call(ctx, "POST "+qqGroupPath(gid)+"/restrict_chat_setting", map[string]any{"members": []any{entry}}))
}

// dispatchQQGroupModeration 是 QQ 群里的 mute/unmute/kick。
func (t *dianaPlatformTool) dispatchQQGroupModeration(ctx context.Context, operation, gid, target string, duration int, rejectAdd bool) (map[string]any, error) {
	call := func(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
		return t.runtime.callPlatformAPIForEvent(ctx, t.event, action, params)
	}
	switch operation {
	case platformOpMute:
		return qqGroupMute(ctx, call, gid, target, time.Duration(duration)*time.Second, t.runtime.clock())
	case platformOpUnmute:
		return qqGroupMute(ctx, call, gid, target, 0, t.runtime.clock())
	case platformOpKick:
		return qqGroupRemoveMembers(ctx, call, gid, []string{target}, rejectAdd)
	}
	return nil, fmt.Errorf("当前平台暂不支持此操作")
}

// qqOfficialGroupEvent 是群事件的公共字段。GROUP_JOIN_REQUEST 另有申请详情。
type qqOfficialGroupEvent struct {
	Timestamp      int64  `json:"timestamp"`
	GroupOpenID    string `json:"group_openid"`
	OpMemberOpenID string `json:"op_member_openid"`
	MemberOpenID   string `json:"member_openid"`
	UserOpenID     string `json:"user_openid"`

	JoinRequestID string `json:"join_request_id"`
	RiskTips      string `json:"risk_tips"`
	Username      string `json:"username"`
	ApplyAt       string `json:"apply_at"`
	ApplySource   string `json:"apply_source"`
	InvitedBy     string `json:"invited_by"`
	Bot           bool   `json:"bot"`
	VerifyInfo    struct {
		Method        string `json:"method"`
		VerifyMessage string `json:"verify_message"`
		ReviewQAList  []struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"review_qa_list"`
	} `json:"verify_info"`
	AutoApproved *struct {
		StrategyID string `json:"strategy_id"`
	} `json:"auto_approved"`
}

// QQ 群里机器人自身状态变化的通知子类型。运行时不回复它们，只给插件和事件记录看。
const (
	qqOfficialNoticeBotJoin         = "qq_official_bot_join"
	qqOfficialNoticeBotLeave        = "qq_official_bot_leave"
	qqOfficialNoticeProactiveReject = "qq_official_proactive_reject"
	qqOfficialNoticeProactiveAccept = "qq_official_proactive_accept"
)

// qqOfficialGroupEventFromDispatch 把 QQ 群的非消息事件翻成统一事件：
//
//   - GROUP_MEMBER_ADD / GROUP_MEMBER_REMOVE -> group_increase / group_decrease，
//     入群欢迎和退群审计照 OneBot 那套走；
//   - GROUP_JOIN_REQUEST -> 入群申请，进请求存储等主人审批；平台已自动通过的不再落待办；
//   - GROUP_ADD_ROBOT / GROUP_DEL_ROBOT / GROUP_MSG_REJECT / GROUP_MSG_RECEIVE -> 通知，
//     user_id 是操作者。
func qqOfficialGroupEventFromDispatch(eventType string, data json.RawMessage, selfID string) (MessageEvent, bool, bool) {
	subType := ""
	switch eventType {
	case "GROUP_MEMBER_ADD":
		subType = "group_increase"
	case "GROUP_MEMBER_REMOVE":
		subType = "group_decrease"
	case "GROUP_ADD_ROBOT":
		subType = qqOfficialNoticeBotJoin
	case "GROUP_DEL_ROBOT":
		subType = qqOfficialNoticeBotLeave
	case "GROUP_MSG_REJECT":
		subType = qqOfficialNoticeProactiveReject
	case "GROUP_MSG_RECEIVE":
		subType = qqOfficialNoticeProactiveAccept
	case "GROUP_JOIN_REQUEST":
	default:
		return MessageEvent{}, false, false
	}
	var raw qqOfficialGroupEvent
	if err := json.Unmarshal(data, &raw); err != nil || strings.TrimSpace(raw.GroupOpenID) == "" {
		return MessageEvent{}, false, true
	}
	if eventType == "GROUP_JOIN_REQUEST" {
		event, ok := qqOfficialJoinRequestEvent(raw, selfID)
		return event, ok, true
	}
	userID := firstNonEmpty(raw.MemberOpenID, raw.OpMemberOpenID)
	if userID == "" {
		return MessageEvent{}, false, true
	}
	when := raw.Timestamp
	if when == 0 {
		when = time.Now().Unix()
	}
	noticeData := map[string]string{"notice_type": subType}
	if subType == "group_decrease" {
		noticeData["sub_type"] = "leave"
	}
	event := MessageEvent{
		Platform: PlatformQQOfficial, PlatformScope: "qq_group",
		Kind: EventKindNotice, SubType: subType, MessageType: "notice",
		Time: when, SelfID: selfID, GroupID: raw.GroupOpenID, UserID: userID, OperatorID: raw.OpMemberOpenID,
		MessageID: fmt.Sprintf("%s-%s-%s-%d", strings.ToLower(eventType), raw.GroupOpenID, userID, when),
		Segments:  []MessageSegment{{Type: "notice", Data: noticeData}},
	}
	return event, true, true
}

func qqOfficialJoinRequestFlag(gid, mid, requestID string) string {
	return qqOfficialJoinRequestFlagPrefix + gid + ":" + mid + ":" + requestID
}

func parseQQOfficialJoinRequestFlag(flag string) (gid, mid, requestID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(flag), qqOfficialJoinRequestFlagPrefix)
	if !found {
		return "", "", "", false
	}
	// openid 是十六进制串不含冒号；申请 ID 放最后，自带冒号也不怕。
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// qqOfficialJoinRequestComment 把验证消息、问答、邀请人和安全提示并进附言，主人审批时看得到。
func qqOfficialJoinRequestComment(raw qqOfficialGroupEvent) string {
	var parts []string
	if name := strings.TrimSpace(raw.Username); name != "" {
		parts = append(parts, "昵称："+name)
	}
	if msg := strings.TrimSpace(raw.VerifyInfo.VerifyMessage); msg != "" {
		parts = append(parts, "验证消息："+msg)
	}
	for _, qa := range raw.VerifyInfo.ReviewQAList {
		parts = append(parts, "问："+strings.TrimSpace(qa.Question)+" 答："+strings.TrimSpace(qa.Answer))
	}
	if raw.ApplySource == "invited" && raw.InvitedBy != "" {
		parts = append(parts, "邀请人："+raw.InvitedBy)
	}
	if raw.Bot {
		parts = append(parts, "申请人是机器人账号")
	}
	switch raw.RiskTips {
	case "warning_tips":
		parts = append(parts, "平台提示：可疑申请")
	case "top_tips":
		parts = append(parts, "平台提示：命中安全规则")
	}
	return strings.Join(parts, "；")
}

func qqOfficialJoinRequestEvent(raw qqOfficialGroupEvent, selfID string) (MessageEvent, bool) {
	gid, mid := strings.TrimSpace(raw.GroupOpenID), strings.TrimSpace(raw.MemberOpenID)
	// 平台按自动审批策略已经放人进群的，没有待办可审。
	if gid == "" || mid == "" || raw.AutoApproved != nil {
		return MessageEvent{}, false
	}
	when := time.Now().Unix()
	if at, err := time.Parse(time.RFC3339, raw.ApplyAt); err == nil {
		when = at.Unix()
	}
	flag := qqOfficialJoinRequestFlag(gid, mid, raw.JoinRequestID)
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{selfID, "group", "add", flag}, "\x00")))
	comment := qqOfficialJoinRequestComment(raw)
	data := map[string]string{"request_type": "group", "sub_type": "add", "user_id": mid, "group_id": gid}
	if comment != "" {
		data["comment"] = comment
	}
	return MessageEvent{
		Platform: PlatformQQOfficial, PlatformScope: "qq_group",
		Kind: EventKindRequest, SubType: "add", Time: when, SelfID: selfID,
		UserID: mid, GroupID: gid, SenderName: strings.TrimSpace(raw.Username),
		MessageID:   fmt.Sprintf("request-%x", fingerprint[:12]),
		MessageType: "group",
		RawMessage:  oneBotRequestSummary("group", "add"),
		Segments:    []MessageSegment{{Type: "request", Data: data}},
		oneBotRequest: &OneBotRequestEvent{
			RequestType: "group", SubType: "add", UserID: mid, GroupID: gid, Comment: comment, Flag: flag,
		},
	}, true
}

// qqOfficialJoinRequestAction 把审批翻成 approval_join_request 调用。
func qqOfficialJoinRequestAction(item OneBotRequestRecord, approve bool, reason string) (string, map[string]any, error) {
	gid, mid, requestID, ok := parseQQOfficialJoinRequestFlag(item.Flag)
	if !ok {
		return "", nil, fmt.Errorf("请求 %s 的 QQ 群申请句柄无效", item.ID)
	}
	params := map[string]any{"op": "approve"}
	if requestID != "" {
		params["join_request_id"] = requestID
	}
	if !approve {
		params["op"] = "decline"
		if reason != "" {
			params["reject_reason"] = reason
		}
	}
	return "POST " + qqGroupPath(gid) + "/approval_join_request/" + url.PathEscape(mid), params, nil
}

// syncQQOfficialJoinRequests 从当前 QQ 群的 join_request_list 补拉没收到推送的申请。
func (r *Runtime) syncQQOfficialJoinRequests(ctx context.Context, event MessageEvent) (int, error) {
	store := r.oneBotRequestStore()
	if store == nil {
		return 0, fmt.Errorf("请求存储未配置")
	}
	gid := strings.TrimSpace(event.GroupID)
	if event.PlatformScope != "qq_group" || gid == "" {
		return 0, fmt.Errorf("QQ 官方机器人只能在 QQ 群里补拉本群的入群申请")
	}
	syncCtx, cancel := context.WithTimeout(ctx, oneBotRequestSyncTimeout)
	defer cancel()
	saved := 0
	cursor := ""
	for page := 0; page < 10; page++ {
		d, err := qqGroupAPIError(r.callPlatformAPIForEvent(syncCtx, event, "GET "+qqGroupPath(gid)+"/join_request_list", map[string]any{"cursor": cursor, "limit": 100}))
		if err != nil {
			return saved, err
		}
		for _, row := range directoryRows(d["list"]) {
			var raw qqOfficialGroupEvent
			encoded, _ := json.Marshal(row)
			if json.Unmarshal(encoded, &raw) != nil {
				continue
			}
			raw.GroupOpenID = gid
			request, ok := qqOfficialJoinRequestEvent(raw, event.SelfID)
			if !ok {
				continue
			}
			now := r.clock()
			req := request.oneBotRequest
			_, inserted, err := store.SaveOneBotRequest(syncCtx, OneBotRequestRecord{
				ProfileID: strings.TrimSpace(event.ProfileID), Platform: PlatformQQOfficial, SelfID: event.SelfID,
				RequestType: req.RequestType, SubType: req.SubType, UserID: req.UserID, GroupID: req.GroupID,
				Comment: req.Comment, Flag: req.Flag, Status: OneBotRequestPending, CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				return saved, err
			}
			if inserted {
				saved++
			}
		}
		next := stringFromAny(d["next_cursor"])
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	return saved, nil
}
