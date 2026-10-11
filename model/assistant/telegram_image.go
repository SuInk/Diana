package assistant

import (
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Cached images may have hash filenames with no extension. Inspect their bytes
// before choosing the Bot API method: GIFs are animations, not photos, and WebP
// stickers must use sendSticker. Oversized/unusual still images remain documents.
func telegramImageMethod(source string) (method, field string, err error) {
	source = strings.TrimSpace(source)
	if path := telegramLocalPath(source); path != "" {
		file, err := os.Open(path)
		if err != nil {
			return "", "", fmt.Errorf("telegram: read image: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return "", "", err
		}
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("telegram: image must be a regular file")
		}
		var header [512]byte
		n, err := file.Read(header[:])
		if err != nil && err != io.EOF {
			return "", "", err
		}
		mime := http.DetectContentType(header[:n])
		if mime == "image/gif" {
			return "sendAnimation", "animation", nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", "", err
		}
		cfg, _, decodeErr := image.DecodeConfig(file)
		if mime == "image/webp" {
			animated := n > 20 && string(header[12:16]) == "VP8X" && header[20]&0x02 != 0
			if !animated && decodeErr == nil && info.Size() <= 512<<10 &&
				((cfg.Width == 512 && cfg.Height <= 512) || (cfg.Height == 512 && cfg.Width <= 512)) {
				return "sendSticker", "sticker", nil
			}
			// General WebP pictures need not satisfy Telegram's sticker limits.
			return "sendDocument", "document", nil
		}
		if info.Size() > 10<<20 || (decodeErr == nil &&
			(cfg.Width+cfg.Height > 10000 || cfg.Width > 20*cfg.Height || cfg.Height > 20*cfg.Width)) {
			return "sendDocument", "document", nil
		}
		return "sendPhoto", "photo", nil
	}
	// URLs can carry a query string; file IDs are opaque and stay photo IDs.
	if parsed, err := url.Parse(source); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		switch strings.ToLower(filepath.Ext(parsed.Path)) {
		case ".gif":
			return "sendAnimation", "animation", nil
		case ".webp":
			return "sendSticker", "sticker", nil
		}
	}
	return "sendPhoto", "photo", nil
}
