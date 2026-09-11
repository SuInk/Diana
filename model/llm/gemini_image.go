// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"google.golang.org/genai"
)

// Gemini 的图片模型不走独立的 images 接口，而是用普通的 generateContent，把图片
// 当成响应里的一种模态交回来。没有这两个方法时 client 断言不成 ImageGenerator，
// 生图选到 gemini 就直接报「image generation is not supported for provider」——
// 配置里明明列着 gemini 的图片模型，却连请求都发不出去。
//
// 尺寸的表达方式两边也不一样：OpenAI 那套收 "1024x1024" 这种像素尺寸，Gemini 收的
// 是宽高比。上层统一传像素尺寸，这里换算成最接近的受支持比例，换算不出来就不带
// 这个字段，让模型用自己的默认值，而不是把请求打回去。

// GenerateImage 用 Gemini 的图片模型按提示词生成图片。
func (c *geminiClient) GenerateImage(ctx context.Context, req ImageGenerateRequest) (*ImageGenerateResponse, error) {
	req = imageRequestWithDefaults(req, c.cfg)
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("llm: image prompt is required")
	}
	parts := []*genai.Part{{Text: req.Prompt}}
	images, err := c.generateImageParts(ctx, req.Model, req.Size, req.N, parts)
	if err != nil {
		return nil, err
	}
	return &ImageGenerateResponse{Provider: ProviderGemini, Model: req.Model, Images: images}, nil
}

// EditImage 把源图和修改要求一起交给 Gemini 的图片模型。
func (c *geminiClient) EditImage(ctx context.Context, req ImageEditRequest) (*ImageGenerateResponse, error) {
	req = imageEditRequestWithDefaults(req, c.cfg)
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("llm: image edit prompt is required")
	}
	if len(req.Images) == 0 {
		return nil, errors.New("llm: image edit requires at least one source image")
	}
	// 源图在前、要求在后：先给模型看图，再说改什么。
	parts := make([]*genai.Part, 0, len(req.Images)+1)
	for index, source := range req.Images {
		input, err := imageEditInputFrom(ctx, c.httpClient, source, index)
		if err != nil {
			return nil, err
		}
		parts = append(parts, genai.NewPartFromBytes(input.data, input.mediaType))
	}
	parts = append(parts, &genai.Part{Text: req.Prompt})
	images, err := c.generateImageParts(ctx, req.Model, req.Size, req.N, parts)
	if err != nil {
		return nil, err
	}
	return &ImageGenerateResponse{Provider: ProviderGemini, Model: req.Model, Images: images}, nil
}

// generateImageParts 发请求并把响应里的图片取出来。
//
// 一次 generateContent 通常只回一张图，要 n 张就得发 n 次。已经拿到图之后再失败
// 不整批作废：少给几张也比一张都没有强，全都失败才把错误抛上去。
func (c *geminiClient) generateImageParts(ctx context.Context, model, size string, n int, parts []*genai.Part) ([]string, error) {
	if n <= 0 {
		n = 1
	}
	config := &genai.GenerateContentConfig{ResponseModalities: []string{string(genai.ModalityText), string(genai.ModalityImage)}}
	if ratio := geminiAspectRatio(size); ratio != "" {
		config.ImageConfig = &genai.ImageConfig{AspectRatio: ratio}
	}
	contents := []*genai.Content{{Role: genai.RoleUser, Parts: parts}}
	images := make([]string, 0, n)
	var lastErr error
	for len(images) < n {
		resp, err := c.client.Models.GenerateContent(ctx, model, contents, config)
		if err != nil {
			lastErr = fmt.Errorf("llm: provider request failed: %w", err)
			break
		}
		if resp == nil {
			lastErr = errors.New("llm: gemini returned an empty response")
			break
		}
		if blocked := geminiContentBlock(resp); blocked != nil {
			lastErr = blocked
			break
		}
		got := geminiInlineImages(resp)
		if len(got) == 0 {
			lastErr = errors.New("llm: gemini response has no image")
			break
		}
		images = append(images, got...)
	}
	if len(images) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("llm: image output is empty")
	}
	if len(images) > n {
		images = images[:n]
	}
	return images, nil
}

// geminiInlineImages 取出响应里的图片，编码成和 OpenAI 那条链路一致的 data URI，
// 上层拿到的东西就与选中了谁无关。
func geminiInlineImages(resp *genai.GenerateContentResponse) []string {
	images := make([]string, 0, 1)
	for _, candidate := range resp.Candidates {
		if candidate == nil || candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part == nil || part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			mediaType := strings.TrimSpace(part.InlineData.MIMEType)
			if !strings.HasPrefix(mediaType, "image/") {
				continue
			}
			images = append(images, "data:"+mediaType+";base64,"+base64.StdEncoding.EncodeToString(part.InlineData.Data))
		}
	}
	return images
}

// geminiSupportedAspectRatios 是 Gemini ImageConfig 接受的宽高比。
var geminiSupportedAspectRatios = []struct {
	name  string
	value float64
}{
	{"1:1", 1.0 / 1.0},
	{"2:3", 2.0 / 3.0},
	{"3:2", 3.0 / 2.0},
	{"3:4", 3.0 / 4.0},
	{"4:3", 4.0 / 3.0},
	{"9:16", 9.0 / 16.0},
	{"16:9", 16.0 / 9.0},
	{"21:9", 21.0 / 9.0},
}

// geminiAspectRatio 把 "1024x1024" 这类像素尺寸换成最接近的受支持宽高比。
// 认不出来就返回空串：不带这个字段让模型用默认值，比为了带上而猜一个更稳妥。
func geminiAspectRatio(size string) string {
	width, height, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return ""
	}
	w, err := strconv.ParseFloat(strings.TrimSpace(width), 64)
	if err != nil || w <= 0 {
		return ""
	}
	h, err := strconv.ParseFloat(strings.TrimSpace(height), 64)
	if err != nil || h <= 0 {
		return ""
	}
	want := w / h
	best, bestDiff := "", math.Inf(1)
	for _, candidate := range geminiSupportedAspectRatios {
		if diff := math.Abs(candidate.value-want) / want; diff < bestDiff {
			best, bestDiff = candidate.name, diff
		}
	}
	return best
}

// 让编译期确认这两个接口都实到了：断言失败时生图会在运行时退回「不支持」，
// 那条错误信息看不出是接口没实现。
var (
	_ ImageGenerator = (*geminiClient)(nil)
	_ ImageEditor    = (*geminiClient)(nil)
)
