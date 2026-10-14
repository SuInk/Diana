package assistant

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 群里常有好几台机器人，发给别的机器人的话不该被这台接走。这里是三道确定性的
// 闸，都不调模型：
//
//  1. 没指向本机的轮次不给改设置和群管的工具（applyUndirectedTurnLimits）。主动
//     接话认错了对象，后果最多是多说一句，不能把别人的「/ai off」真执行一遍。
//  2. @了别的机器人、或回复别的机器人的消息，不进答复流程（addressedToOtherBot）。
//  3. 主动接话开始生成之前，先看这条是不是已经被别的机器人接走了（answeredElsewhere）。
//     放在生成之前而不是发送之前：发送前再看，工具早就执行完了。
//
// 「别的机器人」只认结构事实：平台标了机器人、主人标记过，或人员档案攒出来算作 AI。
// 没被认出来的机器人第 2、3 道看不见，只能靠标记补上。

// undirectedSelfModifyMessage 是模型调用被收起的工具时收到的原话，它会照这句转述。
const undirectedSelfModifyMessage = "这条消息没有 @我、回复我或叫我的名字，可能是发给群里别的机器人的，改设置和群管操作这一轮不执行；真要我来做，请 @我 再说一遍"

// undirectedGroupTurn 报告这一轮是不是主动接话：没有 @、引用或称呼，是接话评分放行的。
func undirectedGroupTurn(event MessageEvent) bool {
	return event.Kind == EventKindGroup && event.proactiveReply
}

// applyUndirectedTurnLimits 在主动接话的轮次收起「改机器人设置和群管」那一类工具，
// 范围和安全模式的同一类完全一致（AgentSafeModeRules），查看类操作照常。
func applyUndirectedTurnLimits(event MessageEvent, registry *agent.ToolRegistry) {
	if registry == nil || !undirectedGroupTurn(event) {
		return
	}
	var tools []string
	var operations []agent.DisabledOperation
	for _, rule := range AgentSafeModeRules {
		if rule.Category != safeModeCategorySelfModify {
			continue
		}
		if len(rule.Operations) == 0 {
			tools = append(tools, rule.Tool)
			continue
		}
		operations = append(operations, agent.DisabledOperation{Tool: rule.Tool, Field: rule.Field, Values: rule.Operations})
	}
	registry.DisableTools(undirectedSelfModifyMessage, tools...)
	registry.DisableOperations(undirectedSelfModifyMessage, operations...)
}

// otherBotAccount 返回「这个账号是不是群里别的机器人」的判断，本机自己不算。
func (r *Runtime) otherBotAccount(event MessageEvent) func(string) bool {
	cfg := r.effectiveConfigForEvent(event)
	self := map[string]bool{}
	for _, id := range []string{event.SelfID, cfg.BotAccount} {
		if id = strings.TrimSpace(id); id != "" {
			self[id] = true
		}
	}
	marked := r.profileMarkedBotIDs(event)
	profileID := r.eventProfileID(event)
	return func(id string) bool {
		id = strings.TrimSpace(id)
		if id == "" || self[id] {
			return false
		}
		return slices.Contains(marked, id) || r.userLikelyAI(profileID, id)
	}
}

// addressedToOtherBot 报告这条群消息是不是在叫别的机器人：@了它，或回复的是它的消息，
// 同时没有 @本机、回复本机、叫本机的称呼，也不是本机的插件指令或链接解析。
func (r *Runtime) addressedToOtherBot(event MessageEvent, text string) bool {
	if event.Kind != EventKindGroup || event.ToMe {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	if eventExplicitlyMentionsBot(event, cfg) || eventRepliesToBot(event, cfg) {
		return false
	}
	isBot := r.otherBotAccount(event)
	target := false
	if event.Quoted != nil && isBot(event.Quoted.UserID) {
		target = true
	}
	for _, segment := range event.Segments {
		if segment.Type == "at" && isBot(segment.Data["qq"]) {
			target = true
		}
	}
	return target && !r.shouldHandle(event, text)
}

// otherBotAnswerWindow 是主动接话前等别的机器人的时间，从消息发出算起。生产上别的
// 机器人接指令一般 2～3 秒，接话评分本身就要花掉一两秒，多数时候不用额外等。
const otherBotAnswerWindow = 5 * time.Second

// InboundSessionEventsStore 由入站队列存储实现：取同一会话里在这条之后到达的消息，
// 不管处理到哪一步。会话按顺序处理，后到的消息这时多半还在排队，没进聊天历史。
type InboundSessionEventsStore interface {
	InboundSessionNewerEvents(ctx context.Context, item InboundQueueItem, limit int) ([]MessageEvent, error)
}

// answeredElsewhere 在主动接话开始生成之前，看这条消息是不是已经被别的机器人接走：
// 窗口内别的机器人发了言，或有人（发送者和本机以外）回复了这条消息。
func (r *Runtime) answeredElsewhere(ctx context.Context, event MessageEvent, item *InboundQueueItem) (bool, string) {
	if event.Kind != EventKindGroup || event.ToMe || strings.TrimSpace(event.MessageID) == "" {
		return false, ""
	}
	sent := time.Now()
	if event.Time > 0 {
		sent = time.Unix(event.Time, 0)
	}
	if wait := time.Until(sent.Add(otherBotAnswerWindow)); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ""
		case <-timer.C:
		}
	}
	later := r.laterSessionEvents(ctx, event, item)
	isBot := r.otherBotAccount(event)
	deadline := sent.Add(otherBotAnswerWindow).Unix()
	for _, other := range later {
		if other.MessageID == event.MessageID || other.UserID == event.UserID || r.isSelfMessage(other) {
			continue
		}
		if other.Time > 0 && (other.Time < event.Time || other.Time > deadline) {
			continue
		}
		name := firstNonEmpty(strings.TrimSpace(other.SenderNameOrID()), other.UserID)
		if other.SenderIsBot || isBot(other.UserID) {
			return true, fmt.Sprintf("别的机器人（%s）已经接了这条消息，本机不再接话", name)
		}
		if quotesMessage(other, event.MessageID) {
			return true, fmt.Sprintf("%s 已经回复了这条消息，本机不再接话", name)
		}
	}
	return false, ""
}

// laterSessionEvents 合并两处来源：排队中的入站消息和已经进历史的消息。
func (r *Runtime) laterSessionEvents(ctx context.Context, event MessageEvent, item *InboundQueueItem) []MessageEvent {
	var out []MessageEvent
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundSessionEventsStore)
	r.mu.RUnlock()
	if store != nil && item != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		events, err := store.InboundSessionNewerEvents(checkCtx, *item, 20)
		cancel()
		if err != nil {
			log.Printf("diana answered-elsewhere check failed: %v", err)
		}
		out = append(out, events...)
	}
	// 本轮早先加载的历史是那一刻的快照，要重读才看得到之后进来的消息。
	fresh := event
	fresh.replyHistory, fresh.replyHistoryLoaded = nil, false
	history, _ := r.sessionContextHistory(fresh)
	for _, past := range history {
		if past.Time >= event.Time {
			out = append(out, past)
		}
	}
	return out
}

// quotesMessage 报告 event 是不是在回复 messageID 那条消息。排队中的消息还没补全引用，
// 所以 reply 段也认。
func quotesMessage(event MessageEvent, messageID string) bool {
	if event.Quoted != nil && event.Quoted.MessageID == messageID {
		return true
	}
	for _, segment := range event.Segments {
		if segment.Type == "reply" && segment.Data["id"] == messageID {
			return true
		}
	}
	return false
}
