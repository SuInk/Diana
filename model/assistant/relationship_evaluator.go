// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

const (
	relationshipEvaluationMinConfidence     = 0.75
	naturalInteractionFavorabilityThreshold = 20
	// maxPortraitObservationsPerTurn 限制一条消息能带来几条画像。画像是慢慢攒
	// 的，一次说出三件稳定的事已经很多；不封顶的话模型会把整段话拆成一堆条目。
	maxPortraitObservationsPerTurn = 3
	// portraitInferredMinConfidence 只管推断来的条目：本人明说的照收，推断的必须
	// 很有把握——画像会被当成事实用出去，记错比不记更糟。
	portraitInferredMinConfidence = 0.85
)

type relationshipEvaluationDecision struct {
	ShouldUpdate bool    `json:"should_update"`
	Delta        int     `json:"delta"`
	Confidence   float64 `json:"confidence"`
	Reason       string  `json:"reason"`
	// Portrait 是这一轮观察到的人员画像。它和好感度共用同一次评估调用：两者问
	// 的都是「这个人是谁、我们处得怎么样」，各跑一次模型等于每条消息付两遍钱。
	Portrait []relationshipPortraitObservation `json:"portrait,omitempty"`
}

// relationshipPortraitObservation 是模型给出的一条画像观察，落库前还要经过
// NormalizePortraitTrait 的字段、长度和置信度校验。
type relationshipPortraitObservation struct {
	Field      string  `json:"field"`
	Value      string  `json:"value"`
	Evidence   string  `json:"evidence,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// relationshipKnownPortrait 是喂回给模型的已知画像，只带栏目和值：模型要用它避免
// 重复上报，不需要证据和时间。
type relationshipKnownPortrait struct {
	Field string `json:"field"`
	Label string `json:"label"`
	Value string `json:"value"`
}

func (decision relationshipEvaluationDecision) effectiveDelta() int {
	if !decision.ShouldUpdate || decision.Confidence < relationshipEvaluationMinConfidence {
		return 0
	}
	return decision.Delta
}

// portraitTraits 把模型的观察整理成可入库的画像条目。
func (decision relationshipEvaluationDecision) portraitTraits(now time.Time) []UserPortraitTrait {
	observations := decision.Portrait
	if len(observations) > maxPortraitObservationsPerTurn {
		observations = observations[:maxPortraitObservationsPerTurn]
	}
	traits := make([]UserPortraitTrait, 0, len(observations))
	for _, observation := range observations {
		source := strings.ToLower(strings.TrimSpace(observation.Source))
		if source != PortraitSourceStated && observation.Confidence < portraitInferredMinConfidence {
			continue
		}
		trait, ok := NormalizePortraitTrait(UserPortraitTrait{
			Field:      UserPortraitField(observation.Field),
			Value:      observation.Value,
			Evidence:   observation.Evidence,
			Source:     source,
			Confidence: observation.Confidence,
			UpdatedAt:  now,
		}, now)
		if !ok {
			continue
		}
		traits = append(traits, trait)
	}
	return traits
}

// knownPortraitForEvaluation 压缩已有画像，喂回给模型做去重参考。
func knownPortraitForEvaluation(traits []UserPortraitTrait) []relationshipKnownPortrait {
	known := make([]relationshipKnownPortrait, 0, len(traits))
	for _, trait := range traits {
		known = append(known, relationshipKnownPortrait{
			Field: string(trait.Field),
			Label: trait.Label,
			Value: trait.Value,
		})
	}
	return known
}

type relationshipEvaluationPayload struct {
	Message                       proactiveReplyPayload       `json:"message"`
	CurrentScore                  int                         `json:"current_score"`
	CurrentTier                   string                      `json:"current_tier"`
	MessageCount                  int                         `json:"message_count"`
	NaturalInteractionGainEnabled bool                        `json:"natural_interaction_gain_enabled"`
	NaturalInteractionThreshold   int                         `json:"natural_interaction_threshold"`
	PortraitFields                []PortraitFieldSpec         `json:"portrait_fields"`
	KnownPortrait                 []relationshipKnownPortrait `json:"known_portrait,omitempty"`
}

func (r *Runtime) evaluateRelationshipUpdate(ctx context.Context, event MessageEvent, text string, handled bool) (relationshipEvaluationDecision, UserMemoryProfile, bool) {
	ctx = withLLMUsagePurpose(ctx, "relationship_evaluate")
	if !handled || !r.relationshipEvaluationAvailable(event) {
		return relationshipEvaluationDecision{}, UserMemoryProfile{}, false
	}
	profile, _ := r.loadUserMemoryProfile(ctx, event)
	policy := RelationshipPolicyFor(profile, r.effectiveConfigForEvent(event).OwnerID, event.UserID)
	payload := relationshipEvaluationPayload{
		Message:                       r.proactiveReplyPayload(event, r.cleanInput(event, text)),
		CurrentScore:                  profile.Favorability,
		CurrentTier:                   policy.Name,
		MessageCount:                  profile.MessageCount,
		NaturalInteractionGainEnabled: profile.Favorability < naturalInteractionFavorabilityThreshold,
		NaturalInteractionThreshold:   naturalInteractionFavorabilityThreshold,
		PortraitFields:                PortraitFieldSpecs(),
		KnownPortrait:                 knownPortraitForEvaluation(profile.Portrait),
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		r.recordRelationshipEvaluationError(ctx, event, err)
		return relationshipEvaluationDecision{}, profile, false
	}
	messages := []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: strings.TrimSpace(`你是聊天机器人 Diana 的关系变化评估器。请判断当前发言是否对“当前发言者与机器人之间的关系”产生了真实、明确的变化，并顺便维护这个人的长期画像。

必须遵守：
1. 必须理解整句话、引用对象和最近对话，不得按关键词、子串、前缀或正则机械加减分。
2. 查询关系状态、权限或功能，要求设置分数，讨论关系计分规则，复述或引用别人的话，提到褒义或贬义表达但并非在表达对机器人的态度，都必须 should_update=false、delta=0。
3. 当 natural_interaction_gain_enabled=true 时，当前仍处于自然熟悉阶段。一次真实、有内容且面向机器人的普通闲聊、提问或任务互动，默认应 should_update=true、delta=1，表示相处带来的轻微熟悉；不能仅以“普通提问”“功能请求”或“任务指令”为理由判为 0。纯 @、只有称呼、无实质内容、重复或近似重复消息、刷屏、自动回复、故障反馈，以及明显只为刷分的互动仍为 0。必须理解语义判断，不得用关键词计分。
4. 当 natural_interaction_gain_enabled=false 时，普通提问、任务请求、唤醒和闲聊默认 delta=0，不能因为 @ 机器人或机器人会回复就加分。
5. 无论是否处于自然熟悉阶段，当前发言者对机器人表达清晰且有上下文支撑的善意、感谢、信任、关心或持续亲近时可以加分；明确针对机器人的轻视、攻击、骚扰、威胁或恶意时应减分。
6. 玩笑、昵称和亲密调侃必须结合双方最近语境判断；拿不准时不更新。混合表达要按整体含义判断，严重威胁不能因同时出现亲密表达而加分。
7. delta 只能是 -3、-2、-1、0、1、2、3。自然熟悉阶段的普通互动只能用 1；其他轻微变化用 1，明确变化用 2，极强且罕见的变化用 3。confidence 是对关系变化判断的置信度，范围 0 到 1。
8. 机器人的主人不是特例：他的关系等级由身份决定，不受分数影响，但好感度照样按上面几条如实评估，该加就加、该减就减，不要因为对方是主人就一律判 0 或一律加分。

同时维护当前发言者的人员画像（portrait）：
9. portrait 只记这个人身上长期稳定的情况，字段取值和含义见 portrait_fields。一次性的行程、当下的心情和身体状况、临时安排、别人的情况、机器人自己的设定都不记。
10. 每条 portrait 必须给出 field、value（不超过 30 字的第三人称短语，直接写事实本身，不要写“用户说……”）、evidence（不超过 30 字的原话片段）、source 和 confidence。source=stated 表示本人在当前发言里明说；需要结合上下文推断时用 inferred，且必须 confidence>=0.85，拿不准就不输出。
11. known_portrait 是已经记下的画像。已经记过且没有变化的不要重复输出；同一栏的情况发生变化（搬家、换工作、作息改了）时直接输出新值，旧值会被顶掉。
12. 具体门牌地址、电话号码、证件号、账号密码这类精确身份与联系方式一律不记，居住地点最细只到城市或城区。
13. 本条没有值得记的画像时 portrait 输出空数组，最多 3 条。
14. 只输出一个合法 JSON 对象，不要输出 Markdown 或额外文字。格式固定为：{"should_update":false,"delta":0,"confidence":0.96,"reason":"中性查询，不改变关系","portrait":[{"field":"occupation","value":"在做后端开发","evidence":"我平时写 Go","source":"stated","confidence":0.95}]}`),
		},
		{
			Role:    llm.RoleUser,
			Content: "请评估这条消息是否改变当前发言者与机器人的关系，并给出这一轮观察到的人员画像。上下文 JSON：\n" + string(payloadJSON),
		},
	}
	callCtx, cancel := context.WithTimeout(ctx, relationshipEvaluationTimeout(r.effectiveConfigForEvent(event)))
	defer cancel()
	raw, err := r.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	})
	if err != nil {
		r.recordRelationshipEvaluationError(ctx, event, err)
		return relationshipEvaluationDecision{}, profile, false
	}
	decision, ok := parseRelationshipEvaluationDecision(raw)
	if !ok {
		r.recordRelationshipEvaluationError(ctx, event, fmt.Errorf("invalid relationship evaluation response"))
		return relationshipEvaluationDecision{}, profile, false
	}
	return decision, profile, true
}

// relationshipEvaluationAvailable 判断这一轮要不要跑后台评估。
//
// 主人以前在这里就被整个挡掉，理由是他的好感度反正固定。现在主人的好感度和画像
// 都照常记录——等级仍由身份决定，分数只是如实反映最近处得怎么样——所以不再有
// 身份上的例外。
func (r *Runtime) relationshipEvaluationAvailable(event MessageEvent) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.userMemory != nil && (r.llmFactory != nil || (r.llmCfgFactory != nil && r.llmStore != nil))
}

// enqueueRelationshipEvaluation runs low-priority relationship scoring only
// after a reply was delivered. Saturation skips scoring instead of delaying
// chat replies or building an unbounded background queue.
func (r *Runtime) enqueueRelationshipEvaluation(event MessageEvent, text string) <-chan struct{} {
	done := make(chan struct{})
	if !r.relationshipEvaluationAvailable(event) {
		close(done)
		return done
	}
	select {
	case r.relationshipEvalSem <- struct{}{}:
	default:
		close(done)
		return done
	}
	r.mu.RLock()
	runCtx := r.runCtx
	r.mu.RUnlock()
	if runCtx == nil {
		runCtx = context.Background()
	}
	r.relationshipEvalWG.Add(1)
	go func() {
		defer r.relationshipEvalWG.Done()
		defer close(done)
		defer func() { <-r.relationshipEvalSem }()
		evaluation, before, evaluated := r.evaluateRelationshipUpdate(runCtx, event, text, true)
		if !evaluated {
			return
		}
		after, stored := before, true
		delta := evaluation.effectiveDelta()
		traits := evaluation.portraitTraits(time.Now())
		if delta != 0 || len(traits) > 0 {
			after, stored = r.applyEvaluatedRelationshipUpdate(event, delta, evaluation.Reason, traits)
		}
		if stored {
			r.recordRelationshipEvaluation(runCtx, event, before, after, evaluation)
		}
	}()
	return done
}

func (r *Runtime) waitForRelationshipEvaluations(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		r.relationshipEvalWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func relationshipEvaluationTimeout(cfg BotConfig) time.Duration {
	if cfg.RequestTimeout > 0 && cfg.RequestTimeout < 20*time.Second {
		return cfg.RequestTimeout
	}
	return 20 * time.Second
}

func parseRelationshipEvaluationDecision(raw string) (relationshipEvaluationDecision, bool) {
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return relationshipEvaluationDecision{}, false
	}
	var payload struct {
		ShouldUpdate *bool    `json:"should_update"`
		Delta        *int     `json:"delta"`
		Confidence   *float64 `json:"confidence"`
		Reason       *string  `json:"reason"`
		// portrait 是后加的，老提示词或小模型不给也算合法：漏掉画像只是少记一
		// 条，把整次评估判为无效连好感度都不动了。
		Portrait []relationshipPortraitObservation `json:"portrait"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil || payload.ShouldUpdate == nil || payload.Delta == nil || payload.Confidence == nil || payload.Reason == nil {
		return relationshipEvaluationDecision{}, false
	}
	decision := relationshipEvaluationDecision{
		ShouldUpdate: *payload.ShouldUpdate,
		Delta:        *payload.Delta,
		Confidence:   *payload.Confidence,
		Reason:       strings.TrimSpace(*payload.Reason),
		Portrait:     payload.Portrait,
	}
	if decision.Delta < -3 || decision.Delta > 3 || decision.Confidence < 0 || decision.Confidence > 1 {
		return relationshipEvaluationDecision{}, false
	}
	if !decision.ShouldUpdate {
		decision.Delta = 0
	}
	return decision, true
}

func (r *Runtime) recordRelationshipEvaluation(ctx context.Context, event MessageEvent, before UserMemoryProfile, after UserMemoryProfile, decision relationshipEvaluationDecision) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "chatbot.relationship_evaluation",
		Message: "模型已完成关系与画像评估",
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id":       event.GroupID,
			"user_id":        event.UserID,
			"before_score":   before.Favorability,
			"after_score":    after.Favorability,
			"delta":          decision.effectiveDelta(),
			"confidence":     decision.Confidence,
			"should_update":  decision.ShouldUpdate,
			"reason":         truncateRunesFromStart(decision.Reason, 240),
			"portrait_count": len(after.Portrait),
		},
	})
}

func (r *Runtime) recordRelationshipEvaluationError(ctx context.Context, event MessageEvent, err error) {
	writer := r.appLogWriter()
	if writer == nil || err == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "chatbot.relationship_evaluation",
		Message: "关系与画像语义评估失败，本条不改变好感度和画像",
		Detail:  err.Error(),
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id": event.GroupID,
			"user_id":  event.UserID,
		},
	})
}
