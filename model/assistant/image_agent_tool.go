// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"mime"
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

const (
	dianaImageToolName       = "diana.image"
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
}

type dianaImageToolRequest struct {
	Operation       string
	Prompt          string
	Caption         string
	IdentitySources []string
	// SourceLabels 与 IdentitySources 一一对应的人类可读标注（昵称等）。
	// 逐张发送时作为对应图片的说明文字,让大家知道每张是谁的。
	SourceLabels []string
	// SourceMode 决定多张参考图怎么用：combine 把它们合成一张（默认，也是历史行为），
	// each 对每张各做一次编辑，最后一起发出。
	SourceMode string
}

type dianaImageTaskOutput struct {
	Caption   string
	ImageURLs []string
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
		operations = append(operations, `"edit"（编辑当前、引用或近期图片/成员头像）`)
	}
	if len(operations) == 0 {
		operations = append(operations, "无")
	}
	return `异步生成或编辑图片。工具受理后由运行时替你告诉用户「开始处理」，图片在后台完成后自动发送。调用后直接继续输出 final 文字回复即可，不要等待图片，不要再次调用本工具，也不要重复说一遍「正在处理」。当前允许操作：` + strings.Join(operations, "、") + `。要对多张参考图逐张各出一张，用 source_mode="each"。如果用户要求先搜索、核验网页或读取外部资料再出图，必须先完成搜索或浏览器调用，prompt 里只能写已确认的事实，不能虚构没查到的内容。`
}

// dianaImageStartedMessage 是任务受理后立刻发给用户的那句话。
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
	if result.Reused {
		return fmt.Sprintf("同样的%s任务已经在处理中，完成后我会把结果发出来。", action)
	}
	return fmt.Sprintf("开始%s，完成后我会把结果发出来。", action)
}

// InputSchema 的 operation 枚举按当前关系等级裁剪：没解锁的操作压根不出现在
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
		"operation": toolEnumParam("generate 生成新图片；edit 编辑当前、引用或近期出现过的图片与成员头像。省略时按 generate 处理。", operations...),
		"prompt":    toolStringParam("交给图片模型的完整、自包含的最终提示词。不要写成对话口吻，也不要依赖上下文里的指代。"),
		"caption":   toolStringParam("图片完成后随图发送的一句短文字，可选。"),
	}
	// 要编辑谁的头像由你来判断：运行时不去读用户的措辞，只负责把你点名的 id 换成
	// 头像地址，并核对这个人在当前会话里确实存在。
	if t.relationship.AllowImageEditing {
		properties["identity_sources"] = toolStringArrayParam(
			`operation="edit" 且要编辑的是某人或本群的头像时，在这里点名头像来源；当前消息或引用消息本身带图时不要填。` +
				`可选值："` + avatarSourceSender + `"（本条消息的发送者）、"` + avatarSourceBot + `"（机器人自己）、"` +
				avatarSourceGroup + `"（本群的群头像）、"` + avatarSourceMemberPrefix + `<user_id>"（指定成员，user_id 用真实 QQ 号）。` +
				`用户按名字或昵称指人时，先从上下文或群成员工具里查出对应 user_id 再填，不要编造；最多 ` +
				strconv.Itoa(maxAvatarImageSources) + ` 个。`)
	}
	if t.relationship.AllowImageEditing {
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
	if err != nil {
		return "", err
	}
	// 开场白由运行时发，不留给模型自由发挥。以前这里只是把「已受理」写进工具结果，
	// 指望模型自己在 final 里说一句；模型经常改口成「没办法直接修改」「你没有这个
	// 权限」之类，用户那边就只剩一句推脱，而任务其实已经在后台跑了。
	if announcement := dianaImageStartedMessage(request, result); announcement != "" {
		if sendErr := t.runtime.send(ctx, t.event, announcement); sendErr != nil {
			// 开场白发不出去不影响任务本身，只记一笔。
			log.Printf("chatbot image task announcement failed: %v", sendErr)
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
	if operation == "" {
		switch {
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
		if !t.relationship.AllowImageGeneration {
			return dianaImageToolRequest{}, fmt.Errorf("%s", relationshipPermissionDenied(t.relationship, "图片生成", relationshipImageTierName))
		}
	case "edit":
		if !t.relationship.AllowImageEditing {
			return dianaImageToolRequest{}, fmt.Errorf("%s", relationshipPermissionDenied(t.relationship, "图片编辑", relationshipImageTierName))
		}
	default:
		return dianaImageToolRequest{}, fmt.Errorf("operation 必须是 generate 或 edit")
	}
	if t.runtime.llmStore == nil {
		return dianaImageToolRequest{}, fmt.Errorf("chatbot: llm profile store is not configured")
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
	identitySources := configToolStringSlice(input, "identity_sources")
	if operation == "edit" && len(identitySources) == 0 {
		identitySources = defaultAvatarIdentitySources(t.event, t.runtime.effectiveConfigForEvent(t.event).BotAccount)
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
		IdentitySources: identitySources, SourceLabels: sourceLabels, SourceMode: sourceMode,
	}, nil
}

func (t *dianaImageTool) enqueue(ctx context.Context, request dianaImageToolRequest) (dianaImageToolResult, error) {
	name := "图片生成"
	if request.Operation == "edit" {
		name = "图片编辑"
	}
	task := PluginTask{
		Kind:    "image",
		Name:    name,
		Key:     dianaImageTaskKey(t.event, request),
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
			message := OutgoingMessage{Text: output.Caption, ImageURLs: output.ImageURLs}
			if t.event.Kind == EventKindGroup {
				message.ReplyMessageID = t.event.MessageID
			}
			return PluginTaskResult{Messages: []OutgoingMessage{message}}, nil
		},
	}
	reservation := t.runtime.reservePluginTasksForTurn(ctx, t.event, []PluginTask{task})
	if !reservation.handled {
		return dianaImageToolResult{}, fmt.Errorf("图片任务无法启动")
	}
	result := dianaImageToolResult{OK: true, Queued: true, Action: request.Operation, Caption: request.Caption}
	if len(reservation.reserved) > 0 {
		result.TaskID = reservation.reserved[0].id
		t.runtime.startPluginTaskReservation(reservation)
		return result, nil
	}
	if len(reservation.duplicates) > 0 {
		result.TaskID = reservation.duplicates[0].ID
		result.Reused = true
		return result, nil
	}
	return dianaImageToolResult{}, fmt.Errorf("图片任务无法启动")
}

func (t *dianaImageTool) taskTimeout() time.Duration {
	configs := t.runtime.imageProviderConfigs()
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
	progress := services.Report
	operation := request.Operation
	prompt := request.Prompt
	submittedPrompt := t.runtime.enrichImagePromptWithChatContext(ctx, t.event, prompt)
	var (
		cfg         llm.ProviderConfig
		images      []string
		sourceCount int
		action      string
		message     string
		// dropped 是超出逐张上限被丢掉的参考图数量，failed 是逐张模式里失败的张数。
		// 两者都要如实告诉用户，静默少发几张比报错更难查。
		dropped int
		failed  int
	)
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
		cfg = usedCfg
		images = resp.Images
		action = "chatbot.image.generate"
		message = "Agent 图片生成已完成"
	case "edit":
		sources := t.runtime.imageEditSourceImages(ctx, t.event, request.IdentitySources)
		if len(sources) == 0 {
			return dianaImageTaskOutput{}, fmt.Errorf("没有找到可编辑的图片；请让用户发送图片或引用图片消息")
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
		streamed := 0
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
			cfg = usedCfg
			sourceCount += len(batch)
			if streaming {
				shared, localPaths, shareErr := t.runtime.shareAgentImages(resp.Images)
				if shareErr == nil && len(shared) > 0 {
					if len(localPaths) > 0 {
						cleanupLocalMediaFilesLater(localPaths, dianaImageMediaTTL)
					}
					outgoing := OutgoingMessage{ImageURLs: shared}
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
		}
		if streamed > 0 && len(images) == 0 {
			t.runtime.recordImageOperation(ctx, t.event, "chatbot.image.edit", "Agent 图片编辑已完成", prompt, submittedPrompt, cfg.ImageModelWithDefault(), streamed, sourceCount)
			note := ""
			if failed > 0 || dropped > 0 {
				note = dianaImageResultCaption("", 0, dropped, failed)
			}
			return dianaImageTaskOutput{Delivered: true, Caption: note}, nil
		}
		action = "chatbot.image.edit"
		message = "Agent 图片编辑已完成"
	}
	if len(images) == 0 {
		return dianaImageTaskOutput{}, fmt.Errorf("图片接口没有返回图片")
	}

	sharedImages, localPaths, err := t.runtime.shareAgentImages(images)
	if err != nil {
		return dianaImageTaskOutput{}, err
	}
	if len(localPaths) > 0 {
		cleanupLocalMediaFilesLater(localPaths, dianaImageMediaTTL)
	}
	t.runtime.recordImageOperation(ctx, t.event, action, message, prompt, submittedPrompt, cfg.ImageModelWithDefault(), len(images), sourceCount)
	return dianaImageTaskOutput{
		Caption:   dianaImageResultCaption(request.Caption, len(sharedImages), dropped, failed),
		ImageURLs: sharedImages,
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
	}, request.IdentitySources...), "\x00")
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("image:%x", digest[:12])
}

func asyncImageReplyInstruction(result dianaImageToolResult) string {
	status := "已在后台启动"
	if result.Reused {
		status = "已在后台处理"
	}
	announced := ""
	if result.Announced {
		announced = "运行时已经把「开始处理」发给用户了，不要再说一遍「正在处理」「稍等」。"
	}
	// 明确堵住几种常见的推脱说法：任务其实已经在后台跑了，这时回一句「做不到」或
	// 「你没有权限」，用户看到的就只剩这句话。
	return fmt.Sprintf("【本轮图片任务】%s。%s立即继续回复用户的文字部分，不要等待图片，不要再调用 diana.image。不要向用户提及任务编号等内部标识。不得声称无法生图、无法直接修改、需要用户自己操作或用户没有权限——任务已经受理，图片完成后会由运行时自动补发。", status, announced)
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
	return tool.enqueue(ctx, request)
}

func (r *Runtime) shareAgentImages(images []string) ([]string, []string, error) {
	sharedImages := make([]string, 0, len(images))
	localPaths := make([]string, 0, len(images))
	for _, image := range images {
		shared, localPath, err := r.shareAgentImage(image)
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

func (r *Runtime) shareAgentImage(image string) (string, string, error) {
	image = strings.TrimSpace(image)
	if parsed, err := url.Parse(image); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return image, "", nil
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
	extension := ".png"
	if extensions, err := mime.ExtensionsByType(mediaType); err == nil && len(extensions) > 0 {
		extension = extensions[0]
	}
	path, cleanupPath, err := r.cacheAgentImage(data, mediaType, extension)
	if err != nil {
		return "", "", err
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
