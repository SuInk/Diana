// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package qqcall

import "testing"

func TestPeerFromLogLine(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"06:15:30 WARN               [MsgPush] message had content but decoded to 0 elements — missing decoder? (kind=friend_message seq=10338 from=12345 msgType=529/11): elems=[] msgContent=0a14", "12345"},
		{"06:15:30 WARN               [MsgPush] c2c message omitted NT sequence; keeping non-authoritative client sequence (kind=friend_message from=12345 clientSeq=10338 time=1793312130)", ""},
		{"06:15:30 WARN [MsgPush] message had content (kind=friend_message seq=1 from=12345 msgType=529/4): elems=[]", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := PeerFromLogLine(c.line); got != c.want {
			t.Errorf("PeerFromLogLine(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestParseCallerText(t *testing.T) {
	name, incoming := ParseCallerText("\n Win ter \n邀请你语音通话\n")
	if name != "Winter" || !incoming {
		t.Fatalf("got %q incoming=%v", name, incoming)
	}
	name, incoming = ParseCallerText("Winter\n")
	if name != "Winter" || incoming {
		t.Fatalf("outgoing window: got %q incoming=%v", name, incoming)
	}
	if name, _ := ParseCallerText("  \n"); name != "" {
		t.Fatalf("blank text gave name %q", name)
	}
}

func TestNameMatches(t *testing.T) {
	if !NameMatches("win ter", "", "Winter") {
		t.Fatal("case and spaces should not matter")
	}
	if NameMatches("Winter2", "Winter") {
		t.Fatal("a longer name must not match")
	}
	if NameMatches("", "") {
		t.Fatal("empty names never match")
	}
}
