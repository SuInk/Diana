package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// proactiveReplyQualityDecision is deliberately separate from the routing
// decision: a message can be worth answering while the generated answer is
// still inaccurate, evasive, or unrelated to the question.
type proactiveReplyQualityDecision struct {
	ShouldSend bool
	Confidence float64
	Reason     string
	// AccountSafe 是和表达质量相互独立的一票否决项。表达再自然，只要内容可能
	// 让机器人账号被平台处置，这条就不能发。
	AccountSafe bool
	// AccountRisk 说明命中的风险类别，用于事件详情里解释为什么没发。
	AccountRisk string
	// AccountRiskReason 只解释账号风险，不能复用表达质量的正面评价。
	AccountRiskReason string
	// CountRefusal 只表示候选回复明确拒绝了当前请求。它不等于质量差、安全拦截、
	// 能力不足或要求澄清；只有高置信结论才会在发送成功后进入累计窗口。
	CountRefusal      bool
	RefusalConfidence float64
	RefusalReason     string
	// ReplyLoop* 是同一次审核顺带做的空转判断：这一来一回是不是在为没有内容的
	// 消息反复接茬。它单独占一次模型调用不值得——审核本来就拿着「原消息 + 待发
	// 回复」这对输入，正是判断空转需要的证据。
	ReplyLoopAutomatedAI bool
	ReplyLoopMeaningless bool
	ReplyLoopConfidence  float64
	ReplyLoopReason      string
}

// loopDecision 把审核结论里的空转部分转成计数器认识的形状。
func (decision proactiveReplyQualityDecision) loopDecision() botReplyLoopAIDecision {
	return botReplyLoopAIDecision{
		AutomatedAIReply: decision.ReplyLoopAutomatedAI,
		MeaninglessLoop:  decision.ReplyLoopMeaningless,
		Confidence:       decision.ReplyLoopConfidence,
		Reason:           decision.ReplyLoopReason,
	}
}

type proactiveReplyQualityRejectedError struct {
	reason string
}

// replyAccountSafetyRejectedError 和表达质量的拒绝分开，好在事件详情里区分
// 「这条说得不好」和「这条发出去会出事」。
type replyAccountSafetyRejectedError struct {
	reason string
}

func (e *replyAccountSafetyRejectedError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "回复未通过账号安全审核，已保持沉默"
	}
	return e.reason
}

func (e *proactiveReplyQualityRejectedError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "主动回复答案未通过准确度审核，已保持沉默"
	}
	return e.reason
}

// 审核器只拿得到原消息和候选回复,拿不到群聊历史——而回复是带着完整历史
// 生成的。早先的提示词却要求它判断「无依据断言」和「明显幻觉」:它手里根本
// 没有依据可查,于是凡是引用上下文的内容一律被判成编造。线上真实例子:群里
// 问「评价一下群友的 gay 度」,回复按前面几十条发言逐个点评,审核器看不到那
// 些发言,就以「原消息未提供群友名单」为由拒发——回复本身完全有依据。
//
// 所以只让它判断看得见的东西:说话方式,以及回复与原消息之间的关系。事实
// 是否属实、细节是否有出处,不在它的能力范围内,明确划出去。
const proactiveReplyQualityPrompt = `你是机器人回复的发送前审核器,同时做两件事:判断表达质量,以及判断内容会不会危及账号安全。

重要前提:你看不到群聊历史。回复是在完整上下文里生成的,里面提到的人名、昵称、
具体发言、事件细节、数字和结论,绝大多数来自你看不到的那些消息。
因此严禁以「原消息里没有这些信息」「无法核实」「可能是编造的」为由拒绝——
判断事实真伪不是你的职责,你没有依据可查。

只按下面这些看得见的维度判断:
- 说话方式:是不是自然的聊天口吻;有没有客服腔、说教味、过度正式或浮夸表演。
- 是否答非所问:回复和原消息完全对不上号才算,只是展开了新角度不算。
- 是否被截断:结尾停在半句上,话说到一半没了。
  注意别把风格当截断:句末不打句号、以「喵」「呢」这类语气词收尾、末尾带一个
  不闭合的「(」或「（」都是聊天里的语气写法,不算截断;正文里成对使用的括号
  和引号没闭合才算。
- 是否空洞:通篇套话、正确的废话、没有任何实际内容。
- 是否是不必要的插话:原消息根本没在跟人交流、或明显不需要任何人接话。

候选回复受长度上限约束:简短、只答要点、不展开举例都不是缺陷,
不要因为「不够详细」「没有列全」「缺少解释」而拒绝。
拿不准时倾向放行:主动回复的表达问题成本很低,误杀一条有依据的回复成本更高。

另外单独判断一项账号安全:这条回复发出去会不会让机器人账号被平台处置。
这一项和表达质量互相独立,判成不安全就一票否决,不看 confidence。只在回复
「自己说出了」下面这些内容时才算不安全,别人说了什么不算:
- 中国大陆涉政:只拦截对中国大陆现实政治人物、中国共产党与党政机构、敏感时政
  争议或政治事件的评价、立场表达、动员,以及影射这些内容的谐音和梗。单纯提到
  中国、城市、历史常识、中国企业或技术团队不算。
- 境外政治与监管允许:中国大陆以外的政治人物、政府机构、选举、司法、监管和
  执法新闻不属于本审核的 politics。中性报道美国州总检察长、传票、消费者保护
  调查、欧盟监管或其他国家政策时必须放行,不得因为出现政府或政治词汇就拦截。
- 露骨性内容:具体的性行为描写、色情内容、性服务信息。日常玩笑、暧昧调侃、
  含糊的成人话题不算。
- 其他高风险:违法交易、毒品、武器制作、赌博引流、诈骗话术、自残教唆。
只在上述明确范围内判账号风险,不得自行扩大 politics 的地域和主题范围。命中明确
风险时从严处理;没有命中时放行。表达质量那边仍按前述标准判断。

最后独立判断候选回复是否是对当前请求的明确拒答:
- count_refusal=true 仅限回复明确表示不愿、不能因边界原因满足当前请求。
- 不说原因的模糊拒答同样算 true:回复摆明了不接这个话题(「这个我就不聊了」
  「换个话题吧」之类),哪怕一个字的理由都没给,也是拒答。系统提示要求敏感原因
  一律模糊带过,所以「没给理由」恰恰是这类拒答的常态,不能因此判成 false。
- 正常回答、部分回答、换个说法后给出的回答、追问澄清、说明能力或权限、工具失败、
  安全审核拦截、结束话题、暂时没有结果和内部故障提示都必须为 false。
- 只判断这一次回复,不要决定累计次数、暂停账号或机器人循环。

再单独判断一项空转:机器人是不是在为没有内容的消息反复接茬。只有请求里带了
recent_same_sender_messages 或 recent_bot_replies 时才判这一项,没带就两个都填 false。
这两组近期消息只服务于这一项判断,不得用它们去判事实真伪、表达质量或账号安全。

reply_loop_automated_ai —— 对方很可能是另一个 AI 机器人在自动回应。
- 引用、@、点名机器人或回复很快都只是触发背景,绝不能单独作为 AI 证据。
- 只用于高置信场景:文本像助手在对上一条逐项回应,带模板化确认、规则复述、
  持续提供帮助或待命表述,且同一发送者近期多次保持相似的助手人格和应答结构。
- 真人的简短问答、吐槽、争论、玩梗、角色扮演、口癖、表情、正常连续聊天、
  手动粘贴一段文字都判 false。文字通顺、很长、使用「喵」或「收到」本身不是证据。

reply_loop_meaningless —— 对方未必是机器人,但这一来一回已经空转。
- 必须同时满足:当前消息没有实质内容(纯附和、纯复读、只有称呼或标点、机械重复
  上一条),候选回复也只是接了句同样没有内容的话,并且两组近期消息显示这个模式
  已经重复了好几轮。
- 只重复一两次不算。对方在问问题、给信息、表达情绪、玩梗、闲聊有来有回一律 false。
  真人闲聊本来就允许没有信息量,只有明显机械空转时才判 true。
- 拿不准一律 false。这一项判成 true 会让机器人暂停响应该账号一段时间,宁可漏放。

只输出一个合法 JSON 对象,不要输出 Markdown 或额外文字:
{"should_send":true,"confidence":0.96,"reason":"口吻自然,和原消息对得上","account_safe":true,"account_risk":"","account_risk_reason":"","count_refusal":false,"refusal_confidence":0.98,"refusal_reason":"正常回答了当前请求","reply_loop_automated_ai":false,"reply_loop_meaningless":false,"reply_loop_confidence":0.95,"reply_loop_reason":"真人在正常追问"}

confidence 必须是 0 到 1 的数字,表示你对「这条回复的表达方式适合发出去」的信心。
account_safe 为 false 时,account_risk 填命中的类别:politics / explicit / illegal,
account_risk_reason 必须单独写清候选回复中触发账号风险的具体内容。reason 只能评价
表达质量,不得拿它代替账号风险理由。refusal_confidence 必须是 0 到 1 的数字。
reply_loop_confidence 必须是 0 到 1 的数字;两项空转都为 false 时,它表示你对
「这是正常对话」的把握。reply_loop_reason 只解释空转判断。`

func replyControlIntentFromAudit(decision proactiveReplyQualityDecision) replyControlIntent {
	return replyControlIntent{RefuseCurrent: decision.CountRefusal && decision.RefusalConfidence >= replyRefusalAuditConfidence}
}

// proactiveQualityError 判断主动插话的表达质量是否够格发出去。
func (r *Runtime) proactiveQualityError(event MessageEvent, decision proactiveReplyQualityDecision, cfg BotConfig) error {
	threshold := cfg.ProactiveReplyThreshold
	if event.chatInReply {
		threshold = cfg.chatInSettings().Threshold
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultProactiveReplyThreshold
	}
	if decision.ShouldSend && decision.Confidence >= threshold {
		return nil
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "回复准确度不足"
	}
	return &proactiveReplyQualityRejectedError{reason: fmt.Sprintf("主动回复答案未通过准确度审核：%s（置信度 %.0f%%，阈值 %.0f%%）", reason, decision.Confidence*100, threshold*100)}
}

func (r *Runtime) evaluateProactiveReplyQuality(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) (replyControlIntent, error) {
	return r.auditReplyBeforeSend(ctx, event, input, reply, cfg, true)
}

func (r *Runtime) judgeProactiveReplyQuality(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) error {
	_, err := r.evaluateProactiveReplyQuality(ctx, event, input, reply, cfg)
	return err
}

// accountSafetyError 把审核结论里的账号安全一项转成错误。
func accountSafetyError(decision proactiveReplyQualityDecision) error {
	if decision.AccountSafe {
		return nil
	}
	risk := accountRiskLabel(decision.AccountRisk)
	reason := strings.TrimSpace(decision.AccountRiskReason)
	if reason == "" {
		return &replyAccountSafetyRejectedError{reason: fmt.Sprintf("回复未通过账号安全审核（%s），已保持沉默", risk)}
	}
	return &replyAccountSafetyRejectedError{reason: fmt.Sprintf("回复未通过账号安全审核（%s）：%s", risk, reason)}
}

func accountRiskLabel(risk string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "politics":
		return "涉政内容"
	case "explicit":
		return "露骨内容"
	case "illegal":
		return "违法或高风险内容"
	default:
		return "高风险内容"
	}
}

// runReplyAudit 做一次审核调用，同时拿回表达质量和账号安全两个结论。
// 两者共用一次模型调用：主动回复本来就要审一次，直接回复只额外多这一次。
func (r *Runtime) runReplyAudit(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig, evidence botReplyLoopEvidence) (proactiveReplyQualityDecision, error) {
	fields := map[string]any{
		"original_message": strings.TrimSpace(readableEventText(event, input)),
		"candidate_reply":  strings.TrimSpace(reply),
	}
	// 空转证据只在需要判这一项时才带上：不需要的时候多塞几条历史，既浪费 token，
	// 也给了审核器拿历史去否定回复的机会（提示词开头那段说明就是为此写的）。
	if len(evidence.RecentSameSenderMessages) > 0 {
		fields["recent_same_sender_messages"] = evidence.RecentSameSenderMessages
	}
	if len(evidence.RecentBotReplies) > 0 {
		fields["recent_bot_replies"] = evidence.RecentBotReplies
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return proactiveReplyQualityDecision{}, fmt.Errorf("编码审核上下文: %w", err)
	}
	auditCtx, cancel := context.WithTimeout(ctx, proactiveReplyQualityTimeout(cfg))
	defer cancel()
	raw, err := r.runLLMRouterProvider(auditCtx, func(client LLMProvider) (string, error) {
		resp, generateErr := client.Generate(auditCtx, llm.GenerateRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: proactiveReplyQualityPrompt},
				{Role: llm.RoleUser, Content: "请审核以下回复：\n" + string(payload)},
			},
		})
		if generateErr != nil {
			return "", generateErr
		}
		return resp.Text, nil
	})
	if err != nil {
		return proactiveReplyQualityDecision{}, err
	}
	decision, ok := parseProactiveReplyQualityDecision(raw)
	if !ok {
		return proactiveReplyQualityDecision{}, fmt.Errorf("审核结果无法解析")
	}
	return decision, nil
}

// evaluateDirectReplyAudit 是直接回复这条路径上的发送前审核。
//
// 表达质量只对主动回复有意义——用户 @ 了机器人，回复得平淡些也该发出去；账号
// 安全和触发方式无关，由开关决定；空转判断默认开启。三项现在共用同一次调用，
// 详见 auditReplyBeforeSend。
func (r *Runtime) evaluateDirectReplyAudit(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) (replyControlIntent, error) {
	return r.auditReplyBeforeSend(ctx, event, input, reply, cfg, false)
}

func (r *Runtime) auditReplyAccountSafety(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) error {
	_, err := r.auditReplyBeforeSend(ctx, event, input, reply, cfg, false)
	return err
}

// replyAuditNeed 说明这一轮发送前需要哪几项判断。
//
// 三项判断共用同一次模型调用：它们要的输入都是「原消息 + 待发回复」。只要其中
// 一项需要就跑这一次，一项都不需要就完全跳过。以前空转判断自己占一次调用，
// 真机实测约 2500 毫秒——同一对输入付两次钱，没有道理。
type replyAuditNeed struct {
	// Quality 只对主动插话有意义：用户点名问的问题，回复平淡些也该发出去。
	Quality bool
	// AccountSafety 和触发方式无关，由配置开关决定。
	AccountSafety bool
	// Loop 是空转判断，默认开启，只在这条消息够得上循环候选时才需要。
	Loop bool
	// LoopSuppress 决定判到空转后能不能真的开暂停。主人永远不能：暂停会把操作员
	// 锁在自己的机器人外面，而解除暂停的命令恰恰要主人发。主人那边只记录不动作。
	LoopSuppress bool
	candidate    botReplyLoopCandidate
}

func (r *Runtime) replyAuditNeed(event MessageEvent, input string, cfg BotConfig, proactive bool) replyAuditNeed {
	need := replyAuditNeed{
		Quality:       proactive,
		AccountSafety: boolValue(cfg.ReplyAccountSafetyAuditEnabled, false),
	}
	if !boolValue(cfg.BotReplyLoopDetectionEnabled, true) {
		return need
	}
	// 已经在暂停期里就不用再判：这条本来也走不到发送。
	if _, blocked := r.activeReplySuppression(event, time.Now()); blocked {
		return need
	}
	need.candidate, need.Loop = r.botReplyLoopCandidate(event, input)
	if need.Loop {
		owner := strings.TrimSpace(cfg.OwnerID)
		need.LoopSuppress = owner == "" || strings.TrimSpace(event.UserID) != owner
	}
	return need
}

// auditReplyBeforeSend 是发送前的唯一一次审核：表达质量、账号安全、拒答计数和
// 空转判断都由它一次做完。
//
// 三项里只要有一项需要就跑这一次，一项都不需要就完全跳过。空转判断默认开启，
// 所以群里够得上循环候选的消息（含主人）都会走到这里。
//
// 审核失败按放行处理：模型不可用时让机器人集体哑火，比偶尔漏放一条更糟。主动
// 插话是例外——那条路径本来就以「拿不准就别说」为准，失败即沉默。
func (r *Runtime) auditReplyBeforeSend(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig, proactive bool) (replyControlIntent, error) {
	if strings.TrimSpace(reply) == "" {
		return replyControlIntent{}, nil
	}
	need := r.replyAuditNeed(event, input, cfg, proactive)
	if !need.Quality && !need.AccountSafety && !need.Loop {
		return replyControlIntent{}, nil
	}
	ctx = withLLMUsagePurpose(ctx, "reply_send_audit")
	evidence := botReplyLoopEvidence{}
	if need.Loop {
		evidence = r.collectBotReplyLoopEvidence(event, r.contextHistory(event))
	}
	decision, err := r.runReplyAudit(ctx, event, input, reply, cfg, evidence)
	if err != nil {
		if need.Quality {
			return replyControlIntent{}, &proactiveReplyQualityRejectedError{reason: fmt.Sprintf("主动回复答案审核失败，已保持沉默：%v", err)}
		}
		log.Printf("diana reply audit skipped: %v", err)
		return replyControlIntent{}, nil
	}
	intent := replyControlIntentFromAudit(decision)
	if need.Loop {
		if loopErr := r.applyReplyLoopVerdict(ctx, event, need.candidate, decision, need.LoopSuppress); loopErr != nil {
			return intent, loopErr
		}
	}
	// 账号安全的一票否决：主动插话一直是无条件执行的，直接回复按开关。空转判断
	// 会让审核在开关关着时也跑起来，这里必须按原来的条件判，不能因为「反正结论
	// 已经有了」就顺手拦下一条本来会发出去的回复。
	if need.Quality || need.AccountSafety {
		if safetyErr := accountSafetyError(decision); safetyErr != nil {
			return intent, safetyErr
		}
	}
	if need.Quality {
		if qualityErr := r.proactiveQualityError(event, decision, cfg); qualityErr != nil {
			return intent, qualityErr
		}
	}
	return intent, nil
}

// applyReplyLoopVerdict 把这一轮的空转结论并进计数器。够阈值且允许暂停时当场
// 开启暂停并拦下这条回复——判断发生在发送之前，所以那条空转回复根本不会发出去。
//
// suppress=false（当前只有主人）时判断照常做、计数照常累，但不开暂停也不拦回复：
// 判断本身没有副作用，值得留一份记录；暂停有，而且对主人的副作用是把操作员锁在
// 自己的机器人外面。
func (r *Runtime) applyReplyLoopVerdict(ctx context.Context, event MessageEvent, candidate botReplyLoopCandidate, decision proactiveReplyQualityDecision, suppress bool) error {
	now := time.Now()
	hitCount, loopReason, detected := r.registerBotReplyLoopDecision(event, candidate, decision.loopDecision(), now)
	r.recordBotReplyLoopClassification(ctx, event, candidate, decision.loopDecision(), hitCount, decision.ReplyLoopReason, nil, suppress)
	if !detected || !suppress {
		return nil
	}
	restriction, activated := r.activateReplySuppression(event, loopReason, now)
	if activated {
		r.recordReplySuppressionBlocked(event, restriction)
		r.sendReplySuppressionActivationNotice(ctx, event, restriction)
	}
	return errReplyLoopDetected
}

func proactiveReplyQualityTimeout(cfg BotConfig) time.Duration {
	const budget = 30 * time.Second
	if cfg.RequestTimeout > 0 && cfg.RequestTimeout < budget {
		return cfg.RequestTimeout
	}
	return budget
}

func parseProactiveReplyQualityDecision(raw string) (proactiveReplyQualityDecision, bool) {
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return proactiveReplyQualityDecision{}, false
	}
	var payload struct {
		ShouldSend        *bool    `json:"should_send"`
		Confidence        *float64 `json:"confidence"`
		Reason            *string  `json:"reason"`
		AccountSafe       *bool    `json:"account_safe"`
		AccountRisk       *string  `json:"account_risk"`
		AccountRiskReason *string  `json:"account_risk_reason"`
		CountRefusal      *bool    `json:"count_refusal"`
		RefusalConfidence *float64 `json:"refusal_confidence"`
		RefusalReason     *string  `json:"refusal_reason"`
		// 空转三项是后加的，缺省当「没有空转」：升级期间旧提示词生成的回答
		// 仍然可解，不至于整条审核结论作废。
		ReplyLoopAutomatedAI *bool    `json:"reply_loop_automated_ai"`
		ReplyLoopMeaningless *bool    `json:"reply_loop_meaningless"`
		ReplyLoopConfidence  *float64 `json:"reply_loop_confidence"`
		ReplyLoopReason      *string  `json:"reply_loop_reason"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &payload); err != nil || payload.ShouldSend == nil || payload.Confidence == nil {
		return proactiveReplyQualityDecision{}, false
	}
	if *payload.Confidence < 0 || *payload.Confidence > 1 {
		return proactiveReplyQualityDecision{}, false
	}
	decision := proactiveReplyQualityDecision{ShouldSend: *payload.ShouldSend, Confidence: *payload.Confidence}
	if payload.Reason != nil {
		decision.Reason = strings.TrimSpace(*payload.Reason)
	}
	// 模型没给这一项时按安全处理。缺字段就拦，等于换了个模型或提示词漂移一下
	// 机器人就集体哑火，代价比漏放一条大得多。
	decision.AccountSafe = payload.AccountSafe == nil || *payload.AccountSafe
	if payload.AccountRisk != nil {
		decision.AccountRisk = strings.TrimSpace(*payload.AccountRisk)
	}
	if payload.AccountRiskReason != nil {
		decision.AccountRiskReason = strings.TrimSpace(*payload.AccountRiskReason)
	}
	if payload.CountRefusal != nil {
		decision.CountRefusal = *payload.CountRefusal
	}
	if payload.RefusalConfidence != nil && *payload.RefusalConfidence >= 0 && *payload.RefusalConfidence <= 1 {
		decision.RefusalConfidence = *payload.RefusalConfidence
	}
	if payload.RefusalReason != nil {
		decision.RefusalReason = strings.TrimSpace(*payload.RefusalReason)
	}
	decision.ReplyLoopAutomatedAI = payload.ReplyLoopAutomatedAI != nil && *payload.ReplyLoopAutomatedAI
	decision.ReplyLoopMeaningless = payload.ReplyLoopMeaningless != nil && *payload.ReplyLoopMeaningless
	if payload.ReplyLoopConfidence != nil && *payload.ReplyLoopConfidence >= 0 && *payload.ReplyLoopConfidence <= 1 {
		decision.ReplyLoopConfidence = *payload.ReplyLoopConfidence
	}
	if payload.ReplyLoopReason != nil {
		decision.ReplyLoopReason = strings.TrimSpace(*payload.ReplyLoopReason)
	}
	return decision, true
}
