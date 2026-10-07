// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"net/url"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

func screenshotImagePart(data []byte) (llm.ContentPart, error) {
	if len(data) == 0 || len(data) > maxScreenshotBytes {
		return llm.ContentPart{}, errors.New("截图为空或超过 8 MiB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return llm.ContentPart{}, errors.New("浏览器没有返回有效且尺寸受限的 PNG 截图")
	}
	return llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), Detail: "high"}, nil
}

// Screenshot hosts are exact host[:port] values, without schemes, paths or wildcards.
func ValidBrowserScreenshotHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	parsed, err := url.Parse("https://" + host)
	return err == nil && parsed.Host == host && parsed.Hostname() != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && !strings.ContainsAny(host, "*\\ \t\r\n/%?#")
}

func NormalizeBrowserScreenshotHosts(hosts []string) []string {
	var result []string
	seen := map[string]bool{}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if ValidBrowserScreenshotHost(host) && !seen[host] {
			seen[host] = true
			result = append(result, host)
		}
	}
	return result
}

func browserScreenshotHostAllowed(rawURL string, hosts []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Host == "" {
		return false
	}
	for _, host := range NormalizeBrowserScreenshotHosts(hosts) {
		if strings.ToLower(parsed.Host) == host {
			return true
		}
	}
	return false
}
