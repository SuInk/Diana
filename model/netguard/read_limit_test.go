// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package netguard

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadLimitedReportsOverflowInsteadOfTruncating(t *testing.T) {
	data, err := ReadLimited(strings.NewReader("abcd"), 4)
	if err != nil || string(data) != "abcd" {
		t.Fatalf("exact limit: %q %v", data, err)
	}
	_, err = ReadLimited(strings.NewReader(strings.Repeat("x", 2<<20+1)), 2<<20)
	if !errors.Is(err, ErrResponseTooLarge) || !strings.Contains(err.Error(), "2 MiB") {
		t.Fatalf("overflow err=%v", err)
	}
	if got := formatByteLimit(64 << 10); got != "64 KiB" {
		t.Fatalf("format=%s", got)
	}
}

func TestReadResponseBodyKeepsStatusErrorsForOversizedErrorPages(t *testing.T) {
	body := strings.Repeat("x", 10)
	resp := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(body))}
	if data, err := ReadResponseBody(resp, 4); err != nil || string(data) != "xxxx" {
		t.Fatalf("error page: %q %v", data, err)
	}
	resp = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	if _, err := ReadResponseBody(resp, 4); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("success overflow err=%v", err)
	}
}
