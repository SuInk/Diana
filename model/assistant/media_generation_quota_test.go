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
	_ "time/tzdata"

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

// 「每天」按日界线时区的零点重置，和进程本地时区无关：Docker 里进程是 UTC，
// 以前按它算，北京时间早上 8 点才重置。没配时区、没设 TZ 时按北京时间。
func TestMediaQuotaResetsAtConfiguredMidnight(t *testing.T) {
	t.Setenv("TZ", "")
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{})
	now := time.Date(2026, 9, 27, 15, 59, 0, 0, time.UTC) // 北京时间 23:59
	runtime.now = func() time.Time { return now }
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	quotaErrorOf(t, reserveAndCommit(t, runtime, event))
	now = time.Date(2026, 9, 27, 16, 1, 0, 0, time.UTC) // 北京时间次日 00:01
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatalf("北京时间过了零点应当重新计数：%v", err)
	}
}

// 机器人上填了时区以它为准，其次 TZ，最后北京时间；填错的时区名退到下一档。
func TestDailyLimitLocationPrecedence(t *testing.T) {
	t.Setenv("TZ", "")
	if got := DailyLimitLocation(BotConfig{}).String(); got != "Asia/Shanghai" {
		t.Fatalf("默认时区 = %s", got)
	}
	t.Setenv("TZ", "Europe/Berlin")
	if got := DailyLimitLocation(BotConfig{}).String(); got != "Europe/Berlin" {
		t.Fatalf("应当读 TZ，得到 %s", got)
	}
	if got := DailyLimitLocation(BotConfig{DailyLimitTimezone: "America/New_York"}).String(); got != "America/New_York" {
		t.Fatalf("机器人配置应当优先，得到 %s", got)
	}
	if got := DailyLimitLocation(BotConfig{DailyLimitTimezone: "Mars/Olympus"}).String(); got != "Europe/Berlin" {
		t.Fatalf("填错的时区应退到 TZ，得到 %s", got)
	}
	// 纽约时间的一天：UTC 03:59 还是前一天，05:01 已是新的一天（夏令时 UTC-4）。
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1, DailyLimitTimezone: "America/New_York"}, GroupConfig{})
	now := time.Date(2026, 9, 28, 3, 59, 0, 0, time.UTC)
	runtime.now = func() time.Time { return now }
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 9, 28, 4, 1, 0, 0, time.UTC)
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatalf("纽约时间过了零点应当重新计数：%v", err)
	}
}

// 群里填了以群为准，留空跟随机器人，填 0 是本群不限。
func TestMediaQuotaGroupOverridesBot(t *testing.T) {
	bot := BotConfig{ImageGenerationDailyGroupLimit: 5}
	if groupLimit, _ := EffectiveMediaGenerationLimits(bot, GroupConfig{}, MediaGenerationImage); groupLimit != 5 {
		t.Fatalf("留空应跟随机器人，得到 %d", groupLimit)
	}
	if groupLimit, _ := EffectiveMediaGenerationLimits(bot, GroupConfig{ImageGenerationDailyGroupLimit: limitPtr(0)}, MediaGenerationImage); groupLimit != 0 {
		t.Fatalf("群里填 0 应当本群不限，得到 %d", groupLimit)
	}
	unlimited, unlimitedEvent := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{ImageGenerationDailyGroupLimit: limitPtr(0)})
	for range 3 {
		if err := reserveAndCommit(t, unlimited, unlimitedEvent); err != nil {
			t.Fatalf("本群不限时不该拦：%v", err)
		}
	}
	runtime, event := mediaQuotaRuntime(t, bot, GroupConfig{ImageGenerationDailyGroupLimit: limitPtr(1)})
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
	group := GroupConfig{GroupID: "20001", BotProfileID: "qq", ImageGenerationDailyGroupLimit: limitPtr(3)}.WithDefaults("20001", restored)
	groupLimit, userLimit := EffectiveMediaGenerationLimits(restored, group, MediaGenerationImage)
	if groupLimit != 3 || userLimit != 5 {
		t.Fatalf("回读后生效上限 = %d/%d，want 3/5", groupLimit, userLimit)
	}
	// 群里存的「不限」（0）和「跟随」（不带这个字段）存一遍再读回来要分得开。
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{`{"group_id":"20001","image_generation_daily_group_limit":0}`, 0},
		{`{"group_id":"20001"}`, 20},
	} {
		var saved GroupConfig
		if err := json.Unmarshal([]byte(tc.raw), &saved); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(saved.WithDefaults("20001", restored))
		if err != nil {
			t.Fatal(err)
		}
		var reloaded GroupConfig
		if err := json.Unmarshal(data, &reloaded); err != nil {
			t.Fatal(err)
		}
		if groupLimit, _ := EffectiveMediaGenerationLimits(restored, reloaded, MediaGenerationImage); groupLimit != tc.want {
			t.Fatalf("%s 回读后每群上限 = %d，want %d", tc.raw, groupLimit, tc.want)
		}
	}
}

func limitPtr(value int64) *int64 { return &value }

// countingMediaUsage 记下存储被读写了几次；blockGroup 的读取先取数、再卡住，
// 模拟某个群的存储很慢，卡住期间别人落的账它看不到。
type countingMediaUsage struct {
	MessageHistoryStore
	memoryMediaGenerationUsage
	reads      atomic.Int64
	writes     atomic.Int64
	blockGroup string
	unblock    chan struct{}
}

func (s *countingMediaUsage) MediaGenerationCounts(ctx context.Context, key MediaGenerationKey) (MediaGenerationCounts, error) {
	s.reads.Add(1)
	counts, err := s.memoryMediaGenerationUsage.MediaGenerationCounts(ctx, key)
	if s.blockGroup != "" && key.GroupID == s.blockGroup {
		select {
		case <-s.unblock:
		case <-ctx.Done():
		}
	}
	return counts, err
}

func (s *countingMediaUsage) AddMediaGeneration(ctx context.Context, key MediaGenerationKey, count int64) error {
	s.writes.Add(1)
	return s.memoryMediaGenerationUsage.AddMediaGeneration(ctx, key, count)
}

// 没设上限时不读库，但成功后照样记账：白天中途打开限制，当天画过的要算进去。
func TestMediaQuotaUnlimitedSkipsReadButStillRecords(t *testing.T) {
	store := &countingMediaUsage{}
	runtime, event := mediaQuotaRuntime(t, BotConfig{}, GroupConfig{})
	runtime.SetMessageHistoryStore(store)
	if err := reserveAndCommit(t, runtime, event); err != nil {
		t.Fatal(err)
	}
	if store.reads.Load() != 0 || store.writes.Load() != 1 {
		t.Fatalf("reads = %d, writes = %d，不限时应当不读、只记一次", store.reads.Load(), store.writes.Load())
	}
	if len(runtime.mediaQuota.pending) != 0 {
		t.Fatalf("不限时不该登记在途预占：%d", len(runtime.mediaQuota.pending))
	}
}

// 一个群的存储读得慢，不能把别的群的生图一起卡住：读库不在全局锁里。
func TestMediaQuotaSlowStoreDoesNotBlockOtherGroups(t *testing.T) {
	store := &countingMediaUsage{blockGroup: "20001", unblock: make(chan struct{})}
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 5}, GroupConfig{})
	runtime.SetMessageHistoryStore(store)
	slowDone := make(chan error, 1)
	go func() { slowDone <- reserveAndCommit(t, runtime, event) }()
	waitForCondition(t, time.Second, func() bool { return store.reads.Load() >= 1 })
	other := event
	other.GroupID = "20009"
	fastDone := make(chan error, 1)
	go func() { fastDone <- reserveAndCommit(t, runtime, other) }()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("别的群被慢存储卡住了")
	}
	close(store.unblock)
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
}

// 读库和判定之间有人刚记完账：必须重读，不能两头都没数到它而超发。
func TestMediaQuotaRereadsWhenCommitLandsDuringRead(t *testing.T) {
	store := &countingMediaUsage{blockGroup: "20001", unblock: make(chan struct{})}
	runtime, event := mediaQuotaRuntime(t, BotConfig{ImageGenerationDailyGroupLimit: 1}, GroupConfig{})
	runtime.SetMessageHistoryStore(store)
	// 一笔在途预占，在别人读库期间落账并移出在途。
	key := MediaGenerationKey{ProfileID: "qq", Platform: NormalizePlatformID(""), Kind: MediaGenerationImage,
		Day: runtime.clock().In(DailyLimitLocation(BotConfig{})).Format(time.DateOnly), GroupID: event.GroupID, UserID: event.UserID}
	inflight := &mediaGenerationReservation{quota: &runtime.mediaQuota, store: store, key: key, amount: 1, expires: time.Now().Add(time.Hour), tracked: true}
	runtime.mediaQuota.pending = map[*mediaGenerationReservation]struct{}{inflight: {}}
	second := make(chan error, 1)
	go func() {
		member := event
		member.UserID = "20003"
		_, err := runtime.reserveMediaGeneration(context.Background(), member, MediaGenerationImage, 1)
		second <- err
	}()
	waitForCondition(t, time.Second, func() bool { return store.reads.Load() >= 1 })
	// 读库卡着的时候，在途那笔记账并移出在途。
	inflight.commit(context.Background(), 1)
	close(store.unblock)
	quotaErrorOf(t, <-second)
}

// 同样的任务已经在跑时直接复用，不占次数：名额只剩在途那一个，用户重发同一个请求
// 不该被判「用完」。
func TestImageToolReusesRunningTaskWithoutQuota(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media" {
			writeTestPNG(w)
			return
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"cXVvdGEtaW1hZ2U="}]}`))
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	runtime, event, policy := imageQuotaToolRuntime(t, server.URL, 1)
	run := func() dianaImageToolResult {
		t.Helper()
		raw, err := newDianaImageTool(runtime, event, policy).Run(context.Background(), map[string]any{"operation": "generate", "prompt": "一只猫"})
		if err != nil {
			t.Fatal(err)
		}
		var result dianaImageToolResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("result = %q: %v", raw, err)
		}
		return result
	}
	first := run()
	if !first.OK || first.Reused {
		t.Fatalf("first = %#v", first)
	}
	second := run()
	if second.QuotaExceeded || !second.Reused || second.TaskID != first.TaskID {
		t.Fatalf("重发同样的请求应当复用在跑的任务：%#v", second)
	}
	close(release)
	waitForCondition(t, 3*time.Second, func() bool { return runtime.activeSubagentTaskCount() == 0 })
}

// 预约了却没跑起来（这一轮回复没发出去、取消了预约）时，预占当场退回，不挂一小时。
func TestImageToolReleasesQuotaWhenDeferredTaskIsCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("取消的任务不该调用图片接口：%s", r.URL.Path)
	}))
	defer server.Close()
	runtime, event, policy := imageQuotaToolRuntime(t, server.URL, 1)
	ctx, sink := withImageAnnouncementSink(context.Background())
	raw, err := newDianaImageTool(runtime, event, policy).Run(ctx, map[string]any{"operation": "generate", "prompt": "一只猫"})
	if err != nil || strings.Contains(raw, "quota_exceeded") {
		t.Fatalf("raw = %q, err = %v", raw, err)
	}
	if err := reserveAndCommitNoop(runtime, event); err == nil {
		t.Fatal("在途的预占应当占住唯一的名额")
	}
	sink.cancelPending()
	if err := reserveAndCommitNoop(runtime, event); err != nil {
		t.Fatalf("取消预约后名额应当退回：%v", err)
	}
}

// 任务预约后排队时运行时就关了：没跑到 Run 也要调用 Finish。
func TestPluginTaskFinishRunsWhenTaskNeverStarts(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	var finished atomic.Int64
	task := PluginTask{Kind: "image", Key: "k", Finish: func() { finished.Add(1) }, Run: func(context.Context, PluginTaskServices) (PluginTaskResult, error) {
		t.Error("不该跑到 Run")
		return PluginTaskResult{}, nil
	}}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "20002"}
	cancelled := runtime.reservePluginTasks(event, []PluginTask{task})
	runtime.cancelPluginTaskReservation(cancelled)
	if finished.Load() != 1 {
		t.Fatalf("取消预约后 Finish 调用了 %d 次", finished.Load())
	}
	queued := runtime.reservePluginTasks(event, []PluginTask{task})
	for range cap(runtime.subagentSem) {
		runtime.subagentSem <- struct{}{}
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime.runPluginTask(rootCtx, queued.reserved[0])
	if finished.Load() != 2 {
		t.Fatalf("排队中关停后 Finish 调用了 %d 次", finished.Load())
	}
}

// reserveAndCommitNoop 只试一下能不能占到名额，占到就放回。
func reserveAndCommitNoop(runtime *Runtime, event MessageEvent) error {
	member := event
	member.UserID = "20009"
	reservation, err := runtime.reserveMediaGeneration(context.Background(), member, MediaGenerationImage, 1)
	reservation.release()
	return err
}

func imageQuotaToolRuntime(t *testing.T, serverURL string, groupLimit int64) (*Runtime, MessageEvent, RelationshipPolicy) {
	t.Helper()
	store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{
		Provider:   llm.ProviderOpenAICompatible,
		APIKey:     "secret",
		BaseURL:    serverURL + "/v1",
		Model:      "gpt-test",
		ImageModel: "gpt-image-2",
	})}
	channel := &recordingChannel{apiResponses: map[string]map[string]any{
		"get_group_info": {"group_id": "20001", "group_name": "测试群"},
	}}
	runtime := NewRuntime(BotConfig{ID: "qq", OwnerID: "10001", ImageGenerationDailyGroupLimit: groupLimit}, channel, NewPluginManager(), store, nil, nil, nil)
	runtime.SetMediaStore(mediaStore(t))
	runtime.SetLocalMediaSharer(&recordingLocalMediaSharer{url: serverURL + "/media"})
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "qq", GroupID: "20001", UserID: "20002", MessageID: "quota-reuse"}
	return runtime, event, RelationshipPolicyFor(UserMemoryProfile{Favorability: 20, MessageCount: 10}, "10001", event.UserID)
}
