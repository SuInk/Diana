package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

func TestRenderFontNeedsMultipleScripts(t *testing.T) {
	groups, unknown := renderFontNeeds("中文 日本語 한국어 English Ελληνικά Русский العربية עברית ไทย हिन्दी বাংলা தமிழ் → 😀👍🏽🇨🇳👩‍💻")
	for _, name := range []string{"cjk", "NotoSans", "NotoSansArabic", "NotoSansHebrew", "NotoSansThai", "NotoSansDevanagari", "NotoSansBengali", "NotoSansTamil", "NotoSansSymbols2", "NotoEmoji"} {
		if len(groups[name]) == 0 {
			t.Errorf("script %s not detected", name)
		}
	}
	if len(unknown) != 0 {
		t.Fatalf("unexpected unsupported characters: %U", unknown)
	}
}
func TestRenderFontDetectionIgnoresScriptsAndStyles(t *testing.T) {
	text := visibleRenderText(`<style>.x{font-family:"中文"}</style><script>let s="العربية";</script><p>English</p><pre>日本語</pre>`)
	if strings.Contains(text, "العربية") || strings.Contains(text, "中文") || !strings.Contains(text, "日本語") {
		t.Fatalf("wrong visible text: %s", text)
	}
}
func TestRenderFontReportsUnsupportedCharacters(t *testing.T) {
	_, unknown := renderFontNeeds("𓀀")
	if len(unknown) != 1 {
		t.Fatalf("unsupported script silently ignored: %U", unknown)
	}
}
func TestRenderFontsMultilingualIntegration(t *testing.T) {
	if os.Getenv("DIANA_RENDER_FONTS_INTEGRATION") != "1" {
		t.Skip("requires isolated browser and download access")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	page := `<!doctype html><html><head><meta charset="utf-8"><style>body{background:white;color:#132435;font-size:24px;margin:22px}p{margin:10px 0}</style></head><body><p>中文 日本語 한국어</p><p>English · Ελληνικά · Русский</p><p dir="auto">العربية: مرحبا بالعالم</p><p dir="auto">עברית: שלום עולם</p><p>ไทย: สวัสดี</p><p>हिन्दी: नमस्ते</p><p>বাংলা: বাংলা</p><p>தமிழ்: வணக்கம்</p><p>Emoji: 😀 👍🏽 🇨🇳 👩‍💻</p><p>Symbols: ⮕ ⬛ → ✓</p></body></html>`
	page, files, err := prepareRenderFontHTML(ctx, page)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 7 {
		t.Fatalf("expected downloaded script fonts in empty-font environment, got %v", files)
	}
	raw, err := agent.CaptureHTMLScreenshot(ctx, agent.ScreenshotRequest{HTML: page, FontFiles: files, WaitForFonts: true, Width: 1000, Height: 750, Timeout: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if inkInHeader(decodePNG(t, raw)) < 100 {
		t.Fatal("blank multilingual screenshot")
	}
	if output := os.Getenv("DIANA_CJK_FONT_TEST_OUTPUT"); output != "" {
		if err := os.WriteFile(filepath.Join(output, "multilingual.png"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("used %d font files; multilingual screenshot=%d bytes", len(files), len(raw))
}

func TestRelationRasterRequiresShapingForComplexScripts(t *testing.T) {
	font, err := loadCJKFont("testdata/fonts/diana-cjk-test.otf")
	if err != nil {
		t.Fatal(err)
	}
	if relationTextNeedsBrowser(font, "关系图") {
		t.Fatal("simple CJK unexpectedly requires browser")
	}
	for _, text := range []string{"العربية", "नमस्ते", "ไทย", "😀"} {
		if !relationTextNeedsBrowser(font, text) {
			t.Fatalf("complex text silently sent to single-font raster: %s", text)
		}
	}
}

func TestRenderMarkdownHighDensityIntegration(t *testing.T) {
	if os.Getenv("DIANA_RENDER_FONTS_INTEGRATION") != "1" {
		t.Skip("requires installed browser and fonts")
	}
	content := "## 清晰度测试\n\n|项目 / System|核心定位 / Positioning|处理模型 / Processing Model|状态与一致性 / State and Consistency|主要优势 / Strengths|适用场景 / Best Fit|\n|---|---|---|---|---|---|\n|中文小字|边缘清晰，保持排版|`fmt.Println(42)`|清晰文字与细线|0123456789|长表头自动换行|\n\n> 中文、数字 0123456789 与细线表格。"
	if path := os.Getenv("DIANA_RENDER_REPLAY_CONTENT"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content = string(raw)
	}
	page, err := buildRenderPage(renderFormatMarkdown, content, "Markdown 高清渲染")
	if err != nil {
		t.Fatal(err)
	}
	page, files, err := prepareRenderFontHTML(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := agent.CaptureHTMLScreenshot(context.Background(), agent.ScreenshotRequest{HTML: page, FontFiles: files, Width: renderImageWidth, Height: renderImageMaxHeight, DeviceScaleFactor: 2})
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, raw)
	if img.Bounds().Dx() != renderImageWidth*2 || img.Bounds().Dy() != renderImageMaxHeight*2 {
		t.Fatalf("unexpected bounds %v", img.Bounds())
	}
	if inkInHeader(img) < 100 {
		t.Fatal("empty rendering")
	}
	bounds := img.Bounds()
	if right := lastContentColumn(img, bounds, img.At(0, 0)); right >= bounds.Max.X-24 {
		t.Fatalf("table reaches screenshot edge: right=%d width=%d", right, bounds.Max.X)
	}
	if output := os.Getenv("DIANA_CJK_FONT_TEST_OUTPUT"); output != "" {
		if err := os.WriteFile(filepath.Join(output, "markdown-hidpi.png"), trimRenderScreenshot(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// 下载过的回退字体直接复用，不能拿某个字去验它、验不过就再下一遍然后整张图失
// 败——Symbols2 连当初的样例字 → 都没有。
func TestRenderScriptFontReusesDownload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	raw, err := os.ReadFile("testdata/fonts/diana-cjk-test.otf")
	if err != nil {
		t.Fatal(err)
	}
	asset := renderFontAsset{name: "DianaCachedTest", sha: "0123456789abcdef", hints: []string{"dianacachedtestnohint"}}
	root, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "diana", "fonts", asset.name+"-"+asset.sha[:8], asset.name+".ttf")
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一旦走到下载就会因为 ctx 已取消而失败
	path, err := ensureRenderScriptFont(ctx, asset)
	if err != nil {
		t.Fatalf("cached font should be reused, got %v", err)
	}
	if path != destination {
		t.Fatalf("got path=%s", path)
	}
}
