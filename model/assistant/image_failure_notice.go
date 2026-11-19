// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"

	"github.com/SuInk/diana/model/llm"
)

// 识图失败时，提示词里那段「当前图片的独立视觉描述」整块消失，模型手上只剩一句「请
// 分析这张图片」却没有图。它只能自己猜，猜出来的通常是「图没传过来」——把我们这边的
// 故障说成用户没发图。2026-09-21 线上就是这么回复的。
//
// 所以失败要明写进提示词，而且要说清是哪一种失败：这几件事对用户的含义完全不同。
//   - 获取失败：图没下下来或解不开，我们手上根本没有图，让对方重发是对的。
//   - 送不进模型：图我们有、也发出去了，是这条视觉链路不接受图片输入。让对方重发
//     没有任何用，得换模型。
//   - 识别超时：图送到了，模型没在时限内答完。重试有用，重发没用。
//   - 调用报错：识图请求本身失败了（上游报错、限流、配置不对），重发也没用。
//
// 2026-11-08 线上把对话模型换成了不收图的模型，识图跟着走，机器人只会说「图没加载
// 出来，你重发一下」，群里和主人都看不出是识图模型的问题。所以真实原因（哪个模型、
// 报了什么错）要一起写进提示词，让回复照实说，同时记一条错误日志。
const (
	imageFailureUnavailable  = "unavailable"
	imageFailureNotDelivered = "not_delivered"
	imageFailureTimeout      = "timeout"
	imageFailureError        = "error"
)

// errVisionImageNotDelivered 表示图片发出去了，但模型那一侧没有收到图片输入。
// 无论是模型本身不支持图片，还是网关把 image part 丢了，对我们都是同一件事：这条
// 链路送不进图，重试同一个模型不会有不同结果。
var errVisionImageNotDelivered = errors.New("vision provider received no image input")

// errVisionImageUnavailable 表示图片内容取不到——文件没了、下载失败或解码失败。
var errVisionImageUnavailable = errors.New("image content is unavailable")

// visionNotDeliveredError 带上是哪个模型说自己没收到图，报给用户时才指得出该换谁。
type visionNotDeliveredError struct {
	model  string
	answer string
}

func newVisionNotDeliveredError(model, answer string) error {
	return &visionNotDeliveredError{model: strings.TrimSpace(model), answer: truncateRunes(answer, 60)}
}

func (e *visionNotDeliveredError) Error() string {
	if e.model == "" {
		return fmt.Sprintf("%v: %s", errVisionImageNotDelivered, e.answer)
	}
	return fmt.Sprintf("%v: model=%s: %s", errVisionImageNotDelivered, e.model, e.answer)
}

func (e *visionNotDeliveredError) Unwrap() error { return errVisionImageNotDelivered }

func classifyImageDescriptionFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errVisionImageNotDelivered):
		return imageFailureNotDelivered
	case errors.Is(err, errVisionImageUnavailable):
		return imageFailureUnavailable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return imageFailureTimeout
	default:
		return imageFailureError
	}
}

const imageFailureDetailLimit = 160

var imageFailureDetailURL = regexp.MustCompile(`(?i)\b(?:https?|wss?|file)://\S+`)

// imageFailureDetail 给出要报给对方的具体原因：哪个模型没收到图，或者报错原文。
// 取不到图和超时没有更多可说的，返回空串。报错原文里的地址去掉——图片直链带着
// 临时凭据，不能跟着报错发进群里。
func imageFailureDetail(err error) string {
	switch classifyImageDescriptionFailure(err) {
	case imageFailureNotDelivered:
		var notDelivered *visionNotDeliveredError
		if errors.As(err, &notDelivered) && notDelivered.model != "" {
			return "识图模型 " + notDelivered.model + " 没收到图片，多半是它不支持图片输入"
		}
		return ""
	case imageFailureError:
		detail := imageFailureDetailURL.ReplaceAllString(err.Error(), "[地址]")
		detail = strings.Join(strings.Fields(detail), " ")
		return truncateRunes(detail, imageFailureDetailLimit)
	default:
		return ""
	}
}

// recordImageDescriptionFailureLog 把这一轮的识图失败记成一条错误日志，日志页能直接
// 查到是哪条消息、哪种失败、报错原文。这一轮被新消息顶掉（context canceled）不算故障。
func (r *Runtime) recordImageDescriptionFailureLog(event MessageEvent, sourceMessageID string, cause error) {
	if cause == nil || errors.Is(cause, context.Canceled) {
		return
	}
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	reason := classifyImageDescriptionFailure(cause)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "image_description_failed",
		Message: "识图失败：" + imageFailureLogLabels[reason],
		Detail:  cause.Error(),
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"profile_id":        event.ProfileID,
			"platform":          event.Platform,
			"group_id":          event.GroupID,
			"user_id":           event.UserID,
			"reason":            reason,
			"source_message_id": sourceMessageID,
		},
	})
}

var imageFailureLogLabels = map[string]string{
	imageFailureNotDelivered: "识图模型没收到图片输入",
	imageFailureUnavailable:  "图片没取到",
	imageFailureTimeout:      "识图超时",
	imageFailureError:        "识图调用报错",
}

// failurePositions 在没有描述时才需要，把失败原因写回每一处出现该图的片段。
func (t *recallImageTarget) failurePositions() []recallImagePosition {
	if t == nil || t.failure == "" {
		return nil
	}
	return t.positions
}

// imageFailureNotice 把片段上记下的失败原因拼成提示词里的一段话。没有失败记录时返回
// 空串，调用方什么也不加。
//
// imageAttached 表示原图是否还附在这一轮的请求里。它决定措辞能说到多满：识图失败的是
// vision 分组，chat 分组可以是另一个模型，原图对它可能完全可读。所以这里绝不能替模型
// 断言「你看不了图」——那会让它谎称自己看不见一张其实看得见的图。只说清楚发生了什么，
// 看得见看不见由模型自己判断。
//
// configs 传机器人配置时读它的覆盖值；标题那行是固定的段头，不开放修改。
func imageFailureNotice(event MessageEvent, imageAttached bool, configs ...BotConfig) string {
	overrides := promptOverridesOf(configs)
	counts := map[string]int{}
	details := map[string][]string{}
	segments := append([]MessageSegment(nil), event.Segments...)
	if event.Quoted != nil {
		segments = append(segments, event.Quoted.Segments...)
	}
	for _, segment := range segments {
		if !recallStillImageSegment(segment) {
			continue
		}
		reason := strings.TrimSpace(segment.Data[recallImageFailureKey])
		if reason == "" {
			continue
		}
		counts[reason]++
		if detail := strings.TrimSpace(segment.Data[recallImageFailureDetailKey]); detail != "" && !containsString(details[reason], detail) {
			details[reason] = append(details[reason], detail)
		}
	}
	if len(counts) == 0 {
		return ""
	}
	var lines []string
	// 顺序固定，提示词才不会因为 map 遍历顺序每轮都变，破坏 prompt 缓存。
	for _, reason := range []string{imageFailureNotDelivered, imageFailureUnavailable, imageFailureTimeout, imageFailureError} {
		count := counts[reason]
		if count == 0 {
			continue
		}
		line := fmt.Sprintf("- %d 张：%s", count, overrides.text(imageFailureExplanations[reason]))
		// 同一轮里原因通常只有一种；多了也只带前两条，免得一串报错把提示词撑长。
		if causes := details[reason]; len(causes) > 0 {
			if len(causes) > 2 {
				causes = causes[:2]
			}
			line += "\n  具体原因：" + strings.Join(causes, "；")
		}
		lines = append(lines, line)
	}
	var tail []string
	if imageAttached {
		tail = append(tail, overrides.text(promptImageFailureAttachedSpec))
	}
	tail = append(tail, overrides.text(promptImageFailureGuidanceSpec))
	return "【图片处理情况】\n" +
		strings.Join(lines, "\n") + "\n" + strings.Join(tail, "\n")
}

// 每条末尾带上重发口径——能不能让对方重发，正是几种失败的区别所在。
var imageFailureExplanations = map[string]*PromptSpec{
	imageFailureNotDelivered: imageFailureSpec("not_delivered", "识图失败：送不进模型", "图片发出去了、但识图模型没收到图片输入时，用这句说明这张图的情况。", "识图失败，图没能送进识图模型；让对方重发同一张不会有任何变化，要换一个能看图的识图模型。"),
	imageFailureUnavailable:  imageFailureSpec("unavailable", "识图失败：图没取到", "图片没下载下来或解不开时，用这句说明这张图的情况。", "图片内容没取到，没下下来或者打不开；可以请对方重发。"),
	imageFailureTimeout:      imageFailureSpec("timeout", "识图失败：超时", "识图模型没在时限内答完时，用这句说明这张图的情况。", "识图超时，这一轮没看完；可以说等下再看，不要请对方重发。"),
	imageFailureError:        imageFailureSpec("error", "识图失败：调用报错", "识图请求本身报错（上游出错、限流、配置不对）时，用这句说明这张图的情况。", "识图调用报错了；让对方重发同一张多半没用。"),
}

var (
	promptImageFailureAttachedSpec = imageFailureSpec("attached", "识图失败但原图仍在", "识图失败、但原图仍附在这一轮请求里时追加：能看到就正常回应。", "原图仍附在本条消息里：你能看到就当普通图片正常回应，不必提这段说明。")
	// 以前这里要求「一句日常说法带过，不解释原因」，结果线上只会说「图没加载出来，
	// 你重发一下」，谁也看不出是识图出了什么问题。现在要求照实报错。
	promptImageFailureGuidanceSpec = imageFailureSpec("guidance", "识图失败时怎么说", "有图识别失败时附在说明末尾：看不到就照实说明是识图哪里出了问题，不说对方没发图。",
		"确实看不到时，把上面的情况照实告诉对方：是识图出了什么问题，有具体原因就把模型名或报错原文带上，一两句话说清。"+
			"不要含糊成「图没加载出来」「这张图没打开」；上面没说可以重发时，不要让对方重发。\n"+
			"对方确实发了图：任何情况下都不要说没收到图片，也不要说对方没发。")
)

func imageFailureSpec(key, title, usage, text string) *PromptSpec {
	return registerPrompt(PromptSpec{Key: "media.image_failure." + key, Group: PromptGroupMedia, Title: title, Usage: usage, Default: text})
}

func llmMessageHasImagePart(message llm.Message) bool {
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL && strings.TrimSpace(part.ImageURL) != "" {
			return true
		}
	}
	return false
}
