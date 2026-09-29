// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // 开放平台要求 MD5 校验值
	"crypto/sha1" //nolint:gosec // 开放平台要求 SHA1 校验值
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// qqMaxImageBytes 是图片的软上限。
	qqMaxImageBytes = 20 << 20
	// qqMD510MBoundary 是 md5_10m 取文件头部的字节数（平台约定）。
	qqMD510MBoundary = 10002432
	qqUploadRetries  = 3
)

// qqUploadImage 把一张图片分片上传到会话，返回可放进 media.file_info 的凭据。
//
// prefix 是 /v2/groups/{id} 或 /v2/users/{id}。图片来源可能是本地文件、内联数据或
// 外链，统一读成字节再上传：外链不一定是公网可达的，直接交给平台下载会失败。
func (c *QQOfficialChannel) qqUploadImage(ctx context.Context, auth, prefix, source string) (string, error) {
	data, contentType, err := readHistoryImageSource(ctx, source, 0)
	if err != nil {
		return "", fmt.Errorf("qq: 读取图片失败: %w", err)
	}
	if len(data) > qqMaxImageBytes {
		return "", fmt.Errorf("qq: 图片 %d 字节超过上限 %d", len(data), qqMaxImageBytes)
	}
	name := "image" + imessageExtensionFor(contentType)
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	headers := map[string]string{"Authorization": auth}
	call := func(path string, body any, out any) error {
		raw, err := platformJSONRequest(ctx, client, http.MethodPost, prefix+path, headers, body)
		if err != nil {
			return err
		}
		return qqDecodeAPIResponse(raw, out)
	}

	md5Sum := md5.Sum(data)   //nolint:gosec
	sha1Sum := sha1.Sum(data) //nolint:gosec
	head := data
	if len(head) > qqMD510MBoundary {
		head = head[:qqMD510MBoundary]
	}
	headSum := md5.Sum(head) //nolint:gosec
	var prepared struct {
		UploadID  string `json:"upload_id"`
		BlockSize string `json:"block_size"`
		Parts     []struct {
			Index        int    `json:"index"`
			PresignedURL string `json:"presigned_url"`
			BlockSize    string `json:"block_size"`
		} `json:"parts"`
	}
	if err := call("/upload_prepare", map[string]any{
		"file_type": 1,
		"file_size": strconv.Itoa(len(data)),
		"file_name": name,
		"md5":       hex.EncodeToString(md5Sum[:]),
		"sha1":      hex.EncodeToString(sha1Sum[:]),
		"md5_10m":   hex.EncodeToString(headSum[:]),
	}, &prepared); err != nil {
		return "", fmt.Errorf("qq: 预上传失败: %w", err)
	}
	if prepared.UploadID == "" || len(prepared.Parts) == 0 {
		return "", fmt.Errorf("qq: 预上传没有返回上传任务")
	}
	sort.Slice(prepared.Parts, func(i, j int) bool { return prepared.Parts[i].Index < prepared.Parts[j].Index })

	offset := 0
	for _, part := range prepared.Parts {
		sizeText := part.BlockSize
		if sizeText == "" {
			sizeText = prepared.BlockSize
		}
		size, err := strconv.Atoi(sizeText)
		if err != nil || size <= 0 {
			return "", fmt.Errorf("qq: 分片 %d 的大小无效: %q", part.Index, sizeText)
		}
		if offset >= len(data) {
			return "", fmt.Errorf("qq: 分片计划超出图片长度")
		}
		end := min(offset+size, len(data))
		chunk := data[offset:end]
		offset = end
		chunkSum := md5.Sum(chunk) //nolint:gosec
		var lastErr error
		for attempt := 0; attempt < qqUploadRetries; attempt++ {
			if attempt > 0 {
				select {
				case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if lastErr = qqPutPart(ctx, client, part.PresignedURL, chunk); lastErr != nil {
				continue
			}
			lastErr = call("/upload_part_finish", map[string]any{
				"upload_id":  prepared.UploadID,
				"part_index": part.Index,
				"block_size": strconv.Itoa(len(chunk)),
				"md5":        hex.EncodeToString(chunkSum[:]),
			}, nil)
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			return "", fmt.Errorf("qq: 上传分片 %d 失败: %w", part.Index, lastErr)
		}
	}
	if offset != len(data) {
		return "", fmt.Errorf("qq: 分片计划没有覆盖整张图片")
	}

	var merged struct {
		FileInfo string `json:"file_info"`
	}
	if err := call("/files", map[string]any{
		"file_type":    1,
		"file_name":    name,
		"upload_id":    prepared.UploadID,
		"srv_send_msg": false,
	}, &merged); err != nil {
		return "", fmt.Errorf("qq: 合并分片失败: %w", err)
	}
	if merged.FileInfo == "" {
		return "", fmt.Errorf("qq: 合并结果缺少 file_info")
	}
	return merged.FileInfo, nil
}

// qqPutPart 把一个分片 PUT 到预签名地址；这个地址自带签名，不能再带机器人的鉴权头。
func qqPutPart(ctx context.Context, client *http.Client, url string, chunk []byte) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("预签名地址为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(chunk))
	if err != nil {
		return fmt.Errorf("预签名地址无效")
	}
	req.ContentLength = int64(len(chunk))
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return safePlatformError(err, url, nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncateForError(string(body)))
	}
	return nil
}

// qqDecodeAPIResponse 解出开放平台的响应；带 code 的错误体即使是 2xx 也算失败。
func qqDecodeAPIResponse(raw []byte, out any) error {
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Code != 0 {
		return fmt.Errorf("%s (code %d)", envelope.Message, envelope.Code)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
