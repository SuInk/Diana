// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 音乐增强做两件事，共用同一条「把一首歌变成一条语音」的通路：
//
//  1. 群里丢一条网易云链接，别人得切出去开 App 才能听——这里把链接换成一条语音，
//     点开就响。这条走插件的消息通路，认的是链接形状。
//  2. 点歌。「放首稻香」「来一首适合睡前听的」都是点歌，但它们没有共同的关键词，
//     补一张同义词表是补不完的。所以这条走 Agent 工具 diana.music：模型读完整条
//     消息自己决定要不要点歌、点哪一首，判断留给它做。
//
// 曲库目前只有网易云（见 netease_source.go）。取源的部分单独成文件，将来加别的
// 平台时这里不用动。

const (
	musicPluginID = "official.music"
	musicToolName = "diana.music"

	musicSettingSources     = "enabled_sources"
	musicSettingPreferred   = "preferred_source"
	musicSettingBitrate     = "bitrate"
	musicSettingMaxDuration = "max_duration_seconds"
	musicSettingMaxMB       = "max_file_mb"
	musicSettingTimeout     = "timeout_seconds"
	musicSettingSendInfo    = "send_song_info"
	musicSettingRequestSong = "request_song_enabled"
	musicSettingSilkEncoder = "silk_encoder_path"

	defaultMusicBitrate     = 320000
	defaultMusicMaxDuration = 600
	defaultMusicMaxMB       = 20
	defaultMusicTimeout     = 45
	musicMediaTTL           = 10 * time.Minute
)

type MusicPlugin struct {
	fetcher       *musicFetcher
	sources       []musicSource
	commandRunner voiceCommandRunner

	mu     sync.RWMutex
	sharer LocalMediaSharer
}

type musicConfig struct {
	EnabledSources  []string
	PreferredSource string
	SourceOptions   map[string]musicSourceOptions
	Bitrate         int
	MaxDuration     time.Duration
	MaxBytes        int64
	Timeout         time.Duration
	SendSongInfo    bool
	RequestedSong   bool
	OutputDir       string
	FFmpegPath      string
	SilkEncoder     string
	SilkBitrate     int
}

func (c musicConfig) sourceOptions(key string) musicSourceOptions {
	return c.SourceOptions[key]
}

func (c musicConfig) sourceEnabled(key string) bool {
	return slices.Contains(c.EnabledSources, key)
}

func NewMusicPlugin(client *http.Client) *MusicPlugin {
	return &MusicPlugin{
		fetcher: &musicFetcher{client: client},
		sources: defaultMusicSources(),
		commandRunner: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
}

func (p *MusicPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          musicPluginID,
		Name:        "音乐增强",
		Version:     "0.2.1",
		Description: "群里分享的音乐链接直接下成一条语音发出来；开启点歌后，模型也能按用户要求搜歌并发送。网易云、QQ 音乐、酷狗并列，一家放不出来自动换下一家。仅 OneBot v11 支持语音。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"agent:tool", "network:http", "file:write", "process:execute", "message:read", "message:send"},
		Settings: []PluginSettingSpec{
			{
				Key:         musicSettingRequestSong,
				Label:       "允许点歌",
				Type:        PluginSettingTypeBool,
				Default:     true,
				Description: "开启后模型可以按用户要求搜歌并直接发出语音。关掉只保留链接解析。",
			},
			{
				Key:         musicSettingSources,
				Label:       "启用曲库",
				Type:        PluginSettingTypeMultiSelect,
				Default:     musicSourceKeys(),
				Options:     musicSourceOptionsList(),
				Description: "一首歌在这家是会员专享、在那家能试听是常事。勾多几家，一家放不出来就自动换下一家。",
			},
			{
				Key:         musicSettingPreferred,
				Label:       "点歌优先曲库",
				Type:        PluginSettingTypeSelect,
				Default:     "",
				Options:     append([]PluginSettingOption{{Value: "", Label: "按启用顺序"}}, musicSourceOptionsList()...),
				Description: "点歌时先问哪家。分享链接始终用链接自己的平台，不受这里影响。",
			},
			// 每家的自建接口和 Cookie 分开填：它们的地址格式和凭据形式本来就不一样，
			// 挤成一个框只会让人不知道该填哪个。
			{
				Key:         musicSourceAPIBaseSetting("netease"),
				Label:       "网易云自建 API 地址",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "自建 NeteaseCloudMusicApi 的地址，例如 http://127.0.0.1:3000。留空走官方接口，只能拿到可试听的歌曲。",
			},
			{
				Key:         musicSourceCookieSetting("netease"),
				Label:       "网易云 MUSIC_U Cookie",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
				Description: "登录 Cookie 里的 MUSIC_U，用于会员音质和受限曲目。",
			},
			{
				Key:         musicSourceAPIBaseSetting("qq"),
				Label:       "QQ 音乐自建 API 地址",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "自建 QQMusicApi 的地址。留空走官方接口，无登录态时多数曲目取不到播放地址。",
			},
			{
				Key:         musicSourceCookieSetting("qq"),
				Label:       "QQ 音乐 Cookie",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
				Description: "完整的 Cookie 串，用于会员和独家曲目。",
			},
			{
				Key:         musicSourceAPIBaseSetting("kugou"),
				Label:       "酷狗自建 API 地址",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "自建 KuGouMusicApi 的地址。留空走官方接口。",
			},
			{
				Key:         musicSourceCookieSetting("kugou"),
				Label:       "酷狗 Cookie",
				Type:        PluginSettingTypeString,
				Default:     "",
				Secret:      true,
				Description: "完整 Cookie（建议包含 token、userid、dfid）。会员曲目需配合自建 KuGouMusicApi；留空只能尝试公开试听。",
			},
			{
				Key:     musicSettingBitrate,
				Label:   "音质",
				Type:    PluginSettingTypeSelect,
				Default: strconv.Itoa(defaultMusicBitrate),
				Options: []PluginSettingOption{
					{Value: "128000", Label: "标准 128k"},
					{Value: "192000", Label: "较高 192k"},
					{Value: "320000", Label: "极高 320k"},
				},
				Description: "只对网易云的自建 API 生效；其余情况由平台自己决定码率。",
			},
			{
				Key:         musicSettingMaxDuration,
				Label:       "最长时长",
				Type:        PluginSettingTypeNumber,
				Default:     defaultMusicMaxDuration,
				Min:         settingRange(30),
				Max:         settingRange(1800),
				Step:        30,
				Unit:        "秒",
				Description: "超过这个时长的曲目只发歌曲信息，不发语音。",
			},
			{
				Key:     musicSettingMaxMB,
				Label:   "最大文件",
				Type:    PluginSettingTypeNumber,
				Default: defaultMusicMaxMB,
				Min:     settingRange(1),
				Max:     settingRange(100),
				Step:    1,
				Unit:    "MB",
			},
			{
				Key:     musicSettingTimeout,
				Label:   "请求超时",
				Type:    PluginSettingTypeNumber,
				Default: defaultMusicTimeout,
				Min:     settingRange(5),
				Max:     settingRange(180),
				Step:    5,
				Unit:    "秒",
			},
			{
				Key:         musicSettingSendInfo,
				Label:       "同时发送歌曲信息",
				Type:        PluginSettingTypeBool,
				Default:     true,
				Description: "在语音前补一条「歌名 - 歌手」，否则群里只看到一条不知道是什么的语音。",
			},
			{
				Key:         musicSettingSilkEncoder,
				Label:       "Silk 编码器路径",
				Type:        PluginSettingTypeString,
				Default:     "",
				Description: "填了就把音频转成 Tencent Silk 再发，适合自己不做转码的 OneBot 客户端。留空沿用语音合成插件的 DIANA_TTS_SILK_ENCODER_PATH。",
			},
		},
	}
}

// ShouldHandle 让没有 @ 机器人的分享消息也能被处理。
//
// 这里认的是链接形状，不是「用户想不想听歌」那种语义意图——和链接解析同一类判断。
// 点歌那半边正相反，没有可认的形状，所以交给模型走 diana.music，不在这里猜。
func (p *MusicPlugin) ShouldHandle(event MessageEvent, text string) bool {
	// 触发判断用「全部曲库」而不是「启用的曲库」：这里还读不到会话级的插件设置，
	// 少认一家的代价是那条链接彻底没反应，多认一家的代价只是 Handle 里空跑一次。
	for _, source := range p.sources {
		if len(source.References(resolverSourceText(event, text))) > 0 {
			return true
		}
	}
	return false
}

func (p *MusicPlugin) SetLocalMediaSharer(sharer LocalMediaSharer) {
	p.mu.Lock()
	p.sharer = sharer
	p.mu.Unlock()
}

func (p *MusicPlugin) share(path string) (string, bool) {
	p.mu.RLock()
	sharer := p.sharer
	p.mu.RUnlock()
	if sharer == nil {
		return "", false
	}
	return sharer.Share(path, musicMediaTTL)
}

func (p *MusicPlugin) AgentTools(settings SettingValues) ([]agent.Tool, error) {
	if !settings.Bool(musicSettingRequestSong, true) {
		return nil, nil
	}
	return []agent.Tool{&dianaMusicTool{plugin: p, settings: settings}}, nil
}

func (p *MusicPlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	cfg := musicConfigFromSettings(req.Settings)
	references := p.musicReferencesIn(cfg, resolverSourceText(req.Event, req.Text))
	if len(references) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	// 一条消息里贴了好几首也只发第一首：连着甩出几条语音在群里是刷屏，
	// 剩下的仍然由链接解析给出标题。
	found, ok := p.songFromReference(ctx, cfg, references[0])
	if !ok {
		// 连歌名都拿不到就别抢这条消息，让链接解析去抓标题。
		return nil, nil
	}
	if reason := musicVoiceUnavailableReason(cfg, found); reason != "" {
		return musicNoticeResponse(found, reason), nil
	}
	if !IsOneBotPlatform(req.Event.Platform) {
		// 只有 OneBot v11 有 record 段。别的平台硬发 CQ 码会变成一行乱字符。
		return musicNoticeResponse(found, "当前平台不支持发送语音"), nil
	}
	playable, ok := p.playableSong(ctx, cfg, found)
	if !ok {
		return musicNoticeResponse(found, "各家曲库都拿不到可播放的音频，可能是会员或独家曲目"), nil
	}
	record, err := p.prepareSongVoice(ctx, cfg, playable)
	if err != nil {
		return musicNoticeResponse(playable, err.Error()), nil
	}
	return &PluginResponse{
		Handled: true,
		Reply:   musicVoiceReply(cfg, playable, record),
		Context: musicSongContext(p, playable, "已作为语音发送"),
	}, nil
}

// songFromReference 把一条分享引用变成一首歌。
func (p *MusicPlugin) songFromReference(ctx context.Context, cfg musicConfig, ref musicReference) (song, bool) {
	source, ok := p.sourceByKey(ref.Source)
	if !ok {
		return song{}, false
	}
	songID := source.ResolveSongID(ctx, p.fetcher, cfg, ref)
	if songID == "" {
		return song{}, false
	}
	return source.SongDetail(ctx, p.fetcher, cfg, songID)
}

// playableSong 补上播放地址。分享链接那首放不出来时，按歌名去别家再找一遍
// ——听的人要的是这首歌，不是这条链接来自哪个 App。
func (p *MusicPlugin) playableSong(ctx context.Context, cfg musicConfig, found song) (song, bool) {
	if source, ok := p.sourceByKey(found.Source); ok && cfg.sourceEnabled(found.Source) {
		if playURL := source.PlayableURL(ctx, p.fetcher, cfg, found.ID); playURL != "" {
			found.PlayURL = playURL
			return found, true
		}
	}
	title := found.Title()
	if title == "" {
		return song{}, false
	}
	return p.pickSong(ctx, cfg, title, found.Source)
}

// pickSong 依次问各家曲库，返回第一首「搜得到而且放得出来」的歌。
//
// 只搜到不算数：搜到却拿不到播放地址是会员和独家曲目的常态，那种结果发不出声，
// 拿它当命中就等于让上层去下载一个空地址。skip 里的曲库刚试过，不必再问一遍。
func (p *MusicPlugin) pickSong(ctx context.Context, cfg musicConfig, query string, skip ...string) (song, bool) {
	found, ok, _ := p.pickSongWithStatus(ctx, cfg, query, skip...)
	return found, ok
}

// pickSongWithStatus 额外报告是否至少有一家搜到了歌曲。这样工具能区分
// 「歌名确实没有匹配」和「搜到了，但所有平台都因会员/版权拿不到播放地址」。
func (p *MusicPlugin) pickSongWithStatus(ctx context.Context, cfg musicConfig, query string, skip ...string) (song, bool, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return song{}, false, false
	}
	matched := false
	for _, source := range p.orderedSources(cfg) {
		if slices.Contains(skip, source.Key()) {
			continue
		}
		found, ok := source.Search(ctx, p.fetcher, cfg, query)
		if !ok {
			continue
		}
		matched = true
		playURL := source.PlayableURL(ctx, p.fetcher, cfg, found.ID)
		if playURL == "" {
			continue
		}
		found.PlayURL = playURL
		return found, true, true
	}
	return song{}, false, matched
}

// musicVoiceReply 把歌名和语音拼成一条待分条的回复。
//
// 中间那个换行是有用的：语音气泡不显示任何文字，自然分条会把它拆成「先一条歌名、
// 再一条语音」两条消息，否则群里只看到一条不知道是什么的语音。
func musicVoiceReply(cfg musicConfig, item song, record string) string {
	if !cfg.SendSongInfo {
		return record
	}
	title := item.Title()
	if title == "" {
		return record
	}
	return "🎵 " + title + "\n" + record
}

// musicVoiceUnavailableReason 返回不发语音的原因，可以发就返回空串。
func musicVoiceUnavailableReason(cfg musicConfig, item song) string {
	if cfg.MaxDuration > 0 && item.Duration > cfg.MaxDuration {
		return fmt.Sprintf("时长 %s 超过设置的上限，没发语音", formatSongDuration(item.Duration))
	}
	return ""
}

// prepareSongVoice 把一首歌做成可以直接发出去的 CQ record，下载、转码、共享
// 都在这里，链接解析和点歌走的是同一条路。
func (p *MusicPlugin) prepareSongVoice(ctx context.Context, cfg musicConfig, item song) (string, error) {
	if strings.TrimSpace(item.PlayURL) == "" {
		return "", fmt.Errorf("拿不到可播放的音频")
	}
	path, err := p.downloadAudio(ctx, cfg, item)
	if err != nil {
		log.Printf("music download failed: source=%s song=%s: %v", item.Source, item.ID, err)
		return "", fmt.Errorf("下载音频失败")
	}
	if encoded, encodeErr := p.encodeSilkIfConfigured(ctx, cfg, path); encodeErr != nil {
		log.Printf("music silk encode failed: song=%s: %v", item.ID, encodeErr)
	} else if encoded != path {
		cleanupLocalMediaFile(path)
		path = encoded
	}
	sharedURL, shared := p.share(path)
	if !shared {
		cleanupLocalMediaFile(path)
		return "", fmt.Errorf("本地媒体共享未配置，语音发不出去")
	}
	cleanupLocalMediaFilesLater([]string{path}, musicMediaTTL)
	return "[CQ:record,file=" + escapeCQParameter(sharedURL) + "]", nil
}

// downloadAudio 把音频落到本地缓存文件。
func (p *MusicPlugin) downloadAudio(ctx context.Context, cfg musicConfig, item song) (string, error) {
	if err := os.MkdirAll(cfg.OutputDir, 0o700); err != nil {
		return "", fmt.Errorf("创建音乐缓存目录失败: %w", err)
	}
	file, err := os.CreateTemp(cfg.OutputDir, "diana-music-*"+musicAudioExt(item.PlayURL))
	if err != nil {
		return "", fmt.Errorf("创建音乐缓存文件失败: %w", err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		cleanupLocalMediaFile(path)
		return "", fmt.Errorf("创建音乐缓存文件失败: %w", closeErr)
	}
	headers := map[string]string{"User-Agent": resolverUserAgent}
	if source, ok := p.sourceByKey(item.Source); ok {
		headers["Referer"] = source.Referer()
	}
	if !p.fetcher.downloadToFile(ctx, cfg, item.PlayURL, path, headers) {
		cleanupLocalMediaFile(path)
		return "", fmt.Errorf("下载音频失败")
	}
	return path, nil
}

func musicAudioExt(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ".mp3"
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if slices.Contains(musicAudioExtensions, ext) {
		return ext
	}
	return ".mp3"
}

// musicNoticeResponse 在发不出语音时仍然把歌曲信息交出去，
// 并说清楚为什么没有语音——只丢一条「解析失败」等于让人自己猜。
func musicNoticeResponse(item song, reason string) *PluginResponse {
	title := item.Title()
	if title == "" {
		return nil
	}
	return &PluginResponse{
		Handled: true,
		Reply:   "🎵 " + title + "（" + reason + "）",
		Context: "音乐：" + title + "\n  备注：" + reason,
	}
}

// musicSongContext 是给模型看的来源标签。带上平台名，模型才知道刚才那条语音
// 是从哪家放的——分享的是网易云链接、实际从酷狗放出来的情况是存在的。
func musicSongContext(p *MusicPlugin, item song, note string) string {
	var builder strings.Builder
	builder.WriteString("音乐：")
	builder.WriteString(item.Title())
	if source, ok := p.sourceByKey(item.Source); ok {
		builder.WriteString("\n  来源：")
		builder.WriteString(source.Label())
	}
	if album := strings.TrimSpace(item.Album); album != "" {
		builder.WriteString("\n  专辑：")
		builder.WriteString(album)
	}
	if item.Duration > 0 {
		builder.WriteString("\n  时长：")
		builder.WriteString(formatSongDuration(item.Duration))
	}
	if note = strings.TrimSpace(note); note != "" {
		builder.WriteString("\n  备注：")
		builder.WriteString(note)
	}
	return builder.String()
}

func formatSongDuration(d time.Duration) string {
	total := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// dianaMusicTool 是点歌工具。
type dianaMusicTool struct {
	plugin   *MusicPlugin
	settings SettingValues
}

type musicToolResult struct {
	OK       bool   `json:"ok"`
	Action   string `json:"action"`
	Song     string `json:"song"`
	Source   string `json:"source"`
	CQRecord string `json:"cq_record"`
	Reply    string `json:"reply"`
}

func (t *dianaMusicTool) Name() string { return musicToolName }

func (t *dianaMusicTool) Description() string {
	return `按歌名或歌手依次搜索已启用的网易云、QQ 音乐和酷狗曲库，把第一首可播放的匹配歌曲下载成语音直接发出去（点歌）。仅当用户要求放歌、点歌、来一首，或指名要听某首歌时调用；只是聊到某首歌、讨论音乐话题、问歌词或歌手信息时严禁调用。调用后工具会直接完成本次回复，不要再发送重复文字。语音只在 QQ（OneBot v11）上能正常播放。input: {"query":"搜索词，尽量写成「歌名 歌手」，例如「稻香 周杰伦」"}`
}

func (t *dianaMusicTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "搜索词，尽量写成「歌名 歌手」。",
			},
		},
		"required": []string{"query"},
	}
}

func (t *dianaMusicTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.plugin == nil {
		return "", fmt.Errorf("音乐插件未配置")
	}
	query := strings.TrimSpace(configToolString(input, "query"))
	if query == "" {
		return "", fmt.Errorf("query 不能为空")
	}
	cfg := musicConfigFromSettings(t.settings)
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	ctx, diagnostics := withMusicDiagnostics(ctx)
	found, ok, matched := t.plugin.pickSongWithStatus(ctx, cfg, query)
	if !ok {
		if matched {
			return "", fmt.Errorf("已找到《%s》，但启用的曲库都没有返回可播放地址；可能需要对应平台的有效会员 Cookie，或该曲目受版权/地区限制", query)
		}
		if diagnostics.failed() {
			return "", fmt.Errorf("搜索《%s》时曲库请求异常，请稍后重试或检查曲库配置", query)
		}
		return "", fmt.Errorf("各家曲库都没搜到能放的《%s》，换个歌名或补上歌手再试", query)
	}
	if reason := musicVoiceUnavailableReason(cfg, found); reason != "" {
		return "", fmt.Errorf("《%s》%s", found.Title(), reason)
	}
	record, err := t.plugin.prepareSongVoice(ctx, cfg, found)
	if err != nil {
		return "", fmt.Errorf("《%s》%s", found.Title(), err.Error())
	}
	body, err := json.Marshal(musicToolResult{
		OK:       true,
		Action:   "song_ready",
		Song:     found.Title(),
		Source:   found.Source,
		CQRecord: record,
		Reply:    musicVoiceReply(cfg, found, record),
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// TerminalResult 让点歌成功后直接结束这一轮：歌已经发出去了，
// 再让模型补一段「我给你放了一首」只会变成两条重复的话。
func (t *dianaMusicTool) TerminalResult(output string) (string, bool) {
	var result musicToolResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", false
	}
	if !result.OK || strings.TrimSpace(result.CQRecord) == "" {
		return "", false
	}
	return firstNonEmpty(strings.TrimSpace(result.Reply), result.CQRecord), true
}

func musicConfigFromSettings(settings SettingValues) musicConfig {
	timeout := settings.Int(musicSettingTimeout, defaultMusicTimeout)
	if timeout < 5 {
		timeout = defaultMusicTimeout
	}
	maxMB := settings.Int(musicSettingMaxMB, defaultMusicMaxMB)
	if maxMB < 1 {
		maxMB = defaultMusicMaxMB
	}
	bitrate, err := strconv.Atoi(strings.TrimSpace(settings.String(musicSettingBitrate, "")))
	if err != nil || bitrate <= 0 {
		bitrate = defaultMusicBitrate
	}
	maxDuration := settings.Int(musicSettingMaxDuration, defaultMusicMaxDuration)
	if maxDuration < 0 {
		maxDuration = defaultMusicMaxDuration
	}
	return musicConfig{
		EnabledSources:  musicEnabledSourcesFromSettings(settings),
		PreferredSource: musicPreferredSourceFromSettings(settings),
		SourceOptions:   musicSourceOptionsFromSettings(settings),
		Bitrate:         bitrate,
		MaxDuration:     time.Duration(maxDuration) * time.Second,
		MaxBytes:        int64(maxMB) << 20,
		Timeout:         time.Duration(timeout) * time.Second,
		SendSongInfo:    settings.Bool(musicSettingSendInfo, true),
		RequestedSong:   settings.Bool(musicSettingRequestSong, true),
		OutputDir:       musicOutputDir(),
		FFmpegPath:      firstNonEmpty(strings.TrimSpace(os.Getenv("DIANA_TTS_FFMPEG_PATH")), "ffmpeg"),
		// Silk 编码器全机器一台就够，默认沿用语音合成插件已经配好的那个，
		// 不逼用户在两个插件里把同一个路径填两遍。
		SilkEncoder: firstNonEmpty(
			strings.TrimSpace(settings.String(musicSettingSilkEncoder, "")),
			strings.TrimSpace(os.Getenv("DIANA_TTS_SILK_ENCODER_PATH")),
		),
		SilkBitrate: voiceTTSSilkBitrate(),
	}
}

// musicSourceKeys 是登记在册的曲库键，顺序就是默认的问询顺序。
func musicSourceKeys() []string {
	sources := defaultMusicSources()
	keys := make([]string, 0, len(sources))
	for _, source := range sources {
		keys = append(keys, source.Key())
	}
	return keys
}

func musicSourceOptionsList() []PluginSettingOption {
	sources := defaultMusicSources()
	options := make([]PluginSettingOption, 0, len(sources))
	for _, source := range sources {
		options = append(options, PluginSettingOption{Value: source.Key(), Label: source.Label()})
	}
	return options
}

func musicSourceAPIBaseSetting(key string) string { return key + "_api_base" }
func musicSourceCookieSetting(key string) string  { return key + "_cookie" }

// musicEnabledSourcesFromSettings 读勾选的曲库。没配过就是全开——新装一台机器
// 不该因为「还没勾」而什么都放不出来；勾成空则视为用户明确只留默认那家，
// 否则整个插件会静默失效，比留一家更难排查。
func musicEnabledSourcesFromSettings(settings SettingValues) []string {
	if _, configured := settings[musicSettingSources]; !configured {
		return musicSourceKeys()
	}
	known := musicSourceKeys()
	enabled := make([]string, 0, len(known))
	for _, key := range settings.StringSlice(musicSettingSources) {
		key = strings.TrimSpace(key)
		if slices.Contains(known, key) && !slices.Contains(enabled, key) {
			enabled = append(enabled, key)
		}
	}
	if len(enabled) == 0 {
		return known[:1]
	}
	return enabled
}

func musicPreferredSourceFromSettings(settings SettingValues) string {
	preferred := strings.TrimSpace(settings.String(musicSettingPreferred, ""))
	if !slices.Contains(musicSourceKeys(), preferred) {
		return ""
	}
	return preferred
}

func musicSourceOptionsFromSettings(settings SettingValues) map[string]musicSourceOptions {
	options := make(map[string]musicSourceOptions, len(musicSourceKeys()))
	for _, key := range musicSourceKeys() {
		options[key] = musicSourceOptions{
			APIBase: strings.TrimRight(strings.TrimSpace(settings.String(musicSourceAPIBaseSetting(key), "")), "/"),
			Cookie:  strings.TrimSpace(settings.String(musicSourceCookieSetting(key), "")),
		}
	}
	return options
}

func musicOutputDir() string {
	if dbPath := strings.TrimSpace(os.Getenv("APP_DB_PATH")); dbPath != "" {
		if absolute, err := filepath.Abs(dbPath); err == nil {
			return filepath.Join(filepath.Dir(absolute), "music-cache")
		}
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "diana", "music-cache")
	}
	return filepath.Join(os.TempDir(), "diana-music")
}

// encodeSilkIfConfigured 在配了编码器时把音频转成 Tencent Silk。
//
// 没配就原样返回：NapCat、Lagrange 这些客户端自己会转码，逼所有人先装一个
// 编码器才能听歌是没必要的门槛。转码失败也返回原文件——发一条客户端可能转不了的
// mp3，好过什么都不发。
func (p *MusicPlugin) encodeSilkIfConfigured(ctx context.Context, cfg musicConfig, audioPath string) (string, error) {
	if cfg.SilkEncoder == "" {
		return audioPath, nil
	}
	pcmPath := audioPath + ".pcm"
	silkPath := audioPath + ".silk"
	defer cleanupLocalMediaFile(pcmPath)

	output, err := p.runCommand(ctx, cfg.FFmpegPath,
		"-hide_banner", "-loglevel", "error", "-y", "-i", audioPath,
		"-ar", "24000", "-ac", "1", "-f", "s16le", pcmPath,
	)
	if err != nil {
		return audioPath, voiceCommandError("系统 ffmpeg 转换 PCM", output, err)
	}
	output, err = p.runCommand(ctx, cfg.SilkEncoder,
		"-i", pcmPath,
		"-o", silkPath,
		"-Fs_API", "24000",
		"-Fs_maxInternal", "24000",
		"-packetlength", "20",
		"-rate", strconv.Itoa(cfg.SilkBitrate),
		"-complexity", "2",
		"-STX=true",
	)
	if err != nil {
		cleanupLocalMediaFile(silkPath)
		return audioPath, voiceCommandError("Silk 编码", output, err)
	}
	header, err := readFilePrefix(silkPath, 16)
	if err != nil || !looksLikeTencentSilk(header) {
		cleanupLocalMediaFile(silkPath)
		return audioPath, fmt.Errorf("Silk 编码器未返回有效 Tencent Silk 音频")
	}
	return silkPath, nil
}

func (p *MusicPlugin) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if p.commandRunner == nil {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	return p.commandRunner(ctx, name, args...)
}
