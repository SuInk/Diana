// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
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

// runOwnerStickerRemove 处理「删除表情包」（也认 /remove）：主人回复一条带表情包的消息发这句，
// 就把引用里的图移出这个机器人的表情包库，并拉黑，之后再有人发同一张也不收。
// 认图靠内容哈希，所以回复别人发的原图和回复机器人发出去的那张都行。
func runOwnerStickerRemove(r *Runtime, event MessageEvent, _ string) string {
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	remover, ok := store.(StickerAssetRemover)
	if !ok {
		return "表情包库没有配置，无法删除。"
	}
	ctx, cancel := context.WithTimeout(context.Background(), stickerRemoveReadTimeout)
	defer cancel()
	if event.Quoted == nil || !hasImageSegment(event.Quoted.Segments) {
		return "请回复要删除的那张表情包，再发「删除表情包」。"
	}
	hashes, unreadable := quotedImageHashes(ctx, event.Quoted.Segments)
	if len(hashes) == 0 {
		return fmt.Sprintf("引用里的 %d 张图都读取失败，没法确认是哪一张，请稍后再试。", unreadable)
	}
	removed := 0
	for _, hash := range hashes {
		count, err := remover.DeleteStickerAsset(ctx, hash, event.ProfileID)
		if err != nil {
			log.Printf("diana owner sticker remove failed: %v", err)
			return "删除表情包失败，请稍后重试或检查服务日志。"
		}
		if count > 0 {
			removed++
		}
	}
	if removed == 0 {
		return "这张图不在表情包库里；已加入黑名单，以后收到也不会收录。"
	}
	return fmt.Sprintf("已从表情包库移除 %d 张，以后收到也不会再收录；聊天记录里的图片不受影响。", removed)
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
