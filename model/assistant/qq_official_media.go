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
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

const (
	// 开放平台富媒体接口的 file_type：1 图片、2 视频、3 语音、4 文件。
	qqFileTypeImage = 1
	qqFileTypeVideo = 2
	qqFileTypeAudio = 3
	qqFileTypeFile  = 4

	// qqMaxImageBytes 是图片的软上限。
	qqMaxImageBytes = 20 << 20
	// qqMaxVideoBytes 是视频上限。36MB 分片上传通过，210MB 在预上传被拒
	//（500/850012），取 100MB 留出余量。
	qqMaxVideoBytes = 100 << 20
	// qqMaxAudioBytes 是语音/音频上限；平台会转码，过大的音频无实际意义。
	qqMaxAudioBytes = 20 << 20
	// qqMaxFileBytes 是文件附件上限。平台硬限制 200MB，与视频同取 100MB；
	// send_attachment 读文件本身只到 32MB，这里只是兜住其他来源。
	qqMaxFileBytes = 100 << 20
	// qqMD510MBoundary 是 md5_10m 取文件头部的字节数（平台约定）。
	qqMD510MBoundary = 10002432
	qqUploadRetries  = 3
)

// qqUploadMedia 把一个富媒体上传到会话，返回可放进 media.file_info 的凭据。
//
// prefix 是 /v2/groups/{id} 或 /v2/users/{id}。媒体来源可能是本地文件、内联数据或
// 外链，统一读成字节再上传：外链不一定公网可达，直接交给平台下载会失败。
// 图片、视频、语音、文件共用同一条分片上传链路，差别只在 file_type 与大小上限。
// filename 是文件段带的展示名：外链来源的 URL 末段往往不是真文件名，有就用它。
func (c *QQOfficialChannel) qqUploadMedia(ctx context.Context, auth, prefix, source, filename string, fileType int, maxBytes int64) (string, error) {
	data, name, err := qqMediaPayload(ctx, source, fileType, maxBytes)
	if err != nil {
		return "", err
	}
	if fileType == qqFileTypeFile {
		if base := filepath.Base(strings.TrimSpace(filename)); base != "" && base != "." && base != "/" {
			name = base
		}
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	headers := map[string]string{"Authorization": auth}
	call := func(path string, body any, out any) error {
		raw, err := platformJSONRequest(ctx, client, http.MethodPost, prefix+path, headers, body)
		if err != nil {
			c.noteTokenRejected(err)
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
		"file_type": fileType,
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
			return "", fmt.Errorf("qq: 分片计划超出媒体长度")
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
		return "", fmt.Errorf("qq: 分片计划没有覆盖整个媒体")
	}

	var merged struct {
		FileInfo string `json:"file_info"`
	}
	if err := call("/files", map[string]any{
		"file_type":    fileType,
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

// qqMediaPayload 读出上传用的字节和文件名。图片走 readHistoryImageSource（内联
// 数据、下载缓存均在该路径）；视频、音频和文件读本地文件，外链先下载到缓存再读。
func qqMediaPayload(ctx context.Context, source string, fileType int, maxBytes int64) ([]byte, string, error) {
	if fileType == qqFileTypeImage {
		data, contentType, err := readHistoryImageSource(ctx, source, 0)
		if err != nil {
			return nil, "", fmt.Errorf("qq: 读取图片失败: %w", err)
		}
		if int64(len(data)) > maxBytes {
			return nil, "", fmt.Errorf("qq: 图片 %d 字节超过上限 %d", len(data), maxBytes)
		}
		return data, "image" + mediaExtensionFor(contentType), nil
	}
	kind, defaultName := "视频", "video.mp4"
	switch fileType {
	case qqFileTypeAudio:
		kind, defaultName = "音频", "audio.mp3"
	case qqFileTypeFile:
		kind, defaultName = "文件", "file"
	}
	data, err := qqReadMediaBytes(ctx, source, maxBytes)
	if err != nil {
		return nil, "", fmt.Errorf("qq: 读取%s失败: %w", kind, err)
	}
	if fileType == qqFileTypeFile {
		// 文件附件的名字就是用户看到的文件名，没有扩展名（Makefile 之类）也照用。
		if base := filepath.Base(rawAbsoluteMediaPath(strings.TrimSpace(source))); base != "" && base != "." && base != "/" {
			return data, base, nil
		}
	}
	return data, qqUploadName(source, defaultName), nil
}

// qqReadMediaBytes 读出非图片媒体的字节：本地路径直接读，外链下载到缓存再读。
func qqReadMediaBytes(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
	value := strings.TrimSpace(source)
	if path := rawAbsoluteMediaPath(value); path != "" {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return nil, fmt.Errorf("文件不可读: %s", filepath.Base(path))
		}
		if info.Size() > maxBytes {
			return nil, fmt.Errorf("文件 %d 字节超过上限 %d", info.Size(), maxBytes)
		}
		return os.ReadFile(path)
	}
	remote := normalizedHTTPURL(value)
	if remote == "" {
		return nil, fmt.Errorf("无法识别的媒体来源")
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	path, _, release, err := acquireMediaDownload(callCtx, netguard.NewPublicHTTPClient(60*time.Second), remote, "media", "", "qq", maxBytes)
	if err != nil {
		return nil, err
	}
	defer release()
	return os.ReadFile(path)
}

// qqUploadName 从来源中取平台可识别的文件名；取不到或没有扩展名时用默认名。
func qqUploadName(source, defaultName string) string {
	value := strings.TrimSpace(source)
	base := ""
	if remote := normalizedHTTPURL(value); remote != "" {
		if parsed, err := url.Parse(remote); err == nil {
			base = filepath.Base(parsed.Path)
		}
	} else if path := rawAbsoluteMediaPath(value); path != "" {
		base = filepath.Base(path)
	}
	if base == "" || base == "." || base == "/" || filepath.Ext(base) == "" {
		return defaultName
	}
	return base
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
