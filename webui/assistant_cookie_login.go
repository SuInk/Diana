package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/netguard"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
)

type youtubeCookieLogin struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

func (h *BotHandler) testResolverCookieLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2*resolverCookieFileLimit)
	var payload struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || len(payload.Content) > resolverCookieFileLimit {
		c.JSON(400, gin.H{"error": "Cookie 请求无效或超过 1 MiB"})
		return
	}
	if err := validateResolverCookieContent(payload.Content); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	client := netguard.NewPublicHTTPClient(20 * time.Second)
	_, settings, _ := h.runtime.Plugins().PluginForConfiguration(assistant.ResolverPluginID, "")
	if proxy := strings.TrimSpace(settings.String("proxy_url", "")); proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" && parsed.Scheme != "socks5h") {
			c.JSON(200, youtubeCookieLogin{"unknown", "解析代理配置无效，无法检测登录状态。"})
			return
		}
		// The proxy is an administrator setting; the destination remains fixed.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyURL(parsed)
		client = &http.Client{Transport: transport, Timeout: 20 * time.Second}
	}
	defer client.CloseIdleConnections()
	c.JSON(200, probeYouTubeCookieLogin(c.Request.Context(), payload.Content, client))
}

func probeYouTubeCookieLogin(ctx context.Context, content string, client *http.Client) youtubeCookieLogin {
	unknown := youtubeCookieLogin{"unknown", "无法确认登录状态：网络、代理、风控或页面变化可能影响检测，请稍后重试。"}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.youtube.com/feed/library?hl=en", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_") {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(line, "#HttpOnly_"), "\t")
		if len(fields) != 7 {
			return unknown
		}
		domain := strings.TrimPrefix(strings.ToLower(fields[0]), ".")
		if domain != "www.youtube.com" && !(fields[1] == "TRUE" && domain == "youtube.com") {
			continue
		}
		path := fields[2]
		if req.URL.Path != path && !(strings.HasPrefix(req.URL.Path, path) && (strings.HasSuffix(path, "/") || strings.HasPrefix(strings.TrimPrefix(req.URL.Path, path), "/"))) {
			continue
		}
		expiry, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil || expiry != 0 && expiry <= uint64(time.Now().Unix()) {
			continue
		}
		cookie := &http.Cookie{Name: fields[5], Value: fields[6]}
		if cookie.Valid() != nil {
			return youtubeCookieLogin{"unknown", "Cookie 含有不能用于 HTTP 请求的名称或值，请重新导出。"}
		}
		req.AddCookie(cookie)
		count++
	}
	if count == 0 {
		return youtubeCookieLogin{"invalid", "没有适用于 YouTube 的未过期 Cookie，请登录 YouTube 后重新导出。"}
	}
	safeClient := *client
	safeClient.Jar = nil
	// Never forward draft credentials to a redirect destination or use ambient cookies.
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return unknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unknown
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil || len(body) > 4*1024*1024 {
		return unknown
	}
	state, found := youtubeLoggedInConfig(string(body))
	if !found {
		return unknown
	}
	if state {
		return youtubeCookieLogin{"valid", "YouTube 已确认登录。此结果不保证所有视频均可下载。"}
	}
	return youtubeCookieLogin{"invalid", "YouTube 返回未登录状态，这份 Cookie 未能登录，请重新导出。"}
}

var youtubeConfigSet = regexp.MustCompile(`\bytcfg\s*\.\s*set\s*\(\s*`)

// YouTube's own ytcfg boolean is evidence of login; HTTP 200 or cookie names are not.
func youtubeLoggedInConfig(page string) (bool, bool) {
	tokenizer := html.NewTokenizer(strings.NewReader(page))
	inScript := false
	var logged *bool
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if logged == nil {
				return false, false
			}
			return *logged, true
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			inScript = string(name) == "script"
		case html.EndTagToken:
			inScript = false
		case html.TextToken:
			if !inScript {
				continue
			}
			script := string(tokenizer.Text())
			for _, match := range youtubeConfigSet.FindAllStringIndex(script, -1) {
				var cfg struct {
					LoggedIn *bool `json:"LOGGED_IN"`
				}
				if json.NewDecoder(strings.NewReader(script[match[1]:])).Decode(&cfg) == nil && cfg.LoggedIn != nil {
					if logged != nil && *logged != *cfg.LoggedIn {
						return false, false
					}
					logged = cfg.LoggedIn
				}
			}
		}
	}
}
