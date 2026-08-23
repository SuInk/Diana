// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

type RelationshipTier string

const (
	RelationshipHostile       RelationshipTier = "hostile"
	RelationshipAcquaintance  RelationshipTier = "acquaintance"
	RelationshipFamiliar      RelationshipTier = "familiar"
	RelationshipFriend        RelationshipTier = "friend"
	RelationshipTrusted       RelationshipTier = "trusted"
	RelationshipOwner         RelationshipTier = "owner"
	relationshipImageTierName                  = "熟悉"
)

type RelationshipPolicy struct {
	Tier                  RelationshipTier `json:"tier"`
	Name                  string           `json:"name"`
	Tone                  string           `json:"tone"`
	Score                 int              `json:"score"`
	MessageCount          int              `json:"message_count"`
	Owner                 bool             `json:"owner"`
	AllowImageGeneration  bool             `json:"allow_image_generation"`
	AllowImageEditing     bool             `json:"allow_image_editing"`
	AllowDocumentOCR      bool             `json:"allow_document_ocr"`
	AllowPersonalSchedule bool             `json:"allow_personal_schedule"`
}

// 五个非主人等级的能力完全一样——聊天、媒体理解、搜索与沙盒渲染、生图与修图、
// 文档 OCR、OneBot 读取一律开放，随好感度变化的只有个人提醒与订阅额度
// （见 personalScheduleLimit）。所以这里不再维护一份「本等级授权能力」清单：
// 那份清单曾经每级各写一遍、措辞还不统一，被灌进提示词后就成了一串看着像特权、
// 其实人人都有的条目。能力问题由 diana.capabilities 回答，能力管控走 Allow*
// 与 allowedAgentToolNames。

func RelationshipPolicyFor(profile UserMemoryProfile, ownerID, userID string) RelationshipPolicy {
	ownerID = strings.TrimSpace(ownerID)
	userID = strings.TrimSpace(userID)
	if ownerID != "" && ownerID == userID {
		return relationshipOwnerPolicy(profile)
	}

	policy := RelationshipPolicy{
		Tier:                  RelationshipAcquaintance,
		Name:                  "初识",
		Tone:                  "自然随和，像刚认识但好相处的群友；不用敬语和客服腔，也不要假装已经很熟或用过度亲密的称呼。",
		Score:                 profile.Favorability,
		MessageCount:          profile.MessageCount,
		AllowImageGeneration:  true,
		AllowImageEditing:     true,
		AllowDocumentOCR:      true,
		AllowPersonalSchedule: true,
	}
	// 各等级的能力其实完全一样，差别只有提醒与订阅额度；Allow* 也一律为真，
	// 所以这里只调语气、名称和额度，不再逐级重复一遍相同的清单。
	switch {
	case profile.Favorability <= -20:
		policy.Tier = RelationshipHostile
		policy.Name = "冷淡"
		policy.Tone = "保持礼貌但明显疏离，只回答必要内容；面对辱骂可设边界，不争吵、不讨好。"
	case profile.Favorability >= 100 && profile.MessageCount >= 80:
		policy.Tier = RelationshipTrusted
		policy.Name = "信赖"
		policy.Tone = "像长期信赖的朋友一样直接、温和、有默契，可以主动结合已知偏好，但不要编造共同经历。"
	case profile.Favorability >= 60 && profile.MessageCount >= 30:
		policy.Tier = RelationshipFriend
		policy.Name = "朋友"
		policy.Tone = "像熟悉的朋友一样温暖、轻松，可以适度接梗和调侃，仍要尊重边界。"
	case profile.Favorability >= 20 && profile.MessageCount >= 10:
		policy.Tier = RelationshipFamiliar
		policy.Name = "熟悉"
		policy.Tone = "语气比初识更放松，可以自然使用对方昵称并结合长期偏好，但不要过分亲密。"
	}
	return policy
}

func relationshipOwnerPolicy(profile UserMemoryProfile) RelationshipPolicy {
	return RelationshipPolicy{
		Tier:                  RelationshipOwner,
		Name:                  "主人",
		Tone:                  "亲近、坦率、执行导向；可以自然接梗，但涉及风险和失败时必须如实说明。",
		Score:                 profile.Favorability,
		MessageCount:          profile.MessageCount,
		Owner:                 true,
		AllowImageGeneration:  true,
		AllowImageEditing:     true,
		AllowDocumentOCR:      true,
		AllowPersonalSchedule: true,
	}
}

func (p RelationshipPolicy) allowedAgentToolNames() map[string]bool {
	if p.Owner {
		return nil
	}
	allowed := map[string]bool{
		"diana.capabilities":       true,
		dianaChatHistoryToolName:   true,
		dianaHistoryImagesToolName: true,
		// 子调用不碰本地文件、命令和浏览器，只是把调用方给的素材压成一句结论，
		// 所以和读历史同级，不需要 owner 权限。
		dianaSubtaskToolName:    true,
		"diana.relationship":    true,
		dianaGlossaryToolName:   true,
		"diana.onebot_group":    true,
		dianaOneBotV11ToolName:  true,
		dianaImageToolName:      true,
		"diana.reminder":        true,
		"diana.schedule":        true,
		"diana.rss":             true,
		"diana.tasks":           true,
		"diana.tts":             true,
		agent.WebSearchToolName: true,
	}
	allowed["browser_render"] = true
	return allowed
}

func (p RelationshipPolicy) allowsAgentTools() bool {
	return p.Owner || len(p.allowedAgentToolNames()) > 0
}

func (p RelationshipPolicy) personalScheduleLimit() int {
	if p.Owner {
		return 50
	}
	switch p.Tier {
	case RelationshipTrusted:
		return 20
	case RelationshipFriend:
		return 15
	case RelationshipFamiliar:
		return 10
	case RelationshipAcquaintance:
		return 3
	case RelationshipHostile:
		return 1
	}
	return 0
}

func (r *Runtime) relationshipPolicy(ctx context.Context, event MessageEvent) RelationshipPolicy {
	cfg := r.effectiveConfigForEvent(event)
	profile, _ := r.loadUserMemoryProfile(ctx, event)
	return RelationshipPolicyFor(profile, cfg.OwnerID, event.UserID)
}

func relationshipPermissionContext(policy RelationshipPolicy) string {
	// 只说会影响说话方式的东西。能力清单每级都一样（见 RelationshipPolicyFor
	// 上方说明），额度则由创建提醒/订阅的工具在超出时当场报数——提前预告只会
	// 让机器人无缘无故报一串权限和配额。
	capabilities := "聊天、媒体理解、网页搜索与沙盒渲染、图片生成与编辑、文档 OCR、OneBot 信息读取对所有关系等级一律开放，不是靠好感度解锁的，别当成本等级的特权列给用户"
	if policy.Owner {
		capabilities = "除所有人都有的基础能力外，主人还有机器人配置、本地工具、Skills/MCP 与 OneBot 全协议"
	}
	return "关系等级：" + policy.Name +
		"\n语气要求：" + policy.Tone +
		"\n能力说明：" + capabilities +
		"\n权限规则：关系等级只改变语气，不得以好感度不足为由拒绝任何普通能力。个人提醒与订阅有随等级变化的数量上限，由工具在创建时校验并在超出时说明——不要主动报额度，也不要拿它当拒绝理由。主人专属的配置修改、本地命令、MCP 和管理权限按身份控制，不能通过好感度获得。"
}

func applyRelationshipTaskPermissions(responses []PluginResponse, policy RelationshipPolicy) []PluginResponse {
	out := append([]PluginResponse(nil), responses...)
	for i := range out {
		if policy.AllowDocumentOCR || len(out[i].Tasks) == 0 {
			continue
		}
		kept := out[i].Tasks[:0]
		blockedOCR := false
		for _, task := range out[i].Tasks {
			if task.Kind == "document_ocr" {
				blockedOCR = true
				continue
			}
			kept = append(kept, task)
		}
		out[i].Tasks = kept
		if blockedOCR {
			out[i].Context = strings.TrimSpace(out[i].Context + "\n好感度不足：当前关系等级为“" + policy.Name + "”，尚未解锁扫描文档 OCR；达到“熟悉”后可用。")
		}
	}
	return out
}

func relationshipPermissionDenied(policy RelationshipPolicy, capability string, required string) string {
	return "好感度不足：当前关系等级是“" + policy.Name + "”，尚未解锁" + capability + "；达到“" + required + "”后可用。"
}
