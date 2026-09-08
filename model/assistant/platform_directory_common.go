package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

func platformRequestURL(base, path, method string, params map[string]any) (string, error) {
	u, err := url.Parse(base + path)
	if err != nil {
		return "", fmt.Errorf("invalid platform API URL")
	}
	if method == http.MethodGet {
		q := u.Query()
		for key, value := range params {
			if value == nil {
				continue
			}
			q.Del(key)
			switch v := value.(type) {
			case []string:
				for _, item := range v {
					q.Add(key, item)
				}
			case []any:
				for _, item := range v {
					q.Add(key, fmt.Sprint(item))
				}
			default:
				q.Set(key, fmt.Sprint(v))
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func directoryData(data map[string]any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"code", "errcode"} {
		if intFromAny(data[key]) != 0 {
			return nil, fmt.Errorf("平台拒绝查询 (%s=%v): %s", key, data[key], firstNonEmpty(stringFromAny(data["msg"]), stringFromAny(data["errmsg"]), stringFromAny(data["message"])))
		}
	}
	if success, ok := data["success"].(bool); ok && !success {
		return nil, fmt.Errorf("平台查询未成功")
	}
	if nested, ok := data["data"].(map[string]any); ok {
		return nested, nil
	}
	if nested, ok := data["result"].(map[string]any); ok {
		return nested, nil
	}
	return data, nil
}

func directoryStrings(value any) []string {
	var out []string
	switch v := value.(type) {
	case []any:
		for _, x := range v {
			out = append(out, stringFromAny(x))
		}
	case []string:
		out = append(out, v...)
	}
	return out
}

func directoryRows(value any) []map[string]any {
	var out []map[string]any
	if raw, ok := value.(json.RawMessage); ok {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			return directoryRows(decoded)
		}
	}
	if rows, ok := value.([]any); ok {
		for _, row := range rows {
			if item, ok := row.(map[string]any); ok {
				out = append(out, item)
			}
		}
	}
	return out
}

func platformAvatar(ctx context.Context, source string) (GroupAvatar, error) {
	u, err := url.Parse(strings.TrimSpace(source))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return GroupAvatar{}, fmt.Errorf("平台未返回可访问的头像地址")
	}
	client := netguard.NewPublicHTTPClient(15 * time.Second)
	checkRedirect := client.CheckRedirect
	credentialed := u.Query().Get("access_token") != "" || u.Query().Get("token") != ""
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if credentialed {
			return http.ErrUseLastResponse
		}
		req.Header.Del("Referer")
		return checkRedirect(req, via)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return GroupAvatar{}, fmt.Errorf("无效头像请求")
	}
	resp, err := client.Do(req)
	if err != nil {
		return GroupAvatar{}, fmt.Errorf("平台头像下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GroupAvatar{}, fmt.Errorf("平台头像下载 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, telegramGroupAvatarMaxBytes+1))
	if err != nil || len(body) == 0 || len(body) > telegramGroupAvatarMaxBytes {
		return GroupAvatar{}, fmt.Errorf("平台头像下载失败")
	}
	mime := http.DetectContentType(body)
	if !strings.HasPrefix(mime, "image/") {
		return GroupAvatar{}, fmt.Errorf("平台头像不是图片")
	}
	return GroupAvatar{Data: body, ContentType: mime}, nil
}

type directoryEventKey struct{}

func withDirectoryEvent(ctx context.Context, event MessageEvent) context.Context {
	return context.WithValue(ctx, directoryEventKey{}, event)
}
func directoryEvent(ctx context.Context) MessageEvent {
	event, _ := ctx.Value(directoryEventKey{}).(MessageEvent)
	return event
}

func requireDirectoryID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("需要明确的平台标识")
	}
	return nil
}
func directoryRole(userID, owner string, admins []string) string {
	if userID == owner && owner != "" {
		return "owner"
	}
	for _, id := range admins {
		if id == userID {
			return "admin"
		}
	}
	return "member"
}
func directoryInt(v any) int {
	if text, ok := v.(string); ok {
		n, _ := strconv.Atoi(text)
		return n
	}
	return intFromAny(v)
}

func safePlatformError(err error, endpoint string, headers map[string]string) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	text := err.Error()
	if u, e := url.Parse(endpoint); e == nil {
		for key, values := range u.Query() {
			key = strings.ToLower(key)
			if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") {
				for _, v := range values {
					if v != "" {
						text = strings.ReplaceAll(text, v, "[redacted]")
					}
				}
			}
		}
	}
	for key, value := range headers {
		key = strings.ToLower(key)
		if strings.Contains(key, "authorization") || strings.Contains(key, "token") {
			for _, v := range append([]string{value}, strings.Fields(value)...) {
				if len(v) > 5 {
					text = strings.ReplaceAll(text, v, "[redacted]")
				}
			}
		}
	}
	return errors.New(text)
}
