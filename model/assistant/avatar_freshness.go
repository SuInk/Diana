// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 头像「换没换过」的证据。
//
// 线上：主人手动换了机器人的 QQ 头像，十来分钟后群友说「Diana头像变了」，机器人
// 先回「头像根本没变啦 你喝多眼花了吧」；被要求再看一眼，它真的用 view_avatar 取回
// 了新头像，也描述对了，却仍然说「一点没变」。它手里只有一张图：没有上一次的样子
// 可比，也不知道这张图是什么时候换上的，只好顺着自己上一句往下说。
//
// 这里补两样现成、不花额外请求的证据：平台响应里的最后修改时间，以及本进程上一次
// 看到的机器人头像指纹。都交给模型自己判断，不替它下「换了」的结论。

// qlogo 的 Last-Modified 实际是北京时间，却标着 GMT：线上头像在北京时间 00:54 换的，
// 响应头写的是「00:54:02 GMT」。按字面当 UTC 会晚出 8 小时，算出来在未来。
var avatarBeijingZone = time.FixedZone("CST", 8*60*60)

// 服务器时钟和本机时钟总有点偏差，超出这个量才认为时间「在未来」。
const avatarLastModifiedFutureTolerance = 5 * time.Minute

type avatarImageMeta struct {
	// Hash 是原始图片字节的指纹，用来和上一次看到的比对。
	Hash string
	// UpdatedAt 是平台给出的头像最后修改时间，拿不到时为零值。
	UpdatedAt time.Time
}

// fetchAvatarImage 是头像下载入口，测试里换成本地服务。
var fetchAvatarImage = downloadImageBytesWithHeader

// loadAvatarImage 读一张头像并交回给模型的第一张图和它的元信息。
//
// 通用的 loadLLMImageURLs 不带响应头；头像这条路单独下载，是为了拿到 Last-Modified，
// 不去改其他调用方的行为。Telegram 等平台交来的是 data URL，没有修改时间，只留指纹。
func loadAvatarImage(ctx context.Context, source string, now time.Time) (string, avatarImageMeta, error) {
	source = strings.TrimSpace(source)
	var meta avatarImageMeta
	var ready []string
	switch {
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		body, contentType, header, err := fetchAvatarImage(ctx, source, maxLLMImageSourceBytes)
		if err != nil {
			return "", meta, err
		}
		ready, err = normalizeLLMImageParts(body, contentType)
		if err != nil {
			return "", meta, err
		}
		meta.Hash = avatarContentHash(body)
		meta.UpdatedAt = avatarLastModified(source, header.Get("Last-Modified"), now)
	case strings.HasPrefix(source, "data:image/"):
		var err error
		ready, err = normalizeLLMDataURLParts(source)
		if err != nil {
			return "", meta, err
		}
		meta.Hash = avatarContentHash([]byte(source))
	default:
		return "", meta, fmt.Errorf("不支持的头像来源")
	}
	if len(ready) == 0 {
		return "", meta, fmt.Errorf("头像解码结果为空")
	}
	return ready[0], meta, nil
}

func avatarContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// avatarLastModified 把响应头里的 Last-Modified 换成真实时刻，靠不住时返回零值。
//
// qlogo 一律按北京时间解释。别的来源按 HTTP 规范当 GMT；但算出来明显在未来时，多半
// 也是把北京时间标成了 GMT，退回按北京时间再算一次。仍然在未来就不给，宁可不说，
// 也不能拿一个假时间去说服模型。
func avatarLastModified(imageURL, raw string, now time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := http.ParseTime(raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}
	}
	asBeijing := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), avatarBeijingZone)
	candidate := parsed
	if isQLogoURL(imageURL) || (!now.IsZero() && parsed.After(now.Add(avatarLastModifiedFutureTolerance))) {
		candidate = asBeijing
	}
	if !now.IsZero() && candidate.After(now.Add(avatarLastModifiedFutureTolerance)) {
		return time.Time{}
	}
	return candidate
}

func isQLogoURL(imageURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "qlogo.cn" || strings.HasSuffix(host, ".qlogo.cn")
}

// formatAvatarUpdateHint 把更新时间换成一句人话。模型对「00:54」离现在多久不敏感，
// 直接告诉它「约 11 分钟前」，它才会意识到群友说的「刚换了」对得上。
func formatAvatarUpdateHint(updatedAt, now time.Time) string {
	age := now.Sub(updatedAt)
	switch {
	case age < time.Minute:
		return "头像刚刚更新过"
	case age < time.Hour:
		return fmt.Sprintf("头像约 %d 分钟前更新过", int(age/time.Minute))
	case age < 48*time.Hour:
		return fmt.Sprintf("头像约 %d 小时前更新过", int(age/time.Hour))
	case age < 60*24*time.Hour:
		return fmt.Sprintf("头像约 %d 天前更新过", int(age/(24*time.Hour)))
	default:
		return fmt.Sprintf("头像约 %d 个月前更新过", int(age/(30*24*time.Hour)))
	}
}

// botAvatarMemory 记每台机器人上一次看到的自己头像指纹。只在内存里：重启后第一次
// 看没有可比的，照实不说就行；为了这件事落库或定时轮询都不值当。自带锁，不受 Runtime.mu 保护。
type botAvatarMemory struct {
	mu      sync.Mutex
	entries map[string]botAvatarSighting
}

type botAvatarSighting struct {
	hash string
	at   time.Time
}

// observe 记下这一次看到的指纹，并交回上一次的记录（没有时 ok 为 false）。
func (m *botAvatarMemory) observe(key, hash string, at time.Time) (botAvatarSighting, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]botAvatarSighting{}
	}
	previous, ok := m.entries[key]
	m.entries[key] = botAvatarSighting{hash: hash, at: at}
	return previous, ok
}

// avatarFreshnessFields 给 view_avatar 的结果补上更新时间和「和上次比」两样证据。
func (r *Runtime) avatarFreshnessFields(event MessageEvent, source string, meta avatarImageMeta, now time.Time) map[string]any {
	fields := map[string]any{}
	if !meta.UpdatedAt.IsZero() {
		fields["updated_at"] = meta.UpdatedAt.In(now.Location()).Format("2006-01-02 15:04")
		fields["updated_hint"] = formatAvatarUpdateHint(meta.UpdatedAt, now)
	}
	if source != avatarSourceBot || meta.Hash == "" || r == nil {
		return fields
	}
	botID := r.avatarBotID(event)
	if botID == "" {
		return fields
	}
	previous, ok := r.botAvatars.observe(r.currentPlatform(event)+"\x00"+botID, meta.Hash, now)
	if !ok {
		return fields
	}
	seenAt := previous.at.In(now.Location()).Format("2006-01-02 15:04")
	fields["last_viewed_at"] = seenAt
	fields["same_as_last_view"] = previous.hash == meta.Hash
	if previous.hash == meta.Hash {
		fields["change_note"] = "和上次（" + seenAt + "）看到的是同一张"
	} else {
		fields["change_note"] = "和上次（" + seenAt + "）看到的不一样，头像在这之后换过"
	}
	return fields
}
