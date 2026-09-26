// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/browserbox"

	"github.com/gorilla/websocket"
)

// 实时画面的实测：真起一个 Chrome，走完整的 /api/browser-box/live，在限速的连接上量
// 帧率、画面滞后和「点一下到画面变」的延迟。要本机有 Chrome，默认不跑：
//
//	DIANA_BROWSER_LIVE_PERF=1 go test ./webui -run TestBrowserBoxLivePerf -v -count=1
const livePerfPage = `<!doctype html><html><body style="margin:0;background:#fff;overflow:hidden">
<div id="sq" style="position:fixed;left:0;top:0;width:200px;height:200px;background:rgb(255,0,0);z-index:2"></div>
<canvas id="c" width="1280" height="800" style="position:fixed;left:0;top:0"></canvas>
<script>
const sq = document.getElementById("sq"); let red = true;
document.addEventListener("mousedown", () => { red = !red; sq.style.background = red ? "rgb(255,0,0)" : "rgb(0,0,255)"; });
const c = document.getElementById("c").getContext("2d");
const dots = Array.from({length: 120}, (_, i) => ({x: Math.random()*1280, y: Math.random()*800, vx: Math.random()*6-3, vy: Math.random()*6-3, h: i*3}));
function tick(t) {
  c.fillStyle = "#f4f4f4"; c.fillRect(0, 0, 1280, 800);
  for (const d of dots) { d.x = (d.x + d.vx + 1280) % 1280; d.y = (d.y + d.vy + 800) % 800;
    c.fillStyle = "hsl(" + d.h + ",70%,50%)"; c.beginPath(); c.arc(d.x, d.y, 18, 0, 7); c.fill(); }
  c.fillStyle = "#222"; c.font = "28px sans-serif";
  for (let i = 0; i < 12; i++) c.fillText("Diana 实时画面 " + Math.floor(t) + " 第 " + i + " 行文字，模拟一整页正在滚动的内容", 220, 60 + i * 60);
  requestAnimationFrame(tick);
}
requestAnimationFrame(tick);
</script></body></html>`

type throttledConn struct {
	net.Conn
	bytesPerSecond int
}

func (c *throttledConn) Read(p []byte) (int, error) {
	if c.bytesPerSecond <= 0 {
		return c.Conn.Read(p)
	}
	const chunk = 8 << 10
	if len(p) > chunk {
		p = p[:chunk]
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		time.Sleep(time.Duration(float64(n) / float64(c.bytesPerSecond) * float64(time.Second)))
	}
	return n, err
}

type perfFrame struct {
	received  time.Time
	size      int
	timestamp float64
	red       bool
}

// decodePerfFrame 认两种格式：旧的 JSON 文本帧（base64 JPEG），和二进制帧
// （4 字节大端元数据长度 + 元数据 JSON + JPEG）。
func decodePerfFrame(kind int, data []byte) (perfFrame, bool) {
	var meta browserbox.Frame
	var jpegBytes []byte
	switch kind {
	case websocket.TextMessage:
		var message struct {
			Type  string `json:"type"`
			Frame struct {
				Data      string  `json:"data"`
				Timestamp float64 `json:"timestamp"`
			} `json:"frame"`
		}
		if json.Unmarshal(data, &message) != nil || message.Type != "frame" {
			return perfFrame{}, false
		}
		meta.Timestamp = message.Frame.Timestamp
		decoded, err := base64.StdEncoding.DecodeString(message.Frame.Data)
		if err != nil {
			return perfFrame{}, false
		}
		jpegBytes = decoded
	case websocket.BinaryMessage:
		if len(data) < 4 {
			return perfFrame{}, false
		}
		n := int(binary.BigEndian.Uint32(data))
		if json.Unmarshal(data[4:4+n], &meta) != nil {
			return perfFrame{}, false
		}
		jpegBytes = data[4+n:]
	default:
		return perfFrame{}, false
	}
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return perfFrame{}, false
	}
	r, _, b, _ := img.At(100*img.Bounds().Dx()/1280, 100*img.Bounds().Dy()/800).RGBA()
	return perfFrame{received: time.Now(), size: len(data), timestamp: meta.Timestamp, red: r > b}, true
}

func TestBrowserBoxLivePerf(t *testing.T) {
	if os.Getenv("DIANA_BROWSER_LIVE_PERF") == "" {
		t.Skip("设 DIANA_BROWSER_LIVE_PERF=1 才跑，要本机有 Chrome")
	}
	router, manager := newBrowserBoxRouter(t)
	ctx := context.Background()
	if _, err := manager.SetSettings(ctx, browserbox.Settings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	bot := manager.Bot("perf")
	if err := bot.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.StopAll)

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(livePerfPage))
	}))
	t.Cleanup(page.Close)
	tab, err := browserbox.OpenTab(ctx, bot.CDPURL(), page.URL)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	rates := []struct {
		name string
		bps  int
	}{{"不限速", 0}, {"2MB/s", 2 << 20}, {"500KB/s", 500 << 10}}
	for _, rate := range rates {
		t.Run(rate.name, func(t *testing.T) {
			runLivePerf(t, server.URL, bot.ID(), tab.ID, rate.bps)
			bot.SetTakeover(false)
		})
	}
}

func runLivePerf(t *testing.T, serverURL, botID, tabID string, bps int) {
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &throttledConn{Conn: conn, bytesPerSecond: bps}, nil
	}}
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/api/browser-box/live?bot=" + botID + "&tab=" + tabID
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var mu sync.Mutex
	var frames []perfFrame
	var writeMu sync.Mutex
	send := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(v)
	}
	go func() {
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frame, ok := decodePerfFrame(kind, data)
			if !ok {
				continue
			}
			mu.Lock()
			frames = append(frames, frame)
			mu.Unlock()
			send(map[string]any{"type": "ack"})
		}
	}()
	snapshot := func() []perfFrame {
		mu.Lock()
		defer mu.Unlock()
		return append([]perfFrame(nil), frames...)
	}

	// 先让画面稳定下来，再量一段纯看的帧率和滞后。
	time.Sleep(2 * time.Second)
	start := len(snapshot())
	began := time.Now()
	time.Sleep(5 * time.Second)
	window := snapshot()[start:]
	elapsed := time.Since(began).Seconds()
	var totalBytes int
	var ages []float64
	for _, f := range window {
		totalBytes += f.size
		if f.timestamp > 0 {
			ages = append(ages, float64(f.received.UnixNano())/1e9-f.timestamp)
		}
	}

	// 点一下画面，看多久之后收到颜色已经翻过来的一帧。
	var clicks []float64
	for i := 0; i < 8; i++ {
		before := snapshot()
		if len(before) == 0 {
			t.Fatal("没有收到画面")
		}
		wasRed := before[len(before)-1].red
		mark := len(before)
		clicked := time.Now()
		send(map[string]any{"type": "mouse", "mouse": map[string]any{"type": "mousePressed", "x": 600, "y": 400, "button": "left", "buttons": 1, "click_count": 1}})
		send(map[string]any{"type": "mouse", "mouse": map[string]any{"type": "mouseReleased", "x": 600, "y": 400, "button": "left", "click_count": 1}})
		deadline := clicked.Add(5 * time.Second)
		for time.Now().Before(deadline) {
			all := snapshot()
			found := false
			for _, f := range all[mark:] {
				if f.red != wasRed {
					clicks = append(clicks, f.received.Sub(clicked).Seconds()*1000)
					found = true
					break
				}
			}
			if found {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(600 * time.Millisecond)
	}

	fmt.Printf("  帧率 %.1f fps，平均每帧 %.0f KB，吞吐 %.2f MB/s，画面滞后 %s，点击到画面变 %s（%d/8 次看到）\n",
		float64(len(window))/elapsed, float64(totalBytes)/float64(max(len(window), 1))/1024,
		float64(totalBytes)/elapsed/(1<<20), percentiles(ages, 1000), percentiles(clicks, 1), len(clicks))
}

func percentiles(values []float64, scale float64) string {
	if len(values) == 0 {
		return "无数据"
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	at := func(p float64) float64 { return sorted[int(p*float64(len(sorted)-1))] * scale }
	return fmt.Sprintf("p50 %.0fms / p90 %.0fms / max %.0fms", at(0.5), at(0.9), at(1))
}
