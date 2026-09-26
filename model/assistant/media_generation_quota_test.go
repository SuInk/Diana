// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// persistentMediaUsage 模拟数据库：两个 Runtime 共用同一份，等于重启前后读同一个库。
type persistentMediaUsage struct {
	MessageHistoryStore
	memoryMediaGenerationUsage
}

func mediaQuotaRuntime(t *testing.T, bot BotConfig, group GroupConfig) (*Runtime, MessageEvent) {
	t.Helper()
	if bot.ID == "" {
		bot.ID = "qq"
	}
	if bot.OwnerID == "" {
		bot.OwnerID = "10001"
	}
	if group.GroupID == "" {
		group.GroupID = "20001"
	}
	group.BotProfileID = bot.ID
	group.Enabled = true
	runtime := NewRuntime(bot, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{group.GroupID: group}})
	return runtime, MessageEvent{Kind: EventKindGroup, ProfileID: bot.ID, GroupID: group.GroupID, UserID: "20002"}
}

func reserveAndCommit(t *testing.T, runtime *Runtime, event MessageEvent) error {
	t.Helper()
	reservation, err := runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationImage, 1)
	if err != nil {
		return err
	}
	reservation.commit(context.Background(), 1)
	return nil
}

func quotaErrorOf(t *testing.T, err error) *mediaGenerationQuotaError {
	t.Helper()
	var quotaErr *mediaGenerationQuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("err = %v，应当是生图次数超限", err)
	}
	return quotaErr
}

// 每群上限用满就拦，说明里写清用了多少、上限多少、明天再来。
func TestMediaQuotaBlocksGroupWhenExhausted(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 2}, GroupConfig{})
	for i := range 2 {
		event.UserID = []string{"20002", "20003"}[i]
		if err := reserveAndCommit(t, runtime, event); err != nil {
			t.Fatalf("第 %d 次不该拦：%v", i+1, err)
		}
	}
	event.UserID = "20004"
	quotaErr := quotaErrorOf(t, reserveAndCommit(t, runtime, event))
	if notice := quotaErr.Notice(); notice != "今天本群的生图次数已用完（2/2），明天再来" {
		t.Fatalf("notice = %q", notice)
	}
}

// 每人上限跨群合计：换个群接着画照样拦。
func TestMediaQuotaBlocksUserAcrossGroups(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyUserLimit: 1}, GroupConfig{})
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	event.GroupID = "20009"
	quotaErr := quotaErrorOf(t, reserveAndCommit(t, runtime, event))
	if notice := quotaErr.Notice(); notice != "今天你的生图次数已用完（1/1），明天再来" {
		t.Fatalf("notice = %q", notice)
	}
	private := MessageEvent{Kind: EventKindPrivate, ProfileID: event.ProfileID, UserID: event.UserID}
	quotaErrorOf(t, reserveAndCommit(t, runtime, private))
}

// 主人不受限，也不占群里的份额。
func TestMediaQuotaExemptsOwner(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1, ImageGenerationDailyUserLimit: 1}, GroupConfig{})
	owner := event
	owner.UserID = "10001"
	for range 3 {
		if err := reserveAndCommit(t, runtime, owner); err != nil {
			t.Fatalf("主人不该被拦：%v", err)
		}
	}
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatalf("主人画的不该占群友的次数：%v", err)
	}
}

// 失败、被拒都不计：预占放弃后次数原样还回来。
func TestMediaQuotaReleaseDoesNotCount(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{})
	reservation, err := runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationImage, 1)
	if err != nil {
		t.Fatal(err)
	}
	// 在途的预占要占住名额，不然并发时会超发。
	quotaErrorOf(t, reserveAndCommit(t, runtime, event))
	reservation.release()
	reservation.commit(context.Background(), 1) // 结清只生效一次
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatalf("放弃的预占不该算次数：%v", err)
	}
}

// 「每天」按机器人时钟的自然日：过了零点就重新算。
func TestMediaQuotaResetsAtMidnight(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{})
	zone := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 9, 27, 23, 59, 0, 0, zone)
	runtime.now = func() time.Time { return now }
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	quotaErrorOf(t, reserveAndCommit(t, runtime, event))
	now = time.Date(2026, 9, 28, 0, 1, 0, 0, zone)
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatalf("过了零点应当重新计数：%v", err)
	}
}

// 群里填了以群为准，留空跟随机器人。
func TestMediaQuotaGroupOverridesBot(t *testing.T) {
	bot := BotConfig{ImageGenerationDailyGroupLimit: 5}
	if groupLimit, _ := EffectiveMediaGenerationLimits(bot, GroupConfig{}, MediaGenerationImage); groupLimit != 5 {
		t.Fatalf("留空应跟随机器人，得到 %d", groupLimit)
	}
	runtime, event := mediaQuotaRuntime(t, bot, GroupConfig{ImageGenerationDailyGroupLimit: 1})
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	quotaErr := quotaErrorOf(t, reserveAndCommit(t, runtime, event))
	if !quotaErr.Group || quotaErr.Limit != 1 {
		t.Fatalf("应当按群里的 1 次拦：%#v", quotaErr)
	}
	// 没设上限的种类（以后的视频）不受生图字段影响。
	if groupLimit, userLimit := EffectiveMediaGenerationLimits(bot, GroupConfig{}, "video"); groupLimit != 0 || userLimit != 0 {
		t.Fatalf("video limits = %d/%d", groupLimit, userLimit)
	}
}

// 最后几次被一群人同时抢时，只能放出上限那么多。
func TestMediaQuotaConcurrentReservationsDoNotOversell(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 3}, GroupConfig{})
	var granted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 20 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			member := event
			member.UserID = fmt.Sprintf("3%04d", index)
			reservation, err := runtime.reserveMediaGeneration(context.Background(), member, MediaGenerationImage, 1)
			if err != nil {
				return
			}
			granted.Add(1)
			// 一半成功一半失败：失败放回的名额可以被后来者拿走，但总成功数不能超过上限。
			if index%2 == 0 {
				reservation.commit(context.Background(), 1)
			} else {
				reservation.release()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	counts, _ := runtime.mediaGenerationStore().MediaGenerationCounts(context.Background(), MediaGenerationKey{
		ProfileID: "qq", Platform: NormalizePlatformID(""), Kind: MediaGenerationImage, Day: runtime.clock().Format(time.DateOnly), GroupID: event.GroupID,
	})
	if counts.Group > 3 {
		t.Fatalf("记下了 %d 次，超过上限 3", counts.Group)
	}
	if granted.Load() < 3 {
		t.Fatalf("只放出了 %d 次，上限 3 应当用得满", granted.Load())
	}
}

// 次数记在存储里，重启（换一个 Runtime 读同一个库）后接着算。
func TestMediaQuotaSurvivesRestart(t *testing.T) {
	store := &persistentMediaUsage{}
	first, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{})
	first.SetMessageHistoryStore(store)
	if err := reserveAndCommit(t, first, event); err != nil {
		t.Fatal(err)
	}
	second, _ := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{})
	second.SetMessageHistoryStore(store)
	quotaErrorOf(t, reserveAndCommit(t, second, event))
}

// 逐张改图一次要占几张就预占几张，剩余不够时说清还剩多少。
func TestMediaQuotaNoticeForPartialRemainder(t *testing.T) {
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 3}, GroupConfig{})
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.reserveMediaGeneration(context.Background(), event, MediaGenerationImage, 3)
	notice := quotaErrorOf(t, err).Notice()
	if !strings.Contains(notice, "只剩 2 次") || !strings.Contains(notice, "1/3") {
		t.Fatalf("notice = %q", notice)
	}
}

// 走工具的完整路径：成功一张记一次，接口失败不记，用完之后工具如实返回超限，
// 而不是报一个模型会去重试的错误。
func TestImageToolCountsOnlySuccessfulGenerations(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		if fail.Load() {
			http.Error(w, `{"error":{"message":"upstream down"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"cXVvdGEtaW1hZ2U="}]}`))
	}))
	defer server.Close()
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    server.URL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info": {"group_id": "20001", "group_name": "测试群"},
	}}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001", ImageGenerationDailyGroupLimit: 1}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: server.URL + "/media"})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: "20002", MessageID: "quota-1"}
	policy := RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "10001", event.UserID)
	run := func(messageID, prompt string) dianaImageToolResult {
		t.Helper()
		event.MessageID = messageID
		raw, err := newDianaImageTool(runtime, event, policy).Run(context.Background(), map[string]any{"operation": "generate", "prompt": prompt})
		if err != nil {
			t.Fatal(err)
		}
		var result dianaImageToolResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("result = %q: %v", raw, err)
		}
		waitForCondition(t, 3*time.Second, func() bool { return runtime.activeSubagentTaskCount() == 0 })
		return result
	}

	fail.Store(true)
	if result := run("quota-fail", "一只猫"); !result.OK || result.QuotaExceeded {
		t.Fatalf("失败那次照常受理：%#v", result)
	}
	fail.Store(false)
	if result := run("quota-ok", "一只狗"); !result.OK || result.QuotaExceeded {
		t.Fatalf("失败的那次不该占掉次数：%#v", result)
	}
	result := run("quota-over", "一只鸟")
	if result.OK || !result.QuotaExceeded {
		t.Fatalf("成功一次之后应当用完：%#v", result)
	}
	for _, want := range []string{"今天本群的生图次数已用完（1/1），明天再来", "不要再调用 image", "变相出图"} {
		if !strings.Contains(result.Notice, want) {
			t.Fatalf("notice 缺少 %q：%q", want, result.Notice)
		}
	}
}

// 配置保存要走一遍 JSON 和归一化，两个上限都不能在这一路上丢掉。
func TestMediaQuotaConfigSurvivesSaveRoundTrip(t *testing.T) {
	bot := BotConfig{ID: "qq", ImageGenerationDailyGroupLimit: 20, ImageGenerationDailyUserLimit: 5}
	data, err := json.Marshal(bot.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"image_generation_daily_group_limit":20`, `"image_generation_daily_user_limit":5`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("机器人配置缺少 %s：%s", field, data)
		}
	}
	var restored BotConfig
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	group := GroupConfig{GroupID: "20001", BotProfileID: "qq", ImageGenerationDailyGroupLimit: 3}.WithDefaults("20001", restored)
	groupLimit, userLimit := EffectiveMediaGenerationLimits(restored, group, MediaGenerationImage)
	if groupLimit != 3 || userLimit != 5 {
		t.Fatalf("回读后生效上限 = %d/%d，want 3/5", groupLimit, userLimit)
	}
}
