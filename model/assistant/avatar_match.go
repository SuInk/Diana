// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	avatarMatchSize           = 64
	avatarMatchMaximumMembers = 100
	avatarMatchMinimumScore   = 0.82
	avatarMatchMinimumLead    = 0.08
	avatarMatchTimeout        = 10 * time.Second
)

type groupMemberAvatarMatch struct {
	Matched       bool    `json:"matched"`
	UserID        string  `json:"user_id,omitempty"`
	DisplayName   string  `json:"display_name,omitempty"`
	Score         float64 `json:"score,omitempty"`
	RunnerUpScore float64 `json:"runner_up_score,omitempty"`
	Compared      int     `json:"compared"`
}

type avatarMatchCandidate struct {
	member OneBotGroupMemberInfo
	score  float64
}

func (r *Runtime) matchCurrentGroupMemberAvatar(ctx context.Context, event MessageEvent) (groupMemberAvatarMatch, error) {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return groupMemberAvatarMatch{}, fmt.Errorf("头像匹配只能在群聊中使用")
	}
	segment, ok := firstStillImageSegment(event.Segments)
	if !ok {
		return groupMemberAvatarMatch{}, fmt.Errorf("当前消息没有可匹配的图片")
	}
	body, _, err := ReadMessageImageSegment(ctx, segment)
	if err != nil {
		return groupMemberAvatarMatch{}, fmt.Errorf("读取当前图片失败: %w", err)
	}
	config, _, configErr := image.DecodeConfig(bytes.NewReader(body))
	if configErr != nil {
		return groupMemberAvatarMatch{}, fmt.Errorf("解析当前图片失败: %w", configErr)
	}
	shortSide, longSide := min(config.Width, config.Height), max(config.Width, config.Height)
	if shortSide <= 0 || longSide > shortSide*5/4 {
		return groupMemberAvatarMatch{}, nil
	}
	source, err := avatarFingerprint(body)
	if err != nil {
		return groupMemberAvatarMatch{}, fmt.Errorf("解析当前图片失败: %w", err)
	}
	members, err := r.getGroupMemberListForEvent(ctx, event, event.GroupID)
	if err != nil {
		return groupMemberAvatarMatch{}, fmt.Errorf("读取群成员失败: %w", err)
	}
	if len(members) > avatarMatchMaximumMembers {
		members = members[:avatarMatchMaximumMembers]
	}

	matchCtx, cancel := context.WithTimeout(ctx, avatarMatchTimeout)
	defer cancel()
	jobs := make(chan OneBotGroupMemberInfo)
	results := make(chan avatarMatchCandidate, len(members))
	workers := min(8, max(1, len(members)))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer recoverGoroutinePanic("avatar_match.worker")
			defer wg.Done()
			for member := range jobs {
				avatarURL := strings.TrimSpace(member.AvatarURL)
				if avatarURL == "" {
					continue
				}
				separator := "?"
				if strings.Contains(avatarURL, "?") {
					separator = "&"
				}
				// Avatar URLs are stable while their content can change. A daily cache
				// generation keeps matching fresh without downloading every message.
				avatarURL += separator + "diana_avatar_day=" + time.Now().Format("20060102")
				avatarBody, _, fetchErr := readHistoryImageSource(matchCtx, avatarURL, 0)
				if fetchErr != nil {
					continue
				}
				fingerprint, decodeErr := avatarFingerprint(avatarBody)
				if decodeErr != nil {
					continue
				}
				results <- avatarMatchCandidate{member: member, score: avatarSimilarity(source, fingerprint)}
			}
		}()
	}
	go func() {
		defer recoverGoroutinePanic("avatar_match.feed")
		defer close(jobs)
		for _, member := range members {
			select {
			case jobs <- member:
			case <-matchCtx.Done():
				return
			}
		}
	}()
	go func() {
		defer recoverGoroutinePanic("avatar_match.close")
		wg.Wait()
		close(results)
	}()

	candidates := make([]avatarMatchCandidate, 0, len(members))
	for candidate := range results {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(left, right int) bool { return candidates[left].score > candidates[right].score })
	result := groupMemberAvatarMatch{Compared: len(candidates)}
	if len(candidates) == 0 {
		return result, nil
	}
	result.Score = candidates[0].score
	if len(candidates) > 1 {
		result.RunnerUpScore = candidates[1].score
	}
	if result.Score < avatarMatchMinimumScore || result.Score-result.RunnerUpScore < avatarMatchMinimumLead {
		return result, nil
	}
	result.Matched = true
	result.UserID = candidates[0].member.UserID
	result.DisplayName = candidates[0].member.DisplayName()
	return result, nil
}

func firstStillImageSegment(segments []MessageSegment) (MessageSegment, bool) {
	for _, segment := range segments {
		if segment.Type == "image" && !strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame") {
			return segment, true
		}
	}
	return MessageSegment{}, false
}

func avatarFingerprint(body []byte) ([]float64, error) {
	decoded, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	bounds := decoded.Bounds()
	size := min(bounds.Dx(), bounds.Dy())
	if size <= 0 {
		return nil, fmt.Errorf("empty image")
	}
	left := bounds.Min.X + (bounds.Dx()-size)/2
	top := bounds.Min.Y + (bounds.Dy()-size)/2
	values := make([]float64, 0, avatarMatchSize*avatarMatchSize)
	for y := range avatarMatchSize {
		sourceY := top + min(size-1, (y*size+size/(avatarMatchSize*2))/avatarMatchSize)
		for x := range avatarMatchSize {
			sourceX := left + min(size-1, (x*size+size/(avatarMatchSize*2))/avatarMatchSize)
			red, green, blue, _ := decoded.At(sourceX, sourceY).RGBA()
			values = append(values, (0.299*float64(red)+0.587*float64(green)+0.114*float64(blue))/257)
		}
	}
	return values, nil
}

func avatarSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var leftMean, rightMean, absolute float64
	for index := range left {
		leftMean += left[index]
		rightMean += right[index]
		absolute += math.Abs(left[index] - right[index])
	}
	leftMean /= float64(len(left))
	rightMean /= float64(len(right))
	var covariance, leftVariance, rightVariance float64
	for index := range left {
		leftDelta := left[index] - leftMean
		rightDelta := right[index] - rightMean
		covariance += leftDelta * rightDelta
		leftVariance += leftDelta * leftDelta
		rightVariance += rightDelta * rightDelta
	}
	correlation := 0.0
	if denominator := math.Sqrt(leftVariance * rightVariance); denominator > 0 {
		correlation = covariance / denominator
	}
	correlation = max(0, min(1, (correlation+1)/2))
	mae := max(0, 1-absolute/(float64(len(left))*255))
	return correlation*0.8 + mae*0.2
}

func stillImageContentHashes(segments []MessageSegment) map[string]bool {
	hashes := map[string]bool{}
	for _, segment := range segments {
		if segment.Type != "image" || strings.EqualFold(strings.TrimSpace(segment.Data["source_type"]), "video_frame") {
			continue
		}
		if hash := strings.TrimSpace(segment.Data[imageContentSHA256Key]); hash != "" {
			hashes[hash] = true
		}
	}
	return hashes
}

// imageEvidenceNewSinceLastBot is deliberately structural: it compares image
// identities and message roles, never words such as “again” or “different”. A
// router may call repeated text low-value only when the attached evidence is
// actually the same.
func imageEvidenceNewSinceLastBot(event MessageEvent, history []MessageEvent, botAccount string) bool {
	current := stillImageContentHashes(event.Segments)
	if len(current) == 0 {
		return false
	}
	seenBot := false
	previous := map[string]bool{}
	for index := len(history) - 1; index >= 0; index-- {
		item := history[index]
		if strings.TrimSpace(item.MessageID) == strings.TrimSpace(event.MessageID) {
			continue
		}
		if event.Time > 0 && item.Time > 0 && event.Time-item.Time > 5*60 {
			break
		}
		if assistantHistoryEvent(item, botAccount) {
			seenBot = true
		}
		for hash := range stillImageContentHashes(item.Segments) {
			previous[hash] = true
		}
	}
	if !seenBot {
		return false
	}
	for hash := range current {
		if !previous[hash] {
			return true
		}
	}
	return false
}
