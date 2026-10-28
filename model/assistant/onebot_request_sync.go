// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 请求推送只在机器人在线、连接正常的那一刻才收得到：断线、重启期间来的入群申请，
// 以及被 QQ 折叠进「可疑好友申请」的那些，压根不会推给 Diana。主人一问「有没有
// 人申请进群」，只看推送记录就会漏。SnowLuma 能直接从 QQ 拉这两个收件箱，list 时
// 先补拉一遍，存进同一张请求表，之后审批走同一条路。

// oneBotRequestSubTypeDoubt 标记被 QQ 折叠的可疑好友申请，审批要走 set_doubt_friends_add_request。
const oneBotRequestSubTypeDoubt = "doubt"

const (
	oneBotRequestSyncTimeout = 10 * time.Second
	oneBotRequestSyncCount   = 50
)

// syncOneBotPendingRequests 补拉待处理的入群申请和可疑好友申请，返回新存下的条数。
// 两个收件箱互不影响：一边失败另一边照常存，两边都失败才报错。
func (r *Runtime) syncOneBotPendingRequests(ctx context.Context, event MessageEvent) (int, error) {
	store := r.oneBotRequestStore()
	if store == nil {
		return 0, fmt.Errorf("请求存储未配置")
	}
	syncCtx, cancel := context.WithTimeout(ctx, oneBotRequestSyncTimeout)
	defer cancel()
	base := OneBotRequestRecord{
		ProfileID: strings.TrimSpace(event.ProfileID),
		Platform:  firstNonEmpty(NormalizePlatformID(event.Platform), PlatformOneBotV11),
		SelfID:    strings.TrimSpace(event.SelfID),
		Status:    OneBotRequestPending,
	}
	var failures []error
	apiFailures := 0
	saved := 0
	save := func(record OneBotRequestRecord) {
		now := r.clock()
		record.CreatedAt, record.UpdatedAt = now, now
		if _, inserted, err := store.SaveOneBotRequest(syncCtx, record); err != nil {
			failures = append(failures, err)
		} else if inserted {
			saved++
		}
	}

	groups, err := r.callOneBotAPIForEvent(syncCtx, event, "get_group_system_msg", map[string]any{"only_pending": true, "count": oneBotRequestSyncCount})
	if err != nil {
		apiFailures++
		failures = append(failures, fmt.Errorf("入群申请：%w", err))
	} else {
		for _, item := range oneBotResultItems(groups) {
			flag := strings.TrimSpace(stringFromAny(item["flag"]))
			if flag == "" || toolInputBool(item, "checked") {
				continue
			}
			record := base
			record.RequestType, record.SubType = "group", "add"
			record.GroupID = strings.TrimSpace(stringFromAny(item["group_id"]))
			record.UserID = strings.TrimSpace(stringFromAny(item["requester_uin"]))
			record.Comment = strings.TrimSpace(stringFromAny(item["message"]))
			record.Flag = flag
			save(record)
		}
	}

	doubts, err := r.callOneBotAPIForEvent(syncCtx, event, "get_doubt_friends_add_request", map[string]any{"count": oneBotRequestSyncCount})
	if err != nil {
		apiFailures++
		failures = append(failures, fmt.Errorf("可疑好友申请：%w", err))
	} else {
		for _, item := range oneBotResultItems(doubts) {
			uid := strings.TrimSpace(stringFromAny(item["uid"]))
			if uid == "" {
				continue
			}
			// 可疑申请只给 uid 不给 QQ 号，昵称、来源和留言并进附言让主人认得出是谁。
			var parts []string
			for _, field := range []struct{ label, key string }{{"昵称", "nick"}, {"来源", "source"}, {"留言", "msg"}} {
				if value := strings.TrimSpace(stringFromAny(item[field.key])); value != "" {
					parts = append(parts, field.label+"："+value)
				}
			}
			record := base
			record.RequestType, record.SubType = "friend", oneBotRequestSubTypeDoubt
			record.Comment = strings.Join(parts, "；")
			record.Flag = doubtRequestFlag(uid, stringFromAny(item["reqTime"]))
			save(record)
		}
	}
	// 只有一个收件箱拉不到（比如别的 OneBot 实现没有可疑申请接口）不算失败。
	if apiFailures == 2 || len(failures) > apiFailures {
		return saved, errors.Join(failures...)
	}
	return saved, nil
}

// oneBotResultItems 取出返回值里的对象数组。接入端直接回数组时，callOneBotAPIForEvent
// 会把它包进 items/list。
func oneBotResultItems(data map[string]any) []map[string]any {
	raw, _ := structuredGroupListItems(data)
	out := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		if item, ok := value.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

// doubtRequestFlag 把申请时间拼进存储用的句柄。请求表按句柄去重，只用 uid 的话，
// 同一个人被拒后再申请就会被当成旧记录吞掉；审批时用 doubtRequestUID 拆回 uid。
func doubtRequestFlag(uid, requestTime string) string {
	if requestTime = strings.TrimSpace(requestTime); requestTime != "" {
		return uid + "|" + requestTime
	}
	return uid
}

func doubtRequestUID(flag string) string {
	uid, _, _ := strings.Cut(flag, "|")
	return uid
}
