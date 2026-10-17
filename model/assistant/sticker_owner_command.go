// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// StickerAssetRemover 把一张表情包移出池子并拉黑，返回删掉了几条会话记录。
// profileID 非空时只动这个机器人的。
type StickerAssetRemover interface {
	DeleteStickerAsset(ctx context.Context, hash, profileID string) (int, error)
}

// stickerRemoveReadTimeout 是引用的图没有本地缓存、要现下载来算哈希时的等待上限。
const stickerRemoveReadTimeout = 15 * time.Second

var (
	errStickerRemoveNoStore = errors.New("表情包库没有配置，无法删除")
	errStickerRemoveNoQuote = errors.New("没有引用带图的消息")
)

// stickerRemoveResult 是一次「删除引用里的表情包」的结果：Removed 是原本在库里、
// 这次移出的张数，Blocked 是拉黑的张数（含本来不在库里的），Unreadable 是读不到的图。
type stickerRemoveResult struct {
	Removed    int
	Blocked    int
	Unreadable int
}

// removeQuotedStickers 把这条消息引用里的图移出这个机器人的表情包库并拉黑，之后再有人
// 发同一张也不收。认图靠内容哈希，所以引用别人发的原图和机器人发出去的那张都行。
// 主人的固定指令和 Agent 工具都走这里；谁能调由调用方把关。
func (r *Runtime) removeQuotedStickers(ctx context.Context, event MessageEvent) (stickerRemoveResult, error) {
	var result stickerRemoveResult
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	remover, ok := store.(StickerAssetRemover)
	if !ok {
		return result, errStickerRemoveNoStore
	}
	if event.Quoted == nil || !hasImageSegment(event.Quoted.Segments) {
		return result, errStickerRemoveNoQuote
	}
	ctx, cancel := context.WithTimeout(ctx, stickerRemoveReadTimeout)
	defer cancel()
	hashes, unreadable := quotedImageHashes(ctx, event.Quoted.Segments)
	result.Unreadable = unreadable
	for _, hash := range hashes {
		count, err := remover.DeleteStickerAsset(ctx, hash, event.ProfileID)
		if err != nil {
			return result, fmt.Errorf("delete sticker: %w", err)
		}
		result.Blocked++
		if count > 0 {
			result.Removed++
		}
	}
	return result, nil
}

// runOwnerStickerRemove 是主人的固定指令「删除表情包」：回复那张表情包发这句，直接执行，
// 不过模型。别的说法交给 Agent 的 sticker_remove 工具去理解。
func runOwnerStickerRemove(r *Runtime, event MessageEvent, _ string) string {
	result, err := r.removeQuotedStickers(context.Background(), event)
	switch {
	case errors.Is(err, errStickerRemoveNoStore):
		return err.Error() + "。"
	case errors.Is(err, errStickerRemoveNoQuote):
		return "请回复要删除的那张表情包，再发「删除表情包」。"
	case err != nil:
		log.Printf("diana owner sticker remove failed: %v", err)
		return "删除表情包失败，请稍后重试或检查服务日志。"
	}
	return result.summary()
}

func (result stickerRemoveResult) summary() string {
	switch {
	case result.Blocked == 0:
		return fmt.Sprintf("引用里的 %d 张图都读取失败，没法确认是哪一张，请稍后再试。", result.Unreadable)
	case result.Removed == 0:
		return "这张图不在表情包库里；已加入黑名单，以后收到也不会收录。"
	default:
		return fmt.Sprintf("已从表情包库移除 %d 张，以后收到也不会再收录；聊天记录里的图片不受影响。", result.Removed)
	}
}

// quotedImageHashes 算出引用里每张图的内容哈希，和入库时的 content_sha256 一致。
// 引用的图多半没走过缓存（Agent 模式只预处理当前消息的图），认不出就现下载一次。
func quotedImageHashes(ctx context.Context, segments []MessageSegment) ([]string, int) {
	var hashes []string
	seen := map[string]bool{}
	unreadable := 0
	for _, segment := range segments {
		if segment.Type != "image" {
			continue
		}
		hash, ok := imageSegmentContentSHA256(segment)
		if !ok {
			body, _, err := ReadMessageImageSegment(ctx, segment)
			if err != nil || len(body) == 0 {
				unreadable++
				continue
			}
			hash = imageBytesSHA256(body)
		}
		if !seen[hash] {
			seen[hash] = true
			hashes = append(hashes, hash)
		}
	}
	return hashes, unreadable
}
