// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

// renderPreviewMaxRounds 是带检查的预览轮数上限，到了就照实发出去。回放里检查每轮都
// 能再挑出点毛病，不设上限会一直改下去。
const renderPreviewMaxRounds = 2

// renderPreviewer 让 render、render_media 可以先把图给模型自己看：不看见自己画了什么，
// 就只能一次画完、画歪了也不知道（11-20 线上画「狗熊骑自行车」，写完 SVG 直接渲染发群，
// 脚没踩到踏板）。同一轮回复里两个工具共用一个，检查轮数合在一起算。
type renderPreviewer struct {
	mu     sync.Mutex
	parts  []llm.ContentPart // 附给下一轮模型看的预览图
	rounds int               // 已经带检查预览过几轮
}

// wantPreview 决定这次渲染是不是只给模型看：模型传了 preview=true 才预览，不传照常发出。
func wantPreview(input map[string]any) bool {
	requested, _ := input["preview"].(bool)
	return requested
}

// ToolResultParts 把预览图附给下一轮模型，取一次就清掉。
func (p *renderPreviewer) ToolResultParts(string) []llm.ContentPart {
	p.mu.Lock()
	defer p.mu.Unlock()
	parts := p.parts
	p.parts = nil
	return parts
}

// renderPreviewSchema 是两个渲染工具共用的预览参数。
func renderPreviewSchema() map[string]any {
	return map[string]any{
		"preview": map[string]any{"type": "boolean", "description": "true 时只把渲染结果给你看并请视觉模型挑毛病，不发到会话"},
		"expect":  toolStringParam("预览时写清这张图应该画成什么样（照用户的要求），会另请视觉模型对照着挑毛病"),
	}
}

// renderPreviewDescription 放在渲染工具说明的最前面：按需目录只带说明的前 120 字。
const renderPreviewDescription = `画 SVG 图可先传 preview=true 预览：只给你看，另请视觉模型挑毛病，改好再不带 preview 发出去。`

// renderPreviewReviewQuestion 是预览时请视觉模型挑毛病的问题。模型看自己画的图会直接
// 放行：11-20 回放画「狗熊骑自行车」，两次预览一张熊浮在车上只有一条腿、一张脚落在
// 车轮下面，模型看完都说可以发了。换一个只管挑问题的检查者，逐项点名才改得动。
func renderPreviewReviewQuestion(expect string) string {
	lead := "逐条列出画面里画得不对的地方："
	if expect = strings.TrimSpace(expect); expect != "" {
		lead = "这张图应当是：" + expect + "\n逐条列出画面里和这个要求不符、或者画得不对的地方："
	}
	return lead + "部件没连上或错位，该接触的没接触（比如脚没踩在踏板上、人没坐在座上、手没握住把手），比例失调，互相遮挡，缺了要求里的元素，文字写错。每条写清是哪个部件、错在哪。没有问题就只回答「没有问题」。"
}

// renderPreviewMessage 是预览结果里告诉模型下一步怎么做的话。结构错误要明说重写那部
// 分，只挪原坐标改不动姿势；到了轮数上限就别再改，发出去并说清还差在哪，不能说画好了。
func renderPreviewMessage(review string, round int) string {
	if review == "" {
		return "预览图附在后面，没有发到会话。对照要求检查，有问题改了再预览；满意了不带 preview 再调一次发出去。"
	}
	if strings.TrimSpace(review) == "没有问题" {
		return "预览图附在后面，没有发到会话。检查没挑出问题，不带 preview 再调一次发出去。"
	}
	if round >= renderPreviewMaxRounds {
		return "预览图附在后面，没有发到会话。review 是另一个视觉模型挑出的问题。已经改过 " + strconv.Itoa(round) + " 轮，不要再预览：现在不带 preview 发出去，回复里照实说还有哪些地方没画好，不要说画好了。"
	}
	return "预览图附在后面，没有发到会话。review 是另一个视觉模型挑出的问题，逐条改掉再带 preview 和 expect 预览。" +
		"姿势、位置、部件连接这类结构问题，先定好关键点的坐标（比如脚踏、车座、把手在哪），再按关键点重写画错的那几个部件，不要只把原坐标挪一点。确实没问题了不带 preview 发出去。"
}

// preview 存下预览图并请视觉模型挑毛病，返回检查结果和给模型的话。没写 expect 时只挑
// 画错的地方。
func (p *renderPreviewer) preview(ctx context.Context, runtime *Runtime, event MessageEvent, png []byte, expect string) (review, message string) {
	image := llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), Detail: "high"}
	p.mu.Lock()
	p.parts = []llm.ContentPart{image}
	p.mu.Unlock()
	answer, err := runtime.askImages(ctx, event, []llm.ContentPart{image}, renderPreviewReviewQuestion(expect))
	if err != nil {
		return "", renderPreviewMessage("", 0)
	}
	p.mu.Lock()
	p.rounds++
	round := p.rounds
	p.mu.Unlock()
	return answer, renderPreviewMessage(answer, round)
}
