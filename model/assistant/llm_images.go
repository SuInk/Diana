package assistant

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

const maxLLMImageBytes = 8 << 20

const maxConcurrentLLMImageLoads = 4

func llmReadyImageURLs(ctx context.Context, imageURLs []string) []string {
	ready, _ := loadLLMImageURLs(ctx, imageURLs)
	out := make([]string, 0, len(ready))
	seen := map[string]struct{}{}
	for _, imageURL := range ready {
		if _, ok := seen[imageURL]; ok {
			continue
		}
		seen[imageURL] = struct{}{}
		out = append(out, imageURL)
	}
	return out
}

func loadLLMImageURLs(ctx context.Context, imageURLs []string) ([]string, bool) {
	if len(imageURLs) == 0 {
		return nil, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	inputs := make([]string, 0, len(imageURLs))
	for _, imageURL := range imageURLs {
		imageURL = strings.TrimSpace(imageURL)
		if imageURL == "" {
			continue
		}
		inputs = append(inputs, imageURL)
	}
	if len(inputs) == 0 {
		return nil, true
	}

	ready := make([]string, len(inputs))
	workerCount := min(len(inputs), maxConcurrentLLMImageLoads)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func(offset int) {
			defer workers.Done()
			for index := offset; index < len(inputs); index += workerCount {
				imageURL := inputs[index]
				var readyURL string
				switch {
				case strings.HasPrefix(imageURL, "data:image/"):
					readyURL = imageURL
				case strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://"):
					dataURL, err := fetchImageAsDataURL(ctx, imageURL)
					if err != nil {
						continue
					}
					readyURL = dataURL
				default:
					dataURL, err := localImageAsDataURL(imageURL)
					if err != nil {
						continue
					}
					readyURL = dataURL
				}
				ready[index] = readyURL
			}
		}(worker)
	}
	workers.Wait()

	out := make([]string, 0, len(ready))
	complete := true
	for _, readyURL := range ready {
		if readyURL == "" {
			complete = false
			continue
		}
		out = append(out, readyURL)
	}
	return out, complete
}

func fetchImageAsDataURL(ctx context.Context, imageURL string) (string, error) {
	body, contentType, err := downloadImageBytes(ctx, imageURL)
	if err != nil {
		return "", err
	}
	return imageBytesAsDataURL(body, contentType), nil
}

func downloadImageBytes(ctx context.Context, imageURL string) ([]byte, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36")

	resp, err := netguard.NewPublicHTTPClient(8 * time.Second).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("image download failed: status=%d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLLMImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 {
		return nil, "", fmt.Errorf("image download returned empty body")
	}
	if len(body) > maxLLMImageBytes {
		return nil, "", fmt.Errorf("image is too large")
	}

	contentType := imageContentType(resp.Header.Get("Content-Type"), body)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("downloaded content is not an image: %s", contentType)
	}
	return body, contentType, nil
}

func localImageAsDataURL(path string) (string, error) {
	path = strings.TrimSpace(strings.TrimPrefix(path, "file://"))
	if path == "" {
		return "", fmt.Errorf("image path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("image path is a directory")
	}
	if info.Size() <= 0 || info.Size() > maxLLMImageBytes {
		return "", fmt.Errorf("image size is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	contentType := imageContentType("", data)
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("local content is not an image: %s", contentType)
	}
	return imageBytesAsDataURL(data, contentType), nil
}

func imageBytesAsDataURL(data []byte, contentType string) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func imageContentType(header string, body []byte) string {
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(header)); err == nil && strings.HasPrefix(mediaType, "image/") {
		return mediaType
	}
	return http.DetectContentType(body)
}
