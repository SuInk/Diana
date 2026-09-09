package webui

import "time"

type eventSummaryCacheEntry struct {
	response assistantEventsResponse
	expires  time.Time
}

func (h *BotHandler) cachedEventSummary(key string) (assistantEventsResponse, bool) {
	h.eventSummaryMu.Lock()
	defer h.eventSummaryMu.Unlock()
	entry, ok := h.eventSummaryCache[key]
	return entry.response, ok && time.Now().Before(entry.expires)
}

func (h *BotHandler) cacheEventSummary(key string, response assistantEventsResponse) {
	h.eventSummaryMu.Lock()
	defer h.eventSummaryMu.Unlock()
	if h.eventSummaryCache == nil {
		h.eventSummaryCache = map[string]eventSummaryCacheEntry{}
	}
	for k, entry := range h.eventSummaryCache {
		if !time.Now().Before(entry.expires) {
			delete(h.eventSummaryCache, k)
		}
	}
	if len(h.eventSummaryCache) >= 32 {
		clear(h.eventSummaryCache)
	}
	h.eventSummaryCache[key] = eventSummaryCacheEntry{response: response, expires: time.Now().Add(15 * time.Second)}
}
