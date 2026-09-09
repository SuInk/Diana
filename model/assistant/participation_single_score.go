package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SuInk/diana/model/applog"
	"io"
	"strings"
)

type participationRating struct {
	Score  *float64 `json:"score"`
	Reason string   `json:"reason"`
}
type participationRatings struct {
	Relevance     participationRating `json:"relevance"`
	ChatIn        participationRating `json:"chat_in"`
	Answerability participationRating `json:"answerability"`
}

func parseParticipationRatings(raw string) (participationRatings, error) {
	var p participationRatings
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, fmt.Errorf("unexpected trailing output")
	}
	for _, v := range []participationRating{p.Relevance, p.ChatIn, p.Answerability} {
		if v.Score == nil || *v.Score < 0 || *v.Score > 1 || strings.TrimSpace(v.Reason) == "" {
			return p, fmt.Errorf("each rating needs a score from 0 to 1 and a reason")
		}
	}
	return p, nil
}
func validParticipationLevel(s string) bool {
	switch s {
	case "off", "minimal", "low", "medium", "high", "extreme", "always":
		return true
	}
	return false
}
func (p ParticipationPreferences) ratingLevels() (string, string) {
	r, c := p.RelevanceLevel, p.ChatLevel
	if !validParticipationLevel(r) {
		r = "medium"
		if p.replyLevel() == ChatInLevelOff {
			r = "off"
		}
	}
	if !validParticipationLevel(c) {
		c = string(p.replyLevel())
		if c == "max" {
			c = "always"
		}
		if !validParticipationLevel(c) {
			c = "low"
		}
	}
	return r, c
}
func ratingPasses(score float64, level string) bool {
	if level == "off" {
		return false
	}
	if level == "always" {
		return true
	}
	threshold, valid := map[string]float64{"minimal": 0.90, "low": 0.70, "medium": 0.50, "high": 0.30, "extreme": 0.10}[level]
	if !valid {
		return false
	}
	return score > 0 && score >= threshold
}
func (p ParticipationPreferences) ratingsAllow(v participationRatings, cooldown bool) (bool, bool) {
	if v.Answerability.Score == nil {
		return false, false
	}
	if level := p.answerabilityLevel(); level != "off" && !ratingPasses(*v.Answerability.Score, level) {
		return false, false
	}
	r, c := p.ratingLevels()
	// A zero in both dimensions encodes stop/repetition; always does not override it.
	if *v.Relevance.Score == 0 && *v.ChatIn.Score == 0 {
		return false, false
	}
	related := ratingPasses(*v.Relevance.Score, r)
	chat := ratingPasses(*v.ChatIn.Score, c) && cooldown
	return related || chat, !related && chat
}

func (p ParticipationPreferences) answerabilityLevel() string {
	if validParticipationLevel(p.AnswerabilityLevel) {
		return p.AnswerabilityLevel
	}
	return "medium"
}

func (r *Runtime) recordParticipationRatings(ctx context.Context, event MessageEvent, v participationRatings, parsed, allowed bool, cfg BotConfig, raw string) {
	w := r.appLogWriter()
	if w == nil {
		return
	}
	a, b := cfg.participationPreferences().ratingLevels()
	_ = w.AppendLog(ctx, applog.Entry{Kind: applog.KindOperation, Level: applog.LevelInfo, Action: "diana.proactive_reply_route", Message: "模型已完成接话评分", Actor: oneBotEventActor(event), Target: event.MessageID, Metadata: map[string]any{"group_id": event.GroupID, "ratings": v, "relevance_level": a, "chat_level": b, "answerability_level": cfg.participationPreferences().answerabilityLevel(), "parsed": parsed, "allowed": allowed, "reason": event.routingReason, "raw": truncateRunesFromStart(raw, 1000)}})
}
