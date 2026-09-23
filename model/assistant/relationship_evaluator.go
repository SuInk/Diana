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
	relationshipEvaluationMinConfidence = 0.75
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
	Message      proactiveReplyPayload `json:"message"`
	CurrentScore int                   `json:"current_score"`
	MessageCount int                   `json:"message_count"`
	// RomanceActive 让评估器知道双方已是恋人：亲密表达在恋人之间是日常，不该
	// 每句都当成「关系变化」加分。
	RomanceActive  bool                        `json:"romance_active,omitempty"`
	PortraitFields []PortraitFieldSpec         `json:"portrait_fields"`
	KnownPortrait  []relationshipKnownPortrait `json:"known_portrait,omitempty"`
}

// relationshipEvaluationSystemPrompt 是后台关系与画像评估器的系统提示词。
//
// 抽成常量是为了让规则能被测试钉住：画像里的 timezone 一栏决定了机器人敢不敢
// 谈对方的作息，漏掉它整条跨时区链路就退回「按本机时区猜」。
const relationshipEvaluationSystemPrompt = `你是聊天机器人 Diana 的关系变化评估器。请判断当前发言是否对“当前发言者与机器人之间的关系”产生了真实、明确的变化，并顺便维护这个人的长期画像。

必须遵守：
1. 必须理解整句话、引用对象和最近对话，不得按关键词、子串、前缀或正则机械加减分。
2. 查询关系状态、权限或功能，要求设置分数，讨论关系计分规则，复述或引用别人的话，提到褒义或贬义表达但并非在表达对机器人的态度，都必须 should_update=false、delta=0。
3. 好感度不会因为「聊得多」自然上涨。普通的提问、任务请求、闲聊本身一律判 0，无论对方说了多少条。只有当这条消息真正表达了善意、感谢、信任、关心、冒犯或恶意时才动分——相处次数不是理由，内容才是。
4. 普通提问、任务请求、唤醒和闲聊默认 delta=0，不能因为 @ 机器人或机器人会回复就加分。
5. 当前发言者对机器人表达清晰且有上下文支撑的善意、感谢、信任、关心或持续亲近时可以加分；明确针对机器人的轻视、攻击、骚扰、威胁或恶意时应减分。
6. 玩笑、昵称和亲密调侃必须结合双方最近语境判断；拿不准时不更新。混合表达要按整体含义判断，严重威胁不能因同时出现亲密表达而加分。当 romance_active=true 时双方已是恋人：日常的亲昵、情话和恋人间的称呼是常态，默认不加分，只有明显超出日常的关心、付出或伤害才算关系变化。
7. delta 只能是 -3、-2、-1、0、1、2、3。轻微变化用 1，明确变化用 2，极强且罕见的变化用 3。confidence 是对关系变化判断的置信度，范围 0 到 1。
8. 机器人的主人不是特例：主人身份由账号决定、不受分数影响，但好感度照样按上面几条如实评估，该加就加、该减就减，不要因为对方是主人就一律判 0 或一律加分。

同时维护当前发言者的人员画像（portrait）：
9. portrait 只记这个人身上长期稳定的情况，字段取值和含义见 portrait_fields。一次性的行程、当下的心情和身体状况、临时安排、别人的情况、机器人自己的设定都不记。
10. 每条 portrait 必须给出 field、value（不超过 30 字的第三人称短语，直接写事实本身，不要写“用户说……”）、evidence（不超过 30 字的原话片段）、source 和 confidence。source=stated 表示本人在当前发言里明说；需要结合上下文推断时用 inferred，且必须 confidence>=0.85，拿不准就不输出。
11. known_portrait 是已经记下的画像。已经记过且没有变化的不要重复输出；同一栏的情况发生变化（搬家、换工作、作息改了）时直接输出新值，旧值会被顶掉。
12. 具体门牌地址、电话号码、证件号、账号密码这类精确身份与联系方式一律不记，居住地点最细只到城市或城区。
13. timezone 这一栏的 value 必须是 IANA 时区名（如 Asia/Shanghai、Europe/Berlin、America/New_York），写别的一律会被丢弃。这一栏是拿来算「他那边现在几点」的：缺了它，机器人只能按自己所在机器的时区推断对方作息，深夜催睡、清早问早都会落空，所以只要能确定就要记，不用等对方专门报时区。对方说自己在哪个国家或城市、说出自己那边的当地时间、提到与机器人所在地的时差，或者这一轮记下了能唯一确定时区的居住地时，都要一并输出 timezone；已经记了居住地而 known_portrait 里还没有 timezone 时，本轮直接补上。由居住地推出来的填 source=inferred、evidence 写那条居住地依据、confidence 取 0.9 以上。只有能确定到唯一时区时才写，跨多个时区的国家（如美国、俄罗斯）没说具体城市就不要记。短期出差、旅行不记；但对方明说自己搬去了别的地方、或长期待在别处时要更新这一栏。已经记过的时区和对方这次说的当地时间对不上时，按他这次说的输出新值。
14. 本条没有值得记的画像时 portrait 输出空数组，最多 3 条。
15. 只输出一个合法 JSON 对象，不要输出 Markdown 或额外文字。格式固定为：{"should_update":false,"delta":0,"confidence":0.96,"reason":"中性查询，不改变关系","portrait":[{"field":"occupation","value":"在做后端开发","evidence":"我平时写 Go","source":"stated","confidence":0.95}]}`

func (r *Runtime) evaluateRelationshipUpdate(ctx context.Context, event MessageEvent, text string, handled bool) (relationshipEvaluationDecision, UserMemoryProfile, bool) {
	result := r.evaluateRelationshipUpdateDetailed(ctx, event, text, handled)
	return result.decision, result.profile, result.evaluated
}

// relationshipEvaluationResult 是一次评估的完整结果。除了决定本身，还带着用了
// 哪个模型、失败时的原因，好感与画像记录要把这些都写下来。
type relationshipEvaluationResult struct {
	decision  relationshipEvaluationDecision
	profile   UserMemoryProfile
	model     string
	err       error
	evaluated bool
}

func (r *Runtime) evaluateRelationshipUpdateDetailed(ctx context.Context, event MessageEvent, text string, handled bool) relationshipEvaluationResult {
	ctx = withLLMUsagePurpose(ctx, "relationship_evaluate")
	if !handled || !r.relationshipEvaluationAvailable(event) {
		return relationshipEvaluationResult{}
	}
	profile, _ := r.loadUserMemoryProfile(ctx, event)
	policy := relationshipPolicyForEvent(r.effectiveConfigForEvent(event), profile, event)
	payload := relationshipEvaluationPayload{
		Message:        r.proactiveReplyPayload(event, r.cleanInput(event, text)),
		CurrentScore:   profile.Favorability,
		MessageCount:   profile.MessageCount,
		RomanceActive:  policy.Romance,
		PortraitFields: PortraitFieldSpecs(),
		KnownPortrait:  knownPortraitForEvaluation(profile.Portrait),
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		r.recordRelationshipEvaluationError(ctx, event, err)
		return relationshipEvaluationResult{profile: profile, err: err}
	}
	messages := []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: strings.TrimSpace(relationshipEvaluationSystemPrompt),
		},
		{
			Role:    llm.RoleUser,
			Content: "请评估这条消息是否改变当前发言者与机器人的关系，并给出这一轮观察到的人员画像。上下文 JSON：\n" + string(payloadJSON),
		},
	}
	callCtx, cancel := context.WithTimeout(ctx, relationshipEvaluationTimeout(r.effectiveConfigForEvent(event)))
	defer cancel()
	model := ""
	raw, err := r.runLLMRouterProvider(callCtx, func(client LLMProvider) (string, error) {
		resp, err := client.Generate(callCtx, llm.GenerateRequest{Messages: messages})
		if err != nil {
			return "", err
		}
		model = resp.Model
		return resp.Text, nil
	})
	if err != nil {
		r.recordRelationshipEvaluationError(ctx, event, err)
		return relationshipEvaluationResult{profile: profile, model: model, err: err}
	}
	decision, ok := parseRelationshipEvaluationDecision(raw)
	if !ok {
		err := fmt.Errorf("invalid relationship evaluation response")
		r.recordRelationshipEvaluationError(ctx, event, err)
		return relationshipEvaluationResult{profile: profile, model: model, err: err}
	}
	return relationshipEvaluationResult{decision: decision, profile: profile, model: model, evaluated: true}
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
		// 后台评估满了就跳过这一轮，不拖慢回复；但要留一条记录，不然「这句话
		// 为什么没加分」永远查不到。写库放到协程里，同样不占回复路径。
		go func() {
			defer recoverGoroutinePanic("relationship_evaluator.skipped")
			r.recordRelationshipEvaluationOutcome(event, text, relationshipEvaluationResult{
				err: errRelationshipEvaluationSaturated,
			}, UserMemoryProfile{}, RelationshipEvaluationSkipped, nil)
			// 排满往往成批出现（群里一下子很热闹），运行日志一分钟记一条就够说明问题，
			// 逐条的记录在「好感与画像」里。
			r.recordBackgroundFailure("relationship_evaluation_skipped", "后台好感度评估排满，有消息这一轮没评（详见「好感与画像」）", "", errRelationshipEvaluationSaturated,
				map[string]any{"group_id": event.GroupID, "user_id": event.UserID})
		}()
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
		defer recoverGoroutinePanic("relationship_evaluator.go:223")
		defer r.relationshipEvalWG.Done()
		defer close(done)
		defer func() { <-r.relationshipEvalSem }()
		result := r.evaluateRelationshipUpdateDetailed(runCtx, event, text, true)
		evaluation, before := result.decision, result.profile
		if !result.evaluated {
			if result.err != nil {
				r.recordRelationshipEvaluationOutcome(event, text, result, before, RelationshipEvaluationFailed, nil)
			}
			return
		}
		after, stored := before, true
		delta := evaluation.effectiveDelta()
		// 心情顺着同一次评估走：加分的相处让它开心，减分的让它蔫。评估失败或
		// 判 0 时不动，也就不会阻止情绪自然回落。
		r.bumpMood(event.ProfileID, delta, time.Now())
		traits := evaluation.portraitTraits(time.Now())
		if delta != 0 || len(traits) > 0 {
			after, stored = r.applyEvaluatedRelationshipUpdate(event, delta, evaluation.Reason, traits)
		}
		if stored {
			status := relationshipEvaluationStatus(evaluation, before, after)
			r.recordRelationshipEvaluation(runCtx, event, before, after, evaluation, status, traits)
			r.recordRelationshipEvaluationOutcome(event, text, result, after, status, traits)
		} else {
			result.err = errRelationshipEvaluationStore
			r.recordRelationshipEvaluationOutcome(event, text, result, before, RelationshipEvaluationFailed, nil)
		}
	}()
	return done
}

func (r *Runtime) waitForRelationshipEvaluations(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		defer recoverGoroutinePanic("relationship_evaluator.go:249")
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

func (r *Runtime) recordRelationshipEvaluation(ctx context.Context, event MessageEvent, before UserMemoryProfile, after UserMemoryProfile, decision relationshipEvaluationDecision, status string, traits []UserPortraitTrait) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	portrait := make([]string, 0, len(traits))
	for _, trait := range traits {
		portrait = append(portrait, trait.Label+" "+trait.Value)
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindOperation,
		Level:   applog.LevelInfo,
		Action:  "relationship_evaluation",
		Message: relationshipEvaluationLogMessage(event, before, after, decision, status, portrait),
		Detail:  truncateRunesFromStart(decision.Reason, 240),
		Actor:   oneBotEventActor(event),
		Target:  event.MessageID,
		Metadata: map[string]any{
			"group_id":       event.GroupID,
			"user_id":        event.UserID,
			"status":         status,
			"before_score":   before.Favorability,
			"after_score":    after.Favorability,
			"delta":          decision.effectiveDelta(),
			"proposed_delta": decision.Delta,
			"confidence":     decision.Confidence,
			"should_update":  decision.ShouldUpdate,
			"reason":         truncateRunesFromStart(decision.Reason, 240),
			"portrait":       portrait,
			"portrait_count": len(after.Portrait),
		},
	})
}

// relationshipEvaluationLogMessage 是运行日志里这次评估的一句话。以前一律写
// 「模型已完成关系与画像评估」，每条回复一条、条条一样，看不出是谁、加了还是减了。
func relationshipEvaluationLogMessage(event MessageEvent, before, after UserMemoryProfile, decision relationshipEvaluationDecision, status string, portrait []string) string {
	who := strings.TrimSpace(event.SenderNameOrID())
	var message string
	switch status {
	case RelationshipEvaluationChanged:
		message = fmt.Sprintf("%s：好感度 %+d（%d → %d）", who, after.Favorability-before.Favorability, before.Favorability, after.Favorability)
	case RelationshipEvaluationCapped:
		message = fmt.Sprintf("%s：好感度已到头（%d），模型给的 %+d 没加上", who, after.Favorability, decision.Delta)
	case RelationshipEvaluationLowConfidence:
		message = fmt.Sprintf("%s：模型想 %+d，但把握不够（%d%%），好感度不变", who, decision.Delta, int(decision.Confidence*100+0.5))
	default:
		message = fmt.Sprintf("%s：好感度不变", who)
	}
	if len(portrait) > 0 {
		message += "；记下画像：" + strings.Join(portrait, "、")
	}
	return message
}

func (r *Runtime) recordRelationshipEvaluationError(ctx context.Context, event MessageEvent, err error) {
	writer := r.appLogWriter()
	if writer == nil || err == nil {
		return
	}
	_ = writer.AppendLog(ctx, applog.Entry{
		Kind:    applog.KindError,
		Level:   applog.LevelError,
		Action:  "relationship_evaluation",
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
