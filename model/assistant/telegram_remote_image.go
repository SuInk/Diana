package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

// 测试里换成假的，免得真去连外网图床。
var telegramRemoteImageClient = func() *http.Client {
	return netguard.NewPublicDownloadClient()
}

// telegramURLFetchRejected 判断 Telegram 是不是因为「它自己的服务器拉不到这个链接」
// 拒收：图床挡了 Telegram 的抓取、返回的不是图片页面等。换成本机下载再上传就能发，
// 别的 400（图片本身坏了、聊天不存在）换了上传也一样被拒，不在这里兜。
func telegramURLFetchRejected(err error) bool {
	var apiErr *telegramAPIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusBadRequest {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "failed to get http url content") ||
		strings.Contains(description, "wrong type of the web page content") ||
		strings.Contains(description, "wrong file identifier/http url specified")
}

func telegramRemoteHTTPURL(source string) bool {
	parsed, err := url.Parse(strings.TrimSpace(source))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// sendRemoteImageUpload 在 Telegram 拉不到图片链接时由本机下载，再按文件上传。
// 下载走公网下载客户端（拦内网地址），上限按 Bot API 的 50MB 上传限制；超过 10MB 的
// 照片由 telegramImageMethod 改成按文件发。
func (c *TelegramChannel) sendRemoteImageUpload(ctx context.Context, chatID, threadID, source string, replyIDs ...string) (map[string]any, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, time.Minute)
	path, _, release, err := acquireMediaDownload(downloadCtx, telegramRemoteImageClient(), source, "image", "", "public", telegramMaxUploadBytes)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("telegram: 图片链接 Telegram 拉不到，本机下载也失败: %w", err)
	}
	defer release()
	method, field, err := telegramImageMethod(path)
	if err != nil {
		return nil, err
	}
	return c.uploadMedia(ctx, method, chatID, threadID, field, path, replyIDs...)
}
