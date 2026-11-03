// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/netguard"
)

// VideoTaskSpec 把一家厂商的异步视频接口写成数据：各家都是「提交 → 拿任务 ID →
// 轮询 → 取成品」，差别只在路径、请求体、字段位置和状态名上，所以由一个执行器照着
// 描述跑，不再每家写一个客户端。鉴权是唯一需要写代码的部分，见 VideoAuth。
type VideoTaskSpec struct {
	// BaseURL 是提供商没填地址时用的默认地址。
	BaseURL string
	Auth    VideoAuth
	// Headers 是每个请求都带的固定头，例如 Runway 的 X-Runway-Version。
	Headers map[string]string

	// Submit 是文生视频的提交；ImageSubmit 是带参考图时的提交，为空时沿用 Submit。
	Submit      VideoRequestTemplate
	ImageSubmit VideoRequestTemplate
	// IDPath 是提交响应里任务 ID 的位置。
	IDPath string

	// Status 是查询请求，模板里可以用 {{id}}。
	Status VideoRequestTemplate
	// StatusPath 是查询响应里状态值的位置，StatusMap 把状态值（不分大小写）对到统一
	// 状态；对照表里没有的交给 normalizeVideoStatus 猜。
	StatusPath string
	StatusMap  map[string]VideoJobStatus
	// VideoURLPath、ErrorPath 是成品地址和失败原因的位置。
	VideoURLPath string
	ErrorPath    string

	// CodePath 非空时，响应里这个位置的值不等于 CodeOK 就算出错，用来接住 HTTP 200
	// 里包着业务错误的厂商（可灵、MiniMax、即梦）。CodeMessagePath 是这种错误的说明。
	CodePath        string
	CodeOK          string
	CodeMessagePath string

	// Result 非空时，任务完成后还要再查一次才拿得到成品地址（MiniMax v1 先给 file_id）。
	// ResultIDPath 从查询响应里取出要代入 {{result}} 的值，ResultURLPath 是那次响应里
	// 成品地址的位置。
	Result        VideoRequestTemplate
	ResultIDPath  string
	ResultURLPath string

	// DownloadAuth 表示下载成品也要带鉴权；否则成品地址当公网直链下。
	DownloadAuth bool
}

// VideoRequestTemplate 是一次请求的模板。Path 接在服务地址后面，可以带查询串；
// Body 是 JSON 模板，变量写法见 renderVideoJSON。
type VideoRequestTemplate struct {
	Method string
	Path   string
	Body   string
}

func (t VideoRequestTemplate) empty() bool {
	return strings.TrimSpace(t.Path) == "" && strings.TrimSpace(t.Body) == ""
}

// VideoAuthKind 是鉴权插件的种类。
type VideoAuthKind string

const (
	VideoAuthBearer VideoAuthKind = "bearer"
	// VideoAuthHeader 把 Key 写进 Header 头，前面加 Prefix（Vidu 是「Token 」）。
	VideoAuthHeader VideoAuthKind = "header"
	// VideoAuthKling 在 Key 形如 AK:SK 时现签 JWT，否则当 API Key 直接 Bearer。
	VideoAuthKling VideoAuthKind = "kling"
	// VideoAuthVolcV4 用 AK:SK 做火山引擎 HMAC-SHA256 签名。
	VideoAuthVolcV4 VideoAuthKind = "volc_v4"
	VideoAuthNone   VideoAuthKind = "none"
)

// VideoAuth 描述鉴权。Region、Service 只给火山签名用。
type VideoAuth struct {
	Kind    VideoAuthKind
	Header  string
	Prefix  string
	Region  string
	Service string
}

// NewVideoTaskGenerator 按描述创建视频任务客户端。
func NewVideoTaskGenerator(cfg ProviderConfig, spec VideoTaskSpec, requestTimeout time.Duration, opts ...ClientOption) (VideoGenerator, error) {
	if requestTimeout <= 0 {
		requestTimeout = defaultVideoRequestTimeout
	}
	if strings.TrimSpace(spec.Submit.Path) == "" && strings.TrimSpace(spec.Submit.Body) == "" {
		return nil, errors.New("llm: video submit template is required")
	}
	if strings.TrimSpace(spec.IDPath) == "" || strings.TrimSpace(spec.StatusPath) == "" {
		return nil, errors.New("llm: video id path and status path are required")
	}
	if spec.Result.empty() && strings.TrimSpace(spec.VideoURLPath) == "" {
		return nil, errors.New("llm: video url path is required")
	}
	kind := spec.Auth.Kind
	if kind == "" {
		kind = VideoAuthBearer
	}
	client, err := newMediaHTTPWithKey(cfg, spec.BaseURL, opts, kind != VideoAuthNone)
	if err != nil {
		return nil, err
	}
	client.policy.AttemptTimeout = requestTimeout
	key := strings.TrimSpace(client.cfg.APIKey)
	var auth func(*http.Request)
	switch kind {
	case VideoAuthNone:
		auth = func(*http.Request) {}
	case VideoAuthBearer:
		auth = func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+key) }
	case VideoAuthHeader:
		header := firstNonEmptyString(spec.Auth.Header, "Authorization")
		if !validHeaderName(header) {
			return nil, fmt.Errorf("llm: invalid auth header %q", header)
		}
		auth = func(req *http.Request) { req.Header.Set(header, spec.Auth.Prefix+key) }
	case VideoAuthKling:
		accessKey, secretKey, pair := splitKeyPair(key)
		if !pair {
			auth = func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+key) }
			break
		}
		secretmask.Register(secretKey)
		auth = func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+klingJWT(accessKey, secretKey, videoAuthNow()))
		}
	case VideoAuthVolcV4:
		accessKey, secretKey, pair := splitKeyPair(key)
		if !pair {
			return nil, errors.New("llm: volcengine signing needs the key as AccessKeyID:SecretAccessKey")
		}
		secretmask.Register(secretKey)
		region := firstNonEmptyString(spec.Auth.Region, "cn-north-1")
		service := firstNonEmptyString(spec.Auth.Service, "cv")
		auth = func(req *http.Request) { signVolcV4(req, accessKey, secretKey, region, service, videoAuthNow()) }
	default:
		return nil, fmt.Errorf("llm: unsupported video auth %q", kind)
	}
	for name := range spec.Headers {
		if !validHeaderName(name) {
			return nil, fmt.Errorf("llm: invalid header name %q", name)
		}
	}
	client.auth = func(req *http.Request) {
		for name, value := range spec.Headers {
			req.Header.Set(name, value)
		}
		auth(req)
	}
	return &taskVideoGenerator{spec: spec, http: client, plain: plainHTTPClient(opts), public: netguard.NewPublicHTTPClient(0)}, nil
}

// videoAuthNow 是签名用的时钟，测试里换成固定时间。
var videoAuthNow = time.Now

// splitKeyPair 把「AK:SK」拆开。可灵、火山的密钥都不含冒号。
func splitKeyPair(key string) (string, string, bool) {
	accessKey, secretKey, ok := strings.Cut(key, ":")
	accessKey, secretKey = strings.TrimSpace(accessKey), strings.TrimSpace(secretKey)
	return accessKey, secretKey, ok && accessKey != "" && secretKey != ""
}

type taskVideoGenerator struct {
	spec   VideoTaskSpec
	http   *mediaHTTP
	plain  *http.Client
	public *http.Client
	// model 是提交时的模型，即梦查询时还要带上 req_key。
	model string
}

func (g *taskVideoGenerator) SubmitVideo(ctx context.Context, req VideoGenerateRequest) (VideoJob, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return VideoJob{}, errors.New("llm: video prompt is required")
	}
	vars, err := g.submitVars(ctx, req)
	if err != nil {
		return VideoJob{}, err
	}
	g.model = vars.values["model"]
	template := g.spec.Submit
	if vars.values["image"] != "" && !g.spec.ImageSubmit.empty() {
		template = g.spec.ImageSubmit
	}
	result, err := g.send(ctx, "video submit", template, vars, true)
	if err != nil {
		return VideoJob{}, err
	}
	doc, err := g.decode("video submit", result)
	if err != nil {
		return VideoJob{}, err
	}
	if err := g.businessError("video submit", result, doc); err != nil {
		return VideoJob{}, err
	}
	id := jsonPathString(doc, g.spec.IDPath)
	if id == "" {
		return VideoJob{}, &MediaAPIError{Op: "video submit", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "response has no job id"}
	}
	job := g.jobFrom(doc)
	job.ID = id
	job.Model = g.model
	if job.Status == VideoJobCompleted && job.VideoURL == "" {
		// 提交响应里的状态字段和查询响应不一定是一回事，没拿到成品就当还在跑。
		job.Status = VideoJobQueued
	}
	return job, nil
}

func (g *taskVideoGenerator) VideoStatus(ctx context.Context, id string) (VideoJob, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return VideoJob{}, errors.New("llm: video job id is required")
	}
	vars := videoVars{values: map[string]string{"id": id, "model": g.model}}
	result, err := g.send(ctx, "video status", g.spec.Status, vars, false)
	if err != nil {
		return VideoJob{}, err
	}
	doc, err := g.decode("video status", result)
	if err != nil {
		return VideoJob{}, err
	}
	if err := g.businessError("video status", result, doc); err != nil {
		var apiErr *MediaAPIError
		if errors.As(err, &apiErr) {
			// 查询时的业务错误码说的是这个任务本身（即梦 done 之后靠它区分成败）。
			return VideoJob{ID: id, Model: g.model, Status: VideoJobFailed, Error: apiErr.Detail}, nil
		}
		return VideoJob{}, err
	}
	job := g.jobFrom(doc)
	job.ID = id
	job.Model = g.model
	if job.Status == VideoJobCompleted && !g.spec.Result.empty() {
		resultID := jsonPathString(doc, g.spec.ResultIDPath)
		if resultID == "" {
			return VideoJob{}, &MediaAPIError{Op: "video status", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "completed job has no result id"}
		}
		job.VideoURL, err = g.resolveResult(ctx, resultID)
		if err != nil {
			return VideoJob{}, err
		}
	}
	return job, nil
}

func (g *taskVideoGenerator) resolveResult(ctx context.Context, resultID string) (string, error) {
	vars := videoVars{values: map[string]string{"result": resultID, "model": g.model}}
	result, err := g.send(ctx, "video result", g.spec.Result, vars, false)
	if err != nil {
		return "", err
	}
	doc, err := g.decode("video result", result)
	if err != nil {
		return "", err
	}
	if err := g.businessError("video result", result, doc); err != nil {
		return "", err
	}
	videoURL := jsonPathString(doc, g.spec.ResultURLPath)
	if videoURL == "" {
		return "", &MediaAPIError{Op: "video result", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "result has no video url"}
	}
	return videoURL, nil
}

func (g *taskVideoGenerator) VideoContent(ctx context.Context, job VideoJob) (*VideoResult, error) {
	direct := strings.TrimSpace(job.VideoURL)
	if direct == "" {
		return nil, &MediaAPIError{Op: "video content", Kind: MediaErrorBadResponse, Detail: "completed job has no video url"}
	}
	var (
		result *mediaResult
		err    error
	)
	if g.spec.DownloadAuth {
		result, err = g.http.do(ctx, mediaRequestSpec{
			op: "video content", method: http.MethodGet, endpoint: direct, accept: "video/*, application/octet-stream",
			noRetry: true, noAttemptTimeout: true,
		}, MaxVideoBytes)
	} else {
		result, err = downloadVideo(ctx, g.downloadClient, direct)
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

// downloadClient 和 openAIVideoGenerator 的一样：同源用普通客户端，其余走 netguard。
func (g *taskVideoGenerator) downloadClient(target *url.URL) *http.Client {
	if base, err := url.Parse(g.http.baseURL); err == nil && sameURLOrigin(base, target) {
		return g.plain
	}
	return g.public
}

func (g *taskVideoGenerator) send(ctx context.Context, op string, template VideoRequestTemplate, vars videoVars, createsJob bool) (*mediaResult, error) {
	endpoint, query, err := renderVideoPath(template.Path, vars)
	if err != nil {
		return nil, fmt.Errorf("llm: %s path: %w", op, err)
	}
	spec := mediaRequestSpec{op: op, method: strings.ToUpper(strings.TrimSpace(template.Method)), endpoint: endpoint, query: query, accept: "application/json", createsJob: createsJob}
	if strings.TrimSpace(template.Body) != "" {
		body, err := renderVideoJSON(template.Body, vars)
		if err != nil {
			return nil, fmt.Errorf("llm: %s body: %w", op, err)
		}
		spec.body, spec.contentType = body, "application/json"
	}
	if spec.method == "" {
		spec.method = http.MethodGet
		if spec.body != nil {
			spec.method = http.MethodPost
		}
	}
	return g.http.do(ctx, spec, maxVideoStatusBytes)
}

func (g *taskVideoGenerator) decode(op string, result *mediaResult) (any, error) {
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(result.body))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, &MediaAPIError{Op: op, Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: "invalid job json", Err: err}
	}
	return doc, nil
}

func (g *taskVideoGenerator) businessError(op string, result *mediaResult, doc any) error {
	if strings.TrimSpace(g.spec.CodePath) == "" {
		return nil
	}
	code := jsonPathString(doc, g.spec.CodePath)
	if code == "" || code == g.spec.CodeOK {
		return nil
	}
	detail := code
	if message := jsonPathString(doc, g.spec.CodeMessagePath); message != "" {
		detail += ": " + message
	}
	kind := MediaErrorInvalid
	if mediaBodyMentionsPolicy(detail) {
		kind = MediaErrorContentPolicy
	}
	return &MediaAPIError{Op: op, Kind: kind, StatusCode: result.status, Detail: detail}
}

func (g *taskVideoGenerator) jobFrom(doc any) VideoJob {
	raw := jsonPathString(doc, g.spec.StatusPath)
	status, ok := g.spec.StatusMap[strings.ToLower(raw)]
	if !ok {
		status = normalizeVideoStatus(raw)
	}
	return VideoJob{Status: status, VideoURL: jsonPathString(doc, g.spec.VideoURLPath), Error: jsonPathString(doc, g.spec.ErrorPath)}
}

// videoVars 是模板变量。image 系列是按需算的：只有模板用到 {{image_base64}} 这类
// 才去读图。
type videoVars struct {
	values map[string]string
	image  *imageEditInputData
}

func (g *taskVideoGenerator) submitVars(ctx context.Context, req VideoGenerateRequest) (videoVars, error) {
	values := map[string]string{"prompt": strings.TrimSpace(req.Prompt), "model": strings.TrimSpace(req.Model)}
	if req.Seconds > 0 {
		values["seconds"] = strconv.Itoa(req.Seconds)
		// 即梦用帧数表示时长：24 帧每秒再加首帧，5 秒是 121。
		values["frames"] = strconv.Itoa(req.Seconds*24 + 1)
	}
	size := strings.TrimSpace(req.Size)
	values["size"] = size
	width, height := parseVideoSize(size)
	if width > 0 && height > 0 {
		values["width"], values["height"] = strconv.Itoa(width), strconv.Itoa(height)
		divisor := gcd(width, height)
		values["ratio"] = fmt.Sprintf("%d:%d", width/divisor, height/divisor)
		values["resolution"] = strconv.Itoa(min(width, height)) + "p"
	}
	if ratio := strings.TrimSpace(req.Ratio); ratio != "" {
		values["ratio"] = ratio
	}
	if resolution := strings.TrimSpace(req.Resolution); resolution != "" {
		values["resolution"] = resolution
	}
	vars := videoVars{values: values}
	image := strings.TrimSpace(req.Image)
	if image == "" {
		return vars, nil
	}
	isURL := strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://")
	template := g.spec.ImageSubmit
	if template.empty() {
		template = g.spec.Submit
	}
	needsBytes := !isURL || strings.Contains(template.Body, "{{image_base64") || strings.Contains(template.Body, "{{image_mime")
	if needsBytes {
		input, err := imageEditInputFrom(ctx, newImageEditSource(g.http.cfg, g.http.client), image, 0)
		if err != nil {
			return videoVars{}, fmt.Errorf("llm: read video reference image: %w", err)
		}
		vars.image = &input
		encoded := base64.StdEncoding.EncodeToString(input.data)
		values["image_base64"] = encoded
		values["image_mime"] = input.mediaType
		if !isURL {
			values["image"] = "data:" + input.mediaType + ";base64," + encoded
			values["image_raw"] = encoded
		}
	}
	if isURL {
		values["image"], values["image_raw"] = image, image
	}
	return vars, nil
}

func parseVideoSize(size string) (int, int) {
	left, right, ok := strings.Cut(strings.ToLower(size), "x")
	if !ok {
		left, right, ok = strings.Cut(size, "*")
	}
	if !ok {
		return 0, 0
	}
	width, err1 := strconv.Atoi(strings.TrimSpace(left))
	height, err2 := strconv.Atoi(strings.TrimSpace(right))
	if err1 != nil || err2 != nil || width <= 0 || height <= 0 {
		return 0, 0
	}
	return width, height
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// lookup 取变量值，支持「名字|upper」把值转成大写（MiniMax 的分辨率写作 768P）。
func (v videoVars) lookup(expr string) string {
	name, filter, _ := strings.Cut(strings.TrimSpace(expr), "|")
	value := v.values[strings.TrimSpace(name)]
	if strings.TrimSpace(filter) == "upper" {
		value = strings.ToUpper(value)
	}
	return value
}

// renderVideoPath 代入路径模板。问号前的变量按路径段转义，问号后的按查询串转义；
// 查询串单独交给 mediaHTTP，它拼地址时会丢掉路径里的问号。
func renderVideoPath(template string, vars videoVars) (string, url.Values, error) {
	pathPart, queryPart, _ := strings.Cut(strings.TrimSpace(template), "?")
	path, err := substituteVideoVars(pathPart, vars, url.PathEscape)
	if err != nil {
		return "", nil, err
	}
	if queryPart == "" {
		return path, nil, nil
	}
	rawQuery, err := substituteVideoVars(queryPart, vars, url.QueryEscape)
	if err != nil {
		return "", nil, err
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, err
	}
	return path, query, nil
}

func substituteVideoVars(template string, vars videoVars, escape func(string) string) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(template, "{{")
		if start < 0 {
			out.WriteString(template)
			return out.String(), nil
		}
		end := strings.Index(template[start:], "}}")
		if end < 0 {
			return "", errors.New("unclosed {{")
		}
		out.WriteString(template[:start])
		out.WriteString(escape(vars.lookup(template[start+2 : start+end])))
		template = template[start+end+2:]
	}
}

// renderVideoJSON 代入 JSON 请求体模板：
//   - 写在引号里的变量按字符串代入，可以和别的文字拼，例如 "{{seconds}}s"；
//   - 不带引号的变量是数字就原样写，否则写成字符串，例如 "duration":{{seconds}}；
//   - 用到的变量为空时，整个值删掉，免得把空时长、空比例发给服务端被拒。
func renderVideoJSON(template string, vars videoVars) ([]byte, error) {
	var out strings.Builder
	for i := 0; i < len(template); {
		switch {
		case template[i] == '"':
			end := i + 1
			for end < len(template) && template[end] != '"' {
				if template[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(template) {
				return nil, errors.New("unclosed string")
			}
			literal := template[i+1 : end]
			missing := false
			rendered, err := substituteVideoVars(literal, vars, func(value string) string {
				if value == "" {
					missing = true
				}
				encoded, _ := json.Marshal(value)
				return string(encoded[1 : len(encoded)-1])
			})
			if err != nil {
				return nil, err
			}
			if missing {
				out.WriteString(videoMissing)
			} else {
				out.WriteString(`"` + rendered + `"`)
			}
			i = end + 1
		case strings.HasPrefix(template[i:], "{{"):
			end := strings.Index(template[i:], "}}")
			if end < 0 {
				return nil, errors.New("unclosed {{")
			}
			value := vars.lookup(template[i+2 : i+end])
			switch {
			case value == "":
				out.WriteString(videoMissing)
			case json.Valid([]byte(value)) && isJSONNumber(value):
				out.WriteString(value)
			default:
				encoded, _ := json.Marshal(value)
				out.Write(encoded)
			}
			i += end + 2
		default:
			out.WriteByte(template[i])
			i++
		}
	}
	var doc any
	if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
		return nil, fmt.Errorf("template is not valid json after substitution: %w", err)
	}
	return json.Marshal(dropVideoMissing(doc))
}

// videoMissing 占位「这里的变量没有值」，渲染完再把带它的字段删掉。
const videoMissing = `"\u0000diana-missing"`

func isJSONNumber(value string) bool {
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}

func dropVideoMissing(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if isVideoMissing(item) {
				delete(typed, key)
				continue
			}
			typed[key] = dropVideoMissing(item)
		}
		return typed
	case []any:
		kept := typed[:0]
		for _, item := range typed {
			if !isVideoMissing(item) {
				kept = append(kept, dropVideoMissing(item))
			}
		}
		return kept
	default:
		return value
	}
}

func isVideoMissing(value any) bool {
	text, ok := value.(string)
	return ok && text == "\u0000diana-missing"
}

// jsonPathString 按「a.b.0.c」取值，数组段可以写成「outputs[type=video]」挑第一个
// 字段匹配的元素。用「|」隔开多个路径时取第一个有值的。取不到返回空串。
func jsonPathString(doc any, paths string) string {
	for _, path := range strings.Split(paths, "|") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if value := jsonScalarString(videoJSONPath(doc, path)); value != "" {
			return value
		}
	}
	return ""
}

func videoJSONPath(doc any, path string) any {
	current := doc
	for _, segment := range strings.Split(path, ".") {
		name, filter, hasFilter := strings.Cut(segment, "[")
		if name != "" {
			switch typed := current.(type) {
			case map[string]any:
				current = typed[name]
			case []any:
				index, err := strconv.Atoi(name)
				if err != nil || index < 0 || index >= len(typed) {
					return nil
				}
				current = typed[index]
			default:
				return nil
			}
		}
		if hasFilter {
			key, want, _ := strings.Cut(strings.TrimSuffix(filter, "]"), "=")
			items, _ := current.([]any)
			current = nil
			for _, item := range items {
				if object, ok := item.(map[string]any); ok && jsonScalarString(object[key]) == want {
					current = object
					break
				}
			}
		}
		if current == nil {
			return nil
		}
	}
	return current
}

func jsonScalarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	case map[string]any, []any:
		// 错误字段有时是对象（{"code":..,"message":..}），整个给出来比丢掉强。
		encoded, _ := json.Marshal(typed)
		return videoJobErrorText(encoded)
	default:
		return fmt.Sprint(typed)
	}
}
