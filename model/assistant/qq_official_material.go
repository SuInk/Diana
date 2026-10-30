// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // 开放平台要求 MD5 校验值
	"crypto/sha1" //nolint:gosec // 开放平台要求 SHA1 校验值
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 素材管理（/v2/media-images）：Markdown 里的图片先传到平台，换成
// qqbot-media://image/v1/<token> 引用再发，QQ 不用自己去拉图。本机文件、data: 图、
// 内网地址这些 QQ 拉不到的图都靠它显示；公网地址照旧原样发。
//
// 每个机器人最多存 100 张，90 天没被引用就失效。上传过的图按内容 SHA1 记在
// file_name 里（diana-<sha1>.<ext>），重启后翻一遍查询接口就能认回来，同一张图
// 不会重复占额度。额度满了先删自己传的最旧一张再试；还不行或任何一步失败，就保留
// 原来的图片地址，整条消息照旧发。
const (
	qqMaterialMaxBytes      = 10 << 20
	qqMaterialNamePrefix    = "diana-"
	qqMaterialQuotaFullCode = "40035007"
	// qqMaterialQuotaBackoff 是删不出空位时暂停上传的时长，免得每条消息都白跑一趟。
	qqMaterialQuotaBackoff = 30 * time.Minute
	// qqMaterialExpirySlack 留出余量：快到期的图宁可重传，也别引用一个刚失效的链接。
	qqMaterialExpirySlack  = 24 * time.Hour
	qqMaterialMaxListPages = 20
)

var qqMarkdownImage = regexp.MustCompile(`!\[([^\]\n]*)\]\(([^)\s]+)\)`)

type qqMaterialEntry struct {
	imageID   string
	url       string
	createdAt time.Time
	expiresAt time.Time
}

// qqMaterialCache 是一个机器人已上传素材的索引，键是图片内容的 SHA1。
type qqMaterialCache struct {
	mu         sync.Mutex
	appID      string
	loaded     bool
	entries    map[string]*qqMaterialEntry
	quotaUntil time.Time
}

// qqMaterializeMarkdownImages 把 Markdown 里 QQ 拉不到的图片换成素材引用。
// 换不成的保留原样，不影响其余图片和整条消息。
func (c *QQOfficialChannel) qqMaterializeMarkdownImages(ctx context.Context, auth, content string) string {
	matches := qqMarkdownImage.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}
	c.mu.RLock()
	appID := c.cfg.AppID
	c.mu.RUnlock()
	cache := &c.material
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.appID != appID || cache.entries == nil {
		// 素材绑定机器人，换了 AppID 旧引用就用不了。
		cache.appID, cache.loaded, cache.quotaUntil = appID, false, time.Time{}
		cache.entries = map[string]*qqMaterialEntry{}
	}

	var b strings.Builder
	last := 0
	used := map[string]bool{}
	for _, m := range matches {
		source := content[m[4]:m[5]]
		if !qqMaterialNeedsUpload(source) {
			continue
		}
		ref, err := c.qqMaterialRef(ctx, auth, cache, source, used)
		if err != nil {
			log.Printf("qq: Markdown 图片上传素材失败，保留原地址: %v", err)
			continue
		}
		b.WriteString(content[last:m[4]])
		b.WriteString(ref)
		last = m[5]
	}
	if last == 0 {
		return content
	}
	b.WriteString(content[last:])
	return b.String()
}

// qqMaterialNeedsUpload 判断图片要不要先传素材：qqbot-media 引用已经是了，公网
// http(s) 地址 QQ 自己能拉，其余（本机路径、data:、内网地址）都要传。
func qqMaterialNeedsUpload(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" || strings.HasPrefix(source, "qqbot-media://") {
		return false
	}
	if normalizedHTTPURL(source) != "" {
		return !qqGalleryURLUsable(source)
	}
	return inlineImageSource(source) || rawAbsoluteMediaPath(source) != ""
}

func (c *QQOfficialChannel) qqMaterialRef(ctx context.Context, auth string, cache *qqMaterialCache, source string, used map[string]bool) (string, error) {
	data, contentType, err := readHistoryImageSource(ctx, source, 0)
	if err != nil {
		return "", fmt.Errorf("读取图片失败: %w", err)
	}
	if len(data) > qqMaterialMaxBytes {
		return "", fmt.Errorf("图片 %d 字节超过素材上限 %d", len(data), qqMaterialMaxBytes)
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if contentType == "image/jpg" {
		contentType = "image/jpeg"
	}
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("素材不支持 %s", contentType)
	}
	sha1Sum := sha1.Sum(data) //nolint:gosec
	key := hex.EncodeToString(sha1Sum[:])
	used[key] = true
	now := time.Now()
	if !cache.loaded {
		cache.loaded = true
		if err := c.qqMaterialLoad(ctx, auth, cache); err != nil {
			// 查不到也照常上传，只是可能多占一个名额。
			log.Printf("qq: 查询已上传素材失败: %v", err)
		}
	}
	if entry := cache.entries[key]; entry != nil && entry.expiresAt.After(now.Add(qqMaterialExpirySlack)) {
		// 每次被引用有效期都会顺延。
		entry.expiresAt = now.Add(90 * 24 * time.Hour)
		return entry.url, nil
	}
	if now.Before(cache.quotaUntil) {
		return "", fmt.Errorf("素材额度已满")
	}
	name := qqMaterialNamePrefix + key + mediaExtensionFor(contentType)
	entry, err := c.qqMaterialUpload(ctx, auth, name, contentType, data)
	if err != nil && strings.Contains(err.Error(), qqMaterialQuotaFullCode) {
		if c.qqMaterialEvictOldest(ctx, auth, cache, used) {
			entry, err = c.qqMaterialUpload(ctx, auth, name, contentType, data)
		}
		if err != nil && strings.Contains(err.Error(), qqMaterialQuotaFullCode) {
			cache.quotaUntil = now.Add(qqMaterialQuotaBackoff)
		}
	}
	if err != nil {
		return "", err
	}
	cache.entries[key] = entry
	return entry.url, nil
}

func (c *QQOfficialChannel) qqMaterialCall(ctx context.Context, auth, method, path string, body, out any) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	raw, err := platformJSONRequest(ctx, client, method, c.apiBase()+path, map[string]string{"Authorization": auth}, body)
	if err != nil {
		return err
	}
	return qqDecodeAPIResponse(raw, out)
}

// qqMaterialUpload 走 prepare → PUT 直传 → complete 三步传一张图。
func (c *QQOfficialChannel) qqMaterialUpload(ctx context.Context, auth, name, contentType string, data []byte) (*qqMaterialEntry, error) {
	md5Sum := md5.Sum(data)   //nolint:gosec
	sha1Sum := sha1.Sum(data) //nolint:gosec
	md5Hex := hex.EncodeToString(md5Sum[:])
	var prepared struct {
		UploadID     string `json:"upload_id"`
		PresignedURL string `json:"presigned_url"`
	}
	if err := c.qqMaterialCall(ctx, auth, http.MethodPost, "/v2/media-images/prepare", map[string]any{
		"file_name":    name,
		"file_size":    len(data),
		"content_type": contentType,
		"md5":          md5Hex,
		"sha1":         hex.EncodeToString(sha1Sum[:]),
	}, &prepared); err != nil {
		return nil, fmt.Errorf("申请素材上传失败: %w", err)
	}
	if prepared.UploadID == "" || prepared.PresignedURL == "" {
		return nil, fmt.Errorf("申请素材上传没有返回上传地址")
	}
	etag, err := c.qqMaterialPut(ctx, prepared.PresignedURL, contentType, data)
	if err != nil {
		return nil, fmt.Errorf("上传素材失败: %w", err)
	}
	if etag == "" {
		etag = md5Hex
	}
	var completed struct {
		ImageID string `json:"image_id"`
		URL     string `json:"url"`
		TTL     int64  `json:"ttl"`
	}
	if err := c.qqMaterialCall(ctx, auth, http.MethodPost, "/v2/media-images/complete", map[string]any{
		"upload_id": prepared.UploadID,
		"md5":       etag,
	}, &completed); err != nil {
		return nil, fmt.Errorf("完成素材上传失败: %w", err)
	}
	if !strings.HasPrefix(completed.URL, "qqbot-media://") {
		return nil, fmt.Errorf("完成素材上传没有返回引用链接")
	}
	now := time.Now()
	ttl := time.Duration(completed.TTL) * time.Second
	if ttl <= 0 {
		ttl = 90 * 24 * time.Hour
	}
	return &qqMaterialEntry{imageID: completed.ImageID, url: completed.URL, createdAt: now, expiresAt: now.Add(ttl)}, nil
}

// qqMaterialPut 把图片 PUT 到预签名地址，返回 ETag（即 MD5）。Content-Type 必须和
// prepare 声明的一致，否则对象存储回 403；这个地址自带签名，不能带机器人鉴权头。
func (c *QQOfficialChannel) qqMaterialPut(ctx context.Context, target, contentType string, data []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("预签名地址无效")
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", contentType)
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", safePlatformError(err, target, nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, truncateForError(string(body)))
	}
	return strings.ToLower(strings.Trim(resp.Header.Get("ETag"), `"`)), nil
}

// qqMaterialLoad 翻查询接口，把以前传过的 diana-<sha1> 素材认回缓存。
func (c *QQOfficialChannel) qqMaterialLoad(ctx context.Context, auth string, cache *qqMaterialCache) error {
	cursor := ""
	for page := 0; page < qqMaterialMaxListPages; page++ {
		path := "/v2/media-images"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		var listed struct {
			Images []struct {
				ImageID   string `json:"image_id"`
				URL       string `json:"url"`
				FileName  string `json:"file_name"`
				CreatedAt int64  `json:"created_at"`
				ExpiresAt int64  `json:"expires_at"`
			} `json:"images"`
			NextCursor string `json:"next_cursor"`
		}
		if err := c.qqMaterialCall(ctx, auth, http.MethodGet, path, nil, &listed); err != nil {
			return err
		}
		for _, image := range listed.Images {
			key, ok := strings.CutPrefix(image.FileName, qqMaterialNamePrefix)
			if !ok {
				continue
			}
			key = strings.TrimSuffix(key, filepath.Ext(key))
			if len(key) != 40 || image.URL == "" {
				continue
			}
			cache.entries[key] = &qqMaterialEntry{
				imageID:   image.ImageID,
				url:       image.URL,
				createdAt: time.Unix(image.CreatedAt, 0),
				expiresAt: time.Unix(image.ExpiresAt, 0),
			}
		}
		if listed.NextCursor == "" || listed.NextCursor == cursor {
			return nil
		}
		cursor = listed.NextCursor
	}
	return nil
}

// qqMaterialEvictOldest 删掉自己传的最旧一张素材腾名额，本条消息正在用的不删。
// 已发出的消息不受影响，平台发送时已经转存了副本。
func (c *QQOfficialChannel) qqMaterialEvictOldest(ctx context.Context, auth string, cache *qqMaterialCache, used map[string]bool) bool {
	keys := make([]string, 0, len(cache.entries))
	for key, entry := range cache.entries {
		if !used[key] && entry.imageID != "" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return cache.entries[keys[i]].createdAt.Before(cache.entries[keys[j]].createdAt) })
	for _, key := range keys {
		err := c.qqMaterialCall(ctx, auth, http.MethodDelete, "/v2/media-images/"+url.PathEscape(cache.entries[key].imageID)+"/delete", nil, nil)
		if err != nil && !strings.Contains(err.Error(), "40035008") {
			log.Printf("qq: 删除旧素材失败: %v", err)
			return false
		}
		delete(cache.entries, key)
		if err == nil {
			return true
		}
		// 40035008：这张已经不在了，删下一张。
	}
	return false
}
