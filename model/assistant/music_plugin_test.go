// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 同一首歌在网易云有好几种地址写法，分享出来的是哪种全看对方从哪儿点的分享。
// 只认其中一种，剩下的在群里就是「贴了链接机器人没反应」。
func TestMusicReferencesAcceptsEveryShareShape(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"网页版查询串", "https://music.163.com/song?id=1974443814", "1974443814"},
		{"单页应用锚点", "听听这个 https://music.163.com/#/song?id=1974443814 还不错", "1974443814"},
		{"移动端路径", "https://y.music.163.com/m/song/1974443814/?userid=1", "1974443814"},
		{"带多余参数", "https://music.163.com/song?id=1974443814&userid=42&app_version=8.0", "1974443814"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := neteaseReferences(tc.text)
			if len(refs) != 1 || refs[0].SongID != tc.want {
				t.Fatalf("neteaseReferences(%q) = %#v, want song %s", tc.text, refs, tc.want)
			}
		})
	}
}

func TestMusicReferencesIgnoresUnrelatedLinks(t *testing.T) {
	// artist/album/playlist 页也带 ?id=，光看参数就会把歌手页当成一首歌去请求。
	text := "https://www.bilibili.com/video/BV1xx411c7mD 和 https://music.163.com/artist?id=6452 " +
		"还有 https://music.163.com/#/playlist?id=7052908 和 https://music.163.com/album?id=34751 " +
		"以及 https://example.com/song?id=1"
	if refs := neteaseReferences(text); len(refs) != 0 {
		t.Fatalf("neteaseReferences() = %#v, want none", refs)
	}
}

func TestMusicReferencesKeepsShortLinksForRedirect(t *testing.T) {
	refs := neteaseReferences("分享单曲 https://163cn.tv/AbCdEf")
	if len(refs) != 1 || refs[0].SongID != "" || refs[0].ShortURL == "" {
		t.Fatalf("short link reference = %#v", refs)
	}
}

// 歌曲 ID 只能是数字：链接里带的别的东西不该被当成 ID 拿去请求接口。
func TestMusicNumericIDRejectsNonNumericValues(t *testing.T) {
	for _, value := range []string{"", "abc", "12a", "0", "00", " 12 3", strings.Repeat("9", 21)} {
		if got := neteaseNumericID(value); got != "" {
			t.Fatalf("neteaseNumericID(%q) = %q, want empty", value, got)
		}
	}
	if got := neteaseNumericID(" 1974443814 "); got != "1974443814" {
		t.Fatalf("neteaseNumericID() = %q", got)
	}
}

// 官方外链对下架和会员曲目会跳到 404 页而不是报错。不看落点就会把一段 HTML
// 当音频发出去，群里收到的是一条打不开的静音语音。
func TestMusicLooksPlayableRejectsFallbackPages(t *testing.T) {
	playable := []string{
		"http://m801.music.126.net/20260828/abc/x.mp3",
		"https://m7.music.126.net/x/y.flac",
	}
	for _, raw := range playable {
		if !neteaseLooksPlayable(raw) {
			t.Fatalf("neteaseLooksPlayable(%q) = false", raw)
		}
	}
	rejected := []string{
		"https://music.163.com/404",
		"https://music.163.com/song/media/outer/url",
		"https://music.163.com/index.html",
		"not a url at all::",
	}
	for _, raw := range rejected {
		if neteaseLooksPlayable(raw) {
			t.Fatalf("neteaseLooksPlayable(%q) = true", raw)
		}
	}
}

// musicTestServer 冒充自建 NeteaseCloudMusicApi：详情、播放地址、音频本体。
func musicTestServer(t *testing.T, durationMS int64, audio []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/song/detail":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"code":200,"songs":[{"id":1974443814,"name":"雾里","duration":%d,"artists":[{"name":"姚六一"}],"album":{"name":"雾里"}}]}`, durationMS)
		case "/song/url":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"code":200,"data":[{"id":1974443814,"url":%q,"size":%d,"type":"mp3"}]}`,
				"http://"+r.Host+"/audio/1974443814.mp3", len(audio))
		case "/search", "/official/search":
			keywords := strings.TrimSpace(r.URL.Query().Get("keywords") + r.URL.Query().Get("s"))
			if !strings.Contains(keywords, "雾里") {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"code":200,"result":{"songs":[]}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// 搜索接口用的是新一代字段名 ar/al/dt，和详情接口的 artists/album/duration
			// 不一样。只认一套就会解出一首没名字的歌。
			fmt.Fprintf(w, `{"code":200,"result":{"songs":[{"id":1974443814,"name":"雾里","dt":%d,"ar":[{"name":"姚六一"}],"al":{"name":"雾里"}}]}}`, durationMS)
		case "/official/detail":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"code":200,"songs":[{"id":1974443814,"name":"雾里","duration":%d,"artists":[{"name":"姚六一"}],"album":{"name":"雾里"}}]}`, durationMS)
		case "/official/outer":
			// 官方外链是一次 302，可试听的曲目落在 CDN 的 mp3 上。
			http.Redirect(w, r, "http://"+r.Host+"/audio/1974443814.mp3", http.StatusFound)
		case "/official/outer-blocked":
			// 下架或会员专享时官方不报错，只是把人送到 404 页。
			http.Redirect(w, r, "http://"+r.Host+"/404", http.StatusFound)
		case "/audio/1974443814.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(audio)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newMusicTestPlugin 把官方接口指向测试服务器，任何一条测试都不会去打
// 网易云的线上接口——否则测试结果取决于外网和那首歌当天上没上架。
func newMusicTestPlugin(server *httptest.Server) *MusicPlugin {
	plugin := NewMusicPlugin(server.Client())
	plugin.officialDetailAPI = server.URL + "/official/detail?ids=%s"
	plugin.officialSearchAPI = server.URL + "/official/search?s=%s"
	plugin.officialOuterURL = server.URL + "/official/outer?id=%s"
	return plugin
}

func musicTestRequest(server *httptest.Server, platform string) PluginRequest {
	return PluginRequest{
		Event: MessageEvent{Platform: platform, Kind: EventKindGroup, GroupID: "123456", UserID: "10001"},
		Text:  "听听这个 https://music.163.com/song?id=1974443814",
		Settings: SettingValues{
			musicSettingAPIBase: server.URL,
			musicSettingMaxMB:   10,
		},
	}
}

func TestMusicPluginSendsVoiceForSharedSong(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)

	resp, err := plugin.Handle(context.Background(), musicTestRequest(server, PlatformOneBotV11))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("Handle() = %#v, want a handled response", resp)
	}
	if !strings.Contains(resp.Reply, "雾里") || !strings.Contains(resp.Reply, "姚六一") {
		t.Fatalf("reply lost the song identity: %q", resp.Reply)
	}
	record := "[CQ:record,file=" + escapeCQParameter(sharer.url) + "]"
	if !strings.Contains(resp.Reply, record) {
		t.Fatalf("reply = %q, want a record segment %q", resp.Reply, record)
	}
	if len(sharer.paths) != 1 || !strings.HasSuffix(sharer.paths[0], ".mp3") {
		t.Fatalf("shared paths = %#v", sharer.paths)
	}
	t.Cleanup(func() { cleanupLocalMediaFile(sharer.paths[0]) })

	// 语音气泡不显示任何文字。歌名和语音必须落成两条消息，否则群里只看到一条
	// 不知道是什么的语音——这正是换行在这里的作用，不是随手加的排版。
	chunks := splitChatReply(resp.Reply, chatSplitLimits{})
	if len(chunks) != 2 {
		t.Fatalf("splitChatReply() = %#v, want the song line and the voice as two messages", chunks)
	}
	if !strings.Contains(chunks[0], "雾里") || chunks[1] != record {
		t.Fatalf("split chunks = %#v", chunks)
	}
}

// 只有 OneBot v11 有 record 段。别的平台原样发 CQ 码会变成一行乱字符，
// 所以那里只发歌曲信息，并说清楚为什么没有语音。
func TestMusicPluginFallsBackToTextOffOneBot(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	plugin.SetLocalMediaSharer(&recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"})

	resp, err := plugin.Handle(context.Background(), musicTestRequest(server, PlatformTelegram))
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("Handle() = %#v", resp)
	}
	if strings.Contains(resp.Reply, "[CQ:") {
		t.Fatalf("CQ code leaked to a non-OneBot platform: %q", resp.Reply)
	}
	if !strings.Contains(resp.Reply, "雾里") || !strings.Contains(resp.Reply, "不支持发送语音") {
		t.Fatalf("reply = %q", resp.Reply)
	}
}

// 超长曲目不发语音，但也别装作没看见这条链接。
func TestMusicPluginSkipsOverlongSongsWithAReason(t *testing.T) {
	server := musicTestServer(t, 3600000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)

	req := musicTestRequest(server, PlatformOneBotV11)
	req.Settings[musicSettingMaxDuration] = 600

	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || strings.Contains(resp.Reply, "[CQ:") {
		t.Fatalf("Handle() = %#v, want a text-only notice", resp)
	}
	if !strings.Contains(resp.Reply, "60:00") || !strings.Contains(resp.Reply, "上限") {
		t.Fatalf("reply = %q, want the duration and why it was skipped", resp.Reply)
	}
	if len(sharer.paths) != 0 {
		t.Fatalf("overlong song was still downloaded: %#v", sharer.paths)
	}
}

// 拿不到歌名就别抢这条消息：链接解析的标题摘要还能兜底，
// 抢下来再回一句「解析失败」反而把能用的结果挤掉了。
func TestMusicPluginYieldsWhenTheSongIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	plugin := newMusicTestPlugin(server)

	req := musicTestRequest(server, PlatformOneBotV11)
	req.Settings[musicSettingTimeout] = 5
	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("Handle() = %#v, want nil so the link resolver can still answer", resp)
	}
}

func TestMusicPluginShouldHandleOnlyOnSongLinks(t *testing.T) {
	plugin := NewMusicPlugin(nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "123456"}
	if !plugin.ShouldHandle(event, "https://music.163.com/song?id=1974443814") {
		t.Fatal("song link did not trigger the plugin")
	}
	if plugin.ShouldHandle(event, "今天想听点歌") {
		t.Fatal("plain chat triggered the plugin")
	}
	// 消息段里的链接也要认：QQ 的分享卡片正文常常只在 RawMessage 里。
	card := MessageEvent{Kind: EventKindGroup, RawMessage: "[分享]https://y.music.163.com/m/song/1974443814/"}
	if !plugin.ShouldHandle(card, "") {
		t.Fatal("share card link did not trigger the plugin")
	}
}

func TestMusicConfigFallsBackOnInvalidSettings(t *testing.T) {
	cfg := musicConfigFromSettings(SettingValues{
		musicSettingAPIBase:     "  http://127.0.0.1:3000/  ",
		musicSettingBitrate:     "not-a-number",
		musicSettingMaxMB:       0,
		musicSettingTimeout:     1,
		musicSettingMaxDuration: -5,
	})
	if cfg.APIBase != "http://127.0.0.1:3000" {
		t.Fatalf("APIBase = %q", cfg.APIBase)
	}
	if cfg.Bitrate != defaultMusicBitrate {
		t.Fatalf("Bitrate = %d", cfg.Bitrate)
	}
	if cfg.MaxBytes != int64(defaultMusicMaxMB)<<20 {
		t.Fatalf("MaxBytes = %d", cfg.MaxBytes)
	}
	if cfg.Timeout != defaultMusicTimeout*time.Second {
		t.Fatalf("Timeout = %s", cfg.Timeout)
	}
	if cfg.MaxDuration != defaultMusicMaxDuration*time.Second {
		t.Fatalf("MaxDuration = %s", cfg.MaxDuration)
	}
	if !cfg.SendSongInfo {
		t.Fatal("SendSongInfo should default to on")
	}
}

// 插件 ID 必须排在链接解析前面：两个插件都能给出回复时，运行时取排序后的第一个。
// 排到后面的话语音永远发不出去，只会看到链接解析的标题。
func TestMusicPluginOutranksTheLinkResolver(t *testing.T) {
	if musicPluginID >= resolverPluginID {
		t.Fatalf("%q must sort before %q", musicPluginID, resolverPluginID)
	}
	if _, ok := NewDefaultPluginManager().Get(musicPluginID); !ok {
		t.Fatal("plugin is not registered in the default manager")
	}
}

// 默认配置里没有自建 API，走的是官方外链那条路。它才是大多数部署实际用的分支。
func TestMusicPluginUsesOfficialEndpointsWithoutSelfHostedAPI(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)

	req := musicTestRequest(server, PlatformOneBotV11)
	delete(req.Settings, musicSettingAPIBase)

	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Reply, "[CQ:record,") {
		t.Fatalf("Handle() = %#v, want a voice from the official endpoints", resp)
	}
	if len(sharer.paths) == 1 {
		t.Cleanup(func() { cleanupLocalMediaFile(sharer.paths[0]) })
	}
}

// 自建实例挂了不该让整个功能一起哑掉：详情仍然可以退回官方接口拿。
func TestMusicPluginFallsBackToOfficialWhenSelfHostedAPIIsDown(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)

	req := musicTestRequest(server, PlatformOneBotV11)
	// 指向同一台测试服务器上一个不存在的前缀，等价于自建实例 404/挂掉。
	req.Settings[musicSettingAPIBase] = server.URL + "/down"

	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Reply, "雾里") || !strings.Contains(resp.Reply, "[CQ:record,") {
		t.Fatalf("Handle() = %#v, want the official fallback to still deliver a voice", resp)
	}
	if len(sharer.paths) == 1 {
		t.Cleanup(func() { cleanupLocalMediaFile(sharer.paths[0]) })
	}
}

// 会员或独家曲目在官方外链上会跳到 404 页。跟着下载下来的是一段 HTML，
// 发出去就是一条打不开的静音语音——这里必须停下来并说明原因。
func TestMusicPluginRefusesTheFallbackPageInsteadOfSendingSilence(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	plugin.officialOuterURL = server.URL + "/official/outer-blocked?id=%s"
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)

	req := musicTestRequest(server, PlatformOneBotV11)
	delete(req.Settings, musicSettingAPIBase)

	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || strings.Contains(resp.Reply, "[CQ:") {
		t.Fatalf("Handle() = %#v, want a text-only notice", resp)
	}
	if !strings.Contains(resp.Reply, "雾里") || !strings.Contains(resp.Reply, "会员") {
		t.Fatalf("reply = %q, want the song name and why there is no voice", resp.Reply)
	}
	if len(sharer.paths) != 0 {
		t.Fatalf("a fallback page was downloaded and shared anyway: %#v", sharer.paths)
	}
}

// 超过大小上限的曲目不能截断了发：半首歌的语音比不发更让人困惑。
func TestMusicPluginRefusesOversizedAudio(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("a", 3<<20)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/netease-token"}
	plugin.SetLocalMediaSharer(sharer)

	req := musicTestRequest(server, PlatformOneBotV11)
	req.Settings[musicSettingMaxMB] = 1

	resp, err := plugin.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || strings.Contains(resp.Reply, "[CQ:") {
		t.Fatalf("Handle() = %#v, want a text-only notice", resp)
	}
	if len(sharer.paths) != 0 {
		t.Fatalf("oversized audio was shared anyway: %#v", sharer.paths)
	}
}

// musicRequestSettings 是点歌工具用的设置，指向同一台假服务器。
func musicRequestSettings(server *httptest.Server) SettingValues {
	return SettingValues{
		musicSettingAPIBase: server.URL,
		musicSettingMaxMB:   10,
	}
}

func musicRequestTool(t *testing.T, plugin *MusicPlugin, settings SettingValues) *dianaMusicTool {
	t.Helper()
	tools, err := plugin.AgentTools(settings)
	if err != nil {
		t.Fatalf("AgentTools() error = %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("AgentTools() = %#v, want exactly the song request tool", tools)
	}
	tool, ok := tools[0].(*dianaMusicTool)
	if !ok || tool.Name() != musicToolName {
		t.Fatalf("AgentTools() gave %T named %q", tools[0], tools[0].Name())
	}
	return tool
}

// 点歌：「放首雾里」这种说法没有可认的形状，判断交给模型，工具只管把歌发出去。
func TestMusicRequestToolSendsTheSearchedSong(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/music-token"}
	plugin.SetLocalMediaSharer(sharer)

	tool := musicRequestTool(t, plugin, musicRequestSettings(server))
	output, err := tool.Run(context.Background(), map[string]any{"query": "雾里 姚六一"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(sharer.paths) == 1 {
		t.Cleanup(func() { cleanupLocalMediaFile(sharer.paths[0]) })
	}
	record := "[CQ:record,file=" + escapeCQParameter(sharer.url) + "]"
	if !strings.Contains(output, "song_ready") {
		t.Fatalf("Run() = %q", output)
	}

	// 歌发出去了这一轮就该结束，再让模型补一句「我给你放了一首」只会重复一遍。
	reply, done := tool.TerminalResult(output)
	if !done {
		t.Fatalf("TerminalResult(%q) did not finish the turn", output)
	}
	if !strings.Contains(reply, "雾里") || !strings.Contains(reply, record) {
		t.Fatalf("terminal reply = %q", reply)
	}
	chunks := splitChatReply(reply, chatSplitLimits{})
	if len(chunks) != 2 || chunks[1] != record {
		t.Fatalf("splitChatReply() = %#v, want the song line and the voice as two messages", chunks)
	}
}

// 搜不到就说搜不到，别随手发一首别的歌充数。
func TestMusicRequestToolReportsAMiss(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/music-token"}
	plugin.SetLocalMediaSharer(sharer)

	tool := musicRequestTool(t, plugin, musicRequestSettings(server))
	output, err := tool.Run(context.Background(), map[string]any{"query": "根本不存在的歌"})
	if err == nil {
		t.Fatalf("Run() = %q, want an error the model can relay", output)
	}
	if !strings.Contains(err.Error(), "没搜到") {
		t.Fatalf("Run() error = %v", err)
	}
	if len(sharer.paths) != 0 {
		t.Fatalf("a miss still downloaded something: %#v", sharer.paths)
	}
}

func TestMusicRequestToolRejectsAnEmptyQuery(t *testing.T) {
	server := musicTestServer(t, 213000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	tool := musicRequestTool(t, plugin, musicRequestSettings(server))
	if _, err := tool.Run(context.Background(), map[string]any{"query": "   "}); err == nil {
		t.Fatal("empty query was accepted")
	}
}

// 超长曲目在点歌这条路上同样拦住，而且要说清楚是哪首、为什么。
func TestMusicRequestToolHonoursTheDurationCap(t *testing.T) {
	server := musicTestServer(t, 3600000, []byte("audio"))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/music-token"}
	plugin.SetLocalMediaSharer(sharer)

	settings := musicRequestSettings(server)
	settings[musicSettingMaxDuration] = 600
	tool := musicRequestTool(t, plugin, settings)

	_, err := tool.Run(context.Background(), map[string]any{"query": "雾里"})
	if err == nil {
		t.Fatal("an hour-long track was accepted")
	}
	if !strings.Contains(err.Error(), "雾里") || !strings.Contains(err.Error(), "60:00") {
		t.Fatalf("Run() error = %v, want the song and the reason", err)
	}
	if len(sharer.paths) != 0 {
		t.Fatalf("overlong song was still downloaded: %#v", sharer.paths)
	}
}

// 关掉点歌开关，模型就看不到这个工具——链接解析那半边不受影响。
func TestMusicRequestToolDisappearsWhenTurnedOff(t *testing.T) {
	server := musicTestServer(t, 213000, []byte(strings.Repeat("audio", 512)))
	plugin := newMusicTestPlugin(server)
	sharer := &recordingLocalMediaSharer{url: "http://127.0.0.1:18080/media/music-token"}
	plugin.SetLocalMediaSharer(sharer)

	settings := musicRequestSettings(server)
	settings[musicSettingRequestSong] = false
	tools, err := plugin.AgentTools(settings)
	if err != nil {
		t.Fatalf("AgentTools() error = %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("AgentTools() = %#v, want none while song requests are off", tools)
	}

	req := musicTestRequest(server, PlatformOneBotV11)
	req.Settings[musicSettingRequestSong] = false
	resp, handleErr := plugin.Handle(context.Background(), req)
	if handleErr != nil {
		t.Fatalf("Handle() error = %v", handleErr)
	}
	if resp == nil || !strings.Contains(resp.Reply, "[CQ:record,") {
		t.Fatalf("link sharing broke when song requests were turned off: %#v", resp)
	}
	if len(sharer.paths) == 1 {
		t.Cleanup(func() { cleanupLocalMediaFile(sharer.paths[0]) })
	}
}

// 搜索接口给的是 ar/al/dt，详情接口给的是 artists/album/duration。
// 两套字段名都得认，否则在别人的自建实例上会解出一首没名字的歌。
func TestNeteaseSongPayloadAcceptsBothFieldGenerations(t *testing.T) {
	legacy := neteaseSongPayload{
		ID: "1", Name: "雾里",
		Artists:  []neteaseNamed{{Name: "姚六一"}},
		Album:    neteaseNamed{Name: "雾里"},
		Duration: 213000,
	}
	modern := neteaseSongPayload{
		ID: "1", Name: "雾里",
		AR: []neteaseNamed{{Name: "姚六一"}},
		AL: neteaseNamed{Name: "雾里"},
		DT: 213000,
	}
	for name, payload := range map[string]neteaseSongPayload{"legacy": legacy, "modern": modern} {
		got, ok := payload.toSong()
		if !ok {
			t.Fatalf("%s: toSong() reported failure", name)
		}
		if got.Title() != "雾里 - 姚六一" || got.Album != "雾里" || got.Duration != 213*time.Second {
			t.Fatalf("%s: toSong() = %#v", name, got)
		}
	}
	if _, ok := (neteaseSongPayload{ID: "1"}).toSong(); ok {
		t.Fatal("a nameless payload was accepted")
	}
}

// 点歌得对群里所有人可用。非主人走的是工具白名单，漏掉就等于这个功能只有主人能用。
func TestMusicRequestToolIsAvailableToOrdinaryMembers(t *testing.T) {
	member := RelationshipPolicy{Tier: RelationshipAcquaintance}
	allowed := member.allowedAgentToolNames()
	if allowed == nil {
		t.Fatal("a non-owner should have an explicit allowlist")
	}
	if !allowed[musicToolName] {
		t.Fatalf("%s is not reachable for ordinary members: %#v", musicToolName, allowed)
	}
}
