// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

const (
	defaultFeedListLimit = 10
	maximumFeedListLimit = 30
)

// dianaFeedTool 是模型往「动态」页发帖的入口。
//
// 只给主人：动态是机器人公开摆在控制台里的内容，群里谁都能触发的话，一句「发条动态说
// XXX」就成了往主人控制台里塞任意文字和图片的通道。主人自己让它写日记、发动态，或者
// 用定时提醒、事件触发让它定时写，都以主人身份跑，所以不需要额外放行。
type dianaFeedTool struct {
	runtime *Runtime
	event   MessageEvent
}

type dianaFeedResult struct {
	OK      bool         `json:"ok"`
	Action  string       `json:"action"`
	Message string       `json:"message,omitempty"`
	Post    *FeedPost    `json:"post,omitempty"`
	Items   []FeedPost   `json:"items,omitempty"`
	Comment *FeedComment `json:"comment,omitempty"`
	Likes   *FeedLikes   `json:"likes,omitempty"`
}

func newDianaFeedTool(runtime *Runtime, event MessageEvent) *dianaFeedTool {
	return &dianaFeedTool{runtime: runtime, event: event}
}

func (*dianaFeedTool) Name() string { return dianaFeedToolName }

// Description 精简时保留了 #939 原描述里的这些约束，各压成一句：
//   - 动态页只给主人看，不会发到群或私聊：不能拿它当通知某人的渠道。
//   - 写日记前先 list，避免重复。
//   - needs_reply + comment：主人评论后的回复流程。
//   - 配图来源限制、生图异步要等落盘：代码会拒绝，但提前说能省掉白费的一轮。
//   - 第一人称、不编造、不写别人隐私。
func (*dianaFeedTool) Description() string {
	return "往你自己的「动态」页发动态（kind=post）或日记（kind=diary），只在 WebUI 给主人看，不会发到群或私聊。" +
		"写日记前先 list 免得重复。主人评论了的动态用 list needs_reply=true 找出，再 comment 回复，一两句即可。" +
		"配图只能用你自己生成或发过的图；生图是异步的，等文件落盘再发。" +
		"用第一人称写自己的经历和想法，不编造，不写别人的隐私。"
}

func (*dianaFeedTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("comment 评论或回复，like 点赞或取消", "post", "list", "delete", "comment", "like"),
		"kind":      toolEnumParam("post 动态（默认）、diary 日记；list 时按类筛选", FeedKindPost, FeedKindDiary),
		"title":     toolStringParam("标题，可选，最多 " + itoa(FeedTitleMaxRunes) + " 字"),
		"content":   toolStringParam("post 正文最多 " + itoa(FeedContentMaxRunes) + " 字，comment 内容最多 " + itoa(FeedCommentMaxRunes) + " 字"),
		"images": toolStringArrayParam("最多 " + itoa(FeedMaxImages) + " 张：" + agent.WorkspaceOutputsDir + "/ 下的图片路径，" +
			"或 chat:<message_id>[:<序号>]（须是你发的消息）"),
		"id":          toolStringParam("动态 ID，取自 list"),
		"reply_to":    toolStringParam("要回复的评论 ID，取自 list；不填为顶层评论"),
		"liked":       toolBoolParam("false 取消点赞，默认 true"),
		"needs_reply": toolBoolParam("只列主人评论后你还没回的动态"),
		"limit":       toolIntParam("返回条数，默认 "+itoa(defaultFeedListLimit), 1, maximumFeedListLimit),
	})
}

func (t *dianaFeedTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("feed_post: runtime is not configured")
	}
	store := t.runtime.feedStore()
	if store == nil {
		return "", fmt.Errorf("动态功能需要持久化存储，当前部署没有启用")
	}
	if strings.TrimSpace(t.event.ProfileID) == "" {
		// 动态按机器人存；拿不到身份就写进一个没人认领的桶，控制台按机器人筛选时永远看不到。
		return "", fmt.Errorf("拿不到机器人身份，无法发动态")
	}
	switch strings.ToLower(strings.TrimSpace(configToolString(input, "operation"))) {
	case "post", "add", "create":
		return t.post(ctx, store, input)
	case "list", "get":
		return t.list(ctx, store, input)
	case "delete", "remove":
		return t.delete(ctx, store, input)
	case "comment", "reply":
		return t.comment(ctx, store, input)
	case "like", "unlike":
		return t.like(ctx, store, input)
	default:
		return "", fmt.Errorf("不支持的操作，operation 只能是 post、list、delete、comment 或 like")
	}
}

func (t *dianaFeedTool) post(ctx context.Context, store FeedStore, input map[string]any) (string, error) {
	content := NormalizeFeedContent(configToolString(input, "content"))
	if content == "" {
		return "", fmt.Errorf("正文不能为空")
	}
	sources := feedImageSources(input["images"])
	if len(sources) > FeedMaxImages {
		return "", fmt.Errorf("一条动态最多 %d 张图，这次给了 %d 张", FeedMaxImages, len(sources))
	}
	images := make([]FeedImageInput, 0, len(sources))
	total := 0
	for index, source := range sources {
		image, err := t.loadImage(ctx, source)
		if err != nil {
			return "", fmt.Errorf("第 %d 张图（%s）不能用：%w；这条动态没有发出，处理好之后重新发", index+1, source, err)
		}
		total += len(image.Data)
		if total > FeedPostImagesMaxBytes {
			return "", fmt.Errorf("配图加起来超过 %d MB，这条动态没有发出；少配几张或换小一点的图", FeedPostImagesMaxBytes>>20)
		}
		images = append(images, image)
	}
	post, err := store.CreateFeedPost(ctx, FeedPostCreateRequest{
		ProfileID:     strings.TrimSpace(t.event.ProfileID),
		Kind:          NormalizeFeedKind(configToolString(input, "kind")),
		Title:         NormalizeFeedTitle(configToolString(input, "title")),
		Content:       content,
		Images:        images,
		SourceSession: sessionKey(t.event),
		SourceUserID:  strings.TrimSpace(t.event.UserID),
		Now:           t.runtime.clock(),
	})
	if err != nil {
		return "", err
	}
	return encodeFeedResult(dianaFeedResult{OK: true, Action: "post", Message: "已发布到动态页；提醒一下，这只是发在控制台里，没有发给任何人", Post: &post})
}

func (t *dianaFeedTool) list(ctx context.Context, store FeedStore, input map[string]any) (string, error) {
	limit := intFromAny(input["limit"])
	if limit <= 0 {
		limit = defaultFeedListLimit
	}
	if limit > maximumFeedListLimit {
		limit = maximumFeedListLimit
	}
	kind := ""
	if raw := strings.TrimSpace(configToolString(input, "kind")); raw != "" {
		kind = NormalizeFeedKind(raw)
	}
	needsReply := toolInputBool(input, "needs_reply")
	query := FeedListQuery{ProfileID: strings.TrimSpace(t.event.ProfileID), Kind: kind, Limit: limit}
	if needsReply {
		// 过滤发生在取回之后，要多取一些才不会因为大部分帖子都已回复而只剩几条。
		query.Limit = maximumFeedListLimit * 3
	}
	posts, err := store.ListFeedPosts(ctx, query)
	if err != nil {
		return "", err
	}
	if needsReply {
		waiting := posts[:0]
		for _, post := range posts {
			if last := len(post.Comments) - 1; last >= 0 && post.Comments[last].AuthorKind == FeedAuthorAdmin {
				waiting = append(waiting, post)
			}
		}
		posts = waiting
		if len(posts) > limit {
			posts = posts[:limit]
		}
	}
	message := fmt.Sprintf("最近 %d 条", len(posts))
	if len(posts) == 0 {
		message = "还没有发过动态"
	}
	return encodeFeedResult(dianaFeedResult{OK: true, Action: "list", Message: message, Items: posts})
}

func (t *dianaFeedTool) comment(ctx context.Context, store FeedStore, input map[string]any) (string, error) {
	postID := strings.TrimSpace(configToolString(input, "id"))
	if postID == "" {
		return "", fmt.Errorf("评论要带上动态 id，先用 list 查一遍")
	}
	content := NormalizeFeedComment(configToolString(input, "content"))
	if content == "" {
		return "", fmt.Errorf("评论内容不能为空")
	}
	comment, err := store.AddFeedComment(ctx, FeedCommentRequest{
		PostID: postID, ReplyToID: strings.TrimSpace(configToolString(input, "reply_to")),
		AuthorKind: FeedAuthorBot, Content: content, ProfileID: strings.TrimSpace(t.event.ProfileID), Now: t.runtime.clock(),
	})
	switch {
	case errors.Is(err, ErrFeedPostNotFound):
		return "", fmt.Errorf("没有这条动态（或不是你发的），先用 list 查一遍")
	case errors.Is(err, ErrFeedCommentNotFound):
		return "", fmt.Errorf("要回复的评论不在这条动态下，先用 list 查一遍评论 id")
	case errors.Is(err, ErrFeedCommentCapacity):
		return "", fmt.Errorf("这条动态下的评论已经有 %d 条，到上限了", FeedMaxCommentsPerPost)
	case err != nil:
		return "", err
	}
	return encodeFeedResult(dianaFeedResult{OK: true, Action: "comment", Message: "已评论；这只出现在动态页里，没有通知任何人", Comment: &comment})
}

func (t *dianaFeedTool) like(ctx context.Context, store FeedStore, input map[string]any) (string, error) {
	postID := strings.TrimSpace(configToolString(input, "id"))
	if postID == "" {
		return "", fmt.Errorf("点赞要带上动态 id，先用 list 查一遍")
	}
	liked := true
	if value, ok := input["liked"]; ok {
		liked = toolInputBool(map[string]any{"liked": value}, "liked")
	}
	likes, err := store.SetFeedLike(ctx, strings.TrimSpace(t.event.ProfileID), postID, FeedAuthorBot, liked, t.runtime.clock())
	if errors.Is(err, ErrFeedPostNotFound) {
		return "", fmt.Errorf("没有这条动态（或不是你发的），先用 list 查一遍")
	}
	if err != nil {
		return "", err
	}
	message := "已点赞"
	if !liked {
		message = "已取消点赞"
	}
	return encodeFeedResult(dianaFeedResult{OK: true, Action: "like", Message: message, Likes: &likes})
}

func (t *dianaFeedTool) delete(ctx context.Context, store FeedStore, input map[string]any) (string, error) {
	id := strings.TrimSpace(configToolString(input, "id"))
	if id == "" {
		return "", fmt.Errorf("要删的动态 id 不能为空，先用 list 查一遍")
	}
	post, err := store.DeleteFeedPost(ctx, strings.TrimSpace(t.event.ProfileID), id)
	if errors.Is(err, ErrFeedPostNotFound) {
		return encodeFeedResult(dianaFeedResult{Action: "delete", Message: "没有这条动态，可能已经删过了"})
	}
	if err != nil {
		return "", err
	}
	return encodeFeedResult(dianaFeedResult{OK: true, Action: "delete", Message: "已删掉这条动态", Post: &post})
}

// feedImageSources 把 images 参数规整成去空、去重的来源列表。
func feedImageSources(raw any) []string {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	case string:
		values = []string{typed}
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// feedImageExtensions 是生图落盘可能用到的扩展名。生图工具只告诉模型不含扩展名的
// 路径前缀，模型原样传过来时按这几个依次试。
var feedImageExtensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif"}

func (t *dianaFeedTool) loadImage(ctx context.Context, source string) (FeedImageInput, error) {
	var data []byte
	if strings.HasPrefix(strings.ToLower(source), "chat:") {
		messageID, index := parseFeedChatSource(source)
		payload, err := t.runtime.workspaceChatMedia(ctx, t.event, messageID, index)
		if err != nil {
			return FeedImageInput{}, err
		}
		// generated 表示这条消息是机器人自己发出的（引用别人的图不算）。别人发的图、
		// 网上的图都不能配进动态：那是别人的内容，不是它自己的。
		if !payload.generated {
			return FeedImageInput{}, fmt.Errorf("只能用你自己发出去的图，这条消息的图不是你发的")
		}
		data = payload.data
	} else {
		var err error
		data, err = readFeedWorkspaceImage(source)
		if err != nil {
			return FeedImageInput{}, err
		}
	}
	return checkFeedImage(data)
}

// checkFeedImage 只放行位图：按内容认类型，不信文件名。这一步同时挡住了「把工作目录里
// 别的文件当图片读进动态页」——凭据、配置文件读出来不是图片，直接被拒。
func checkFeedImage(data []byte) (FeedImageInput, error) {
	if len(data) == 0 {
		return FeedImageInput{}, fmt.Errorf("内容是空的")
	}
	if len(data) > FeedImageMaxBytes {
		return FeedImageInput{}, fmt.Errorf("单张图不能超过 %d MB", FeedImageMaxBytes>>20)
	}
	mime := agent.SniffMediaType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return FeedImageInput{}, fmt.Errorf("不是 PNG、JPEG、GIF 或 WebP 图片")
	}
	image := FeedImageInput{Data: data, MIME: mime}
	if width, height, ok := agent.ImageDimensions(data); ok {
		image.Width, image.Height = width, height
	}
	return image, nil
}

// readFeedWorkspaceImage 只读 outputs/ 下的文件：生图结果和机器人自己发出去的图都落在
// 这里，downloads/ 是别人发来的和网上下载的，不算它自己的。
func readFeedWorkspaceImage(raw string) ([]byte, error) {
	root := AgentWorkspaceDir()
	rel, err := agent.NormalizeWorkspacePath(root, raw)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(rel, agent.WorkspaceOutputsDir+"/") {
		return nil, fmt.Errorf("配图只能用 %s/ 下你自己生成或发出去的图", agent.WorkspaceOutputsDir)
	}
	data, err := agent.ReadWorkspaceFile(root, rel, FeedImageMaxBytes)
	if err == nil {
		return data, nil
	}
	// 没有扩展名时按生图落盘的几种扩展名再找一遍。
	if !strings.Contains(baseName(rel), ".") {
		for _, ext := range feedImageExtensions {
			if data, retry := agent.ReadWorkspaceFile(root, rel+ext, FeedImageMaxBytes); retry == nil {
				return data, nil
			}
		}
	}
	return nil, err
}

func baseName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

// parseFeedChatSource 解析 chat:<message_id>[:<序号>]，序号缺省为 0（由取图逻辑判断
// 消息里是不是只有一个媒体）。
func parseFeedChatSource(source string) (string, int) {
	rest := strings.TrimSpace(source[len("chat:"):])
	if index := strings.LastIndex(rest, ":"); index >= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(rest[index+1:])); err == nil {
			return strings.TrimSpace(rest[:index]), n
		}
	}
	return rest, 0
}

func encodeFeedResult(result dianaFeedResult) (string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
