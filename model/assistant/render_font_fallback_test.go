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
	page := `<!doctype html><html><head><meta charset="utf-8"><style>body{background:white;color:#132435;font-size:24px;margin:22px}p{margin:10px 0}</style></head><body><p>中文 日本語 한국어</p><p>English · Ελληνικά · Русский</p><p dir="auto">العربية: مرحبا بالعالم</p><p dir="auto">עברית: שלום עולם</p><p>ไทย: สวัสดี</p><p>हिन्दी: नमस्ते</p><p>বাংলা: বাংলা</p><p>தமிழ்: வணக்கம்</p><p>Emoji: 😀 👍🏽 🇨🇳 👩‍💻</p></body></html>`
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
