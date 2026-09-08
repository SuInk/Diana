package assistant

import (
	"fmt"
	"strings"
)

const participationScoreThreshold = 60.0

type participationDimensionScore struct {
	Score  *int   `json:"score"`
	Reason string `json:"reason"`
}

type participationScores map[string]participationDimensionScore

var participationDimensions = []struct{ Key, Label string }{
	{"desire", "主动参与"},
	{"social", "闲聊接话"},
	{"followup", "连续跟进"},
	{"restraint", "介入克制"},
	{"information", "新增信息要求"},
}

func (scores participationScores) valid() bool {
	if len(scores) != len(participationDimensions) {
		return false
	}
	for _, dimension := range participationDimensions {
		value, ok := scores[dimension.Key]
		if !ok || value.Score == nil || *value.Score < 0 || *value.Score > 100 || strings.TrimSpace(value.Reason) == "" {
			return false
		}
	}
	return true
}

func (scores participationScores) average() float64 {
	total := 0
	for _, dimension := range participationDimensions {
		total += *scores[dimension.Key].Score
	}
	return float64(total) / float64(len(participationDimensions))
}

func (scores participationScores) description() string {
	parts := make([]string, 0, len(participationDimensions))
	for _, dimension := range participationDimensions {
		value := scores[dimension.Key]
		parts = append(parts, fmt.Sprintf("%s %d：%s", dimension.Label, *value.Score, strings.TrimSpace(value.Reason)))
	}
	return strings.Join(parts, "；")
}
