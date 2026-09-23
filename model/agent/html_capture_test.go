package agent

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func requireHTMLCaptureBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("DIANA_HEADLESS_BROWSER_PROBE_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_PROBE_INTEGRATION=1 to run against the installed browser")
	}
}

func TestHTMLCaptureClockDrivesAnimations(t *testing.T) {
	requireHTMLCaptureBrowser(t)
	// 左块靠 CSS 动画由黑变白，右块靠定时器在 500ms 变红，下块靠 rAF 按
	// performance.now 着色——三种时间源都要跟着虚拟时钟走。
	page := `<!doctype html><html><head><style>
body{margin:0;background:#fff}
#css{position:absolute;left:0;top:0;width:50px;height:50px;background:#000;animation:fade 1s linear forwards}
@keyframes fade{from{background:#000}to{background:#fff}}
#timer{position:absolute;left:60px;top:0;width:50px;height:50px;background:#00f}
canvas{position:absolute;left:0;top:60px}
</style></head><body><div id="css"></div><div id="timer"></div><canvas id="c" width="50" height="50"></canvas>
<script>
setTimeout(() => { document.getElementById('timer').style.background = '#f00'; }, 500);
const ctx = document.getElementById('c').getContext('2d');
function draw() { ctx.fillStyle = performance.now() >= 900 ? '#0f0' : '#000'; ctx.fillRect(0, 0, 50, 50); requestAnimationFrame(draw); }
requestAnimationFrame(draw);
</script></body></html>`
	var frames [][]byte
	size, err := CaptureHTMLFrames(context.Background(), HTMLCaptureRequest{HTML: page, Width: 120, Height: 120, Timeout: 30 * time.Second},
		HTMLFrameOptions{FPS: 10, Frames: 11, OnFrame: func(_ int, data []byte) error { frames = append(frames, data); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if size.Width != 120 || size.Height != 120 || len(frames) != 11 {
		t.Fatalf("size=%+v frames=%d", size, len(frames))
	}
	first, last := decodeCaptureFrame(t, frames[0]), decodeCaptureFrame(t, frames[10])
	assertCapturePixel(t, "css start", first, 25, 25, 0, 0, 0)
	assertCapturePixel(t, "css end", last, 25, 25, 255, 255, 255)
	assertCapturePixel(t, "timer start", first, 85, 25, 0, 0, 255)
	assertCapturePixel(t, "timer end", last, 85, 25, 255, 0, 0)
	assertCapturePixel(t, "raf start", first, 25, 85, 0, 0, 0)
	assertCapturePixel(t, "raf end", last, 25, 85, 0, 255, 0)
	// 第 5 帧（500ms）定时器刚好触发，中间帧的 CSS 动画在半程。
	middle := decodeCaptureFrame(t, frames[5])
	assertCapturePixel(t, "timer middle", middle, 85, 25, 255, 0, 0)
	if r, _, _, _ := middle.At(25, 25).RGBA(); r>>8 < 100 || r>>8 > 155 {
		t.Fatalf("css animation at 500ms should be mid-grey, got r=%d", r>>8)
	}
}

func TestHTMLCaptureBlocksNetworkAndLocalFiles(t *testing.T) {
	requireHTMLCaptureBrowser(t)
	page := `<!doctype html><html><body><script>
window.results = {};
const done = (key, value) => { window.results[key] = value; };
fetch('https://example.com/').then(() => done('fetch', 'ok'), () => done('fetch', 'blocked'));
const img = new Image(); img.onload = () => done('file', 'ok'); img.onerror = () => done('file', 'blocked'); img.src = 'file:///etc/hosts';
try { const ws = new WebSocket('wss://example.com/'); ws.onopen = () => done('ws', 'ok'); ws.onerror = () => done('ws', 'blocked'); } catch (e) { done('ws', 'blocked'); }
</script></body></html>`
	var results map[string]string
	err := withHTMLCaptureSession(context.Background(), HTMLCaptureRequest{HTML: page, Width: 100, Height: 100, Timeout: 30 * time.Second}, func(session *htmlCaptureSession) error {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if err := chromedp.Run(session.ctx, chromedp.Evaluate(`window.results`, &results)); err != nil {
				return err
			}
			if len(results) == 3 {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"fetch", "file", "ws"} {
		if results[key] != "blocked" {
			t.Fatalf("%s should be blocked, results=%v", key, results)
		}
	}
}

func TestHTMLCaptureStillFitsPageHeight(t *testing.T) {
	requireHTMLCaptureBrowser(t)
	page := `<!doctype html><html><body style="margin:0"><div style="height:1500px;background:linear-gradient(#fff,#000)"></div></body></html>`
	shot, size, err := CaptureHTMLStill(context.Background(), HTMLCaptureRequest{HTML: page, Width: 300, MaxHeight: 1200, Timeout: 30 * time.Second}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	bounds := decodeCaptureFrame(t, shot).Bounds()
	if size.Height != 1200 || bounds.Dx() != 300 || bounds.Dy() != 1200 {
		t.Fatalf("size=%+v bounds=%v", size, bounds)
	}
}

func decodeCaptureFrame(t *testing.T, data []byte) image.Image {
	t.Helper()
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func assertCapturePixel(t *testing.T, label string, img image.Image, x, y int, wantR, wantG, wantB uint32) {
	t.Helper()
	r, g, b, _ := img.At(x, y).RGBA()
	near := func(got, want uint32) bool { return got>>8+24 >= want && got>>8 <= want+24 }
	if !near(r, wantR) || !near(g, wantG) || !near(b, wantB) {
		t.Fatalf("%s: pixel(%d,%d)=%d,%d,%d want %d,%d,%d", label, x, y, r>>8, g>>8, b>>8, wantR, wantG, wantB)
	}
}
