// Local OneBot simulator and transparent DeepSeek usage recorder.
// It never connects to QQ. Bind only to loopback.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type object = map[string]any

type simulator struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	history []object
	next    int64
	output  *json.Encoder
}

var people = []object{
	{"user_id": 71009, "nickname": "阿澄", "card": "阿澄", "role": "member", "level": "20"},
	{"user_id": 71001, "nickname": "小林", "card": "小林", "role": "owner", "level": "15"},
	{"user_id": 71002, "nickname": "可可", "card": "可可", "role": "admin", "level": "18"},
	{"user_id": 71003, "nickname": "阿泽", "card": "阿泽", "role": "member", "level": "12"},
	{"user_id": 71099, "nickname": "Diana", "card": "Diana", "role": "member", "level": "10"},
}

// WebSocket action parameters are decoded into any, whose JSON numbers become
// float64. fmt.Sprint uses scientific notation for large message IDs, so compare the
// decimal representation used by typed inbound events and string parameters.
func identifier(value any) string {
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

func member(id any) object {
	for _, p := range people {
		if identifier(p["user_id"]) == identifier(id) {
			return p
		}
	}
	return object{"user_id": id, "nickname": identifier(id), "role": "member", "level": "1"}
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *simulator) websocket(w http.ResponseWriter, r *http.Request) {
	c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	s.mu.Lock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.conn = c
	_ = c.WriteJSON(object{"time": time.Now().Unix(), "self_id": 71099, "post_type": "meta_event", "meta_event_type": "lifecycle", "sub_type": "connect"})
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.conn == c {
			s.conn = nil
		}
		s.mu.Unlock()
	}()
	for {
		var request struct {
			Action string `json:"action"`
			Params object `json:"params"`
			Echo   any    `json:"echo"`
		}
		if c.ReadJSON(&request) != nil {
			return
		}
		s.mu.Lock()
		data, ok := s.action(request.Action, request.Params)
		response := object{"status": "ok", "retcode": 0, "data": data, "echo": request.Echo}
		if !ok {
			response["status"] = "failed"
			response["retcode"] = 1404
			response["wording"] = "local simulator does not implement this action"
		}
		_ = c.WriteJSON(response)
		s.mu.Unlock()
	}
}

// Called under mu; unsupported actions fail explicitly rather than forging success.
func (s *simulator) action(action string, p object) (any, bool) {
	switch action {
	case "get_status":
		return object{"online": true, "good": true}, true
	case "get_login_info":
		return object{"user_id": 71099, "nickname": "Diana"}, true
	case "get_version_info":
		return object{"app_name": "local-cache-replay", "app_version": "1", "protocol_version": "v11"}, true
	case "get_group_list":
		return []object{{"group_id": 72001, "group_name": "回放 · 开发协作"}, {"group_id": 72002, "group_name": "回放 · 生活分享"}}, true
	case "get_group_info":
		return object{"group_id": p["group_id"], "group_name": groupName(p["group_id"]), "member_count": len(people), "max_member_count": 200}, true
	case "get_group_member_info", "get_stranger_info":
		return member(p["user_id"]), true
	case "get_group_member_list":
		return people, true
	case "get_friend_list":
		return []object{}, true
	case "get_recent_contact":
		return object{"items": []object{}}, true
	case "get_group_msg_history":
		messages := []object{}
		for _, m := range s.history {
			if identifier(m["group_id"]) == identifier(p["group_id"]) {
				messages = append(messages, m)
			}
		}
		if len(messages) > 100 {
			messages = messages[len(messages)-100:]
		}
		return object{"messages": messages}, true
	case "get_msg":
		for _, m := range s.history {
			if identifier(m["message_id"]) == identifier(p["message_id"]) {
				return m, true
			}
		}
		return nil, false
	case "send_group_msg", "send_private_msg", "send_msg", "send_group_forward_msg", "send_private_forward_msg":
		s.next++
		record := object{"time": time.Now().Unix(), "self_id": 71099, "user_id": 71099, "message_id": s.next, "group_id": p["group_id"], "message": p["message"], "sender": member(71099)}
		if p["nodes"] != nil {
			record["nodes"] = p["nodes"]
		}
		s.history = append(s.history, record)
		_ = s.output.Encode(object{"type": "outbound", "action": action, "event": record})
		return object{"message_id": s.next}, true
	case "can_send_image", "can_send_record":
		return object{"yes": false}, true
	case "get_group_root_files":
		return object{"files": []object{}, "folders": []object{}}, true
	}
	return nil, false
}

func groupName(id any) string {
	if identifier(id) == "72002" {
		return "回放 · 生活分享"
	}
	return "回放 · 开发协作"
}

func (s *simulator) emit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonResponse(w, 405, object{"error": "POST required"})
		return
	}
	var input struct {
		GroupID int64  `json:"group_id"`
		UserID  int64  `json:"user_id"`
		Text    string `json:"text"`
		ReplyTo int64  `json:"reply_to"`
		Mention bool   `json:"mention"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		jsonResponse(w, 400, object{"error": "invalid event"})
		return
	}
	if (input.GroupID != 72001 && input.GroupID != 72002) || input.Text == "" {
		jsonResponse(w, 400, object{"error": "fixture group and text required"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		jsonResponse(w, 503, object{"error": "Diana is not connected"})
		return
	}
	s.next++
	segments := []object{}
	if input.ReplyTo != 0 {
		segments = append(segments, object{"type": "reply", "data": object{"id": fmt.Sprint(input.ReplyTo)}})
	}
	if input.Mention {
		segments = append(segments, object{"type": "at", "data": object{"qq": "71099"}})
	}
	segments = append(segments, object{"type": "text", "data": object{"text": input.Text}})
	event := object{"time": time.Now().Unix(), "self_id": 71099, "post_type": "message", "message_type": "group", "sub_type": "normal", "message_id": s.next, "message_seq": s.next, "group_id": input.GroupID, "user_id": input.UserID, "message": segments, "raw_message": input.Text, "sender": member(input.UserID)}
	if err := s.conn.WriteJSON(event); err != nil {
		jsonResponse(w, 502, object{"error": "WebSocket write failed"})
		return
	}
	s.history = append(s.history, event)
	_ = s.output.Encode(object{"type": "inbound", "event": event})
	jsonResponse(w, 200, object{"message_id": s.next})
}

// The response recorder forwards bytes unchanged, including provider-native SSE.
type captureBody struct {
	io.ReadCloser
	mu   sync.Mutex
	buf  bytes.Buffer
	once sync.Once
	done func([]byte, bool)
}

func (b *captureBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	b.mu.Lock()
	if b.buf.Len() < 16<<20 {
		_, _ = b.buf.Write(p[:n])
	}
	b.mu.Unlock()
	if e == io.EOF {
		b.finish(true)
	}
	return n, e
}
func (b *captureBody) Close() error { e := b.ReadCloser.Close(); b.finish(false); return e }
func (b *captureBody) finish(complete bool) {
	b.once.Do(func() {
		b.mu.Lock()
		data := append([]byte(nil), b.buf.Bytes()...)
		b.mu.Unlock()
		b.done(data, complete)
	})
}

func decodedUsage(data []byte, encoding string) (object, bool) {
	if strings.EqualFold(encoding, "gzip") {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, false
		}
		defer reader.Close()
		decoded, err := io.ReadAll(io.LimitReader(reader, 16<<20))
		if err != nil {
			return nil, false
		}
		data = decoded
	} else if encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, false
	}
	return usageIn(data)
}

func usageIn(body []byte) (object, bool) {
	var found object
	consume := func(data []byte) {
		var v object
		if json.Unmarshal(data, &v) != nil {
			return
		}
		if u, ok := v["usage"].(map[string]any); ok {
			found = u
		}
		if response, ok := v["response"].(map[string]any); ok {
			if u, ok := response["usage"].(map[string]any); ok {
				found = u
			}
		}
	}
	consume(body)
	if found == nil {
		for _, line := range bytes.Split(body, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				consume(bytes.TrimSpace(line[5:]))
			}
		}
	}
	if found == nil {
		return nil, false
	}
	known := false
	if _, ok := found["prompt_cache_hit_tokens"]; ok {
		known = true
	}
	for _, field := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if details, ok := found[field].(map[string]any); ok {
			if _, present := details["cached_tokens"]; present {
				known = true
			}
		}
	}
	return found, known
}

type recorder struct {
	mu    sync.Mutex
	out   *json.Encoder
	count atomic.Int64
	limit int64
}

func (a *recorder) record(v any) { a.mu.Lock(); defer a.mu.Unlock(); _ = a.out.Encode(v) }
func (a *recorder) proxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonResponse(w, 405, object{"error": "POST required"})
		return
	}
	sequence := a.count.Add(1)
	if sequence > a.limit {
		jsonResponse(w, 429, object{"error": "local replay request budget reached"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		jsonResponse(w, 413, object{"error": "request too large"})
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	hash := sha256.Sum256(body)
	started := time.Now()
	var request any
	_ = json.Unmarshal(body, &request)
	base := object{"sequence": sequence, "started_at": started.UTC().Format(time.RFC3339Nano), "path": r.URL.Path, "request_sha256": hex.EncodeToString(hash[:]), "request": request}
	proxy := &httputil.ReverseProxy{FlushInterval: -1, Director: func(req *http.Request) {
		req.URL.Scheme = "https"
		req.URL.Host = "api.deepseek.com"
		req.Host = "api.deepseek.com"
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/deepseek")
		req.URL.RawPath = ""
	},
		ModifyResponse: func(resp *http.Response) error {
			encoding := resp.Header.Get("Content-Encoding")
			resp.Body = &captureBody{ReadCloser: resp.Body, done: func(data []byte, complete bool) {
				usage, known := decodedUsage(data, encoding)
				entry := object{}
				for k, v := range base {
					entry[k] = v
				}
				entry["status"] = resp.StatusCode
				entry["duration_ms"] = time.Since(started).Milliseconds()
				entry["usage"] = usage
				entry["cache_field_present"] = known
				entry["response_complete"] = complete
				entry["response_encoding"] = encoding
				entry["response_bytes"] = len(data)
				a.record(entry)
			}}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
			entry := object{}
			for k, v := range base {
				entry[k] = v
			}
			entry["status"] = 502
			entry["duration_ms"] = time.Since(started).Milliseconds()
			entry["error_type"] = fmt.Sprintf("%T", e)
			entry["response_complete"] = false
			a.record(entry)
			jsonResponse(w, 502, object{"error": "local recorder upstream request failed"})
		}}
	proxy.ServeHTTP(w, r)
}

func main() {
	address := flag.String("listen", "127.0.0.1:19090", "loopback address")
	out := flag.String("out", ".tmp-dev/cache-replay-live", "private output directory")
	budget := flag.Int64("max-requests", 240, "physical DeepSeek request ceiling")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("listen must be a numeric loopback address")
	}
	if err = os.MkdirAll(*out, 0700); err != nil {
		log.Fatal(err)
	}
	wire, err := os.OpenFile(filepath.Join(*out, "wire.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer wire.Close()
	traffic, err := os.OpenFile(filepath.Join(*out, "onebot.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer traffic.Close()
	s := &simulator{next: time.Now().UnixMilli(), output: json.NewEncoder(traffic)}
	a := &recorder{out: json.NewEncoder(wire), limit: *budget}
	mux := http.NewServeMux()
	mux.HandleFunc("/onebot", s.websocket)
	mux.HandleFunc("/emit", s.emit)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		connected := s.conn != nil
		s.mu.Unlock()
		jsonResponse(w, 200, object{"connected": connected, "requests": a.count.Load()})
	})
	mux.HandleFunc("/deepseek/", a.proxy)
	server := &http.Server{Addr: *address, Handler: mux, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return context.Background() }}
	log.Printf("Local OneBot/DeepSeek recorder listening at %s (no QQ connection)", *address)
	log.Fatal(server.ListenAndServe())
}
