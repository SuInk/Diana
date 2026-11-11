package assistant

import (
	"context"
	"strings"
	"testing"
)

// 同一个人追发并进这一轮后，回复引用挂在最晚那条上，不再挂在最早的触发消息上
// （生产 2026-11-03：「我听到了」之后连发四条点评，回复讲点评，引用的却是「我听到了」）。
func replyAnchorTestRuntime(mode ReplyDecorationMode) (*Runtime, *recordingChannel) {
	channel := &recordingChannel{}
	p := &topicTestProvider{result: `{"relation":"supplement","confidence":0.99}`}
	r := NewRuntime(BotConfig{BotAccount: "42", ReplyReferenceMode: mode}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return p, nil })
	return r, channel
}

func mergedReplyTurn(t *testing.T, r *Runtime) (context.Context, MessageEvent, MessageEvent) {
	t.Helper()
	root := directedGroupMessage("10001", "user", "我听到了")
	root.Time = 100
	ctx, finish := r.beginDirectReply(context.Background(), root)
	t.Cleanup(finish)
	follow := directedGroupMessage("10005", "user", "opus 那版最难听")
	follow.Time = 110
	if _, merged := r.mergeIntoActiveDirectReply(ctx, follow, follow.RawMessage); !merged {
		t.Fatal("follow-up not merged")
	}
	return ctx, root, follow
}

func TestReplyReferenceOnQuotesLatestMergedFollowUp(t *testing.T) {
	withFastSendTiming(t)
	r, channel := replyAnchorTestRuntime(ReplyDecorationOn)
	ctx, root, follow := mergedReplyTurn(t, r)

	if err := r.send(ctx, root, "opus 这波编曲翻车了"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 || channel.sent[0].ReplyMessageID != follow.MessageID {
		t.Fatalf("reply did not quote the latest merged message: %#v", channel.sent)
	}
}

func TestReplyReferenceSkipsRecalledFollowUp(t *testing.T) {
	withFastSendTiming(t)
	r, channel := replyAnchorTestRuntime(ReplyDecorationOn)
	ctx, root, follow := mergedReplyTurn(t, r)
	r.noteRecalledInbound(follow)

	if err := r.send(ctx, root, "收到"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 || channel.sent[0].ReplyMessageID != root.MessageID {
		t.Fatalf("recalled follow-up should fall back to the trigger: %#v", channel.sent)
	}
}

// auto 档把触发消息和并进来的补充都列成候选，让模型挑回复第一句接的那条。
func TestReplyDecorationPromptOffersEveryMergedMessage(t *testing.T) {
	r, _ := replyAnchorTestRuntime(ReplyDecorationAuto)
	ctx, root, follow := mergedReplyTurn(t, r)
	root.replyQuoteCandidates = r.replyQuoteCandidates(ctx, root)

	prompt := replyDecorationPrompt(r.effectiveConfigForEvent(root), root, nil)
	rootAt := strings.Index(prompt, replyMarkerFor(root.MessageID))
	followAt := strings.Index(prompt, replyMarkerFor(follow.MessageID))
	if rootAt < 0 || followAt < 0 || rootAt > followAt {
		t.Fatalf("auto prompt should list the trigger and the follow-up oldest first:\n%s", prompt)
	}
	if !strings.Contains(prompt, "opus 那版最难听」") || !strings.Contains(prompt, "第一句接的那一条") {
		t.Fatalf("candidates should carry a snippet and the pick rule:\n%s", prompt)
	}
}

// 没有补充时仍是原来那句：指向当前这条消息。
func TestReplyDecorationPromptSingleCandidateUnchanged(t *testing.T) {
	r, _ := replyAnchorTestRuntime(ReplyDecorationAuto)
	root := directedGroupMessage("10001", "user", "我听到了")
	root.replyQuoteCandidates = r.replyQuoteCandidates(context.Background(), root)

	prompt := replyDecorationPrompt(r.effectiveConfigForEvent(root), root, nil)
	if !strings.Contains(prompt, replyMarkerFor(root.MessageID)+" 来指向当前这条消息") || strings.Contains(prompt, "候选按时间") {
		t.Fatalf("single-message turn should keep the plain prompt:\n%s", prompt)
	}
}

// 模型照候选写了补充那条的 ID：它就在这一轮里，不必等历史落库也算数。
func TestModelMarkerForMergedFollowUpIsKept(t *testing.T) {
	withFastSendTiming(t)
	r, channel := replyAnchorTestRuntime(ReplyDecorationAuto)
	ctx, root, follow := mergedReplyTurn(t, r)

	if err := r.send(ctx, root, replyMarkerFor(follow.MessageID)+"opus 这波编曲翻车了"); err != nil {
		t.Fatalf("send() error = %v", err)
	}
	if len(channel.sent) != 1 || channel.sent[0].ReplyMessageID != follow.MessageID {
		t.Fatalf("model marker for the merged follow-up was dropped: %#v", channel.sent)
	}
}

// 补的是一张贴纸：那只是个反应，不进引用候选，on 档自动引用也不挂在它上面
// （生产 2026-11-12：「欲火焚身」之后甩了张贴纸，回复「这还差不多」挂在了贴纸下面）。
func TestReplyQuoteCandidatesSkipStickerOnlyFollowUp(t *testing.T) {
	r, _ := replyAnchorTestRuntime(ReplyDecorationAuto)
	ctx, root, follow := mergedReplyTurn(t, r)
	sticker := MessageEvent{
		Kind: EventKindGroup, GroupID: root.GroupID, UserID: root.UserID, MessageID: "10007", Time: 120,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"file_id": "s1", "sub_type": "telegram_sticker", "summary": "😆"}}},
	}
	if _, merged := r.mergeIntoActiveDirectReply(ctx, sticker, ""); !merged {
		t.Fatal("sticker follow-up not merged")
	}
	candidates := r.replyQuoteCandidates(ctx, root)
	if len(candidates) != 2 || candidates[0].MessageID != root.MessageID || candidates[1].MessageID != follow.MessageID {
		t.Fatalf("candidates = %#v, want trigger and worded follow-up only", candidates)
	}
	if got := r.replyAnchorEvent(ctx, root).MessageID; got != follow.MessageID {
		t.Fatalf("anchor = %q, want the latest worded follow-up %q", got, follow.MessageID)
	}
}
