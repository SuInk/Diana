// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// HTTPSearchConfig adapts JSON search APIs without executing user templates.
// Paths use dot-separated object keys or array indices; empty results_path
// accepts a root array or a root object with a results array.
type HTTPSearchConfig struct {
	Method      string         `json:"method,omitempty"`
	AuthType    string         `json:"auth_type,omitempty"`
	AuthHeader  string         `json:"auth_header,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	ResultsPath string         `json:"results_path,omitempty"`
	URLPath     string         `json:"url_path,omitempty"`
	TitlePath   string         `json:"title_path,omitempty"`
	SnippetPath string         `json:"snippet_path,omitempty"`
}

var httpSearchPathPart = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var httpSearchHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~A-Za-z0-9-]+$")

func normalizeHTTPSearchConfig(provider *WebSearchProviderConfig) error {
	cfg := HTTPSearchConfig{}
	if provider.HTTPConfig != nil {
		cfg = *provider.HTTPConfig
	}
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.Method != http.MethodGet && cfg.Method != http.MethodPost {
		return errors.New("HTTP 搜索只支持 GET 或 POST")
	}
	cfg.AuthType = strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if cfg.AuthType == "" {
		cfg.AuthType = "none"
	}
	switch cfg.AuthType {
	case "none", "bearer":
		cfg.AuthHeader = ""
	case "header":
		cfg.AuthHeader = strings.TrimSpace(cfg.AuthHeader)
		if cfg.AuthHeader == "" {
			cfg.AuthHeader = "X-API-Key"
		}
		if !httpSearchHeaderName.MatchString(cfg.AuthHeader) {
			return errors.New("密钥请求头名称无效")
		}
		switch strings.ToLower(cfg.AuthHeader) {
		case "host", "content-length", "content-type", "connection", "transfer-encoding", "trailer":
			return errors.New("密钥请求头不能覆盖 HTTP 协议头")
		}
	default:
		return errors.New("HTTP 搜索认证方式无效")
	}
	for _, field := range []*string{&cfg.ResultsPath, &cfg.URLPath, &cfg.TitlePath, &cfg.SnippetPath} {
		*field = strings.TrimSpace(*field)
		if len(*field) > 256 || len(strings.Split(*field, ".")) > 16 {
			return errors.New("结果字段路径过长")
		}
		if *field != "" {
			for _, part := range strings.Split(*field, ".") {
				if !httpSearchPathPart.MatchString(part) {
					return errors.New("结果字段路径请使用点分隔的字段名或数组下标")
				}
			}
		}
	}
	if cfg.TitlePath == "" {
		cfg.TitlePath = "title"
	}
	// Clone params so snapshots and drafts never share mutable maps.
	if cfg.Params != nil {
		raw, err := json.Marshal(cfg.Params)
		if err != nil || len(raw) > 16*1024 {
			return errors.New("固定请求参数必须是最多 16 KiB 的 JSON 对象")
		}
		cfg.Params = nil
		if err := json.Unmarshal(raw, &cfg.Params); err != nil {
			return err
		}
	}
	provider.HTTPConfig = &cfg
	return nil
}

func (t *WebSearchTool) runHTTPSearch(ctx context.Context, provider WebSearchProviderConfig, query, apiKey string) (string, error) {
	cfg := provider.HTTPConfig
	if cfg == nil {
		return "", errors.New("HTTP 搜索配置缺失")
	}
	if cfg.AuthType != "none" && apiKey == "" {
		return "", errors.New("HTTP 搜索需要 API Key")
	}
	params := make(map[string]any, len(cfg.Params)+2)
	for key, value := range cfg.Params {
		resolved, err := resolveHTTPSearchParam(value, apiKey)
		if err != nil {
			return "", err
		}
		params[key] = resolved
	}
	params[provider.QueryParam] = query
	if provider.ResultsParam != "" {
		params[provider.ResultsParam] = provider.MaxResults
	}
	target := provider.URL
	var body []byte
	if cfg.Method == http.MethodGet {
		parsed, err := url.Parse(target)
		if err != nil {
			return "", err
		}
		values := parsed.Query()
		for key, value := range params {
			if text, ok := value.(string); ok {
				values.Set(key, text)
			} else {
				raw, err := json.Marshal(value)
				if err != nil {
					return "", err
				}
				values.Set(key, string(raw))
			}
		}
		parsed.RawQuery = values.Encode()
		target = parsed.String()
	} else {
		var err error
		body, err = json.Marshal(params)
		if err != nil {
			return "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, cfg.Method, target, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if cfg.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "github.com/SuInk/diana/0.1")
	switch cfg.AuthType {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+apiKey)
	case "header":
		req.Header.Set(cfg.AuthHeader, apiKey)
	}
	// Search endpoints should be configured directly. Do not forward custom
	// credential headers or body credentials to redirected services.
	client := *t.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP 搜索请求失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("搜索服务返回 HTTP %d", response.StatusCode)
	}
	raw, err := readWebSearchBody(response.Body)
	if err != nil {
		return "", err
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", errors.New("搜索响应不是有效 JSON")
	}
	items := httpSearchPathValue(payload, cfg.ResultsPath)
	if cfg.ResultsPath == "" {
		if object, ok := payload.(map[string]any); ok {
			items = object["results"]
		}
	}
	array, ok := items.([]any)
	if !ok {
		return "", errors.New("结果列表路径未找到 JSON 数组，请检查字段映射")
	}
	hits := []WebSearchHit{}
	seen := map[string]bool{}
	for _, item := range array {
		link := httpSearchString(item, cfg.URLPath, "url", "link")
		parsed, err := url.Parse(link)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			continue
		}
		key, normalized := canonicalWebSearchURL(link)
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, WebSearchHit{URL: normalized, Title: truncateRunes(httpSearchString(item, cfg.TitlePath), 500), Snippet: truncateRunes(httpSearchString(item, cfg.SnippetPath, "snippet", "content", "description"), 4000)})
		if len(hits) >= provider.MaxResults {
			break
		}
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("HTTP 搜索没有有效结果：%w", errWebSearchNoResults)
	}
	formatted, err := json.Marshal(map[string]any{"results": hits})
	return string(formatted), err
}

func resolveHTTPSearchParam(value any, apiKey string) (any, error) {
	switch typed := value.(type) {
	case string:
		if strings.Contains(typed, "{api_key}") && apiKey == "" {
			return nil, errors.New("固定参数引用了 {api_key}，请填写 API Key")
		}
		return strings.ReplaceAll(typed, "{api_key}", apiKey), nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := resolveHTTPSearchParam(item, apiKey)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			resolved, err := resolveHTTPSearchParam(item, apiKey)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}

func httpSearchPathValue(value any, path string) any {
	if path == "" {
		return value
	}
	for _, part := range strings.Split(path, ".") {
		switch typed := value.(type) {
		case map[string]any:
			value = typed[part]
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil
			}
			value = typed[index]
		default:
			return nil
		}
	}
	return value
}

func httpSearchString(item any, path string, defaults ...string) string {
	paths := []string{path}
	if path == "" {
		paths = defaults
	}
	for _, candidate := range paths {
		if text, ok := httpSearchPathValue(item, candidate).(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}
