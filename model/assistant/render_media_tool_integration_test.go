//go:build browser_integration

package assistant

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 真实浏览器 + ffmpeg 跑一遍完整编码链。
func TestRenderMediaEncodesVideoAndGIFIntegration(t *testing.T) {
	ffmpeg, err := lookResolverCommand("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	html := `<!doctype html><html><body style="margin:0;background:#123"><canvas id="c" width="320" height="180"></canvas><script>
const ctx = document.getElementById('c').getContext('2d');
function draw(t) { ctx.fillStyle = '#123'; ctx.fillRect(0, 0, 320, 180); ctx.fillStyle = '#fc0'; ctx.beginPath(); ctx.arc(20 + t / 5, 90, 20, 0, Math.PI * 2); ctx.fill(); requestAnimationFrame(draw); }
requestAnimationFrame(draw);
</script></body></html>`
	for _, output := range []string{renderMediaOutputVideo, renderMediaOutputGIF} {
		t.Run(output, func(t *testing.T) {
			tool := newDianaRenderMediaTool(nil, MessageEvent{}, nil, RelationshipPolicy{})
			spec, err := tool.parse(map[string]any{"format": "html", "content": html, "output": output, "duration": 1, "width": 321, "height": 181})
			if err != nil {
				t.Fatal(err)
			}
			request, err := buildRenderMediaRequest(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			path, size, err := encodeRenderMedia(context.Background(), ffmpeg, request, spec, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if size.Width != 321 || size.Height != 181 {
				t.Fatalf("size=%+v", size)
			}
			switch output {
			case renderMediaOutputVideo:
				if filepath.Ext(path) != ".mp4" || !bytes.Contains(data[:64], []byte("ftyp")) {
					t.Fatalf("not an mp4: %s", path)
				}
			case renderMediaOutputGIF:
				if !bytes.HasPrefix(data, []byte("GIF89a")) {
					t.Fatalf("not a gif: %s", path)
				}
			}
		})
	}
}
