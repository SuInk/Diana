package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type qqFakeAPI struct {
	mu       sync.Mutex
	messages []map[string]any
	// messagePosts 记录每次打到 /messages 的请求体（含被去重拒收的那次）。
	messagePosts []map[string]any
	// prepares 记录每次 /upload_prepare 的请求体，用来核对 file_type。
	prepares []map[string]any
	merges   []map[string]any
	// uploadError 模拟平台拒绝文件上传，不能继续发送或报告成功。
	uploadError bool
	paths       []string
	put         []byte
	// dedup 让前 N 条 /messages 回 40054005，模拟平台去重。
	dedup int
	// rejectMarkdown 让 msg_type 2 的消息被拒收，模拟没开 Markdown 权限。
	rejectMarkdown bool
	// rejectKeyboard 让带 keyboard 的消息被拒收，模拟没开通自定义按钮。
	rejectKeyboard bool
	// rejectGallery 让带 mqqapi 图集节点的消息被拒收，和生产实测的回包一致。
	rejectGallery bool
	server        *httptest.Server
}

func newQQFakeAPI(t *testing.T) *qqFakeAPI {
	t.Helper()
	api := &qqFakeAPI{}
	mux := http.NewServeMux()
	mux.HandleFunc("/put/1", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.put = body
		api.paths = append(api.paths, "PUT "+r.URL.Path)
		api.mu.Unlock()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		api.mu.Lock()
		api.paths = append(api.paths, r.Method+" "+r.URL.Path)
		api.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload_prepare"):
			api.mu.Lock()
			api.prepares = append(api.prepares, body)
			api.mu.Unlock()
			if api.uploadError {
				_, _ = w.Write([]byte(`{"code":850012,"message":"file rejected"}`))
				return
			}
			size := body["file_size"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id": "up1", "block_size": size,
				"parts": []map[string]any{{"index": 1, "presigned_url": api.server.URL + "/put/1"}},
			})
		case strings.HasSuffix(r.URL.Path, "/upload_part_finish"):
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/files"):
			api.mu.Lock()
			api.merges = append(api.merges, body)
			api.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "FILEINFO", "ttl": 0})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			api.mu.Lock()
			api.messagePosts = append(api.messagePosts, body)
			_, hasKeyboard := body["keyboard"]
			if markdown, _ := body["markdown"].(map[string]any); api.rejectGallery && strings.Contains(markdownContentOf(markdown), "mqqapi://markdown/node") {
				api.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "请求参数不允许包含url mqqapi://markdown/node", "code": 40034028})
				return
			}
			if (api.rejectMarkdown && body["msg_type"] == float64(2)) || (api.rejectKeyboard && hasKeyboard) {
				api.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "不允许发送原生 markdown", "code": 304003})
				return
			}
			dedup := api.dedup > 0
			if dedup {
				api.dedup--
			} else {
				api.messages = append(api.messages, body)
			}
			n := len(api.messages)
			api.mu.Unlock()
			if dedup {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "消息被去重，请检查请求msgseq", "code": 40054005})
				return
			}
			// 发送响应带 ext_info.ref_idx，拟真环境同样返回它。
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       "m" + string(rune('0'+n)),
				"ext_info": map[string]any{"ref_idx": "REFIDX_m" + string(rune('0'+n))},
			})
		default:
			http.NotFound(w, r)
		}
	})
	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)
	return api
}

func TestQQOfficialSendsFiles(t *testing.T) {
	for _, group := range []bool{true, false} {
		for _, caption := range []string{"", "附件"} {
			t.Run(fmt.Sprintf("group=%t/caption=%s", group, caption), func(t *testing.T) {
				api := newQQFakeAPI(t)
				path := filepath.Join(t.TempDir(), "snapshot.bin")
				data := []byte("原文件内容\x00\xff")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				msg := OutgoingMessage{UserID: "U", Text: caption, PassiveReplyMessageID: "trigger", Segments: []MessageSegment{
					{Type: "file", Data: map[string]string{"file": path, "name": "报告.pdf"}},
					{Type: "file", Data: map[string]string{"file": path}},
				}}
				prefix := "/v2/users/U"
				if group {
					msg.GroupID = "G"
					prefix = "/v2/groups/G"
				}
				result, err := api.channel().SendWithResult(context.Background(), msg)
				if err != nil {
					t.Fatal(err)
				}
				if result["message_id"] != "m1" || !bytes.Equal(api.put, data) {
					t.Fatalf("result = %#v, uploaded %d bytes", result, len(api.put))
				}
				var wantPaths []string
				wantMessages := 2
				if caption != "" {
					wantMessages++
					wantPaths = append(wantPaths, "POST "+prefix+"/messages")
				}
				if len(api.messages) != wantMessages {
					t.Fatalf("messages = %#v, want %d", api.messages, wantMessages)
				}
				for i, name := range []string{"报告.pdf", "snapshot.bin"} {
					if len(api.prepares) != 2 || len(api.merges) != 2 {
						t.Fatalf("prepares = %#v, merges = %#v", api.prepares, api.merges)
					}
					prepare, merge := api.prepares[i], api.merges[i]
					if prepare["file_type"] != float64(4) || prepare["file_name"] != name || merge["file_type"] != float64(4) || merge["file_name"] != name || merge["srv_send_msg"] != false {
						t.Fatalf("prepare = %#v, merge = %#v", prepare, merge)
					}
					wantPaths = append(wantPaths, "POST "+prefix+"/upload_prepare", "PUT /put/1", "POST "+prefix+"/upload_part_finish", "POST "+prefix+"/files", "POST "+prefix+"/messages")
				}
				if !reflect.DeepEqual(api.paths, wantPaths) {
					t.Fatalf("request paths = %v, want %v", api.paths, wantPaths)
				}
				for i, sent := range api.messages {
					if sent["msg_id"] != "trigger" || sent["msg_seq"] != float64(i+1) {
						t.Fatalf("reply metadata = %#v", sent)
					}
					if i == 0 && caption != "" {
						continue
					}
					media, _ := sent["media"].(map[string]any)
					if sent["msg_type"] != float64(7) || media["file_info"] != "FILEINFO" {
						t.Fatalf("file message = %#v", sent)
					}
				}
			})
		}
	}
}

func TestQQOfficialRejectsInvalidFileMessages(t *testing.T) {
	for _, kind := range []string{"guild", "known_guild", "missing_source", "upload_error", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			api := newQQFakeAPI(t)
			channel := api.channel()
			path := filepath.Join(t.TempDir(), "file.txt")
			if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
				t.Fatal(err)
			}
			msg := OutgoingMessage{GroupID: "G", Segments: []MessageSegment{{Type: "file", Data: map[string]string{"file": path}}}}
			want := ""
			switch kind {
			case "guild":
				msg.PlatformScope, want = "qq_guild", "频道消息暂不支持发送富媒体"
			case "known_guild":
				channel.guildChannels = map[string]string{"G": "guild"}
				want = "频道消息暂不支持发送富媒体"
			case "missing_source":
				msg.Segments[0].Data["file"] = " "
				want = "缺少文件来源"
			case "upload_error":
				api.uploadError, want = true, "file rejected"
			case "oversize":
				if err := os.Truncate(path, qqMaxFileBytes+1); err != nil {
					t.Fatal(err)
				}
				want = "超过上限"
			}
			if _, err := channel.SendWithResult(context.Background(), msg); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
			if len(api.messages) != 0 || len(api.merges) != 0 || len(api.put) != 0 {
				t.Fatalf("failed file was uploaded or sent: %v", api.paths)
			}
			if kind != "upload_error" && len(api.paths) != 0 {
				t.Fatalf("unexpected API calls: %v", api.paths)
			}
		})
	}
}

func (a *qqFakeAPI) channel() *QQOfficialChannel {
	channel := NewQQOfficialChannel(QQOfficialConfig{AppID: "app", AppSecret: "secret"})
	channel.apiBaseOverride = a.server.URL
	channel.tokens = &platformTokenCache{fetch: func(context.Context) (string, time.Duration, error) {
		return "token", time.Hour, nil
	}}
	return channel
}

func TestQQOfficialSendsTextThenImageWithConsecutiveSeq(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "看图", ImageURLs: []string{png}, ReplyMessageID: "in1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["message_id"] != "m1" {
		t.Fatalf("result = %#v", result)
	}
	if len(api.messages) != 2 {
		t.Fatalf("messages = %#v", api.messages)
	}
	text, image := api.messages[0], api.messages[1]
	if text["content"] != "看图" || text["msg_seq"].(float64) != 1 {
		t.Fatalf("text message = %#v", text)
	}
	media, _ := image["media"].(map[string]any)
	if image["msg_type"].(float64) != 7 || media["file_info"] != "FILEINFO" || image["msg_seq"].(float64) != 2 || image["msg_id"] != "in1" {
		t.Fatalf("image message = %#v", image)
	}
	if len(api.put) == 0 {
		t.Fatal("image bytes were never uploaded to the presigned URL")
	}
	want := []string{"POST /v2/groups/G/messages", "POST /v2/groups/G/upload_prepare", "PUT /put/1", "POST /v2/groups/G/upload_part_finish", "POST /v2/groups/G/files", "POST /v2/groups/G/messages"}
	if strings.Join(api.paths, "|") != strings.Join(want, "|") {
		t.Fatalf("request order = %v", api.paths)
	}
}

func TestQQOfficialProactiveMessageHasNoMsgSeq(t *testing.T) {
	api := newQQFakeAPI(t)
	if _, err := api.channel().SendWithResult(context.Background(), OutgoingMessage{GroupID: "G", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, has := api.messages[0]["msg_seq"]; has {
		t.Fatalf("proactive message carried msg_seq: %#v", api.messages[0])
	}
}

// 链接解析出视频后的真实形态：正文 + 封面图 + 本地 mp4。此前视频在这一步被静默
// 丢弃（只发文字和封面、不报错）；现在按 file_type=2 分片上传后用富媒体消息发出。
func TestQQOfficialSendsVideoAfterTextAndCover(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	dir := t.TempDir()
	video := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(video, append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, make([]byte, 64*1024)...), 0o600); err != nil {
		t.Fatal(err)
	}
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID:        "G",
		Text:           "[Bilibili] 标题",
		ImageURLs:      []string{png},
		VideoURLs:      []string{video},
		ReplyMessageID: "in1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["message_id"] != "m1" {
		t.Fatalf("result = %#v", result)
	}
	if len(api.messages) != 3 {
		t.Fatalf("messages = %#v, want text + cover + video", api.messages)
	}
	if api.messages[0]["content"] != "[Bilibili] 标题" || api.messages[0]["msg_type"].(float64) != 0 {
		t.Fatalf("text message = %#v", api.messages[0])
	}
	videoMsg := api.messages[2]
	media, _ := videoMsg["media"].(map[string]any)
	if videoMsg["msg_type"].(float64) != 7 || media["file_info"] != "FILEINFO" || videoMsg["msg_seq"].(float64) != 3 {
		t.Fatalf("video message = %#v", videoMsg)
	}
	if len(api.prepares) != 2 {
		t.Fatalf("upload_prepare calls = %#v, want one for the cover and one for the video", api.prepares)
	}
	if api.prepares[0]["file_type"].(float64) != 1 {
		t.Fatalf("cover prepare = %#v, want file_type 1", api.prepares[0])
	}
	if prepare := api.prepares[1]; prepare["file_type"].(float64) != 2 || prepare["file_name"] != "video.mp4" {
		t.Fatalf("video prepare = %#v, want file_type 2 named video.mp4", prepare)
	}
}

// 只发视频时此前不发任何请求、仍返回成功；现在会发出视频。
func TestQQOfficialSendsVideoOnlyMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	video := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(video, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID:   "G",
		VideoURLs: []string{video},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || api.messages[0]["msg_type"].(float64) != 7 {
		t.Fatalf("messages = %#v, want one rich-media message", api.messages)
	}
	if result == nil || result["message_id"] == "" {
		t.Fatalf("result = %#v, want the platform message id", result)
	}
}

// 音频按 file_type=3 上传，平台会转为语音消息。
func TestQQOfficialSendsAudioAsVoiceMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	audio := filepath.Join(t.TempDir(), "song.mp3")
	if err := os.WriteFile(audio, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID:   "G",
		AudioURLs: []string{audio},
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || api.messages[0]["msg_type"].(float64) != 7 {
		t.Fatalf("messages = %#v", api.messages)
	}
	if len(api.prepares) != 1 || api.prepares[0]["file_type"].(float64) != 3 || api.prepares[0]["file_name"] != "song.mp3" {
		t.Fatalf("prepares = %#v, want file_type 3 named song.mp3", api.prepares)
	}
}

// 40054005「消息被去重」换一个 msg_seq 重发一次可通过；通道内换号重试，
// 不把这种错误漏给上层当作普通失败。
func TestQQOfficialRetriesDedupRejectionWithNextSeq(t *testing.T) {
	api := newQQFakeAPI(t)
	api.dedup = 1
	channel := api.channel()
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID:        "G",
		Text:           "看视频",
		ReplyMessageID: "in1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 {
		t.Fatalf("messages = %#v, want the retried message delivered", api.messages)
	}
	if len(api.messagePosts) != 2 {
		t.Fatalf("message posts = %#v, want one rejected and one retried", api.messagePosts)
	}
	if api.messagePosts[0]["msg_seq"].(float64) != 1 || api.messagePosts[1]["msg_seq"].(float64) != 2 {
		t.Fatalf("msg_seq values = %#v, want 1 then 2", api.messagePosts)
	}
}

// 出站引用：message_reference 与被动回复 msg_id 正交共存，引用目标取 REFIDX_ 形态。
// 索引里能反查出目标消息的 REFIDX_ 键时，文本消息携带 message_reference；富媒体
// 消息不携带；反查不到时不带引用照常发送（引用值非法可能被平台整条拒收）。
func TestQQOfficialSendsOutboundQuoteOnTextMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	// 触发消息的 d.id 已在入站侧登记进引用索引（handleDispatch 里做，这里直接种）。
	channel.refs.recordInbound("REFIDX_in", "ROBOT1.0_trigger", "member-1", false, time.Now())
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "收到", ImageURLs: []string{png},
		PassiveReplyMessageID: "ROBOT1.0_trigger", ReplyMessageID: "ROBOT1.0_trigger",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 2 {
		t.Fatalf("messages = %#v, want text + image", api.messages)
	}
	text, image := api.messages[0], api.messages[1]
	// msg_id（被动回复）与 message_reference（引用卡片）同体携带，互不干扰。
	if text["msg_id"] != "ROBOT1.0_trigger" || text["msg_seq"].(float64) != 1 {
		t.Fatalf("text message = %#v, want passive reply metadata", text)
	}
	reference, ok := text["message_reference"].(map[string]any)
	if !ok || reference["message_id"] != "REFIDX_in" {
		t.Fatalf("text message_reference = %#v, want the REFIDX_ key of the trigger", text["message_reference"])
	}
	if _, has := image["message_reference"]; has {
		t.Fatalf("image message = %#v, media must not carry message_reference", image)
	}
	if image["msg_id"] != "ROBOT1.0_trigger" {
		t.Fatalf("image message = %#v, want passive reply metadata only", image)
	}
}

// 引用目标在索引里查不到（进程重启后索引丢失、目标超出时效）：不带引用照常发送，
// 不能因为引用拼不上就丢消息。
func TestQQOfficialOutboundQuoteSkippedWhenIndexMisses(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "收到",
		PassiveReplyMessageID: "ROBOT1.0_trigger", ReplyMessageID: "ROBOT1.0_trigger",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 {
		t.Fatalf("messages = %#v, want one text message", api.messages)
	}
	if _, has := api.messages[0]["message_reference"]; has {
		t.Fatalf("message = %#v, unresolvable quote target must not attach message_reference", api.messages[0])
	}
}

// 机器人引用自己上一条发言：发送响应的 ext_info.ref_idx 已登记进索引（链式引用），
// 下一轮拿响应 id 反查即得 REFIDX_ 键。
func TestQQOfficialOutboundQuoteOfOwnPreviousMessage(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	// 第一条：无引用，响应 m1 的 ref_idx=REFIDX_m1 被登记。
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "第一条", PassiveReplyMessageID: "in1",
	}); err != nil {
		t.Fatal(err)
	}
	// 第二条：引用自己上一条（运行时用响应 id 标识历史消息）。
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", Text: "第二条",
		PassiveReplyMessageID: "in2", ReplyMessageID: "m1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 2 {
		t.Fatalf("messages = %#v, want two messages", api.messages)
	}
	reference, ok := api.messages[1]["message_reference"].(map[string]any)
	if !ok || reference["message_id"] != "REFIDX_m1" {
		t.Fatalf("message_reference = %#v, want the previous response ref_idx", api.messages[1]["message_reference"])
	}
}

// 频道（子频道）通道不携带 message_reference。
func TestQQOfficialGuildMessageOmitsOutboundQuote(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	channel.refs.recordInbound("REFIDX_guild", "ROBOT1.0_guild", "member-1", false, time.Now())
	if _, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		GroupID: "G", PlatformScope: "qq_guild", Text: "收到",
		PassiveReplyMessageID: "ROBOT1.0_guild", ReplyMessageID: "ROBOT1.0_guild",
	}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 {
		t.Fatalf("messages = %#v, want one message", api.messages)
	}
	if _, has := api.messages[0]["message_reference"]; has {
		t.Fatalf("message = %#v, guild messages must not carry message_reference", api.messages[0])
	}
}

// 带 Markdown 结构的回复走 msg_type 2，平台不认的行内代码就地去掉标记，列表前补
// 空行（紧贴正文的列表 QQ 不识别）。
func TestQQOfficialSendsMarkdownAsNativeMarkdown(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "G", Text: "**结论**\n- 用 `go test`\n- 再看日志", ReplyMessageID: "in1"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 {
		t.Fatalf("messages = %#v", api.messages)
	}
	msg := api.messages[0]
	markdown, _ := msg["markdown"].(map[string]any)
	if msg["msg_type"].(float64) != 2 || markdown["content"] != "**结论**\n\n- 用 go test\n- 再看日志" || msg["content"] != nil || msg["msg_id"] != "in1" {
		t.Fatalf("markdown message = %#v", msg)
	}
}

// 大白话不换消息类型，照旧走纯文本。
func TestQQOfficialKeepsPlainTextAsMsgType0(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "G", Text: "在呢，刚看到", ReplyMessageID: "in1"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messages) != 1 || api.messages[0]["msg_type"].(float64) != 0 || api.messages[0]["content"] != "在呢，刚看到" {
		t.Fatalf("messages = %#v", api.messages)
	}
}

// Markdown 被平台拒收时降成纯文本重发，回复不能整条丢掉。
func TestQQOfficialFallsBackToPlainWhenMarkdownRejected(t *testing.T) {
	api := newQQFakeAPI(t)
	api.rejectMarkdown = true
	channel := api.channel()
	if err := channel.Send(context.Background(), OutgoingMessage{GroupID: "G", Text: "**结论**：可以", ReplyMessageID: "in1"}); err != nil {
		t.Fatal(err)
	}
	if len(api.messagePosts) != 2 || len(api.messages) != 1 {
		t.Fatalf("posts = %#v messages = %#v", api.messagePosts, api.messages)
	}
	msg := api.messages[0]
	if msg["msg_type"].(float64) != 0 || msg["content"] != "结论：可以" || msg["msg_seq"].(float64) != 2 {
		t.Fatalf("fallback message = %#v", msg)
	}
}

// send_attachment 的 mode=file 交来的是 file 段。此前 QQ 官方在 sendFileAttachment
// 的平台白名单前就报 unavailable；现在按 file_type=4 上传，文件名原样保留（含无扩展名）。
func TestQQOfficialSendsFileSegment(t *testing.T) {
	api := newQQFakeAPI(t)
	channel := api.channel()
	dir := t.TempDir()
	file := filepath.Join(dir, "Makefile")
	if err := os.WriteFile(file, []byte("all:\n\techo hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := channel.SendWithResult(context.Background(), OutgoingMessage{
		UserID:   "U",
		Segments: []MessageSegment{{Type: "file", Data: map[string]string{"file": file, "name": "Makefile"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["message_id"] != "m1" || len(api.messages) != 1 {
		t.Fatalf("result = %#v messages = %#v", result, api.messages)
	}
	if prepare := api.prepares[0]; prepare["file_type"].(float64) != 4 || prepare["file_name"] != "Makefile" {
		t.Fatalf("prepare = %#v, want file_type 4 named Makefile", prepare)
	}
	if string(api.put) != "all:\n\techo hi\n" {
		t.Fatalf("uploaded = %q", api.put)
	}
	want := []string{"POST /v2/users/U/upload_prepare", "PUT /put/1", "POST /v2/users/U/upload_part_finish", "POST /v2/users/U/files", "POST /v2/users/U/messages"}
	if strings.Join(api.paths, "|") != strings.Join(want, "|") {
		t.Fatalf("request order = %v", api.paths)
	}
}

func markdownContentOf(markdown map[string]any) string {
	content, _ := markdown["content"].(string)
	return content
}
