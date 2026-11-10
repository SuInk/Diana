package assistant

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

var rssWatchVideoTagPattern = regexp.MustCompile(`\[CQ:video,file=[^\]]+\]`)

func rssWatchVideoAllowed(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && strings.EqualFold(u.Host, "video.twimg.com") && strings.HasSuffix(strings.ToLower(u.Path), ".mp4")
}

// Persist remote media tags with the notification so retries keep the same
// video/cover decision and do not depend on an expiring local download.
func (p *RSSWatchPlugin) replyMedia(ctx context.Context, reply string, items []rssWatchItem, settings SettingValues) string {
	hits := rssWatchMatchedItems(reply, items)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	maxBytes := int64(max(1, min(2000, settings.Int(rssWatchSettingVideoMaxMB, defaultVideoMaxMB)))) * 1024 * 1024
	seen := make(map[string]bool)
	var videos strings.Builder
	checked := 0
	for i := range hits {
		hits[i].Images = slices.Clone(hits[i].Images)
		for _, video := range hits[i].Videos {
			raw := strings.TrimSpace(video.URL)
			if seen[raw] {
				continue
			}
			seen[raw] = true
			if checked >= 2 {
				break
			}
			checked++
			if p.videoWithinLimit(ctx, raw, maxBytes) {
				videos.WriteString("\n[CQ:video,file=" + escapeCQParameter(raw) + "]")
			} else {
				// An oversized video item sends only its cover, including quoted-photo posts.
				hits[i].Images = nil
				if rssWatchImageAllowed(video.ThumbnailURL) {
					hits[i].Images = []string{video.ThumbnailURL}
				}
			}
			break
		}
	}
	return rssWatchItemsImages(hits) + videos.String()
}

func (p *RSSWatchPlugin) videoWithinLimit(ctx context.Context, raw string, maxBytes int64) bool {
	if !rssWatchVideoAllowed(raw) {
		return false
	}
	client := *p.client
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !rssWatchVideoAllowed(req.URL.String()) {
			return fmt.Errorf("RSS video redirect rejected")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxBytes {
		return false
	}
	mediaType := strings.ToLower(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mediaType != "video/mp4" && mediaType != "application/octet-stream" {
		return false
	}
	// Content-Length is optional and untrusted: enforce the limit on bytes read.
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBytes+1))
	return err == nil && n > 0 && n <= maxBytes
}

// OneBot implementations can require video to be the sole message segment.
// Split before delivery so each fragment is sent and acknowledged separately.
func rssWatchMediaChunks(chunks []string) []string {
	var out []string
	for _, chunk := range chunks {
		var videos []string
		text := rssWatchVideoTagPattern.ReplaceAllStringFunc(chunk, func(tag string) string {
			segments := TextToOneBotSegments(tag)
			if len(segments) != 1 || !rssWatchVideoAllowed(segments[0].Data["file"]) {
				return tag
			}
			videos = append(videos, tag)
			return ""
		})
		if strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
		out = append(out, videos...)
	}
	return out
}

// Telegram 按链接发视频时由它的服务器自己去取，超过 20MB 一律拒收；本机上传的
// 上限是 50MB。订阅的视频上限默认 200MiB，是按 QQ 定的。
const (
	telegramURLVideoLimit       = 20 << 20
	rssWatchTelegramOversizeTip = "（视频超过 Telegram 50MB 上传上限，请到原推查看）"
)

// 测试里换成假的，免得投递测试真去连推特。
var rssWatchTelegramVideoClient = func() *http.Client {
	return netguard.NewPublicDownloadClient()
}

// rssWatchTelegramVideos 让推特视频在 Telegram 上发得出去：20MB 以内照旧交链接，
// 20–50MB 先下到本机再上传，更大的换成一句说明。返回的 release 要在发送完成后调用。
//
// 探不到大小（本机连不上推特）时照旧交链接，让 Telegram 自己去取。大小在 50MB
// 以内却下载失败算临时故障，报错让整条推送稍后重试。
func rssWatchTelegramVideos(ctx context.Context, client *http.Client, msg OutgoingMessage) (OutgoingMessage, func(), error) {
	var releases []func()
	release := func() {
		for _, fn := range releases {
			fn()
		}
	}
	videos := make([]string, 0, len(msg.VideoURLs))
	oversized := false
	for _, raw := range msg.VideoURLs {
		size := int64(0)
		if rssWatchVideoAllowed(raw) {
			size = rssWatchRemoteSize(ctx, client, raw)
		}
		if size <= telegramURLVideoLimit {
			videos = append(videos, raw)
			continue
		}
		if size > telegramMaxUploadBytes {
			oversized = true
			continue
		}
		downloadCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		path, _, done, err := acquireMediaDownload(downloadCtx, client, raw, "video.mp4", "", "public", telegramMaxUploadBytes)
		cancel()
		if err != nil {
			release()
			return msg, func() {}, fmt.Errorf("telegram: 下载订阅视频失败: %w", err)
		}
		releases = append(releases, done)
		videos = append(videos, path)
	}
	msg.VideoURLs = videos
	if oversized {
		msg.Text = strings.TrimSpace(msg.Text + "\n" + rssWatchTelegramOversizeTip)
	}
	return msg, release, nil
}

// rssWatchRemoteSize 返回远端文件大小，拿不到时返回 0。
func rssWatchRemoteSize(ctx context.Context, client *http.Client, raw string) int64 {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, raw, nil)
	if err != nil {
		return 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return 0
	}
	return resp.ContentLength
}
