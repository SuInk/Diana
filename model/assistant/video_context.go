// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/model/netguard"
)

const (
	minVideoContextFrames    = 4
	maxVideoContextFrames    = 16
	videoFrameGrowthInterval = 30.0
)

type videoContextMaxBytesKey struct{}

func withVideoContextMaxBytes(ctx context.Context, maxBytes int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxBytes <= 0 {
		maxBytes = defaultFileParserMaxVideoBytes
	}
	return context.WithValue(ctx, videoContextMaxBytesKey{}, maxBytes)
}

func videoContextMaxBytes(ctx context.Context) int64 {
	if ctx != nil {
		if value, ok := ctx.Value(videoContextMaxBytesKey{}).(int64); ok && value > 0 {
			return value
		}
	}
	return defaultFileParserMaxVideoBytes
}

func localVideoPath(value string) string {
	return localVideoPathWithLimit(value, defaultFileParserMaxVideoBytes)
}

func localVideoPathWithLimit(value string, maxBytes int64) string {
	path := strings.TrimSpace(strings.TrimPrefix(value, "file://"))
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > maxBytes {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !videoContextPathAllowed(resolved) {
		return ""
	}
	return resolved
}

func videoContextPathAllowed(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	roots := []string{
		filepath.Join(home, "Library", "Containers", "com.tencent.qq"),
		filepath.Join(home, "Library", "Application Support", "QQ"),
		filepath.Join(home, "Library", "Application Support", "diana"),
		os.TempDir(),
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if resolvedRoot, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			root = resolvedRoot
		}
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func extractVideoContextFrames(ctx context.Context, sources []string) []string {
	return extractVideoContextFramesAfterReady(ctx, sources, 0)
}

func extractVideoContextFramesAfterReady(ctx context.Context, sources []string, wait time.Duration) []string {
	frames, _ := extractVideoContextFramesDetailed(ctx, sources, wait)
	return frames
}

// extractVideoContextFramesDetailed 除了画面还返回失败原因。
//
// 读不到视频时原先只往提示词里写一句「读取或抽帧失败」，模型只能照着复述——用户
// 得到的是「我暂时读不了这个视频」，既不知道是这台机器没装 ffmpeg，还是视频太大
// 超了上限，也不知道该找谁修。原因就在手边，一路丢掉了而已。
//
// 措辞是给用户看的，所以不提「抽帧」：那是内部实现，只有对方专门问起读取方式时
// 才有必要说（见 videoFrameNarrationRule）。
func extractVideoContextFramesDetailed(ctx context.Context, sources []string, wait time.Duration) ([]string, string) {
	if len(sources) == 0 {
		return nil, ""
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Printf("chatbot video context unavailable: ffmpeg not found")
		return nil, "这台机器上没有安装 ffmpeg，视频读不了；这是部署环境缺组件，不是视频本身的问题。"
	}
	out := make([]string, 0, maxVideoContextFrames)
	seen := map[string]bool{}
	reason := ""
	for _, source := range sources {
		path, cleanup, err := materializeVideoContextSource(ctx, source, wait)
		if err != nil {
			reason = firstNonEmpty(reason, err.Error())
		}
		if path == "" || seen[path] {
			cleanup()
			continue
		}
		seen[path] = true
		frames, frameErr := extractLocalVideoFrames(ctx, path, maxVideoContextFrames-len(out))
		cleanup()
		if frameErr != nil {
			reason = firstNonEmpty(reason, frameErr.Error())
		}
		out = append(out, frames...)
		if len(out) >= maxVideoContextFrames {
			break
		}
	}
	if len(out) > 0 {
		return out, ""
	}
	return nil, firstNonEmpty(reason, "视频没读出可用画面，原因不明。")
}

func materializeVideoContextSource(ctx context.Context, source string, wait time.Duration) (string, func(), error) {
	maxBytes := videoContextMaxBytes(ctx)
	if remote := normalizedHTTPURL(source); remote != "" {
		path, dir, err := downloadVideoContextSource(ctx, remote)
		if err != nil {
			log.Printf("chatbot video download failed: %v", err)
			return "", func() {}, fmt.Errorf("视频下载失败（%s）。", describeVideoContextError(err, maxBytes))
		}
		return path, func() { _ = os.RemoveAll(dir) }, nil
	}
	path := waitForLocalMediaPath(ctx, source, wait, maxBytes)
	if path == "" {
		return "", func() {}, fmt.Errorf("视频文件没能在本地就绪，可能还没下载完、已经被清理，或者超过了 %s 的大小上限。", formatVideoContextSize(maxBytes))
	}
	path = localVideoPathWithLimit(path, maxBytes)
	if path == "" {
		return "", func() {}, fmt.Errorf("视频文件不可读，或者超过了 %s 的大小上限。", formatVideoContextSize(maxBytes))
	}
	return path, func() {}, nil
}

// describeVideoContextError 把内部错误翻成一句用户看得懂的话。超限是最常见的一种，
// 单独认出来——「HTTP 404」和「视频太大」对用户来说是完全不同的两件事。
func describeVideoContextError(err error, maxBytes int64) string {
	if err == nil {
		return "原因不明"
	}
	text := err.Error()
	if strings.Contains(text, "exceeds file parser limit") {
		return "视频超过了 " + formatVideoContextSize(maxBytes) + " 的大小上限"
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "timeout") || strings.Contains(text, "Client.Timeout") {
		return "下载超时"
	}
	return text
}

func formatVideoContextSize(maxBytes int64) string {
	if maxBytes <= 0 {
		return "当前"
	}
	return fmt.Sprintf("%d MB", maxBytes/(1024*1024))
}

func downloadVideoContextSource(ctx context.Context, source string) (string, string, error) {
	workDir, err := os.MkdirTemp("", "diana-video-download-*")
	if err != nil {
		return "", "", err
	}
	cleanup := func(err error) (string, string, error) {
		_ = os.RemoveAll(workDir)
		return "", "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, source, nil)
	if err != nil {
		return cleanup(err)
	}
	req.Header.Set("User-Agent", "Diana/0.1")
	resp, err := netguard.NewPublicHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return cleanup(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return cleanup(fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	maxBytes := videoContextMaxBytes(ctx)
	if resp.ContentLength > maxBytes {
		return cleanup(fmt.Errorf("video exceeds file parser limit: %d > %d bytes", resp.ContentLength, maxBytes))
	}
	path := filepath.Join(workDir, "source.mp4")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return cleanup(err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return cleanup(copyErr)
	}
	if closeErr != nil {
		return cleanup(closeErr)
	}
	if written <= 0 || written > maxBytes {
		return cleanup(fmt.Errorf("video exceeds file parser limit: %d > %d bytes", written, maxBytes))
	}
	return path, workDir, nil
}

func waitForLocalMediaPath(ctx context.Context, source string, wait time.Duration, maxBytes int64) string {
	path := rawAbsoluteMediaPath(source)
	if path == "" {
		return ""
	}
	deadline := time.Now().Add(wait)
	for {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Size() > 0 && info.Size() <= maxBytes {
			return path
		}
		if wait <= 0 || time.Now().After(deadline) {
			return ""
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
}

func rawAbsoluteMediaPath(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "file://"))
	if value == "" || !filepath.IsAbs(value) {
		return ""
	}
	return filepath.Clean(value)
}

func extractLocalVideoFrames(ctx context.Context, videoPath string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	workDir, err := os.MkdirTemp("", "diana-video-context-*")
	if err != nil {
		return nil, fmt.Errorf("临时目录建不出来（%v），这是机器上的问题。", err)
	}
	stagedPath, err := stageVideoForContext(ctx, videoPath, workDir)
	if err != nil {
		log.Printf("chatbot video staging failed for %s: %v", filepath.Base(videoPath), err)
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("视频准备失败（%s）。", describeVideoContextError(err, videoContextMaxBytes(ctx)))
	}
	duration := probeVideoDuration(ctx, stagedPath)
	timestamps := videoFrameTimestamps(duration, limit)
	frames := make([]string, 0, len(timestamps))
	lastFFmpegError := ""
	for i, timestamp := range timestamps {
		framePath := filepath.Join(workDir, fmt.Sprintf("frame-%02d.jpg", i+1))
		callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		cmd := exec.CommandContext(callCtx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-ss", strconv.FormatFloat(timestamp, 'f', 3, 64), "-i", stagedPath, "-frames:v", "1", "-vf", "scale='min(1280,iw)':-2", "-q:v", "3", framePath)
		output, runErr := cmd.CombinedOutput()
		cancel()
		if runErr != nil {
			log.Printf("chatbot video frame extraction failed: %v: %s", runErr, strings.TrimSpace(string(output)))
			lastFFmpegError = strings.TrimSpace(string(output))
			continue
		}
		if info, statErr := os.Stat(framePath); statErr == nil && info.Size() > 0 {
			frames = append(frames, framePath)
		}
	}
	if len(frames) == 0 {
		_ = os.RemoveAll(workDir)
		// ffmpeg 自己的报错往往就是答案（编码不支持、文件损坏），原样带出去比
		// 一句「失败了」有用得多；它一个字都没说时才退回泛化文案。
		if lastFFmpegError != "" {
			return nil, fmt.Errorf("ffmpeg 读不了这个视频：%s", truncateRunes(lastFFmpegError, 200))
		}
		return nil, fmt.Errorf("ffmpeg 没能从这个视频里读出画面，可能是编码不受支持或者文件损坏。")
	}
	return frames, nil
}

func stageVideoForContext(ctx context.Context, sourcePath, workDir string) (string, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	destinationPath := filepath.Join(workDir, "source"+filepath.Ext(sourcePath))
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	maxBytes := videoContextMaxBytes(ctx)
	written, copyErr := io.Copy(destination, io.LimitReader(source, maxBytes+1))
	closeErr := destination.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written <= 0 || written > maxBytes {
		return "", fmt.Errorf("video exceeds file parser limit: %d > %d bytes", written, maxBytes)
	}
	return destinationPath, nil
}

func probeVideoDuration(ctx context.Context, path string) float64 {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return 0
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(callCtx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0
	}
	duration, _ := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	return duration
}

func videoFrameTimestamps(duration float64, limit int) []float64 {
	if limit <= 0 {
		return nil
	}
	if duration <= 0 {
		count := minVideoContextFrames
		if count > limit {
			count = limit
		}
		out := make([]float64, 0, count)
		for index := 0; index < count; index++ {
			out = append(out, float64(index))
		}
		return out
	}
	count := desiredVideoFrameCount(duration)
	if count > limit {
		count = limit
	}
	out := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, duration*float64(i+1)/float64(count+1))
	}
	return out
}

func desiredVideoFrameCount(duration float64) int {
	if duration <= 0 {
		return minVideoContextFrames
	}
	count := minVideoContextFrames
	if duration > videoFrameGrowthInterval {
		count += int(math.Ceil(duration/videoFrameGrowthInterval)) - 1
	}
	if count > maxVideoContextFrames {
		return maxVideoContextFrames
	}
	return count
}

func cleanupVideoContextFrames(frames []string) {
	seen := map[string]bool{}
	for _, frame := range frames {
		dir := filepath.Dir(frame)
		if seen[dir] || !strings.HasPrefix(filepath.Base(dir), "diana-video-context-") {
			continue
		}
		seen[dir] = true
		_ = os.RemoveAll(dir)
	}
}
