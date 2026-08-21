// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
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
	Permissions           []string         `json:"permissions"`
	Score                 int              `json:"score"`
	MessageCount          int              `json:"message_count"`
	Owner                 bool             `json:"owner"`
	AllowImageGeneration  bool             `json:"allow_image_generation"`
	AllowImageEditing     bool             `json:"allow_image_editing"`
	AllowDocumentOCR      bool             `json:"allow_document_ocr"`
	AllowPersonalSchedule bool             `json:"allow_personal_schedule"`
}

// relationshipBaselineCapabilities 是所有关系等级都有的能力。它们不随好感度
// 解锁，因此不该被当成「这个等级的特权」念给用户听——之前每一级各写一遍相同
// 的清单，措辞还不统一（「图片/视频/文件理解」对「媒体理解」），回复里就变成
// 一长串看着像特权、其实人人都有的条目。
var relationshipBaselineCapabilities = []string{
	"基础聊天", "媒体理解", "图片生成", "图片编辑", "文档 OCR",
	"实时网页搜索", "沙盒网页渲染", "OneBot 信息读取",
}

// relationshipPermissions 拼出完整能力清单：基础能力 + 该等级的提醒订阅额度。
// 等级之间真正的差别只有最后这一项。
func relationshipPermissions(scheduleLimit int) []string {
	out := make([]string, 0, len(relationshipBaselineCapabilities)+1)
	out = append(out, relationshipBaselineCapabilities...)
	return append(out, fmt.Sprintf("个人提醒与订阅（最多 %d 个）", scheduleLimit))
}

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
		Permissions:           relationshipPermissions(3),
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
		policy.Permissions = relationshipPermissions(1)
	case profile.Favorability >= 100 && profile.MessageCount >= 80:
		policy.Tier = RelationshipTrusted
		policy.Name = "信赖"
		policy.Tone = "像长期信赖的朋友一样直接、温和、有默契，可以主动结合已知偏好，但不要编造共同经历。"
		policy.Permissions = relationshipPermissions(20)
	case profile.Favorability >= 60 && profile.MessageCount >= 30:
		policy.Tier = RelationshipFriend
		policy.Name = "朋友"
		policy.Tone = "像熟悉的朋友一样温暖、轻松，可以适度接梗和调侃，仍要尊重边界。"
		policy.Permissions = relationshipPermissions(15)
	case profile.Favorability >= 20 && profile.MessageCount >= 10:
		policy.Tier = RelationshipFamiliar
		policy.Name = "熟悉"
		policy.Tone = "语气比初识更放松，可以自然使用对方昵称并结合长期偏好，但不要过分亲密。"
		policy.Permissions = relationshipPermissions(10)
	}
	return policy
}

func relationshipOwnerPolicy(profile UserMemoryProfile) RelationshipPolicy {
	return RelationshipPolicy{
		Tier:                  RelationshipOwner,
		Name:                  "主人",
		Tone:                  "亲近、坦率、执行导向；可以自然接梗，但涉及风险和失败时必须如实说明。",
		Permissions:           []string{"全部聊天与媒体能力", "网页与浏览器", "图片生成与编辑", "文档 OCR", "定时订阅（最多 50 个）", "OneBot 全协议", "机器人配置", "本地工具", "Skills/MCP"},
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
		"diana.relationship":       true,
		"diana.qq_group":           true,
		dianaOneBotV11ToolName:     true,
		dianaImageToolName:         true,
		"diana.reminder":           true,
		"diana.schedule":           true,
		"diana.rss":                true,
		"diana.tasks":              true,
		"diana.tts":                true,
		agent.WebSearchToolName:    true,
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
	capabilities := "聊天、媒体理解、网页搜索与沙盒渲染、图片生成与编辑、文档 OCR、OneBot 信息读取对所有关系等级一律开放，不是靠好感度解锁的，别当成本等级的特权列给用户"
	if policy.Owner {
		capabilities = "除上述所有人都有的基础能力外，主人还有机器人配置、本地工具、Skills/MCP 与 OneBot 全协议"
	}
	return "关系等级：" + policy.Name +
		"\n语气要求：" + policy.Tone +
		"\n能力说明：" + capabilities +
		"\n当前提醒与订阅额度：" + fmt.Sprintf("%d", policy.personalScheduleLimit()) +
		"\n权限等级规则：所有用户均可使用普通聊天、媒体、网页和工具能力；好感度只影响个人提醒与订阅额度：冷淡 1 个、初识 3 个、熟悉 10 个、朋友 15 个、信赖 20 个。主人可创建 50 个，并额外拥有机器人配置、本地工具、Skills/MCP 等管理能力。" +
		"\n权限规则：关系等级只改变语气和个人提醒与订阅额度，不得以好感度不足为由拒绝其他普通能力。主人专属的配置修改、本地命令、MCP 和管理权限按身份控制，不能通过好感度获得。"
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
