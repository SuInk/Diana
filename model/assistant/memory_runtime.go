// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/SuInk/diana/model/llm"
)

const (
	memoryWorkerCount       = 2
	memoryPollInterval      = 750 * time.Millisecond
	memoryLeaseDuration     = 3 * time.Minute
	memoryExtractionTimeout = 60 * time.Second
	memoryMaxAttempts       = 8
	memorySummaryMaxEvents  = 100
	// memoryThreadRetentionDays 让冷会话的线程便签自然过期：一周没人说话，
	// 「当前进行到哪」这件事本身就不成立了，不该继续常驻注入。
	memoryThreadRetentionDays = 7
	memorySummaryRollupSize   = 12
	// memoryEventBatchMax 是一次记忆门控最多带几条新消息。
	//
	// 攒批的动机是缓存：门控的固定前缀约 600 token，够不到供应商 1024 token 的
	// 最低可缓存长度，所以每次调用都是全价——实测这条链路缓存命中率恒为 0%。
	// 一条消息一次调用时，system、existing_memories、recent_messages 会被重复
	// 付费 N 遍；攒成一次就只付一遍。
	memoryEventBatchMax = 6
)

// MemoryEventJobDelay 是事件记忆任务入队后的等待时间，用来把同一会话同一发言者
// 的连续消息攒进一次门控调用。
//
// 代价是长期记忆晚成形这一会儿。可以接受：这段时间里那几条消息本来就还在最近
// 历史里，回复时看得到；长期记忆负责的是跨会话那部分，不差这几十秒。
const MemoryEventJobDelay = 45 * time.Second

// memoryGateSystemPrompt 是记忆门控的固定前缀。单条和攒批共用同一份，缓存才有
// 可能在两种形状之间互相命中。
const memoryGateSystemPrompt = `你是 Diana 的长期记忆门控器。消息原文已经单独、永久保存在事件日志中；你的任务不是复述聊天，而是只提议值得形成派生长期记忆的内容。

必须遵守：
1. 逐句理解语义、指代、引用和最近上下文，不得用关键词、前缀、子串或正则机械判断。
2. 只记录关于当前发言者的稳定事实、持续偏好、长期交互要求，或未来仍有明显价值的重要情景。普通问题、一次性任务、寒暄、玩梗、短暂情绪、媒体占位、链接、机器人回答、未经证实的第三方传闻都不记。当前任务里的格式要求、分析角度、修改意见、验收条件和临时约束即使表达得很明确，也只属于工作记忆；除非语义明确要求今后跨话题默认遵循或长期记住，否则绝不能写成 instruction。
3. 提醒、订阅、待办已有独立任务系统，不要重复写成长期记忆。好感度也由独立关系系统维护。
4. source_type=explicit 只用于当前发言者直接明确陈述；需要结合上下文推断时用 inferred。inferred 必须 confidence>=0.90，拿不准就不输出。
5. 每条记忆必须有稳定且颗粒度足够细的 key，例如 preference.food.spicy、profile.pet.cat、instruction.reply_style。更新、否定或要求忘记已有记忆时必须复用 existing_memories 中的原 key；不要用笼统的 profile、preference、chat 作为 key。
6. action=upsert 表示新增、确认或更新；action=forget 只用于当前发言者明确要求忘记、撤销或纠正已有记忆。forget 时 content 可以为空。
7. kind 只能是 fact、preference、episode、instruction。instruction 只表示跨会话长期有效的交互规则，不表示本次任务要求。episode 只用于重要的一次性经历，默认 retention_days=90；稳定事实和偏好可以为 0 表示不过期。
8. visibility=session 表示只在当前私聊或群可见；visibility=user 只适用于当前发言者明确陈述、非敏感且跨会话确有帮助的稳定事实/偏好。医疗、心理、财务、身份凭证、住址、联系方式、隐私关系等 sensitive=true，且必须 visibility=session。
9. importance 和 confidence 均为 0 到 1。只有 importance>=0.45 的内容才输出；明确要求“记住”的重要内容可提高 importance，但仍要按真实语义组织，不照抄命令。
10. content 必须写成自包含、无歧义的第三人称事实，保留实体；evidence 是不超过 60 字的最小证据片段。最多输出 5 条。
11. 上下文里给的是 current 还是 current_batch 取决于这一轮攒了几条。给 current_batch 时要把整批按时间顺序当成同一个人连续说的话一起理解：跨条的指代、补充和改口都要接上，同一件事不要拆成多条记忆；每条候选必须用 source_index 标明出自 current_batch 的第几条（从 0 开始），最能支撑这条记忆的那一条。整批合计最多输出 5 条。
12. 调用 memory_submit 提交候选，字段含义以工具参数说明为准；没有候选时提交空数组。只有在不支持工具调用时，才退回输出合法 JSON 对象 {"memories":[...]}，不要 Markdown 或解释。`

var memoryProfileGroups = []string{"memory", "memories", "recall"}

type memoryGatePayload struct {
	Current          *memoryGateEvent   `json:"current,omitempty"`
	CurrentBatch     []memoryGateEvent  `json:"current_batch,omitempty"`
	RecentMessages   []memoryGateEvent  `json:"recent_messages,omitempty"`
	ExistingMemories []memoryGateMemory `json:"existing_memories,omitempty"`
}

type memoryGateEvent struct {
	Time      string `json:"time,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Text      string `json:"text,omitempty"`
	Quoted    string `json:"quoted,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

type memoryGateMemory struct {
	Key        string           `json:"key"`
	Kind       MemoryKind       `json:"kind"`
	Topic      string           `json:"topic"`
	Entity     string           `json:"entity,omitempty"`
	Content    string           `json:"content"`
	Confidence float64          `json:"confidence"`
	Importance float64          `json:"importance"`
	Visibility MemoryVisibility `json:"visibility"`
	Version    int              `json:"version"`
}

type memorySummaryRollup struct {
	Level     string                 `json:"level"`
	TargetKey string                 `json:"target_key"`
	Sources   []memoryGateMemory     `json:"source_summaries"`
	Items     []StructuredMemoryItem `json:"-"`
}

func (r *Runtime) runMemoryCoordinator(ctx context.Context, leaseOwner string, releaseStale bool, done chan struct{}) {
	defer close(done)
	r.mu.RLock()
	store := r.structuredMemory
	r.mu.RUnlock()
	if store == nil {
		return
	}
	if releaseStale {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := store.ReleaseMemoryJobLeases(releaseCtx, ""); err != nil {
			log.Printf("diana memory stale lease recovery failed: %v", err)
		}
		cancel()
	}

	var workers sync.WaitGroup
	for index := 0; index < memoryWorkerCount; index++ {
		workers.Add(1)
		go func() {
			defer recoverGoroutinePanic("memory_runtime.go:90")
			defer workers.Done()
			r.runMemoryWorker(ctx, leaseOwner, store)
		}()
	}
	<-ctx.Done()
	workers.Wait()
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := store.ReleaseMemoryJobLeases(releaseCtx, leaseOwner); err != nil {
		log.Printf("diana memory lease release failed: %v", err)
	}
	cancel()
}

func (r *Runtime) runMemoryWorker(ctx context.Context, leaseOwner string, store StructuredMemoryStore) {
	ticker := time.NewTicker(memoryPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.memoryWake:
		}
		for ctx.Err() == nil {
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			jobs, err := claimMemoryJobs(claimCtx, store, leaseOwner, time.Now().Add(memoryLeaseDuration))
			cancel()
			if err != nil {
				log.Printf("diana memory job claim failed: %v", err)
				break
			}
			if len(jobs) == 0 {
				break
			}
			live := make([]MemoryJob, 0, len(jobs))
			for _, job := range jobs {
				if memoryJobAttemptsExhausted(job.Attempts) {
					log.Printf("diana memory job abandoned after %d attempts: id=%s", job.Attempts-1, job.ID)
					r.recordBackgroundFailure("memory_job_abandoned", memoryJobKindLabel(job.Payload.Kind)+"连续失败，重试次数用完已放弃，这段对话不会再提取记忆",
						"memory_job_abandoned|"+job.Payload.Session, nil,
						map[string]any{"job_id": job.ID, "kind": string(job.Payload.Kind), "session": job.Payload.Session, "attempts": job.Attempts - 1})
					commitCtx, commitCancel := context.WithTimeout(context.Background(), 5*time.Second)
					if err := store.CompleteMemoryJob(commitCtx, job.ID, leaseOwner); err != nil {
						log.Printf("diana memory job state update failed: %v", err)
					}
					commitCancel()
					continue
				}
				live = append(live, job)
			}
			if len(live) == 0 {
				continue
			}
			jobCtx, jobCancel := context.WithTimeout(ctx, memoryExtractionTimeout)
			err = r.processMemoryJobs(jobCtx, store, live)
			jobCancel()
			if err != nil && ctx.Err() == nil {
				r.recordBackgroundFailure("memory_job_failed", "长期记忆提取失败，稍后自动重试", "", err,
					map[string]any{"jobs": len(live), "kind": string(live[0].Payload.Kind), "attempts": live[0].Attempts})
			}

			for _, job := range live {
				commitCtx, commitCancel := context.WithTimeout(context.Background(), 5*time.Second)
				var stateErr error
				if err == nil {
					stateErr = store.CompleteMemoryJob(commitCtx, job.ID, leaseOwner)
				} else {
					retryAt := time.Now().Add(memoryRetryDelay(job.Attempts))
					stateErr = store.RetryMemoryJob(commitCtx, job.ID, leaseOwner, retryAt, err.Error())
				}
				commitCancel()
				if stateErr != nil {
					log.Printf("diana memory job state update failed: %v", stateErr)
				}
			}
		}
	}
}

// memoryJobKindLabel 是日志里给人看的任务名。
func memoryJobKindLabel(kind MemoryJobKind) string {
	switch kind {
	case MemoryJobSummary:
		return "会话摘要"
	case MemoryJobEvent:
		return "长期记忆提取"
	default:
		return "记忆任务"
	}
}

func memoryJobAttemptsExhausted(attempts int) bool {
	return attempts > memoryMaxAttempts
}

func memoryRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Duration(1<<(attempt-1)) * 15 * time.Second
}

// claimMemoryJobs 优先走批量领取；存储没实现那个可选接口时退回单条。
func claimMemoryJobs(ctx context.Context, store StructuredMemoryStore, leaseOwner string, leaseUntil time.Time) ([]MemoryJob, error) {
	if batcher, ok := store.(MemoryJobBatchClaimer); ok && batcher != nil {
		return batcher.ClaimMemoryJobBatch(ctx, leaseOwner, leaseUntil, memoryEventBatchMax)
	}
	job, ok, err := store.ClaimNextMemoryJob(ctx, leaseOwner, leaseUntil)
	if err != nil || !ok {
		return nil, err
	}
	return []MemoryJob{job}, nil
}

func (r *Runtime) processMemoryJobs(ctx context.Context, store StructuredMemoryStore, jobs []MemoryJob) error {
	if len(jobs) == 0 {
		return nil
	}
	if len(jobs) > 1 {
		// 批量领取只会把同会话同发言者的事件任务凑在一起，摘要任务永远单独来。
		payloads := make([]MemoryJobPayload, 0, len(jobs))
		for _, job := range jobs {
			if job.Payload.Kind != MemoryJobEvent {
				return fmt.Errorf("unsupported memory job kind %q in batch", job.Payload.Kind)
			}
			payloads = append(payloads, job.Payload)
		}
		return r.processEventMemoryJobs(ctx, store, payloads)
	}
	job := jobs[0]
	switch job.Payload.Kind {
	case MemoryJobEvent:
		return r.processEventMemoryJobs(ctx, store, []MemoryJobPayload{job.Payload})
	case MemoryJobSummary:
		return r.processSummaryMemoryJob(ctx, store, job)
	default:
		return fmt.Errorf("unsupported memory job kind %q", job.Payload.Kind)
	}
}

func (r *Runtime) processEventMemoryJobs(ctx context.Context, store StructuredMemoryStore, payloads []MemoryJobPayload) error {
	ctx = withLLMUsagePurpose(ctx, "memory_extract")
	type gateSource struct {
		payload MemoryJobPayload
		event   MessageEvent
		text    string
	}
	sources := make([]gateSource, 0, len(payloads))
	for _, payload := range payloads {
		event := payload.Event
		// 停用的机器人不做后台活儿：记忆抽取要花一次模型调用，而这台机器人既不
		// 收消息也不回消息，抽出来的东西没人会用。任务照常完成，不在队列里积压。
		if r.profileDisabled(event.ProfileID) {
			continue
		}
		text := memoryEventText(event)
		if !memoryEventEligible(r.effectiveConfigForEvent(event), event, text) {
			continue
		}
		sources = append(sources, gateSource{payload: payload, event: event, text: text})
	}
	if len(sources) == 0 {
		return nil
	}
	// 相关记忆按整批消息一起检索：分开领取时每条消息都要重查一遍，查出来的还
	// 大量重合。
	last := sources[len(sources)-1]
	searchText := last.text
	for index := 0; index < len(sources)-1; index++ {
		searchText = sources[index].text + "\n" + searchText
	}
	// 门控要求模型「更新已有记忆时复用原 key」，前提是相关的旧记忆真的出现在
	// existing_memories 里。只按 importance 取前 40 条时，记忆一多，与当前消息
	// 相关但权重不高的旧 key 就会掉出窗口，模型只能另立新 key——改口后的偏好
	// 与旧偏好并存。先按当前消息的相关性取一批，再用 importance 批兜底合并。
	relevant, err := store.ListStructuredMemories(ctx, StructuredMemoryQuery{
		SubjectUserID: last.event.UserID,
		Session:       last.payload.Session,
		GroupID:       last.event.GroupID,
		SearchTerms:   structuredMemorySearchTerms(searchText, 32),
		Now:           time.Now(),
		MaxCandidates: 24,
		ExcludeKinds:  []MemoryKind{MemoryKindThread},
	})
	if err != nil {
		return fmt.Errorf("load relevant memories: %w", err)
	}
	important, err := store.ListStructuredMemories(ctx, StructuredMemoryQuery{
		SubjectUserID: last.event.UserID,
		Session:       last.payload.Session,
		GroupID:       last.event.GroupID,
		Now:           time.Now(),
		MaxCandidates: 40,
		ExcludeKinds:  []MemoryKind{MemoryKindThread},
	})
	if err != nil {
		return fmt.Errorf("load existing memories: %w", err)
	}
	existing := mergeStructuredMemories(relevant, important, 40)
	current := memoryGateEventFromMessage(last.event, last.text)
	gatePayload := memoryGatePayload{
		Current:          &current,
		RecentMessages:   r.memoryGateRecentEvents(last.event),
		ExistingMemories: memoryGateExistingMemories(existing, last.event.UserID),
	}
	if len(sources) > 1 {
		// 多条时 current 不再是单数：整批按时间顺序给出，模型用 source_index
		// 标注每条候选出自哪一条。current 留空，避免同一条消息出现两次。
		gatePayload.Current = nil
		batch := make([]memoryGateEvent, 0, len(sources))
		for _, source := range sources {
			batch = append(batch, memoryGateEventFromMessage(source.event, source.text))
		}
		gatePayload.CurrentBatch = batch
		// 攒批的这几条本来就在最近历史里，不必再作为上下文重复一遍。
		gatePayload.RecentMessages = memoryGateEventsExcluding(gatePayload.RecentMessages, batch)
	}
	payloadJSON, err := json.Marshal(gatePayload)
	if err != nil {
		return err
	}
	instruction := "请对当前消息执行记忆门控。上下文 JSON：\n"
	if len(sources) > 1 {
		instruction = "请对 current_batch 里的每一条消息执行记忆门控，每条候选用 source_index 标明出自第几条（从 0 开始）。上下文 JSON：\n"
	}
	messages := []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: memoryGateSystemPrompt,
		},
		{
			Role:    llm.RoleUser,
			Content: instruction + string(payloadJSON),
		},
	}
	raw, err := r.runLLMMemoryProvider(ctx, func(client LLMProvider) (string, error) {
		return generateMemoryCandidates(ctx, client, messages, memoryGateSubmitTool())
	})
	if err != nil {
		return fmt.Errorf("memory gate llm: %w", err)
	}
	candidates, err := parseMemoryCandidates(raw)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	grouped := make([][]MemoryCandidate, len(sources))
	for _, candidate := range candidates {
		// 没标或标歪了都归到最后一条：它是最新的那条，出处写错时的影响最小。
		index := len(sources) - 1
		if candidate.SourceIndex != nil && *candidate.SourceIndex >= 0 && *candidate.SourceIndex < len(sources) {
			index = *candidate.SourceIndex
		}
		grouped[index] = append(grouped[index], candidate)
	}
	for index, source := range sources {
		if len(grouped[index]) == 0 {
			continue
		}
		_, err = store.ApplyMemoryCandidates(ctx, MemoryWriteRequest{
			SubjectUserID:   strings.TrimSpace(source.event.UserID),
			SubjectName:     strings.TrimSpace(source.event.SenderNameOrID()),
			Session:         source.payload.Session,
			EventKind:       source.event.Kind,
			GroupID:         source.event.GroupID,
			SourceMessageID: source.event.MessageID,
			SourceEventTime: memoryEventTime(source.event),
			Candidates:      grouped[index],
		})
		if err != nil {
			return fmt.Errorf("apply memory candidates: %w", err)
		}
	}
	return nil
}

// memoryGateEventsExcluding 去掉已经出现在批次里的那几条，按 message_id 比对。
func memoryGateEventsExcluding(items []memoryGateEvent, batch []memoryGateEvent) []memoryGateEvent {
	if len(items) == 0 || len(batch) == 0 {
		return items
	}
	seen := make(map[string]bool, len(batch))
	for _, item := range batch {
		if item.MessageID != "" {
			seen[item.MessageID] = true
		}
	}
	kept := items[:0]
	for _, item := range items {
		if item.MessageID != "" && seen[item.MessageID] {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

func (r *Runtime) processSummaryMemoryJob(ctx context.Context, store StructuredMemoryStore, job MemoryJob) error {
	ctx = withLLMUsagePurpose(ctx, "memory_summary")
	events := job.Payload.Events
	if len(events) == 0 {
		return nil
	}
	if r.profileDisabled(events[len(events)-1].ProfileID) {
		return nil
	}
	if len(events) > memorySummaryMaxEvents {
		events = events[len(events)-memorySummaryMaxEvents:]
	}
	existing, err := store.ListStructuredMemories(ctx, StructuredMemoryQuery{
		Session:       job.Payload.Session,
		Now:           time.Now(),
		MaxCandidates: 200,
		Kinds:         []MemoryKind{MemoryKindSummary},
	})
	if err != nil {
		return fmt.Errorf("load conversation summaries: %w", err)
	}
	// thread 和 summary 由同一次调用产出：这批事件本来就在手上，多要一条「当前
	// 进行状态」不额外花一次 LLM 调用。
	threadKey := ThreadMemoryKey(job.Payload.Session)
	currentThread := ""
	if items, threadErr := store.ListStructuredMemories(ctx, StructuredMemoryQuery{
		Session:            job.Payload.Session,
		Now:                time.Now(),
		MaxCandidates:      4,
		Kinds:              []MemoryKind{MemoryKindThread},
		CurrentSessionOnly: true,
	}); threadErr == nil {
		// 同样不按 key 精确比较——理由见 sessionThreadNote。这里对不上的代价更
		// 隐蔽：currentThread 恒为空，模型每轮都以为「还没有便签」，于是从零重
		// 写一条只覆盖这一批事件的状态，增量更新从来没真正发生过。
		for _, item := range items {
			if content := strings.TrimSpace(item.Content); content != "" {
				currentThread = content
				break
			}
		}
	}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		if line := compactContextEventWithTime(event); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	rollup := selectMemorySummaryRollup(existing)
	input := struct {
		Session       string               `json:"session"`
		Events        []string             `json:"events"`
		Existing      []memoryGateMemory   `json:"existing_summaries,omitempty"`
		Rollup        *memorySummaryRollup `json:"rollup,omitempty"`
		ThreadKey     string               `json:"thread_key"`
		CurrentThread string               `json:"current_thread,omitempty"`
	}{
		Session:       job.Payload.Session,
		Events:        lines,
		Existing:      memoryGateExistingMemories(existing, ""),
		Rollup:        rollup,
		ThreadKey:     threadKey,
		CurrentThread: currentThread,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return err
	}
	messages := []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: strings.TrimSpace(`你是 Diana 的会话记忆整合器。请把一批较早的原始聊天事件整理为按时间和主题组织的长期会话摘要，原始事件会继续保留。

要求：
1. 理解整段对话后按主题聚合，保留人物、时间、事件、决定、未解决问题和事实变化；删除寒暄、重复和无后续价值的噪声。
2. 不得按关键词机械摘抄，不得把提问误当事实，不得补充原文没有的信息。
3. existing_summaries 是同会话已有摘要。相同日期和主题必须复用原 key，并生成包含旧摘要与新事件的完整更新版；不同主题建立新 key。
4. 若提供 rollup，必须额外输出且只输出一条 key 精确等于 rollup.target_key 的层级摘要，把 source_summaries 合并为自包含的时间线；保留人物、关键事实、决定、变化和未解决事项，不得遗漏相互矛盾的信息。不要为 source_summaries 输出逐条副本。
5. 普通摘要 key 使用 summary.<YYYY-MM-DD>.<topic>；层级摘要必须使用给定 target_key。topic 简短明确，content 自包含。importance/confidence 为 0 到 1，visibility 固定 session，source_type 固定 summary，sensitive 按内容判断。普通摘要 retention_days=365，month 层级=730，year 层级=3650。
6. 除普通摘要外，必须再输出且只输出一条 key 精确等于 thread_key 的会话线程便签，kind="thread"：写清这个会话「当前进行到哪」——正在聊的事、已经推进到的步骤、已经做出的决定、以及还悬而未决的问题。它是给下一轮对话直接看的状态便签，不是历史流水。
7. 写 thread 时以 current_thread 为基础做增量更新：已经完结、被取代或不再推进的话题从 thread 里移走（它们归 summary 管），只保留仍然活着的线索。没有任何进行中的事情时，content 写一句话说明会话处于空闲状态。thread 控制在 300 字以内，retention_days 固定 7。
8. 最多输出 6 条摘要（thread 不计入）；完全没有长期价值且没有 rollup 时摘要可以为空，但 thread 仍要输出。
9. 调用 memory_submit 提交摘要和 thread 便签，字段含义以工具参数说明为准。只有在不支持工具调用时，才退回输出合法 JSON {"memories":[...]}。`),
		},
		{Role: llm.RoleUser, Content: "请整合这批较早会话。上下文 JSON：\n" + string(inputJSON)},
	}
	raw, err := r.runLLMMemoryProvider(ctx, func(client LLMProvider) (string, error) {
		return generateMemoryCandidates(ctx, client, messages, memorySummarySubmitTool())
	})
	if err != nil {
		return fmt.Errorf("memory summary llm: %w", err)
	}
	candidates, err := parseMemoryCandidates(raw)
	if err != nil {
		return err
	}
	for index := range candidates {
		candidates[index].Action = MemoryActionUpsert
		candidates[index].SourceType = MemorySourceSummary
		candidates[index].Visibility = MemoryVisibilitySession
		if candidates[index].Key == threadKey || candidates[index].Kind == MemoryKindThread {
			// 线程便签只认固定 key：模型另起 key 会让同一个会话出现多条状态，
			// 常驻注入就成了互相矛盾的几份。
			candidates[index].Key = threadKey
			candidates[index].Kind = MemoryKindThread
			candidates[index].RetentionDays = memoryThreadRetentionDays
			continue
		}
		candidates[index].Kind = MemoryKindSummary
		if candidates[index].RetentionDays == 0 {
			switch {
			case strings.HasPrefix(candidates[index].Key, "summary.rollup.year."):
				candidates[index].RetentionDays = 3650
			case strings.HasPrefix(candidates[index].Key, "summary.rollup.month."):
				candidates[index].RetentionDays = 730
			default:
				candidates[index].RetentionDays = 365
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	first := events[0]
	last := events[len(events)-1]
	written, err := store.ApplyMemoryCandidates(ctx, MemoryWriteRequest{
		Session:         job.Payload.Session,
		EventKind:       first.Kind,
		GroupID:         first.GroupID,
		SourceMessageID: "summary:" + job.ID,
		SourceEventTime: memoryEventTime(last),
		Candidates:      candidates,
	})
	if err != nil {
		return fmt.Errorf("apply conversation summaries: %w", err)
	}
	if rollup != nil && memoryRollupWasWritten(written, rollup.TargetKey) {
		if err := forgetRolledUpSummaries(ctx, store, job, first, last, rollup); err != nil {
			return err
		}
	}
	return nil
}

// mergeStructuredMemories 按顺序合并两批记忆并按 ID 去重：前一批（相关性）整体
// 排在兜底批（重要度）之前，总数不超过 limit。
func mergeStructuredMemories(primary, fallback []StructuredMemoryItem, limit int) []StructuredMemoryItem {
	merged := make([]StructuredMemoryItem, 0, limit)
	seen := make(map[string]bool, limit)
	for _, batch := range [][]StructuredMemoryItem{primary, fallback} {
		for _, item := range batch {
			if len(merged) >= limit {
				return merged
			}
			if item.ID == "" || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			merged = append(merged, item)
		}
	}
	return merged
}

func selectMemorySummaryRollup(existing []StructuredMemoryItem) *memorySummaryRollup {
	monthly := make([]StructuredMemoryItem, 0)
	daily := make([]StructuredMemoryItem, 0)
	for _, item := range existing {
		switch {
		case strings.HasPrefix(item.Key, "summary.rollup.month."):
			monthly = append(monthly, item)
		case !strings.HasPrefix(item.Key, "summary.rollup."):
			daily = append(daily, item)
		}
	}
	if len(monthly) >= memorySummaryRollupSize {
		return buildMemorySummaryRollup("year", monthly)
	}
	if len(daily) >= memorySummaryRollupSize {
		return buildMemorySummaryRollup("month", daily)
	}
	return nil
}

func buildMemorySummaryRollup(level string, items []StructuredMemoryItem) *memorySummaryRollup {
	sort.SliceStable(items, func(left, right int) bool {
		return memorySummaryItemTime(items[left]).Before(memorySummaryItemTime(items[right]))
	})
	if len(items) > memorySummaryRollupSize {
		items = items[:memorySummaryRollupSize]
	}
	start := memorySummaryItemTime(items[0]).Format("2006.01.02")
	end := memorySummaryItemTime(items[len(items)-1]).Format("2006.01.02")
	return &memorySummaryRollup{
		Level:     level,
		TargetKey: fmt.Sprintf("summary.rollup.%s.%s.%s", level, start, end),
		Sources:   memoryGateExistingMemories(items, ""),
		Items:     append([]StructuredMemoryItem(nil), items...),
	}
}

func memorySummaryItemTime(item StructuredMemoryItem) time.Time {
	for _, value := range []time.Time{item.SourceEventTime, item.LastVerifiedAt, item.CreatedAt} {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

func memoryRollupWasWritten(items []StructuredMemoryItem, targetKey string) bool {
	for _, item := range items {
		if item.Status == MemoryStatusActive && item.Key == targetKey {
			return true
		}
	}
	return false
}

func forgetRolledUpSummaries(ctx context.Context, store StructuredMemoryStore, job MemoryJob, first, last MessageEvent, rollup *memorySummaryRollup) error {
	const batchSize = 8
	for offset := 0; offset < len(rollup.Items); offset += batchSize {
		end := min(offset+batchSize, len(rollup.Items))
		candidates := make([]MemoryCandidate, 0, end-offset)
		for _, item := range rollup.Items[offset:end] {
			candidates = append(candidates, MemoryCandidate{
				Action: MemoryActionForget, Key: item.Key, Kind: MemoryKindSummary,
				Topic: "分层摘要压缩", SourceType: MemorySourceSummary,
				Confidence: 1, Importance: item.Importance, Visibility: MemoryVisibilitySession,
			})
		}
		_, err := store.ApplyMemoryCandidates(ctx, MemoryWriteRequest{
			Session: job.Payload.Session, EventKind: first.Kind, GroupID: first.GroupID,
			SourceMessageID: fmt.Sprintf("summary-rollup-forget:%s:%d", job.ID, offset),
			SourceEventTime: memoryEventTime(last), Candidates: candidates,
		})
		if err != nil {
			return fmt.Errorf("retire rolled-up conversation summaries: %w", err)
		}
	}
	return nil
}

func (r *Runtime) enqueueEventMemory(event MessageEvent, text string) {
	event = withoutReplyRuntimeState(event)
	cfg := r.effectiveConfigForEvent(event)
	if !memoryEventEligible(cfg, event, text) || hasKnownResolverPlatformURL(event, text) {
		return
	}
	r.mu.RLock()
	store := r.structuredMemory
	r.mu.RUnlock()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, inserted, err := store.EnqueueMemoryJob(ctx, MemoryJobPayload{
		Kind:    MemoryJobEvent,
		Session: sessionKey(event),
		Event:   event,
	})
	cancel()
	if err != nil {
		log.Printf("diana memory event enqueue failed: %v", err)
		r.recordBackgroundFailure("memory_enqueue_failed", "消息没能排进长期记忆提取队列，这条不会被记住", "", err,
			map[string]any{"session": sessionKey(event), "message_id": event.MessageID})
		return
	}
	if inserted {
		r.wakeMemoryWorkers()
	}
}

func (r *Runtime) enqueueContextSummary(session string, events []MessageEvent) {
	if strings.TrimSpace(session) == "" || len(events) == 0 {
		return
	}
	r.mu.RLock()
	store := r.structuredMemory
	r.mu.RUnlock()
	if store == nil {
		return
	}
	copyEvents := append([]MessageEvent(nil), events...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, inserted, err := store.EnqueueMemoryJob(ctx, MemoryJobPayload{
		Kind:    MemoryJobSummary,
		Session: session,
		Events:  copyEvents,
	})
	cancel()
	if err != nil {
		log.Printf("diana memory summary enqueue failed: %v", err)
		r.recordBackgroundFailure("memory_enqueue_failed", "会话摘要没能排进队列，这一段不会生成摘要", "memory_summary_enqueue_failed", err,
			map[string]any{"session": session, "events": len(events)})
		return
	}
	if inserted {
		r.wakeMemoryWorkers()
	}
}

func (r *Runtime) wakeMemoryWorkers() {
	select {
	case r.memoryWake <- struct{}{}:
	default:
	}
}

func (r *Runtime) runLLMMemoryProvider(ctx context.Context, run llmProviderRunFunc) (string, error) {
	run = r.withLLMIdentityPrivacyRun(ctx, run)
	// 记忆抽取和归纳各自打了用途标签，却一直没挂记账：这两类调用的 token 从来
	// 没进过统计。
	run = r.withDebugTraceRun(ctx, run)
	run = r.withLLMUsageAccountingRun(ctx, run)
	r.mu.RLock()
	cfgFactory := r.llmCfgFactory
	factory := r.llmFactory
	store := r.llmStore
	r.mu.RUnlock()
	if cfgFactory != nil && store != nil {
		set := store.Profiles().WithDefaults()
		// 记忆是自动文本任务：专用 memory 分组优先，其次使用机器人已经绑定的
		// intent（未绑定 intent 时 roleBoundProfiles 会回退 chat）。不能直接取
		// Current，否则激活生图配置时会拿图片模型发送文本 Responses 请求。
		groups := append([]string(nil), memoryProfileGroups...)
		seen := map[string]bool{}
		for _, group := range groups {
			group = llm.NormalizeProfileGroup(group)
			if seen[group] {
				continue
			}
			seen[group] = true
			profiles := llmProfilesInGroup(set, group)
			if len(profiles) > 0 {
				return r.runLLMProviderProfileAttempts(ctx, profiles, cfgFactory, true, run)
			}
		}
		profiles, roleErr := r.roleBoundProfiles(llmUsagePurposeFromContext(ctx), set, llm.GroupIntent)
		if roleErr != nil {
			return "", roleErr
		}
		if len(profiles) > 0 {
			return r.runLLMProviderProfileAttempts(ctx, profiles, cfgFactory, true, run)
		}
		for _, group := range semanticRouteProfileGroups {
			group = llm.NormalizeProfileGroup(group)
			if seen[group] {
				continue
			}
			seen[group] = true
			if profiles := llmProfilesInGroup(set, group); len(profiles) > 0 {
				return r.runLLMProviderProfileAttempts(ctx, profiles, cfgFactory, true, run)
			}
		}
		if profiles := llmProfilesInGroup(set, llm.GroupChat); len(profiles) > 0 {
			return r.runLLMProviderProfileAttempts(ctx, profiles, cfgFactory, true, run)
		}
		return "", fmt.Errorf("diana: no text-capable llm profile is configured for memory")
	}
	if factory == nil {
		return "", fmt.Errorf("diana: llm provider is not configured")
	}
	client, err := factory()
	if err != nil {
		return "", err
	}
	return run(withTransientLLMRetry(client, true))
}

func parseMemoryCandidates(raw string) ([]MemoryCandidate, error) {
	raw = strings.TrimSpace(stripJSONCodeFence(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("invalid memory gate response")
	}
	var envelope struct {
		Memories []MemoryCandidate `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &envelope); err != nil {
		return nil, fmt.Errorf("decode memory gate response: %w", err)
	}
	if len(envelope.Memories) > 8 {
		envelope.Memories = envelope.Memories[:8]
	}
	return envelope.Memories, nil
}

func memoryEventEligible(cfg BotConfig, event MessageEvent, text string) bool {
	if !boolValue(cfg.LongTermMemoryEnabled, true) {
		return false
	}
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate {
		return false
	}
	if strings.TrimSpace(event.UserID) == "" || (strings.TrimSpace(cfg.BotAccount) != "" && event.UserID == cfg.BotAccount) {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" || memoryTextOnlyURLs(text) {
		return false
	}
	meaningful := 0
	for _, value := range text {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			meaningful++
		}
	}
	return meaningful >= 2
}

func memoryTextOnlyURLs(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		parsed, err := url.Parse(strings.Trim(field, "，。！？、,!?()[]{}<>\"'"))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return false
		}
	}
	return true
}

func memoryEventText(event MessageEvent) string {
	var builder strings.Builder
	for _, segment := range event.Segments {
		if segment.Type == "text" {
			builder.WriteString(segment.Data["text"])
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" && len(event.Segments) == 0 {
		text = strings.TrimSpace(event.RawMessage)
	}
	return strings.Join(strings.Fields(text), " ")
}

func memoryEventTime(event MessageEvent) time.Time {
	if event.Time > 0 {
		return time.Unix(event.Time, 0).UTC()
	}
	return time.Now().UTC()
}

func memoryGateEventFromMessage(event MessageEvent, text string) memoryGateEvent {
	item := memoryGateEvent{
		Time:      memoryEventTime(event).Format(time.RFC3339),
		UserID:    strings.TrimSpace(event.UserID),
		Sender:    strings.TrimSpace(event.SenderNameOrID()),
		Text:      truncateRunesFromStart(strings.TrimSpace(text), 500),
		GroupID:   strings.TrimSpace(event.GroupID),
		MessageID: strings.TrimSpace(event.MessageID),
	}
	if event.Quoted != nil {
		item.Quoted = truncateRunesFromStart(quotedPromptText(event.Quoted), 300)
	}
	return item
}

func (r *Runtime) memoryGateRecentEvents(current MessageEvent) []memoryGateEvent {
	history, _ := r.sessionContextHistory(current)
	items := make([]memoryGateEvent, 0, 6)
	for index := len(history) - 1; index >= 0 && len(items) < 6; index-- {
		event := history[index]
		if event.MessageID != "" && event.MessageID == current.MessageID {
			continue
		}
		text := memoryEventText(event)
		if text == "" {
			continue
		}
		items = append(items, memoryGateEventFromMessage(event, text))
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items
}

func memoryGateExistingMemories(items []StructuredMemoryItem, subjectUserID string) []memoryGateMemory {
	out := make([]memoryGateMemory, 0, len(items))
	for _, item := range items {
		if subjectUserID != "" && item.SubjectUserID != subjectUserID {
			continue
		}
		out = append(out, memoryGateMemory{
			Key:        item.Key,
			Kind:       item.Kind,
			Topic:      item.Topic,
			Entity:     item.Entity,
			Content:    item.Content,
			Confidence: item.Confidence,
			Importance: item.Importance,
			Visibility: item.Visibility,
			Version:    item.Version,
		})
		if len(out) >= 30 {
			break
		}
	}
	return out
}

func compactContextEventWithTime(event MessageEvent) string {
	line := compactContextEvent(event)
	if line == "" {
		return ""
	}
	return memoryEventTime(event).Format("2006-01-02 15:04") + " " + line
}
