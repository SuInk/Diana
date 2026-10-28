// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 自助绑定账号：本人在一台机器人的私聊里发「绑定账号」拿到验证码，再到另一台机器人
// 的私聊里发「绑定账号 123456」。两条消息都是平台认证过的发送者发来的，验证码又只
// 回在第一个账号的私聊里，能在第二个账号上念出它，就证明两个账号在同一个人手里。
//
// 群里一律不发码：验证码出现在群里就等于公开了。群里有人发「绑定账号 123456」时，
// 那个码当场作废。
//
// 兑码失败有次数上限（每个账号、全局各一个窗口），六位数验证码不能靠换号硬猜：
// 猜中别人的码等于把别人的账号关联到自己名下，对方若是主人，后果就是主人权限。

const (
	identityBindCodeTTL = 10 * time.Minute
	// identityBindStateLimit 是验证码和失败计数各自的条数上限，满了整份清空重来。
	identityBindStateLimit = 4096
	// identityBindAccountFailureLimit 是一个账号在一个窗口内最多输错几次。
	identityBindAccountFailureLimit = 5
	// identityBindGlobalFailureLimit 是全实例一个窗口内最多输错几次，挡换号硬猜。
	// 触顶后这个窗口里谁都兑不了码，代价只是稍等一会儿再试。
	identityBindGlobalFailureLimit = 100
)

var (
	identityBindIssuePattern  = regexp.MustCompile(`^/?绑定账号$`)
	identityBindRedeemPattern = regexp.MustCompile(`^/?绑定账号\s*(\d{6})$`)
)

type identityBindCode struct {
	account   IdentityAccount
	expiresAt time.Time
}

type identityBindState struct {
	mu    sync.Mutex
	codes map[string]identityBindCode
	// failures 按账号记输错次数，globalFailures 是全实例的；窗口从第一次输错起算。
	failures       map[string]int
	globalFailures int
	windowStart    time.Time
}

func identityBindText(event MessageEvent) string {
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	return text
}

// handleIdentityBind 处理「绑定账号」和「绑定账号 123456」两条命令，处理了就返回 true。
func (r *Runtime) handleIdentityBind(ctx context.Context, event MessageEvent) bool {
	if r == nil || event.webChat || event.SenderIsBot || event.Outbound || strings.TrimSpace(event.UserID) == "" {
		return false
	}
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate {
		return false
	}
	text := identityBindText(event)
	issue := identityBindIssuePattern.MatchString(text)
	redeem := identityBindRedeemPattern.FindStringSubmatch(text)
	if !issue && redeem == nil {
		return false
	}
	if r.identityLinkStore() == nil {
		return false
	}
	if event.Kind == EventKindGroup {
		revoked := redeem != nil && r.revokeIdentityBindCode(redeem[1])
		// 没冲机器人说的群消息不回话，码照样作废。
		if !event.ToMe {
			return false
		}
		reply := "账号绑定只能在私聊里进行：请私聊我发「绑定账号」。"
		if revoked {
			reply = "这个验证码已经发到群里，已作废。账号绑定只能在私聊里进行：请私聊我重新发「绑定账号」。"
		}
		r.replyIdentityBind(ctx, event, "群里不发绑定验证码", reply)
		return true
	}
	self := r.identityAccountForEvent(event)
	if r.identityAccountIsBot(self) {
		r.replyIdentityBind(ctx, event, "机器人账号不能关联", "机器人账号不能关联到别的账号。")
		return true
	}
	if issue {
		reply := "生成验证码失败，请稍后再试。"
		if code, err := r.issueIdentityBindCode(self); err == nil {
			reply = fmt.Sprintf("绑定验证码：%s（%d 分钟内有效）。请用你在另一个平台上的账号私聊那边的机器人，发「绑定账号 %s」，两个账号就会被认作同一个人。不要把验证码发到群里或告诉别人。", code, int(identityBindCodeTTL.Minutes()), code)
		}
		r.replyIdentityBind(ctx, event, "跨平台账号绑定验证码", reply)
		return true
	}
	reply := r.redeemIdentityBindCode(ctx, event, self, redeem[1])
	r.replyIdentityBind(ctx, event, "跨平台账号绑定", reply)
	return true
}

func (r *Runtime) replyIdentityBind(ctx context.Context, event MessageEvent, reason, reply string) {
	record := r.decisionEventRecord(event, "[绑定账号]", "replied")
	record.Reason = reason
	r.record(record)
	_ = r.sendOutgoing(ctx, event, OutgoingMessage{Text: reply, LiteralText: true})
}

// issueIdentityBindCode 给账号生成验证码，它之前拿到的码同时作废。
func (r *Runtime) issueIdentityBindCode(account IdentityAccount) (string, error) {
	state := &r.identityBind
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.codes == nil || len(state.codes) >= identityBindStateLimit {
		state.codes = map[string]identityBindCode{}
	}
	for code, pending := range state.codes {
		if pending.account.key() == account.key() {
			delete(state.codes, code)
		}
	}
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		if _, taken := state.codes[code]; taken {
			continue
		}
		state.codes[code] = identityBindCode{account: account, expiresAt: r.clock().Add(identityBindCodeTTL)}
		return code, nil
	}
}

// revokeIdentityBindCode 作废一个验证码，报告它原来是否有效。
func (r *Runtime) revokeIdentityBindCode(code string) bool {
	state := &r.identityBind
	state.mu.Lock()
	defer state.mu.Unlock()
	pending, ok := state.codes[code]
	delete(state.codes, code)
	return ok && r.clock().Before(pending.expiresAt)
}

// takeIdentityBindCode 取出验证码对应的账号。码只能用一次；输错计入失败次数。
func (r *Runtime) takeIdentityBindCode(redeemer IdentityAccount, code string) (IdentityAccount, error) {
	state := &r.identityBind
	state.mu.Lock()
	defer state.mu.Unlock()
	now := r.clock()
	if state.windowStart.IsZero() || !now.Before(state.windowStart.Add(identityBindCodeTTL)) {
		state.windowStart = now
		state.failures = map[string]int{}
		state.globalFailures = 0
	}
	if state.failures[redeemer.key()] >= identityBindAccountFailureLimit || state.globalFailures >= identityBindGlobalFailureLimit {
		return IdentityAccount{}, errors.New("验证码输错次数太多，请过十分钟再试")
	}
	pending, ok := state.codes[code]
	if !ok || !now.Before(pending.expiresAt) {
		delete(state.codes, code)
		if state.failures == nil || len(state.failures) >= identityBindStateLimit {
			state.failures = map[string]int{}
		}
		state.failures[redeemer.key()]++
		state.globalFailures++
		return IdentityAccount{}, errors.New("验证码不对或已过期，请在另一台机器人的私聊里重新发「绑定账号」获取")
	}
	delete(state.codes, code)
	return pending.account, nil
}

func (r *Runtime) redeemIdentityBindCode(ctx context.Context, event MessageEvent, self IdentityAccount, code string) string {
	issuer, err := r.takeIdentityBindCode(self, code)
	if err != nil {
		return err.Error()
	}
	if issuer.key() == self.key() {
		return "这个验证码是发给这个账号的，要用你在另一个平台上的账号私聊那边的机器人发「绑定账号 " + code + "」。验证码已作废，请重新获取。"
	}
	if issuer.ProfileID == self.ProfileID {
		return "两个账号要在不同的机器人上各走一步：请在另一台机器人的私聊里发「绑定账号 " + code + "」。验证码已作废，请重新获取。"
	}
	issuer.Source, self.Source = IdentityLinkSourceSelfService, IdentityLinkSourceSelfService
	if _, err := r.LinkIdentityAccounts(ctx, issuer, self); err != nil {
		if errors.Is(err, ErrIdentityLinkConflict) || errors.Is(err, ErrIdentityLinkGroupFull) {
			return "没有关联：" + err.Error() + "。"
		}
		return "关联失败，请稍后再试。"
	}
	reply := "已关联：你在" + identityAccountLabel(issuer) + "上的账号和这个账号现在被认作同一个人，好感度和个人记忆会合在一起看。"
	cfg := r.effectiveConfigForEvent(event)
	probe := event
	probe.linkedOwner = false
	if !cfg.IsOwnerEvent(probe) && r.linkedOwnerFor(cfg, event) {
		reply += "你在这台机器人上也会被认作主人。"
	}
	return reply
}
