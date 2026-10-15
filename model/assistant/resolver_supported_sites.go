package assistant

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/procgroup"
)

// YTDLPSupportedExtractors lists this installation's built-in extractors without
// loading user config, cookies, or plugins, and without fetching any site.
func YTDLPSupportedExtractors(ctx context.Context) ([]string, error) {
	path, err := lookResolverCommand("yt-dlp")
	if err != nil {
		return nil, errors.New("尚未安装 yt-dlp，可先在插件依赖中安装，或查看官方完整列表")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := procgroup.CommandContext(ctx, path, "--ignore-config", "--no-plugin-dirs", "--list-extractors")
	cmd.Env = resolverCommandEnv()
	output := &limitedCommandOutput{remaining: 1024 * 1024}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("无法读取本机 yt-dlp 支持列表，请检查安装后重试")
	}
	if output.truncated {
		return nil, errors.New("yt-dlp 支持列表过大，请查看官方完整列表")
	}
	entries := parseYTDLPExtractors(output.String())
	if len(entries) == 0 {
		return nil, errors.New("yt-dlp 未返回支持列表，请检查版本")
	}
	return entries, nil
}

func parseYTDLPExtractors(output string) []string {
	seen := map[string]bool{}
	var entries []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		entries = append(entries, line)
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i]) < strings.ToLower(entries[j]) })
	return entries
}
