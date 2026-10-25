// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/SuInk/diana/internal/safego"
)

// cua-driver is fetched on demand the first time the owner turns desktop
// control on, so the release packages stay small for everyone who never uses
// it. The version is pinned: the driver's contract is still 0.x and changes
// between releases. Bumping it means re-running the live tests, then updating
// both hashes from the release's SHA256SUMS and the extracted binary.
const (
	CUADriverVersion = "0.38.0"
	cuaDriverRelease = "https://github.com/trycua/cua/releases/download/cua-driver-rs-v" + CUADriverVersion + "/"
	cuaDriverMaxSize = 200 << 20
)

type cuaDriverArtifact struct {
	asset         string
	archiveSHA256 string
	binarySHA256  string
}

// cuaDriverArtifacts lists the platforms Diana has verified. macOS ships a
// universal binary; Windows and Linux use the same adapter once validated.
var cuaDriverArtifacts = map[string]cuaDriverArtifact{
	"darwin": {
		asset:         "cua-driver-rs-" + CUADriverVersion + "-darwin-universal-binary.tar.gz",
		archiveSHA256: "2bb8dc4a7e69185ffd04c4a9c0eab590bd2c54b72c2c1be13ab675be4f487732",
		binarySHA256:  "92067848d0eeb9de76929fbd06a137af801b0ff6caedbb3109bd7a6cc84a1f9f",
	},
}

// CUADriverInstaller keeps one pinned cua-driver under dir.
type CUADriverInstaller struct {
	dir      string
	goos     string
	client   *http.Client
	mirror   func(ctx context.Context, rawURL string) string
	artifact *cuaDriverArtifact

	mu      sync.Mutex
	running bool
	failed  time.Time
	lastErr error
}

// NewCUADriverInstaller returns nil when this platform has no verified build.
// mirror rewrites a GitHub URL through the configured download mirror; nil
// means direct.
func NewCUADriverInstaller(dir string, client *http.Client, mirror func(ctx context.Context, rawURL string) string) *CUADriverInstaller {
	return newCUADriverInstaller(dir, runtime.GOOS, client, mirror)
}

func newCUADriverInstaller(dir, goos string, client *http.Client, mirror func(ctx context.Context, rawURL string) string) *CUADriverInstaller {
	artifact, ok := cuaDriverArtifacts[goos]
	if !ok || dir == "" {
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &CUADriverInstaller{dir: dir, goos: goos, client: client, mirror: mirror, artifact: &artifact}
}

func (i *CUADriverInstaller) binaryPath() string {
	name := "cua-driver"
	if i.goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(i.dir, CUADriverVersion, name)
}

// Installed returns the verified binary path, or "" when it is missing or its
// hash no longer matches the pin (a replaced binary is never run).
func (i *CUADriverInstaller) Installed() string {
	if i == nil {
		return ""
	}
	target := i.binaryPath()
	sum, err := fileSHA256(target)
	if err != nil || sum != i.artifact.binarySHA256 {
		return ""
	}
	return target
}

// Status reports an in-flight download or the last failure for the console.
func (i *CUADriverInstaller) Status() (installing bool, err error) {
	if i == nil {
		return false, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.running, i.lastErr
}

// Start downloads in the background unless already installed, running, or a
// recent attempt failed (retried after five minutes so a mirror outage does not
// turn into a download loop every Sync tick). done runs after a success.
func (i *CUADriverInstaller) Start(ctx context.Context, done func()) {
	if i == nil || i.Installed() != "" {
		return
	}
	i.mu.Lock()
	if i.running || (!i.failed.IsZero() && time.Since(i.failed) < 5*time.Minute) {
		i.mu.Unlock()
		return
	}
	i.running = true
	i.mu.Unlock()
	go func() {
		defer func() { safego.Report("desktopctl.cua-driver-install", recover()) }()
		err := i.install(ctx)
		i.mu.Lock()
		i.running, i.lastErr = false, err
		if err != nil {
			i.failed = time.Now()
		} else {
			i.failed = time.Time{}
		}
		i.mu.Unlock()
		if err == nil && done != nil {
			done()
		}
	}()
}

func (i *CUADriverInstaller) install(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(i.binaryPath()), 0o755); err != nil {
		return err
	}
	rawURL := cuaDriverRelease + i.artifact.asset
	downloadURL := rawURL
	if i.mirror != nil {
		if mirrored := i.mirror(ctx, rawURL); mirrored != "" {
			downloadURL = mirrored
		}
	}
	archive, err := os.CreateTemp(i.dir, ".cua-driver-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	if err := i.download(ctx, downloadURL, archive); err != nil {
		return err
	}
	if sum, err := fileSHA256(archive.Name()); err != nil || sum != i.artifact.archiveSHA256 {
		return fmt.Errorf("cua-driver 下载校验失败（SHA-256 不符），已丢弃")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return i.extract(archive)
}

func (i *CUADriverInstaller) download(ctx context.Context, rawURL string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		// The URL (often a signed CDN redirect) goes to the console; keep only the cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("下载 cua-driver 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 cua-driver 失败：HTTP %d", resp.StatusCode)
	}
	written, err := io.Copy(out, io.LimitReader(resp.Body, cuaDriverMaxSize+1))
	if err != nil {
		return fmt.Errorf("下载 cua-driver 失败：%w", err)
	}
	if written > cuaDriverMaxSize {
		return errors.New("cua-driver 安装包超过大小上限")
	}
	return nil
}

// extract writes only the driver executable; the SDK libraries and node
// bindings in the archive are not needed to run `cua-driver mcp`.
func (i *CUADriverInstaller) extract(archive io.Reader) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("cua-driver 安装包无法解压：%w", err)
	}
	defer gz.Close()
	want := path.Base(i.binaryPath())
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("cua-driver 安装包里没有可执行文件")
		}
		if err != nil {
			return fmt.Errorf("cua-driver 安装包无法解压：%w", err)
		}
		if header.Typeflag != tar.TypeReg || path.Base(path.Clean(header.Name)) != want {
			continue
		}
		if header.Size > cuaDriverMaxSize {
			return errors.New("cua-driver 可执行文件超过大小上限")
		}
		tmp, err := os.CreateTemp(filepath.Dir(i.binaryPath()), ".cua-driver-*")
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(reader, cuaDriverMaxSize))
		closeErr := tmp.Close()
		if copyErr != nil || closeErr != nil {
			os.Remove(tmp.Name())
			return fmt.Errorf("写入 cua-driver 失败：%v", errors.Join(copyErr, closeErr))
		}
		if hex.EncodeToString(hash.Sum(nil)) != i.artifact.binarySHA256 {
			os.Remove(tmp.Name())
			return errors.New("cua-driver 可执行文件校验失败（SHA-256 不符），已丢弃")
		}
		if err := os.Chmod(tmp.Name(), 0o755); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		if err := os.Rename(tmp.Name(), i.binaryPath()); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		return nil
	}
}

func fileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
