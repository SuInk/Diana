package assistant

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"time"
)

// RSS 订阅的「话题池」送达方式：命中的条目不直接发群，先存进这条订阅自己的池子。
// 目标群冷场、主动开口找话题时拿最新的几条当素材破冰；开口发出去后，这几条对那个群
// 标记已提，不再反复提。
//
// 不在普通回复里按话题匹配带素材：生产回放里关键词匹配几乎条条误中，向量相似度对
// 相关和无关的群聊分不开（相关 0.65 上下，闲聊也有 0.59），阈值定不住。
//
// 池子存在订阅记录（Reminder.FeedTopicPoolJSON）里，跟着订阅一起保存、一起删除，
// 不另开存储。

const (
	rssDeliveryPush = "push"
	rssDeliveryPool = "pool"

	// rssTopicPoolTTL 之后的条目算过时，不再带进上下文。
	rssTopicPoolTTL = 48 * time.Hour
	// rssTopicPoolMaxItems 是一条订阅最多留的条目数，超出时丢最旧的。
	rssTopicPoolMaxItems = 30
	// rssTopicPoolOfferItems 是一次冷场开口最多给模型几条池条目。
	rssTopicPoolOfferItems = 2
)

const rssDeliveryDescription = "命中条目怎么送到群里：push 直接推送（默认）；pool 不推送，存进话题池，等群里冷场、主动开口时拿来找话题（需要该群开着主动开口）"

var errRSSDelivery = errors.New("delivery 只能是 push 或 pool")

// rssTopicPoolItem 是池子里的一条。SharedTargets 记的是已经提过它的群
// （messageEventDeliveryKey），按群分别去重。
type rssTopicPoolItem struct {
	ID            string   `json:"id"`
	Source        string   `json:"source,omitempty"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary,omitempty"`
	URL           string   `json:"url,omitempty"`
	CreatedAt     int64    `json:"created_at"`
	SharedTargets []string `json:"shared_targets,omitempty"`
}

func (item rssTopicPoolItem) sharedWith(target string) bool {
	return slices.Contains(item.SharedTargets, target)
}

// normalizeRSSDelivery 把送达方式规整成 push 或 pool；空值按 push。
func normalizeRSSDelivery(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", rssDeliveryPush:
		return rssDeliveryPush, true
	case rssDeliveryPool:
		return rssDeliveryPool, true
	default:
		return "", false
	}
}

func rssWatchUsesTopicPool(item Reminder) bool {
	return item.FeedDelivery == rssDeliveryPool
}

func decodeRSSTopicPool(raw string) []rssTopicPoolItem {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var items []rssTopicPoolItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

func encodeRSSTopicPool(items []rssTopicPoolItem) string {
	if len(items) == 0 {
		return ""
	}
	body, _ := json.Marshal(items)
	return string(body)
}

// pruneRSSTopicPool 去掉过期条目，并把条数压到上限（留最新的）。
func pruneRSSTopicPool(items []rssTopicPoolItem, now time.Time) []rssTopicPoolItem {
	cutoff := now.Add(-rssTopicPoolTTL).Unix()
	kept := make([]rssTopicPoolItem, 0, len(items))
	for _, item := range items {
		if item.CreatedAt >= cutoff {
			kept = append(kept, item)
		}
	}
	if len(kept) > rssTopicPoolMaxItems {
		kept = kept[len(kept)-rssTopicPoolMaxItems:]
	}
	return kept
}

// RSSTopicPoolSize 是订阅池子里还没过期的条目数，给列表显示用。
func RSSTopicPoolSize(item Reminder, now time.Time) int {
	return len(pruneRSSTopicPool(decodeRSSTopicPool(item.FeedTopicPoolJSON), now))
}

func rssTopicPoolItemID(title, link string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(title) + "\x00" + strings.TrimSpace(link)))
	return hex.EncodeToString(sum[:])[:12]
}

// addRSSTopicPoolEntries 把这一轮命中的条目放进订阅的话题池。已在池里的条目
// （同标题同链接）不重复放，也不清掉它的已提记录。
func (r *Runtime) addRSSTopicPoolEntries(item Reminder, source ReminderFeedSource, feedName string, entries []rssWatchItem) error {
	if len(entries) == 0 {
		return nil
	}
	label := strings.TrimPrefix(strings.TrimPrefix(rssWatchNoticeHeader(source, feedName), "RSS "), "Twitter ")
	now := r.clock()
	_, err := r.mutateRSSWatch(item.OwnerID, item.ID, func(saved *Reminder) error {
		pool := pruneRSSTopicPool(decodeRSSTopicPool(saved.FeedTopicPoolJSON), now)
		known := make(map[string]bool, len(pool))
		for _, existing := range pool {
			known[existing.ID] = true
		}
		for _, entry := range entries {
			title := strings.TrimSpace(entry.Title)
			summary := cleanFeedText(entry.Content, 160)
			if title == "" {
				title, summary = truncateRunes(summary, 60), ""
			}
			if title == "" {
				continue
			}
			id := rssTopicPoolItemID(title, entry.Link)
			if known[id] {
				continue
			}
			known[id] = true
			if summary == title {
				summary = ""
			}
			pool = append(pool, rssTopicPoolItem{ID: id, Source: label, Title: title, Summary: summary, URL: strings.TrimSpace(entry.Link), CreatedAt: now.Unix()})
		}
		saved.FeedTopicPoolJSON = encodeRSSTopicPool(pruneRSSTopicPool(pool, now))
		return nil
	})
	return err
}

// rssTopicPoolMatch 是一条池条目和它属于哪条订阅。
type rssTopicPoolMatch struct {
	owner, watchID string
	item           rssTopicPoolItem
}

// rssTopicPoolCandidates 是投递目标包含这个群的话题池订阅里，没过期、这个群还没提过的
// 条目，按新到旧排。
func (r *Runtime) rssTopicPoolCandidates(event MessageEvent) []rssTopicPoolMatch {
	if r == nil || event.Kind != EventKindGroup {
		return nil
	}
	target := messageEventDeliveryKey(event)
	now := r.clock()
	var out []rssTopicPoolMatch
	for _, watch := range r.allRSSWatches() {
		if !rssWatchUsesTopicPool(watch) || !watch.CancelledAt.IsZero() || !rssWatchTargetsEvent(watch, target) {
			continue
		}
		for _, item := range pruneRSSTopicPool(decodeRSSTopicPool(watch.FeedTopicPoolJSON), now) {
			if !item.sharedWith(target) {
				out = append(out, rssTopicPoolMatch{owner: watch.OwnerID, watchID: watch.ID, item: item})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b rssTopicPoolMatch) int { return int(b.item.CreatedAt - a.item.CreatedAt) })
	return out
}

func rssWatchTargetsEvent(watch Reminder, target string) bool {
	for _, candidate := range repositoryWatchDeliveryTargets(watch) {
		if candidate.Kind == EventKindGroup && messageEventDeliveryKey(candidate) == target {
			return true
		}
	}
	return false
}

func rssTopicPoolLine(item rssTopicPoolItem) string {
	line := item.Title
	if item.Source != "" {
		line = "[" + item.Source + "] " + line
	}
	if item.Summary != "" {
		line += "（" + truncateRunes(item.Summary, 110) + "）"
	}
	return line
}

// rssTopicPoolNews 给冷场开口找话题用：这个群还没提过的最新几条。
func (r *Runtime) rssTopicPoolNews(event MessageEvent) proactiveNews {
	candidates := r.rssTopicPoolCandidates(event)
	if len(candidates) == 0 {
		return proactiveNews{}
	}
	if len(candidates) > rssTopicPoolOfferItems {
		candidates = candidates[:rssTopicPoolOfferItems]
	}
	lines := make([]string, 0, len(candidates))
	for _, match := range candidates {
		lines = append(lines, rssTopicPoolLine(match.item))
	}
	return proactiveNews{Query: "订阅话题池", Results: strings.Join(lines, "\n"), topicPool: candidates}
}

// markRSSTopicPoolShared 在冷场开口发出去后，把给过模型的那几条对这个群标记已提。
// 模型一般只挑一条说，另一条也一起算：同一批素材下次再给，多半还是没话可接。
func (r *Runtime) markRSSTopicPoolShared(event MessageEvent, matches []rssTopicPoolMatch) {
	if r == nil || event.Kind != EventKindGroup || len(matches) == 0 {
		return
	}
	target := messageEventDeliveryKey(event)
	type watchKey struct{ owner, id string }
	byWatch := map[watchKey][]string{}
	for _, match := range matches {
		key := watchKey{match.owner, match.watchID}
		byWatch[key] = append(byWatch[key], match.item.ID)
	}
	for key, ids := range byWatch {
		_, err := r.mutateRSSWatch(key.owner, key.id, func(saved *Reminder) error {
			pool := decodeRSSTopicPool(saved.FeedTopicPoolJSON)
			for index := range pool {
				if slices.Contains(ids, pool[index].ID) && !pool[index].sharedWith(target) {
					pool[index].SharedTargets = append(pool[index].SharedTargets, target)
				}
			}
			saved.FeedTopicPoolJSON = encodeRSSTopicPool(pool)
			return nil
		})
		if err != nil {
			log.Printf("diana rss topic pool mark shared %s failed: %v", key.id, err)
		}
	}
}
