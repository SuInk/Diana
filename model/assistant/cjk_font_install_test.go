package assistant

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"golang.org/x/image/font/gofont/goregular"
)

func cjkTestFont(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/fonts/diana-cjk-test.otf")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestCJKFontDownloadValidatesAndInstallsAtomically(t *testing.T) {
	raw := cjkTestFont(t)
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "NotoSansCJKsc-Regular.otf")
	if err := downloadCJKFont(context.Background(), server.Client(), server.URL, destination, sum); err != nil {
		t.Fatal(err)
	}
	font, err := loadCJKFont(destination)
	if err != nil || !fontHasCJKGlyphs(font) {
		t.Fatalf("unusable installed font: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "OFL.txt")); err != nil {
		t.Fatal("missing font license", err)
	}
	before, _ := os.ReadFile(destination)
	if err := downloadCJKFont(context.Background(), server.Client(), server.URL, destination, "wrong"); err == nil {
		t.Fatal("bad checksum accepted")
	}
	after, _ := os.ReadFile(destination)
	if string(before) != string(after) {
		t.Fatal("failed download replaced existing font")
	}
	parts, _ := filepath.Glob(filepath.Join(filepath.Dir(destination), "*.part"))
	if len(parts) > 0 {
		t.Fatal("partial downloads left behind", parts)
	}
}
func TestCJKFontDownloadRejectsLatinFontAndHTTPFailure(t *testing.T) {
	for _, status := range []int{200, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); w.Write(goregular.TTF) }))
			defer server.Close()
			destination := filepath.Join(t.TempDir(), "font.otf")
			err := downloadCJKFont(context.Background(), server.Client(), server.URL, destination, fmt.Sprintf("%x", sha256.Sum256(goregular.TTF)))
			if err == nil {
				t.Fatal("invalid download accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("failed file became installed")
			}
		})
	}
}
func TestCJKFontCacheRecoversAfterMissingFontIsInstalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "font.otf")
	t.Setenv(cjkFontEnvPrimary, path)
	resetCJKFontCache()
	t.Cleanup(resetCJKFontCache)
	if _, _, err := LoadCJKFont(); err == nil {
		t.Fatal("missing font accepted")
	}
	if err := os.WriteFile(path, cjkTestFont(t), 0600); err != nil {
		t.Fatal(err)
	}
	if _, got, err := LoadCJKFont(); err != nil || got != path {
		t.Fatalf("missing-font result cached permanently: %q %v", got, err)
	}
	html, fontFiles, err := prepareRenderFontHTML(context.Background(), "<!doctype html><html><head></head><body>关系图</body></html>")
	if err != nil || !strings.Contains(html, "diana-font-0.ttf") || len(fontFiles) != 1 {
		t.Fatalf("private font not embedded for Chrome: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCJKFont(); err == nil {
		t.Fatal("removed font remained available in the cache")
	}

}
func TestCJKFontDownloadCancellationLeavesNoInstalledFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(t.TempDir(), "font.otf")
	err := downloadCJKFont(ctx, http.DefaultClient, "https://example.invalid/font", destination, "irrelevant")
	if err == nil {
		t.Fatal("canceled download accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("canceled download published a file")
	}
}
func TestCJKFontInstallAndScreenshotIntegration(t *testing.T) {
	if os.Getenv("DIANA_CJK_FONT_INSTALL_INTEGRATION") != "1" {
		t.Skip("requires an isolated environment without CJK fonts")
	}
	resetCJKFontCache()
	t.Cleanup(resetCJKFontCache)
	if _, _, err := LoadCJKFont(); err == nil && os.Getenv("DIANA_CJK_FONT_TEST_REUSE") != "1" {
		t.Fatal("test requires no installed CJK fonts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dep := cjkFontDependency()
	if !dep.Installable && !dep.Available {
		t.Fatalf("missing font is not installable: %+v", dep)
	}
	if _, _, err := ensureCJKFont(ctx); err != nil {
		t.Fatal(err)
	}
	dep = cjkFontDependency()
	if !dep.Available {
		t.Fatalf("font unavailable after install: %+v", dep)
	}
	html, fontFiles, err := prepareRenderFontHTML(ctx, `<!doctype html><html><head><meta charset="utf-8"></head><body style="background:white;color:#123;padding:12px;font-size:22px"><h2>中文字体下载验证成功</h2><p>关系图、机器人、网页截图</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := agent.CaptureHTMLScreenshot(ctx, agent.ScreenshotRequest{HTML: html, WaitForFonts: true, FontFiles: fontFiles, Width: 720, Height: 360, Timeout: 20 * time.Second, VirtualTimeBudget: 1000 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if inkInHeader(decodePNG(t, raw)) < 100 {
		t.Fatal("font screenshot contains no rendered text")
	}
	raster, err := RenderGroupRelationPNG(relationTestGraph(), "中文字体下载验证成功", "近 7 天", 12)
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("DIANA_CJK_FONT_TEST_OUTPUT"); output != "" {
		if err := os.WriteFile(filepath.Join(output, "font-browser.png"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, "font-raster.png"), raster, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("installed %s; browser screenshot=%d bytes; raster=%d bytes", dep.Path, len(raw), len(raster))
}
