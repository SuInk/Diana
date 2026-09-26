// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// ErrOutboundOutcomeUnknown 表示请求已经写给了接入端，却没等到回执：消息可能
// 已经发出去了，也可能没有。
//
// 以前超时和「没连上」一样按确定失败处理，退避一分钟后原样重发——而接入端那边
// 其实早就发出去了，只是回执慢了，于是同一张生成图在群里出现两遍。遇到这种错误
// 的一方不许直接重发，要先去确认（见 confirmOutboundOutcome）。
var ErrOutboundOutcomeUnknown = errors.New("diana: outbound outcome unknown")

const (
	// oneBotTextActionTimeout 是纯文本 action 等回执的默认上限。
	oneBotTextActionTimeout = 30 * time.Second
	// oneBotMediaActionTimeout 是带图片、视频、语音、文件的 action 的上限。接入端
	// 要先回源拉取媒体再上传，30 秒经常不够：实测一张生成图 30 秒没回执，其实已经
	// 发出去了。
	oneBotMediaActionTimeout = 90 * time.Second
)

type outboundOutcomeUnknownError struct {
	action string
	cause  error
}

func (e *outboundOutcomeUnknownError) Error() string {
	return fmt.Sprintf("diana: onebot %s sent but no response (outcome unknown): %v", e.action, e.cause)
}

func (e *outboundOutcomeUnknownError) Unwrap() []error {
	return []error{ErrOutboundOutcomeUnknown, e.cause}
}

// oneBotPostTypeMessageSent 是 NapCat、SnowLuma 推送机器人自己发出的消息用的
// post_type。以前三条连接都只收 message/notice/request，这一类整个被丢掉：
// 生产上两天 876 条回复，一条回推都没记到。
const oneBotPostTypeMessageSent = "message_sent"

// oneBotDispatchedPostType 是需要交给运行时处理的事件类型。
func oneBotDispatchedPostType(postType string) bool {
	switch postType {
	case "message", oneBotPostTypeMessageSent, "notice", "request":
		return true
	}
	return false
}

// errOneBotDisconnectedAwaitingResponse 是正向连接在请求写出后断开、等回执的
// 调用被统一唤醒时的错误。请求已经到了接入端，照样算结果不明。
var errOneBotDisconnectedAwaitingResponse = errors.New("diana: onebot websocket disconnected")

// oneBotBridgeSendTimeoutMarkers 是接入端自己等 QQ 发送结果超时回的报错。它是
// 一个明确的失败回执，但接入端只是没等到 QQ 的确认，消息常常随后照样出现在群里。
// 这类按结果不明处理，先确认再说。「发送消息失败」后面带着 result= 的是 QQ 给的
// 拒收码（120 之类，可能是禁言或风控），仍然是确定失败。
var oneBotBridgeSendTimeoutMarkers = []string{
	"timeout: ntevent",
	"发送消息超时",
	"sendmsg timeout",
}

// classifyOneBotSendFailure 把接入端回来的失败再分一次：断线、接入端自己超时的
// 失败都可能已经发出去了，改标成结果不明。
func classifyOneBotSendFailure(action string, err error) error {
	if err == nil || errors.Is(err, ErrOutboundOutcomeUnknown) {
		return err
	}
	if errors.Is(err, errOneBotDisconnectedAwaitingResponse) {
		return &outboundOutcomeUnknownError{action: action, cause: err}
	}
	if !strings.HasPrefix(action, "send_") {
		return err
	}
	message := strings.ToLower(err.Error())
	// 接入端说清了为什么发不出去（不是好友、被拉黑……），那就是确定没发。
	for _, marker := range permanentSendRejectionMarkers {
		if strings.Contains(message, strings.ToLower(marker)) {
			return err
		}
	}
	// 富媒体上传阶段超时发生在真正发消息之前：图还没传上去，消息一定没发。
	for _, marker := range oneBotPreSendUploadMarkers {
		if strings.Contains(message, marker) {
			return err
		}
	}
	for _, marker := range oneBotBridgeSendTimeoutMarkers {
		if strings.Contains(message, marker) {
			return &outboundOutcomeUnknownError{action: action, cause: err}
		}
	}
	if strings.Contains(message, "发送消息失败") && !strings.Contains(message, "result=") {
		return &outboundOutcomeUnknownError{action: action, cause: err}
	}
	return err
}

// oneBotPreSendUploadMarkers 是 NapCat 在上传图片、视频等富媒体时超时或失败的报错
// 特征（NTEvent 的服务名是 RichMedia 一类）。这一步在发消息之前。
var oneBotPreSendUploadMarkers = []string{
	"richmedia",
	"uploadrmfile",
	"upload file",
	"文件上传",
}

// oneBotActionError 是接入端明确回的失败。文本和以前的 errors.New 一样，多带
// 一个 retcode 供分类用。
type oneBotActionError struct {
	retCode int
	message string
}

func (e *oneBotActionError) Error() string { return e.message }

// oneBotInvalidParamsRetCode 是 OneBot v11 标准里的「请求参数错误」：NapCat 在参数
// 校验不过时回它，这条请求原样再发多少遍都一样。
const oneBotInvalidParamsRetCode = 1400

const oneBotHTTPBadRequestRetCode = 400

func oneBotInvalidParamsText(message string) bool {
	message = strings.ToLower(message)
	for _, marker := range []string{"param", "参数", "invalid", "validat"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// errOutboundPermanentRejection 标记「这条消息本身就发不出去」的失败。
var errOutboundPermanentRejection = errors.New("diana: outbound message is invalid and will not be retried")

// permanentOutboundRejectionMarkers 是消息组包、段校验阶段的报错：本地组包时发现
// 的，或者接入端校验消息段时回的。这里匹配的是程序报错原文，不是用户说的话。
//
// 生产日志里这两类都按退避重试了四次、拖了十来分钟才丢弃，一次都不可能成功：
// 「numeric message segment field must contain only an integer」（9/20，模型手写
// 的 at 别名进了数字字段）和「message element "video" must be the only segment
// in a message」（9/24）。
//
// QQ 侧的拒收（比如 result=120，可能是禁言或风控）不在这里，仍走原来的退避。
var permanentOutboundRejectionMarkers = []string{
	"must contain only an integer",
	"must be the only segment",
	"diana: invalid group id",
	"diana: invalid user id",
	"diana: invalid temp session group id",
}

// isPermanentOutboundRejection 判断发送失败是不是消息本身无效、重试不可能成功。
func isPermanentOutboundRejection(err error) bool {
	if err == nil || errors.Is(err, ErrOutboundOutcomeUnknown) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errOutboundPermanentRejection) {
		return true
	}
	var actionErr *oneBotActionError
	if errors.As(err, &actionErr) {
		if actionErr.retCode == oneBotInvalidParamsRetCode {
			return true
		}
		// NapCat 的 HTTP 服务端参数校验不过回 400。400 也可能是别的请求错误，
		// 所以还要看报错里说的是不是参数问题。
		if actionErr.retCode == oneBotHTTPBadRequestRetCode && oneBotInvalidParamsText(actionErr.message) {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, marker := range permanentOutboundRejectionMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// permanentOutboundSendError 把无效消息的失败记一笔（标准输出和应用日志各一份），
// 并标成终态：不退避、不重试，入站队列据此直接落「发送被拒」。
func (r *Runtime) permanentOutboundSendError(event MessageEvent, action string, err error) error {
	r.logOutboundOutcome(event, action, applog.LevelError, "outbound_permanent_rejection", "消息被判定无效，不再重试", err.Error(), nil)
	return &outboundSendError{
		GroupID:            strings.TrimSpace(event.GroupID),
		Cause:              err,
		PermanentRejection: true,
	}
}

// oneBotAwaitResponse 等一个已经写出去的 OneBot 请求的回执。写出去之后才超时或
// 被取消的，一律标成结果不明；错误链里仍保留 ctx 的原始错误，errors.Is 照常认得出。
func oneBotAwaitResponse(ctx context.Context, action string, resultCh <-chan callResult) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, &outboundOutcomeUnknownError{action: action, cause: ctx.Err()}
	case result := <-resultCh:
		return result.data, classifyOneBotSendFailure(action, result.err)
	}
}

// oneBotCallTimeout 按 action 选等回执的上限：消息里带媒体段（或本身就是上传文件）
// 的用 oneBotMediaActionTimeout，其余用 textTimeout。
func oneBotCallTimeout(action string, params map[string]any, textTimeout time.Duration) time.Duration {
	if oneBotActionCarriesMedia(action, params) {
		return oneBotMediaActionTimeout
	}
	return textTimeout
}

func oneBotActionCarriesMedia(action string, params map[string]any) bool {
	if strings.HasPrefix(action, "upload_") {
		return true
	}
	if !strings.HasPrefix(action, "send_") {
		return false
	}
	return oneBotSegmentsCarryMedia(params["message"]) || oneBotSegmentsCarryMedia(params["messages"])
}

func oneBotSegmentsCarryMedia(value any) bool {
	var items []any
	switch typed := value.(type) {
	case []map[string]any:
		for _, item := range typed {
			items = append(items, item)
		}
	case []any:
		items = typed
	default:
		return false
	}
	for _, raw := range items {
		segment, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := segment["type"].(string)
		switch kind {
		case "image", "video", "record", "file":
			return true
		case "node":
			// 合并转发的节点把媒体放在 data.content 里。
			if data, ok := segment["data"].(map[string]any); ok && oneBotSegmentsCarryMedia(data["content"]) {
				return true
			}
		}
	}
	return false
}

// outboundShape 是一条消息在接入端回推里认得出来的样子：去掉空白的正文，加上
// 各类媒体段的个数。回复引用、@、表情这些接入端会改写的段不算进来。
type outboundShape struct {
	text  string
	media string
}

func (s outboundShape) empty() bool { return s.text == "" && s.media == "" }

func newOutboundShape(text string, media map[string]int) outboundShape {
	keys := make([]string, 0, len(media))
	for key, count := range media {
		if count > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+":"+strconv.Itoa(media[key]))
	}
	return outboundShape{text: strings.Join(strings.Fields(text), ""), media: strings.Join(parts, ",")}
}

// outboundStandaloneMedia 是 QQ 不允许和别的内容同条发送的段：接入端会把它们
// 拆成单独的消息，回推也就是好几条。
var outboundStandaloneMedia = map[string]bool{"video": true, "record": true, "file": true}

// outboundConfirmFingerprint 用来在回推和历史里认出「这就是刚才那条」。
type outboundConfirmFingerprint struct {
	// pieces[0] 是整条消息；带独立媒体段时后面跟着接入端拆开后的每一部分，任何
	// 一部分出现都说明请求已经被执行过，不能再发。
	pieces []outboundShape
	// images 是本地能读到的图片来源，确认时才去算 MD5：NapCat 回推的图片文件名
	// 就是内容的 MD5，只在同样正文的候选有好几条时用来挑对的那条。
	images []string
}

type outboundConfirmFingerprintContextKey struct{}

func withOutboundConfirmFingerprint(ctx context.Context, fingerprint outboundConfirmFingerprint) context.Context {
	if len(fingerprint.pieces) == 0 {
		return ctx
	}
	return context.WithValue(ctx, outboundConfirmFingerprintContextKey{}, fingerprint)
}

func outboundConfirmFingerprintFromContext(ctx context.Context) (outboundConfirmFingerprint, bool) {
	fingerprint, ok := ctx.Value(outboundConfirmFingerprintContextKey{}).(outboundConfirmFingerprint)
	return fingerprint, ok && len(fingerprint.pieces) > 0
}

// outboundFingerprintFromSegments 从真正发给接入端的 OneBot 段算指纹。
func outboundFingerprintFromSegments(segments []map[string]any) outboundConfirmFingerprint {
	var text strings.Builder
	media := map[string]int{}
	rest := map[string]int{}
	var standalone []outboundShape
	var images []string
	for _, segment := range segments {
		kind, data := oneBotSegmentTypeData(segment)
		switch kind {
		case "text":
			text.WriteString(data["text"])
		case "image", "video", "record", "file", "forward":
			media[kind]++
			if outboundStandaloneMedia[kind] {
				standalone = append(standalone, newOutboundShape("", map[string]int{kind: 1}))
			} else {
				rest[kind]++
			}
			if kind == "image" {
				images = append(images, data["file"])
			}
		}
	}
	whole := newOutboundShape(text.String(), media)
	if whole.empty() {
		return outboundConfirmFingerprint{}
	}
	pieces := []outboundShape{whole}
	if len(standalone) > 0 {
		if remainder := newOutboundShape(text.String(), rest); !remainder.empty() {
			pieces = append(pieces, remainder)
		}
		pieces = append(pieces, standalone...)
	}
	return outboundConfirmFingerprint{pieces: pieces, images: images}
}

func outboundFingerprintForForward() outboundConfirmFingerprint {
	return outboundConfirmFingerprint{pieces: []outboundShape{newOutboundShape("", map[string]int{"forward": 1})}}
}

func oneBotSegmentTypeData(segment map[string]any) (string, map[string]string) {
	kind, _ := segment["type"].(string)
	data := map[string]string{}
	switch typed := segment["data"].(type) {
	case map[string]string:
		data = typed
	case map[string]any:
		for key, value := range typed {
			data[key] = stringFromAny(value)
		}
	}
	return strings.TrimSpace(kind), data
}

var napCatImageMD5Name = regexp.MustCompile(`^([0-9A-Fa-f]{32})(\.[A-Za-z0-9]+)?$`)

// observedOutboundMessage 是回推或历史里的一条机器人自己的消息。
type observedOutboundMessage struct {
	event  MessageEvent
	shape  outboundShape
	images []string
	at     time.Time
}

func newObservedOutboundMessage(event MessageEvent, at time.Time) observedOutboundMessage {
	var text strings.Builder
	media := map[string]int{}
	var images []string
	for _, segment := range event.Segments {
		switch segment.Type {
		case "text":
			text.WriteString(segment.Data["text"])
		case "image", "video", "record", "file", "forward":
			media[segment.Type]++
			if segment.Type != "image" {
				continue
			}
			if match := napCatImageMD5Name.FindStringSubmatch(strings.TrimSpace(segment.Data["file"])); match != nil {
				images = append(images, strings.ToUpper(match[1]))
			}
		}
	}
	return observedOutboundMessage{event: event, shape: newOutboundShape(text.String(), media), images: images, at: at}
}

func (m observedOutboundMessage) matches(target MessageEvent, fingerprint outboundConfirmFingerprint, since time.Time) bool {
	if strings.TrimSpace(m.event.MessageID) == "" || !sameOutboundTarget(m.event, target, since) {
		return false
	}
	for _, piece := range fingerprint.pieces {
		if piece == m.shape {
			return true
		}
	}
	return false
}

func sameOutboundTarget(observed, target MessageEvent, since time.Time) bool {
	if a, b := strings.TrimSpace(observed.ProfileID), strings.TrimSpace(target.ProfileID); a != "" && b != "" && a != b {
		return false
	}
	if a, b := strings.TrimSpace(observed.SelfID), strings.TrimSpace(target.SelfID); a != "" && b != "" && a != b {
		return false
	}
	switch target.Kind {
	case EventKindGroup:
		return observed.Kind == EventKindGroup && strings.TrimSpace(observed.GroupID) == strings.TrimSpace(target.GroupID)
	case EventKindPrivate:
		if observed.Kind != EventKindPrivate {
			return false
		}
		// 私聊回推的 user_id 是机器人自己，对方在 target_id 里。
		if peer := strings.TrimSpace(observed.TargetID); peer != "" {
			return peer == strings.TrimSpace(target.UserID)
		}
		// 没给 target_id 的实现分不出发给了谁，光凭正文不认：还得是同一个机器人
		// 账号、而且就在这次请求在途的那段时间里发出的。
		if strings.TrimSpace(observed.SelfID) == "" || strings.TrimSpace(target.SelfID) == "" || observed.Time <= 0 {
			return false
		}
		sent := time.Unix(observed.Time, 0)
		return !sent.Before(since.Add(-outboundConfirmClockSkew)) &&
			!sent.After(since.Add(oneBotMediaActionTimeout+outboundConfirmClockSkew))
	}
	return false
}

// outboundClaimKey 标识一条已经认领的消息：同一个 message_id 在不同机器人账号、
// 不同会话里可能各有一条，所以带上账号和会话。
func outboundClaimKey(event MessageEvent, messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return ""
	}
	account := firstNonEmpty(strings.TrimSpace(event.ProfileID), strings.TrimSpace(event.SelfID))
	conversation := strings.TrimSpace(event.UserID)
	if event.Kind == EventKindGroup {
		conversation = strings.TrimSpace(event.GroupID)
	}
	return strings.Join([]string{account, string(event.Kind), conversation, messageID}, "\x00")
}

const (
	defaultOutboundEchoWait       = 2 * time.Minute
	defaultOutboundHistoryTimeout = 15 * time.Second
	outboundConfirmHistoryCount   = 50
	outboundEchoRetention         = 5 * time.Minute
	outboundEchoMaxEntries        = 256
	outboundClaimRetention        = 15 * time.Minute
	outboundConfirmClockSkew      = 30 * time.Second
)

// outboundEchoTracker 记着最近几分钟机器人自己消息的回推，以及哪些 message_id
// 已经认领给了某次发送。零值可用；字段 now、echoWait、historyTimeout 只给测试调。
type outboundEchoTracker struct {
	mu             sync.Mutex
	now            func() time.Time
	echoWait       time.Duration
	historyTimeout time.Duration
	echoes         []observedOutboundMessage
	claimed        map[string]time.Time
	changed        chan struct{}
	// sendsSinceEcho 按机器人账号记：上一次收到回推之后又确认发出了几条。
	// 接入端没开自身消息上报时这个数只涨不落，等两分钟回推纯属浪费。
	sendsSinceEcho map[string]int
}

// outboundEchoSilentAfter 是「连续这么多条确认发出的消息都没有回推」就认定这个
// 账号不上报自身消息，结果不明时直接查历史。
const outboundEchoSilentAfter = 5

func outboundEchoAccount(event MessageEvent) string {
	return firstNonEmpty(strings.TrimSpace(event.ProfileID), strings.TrimSpace(event.SelfID))
}

// echoesSilent 判断这个账号是不是看起来不上报自身消息。
func (t *outboundEchoTracker) echoesSilent(event MessageEvent) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sendsSinceEcho[outboundEchoAccount(event)] >= outboundEchoSilentAfter
}

func (t *outboundEchoTracker) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *outboundEchoTracker) timings() (echoWait, historyTimeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	echoWait, historyTimeout = t.echoWait, t.historyTimeout
	if echoWait <= 0 {
		echoWait = defaultOutboundEchoWait
	}
	if historyTimeout <= 0 {
		historyTimeout = defaultOutboundHistoryTimeout
	}
	return echoWait, historyTimeout
}

// observe 记下一条机器人自己消息的回推，并叫醒正在等回推的确认。
func (t *outboundEchoTracker) observe(event MessageEvent) {
	if strings.TrimSpace(event.MessageID) == "" {
		return
	}
	now := t.clock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	delete(t.sendsSinceEcho, outboundEchoAccount(event))
	t.echoes = append(t.echoes, newObservedOutboundMessage(event, now))
	if len(t.echoes) > outboundEchoMaxEntries {
		t.echoes = append([]observedOutboundMessage(nil), t.echoes[len(t.echoes)-outboundEchoMaxEntries:]...)
	}
	if t.changed != nil {
		close(t.changed)
		t.changed = nil
	}
}

// observed 找已经收到的、发往同一会话的某条消息的回推。
func (t *outboundEchoTracker) observed(target MessageEvent, messageID string) (MessageEvent, bool) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return MessageEvent{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observedLocked(target, messageID)
}

func (t *outboundEchoTracker) observedLocked(target MessageEvent, messageID string) (MessageEvent, bool) {
	messageID = strings.TrimSpace(messageID)
	for _, echo := range t.echoes {
		if messageID == "" || strings.TrimSpace(echo.event.MessageID) != messageID {
			continue
		}
		if target.Kind == EventKindGroup && strings.TrimSpace(echo.event.GroupID) != strings.TrimSpace(target.GroupID) {
			continue
		}
		return echo.event, true
	}
	return MessageEvent{}, false
}

// claim 把一个 message_id 认领给一次已经确定送达的发送，之后的确认不会再拿它
// 当作别的消息的证据。
func (t *outboundEchoTracker) claim(event MessageEvent, messageID string) {
	key := outboundClaimKey(event, messageID)
	if key == "" {
		return
	}
	now := t.clock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	if t.claimed == nil {
		t.claimed = make(map[string]time.Time)
	}
	t.claimed[key] = now
	if t.sendsSinceEcho == nil {
		t.sendsSinceEcho = make(map[string]int)
	}
	// 回推通常比回执先到，到了就已经清零；这里只在它一直不来时累积。
	if _, echoed := t.observedLocked(event, messageID); !echoed {
		t.sendsSinceEcho[outboundEchoAccount(event)]++
	}
}

func (t *outboundEchoTracker) claimedLocked(event MessageEvent, messageID string) bool {
	_, ok := t.claimed[outboundClaimKey(event, messageID)]
	return ok
}

func (t *outboundEchoTracker) pruneLocked(now time.Time) {
	kept := t.echoes[:0]
	for _, echo := range t.echoes {
		if now.Sub(echo.at) <= outboundEchoRetention {
			kept = append(kept, echo)
		}
	}
	t.echoes = kept
	for key, at := range t.claimed {
		if now.Sub(at) > outboundClaimRetention {
			delete(t.claimed, key)
		}
	}
}

// pickLocked 在候选里挑一条没认领过的：同样正文有好几条时优先图片 MD5 对得上
// 的，其次最早的。挑中就顺手认领。
func (t *outboundEchoTracker) pickLocked(target MessageEvent, fingerprint outboundConfirmFingerprint, imageMD5 []string, since time.Time, candidates []observedOutboundMessage) (string, bool) {
	best := -1
	for index, candidate := range candidates {
		if !candidate.matches(target, fingerprint, since) || t.claimedLocked(target, candidate.event.MessageID) {
			continue
		}
		if best < 0 {
			best = index
		}
		if sharesAny(candidate.images, imageMD5) {
			best = index
			break
		}
	}
	if best < 0 {
		return "", false
	}
	messageID := strings.TrimSpace(candidates[best].event.MessageID)
	if t.claimed == nil {
		t.claimed = make(map[string]time.Time)
	}
	t.claimed[outboundClaimKey(target, messageID)] = t.clock()
	return messageID, true
}

// waitForEcho 等发送之后出现的匹配回推，最多等 wait。
func (t *outboundEchoTracker) waitForEcho(ctx context.Context, target MessageEvent, fingerprint outboundConfirmFingerprint, imageMD5 []string, since time.Time, wait time.Duration) (string, bool, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		t.mu.Lock()
		candidates := make([]observedOutboundMessage, 0, len(t.echoes))
		for _, echo := range t.echoes {
			if !echo.at.Before(since) {
				candidates = append(candidates, echo)
			}
		}
		messageID, found := t.pickLocked(target, fingerprint, imageMD5, since, candidates)
		if found {
			t.mu.Unlock()
			return messageID, true, nil
		}
		if t.changed == nil {
			t.changed = make(chan struct{})
		}
		changed := t.changed
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-timer.C:
			return "", false, nil
		case <-changed:
		}
	}
}

func sharesAny(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a != "" && a == b {
				return true
			}
		}
	}
	return false
}

// observeOutboundEcho 在自发消息回推进来时调用，要赶在补全引用、下图这些慢操作之前。
func (r *Runtime) observeOutboundEcho(event MessageEvent) {
	r.outboundEchoes.observe(event)
}

var errOutboundOutcomeUnconfirmed = errors.New("diana: outbound outcome could not be confirmed")

// confirmOutboundOutcome 包住一次发送：结果不明时先确认、不直接重发。
//
//  1. 等最多两分钟机器人自己这条消息的回推；
//  2. 没等到就翻最近 50 条历史找这条；
//  3. 两边都说没有才重发一次。这次重发如果又是结果不明，同样先确认（两次发送
//     任何一次的回推都算数），还是没有就放下这条，不再进指数退避。
//
// 实测的三连发：接入端每次上传图片都超过 30 秒，每次其实都发出去了，Diana 却
// 每次都当失败——30 秒超时加第一次退避 60~72 秒、再 30 秒超时加第二次退避
// 120~144 秒，同一张图发了三遍。所以结果不明的发送一旦进了这里，就再也不回到
// 退避链上；只有确定没发出去的失败（重发时接入端明确报错）才交给外层退避。
//
// 回推和历史都拿不到结论（历史接口也报错）时同样不重发：宁可少一条，也不再
// 刷一遍屏。返回的错误带着 DeliveryDropped，上层不会把整条回复重新生成一遍。
//
// 没有指纹的发送（上传群文件、转发暂存）认不出回推，照旧按确定失败处理。
func (r *Runtime) confirmOutboundOutcome(ctx context.Context, event MessageEvent, action string, call func(context.Context) (map[string]any, error)) (map[string]any, error) {
	fingerprint, confirmable := outboundConfirmFingerprintFromContext(ctx)
	since := r.outboundEchoes.clock()
	result, err := call(ctx)
	for resent := false; ; resent = true {
		if err == nil {
			r.outboundEchoes.claim(event, apiMessageID(result))
			return result, nil
		}
		if !errors.Is(err, ErrOutboundOutcomeUnknown) || !confirmable || ctx.Err() != nil {
			return result, err
		}
		r.logOutboundOutcome(event, action, applog.LevelInfo, "outbound_outcome_unknown", "发送结果不明（超时），等待回执确认", err.Error(), nil)
		// 确认加上可能的一次重发最坏要好几分钟，入站租约只有 10 分钟、生成回复已经
		// 用掉一截。租约到期会被另一个 worker 领走重新生成再发一遍，正是这里要防的
		// 重复，所以按这一轮确认和重发的上限把租约往后推。
		echoWait, historyTimeout := r.outboundEchoes.timings()
		extendInboundLease(ctx, echoWait+historyTimeout+oneBotMediaActionTimeout+time.Minute)
		messageID, source, confirmErr := r.confirmOutboundDelivered(ctx, event, fingerprint, since)
		if messageID != "" {
			r.logOutboundOutcome(event, action, applog.LevelInfo, "outbound_outcome_confirmed", "已确认送达（"+source+"）", "", map[string]any{"outbound_message_id": messageID, "confirmed_by": source})
			return map[string]any{"message_id": messageID}, nil
		}
		if confirmErr != nil && ctx.Err() != nil {
			return nil, err
		}
		if confirmErr != nil || resent {
			message, detail := "发送结果无法确认，不再重发", err.Error()
			if confirmErr != nil {
				detail = confirmErr.Error()
			} else {
				message = "重发后仍未确认送达，不再重发"
			}
			r.logOutboundOutcome(event, action, applog.LevelError, "outbound_outcome_unconfirmed", message, detail, nil)
			cause := fmt.Errorf("%w: %v", errOutboundOutcomeUnconfirmed, err)
			if confirmErr != nil {
				cause = fmt.Errorf("%w: %v (confirmation failed: %v)", errOutboundOutcomeUnconfirmed, err, confirmErr)
			}
			return nil, &outboundSendError{
				GroupID:            strings.TrimSpace(event.GroupID),
				Cause:              cause,
				DeliveryDropped:    true,
				OutcomeUnconfirmed: true,
			}
		}
		r.logOutboundOutcome(event, action, applog.LevelInfo, "outbound_outcome_resend", "确认未送达，重发一次", err.Error(), nil)
		// since 不重置：第一次那条的回推晚到，也照样算送达。
		result, err = call(ctx)
	}
}

// confirmOutboundDelivered 先等回推再查历史。source 是「回执」或「历史」；
// 两边都确定没有时三个返回值都是零值，查不了历史时 err 非空。
func (r *Runtime) confirmOutboundDelivered(ctx context.Context, event MessageEvent, fingerprint outboundConfirmFingerprint, since time.Time) (messageID, source string, err error) {
	imageMD5 := r.outboundImageMD5(fingerprint.images)
	echoWait, historyTimeout := r.outboundEchoes.timings()
	if r.outboundEchoes.echoesSilent(event) {
		// 这个账号最近一直没有回推（接入端多半没开自身消息上报）：已经到了的
		// 照样认，但不再干等两分钟，直接查历史。
		echoWait = 0
	}
	messageID, found, err := r.outboundEchoes.waitForEcho(ctx, event, fingerprint, imageMD5, since, echoWait)
	if err != nil {
		return "", "", err
	}
	if found {
		return messageID, "回执", nil
	}
	historyCtx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	messageID, err = r.findOutboundInHistory(historyCtx, event, fingerprint, imageMD5, since)
	if err != nil {
		return "", "", err
	}
	if messageID != "" {
		return messageID, "历史", nil
	}
	return "", "", nil
}

func (r *Runtime) findOutboundInHistory(ctx context.Context, event MessageEvent, fingerprint outboundConfirmFingerprint, imageMD5 []string, since time.Time) (string, error) {
	session := HistorySession{Kind: event.Kind, ProfileID: event.ProfileID, Platform: event.Platform}
	action, idParam := "get_group_msg_history", "group_id"
	switch event.Kind {
	case EventKindGroup:
		session.ID = strings.TrimSpace(event.GroupID)
	case EventKindPrivate:
		session.ID = strings.TrimSpace(event.UserID)
		action, idParam = "get_friend_msg_history", "user_id"
	default:
		return "", fmt.Errorf("diana: cannot look up %s history", event.Kind)
	}
	data, err := r.callOneBotAPIForEvent(ctx, event, action, map[string]any{
		idParam:           oneBotIDParam(session.ID),
		"count":           outboundConfirmHistoryCount,
		"disable_get_url": true,
	})
	if err != nil {
		return "", err
	}
	// 历史里的时间是接入端的秒级时间，放宽 30 秒免得两边时钟差把刚发的那条挡在外面。
	earliest := since.Add(-outboundConfirmClockSkew).Unix()
	candidates := make([]observedOutboundMessage, 0, outboundConfirmHistoryCount)
	for _, item := range oneBotHistoryItems(data) {
		observed, ok := r.historyEventFromData(session, item)
		if !ok || !r.isSelfMessage(observed) {
			continue
		}
		if observed.Time > 0 && observed.Time < earliest {
			continue
		}
		if session.Kind == EventKindPrivate && strings.TrimSpace(observed.TargetID) == "" {
			// 好友历史本身就只有和这个人的对话。
			observed.TargetID = session.ID
		}
		candidates = append(candidates, newObservedOutboundMessage(observed, time.Unix(observed.Time, 0)))
	}
	t := &r.outboundEchoes
	t.mu.Lock()
	defer t.mu.Unlock()
	messageID, _ := t.pickLocked(event, fingerprint, imageMD5, since, candidates)
	return messageID, nil
}

// outboundImageMD5 算本地能读到的图片的 MD5（大写），和 NapCat 回推的文件名对照。
// 远程 URL 不去下载，读不到的直接跳过：MD5 只用来在多个候选里挑，不是必要条件。
func (r *Runtime) outboundImageMD5(sources []string) []string {
	if len(sources) == 0 {
		return nil
	}
	r.mu.RLock()
	resolver, _ := r.localMedia.(LocalMediaPathResolver)
	r.mu.RUnlock()
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		if encoded, ok := strings.CutPrefix(strings.TrimSpace(source), "base64://"); ok {
			if raw, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				sum := md5.Sum(raw)
				out = append(out, strings.ToUpper(hex.EncodeToString(sum[:])))
			}
			continue
		}
		path := localMediaPath(source)
		if path == "" && resolver != nil {
			path, _ = resolver.ResolveSharedPath(source)
		}
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		hash := md5.New()
		_, err = io.Copy(hash, file)
		_ = file.Close()
		if err == nil {
			out = append(out, strings.ToUpper(hex.EncodeToString(hash.Sum(nil))))
		}
	}
	return out
}

// logOutboundOutcome 同时写标准输出和应用日志。数据库忙的时候应用日志可能写不进
// 去——那次重复发图就是这样，退避日志两秒超时后被吞掉，事后什么也查不到——所以
// 标准输出这一份不能省，写库失败也要在标准输出里留一笔。
func (r *Runtime) logOutboundOutcome(event MessageEvent, action string, level applog.Level, logAction, message, detail string, extra map[string]any) {
	metadata := map[string]any{
		"group_id":      event.GroupID,
		"user_id":       event.UserID,
		"message_id":    event.MessageID,
		"onebot_action": action,
	}
	for key, value := range extra {
		metadata[key] = value
	}
	kind := applog.KindOperation
	if level == applog.LevelError {
		kind = applog.KindError
	}
	target := event.GroupID
	if target == "" {
		target = event.UserID
	}
	r.appendOutboundAppLog(applog.Entry{
		Kind:     kind,
		Level:    level,
		Action:   logAction,
		Message:  message,
		Detail:   detail,
		Actor:    oneBotEventActor(event),
		Target:   target,
		Metadata: metadata,
	})
}

// appendOutboundAppLog 把发送相关的日志先写标准输出，再写应用日志；写库失败时
// 把错误也打到标准输出，不再悄悄吞掉。
func (r *Runtime) appendOutboundAppLog(entry applog.Entry) {
	line := fmt.Sprintf("diana outbound %s: %s target=%s", entry.Action, entry.Message, entry.Target)
	if action := stringFromAny(entry.Metadata["onebot_action"]); action != "" {
		line += " action=" + action
	}
	keys := make([]string, 0, len(entry.Metadata))
	for key := range entry.Metadata {
		switch key {
		case "onebot_action", "group_id", "user_id":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := stringFromAny(entry.Metadata[key]); value != "" {
			line += " " + key + "=" + value
		}
	}
	if entry.Detail != "" {
		line += " detail=" + entry.Detail
	}
	log.Print(line)
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := writer.AppendLog(logCtx, entry); err != nil {
		log.Printf("diana outbound app log %s not persisted: %v", entry.Action, err)
	}
}
