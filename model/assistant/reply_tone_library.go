// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/maibotreply"
	"github.com/google/uuid"
)

const (
	ReplyToneLibraryMaxEntries = 50
	replyToneNameMaxRunes      = 40
	replyToneTextMaxRunes      = 8000
	builtinReplyToneID         = "builtin:default"
)

// ReplyTonePreset is a named final-wording preference. The library is only a
// source for the editor; runtime configuration keeps the copied text.
type ReplyTonePreset struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Text      string    `json:"text"`
	Builtin   bool      `json:"builtin,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type ReplyToneSet struct {
	Presets []ReplyTonePreset `json:"presets"`
}

func BuiltinReplyTones() []ReplyTonePreset {
	return []ReplyTonePreset{{ID: builtinReplyToneID, Name: "自然简短", Text: maibotreply.DefaultReplyStyle, Builtin: true}}
}

func IsBuiltinReplyToneID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), "builtin:")
}

func (preset ReplyTonePreset) normalized() ReplyTonePreset {
	preset.ID = strings.TrimSpace(preset.ID)
	if preset.ID == "" {
		preset.ID = uuid.NewString()
	}
	preset.Name = truncateRunesPlain(strings.TrimSpace(preset.Name), replyToneNameMaxRunes)
	preset.Text = strings.TrimSpace(truncateRunesPlain(strings.ReplaceAll(preset.Text, "\r\n", "\n"), replyToneTextMaxRunes))
	preset.Builtin = false
	return preset
}

func (set ReplyToneSet) WithDefaults() ReplyToneSet {
	seen := make(map[string]struct{}, len(set.Presets))
	presets := make([]ReplyTonePreset, 0, len(set.Presets))
	for _, preset := range set.Presets {
		preset = preset.normalized()
		if preset.Name == "" || preset.Text == "" || IsBuiltinReplyToneID(preset.ID) {
			continue
		}
		if _, ok := seen[preset.ID]; ok {
			continue
		}
		seen[preset.ID] = struct{}{}
		presets = append(presets, preset)
	}
	sort.SliceStable(presets, func(i, j int) bool {
		if !presets[i].UpdatedAt.Equal(presets[j].UpdatedAt) {
			return presets[i].UpdatedAt.After(presets[j].UpdatedAt)
		}
		return strings.ToLower(presets[i].Name) < strings.ToLower(presets[j].Name)
	})
	if len(presets) > ReplyToneLibraryMaxEntries {
		presets = presets[:ReplyToneLibraryMaxEntries]
	}
	return ReplyToneSet{Presets: presets}
}

func (set ReplyToneSet) Save(preset ReplyTonePreset, now time.Time) (ReplyToneSet, ReplyTonePreset, error) {
	if IsBuiltinReplyToneID(preset.ID) {
		return set, ReplyTonePreset{}, errReplyToneBuiltin
	}
	preset = preset.normalized()
	if preset.Name == "" {
		return set, ReplyTonePreset{}, errReplyToneNameRequired
	}
	if preset.Text == "" {
		return set, ReplyTonePreset{}, errReplyToneEmpty
	}
	preset.UpdatedAt = now
	set = set.WithDefaults()
	for i := range set.Presets {
		if set.Presets[i].ID == preset.ID {
			set.Presets[i] = preset
			return set.WithDefaults(), preset, nil
		}
	}
	if len(set.Presets) >= ReplyToneLibraryMaxEntries {
		return set, ReplyTonePreset{}, errReplyToneLibraryFull
	}
	set.Presets = append(set.Presets, preset)
	return set.WithDefaults(), preset, nil
}

func (set ReplyToneSet) Delete(id string) ReplyToneSet {
	id = strings.TrimSpace(id)
	presets := make([]ReplyTonePreset, 0, len(set.Presets))
	for _, preset := range set.Presets {
		if preset.ID != id {
			presets = append(presets, preset)
		}
	}
	return ReplyToneSet{Presets: presets}.WithDefaults()
}

func (set ReplyToneSet) Find(id string) (ReplyTonePreset, bool) {
	for _, preset := range set.Presets {
		if preset.ID == strings.TrimSpace(id) {
			return preset, true
		}
	}
	return ReplyTonePreset{}, false
}

var (
	errReplyToneNameRequired = errors.New("assistant: reply tone name is required")
	errReplyToneEmpty        = errors.New("assistant: reply tone has no content")
	errReplyToneLibraryFull  = errors.New("assistant: reply tone library is full")
	errReplyToneBuiltin      = errors.New("assistant: builtin reply tones are read-only")
)
