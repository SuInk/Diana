// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/netguard"
)

// save_to_workspace 把二进制文件存进 Agent 工作目录：聊天里的图片、视频、语音、文件
// （包括机器人自己发出去的生成图），网址上的文件，MCP 工具交出来的媒体。
//
// 以前工作目录只有文本工具：主人说「把刚才那张图存下来」，模型只能用 write_file
// 写一份 .md 描述，然后说存好了。这里不重新发明下载：聊天媒体走历史媒体那套解析和
// 缓存，网址走带 SSRF 防护的下载缓存，MCP 媒体直接从暂存区取字节。
const dianaSaveToWorkspaceToolName = "save_to_workspace"

const (
	saveSourceChat = "chat"
	saveSourceURL  = "url"
	saveSourceMCP  = "mcp"
	// saveWorkspaceFetchTimeout 是网址下载的整体时限，和视频上下文下载同一量级。
	saveWorkspaceFetchTimeout = 60 * time.Second
	// saveWorkspaceNameMaxRunes 限制文件名长度：平台给的文件名偶尔是一整句话。
	saveWorkspaceNameMaxRunes = 80
)

type dianaSaveToWorkspaceTool struct {
	runtime *Runtime
	event   MessageEvent
	now     func() time.Time
}

func newDianaSaveToWorkspaceTool(r *Runtime, event MessageEvent) *dianaSaveToWorkspaceTool {
	return &dianaSaveToWorkspaceTool{runtime: r, event: event}
}

func (t *dianaSaveToWorkspaceTool) Name() string { return dianaSaveToWorkspaceToolName }

func (t *dianaSaveToWorkspaceTool) Description() string {
	return "把图片、视频、语音、PDF、压缩包这类二进制文件原样存进 Agent 工作目录。source=chat 按 message_id 取当前会话里某条消息的媒体（包括你自己发出去的生成图），" +
		"source=url 下载公网文件，source=mcp 按 media_id 存 MCP 工具返回的媒体。按内容认类型并纠正扩展名，返回实际保存的 path、大小、类型和图片宽高；" +
		"没指定 path 时聊天媒体和网址存到 " + agent.WorkspaceDownloadsDir + "/，机器人自己产出的存到 " + agent.WorkspaceOutputsDir + "/，同名不覆盖、自动加序号。" +
		"文本文件用 write_file；存好后要发给用户用 send_attachment，要整理用 manage_files。仅主人可用。"
}

func (t *dianaSaveToWorkspaceTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"source"}, map[string]any{
		"source":      toolEnumParam("从哪里存：chat 当前会话的消息媒体，url 公网文件，mcp MCP 工具返回的媒体。", saveSourceChat, saveSourceURL, saveSourceMCP),
		"message_id":  toolStringParam("source=chat 必填：带媒体的那条消息的 message_id。"),
		"media_index": toolIntParam("source=chat 时取消息里第几个媒体（图片、视频、语音、文件按出现顺序统一编号，从 1 开始）；消息里只有一个媒体时可省略。", 1, 64),
		"url":         toolStringParam("source=url 必填：公网 HTTP(S) 文件直链。"),
		"media_id":    toolStringParam("source=mcp 必填：MCP 结果里的 media_id（mcpm_ 开头）。"),
		"path":        toolStringParam("可选：工作目录内的相对保存路径。以 / 结尾表示目录，文件名自动取；给了文件名时扩展名仍按内容纠正。"),
		"overwrite":   toolBoolParam("可选：目标文件已存在时覆盖，默认 false（自动改名加序号）。"),
	})
}

// workspaceMediaPayload 是从某个来源取到的一份字节，以及给它起名要用的线索。
type workspaceMediaPayload struct {
	data []byte
	// name 是来源自带的文件名（群文件名、网址末段、MCP 给的名字），可能为空。
	name string
	// kind 在没有文件名时用来起名：image、video、audio、file。
	kind string
	// generated 表示这是机器人自己产出的东西，默认落进 outputs/ 而不是 downloads/。
	generated bool
}

type saveToWorkspaceResult struct {
	Status             string `json:"status"`
	Source             string `json:"source"`
	Path               string `json:"path"`
	Bytes              int    `json:"bytes"`
	MIME               string `json:"mime"`
	Width              int    `json:"width,omitempty"`
	Height             int    `json:"height,omitempty"`
	ExtensionCorrected string `json:"extension_corrected,omitempty"`
	Message            string `json:"message"`
}

func (t *dianaSaveToWorkspaceTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("save_to_workspace: runtime is not configured")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source := strings.ToLower(strings.TrimSpace(configToolString(input, "source")))
	var (
		payload workspaceMediaPayload
		err     error
	)
	switch source {
	case saveSourceChat:
		payload, err = t.runtime.workspaceChatMedia(ctx, t.event, configToolString(input, "message_id"), intFromAny(input["media_index"]))
	case saveSourceURL:
		payload, err = fetchWorkspaceURL(ctx, configToolString(input, "url"))
	case saveSourceMCP:
		payload, err = workspaceMCPMedia(configToolString(input, "media_id"))
	default:
		return "", fmt.Errorf("source 必须是 chat、url 或 mcp")
	}
	if err != nil {
		return "", err
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	result, err := t.runtime.saveWorkspacePayload(t.event, payload, configToolString(input, "path"), toolInputBool(input, "overwrite"), now())
	if err != nil {
		return "", err
	}
	result.Source = source
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// saveWorkspacePayload 给一份字节定下保存路径并落盘：按内容认类型、纠正扩展名，
// 没给路径时按来源选默认目录。生图落盘也走这里。
func (r *Runtime) saveWorkspacePayload(event MessageEvent, payload workspaceMediaPayload, target string, overwrite bool, now time.Time) (saveToWorkspaceResult, error) {
	if len(payload.data) == 0 {
		return saveToWorkspaceResult{}, fmt.Errorf("取到的内容是空的，没有可保存的东西")
	}
	if len(payload.data) > agent.WorkspaceMediaMaxBytes {
		return saveToWorkspaceResult{}, fmt.Errorf("文件 %d MB，超过工作目录单文件 %d MB 上限", len(payload.data)>>20, agent.WorkspaceMediaMaxBytes>>20)
	}
	mediaType := agent.SniffMediaType(payload.data)
	target = strings.TrimSpace(strings.ReplaceAll(target, "\\", "/"))
	dir, name := "", ""
	switch {
	case target == "":
		dir = agent.WorkspaceDownloadsDir
		if payload.generated {
			dir = agent.WorkspaceOutputsDir
		}
	case strings.HasSuffix(target, "/"):
		dir = strings.TrimSuffix(target, "/")
	default:
		dir, name = path.Split(target)
		dir = strings.TrimSuffix(dir, "/")
	}
	requested := name
	if name == "" {
		name = workspaceFileName(payload, now)
	}
	name = agent.CorrectFileExtension(name, mediaType)
	rel := name
	if dir != "" && dir != "." {
		rel = dir + "/" + name
	}
	saved, err := agent.WriteWorkspaceBytes(r.agentWorkspaceConfig(event), rel, payload.data, agent.WorkspaceWriteOptions{Overwrite: overwrite})
	if err != nil {
		return saveToWorkspaceResult{}, err
	}
	result := saveToWorkspaceResult{
		Status:  "saved",
		Path:    saved,
		Bytes:   len(payload.data),
		MIME:    mediaType,
		Message: "已原样存进工作目录；path 以这里返回的为准。要发给用户用 send_attachment，这一步本身没有发送任何东西。",
	}
	if requested != "" && requested != name {
		result.ExtensionCorrected = requested + " → " + name
	}
	if strings.HasPrefix(mediaType, "image/") {
		if width, height, ok := agent.ImageDimensions(payload.data); ok {
			result.Width, result.Height = width, height
		}
	}
	return result, nil
}

// agentWorkspaceConfig 是按工作目录读写文件时判断凭据名单要用的配置：MCP 配置
// 可能被机器人配置指到别处，名单要跟着它走。
func (r *Runtime) agentWorkspaceConfig(event MessageEvent) agent.Config {
	cfg := agent.Config{WorkDir: AgentWorkspaceDir()}
	if r != nil {
		cfg.MCPConfigPath = r.effectiveConfigForEvent(event).AgentMCPConfigPath
	}
	return cfg
}

// workspaceFileName 给没有文件名的媒体起名。来源给的名字优先，但要清掉路径分隔符
// 和控制字符；图片、语音这类平台只给一串哈希当名字的，按「类型-时间」起名更好认。
func workspaceFileName(payload workspaceMediaPayload, now time.Time) string {
	if name := sanitizeWorkspaceFileName(payload.name); name != "" {
		return name
	}
	kind := firstNonEmpty(payload.kind, "file")
	return kind + "-" + now.Format("20060102-150405")
}

func sanitizeWorkspaceFileName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = path.Base(name)
	if name == "." || name == "/" {
		return ""
	}
	var builder strings.Builder
	count := 0
	for _, r := range name {
		if count >= saveWorkspaceNameMaxRunes {
			break
		}
		switch {
		case unicode.IsControl(r), strings.ContainsRune(`<>:"|?*`, r):
			builder.WriteRune('_')
		default:
			builder.WriteRune(r)
		}
		count++
	}
	cleaned := strings.Trim(builder.String(), " .")
	if cleaned == "" {
		return ""
	}
	return cleaned
}

// workspaceMCPMedia 从 MCP 暂存区取一份媒体。MCP 工具的产物算机器人自己产出的东西。
func workspaceMCPMedia(id string) (workspaceMediaPayload, error) {
	media, ok := agent.LookupMCPMedia(id)
	if !ok {
		return workspaceMediaPayload{}, fmt.Errorf("media_id 无效或已过期（只暂存 30 分钟）；重新调用对应的 MCP 工具拿新的 media_id")
	}
	return workspaceMediaPayload{data: media.Data, name: media.Name, kind: string(media.Kind), generated: true}, nil
}

// fetchWorkspaceURL 下载公网文件。复用下载缓存：同一个地址不重复下载，客户端是
// 挡内网地址的那一个，大小超限在读的过程中就截住。
func fetchWorkspaceURL(ctx context.Context, raw string) (workspaceMediaPayload, error) {
	source := normalizedHTTPURL(raw)
	if source == "" {
		return workspaceMediaPayload{}, fmt.Errorf("url 必须是 http(s) 公网地址")
	}
	parsed, _ := url.Parse(source)
	name := ""
	if parsed != nil {
		name = path.Base(parsed.Path)
	}
	callCtx, cancel := context.WithTimeout(ctx, saveWorkspaceFetchTimeout)
	defer cancel()
	cached, _, release, err := acquireMediaDownload(callCtx, netguard.NewPublicHTTPClient(saveWorkspaceFetchTimeout), source, name, "", "public", agent.WorkspaceMediaMaxBytes)
	defer release()
	if err != nil {
		return workspaceMediaPayload{}, fmt.Errorf("下载失败：%s", describeVideoContextError(err, agent.WorkspaceMediaMaxBytes))
	}
	data, err := readBoundedFile(cached, agent.WorkspaceMediaMaxBytes)
	if err != nil {
		return workspaceMediaPayload{}, err
	}
	return workspaceMediaPayload{data: data, name: name, kind: "file"}, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文件超过 %d MB 上限", limit>>20)
	}
	return data, nil
}

// chatMediaRef 是一条消息里的一个媒体段，quoted 表示它在被引用的那条消息里。
type chatMediaRef struct {
	segment MessageSegment
	quoted  bool
}

// chatMediaRefs 按出现顺序列出消息里的图片、视频、语音和文件，被引用消息里的排在后面。
func chatMediaRefs(event MessageEvent) []chatMediaRef {
	var refs []chatMediaRef
	collect := func(segments []MessageSegment, quoted bool) {
		for _, segment := range segments {
			// 视频关键帧是运行时从视频里抽出来的图片段，不是用户发的媒体，不参与编号。
			if segment.Type == "image" && strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame") {
				continue
			}
			switch segment.Type {
			case "image", "video", "record", "file":
				refs = append(refs, chatMediaRef{segment: segment, quoted: quoted})
			}
		}
	}
	collect(event.Segments, false)
	if event.Quoted != nil {
		collect(event.Quoted.Segments, true)
	}
	return refs
}

// workspaceChatMedia 取当前会话里某条消息的第 index 个媒体的原始字节。
func (r *Runtime) workspaceChatMedia(ctx context.Context, event MessageEvent, messageID string, index int) (workspaceMediaPayload, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return workspaceMediaPayload{}, fmt.Errorf("source=chat 需要 message_id")
	}
	source, found, _ := newDianaHistoryImagesTool(r, event).findSourceEvent(ctx, messageID)
	if !found {
		return workspaceMediaPayload{}, fmt.Errorf("当前会话里找不到 message_id=%s", messageID)
	}
	refs := chatMediaRefs(source)
	if len(refs) == 0 {
		return workspaceMediaPayload{}, fmt.Errorf("message_id=%s 里没有图片、视频、语音或文件", messageID)
	}
	switch {
	case index <= 0 && len(refs) == 1:
		index = 1
	case index <= 0:
		return workspaceMediaPayload{}, fmt.Errorf("message_id=%s 里有 %d 个媒体，用 media_index 指定要第几个", messageID, len(refs))
	case index > len(refs):
		return workspaceMediaPayload{}, fmt.Errorf("message_id=%s 里只有 %d 个媒体，没有第 %d 个", messageID, len(refs), index)
	}
	ref := refs[index-1]
	item := source
	if ref.quoted && source.Quoted != nil {
		item.GroupID = firstNonEmpty(source.Quoted.GroupID, source.GroupID)
		item.UserID = firstNonEmpty(source.Quoted.UserID, source.UserID)
		item.MessageID = source.Quoted.MessageID
	}
	item.Quoted = nil
	segment := ref.segment
	payload := workspaceMediaPayload{generated: !ref.quoted && r.isSelfMessage(source)}
	switch segment.Type {
	case "image":
		payload.kind = "image"
	case "video":
		payload.kind = "video"
	case "record":
		payload.kind = "audio"
	default:
		payload.kind = "file"
		payload.name = mediaSegmentName(segment)
	}
	data, err := r.readChatMediaSegment(ctx, item, segment)
	if err != nil {
		return workspaceMediaPayload{}, fmt.Errorf("message_id=%s 的第 %d 个媒体读取失败：%w", messageID, index, err)
	}
	payload.data = data
	return payload, nil
}

// readChatMediaSegment 读一个聊天媒体段的原始字节：先用已有的本地缓存，没有再按
// 平台接口补全下载地址。图片走 WebUI 预览同一套读取（缓存优先、逐个地址回退）。
func (r *Runtime) readChatMediaSegment(ctx context.Context, item MessageEvent, segment MessageSegment) ([]byte, error) {
	if segment.Type == "image" {
		if strings.EqualFold(strings.TrimSpace(segment.Data[imageUnavailableKey]), "true") && segment.Data["cached_file"] == "" {
			return nil, fmt.Errorf("原始图片已失效或无法恢复")
		}
		if data, _, err := ReadMessageImageSegment(ctx, segment); err == nil {
			return data, nil
		}
	} else if data, err := r.readChatMediaSources(ctx, item, segment); err == nil {
		return data, nil
	}
	enriched, _ := r.enrichMediaSegmentsDetailed(ctx, item, []MessageSegment{segment})
	if len(enriched) == 1 {
		segment = enriched[0]
	}
	if segment.Type == "image" {
		data, _, err := ReadMessageImageSegment(ctx, segment)
		if err != nil {
			return nil, errors.New("图片取不到了（缓存已清理、平台地址也已失效）")
		}
		return data, nil
	}
	return r.readChatMediaSources(ctx, item, segment)
}

// readChatMediaSources 按段里记录的本地路径和下载地址逐个尝试。本地路径是平台适配器
// 给的缓存位置，照样过一遍凭据名单：不能让一条消息记录把数据库「存」进工作目录。
func (r *Runtime) readChatMediaSources(ctx context.Context, item MessageEvent, segment MessageSegment) ([]byte, error) {
	cfg := r.agentWorkspaceConfig(item)
	for _, key := range []string{"cached_file", "path", "file_path", "filePath", "local_path", "localPath", "sourcePath", "source_path", "file"} {
		local := rawAbsoluteMediaPath(segment.Data[key])
		if local == "" || !usableLocalMediaPath(local) || agent.RuntimeSecretPath(cfg, local) {
			continue
		}
		if data, err := readBoundedFile(local, agent.WorkspaceMediaMaxBytes); err == nil {
			return data, nil
		}
	}
	name := firstNonEmpty(mediaSegmentName(segment), "media")
	var lastErr error
	for _, key := range []string{"url", "download_url", "file_url", "video_url", "src", "file"} {
		source := normalizedHTTPURL(segment.Data[key])
		if source == "" {
			continue
		}
		// 群文件的下载地址是平台适配器查出来的，可能就在本机（适配器自己的文件服务），
		// 和文件解析插件一样用不挡内网的客户端，并按 adapter 信任域单独建缓存索引，
		// 不和公网下载混用。视频、语音照旧走挡内网的客户端。
		client, scope := netguard.NewPublicHTTPClient(saveWorkspaceFetchTimeout), "public"
		if segment.Type == "file" {
			client, scope = &http.Client{Timeout: saveWorkspaceFetchTimeout}, "adapter"
		}
		callCtx, cancel := context.WithTimeout(ctx, saveWorkspaceFetchTimeout)
		cached, _, release, err := acquireMediaDownload(callCtx, client, source, name, platformMediaMD5(item.Platform, segment), scope, agent.WorkspaceMediaMaxBytes)
		if err == nil {
			var data []byte
			data, err = readBoundedFile(cached, agent.WorkspaceMediaMaxBytes)
			release()
			cancel()
			if err == nil {
				return data, nil
			}
		} else {
			release()
			cancel()
		}
		lastErr = err
	}
	if lastErr != nil {
		// 下载地址里常带平台的临时凭据，报错只留原因，不带地址。
		return nil, errors.New(describeVideoContextError(lastErr, agent.WorkspaceMediaMaxBytes))
	}
	return nil, errors.New("没有可用的本地缓存或下载地址")
}

// platformMediaMD5 取 OneBot 媒体段里平台给的内容摘要，下载缓存据此复用同一份字节。
func platformMediaMD5(platform string, segment MessageSegment) string {
	if platform != PlatformOneBotV11 {
		return ""
	}
	return explicitMediaMD5(segment.Data)
}
