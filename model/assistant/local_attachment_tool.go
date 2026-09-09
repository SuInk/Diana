package assistant

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

const localAttachmentMaxBytes = 32 << 20

type dianaLocalAttachmentTool struct {
	runtime *Runtime
	event   MessageEvent
	view    bool
	mu      sync.Mutex
	parts   []llm.ContentPart
}

func (t *dianaLocalAttachmentTool) Name() string {
	if t.view {
		return "diana.view_image"
	}
	return "diana.send_attachment"
}

func (t *dianaLocalAttachmentTool) Description() string {
	if t.view {
		return "读取 Agent 工作目录内的真实图片，把画面作为附件交给下一轮模型。用于查看 run_command 下载或生成的图片；不能把文件名当作画面证据。path 必须是工作目录相对路径。读取成功不代表已发送到聊天。"
	}
	return "把 Agent 工作目录内的文件发送到当前会话，支持 Telegram 和 OneBot。mode=image 作为图片发送，mode=file 作为原文件附件发送。命令下载成功不等于用户收到文件；需要交付时调用本工具。描述图片内容前先用 diana.view_image 查看。不会执行文件；仅主人可用。"
}

func (t *dianaLocalAttachmentTool) InputSchema() map[string]any {
	properties := map[string]any{"path": toolStringParam("Agent 工作目录相对路径，例如 downloads/photo.jpg。")}
	required := []string{"path"}
	if !t.view {
		properties["mode"] = toolEnumParam("图片或原始文件附件。", "image", "file")
		required = append(required, "mode")
	}
	return toolObjectSchema(required, properties)
}

func (t *dianaLocalAttachmentTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := configToolString(input, "path")
	mode := configToolString(input, "mode")
	if !t.view && mode != "image" && mode != "file" {
		return "", fmt.Errorf("mode must be image or file")
	}
	data, err := agent.ReadWorkspaceFile(AgentWorkspaceDir(), path, localAttachmentMaxBytes)
	if err != nil {
		return "", err
	}
	if t.view || mode == "image" {
		images, err := normalizeLLMImageParts(data, http.DetectContentType(data))
		if err != nil {
			return "", err
		}
		if t.view {
			for _, image := range images {
				t.parts = append(t.parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: image, Detail: "high"})
			}
			return `{"status":"loaded","message":"真实画面已附加到工具结果；尚未发送到聊天。"}`, nil
		}
		shared, paths, err := t.runtime.shareAgentImages(ctx, t.event.Platform, images[:1])
		if err != nil {
			return "", err
		}
		defer func() {
			for _, p := range paths {
				cleanupLocalMediaFile(p)
			}
		}()
		err = t.runtime.sendOutgoing(ctx, t.event, OutgoingMessage{ImageURLs: shared})
		if err != nil {
			return "", err
		}
	} else {
		platform := NormalizePlatformID(t.event.Platform)
		if platform != PlatformTelegram && !IsOneBotPlatform(platform) {
			return "", fmt.Errorf("file attachment sending is unavailable for this platform")
		}
		// Send a private snapshot, not a path the command process can change during upload.
		dir, err := os.MkdirTemp("", "diana-attachment-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		name := filepath.Base(path)
		snapshot := filepath.Join(dir, name)
		if err = os.WriteFile(snapshot, data, 0600); err != nil {
			return "", err
		}
		if platform == PlatformTelegram {
			err = t.runtime.sendOutgoing(ctx, t.event, OutgoingMessage{Segments: []MessageSegment{{Type: "file", Data: map[string]string{"file": snapshot, "name": name}}}})
		} else {
			err = t.runtime.uploadResolverVideoFile(ctx, t.event, resolverVideoUpload{Path: snapshot, Name: name})
		}
		if err != nil {
			return "", err
		}
	}
	return `{"status":"sent","message":"附件已发送到当前会话。"}`, nil
}

func (t *dianaLocalAttachmentTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
