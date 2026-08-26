// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/netguard"
	golangdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var (
	errLLMImageSourceTooLarge  = errors.New("image exceeds the source size limit")
	errLLMImageDecodeFailed    = errors.New("image format could not be decoded")
	errLLMImageDimensions      = errors.New("image dimensions are invalid or too large")
	errLLMImagePayloadTooLarge = errors.New("image exceeds the model payload limit after resizing")
)

const (
	maxLLMImageBytes           = 8 << 20
	maxLLMImageSourceBytes     = 32 << 20
	maxLLMImageWidth           = 2000
	maxLLMImageHeight          = 2000
	maxLLMImageBase64Bytes     = 9 << 19 // 4.5 MiB，给 Anthropic 的 5 MiB 限制留余量。
	maxLLMImagePixels          = 80_000_000
	defaultLLMJPEGQuality      = 80
	imageResizeStepNumerator   = 3
	imageResizeStepDenominator = 4
	longImageAspectNumerator   = 5
	longImageAspectDenominator = 2
	longImageTileNumerator     = 3
	longImageTileDenominator   = 2
	longImageTileOverlap       = 20
	maxLongImageTiles          = 8
)

const maxConcurrentLLMImageLoads = 4

const (
	highDetailCropMinimumLongSide  = 2400
	highDetailCropMinimumShortSide = 1200
	highDetailCropLongSidePercent  = 60
)

func llmReadyImageURLs(ctx context.Context, imageURLs []string) []string {
	groups, _ := loadLLMImageURLGroupsDetailed(ctx, imageURLs)
	return flattenLLMImageGroups(dedupeLLMImageGroups(groups))
}

func loadLLMImageURLs(ctx context.Context, imageURLs []string) ([]string, bool) {
	ready, failures := loadLLMImageURLsDetailed(ctx, imageURLs)
	return ready, len(failures) == 0
}

func loadLLMImageURLsDetailed(ctx context.Context, imageURLs []string) ([]string, []error) {
	groups, failures := loadLLMImageURLGroupsDetailed(ctx, imageURLs)
	return flattenLLMImageGroups(groups), failures
}

func loadLLMImageURLGroupsDetailed(ctx context.Context, imageURLs []string) ([][]string, []error) {
	if len(imageURLs) == 0 {
		return nil, nil
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
		return nil, nil
	}

	ready := make([][]string, len(inputs))
	failures := make([]error, len(inputs))
	workerCount := min(len(inputs), maxConcurrentLLMImageLoads)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func(offset int) {
			defer workers.Done()
			for index := offset; index < len(inputs); index += workerCount {
				imageURL := inputs[index]
				var readyURLs []string
				var err error
				switch {
				case strings.HasPrefix(imageURL, "data:image/"):
					readyURLs, err = normalizeLLMDataURLParts(imageURL)
				case strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://"):
					readyURLs, err = fetchImageAsDataURLs(ctx, imageURL)
				default:
					readyURLs, err = localImageAsDataURLs(imageURL)
				}
				if err != nil {
					failures[index] = err
					continue
				}
				ready[index] = readyURLs
			}
		}(worker)
	}
	workers.Wait()

	out := make([][]string, 0, len(ready))
	outFailures := make([]error, 0)
	for index, readyURLs := range ready {
		if len(readyURLs) == 0 {
			if failures[index] != nil {
				outFailures = append(outFailures, failures[index])
			}
			continue
		}
		out = append(out, readyURLs)
	}
	return out, outFailures
}

func dedupeLLMImageGroups(groups [][]string) [][]string {
	out := make([][]string, 0, len(groups))
	seen := map[string]struct{}{}
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		if _, exists := seen[group[0]]; exists {
			continue
		}
		seen[group[0]] = struct{}{}
		out = append(out, group)
	}
	return out
}

func flattenLLMImageGroups(groups [][]string) []string {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	out := make([]string, 0, count)
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func fetchImageAsDataURL(ctx context.Context, imageURL string) (string, error) {
	urls, err := fetchImageAsDataURLs(ctx, imageURL)
	if err != nil || len(urls) == 0 {
		return "", err
	}
	return urls[0], nil
}

func fetchImageAsDataURLs(ctx context.Context, imageURL string) ([]string, error) {
	body, contentType, err := downloadImageBytesWithLimit(ctx, imageURL, maxLLMImageSourceBytes)
	if err != nil {
		return nil, err
	}
	return normalizeLLMImageParts(body, contentType)
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
		return nil, "", fmt.Errorf("%w (limit %d MiB)", errLLMImageSourceTooLarge, maxBytes>>20)
	}

	contentType := imageContentType(resp.Header.Get("Content-Type"), body)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("downloaded content is not an image: %s", contentType)
	}
	return body, contentType, nil
}

func localImageAsDataURL(path string) (string, error) {
	urls, err := localImageAsDataURLs(path)
	if err != nil || len(urls) == 0 {
		return "", err
	}
	return urls[0], nil
}

func localImageAsDataURLs(path string) ([]string, error) {
	path = strings.TrimSpace(strings.TrimPrefix(path, "file://"))
	if path == "" {
		return nil, fmt.Errorf("image path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("image path is a directory")
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("image size is invalid")
	}
	if info.Size() > maxLLMImageSourceBytes {
		return nil, fmt.Errorf("%w (32 MiB)", errLLMImageSourceTooLarge)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	contentType := imageContentType("", data)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, fmt.Errorf("local content is not an image: %s", contentType)
	}
	return normalizeLLMImageParts(data, contentType)
}

func normalizeLLMDataURLParts(value string) ([]string, error) {
	prefix, encoded, ok := strings.Cut(strings.TrimSpace(value), ",")
	if !ok || !strings.HasPrefix(strings.ToLower(prefix), "data:image/") || !strings.Contains(strings.ToLower(prefix), ";base64") {
		return nil, fmt.Errorf("image is not a base64 data URL")
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > maxLLMImageSourceBytes {
		return nil, fmt.Errorf("%w (32 MiB)", errLLMImageSourceTooLarge)
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Tiny synthetic/provider-specific payloads historically passed through untouched.
		if len(encoded) < maxLLMImageBase64Bytes {
			return []string{value}, nil
		}
		return nil, fmt.Errorf("decode image data URL: %w", err)
	}
	config, _, configErr := image.DecodeConfig(bytes.NewReader(body))
	if configErr != nil || !longImageDimensions(config.Width, config.Height) {
		url, normalizeErr := normalizeLLMDataURL(value)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		return []string{url}, nil
	}
	contentType := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(prefix), "data:"), ";base64")
	return normalizeLLMImageParts(body, contentType)
}

func normalizeLLMDataURL(value string) (string, error) {
	prefix, encoded, ok := strings.Cut(strings.TrimSpace(value), ",")
	if !ok || !strings.HasPrefix(strings.ToLower(prefix), "data:image/") || !strings.Contains(strings.ToLower(prefix), ";base64") {
		return "", fmt.Errorf("image is not a base64 data URL")
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > maxLLMImageSourceBytes {
		return "", fmt.Errorf("%w (32 MiB)", errLLMImageSourceTooLarge)
	}
	if len(encoded) < maxLLMImageBase64Bytes {
		return value, nil
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode image data URL: %w", err)
	}
	contentType := strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(prefix), "data:"), ";base64")
	return normalizeLLMImageBytes(body, contentType)
}

func normalizeLLMImageParts(body []byte, contentType string) ([]string, error) {
	overview, err := normalizeLLMImageBytes(body, contentType)
	if err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || !longImageDimensions(config.Width, config.Height) {
		return []string{overview}, nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errLLMImageDecodeFailed, err)
	}
	parts := []string{overview}
	for _, cropBounds := range longImageTileBounds(decoded.Bounds()) {
		crop := image.NewRGBA(image.Rect(0, 0, cropBounds.Dx(), cropBounds.Dy()))
		draw.Draw(crop, crop.Bounds(), decoded, cropBounds.Min, draw.Src)
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, crop); err != nil {
			return nil, fmt.Errorf("encode long-image tile: %w", err)
		}
		tile, err := normalizeLLMImageBytes(encoded.Bytes(), "image/png")
		if err != nil {
			return nil, fmt.Errorf("normalize long-image tile: %w", err)
		}
		parts = append(parts, tile)
	}
	return parts, nil
}

func longImageDimensions(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	longSide, shortSide := max(width, height), min(width, height)
	return longSide*longImageAspectDenominator >= shortSide*longImageAspectNumerator
}

func longImageTileBounds(bounds image.Rectangle) []image.Rectangle {
	width, height := bounds.Dx(), bounds.Dy()
	if !longImageDimensions(width, height) {
		return nil
	}
	vertical := height >= width
	longSide, shortSide := max(width, height), min(width, height)
	window := max(1, shortSide*longImageTileNumerator/longImageTileDenominator)
	window = min(window, longSide)
	step := max(1, window*(100-longImageTileOverlap)/100)
	count := 1
	if longSide > window {
		count = (longSide-window+step-1)/step + 1
	}
	if count > maxLongImageTiles {
		count = maxLongImageTiles
		// With a hard tile cap, enlarge windows enough to cover the complete long edge
		// while retaining the requested overlap between adjacent tiles.
		coverageUnits := 100 + (count-1)*(100-longImageTileOverlap)
		window = min(longSide, (longSide*100+coverageUnits-1)/coverageUnits)
	}
	starts := make([]int, count)
	if count > 1 {
		for index := range count {
			starts[index] = index * (longSide - window) / (count - 1)
		}
	}
	tiles := make([]image.Rectangle, 0, count)
	for _, start := range starts {
		if vertical {
			tiles = append(tiles, image.Rect(bounds.Min.X, bounds.Min.Y+start, bounds.Max.X, bounds.Min.Y+start+window))
		} else {
			tiles = append(tiles, image.Rect(bounds.Min.X+start, bounds.Min.Y, bounds.Min.X+start+window, bounds.Max.Y))
		}
	}
	return tiles
}

// normalizeLLMImageBytes mirrors Pi Agent's provider-neutral inline-image strategy: keep an
// already-small image, otherwise fit it inside 2000x2000, try PNG and several JPEG qualities,
// then shrink both dimensions by 75% until the base64 payload fits below 4.5 MiB.
func normalizeLLMImageBytes(body []byte, contentType string) (string, error) {
	if len(body) == 0 || len(body) > maxLLMImageSourceBytes {
		if len(body) > maxLLMImageSourceBytes {
			return "", fmt.Errorf("%w (32 MiB)", errLLMImageSourceTooLarge)
		}
		return "", fmt.Errorf("image is empty")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		// Preserve the old pass-through behavior for tiny provider-specific image payloads.
		// Oversized inputs must be locally decodable because they need normalization.
		if base64EncodedSize(len(body)) < maxLLMImageBase64Bytes {
			return imageBytesAsDataURL(body, contentType), nil
		}
		return "", fmt.Errorf("%w: %v", errLLMImageDecodeFailed, err)
	}
	width, height := config.Width, config.Height
	if width <= 0 || height <= 0 || int64(width)*int64(height) > maxLLMImagePixels {
		return "", fmt.Errorf("%w: %dx%d", errLLMImageDimensions, width, height)
	}
	if width <= maxLLMImageWidth && height <= maxLLMImageHeight && base64EncodedSize(len(body)) < maxLLMImageBase64Bytes {
		return imageBytesAsDataURL(body, contentType), nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: %v", errLLMImageDecodeFailed, err)
	}
	bounds := decoded.Bounds()
	targetWidth, targetHeight := width, height
	if targetWidth > maxLLMImageWidth {
		targetHeight = max(1, targetHeight*maxLLMImageWidth/targetWidth)
		targetWidth = maxLLMImageWidth
	}
	if targetHeight > maxLLMImageHeight {
		targetWidth = max(1, targetWidth*maxLLMImageHeight/targetHeight)
		targetHeight = maxLLMImageHeight
	}

	for {
		canvas := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		golangdraw.CatmullRom.Scale(canvas, canvas.Bounds(), decoded, bounds, draw.Src, nil)

		var pngEncoded bytes.Buffer
		if err := png.Encode(&pngEncoded, canvas); err != nil {
			return "", fmt.Errorf("encode resized PNG: %w", err)
		}
		if base64EncodedSize(pngEncoded.Len()) < maxLLMImageBase64Bytes {
			return imageBytesAsDataURL(pngEncoded.Bytes(), "image/png"), nil
		}

		jpegCanvas := image.NewRGBA(canvas.Bounds())
		draw.Draw(jpegCanvas, jpegCanvas.Bounds(), image.White, image.Point{}, draw.Src)
		draw.Draw(jpegCanvas, jpegCanvas.Bounds(), canvas, image.Point{}, draw.Over)
		for _, quality := range []int{defaultLLMJPEGQuality, 85, 70, 55, 40} {
			var jpegEncoded bytes.Buffer
			if err := jpeg.Encode(&jpegEncoded, jpegCanvas, &jpeg.Options{Quality: quality}); err != nil {
				return "", fmt.Errorf("encode resized JPEG: %w", err)
			}
			if base64EncodedSize(jpegEncoded.Len()) < maxLLMImageBase64Bytes {
				return imageBytesAsDataURL(jpegEncoded.Bytes(), "image/jpeg"), nil
			}
		}

		if targetWidth == 1 && targetHeight == 1 {
			break
		}
		nextWidth := max(1, targetWidth*imageResizeStepNumerator/imageResizeStepDenominator)
		nextHeight := max(1, targetHeight*imageResizeStepNumerator/imageResizeStepDenominator)
		if nextWidth == targetWidth && nextHeight == targetHeight {
			break
		}
		targetWidth, targetHeight = nextWidth, nextHeight
	}
	return "", fmt.Errorf("%w (4.5 MiB base64)", errLLMImagePayloadTooLarge)
}

func base64EncodedSize(decodedBytes int) int {
	return ((decodedBytes + 2) / 3) * 4
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
