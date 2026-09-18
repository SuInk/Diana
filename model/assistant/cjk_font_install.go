package assistant

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
	"golang.org/x/image/font/sfnt"
)

const (
	cjkFontDownloadURL    = "https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/OTF/SimplifiedChinese/NotoSansCJKsc-Regular.otf"
	cjkFontSHA256         = "2c76254f6fc379fddfce0a7e84fb5385bb135d3e399294f6eeb6680d0365b74b"
	cjkFontMaxBytes       = 32 << 20
	cjkFontInstallTimeout = 2 * time.Minute
)

//go:embed render_assets/NotoSansCJK-OFL.txt
var cjkFontLicense []byte

var cjkFontInstallGate = make(chan struct{}, 1)

func managedCJKFontPath() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "diana", "fonts", "noto-sans-cjk-sc-f8d15753", "NotoSansCJKsc-Regular.otf"), nil
}

func configuredCJKFont() bool {
	return strings.TrimSpace(os.Getenv(cjkFontEnvPrimary)) != "" || strings.TrimSpace(os.Getenv(cjkFontEnvFallback)) != ""
}

// ensureCJKFont is called only by an explicit install or an actual rendering
// request. Merely opening the dependency panel never downloads anything.
func ensureCJKFont(ctx context.Context) (*sfnt.Font, string, error) {
	if font, path, err := LoadCJKFont(); err == nil {
		return font, path, nil
	} else if configuredCJKFont() {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, cjkFontInstallTimeout)
	defer cancel()
	select {
	case cjkFontInstallGate <- struct{}{}:
		defer func() { <-cjkFontInstallGate }()
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	if font, path, err := LoadCJKFont(); err == nil {
		return font, path, nil
	}
	path, err := managedCJKFontPath()
	if err != nil {
		return nil, "", err
	}
	client := netguard.NewPublicHTTPClient(cjkFontInstallTimeout)
	if err := downloadCJKFont(ctx, client, cjkFontDownloadURL, path, cjkFontSHA256); err != nil {
		return nil, "", fmt.Errorf("中文字体下载失败（可在插件依赖管理中重试）：%w", err)
	}
	resetCJKFontCache()
	font, installed, err := LoadCJKFont()
	if err == nil {
		dep := cjkFontDependency()
		browserDepsMu.Lock()
		for i := range browserDepsCache {
			if browserDepsCache[i].Name == relationFontDependencyName {
				browserDepsCache[i] = dep
			}
		}
		browserDepsMu.Unlock()
	}
	return font, installed, err
}

// downloadCJKFont stages and validates a complete file before replacing the
// managed copy. Canceled/failed downloads never become usable font candidates.
func downloadCJKFont(ctx context.Context, client *http.Client, source, destination, wantHash string) error {
	return downloadVerifiedFont(ctx, client, source, destination, wantHash, cjkFontProbeRunes, cjkFontLicense)
}

func downloadVerifiedFont(ctx context.Context, client *http.Client, source, destination, wantHash string, glyphs []rune, license []byte) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".font-*.part")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	defer temporary.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Diana-font-installer")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("字体服务返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > cjkFontMaxBytes {
		return fmt.Errorf("字体文件超过 %d MiB 上限", cjkFontMaxBytes>>20)
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(resp.Body, cjkFontMaxBytes+1))
	if err != nil {
		return err
	}
	if size <= 0 || size > cjkFontMaxBytes {
		return fmt.Errorf("字体文件大小异常：%d", size)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != wantHash {
		return fmt.Errorf("字体 SHA-256 校验失败")
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	font, err := loadFontForGlyphs(tempPath, glyphs)
	if err != nil {
		return err
	}
	if !fontCoversGlyphs(font, glyphs) {
		return fmt.Errorf("下载的字体缺少所需字形")
	}
	// Keep the upstream redistribution license beside the installed font.
	if err := os.WriteFile(filepath.Join(parent, "OFL.txt"), license, 0600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("安装字体：%w", err)
	}
	return nil
}

func cjkFontDependency() ResolverDependency {
	dep := ResolverDependency{Name: relationFontDependencyName, Purpose: "中文字体：关系图与中文截图"}
	if _, path, err := LoadCJKFont(); err == nil {
		dep.Available = true
		dep.Path = path
		dep.Version = "可用"
	} else {
		dep.Detail = err.Error()
		if !configuredCJKFont() {
			if _, pathErr := managedCJKFontPath(); pathErr == nil {
				dep.Installable = true
				dep.Installer = "Diana 下载 Noto Sans CJK SC（约 16 MiB）"
			} else {
				dep.Detail += "；" + pathErr.Error()
			}
		}
	}
	return dep
}

func installCJKFontDependency(ctx context.Context) (ResolverDependencyInstallResult, error) {
	if _, _, err := ensureCJKFont(ctx); err != nil {
		return ResolverDependencyInstallResult{}, err
	}
	deps := RefreshBrowserDependencies()
	return ResolverDependencyInstallResult{Dependency: cjkFontDependency(), Plugins: browserDependencyGroup(deps), Installer: "Diana 下载 Noto Sans CJK SC"}, nil
}
