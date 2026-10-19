package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"testing"
)

func TestNumericActionIdentifiers(t *testing.T) {
	// Match the map decoding performed by the actual WebSocket action handler.
	var params object
	if err := json.Unmarshal([]byte(`{"user_id":71001,"group_id":72002,"message_id":1792317629700}`), &params); err != nil {
		t.Fatal(err)
	}
	s := &simulator{history: []object{{"group_id": int64(72002), "message_id": int64(1792317629700)}}}
	person, ok := s.action("get_group_member_info", params)
	if !ok || person.(object)["role"] != "owner" || person.(object)["nickname"] != "小林" {
		t.Fatalf("numeric user ID lost its fixture identity: %v", person)
	}
	group, ok := s.action("get_group_info", params)
	if !ok || group.(object)["group_name"] != "回放 · 生活分享" {
		t.Fatalf("numeric group ID selected the wrong group: %v", group)
	}
	history, ok := s.action("get_group_msg_history", params)
	if !ok || len(history.(object)["messages"].([]object)) != 1 {
		t.Fatalf("numeric group ID lost the platform history: %v", history)
	}
	if _, ok := s.action("get_msg", params); !ok {
		t.Fatal("numeric message ID failed to resolve")
	}
}

func TestUsagePresence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		known      bool
	}{
		{"explicit_zero", `{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":0}}}`, true},
		{"missing_details", `{"usage":{"input_tokens":100}}`, false},
		{"stream_final", "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":64}}}}\n\ndata: [DONE]\n", true},
		{"no_usage", `{"error":{"message":"failed"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, known := usageIn([]byte(tc.body))
			if known != tc.known {
				t.Fatalf("presence=%v want %v", known, tc.known)
			}
		})
	}
}

func TestGzipCapturePreservesWireBytes(t *testing.T) {
	plain := []byte("data: {\"response\":{\"usage\":{\"input_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\ndata: [DONE]\n")
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	_, _ = writer.Write(plain)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	called := 0
	body := &captureBody{ReadCloser: io.NopCloser(bytes.NewReader(zipped.Bytes())), done: func(data []byte, complete bool) {
		called++
		usage, known := decodedUsage(data, "gzip")
		if !complete || !known || usage["input_tokens"] != float64(100) {
			t.Fatalf("capture complete=%v known=%v usage=%v", complete, known, usage)
		}
	}}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	if !bytes.Equal(got, zipped.Bytes()) || called != 1 {
		t.Fatalf("modified wire response or duplicated capture: calls=%d", called)
	}
}

func TestInterruptedCaptureIsNotComplete(t *testing.T) {
	complete := true
	body := &captureBody{ReadCloser: io.NopCloser(bytes.NewReader([]byte("unfinished"))), done: func(_ []byte, ok bool) { complete = ok }}
	_, _ = body.Read(make([]byte, 2))
	_ = body.Close()
	if complete {
		t.Fatal("interrupted response marked complete")
	}
}
