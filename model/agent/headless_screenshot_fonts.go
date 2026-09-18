package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Local generated pages with downloaded fonts need an explicit font-ready
// barrier; a complete PNG may otherwise contain only the font-loading blank.
func captureFontReadyScreenshot(ctx context.Context, req ScreenshotRequest) ([]byte, error) {
	executable, err := findHeadlessBrowserExecutable(req.Executable)
	if err != nil {
		return nil, err
	}
	dirs, err := newBrowserSandboxDirs("diana-font-screenshot-")
	if err != nil {
		return nil, err
	}
	defer dirs.remove()
	if len(req.FontFiles) > 16 {
		return nil, fmt.Errorf("too many screenshot font files")
	}
	for i, path := range req.FontFiles {
		if err := stageScreenshotFont(path, filepath.Join(dirs.root, fmt.Sprintf("diana-font-%d.ttf", i))); err != nil {
			return nil, err
		}
	}
	pagePath := filepath.Join(dirs.root, "page.html")
	if err := os.WriteFile(pagePath, []byte(req.HTML), 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	args := sandboxedChromeArgs(dirs.profile, dirs.cache, dirs.crash, sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{}))
	// Preserve the existing local-HTML screenshot isolation policy. Public
	// webpage rendering still uses launchSandboxedChrome without this flag.
	args = append(args, "--no-sandbox", "--disable-gpu", "about:blank")
	var extraEnv []string
	if runtime.GOOS == "linux" && len(req.FontFiles) > 0 {
		configPath, err := screenshotFontConfig(dirs.root, dirs.cache)
		if err != nil {
			return nil, err
		}
		extraEnv = []string{"FONTCONFIG_FILE=" + configPath}
	}
	process, err := launchChromeProcessWithEnv(ctx, executable, dirs.root, args, extraEnv)
	if err != nil {
		return nil, err
	}
	defer process.stop()
	allocator, cancelAllocator := chromedp.NewRemoteAllocator(ctx, process.wsURL, chromedp.NoModifyURL)
	defer cancelAllocator()
	browser, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	path := filepath.ToSlash(pagePath)
	if len(path) > 1 && path[1] == ':' {
		path = "/" + path
	}
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	diagnostics := newChromeDiagnosticBuffer(2048)
	chromedp.ListenTarget(browser, func(ev any) {
		if event, ok := ev.(*network.EventLoadingFailed); ok {
			diagnostics.Write([]byte(event.ErrorText + "\n"))
		}
		if event, ok := ev.(*cdplog.EventEntryAdded); ok && !strings.Contains(event.Entry.Text, "data:") {
			diagnostics.Write([]byte(event.Entry.Text + "\n"))
		}
	})
	actions := []chromedp.Action{cdplog.Enable(), network.Enable(), emulation.SetDeviceMetricsOverride(int64(req.Width), int64(req.Height), 1, false), chromedp.Navigate(fileURL)}
	if req.VirtualTimeBudget > 0 {
		actions = append(actions, chromedp.Sleep(min(req.VirtualTimeBudget, 2*time.Second)))
	}
	var fontsLoaded bool
	actions = append(actions, chromedp.Evaluate(`Promise.all(Array.from(document.fonts).map(face => face.load())).then(() => document.fonts.ready).then(() => true)`, &fontsLoaded, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return p.WithAwaitPromise(true) }))
	if err := chromedp.Run(browser, actions...); err != nil {
		return nil, fmt.Errorf("screenshot font readiness: %w: %s", err, diagnostics.String())
	}
	if !fontsLoaded {
		var states string
		_ = chromedp.Run(browser, chromedp.Evaluate(`JSON.stringify({faces:Array.from(document.fonts).map(f=>({family:f.family,status:f.status})),body:getComputedStyle(document.body).fontFamily})`, &states))
		return nil, fmt.Errorf("screenshot: 字体未能加载：%s %s", states, diagnostics.String())
	}
	var raw []byte
	if err := chromedp.Run(browser, chromedp.CaptureScreenshot(&raw)); err != nil {
		return nil, err
	}
	if len(raw) > maxScreenshotBytes {
		return nil, fmt.Errorf("screenshot exceeds size limit")
	}
	if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return raw, nil
}

func stageScreenshotFont(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	const limit = 32 << 20
	n, err := io.Copy(output, io.LimitReader(input, limit+1))
	if err != nil {
		return err
	}
	if n <= 0 || n > limit {
		return fmt.Errorf("screenshot font size invalid")
	}
	return output.Close()
}

// Fontconfig needs a usable base face even to load @font-face resources on
// minimal Linux images. Register staged fonts only for this browser process.
func screenshotFontConfig(root, cache string) (string, error) {
	var dirXML, cacheXML bytes.Buffer
	_ = xml.EscapeText(&dirXML, []byte(root))
	_ = xml.EscapeText(&cacheXML, []byte(cache))
	body := `<?xml version="1.0"?><!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd"><fontconfig><include ignore_missing="yes">/etc/fonts/fonts.conf</include><dir>` + dirXML.String() + `</dir><cachedir>` + cacheXML.String() + `</cachedir></fontconfig>`
	path := filepath.Join(root, "fonts.conf")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return "", err
	}
	return path, nil
}
