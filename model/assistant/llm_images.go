// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/netguard"
)

const maxLLMImageBytes = 8 << 20

const maxConcurrentLLMImageLoads = 4

const (
	highDetailCropMinimumLongSide  = 2400
	highDetailCropMinimumShortSide = 1200
	highDetailCropLongSidePercent  = 60
)

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
	return downloadImageBytesWithLimit(ctx, imageURL, maxLLMImageBytes)
}

func downloadImageBytesWithLimit(ctx context.Context, imageURL string, maxBytes int64) ([]byte, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxBytes <= 0 {
		maxBytes = maxLLMImageBytes
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) == 0 {
		return nil, "", fmt.Errorf("image download returned empty body")
	}
	if int64(len(body)) > maxBytes {
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

// highDetailImageParts keeps the complete frame and adds two overlapping crops
// along its long edge. Some OpenAI-compatible providers accept detail=high but
// still downscale the full frame enough to lose small faces and text.
func highDetailImageParts(imageURL, detail string) []llm.ContentPart {
	original := llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: imageURL, Detail: detail}
	if !strings.EqualFold(strings.TrimSpace(detail), "high") {
		return []llm.ContentPart{original}
	}
	decoded, err := decodeDataURLImage(imageURL)
	if err != nil {
		return []llm.ContentPart{original}
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	longSide, shortSide := max(width, height), min(width, height)
	if longSide < highDetailCropMinimumLongSide || shortSide < highDetailCropMinimumShortSide {
		return []llm.ContentPart{original}
	}

	cropLongSide := longSide * highDetailCropLongSidePercent / 100
	starts := []int{0, longSide - cropLongSide}
	// Very tall screenshots need smaller windows than a simple top/bottom split;
	// otherwise each crop is still downscaled enough to make chat text unreadable.
	if readableLongSide := shortSide * 3 / 2; cropLongSide > readableLongSide {
		cropLongSide = readableLongSide
		starts = []int{0, (longSide - cropLongSide) / 2, longSide - cropLongSide}
	}
	parts := make([]llm.ContentPart, 0, 1+len(starts))
	parts = append(parts, original)
	for _, start := range starts {
		cropBounds := image.Rect(0, 0, width, height)
		if width >= height {
			cropBounds = image.Rect(bounds.Min.X+start, bounds.Min.Y, bounds.Min.X+start+cropLongSide, bounds.Max.Y)
		} else {
			cropBounds = image.Rect(bounds.Min.X, bounds.Min.Y+start, bounds.Max.X, bounds.Min.Y+start+cropLongSide)
		}
		crop := image.NewRGBA(image.Rect(0, 0, cropBounds.Dx(), cropBounds.Dy()))
		draw.Draw(crop, crop.Bounds(), decoded, cropBounds.Min, draw.Src)
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, crop, &jpeg.Options{Quality: 90}); err != nil {
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type:     llm.ContentPartImageURL,
			ImageURL: imageBytesAsDataURL(encoded.Bytes(), "image/jpeg"),
			Detail:   "high",
		})
	}
	return parts
}

func decodeDataURLImage(imageURL string) (image.Image, error) {
	prefix, encoded, ok := strings.Cut(strings.TrimSpace(imageURL), ",")
	if !ok || !strings.HasPrefix(strings.ToLower(prefix), "data:image/") || !strings.Contains(strings.ToLower(prefix), ";base64") {
		return nil, fmt.Errorf("image is not a base64 data URL")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	return decoded, err
}

func imageContentType(header string, body []byte) string {
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(header)); err == nil && strings.HasPrefix(mediaType, "image/") {
		return mediaType
	}
	return http.DetectContentType(body)
}
