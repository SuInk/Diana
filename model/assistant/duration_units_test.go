// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"
	"time"
)

func TestParseDurationUnits(t *testing.T) {
	cases := []struct {
		raw    string
		months int
		fixed  time.Duration
	}{
		{"30s", 0, 30 * time.Second},
		{"5min", 0, 5 * time.Minute},
		{"2h", 0, 2 * time.Hour},
		{"1h30min", 0, 90 * time.Minute},
		{"1.5h", 0, 90 * time.Minute},
		{"1d", 0, 24 * time.Hour},
		{"1w", 0, 7 * 24 * time.Hour},
		{"2w3d", 0, 17 * 24 * time.Hour},
		{"1m", 1, 0},
		{"6m", 6, 0},
		{"1y", 12, 0},
		{"1y6m", 18, 0},
		{" 1 D ", 0, 24 * time.Hour},
		{"3days", 0, 72 * time.Hour},
		{"1month", 1, 0},
	}
	for _, tc := range cases {
		got, err := parseDurationUnits(tc.raw)
		if err != nil {
			t.Fatalf("%q: %v", tc.raw, err)
		}
		if got.Months != tc.months || got.Fixed != tc.fixed {
			t.Fatalf("%q = %+v, want months=%d fixed=%s", tc.raw, got, tc.months, tc.fixed)
		}
	}
	for _, raw := range []string{"", "5", "h", "1ms", "1.5m", "-1h", "0s", "1q"} {
		if _, err := parseDurationUnits(raw); err == nil {
			t.Fatalf("%q should be rejected", raw)
		}
	}
}

func TestFormatDurationUnitsRoundTrips(t *testing.T) {
	cases := map[time.Duration]string{
		time.Minute:                  "1min",
		90 * time.Minute:             "1h30min",
		24 * time.Hour:               "1d",
		7 * 24 * time.Hour:           "1w",
		365 * 24 * time.Hour:         "52w1d",
		30*time.Second + 2*time.Hour: "2h30s",
	}
	for value, want := range cases {
		got := formatDurationUnits(value)
		if got != want {
			t.Fatalf("format(%s) = %q, want %q", value, got, want)
		}
		parsed, err := parseDurationUnits(got)
		if err != nil || parsed.Months != 0 || parsed.Fixed != value {
			t.Fatalf("parse(%q) = %+v, %v", got, parsed, err)
		}
	}
	if got := (calendarDuration{Months: 18}).String(); got != "1y6m" {
		t.Fatalf("18 months = %q", got)
	}
}

func TestAddMonthsClampsToMonthEnd(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	jan31 := time.Date(2026, time.January, 31, 9, 0, 0, 0, zone)
	if got := addMonthsClamped(jan31, 1); !got.Equal(time.Date(2026, time.February, 28, 9, 0, 0, 0, zone)) {
		t.Fatalf("jan31+1m = %s", got)
	}
	leap := time.Date(2028, time.February, 29, 9, 0, 0, 0, zone)
	if got := addMonthsClamped(leap, 12); !got.Equal(time.Date(2029, time.February, 28, 9, 0, 0, 0, zone)) {
		t.Fatalf("leap+1y = %s", got)
	}
}

// 每月 31 号经过 2 月之后回到 3 月仍是 31 号，不会一路缩成 28 号。
func TestCalendarSlotAfterStaysOnAnchorDay(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	anchor := time.Date(2026, time.January, 31, 9, 0, 0, 0, zone)
	afterFeb := time.Date(2026, time.February, 28, 10, 0, 0, 0, zone)
	if got := calendarSlotAfter(anchor, 1, afterFeb); !got.Equal(time.Date(2026, time.March, 31, 9, 0, 0, 0, zone)) {
		t.Fatalf("slot after feb = %s", got)
	}
	beforeFebSlot := time.Date(2026, time.February, 1, 0, 0, 0, 0, zone)
	if got := calendarSlotAfter(anchor, 1, beforeFebSlot); !got.Equal(time.Date(2026, time.February, 28, 9, 0, 0, 0, zone)) {
		t.Fatalf("slot in feb = %s", got)
	}
	years := calendarSlotAfter(anchor, 12, time.Date(2030, time.June, 1, 0, 0, 0, 0, zone))
	if !years.Equal(time.Date(2031, time.January, 31, 9, 0, 0, 0, zone)) {
		t.Fatalf("yearly slot = %s", years)
	}
}
