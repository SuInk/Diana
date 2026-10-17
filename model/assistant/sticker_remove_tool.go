// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
)

const dianaStickerRemoveToolName = "sticker_remove"

// dianaStickerRemoveTool 让主人用任意说法删表情包：「这张别用了」之类由模型理解后来调，
// 不用一条条写死口令。/remove 是内置指令，不过模型。
// 只给主人注册，Run 里再按事件核一次，不靠提示词把关。
type dianaStickerRemoveTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *dianaStickerRemoveTool) Name() string { return dianaStickerRemoveToolName }

func (t *dianaStickerRemoveTool) Description() string {
	return "主人要求删掉、不再使用某张表情包时调用：把当前消息引用的那条消息里的图移出表情包库并拉黑，以后有人再发也不收录。" +
		"只在主人明确要求时用，不要因为图不好看就自行删除；没有引用图片时请主人回复那张表情包再说。"
}

func (t *dianaStickerRemoveTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{})
}

func (t *dianaStickerRemoveTool) Run(ctx context.Context, _ map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("机器人运行时不可用")
	}
	if !t.runtime.effectiveConfigForEvent(t.event).IsOwnerEvent(t.event) {
		return "", fmt.Errorf("只有本机主人可以删除表情包")
	}
	result, err := t.runtime.removeQuotedStickers(ctx, t.event)
	switch {
	case errors.Is(err, errStickerRemoveNoQuote):
		return "", fmt.Errorf("当前消息没有引用带图的消息；请主人回复要删除的那张表情包再说")
	case err != nil:
		return "", err
	}
	return result.summary(), nil
}
