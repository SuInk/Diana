package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type semanticReferencePromptContext struct {
	Requested      int
	Resolved       int
	TextSources    int
	ExpectedImages int
	Block          string
}

func (r *Runtime) semanticReferenceContextBlock(ctx context.Context, event MessageEvent) semanticReferencePromptContext {
	ids := eventSemanticSourceMessageIDs(event)
	result := semanticReferencePromptContext{Requested: len(ids)}
	if len(ids) == 0 {
		return result
	}
	botAccount := r.effectiveConfigForEvent(event).BotAccount
	lines := make([]string, 0, len(ids))
	for _, messageID := range ids {
		source, found := r.findSemanticReferenceEvent(ctx, event, messageID)
		if !found {
			continue
		}
		result.Resolved++
		result.ExpectedImages += historicalStillImageCount(source)
		text := strings.TrimSpace(rawMessageWithoutImagePlaceholders(firstNonEmpty(PlainText(source.Segments), source.RawMessage, source.botReply)))
		if text == "" {
			continue
		}
		result.TextSources++
		role := "用户"
		if strings.TrimSpace(source.botReply) != "" || assistantHistoryEvent(source, botAccount) {
			role = "Diana"
		}
		timeLabel := "未知时间"
		if source.Time > 0 {
			timeLabel = time.Unix(source.Time, 0).In(profileLocation(source.ProfileID)).Format("2006-01-02 15:04:05")
		}
		lines = append(lines, fmt.Sprintf("- message_id=%s；时间=%s；角色=%s；发送者=%s\n  正文：%s",
			messageID, timeLabel, role, source.SenderNameOrID(), text))
	}
	if len(lines) > 0 {
		result.Block = fmt.Sprintf("【语义指代选中的历史文字来源，共 %d 条；以下是完整来源，必须逐条核对】\n%s",
			result.TextSources, strings.Join(lines, "\n"))
	}
	return result
}
