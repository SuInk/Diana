// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package ghmirror

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 握手快不等于下载快：公共代理常常秒回应答头，然后把速率压到几十 KB/s。
// 只看握手会把这种线路排在最前面，用户点了更新却发现半天下不完。
func TestSelectorPrefersThroughputOverHandshake(t *testing.T) {
	// 秒回，但发完头 64 KiB 就限速到 32 KiB/s——公共代理的典型形态。
	throttled := throttledServer(t, 0, 64<<10, 32<<10)
	defer throttled.Close()
	// 握手要 250 ms，之后带宽拉满。
	roomy := throttledServer(t, 250*time.Millisecond, 0, 0)
	defer roomy.Close()

	selector := newTestSelector([]Mirror{
		{Name: "限速线路", BaseURL: "https://throttled.invalid"},
		{Name: "带宽线路", BaseURL: "https://roomy.invalid"},
	})
	selector.client = &http.Client{
		Timeout: 8 * time.Second,
		Transport: probeRouter{mirrors: map[string]string{
			"https://throttled.invalid": throttled.URL,
			"https://roomy.invalid":     roomy.URL,
		}},
	}

	results := selector.Probe(context.Background(), releaseArchiveURL)
	throttledResult := findProbe(t, results, "限速线路")
	roomyResult := findProbe(t, results, "带宽线路")

	if throttledResult.LatencyMS >= roomyResult.LatencyMS {
		t.Fatalf("限速线路本该握手更快：限速 %d ms，带宽 %d ms", throttledResult.LatencyMS, roomyResult.LatencyMS)
	}
	if throttledResult.SpeedKBPS == 0 || roomyResult.SpeedKBPS == 0 {
		t.Fatalf("两条线路都该测出速度：%#v", results)
	}
	if throttledResult.SpeedKBPS >= roomyResult.SpeedKBPS {
		t.Fatalf("限速线路的速率不该更高：限速 %d KB/s，带宽 %d KB/s", throttledResult.SpeedKBPS, roomyResult.SpeedKBPS)
	}
	if base := fastestBase(results); base != "https://roomy.invalid" {
		t.Fatalf("挑线路只看了握手，没看速度：%q", base)
	}
}

// 直连够快就走直连，哪怕镜像握手更快、速率还更高：链路里少一个第三方，
// 值得让出这点速度。
func TestSelectorPrefersDirectWhenFastEnough(t *testing.T) {
	// 直连握手慢（国内连 GitHub 的常态），但带宽足。
	direct := throttledServer(t, 400*time.Millisecond, 0, 0)
	defer direct.Close()
	mirror := throttledServer(t, 0, 0, 0)
	defer mirror.Close()

	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: "https://mirror.invalid"}})
	selector.client = &http.Client{
		Timeout:   8 * time.Second,
		Transport: probeRouter{direct: direct.URL, mirrors: map[string]string{"https://mirror.invalid": mirror.URL}},
	}

	results := selector.Probe(context.Background(), releaseArchiveURL)
	directResult := findProbe(t, results, "直连 GitHub")
	mirrorResult := findProbe(t, results, "镜像")
	if directResult.LatencyMS <= mirrorResult.LatencyMS {
		t.Fatalf("这一局本该是直连握手更慢：直连 %d ms，镜像 %d ms", directResult.LatencyMS, mirrorResult.LatencyMS)
	}
	if directResult.SpeedKBPS < directFastEnoughKBPS {
		t.Fatalf("直连没测到「够快」线，这个用例说明不了问题：%d KB/s", directResult.SpeedKBPS)
	}
	if base := fastestBase(results); base != "" {
		t.Fatalf("直连已经够快，不该改走镜像：%q", base)
	}
}

// 直连拉不动样本时才把下载交给镜像。
func TestSelectorFallsBackWhenDirectCannotPullSample(t *testing.T) {
	direct := throttledServer(t, 0, 0, 8<<10)
	defer direct.Close()
	mirror := throttledServer(t, 0, 0, 0)
	defer mirror.Close()

	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: "https://mirror.invalid"}})
	selector.client = &http.Client{
		Timeout:   8 * time.Second,
		Transport: probeRouter{direct: direct.URL, mirrors: map[string]string{"https://mirror.invalid": mirror.URL}},
	}

	results := selector.Probe(context.Background(), releaseArchiveURL)
	if base := fastestBase(results); base != "https://mirror.invalid" {
		t.Fatalf("直连只有 8 KB/s，该让镜像来：%q", base)
	}
}

// 测速必须逐条串行，而且只测握手最快的几条。同时测多条会互相抢带宽，测出来
// 的每条都比实际慢；条条都测则会把测速本身变成一次大流量下载。
func TestSpeedProbeIsSerialAndBounded(t *testing.T) {
	var inFlight, peak, sampled int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := rangeLimit(r)
		if limit > reachReadLimit {
			atomic.AddInt64(&sampled, 1)
			current := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if current <= old || atomic.CompareAndSwapInt64(&peak, old, current) {
					break
				}
			}
			defer atomic.AddInt64(&inFlight, -1)
			time.Sleep(60 * time.Millisecond)
		}
		writeSample(w, r, limit, 0, 0)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	mirrors := make([]Mirror, 0, 6)
	routes := map[string]string{}
	for i := 0; i < 6; i++ {
		base := "https://mirror" + strconv.Itoa(i) + ".invalid"
		mirrors = append(mirrors, Mirror{Name: "镜像" + strconv.Itoa(i), BaseURL: base})
		routes[base] = server.URL
	}
	selector := newTestSelector(mirrors)
	selector.client = &http.Client{Timeout: 8 * time.Second, Transport: probeRouter{direct: server.URL, mirrors: routes}}

	selector.Probe(context.Background(), releaseArchiveURL)
	if peak != 1 {
		t.Fatalf("测速并发数 = %d，同时测多条会互相抢带宽", peak)
	}
	// 直连 + 握手最快的 speedCandidates 条镜像。
	if want := int64(speedCandidates + 1); sampled != want {
		t.Fatalf("测速线路条数 = %d，期望 %d", sampled, want)
	}
}

// 样本太小就老实说测不出速度，不要把延时的倒数当成速率报出去。
func TestSpeedUnmeasurableOnTinySample(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("SHA256SUMS 就这么大"))
	}))
	defer server.Close()

	selector := newTestSelector([]Mirror{{Name: "镜像", BaseURL: "https://mirror.invalid"}})
	selector.client = &http.Client{
		Timeout:   8 * time.Second,
		Transport: probeRouter{direct: server.URL, mirrors: map[string]string{"https://mirror.invalid": server.URL}},
	}

	results := selector.Probe(context.Background(), releaseArchiveURL)
	for _, result := range results {
		if !result.OK {
			continue
		}
		if result.SpeedKBPS != 0 {
			t.Fatalf("%s 的样本只有几十字节，不该报出速度 %d KB/s", result.Name, result.SpeedKBPS)
		}
	}
	// 测不出速度时退回老口径：按握手快慢挑，直连仍然优先。
	if base := fastestBase(results); base != "" {
		t.Fatalf("测不出速度时该退回按握手比较并优先直连：%q", base)
	}
}

const releaseArchiveURL = "https://github.com/SuInk/Diana/releases/download/v1/diana_linux_amd64.tar.gz"

func findProbe(t *testing.T, results []ProbeResult, name string) ProbeResult {
	t.Helper()
	for _, result := range results {
		if result.Name == name {
			return result
		}
	}
	t.Fatalf("实测结果里没有 %q：%#v", name, results)
	return ProbeResult{}
}

// throttledServer 造一条线路：ttfb 之后开始发数据，先按 burst 字节全速发，
// 之后按 rate 字节每秒限速。把「握手快慢」和「带宽大小」分成两个旋钮，才能
// 造出「秒回应答头但下载很慢」这种真实存在、老口径又看不出来的线路。
// rate 为 0 表示不限速。
func throttledServer(t *testing.T, ttfb time.Duration, burst, rate int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ttfb > 0 {
			time.Sleep(ttfb)
		}
		writeSample(w, r, rangeLimit(r), burst, rate)
	}))
}

func writeSample(w http.ResponseWriter, r *http.Request, limit, burst, rate int) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunk := make([]byte, 8<<10)
	sent := 0
	for sent < limit {
		size := len(chunk)
		if remaining := limit - sent; remaining < size {
			size = remaining
		}
		if _, err := w.Write(chunk[:size]); err != nil {
			return
		}
		sent += size
		if flusher != nil {
			flusher.Flush()
		}
		if r.Context().Err() != nil {
			return
		}
		if rate > 0 && sent > burst {
			time.Sleep(time.Duration(float64(size) / float64(rate) * float64(time.Second)))
		}
	}
}

// rangeLimit 读出这次请求要多少字节。探连通性和测速用的是不同的 Range 区间，
// 测试正是靠这个区分两种请求。
func rangeLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.Header.Get("Range"))
	raw = strings.TrimPrefix(raw, "bytes=")
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return reachReadLimit
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return reachReadLimit
	}
	return end + 1
}

// probeRouter 把探测请求按镜像前缀送到对应的本地服务，其余（也就是直连）送
// direct；direct 为空表示直连不通。Range 头要原样带过去，本地服务靠它区分
// 这次是探连通性还是测速。
type probeRouter struct {
	direct  string
	mirrors map[string]string
}

func (p probeRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	raw := req.URL.String()
	target := p.direct
	for base, local := range p.mirrors {
		if strings.HasPrefix(raw, base) {
			target = local
			break
		}
	}
	if target == "" {
		return nil, errors.New("直连不通")
	}
	routed, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		return nil, err
	}
	routed.Header = req.Header.Clone()
	return http.DefaultTransport.RoundTrip(routed)
}

// 握手都快到取整成 0 ms 时，直连不该被一次调度抖动踢掉。
//
// 这是个真的会发生的情形，不只是测试脆：LatencyMS 取整到毫秒，局域网镜像或者
// 很近的 CDN 节点会让两条线路都落在 0～1 ms。只用倍数判断的话，0 的 1.5 倍还是
// 0，直连多花的那 1 ms 就成了「慢一倍多」。
//
// 直接喂实测结果给挑选逻辑，不依赖真实计时——这个用例要么永远过要么永远挂，
// 不能自己也是个偶发。
func TestDirectSurvivesSubMillisecondJitter(t *testing.T) {
	cases := []struct {
		name       string
		directMS   int64
		mirrorMS   int64
		wantDirect bool
	}{
		{"都取整成 0", 0, 0, true},
		{"直连抖了 1 ms", 1, 0, true},
		{"直连抖了 3 ms", 3, 0, true},
		{"局域网镜像 vs 直连", 30, 1, true},
		{"差在宽限之内", 60, 12, true},
		// 宽限之外才谈倍数：120 > 12+50，且 120 > 12*1.5，镜像确实快得多。
		{"镜像确实快得多", 120, 12, false},
		// 宽限之外但倍数之内，仍然优待直连。
		{"慢一点但没到 1.5 倍", 260, 200, true},
	}
	for _, tc := range cases {
		results := []ProbeResult{
			{Name: "直连 GitHub", Direct: true, OK: true, LatencyMS: tc.directMS},
			{Name: "镜像", BaseURL: "https://mirror.invalid", OK: true, LatencyMS: tc.mirrorMS},
		}
		base := fastestBase(results)
		if gotDirect := base == ""; gotDirect != tc.wantDirect {
			t.Fatalf("%s：直连 %d ms，镜像 %d ms，选中 %q（期望走%s）",
				tc.name, tc.directMS, tc.mirrorMS, base, map[bool]string{true: "直连", false: "镜像"}[tc.wantDirect])
		}
	}
}

// 有速度可比时不受这个宽限影响：宽限只是握手兜底那条路的补丁。
func TestLatencySlackDoesNotOverrideMeasuredSpeed(t *testing.T) {
	results := []ProbeResult{
		// 直连握手更快，但只有 100 KB/s，离「够快」线还差得远。
		{Name: "直连 GitHub", Direct: true, OK: true, LatencyMS: 5, SpeedKBPS: 100},
		{Name: "镜像", BaseURL: "https://mirror.invalid", OK: true, LatencyMS: 40, SpeedKBPS: 800},
	}
	if base := fastestBase(results); base != "https://mirror.invalid" {
		t.Fatalf("镜像快 8 倍，不该因为直连握手快就留在直连：%q", base)
	}
}
