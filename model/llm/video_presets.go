// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"fmt"
	"strings"
)

// 直连厂商的视频接口。每个预设的路径、字段、状态值和鉴权都对着旁边链接的官方文档
// 核对过（2026-11），文档改了就改这里的数据，执行器不用动。
const (
	VideoAPIKling     VideoAPI = "kling"
	VideoAPIJimeng    VideoAPI = "jimeng"
	VideoAPIVidu      VideoAPI = "vidu"
	VideoAPISeedance  VideoAPI = "seedance"
	VideoAPIRunway    VideoAPI = "runway"
	VideoAPILuma      VideoAPI = "luma"
	VideoAPIMiniMax   VideoAPI = "minimax"
	VideoAPIMiniMaxV2 VideoAPI = "minimax_v2"
	VideoAPICustom    VideoAPI = "custom"
)

var videoTaskPresets = map[VideoAPI]VideoTaskSpec{
	// 可灵新版接口，模型写在路径里。API Key 直接 Bearer；填 AK:SK 时按旧版规则现签 JWT。
	// https://klingai.com/document-api/api/get-started/authentication
	// https://kling.ai/document-api/api/video/2-6/text-to-video
	// https://kling.ai/document-api/api/video/2-6/image-to-video
	// https://kling.ai/document-api/api/get-started/error-codes
	VideoAPIKling: {
		BaseURL: "https://api-beijing.klingai.com",
		Auth:    VideoAuth{Kind: VideoAuthKling},
		Submit: VideoRequestTemplate{Method: "POST", Path: "text-to-video/{{model}}",
			Body: `{"prompt":"{{prompt}}","settings":{"resolution":"{{resolution}}","aspect_ratio":"{{ratio}}","duration":{{seconds}}}}`},
		// 图生视频的 url 收链接或 Base64，文档没说带不带 data: 前缀，按旧版的裸 Base64 给。
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "image-to-video/{{model}}",
			Body: `{"contents":[{"type":"prompt","text":"{{prompt}}"},{"type":"first_frame","url":"{{image_raw}}"}],"settings":{"resolution":"{{resolution}}","duration":{{seconds}}}}`},
		IDPath:     "data.id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "tasks?task_ids={{id}}"},
		StatusPath: "data.0.status",
		StatusMap: map[string]VideoJobStatus{
			"submitted": VideoJobQueued, "processing": VideoJobInProgress, "succeeded": VideoJobCompleted, "failed": VideoJobFailed,
		},
		VideoURLPath: "data.0.outputs[type=video].url",
		ErrorPath:    "data.0.message",
		CodePath:     "code", CodeOK: "0", CodeMessagePath: "message",
	},
	// 即梦 3.0（火山引擎视觉智能），文档已标「下线中」。模型填 req_key，如 jimeng_t2v_v30；
	// 时长用帧数表示。done 只说明结束了，成败看外层 code。
	// https://www.volcengine.com/docs/85621/1777001
	// https://www.volcengine.com/docs/85621/1792704
	// https://www.volcengine.com/docs/6369/67269（签名）
	VideoAPIJimeng: {
		BaseURL: "https://visual.volcengineapi.com",
		Auth:    VideoAuth{Kind: VideoAuthVolcV4, Region: "cn-north-1", Service: "cv"},
		Submit: VideoRequestTemplate{Method: "POST", Path: "?Action=CVSync2AsyncSubmitTask&Version=2022-08-31",
			Body: `{"req_key":"{{model}}","prompt":"{{prompt}}","frames":{{frames}},"aspect_ratio":"{{ratio}}"}`},
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "?Action=CVSync2AsyncSubmitTask&Version=2022-08-31",
			Body: `{"req_key":"{{model}}","prompt":"{{prompt}}","frames":{{frames}},"binary_data_base64":["{{image_base64}}"]}`},
		IDPath: "data.task_id",
		Status: VideoRequestTemplate{Method: "POST", Path: "?Action=CVSync2AsyncGetResult&Version=2022-08-31",
			Body: `{"req_key":"{{model}}","task_id":"{{id}}"}`},
		StatusPath: "data.status",
		StatusMap: map[string]VideoJobStatus{
			"in_queue": VideoJobQueued, "generating": VideoJobInProgress, "done": VideoJobCompleted,
			"not_found": VideoJobFailed, "expired": VideoJobFailed,
		},
		VideoURLPath: "data.video_url",
		ErrorPath:    "message",
		CodePath:     "code", CodeOK: "10000", CodeMessagePath: "message|ResponseMetadata.Error.Message",
	},
	// Vidu 国内站；海外站地址是 https://api.vidu.com。鉴权头是「Token 」不是 Bearer。
	// https://platform.vidu.com/docs/api-reference/video-models/vidu-q3/text-to-video
	// https://platform.vidu.com/docs/api-reference/video-models/vidu-q3/image-to-video
	// https://platform.vidu.com/docs/api-reference/task-management/task-detail
	// https://docs.platform.vidu.cn/331844698e0
	VideoAPIVidu: {
		BaseURL: "https://api.vidu.cn",
		Auth:    VideoAuth{Kind: VideoAuthHeader, Header: "Authorization", Prefix: "Token "},
		Submit: VideoRequestTemplate{Method: "POST", Path: "ent/v2/text2video",
			Body: `{"model":"{{model}}","prompt":"{{prompt}}","duration":{{seconds}},"aspect_ratio":"{{ratio}}","resolution":"{{resolution}}"}`},
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "ent/v2/img2video",
			Body: `{"model":"{{model}}","prompt":"{{prompt}}","images":["{{image}}"],"duration":{{seconds}},"resolution":"{{resolution}}"}`},
		IDPath:     "task_id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "ent/v2/tasks/{{id}}/creations"},
		StatusPath: "state",
		StatusMap: map[string]VideoJobStatus{
			"created": VideoJobQueued, "queueing": VideoJobQueued, "processing": VideoJobInProgress, "success": VideoJobCompleted, "failed": VideoJobFailed,
		},
		VideoURLPath: "creations.0.url",
		ErrorPath:    "err_code",
	},
	// 火山方舟 Seedance；BytePlus 海外站是 https://ark.ap-southeast.bytepluses.com/api/v3。
	// https://docs.volcengine.com/docs/zh/ark/create-video-generation-task-api.md
	// https://docs.volcengine.com/docs/zh/ark/get-video-generation-task-api.md
	// https://docs.volcengine.com/docs/zh/ark/base-url-and-authentication.md
	VideoAPISeedance: {
		BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Auth:    VideoAuth{Kind: VideoAuthBearer},
		Submit: VideoRequestTemplate{Method: "POST", Path: "contents/generations/tasks",
			Body: `{"model":"{{model}}","content":[{"type":"text","text":"{{prompt}}"}],"ratio":"{{ratio}}","resolution":"{{resolution}}","duration":{{seconds}}}`},
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "contents/generations/tasks",
			Body: `{"model":"{{model}}","content":[{"type":"text","text":"{{prompt}}"},{"type":"image_url","image_url":{"url":"{{image}}"},"role":"first_frame"}],"resolution":"{{resolution}}","duration":{{seconds}}}`},
		IDPath:     "id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "contents/generations/tasks/{{id}}"},
		StatusPath: "status",
		StatusMap: map[string]VideoJobStatus{
			"queued": VideoJobQueued, "running": VideoJobInProgress, "succeeded": VideoJobCompleted,
			"failed": VideoJobFailed, "cancelled": VideoJobFailed, "expired": VideoJobFailed,
		},
		VideoURLPath: "content.video_url",
		ErrorPath:    "error.message|error.code",
	},
	// Runway。版本头必须带，目前只认 2024-11-06；ratio 写成 1280:720。
	// https://docs.dev.runwayml.com/api/
	// https://docs.dev.runwayml.com/api-details/versioning/
	// https://docs.dev.runwayml.com/errors/task-failures.md
	VideoAPIRunway: {
		BaseURL: "https://api.dev.runwayml.com",
		Auth:    VideoAuth{Kind: VideoAuthBearer},
		Headers: map[string]string{"X-Runway-Version": "2024-11-06"},
		Submit: VideoRequestTemplate{Method: "POST", Path: "v1/text_to_video",
			Body: `{"model":"{{model}}","promptText":"{{prompt}}","ratio":"{{width}}:{{height}}","duration":{{seconds}}}`},
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "v1/image_to_video",
			Body: `{"model":"{{model}}","promptText":"{{prompt}}","promptImage":"{{image}}","ratio":"{{width}}:{{height}}","duration":{{seconds}}}`},
		IDPath:     "id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "v1/tasks/{{id}}"},
		StatusPath: "status",
		StatusMap: map[string]VideoJobStatus{
			"pending": VideoJobQueued, "throttled": VideoJobQueued, "running": VideoJobInProgress,
			"succeeded": VideoJobCompleted, "failed": VideoJobFailed, "cancelled": VideoJobFailed,
		},
		VideoURLPath: "output.0",
		ErrorPath:    "failure|failureCode",
	},
	// Luma Agents API（取代 Dream Machine API，旧模型 ray-2 不再收）。时长写成 "5s"。
	// https://docs.agents.lumalabs.ai/guides/videos/generation/
	// https://docs.agents.lumalabs.ai/guides/videos/migration/
	VideoAPILuma: {
		BaseURL: "https://agents.lumalabs.ai/v1",
		Auth:    VideoAuth{Kind: VideoAuthBearer},
		Submit: VideoRequestTemplate{Method: "POST", Path: "generations",
			Body: `{"model":"{{model}}","type":"video","prompt":"{{prompt}}","aspect_ratio":"{{ratio}}","video":{"resolution":"{{resolution}}","duration":"{{seconds}}s"}}`},
		// start_frame 的 url 要公网链接，统一传图片内容最稳。
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "generations",
			Body: `{"model":"{{model}}","type":"video","prompt":"{{prompt}}","aspect_ratio":"{{ratio}}","video":{"resolution":"{{resolution}}","duration":"{{seconds}}s","start_frame":{"data":"{{image_base64}}","media_type":"{{image_mime}}"}}}`},
		IDPath:     "id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "generations/{{id}}"},
		StatusPath: "state",
		StatusMap: map[string]VideoJobStatus{
			"queued": VideoJobQueued, "processing": VideoJobInProgress, "completed": VideoJobCompleted, "failed": VideoJobFailed,
		},
		VideoURLPath: "output[type=video].url",
		ErrorPath:    "failure_reason|failure_code",
	},
	// MiniMax 海螺 v1 接口，国内站；海外站是 https://api.minimax.io。完成后只给
	// file_id，要再查一次文件接口才有下载地址。分辨率写作 768P。
	// https://platform.minimax.io/docs/api-reference/video-generation-t2v
	// https://platform.minimax.io/docs/api-reference/video-generation-query
	// https://platform.minimax.io/docs/api-reference/video-generation-download
	VideoAPIMiniMax: {
		BaseURL: "https://api.minimax.cn",
		Auth:    VideoAuth{Kind: VideoAuthBearer},
		Submit: VideoRequestTemplate{Method: "POST", Path: "v1/video_generation",
			Body: `{"model":"{{model}}","prompt":"{{prompt}}","duration":{{seconds}},"resolution":"{{resolution|upper}}"}`},
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "v1/video_generation",
			Body: `{"model":"{{model}}","prompt":"{{prompt}}","first_frame_image":"{{image}}","duration":{{seconds}},"resolution":"{{resolution|upper}}"}`},
		IDPath:     "task_id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "v1/query/video_generation?task_id={{id}}"},
		StatusPath: "status",
		StatusMap: map[string]VideoJobStatus{
			"preparing": VideoJobQueued, "queueing": VideoJobQueued, "processing": VideoJobInProgress, "success": VideoJobCompleted, "fail": VideoJobFailed,
		},
		CodePath: "base_resp.status_code", CodeOK: "0", CodeMessagePath: "base_resp.status_msg",
		Result:        VideoRequestTemplate{Method: "GET", Path: "v1/files/retrieve?file_id={{result}}"},
		ResultIDPath:  "file_id",
		ResultURLPath: "file.download_url",
	},
	// MiniMax v2 接口（H3 系列），分辨率和时长必填，分辨率是 480P / 768P / 2K。
	// https://platform.minimax.io/docs/api-reference/video-generation-v2-create.md
	// https://platform.minimax.io/docs/api-reference/video-generation-v2-query.md
	VideoAPIMiniMaxV2: {
		BaseURL: "https://api.minimax.cn",
		Auth:    VideoAuth{Kind: VideoAuthBearer},
		Submit: VideoRequestTemplate{Method: "POST", Path: "v2/video_generation",
			Body: `{"model":"{{model}}","content":[{"type":"text","text":"{{prompt}}"}],"resolution":"{{resolution|upper}}","duration":{{seconds}},"ratio":"{{ratio}}"}`},
		ImageSubmit: VideoRequestTemplate{Method: "POST", Path: "v2/video_generation",
			Body: `{"model":"{{model}}","content":[{"type":"text","text":"{{prompt}}"},{"type":"image_url","image_url":{"url":"{{image}}"},"role":"first_frame"}],"resolution":"{{resolution|upper}}","duration":{{seconds}},"ratio":"{{ratio}}"}`},
		IDPath:     "task_id",
		Status:     VideoRequestTemplate{Method: "GET", Path: "v2/query/video_generation/{{id}}"},
		StatusPath: "task.status",
		StatusMap: map[string]VideoJobStatus{
			"queued": VideoJobQueued, "running": VideoJobInProgress, "succeeded": VideoJobCompleted, "failed": VideoJobFailed, "cancelled": VideoJobFailed,
		},
		VideoURLPath: "task.content.url",
		ErrorPath:    "task.error.message|task.error.code",
	},
}

// VideoTaskPreset 返回内置预设的副本。
func VideoTaskPreset(api VideoAPI) (VideoTaskSpec, bool) {
	spec, ok := videoTaskPresets[api]
	if !ok {
		return VideoTaskSpec{}, false
	}
	statusMap := make(map[string]VideoJobStatus, len(spec.StatusMap))
	for key, value := range spec.StatusMap {
		statusMap[key] = value
	}
	spec.StatusMap = statusMap
	headers := make(map[string]string, len(spec.Headers))
	for key, value := range spec.Headers {
		headers[key] = value
	}
	spec.Headers = headers
	return spec, true
}

// VideoTaskSpecFromParams 用插槽参数拼「自定义」格式。template 指定一个预设当底，
// 其余填了的字段覆盖它，键名和界面上的一一对应。
func VideoTaskSpecFromParams(params map[string]string) (VideoTaskSpec, error) {
	get := func(key string) string { return strings.TrimSpace(params[key]) }
	var spec VideoTaskSpec
	if template := get("template"); template != "" {
		preset, ok := VideoTaskPreset(VideoAPI(template))
		if !ok {
			return VideoTaskSpec{}, fmt.Errorf("llm: unknown video template %q", template)
		}
		spec = preset
	}
	if spec.StatusMap == nil {
		spec.StatusMap = map[string]VideoJobStatus{}
	}
	if spec.Headers == nil {
		spec.Headers = map[string]string{}
	}
	set := func(target *string, key string) {
		if value := get(key); value != "" {
			*target = value
		}
	}
	set(&spec.Submit.Method, "submit_method")
	set(&spec.Submit.Path, "submit_path")
	set(&spec.Submit.Body, "submit_body")
	set(&spec.ImageSubmit.Path, "image_submit_path")
	set(&spec.ImageSubmit.Body, "image_submit_body")
	if spec.ImageSubmit.Method == "" && !spec.ImageSubmit.empty() {
		spec.ImageSubmit.Method = spec.Submit.Method
	}
	set(&spec.IDPath, "id_path")
	set(&spec.Status.Method, "status_method")
	set(&spec.Status.Path, "status_path")
	set(&spec.Status.Body, "status_body")
	set(&spec.StatusPath, "status_field")
	set(&spec.VideoURLPath, "video_url_path")
	set(&spec.ErrorPath, "error_path")
	set(&spec.CodePath, "code_path")
	set(&spec.CodeOK, "code_ok")
	set(&spec.CodeMessagePath, "code_message_path")
	if value := get("status_map"); value != "" {
		statusMap, err := parseVideoStatusMap(value)
		if err != nil {
			return VideoTaskSpec{}, err
		}
		spec.StatusMap = statusMap
	}
	if value := get("auth"); value != "" {
		spec.Auth = VideoAuth{Kind: VideoAuthKind(value)}
	}
	set(&spec.Auth.Header, "auth_header")
	if prefix := get("auth_prefix"); prefix != "" {
		// 界面会去掉首尾空格，前缀和 Key 之间的空格在这里补上（「Token xxx」）。
		spec.Auth.Prefix = prefix + " "
	}
	switch get("download_auth") {
	case "true":
		spec.DownloadAuth = true
	case "false":
		spec.DownloadAuth = false
	}
	return spec, nil
}

// parseVideoStatusMap 读「厂商状态=统一状态」的对照表，逗号或换行隔开，
// 统一状态是 queued、in_progress、completed、failed 之一。
func parseVideoStatusMap(text string) (map[string]VideoJobStatus, error) {
	statusMap := map[string]VideoJobStatus{}
	for _, entry := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' || r == '，' }) {
		raw, status, ok := strings.Cut(entry, "=")
		raw, status = strings.ToLower(strings.TrimSpace(raw)), strings.TrimSpace(status)
		if !ok || raw == "" {
			return nil, fmt.Errorf("llm: invalid video status map entry %q", entry)
		}
		switch VideoJobStatus(status) {
		case VideoJobQueued, VideoJobInProgress, VideoJobCompleted, VideoJobFailed:
			statusMap[raw] = VideoJobStatus(status)
		default:
			return nil, fmt.Errorf("llm: unknown video status %q (want queued, in_progress, completed or failed)", status)
		}
	}
	return statusMap, nil
}
