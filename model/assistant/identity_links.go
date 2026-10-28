// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// 跨平台认人：同一个实例里的几台机器人分布在不同平台上，同一个人在 QQ 和 Telegram
// 上本来是两个毫不相干的账号。账号关联把它们归到同一个 person_id 下：
//
//   - 主人身份：和某台机器人的主人是同一个人时，在这台机器人上也按主人认（规则见
//     linkedOwnerFor）；判定只经过 OwnerIDForEvent 这一个入口。
//   - 好感度和个人记忆：读当前发言者时把同组账号一起读出来合并，写仍只写当前账号。
//   - 提示词：尾部发言者身份旁边说一句这个人在别的平台上的账号。
//
// 建立关联有两条路：本人在两台机器人的私聊里各走一步验证码（identity_bind.go），
// 或主人用 identity_links 工具直接关联（identity_links_tool.go）。

// IdentityAccount 是一台机器人上的一个账号。三元组 {ProfileID, Platform, UserID}
// 唯一确定一个账号：QQ 官方机器人的 openid 只在签发它的那台机器人下有意义，所以
// 账号必须带上机器人。
type IdentityAccount struct {
	PersonID  string `json:"person_id,omitempty"`
	ProfileID string `json:"profile_id"`
	Platform  string `json:"platform"`
	UserID    string `json:"user_id"`
	// Username 是平台上的用户名（Telegram 的 @handle），DisplayName 是关联时的昵称。
	// 两者都只用于展示，判定只认 UserID。
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	Source      string    `json:"source,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

const (
	// IdentityLinkSourceSelfService 是本人用验证码在两台机器人上各走一步建立的关联。
	IdentityLinkSourceSelfService = "self_service"
	// IdentityLinkSourceOwner 是主人用工具直接建立的关联。
	IdentityLinkSourceOwner = "owner"
	// IdentityLinkGroupLimit 是一个人名下最多关联的账号数。正常人在几个平台上各有
	// 一两个号，再多基本是误操作。
	IdentityLinkGroupLimit = 16
)

var (
	// ErrIdentityLinkConflict 表示两个账号已经分别属于两个不同的人。不自动合并：
	// 一个验证码就把两整组账号并到一起，误操作的代价太大，要先解除一边。
	ErrIdentityLinkConflict = errors.New("两个账号已经分别关联到不同的人，先解除其中一边再关联")
	// ErrIdentityLinkGroupFull 表示这个人名下的账号已经到上限。
	ErrIdentityLinkGroupFull = fmt.Errorf("一个人最多关联 %d 个账号", IdentityLinkGroupLimit)
)

// IdentityLinkStore 持久化账号关联。冲突和上限在存储层的同一个事务里判，避免两条
// 并发的绑定各自读到旧状态。
type IdentityLinkStore interface {
	// ListIdentityLinks 返回全部关联，按 person_id 聚在一起。
	ListIdentityLinks(ctx context.Context) ([]IdentityAccount, error)
	// LinkIdentityAccounts 把 b 关联到 a 所在的人（两者都没有时新建一个人），返回
	// 关联后这个人名下的全部账号。两者已属不同的人时返回 ErrIdentityLinkConflict。
	LinkIdentityAccounts(ctx context.Context, a, b IdentityAccount) ([]IdentityAccount, error)
	// UnlinkIdentityAccount 把账号从它所在的人名下移除；剩下一个账号时整组删掉。
	UnlinkIdentityAccount(ctx context.Context, account IdentityAccount) (bool, error)
	// LinkedIdentityAccounts 返回和这个账号同属一个人的全部账号（含自己），没有关联时为空。
	LinkedIdentityAccounts(ctx context.Context, account IdentityAccount) ([]IdentityAccount, error)
}

// identityLinkCache 是关联表的内存副本。每条入站消息都要判一次主人身份，不能每次
// 都查库；写操作都经过 Runtime，写完整份重载即可（表很小）。
type identityLinkCache struct {
	mu        sync.RWMutex
	loaded    bool
	byAccount map[string]string
	byPerson  map[string][]IdentityAccount
}

func identityAccountKey(profileID, platform, userID string) string {
	return strings.TrimSpace(profileID) + "\x00" + NormalizePlatformID(platform) + "\x00" + strings.TrimSpace(userID)
}

func (a IdentityAccount) key() string { return identityAccountKey(a.ProfileID, a.Platform, a.UserID) }

func (a IdentityAccount) normalized() IdentityAccount {
	a.ProfileID = strings.TrimSpace(a.ProfileID)
	a.Platform = NormalizePlatformID(a.Platform)
	a.UserID = strings.TrimSpace(a.UserID)
	a.Username = strings.TrimPrefix(strings.TrimSpace(a.Username), "@")
	a.DisplayName = strings.TrimSpace(a.DisplayName)
	return a
}

// SetIdentityLinkStore 接入账号关联存储。
func (r *Runtime) SetIdentityLinkStore(store IdentityLinkStore) {
	r.mu.Lock()
	r.identityLinks = store
	r.mu.Unlock()
	r.identityLinkCache.mu.Lock()
	r.identityLinkCache.loaded = false
	r.identityLinkCache.mu.Unlock()
}

func (r *Runtime) identityLinkStore() IdentityLinkStore {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.identityLinks
}

// reloadIdentityLinks 从存储整份重载缓存。失败时缓存保持未加载，下次再试。
func (r *Runtime) reloadIdentityLinks(ctx context.Context) error {
	store := r.identityLinkStore()
	if store == nil {
		return nil
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	items, err := store.ListIdentityLinks(loadCtx)
	if err != nil {
		return err
	}
	byAccount := make(map[string]string, len(items))
	byPerson := make(map[string][]IdentityAccount)
	for _, item := range items {
		item = item.normalized()
		if item.PersonID == "" || item.UserID == "" {
			continue
		}
		byAccount[item.key()] = item.PersonID
		byPerson[item.PersonID] = append(byPerson[item.PersonID], item)
	}
	r.identityLinkCache.mu.Lock()
	r.identityLinkCache.byAccount = byAccount
	r.identityLinkCache.byPerson = byPerson
	r.identityLinkCache.loaded = true
	r.identityLinkCache.mu.Unlock()
	return nil
}

// identityGroup 返回和账号同属一个人的全部账号（含自己）；没有关联时为空。
func (r *Runtime) identityGroup(account IdentityAccount) []IdentityAccount {
	if r == nil || r.identityLinkStore() == nil {
		return nil
	}
	account = account.normalized()
	if account.UserID == "" {
		return nil
	}
	r.identityLinkCache.mu.RLock()
	loaded := r.identityLinkCache.loaded
	r.identityLinkCache.mu.RUnlock()
	if !loaded {
		if err := r.reloadIdentityLinks(context.Background()); err != nil {
			log.Printf("diana identity links load failed: %v", err)
			return nil
		}
	}
	r.identityLinkCache.mu.RLock()
	defer r.identityLinkCache.mu.RUnlock()
	person := r.identityLinkCache.byAccount[account.key()]
	if person == "" {
		return nil
	}
	return append([]IdentityAccount(nil), r.identityLinkCache.byPerson[person]...)
}

// identityAccountForEvent 是这条消息发送者的账号。
func (r *Runtime) identityAccountForEvent(event MessageEvent) IdentityAccount {
	username := ""
	if NormalizePlatformID(event.Platform) == PlatformTelegram {
		username = TelegramOwnerUsername(event.SenderUsername)
	}
	return IdentityAccount{
		ProfileID:   r.eventProfileID(event),
		Platform:    NormalizePlatformID(firstNonEmpty(event.Platform, r.currentPlatform(event))),
		UserID:      strings.TrimSpace(event.UserID),
		Username:    username,
		DisplayName: neutralizeIdentityMarkers(strings.TrimSpace(event.SenderName)),
	}.normalized()
}

// linkedIdentityAccounts 返回当前发言者关联的其他账号（不含自己）。webChat、机器人
// 和自己发出的消息一律没有关联：它们不是平台认证过的真人发言。
func (r *Runtime) linkedIdentityAccounts(event MessageEvent) []IdentityAccount {
	if r == nil || event.webChat || event.SenderIsBot || event.Outbound || strings.TrimSpace(event.UserID) == "" {
		return nil
	}
	self := r.identityAccountForEvent(event)
	group := r.identityGroup(self)
	out := make([]IdentityAccount, 0, len(group))
	for _, account := range group {
		if account.key() != self.key() {
			out = append(out, account)
		}
	}
	return out
}

// platformHasGlobalUserIDs 报告同一平台上不同机器人看到的用户 ID 是不是同一个号。
// QQ 号、Telegram 用户 ID 是全平台唯一的；QQ 官方机器人的 openid、微信 iLink 和
// iMessage 接入端给的标识都只在那台机器人下有意义，不能跨机器人当成同一个人。
func platformHasGlobalUserIDs(platform string) bool {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11, PlatformTelegram:
		return true
	}
	return false
}

// identityAccountIsBot 报告账号是不是某台机器人自己，或被标记成机器人的账号。
// 机器人账号不能被关联：关联后它会继承主人身份，等于让一个自动程序拿到主人权限。
func (r *Runtime) identityAccountIsBot(account IdentityAccount) bool {
	account = account.normalized()
	if account.UserID == "" {
		return false
	}
	for _, profile := range r.ProfileConfigs() {
		if NormalizePlatformID(profile.Platform) != account.Platform {
			continue
		}
		if profile.ID != account.ProfileID && !platformHasGlobalUserIDs(account.Platform) {
			continue
		}
		if strings.TrimSpace(profile.BotAccount) == account.UserID {
			return true
		}
		for _, marked := range profile.MarkedBotIDs {
			if strings.TrimSpace(marked) == account.UserID {
				return true
			}
		}
	}
	return false
}

// accountIsProfileOwner 报告账号是不是这台机器人配置里的主人。只认配置里写死的账号
// （或 Telegram 主人开口时记下的数字 ID），不认关联：不然 A 关联到 B 的主人、C 又
// 关联到 A，主人身份会沿着关联链一路传下去。
func (r *Runtime) accountIsProfileOwner(profile BotConfig, account IdentityAccount) bool {
	owner := strings.TrimSpace(profile.OwnerID)
	if owner == "" || account.UserID == "" || NormalizePlatformID(profile.Platform) != account.Platform {
		return false
	}
	if account.ProfileID != profile.ID && !platformHasGlobalUserIDs(account.Platform) {
		return false
	}
	if owner == account.UserID {
		return true
	}
	if account.Platform == PlatformTelegram && TelegramOwnerUsername(owner) != "" {
		if id, ok := r.telegramOwnerIDs.Load(profile.ID); ok && id.(string) == account.UserID {
			return true
		}
	}
	return false
}

// linkedOwnerFor 判断发送者在这台机器人上是不是因为账号关联而算主人：
//
//   - 这台机器人配了主人：发送者关联的账号里有这台机器人的主人账号，才算主人。
//     别的机器人的主人关联过来不算——配置里写明的主人优先，否则「A 的主人」只要
//     和自己的另一个号关联，就能在 B 上顶替 B 的主人。
//   - 这台机器人没配主人：实例里所有配了主人的机器人，它们的主人账号都在发送者
//     的关联里，才算主人。也就是「整个实例只有一位主人，而且就是这个人」，不会把
//     一台没主人的机器人交给多位主人中的某一位。
//
// 机器人账号、自己发出的消息、WebUI 聊天页一律不算（它们走各自原有的判定）。
func (r *Runtime) linkedOwnerFor(cfg BotConfig, event MessageEvent) bool {
	if r == nil || event.webChat || event.SenderIsBot || event.Outbound || strings.TrimSpace(event.UserID) == "" {
		return false
	}
	probe := event
	probe.linkedOwner = false
	if cfg.IsOwnerEvent(probe) {
		return false
	}
	self := r.identityAccountForEvent(event)
	group := r.identityGroup(self)
	if len(group) < 2 || r.identityAccountIsBot(self) {
		return false
	}
	others := make([]IdentityAccount, 0, len(group))
	for _, account := range group {
		if account.key() != self.key() {
			others = append(others, account)
		}
	}
	hasOwner := func(profile BotConfig) bool {
		for _, account := range others {
			if r.accountIsProfileOwner(profile, account) {
				return true
			}
		}
		return false
	}
	if strings.TrimSpace(cfg.OwnerID) != "" {
		if cfg.ID == "" {
			cfg.ID = self.ProfileID
		}
		return hasOwner(cfg)
	}
	owned := 0
	for _, profile := range r.ProfileConfigs() {
		if strings.TrimSpace(profile.OwnerID) == "" {
			continue
		}
		owned++
		if !hasOwner(profile) {
			return false
		}
	}
	return owned > 0
}

// withLinkedOwner 给入站消息打上「因关联算主人」的标记。只在平台消息进门时调用
// （HandleEvent 和持久化队列取出时），标记是未导出字段，平台消息伪造不了；序列化
// 后会丢，重新取出时再算一次，所以关联解除后不会一直沿用旧结论。
func (r *Runtime) withLinkedOwner(event MessageEvent) MessageEvent {
	event.linkedOwner = false
	if r == nil || r.identityLinkStore() == nil {
		return event
	}
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate && event.Kind != EventKindNotice {
		return event
	}
	event.linkedOwner = r.linkedOwnerFor(r.effectiveConfigForEvent(event), event)
	return event
}

// LinkIdentityAccounts 关联两个账号，并刷新缓存。两个账号都要是真人账号。
func (r *Runtime) LinkIdentityAccounts(ctx context.Context, a, b IdentityAccount) ([]IdentityAccount, error) {
	store := r.identityLinkStore()
	if store == nil {
		return nil, fmt.Errorf("当前没有接入账号关联存储")
	}
	a, b = a.normalized(), b.normalized()
	for _, account := range []IdentityAccount{a, b} {
		if account.ProfileID == "" || account.Platform == "" || account.UserID == "" {
			return nil, fmt.Errorf("账号必须指明机器人、平台和用户 ID")
		}
		if r.identityAccountIsBot(account) {
			return nil, fmt.Errorf("%s 是机器人账号，不能关联", account.UserID)
		}
	}
	if a.key() == b.key() {
		return nil, fmt.Errorf("不能把账号关联到它自己")
	}
	group, err := store.LinkIdentityAccounts(ctx, a, b)
	if reloadErr := r.reloadIdentityLinks(ctx); reloadErr != nil {
		log.Printf("diana identity links reload failed: %v", reloadErr)
		r.identityLinkCache.mu.Lock()
		r.identityLinkCache.loaded = false
		r.identityLinkCache.mu.Unlock()
	}
	return group, err
}

// UnlinkIdentityAccount 解除一个账号的关联，并刷新缓存。
func (r *Runtime) UnlinkIdentityAccount(ctx context.Context, account IdentityAccount) (bool, error) {
	store := r.identityLinkStore()
	if store == nil {
		return false, fmt.Errorf("当前没有接入账号关联存储")
	}
	removed, err := store.UnlinkIdentityAccount(ctx, account.normalized())
	if reloadErr := r.reloadIdentityLinks(ctx); reloadErr != nil {
		log.Printf("diana identity links reload failed: %v", reloadErr)
		r.identityLinkCache.mu.Lock()
		r.identityLinkCache.loaded = false
		r.identityLinkCache.mu.Unlock()
	}
	return removed, err
}

// ListIdentityLinks 返回全部关联，给主人工具和 WebUI 用。
func (r *Runtime) ListIdentityLinks(ctx context.Context) ([]IdentityAccount, error) {
	store := r.identityLinkStore()
	if store == nil {
		return nil, fmt.Errorf("当前没有接入账号关联存储")
	}
	return store.ListIdentityLinks(ctx)
}

// IdentityPerson 是按人聚好的一组账号。
type IdentityPerson struct {
	PersonID string            `json:"person_id"`
	Accounts []IdentityAccount `json:"accounts"`
}

// GroupIdentityLinks 把关联按人聚起来，人按最早关联的时间排。
func GroupIdentityLinks(items []IdentityAccount) []IdentityPerson {
	index := map[string]int{}
	var out []IdentityPerson
	for _, item := range items {
		position, ok := index[item.PersonID]
		if !ok {
			position = len(out)
			index[item.PersonID] = position
			out = append(out, IdentityPerson{PersonID: item.PersonID})
		}
		out[position].Accounts = append(out[position].Accounts, item)
	}
	earliest := func(person IdentityPerson) time.Time {
		var first time.Time
		for _, account := range person.Accounts {
			if first.IsZero() || account.CreatedAt.Before(first) {
				first = account.CreatedAt
			}
		}
		return first
	}
	sort.SliceStable(out, func(i, j int) bool { return earliest(out[i]).Before(earliest(out[j])) })
	return out
}

// identityAccountLabel 把账号写成「Telegram @alice（12345）」这样给人和模型看。
func identityAccountLabel(account IdentityAccount) string {
	platform := account.Platform
	if def, ok := PlatformByID(account.Platform); ok {
		platform = def.Name
	}
	// OneBot 是协议名，对人和模型说「QQ」才对得上号。
	if account.Platform == PlatformOneBotV11 {
		platform = "QQ"
	}
	name := ""
	switch {
	case account.Username != "":
		name = "@" + account.Username
	case account.DisplayName != "":
		name = neutralizeIdentityMarkers(account.DisplayName)
	}
	if name != "" && name != account.UserID {
		return platform + " " + name + "（" + account.UserID + "）"
	}
	return platform + " " + account.UserID
}
