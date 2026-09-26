// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const (
	dianaImageSourceModeCombine = "combine"
	dianaImageSourceModeEach    = "each"
	// dianaImageMaxEachSources 限制逐张模式的扇出：每一张都要单独打一次图片接口，
	// 数量不设上限的话一条消息就能把配额和后台队列吃干净。
	dianaImageMaxEachSources = 6
)

// errImageEditSourceNotFound 在受理时返回给模型，模型据此让用户补图，
// 不会先答应「在画了」。
var errImageEditSourceNotFound = errors.New("没有找到可编辑的图片：当前消息、引用链和上一次改图任务里都没有图。这次没有开始画，不要对用户说「在画了」；要改的图在聊天记录或「稍早发的图」里的话，把它的 message_id 填进 source_message_ids 重新调用，否则请让用户重新发送图片，或直接引用那张图片再说要怎么改")

// imageEditSourceMissingInstruction 是意图路由判定要改图、却找不到原图时给正文
// 生成的提示。
const imageEditSourceMissingInstruction = "【本轮图片任务】用户想改图，但当前消息、引用链、最近聊天记录和上一次改图任务里都没有找到原图，这次没有开始画。不要说「在画了」「马上发出来」，也不要假装已经受理；用一句话请用户重新发送图片，或直接引用那张图片再说要怎么改。"

const (
	dianaImageToolName       = "image"
	dianaImageMediaTTL       = 10 * time.Minute
	dianaImageMaxDecodedSize = 32 << 20
	dianaImageTimeoutGrace   = 30 * time.Second
)

type dianaImageTool struct {
	runtime      *Runtime
	event        MessageEvent
	relationship RelationshipPolicy
}

type dianaImageToolResult struct {
	OK      bool   `json:"ok"`
	Queued  bool   `json:"queued"`
	TaskID  string `json:"task_id"`
	Action  string `json:"action"`
	Caption string `json:"caption,omitempty"`
	Reused  bool   `json:"reused,omitempty"`
	// Announced 表示运行时已经替你把「开始处理」发给用户了，final 回复不用再重复
	// 一遍「正在处理」。
	Announced bool `json:"announced,omitempty"`
	// WorkspacePathPrefix 是成品原图在工作目录里的落点（不含扩展名）：图画完后存成
	// <前缀>.<按格式定的扩展名>，多张时是 <前缀>-2.… 依次往后。只在主人开了文件写入时有。
	WorkspacePathPrefix string `json:"workspace_path_prefix,omitempty"`
	WorkspaceNote       string `json:"workspace_note,omitempty"`
	// SourcesUsed 是这次真正交给图片模型的原图来历。模型回复里说用了谁的头像、
	// 哪条消息的图，要以它为准。
	SourcesUsed []imageEditSourceUsed `json:"sources_used,omitempty"`
	SourcesNote string                `json:"sources_note,omitempty"`
	// QuotaExceeded 表示今天的生图次数用完了，这次没有受理；Notice 是要照实转告
	// 用户的说明。
	QuotaExceeded bool   `json:"quota_exceeded,omitempty"`
	Notice        string `json:"notice,omitempty"`
}

// imageSourcesUsedNote 跟着 sources_used 一起给模型：光给一张来历表，模型未必会拿它
// 去对自己的说法。
const imageSourcesUsedNote = "sources_used 就是这次真正交给图片模型的原图；回复里提到用了谁的头像、哪条消息的图，只能照这里说。和用户要的不一致就直接说明并重新调用。候选图不止一张、用户又没说清是哪张时，先问清楚，或在回复里说明用的是哪张。"

type dianaImageToolRequest struct {
	Operation       string
	Prompt          string
	Caption         string
	IdentitySources []string
	// DefaultIdentitySources 是模型没点名时的兜底头像（被 @ 的成员），属于隐式来源，
	// 排在当前消息和引用消息的图之后。
	DefaultIdentitySources []string
	// AllowRecentSenderImages 见 imageEditSourcePlan。
	AllowRecentSenderImages bool
	// SourcesUsed 与 Sources 一起在受理时解析出来。
	SourcesUsed []imageEditSourceUsed
	// SourceLabels 与 IdentitySources 一一对应的人类可读标注（昵称等）。
	// 逐张发送时作为对应图片的说明文字,让大家知道每张是谁的。
	SourceLabels []string
	// SourceMode 决定多张参考图怎么用：combine 把它们合成一张（默认，也是历史行为），
	// each 对每张各做一次编辑，最后一起发出。
	SourceMode string
	// SourceMessageIDs 是模型指认的原图所在消息。指代由模型自己判断（它看得到
	// 聊天记录里每条媒体的 message_id），运行时只负责把这些消息里的图取出来。
	SourceMessageIDs []string
	// Sources 是受理时就解析好的原图。在受理时解析，找不到图能当场告诉模型，
	// 而不是先回一句「在画了」，过一会儿再发一条失败通知。
	Sources []string
	// WorkspaceStem 是成品在工作目录 outputs/ 里的文件名前缀，受理时定下来，
	// 这样受理结果里就能告诉模型图会存到哪。为空表示不落盘。
	WorkspaceStem string
	// quota 是受理时占下的每日生图次数，任务结束时按实际成功张数结清。
	quota *mediaGenerationReservation
}

type dianaImageTaskOutput struct {
	Caption   string
	ImageURLs []string
	Models    []GeneratedImageModel
	// Delivered 表示图片已在执行过程中逐张发出,Caption 只剩失败/超限说明
	//（可能为空）,调用方不要再做一次汇总投递。
	Delivered bool
}

func newDianaImageTool(runtime *Runtime, event MessageEvent, relationship RelationshipPolicy) agent.Tool {
	return &dianaImageTool{runtime: runtime, event: event, relationship: relationship}
}

func (t *dianaImageTool) Name() string {
	return dianaImageToolName
}

func (t *dianaImageTool) Description() string {
	operations := make([]string, 0, 2)
	if t.relationship.AllowImageGeneration {
		operations = append(operations, `"generate"（根据完整文字 prompt 生成新图片）`)
	}
	if t.relationship.AllowImageEditing {
		operations = append(operations, `"edit"（编辑当前、引用或指定消息里的图片/成员头像）`)
	}
	if len(operations) == 0 {
		operations = append(operations, "无")
	}
	return `异步生成或编辑图片。工具受理后由运行时替你告诉用户「开始处理」，图片在后台完成后自动发送。调用后直接继续输出 final 文字回复即可，不要等待图片，不要再次调用本工具，也不要重复说一遍「正在处理」。当前允许操作：` + strings.Join(operations, "、") + `。要对多张参考图逐张各出一张，用 source_mode="each"。如果用户要求先搜索、核验网页或读取外部资料再出图，必须先完成搜索或浏览器调用，prompt 里只能写已确认的事实，不能虚构没查到的内容。结果里的 sources_used 是这次实际用到的原图，回复里说用了什么只能照它说。结果里 quota_exceeded 为 true 表示今天的生图次数用完了，照 notice 如实告诉用户，不要换别的途径出图。`
}

// imageAnnouncementSubjectMaxRunes 是开场白里能带上的画面描述长度上限。
//
// prompt 交给图片模型时是一段完整、自包含的提示词，经常是几百字的英文加风格堆砌；
// 原样念给群里比不念更糟。短到像一句话的才当画面描述用，长的宁可只报动作。
const imageAnnouncementSubjectMaxRunes = 40

// imageAnnouncementSubject 从提示词里取一句能直接念出来的画面描述，取不到就返回空。
func imageAnnouncementSubject(prompt string) string {
	subject := strings.TrimSpace(prompt)
	if index := strings.IndexAny(subject, "\r\n"); index >= 0 {
		subject = strings.TrimSpace(subject[:index])
	}
	if subject == "" || len([]rune(subject)) > imageAnnouncementSubjectMaxRunes {
		return ""
	}
	return subject
}

// dianaImageStartedMessage 是任务受理后立刻发给用户的那句话。
//
// 只报「开始生成图片」不够：用户要等到图发出来才知道理解对没对。能把画面描述念出来
// 就带上，画歪了对方当场就能喊停。
func dianaImageStartedMessage(request dianaImageToolRequest, result dianaImageToolResult) string {
	if !result.OK || result.TaskID == "" {
		return ""
	}
	action := "生成图片"
	if request.Operation == "edit" {
		action = "编辑图片"
		// 参考图要等任务真正跑起来才解析得出来，这里说不出确切张数，就不说，
		// 免得开场白报一个数、结果发另一个数。确切张数由结果说明给出。
		if request.SourceMode == dianaImageSourceModeEach {
			action = "逐张编辑图片"
		}
	}
	subject := imageAnnouncementSubject(request.Prompt)
	if result.Reused {
		if subject != "" {
			return fmt.Sprintf("同样的%s任务已经在处理中（%s），完成后我会把结果发出来。", action, subject)
		}
		return fmt.Sprintf("同样的%s任务已经在处理中，完成后我会把结果发出来。", action)
	}
	if subject != "" {
		return fmt.Sprintf("开始%s：%s，完成后我会把结果发出来。", action, subject)
	}
	return fmt.Sprintf("开始%s，完成后我会把结果发出来。", action)
}

// InputSchema 的 operation 枚举按当前可用能力裁剪：不可用的操作压根不出现在
// 参数里，比在描述里说明「你没有权限」更省事，模型也不会去试。
func (t *dianaImageTool) InputSchema() map[string]any {
	operations := make([]string, 0, 2)
	if t.relationship.AllowImageGeneration {
		operations = append(operations, "generate")
	}
	if t.relationship.AllowImageEditing {
		operations = append(operations, "edit")
	}
	properties := map[string]any{
		"operation": toolEnumParam("generate 凭文字生成新图片，不读任何原图；edit 以图片或头像为底修改。填了 identity_sources 或 source_message_ids 就是 edit；省略时有原图来源按 edit 处理，否则按 generate。", operations...),
		"prompt":    toolStringParam("交给图片模型的完整、自包含的最终提示词。不要写成对话口吻，也不要依赖上下文里的指代。"),
		"caption":   toolStringParam("图片完成后随图发送的一句短文字，可选。"),
	}
	// 要编辑谁的头像由你来判断：运行时不去读用户的措辞，只负责把你点名的 id 换成
	// 头像地址，并核对这个人在当前会话里确实存在。
	if t.relationship.AllowImageEditing {
		properties["identity_sources"] = toolStringArrayParam(
			`用户说到某个人的头像（包括「把 XX 的头像改成……」「照着 XX 头像画」）时，在这里点名头像来源；当前消息或引用消息带着别的图（例如一张表情）也照样要填，点名的来源优先，不会被当前消息里的图顶掉。` +
				`可选值："` + avatarSourceSender + `"（本条消息的发送者）、"` + avatarSourceBot + `"（机器人自己）、"` +
				avatarSourceGroup + `"（本群的群头像）、"` + avatarSourceGroupPrefix + `<group_id>"（私聊里用户明确给出群号时的群头像）、"` +
				avatarSourceMemberPrefix + `<user_id>"（指定成员，user_id 使用当前平台的账号标识或其脱敏别名）。` +
				`用户按名字或昵称指人时，先从上下文或群成员工具里查出对应 user_id 再填，不要编造；最多 ` +
				strconv.Itoa(maxAvatarImageSources) + ` 个。`)
	}
	if t.relationship.AllowImageEditing {
		properties["source_message_ids"] = toolStringArrayParam(
			`要改的图在哪几条消息里：填聊天记录、媒体索引或「稍早发的图」里的 message_id，可以多条，每条消息里的所有图片都会作为原图。` +
				`用户指的是某条具体消息里的图（「这张」「刚才那几张」「他刚发的图」「重试」「继续改」）时就填；同一个人稍早发的候选图不会自动当原图，要用就在这里点名。` +
				`重试或继续改上一张时填最初那张原图（或上一次生成结果）所在的消息。填了它和 identity_sources 就只用这些来源；两个都不填才按当前消息、引用消息里的图去找。最多 ` +
				strconv.Itoa(maxImageEditSourceMessages) + ` 条。`)
		properties["source_labels"] = toolStringArrayParam(
			`与 identity_sources 一一对应的说明文字，可选，逐张发送时原样作为对应图片附带的说明发出（例如「Winter 的头像」），让大家知道每张是谁的。` +
				`填写时数量必须与 identity_sources 相同。`)
		properties["source_mode"] = toolEnumParam(
			`operation="edit" 时多张参考图怎么用。combine：把它们合成为一张（默认）。`+
				`each：对每张各做一次编辑，产出多张图一起发出——用户要求「每个人的头像都处理一下」`+
				`「挨个改」这类逐张产出时用它。`,
			"combine", "each")
	}
	return toolObjectSchema([]string{"prompt"}, properties)
}

func (t *dianaImageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("图片工具未配置")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request, err := t.prepareRequest(input)
	if err != nil {
		return "", err
	}
	result, err := t.enqueue(ctx, request)
	var quotaErr *mediaGenerationQuotaError
	if errors.As(err, &quotaErr) {
		// 超限是明确的业务结果，不是工具故障：给模型一句能照念的话，别让它当成
		// 偶发错误去重试，或者换条路子接着画。
		body, marshalErr := json.Marshal(dianaImageToolResult{
			Action: request.Operation, QuotaExceeded: true,
			Notice: imageQuotaExceededInstruction(quotaErr, t.runtime.effectiveConfigForEvent(t.event)),
		})
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(body), nil
	}
	if err != nil {
		return "", err
	}
	// 开场白不留给模型自由发挥：它经常改口成「没办法直接修改」「你没有这个权限」，
	// 用户那边就只剩一句推脱，而任务其实已经在后台跑了。
	//
	// 但也不能当场就发：模型随后的 final 回复照样会说一遍图片的事，用户连着收到
	// 两条几乎一样的话。所以先交给本轮回复攒着——模型自己说了就用模型那句，模型
	// 什么都没说才把开场白作为这一轮的回复发出去（见 drainPendingImageAnnouncement）。
	// 不在回复轮次里（后台任务直接调工具）没有 final 可兜底，维持当场发送。
	if announcement := dianaImageStartedMessage(request, result); announcement != "" {
		if sink := imageAnnouncementSinkFrom(ctx); sink != nil {
			sink.offer(announcement)
		} else if sendErr := t.runtime.send(ctx, t.event, announcement); sendErr != nil {
			// 开场白发不出去不影响任务本身，只记一笔。
			log.Printf("diana image task announcement failed: %v", sendErr)
		} else {
			result.Announced = true
		}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (t *dianaImageTool) prepareRequest(input map[string]any) (dianaImageToolRequest, error) {
	operation := strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
	prompt := strings.TrimSpace(configToolString(input, "prompt"))
	if prompt == "" {
		return dianaImageToolRequest{}, fmt.Errorf("prompt 不能为空")
	}
	if len([]rune(prompt)) > 12000 {
		return dianaImageToolRequest{}, fmt.Errorf("prompt 过长，请压缩到 12000 字以内")
	}
	identitySources := configToolStringSlice(input, "identity_sources")
	sourceMessageIDs := configToolStringSlice(input, "source_message_ids")
	explicitSources := len(identitySources) > 0 || len(sourceMessageIDs) > 0
	if operation == "" {
		// 点名了原图来源、或者用户就是拿着图在说（当前消息或引用消息带图），却没填
		// operation：以前一律按 generate，来源被整个忽略，出来一张纯文生图，模型还
		// 以为用的是那张头像。有来源就是改图。
		implicitSources := len(availableImageURLs(t.event.Segments)) > 0 ||
			(t.event.Quoted != nil && len(availableImageURLs(t.event.Quoted.Segments)) > 0)
		switch {
		case (explicitSources || implicitSources) && t.relationship.AllowImageEditing:
			operation = "edit"
		case t.relationship.AllowImageGeneration:
			operation = "generate"
		case t.relationship.AllowImageEditing:
			operation = "edit"
		default:
			return dianaImageToolRequest{}, fmt.Errorf("operation 必须是 generate 或 edit")
		}
	}
	switch operation {
	case "generate":
		if explicitSources {
			// 生成不读任何原图。静默忽略的话，模型会以为头像已经用上了。
			return dianaImageToolRequest{}, fmt.Errorf("operation=\"generate\" 只凭文字生成新图，会忽略 identity_sources 和 source_message_ids。要以头像或某条消息里的图为底，请改用 operation=\"edit\" 重新调用；确实只想凭文字生成，就去掉这两个参数")
		}
	case "edit":
	default:
		return dianaImageToolRequest{}, fmt.Errorf("operation 必须是 generate 或 edit")
	}
	if t.runtime.llmStore == nil {
		return dianaImageToolRequest{}, fmt.Errorf("diana: llm profile store is not configured")
	}
	caption := strings.TrimSpace(configToolString(input, "caption"))
	if len([]rune(caption)) > 200 {
		caption = string([]rune(caption)[:200])
	}
	if caption == "" {
		if operation == "edit" {
			caption = "图片编辑完成。"
		} else {
			caption = "图片生成完成。"
		}
	}
	if len(sourceMessageIDs) > maxImageEditSourceMessages {
		return dianaImageToolRequest{}, fmt.Errorf("source_message_ids 最多 %d 条", maxImageEditSourceMessages)
	}
	var defaultIdentitySources []string
	if operation == "edit" && !explicitSources {
		defaultIdentitySources = defaultAvatarIdentitySources(t.event, t.runtime.effectiveConfigForEvent(t.event).BotAccount)
	}
	sourceMode := strings.ToLower(strings.TrimSpace(configToolString(input, "source_mode")))
	if sourceMode != dianaImageSourceModeEach {
		sourceMode = dianaImageSourceModeCombine
	}
	sourceLabels := configToolStringSlice(input, "source_labels")
	if len(sourceLabels) != len(identitySources) {
		// 数量对不上就整组丢弃:错位的标注比没有标注更糟(把 A 的头像标成 B)。
		sourceLabels = nil
	}
	return dianaImageToolRequest{
		Operation: operation, Prompt: prompt, Caption: caption,
		IdentitySources: identitySources, DefaultIdentitySources: defaultIdentitySources,
		SourceLabels: sourceLabels, SourceMode: sourceMode,
		SourceMessageIDs: sourceMessageIDs,
	}, nil
}

func (t *dianaImageTool) sourcePlan(request dianaImageToolRequest) imageEditSourcePlan {
	return imageEditSourcePlan{
		IdentitySources:         request.IdentitySources,
		SourceMessageIDs:        request.SourceMessageIDs,
		DefaultIdentitySources:  request.DefaultIdentitySources,
		AllowRecentSenderImages: request.AllowRecentSenderImages,
	}
}

func (t *dianaImageTool) enqueue(ctx context.Context, request dianaImageToolRequest) (dianaImageToolResult, error) {
	name := "图片生成"
	if request.Operation == "edit" {
		name = "图片编辑"
		if len(request.Sources) == 0 {
			// 显式来源（source_message_ids、identity_sources）优先，「按某人头像的样子改
			// 这张图」时点名的头像跟在原图后面一起交给图片模型。都没点名才按当前消息、
			// 引用消息去找，见 image_edit_source_plan.go。
			sources, used, err := t.runtime.resolveImageEditSources(ctx, t.event, t.sourcePlan(request))
			if err != nil {
				return dianaImageToolResult{}, err
			}
			request.Sources, request.SourcesUsed = sources, used
		}
		if len(request.Sources) == 0 {
			return dianaImageToolResult{}, errImageEditSourceNotFound
		}
		t.runtime.imageEditSources.remember(sessionKey(t.event), request.Sources, time.Now())
	}
	// 图片工具是通用工具，不为某种任务单独适配。以前这里会扫 prompt 里有没有
	// 「五子棋 / 棋盘」，命中就把图片绑到共享棋局状态的版本上，防旧图盖掉新落子。
	// 那是拿关键词猜语义，而且只认五子棋；要防「图片发出来时局面已经变了」，该由
	// 模型在拿到图之后自己核对状态再决定发不发，不该由通用工具替某个游戏兜底。
	// 放在找原图之后：原图都没有的话，该让用户补图，而不是先占掉一次次数。
	amount := 1
	if request.Operation == "edit" && request.SourceMode == dianaImageSourceModeEach && len(request.Sources) > 1 {
		amount = min(len(request.Sources), dianaImageMaxEachSources)
	}
	quota, err := t.runtime.reserveMediaGeneration(ctx, t.event, MediaGenerationImage, amount)
	if err != nil {
		return dianaImageToolResult{}, err
	}
	request.quota = quota
	taskKey := dianaImageTaskKey(t.event, request)
	if t.persistsToWorkspace() {
		request.WorkspaceStem = dianaImageWorkspaceStem(taskKey, time.Now())
	}
	task := PluginTask{
		Kind:    "image",
		Name:    name,
		Key:     taskKey,
		Timeout: t.taskTimeout(),
		Run: func(ctx context.Context, services PluginTaskServices) (PluginTaskResult, error) {
			output, err := t.execute(ctx, request, services)
			if err != nil {
				return PluginTaskResult{}, err
			}
			// 逐张模式下图片已经边完成边发出去了,这里只补失败/超限说明,
			// 全部成功时安静收尾,不再来一条汇总。
			if output.Delivered {
				result := PluginTaskResult{Delivered: true}
				if note := strings.TrimSpace(output.Caption); note != "" {
					result.Reply = note
				}
				return result, nil
			}
			message := OutgoingMessage{Text: output.Caption, ImageURLs: output.ImageURLs, GeneratedImageModels: output.Models}
			if t.event.Kind == EventKindGroup {
				message.ReplyMessageID = t.event.MessageID
			}
			return PluginTaskResult{Messages: []OutgoingMessage{message}}, nil
		},
	}
	reservation := t.runtime.reservePluginTasksForTurn(ctx, t.event, []PluginTask{task})
	if !reservation.handled {
		quota.release()
		return dianaImageToolResult{}, fmt.Errorf("图片任务无法启动")
	}
	result := dianaImageToolResult{OK: true, Queued: true, Action: request.Operation, Caption: request.Caption}
	if len(request.SourcesUsed) > 0 {
		result.SourcesUsed = request.SourcesUsed
		result.SourcesNote = imageSourcesUsedNote
	}
	if len(reservation.reserved) > 0 {
		result.TaskID = reservation.reserved[0].id
		if request.WorkspaceStem != "" {
			result.WorkspacePathPrefix = agent.WorkspaceOutputsDir + "/" + request.WorkspaceStem
			result.WorkspaceNote = "图画完后原图会存进工作目录，文件名以 workspace_path_prefix 开头、扩展名按实际格式定；这时还没画完，文件还不存在。之后要再发或整理，先用 find_files 按这个前缀找到确切路径。"
		}
		if sink := imageAnnouncementSinkFrom(ctx); sink != nil {
			sink.deferTask(
				func() { t.runtime.startPluginTaskReservation(reservation) },
				func() {
					t.runtime.cancelPluginTaskReservation(reservation)
					quota.release()
				},
			)
		} else {
			t.runtime.startPluginTaskReservation(reservation)
		}
		return result, nil
	}
	// 复用已有任务不另算一次：那张图记在最初受理的那笔上。
	quota.release()
	if len(reservation.duplicates) > 0 {
		result.TaskID = reservation.duplicates[0].ID
		result.Reused = true
		return result, nil
	}
	return dianaImageToolResult{}, fmt.Errorf("图片任务无法启动")
}

func (t *dianaImageTool) taskTimeout() time.Duration {
	configs := t.runtime.imageProviderConfigs(withModelConfigEvent(context.Background(), t.event))
	if len(configs) == 0 {
		return defaultSubagentTaskTimeout
	}
	cfg := configs[0]
	timeout := cfg.ImageTimeout + dianaImageTimeoutGrace
	if timeout <= dianaImageTimeoutGrace {
		return defaultSubagentTaskTimeout
	}
	return timeout
}

func (t *dianaImageTool) execute(ctx context.Context, request dianaImageToolRequest, services PluginTaskServices) (dianaImageTaskOutput, error) {
	// 生图在后台任务里跑，拿到的 ctx 不带消息事件；不挂上的话用量记不到这条消息名下。
	if llmUsageFromContext(ctx) == nil {
		ctx = withLLMUsageContext(ctx, t.event)
	}
	progress := services.Report
	operation := request.Operation
	prompt := request.Prompt
	submittedPrompt := t.runtime.enrichImagePromptWithChatContext(ctx, t.event, prompt)
	var (
		cfg         llm.ProviderConfig
		images      []string
		models      []GeneratedImageModel
		sourceCount int
		action      string
		message     string
		// dropped 是超出逐张上限被丢掉的参考图数量，failed 是逐张模式里失败的张数。
		// 两者都要如实告诉用户，静默少发几张比报错更难查。
		dropped int
		failed  int
		// produced 是这次拿到的全部成品，逐张模式里已经发出去的也在内，落盘用。
		produced []string
		// generated 是图片接口成功返回的次数，streamed 是逐张模式里已经发出去的张数。
		// 任务成功按 generated 结清每日次数；中途失败时只有已经发到用户手里的才算，
		// 没发出去的图对用户来说就是没画成。
		generated int
		streamed  int
		succeeded bool
	)
	defer func() {
		if succeeded {
			request.quota.commit(ctx, generated)
			return
		}
		request.quota.commit(ctx, streamed)
	}()
	// 成品原图同时存进工作目录，模型之后才能用 send_attachment 再发、用 manage_files
	// 整理。以前图只进媒体缓存，主人说「存下来」时模型手里没有任何能存的东西。
	defer func() { t.runtime.persistGeneratedImages(ctx, t.event, request.WorkspaceStem, produced) }()
	switch operation {
	case "generate":
		resp, usedCfg, err := t.runtime.generateImageWithFailover(ctx, llm.ImageGenerateRequest{
			Prompt: submittedPrompt,
			Size:   "1024x1024",
			N:      1,
		})
		if err != nil {
			return dianaImageTaskOutput{}, err
		}
		if len(resp.Images) > 0 {
			generated++
		}
		cfg = usedCfg
		images = resp.Images
		produced = append(produced, resp.Images...)
		models = generatedImageModels(usedCfg, operation, len(resp.Images), resp)
		action = "image_generate"
		message = "Agent 图片生成已完成"
	case "edit":
		sources := append([]string(nil), request.Sources...)
		if len(sources) == 0 {
			sources, _, _ = t.runtime.resolveImageEditSources(ctx, t.event, t.sourcePlan(request))
		}
		if len(sources) == 0 {
			return dianaImageTaskOutput{}, errImageEditSourceNotFound
		}
		// combine 把所有参考图交给一次编辑（合成一张）；each 对每张各编辑一次，
		// 产出多张。以前只有前者，「把每个人的头像都改一下」这类请求做不出来。
		batches := [][]string{sources}
		if request.SourceMode == dianaImageSourceModeEach && len(sources) > 1 {
			if len(sources) > dianaImageMaxEachSources {
				dropped = len(sources) - dianaImageMaxEachSources
				sources = sources[:dianaImageMaxEachSources]
			}
			batches = make([][]string, 0, len(sources))
			for _, source := range sources {
				batches = append(batches, []string{source})
			}
		}
		// 逐张模式且能中途投递时,完成一张立刻发一张:用户马上看到成果,
		// 也不再需要「正在编辑第 N/M 张」这种带内部味道的进度播报——
		// 图片本身就是进度。发不出去再退回攒总。
		streaming := request.SourceMode == dianaImageSourceModeEach && len(batches) > 1 && services.Send != nil
		// 标注按「来源→解析出的头像地址」建映射:来源解析是保序但有损的
		//（解析失败会整个跳过）,按下标硬对会把 A 的头像标成 B。查不到就
		// 不标,宁缺毋错。
		labelByURL := map[string]string{}
		if streaming && len(request.SourceLabels) == len(request.IdentitySources) {
			for labelIndex, identity := range request.IdentitySources {
				label := strings.TrimSpace(request.SourceLabels[labelIndex])
				if label == "" {
					continue
				}
				for _, resolved := range t.runtime.avatarIdentityImageURLs(ctx, t.event, []string{identity}) {
					if _, exists := labelByURL[resolved]; !exists {
						labelByURL[resolved] = label
					}
				}
			}
		}
		for index, batch := range batches {
			if err := ctx.Err(); err != nil {
				return dianaImageTaskOutput{}, err
			}
			if progress != nil && len(batches) > 1 {
				// 只更新后台任务状态,不发聊天消息(Message 留空)。
				progress(PluginTaskProgress{Phase: "running", Completed: index, Total: len(batches)})
			}
			resp, usedCfg, err := t.runtime.editImageWithFailover(ctx, llm.ImageEditRequest{
				Prompt: submittedPrompt,
				Images: batch,
				Size:   "1024x1024",
				N:      1,
			})
			if err != nil {
				// 逐张模式里一张失败不该埋掉已经成功的那些。
				if len(batches) == 1 || (len(images) == 0 && streamed == 0) {
					return dianaImageTaskOutput{}, err
				}
				failed++
				continue
			}
			if len(resp.Images) > 0 {
				generated++
			}
			cfg = usedCfg
			sourceCount += len(batch)
			produced = append(produced, resp.Images...)
			if streaming {
				shared, localPaths, shareErr := t.runtime.shareAgentImages(ctx, t.event.Platform, resp.Images)
				if shareErr == nil && len(shared) > 0 {
					if len(localPaths) > 0 {
						cleanupLocalMediaFilesLater(localPaths, dianaImageMediaTTL)
					}
					outgoing := OutgoingMessage{ImageURLs: shared, GeneratedImageModels: generatedImageModels(usedCfg, operation, len(shared), resp)}
					// 有标注就每张带上「这是谁的」;没有标注时第一张带整体说明。
					if label := labelByURL[batch[0]]; label != "" {
						outgoing.Text = label
					} else if streamed == 0 {
						outgoing.Text = request.Caption
					}
					if streamed == 0 && t.event.Kind == EventKindGroup {
						// 第一张引用原消息,后面的直接发图。
						outgoing.ReplyMessageID = t.event.MessageID
					}
					if sendErr := services.Send(ctx, outgoing); sendErr == nil {
						streamed++
						continue
					}
				}
				// 分享或发送失败就把这张并回攒总,任务结束时统一投递,不丢图。
			}
			images = append(images, resp.Images...)
			models = append(models, generatedImageModels(usedCfg, operation, len(resp.Images), resp)...)
		}
		if streamed > 0 && len(images) == 0 {
			t.runtime.recordImageOperation(ctx, t.event, "image_edit", "Agent 图片编辑已完成", prompt, submittedPrompt, cfg.ImageModelWithDefault(), streamed, sourceCount)
			note := ""
			if failed > 0 || dropped > 0 {
				note = dianaImageResultCaption("", 0, dropped, failed)
			}
			succeeded = true
			return dianaImageTaskOutput{Delivered: true, Caption: note}, nil
		}
		action = "image_edit"
		message = "Agent 图片编辑已完成"
	}
	if len(images) == 0 {
		return dianaImageTaskOutput{}, fmt.Errorf("图片接口没有返回图片")
	}

	sharedImages, localPaths, err := t.runtime.shareAgentImages(ctx, t.event.Platform, images)
	if err != nil {
		return dianaImageTaskOutput{}, err
	}
	if len(localPaths) > 0 {
		cleanupLocalMediaFilesLater(localPaths, dianaImageMediaTTL)
	}
	t.runtime.recordImageOperation(ctx, t.event, action, message, prompt, submittedPrompt, cfg.ImageModelWithDefault(), len(images), sourceCount)
	succeeded = true
	return dianaImageTaskOutput{
		Caption:   dianaImageResultCaption(request.Caption, len(sharedImages), dropped, failed),
		ImageURLs: sharedImages,
		Models:    models,
	}, nil
}

// dianaImageResultCaption 在结果说明里如实带上少发的张数。逐张模式下超出上限或
// 中途失败时静默少发几张，用户看不出差别，也没法判断要不要重试。
func dianaImageResultCaption(caption string, delivered, dropped, failed int) string {
	notes := make([]string, 0, 2)
	if delivered > 1 {
		notes = append(notes, fmt.Sprintf("共 %d 张", delivered))
	}
	if failed > 0 {
		notes = append(notes, fmt.Sprintf("%d 张处理失败", failed))
	}
	if dropped > 0 {
		notes = append(notes, fmt.Sprintf("另有 %d 张超出单次上限未处理", dropped))
	}
	if len(notes) == 0 {
		return caption
	}
	return strings.TrimSpace(caption) + "（" + strings.Join(notes, "，") + "）"
}

func dianaImageTaskKey(event MessageEvent, request dianaImageToolRequest) string {
	payload := strings.Join(append([]string{
		sessionKey(event), event.MessageID, request.Operation, request.Prompt, request.SourceMode,
	}, append(append([]string(nil), request.IdentitySources...), request.SourceMessageIDs...)...), "\x00")
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("image:%x", digest[:12])
}

// 明确堵住几种常见的推脱说法：任务其实已经在后台跑了，这时回一句「做不到」或
// 「你没有权限」，用户看到的就只剩这句话。
const (
	promptAsyncImageReply     = "【本轮图片任务】{status}。{announced}立即继续回复用户的文字部分，不要等待图片，不要再调用 image。这一轮只是把任务提交了，图还没画出来：用「在画了」「马上发出来」这类进行中的说法，不要说成「已经生成好了」。同时要用一句话讲清这次准备画什么（prompt 里的主体、动作、场景），不能只回一句「已受理」「在画了」就完事，用户得知道你要画的是不是他想要的；但只说打算画的内容，不要描述成品的构图、配色、画风细节或图上写了什么——那张图你还没看到。不要向用户提及任务编号等内部标识。不得声称无法生图、无法直接修改、需要用户自己操作或用户没有权限——任务已经受理，图片完成后会由运行时自动补发。"
	promptAsyncImageAnnounced = "运行时已经把「开始处理」发给用户了，不要再说一遍。"
)

var (
	promptAsyncImageReplySpec = registerPrompt(PromptSpec{
		Key:     "media.image_async",
		Group:   PromptGroupMedia,
		Title:   "后台生图已受理",
		Usage:   "意图识别判定要生图、任务已提交到后台时，告诉正式回复这一轮只说准备画什么，别说成已经画好。",
		Default: promptAsyncImageReply,
		Vars: []PromptVar{
			{Name: "status", Description: "「已在后台启动」或复用已有任务时的「已在后台处理」"},
			{Name: "announced", Description: "运行时已经发过开始提示时填入下一条说明，否则为空"},
		},
	})
	promptAsyncImageAnnouncedSpec = registerPrompt(PromptSpec{
		Key:     "media.image_async.announced",
		Group:   PromptGroupMedia,
		Title:   "生图开始提示已发出",
		Usage:   "运行时已经替机器人发过「开始处理」时，填进上一段的 {announced}，避免重复说。",
		Default: promptAsyncImageAnnounced,
	})
)

// 次数用完时最容易出的两种岔子：模型照样说「在画了」，或者换个工具（搜图、HTML
// 渲染、插件）变相交一张图。两样都要明确堵住。
const promptImageQuotaExceeded = "【本轮图片任务】{notice}。这次没有开始画，之后也不会补发。照实把这句话告诉用户，不要说「在画了」「马上发出来」，不要再调用 image，也不要改用搜图、HTML 渲染、浏览器、插件或其他任何途径变相出图。"

var promptImageQuotaExceededSpec = registerPrompt(PromptSpec{
	Key:     "media.image_quota_exceeded",
	Group:   PromptGroupMedia,
	Title:   "生图次数已用完",
	Usage:   "本群或这个人今天的生图次数用完时，告诉正式回复如实转告、别换别的途径出图。",
	Default: promptImageQuotaExceeded,
	Vars: []PromptVar{
		{Name: "notice", Description: "给用户的说明，如「今天本群的生图次数已用完（10/10），明天再来」"},
	},
})

// imageQuotaExceededInstruction 生成次数用完时的说明，工具结果和意图路由两条路共用。
func imageQuotaExceededInstruction(err *mediaGenerationQuotaError, configs ...BotConfig) string {
	return promptOverridesOf(configs).render(promptImageQuotaExceededSpec, map[string]string{"notice": err.Notice()})
}

// asyncImageReplyInstruction 生成本轮图片任务的说明。configs 传机器人配置时读它的覆盖值。
func asyncImageReplyInstruction(result dianaImageToolResult, configs ...BotConfig) string {
	overrides := promptOverridesOf(configs)
	status := "已在后台启动"
	if result.Reused {
		status = "已在后台处理"
	}
	announced := ""
	if result.Announced {
		announced = overrides.text(promptAsyncImageAnnouncedSpec)
	}
	return overrides.render(promptAsyncImageReplySpec, map[string]string{"status": status, "announced": announced})
}

func (r *Runtime) enqueueImageReplyTask(ctx context.Context, event MessageEvent, relationship RelationshipPolicy, operation string, prompt string, caption string) (dianaImageToolResult, error) {
	tool := &dianaImageTool{runtime: r, event: event, relationship: relationship}
	if err := ctx.Err(); err != nil {
		return dianaImageToolResult{}, err
	}
	request, err := tool.prepareRequest(map[string]any{
		"operation": operation,
		"prompt":    prompt,
		"caption":   caption,
	})
	if err != nil {
		return dianaImageToolResult{}, err
	}
	// 意图路由这条路没有模型点名原图，「先发图、隔几秒说改成黑白」只能靠同一个人
	// 刚发的那批图兜底。
	request.AllowRecentSenderImages = true
	return tool.enqueue(ctx, request)
}

func (r *Runtime) shareAgentImages(ctx context.Context, platform string, images []string) ([]string, []string, error) {
	sharedImages := make([]string, 0, len(images))
	localPaths := make([]string, 0, len(images))
	for _, image := range images {
		shared, localPath, err := r.shareAgentImage(ctx, platform, image)
		if err != nil {
			for _, path := range localPaths {
				cleanupLocalMediaFile(path)
			}
			return nil, nil, err
		}
		sharedImages = append(sharedImages, shared)
		if localPath != "" {
			localPaths = append(localPaths, localPath)
		}
	}
	return sharedImages, localPaths, nil
}

func (r *Runtime) shareAgentImage(ctx context.Context, platform, image string) (string, string, error) {
	image = strings.TrimSpace(image)
	if parsed, err := url.Parse(image); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if NormalizePlatformID(platform) != PlatformTelegram {
			return image, "", nil
		}
		data, mediaType, err := downloadImageBytesWithLimit(ctx, image, dianaImageMaxDecodedSize)
		if err != nil {
			return "", "", fmt.Errorf("Telegram 发送前下载生成图片失败: %w", err)
		}
		path, cleanupPath, err := r.cacheAgentImage(data, mediaType, generatedImageExtension(mediaType, data))
		return path, cleanupPath, err
	}
	mediaType, encoded, ok := strings.Cut(image, ",")
	mediaType = strings.TrimPrefix(mediaType, "data:")
	if !ok || !strings.HasPrefix(strings.ToLower(mediaType), "image/") || !strings.HasSuffix(strings.ToLower(mediaType), ";base64") {
		return "", "", fmt.Errorf("图片接口返回了不支持的图片地址")
	}
	mediaType = strings.TrimSuffix(mediaType, ";base64")
	if base64.StdEncoding.DecodedLen(len(encoded)) > dianaImageMaxDecodedSize {
		return "", "", fmt.Errorf("图片接口返回的图片超过 32 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return "", "", fmt.Errorf("图片接口返回的 base64 图片无效")
	}
	path, cleanupPath, err := r.cacheAgentImage(data, mediaType, generatedImageExtension(mediaType, data))
	if err != nil {
		return "", "", err
	}
	if NormalizePlatformID(platform) == PlatformTelegram {
		return path, cleanupPath, nil
	}
	r.mu.RLock()
	sharer := r.localMedia
	r.mu.RUnlock()
	if sharer == nil {
		if cleanupPath != "" {
			cleanupLocalMediaFile(cleanupPath)
		}
		return "", "", fmt.Errorf("本地媒体共享未配置，无法把生成图片交给 OneBot v11 客户端")
	}
	shared, ok := sharer.Share(path, dianaImageMediaTTL)
	if !ok {
		if cleanupPath != "" {
			cleanupLocalMediaFile(cleanupPath)
		}
		return "", "", fmt.Errorf("生成图片无法通过本地媒体代理共享")
	}
	return shared, cleanupPath, nil
}

// persistsToWorkspace 判断这次的成品要不要落进工作目录：工作目录是主人的，只有主人
// 自己出的图、且开了文件写入时才存，群成员出图不往主人的磁盘上堆东西。群里别人出
// 的图主人要存，照样能用 save_to_workspace 从聊天记录里取。
func (t *dianaImageTool) persistsToWorkspace() bool {
	if t.runtime == nil || !t.relationship.Owner {
		return false
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	return cfg.AgentEnabled && cfg.agentFileWriteAllowed()
}

// dianaImageWorkspaceStem 给一次出图任务定文件名前缀：时间方便人认，任务键的一段
// 保证同一秒的两个任务不撞名。
func dianaImageWorkspaceStem(taskKey string, now time.Time) string {
	suffix := strings.TrimPrefix(taskKey, "image:")
	if len(suffix) > 6 {
		suffix = suffix[:6]
	}
	return "image-" + now.Format("20060102-150405") + "-" + suffix
}

// persistGeneratedImages 把成品原图存进工作目录 outputs/。尽力而为：存失败只记日志，
// 不影响图片投递。内联的 base64 直接写；接口只给了网址的（OneBot 下原样转发、本地
// 没有副本）在后台另行下载，不拖慢发图。
func (r *Runtime) persistGeneratedImages(ctx context.Context, event MessageEvent, stem string, images []string) {
	if r == nil || stem == "" || len(images) == 0 {
		return
	}
	now := time.Now()
	var remote []int
	for index, image := range images {
		if normalizedHTTPURL(image) != "" {
			remote = append(remote, index)
			continue
		}
		r.persistGeneratedImage(ctx, event, stem, index, len(images), image, now)
	}
	if len(remote) == 0 {
		return
	}
	background := context.WithoutCancel(ctx)
	go func() {
		defer recoverGoroutinePanic("image_agent_tool.persist")
		for _, index := range remote {
			r.persistGeneratedImage(background, event, stem, index, len(images), images[index], now)
		}
	}()
}

func (r *Runtime) persistGeneratedImage(ctx context.Context, event MessageEvent, stem string, index, total int, image string, now time.Time) {
	var (
		data []byte
		err  error
	)
	if remote := normalizedHTTPURL(image); remote != "" {
		data, _, err = downloadImageBytesWithLimit(ctx, remote, dianaImageMaxDecodedSize)
	} else {
		data, _, err = decodeInlineHistoryImage(image)
	}
	if err != nil {
		log.Printf("diana image: 成品存进工作目录失败（取图）：%v", err)
		return
	}
	name := stem
	if total > 1 && index > 0 {
		name = fmt.Sprintf("%s-%d", stem, index+1)
	}
	name += generatedImageExtension("", data)
	saved, err := r.saveWorkspacePayload(event, workspaceMediaPayload{data: data, kind: "image", generated: true}, agent.WorkspaceOutputsDir+"/"+name, false, now)
	if err != nil {
		log.Printf("diana image: 成品存进工作目录失败：%v", err)
		return
	}
	log.Printf("diana image: 成品已存进工作目录 %s", saved.Path)
}

// generatedImageExtension 给生成图片定扩展名：先按字节认，认不出再看接口报的类型，
// 都不行才退回 .png。以前用 mime.ExtensionsByType，macOS 上 image/jpeg 排第一的是
// .jfif，存出来的 image.jfif 连 send_attachment 的白名单都不认。
func generatedImageExtension(mediaType string, data []byte) string {
	if sniffed := agent.SniffMediaType(data); strings.HasPrefix(sniffed, "image/") {
		if ext := agent.CanonicalMediaExtension(sniffed); ext != "" {
			return ext
		}
	}
	if ext := agent.CanonicalMediaExtension(mediaType); ext != "" && strings.HasPrefix(strings.ToLower(mediaType), "image/") {
		return ext
	}
	return ".png"
}

func (r *Runtime) cacheAgentImage(data []byte, mediaType, extension string) (string, string, error) {
	r.mu.RLock()
	store := r.media
	r.mu.RUnlock()
	if store != nil {
		path, err := store.StoreImage(data, mediaType)
		if err == nil {
			return path, "", nil
		}
		log.Printf("media: cache generated image failed: %v", err)
	}

	workDir, err := os.MkdirTemp("", "diana-agent-image-")
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(workDir, "image"+extension)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return "", "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(workDir)
		return "", "", err
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(workDir)
		return "", "", err
	}
	return path, path, nil
}
