package assistant

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const sharedRouteTTL = 15 * time.Minute
const sharedRouteLimit = 4096

// OutboundProfileLookup resolves only messages sent by the supplied profiles,
// within the same group. Implementations must not match platform account IDs.
type OutboundProfileLookup interface {
	LookupOutboundMessageProfile(context.Context, []string, string, string) (string, error)
}

type sharedRouteEntry struct {
	profile  string
	directed bool
	expires  time.Time
}
type sharedRouteState struct {
	mu        sync.Mutex
	decisions map[string]sharedRouteEntry
	outgoing  map[string]sharedRouteEntry
}

func sharedConnectionID(cfg BotConfig) string {
	if cfg.ConnectionProfileID != "" {
		return cfg.ConnectionProfileID
	}
	return cfg.ID
}

func (r *Runtime) sharedConnectionProfiles(profileID string) (string, []BotConfig) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.profileConfigs[profileID]
	if !ok || !IsOneBotPlatform(cfg.Platform) {
		return "", nil
	}
	id := sharedConnectionID(cfg)
	var profiles []BotConfig
	// The source wins ties regardless of the order used in the UI.
	if source, ok := r.profileConfigs[id]; ok {
		profiles = append(profiles, source)
	}
	for _, pid := range r.profileOrder {
		p := r.profileConfigs[pid]
		if pid != id && IsOneBotPlatform(p.Platform) && sharedConnectionID(p) == id {
			profiles = append(profiles, p)
		}
	}
	return id, profiles
}

func sharedRouteKey(connection, group, value string) string {
	return connection + "\x00" + group + "\x00" + value
}
func pruneSharedRoutes(items map[string]sharedRouteEntry, now time.Time) {
	for key, entry := range items {
		if !now.Before(entry.expires) {
			delete(items, key)
		}
	}
	if len(items) >= sharedRouteLimit {
		var oldest string
		var at time.Time
		for key, entry := range items {
			if oldest == "" || entry.expires.Before(at) {
				oldest, at = key, entry.expires
			}
		}
		delete(items, oldest)
	}
}

// Select once per physical message, before any profile starts a model request.
// Other profiles retain the incoming message as context, without running plugins
// or registering it as a directed turn of their own.
func (r *Runtime) routeSharedGroupEvent(ctx context.Context, event MessageEvent) (MessageEvent, bool) {
	if event.Kind != EventKindGroup {
		return event, true
	}
	connection, profiles := r.sharedConnectionProfiles(event.ProfileID)
	if len(profiles) < 2 {
		return event, true
	}
	effective := make(map[string]BotConfig, len(profiles))
	eligible := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		e := event
		e.ProfileID = p.ID
		e.ContextNamespace = p.ID
		cfg := r.effectiveConfigForEvent(e)
		effective[p.ID] = cfg
		eligible[p.ID] = !r.profileDisabled(p.ID) && r.admits(cfg, e)
	}
	now := time.Now()
	state := &r.sharedRoutes
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.decisions == nil {
		state.decisions = map[string]sharedRouteEntry{}
		state.outgoing = map[string]sharedRouteEntry{}
	}
	key := sharedRouteKey(connection, event.GroupID, event.MessageID)
	if entry, ok := state.decisions[key]; event.MessageID != "" && ok && now.Before(entry.expires) {
		if entry.profile == event.ProfileID && eligible[entry.profile] {
			event.ToMe = event.ToMe || entry.directed
			return event, true
		}
		return event, false
	}
	selected := ""
	directed := false
	explicit := false
	longest := 0
	for _, p := range profiles {
		cfg := effective[p.ID]
		cfg.GroupTriggers = append(append([]string(nil), cfg.GroupTriggers...), cfg.Name)
		for _, alias := range matchedGroupAliases(event, cfg, event.RawMessage) {
			if n := utf8.RuneCountInString(alias); n > longest {
				selected, longest, explicit, directed = p.ID, n, true, true
			}
		}
	}
	quotedID := ""
	if event.Quoted != nil {
		quotedID = event.Quoted.MessageID
	} else if ids := replyReferenceIDs(event.Segments); len(ids) > 0 {
		quotedID = ids[0]
	}
	// OneBot quotes arrive as reply segments; full quote enrichment happens
	// later. Our outbound ledger itself proves ownership of this message ID.
	if !explicit && quotedID != "" {
		qkey := sharedRouteKey(connection, event.GroupID, quotedID)
		if entry, ok := state.outgoing[qkey]; ok && now.Before(entry.expires) {
			selected = entry.profile
		}
		if selected == "" {
			r.mu.RLock()
			lookup, _ := r.inboundStore.(OutboundProfileLookup)
			r.mu.RUnlock()
			if lookup != nil {
				ids := make([]string, 0, len(profiles))
				for _, p := range profiles {
					ids = append(ids, p.ID)
				}
				lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				id, err := lookup.LookupOutboundMessageProfile(lookupCtx, ids, event.GroupID, quotedID)
				cancel()
				if err != nil {
					log.Printf("diana shared connection quote lookup failed: %v", err)
					return event, false
				}
				selected = id
			}
		}
		directed = selected != ""
	}
	if selected == "" {
		for _, p := range profiles {
			if eligible[p.ID] {
				selected = p.ID
				break
			}
		}
	}
	if !eligible[selected] {
		selected = ""
	}
	if event.MessageID != "" {
		pruneSharedRoutes(state.decisions, now)
		state.decisions[key] = sharedRouteEntry{profile: selected, directed: directed, expires: now.Add(sharedRouteTTL)}
	}
	if selected != event.ProfileID {
		return event, false
	}
	event.ToMe = event.ToMe || directed
	return event, true
}

func (r *Runtime) rememberSharedReply(source MessageEvent, msg OutgoingMessage, messageID string) {
	if source.Kind != EventKindGroup || msg.GroupID != source.GroupID {
		return
	}
	connection, profiles := r.sharedConnectionProfiles(source.ProfileID)
	if len(profiles) < 2 {
		return
	}
	now := time.Now()
	state := &r.sharedRoutes
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.outgoing == nil {
		state.decisions = map[string]sharedRouteEntry{}
		state.outgoing = map[string]sharedRouteEntry{}
	}
	if messageID != "" {
		pruneSharedRoutes(state.outgoing, now)
		state.outgoing[sharedRouteKey(connection, source.GroupID, messageID)] = sharedRouteEntry{profile: source.ProfileID, expires: now.Add(24 * time.Hour)}
	}

}

func (r *Runtime) sharedReplyPrefix(msg OutgoingMessage) OutgoingMessage {
	if msg.GroupID == "" || strings.TrimSpace(msg.Text) == "" || len(msg.Segments) > 0 {
		return msg
	}
	_, profiles := r.sharedConnectionProfiles(msg.ProfileID)
	active := 0
	for _, profile := range profiles {
		if !r.profileDisabled(profile.ID) && !r.isGroupDisabled(profile.ID, msg.GroupID) {
			active++
		}
	}
	if active < 2 {
		return msg
	}
	cfg := r.ProfileConfig(msg.ProfileID)
	if !cfg.ConnectionReplyPrefix {
		return msg
	}
	name := strings.Join(strings.Fields(cfg.Name), " ")
	if name == "" {
		return msg
	}
	prefix := "【" + name + "】"
	if !strings.HasPrefix(msg.Text, prefix) {
		msg.Text = prefix + msg.Text
	}
	return msg
}
