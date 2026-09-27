// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
	"github.com/google/uuid"
)

// VideoAPI 是视频生成接口的形态。各家视频服务的任务接口差别很大（Runway、Luma、
// Kling 的字段和状态名都不一样），所以不像语音那样按地址猜，由插槽参数显式指定；
// 新接一家就是实现一个 VideoGenerator，再在 NewVideoGenerator 里加一个分支。
type VideoAPI string

const (
	// VideoAPIOpenAI 是 OpenAI Sora 的 /videos 任务接口，也是不少聚合网关照搬的形态。
	VideoAPIOpenAI VideoAPI = "openai"
)

const (
	defaultVideoRequestTimeout = 60 * time.Second
	defaultVideoPollInterval   = 5 * time.Second
	defaultVideoJobTimeout     = 10 * time.Minute
	maxVideoStatusBytes        = 1 << 20
	// MaxVideoBytes 是成品视频的大小上限。再大的文件聊天平台也发不出去。
	MaxVideoBytes = 100 << 20
)

// VideoJobStatus 是统一后的任务状态。
type VideoJobStatus string

const (
	VideoJobQueued     VideoJobStatus = "queued"
	VideoJobInProgress VideoJobStatus = "in_progress"
	VideoJobCompleted  VideoJobStatus = "completed"
	VideoJobFailed     VideoJobStatus = "failed"
)

// VideoGenerateRequest 是一次视频生成请求。Image 非空时是图生视频。
type VideoGenerateRequest struct {
	Model  string
	Prompt string
	// Image 是参考首帧：data URL、http(s) 链接或绝对路径，和改图的原图同一套读法。
	Image string
	// Seconds 为 0 时由服务端决定时长。
	Seconds int
	// Size 形如 1280x720，为空时由服务端决定。
	Size string
}

// VideoJob 是一个异步视频任务的当前状态。
type VideoJob struct {
	ID       string
	Status   VideoJobStatus
	Progress int
	Model    string
	// Error 是任务失败时服务端给的原因。
	Error string
	// VideoURL 是部分网关在任务完成时直接给出的成品地址；为空时从内容接口取。
	VideoURL string
}

// Done 报告任务是否已经结束（成功或失败）。
func (j VideoJob) Done() bool {
	return j.Status == VideoJobCompleted || j.Status == VideoJobFailed
}

// VideoResult 是取回的成品视频。
type VideoResult struct {
	Job       VideoJob
	Video     []byte
	MediaType string
}

// VideoGenerator 是「提交 → 查状态 → 取成品」三步的任务接口。
type VideoGenerator interface {
	SubmitVideo(ctx context.Context, req VideoGenerateRequest) (VideoJob, error)
	VideoStatus(ctx context.Context, id string) (VideoJob, error)
	VideoContent(ctx context.Context, job VideoJob) (*VideoResult, error)
}

// VideoPollOptions 控制轮询。
type VideoPollOptions struct {
	// Interval 为 0 时每 5 秒查一次。
	Interval time.Duration
	// Timeout 是从提交到取回成品的总时限，为 0 时 10 分钟。
	Timeout time.Duration
	// OnProgress 在每次拿到新状态时调用，可以为空。
	OnProgress func(VideoJob)
}

// NewVideoGenerator 按接口形态创建视频任务客户端。requestTimeout 是单次 HTTP 请求的上限。
func NewVideoGenerator(cfg ProviderConfig, api VideoAPI, requestTimeout time.Duration, opts ...ClientOption) (VideoGenerator, error) {
	if requestTimeout <= 0 {
		requestTimeout = defaultVideoRequestTimeout
	}
	switch VideoAPI(strings.ToLower(strings.TrimSpace(string(api)))) {
	case "", VideoAPIOpenAI:
		client, err := newMediaHTTP(cfg, defaultOpenAIMediaBaseURL, opts)
		if err != nil {
			return nil, err
		}
		client.policy.AttemptTimeout = requestTimeout
		return &openAIVideoGenerator{http: client, plain: plainHTTPClient(opts), public: netguard.NewPublicHTTPClient(0)}, nil
	default:
		return nil, fmt.Errorf("llm: unsupported video api %q", api)
	}
}

// VideoAcceptedError 表示失败发生在服务端可能已经建好任务之后：任务受理后轮询
// 超时、任务失败、取成品失败，或者提交请求本身超时、断连（响应丢了，不知道建没建）。
// 这时任务多半已经在计费，调用方不能换一家再提交一遍。
type VideoAcceptedError struct {
	// JobID 为空表示提交结果不确定。
	JobID string
	Err   error
}

func (e *VideoAcceptedError) Error() string {
	if e.JobID == "" {
		return "video job may have been created (submit response lost): " + e.Err.Error()
	}
	return "video job " + e.JobID + ": " + e.Err.Error()
}

func (e *VideoAcceptedError) Unwrap() error { return e.Err }

// VideoJobMayBeBilled 报告错误是不是发生在任务可能已经建好之后。
func VideoJobMayBeBilled(err error) bool {
	var accepted *VideoAcceptedError
	return errors.As(err, &accepted)
}

// GenerateVideo 提交任务、轮询到结束并取回成品。提交之后的失败都包成 VideoAcceptedError。
func GenerateVideo(ctx context.Context, generator VideoGenerator, req VideoGenerateRequest, poll VideoPollOptions) (*VideoResult, error) {
	timeout := poll.Timeout
	if timeout <= 0 {
		timeout = defaultVideoJobTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// 还没发请求就已经取消的，服务端肯定没建任务。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job, err := generator.SubmitVideo(ctx, req)
	if err != nil {
		// 请求发出去之后调用方取消，和超时、断连一样不知道服务端建没建任务，按可能
		// 已计费处理。仍然包着 context.Canceled，上层「取消了不换后备」照旧生效。
		if kind := MediaErrorKindOf(err); kind == MediaErrorTimeout || kind == MediaErrorNetwork || errors.Is(err, context.Canceled) {
			return nil, &VideoAcceptedError{Err: err}
		}
		return nil, err
	}
	job, err = WaitVideo(ctx, generator, job, poll)
	if err != nil {
		return nil, &VideoAcceptedError{JobID: job.ID, Err: err}
	}
	result, err := generator.VideoContent(ctx, job)
	if err != nil {
		return nil, &VideoAcceptedError{JobID: job.ID, Err: err}
	}
	return result, nil
}

// WaitVideo 轮询到任务结束。任务失败时返回 MediaErrorJobFailed。查状态碰上瞬时故障
// 不放弃：任务还在服务端跑着，一次网关抖动就丢掉它等于白付了钱，接着查到总时限为止。
func WaitVideo(ctx context.Context, generator VideoGenerator, job VideoJob, poll VideoPollOptions) (VideoJob, error) {
	interval := poll.Interval
	if interval <= 0 {
		interval = defaultVideoPollInterval
	}
	for {
		if poll.OnProgress != nil {
			poll.OnProgress(job)
		}
		if job.Status == VideoJobCompleted {
			return job, nil
		}
		if job.Status == VideoJobFailed {
			kind := MediaErrorJobFailed
			if mediaBodyMentionsPolicy(job.Error) {
				kind = MediaErrorContentPolicy
			}
			return job, &MediaAPIError{Op: "video", Kind: kind, Detail: firstNonEmptyString(job.Error, "video job failed")}
		}
		if err := sleepContext(ctx, interval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return job, &MediaAPIError{Op: "video", Kind: MediaErrorTimeout, Detail: fmt.Sprintf("job %s still %s", job.ID, job.Status), Err: err}
			}
			return job, err
		}
		next, err := generator.VideoStatus(ctx, job.ID)
		if err != nil {
			if IsRetryableMediaError(err) && ctx.Err() == nil {
				continue
			}
			if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return job, &MediaAPIError{Op: "video", Kind: MediaErrorTimeout, Detail: fmt.Sprintf("job %s still %s", job.ID, job.Status), Err: err}
			}
			return job, err
		}
		job = next
	}
}

func plainHTTPClient(opts []ClientOption) *http.Client {
	options := clientOptions{httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(&options)
	}
	return options.httpClient
}

type openAIVideoGenerator struct {
	http *mediaHTTP
	// plain 用来下载和服务地址同源的成品直链（自建网关的文件接口），不带 API Key。
	plain *http.Client
	// public 下载其余直链：地址是服务端响应里给的，指向哪里都有可能，走 netguard
	// 拦掉内网和元数据地址，和其他插件下载外链用同一道防线。
	public *http.Client
}

type openAIVideoJob struct {
	ID       string          `json:"id"`
	Status   string          `json:"status"`
	Progress float64         `json:"progress"`
	Model    string          `json:"model"`
	Error    json.RawMessage `json:"error"`
	VideoURL string          `json:"video_url"`
	URL      string          `json:"url"`
}

func (g *openAIVideoGenerator) SubmitVideo(ctx context.Context, req VideoGenerateRequest) (VideoJob, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return VideoJob{}, errors.New("llm: video prompt is required")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := [][2]string{{"prompt", prompt}}
	if model := strings.TrimSpace(req.Model); model != "" {
		fields = append(fields, [2]string{"model", model})
	}
	if req.Seconds > 0 {
		fields = append(fields, [2]string{"seconds", strconv.Itoa(req.Seconds)})
	}
	if size := strings.TrimSpace(req.Size); size != "" {
		fields = append(fields, [2]string{"size", size})
	}
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return VideoJob{}, err
		}
	}
	if image := strings.TrimSpace(req.Image); image != "" {
		input, err := imageEditInputFrom(ctx, newImageEditSource(g.http.cfg, g.http.client), image, 0)
		if err != nil {
			return VideoJob{}, fmt.Errorf("llm: read video reference image: %w", err)
		}
		part, err := writer.CreatePart(imageEditPartHeaderForField("input_reference", input.filename, input.mediaType))
		if err != nil {
			return VideoJob{}, err
		}
		if _, err := part.Write(input.data); err != nil {
			return VideoJob{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return VideoJob{}, err
	}
	result, err := g.http.do(ctx, mediaRequestSpec{
		op: "video submit", method: http.MethodPost, endpoint: "videos",
		body: body.Bytes(), contentType: writer.FormDataContentType(), accept: "application/json",
		// 同一次提交的重试共用一个幂等键，支持它的服务端不会因为重发多建一个任务。
		headers: map[string]string{"Idempotency-Key": uuid.NewString()}, createsJob: true,
	}, maxVideoStatusBytes)
	if err != nil {
		return VideoJob{}, err
	}
	job, err := decodeOpenAIVideoJob("video submit", result)
	if err != nil {
		return VideoJob{}, err
	}
	if job.ID == "" && !job.Done() {
		return VideoJob{}, &MediaAPIError{Op: "video submit", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "response has no job id"}
	}
	return job, nil
}

func (g *openAIVideoGenerator) VideoStatus(ctx context.Context, id string) (VideoJob, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return VideoJob{}, errors.New("llm: video job id is required")
	}
	result, err := g.http.do(ctx, mediaRequestSpec{
		op: "video status", method: http.MethodGet, endpoint: "videos/" + url.PathEscape(id), accept: "application/json",
	}, maxVideoStatusBytes)
	if err != nil {
		return VideoJob{}, err
	}
	return decodeOpenAIVideoJob("video status", result)
}

func (g *openAIVideoGenerator) VideoContent(ctx context.Context, job VideoJob) (*VideoResult, error) {
	var (
		result *mediaResult
		err    error
	)
	if direct := strings.TrimSpace(job.VideoURL); direct != "" {
		result, err = downloadVideo(ctx, g.downloadClient, direct)
	} else {
		result, err = g.http.do(ctx, mediaRequestSpec{
			op: "video content", method: http.MethodGet, endpoint: "videos/" + url.PathEscape(job.ID) + "/content", accept: "video/*, application/octet-stream",
			noRetry: true, noAttemptTimeout: true,
		}, MaxVideoBytes)
	}
	if err != nil {
		return nil, err
	}
	if err := jsonErrorOn200("video content", result); err != nil {
		return nil, err
	}
	if len(result.body) == 0 {
		return nil, &MediaAPIError{Op: "video content", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "empty video"}
	}
	mediaType := strings.TrimSpace(strings.Split(result.header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(strings.ToLower(mediaType), "video/") {
		mediaType = "video/mp4"
	}
	return &VideoResult{Job: job, Video: result.body, MediaType: mediaType}, nil
}

// downloadClient 给成品直链挑客户端：和服务地址同源的用普通客户端，其余走公网客户端。
func (g *openAIVideoGenerator) downloadClient(target *url.URL) *http.Client {
	if base, err := url.Parse(g.http.baseURL); err == nil && sameURLOrigin(base, target) {
		return g.plain
	}
	return g.public
}

func downloadVideo(ctx context.Context, clientFor func(*url.URL) *http.Client, target string) (*mediaResult, error) {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, &MediaAPIError{Op: "video content", Kind: MediaErrorBadResponse, Detail: "invalid video url"}
	}
	client := clientFor(parsed)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyMediaTransportError(ctx, "video content", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &MediaAPIError{Op: "video content", Kind: classifyMediaStatus(resp.StatusCode, ""), StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxVideoBytes+1))
	if err != nil {
		return nil, classifyMediaTransportError(ctx, "video content", err)
	}
	if len(body) > MaxVideoBytes {
		return nil, &MediaAPIError{Op: "video content", Kind: MediaErrorBadResponse, Detail: fmt.Sprintf("video exceeds %d MiB", MaxVideoBytes>>20)}
	}
	return &mediaResult{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

func decodeOpenAIVideoJob(op string, result *mediaResult) (VideoJob, error) {
	var raw openAIVideoJob
	if err := json.Unmarshal(result.body, &raw); err != nil {
		return VideoJob{}, &MediaAPIError{Op: op, Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "invalid job json", Err: err}
	}
	job := VideoJob{
		ID:       strings.TrimSpace(raw.ID),
		Status:   normalizeVideoStatus(raw.Status),
		Progress: int(raw.Progress),
		Model:    strings.TrimSpace(raw.Model),
		Error:    videoJobErrorText(raw.Error),
		VideoURL: firstNonEmptyString(raw.VideoURL, raw.URL),
	}
	return job, nil
}

// normalizeVideoStatus 把各家的状态名收拢成四种。认不出的一律当「还在跑」：
// 当成失败会丢掉一个其实在正常进行的任务，当成还在跑最多是等到总时限。
func normalizeVideoStatus(status string) VideoJobStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success", "done", "finished":
		return VideoJobCompleted
	case "failed", "failure", "error", "cancelled", "canceled", "expired":
		return VideoJobFailed
	case "queued", "pending", "submitted", "":
		return VideoJobQueued
	default:
		return VideoJobInProgress
	}
}

func videoJobErrorText(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return ""
	}
	var structured struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &structured) == nil && (structured.Code != "" || structured.Message != "") {
		return strings.TrimSpace(strings.Trim(structured.Code+": "+structured.Message, ": "))
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(string(raw))
}
