// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DeepSeek OCR 是工具 API 里的 POST /v1/tools/ocr：multipart 上传 file 和 purpose，
// 同步返回逐页文字。识别期间服务端先发换行保活，再发 JSON；识别开始后状态码已经是
// 200，失败改在正文里回 {"error": ...}。文档：api-docs.deepseek.com/guides/ocr。
const (
	// DeepSeekOCRPurposeText 只要文字：没有图片的 Word、PPT 直接抽文字，其余逐页识别。
	DeepSeekOCRPurposeText = "text_recognition"
	// DeepSeekOCRMaxBytes 是单个文件的上限，超了服务端回 413。
	DeepSeekOCRMaxBytes = 100 << 20
	// 服务端从开始上传算起最多 180 秒，这里多留一点给网络。
	deepSeekOCRTimeout      = 200 * time.Second
	maxDeepSeekOCRRespBytes = 32 << 20
)

// DeepSeekOCRRequest 是一次 OCR。Filename 只用来带扩展名，格式由服务端按内容识别。
type DeepSeekOCRRequest struct {
	Data     []byte
	Filename string
	// Purpose 为空时用 DeepSeekOCRPurposeText。
	Purpose string
	// Timeout 为 0 时用 200 秒。
	Timeout time.Duration
}

// DeepSeekOCRPage 是一页的识别结果。Text 已经去掉了版面坐标标记。
type DeepSeekOCRPage struct {
	Index int
	Text  string
	// Truncated 表示这一页的输出到了长度上限，文字不完整。
	Truncated bool
}

// DeepSeekOCR 调 DeepSeek 的 OCR 接口。cfg 只用 APIKey、BaseURL（留空走官方地址，
// 填 https://api.deepseek.com、带 /v1 或直接填到 /v1/tools/ocr 都行）和自定义请求头。
func DeepSeekOCR(ctx context.Context, cfg ProviderConfig, req DeepSeekOCRRequest, opts ...ClientOption) ([]DeepSeekOCRPage, error) {
	if len(req.Data) == 0 {
		return nil, errors.New("llm: ocr file is required")
	}
	if len(req.Data) > DeepSeekOCRMaxBytes {
		return nil, &MediaAPIError{Op: "ocr", Kind: MediaErrorInvalid, Detail: "DeepSeek OCR 单个文件不能超过 100 MiB"}
	}
	client, err := newMediaHTTP(cfg, defaultDeepSeekBaseURL, opts)
	if err != nil {
		return nil, err
	}
	client.auth = bearerAuth(cfg.APIKey)
	client.policy.AttemptTimeout = req.Timeout
	if client.policy.AttemptTimeout <= 0 {
		client.policy.AttemptTimeout = deepSeekOCRTimeout
	}
	filename := filepath.Base(strings.TrimSpace(req.Filename))
	if filename == "" || filename == "." || filename == "/" {
		filename = "document"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", firstNonEmptyString(req.Purpose, DeepSeekOCRPurposeText)); err != nil {
		return nil, err
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(req.Data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	result, err := client.do(ctx, mediaRequestSpec{
		op: "ocr", method: http.MethodPost, endpoint: deepSeekToolEndpoint(client.baseURL, "ocr"),
		body: body.Bytes(), contentType: writer.FormDataContentType(), accept: "application/json",
	}, maxDeepSeekOCRRespBytes)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Pages []struct {
			Index        int    `json:"index"`
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
		} `json:"pages"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	// 正文前面的保活换行 json.Unmarshal 会当空白跳过。
	if err := json.Unmarshal(result.body, &parsed); err != nil {
		return nil, &MediaAPIError{Op: "ocr", Kind: MediaErrorBadResponse, StatusCode: result.status, Detail: truncateMediaDetail(string(result.body))}
	}
	if parsed.Error != nil {
		kind := MediaErrorBadResponse
		if strings.Contains(strings.ToLower(parsed.Error.Type), "timeout") || strings.Contains(strings.ToLower(parsed.Error.Type), "server") {
			kind = MediaErrorUpstream
		}
		return nil, &MediaAPIError{Op: "ocr", Kind: kind, StatusCode: result.status, Detail: strings.TrimSpace(parsed.Error.Type + ": " + parsed.Error.Message)}
	}
	pages := make([]DeepSeekOCRPage, 0, len(parsed.Pages))
	for _, page := range parsed.Pages {
		pages = append(pages, DeepSeekOCRPage{Index: page.Index, Text: CleanDeepSeekOCRText(page.Text), Truncated: page.FinishReason == "length"})
	}
	return pages, nil
}

var (
	// 版面坐标块：<｜det｜>[[x1, y1, x2, y2]]<｜/det｜>，box 写法同理。开源 DeepSeek-OCR
	// 用半角竖线，API 文档用全角，两种都认。
	deepSeekOCRBoxBlock = regexp.MustCompile(`<[|｜](det|box)[|｜]>[\s\S]*?<[|｜]/(det|box)[|｜]>`)
	deepSeekOCRToken    = regexp.MustCompile(`<[|｜]/?[A-Za-z_]+[|｜]>`)
	deepSeekOCRCoords   = regexp.MustCompile(`\[\[\s*\d+(\s*,\s*\d+){3}\s*\]\]`)
	deepSeekOCRBlank    = regexp.MustCompile(`\n{3,}`)
)

// CleanDeepSeekOCRText 去掉 OCR 输出里的版面标记，只留文字：ref 标签里的文字保留，
// 坐标块整段删掉。
func CleanDeepSeekOCRText(text string) string {
	text = deepSeekOCRBoxBlock.ReplaceAllString(text, "")
	text = deepSeekOCRToken.ReplaceAllString(text, "")
	text = deepSeekOCRCoords.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(deepSeekOCRBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func truncateMediaDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) > 2000 {
		return detail[:2000]
	}
	return detail
}
