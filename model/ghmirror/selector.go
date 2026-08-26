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

	// reachReadLimit 是连通性探测读取的字节数上限。这一步只回答「通不通、
	// 握手多久」，不下「快不快」的结论——那是后面测速要干的事。
	reachReadLimit = 32 << 10
	// reachDrainBudget 是探完连通性后清 body 的时间上限。清掉才能复用连接，
	// 省下测速那一步的握手；但线路慢的时候不能一直等，否则一条慢线路会把整轮
	// 并发探测拖成它一个人的等待时间。
	reachDrainBudget = 300 * time.Millisecond

	// speedSampleBytes / speedWindow 限定测速的取样规模：读满 2 MiB 或者读满
	// 2.5 秒就停，谁先到算谁。够算出一个稳定的速率，又不至于为了测速真的把
	// 整包拉下来。
	speedSampleBytes = 2 << 20
	speedWindow      = 2500 * time.Millisecond
	// speedMinSample 是能算出速度的最小样本。样本太小时，耗时几乎全是握手和
	// 首字节，除出来的数字只是延时的倒数，不是速度——这种情况宁可报「测不出」，
	// 也不报一个假的速率。
	speedMinSample = 64 << 10
	// speedCandidates 是除直连以外参与测速的镜像条数。测速要真的把数据拉下来，
	// 而且必须逐条串行——同时测多条会互相抢带宽，测出来的每条都比实际慢。所以
	// 先按握手快慢粗筛一遍，只给前几名做实测。
	speedCandidates = 3

	// directFastEnoughKBPS 是直连的「够快」线。直连能跑到这个速度就直接用，
	// 不再管镜像是不是还能更快：链路里少一个第三方，就少一处可能限速、插广告、
	// 或者哪天直接消失的环节，省下的那点时间换不来这个。
	directFastEnoughKBPS = 1024
	// directPreferenceFactor 是直连没到「够快」线时的优待系数：镜像得比直连快
	// 过这个倍数，才值得把下载交出去。
	directPreferenceFactor = 1.5

	probeUserAgent = "diana-release-updater"
)

// ProbeResult 是一条线路的实测结果。
//
// LatencyMS 是握手加首字节的耗时，SpeedKBPS 是拉取样本时的实际速率。两者分开
// 看：公共代理常常握手很快但限速很死，只看延时会把这种线路排到最前面。
// SpeedKBPS 为 0 表示样本太小、没测出速度，此时只能退回按延时比较。
type ProbeResult struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url,omitempty"`
	Direct    bool   `json:"direct,omitempty"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	SpeedKBPS int64  `json:"speed_kbps,omitempty"`
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

// NewSelector 创建线路选择器；client 为空时用一个默认客户端。超时要留够测速
// 那一段的读取时间，否则样本还没读完连接就被掐了。
func NewSelector(client *http.Client) *Selector {
	if client == nil {
		client = &http.Client{Timeout: defaultProbeTimeout + speedWindow}
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
// 线路确实供得出这个文件。传安装包地址而不是校验清单——清单只有几 KB，
// 读完了也测不出速度。
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

// Probe 实测所有线路，返回按「谁更值得用」排序的结果。
//
// 分两趟：先并发探一遍连通性和握手耗时，把不通的线路筛掉；再挑直连和握手最快
// 的几条镜像，逐条把样本真的拉一段下来算速率。并发能探连通性，但不能测速度——
// 十条线路同时拉，每条都只分到十分之一的带宽。
func (s *Selector) Probe(ctx context.Context, probeURL string) []ProbeResult {
	if !Accelerable(probeURL) {
		return nil
	}
	results := s.probeReachability(ctx, probeURL)
	s.measureSpeeds(ctx, probeURL, results)
	sortProbeResults(results)
	s.mu.Lock()
	s.lastProbe = append([]ProbeResult(nil), results...)
	s.mu.Unlock()
	return results
}

// probeReachability 并发探测每条线路通不通、握手多久，结果按候选顺序返回。
func (s *Selector) probeReachability(ctx context.Context, probeURL string) []ProbeResult {
	candidates := append([]Mirror{{Name: "直连 GitHub"}}, s.mirrors...)
	results := make([]ProbeResult, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Add(1)
		go func(index int, mirror Mirror) {
			defer wg.Done()
			result := ProbeResult{Name: mirror.Name, BaseURL: mirror.BaseURL, Direct: mirror.BaseURL == ""}
			elapsed, err := s.measureLatency(ctx, Rewrite(mirror.BaseURL, probeURL))
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
	return results
}

// measureSpeeds 给直连和握手最快的几条镜像逐条测速，把速率写回 results。
//
// 直连不受名额限制：用户的诉求是「直连够快就走直连」，那就必须每次都拿到直连
// 的真实速度，不能因为它握手慢就连测都不测——公共代理握手快、直连握手慢，
// 但下载速度常常反过来。
func (s *Selector) measureSpeeds(ctx context.Context, probeURL string, results []ProbeResult) {
	order := speedProbeOrder(results)
	for _, index := range order {
		results[index].SpeedKBPS = s.measureSpeed(ctx, Rewrite(results[index].BaseURL, probeURL))
	}
}

// speedProbeOrder 挑出要测速的线路下标：直连（如果通）加上握手最快的几条镜像。
func speedProbeOrder(results []ProbeResult) []int {
	mirrors := make([]int, 0, len(results))
	order := make([]int, 0, speedCandidates+1)
	for i := range results {
		if !results[i].OK {
			continue
		}
		if results[i].Direct {
			order = append(order, i)
			continue
		}
		mirrors = append(mirrors, i)
	}
	sort.SliceStable(mirrors, func(a, b int) bool {
		return results[mirrors[a]].LatencyMS < results[mirrors[b]].LatencyMS
	})
	if len(mirrors) > speedCandidates {
		mirrors = mirrors[:speedCandidates]
	}
	return append(order, mirrors...)
}

// measureLatency 发一次小请求，量握手加首字节的耗时。
func (s *Selector) measureLatency(ctx context.Context, rawURL string) (time.Duration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", probeUserAgent)
	// 只要开头一小段：够判断线路通不通，又不至于在探连通性时就把带宽占掉。
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", reachReadLimit-1))
	started := s.now()
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, &httpStatusError{StatusCode: resp.StatusCode}
	}
	elapsed := s.now().Sub(started)
	s.drain(resp.Body)
	return elapsed, nil
}

// drain 在预算内清掉一小段 body，让连接能复用；读不完就放手，反正接下来关掉
// 连接最多也只是多握一次手。
func (s *Selector) drain(body io.Reader) {
	deadline := s.now().Add(reachDrainBudget)
	buf := make([]byte, 8<<10)
	var read int64
	for read < reachReadLimit && s.now().Before(deadline) {
		n, err := body.Read(buf)
		read += int64(n)
		if err != nil {
			return
		}
	}
}

// measureSpeed 拉一段样本，返回实测速率（KiB/s）；样本不够大时返回 0，表示
// 这条线路测不出速度。
//
// 出错不当失败处理：读到一半断了、超时了，手里那段数据照样能算出速率，而且
// 「拉了 2.5 秒只拉到 200 KB」本身就是这条线路慢的证据。
func (s *Selector) measureSpeed(ctx context.Context, rawURL string) int64 {
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout+speedWindow)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", probeUserAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", speedSampleBytes-1))
	resp, err := s.client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	// 计时从拿到响应头开始：握手和首字节的耗时归延时那一栏，摊进速度里会把
	// 距离远但带宽足的线路（比如直连）冤枉成慢线路。
	started := s.now()
	deadline := started.Add(speedWindow)
	buf := make([]byte, 32<<10)
	var read int64
	for read < speedSampleBytes && s.now().Before(deadline) {
		n, readErr := resp.Body.Read(buf)
		read += int64(n)
		if readErr != nil {
			break
		}
	}
	elapsed := s.now().Sub(started)
	if read < speedMinSample || elapsed <= 0 {
		return 0
	}
	return int64(float64(read) / elapsed.Seconds() / 1024)
}

// sortProbeResults 把结果排成界面上的展示顺序：能用的在前，然后按谁更值得用。
func sortProbeResults(results []ProbeResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].OK != results[j].OK {
			return results[i].OK
		}
		if !results[i].OK {
			return false
		}
		return betterLine(results[i], results[j])
	})
}

// betterLine 报告 a 是不是比 b 更值得用：两条都测出速度就比速度，否则比握手。
func betterLine(a, b ProbeResult) bool {
	if a.SpeedKBPS > 0 && b.SpeedKBPS > 0 {
		return a.SpeedKBPS > b.SpeedKBPS
	}
	if (a.SpeedKBPS > 0) != (b.SpeedKBPS > 0) {
		// 只有一条测出了速度。测出来的那条是有据可依的，优先它。
		return a.SpeedKBPS > 0
	}
	return a.LatencyMS < b.LatencyMS
}

// fastestBase 从实测结果里挑一条线路：直连够快就走直连，除非镜像明显更快。
func fastestBase(results []ProbeResult) string {
	var direct *ProbeResult
	var best *ProbeResult
	for i := range results {
		if !results[i].OK {
			continue
		}
		if results[i].Direct {
			if direct == nil {
				direct = &results[i]
			}
			continue
		}
		if best == nil || betterLine(results[i], *best) {
			best = &results[i]
		}
	}
	switch {
	case best == nil:
		// 直连能用就直连；一条镜像都不通时也回落直连——真正的下载还会再试一次，
		// 错误信息也更好懂。
		return ""
	case direct == nil:
		return best.BaseURL
	}
	if preferDirect(*direct, *best) {
		return ""
	}
	return best.BaseURL
}

// preferDirect 决定要不要把下载交给镜像。
func preferDirect(direct, best ProbeResult) bool {
	switch {
	case direct.SpeedKBPS >= directFastEnoughKBPS:
		// 直连自己就够快，镜像再快也不换：多一个第三方不值这点时间。
		return true
	case direct.SpeedKBPS > 0 && best.SpeedKBPS > 0:
		return float64(best.SpeedKBPS) < float64(direct.SpeedKBPS)*directPreferenceFactor
	case best.SpeedKBPS > 0:
		// 直连连样本都拉不动，镜像拉得动：这时候该让镜像上。
		return false
	default:
		// 两条都没测出速度（样本太小），只能按握手快慢判断。
		return float64(direct.LatencyMS) <= float64(best.LatencyMS)*directPreferenceFactor
	}
}

type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}
