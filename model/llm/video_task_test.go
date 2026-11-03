// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// videoVendorCase 模拟一家厂商：照官方文档的路径和字段回响应，检查我们发出去的请求。
type videoVendorCase struct {
	api   VideoAPI
	key   string
	model string
	// checkAuth 检查每个请求的鉴权头。
	checkAuth func(t *testing.T, r *http.Request)
	// submit 认出提交请求并检查请求体，image 表示这次带参考图。
	submit func(t *testing.T, r *http.Request, body map[string]any, image bool) bool
	// submitResponse 是受理后的响应。
	submitResponse string
	// status 认出查询请求。
	status func(r *http.Request, body map[string]any) bool
	// running、done、failed 是三种查询响应，done 里的 {{url}} 换成成品地址。
	running, done, failed string
	// result 非空时认出取成品地址的那一步，响应里的 {{url}} 同样替换。
	result         func(r *http.Request) bool
	resultResponse string
	// failedMessage 是失败时报错里应该带的原因。
	failedMessage string
}

func videoVendorCases() []videoVendorCase {
	bearer := func(want string) func(*testing.T, *http.Request) {
		return func(t *testing.T, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != want {
				t.Errorf("%s %s Authorization = %q, want %q", r.Method, r.URL.Path, got, want)
			}
		}
	}
	return []videoVendorCase{
		{
			api: VideoAPIKling, key: "kling-key", model: "kling-2.6", checkAuth: bearer("Bearer kling-key"),
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if image {
					if r.URL.Path != "/image-to-video/kling-2.6" {
						return false
					}
					contents := body["contents"].([]any)
					frame := contents[1].(map[string]any)
					if frame["type"] != "first_frame" || strings.HasPrefix(frame["url"].(string), "data:") || frame["url"] == "" {
						t.Errorf("first frame = %#v", frame)
					}
					return true
				}
				if r.URL.Path != "/text-to-video/kling-2.6" {
					return false
				}
				settings := body["settings"].(map[string]any)
				if body["prompt"] != "一只猫" || settings["resolution"] != "720p" || settings["aspect_ratio"] != "16:9" || settings["duration"] != float64(5) {
					t.Errorf("body = %#v", body)
				}
				return true
			},
			submitResponse: `{"code":0,"message":"SUCCEED","data":{"id":"task-1","status":"submitted"}}`,
			status: func(r *http.Request, _ map[string]any) bool {
				return r.Method == http.MethodGet && r.URL.Path == "/tasks" && r.URL.Query().Get("task_ids") == "task-1"
			},
			running:       `{"code":0,"data":[{"id":"task-1","status":"processing"}]}`,
			done:          `{"code":0,"data":[{"id":"task-1","status":"succeeded","outputs":[{"type":"image","url":"wrong"},{"type":"video","url":"{{url}}"}]}]}`,
			failed:        `{"code":0,"data":[{"id":"task-1","status":"failed","message":"render crashed"}]}`,
			failedMessage: "render crashed",
		},
		{
			api: VideoAPIJimeng, key: "AKTEST:SKTEST", model: "jimeng_t2v_v30",
			checkAuth: func(t *testing.T, r *http.Request) {
				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "HMAC-SHA256 Credential=AKTEST/") || !strings.Contains(auth, "/cn-north-1/cv/request, SignedHeaders=host;x-date, Signature=") || r.Header.Get("X-Date") == "" {
					t.Errorf("Authorization = %q", auth)
				}
			},
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if r.URL.Query().Get("Action") != "CVSync2AsyncSubmitTask" || r.URL.Query().Get("Version") != "2022-08-31" {
					return false
				}
				if body["req_key"] != "jimeng_t2v_v30" || body["frames"] != float64(121) {
					t.Errorf("body = %#v", body)
				}
				if image {
					images := body["binary_data_base64"].([]any)
					if len(images) != 1 || strings.HasPrefix(images[0].(string), "data:") {
						t.Errorf("images = %#v", images)
					}
				} else if body["aspect_ratio"] != "16:9" {
					t.Errorf("aspect_ratio = %#v", body["aspect_ratio"])
				}
				return true
			},
			submitResponse: `{"code":10000,"message":"Success","data":{"task_id":"task-1"}}`,
			status: func(r *http.Request, body map[string]any) bool {
				return r.URL.Query().Get("Action") == "CVSync2AsyncGetResult" && body["task_id"] == "task-1" && body["req_key"] == "jimeng_t2v_v30"
			},
			running: `{"code":10000,"message":"Success","data":{"status":"generating"}}`,
			done:    `{"code":10000,"message":"Success","data":{"status":"done","video_url":"{{url}}"}}`,
			// done 也可能是失败，要看外层 code。
			failed:        `{"code":50411,"message":"Pre Img Risk Not Pass","data":{"status":"done"}}`,
			failedMessage: "50411: Pre Img Risk Not Pass",
		},
		{
			api: VideoAPIVidu, key: "vidu-key", model: "viduq3-pro", checkAuth: bearer("Token vidu-key"),
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if image {
					if r.URL.Path != "/ent/v2/img2video" {
						return false
					}
					images := body["images"].([]any)
					if len(images) != 1 || !strings.HasPrefix(images[0].(string), "data:image/png;base64,") {
						t.Errorf("images = %#v", images)
					}
					return true
				}
				if r.URL.Path != "/ent/v2/text2video" {
					return false
				}
				if body["model"] != "viduq3-pro" || body["duration"] != float64(5) || body["aspect_ratio"] != "16:9" || body["resolution"] != "720p" {
					t.Errorf("body = %#v", body)
				}
				return true
			},
			submitResponse: `{"task_id":"task-1","state":"created"}`,
			status:         func(r *http.Request, _ map[string]any) bool { return r.URL.Path == "/ent/v2/tasks/task-1/creations" },
			running:        `{"id":"task-1","state":"processing"}`,
			done:           `{"id":"task-1","state":"success","creations":[{"id":"c1","url":"{{url}}"}]}`,
			failed:         `{"id":"task-1","state":"failed","err_code":"TaskPromptPolicyViolation"}`,
			failedMessage:  "TaskPromptPolicyViolation",
		},
		{
			api: VideoAPISeedance, key: "ark-key", model: "doubao-seedance-1-0-pro-250528", checkAuth: bearer("Bearer ark-key"),
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if r.URL.Path != "/api/v3/contents/generations/tasks" || r.Method != http.MethodPost {
					return false
				}
				content := body["content"].([]any)
				if image {
					item := content[1].(map[string]any)
					if item["role"] != "first_frame" || !strings.HasPrefix(item["image_url"].(map[string]any)["url"].(string), "data:image/png;base64,") {
						t.Errorf("image item = %#v", item)
					}
				} else if len(content) != 1 || body["ratio"] != "16:9" || body["duration"] != float64(5) {
					t.Errorf("body = %#v", body)
				}
				return true
			},
			submitResponse: `{"id":"task-1"}`,
			status: func(r *http.Request, _ map[string]any) bool {
				return r.Method == http.MethodGet && r.URL.Path == "/api/v3/contents/generations/tasks/task-1"
			},
			running:       `{"id":"task-1","status":"running"}`,
			done:          `{"id":"task-1","status":"succeeded","content":{"video_url":"{{url}}"}}`,
			failed:        `{"id":"task-1","status":"failed","error":{"code":"OutputVideoSensitiveContentDetected","message":"output blocked"}}`,
			failedMessage: "output blocked",
		},
		{
			api: VideoAPIRunway, key: "rw-key", model: "gen4.5",
			checkAuth: func(t *testing.T, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer rw-key" || r.Header.Get("X-Runway-Version") != "2024-11-06" {
					t.Errorf("headers = %v", r.Header)
				}
			},
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				want := "/v1/text_to_video"
				if image {
					want = "/v1/image_to_video"
				}
				if r.URL.Path != want {
					return false
				}
				if body["promptText"] != "一只猫" || body["ratio"] != "1280:720" || body["duration"] != float64(5) {
					t.Errorf("body = %#v", body)
				}
				if image && !strings.HasPrefix(body["promptImage"].(string), "data:image/png;base64,") {
					t.Errorf("promptImage = %#v", body["promptImage"])
				}
				return true
			},
			submitResponse: `{"id":"task-1"}`,
			status:         func(r *http.Request, _ map[string]any) bool { return r.URL.Path == "/v1/tasks/task-1" },
			running:        `{"id":"task-1","status":"THROTTLED"}`,
			done:           `{"id":"task-1","status":"SUCCEEDED","output":["{{url}}"]}`,
			failed:         `{"id":"task-1","status":"FAILED","failure":"Input image flagged","failureCode":"SAFETY.INPUT.IMAGE"}`,
			failedMessage:  "Input image flagged",
		},
		{
			api: VideoAPILuma, key: "luma-api-key", model: "ray-3.2", checkAuth: bearer("Bearer luma-api-key"),
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if r.URL.Path != "/v1/generations" || r.Method != http.MethodPost {
					return false
				}
				video := body["video"].(map[string]any)
				if body["type"] != "video" || body["aspect_ratio"] != "16:9" || video["duration"] != "5s" || video["resolution"] != "720p" {
					t.Errorf("body = %#v", body)
				}
				if image {
					frame := video["start_frame"].(map[string]any)
					if frame["media_type"] != "image/png" || frame["data"] == "" {
						t.Errorf("start_frame = %#v", frame)
					}
				}
				return true
			},
			submitResponse: `{"id":"task-1","state":"queued"}`,
			status: func(r *http.Request, _ map[string]any) bool {
				return r.Method == http.MethodGet && r.URL.Path == "/v1/generations/task-1"
			},
			running:       `{"id":"task-1","state":"processing"}`,
			done:          `{"id":"task-1","state":"completed","output":[{"type":"video","url":"{{url}}"}]}`,
			failed:        `{"id":"task-1","state":"failed","failure_reason":"prompt rejected"}`,
			failedMessage: "prompt rejected",
		},
		{
			api: VideoAPIMiniMax, key: "mm-key", model: "MiniMax-Hailuo-2.3", checkAuth: bearer("Bearer mm-key"),
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if r.URL.Path != "/v1/video_generation" {
					return false
				}
				if body["resolution"] != "720P" || body["duration"] != float64(5) {
					t.Errorf("body = %#v", body)
				}
				if image && !strings.HasPrefix(body["first_frame_image"].(string), "data:image/png;base64,") {
					t.Errorf("first_frame_image = %#v", body["first_frame_image"])
				}
				return true
			},
			submitResponse: `{"task_id":"task-1","base_resp":{"status_code":0,"status_msg":"success"}}`,
			status: func(r *http.Request, _ map[string]any) bool {
				return r.URL.Path == "/v1/query/video_generation" && r.URL.Query().Get("task_id") == "task-1"
			},
			running:       `{"task_id":"task-1","status":"Processing","base_resp":{"status_code":0}}`,
			done:          `{"task_id":"task-1","status":"Success","file_id":"200001","base_resp":{"status_code":0}}`,
			failed:        `{"task_id":"task-1","status":"Fail","base_resp":{"status_code":1027,"status_msg":"output video sensitive"}}`,
			failedMessage: "1027: output video sensitive",
			result: func(r *http.Request) bool {
				return r.URL.Path == "/v1/files/retrieve" && r.URL.Query().Get("file_id") == "200001"
			},
			resultResponse: `{"file":{"file_id":200001,"download_url":"{{url}}"},"base_resp":{"status_code":0}}`,
		},
		{
			api: VideoAPIMiniMaxV2, key: "mm-key", model: "MiniMax-H3", checkAuth: bearer("Bearer mm-key"),
			submit: func(t *testing.T, r *http.Request, body map[string]any, image bool) bool {
				if r.URL.Path != "/v2/video_generation" {
					return false
				}
				content := body["content"].([]any)
				if body["ratio"] != "16:9" || body["resolution"] != "720P" || content[0].(map[string]any)["text"] != "一只猫" {
					t.Errorf("body = %#v", body)
				}
				if image && content[1].(map[string]any)["role"] != "first_frame" {
					t.Errorf("content = %#v", content)
				}
				return true
			},
			submitResponse: `{"task_id":"task-1"}`,
			status:         func(r *http.Request, _ map[string]any) bool { return r.URL.Path == "/v2/query/video_generation/task-1" },
			running:        `{"task":{"status":"running"}}`,
			done:           `{"task":{"status":"succeeded","content":{"url":"{{url}}"}}}`,
			failed:         `{"task":{"status":"failed","error":{"code":"2013","message":"bad prompt"}}}`,
			failedMessage:  "bad prompt",
		},
	}
}

const videoTestPixel = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg=="

// fakeVideoVendor 起一个照 tc 回响应的服务。outcome 是任务最后的结局：done 或 failed。
func fakeVideoVendor(t *testing.T, tc videoVendorCase, outcome string, image bool) (*httptest.Server, *atomic.Int32) {
	var polls, downloads atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		videoURL := server.URL + "/files/out.mp4"
		if r.URL.Path == "/files/out.mp4" {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("video download must not carry credentials")
			}
			downloads.Add(1)
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("mp4-" + string(tc.api)))
			return
		}
		tc.checkAuth(t, r)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body is not json: %s", raw)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case tc.result != nil && tc.result(r):
			_, _ = w.Write([]byte(strings.ReplaceAll(tc.resultResponse, "{{url}}", videoURL)))
		case tc.status(r, body):
			if polls.Add(1) == 1 {
				_, _ = w.Write([]byte(tc.running))
				return
			}
			response := tc.done
			if outcome == "failed" {
				response = tc.failed
			}
			_, _ = w.Write([]byte(strings.ReplaceAll(response, "{{url}}", videoURL)))
		case r.Method == http.MethodPost && tc.submit(t, r, body, image):
			_, _ = w.Write([]byte(tc.submitResponse))
		default:
			t.Errorf("unexpected %s %s %s", r.Method, r.URL.String(), raw)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &downloads
}

func videoPresetTestConfig(t *testing.T, tc videoVendorCase, serverURL string) ProviderConfig {
	spec, _ := VideoTaskPreset(tc.api)
	base, err := url.Parse(spec.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// 保留预设地址的路径（/api/v3、/v1），只把主机换成模拟服务。
	return ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: tc.key, BaseURL: serverURL + base.Path}
}

func TestVideoPresetsSubmitPollDownload(t *testing.T) {
	for _, tc := range videoVendorCases() {
		for _, image := range []bool{false, true} {
			name := string(tc.api)
			if image {
				name += "/image"
			}
			t.Run(name, func(t *testing.T) {
				server, downloads := fakeVideoVendor(t, tc, "done", image)
				generator, err := NewVideoGenerator(videoPresetTestConfig(t, tc, server.URL), tc.api, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				req := VideoGenerateRequest{Model: tc.model, Prompt: "一只猫", Seconds: 5, Size: "1280x720"}
				if image {
					req.Image = "data:image/png;base64," + videoTestPixel
				}
				var seen []VideoJobStatus
				result, err := GenerateVideo(context.Background(), generator, req, VideoPollOptions{Interval: time.Millisecond, OnProgress: func(job VideoJob) { seen = append(seen, job.Status) }})
				if err != nil {
					t.Fatal(err)
				}
				if string(result.Video) != "mp4-"+string(tc.api) || result.MediaType != "video/mp4" || result.Job.ID != "task-1" {
					t.Fatalf("result = %#v", result)
				}
				if downloads.Load() != 1 {
					t.Fatalf("downloads = %d", downloads.Load())
				}
				if len(seen) < 3 || seen[1] == VideoJobCompleted || seen[len(seen)-1] != VideoJobCompleted {
					t.Fatalf("progress = %v", seen)
				}
			})
		}
	}
}

func TestVideoPresetsReportJobFailure(t *testing.T) {
	for _, tc := range videoVendorCases() {
		t.Run(string(tc.api), func(t *testing.T) {
			server, downloads := fakeVideoVendor(t, tc, "failed", false)
			generator, err := NewVideoGenerator(videoPresetTestConfig(t, tc, server.URL), tc.api, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = GenerateVideo(context.Background(), generator, VideoGenerateRequest{Model: tc.model, Prompt: "一只猫", Seconds: 5, Size: "1280x720"}, VideoPollOptions{Interval: time.Millisecond})
			if MediaErrorKindOf(err) != MediaErrorJobFailed || !strings.Contains(err.Error(), tc.failedMessage) {
				t.Fatalf("err = %v (kind %q), want job failure mentioning %q", err, MediaErrorKindOf(err), tc.failedMessage)
			}
			if !VideoJobMayBeBilled(err) || downloads.Load() != 0 {
				t.Fatalf("billed=%v downloads=%d", VideoJobMayBeBilled(err), downloads.Load())
			}
		})
	}
}

// HTTP 200 里包着业务错误码的提交没建任务，可以换后备。
func TestVideoTaskSubmitBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":1201,"message":"model_name is invalid","data":null}`))
	}))
	defer server.Close()
	generator, _ := NewVideoGenerator(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL}, VideoAPIKling, time.Second)
	_, err := GenerateVideo(context.Background(), generator, VideoGenerateRequest{Model: "nope", Prompt: "x"}, VideoPollOptions{Interval: time.Millisecond})
	if err == nil || VideoJobMayBeBilled(err) || !strings.Contains(err.Error(), "1201: model_name is invalid") {
		t.Fatalf("err = %v", err)
	}
}

func TestVideoTaskDownloadAuthCarriesKey(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("%s missing auth", r.URL.Path)
		}
		switch r.URL.Path {
		case "/jobs":
			_, _ = w.Write([]byte(`{"job":{"id":"j1"}}`))
		case "/jobs/j1":
			_, _ = w.Write([]byte(`{"state":"ok","link":"` + server.URL + `/blob"}`))
		case "/blob":
			_, _ = w.Write([]byte("private-video"))
		}
	}))
	defer server.Close()
	spec, err := VideoTaskSpecFromParams(map[string]string{
		"submit_path": "jobs", "submit_body": `{"text":"{{prompt}}","len":{{seconds}}}`, "id_path": "job.id",
		"status_path": "jobs/{{id}}", "status_field": "state", "status_map": "ok=completed, bad=failed",
		"video_url_path": "link", "download_auth": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewVideoTaskGenerator(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", BaseURL: server.URL}, spec, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := GenerateVideo(context.Background(), generator, VideoGenerateRequest{Prompt: "x"}, VideoPollOptions{Interval: time.Millisecond})
	if err != nil || string(result.Video) != "private-video" {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestVideoTaskSpecFromParamsUsesTemplate(t *testing.T) {
	spec, err := VideoTaskSpecFromParams(map[string]string{"template": "vidu", "status_field": "status", "auth_prefix": "Key"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Submit.Path != "ent/v2/text2video" || spec.StatusPath != "status" || spec.Auth.Kind != VideoAuthHeader || spec.Auth.Prefix != "Key " {
		t.Fatalf("spec = %#v", spec)
	}
	if preset, _ := VideoTaskPreset(VideoAPIVidu); preset.StatusPath != "state" {
		t.Fatal("custom overrides must not leak into the preset")
	}
	if _, err := VideoTaskSpecFromParams(map[string]string{"status_map": "ok=done"}); err == nil {
		t.Fatal("unknown unified status must be rejected")
	}
}

func TestRenderVideoJSONDropsEmptyVariables(t *testing.T) {
	vars := videoVars{values: map[string]string{"prompt": `说"你好"`, "seconds": "5", "resolution": "768p"}}
	got, err := renderVideoJSON(`{"p":"{{prompt}}","d":{{seconds}},"s":"{{seconds}}s","r":"{{resolution|upper}}","ratio":"{{ratio}}","n":{{frames}},"list":["{{image}}","x"],"o":{"a":"{{size}}"}}`, vars)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"d":5,"list":["x"],"o":{},"p":"说\"你好\"","r":"768P","s":"5s"}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestJSONPathFilters(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"data":[{"outputs":[{"type":"image","url":"a"},{"type":"video","url":"b"}]}],"code":0,"err":{"code":"x","message":"y"}}`), &doc)
	for path, want := range map[string]string{
		"data.0.outputs[type=video].url": "b",
		"missing|data.0.outputs.0.url":   "a",
		"code":                           "0",
		"err":                            "x: y",
		"data.3.outputs":                 "",
	} {
		if got := jsonPathString(doc, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

// 官方文档的示例：https://www.volcengine.com/docs/6369/67269
func TestSignVolcV4MatchesOfficialExample(t *testing.T) {
	at := time.Date(2025, 3, 29, 18, 9, 37, 0, time.UTC)
	const (
		ak = "example-access-key" // 签名只由 SK 决定，AK 换成占位值，免得被当成泄漏的密钥
		sk = "WkRZeE1EQmxPVGhsWWpWak5HVmtNbUUxTXpZeU9UVXlOMlE1TmpZeVlqTQ=="
	)
	for _, tc := range []struct {
		method, action, body, signature string
	}{
		{http.MethodGet, "QueryBalanceAcct", "", "1eda9e7e6b1728151a8e8791fdaf67cfbd28bd5c80d0fce2eb208746cf483105"},
		{http.MethodPost, "ListBill", `{"Limit":10,"BillPeriod":"2023-08"}`, "5e8480ceea12d0000a23c054151c50dd02c1a7dec835004057d19f13d53a7658"},
	} {
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		req, _ := http.NewRequest(tc.method, "https://billing.volcengineapi.com/?Action="+tc.action+"&Version=2022-01-01", body)
		signVolcV4(req, ak, sk, "cn-beijing", "billing", at)
		want := "HMAC-SHA256 Credential=" + ak + "/20250329/cn-beijing/billing/request, SignedHeaders=host;x-date, Signature=" + tc.signature
		if got := req.Header.Get("Authorization"); got != want || req.Header.Get("X-Date") != "20250329T180937Z" {
			t.Errorf("%s: Authorization = %q\nwant %q", tc.method, got, want)
		}
	}
}

// 固定输入、固定输出；签名值用 Python 标准库 hmac 独立算出。
func TestKlingJWTFixedVector(t *testing.T) {
	got := klingJWT("ak-test", "sk-test", time.Unix(1700000000, 0))
	const want = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJhay10ZXN0IiwiZXhwIjoxNzAwMDAxODAwLCJuYmYiOjE2OTk5OTk5OTV9.tMljlG8Xy1G8AKj5WkEZRcRIADvbH0FP6wgzpwHAfs4"
	if got != want {
		t.Fatalf("jwt = %s\nwant  %s", got, want)
	}
}

func TestKlingAuthSignsJWTForKeyPair(t *testing.T) {
	previous := videoAuthNow
	videoAuthNow = func() time.Time { return time.Unix(1700000000, 0) }
	t.Cleanup(func() { videoAuthNow = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+klingJWT("ak-test", "sk-test", time.Unix(1700000000, 0)) {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"id":"t"}}`))
	}))
	defer server.Close()
	generator, err := NewVideoGenerator(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "ak-test:sk-test", BaseURL: server.URL}, VideoAPIKling, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.SubmitVideo(context.Background(), VideoGenerateRequest{Model: "kling-2.6", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestJimengRequiresKeyPair(t *testing.T) {
	if _, err := NewVideoGenerator(ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "only-one-part"}, VideoAPIJimeng, 0); err == nil {
		t.Fatal("volcengine signing without AK:SK must be rejected")
	}
}
