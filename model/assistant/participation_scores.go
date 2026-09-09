package assistant

import (
	"fmt"
	"strings"
)

const participationContentThreshold = 60

func participationThreshold(value *int) int {
	if value == nil {
		return participationContentThreshold
	}
	return max(0, min(100, *value))
}

func (p ParticipationPreferences) relevanceThreshold() int {
	return participationThreshold(p.RelevanceThreshold)
}

func (p ParticipationPreferences) substanceThreshold() int {
	return participationThreshold(p.SubstanceThreshold)
}

type participationDimensionScore struct {
	Score  *int   `json:"score"`
	Reason string `json:"reason"`
}

type participationScores struct {
	Relevance participationDimensionScore `json:"relevance"`
	Substance participationDimensionScore `json:"substance"`
}

func (scores *participationScores) valid() bool {
	if scores == nil {
		return false
	}
	for _, value := range []participationDimensionScore{scores.Relevance, scores.Substance} {
		if value.Score == nil || *value.Score < 0 || *value.Score > 100 || strings.TrimSpace(value.Reason) == "" {
			return false
		}
	}
	return true
}

func (scores *participationScores) description() string {
	if !scores.valid() {
		return "评分缺失或无效"
	}
	return fmt.Sprintf("相关度 %d：%s；内容实质性 %d：%s", *scores.Relevance.Score, strings.TrimSpace(scores.Relevance.Reason), *scores.Substance.Score, strings.TrimSpace(scores.Substance.Reason))
}
