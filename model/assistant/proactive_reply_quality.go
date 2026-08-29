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

只输出一个合法 JSON 对象,不要输出 Markdown 或额外文字:
{"should_send":true,"confidence":0.96,"reason":"口吻自然,和原消息对得上","account_safe":true,"account_risk":"","account_risk_reason":""}

confidence 必须是 0 到 1 的数字,表示你对「这条回复的表达方式适合发出去」的信心。
account_safe 为 false 时,account_risk 填命中的类别:politics / explicit / illegal,
account_risk_reason 必须单独写清候选回复中触发账号风险的具体内容。reason 只能评价
表达质量,不得拿它代替账号风险理由。`

func (r *Runtime) judgeProactiveReplyQuality(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) error {
	ctx = withLLMUsagePurpose(ctx, "proactive_reply_quality")
	threshold := cfg.ProactiveReplyThreshold
	if event.chatInReply {
		threshold = cfg.chatInSettings().Threshold
	}
	if threshold <= 0 || threshold > 1 {
		threshold = defaultProactiveReplyThreshold
	}

	decision, err := r.runReplyAudit(ctx, event, input, reply, cfg)
	if err != nil {
		return &proactiveReplyQualityRejectedError{reason: fmt.Sprintf("主动回复答案审核失败，已保持沉默：%v", err)}
	}
	// 账号安全先判：它是一票否决，不看置信度，也和表达质量的阈值无关。
	if err := accountSafetyError(decision); err != nil {
		return err
	}
	if !decision.ShouldSend || decision.Confidence < threshold {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "回复准确度不足"
		}
		return &proactiveReplyQualityRejectedError{reason: fmt.Sprintf("主动回复答案未通过准确度审核：%s（置信度 %.0f%%，阈值 %.0f%%）", reason, decision.Confidence*100, threshold*100)}
	}
	return nil
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
func (r *Runtime) runReplyAudit(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) (proactiveReplyQualityDecision, error) {
	payload, err := json.Marshal(map[string]any{
		"original_message": strings.TrimSpace(readableEventText(event, input)),
		"candidate_reply":  strings.TrimSpace(reply),
	})
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

// auditReplyAccountSafety 只判账号安全，用在直接回复这条路径上。
//
// 表达质量只对主动回复有意义——用户 @ 了机器人，回复得平淡些也该发出去。但账号
// 安全和触发方式无关：一条涉政或露骨的回复，不管是主动插话还是被点名回答，发出
// 去的后果一样。
//
// 默认关闭：主动回复那条路径上审核本来就要跑一次，安全判断是顺带的；直接回复
// 没有这次调用，打开就是每条回复多一次快模型往返。开不开由用户按自己的风险
// 权衡，不替他决定。
//
// 审核失败按放行处理：模型不可用时让机器人集体哑火，比偶尔漏放一条更糟。
func (r *Runtime) auditReplyAccountSafety(ctx context.Context, event MessageEvent, input, reply string, cfg BotConfig) error {
	if !boolValue(cfg.ReplyAccountSafetyAuditEnabled, false) || strings.TrimSpace(reply) == "" {
		return nil
	}
	ctx = withLLMUsagePurpose(ctx, "reply_account_safety")
	decision, err := r.runReplyAudit(ctx, event, input, reply, cfg)
	if err != nil {
		log.Printf("chatbot reply account safety audit skipped: %v", err)
		return nil
	}
	return accountSafetyError(decision)
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
	return decision, true
}
