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
			} else if rssWatchImageAllowed(video.ThumbnailURL) {
				// An oversized video item sends only its cover, including quoted-photo posts.
				hits[i].Images = []string{video.ThumbnailURL}
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
