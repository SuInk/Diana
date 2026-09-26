// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const (
	dianaVideoToolName = "video"
	// 视频比图片大得多，接入端取文件也慢，共享链接多留一会儿。
	dianaVideoMediaTTL     = 30 * time.Minute
	dianaVideoTimeoutGrace = time.Minute
	dianaVideoMaxSeconds   = 60
)

var errVideoSourceNotFound = errors.New("没有找到可作为首帧的图片：当前消息、引用消息和 source_message_ids 指向的消息里都没有图。这次没有开始生成；要图生视频就请用户发图或引用那张图，否则去掉 use_image 和 source_message_ids 改为纯文字生成")

// dianaVideoTool 把视频生成插槽交给模型。视频任务要跑几分钟，所以和生图一样
// 受理后放进后台任务，完成时由运行时补发。
type dianaVideoTool struct {
	runtime      *Runtime
	event        MessageEvent
	relationship RelationshipPolicy
}

type dianaVideoToolResult struct {
	OK        bool   `json:"ok"`
	Queued    bool   `json:"queued"`
	TaskID    string `json:"task_id,omitempty"`
	Mode      string `json:"mode"`
	Reused    bool   `json:"reused,omitempty"`
	Announced bool   `json:"announced,omitempty"`
	Note      string `json:"note,omitempty"`
}

type dianaVideoRequest struct {
	Prompt  string
	Caption string
	Image   string
	Seconds int
	Size    string
}

func newDianaVideoTool(runtime *Runtime, event MessageEvent, relationship RelationshipPolicy) agent.Tool {
	return &dianaVideoTool{runtime: runtime, event: event, relationship: relationship}
}

func (t *dianaVideoTool) Name() string { return dianaVideoToolName }

func (t *dianaVideoTool) Description() string {
	return "异步生成一段短视频并发到当前会话：只给 prompt 是文生视频；use_image=true 或填 source_message_ids 时以那张图为首帧做图生视频。视频要几分钟才能生成完，调用后直接继续输出 final 文字回复，说清准备生成什么即可，不要等待、不要重复调用，也不要说成已经生成好。只在用户明确要视频时调用。"
}

func (t *dianaVideoTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"prompt"}, map[string]any{
		"prompt":             toolStringParam("交给视频模型的完整、自包含的画面描述：主体、动作、镜头、风格。不要写成对话口吻，也不要依赖上下文里的指代。"),
		"caption":            toolStringParam("视频完成后随视频发送的一句短文字，可选。"),
		"use_image":          map[string]any{"type": "boolean", "description": "用当前消息或引用消息里的图作为首帧（图生视频）。"},
		"source_message_ids": toolStringArrayParam("首帧图所在消息的 message_id，用户指的是聊天记录里某条消息的图时填；只取第一张图。"),
		"seconds":            map[string]any{"type": "integer", "description": "视频时长（秒），可选；不填用模型分配里的默认值。", "minimum": 1, "maximum": dianaVideoMaxSeconds},
		"size":               toolStringParam("分辨率，形如 1280x720 或 720x1280，可选；不填用模型分配里的默认值。"),
	})
}

func (t *dianaVideoTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("视频工具未配置")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !t.relationship.Owner && !t.relationship.AllowImageGeneration {
		return "", fmt.Errorf("当前用户没有生成图片或视频的权限")
	}
	if !t.runtime.mediaSlotConfigured(withModelConfigEvent(ctx, t.event), mediaSlotVideo) {
		return "", fmt.Errorf("视频生成：%w，请主人在模型分配里给「视频生成」选好提供商和模型", errMediaSlotNotConfigured)
	}
	request, err := t.prepareRequest(ctx, input)
	if err != nil {
		return "", err
	}
	result, err := t.enqueue(ctx, request)
	if err != nil {
		return "", err
	}
	if announcement := dianaVideoStartedMessage(request, result); announcement != "" {
		if sink := imageAnnouncementSinkFrom(ctx); sink != nil {
			sink.offer(announcement)
		} else if sendErr := t.runtime.send(ctx, t.event, announcement); sendErr != nil {
			log.Printf("diana video task announcement failed: %v", sendErr)
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

func (t *dianaVideoTool) prepareRequest(ctx context.Context, input map[string]any) (dianaVideoRequest, error) {
	prompt := strings.TrimSpace(configToolString(input, "prompt"))
	if prompt == "" {
		return dianaVideoRequest{}, fmt.Errorf("prompt 不能为空")
	}
	if len([]rune(prompt)) > 4000 {
		return dianaVideoRequest{}, fmt.Errorf("prompt 过长，请压缩到 4000 字以内")
	}
	caption := strings.TrimSpace(configToolString(input, "caption"))
	if runes := []rune(caption); len(runes) > 200 {
		caption = string(runes[:200])
	}
	request := dianaVideoRequest{Prompt: prompt, Caption: caption, Size: strings.TrimSpace(configToolString(input, "size"))}
	if seconds, ok := numberValue(input["seconds"]); ok && seconds > 0 {
		if seconds > dianaVideoMaxSeconds {
			return dianaVideoRequest{}, fmt.Errorf("seconds 最多 %d", dianaVideoMaxSeconds)
		}
		request.Seconds = int(seconds)
	}
	sourceMessageIDs := configToolStringSlice(input, "source_message_ids")
	useImage, _ := input["use_image"].(bool)
	if len(sourceMessageIDs) > maxImageEditSourceMessages {
		return dianaVideoRequest{}, fmt.Errorf("source_message_ids 最多 %d 条", maxImageEditSourceMessages)
	}
	if useImage || len(sourceMessageIDs) > 0 {
		// 首帧在受理时就解析：找不到图能当场告诉模型，而不是先说「在生成了」。
		sources, _, err := t.runtime.resolveImageEditSources(ctx, t.event, imageEditSourcePlan{SourceMessageIDs: sourceMessageIDs})
		if err != nil {
			return dianaVideoRequest{}, err
		}
		if len(sources) == 0 {
			return dianaVideoRequest{}, errVideoSourceNotFound
		}
		request.Image = sources[0]
	}
	return request, nil
}

func (request dianaVideoRequest) mode() string {
	if request.Image != "" {
		return "image_to_video"
	}
	return "text_to_video"
}

func dianaVideoTaskKey(event MessageEvent, request dianaVideoRequest) string {
	payload := strings.Join([]string{sessionKey(event), event.MessageID, request.Prompt, request.Image, strconv.Itoa(request.Seconds), request.Size}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("video:%x", digest[:12])
}

func (t *dianaVideoTool) taskTimeout() time.Duration {
	routes := t.runtime.mediaSlotRoutes(withModelConfigEvent(context.Background(), t.event), mediaSlotVideo)
	timeout := defaultVideoSlotTimeout
	if len(routes) > 0 {
		timeout = routes[0].videoJobTimeout()
	}
	// 后备路由各自有一整份时限，任务总时限按条数放宽，否则主路由超时后后备来不及跑。
	if len(routes) > 1 {
		timeout *= time.Duration(len(routes))
	}
	return timeout + dianaVideoTimeoutGrace
}

func (t *dianaVideoTool) enqueue(ctx context.Context, request dianaVideoRequest) (dianaVideoToolResult, error) {
	task := PluginTask{
		Kind:    "video",
		Name:    "视频生成",
		Key:     dianaVideoTaskKey(t.event, request),
		Timeout: t.taskTimeout(),
		Run: func(ctx context.Context, services PluginTaskServices) (PluginTaskResult, error) {
			message, err := t.execute(ctx, request, services)
			if err != nil {
				return PluginTaskResult{}, err
			}
			return PluginTaskResult{Messages: []OutgoingMessage{message}}, nil
		},
	}
	reservation := t.runtime.reservePluginTasksForTurn(ctx, t.event, []PluginTask{task})
	if !reservation.handled {
		return dianaVideoToolResult{}, fmt.Errorf("视频任务无法启动")
	}
	result := dianaVideoToolResult{OK: true, Queued: true, Mode: request.mode()}
	if len(reservation.reserved) > 0 {
		result.TaskID = reservation.reserved[0].id
		if sink := imageAnnouncementSinkFrom(ctx); sink != nil {
			sink.deferTask(
				func() { t.runtime.startPluginTaskReservation(reservation) },
				func() { t.runtime.cancelPluginTaskReservation(reservation) },
			)
		} else {
			t.runtime.startPluginTaskReservation(reservation)
		}
		result.Note = "任务已受理，视频还没生成出来；完成后运行时会自动发送。回复里只说准备生成什么，不要描述成品。"
		return result, nil
	}
	if len(reservation.duplicates) > 0 {
		result.TaskID = reservation.duplicates[0].ID
		result.Reused = true
		return result, nil
	}
	return dianaVideoToolResult{}, fmt.Errorf("视频任务无法启动")
}

func dianaVideoStartedMessage(request dianaVideoRequest, result dianaVideoToolResult) string {
	if !result.OK || result.TaskID == "" {
		return ""
	}
	action := "生成视频"
	if request.Image != "" {
		action = "用这张图生成视频"
	}
	if result.Reused {
		return "同样的视频任务已经在处理中，完成后我会把视频发出来。"
	}
	if subject := imageAnnouncementSubject(request.Prompt); subject != "" {
		return fmt.Sprintf("开始%s：%s，要几分钟，好了我发出来。", action, subject)
	}
	return fmt.Sprintf("开始%s，要几分钟，好了我发出来。", action)
}

func (t *dianaVideoTool) execute(ctx context.Context, request dianaVideoRequest, services PluginTaskServices) (OutgoingMessage, error) {
	// 后台任务的 ctx 不带消息事件；不挂上的话插槽按默认机器人解析，用量也记不到这条消息名下。
	if llmUsageFromContext(ctx) == nil {
		ctx = withLLMUsageContext(ctx, t.event)
	}
	lastProgress := -1
	result, route, err := t.runtime.generateVideo(ctx, llm.VideoGenerateRequest{
		Prompt:  request.Prompt,
		Image:   request.Image,
		Seconds: request.Seconds,
		Size:    request.Size,
	}, func(job llm.VideoJob) {
		if services.Report == nil || job.Progress == lastProgress {
			return
		}
		lastProgress = job.Progress
		// 只更新后台任务状态，不往聊天里发进度。
		services.Report(PluginTaskProgress{Phase: string(job.Status), Completed: job.Progress, Total: 100})
	})
	if err != nil {
		return OutgoingMessage{}, err
	}
	shared, err := t.runtime.shareGeneratedVideo(t.event.Platform, result)
	if err != nil {
		return OutgoingMessage{}, err
	}
	log.Printf("diana video: %s 已完成（%s，%s）", request.mode(), route.label(), sessionKey(t.event))
	message := OutgoingMessage{Text: request.Caption, VideoURLs: []string{shared}}
	if t.event.Kind == EventKindGroup {
		message.ReplyMessageID = t.event.MessageID
	}
	return message, nil
}

// shareGeneratedVideo 把成品落到本地，再交给平台能取到的地址：Telegram 直接上传
// 本地文件；OneBot 的接入端多半不在本机，走本地媒体代理给一个 HTTP 地址。
func (r *Runtime) shareGeneratedVideo(platform string, result *llm.VideoResult) (string, error) {
	if result == nil || len(result.Video) == 0 {
		return "", fmt.Errorf("视频接口没有返回视频")
	}
	extension := agent.CanonicalMediaExtension(result.MediaType)
	if extension == "" || !strings.HasPrefix(strings.ToLower(result.MediaType), "video/") {
		extension = ".mp4"
	}
	workDir, err := os.MkdirTemp("", "diana-agent-video-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(workDir, "video"+extension)
	if err := os.WriteFile(path, result.Video, 0o600); err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
	}
	cleanupLocalMediaFilesLater([]string{path}, dianaVideoMediaTTL)
	if NormalizePlatformID(platform) == PlatformTelegram {
		return path, nil
	}
	r.mu.RLock()
	sharer := r.localMedia
	r.mu.RUnlock()
	if sharer == nil {
		return path, nil
	}
	shared, ok := sharer.Share(path, dianaVideoMediaTTL)
	if !ok {
		return "", fmt.Errorf("生成的视频无法通过本地媒体代理共享")
	}
	return shared, nil
}
