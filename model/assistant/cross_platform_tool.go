// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cross_platform 让主人在一台机器人上读写另一台机器人那边的会话：列出实例里在跑的
// 机器人、看某台机器人在哪些群、读那边群里最近的消息或按关键词搜、往那边的群或
// 私聊发一条。
//
// 只给主人。别的机器人那边的群聊记录对这边的群友来说就是别人家的聊天，谁都能问
// 就成了窃听；发消息同理。读写都复用现有的链路：读走 chat_history 的同一套实现，
// 发走 cross_session_message 的投递（准入、屏蔽、限流、好友托管全都照旧）。
const (
	dianaCrossPlatformToolName = "cross_platform"
	// maxCrossPlatformGroups 和 group_list 同一个道理，几百个群全倒进结果只会挤占上下文。
	maxCrossPlatformGroups = 200
)

type dianaCrossPlatformTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaCrossPlatformTool(runtime *Runtime, event MessageEvent) *dianaCrossPlatformTool {
	return &dianaCrossPlatformTool{runtime: runtime, event: event}
}

func (t *dianaCrossPlatformTool) Name() string { return dianaCrossPlatformToolName }

func (t *dianaCrossPlatformTool) Description() string {
	return "跨机器人、跨平台读写会话，仅主人：bots 列出实例里在跑的机器人（各在哪个平台），groups 看某台机器人在哪些群，" +
		"recent/search 读那台机器人某个群或私聊的记录，send 往那边的群或私聊发消息。" +
		"profile_id 取自 bots，group_id 取自 groups，勿编。当前这台机器人自己的会话用 chat_history 和 cross_session_message。"
}

func (t *dianaCrossPlatformTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":      toolEnumParam("bots 列机器人；groups 列群；recent 最近记录；search 搜记录；send 发消息", "bots", "groups", "recent", "search", "send"),
		"profile_id":     toolStringParam("目标机器人 ID，取自 bots"),
		"group_id":       toolStringParam("目标群 ID，与 user_id 二选一"),
		"user_id":        toolStringParam("目标私聊账号，与 group_id 二选一"),
		"query":          toolStringParam("search：关键词；设了 sender_user_id 可省略"),
		"sender_user_id": toolStringParam("search：只看这个账号发的消息"),
		"days":           toolIntParam("search：只看最近多少天", 1, 365),
		"limit":          toolIntParam("recent/search 返回条数，默认 "+itoa(defaultChatHistoryRecentLimit), 1, maximumChatHistoryResultLimit),
		"message":        toolStringParam("send：完整正文，≤" + itoa(crossSessionMaxRunes) + " 字；分条用 <diana-message/>；只转附件时可留空"),
		"forward_media":  toolBoolParam("send：把当前消息和它引用的消息里的图片、视频、语音、文件一并发过去"),
	})
}

// CanonicalOperation 是 Run 实际执行的操作，按操作拦截时用同一套换算。
func (*dianaCrossPlatformTool) CanonicalOperation(input map[string]any) string {
	return strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
}

type dianaCrossPlatformBot struct {
	ProfileID string `json:"profile_id"`
	Name      string `json:"name,omitempty"`
	Platform  string `json:"platform"`
	Account   string `json:"account,omitempty"`
	Connected bool   `json:"connected"`
	Current   bool   `json:"current,omitempty"`
	// Owned 为假的机器人属于别的主人，只列出来让人知道它存在，读写都会被拒。
	Owned bool `json:"owned"`
}

func (t *dianaCrossPlatformTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("跨平台：运行时不可用")
	}
	if !t.runtime.relationshipPolicy(ctx, t.event).Owner {
		return "", fmt.Errorf("跨机器人读写会话只有机器人主人能用")
	}
	switch operation := t.CanonicalOperation(input); operation {
	case "bots":
		return t.bots()
	case "groups":
		return t.groups(ctx, input)
	case "recent", "search":
		return t.read(ctx, operation, input)
	case "send":
		return t.send(ctx, input)
	default:
		return "", fmt.Errorf("operation 必须是 bots、groups、recent、search 或 send")
	}
}

func (t *dianaCrossPlatformTool) bots() (string, error) {
	profiles := t.runtime.runnableProfiles()
	bots := make([]dianaCrossPlatformBot, 0, len(profiles))
	for _, cfg := range profiles {
		bot := dianaCrossPlatformBot{
			ProfileID: cfg.ID,
			Name:      strings.TrimSpace(cfg.Name),
			Platform:  NormalizePlatformID(cfg.Platform),
			Account:   strings.TrimSpace(cfg.BotAccount),
			Current:   cfg.ID == strings.TrimSpace(t.event.ProfileID),
			Owned:     t.runtime.requesterOwnsProfile(t.event, cfg),
		}
		if status, ok := t.runtime.profileChannelStatus(cfg); ok {
			bot.Connected = status.Connected
			bot.Account = firstNonEmpty(strings.TrimSpace(status.SelfID), bot.Account)
		}
		bots = append(bots, bot)
	}
	return marshalCrossPlatform("bots", map[string]any{"bots": bots})
}

// groups 列出一台机器人所在的群。OneBot 能实时拉群列表；别的平台没有这个接口，
// 只能列出控制台里有配置的群（机器人在群里说过话或被配置过才会有）。
func (t *dianaCrossPlatformTool) groups(ctx context.Context, input map[string]any) (string, error) {
	target, err := t.runtime.ownedCrossBotTarget(t.event, configToolString(input, "profile_id"), "", "")
	if err != nil {
		return "", err
	}
	cfg := target.profile
	var groups []dianaPlatformGroupItem
	source := "group_configs"
	if IsOneBotPlatform(cfg.Platform) {
		callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		data, callErr := t.runtime.callOneBotAPIForEvent(callCtx, MessageEvent{ProfileID: cfg.ID, Platform: cfg.Platform}, "get_group_list", map[string]any{"no_cache": true})
		cancel()
		if callErr != nil {
			return "", fmt.Errorf("拉取群列表失败：%w", callErr)
		}
		raw, ok := structuredGroupListItems(data)
		if !ok {
			return "", fmt.Errorf("群列表返回格式无法识别")
		}
		source = "onebot_group_list"
		for _, value := range raw {
			item, ok := value.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(stringFromAny(item["group_id"]))
			if id == "" {
				continue
			}
			count, _ := strconv.Atoi(strings.TrimSpace(stringFromAny(item["member_count"])))
			groups = append(groups, dianaPlatformGroupItem{GroupID: id, GroupName: strings.TrimSpace(stringFromAny(item["group_name"])), MemberCount: count})
		}
	} else {
		for _, id := range t.runtime.configuredGroupIDs(cfg.ID) {
			groups = append(groups, dianaPlatformGroupItem{GroupID: id})
		}
	}
	payload := map[string]any{"profile_id": cfg.ID, "platform": NormalizePlatformID(cfg.Platform), "group_count": len(groups), "source": source}
	if source == "group_configs" {
		payload["note"] = "这个平台没有「列出已加入的群」接口，这里只有控制台里有配置的群，不是全部。"
	}
	if len(groups) > maxCrossPlatformGroups {
		groups = groups[:maxCrossPlatformGroups]
		payload["groups_truncated"] = true
	}
	payload["groups"] = groups
	return marshalCrossPlatform("groups", payload)
}

// target 解析 profile_id + group_id/user_id。
func (t *dianaCrossPlatformTool) target(input map[string]any) (crossBotTarget, error) {
	groupID := strings.TrimSpace(configToolString(input, "group_id"))
	userID := strings.TrimSpace(configToolString(input, "user_id"))
	switch {
	case groupID != "" && userID != "":
		return crossBotTarget{}, fmt.Errorf("group_id 和 user_id 只能填一个")
	case groupID != "":
		return t.runtime.ownedCrossBotTarget(t.event, configToolString(input, "profile_id"), EventKindGroup, groupID)
	case userID != "":
		return t.runtime.ownedCrossBotTarget(t.event, configToolString(input, "profile_id"), EventKindPrivate, userID)
	}
	return crossBotTarget{}, fmt.Errorf("需要 group_id 或 user_id 指明是哪个会话")
}

// read 借 chat_history 的实现读另一台机器人的会话：把工具的「当前会话」换成目标会话，
// 检索、分页、图片描述、身份标注都和本机一样。
func (t *dianaCrossPlatformTool) read(ctx context.Context, operation string, input map[string]any) (string, error) {
	target, err := t.target(input)
	if err != nil {
		return "", err
	}
	history := newDianaChatHistoryTool(t.runtime, target.event)
	forwarded := map[string]any{}
	for _, key := range []string{"query", "sender_user_id", "days", "limit"} {
		if value, ok := input[key]; ok {
			forwarded[key] = value
		}
	}
	var result dianaChatHistoryResult
	if operation == "recent" {
		result, err = history.recent(ctx, forwarded)
	} else {
		if strings.TrimSpace(configToolString(input, "query")) == "" && strings.TrimSpace(configToolString(input, "sender_user_id")) == "" {
			return "", fmt.Errorf("search 需要 query 或 sender_user_id")
		}
		result, err = history.search(ctx, forwarded)
	}
	if err != nil {
		return "", err
	}
	result.Action = operation
	result.Message = strings.TrimSpace(result.Message + " 这是机器人 " + target.profile.ID + "（" + NormalizePlatformID(target.profile.Platform) + "）那边的会话，不是当前会话。")
	return marshalDianaChatHistoryResult(result)
}

// send 用 cross_session_message 的投递链路发到另一台机器人的会话。目的地的准入、
// 群开关、屏蔽名单按那台机器人的配置判，不因为是主人从别处要求就绕过。
func (t *dianaCrossPlatformTool) send(ctx context.Context, input map[string]any) (string, error) {
	target, err := t.target(input)
	if err != nil {
		return "", err
	}
	portable := NormalizePlatformID(target.profile.Platform) != NormalizePlatformID(t.event.Platform)
	message, media, err := crossSessionSendInput(input, t.event, portable)
	if err != nil {
		return "", err
	}
	event := target.event
	if sessionKey(event) == sessionKey(t.event) && event.ProfileID == t.event.ProfileID {
		return "", fmt.Errorf("这就是当前会话，直接把话说出来就行，不用这个工具")
	}
	cfg := t.runtime.effectiveConfigForEvent(event)
	label := ""
	if event.Kind == EventKindGroup {
		if !t.runtime.admitsGroupScope(cfg, event) {
			return "", fmt.Errorf("机器人 %s 在群 %s 没有准入或已被关掉，先在控制台打开再发", target.profile.ID, event.GroupID)
		}
		if missing, verified := t.runtime.groupMissingFromOneBot(event); verified && missing {
			return "", fmt.Errorf("机器人 %s 不在群 %s 里", target.profile.ID, event.GroupID)
		}
		label = NormalizePlatformID(target.profile.Platform) + " 群 " + event.GroupID
	} else {
		if selfID := strings.TrimSpace(event.SelfID); selfID != "" && event.UserID == selfID {
			return "", fmt.Errorf("不能给机器人自己发消息")
		}
		if !privateAdmissionAllowsConfig(cfg, event) {
			return "", fmt.Errorf("机器人 %s 的私聊准入没放行这个人，不能给他发私聊", target.profile.ID)
		}
		label = NormalizePlatformID(target.profile.Platform) + " 账号 " + event.UserID + " 的私聊"
	}
	outcome, err := t.runtime.sendCrossSessionMessage(ctx, t.event, crossSessionTarget{
		event: event,
		key:   "profile|" + target.profile.ID + "|" + strings.TrimPrefix(sessionKey(event), event.ContextNamespace+":"),
		label: label,
	}, message, media)
	if err != nil {
		return "", err
	}
	markExternalSideEffect(ctx)
	result := dianaCrossSessionResult{
		OK: true, Delivered: !outcome.Pending, Pending: outcome.Pending,
		Target: label, Runes: len([]rune(message)), Media: media.count(),
		Message: "已经发到" + label + "了。在当前会话里只说一句交代，不要把正文复述出来，也不要再调一次这个工具。",
	}
	if outcome.Pending {
		result.Message = outcome.Note
	}
	body, err := json.Marshal(result)
	return string(body), err
}

// configuredGroupIDs 列出控制台里归这台机器人的群配置。
func (r *Runtime) configuredGroupIDs(profileID string) []string {
	r.mu.RLock()
	lister, _ := r.groupConfigs.(groupConfigLister)
	r.mu.RUnlock()
	if lister == nil {
		return nil
	}
	var ids []string
	for _, groupCfg := range lister.Groups().Groups {
		if strings.TrimSpace(groupCfg.BotProfileID) == profileID {
			ids = append(ids, groupCfg.GroupID)
		}
	}
	return cleanStrings(ids)
}

func marshalCrossPlatform(operation string, extra map[string]any) (string, error) {
	payload := map[string]any{"ok": true, "operation": operation}
	for key, value := range extra {
		payload[key] = value
	}
	body, err := json.Marshal(payload)
	return string(body), err
}
