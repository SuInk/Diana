// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

type modelRouteSchedule struct {
	signature string
	next      uint64
	scores    []int
}

func modelRouteWeight(role ModelRole) int {
	if role.Weight < 1 {
		return 1
	}
	if role.Weight > 1000 {
		return 1000
	}
	return role.Weight
}

func modelRouteProfileKey(profile llm.Profile) string {
	return profile.ID + "\x00" + profile.Config.Model
}

// scheduledRoleProfiles is used only when opening a real provider run. Catalog,
// token-budget and capability inspection continue to use roleBoundProfiles and
// must never consume a slot in the round-robin sequence. A provider run retains
// this ordered list throughout its response and tool calls, including failover.
func (r *Runtime) scheduledRoleProfiles(ctx context.Context, purpose string, set llm.ProfileSet, group string, roles map[string]ModelRole) ([]llm.Profile, error) {
	profiles, err := r.roleBoundProfiles(purpose, set, group, roles)
	if err != nil || len(profiles) < 2 {
		return profiles, err
	}
	role, ok := modelRoleFor(roles, purpose, group)
	if !ok || (role.RoutingStrategy != "round_robin" && role.RoutingStrategy != "weighted") {
		return profiles, nil
	}
	type allocation struct {
		weight  int
		standby bool
	}
	allocations := map[string]allocation{}
	for index, route := range append([]ModelRole{role}, role.Fallbacks...) {
		if route.Disabled {
			continue
		}
		candidates, routeErr := profilesForModelRole(set, route)
		if routeErr != nil {
			return nil, routeErr
		}
		for _, profile := range candidates {
			key := modelRouteProfileKey(profile)
			if _, exists := allocations[key]; !exists {
				allocations[key] = allocation{weight: modelRouteWeight(route), standby: index > 0 && route.Standby}
			}
		}
	}
	var pool, standby []llm.Profile
	var weights []int
	for _, profile := range profiles {
		allocation := allocations[modelRouteProfileKey(profile)]
		if allocation.standby {
			standby = append(standby, profile)
		} else {
			pool = append(pool, profile)
			weights = append(weights, allocation.weight)
		}
	}
	if len(pool) < 2 {
		return append(pool, standby...), nil
	}
	profileID := ""
	if ctx != nil {
		profileID, _ = ctx.Value(modelProfileContextKey{}).(string)
	}
	if profileID == "" {
		if usage := llmUsageFromContext(ctx); usage != nil {
			profileID = usage.event.ProfileID
		}
	}
	if profileID == "" {
		profileID = r.profileConfig("").ID
	}
	key := profileID + "\x00" + group + "\x00" + purpose
	body, _ := json.Marshal(role)
	var signature strings.Builder
	signature.Write(body)
	for _, profile := range pool {
		signature.WriteString(modelRouteProfileKey(profile))
		signature.WriteByte('\n')
	}
	selected := r.nextModelRoute(key, signature.String(), role.RoutingStrategy, weights)
	ordered := make([]llm.Profile, 0, len(profiles))
	ordered = append(ordered, pool[selected])
	for offset := 1; offset < len(pool); offset++ {
		ordered = append(ordered, pool[(selected+offset)%len(pool)])
	}
	return append(ordered, standby...), nil
}

func (r *Runtime) nextModelRoute(key, signature, strategy string, weights []int) int {
	r.modelRoutingMu.Lock()
	defer r.modelRoutingMu.Unlock()
	if r.modelRouteSchedules == nil {
		r.modelRouteSchedules = make(map[string]*modelRouteSchedule)
	}
	state := r.modelRouteSchedules[key]
	if state == nil || state.signature != signature {
		// Removed bots and one-off purpose keys must not grow this cache forever.
		if len(r.modelRouteSchedules) >= 1024 {
			clear(r.modelRouteSchedules)
		}
		state = &modelRouteSchedule{signature: signature, scores: make([]int, len(weights))}
		r.modelRouteSchedules[key] = state
	}
	if strategy == "round_robin" {
		selected := int(state.next % uint64(len(weights)))
		state.next++
		return selected
	}
	selected, total := 0, 0
	for index, weight := range weights {
		total += weight
		state.scores[index] += weight
		if state.scores[index] > state.scores[selected] {
			selected = index
		}
	}
	state.scores[selected] -= total
	return selected
}
