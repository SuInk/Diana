// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package ghmirror

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	defaultProbeTimeout = 6 * time.Second
	defaultCacheTTL     = 10 * time.Minute
	// probeReadLimit 是实测时最多读取的字节数。只要能读到头几十 KB，线路的
	// 连通性和大致速度就已经反映出来了，没必要为了测速真的把包拉完。
	probeReadLimit = 64 << 10
	// directPreferenceFactor 是直连的优待系数：只要直连不比最快的镜像慢过这个
	// 倍数，就还是走直连。链路里少一个第三方，就少一处可能限速、插广告、
	// 或者哪天直接消失的环节。
	directPreferenceFactor = 1.5
)

// ProbeResult 是一条线路的实测结果。
type ProbeResult struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url,omitempty"`
	Direct    bool   `json:"direct,omitempty"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Selector 按当前策略决定下载走哪条线路，并缓存实测结果。
type Selector struct {
	client       *http.Client
	mirrors      []Mirror
	ttl          time.Duration
	probeTimeout time.Duration
	now          func() time.Time

	mu         sync.Mutex
	mode       string
	cachedBase string
	cachedAt   time.Time
	lastProbe  []ProbeResult
}

// NewSelector 创建线路选择器；client 为空时用一个短超时的默认客户端。
func NewSelector(client *http.Client) *Selector {
	if client == nil {
		client = &http.Client{Timeout: defaultProbeTimeout}
	}
	return &Selector{
		client:       client,
		mirrors:      Builtin(),
		ttl:          defaultCacheTTL,
		probeTimeout: defaultProbeTimeout,
		now:          time.Now,
		mode:         ModeAuto,
	}
}

// SetMode 切换策略。策略变了就丢掉缓存，否则改完设置还得等缓存过期才生效。
func (s *Selector) SetMode(mode string) {
	normalized := NormalizeMode(mode)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == normalized {
		return
	}
	s.mode = normalized
	s.cachedBase = ""
	s.cachedAt = time.Time{}
}

// Mode 返回当前策略。
func (s *Selector) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// LastProbe 返回最近一次实测结果，供界面展示；没测过时返回空。
func (s *Selector) LastProbe() []ProbeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ProbeResult(nil), s.lastProbe...)
}

// Base 返回这次下载该用的镜像前缀，空字符串表示直连。
//
// probeURL 是即将下载的地址，自动模式下就拿它当测速样本：比起找一个固定的
// 探针文件，直接测「等会儿真要下的东西」既不用维护探针，也能顺带验证这条
// 线路确实供得出这个文件。
func (s *Selector) Base(ctx context.Context, probeURL string) string {
	s.mu.Lock()
	mode := s.mode
	cachedBase := s.cachedBase
	fresh := !s.cachedAt.IsZero() && s.now().Sub(s.cachedAt) < s.ttl
	s.mu.Unlock()

	switch mode {
	case ModeDirect:
		return ""
	case ModeAuto:
	default:
		// 用户手动指定了线路。
		return mode
	}
	if !Accelerable(probeURL) {
		return ""
	}
	if fresh {
		return cachedBase
	}
	results := s.Probe(ctx, probeURL)
	base := fastestBase(results)
	s.mu.Lock()
	s.cachedBase = base
	s.cachedAt = s.now()
	s.mu.Unlock()
	return base
}

// Probe 并发实测直连和每条候选线路，返回按耗时排序的结果。
func (s *Selector) Probe(ctx context.Context, probeURL string) []ProbeResult {
	if !Accelerable(probeURL) {
		return nil
	}
	candidates := append([]Mirror{{Name: "直连 GitHub"}}, s.mirrors...)
	results := make([]ProbeResult, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Add(1)
		go func(index int, mirror Mirror) {
			defer wg.Done()
			result := ProbeResult{Name: mirror.Name, BaseURL: mirror.BaseURL, Direct: mirror.BaseURL == ""}
			elapsed, err := s.measure(ctx, Rewrite(mirror.BaseURL, probeURL))
			if err != nil {
				result.Error = err.Error()
			} else {
				result.OK = true
				result.LatencyMS = elapsed.Milliseconds()
			}
			results[index] = result
		}(i, candidate)
	}
	wg.Wait()
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].OK != results[j].OK {
			return results[i].OK
		}
		if !results[i].OK {
			return false
		}
		return results[i].LatencyMS < results[j].LatencyMS
	})
	s.mu.Lock()
	s.lastProbe = append([]ProbeResult(nil), results...)
	s.mu.Unlock()
	return results
}

func (s *Selector) measure(ctx context.Context, rawURL string) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "diana-release-updater")
	// 只要开头一段：够判断线路通不通、快不快，又不至于为了测速把整包拉下来。
	req.Header.Set("Range", "bytes=0-65535")
	started := s.now()
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, &httpStatusError{StatusCode: resp.StatusCode}
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, probeReadLimit)); err != nil {
		return 0, err
	}
	return s.now().Sub(started), nil
}

// fastestBase 从实测结果里挑一条线路：直连能用就优先直连，除非它明显更慢。
func fastestBase(results []ProbeResult) string {
	var direct *ProbeResult
	var best *ProbeResult
	for i := range results {
		result := results[i]
		if !result.OK {
			continue
		}
		if result.Direct {
			if direct == nil {
				direct = &results[i]
			}
			continue
		}
		if best == nil || result.LatencyMS < best.LatencyMS {
			best = &results[i]
		}
	}
	switch {
	case direct != nil && best == nil:
		return ""
	case direct == nil && best != nil:
		return best.BaseURL
	case direct == nil && best == nil:
		// 全都不通。回落直连：真正的下载还会重试一次，错误信息也更好懂。
		return ""
	}
	if float64(direct.LatencyMS) <= float64(best.LatencyMS)*directPreferenceFactor {
		return ""
	}
	return best.BaseURL
}

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}
