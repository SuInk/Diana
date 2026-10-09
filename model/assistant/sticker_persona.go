// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 表情包库是从群聊里收来的，什么都有：成人梗、粗口、猥琐大叔口吻。关键词再对得上，
// 以机器人本人的身份发出去不合人设也不能发。判断交给模型，带着 SOUL.md 以角色本人的
// 视角回答「这张你会发吗」；结果按人设指纹缓存，同一张图同一份人设只问一次。

const stickerPersonaFitTimeout = 20 * time.Second

// StickerPersonaFitStore 缓存人设判断。没判过的图不在 StickerPersonaFit 的结果里。
type StickerPersonaFitStore interface {
	StickerPersonaFit(ctx context.Context, personaKey string, hashes []string) (map[string]bool, error)
	SaveStickerPersonaFit(ctx context.Context, personaKey, contentSHA256 string, fit bool, reason string) error
}

var promptStickerPersonaFitSpec = registerPrompt(PromptSpec{
	Key:   "media.sticker.persona_fit",
	Group: PromptGroupMedia,
	Title: "表情包 · 合不合人设",
	Usage: "机器人要发一张表情包前，按人设判断它以自己的身份发这张自不自然；判定不会发的就不发，结果按人设缓存。",
	Default: "你要在聊天里以自己的身份发一张表情包，先判断它合不合你的人设。表情包的内容：\n名称：{name}\n简介：{description}\n标签：{tags}\n" +
		"表情包是借来的表达：图里是别的角色、用了别的自称（比如「小肥鱼」「本喵」）、带着撒娇自恋调皮摆烂这类情绪都没关系，只要这份情绪你会有，就可以发。" +
		"只有这些不发：低俗露骨或成人梗（包括带性暗示的动作）、辱骂挑衅或恶意嘲讽、和你的气质完全相反的口吻（例如猥琐大叔腔、油腻撩人）。" +
		"只回答一行：「会」或者「不会：一句原因」。",
	Vars: []PromptVar{
		{Name: "name", Description: "表情包名称，平台没给名称时是「动画表情」"},
		{Name: "description", Description: "表情包的简介"},
		{Name: "tags", Description: "表情包的检索标签，没有时是「无」"},
	},
})

func (r *Runtime) stickerPersonaFitStore() StickerPersonaFitStore {
	r.mu.RLock()
	store := r.messageStore
	r.mu.RUnlock()
	fitStore, _ := store.(StickerPersonaFitStore)
	return fitStore
}

// stickerPersonaKey 是人设全文加标注版本的指纹：人设一改、或者看图方式变了（动图改成
// 多帧分镜），旧的判断自然作废。
func stickerPersonaKey(cfg BotConfig) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(cfg.personaPrompt()) + "\x00" + stickerAnnotationVersion))
	return hex.EncodeToString(sum[:8])
}

// dropKnownUnfitStickers 按缓存去掉已经判定不合人设的候选。只查缓存，不调模型：
// 检索每一轮都跑，候选成百上千张。
func (t *dianaStickerTool) dropKnownUnfitStickers(ctx context.Context, candidates []stickerCandidate) []stickerCandidate {
	store := t.runtime.stickerPersonaFitStore()
	if store == nil || len(candidates) == 0 {
		return candidates
	}
	hashes := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Hash != "" {
			hashes = append(hashes, candidate.Hash)
		}
	}
	verdicts, err := store.StickerPersonaFit(ctx, stickerPersonaKey(t.runtime.effectiveConfigForEvent(t.event)), hashes)
	if err != nil || len(verdicts) == 0 {
		return candidates
	}
	kept := candidates[:0]
	for _, candidate := range candidates {
		if fit, judged := verdicts[candidate.Hash]; judged && !fit {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

// fitsPersona 判断这张表情包以机器人本人的身份发出去合不合人设。只用在自动配图上：
// 走 sticker 工具时是带着人设的 Agent 自己看过候选再挑的，不用再问一遍。
// 没有简介时靠平台给的名称判断，连名称都没有就无从判断，按不发处理；模型调用失败
// 也不发，但不缓存，下次再问。
func (t *dianaStickerTool) fitsPersona(ctx context.Context, candidate stickerCandidate) (bool, string) {
	description := strings.TrimSpace(candidate.Description)
	named := candidate.Summary != "" && candidate.Summary != "动画表情"
	if candidate.Hash == "" || (description == "" && !named) {
		return false, "没有简介也没有名称，无从判断"
	}
	if description == "" {
		description = "（没有简介，只能看名称）"
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	personaKey := stickerPersonaKey(cfg)
	store := t.runtime.stickerPersonaFitStore()
	if store != nil {
		if verdicts, err := store.StickerPersonaFit(ctx, personaKey, []string{candidate.Hash}); err == nil {
			if fit, judged := verdicts[candidate.Hash]; judged {
				return fit, "缓存"
			}
		}
	}
	tags := strings.Join(candidate.Tags, "、")
	if tags == "" {
		tags = "无"
	}
	instruction := cfg.promptf(promptStickerPersonaFitSpec, map[string]string{
		"name": firstNonEmpty(candidate.Summary, "动画表情"), "description": truncateRunes(description, 400), "tags": tags,
	})
	messages := t.runtime.withUserFacingPersona(t.event, []llm.Message{{Role: llm.RoleUser, Content: instruction}})
	callCtx, cancel := context.WithTimeout(withLLMUsagePurpose(withLLMUsageContext(ctx, t.event), PurposeStickerPersonaFit), stickerPersonaFitTimeout)
	defer cancel()
	raw, err := t.runtime.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		log.Printf("diana sticker persona fit failed: %v", err)
		return false, "判断失败"
	}
	fit, reason := parseStickerPersonaVerdict(raw)
	if store != nil {
		saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancelSave()
		if err := store.SaveStickerPersonaFit(saveCtx, personaKey, candidate.Hash, fit, reason); err != nil {
			log.Printf("diana sticker persona fit save failed: %v", err)
		}
	}
	return fit, reason
}

// parseStickerPersonaVerdict 读模型的一行回答：以「会」开头算合适，其余（含「不会」和答非所问）都不发。
func parseStickerPersonaVerdict(raw string) (bool, string) {
	line := strings.TrimSpace(raw)
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	line = strings.Trim(line, "「」\"'*` ")
	if strings.HasPrefix(line, "会") {
		return true, ""
	}
	reason := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(line, "不会"), "：: ，,"))
	return false, truncateRunes(firstNonEmpty(reason, line), 80)
}
