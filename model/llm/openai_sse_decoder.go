// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3/packages/ssestream"
)

// openai-go v3.41.0 dispatches an SSE frame even when it only contains a
// comment such as ": keep-alive". The SDK then tries to decode the empty data
// as JSON and aborts an otherwise healthy Responses stream. Register a
// spec-compatible decoder that ignores comment-only and empty-data frames.
func init() {
	ssestream.RegisterDecoder("text/event-stream", newOpenAISSEDecoder)
}

type openAISSEDecoder struct {
	event   ssestream.Event
	body    io.ReadCloser
	scanner *bufio.Scanner
	err     error
}

func newOpenAISSEDecoder(body io.ReadCloser) ssestream.Decoder {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, bufio.MaxScanTokenSize<<9)
	return &openAISSEDecoder{body: body, scanner: scanner}
}

func (d *openAISSEDecoder) Next() bool {
	if d.err != nil {
		return false
	}
	eventType := ""
	var data bytes.Buffer
	hasData := false
	for d.scanner.Scan() {
		line := d.scanner.Bytes()
		if len(line) == 0 {
			// SSE comments and empty data fields are connection activity, not
			// application events. Keep reading until a frame has real data.
			if hasData && len(bytes.TrimSpace(data.Bytes())) > 0 {
				d.event = ssestream.Event{Type: eventType, Data: bytes.Clone(data.Bytes())}
				return true
			}
			eventType = ""
			data.Reset()
			hasData = false
			continue
		}
		name, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			value = nil
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(name) {
		case "":
			// Lines beginning with ':' are SSE comments.
			continue
		case "event":
			eventType = string(value)
		case "data":
			hasData = true
			_, d.err = data.Write(value)
			if d.err == nil {
				d.err = data.WriteByte('\n')
			}
			if d.err != nil {
				return false
			}
		}
	}
	if err := d.scanner.Err(); err != nil {
		d.err = err
	}
	return false
}

func (d *openAISSEDecoder) Event() ssestream.Event { return d.event }
func (d *openAISSEDecoder) Close() error           { return d.body.Close() }
func (d *openAISSEDecoder) Err() error             { return d.err }

// openai-go selects custom SSE decoders by an exact Content-Type lookup. A
// standards-compliant value with parameters (for example charset=utf-8) would
// otherwise bypass the decoder, so normalize only SSE responses for the SDK.
type openAISSETransport struct{ base http.RoundTripper }

func (t openAISSETransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	mediaType, _, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if parseErr == nil && strings.EqualFold(mediaType, "text/event-stream") {
		resp.Header = resp.Header.Clone()
		resp.Header.Set("Content-Type", "text/event-stream")
	}
	return resp, nil
}
