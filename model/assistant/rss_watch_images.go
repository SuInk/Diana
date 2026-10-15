package assistant

import (
	"net/url"
	"regexp"
	"strings"
)

var rssWatchContentURLPattern = regexp.MustCompile(`https?://[^\s<>"'）)\]]+`)

const (
	rssWatchMaxImagesPerItem   = 2
	rssWatchMaxImagesPerNotice = 4
)

// rssWatchReplyImages keeps photos out of the judge prompt and attaches only
// unambiguously cited items. A single-item batch needs no explicit citation.
func rssWatchReplyImages(reply string, items []rssWatchItem) string {
	replyLinks := rssWatchContentLinks(reply)
	owners := make(map[string]int)
	for _, item := range items {
		for link := range rssWatchContentLinks(item.Content) {
			owners[link]++
		}
	}
	hits := make([]rssWatchItem, 0, len(items))
	for _, item := range items {
		matched := len(items) == 1
		for link := range replyLinks {
			if link == strings.TrimSpace(item.Link) || rssWatchTweetLinkID(link) == strings.TrimSpace(item.ID) && rssWatchTweetLinkID(link) != "" {
				matched = true
			}
		}
		for link := range rssWatchContentLinks(item.Content) {
			if owners[link] == 1 && replyLinks[link] {
				matched = true
			}
		}
		if matched {
			hits = append(hits, item)
		}
	}
	seen := make(map[string]bool)
	var out strings.Builder
	total := 0
	for _, item := range hits {
		perItem := 0
		for _, raw := range item.Images {
			raw = strings.TrimSpace(raw)
			if total >= rssWatchMaxImagesPerNotice {
				return out.String()
			}
			if perItem >= rssWatchMaxImagesPerItem || !rssWatchImageAllowed(raw) || seen[raw] {
				continue
			}
			seen[raw] = true
			out.WriteString("\n[CQ:image,file=" + escapeCQParameter(strings.TrimSpace(raw)) + "]")
			perItem++
			total++
		}
	}
	return out.String()
}

func rssWatchImageAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	return parsed.User == nil && parsed.Port() == "" && strings.EqualFold(parsed.Host, "pbs.twimg.com")
}

// Exact URL tokens avoid matching /news against /news-other.
func rssWatchContentLinks(content string) map[string]bool {
	links := make(map[string]bool)
	for _, link := range rssWatchContentURLPattern.FindAllString(content, -1) {
		links[strings.TrimRight(link, ".,;:!?。，；：！？")] = true
	}
	return links
}

// Accept Twitter's alternate status URL forms without matching arbitrary numbers
// or IDs embedded in another host's URL.
func rssWatchTweetLinkID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	if host != "x.com" && host != "twitter.com" && host != "www.x.com" && host != "www.twitter.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 3 && parts[1] == "status" {
		return parts[2]
	}
	return ""
}

var rssWatchImageTagPattern = regexp.MustCompile(`\[CQ:image,file=[^\]]+\]`)

// Convert persisted photo tags to the shared media representation before sending,
// so native transports receive images rather than literal OneBot control text.
func rssWatchOutgoingImages(msg OutgoingMessage) OutgoingMessage {
	msg.Text = rssWatchImageTagPattern.ReplaceAllStringFunc(msg.Text, func(tag string) string {
		segments := TextToOneBotSegments(tag)
		if len(segments) != 1 || segments[0].Type != "image" {
			return tag
		}
		raw := segments[0].Data["file"]
		if !rssWatchImageAllowed(raw) {
			return tag
		}
		msg.ImageURLs = append(msg.ImageURLs, raw)
		return ""
	})
	msg.Text = strings.TrimSpace(msg.Text)
	return msg
}
