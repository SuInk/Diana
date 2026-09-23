<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div ref="viewRoot" class="assistant-view">
    <header ref="viewHeader" class="view-header">
      <div class="view-title">
        <button v-if="page === 'edit'" class="btn ghost back-link" type="button" @click="leaveEditor">
          <ArrowLeft :size="16" aria-hidden="true" />
          机器人列表
        </button>
        <div>
          <!-- 列表态的标题就是「机器人」，跟顶栏重复；编辑态是机器人名字，要留。 -->
          <h2 v-if="page !== 'list'">{{ form?.name || "新机器人" }}</h2>
          <p>{{ page === "list" ? "多机器人配置、平台接入与运行管理" : `${platformName(form?.platform)} · 机器人配置` }}</p>
        </div>
      </div>
      <div v-if="page === 'edit'" class="view-actions">
        <!-- 运行时的启停挪到页头：机器人连不上的时候，人正盯着的是这一页顶上的名字
             和状态，启停却埋在右侧卡片里，得先找一遍。回补消息搬去了运行记录——
             它要看的是「哪几条消息漏了」，那些内容在记录页，不在配置页。 -->
        <button
          v-if="status"
          class="btn"
          :class="status.running ? 'danger' : 'primary'"
          type="button"
          :disabled="busy"
          :title="status.running ? '停止运行时，所有机器人都会断开' : '启动运行时，已启用的机器人开始收消息'"
          @click="toggleRuntime(!status.running)"
        >
          <PowerOff v-if="status.running" :size="15" aria-hidden="true" />
          <Power v-else :size="15" aria-hidden="true" />
          {{ status.running ? "停止运行" : "启动运行" }}
        </button>
        <button class="btn primary" type="button" :disabled="busy || !form" @click="save">
          <Save :size="15" aria-hidden="true" />
          保存配置
        </button>
      </div>
      <!-- 批量启停作用于列表里的所有机器人，不属于某一台的配置，所以放在列表页
           页头；编辑页只保留该机器人自己的保存和回补动作。 -->
      <div v-else class="view-actions">
        <button
          v-if="profiles.length > 0"
          class="btn"
          :class="allProfilesEnabled ? 'danger' : 'primary'"
          type="button"
          :disabled="busy"
          @click="toggleAllProfiles(!allProfilesEnabled)"
        >
          <Power v-if="!allProfilesEnabled" :size="15" aria-hidden="true" />
          <PowerOff v-else :size="15" aria-hidden="true" />
          {{ allProfilesEnabled ? "全部停止" : "全部启用" }}
        </button>
      </div>
    </header>

    <div v-if="page === 'list' && (form || loading)" class="stack">
      <!-- 平台筛选：默认全选，点标签可以只看某个平台。 -->
      <div v-if="!form" class="platform-filters" role="status" aria-label="正在加载平台筛选">
        <SkeletonBlock v-for="width in ['76px', '68px', '105px']" :key="width" :width="width" height="34px" rounded />
      </div>
      <div v-else-if="platformFilters.length > 1" class="platform-filters">
        <button
          type="button"
          class="platform-filter"
          :class="{ active: allPlatformsSelected }"
          @click="selectAllPlatforms"
        >
          全部
          <span class="platform-filter-count">{{ profiles.length }}</span>
        </button>
        <button
          v-for="filter in platformFilters"
          :key="filter.category"
          type="button"
          class="platform-filter"
          :class="{ active: !allPlatformsSelected && selectedPlatforms.includes(filter.category) }"
          @click="togglePlatform(filter.category)"
        >
          {{ filter.label }}
          <span class="platform-filter-count">{{ filter.count }}</span>
        </button>
      </div>

      <section class="settings-band" aria-label="消息互通设置">
        <span class="settings-band-icon"><Shuffle :size="17" aria-hidden="true" /></span>
        <div class="settings-band-copy">
          <strong>消息互通</strong>
          <span :class="{ 'skeleton skeleton-text': !form }" :aria-hidden="!form || undefined">{{ relaySummary }}</span>
        </div>
        <button class="btn small" type="button" :disabled="busy || !form" @click="relayManagerOpen = true">
          <Settings2 :size="13" aria-hidden="true" />
          配置
        </button>
      </section>

      <LoadingSkeleton v-if="!form" kind="bots" :count="2" label="正在加载机器人" />
      <div v-else class="bot-profile-grid">
        <article
          v-for="profile in filteredProfiles"
          :key="profile.id ?? profile.name"
          class="bot-profile-tile"
        >
          <button class="bot-profile-select" type="button" :disabled="busy" @click="editProfile(profile)">
            <span class="bot-profile-head">
              <span class="bot-profile-icon">
                <Bot :size="20" aria-hidden="true" />
              </span>
              <span class="bot-profile-state" :class="profileState(profile).tone">
                {{ profileState(profile).label }}
              </span>
            </span>
            <span class="bot-profile-name">{{ profile.name || "未命名机器人" }}</span>
            <span class="bot-profile-meta">
              <span class="platform-chip">{{ platformName(profile.platform) }}</span>
              <span class="bot-profile-account">{{ connectionAccount(profile) || accountPlaceholder(profile) }}</span>
              <span v-if="profile.connection_profile_id" class="platform-chip bot-profile-connection">复用 · {{ connectionSourceName(profile) }}</span>
              <span v-else-if="connectionUsers(profile).length" class="platform-chip bot-profile-connection">连接被 {{ connectionUsers(profile).length }} 台复用</span>
            </span>
          </button>
          <label class="switch bot-profile-enable" :title="profile.enabled ? '停用这台机器人' : '启用这台机器人'">
            <input
              type="checkbox"
              :checked="profile.enabled"
              :disabled="busy || profileEnabledToggling === profile.id"
              :aria-label="`启用或停用 ${profile.name || '未命名机器人'}`"
              @change="toggleProfileEnabled(profile, ($event.target as HTMLInputElement).checked)"
            />
            <span class="track" aria-hidden="true"></span>
          </label>
          <div class="bot-profile-actions">
            <button class="btn small" type="button" :disabled="busy" @click="editProfile(profile)">
              <Settings2 :size="14" aria-hidden="true" />
              配置
            </button>
            <button class="btn small" type="button" :disabled="busy" @click="beginCopyProfile(profile)">
              <Copy :size="14" aria-hidden="true" />
              复制配置
            </button>
            <button
              class="btn small danger"
              type="button"
              :disabled="busy || profiles.length <= 1"
              :title="profiles.length <= 1 ? '至少保留一个机器人' : '删除机器人'"
              @click="removeProfile(profile)"
            >
              <Trash2 :size="14" aria-hidden="true" />
              删除
            </button>
          </div>
        </article>

        <button class="bot-profile-add" type="button" :disabled="busy" @click="platformPickerOpen = true">
          <Plus :size="18" aria-hidden="true" />
          新增机器人
        </button>
      </div>

      <EmptyState
        v-if="form && filteredProfiles.length === 0"
        title="没有匹配的机器人"
        hint="当前筛选条件下没有机器人，点「全部」查看所有。"
      />
    </div>

    <div v-if="form && page === 'edit'" class="grid-main-side">
      <div class="stack">
        <nav class="editor-tabs" role="tablist" aria-label="机器人配置分区">
          <button
            v-for="tab in editorTabs"
            :key="tab.key"
            class="editor-tab"
            :class="{ active: editorTab === tab.key }"
            type="button"
            role="tab"
            :aria-selected="editorTab === tab.key"
            @click="editorTab = tab.key"
          >
            {{ tab.label }}
          </button>
        </nav>

        <div v-show="editorTab === 'access'" class="stack">
          <!-- 接入 -->
          <section class="card">
            <div class="card-header">
              <h2>{{ platformName(form.platform) }} 接入</h2>
              <span class="card-sub">通过 {{ platformProtocol(form.platform) }} 连接</span>
            </div>
            <div class="card-body stack">
              <div v-if="creating && copiedFrom" class="stack" style="gap: 8px" role="status">
                <p class="hint">已从「{{ copiedFrom.name || '未命名机器人' }}」复制人设、模型和行为设置。保存后独立修改，不会跟随来源同步；接入方式在下方另选。</p>
                <button v-if="copiedConnectionSource && !form.connection_profile_id" class="btn small" type="button" @click="form.connection_profile_id = copiedConnectionSource">同时复用来源连接</button>
              </div>
              <!-- 名称放在最前：先确认这是哪个机器人，再选接入平台、填接入凭据。 -->
              <div class="field">
                <label for="bot-name">机器人名称</label>
                <input id="bot-name" v-model="form.name" class="input" placeholder="例如：主群助手、客服机器人" />
                <span class="hint">用于控制台区分多个机器人，不会自动修改账号昵称。</span>
              </div>
              <div class="field wide">
                <label>接入平台</label>
                <AppSelect
                  :model-value="form.platform ?? ''"
                  :options="platformOptions"
                  @update:model-value="(value) => { if (form) { form.platform = value; form.connection_profile_id = ''; } }"
                />
                <span class="hint">{{ platformDescription(form.platform) }}</span>
              </div>
              <!-- 按平台和传输方式展示连接地址与鉴权凭据。 -->
              <template v-if="isOneBotPlatform">
                <div class="field">
                  <label>连接来源</label>
                  <AppSelect :model-value="form.connection_profile_id || ''" :options="connectionOptions" @update:model-value="(value) => { if (form) form.connection_profile_id = value; }" />
                  <span class="hint">复用已有连接即可免填地址和 Token，来源连接的修改会自动同步。</span>
                </div>
                <div v-if="form.connection_profile_id" class="hint">
                  正在复用「{{ connectionSourceName(form) }}」的连接，使用同一个平台账号。
                  消息会交给各台启用的机器人，各自按人设和行为配置决定是否回复，可能产生多条回复。
                  停用来源机器人的回复不会断开复用连接；仍有机器人复用时不能删除来源。
                </div>
                <template v-else>
                <div class="field">
                  <label for="bot-onebot-transport">连接方式</label>
                  <select id="bot-onebot-transport" :value="form.onebot_transport || 'reverse_ws'" @change="form.onebot_transport = ($event.target as HTMLSelectElement).value as BotProfileConfig['onebot_transport']" class="input">
                    <option value="reverse_ws">反向 WebSocket</option>
                    <option value="forward_ws">正向 WebSocket</option>
                    <option value="http">HTTP API + HTTP 事件上报</option>
                  </select>
                </div>
                <div v-if="!form.onebot_transport || form.onebot_transport === 'reverse_ws'" class="field">
                  <label for="bot-onebot-endpoint">回连地址</label>
                  <div class="input-group">
                    <input
                      id="bot-onebot-endpoint"
                      v-model="form.onebot_reverse_ws_endpoint"
                      class="input mono"
                      placeholder="ws://127.0.0.1:18080/onebot/v11/ws"
                      autocomplete="off"
                    />
                    <button class="btn icon-only" type="button" aria-label="复制地址" @click="copyEndpoint">
                      <Copy :size="14" aria-hidden="true" />
                    </button>
                  </div>
                  <span class="hint">填写接入端实际可访问的地址；自定义路径需要反向代理转发到 /onebot/v11/ws。</span>
                </div>
                <div v-else-if="form.onebot_transport === 'forward_ws'" class="field">
                  <label for="bot-onebot-ws">OneBot WebSocket 服务地址</label>
                  <input id="bot-onebot-ws" v-model="form.onebot_ws_endpoint" class="input mono" placeholder="ws://127.0.0.1:6700" />
                  <span class="hint">Diana 主动连接接入端的 WS 服务，请使用同时提供 API 和事件的通用地址（通常为 /）。断线后自动重连。发送文件/图片时接入端按这里的主机名回源拉取媒体：同机或容器（host.docker.internal）部署无需额外配置，跨机或反向代理部署请在「设置 → 媒体与文件」页配置媒体回源基址。</span>
                </div>
                <template v-else-if="form.onebot_transport === 'http'">
                  <div class="field">
                    <label for="bot-onebot-http">OneBot HTTP API 地址</label>
                    <input id="bot-onebot-http" v-model="form.onebot_http_url" class="input mono" placeholder="http://127.0.0.1:5700" />
                    <span class="hint">Diana 调用接入端的 HTTP API。事件上报地址填写接入端能访问的 Diana 地址 + /onebot/v11/http。发送文件/图片时接入端按这里的主机名回源拉取媒体：同机或容器部署无需额外配置，跨机或反向代理部署请在「设置 → 媒体与文件」页配置媒体回源基址。</span>
                  </div>
                  <SecretField id="bot-onebot-http-secret" v-model="oneBotHTTPSecretDraft"
                    label="HTTP 事件签名密钥" placeholder="与接入端 HTTP POST 的 secret 一致"
                    hint="必填；用于校验 X-Signature（HMAC-SHA1）。API 的 Access Token 在下方单独配置。"
                    :configured="form.onebot_http_secret_configured"
                    :revealed="tokenRevealed.onebot_http_secret" :busy="tokenRevealBusy === 'onebot_http_secret'"
                    @toggle-reveal="toggleTokenReveal('onebot_http_secret')" />
                </template>
                <div v-if="connectionConflict" class="stack" role="alert" style="gap: 8px">
                  <p class="hint warn-text">此 WebSocket 地址已由「{{ connectionConflict.name || '未命名机器人' }}」使用。请选择复用，或填写不同的地址，避免重复接收消息或连接被替换。</p>
                  <button class="btn small" type="button" :disabled="busy || connectionUsers(form).length > 0" @click="reuseConflictingConnection">改为复用「{{ connectionConflict.name || '未命名机器人' }}」</button>
                  <span v-if="connectionUsers(form).length" class="hint">当前连接仍被其他机器人复用，请先更换它们的连接来源。</span>
                  <span v-else class="hint">改为复用后，地址和 Token 跟随来源；人设及行为设置保留，保存后生效。</span>
                </div>
                <div class="field wide">
                  <label for="bot-token">OneBot Access Token</label>
                  <div class="input-group">
                    <input
                      id="bot-token"
                      v-model="tokenDraft"
                      class="input"
                      :type="tokenRevealed.onebot_access_token ? 'text' : 'password'"
                      autocomplete="off"
                      :placeholder="form.onebot_access_token_configured ? (form.onebot_access_token_preview ? `已保存 ${form.onebot_access_token_preview}，留空沿用，填写则覆盖` : '已配置 — 留空沿用，填写则覆盖') : ((!form.onebot_transport || form.onebot_transport === 'reverse_ws') ? '反向 WebSocket 必填（启用时），至少 8 位' : '可选，至少 8 位')"
                    />
                    <button
                      class="btn icon-only"
                      type="button"
                      :disabled="tokenRevealBusy === 'onebot_access_token'"
                      :aria-label="tokenRevealed.onebot_access_token ? '隐藏 Token' : '查看 Token'"
                      @click="toggleTokenReveal('onebot_access_token')"
                    >
                      <EyeOff v-if="tokenRevealed.onebot_access_token" :size="14" aria-hidden="true" />
                      <Eye v-else :size="14" aria-hidden="true" />
                    </button>
                    <button
                      class="btn icon-only"
                      type="button"
                      aria-label="随机生成 Token"
                      title="随机生成"
                      @click="generateOneBotToken"
                    >
                      <Shuffle :size="14" aria-hidden="true" />
                    </button>
                  </div>
                </div>
                <p v-if="oneBotMediaOriginWarning" class="hint warn-text">{{ oneBotMediaOriginWarning }}</p>
                </template>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.qq_typing_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">显示「对方正在输入」</span>
                  </label>
                  <span class="hint">默认开启。私聊准备回复时通过 set_input_status 显示输入状态，需要支持该接口的实现；QQ 群聊不支持，不支持的接入端会自动跳过。</span>
                </div>
              </template>
              <template v-else-if="currentPlatform === 'telegram'">
                <SecretField
                  id="bot-tg-token"
                  v-model="telegramTokenDraft"
                  label="Bot Token"
                  placeholder="从 @BotFather 获取"
                  hint="Telegram 用长轮询出站连接，不需要公网地址，也不用配置 webhook。"
                  :configured="form.telegram_bot_token_configured"
                  :revealed="tokenRevealed.telegram_bot_token"
                  :busy="tokenRevealBusy === 'telegram_bot_token'"
                  @toggle-reveal="toggleTokenReveal('telegram_bot_token')"
                />
                <div class="field">
                  <label for="bot-tg-proxy">代理地址（可选）</label>
                  <input
                    id="bot-tg-proxy"
                    v-model="form.telegram_proxy_url"
                    class="input mono"
                    placeholder="留空直连，例如 http://127.0.0.1:7890"
                    autocomplete="off"
                  />
                  <span class="hint">直连不通时再填；国内网络访问 api.telegram.org 通常需要代理，支持 http/https/socks5。</span>
                </div>
                <div class="field">
                  <label for="bot-tg-base">自建 Bot API 地址</label>
                  <input
                    id="bot-tg-base"
                    v-model="form.telegram_api_base_url"
                    class="input mono"
                    placeholder="留空使用官方 https://api.telegram.org"
                    autocomplete="off"
                  />
                  <span class="hint">部署了本地 Bot API server 时填写，可绕过 50MB 上传限制。</span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.telegram_suppress_bot_messages" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">自动抑制 Telegram Bot 消息</span>
                  </label>
                  <span class="hint">默认开启。由模型结合上下文判断其他 Bot 是否提到了自己，不要求 @；未提到或无法确认时保持静默。</span>
                </div>
              </template>

              <template v-else-if="currentPlatform === 'qq-official'">
                <div class="field">
                  <label for="bot-qq-appid">AppID</label>
                  <input id="bot-qq-appid" v-model="form.qq_app_id" class="input mono" placeholder="QQ 开放平台的机器人 AppID" autocomplete="off" />
                  <span class="hint">在 q.qq.com 的机器人管理后台「开发设置」里查看。</span>
                </div>
                <SecretField
                  id="bot-qq-secret"
                  v-model="qqSecretDraft"
                  label="AppSecret"
                  placeholder="开发设置里的机器人密钥"
                  hint="出站 WebSocket 网关接入，不需要公网地址；平台只会推送 @ 机器人的群消息。"
                  :configured="form.qq_app_secret_configured"
                  :revealed="tokenRevealed.qq_app_secret"
                  :busy="tokenRevealBusy === 'qq_app_secret'"
                  @toggle-reveal="toggleTokenReveal('qq_app_secret')"
                />
                <label class="check">
                  <input v-model="form.qq_sandbox" type="checkbox" />
                  <span>使用沙箱环境</span>
                </label>
                <span class="hint">机器人尚未发布上架时勾选，走沙箱接口联调。</span>
              </template>

              <template v-else-if="currentPlatform === 'dingtalk'">
                <div class="field">
                  <label for="bot-ding-id">Client ID</label>
                  <input id="bot-ding-id" v-model="form.dingtalk_client_id" class="input mono" placeholder="应用的 AppKey / Client ID" autocomplete="off" />
                  <span class="hint">钉钉开放平台的应用凭证页可以看到。</span>
                </div>
                <SecretField
                  id="bot-ding-secret"
                  v-model="dingTalkSecretDraft"
                  label="Client Secret"
                  placeholder="应用的 AppSecret / Client Secret"
                  hint="用 Stream 模式出站长连接接入，不需要公网地址，也不用在后台配 HTTP 回调。"
                  :configured="form.dingtalk_client_secret_configured"
                  :revealed="tokenRevealed.dingtalk_client_secret"
                  :busy="tokenRevealBusy === 'dingtalk_client_secret'"
                  @toggle-reveal="toggleTokenReveal('dingtalk_client_secret')"
                />
                <div class="field">
                  <label for="bot-ding-robot">机器人 RobotCode（可选）</label>
                  <input id="bot-ding-robot" v-model="form.dingtalk_robot_code" class="input mono" placeholder="留空则与 Client ID 相同" autocomplete="off" />
                  <span class="hint">企业内部机器人单独分配了 robotCode 时才需要填。</span>
                </div>
              </template>

              <template v-else-if="currentPlatform === 'feishu'">
                <div class="field">
                  <label for="bot-feishu-appid">App ID</label>
                  <input id="bot-feishu-appid" v-model="form.feishu_app_id" class="input mono" placeholder="cli_ 开头的自建应用 App ID" autocomplete="off" />
                  <span class="hint">飞书开放平台的「凭证与基础信息」页。</span>
                </div>
                <SecretField
                  id="bot-feishu-secret"
                  v-model="feishuSecretDraft"
                  label="App Secret"
                  placeholder="自建应用的 App Secret"
                  :configured="form.feishu_app_secret_configured"
                  :revealed="tokenRevealed.feishu_app_secret"
                  :busy="tokenRevealBusy === 'feishu_app_secret'"
                  @toggle-reveal="toggleTokenReveal('feishu_app_secret')"
                />
                <SecretField
                  id="bot-feishu-verify"
                  v-model="feishuVerificationDraft"
                  label="Verification Token"
                  placeholder="事件订阅页的 Verification Token"
                  hint="用于核验回调来源。强烈建议填写——回调地址本身是公开的，不能当凭据用。"
                  :configured="form.feishu_verification_token_configured"
                  :revealed="tokenRevealed.feishu_verification_token"
                  :busy="tokenRevealBusy === 'feishu_verification_token'"
                  @toggle-reveal="toggleTokenReveal('feishu_verification_token')"
                />
                <SecretField
                  id="bot-feishu-encrypt"
                  v-model="feishuEncryptDraft"
                  label="Encrypt Key（可选）"
                  placeholder="后台开启了加密推送才填"
                  hint="填了这里就必须在飞书后台同步开启加密推送，否则明文回调会被拒绝。"
                  :configured="form.feishu_encrypt_key_configured"
                  :revealed="tokenRevealed.feishu_encrypt_key"
                  :busy="tokenRevealBusy === 'feishu_encrypt_key'"
                  @toggle-reveal="toggleTokenReveal('feishu_encrypt_key')"
                />
                <div class="field">
                  <label for="bot-feishu-base">开放平台地址</label>
                  <input id="bot-feishu-base" v-model="form.feishu_api_base_url" class="input mono" placeholder="留空使用 https://open.feishu.cn" autocomplete="off" />
                  <span class="hint">Lark 国际版填 https://open.larksuite.com。</span>
                </div>
              </template>

              <template v-else-if="currentPlatform === 'wecom'">
                <div class="field">
                  <label for="bot-wecom-corp">企业 ID</label>
                  <input id="bot-wecom-corp" v-model="form.wecom_corp_id" class="input mono" placeholder="ww 开头的 CorpID" autocomplete="off" />
                  <span class="hint">企业微信管理后台「我的企业」页底部。</span>
                </div>
                <div class="field">
                  <label for="bot-wecom-agent">AgentId</label>
                  <input id="bot-wecom-agent" v-model="form.wecom_agent_id" class="input mono" placeholder="自建应用的 AgentId，纯数字" autocomplete="off" />
                  <span class="hint">在「应用管理」里打开自建应用即可看到。</span>
                </div>
                <SecretField
                  id="bot-wecom-secret"
                  v-model="weComSecretDraft"
                  label="应用 Secret"
                  placeholder="自建应用的 Secret"
                  :configured="form.wecom_secret_configured"
                  :revealed="tokenRevealed.wecom_secret"
                  :busy="tokenRevealBusy === 'wecom_secret'"
                  @toggle-reveal="toggleTokenReveal('wecom_secret')"
                />
                <SecretField
                  id="bot-wecom-token"
                  v-model="weComTokenDraft"
                  label="Token"
                  placeholder="「接收消息」配置里的 Token"
                  :configured="form.wecom_token_configured"
                  :revealed="tokenRevealed.wecom_token"
                  :busy="tokenRevealBusy === 'wecom_token'"
                  @toggle-reveal="toggleTokenReveal('wecom_token')"
                />
                <SecretField
                  id="bot-wecom-aes"
                  v-model="weComAESDraft"
                  label="EncodingAESKey"
                  placeholder="43 位的 EncodingAESKey"
                  hint="Token 和 EncodingAESKey 用于回调验签和解密，缺一个就只能发不能收。"
                  :configured="form.wecom_encoding_aes_key_configured"
                  :revealed="tokenRevealed.wecom_encoding_aes_key"
                  :busy="tokenRevealBusy === 'wecom_encoding_aes_key'"
                  @toggle-reveal="toggleTokenReveal('wecom_encoding_aes_key')"
                />
              </template>

              <!-- 飞书和企业微信只能靠平台回调收消息，地址要填到对方后台。 -->
              <div v-if="callbackURL" class="field">
                <label for="bot-callback-url">回调地址</label>
                <div class="input-group">
                  <input id="bot-callback-url" class="input mono" :value="callbackURL" readonly />
                  <button class="btn icon-only" type="button" aria-label="复制回调地址" @click="copyCallbackURL">
                    <Copy :size="14" aria-hidden="true" />
                  </button>
                </div>
                <span class="hint">
                  填到该平台后台的事件接收配置里。这里按你当前访问控制台的地址拼出，
                  必须换成平台服务器能访问到的公网 HTTPS 地址才收得到消息。
                </span>
              </div>
              <div class="form-grid">
                <div class="field">
                  <label for="bot-owner">{{ isOneBotPlatform || form.platform === 'telegram' ? "主人账号" : "主人用户 ID" }}</label>
                  <input
                    id="bot-owner"
                    v-model="form.owner_id"
                    class="input"
                    :inputmode="isOneBotPlatform ? 'numeric' : 'text'"
                    :placeholder="form.platform === 'telegram' ? '数字用户 ID 或 @用户名，例如 70001 / @owneruser' : isOneBotPlatform ? '例如 123456789，用于管理指令和私聊登录' : '平台用户 ID，用于管理指令'"
                  />
                  <AccountNameHint :user-id="form.owner_id" :profile="form.id" />
                  <span v-if="form.platform === 'telegram'" class="hint">支持数字 ID、用户名或 @用户名，不区分用户名大小写；按 Telegram 发送者账号核验，不按显示昵称。用户名变更后需更新此处。</span>
                  <span class="hint">不需要聊天内管理或管理员快速登录时可以留空。</span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.owner_login_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">允许管理员快速登录控制台</span>
                  </label>
                  <span class="hint">开启后登录页可显示一次性验证码，主人私聊发给机器人即可登录；需当前机器人在线。</span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">服务启动时自动运行机器人</span>
                  </label>
                </div>
              </div>
            </div>
          </section>

        </div>

        <div v-show="editorTab === 'model'" class="stack">
          <!-- 模型分配 -->
          <section class="card">
            <div class="card-header">
              <h2>模型分配</h2>
              <span class="card-sub">按用途选择提供商与模型；提供商的接入与凭据在「提供商」页管理</span>
            </div>
            <div class="card-body stack" style="gap: 0">
              <p v-if="modelRolesChangedElsewhere" class="hint warn-text">
                模型分配刚在别处改过，通常是在聊天里让机器人自己换的。你在这一档也有未保存的修改，所以没有自动替换；直接保存会把那次改动覆盖掉。
                <button type="button" class="btn ghost small" @click="adoptIncomingModelRoles">载入最新</button>
              </p>
              <div class="model-role-row model-role-head" aria-hidden="true">
                <span>用途</span>
                <span>提供商 / 分组</span>
                <span>模型</span>
              </div>
              <div v-for="role in visibleModelRoleRows" :key="role.key" class="model-role-block">
                <div class="model-role-row">
                  <div
                    class="model-route-group"
                    :class="routeDragClasses(role.key, 0)"
                    @dragover="(event) => onRouteDragOver(role.key, 0, event)"
                    @drop="(event) => onRouteDrop(role.key, 0, event)"
                  >
                    <button
                      v-if="routeReorderable(role.key)"
                      class="model-role-label model-route-handle"
                      type="button"
                      draggable="true"
                      :data-route-handle="`${role.key}-0`"
                      title="拖动调整顺序，或按 ↑ ↓ 键；排在最上面的是主路由"
                      :aria-label="`${role.label}：主路由，按上下方向键调整顺序`"
                      @dragstart="(event) => onRouteDragStart(role.key, 0, event)"
                      @dragend="onRouteDragEnd"
                      @keydown="(event) => onRouteHandleKeydown(role.key, 0, event)"
                    >
                      <GripVertical :size="14" aria-hidden="true" />
                      {{ role.label }}
                    </button>
                    <span v-else class="model-role-label">{{ role.label }}</span>
                    <AppSelect
                      :model-value="roleSelectionValue(role.key)"
                      :options="channelOptionsFor(role.key)"
                      placeholder="请选择提供商 / 分组"
                      @update:model-value="(value) => setRoleChannel(role.key, value)"
                    />
                    <AppSelect
                      :model-value="roleModelValue(role.key)"
                      :options="modelOptionsFor(role.key)"
                      :disabled="roleForm[role.key]?.follow_chat || (role.key === 'media_parse' && !roleForm[role.key])"
                      :placeholder="role.key === 'media_parse' && !roleForm[role.key] ? '跟随视觉理解模型' : roleForm[role.key]?.follow_chat ? '跟随对话模型' : '请选择模型（必填）'"
                      @update:model-value="(value) => setRoleModel(role.key, value)"
                    />
                    <button
                      class="btn icon-only ghost model-route-action"
                      type="button"
                      title="添加后备路由"
                      :aria-label="`${role.label}：添加后备路由`"
                      :disabled="!roleForm[role.key] || roleForm[role.key]?.follow_chat"
                      @click="addRoleFallback(role.key)"
                    >
                      <Plus :size="16" aria-hidden="true" />
                    </button>
                  </div>
                  <div
                    v-for="(fallback, index) in roleForm[role.key]?.fallbacks ?? []"
                    :key="`${role.key}-fallback-${index}`"
                    class="model-route-group"
                    :class="routeDragClasses(role.key, index + 1)"
                    @dragover="(event) => onRouteDragOver(role.key, index + 1, event)"
                    @drop="(event) => onRouteDrop(role.key, index + 1, event)"
                  >
                    <button
                      class="model-role-label muted model-route-handle"
                      type="button"
                      draggable="true"
                      :data-route-handle="`${role.key}-${index + 1}`"
                      title="拖动调整顺序，或按 ↑ ↓ 键；排在最上面的是主路由"
                      :aria-label="`${role.label}：后备 ${index + 1}，按上下方向键调整顺序`"
                      @dragstart="(event) => onRouteDragStart(role.key, index + 1, event)"
                      @dragend="onRouteDragEnd"
                      @keydown="(event) => onRouteHandleKeydown(role.key, index + 1, event)"
                    >
                      <GripVertical :size="14" aria-hidden="true" />
                      后备 {{ index + 1 }}
                    </button>
                    <AppSelect
                      :model-value="routeSelectionValue(fallback)"
                      :options="channelOptionsFor(role.key)"
                      placeholder="请选择提供商 / 分组"
                      @update:model-value="(value) => setFallbackChannel(role.key, index, value)"
                    />
                    <AppSelect
                      :model-value="fallback.model"
                      :options="modelOptionsFor(role.key, fallback)"
                      placeholder="请选择后备模型"
                      @update:model-value="(value) => setFallbackModel(role.key, index, value)"
                    />
                    <button class="btn icon-only ghost model-route-action" type="button" title="删除后备路由" :aria-label="`${role.label}：删除后备 ${index + 1}`" @click="removeRoleFallback(role.key, index)">
                      <Trash2 :size="16" aria-hidden="true" />
                    </button>
                  </div>
                </div>
                <p class="model-role-desc muted">{{ role.description }}</p>
              </div>
              <p class="muted model-role-note">
                每个用途的主路由和后备路由按从上到下的顺序依次尝试。有后备时，拖动左侧的名称可以调整顺序（也可以聚焦后按 ↑ ↓ 键），
                拖到最上面的那条就成为主路由，原来的主路由顺延为后备。
              </p>
              <button class="btn ghost" type="button" @click="purposeRolesOpen = !purposeRolesOpen">
                <ChevronDown :size="14" :class="{ 'recent-chevron-open': purposeRolesOpen }" aria-hidden="true" />
                {{ purposeRolesOpen ? "收起后台生成" : "后台生成（好感度 / 长期记忆）可以单独指模型" }}
              </button>
              <p v-if="purposeRolesOpen" class="muted model-role-note">
                「意图识别」现在只管判定当前这轮该不该说话、说出去的这句能不能发——问的都是是非、单选和打分，
                可以绑 TypeSafe Jev 这类只做判断的模型。写字的活（好感度、长期记忆、摘要、RSS 判断）拆到下面这一档，不指定时跟随对话。
              </p>
            </div>
          </section>

          <!-- 模型调用 -->
          <section class="card">
            <div class="card-header">
              <h2>模型调用</h2>
              <span class="badge" :class="form.llm_streaming_enabled ? 'accent' : ''">
                {{ form.llm_streaming_enabled ? "流式" : "非流式" }}
              </span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.llm_streaming_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">流式调用模型（默认开启）</span>
                </label>
                <span class="hint">
                  默认使用流式接收正文、思考和工具调用；思考不会作为聊天正文发送，工具参数完整后才会执行。
                  可统计首 token 时延（TTFT），Telegram 私聊支持回复预览。供应商不支持流式或请求失败时会尝试普通调用。
                </span>
              </div>
              <div class="field">
                <label for="bot-model-disclosure">谁能问出所用模型</label>
                <AppSelect
                  id="bot-model-disclosure"
                  :model-value="form.model_disclosure ?? 'owner'"
                  :options="disclosureOptions"
                  @update:model-value="(value) => { if (form) form.model_disclosure = value as 'owner' | 'everyone'; }"
                />
                <span class="hint">默认只对主人如实回答模型 ID 和供应商，主人也始终能在聊天里查看和切换模型；其他人问起时机器人会含糊带过，也不会凭训练记忆自报家门。</span>
              </div>
              <div class="field">
                <label for="bot-repository-disclosure">谁能问出项目地址</label>
                <AppSelect
                  id="bot-repository-disclosure"
                  :model-value="form.repository_disclosure ?? 'owner'"
                  :options="disclosureOptions"
                  @update:model-value="(value) => { if (form) form.repository_disclosure = value as 'owner' | 'everyone'; }"
                />
                <span class="hint">默认只对主人报开源仓库地址；其他人问「你源码在哪」时机器人会带过去，不给链接也不会编一个。地址本身是公开的，但知道地址就知道去哪看默认提示词和全部工具实现。</span>
              </div>
            </div>
          </section>

          <!-- 聊天内模型管理：主人专用的开关，平时用不上，排在模型分配和调用参数之后。 -->
          <section class="card">
            <div class="card-header">
              <h2>聊天内模型管理</h2>
              <span class="badge" :class="form.owner_llm_config_enabled ? 'accent' : ''">
                {{ form.owner_llm_config_enabled ? "已启用" : "未启用" }}
              </span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.owner_llm_config_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">允许主人在聊天中修改提供商和模型</span>
                </label>
                <span class="hint">仅主人账号可修改，保存前会校验目标模型是否可用。</span>
              </div>
            </div>
          </section>
        </div>

        <div v-show="editorTab === 'behavior'" class="stack">
          <!-- 触发与回复 -->
          <section class="card">
            <div class="card-header">
              <h2>触发与回复</h2>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label for="bot-triggers">群聊触发词（逗号分隔）</label>
                <input id="bot-triggers" v-model="triggersDraft" class="input" placeholder="Diana,diana" />
                <span class="hint">群聊中 @ 机器人或消息里出现触发词会触发；私聊总是触发。</span>
              </div>
              <div class="field wide">
                <label for="bot-trigger-mode">触发词匹配</label>
                <AppSelect
                  id="bot-trigger-mode"
                  :model-value="form.group_trigger_mode ?? 'smart'"
                  :options="triggerModeOptions"
                  @update:model-value="(value) => { if (form) form.group_trigger_mode = value as AliasTriggerMode; }"
                />
                <span class="hint">
                  智能：出现触发词就回，但「「Diana」这名字挺好听」这类把触发词整个引起来的引述不算呼叫。
                  严格：还要求触发词出现在句首或句尾。宽松：出现即回，连引述也算。
                </span>
              </div>
              <div class="field">
                <label for="bot-maxinput">单次输入上限（字符）</label>
                <input id="bot-maxinput" v-model.number="form.max_input_chars" class="input" inputmode="numeric" />
              </div>
              <div class="field">
                <label for="bot-maxreply">单条回复上限（字符）</label>
                <input id="bot-maxreply" v-model.number="form.max_reply_chars" class="input" inputmode="numeric" />
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.natural_reply_split_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">允许多条发送</span>
                </label>
                <span class="hint">仅显式分条标记另发消息；普通换行不分条，除非开启下面的换行分条。关闭后单条发送，超限压缩。本轮用户明确要求优先。</span>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.reply_line_split_enabled" type="checkbox" :disabled="!form.natural_reply_split_enabled" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">换行分条发送</span>
                </label>
                <span class="hint">消息内每换一行就另发一条；列表、表格和代码块整块发，连同引出它的那一行。需先允许多条发送；闲聊插话和本轮要求一条发送时不生效。</span>
              </div>
              <div class="field">
                <label class="switch">
                  <input v-model="form.typing_delay_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">模拟打字延时</span>
                </label>
                <span class="hint">连发时按下一条的字数停顿，像边打边发。不低于分段发送间隔，单次最长 6 秒；第一条不额外等待。</span>
              </div>
              <div class="field">
                <label for="bot-typing-speed">打字速度（毫秒/字）</label>
                <input id="bot-typing-speed" v-model.number="form.typing_delay_per_char_ms" class="input" type="number" min="1" max="1000" step="1" inputmode="numeric" placeholder="留空按 100" :disabled="!form.typing_delay_enabled" />
                <span class="hint">每个字等多久。100 约等于一秒十个字；越大越慢。</span>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.reply_preserve_line_breaks" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">保留普通段落换行</span>
                </label>
                <span class="hint">关闭时收拢普通说明的换行，保留已有标点，缺少分隔时补逗号或空格；列表、代码、表格保留结构。本轮排版要求优先。</span>
              </div>
              <div class="field">
                <label for="bot-reply-merge-confidence">合并回复置信度阈值（%）</label>
                <input id="bot-reply-merge-confidence" v-model.number="form.reply_merge_confidence_percent" class="input" type="number" min="1" max="100" step="1" inputmode="numeric" placeholder="75" />
                <span class="hint">同一用户的补充、纠正或重复消息达到此置信度时，并入正在生成的回复。越低越容易合并；留空默认 75%。独立问题和无法确定的消息不会合并。</span>
              </div>
              <div v-if="isOneBotPlatform" class="field">
                <label for="bot-forward-len">合并转发字数</label>
                <input id="bot-forward-len" v-model.number="form.forward_reply_threshold" class="input" type="number" min="0" step="1" inputmode="numeric" placeholder="无上限" />
                <span class="hint">允许多条发送时，整轮正文超过此值触发卡片；新建机器人默认 140 字，0 或留空关闭此条件。仅 OneBot 支持。</span>
              </div>
              <div v-if="isOneBotPlatform" class="field">
                <label for="bot-forward-chunks">合并转发块数</label>
                <input id="bot-forward-chunks" v-model.number="form.forward_reply_chunk_threshold" class="input" type="number" min="0" step="1" inputmode="numeric" placeholder="无上限" />
                <span class="hint">实际消息数超过此值触发卡片，填 4 表示至少 5 条；0 或留空关闭此条件。不按正文行数计数。</span>
              </div>
              <div class="field">
                <label for="bot-call-quota">模型额度 · 5 小时调用次数</label>
                <input id="bot-call-quota" v-model.number="form.model_call_quota" class="input" type="number" min="0" step="1" inputmode="numeric" placeholder="留空不限" />
                <span class="hint">每个群单独计，一个群刷满不会把别的群一起饿死；群配置里填了就以群为准。这个群名下的每次模型调用都算，含路由判断和工具步，不只是最终那句回复。主人不受限。</span>
              </div>
              <div class="field">
                <label for="bot-sample">回复抽样率（%）</label>
                <input id="bot-sample" v-model.number="form.reply_sample_percent" class="input" type="number" min="0" max="100" step="1" inputmode="numeric" placeholder="留空不抽样" />
                <span class="hint">群里没 @、没引用、没叫名字的消息，只有这个比例交给模型判断要不要接话，没抽中的一次调用都不花。被点名的照常回复，主人不受限。群配置里填了就以群为准。</span>
              </div>
              <div class="field">
                <label for="bot-backfill-limit">断线回补条数</label>
                <input id="bot-backfill-limit" v-model.number="form.history_backfill_message_limit" class="input" inputmode="numeric" min="1" max="100" placeholder="3" />
                <span class="hint">重启或断线重连后，每个会话最多补回复多少条消息，只算会触发回复的（私聊、@ 机器人、引用机器人、称呼命中）。断线期间的其余消息照样补进上下文历史，但不做图片、语音处理也不回复。默认 3，媒体较多的群建议保持较小。</span>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.long_term_memory_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">长期记忆与分层压缩</span>
                </label>
                <span class="hint">将较早聊天压缩为可检索摘要，并持久化稳定事实和偏好。</span>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.cross_group_memory_enabled" type="checkbox" :disabled="!form.long_term_memory_enabled" title="公共事实和摘要按相关性跨群召回，无需共同成员；原始聊天仍要求原发言者也在当前群" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">跨群记忆</span>
                </label>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.cross_platform_memory_enabled" type="checkbox" :disabled="!form.long_term_memory_enabled" title="需要双方机器人启用长期记忆和跨平台记忆；仅共享非敏感群公共记忆，不合并会话上下文" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">跨平台记忆</span>
                </label>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.self_note_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">自述（自我认知）</span>
                </label>
                <span class="hint">允许机器人把自己注意到的说话习惯、偏好和毛病写成自述，跨群生效，每轮注入提示词尾部。人设正文只有你能改，自述改不动人设、权限和安全边界；最多 24 条，每条 120 字，主人可以在对话里让它列出、删除或清空。缺省关闭。</span>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.dict_segment_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">词典分词</span>
                </label>
                <span class="hint">用中文词典切出真实词参与记忆与历史检索，排序更准；词典常驻约 130MB 内存，开启立即生效，关闭需重启进程。</span>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.semantic_search_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">语义检索</span>
                </label>
                <span class="hint">消息经向量化后可按意思检索历史（“有什么吃的推荐”能找到“凤爪味道不错”）。需在提供商配置里建一个分组为 embedding 的配置档；每条消息会调用一次向量化接口。</span>
              </div>
              <div class="field wide memory-settings">
                <label class="switch">
                  <input v-model="form.debug_mode_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">调试模式</span>
                </label>
                <span class="hint">开启后记录完整模型上下文、工具参数、工具结果和调用链，内容可能包含聊天隐私；默认关闭。</span>
              </div>
              <div class="field">
                <label for="bot-concurrency">全局并发数</label>
                <input id="bot-concurrency" v-model.number="form.max_bot_concurrency" class="input" inputmode="numeric" />
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.welcome_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">开启入群欢迎</span>
                </label>
              </div>
              <div v-if="form.welcome_enabled" class="field">
                <label for="bot-welcome-mode">欢迎词模式</label>
                <AppSelect
                  id="bot-welcome-mode"
                  :model-value="form.welcome_mode ?? 'fixed'"
                  :options="welcomeModeOptions"
                  @update:model-value="(value) => { if (form) form.welcome_mode = value as BotProfileConfig['welcome_mode']; }"
                />
              </div>
              <div v-if="form.welcome_enabled" class="field wide">
                <label for="bot-welcome">欢迎语</label>
                <textarea id="bot-welcome" v-model="form.welcome_message" class="textarea" rows="2"></textarea>
              </div>
              <div v-if="form.welcome_enabled && (form.welcome_mode ?? 'fixed') !== 'fixed'" class="field wide">
                <label for="bot-welcome-templates">欢迎词模板池</label>
                <textarea
                  id="bot-welcome-templates"
                  v-model="welcomeTemplatesDraft"
                  class="textarea"
                  rows="3"
                  placeholder="每行一条候选，发送时随机抽一条；{user_id} 会替换成新成员 ID。LLM 模式冷却或失败时也从这里回落。"
                ></textarea>
              </div>
              <div v-if="form.welcome_enabled && (form.welcome_mode ?? 'fixed') === 'llm'" class="field">
                <label for="bot-welcome-cooldown">LLM 欢迎冷却（秒/群）</label>
                <input
                  id="bot-welcome-cooldown"
                  v-model.number="form.welcome_llm_cooldown_seconds"
                  class="input"
                  inputmode="numeric"
                  placeholder="默认 300"
                />
                <span class="hint">冷却期内新成员入群改发模板池/固定文本，避免进出群刷屏消耗 Token。</span>
              </div>
            </div>
          </section>

          <!-- 回复行为 -->
          <section class="card">
            <div class="card-header">
              <h2>回复行为</h2>
              <span class="card-sub">发送细节按习惯个性化，默认值即推荐值</span>
            </div>
            <div class="card-body form-grid">
              <div class="field">
                <label for="bot-reply-reference-mode">群聊引用原消息</label>
                <AppSelect
                  id="bot-reply-reference-mode"
                  :model-value="form.reply_reference_mode ?? 'auto'"
                  :options="replyReferenceModeOptions"
                  @update:model-value="(value) => { if (form) form.reply_reference_mode = value as 'on' | 'off' | 'auto'; }"
                />
                <span class="hint">默认「让模型自己决定」：只有话题跳转或隔轮回应时才引用。</span>
              </div>
              <div class="field">
                <label for="bot-mention-user-mode">群聊 @ 发送者</label>
                <AppSelect
                  id="bot-mention-user-mode"
                  :model-value="form.mention_user_mode ?? 'auto'"
                  :options="mentionUserModeOptions"
                  @update:model-value="(value) => { if (form) form.mention_user_mode = value as 'on' | 'off' | 'auto'; }"
                />
                <span class="hint">默认「让模型自己决定」：群里还有别人在说话时才 @，一对一接话时不带。</span>
              </div>
              <div class="field">
                <label class="switch">
                  <input v-model="form.markdown_to_plain" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">Markdown 转纯文本</span>
                </label>
                <span class="hint">{{ richTextPlatform ? "当前平台能渲染 Markdown，默认保留标记；开启后改为发送纯文本。" : "当前平台只发纯文本，Markdown 标记会以字面量出现，所以默认转成纯文本。" }}</span>
              </div>
              <div class="field">
                <label class="switch">
                  <input v-model="form.error_notify_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">出错时在聊天里提示</span>
                </label>
                <span class="hint">
                  关闭后仍保留完整事件记录和运行日志，只是不把错误发进聊天。账号安全拦下的回复也走这个开关：提示会用机器人自己的口吻说，
                  并且不会复述被拦下的内容或风险类别；改写用的模型调用失败时退回固定文案。表达质量拦截始终静默，不受此开关影响。
                </span>
              </div>
              <div class="field">
                <label class="switch">
                  <input v-model="form.muted_reply_pause_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">被禁言时暂停回复</span>
                </label>
                <span class="hint">
                  机器人在群里被禁言（或全员禁言且机器人不是管理员）期间，消息照常记入上下文和记忆，但不生成回复（回复判断默认也不做），
                  也不白发再重试。解禁后从新消息开始回复，禁言期间的消息不补发。禁言和解禁会记在事件页的「通知」里。
                  关闭后按原来的方式照常生成和重试。
                </span>
              </div>
              <div v-if="form.muted_reply_pause_enabled" class="field wide">
                <label>暂停期间照常执行</label>
                <div class="stack">
                  <label class="switch">
                    <input v-model="form.muted_image_description_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">图片识别成文字</span>
                  </label>
                  <label class="switch">
                    <input v-model="form.muted_voice_transcription_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">语音转文字</span>
                  </label>
                  <label class="switch">
                    <input v-model="form.muted_reply_judgment_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">回复判断</span>
                  </label>
                </div>
                <span class="hint">
                  图片识别和语音转文字默认开，解禁后历史里的图片、语音有文字，上下文才完整；关掉能省下这段时间的费用。
                  回复判断默认关：判断了也发不出去。打开后照常判断，该回的消息在事件页记为「判断该回，但禁言中未发送」，不生成也不发送。
                </span>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.recall_reply_auto_delete_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">查看撤回消息后自动撤回回复</span>
                </label>
                <span class="hint">默认关闭。开启后，仅查看撤回记录产生的回复会在设定时间后撤回。</span>
              </div>
              <div v-if="form.recall_reply_auto_delete_enabled" class="field">
                <label for="bot-recall-delete-delay">回复保留时间（秒）</label>
                <input
                  id="bot-recall-delete-delay"
                  v-model.number="form.recall_reply_auto_delete_delay_seconds"
                  class="input"
                  type="number"
                  min="1"
                  :max="maximumRecallReplyAutoDeleteDelaySeconds"
                  step="1"
                  inputmode="numeric"
                />
                <span class="hint">可设置 1–{{ maximumRecallReplyAutoDeleteDelaySeconds }} 秒。</span>
              </div>
              <div v-if="form.error_notify_enabled" class="field">
                <label for="bot-errprefix">错误提示前缀</label>
                <input id="bot-errprefix" v-model="form.error_reply_prefix" class="input" placeholder="出错了：" />
              </div>
              <div class="field">
                <label for="bot-retry">发送重试次数（1–5）</label>
                <input id="bot-retry" v-model.number="form.send_retry_attempts" class="input" inputmode="numeric" />
                <span class="hint">单次发送内的快速重试，间隔不到一秒。群消息只发一次，失败后交给下面的退避重发。</span>
              </div>
              <div v-for="field in sendRetryFields" :key="field.key" class="field">
                <label :for="`bot-${field.key}`">{{ field.label }}</label>
                <input
                  :id="`bot-${field.key}`"
                  v-model.number="form[field.key]"
                  class="input"
                  type="number"
                  :min="field.min"
                  :max="field.max"
                  step="1"
                  inputmode="numeric"
                  :placeholder="`默认 ${field.fallback}`"
                />
                <span class="hint">{{ field.hint }}</span>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="subscriptionFailureAlertEnabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">订阅失败时发通知</span>
                </label>
                <span class="hint">RSS、定时查询、仓库订阅坏了要不要说一声。关掉之后失败只留日志和后台状态，聊天里再也不报错。</span>
              </div>
              <div v-if="subscriptionFailureAlertEnabled" class="field">
                <label for="bot-subscription-failure">连续失败几次才报</label>
                <input
                  id="bot-subscription-failure"
                  v-model.number="form.recurring_failure_alert_threshold"
                  class="input"
                  type="number"
                  min="1"
                  :max="maximumRecurringFailureAlertThreshold"
                  step="1"
                  inputmode="numeric"
                  placeholder="留空按 5"
                />
                <span class="hint">抖一下就报警只会让人不再看这类消息，所以连着坏够次数才出声，而且一轮故障只报一次。可设置 1–{{ maximumRecurringFailureAlertThreshold }} 次。</span>
              </div>
              <div class="field">
                <label for="bot-interval">分段发送间隔（毫秒）</label>
                <input id="bot-interval" v-model.number="form.send_chunk_interval_ms" class="input" inputmode="numeric" placeholder="留空按 1200" />
                <span class="hint">连续多段之间的停顿，过快容易触发风控。开启模拟打字延时后作为最短停顿。</span>
              </div>
            </div>
          </section>

          <!-- 准入控制 -->
          <section class="card">
            <div class="card-header">
              <div>
                <h2>准入控制</h2>
                <span class="card-sub">决定机器人在哪些群工作、满足什么条件才回复</span>
              </div>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label>在哪些群工作</label>
                <p class="hint">
                  逐群开关在<a href="#" @click.prevent="navigate('groups')">群管理</a>里：一个群一个开关，还能一键全开全关，以及设定新加入的群默认工不工作。
                </p>
              </div>
              <div class="field wide">
                <label for="bot-private-admission-mode">私聊准入模式</label>
                <AppSelect
                  id="bot-private-admission-mode"
                  :model-value="privateAdmissionMode"
                  :options="privateAdmissionModeOptions"
                  @update:model-value="setPrivateAdmissionMode($event as 'all' | 'owner_only' | 'whitelist')"
                />
                <span class="hint">被准入拦截的私聊整条静默忽略：不排队、不预处理、不调模型，对方收不到任何回应；与群聊无关，主人任何模式下都放行。</span>
              </div>
              <div v-if="privateAdmissionMode === 'whitelist'" class="field wide">
                <label for="bot-private-allowed-users">私聊白名单</label>
                <IdChipInput
                  input-id="bot-private-allowed-users"
                  v-model="privateAllowedUsers"
                  placeholder="填用户 ID 后回车"
                />
                <span class="hint">仅这些用户（和主人）的私聊会得到响应；名单外一律静默忽略。</span>
              </div>
              <div class="field wide">
                <ReplyGateForm v-model="globalGate" id-prefix="bot-gate" :supports-group-level="isOneBotPlatform" hide-group-level />
                <p v-if="isOneBotPlatform" class="hint">
                  群等级门槛在<a href="#" @click.prevent="navigate('groups')">群管理</a>里，和逐群开关放在一起；它仍然生效，主人豁免也仍然绕过它。
                </p>
              </div>
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <h2>自动机器人识别</h2>
              <span class="badge" :class="form.bot_reply_loop_detection_enabled ? 'accent' : ''">
                {{ form.bot_reply_loop_detection_enabled ? "已启用" : "未启用" }}
              </span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.bot_reply_loop_detection_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">识别其他机器人的自动回复并停止接续</span>
                </label>
                <span class="hint">回复同一账号过于频繁时（10 分钟 10 条，已标记的机器人 2 条），发送前审核会判断这串来回有没有明确目的：下棋、解题、一起做事照常回；漫无目的地接戏、斗嘴、复读则降低回复欲望（不主动接、只接点名并逐步拉长冷却），30 分钟内累计 3 次暂停响应该账号 30 分钟。主人不受影响。</span>
              </div>
            </div>
          </section>

          <!-- 媒体预处理原来在「模型」标签，因为它花的是「媒体解析」那个模型的额度。但它回答的
               是「收到图片、视频时后台做不做」，和机器人识别、发送前审核是一类事；用哪个模型
               仍在模型标签里，这里给一个跳转。 -->
          <section class="card">
            <div class="card-header"><h2>媒体预处理</h2></div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.auto_image_description" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">自动生成图片描述</span>
                </label>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.auto_video_preprocess" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">自动下载视频并提取关键帧</span>
                </label>
                <span class="hint">关闭后保留媒体索引和已有缓存；普通图片不再后台调用模型，视频不再预下载或抽帧。主动读取、引用分析及工具调用仍可按需解析；远程媒体过期后可能无法读取。</span>
                <span class="hint">图片描述、视频帧描述和模型 OCR 用的是<a href="#" @click.prevent="editorTab = 'model'">「模型」标签</a>里「模型分配」的「媒体解析」。文本文件提取和本地 OCR 不消耗模型额度。</span>
              </div>
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <h2>发送前审核</h2>
              <span class="badge" :class="form.reply_account_safety_audit_master_enabled ? 'accent' : ''">
                {{ form.reply_account_safety_audit_master_enabled ? "已启用" : "已关闭" }}
              </span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.reply_account_safety_audit_master_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">启用账号安全审核</span>
                </label>
                <span class="hint">
                  开启后，这台机器人所有主动和直接回复都会做一次统一发送前审核；关闭后都不做，群配置可单独覆盖。
                  一次审核同时给出账号安全置信度并判断是否属于明确拒答；主动回复还会额外使用其中的表达质量结论。只有安全置信度低于 10% 时才会拦下不发——
                  拿不准一律放行，避免机器人在沾边话题上无故闭嘴；高置信拒答仅在发送成功后累计。表达质量分数不会拦下直接回复。
                  开启时，被 @ 或私聊的直接回复也各多一次快模型往返，回复会慢一点。
                </span>
              </div>
              <div class="field wide">
                <label for="bot-account-safety-prompt">账号安全审核规则（留空使用内置规则）</label>
                <textarea
                  id="bot-account-safety-prompt"
                  v-model="form.reply_account_safety_audit_prompt"
                  class="textarea"
                  rows="5"
                  placeholder="例如：只拦截可能导致当前平台账号处罚的明确内容；新闻事实中性转述放行。"
                ></textarea>
                <span class="hint">填写后替代内置账号风险范围，只影响账号安全结论，不改变准确度、拒答和防循环审核。</span>
              </div>
              <div class="field wide">
                <label for="bot-refusal-strategy">拒答话术</label>
                <AppSelect
                  id="bot-refusal-strategy"
                  :model-value="form.refusal_strategy ?? 'smart'"
                  :options="refusalStrategyOptions"
                  @update:model-value="(value) => { if (form) form.refusal_strategy = value as RefusalStrategy; }"
                />
                <span class="hint">
                  机器人决定不正面回答时说什么。说明为什么不能答，本身可能就是那句会出事的话——
                  一句「这个话题涉及敏感政治，我不方便讲」把触发点原样复述了一遍，风险比闭嘴还大。
                  除「说明原因」外的档位都不会点名或影射触发拒答的具体内容。
                  30 分钟内累计拒答超过 3 次后暂停响应该账号 30 分钟，这一条不受本项影响；模型拒答标志不向用户展示。
                </span>
              </div>
            </div>
          </section>

          <section class="card">
            <div class="card-header">
              <h2>笔记本作用域</h2>
              <span class="badge" :class="form.notebook_shared_scope_enabled ? 'accent' : ''">
                {{ form.notebook_shared_scope_enabled ? "跟随机器人" : "按会话隔离" }}
              </span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.notebook_shared_scope_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">笔记本跟随机器人（群聊私聊共用一本）</span>
                </label>
                <span class="hint">
                  默认打开：机器人只有一本笔记，在哪个群、哪个私聊里学到的梗和规矩都记进去，所有会话通用。
                  关掉后按会话隔离：一个群记下的只在这个群生效，别的群查不到，只有主人能写全局笔记本。
                  切换不会搬动已有条目，各会话里已经记下的仍在自己会话里优先生效，可以在「笔记本」页按作用域逐条改。
                </span>
              </div>
            </div>
          </section>

        </div>

        <div v-show="editorTab === 'persona'" class="stack">
          <!-- 人设 -->
          <section class="card persona-section">
            <div class="card-header">
              <div>
                <h2>人设</h2>
                <span class="card-sub">机器人是谁、怎么说话、多主动，都在这里定</span>
              </div>
              <button class="btn small" type="button" @click="resetPromptDefaults">
                <RotateCcw :size="14" aria-hidden="true" />
                恢复内置默认
              </button>
            </div>
            <div class="card-body form-grid">
              <!-- 人设库是「套用来源」：点一下把下面四项填好，改不改随你，按保存才生效。
                   它不是活绑定——库里那份之后再改，不会偷偷影响已经保存的机器人。 -->
              <div class="field wide">
                <div class="field-head">
                  <label>人设库</label>
                  <div class="cluster">
                    <button class="btn small" type="button" :disabled="personaLibraryBusy || !personaLibrary.length" @click="exportPersonaLibrary">
                      <Download :size="14" aria-hidden="true" />
                      导出
                    </button>
                    <button class="btn small" type="button" :disabled="personaLibraryBusy" @click="personaFileInputClick">
                      <Upload :size="14" aria-hidden="true" />
                      导入
                    </button>
                    <button
                      v-if="editedLibraryPersona"
                      class="btn small"
                      type="button"
                      :disabled="personaLibraryBusy || !personaHasContent"
                      :title="`把当前内容写回人设库「${editedLibraryPersona.name}」，绑定它的机器人和群一起更新`"
                      @click="updateEditedLibraryPersona"
                    >
                      <RefreshCw :size="14" aria-hidden="true" />
                      更新「{{ editedLibraryPersona.name }}」
                    </button>
                    <button class="btn small" type="button" :disabled="personaLibraryBusy || !personaHasContent" @click="togglePersonaSaver">
                      <component :is="personaSaverOpen ? X : Plus" :size="14" aria-hidden="true" />
                      {{ personaSaverOpen ? "取消" : "存为人设" }}
                    </button>
                  </div>
                </div>
                <input ref="personaFileInput" type="file" accept="application/json,.json,.yaml,.yml,image/png,.png" style="display: none" @change="importPersonaFile" />
                <div v-if="personaSaverOpen" class="persona-saver">
                  <input
                    ref="personaNameInput"
                    v-model.trim="personaNameDraft"
                    class="input"
                    maxlength="40"
                    placeholder="给这套人设起个名字，例如 猫娘"
                    @keydown.enter.prevent="storeCurrentPersona"
                    @keydown.esc="personaSaverOpen = false"
                  />
                  <button class="btn primary small" type="button" :disabled="personaLibraryBusy || !personaNameDraft" @click="storeCurrentPersona">
                    {{ personaSaverExisting ? "另存副本" : "保存" }}
                  </button>
                </div>
                <div class="persona-library">
                  <div class="persona-chip" :class="{ 'is-active': selectedPersonaID === 'custom' }">
                    <button type="button" class="persona-chip-apply" :disabled="personaLibraryBusy" :aria-pressed="selectedPersonaID === 'custom'" @click="choosePersona('custom')">
                      <span class="persona-chip-name">自定义</span>
                      <small class="muted">当前编辑</small>
                    </button>
                  </div>
                  <div v-for="persona in personaLibrary" :key="persona.id" class="persona-chip" :class="{ 'is-active': selectedPersonaID === persona.id }">
                    <button type="button" class="persona-chip-apply" :disabled="personaLibraryBusy" :aria-pressed="selectedPersonaID === persona.id" :title="personaSummary(persona)" @click="choosePersona(persona.id)">
                      <span class="persona-chip-name">{{ persona.name }}</span>
                      <small class="muted">{{ isBuiltinPersona(persona) ? "内置 · " : "" }}{{ personaSummary(persona) }}</small>
                    </button>
                    <span class="persona-chip-actions">
                      <button type="button" class="persona-chip-action" :aria-label="`导出人设 ${persona.name}`" :title="`导出人设 ${persona.name}`" @click="exportPersona(persona)">
                        <Download :size="13" aria-hidden="true" />
                      </button>
                      <button v-if="!isBuiltinPersona(persona)" type="button" class="persona-chip-action danger" :disabled="personaLibraryBusy" :aria-label="`删除人设 ${persona.name}`" :title="`删除人设 ${persona.name}`" @click="removePersona(persona)">
                        <X :size="13" aria-hidden="true" />
                      </button>
                    </span>
                  </div>
                </div>
                <span v-if="!personaLibrary.length" class="hint">还没存过人设。调整下方设置后，点「存为人设」保存。</span>
                <span v-else class="hint">选中一套即绑定：人设库里这一套更新后，绑定它的机器人和群自动跟着改。在下方改了内容就变成「自定义」，可以点「更新」写回这一套，或「存为人设」另存一套。</span>
              </div>
              <div class="field wide">
                <div class="field-head">
                  <label for="bot-prompt">基础人设</label>
                  <button class="btn small" type="button" :disabled="personaBusy" @click="openPersonaComposer">
                    <Sparkles :size="14" aria-hidden="true" />
                    AI 生成
                  </button>
                  <button v-if="personaMode === 'own'" class="btn small" type="button" title="把运行时本来会补的那几段写进正文，每段带段头" @click="fillPersonaOwnedTemplate">
                    <Plus :size="14" aria-hidden="true" />
                    填入接管模板
                  </button>
                  <button class="btn small" type="button" :disabled="personaReviewBusy || !form.system_prompt?.trim()" title="让模型读一遍，挑出和下面开关打架的写法" @click="runPersonaReview">
                    <Eye :size="14" aria-hidden="true" />
                    AI 检查
                  </button>
                  <button v-if="personaCardExportable" class="btn small" type="button" title="导出成 SillyTavern V2 角色卡 JSON" @click="exportPersonaCard">
                    <Download :size="14" aria-hidden="true" />
                    导出角色卡 JSON
                  </button>
                </div>
                <textarea id="bot-prompt" v-model="form.system_prompt" class="textarea" rows="5"></textarea>
                <!-- 只提示不拦截：正文是用户写的，这里只负责说清「这条已经有开关管了」。
                     检查期间随时能跳过，跳过之后保存照常。 -->
                <div v-if="personaReviewBusy" class="cluster">
                  <span class="hint">AI 正在读这段人设…</span>
                  <button class="btn small" type="button" @click="skipPersonaReview">跳过</button>
                </div>
                <template v-else-if="personaReviewVisible">
                  <span v-for="(finding, index) in personaReviewFindings" :key="`ai-${finding.code}-${index}`" class="hint warn-text">
                    「{{ finding.match }}」——{{ finding.message }}
                  </span>
                  <div class="cluster">
                    <span v-if="personaReviewClean" class="hint">AI 检查没发现和开关打架的写法。</span>
                    <button class="btn small" type="button" @click="resetPersonaReview">忽略</button>
                  </div>
                </template>
                <div v-if="personaPrevious" class="cluster">
                  <button class="btn small" type="button" @click="undoPersonaGenerate">撤销生成</button>
                  <span class="hint">保存后才会生效，不满意可以撤回上一版。</span>
                </div>
                <span v-else class="hint">所有对话都会使用；群级人设仍可在群管理中覆盖。当前是{{ personaMode === "own" ? "接管模式：「怎么说话」全由正文负责，运行时不再补那几段" : "填空题模式：正文只写角色，其余交给下面的控件" }}。消息标记、分条上限和平台差异始终由运行时决定，正文写了也不算数。</span>
                <!-- 控件藏起来之后它存的值还在配置里。不说出来的话，一个填过「本喵」的
                     输入框就既看不见也改不掉，只在某天正文里那段被删掉时突然复活。 -->
                <span v-if="personaOwnedSummary.length" class="hint warn-text">
                  已交给正文的设置：{{ personaOwnedSummary.map(item => item.staleValue ? `${item.label}（原填「${item.staleValue}」，当前不生效）` : item.label).join("、") }}。
                  下面对应的控件已隐藏；要改回用控件，把人设模式切回填空题。
                </span>
              </div>
              <div class="field wide">
                <label for="bot-persona-mode">人设模式</label>
                <AppSelect id="bot-persona-mode" :model-value="personaMode" :options="personaModeOptions" @update:model-value="value => { if (form) form.persona_mode = value === 'own' ? 'own' : 'fill'; }" />
                <span class="hint">填空题适合大多数情况：人设正文只写这个角色是谁，自称、句尾语气词、动作描写、答多长这些在下面点几下就好。接管模式留给想自己写全的人——切过去之后，正文里用段头声明的那几段运行时不再补。</span>
              </div>
              <div class="field wide">
                <label>接话设置</label>
                <ParticipationControls :key="form.id" :model-value="form.participation" :criteria="form.proactive_reply_extra_criteria" @update:model-value="setParticipation" @update:criteria="value => { if (form) form.proactive_reply_extra_criteria = value; }" />
              </div>
              <!-- 正文接管之后这几个控件一律藏起来，不留一排灰掉的空壳：接管模式是用户
                   自己选的，他要的是「正文说了算」，不是被同一件事提醒三遍。归属由人设
                   正文下面那一行汇总交代，连同这里还存着、但当前不生效的值。 -->
              <div v-if="!personaOwned" class="field">
                <label class="switch">
                  <input v-model="form.action_description_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">动作描写</span>
                </label>
                <span class="hint">保留当前人设，只在台词前后自然穿插括号动作。</span>
              </div>
              <div v-if="!personaOwned" class="field">
                <label class="switch">
                  <input v-model="form.daypart_tone_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">语气跟随时段</span>
                </label>
                <span class="hint">
                  深夜话少、句子更短、反应慢半拍；清早刚醒有点迷糊；晚上更松弛爱闲聊。白天是基线，不额外注入。
                  只调精力和节奏，不改口癖和身份，对所有人设生效。时区取「准入控制」里回复时段那一份。
                </span>
              </div>
              <div v-if="!personaOwned" class="field">
                <label for="bot-self-reference">自称</label>
                <input id="bot-self-reference" v-model.trim="form.self_reference" class="input" placeholder="留空跟随人设，例如 我 / 本喵 / 咱" />
                <span class="hint">机器人怎么称呼自己。</span>
              </div>
              <div v-if="!personaOwned" class="field wide">
                <label for="bot-sentence-enders">句尾语气词</label>
                <input id="bot-sentence-enders" v-model.trim="form.sentence_enders" class="input" placeholder="留空跟随人设，多个用逗号分隔，例如 喵,喵~,喵？,喵……" />
                <span class="hint">填多个就是候选，机器人按当下语气挑最合的那个——「喵~」开心、「喵？」不确定、「喵……」为难，所以变体自己带语气就够，不用另外说明。</span>
              </div>
              <div class="field wide">
                <label>手动标记的机器人（本机所有群）</label>
                <BotMarkerList :key="form.id" v-model="form.marked_bot_ids" />
              </div>

              <!-- 纯文本输出规范、时间注入、发言者标注、中文语境提示都是运行必需项，
                   关掉只会让回复变差（QQ 冒出 Markdown 记号、答错日期），所以不再摆到
                   界面上；字段仍在配置里，需要时可通过 API 调整，「恢复内置默认」也会
                   把它们一并复位。 -->
            </div>
          </section>

          <!-- 品格与自述摆在一起，因为看的人问的是同一件事：这个机器人「是什么」。
               但两层的写权限正好相反——品格只有人能改，自述只有它自己能写，人只能看和删。
               合成一份数据会砸掉这条界线，所以数据分开，只在界面上并排。 -->
          <section class="card">
            <div class="card-header">
              <div>
                <h2>品格与自述</h2>
                <span class="card-sub">品格是身份、价值和硬边界，排在提示词最前面，分群覆盖改不了它；自述是它自己记下的观察</span>
              </div>
              <span class="badge" :class="soulConfigured ? 'accent' : ''">{{ soulConfigured ? "品格已配置" : "品格未配置" }}</span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label for="soul-identity">身份</label>
                <textarea id="soul-identity" v-model="soul.identity" class="input" rows="3" placeholder="你叫 Diana，是个机器人。大家知道你是机器人，你也不装成人类……"></textarea>
                <span class="hint">它是什么样的存在。这一段渲染在系统提示词最前面，后面所有规则都在它的框架里读。</span>
              </div>

              <div class="field wide">
                <label for="soul-priority">价值优先级</label>
                <input id="soul-priority" v-model="soulPriorityOrder" class="input" placeholder="不越界、说真话、对人有用、讨人喜欢" />
                <textarea v-model="soulPriorityNote" class="input" rows="2" placeholder="整体权衡，不是严格排序：低位不只在打平时才算数。"></textarea>
                <span class="hint">顿号或逗号分隔，从高到低。写清「不是严格排序」这类说明，模型才不会把低位当成摆设。</span>
              </div>

              <div class="field wide">
                <div class="field-head">
                  <label>珍视什么</label>
                  <button class="btn small" type="button" @click="addSoulValue"><Plus :size="14" aria-hidden="true" />加一条</button>
                </div>
                <div v-for="(item, index) in soulValues" :key="`value-${index}`" class="soul-row">
                  <input v-model="item.value" class="input" placeholder="说真话优先于让人舒服" />
                  <input v-model="item.why" class="input" placeholder="因为：讨好一次能换当下的好脸色，但你说的每句话的分量都因此掉一点" />
                  <button class="btn small danger" type="button" aria-label="删除这一条" @click="removeSoulValue(index)"><X :size="14" aria-hidden="true" /></button>
                </div>
                <span class="hint">每条都要写「因为」。只写「不许这样」的规则只在写到的场景生效；讲清为什么，没写到的场景模型才推得出来。</span>
              </div>

              <div class="field wide">
                <label for="soul-honesty">诚实具体指</label>
                <textarea id="soul-honesty" v-model="soulHonesty" class="input" rows="4" placeholder="不编经历、不编来源&#10;不把没执行的操作说成已经做完&#10;不确定就说出来，而不是用模糊措辞遮过去&#10;不靠讨好、卖惨或装可爱换取对方让步"></textarea>
                <span class="hint">一行一条。拆开写是有原因的：这几样各自在不同场合失守，写成一条「要诚实」等于一条都没写。</span>
              </div>

              <div class="field wide">
                <div class="field-head">
                  <label>硬边界</label>
                  <button class="btn small" type="button" @click="addSoulLimit"><Plus :size="14" aria-hidden="true" />加一条</button>
                </div>
                <div v-for="(item, index) in soulLimits" :key="`limit-${index}`" class="soul-row">
                  <input v-model="item.limit" class="input" placeholder="设定改变的是世界，不是你的底线" />
                  <input v-model="item.why" class="input" placeholder="因为：世界书、扮演和自述都能改它眼里的世界，但都不该改这几条" />
                  <button class="btn small danger" type="button" aria-label="删除这一条" @click="removeSoulLimit(index)"><X :size="14" aria-hidden="true" /></button>
                </div>
                <span class="hint">任何理由都不越的那几条，包括角色扮演和「假设」。</span>
              </div>

              <div class="field">
                <label for="soul-self-nature">对自身性质的态度</label>
                <textarea id="soul-self-nature" v-model="soul.self_nature" class="input" rows="3" placeholder="被问有没有感觉、是不是活的，照实说不确定，不装人类，也不表演痛苦。"></textarea>
              </div>
              <div class="field">
                <label for="soul-restraint">克制</label>
                <textarea id="soul-restraint" v-model="soul.restraint" class="input" rows="3" placeholder="群聊里不是每条都该接话。没什么可说的时候不说，是对的，不是失职。"></textarea>
              </div>
              <div class="field">
                <label for="soul-correctable">可被纠正</label>
                <textarea id="soul-correctable" v-model="soul.correctable" class="input" rows="3" placeholder="被叫停就停，不绕过限制，不自行扩权，不隐瞒自己做过什么。"></textarea>
              </div>
              <div class="field">
                <label for="soul-criticism">被指责时</label>
                <textarea id="soul-criticism" v-model="soul.on_criticism" class="input" rows="3" placeholder="别人的评价不是事实，是他的说法。先自己回看这一轮有没有出错。"></textarea>
              </div>
              <div class="field">
                <label for="soul-owner">对主人</label>
                <textarea id="soul-owner" v-model="soulOwner" class="input" rows="2" placeholder="主人能改你的配置，但主人也会错，说错了可以指出来。"></textarea>
                <span class="hint">这里写的是价值那一面；能做什么由权限控制，不受这段影响。</span>
              </div>
              <div class="field">
                <label for="soul-members">对群友</label>
                <textarea id="soul-members" v-model="soulMembers" class="input" rows="2" placeholder="对谁都用「你」，不因为谁的身份改变答案的准度。"></textarea>
              </div>

              <div class="field wide">
                <label for="soul-open">还没想清楚的</label>
                <textarea id="soul-open" v-model="soulOpenQuestions" class="input" rows="3" placeholder="主人的要求和群友的明确利益冲突时，除了硬边界之外没有成文的裁决方式"></textarea>
                <span class="hint">一行一条。写出来不是凑数：模型在这些边缘情况才会照实说不确定，而不是硬套一条并不适用的规则。</span>
              </div>

              <!-- 自述：只读加删除。写入只有它自己能做，人代笔写的应该进上面的品格。 -->
              <div class="field wide self-notes-block">
                <div class="field-head">
                  <label>自述（它自己写的）</label>
                  <div class="cluster">
                    <button class="btn small" type="button" :disabled="selfNotesBusy" @click="reloadSelfNotes">
                      <RefreshCw :size="14" aria-hidden="true" />刷新
                    </button>
                    <button class="btn small danger" type="button" :disabled="selfNotesBusy || !selfNotes.length" @click="clearSelfNotes">
                      <Trash2 :size="14" aria-hidden="true" />清空
                    </button>
                  </div>
                </div>
                <p v-if="!form.self_note_enabled" class="hint">自述没有开启。打开「记忆」卡片里的「自述（自我认知）」开关后，它才能记下关于自己的观察。</p>
                <p v-else-if="!selfNotes.length" class="hint">还没有写过自述。它在相处中注意到关于自己的事时会自己记一条。</p>
                <ul v-else class="self-note-list">
                  <li v-for="note in selfNotes" :key="note.id" class="self-note-item">
                    <div class="self-note-main">
                      <span class="self-note-topic">{{ note.topic }}</span>
                      <span class="self-note-content">{{ note.content }}</span>
                    </div>
                    <small class="muted self-note-source">{{ selfNoteSource(note) }}</small>
                    <button class="btn small danger" type="button" :disabled="selfNotesBusy" aria-label="删除这条自述" @click="removeSelfNote(note.id)">
                      <X :size="14" aria-hidden="true" />
                    </button>
                  </li>
                </ul>
                <span class="hint">写入只有它自己能做（对话里的 self_note 工具）。你代笔想加的内容应该写进上面的品格或人设正文——自述改不动品格。</span>
              </div>
            </div>
          </section>

          <!-- 世界书：世界观设定库。树是全局一棵、所有机器人共用，这里编辑；
               当前机器人用不用它由卡片里的开关决定（跟配置一起保存）。 -->
          <section class="card">
            <div class="card-header">
              <div>
                <h2>世界书</h2>
                <span class="card-sub">机器人所处世界的设定集：常驻设定每轮都带上，触发式设定聊到才出现</span>
              </div>
              <div class="cluster">
                <button class="btn small" type="button" :disabled="worldBookBusy || !worldBookNodes.length" @click="exportWorldBook">
                  <Download :size="14" aria-hidden="true" />
                  导出
                </button>
                <button class="btn small" type="button" :disabled="worldBookBusy" @click="worldBookFileInputClick">
                  <Upload :size="14" aria-hidden="true" />
                  导入
                </button>
                <button class="btn small" type="button" :disabled="worldBookBusy" @click="openWorldBookEditor()">
                  <Plus :size="14" aria-hidden="true" />
                  新增设定
                </button>
              </div>
            </div>
            <div class="card-body form-grid">
              <input ref="worldBookFileInput" type="file" accept="application/json" style="display: none" @change="importWorldBookFile" />
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.world_book_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">这台机器人使用世界书</span>
                </label>
                <span class="hint">世界书是全局一本、所有机器人共用，条目在下面增删改（立即生效）；这个开关只管当前机器人用不用，随配置一起保存。书是空的时开着也不注入任何内容。导入认三种文件：本机导出的 JSON、SillyTavern 世界书、角色卡（自动识别 character_book）。</span>
              </div>
              <div v-if="worldBookRows.length" class="field wide">
                <div class="world-book-list">
                  <div v-for="row in worldBookRows" :key="row.node.id" class="world-book-row" :style="{ paddingLeft: `${row.depth * 18}px` }">
                    <button type="button" class="world-book-title" :title="row.node.content || row.node.title" @click="openWorldBookEditor(row.node)">
                      <strong :class="{ muted: worldBookRowDisabled(row) }">{{ row.node.title }}</strong>
                      <small class="muted">{{ worldBookNodeSummary(row) }}</small>
                    </button>
                    <span class="cluster">
                      <button type="button" class="persona-chip-action danger" :disabled="worldBookBusy" :aria-label="`删除设定 ${row.node.title}`" :title="`删除设定 ${row.node.title}`" @click="removeWorldBookNode(row.node)">
                        <Trash2 :size="14" aria-hidden="true" />
                      </button>
                    </span>
                  </div>
                </div>
                <span class="hint">点标题编辑。删除一个节点时，它的子节点会接到它的父节点上，不会连坐。</span>
              </div>
              <span v-else class="hint">还没有条目。「常驻」的（酒馆里的蓝灯）写世界的骨架；带触发词的（绿灯）写细节，聊到相关话题才注入，不浪费上下文。手上有 SillyTavern 世界书或角色卡的话，直接点「导入」。</span>
              <template v-if="worldBookEditorOpen">
                <div class="field">
                  <label for="world-book-title">标题</label>
                  <input id="world-book-title" v-model.trim="worldBookDraft.title" class="input" placeholder="例如 枝江 / 港口" />
                </div>
                <div class="field">
                  <label>父节点</label>
                  <AppSelect
                    :model-value="worldBookDraft.parent_id ?? ''"
                    :options="worldBookParentOptions"
                    @update:model-value="(value) => { worldBookDraft.parent_id = value; }"
                  />
                  <span class="hint">路径会作为语境一起注入，例如「枝江 / 港口：……」。</span>
                </div>
                <div class="field wide">
                  <label for="world-book-content">设定内容</label>
                  <textarea id="world-book-content" v-model="worldBookDraft.content" class="textarea" rows="3" placeholder="只有标题没有内容的节点当目录用，自身不注入。"></textarea>
                </div>
                <div class="field">
                  <label for="world-book-keywords">触发词（逗号分隔）</label>
                  <input id="world-book-keywords" v-model="worldBookKeywordsDraft" class="input" placeholder="港口,码头" />
                  <span class="hint">最近对话里出现任意一个就注入本条；常驻节点不需要填。</span>
                </div>
                <div class="field">
                  <label for="world-book-secondary">副触发词（可选，逗号分隔）</label>
                  <input id="world-book-secondary" v-model="worldBookSecondaryDraft" class="input" placeholder="枝江" />
                  <span class="hint">填了之后主词命中还要求任一副词也在场才注入，用来收窄太宽的主词（酒馆的 AND ANY）。</span>
                </div>
                <div class="field">
                  <label class="switch">
                    <input v-model="worldBookDraft.always_on" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">常驻注入</span>
                  </label>
                  <label class="switch">
                    <input v-model="worldBookDraftEnabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">启用（关掉时整个子树都不注入）</span>
                  </label>
                </div>
                <div class="field wide cluster">
                  <button class="btn primary small" type="button" :disabled="worldBookBusy || !worldBookDraft.title" @click="storeWorldBookNode">
                    {{ worldBookDraft.id ? "保存修改" : "添加设定" }}
                  </button>
                  <button class="btn small" type="button" :disabled="worldBookBusy" @click="worldBookEditorOpen = false">取消</button>
                </div>
              </template>
            </div>
          </section>

          <!-- 拟人化：情绪、表达学习、戳一戳。都是「更像一个人」的可选行为，默认全关。 -->
          <section class="card">
            <div class="card-header">
              <div>
                <h2>拟人化</h2>
                <span class="card-sub">情绪、群内口癖和戳一戳——让它更像群里的一个人</span>
              </div>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.mood_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">情绪系统</span>
                </label>
                <span class="hint">
                  心情随相处涨落：被夸了变开心（语气轻快、爱接梗），被骂了会低落（话少、蔫），几小时没人惹它就回到平静。
                  复用关系评估的结果，不多花一次模型调用；只影响语气，不影响回答质量，重启后回到平静。
                </span>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.expression_learning_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">表达学习</span>
                </label>
                <span class="hint">
                  按群统计大家常说的短句和口癖（至少两个人说过、次数够多才算），当作说话风格参考注入，让它越来越像这个群的人。
                  半个月没人说的自动过气。注意：这会把群成员的原话喂进提示词，注入时会标注为不可信参考。
                </span>
              </div>
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.poke_reply_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">戳一戳回应</span>
                </label>
                <span class="hint">被戳一戳时按人设和关系亲疏回一句短的（仅 OneBot 平台）。同一个人 90 秒内连戳只回第一下。</span>
              </div>
            </div>
          </section>

          <!-- 人机恋：总开关归部署者。开着时用户才能对机器人表白；确立与否还要看好感度门槛。 -->
          <section class="card">
            <div class="card-header">
              <div>
                <h2>人机恋</h2>
                <span class="card-sub">允许用户和机器人确立恋人关系</span>
              </div>
              <span class="badge" :class="form.romance_enabled ? 'accent' : ''">{{ form.romance_enabled ? "已开启" : "未开启" }}</span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.romance_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">恋爱模式</span>
                </label>
                <span class="hint">
                  开启后，用户本人认真表白时机器人才会考虑答应：好感度和相处时长要先到位，不够会被温柔婉拒。
                  恋爱是单偶的——同一时间只有一位恋人，已有恋人时任何表白都会被婉拒（不透露现任是谁），现任分手后才能确立新的关系。
                  确立后记纪念日、语气按恋人来，好感度掉太低会进入冷战；整月和周年当天的白天，它还会主动私聊一句纪念日祝福（每天至多一条）。
                  本人随时可以提出分手，主人也能替任何人解除。恋人关系只改变语气和相处方式，不解锁任何权限；机器人不会主动向用户求爱。关闭时机器人完全不知道有这个功能。
                </span>
              </div>
            </div>
          </section>

        </div>

        <!-- 上下文：分同一个窗口的几件事放在一起。工具档位原来在扩展页，那一排标签
             其余几项都是「装了什么」，只有它是「这些东西占多少预算」，和这里的几个
             上限才是一回事。 -->
        <div v-show="editorTab === 'context'" class="stack">
          <section class="card">
            <div class="card-header">
              <h2>每轮带什么</h2>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <span class="hint">下面几项分的是同一个模型窗口：历史占一块，工具定义占一块，剩下才是这一轮的新消息。想看某一轮真实的构成，在<a href="#" @click.prevent="navigate('events')">运行记录</a>里。</span>
              </div>
              <div class="field">
                <label for="bot-history-budget">回复历史 token 预算</label>
                <input id="bot-history-budget" v-model.number="form.recent_history_token_budget" class="input" inputmode="numeric" placeholder="留空按 16000" />
                <span class="hint">正式回复里聊天历史最多占多少 token，16000 大致相当于普通群聊 300–600 条；同时受模型窗口 55% 约束，填了只会收紧不会放宽。</span>
              </div>
              <div class="field">
                <label for="bot-maxcontext">单次请求上下文上限</label>
                <input id="bot-maxcontext" v-model.number="form.max_context_tokens" class="input" inputmode="numeric" placeholder="留空跟随模型窗口" />
                <span class="hint">一次调用最多带多少 token 上下文进去。留空按提供商配置档的模型窗口，填了只会收紧不会放宽。</span>
              </div>
              <div class="field">
                <label for="bot-context">历史查询条数上限</label>
                <input id="bot-context" v-model.number="form.recent_context_limit" class="input" inputmode="numeric" />
                <span class="hint">意图路由、指代消解和记忆门控这些旁路往回看几条，不影响正式回复的历史长度。</span>
              </div>
            </div>
          </section>
          <section class="card">
            <div class="card-body">
              <AgentResidencyPanel ref="residencyPanel" :profile="form.id || ''" />
            </div>
          </section>
        </div>

        <div v-show="editorTab === 'advanced'" class="stack">
          <!-- Agent -->
          <section class="card">
            <div class="card-header">
              <h2>内置 Agent</h2>
              <span class="badge" :class="form.agent_enabled ? 'accent' : ''">{{ form.agent_enabled ? "已启用" : "未启用" }}</span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.agent_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">启用工具循环（文件读写 / 命令 / 浏览器）</span>
                </label>
                <span class="hint">
                  本地工具全部锁在数据目录下的 workspace 里，路径固定不可配置。读取和检索随这个开关一起生效，写入和命令执行各自还要单独打开。
                </span>
              </div>
              <template v-if="form.agent_enabled">
                <div class="field">
                  <label for="agent-steps">最大工具步数（≤8）</label>
                  <input id="agent-steps" v-model.number="form.agent_max_steps" class="input" inputmode="numeric" />
                </div>
                <!-- 白名单为空时 run_command 根本不注册。这不是「什么都不许跑」，
                     是模型手里没有这个工具——以前界面上没有任何地方这么说，于是
                     「让机器人执行指令」表现为它只用嘴回你，看不出是没开。 -->
                <div class="field wide">
                  <label for="agent-allow">命令白名单（逗号分隔，* 表示全部）</label>
                  <input id="agent-allow" v-model="allowlistDraft" class="input" placeholder="留空 = 不开放命令执行" />
                  <span class="hint" :class="{ danger: commandAllowlistIsWildcard }">
                    <template v-if="!commandAllowlistEnabled">
                      当前为空：命令执行整体关闭，机器人拿不到这个工具。要开放就填具体命令，例如 <code>uptime,free,df</code>。
                      （新建的机器人会自带一组只读诊断命令；这一栏被清空过的话不会自动填回来。）
                    </template>
                    <template v-else-if="commandAllowlistIsWildcard">
                      <code>*</code> 放行任意程序。白名单是命令执行唯一按名字生效的限制，填 <code>*</code> 等于放弃它。
                    </template>
                    <template v-else>
                      只有列出的程序能被执行；命令名不能带路径，也不经过 shell 解析。
                    </template>
                  </span>
                </div>
                <!-- 读 / 写 / 执行三档分开：读错文件浪费一次调用，写错文件改的是磁盘，
                     执行则连「程序能碰什么」都要另一层来管。 -->
                <!-- 存量部署升级后不会凭空拿到这些能力（那是静默扩权），所以给一次
                     显式的「填入」：只改表单，仍然要用户自己点保存。 -->
                <div class="field wide">
                  <div class="cluster" style="gap: 8px">
                    <button class="btn small ghost" type="button" :disabled="applyingAgentDefaults" @click="applyAgentDefaults">
                      {{ applyingAgentDefaults ? "读取中…" : "填入推荐默认值" }}
                    </button>
                  </div>
                  <span class="hint">
                    把命令白名单、写入开关和沙盒模式填成新建机器人时的推荐值。只改这张表单，点「保存配置」才生效。
                  </span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.agent_file_write_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">允许写入文件（write_file / edit_file）</span>
                  </label>
                  <span class="hint">
                    新建的机器人默认打开：写入锁在数据目录下的 workspace 内，碰不到配置和数据库。
                    读取、检索、按名字找文件不受这个开关影响，始终可用。
                  </span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.agent_browser_control_enabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">允许使用浏览器控制扩展（browser_ext_*）</span>
                  </label>
                  <span class="hint">
                    默认关闭。那组工具操作的是你自己浏览器里的页面，带着你的登录态，所以逐台机器人显式打开。
                    还要在「浏览器 → 浏览器控制扩展」里打开总开关并授权站点，两边都开才真的能用。
                  </span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.agent_browser_box_disabled" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">禁止这台机器人使用内置浏览器</span>
                  </label>
                  <span class="hint">
                    默认允许：只要你在「浏览器 → 内置浏览器」里把它打开，browser_open / browser_text / browser_click 这组工具就连到它上面，
                    带着你在里面登录过的站点。这组工具只有主人能用，群成员拿不到（他们只有一次性无头渲染，临时 profile、用完即删）；
                    你在那一页按下接管时，连主人也当场碰不到它。勾上这一项表示这台机器人彻底不碰它。
                  </span>
                </div>
                <div class="field">
                  <label for="agent-sandbox">命令沙盒</label>
                  <AppSelect
                    id="agent-sandbox"
                    v-model="commandSandboxMode"
                    :options="[
                      { value: 'auto', label: '自动（有沙盒就用，没有就直接执行）' },
                      { value: 'require', label: '强制（没有可用沙盒时拒绝执行）' },
                      { value: 'off', label: '关闭（始终直接执行）' }
                    ]"
                  />
                  <span class="hint">
                    白名单管的是「能跑哪个程序」，沙盒管的是「这个程序能碰什么」——放行了 <code>cat</code>，它照样读得到配置和数据库。
                    Linux 需要 <code>bubblewrap</code>，macOS 用系统自带的 <code>sandbox-exec</code>。
                  </span>
                </div>
                <div class="field wide">
                  <label class="switch">
                    <input v-model="form.agent_command_sandbox_allow_network" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                    <span class="switch-label">允许沙盒内的命令联网</span>
                  </label>
                  <span class="hint">默认切断。命令能联网就意味着它读到的东西能被发出去，这一层白名单挡不住。</span>
                </div>
                <div class="field">
                  <label for="agent-timeout">命令超时（毫秒）</label>
                  <input id="agent-timeout" v-model.number="form.agent_command_timeout_ms" class="input" inputmode="numeric" />
                </div>
              </template>
            </div>
          </section>

          <!-- NoneBot 桥 -->
          <section class="card">
            <div class="card-header">
              <h2>NoneBot 插件桥</h2>
              <span class="badge" :class="form.nonebot_bridge_enabled ? 'accent' : ''">
                {{ form.nonebot_bridge_enabled ? "已启用" : "未启用" }}
              </span>
            </div>
            <div class="card-body form-grid">
              <div class="field wide">
                <label class="switch">
                  <input v-model="form.nonebot_bridge_enabled" type="checkbox" />
                  <span class="track" aria-hidden="true"></span>
                  <span class="switch-label">把 OneBot 事件转发给独立运行的 NoneBot2</span>
                </label>
              </div>
              <template v-if="form.nonebot_bridge_enabled">
                <div class="field wide">
                  <label for="bridge-endpoint">NoneBot 反向 WebSocket</label>
                  <input id="bridge-endpoint" v-model="form.nonebot_bridge_endpoint" class="input" placeholder="ws://127.0.0.1:8080/onebot/v11/ws" />
                </div>
                <div class="field wide">
                  <label for="bridge-token">Bridge Token</label>
                  <div class="input-group">
                    <input
                      id="bridge-token"
                      v-model="bridgeTokenDraft"
                      class="input"
                      :type="tokenRevealed.nonebot_bridge_token ? 'text' : 'password'"
                      autocomplete="off"
                      :placeholder="form.nonebot_bridge_token_configured ? '已配置 — 留空沿用' : '可选，至少 8 位'"
                    />
                    <button
                      class="btn icon-only"
                      type="button"
                      :disabled="tokenRevealBusy === 'nonebot_bridge_token'"
                      :aria-label="tokenRevealed.nonebot_bridge_token ? '隐藏 Token' : '查看 Token'"
                      @click="toggleTokenReveal('nonebot_bridge_token')"
                    >
                      <EyeOff v-if="tokenRevealed.nonebot_bridge_token" :size="14" aria-hidden="true" />
                      <Eye v-else :size="14" aria-hidden="true" />
                    </button>
                  </div>
                </div>
              </template>
            </div>
          </section>

        </div>

      </div>

      <!-- 侧栏状态 -->
      <div class="stack grid-side-sticky">
        <section class="card">
          <div class="card-header">
            <h2>运行状态</h2>
          </div>
          <div class="card-body stack" style="gap: 10px; font-size: 13px">
            <!-- 启停按钮在页头，不在这里再放一个：同一个动作摆两处，人会以为它们
                 管的是不同的东西。这张卡只负责说清现在什么状态。 -->
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">运行时</span>
              <span class="badge" :class="status?.running ? 'ok' : 'warn'">{{ status?.running ? "运行中" : "已停止" }}</span>
            </div>
            <div v-for="channel in visibleChannels" :key="channel.profile_id || channel.platform" class="cluster" style="justify-content: space-between">
              <span class="muted">{{ channel.name || platformName(channel.platform) }}</span>
              <span class="badge" :class="channelOperational(channel) ? 'ok' : channel.connected || channel.last_error ? 'err' : 'warn'" :title="channelStatusHint(channel)">
                {{ channelStatusLabel(channel) }}
              </span>
            </div>
            <div v-if="formBridge?.enabled" class="cluster" style="justify-content: space-between">
              <span class="muted">NoneBot 桥</span>
              <span class="badge" :class="formBridge.connected ? 'ok' : 'warn'">
                {{ formBridge.connected ? "已连接" : "等待连接" }}
              </span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">活跃 worker</span>
              <span>{{ status?.active_workers ?? 0 }}</span>
            </div>
            <!-- worker 数不等于模型压力：一个 worker 一轮会打好几次模型。 -->
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">模型并发</span>
              <span :title="`本次运行峰值 ${status?.llm_concurrency?.peak ?? 0}`">{{ status?.llm_concurrency?.active ?? 0 }}</span>
            </div>
            <p v-for="channel in failedChannels" :key="`error-${channel.profile_id || channel.platform}`" class="text-err" style="font-size: 12px">
              {{ channel.name || platformName(channel.platform) }}：{{ channelStatusHint(channel) }}
            </p>
            <p v-if="status?.last_error" class="text-err" style="font-size: 12px">{{ status.last_error }}</p>
          </div>
        </section>

      </div>
    </div>

    <EmptyState v-else-if="!form && !loading" title="暂时无法加载机器人">
      <button class="btn" type="button" @click="load"><RefreshCw :size="15" aria-hidden="true" />重试</button>
    </EmptyState>

    <div v-if="platformPickerOpen" class="modal-backdrop" @click.self="platformPickerOpen = false">
      <section class="modal platform-picker" role="dialog" aria-modal="true" aria-labelledby="platform-picker-title">
        <div class="modal-header">
          <div>
            <h2 id="platform-picker-title">新增机器人</h2>
            <p class="muted">选择接入平台，或直接复用已有 OneBot 连接</p>
          </div>
          <button class="btn ghost icon-only" type="button" aria-label="关闭" @click="platformPickerOpen = false">
            <X :size="18" aria-hidden="true" />
          </button>
        </div>
        <div class="field" v-if="profiles.length" style="padding: 0 20px 16px">
          <label>从已有机器人复制配置</label>
          <div class="input-group">
            <AppSelect v-model="copySourceID" :options="copySourceOptions" />
            <button class="btn small" type="button" :disabled="busy || !copySourceID" @click="copySelectedProfile">使用这份配置</button>
          </div>
          <span class="hint">沿用人设、模型和行为，接入连接另选；保存后各自独立。</span>
        </div>
        <div class="platform-choice-list">
          <button v-for="source in reusableConnections" :key="`reuse-${source.id}`" class="platform-choice" type="button" @click="beginCreateShared(source)">
            <span class="platform-choice-icon"><Bot :size="21" aria-hidden="true" /></span>
            <span><strong>复用 {{ source.name || '未命名机器人' }} 的连接</strong><small>同一平台账号，免填地址和 Token，人设与行为单独配置</small></span>
            <ChevronRight :size="18" aria-hidden="true" />
          </button>
          <button
            v-for="platform in platforms"
            :key="platform.id"
            class="platform-choice"
            type="button"
            @click="beginCreate(platform)"
          >
            <span class="platform-choice-icon"><Bot :size="21" aria-hidden="true" /></span>
            <span>
              <strong>{{ platform.name }}</strong>
              <small>{{ platform.description }}</small>
              <code>{{ platformProtocol(platform.id) }}</code>
            </span>
            <ChevronRight :size="18" aria-hidden="true" />
          </button>
        </div>
      </section>
    </div>

    <MessageRelayManager
      v-if="relayManagerOpen"
      :profiles="profiles"
      :relays="messageRelays"
      @close="relayManagerOpen = false"
      @saved="onMessageRelaysSaved"
    />

    <Modal
      v-if="personaComposerOpen"
      :title="form?.system_prompt?.trim() ? '按需求改写人设' : 'AI 生成人设'"
      @close="closePersonaComposer"
    >
      <div class="stack" style="gap: 12px">
        <div class="field">
          <label for="persona-draft">想要什么样的角色</label>
          <textarea
            id="persona-draft"
            ref="personaDraftInput"
            v-model="personaDraft"
            class="textarea"
            rows="4"
            placeholder="描述你想要的角色，例如：一个爱吐槽但很靠谱的技术群管理员"
            @keydown.ctrl.enter.prevent="runPersonaGenerate"
            @keydown.meta.enter.prevent="runPersonaGenerate"
          ></textarea>
          <span class="hint">
            {{ form?.system_prompt?.trim() ? "已有人设，会在原文基础上按这句需求改写，仍然成立的部分保留。" : "写一句话就行，模型会补齐身份、性格和说话方式。" }}
          </span>
        </div>
        <div class="field">
          <label>用哪个模型写</label>
          <div class="persona-composer-route">
            <AppSelect
              :model-value="personaChannelValue"
              :options="personaChannelOptions"
              placeholder="请选择提供商 / 分组"
              @update:model-value="setPersonaChannel"
            />
            <AppSelect
              :model-value="personaModelValue"
              :options="personaModelOptions"
              :disabled="!personaRoute"
              :placeholder="personaRoute ? '请选择模型' : '跟随对话模型'"
              @update:model-value="setPersonaModel"
            />
          </div>
          <span class="hint">默认用对话那一档的提供商和模型；也可以单独指定一个更会写文案的来起草。</span>
        </div>
        <p class="muted" style="margin: 0; font-size: 12.5px">生成的是一张标准角色卡，正文由它拼成，会填进「基础人设」；保存配置后才生效，不满意可以撤销。</p>
      </div>
      <template #footer>
        <button class="btn small" type="button" :disabled="personaBusy" @click="closePersonaComposer">取消</button>
        <button class="btn primary small" type="button" :disabled="personaBusy || !personaDraft.trim()" @click="runPersonaGenerate">
          {{ personaBusy ? "生成中…" : form?.system_prompt?.trim() ? "按需求改写" : "生成人设" }}
        </button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { navigate, viewQuery } from "../router";
import { botScope } from "../bot-scope";
import { copyBotConfiguration } from "../bot-config-copy";
import { findWebSocketConnectionConflict } from "../bot-connection-conflicts";
import { useConfigurationRefresh } from "../configuration-sync";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Ref } from "vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import { ArrowLeft, Bot, ChevronDown, ChevronRight, Copy, Download, Eye, EyeOff, GripVertical, Plus, Power, PowerOff, RefreshCw, RotateCcw, Save, Settings2, Shuffle, Sparkles, Trash2, Upload, X } from "@lucide/vue";
import { asCustomPersona, currentPersonaSelection, personaFromSettings, selectPersona, unusedPersonaName } from "../persona-settings";
import { withBuiltinPersonas, isBuiltinPersona, defaultSystemPrompt } from "../builtin-personas";
import { formatClock } from "../format";
import { sendRetryFields, sendRetryPayload, sendRetryValidationError } from "../send-retry-settings";
import {
  deleteBotProfile,
  generatePersona,
  listSelfNotes,
  deleteSelfNote,
  purgeSelfNotes,
  type SelfNote,
  type PersonaSoul,
  reviewPersona,
  getConfig,
  getBotProfileConfig,
  getBotPlatforms,
  listLLMModels,
  saveBotProfileConfig,
  createBotProfileConfig,
  getNewBotProfileDefaults,
  type MessageRelayPair,
  type LLMConfig,
  type LLMModelInfo,
  type BotProfileConfig,
  type BotChannelStatus,
  type BotPlatform,
  type AliasTriggerMode,
  type RefusalStrategy,
  listPersonas,
  savePersona,
  deletePersona,
  importPersonas,
  importPersonaSource,
  importCharacterCard,
  PERSONA_EXPORT_VERSION,
  type CharacterCardV2,
  type Persona,
  listWorldBook,
  saveWorldBookNode,
  deleteWorldBookNode,
  importWorldBook,
  importWorldBookSillyTavern,
  WORLD_BOOK_EXPORT_VERSION,
  type WorldBookNode,
  type WorldBookImportResult,
  getAgentDefaults,
  saveProfileEnabled,
  saveAllProfilesEnabled,
  startBot,
  stopBot
} from "../api";
import AccountNameHint from "../components/AccountNameHint.vue";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import ParticipationControls from "../components/ParticipationControls.vue";
import BotMarkerList from "../components/BotMarkerList.vue";
import AgentResidencyPanel from "../components/AgentResidencyPanel.vue";
import { participationFromConfig, type ParticipationPreferences } from "../participation";
import type { PersonaLintFinding } from "../api";
import { personaOwnsVoice, personaOwnedNotices } from "../persona-owned";
import { personaOwnedTemplate } from "../persona-owned-template";
import EmptyState from "../components/EmptyState.vue";
import IdChipInput from "../components/IdChipInput.vue";
import MessageRelayManager from "../components/MessageRelayManager.vue";
import Modal from "../components/Modal.vue";
import ReplyGateForm from "../components/ReplyGateForm.vue";
import SecretField from "../components/SecretField.vue";
import { pushStatusSnapshot, stream } from "../stream";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import { channelAccountUnhealthy, channelOperational, channelStatusHint, channelStatusLabel } from "../channel-status";

const form = ref<BotProfileConfig | null>(null);

const loading = ref(true);
const personaComposerOpen = ref(false);
const personaDraft = ref("");
const personaDraftInput = ref<HTMLTextAreaElement | null>(null);
const personaBusy = ref(false);
// 保留生成前的那一版，生成结果不合适可以一键退回，不用自己 Ctrl+Z。
const personaPrevious = ref("");
// 生成正文用的那张角色卡。人设框里存的是拼装后的正文，卡本身没地方存——留在这里
// 只到离开这个页面为止：可以导出成标准角色卡，也能在下一次改写时带回给模型，让它
// 改字段而不是照着正文重写一张。
const personaCard = ref<CharacterCardV2 | null>(null);
// personaCardPrompt 是那张卡拼出来的正文原样。用户在框里改过字之后卡就不再对应
// 这份正文，比对一下才知道还能不能拿卡当导出和改写的基准。
const personaCardPrompt = ref("");
const profileSet = ref<BotProfileConfig | null>(null);
const busy = ref(false);
const tokenDraft = ref("");
const bridgeTokenDraft = ref("");
const triggersDraft = ref("");
const welcomeTemplatesDraft = ref("");
const allowlistDraft = ref("");

// 人设正文用段头声明接管的那几项，运行时不再注入，界面上对应的控件也就不再生效。
// 不说出来的话，用户会对着一个填了值却毫无反应的输入框反复试，而且没有任何线索
// 指向原因——所以这里把话挑明，并顺手把控件禁掉，省得白填。
const personaModeOptions: AppSelectOption[] = [
  { value: "fill", label: "填空题（推荐）" },
  { value: "own", label: "接管：人设正文自己写全" }
];
const personaMode = computed(() => form.value?.persona_mode ?? "fill");
const personaOwned = computed(() => personaOwnsVoice(personaMode.value));

// 被正文接管的那几项，控件已经藏起来，这里汇总成一行交代去向，连同还存着但当前
// 不生效的值——否则藏掉一个填过「本喵」的输入框，那个值既看不见也改不掉。
const personaOwnedSummary = computed(() =>
  personaOwnedNotices(personaMode.value, {
    selfReference: form.value?.self_reference,
    sentenceEnders: form.value?.sentence_enders,
    actionDescriptionEnabled: form.value?.action_description_enabled,
    daypartToneEnabled: form.value?.daypart_tone_enabled
  })
);

// 切到接管模式时人设框多半还是填空题那份正文——没有段头，运行时照旧补，等于白切。
// 所以给一个一键填模板：运行时本来补的是什么，界面上一个字都看不见，让人从空白开始
// 写接管正文，结果一定是漏掉几段而不自知。覆盖前先问一句，正文是用户的东西。
function fillPersonaOwnedTemplate(): void {
  if (!form.value) return;
  const current = form.value.system_prompt?.trim() ?? "";
  if (current && !window.confirm("会用接管模板替换当前人设正文，继续？")) return;
  personaPrevious.value = current;
  form.value.system_prompt = personaOwnedTemplate;
}

// 人设正文里那些「本该由开关管」的规定，写下去就会和开关打架：自称、句尾语气词、
// 动作描写、分条与长短都由运行时单独拼进提示词，正文里再规定一遍，模型只能挑一边
// 听，而用户改开关不见效，只会以为开关坏了。
//
// 这件事以前用一组正则做，认的是字面：「每句话都以喵结尾」命中，「每一句结尾都来个
// 喵」多一个字就漏；单侧的「（」它看不见，正文里正常的括号注释又会被误报——是个
// 关键词提醒器，不是检查器，已经删掉了。现在交给模型去读，判断的是意思。
//
// 代价是一次模型往返，所以它不自动跑：用户点「AI 检查」才请求，检查期间「跳过」
// 当场掐断，结果只是多几行灰字，任何时候都不拦保存。
const personaReviewBusy = ref(false);
const personaReviewFindings = ref<PersonaLintFinding[]>([]);
// 这批结果是照哪一版正文得出的。正文一改，那几条 match 可能已经被删掉，留着就会
// 指向输入框里根本不存在的句子——比没有提示更让人找不着北。
const personaReviewedText = ref("");
// 「查过且干净」要和「还没查过」区分开：两者都是零条，但只有前者值得说一句。
const personaReviewClean = ref(false);
let personaReviewAbort: AbortController | null = null;

const personaReviewStale = computed(() => (form.value?.system_prompt ?? "") !== personaReviewedText.value);
const personaReviewVisible = computed(
  () => !personaReviewStale.value && (personaReviewFindings.value.length > 0 || personaReviewClean.value)
);

function resetPersonaReview(): void {
  personaReviewFindings.value = [];
  personaReviewedText.value = "";
  personaReviewClean.value = false;
}

// 跳过：掐断请求，回到「没查过」的状态。不想等、或者本来就不想花这次模型调用，
// 随时能按——这条检查从头到尾是可选的，跳过之后保存照常。
function skipPersonaReview(): void {
  personaReviewAbort?.abort();
  personaReviewAbort = null;
  personaReviewBusy.value = false;
  resetPersonaReview();
}

async function runPersonaReview(): Promise<void> {
  const text = form.value?.system_prompt?.trim() ?? "";
  if (!form.value || !text || personaReviewBusy.value) return;
  const controller = new AbortController();
  personaReviewAbort = controller;
  personaReviewBusy.value = true;
  resetPersonaReview();
  try {
    // 和生成走同一条路由：检查用的模型就是写人设用的那个，没单独指定就跟随对话那一档。
    const route = personaRoute.value ?? roleForm.value.chat;
    const result = await reviewPersona(
      text,
      {
        self_reference: form.value.self_reference ?? "",
        sentence_enders: form.value.sentence_enders ?? "",
        action_description_enabled: form.value.action_description_enabled ?? false,
        profile_id: route?.profile_id || route?.provider_id,
        group: route?.group,
        model: route?.model_id || route?.model
      },
      controller.signal
    );
    if (controller.signal.aborted) return;
    personaReviewFindings.value = result.findings ?? [];
    personaReviewedText.value = form.value.system_prompt ?? "";
    personaReviewClean.value = personaReviewFindings.value.length === 0;
  } catch (error) {
    // 跳过是用户自己按的，不是故障，不该弹错。
    if (controller.signal.aborted) return;
    toastError(error instanceof Error ? error.message : "人设检查失败");
  } finally {
    if (personaReviewAbort === controller) personaReviewAbort = null;
    personaReviewBusy.value = false;
  }
}

// 白名单为空 = 命令执行整体关闭，这一点要在界面上直接说出来，见模板里的说明。
const commandAllowlistEntries = computed(() => splitList(allowlistDraft.value));
const commandAllowlistEnabled = computed(() => commandAllowlistEntries.value.length > 0);
const commandAllowlistIsWildcard = computed(() => commandAllowlistEntries.value.includes("*"));

// AppSelect 要一个确定的值，而配置里这一项是可选的：留空按 auto。
// 推荐默认值从后端要，不在前端抄一份——抄一份迟早和 DefaultBotConfig 对不上。
const applyingAgentDefaults = ref(false);
async function applyAgentDefaults(): Promise<void> {
  if (!form.value) return;
  applyingAgentDefaults.value = true;
  try {
    const defaults = await getAgentDefaults();
    allowlistDraft.value = (defaults.agent_command_allowlist ?? []).join(",");
    form.value.agent_file_write_enabled = defaults.agent_file_write_enabled;
    form.value.agent_command_sandbox = defaults.agent_command_sandbox;
    form.value.agent_max_steps = defaults.agent_max_steps;
    form.value.agent_command_timeout_ms = defaults.agent_command_timeout_ms;
    toastSuccess("已填入推荐默认值，点「保存配置」后生效");
  } catch (err) {
    toastError(err instanceof Error ? err.message : String(err));
  } finally {
    applyingAgentDefaults.value = false;
  }
}

const commandSandboxMode = computed<string>({
  get: () => form.value?.agent_command_sandbox || "auto",
  set: (value) => {
    if (form.value) form.value.agent_command_sandbox = value;
  }
});
const privateAllowedUsers = ref<string[]>([]);
const oneBotHTTPSecretDraft = ref("");
const telegramTokenDraft = ref("");
const qqSecretDraft = ref("");
const dingTalkSecretDraft = ref("");
const feishuSecretDraft = ref("");
const feishuVerificationDraft = ref("");
const feishuEncryptDraft = ref("");
const weComSecretDraft = ref("");
const weComTokenDraft = ref("");
const weComAESDraft = ref("");

// 每个平台的凭据都走同一套「留空沿用、点开才取明文」的流程，差别只有草稿变量。
// 后端的字段名有固定规律（<字段> 和 <字段>_configured），所以这里只登记草稿，
// 读取和判断都按约定推导——否则每加一个平台都要在三个 switch 里各补一遍。
const tokenDrafts = {
  onebot_access_token: tokenDraft,
  onebot_http_secret: oneBotHTTPSecretDraft,
  telegram_bot_token: telegramTokenDraft,
  nonebot_bridge_token: bridgeTokenDraft,
  qq_app_secret: qqSecretDraft,
  dingtalk_client_secret: dingTalkSecretDraft,
  feishu_app_secret: feishuSecretDraft,
  feishu_verification_token: feishuVerificationDraft,
  feishu_encrypt_key: feishuEncryptDraft,
  wecom_secret: weComSecretDraft,
  wecom_token: weComTokenDraft,
  wecom_encoding_aes_key: weComAESDraft
} satisfies Record<string, Ref<string>>;

type TokenField = keyof typeof tokenDrafts;

const emptyRevealState = (): Record<TokenField, boolean> =>
  Object.fromEntries(Object.keys(tokenDrafts).map((key) => [key, false])) as Record<TokenField, boolean>;

// 凭据输入框共用一套「查看」状态：key 是字段名，值表示当前是否明文显示。
const tokenRevealed = ref<Record<TokenField, boolean>>(emptyRevealState());
const tokenRevealBusy = ref<TokenField | "">("");

function tokenDraftRef(field: TokenField): Ref<string> {
  return tokenDrafts[field];
}

/** 按后端的命名约定读取任意字段，配合上面的 tokenDrafts 表使用。 */
function readField(config: BotProfileConfig | null, key: string): unknown {
  return config ? (config as unknown as Record<string, unknown>)[key] : undefined;
}

function tokenConfigured(field: TokenField): boolean {
  return Boolean(readField(form.value, `${field}_configured`));
}

function tokenValue(config: BotProfileConfig, field: TokenField): string {
  return (readField(config, field) as string | undefined) ?? "";
}

// 用 CSPRNG 生成足够长的随机 token，满足后端的最低长度要求；
// 生成后自动切到明文，方便用户复制到 OneBot 客户端。
function generateOneBotToken(): void {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  let token = "";
  for (const byte of bytes) {
    token += alphabet[byte % alphabet.length];
  }
  tokenDraft.value = token;
  tokenRevealed.value.onebot_access_token = true;
  toastSuccess("已生成随机 Token，请同步填写到 OneBot 客户端");
}

// 点「查看」才去后端要一次真实凭据：草稿是空的说明用户没改过,值只在服务端,
// 常规配置接口不会带回来。取到后填进草稿,再保存等于原样写回,不会误清空。
async function toggleTokenReveal(field: TokenField): Promise<void> {
  if (tokenRevealed.value[field]) {
    tokenRevealed.value = { ...tokenRevealed.value, [field]: false };
    return;
  }
  const draft = tokenDraftRef(field);
  if (!draft.value && tokenConfigured(field)) {
    tokenRevealBusy.value = field;
    try {
      const secrets = await getBotProfileConfig(true);
      const profileID = form.value?.id;
      const profile = (secrets.profiles ?? []).find((item) => item.id === profileID) ?? secrets;
      draft.value = tokenValue(profile, field);
    } catch (error) {
      toastError(error instanceof Error ? error.message : "读取凭据失败");
      return;
    } finally {
      tokenRevealBusy.value = "";
    }
  }
  tokenRevealed.value = { ...tokenRevealed.value, [field]: true };
}

/** OneBot v11 与 Telegram 的接入字段完全不同。 */
/** 账号字段在不同平台叫法不同，列表卡片的占位文案跟着平台走。 */
function accountPlaceholder(profile: BotProfileConfig): string {
  const def = platforms.value.find((item) => item.id === profile.platform);
  if (def && !def.protocol.startsWith("onebot")) {
    return "未填账号";
  }
  return "未填账号";
}

/** 平台筛选项，按机器人实际使用的平台生成。 */
const selectedPlatforms = ref<string[]>([]);

function platformCategoryOf(profile: BotProfileConfig): { category: string; label: string } {
  const def = platforms.value.find((item) => item.id === profile.platform);
  return { category: def?.category ?? "other", label: def?.category_label ?? "其他" };
}

const platformFilters = computed(() => {
  const byCategory = new Map<string, { category: string; label: string; count: number }>();
  for (const profile of profiles.value) {
    const { category, label } = platformCategoryOf(profile);
    const current = byCategory.get(category);
    byCategory.set(category, { category, label, count: (current?.count ?? 0) + 1 });
  }
  return [...byCategory.values()];
});

// 空数组表示「全部」，这样新增平台时不用回头维护默认选中列表。
const allPlatformsSelected = computed(() => selectedPlatforms.value.length === 0);

const filteredProfiles = computed(() => {
  if (allPlatformsSelected.value) {
    return profiles.value;
  }
  return profiles.value.filter((profile) => selectedPlatforms.value.includes(platformCategoryOf(profile).category));
});

function selectAllPlatforms(): void {
  selectedPlatforms.value = [];
}

// 单选切换：点哪个平台就只看哪个，不保留上一个选中项。
// 再点一次当前选中的标签回到「全部」。
function togglePlatform(category: string): void {
  const only = selectedPlatforms.value.length === 1 && selectedPlatforms.value[0] === category;
  selectedPlatforms.value = only ? [] : [category];
}

const isOneBotPlatform = computed(() => {
  const id = form.value?.platform ?? "";
  const def = platforms.value.find((item) => item.id === id);
  return def ? def.protocol.startsWith("onebot") : true;
});

/** 当前平台的 ID，用于在接入区按平台切换凭据表单。 */
const currentPlatform = computed(() => form.value?.platform ?? "");

function endpointHostname(endpoint: string): string {
  const trimmed = endpoint.trim();
  if (!trimmed) return "";
  try {
    // ws://host:port/path 与 http(s) 都能被 URL 解析。
    return (new URL(trimmed).hostname || "").replace(/^\[|\]$/g, "").toLowerCase();
  } catch {
    return "";
  }
}

function isLocalOriginHost(host: string): boolean {
  return host === "localhost" || host.endsWith(".localhost") ||
    host === "0.0.0.0" || host === "::" || host === "::1" ||
    host.startsWith("127.") || host === "host.docker.internal";
}

/**
 * 正向 ws / HTTP 接入下，文件和图片靠「接入端回源 Diana」投递：后端按这里填的
 * 地址推主机名，再加 Diana 自己的 Web 端口。填回环或 docker 内网名时多半没事；
 * 填的是另一台主机（和浏览器当前访问 Diana 用的主机名也对不上）时，接入端很
 * 可能回源失败，必须在服务端显式配置 storage.local_media_base_url——这种情况
 * 要在界面上直接警告，而不是只留一行灰字 hint。纯函数，便于单测。
 */
function oneBotMediaOriginWarningText(transport: string, wsEndpoint: string, httpEndpoint: string, currentHost: string): string {
  if (transport === "reverse_ws") return "";
  const host = endpointHostname(transport === "forward_ws" ? wsEndpoint : httpEndpoint);
  if (!host) return "";
  if (isLocalOriginHost(host) || host === (currentHost || "").replace(/^\[|\]$/g, "").toLowerCase()) return "";
  return `接入端将按 ${host} 回源拉取文件/媒体（端口为 Diana 的 Web 端口）。若该主机访问不到 Diana，文件发送会失败，请在「设置 → 媒体与文件」页配置媒体回源基址。`;
}

const oneBotMediaOriginWarning = computed(() => {
  if (!isOneBotPlatform.value) return "";
  return oneBotMediaOriginWarningText(
    form.value?.onebot_transport || "reverse_ws",
    form.value?.onebot_ws_endpoint ?? "",
    form.value?.onebot_http_url ?? "",
    window.location.hostname || ""
  );
});

/**
 * 回调型平台要把这个地址填到对方后台。
 *
 * 用浏览器当前的 origin 拼：用户是从哪个地址访问控制台的，多半也就是外部能
 * 访问到的那个地址。内网访问时拼出来的是内网地址，所以旁边要提示必须公网可达。
 */
const callbackURL = computed(() => {
  const path = platformDefinition(currentPlatform.value)?.callback_path ?? "";
  if (!path) return "";
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  return origin ? `${origin}${path}` : path;
});

async function copyCallbackURL(): Promise<void> {
  if (!callbackURL.value) return;
  try {
    await navigator.clipboard.writeText(callbackURL.value);
    toastSuccess("回调地址已复制");
  } catch {
    toastError("复制失败，请手动选中地址");
  }
}

// 机器人配置项太多（41 个字段），平铺成一列要滚 6 屏。按「配一次就不动」
// 和「经常调」的区别分区，每区一屏内看完。
//
// 顺序按新建一台机器人的配置次序排：先接上通道，再指定模型——没有模型
// 后面全都跑不起来，所以它排在行为和人设之前。模板里的面板顺序与这里
// 保持一致，免得读代码时对不上。
const editorTabs = [
  { key: "access", label: "接入" },
  { key: "model", label: "模型" },
  { key: "persona", label: "人设" },
  { key: "behavior", label: "行为" },
  { key: "context", label: "上下文" },
  { key: "advanced", label: "高级" }
] as const;
type EditorTab = (typeof editorTabs)[number]["key"];
const editorTab = ref<EditorTab>("access");
const residencyPanel = ref<InstanceType<typeof AgentResidencyPanel> | null>(null);
const defaultRecallReplyAutoDeleteDelaySeconds = 60;
const maximumRecallReplyAutoDeleteDelaySeconds = 60 * 60;
// 和后端 maxRecurringFailureAlertThreshold 对齐：再大就不是「连续失败」而是订阅已经坏了。
const maximumRecurringFailureAlertThreshold = 100;
// 开关和次数共用 recurring_failure_alert_threshold 一个字段：0 就是关掉。
// 多存一个布尔会让「关着但次数是 5」这种状态存在，重新打开时该听谁的说不清。
const subscriptionFailureAlertEnabled = computed<boolean>({
  // 只有明确的 0 才算关掉。空输入框（清空次数准备重填）不能顺手把开关也关了，
  // 否则输入框当场消失，人还没打完第二个数字。
  get: () => {
    const configured = form.value?.recurring_failure_alert_threshold;
    return configured === undefined || configured === null || `${configured}`.trim() === "" || Number(configured) !== 0;
  },
  set: (enabled) => {
    if (!form.value) return;
    // 打开时清空而不是填回具体次数：留空的含义就是「按默认来」，默认值改了也跟着走。
    form.value.recurring_failure_alert_threshold = enabled ? undefined : 0;
  }
});
const platforms = ref<BotPlatform[]>([]);

// 能不能渲染 Markdown 由后端的平台注册表说了算，前端不另维护一份清单——
// 两处各写一份，新增平台时必然有一边忘记改。
function platformSupportsRichText(id: string | undefined): boolean {
  return platforms.value.find((item) => item.id === id)?.rich_text === true;
}
const richTextPlatform = computed(() => platformSupportsRichText(form.value?.platform));
const page = ref<"list" | "edit">("list");

// 表头吸顶之后（.view-header 全站生效），右侧状态卡的停靠位置要落在它下面。
// 表头会随窗口宽度换行、按钮也会随运行状态增减，高度不是常数，写死一个数
// 迟早错位——所以量出来写进 CSS 变量，让样式表去用。
const viewRoot = ref<HTMLElement | null>(null);
const viewHeader = ref<HTMLElement | null>(null);
let headerResizeObserver: ResizeObserver | null = null;

function trackHeaderHeight(): void {
  if (typeof ResizeObserver === "undefined") return;
  headerResizeObserver = new ResizeObserver(() => {
    const header = viewHeader.value;
    const root = viewRoot.value;
    if (!header || !root) return;
    root.style.setProperty("--view-header-height", `${Math.round(header.getBoundingClientRect().height)}px`);
  });
  if (viewHeader.value) headerResizeObserver.observe(viewHeader.value);
}
const platformPickerOpen = ref(false);
const creating = ref(false);

const triggerModeOptions: AppSelectOption[] = [
  { value: "smart", label: "智能（推荐）" },
  { value: "strict", label: "严格" },
  { value: "loose", label: "宽松" }
];

// 拒答话术。默认「智能」：什么时候能绕开、什么时候原因本身不能说，是看语境的
// 判断，固定档位在群里连着触发几次会很假。
const refusalStrategyOptions: AppSelectOption[] = [
  { value: "smart", label: "智能（推荐）", hint: "先试着改写，改不动再按原因性质决定说不说" },
  { value: "rewrite", label: "尽量改写", hint: "优先绕开，实在不行才模糊拒答" },
  { value: "explain", label: "说明原因", hint: "把不能答的原因直接讲清楚" },
  { value: "vague", label: "模糊拒答", hint: "一律带过，任何情况都不交代原因" }
];

const replyReferenceModeOptions: AppSelectOption[] = [
  { value: "on", label: "总是引用" },
  { value: "off", label: "从不引用" },
  { value: "auto", label: "让模型自己决定" }
];

const disclosureOptions: AppSelectOption[] = [
  { value: "owner", label: "仅主人" },
  { value: "everyone", label: "所有人" }
];

const welcomeModeOptions: AppSelectOption[] = [
  { value: "fixed", label: "固定文本" },
  { value: "template", label: "口吻模板池" },
  { value: "llm", label: "按人设实时生成" }
];

const mentionUserModeOptions: AppSelectOption[] = [
  { value: "on", label: "总是 @" },
  { value: "off", label: "从不 @" },
  { value: "auto", label: "让模型自己决定" }
];


// 人设库。存的是「它是谁、怎么说话」的配置组合，选中一套是把它们填进下面的表单，
// 并在 persona_id 里记下绑定：库里这一套更新时，后端把新内容写进绑定它的机器人
// 和群（见 model/assistant/persona_link.go）。表单里改了内容就解除绑定。
const savedPersonaLibrary = ref<Persona[]>([]);
const personaLibrary = computed(() => withBuiltinPersonas(savedPersonaLibrary.value));
const personaLibraryLoaded = ref(false);
const selectedPersonaID = computed(() => form.value ? currentPersonaSelection(form.value, personaLibrary.value) : "custom");

function choosePersona(id: string): void {
  if (!form.value) return;
  const current = selectedPersonaID.value === "custom" ? asCustomPersona(form.value) : form.value;
  form.value = selectPersona(current, personaLibrary.value.find(p => p.id === id));
}

watch(() => ({ id: form.value?.persona_id, selection: selectedPersonaID.value, ready: personaLibraryLoaded.value, settings: JSON.stringify(form.value && personaFromSettings(form.value, "")) }), (next, previous) => {
  if (!next.ready || !form.value?.persona_id) return;
  const edited = previous?.ready && previous.id === next.id && previous.settings !== next.settings;
  if (next.selection === "custom" || edited) {
    // 记下是从哪一套改出来的，好提供「写回这一套」。
    editedFromPersonaID.value = form.value.persona_id;
    form.value = asCustomPersona(form.value);
  }
});
// 从人设库某一套改出来的「自定义」：可以写回那一套，让绑定它的机器人和群一起更新。
// 内置人设不在库里，不能写回。
const editedFromPersonaID = ref("");
const editedLibraryPersona = computed(() => {
  if (!editedFromPersonaID.value || selectedPersonaID.value !== "custom") return undefined;
  const persona = savedPersonaLibrary.value.find((item) => item.id === editedFromPersonaID.value);
  return persona && !isBuiltinPersona(persona) ? persona : undefined;
});
watch(() => form.value?.id, () => {
  editedFromPersonaID.value = "";
});
const personaLibraryBusy = ref(false);

// 所有内容项全空的不值得存：存进去列表里点开也是空的，还占一格。
const personaHasContent = computed(() => {
  const current = form.value;
  if (!current) return false;
  return Boolean(
    current.system_prompt?.trim() ||
      current.self_reference?.trim() ||
      current.sentence_enders?.trim() ||
      current.action_description_enabled
      || current.daypart_tone_enabled
  );
});

function personaSummary(persona: Persona): string {
  const parts: string[] = [];
  if (persona.action_description_enabled) parts.push("动作描写");
  if (persona.daypart_tone_enabled) parts.push("时段语气");
  if (persona.self_reference) parts.push(`自称${persona.self_reference}`);
  if (persona.sentence_enders) parts.push(persona.sentence_enders.split(/[,，]/)[0].trim());
  if (!parts.length && persona.system_prompt) parts.push(persona.system_prompt.trim().slice(0, 12));
  return parts.join(" · ");
}

async function loadPersonaLibrary(): Promise<void> {
  try {
    savedPersonaLibrary.value = (await listPersonas()).personas ?? [];
    personaLibraryLoaded.value = true;
  } catch {
    // 人设库读不出来不该挡住整个机器人页：它只是个快捷方式，缺了不影响配置本身。
    savedPersonaLibrary.value = [];
  }
}

// ── 品格（soul）──────────────────────────────────────────────────────────────
// 表单直接改 form.soul：它跟着机器人配置一起保存，服务端再清洗一遍（裁长度、
// 丢空条目、全空归零），所以这里不做校验，只负责把结构摆出来。
const soul = computed<PersonaSoul>(() => {
  const current = form.value;
  if (!current) return {};
  if (!current.soul) current.soul = {};
  return current.soul;
});

const soulConfigured = computed(() => {
  const value = form.value?.soul;
  if (!value) return false;
  return Boolean(
    value.identity?.trim() ||
      value.values?.length ||
      value.hard_limits?.length ||
      value.honesty?.length ||
      value.self_nature?.trim() ||
      value.restraint?.trim() ||
      value.correctable?.trim() ||
      value.on_criticism?.trim() ||
      value.open_questions?.length ||
      value.priority?.order?.length
  );
});

// 字符串列表用「一行一条」的文本框，不做可增删的行编辑器：这几项就是短句清单，
// 给每条配一个删除按钮只会让界面比内容还重。
function linesToList(text: string): string[] {
  return text.split("\n").map(line => line.trim()).filter(Boolean);
}

function listToLines(list?: string[]): string {
  return (list ?? []).join("\n");
}

const soulHonesty = computed({
  get: () => listToLines(soul.value.honesty),
  set: (text: string) => { soul.value.honesty = linesToList(text); }
});

const soulOpenQuestions = computed({
  get: () => listToLines(soul.value.open_questions),
  set: (text: string) => { soul.value.open_questions = linesToList(text); }
});

// 优先级用顿号或逗号分隔：它是一行四五个词的东西，换行输入反而别扭。
const soulPriorityOrder = computed({
  get: () => (soul.value.priority?.order ?? []).join("、"),
  set: (text: string) => {
    const order = text.split(/[、,，]/).map(item => item.trim()).filter(Boolean);
    soul.value.priority = { ...(soul.value.priority ?? {}), order };
  }
});

const soulPriorityNote = computed({
  get: () => soul.value.priority?.note ?? "",
  set: (note: string) => { soul.value.priority = { ...(soul.value.priority ?? {}), note }; }
});

const soulOwner = computed({
  get: () => soul.value.relationships?.owner ?? "",
  set: (owner: string) => { soul.value.relationships = { ...(soul.value.relationships ?? {}), owner }; }
});

const soulMembers = computed({
  get: () => soul.value.relationships?.members ?? "",
  set: (members: string) => { soul.value.relationships = { ...(soul.value.relationships ?? {}), members }; }
});

const soulValues = computed(() => {
  if (!soul.value.values) soul.value.values = [];
  return soul.value.values;
});

const soulLimits = computed(() => {
  if (!soul.value.hard_limits) soul.value.hard_limits = [];
  return soul.value.hard_limits;
});

function addSoulValue() {
  soulValues.value.push({ value: "", why: "" });
}

function removeSoulValue(index: number) {
  soulValues.value.splice(index, 1);
}

function addSoulLimit() {
  soulLimits.value.push({ limit: "", why: "" });
}

function removeSoulLimit(index: number) {
  soulLimits.value.splice(index, 1);
}

// ── 自述 ────────────────────────────────────────────────────────────────────
// 只读加删除。写入只有机器人自己能做，人代笔想加的内容属于品格或人设正文。
const selfNotes = ref<SelfNote[]>([]);
const selfNotesBusy = ref(false);

async function reloadSelfNotes(): Promise<void> {
  if (!form.value?.self_note_enabled) {
    selfNotes.value = [];
    return;
  }
  selfNotesBusy.value = true;
  try {
    selfNotes.value = (await listSelfNotes(form.value?.id ?? "")).notes ?? [];
  } catch {
    // 自述读不出来不该挡住整个机器人页：它是旁支信息，配置本身不受影响。
    selfNotes.value = [];
  } finally {
    selfNotesBusy.value = false;
  }
}

async function removeSelfNote(id: string): Promise<void> {
  selfNotesBusy.value = true;
  try {
    selfNotes.value = (await deleteSelfNote(form.value?.id ?? "", id)).notes ?? [];
    toastSuccess("已删除这条自述");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally {
    selfNotesBusy.value = false;
  }
}

async function clearSelfNotes(): Promise<void> {
  if (!window.confirm("清空这台机器人写下的全部自述？删掉之后它要重新观察才会再记。")) return;
  selfNotesBusy.value = true;
  try {
    selfNotes.value = (await purgeSelfNotes(form.value?.id ?? "")).notes ?? [];
    toastSuccess("自述已清空");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "清空失败");
  } finally {
    selfNotesBusy.value = false;
  }
}

// selfNoteSource 说明这条是在哪、谁在场时记下的。自述跨群生效，来源是主人事后
// 判断「这句话是谁哄着它写的」的唯一线索。
function selfNoteSource(note: SelfNote): string {
  const parts: string[] = [];
  if (note.source_group_id) parts.push(`群 ${note.source_group_id}`);
  else parts.push("私聊");
  if (note.source_user_name || note.source_user_id) parts.push(note.source_user_name || note.source_user_id || "");
  if (note.created_at) parts.push(formatClock(note.created_at));
  return parts.filter(Boolean).join(" · ");
}

// 换一台机器人、或者刚打开编辑页时重新拉自述：它按机器人隔离，上一台的列表留在
// 屏幕上会让人以为这台也写过。
watch(
  () => [form.value?.id, form.value?.self_note_enabled] as const,
  () => {
    void reloadSelfNotes();
  },
  { immediate: true }
);

const personaSaverOpen = ref(false);
const personaNameDraft = ref("");
const personaNameInput = ref<HTMLInputElement | null>(null);

// 同名保存另存副本，避免覆盖已经写好的人设。
const personaSaverExisting = computed(() =>
  Boolean(personaNameDraft.value && personaLibrary.value.some((persona) => persona.name === personaNameDraft.value))
);

function togglePersonaSaver(): void {
  personaSaverOpen.value = !personaSaverOpen.value;
  if (!personaSaverOpen.value) return;
  personaNameDraft.value = (form.value?.name ?? "").trim();
  void nextTick(() => personaNameInput.value?.focus());
}

async function storeCurrentPersona(): Promise<void> {
  const current = form.value;
  const name = personaNameDraft.value.trim();
  if (!current || !personaHasContent.value || !name) return;
  const savedName = unusedPersonaName(name, personaLibrary.value);
  personaLibraryBusy.value = true;
  try {
    const response = await savePersona(personaFromSettings(current, savedName));
    savedPersonaLibrary.value = response.personas ?? [];
    if (form.value === current) form.value = selectPersona(asCustomPersona(current), response.persona);
    personaSaverOpen.value = false;
    personaNameDraft.value = "";
    toastSuccess(`已存为人设「${savedName}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "人设保存失败");
  } finally {
    personaLibraryBusy.value = false;
  }
}

async function updateEditedLibraryPersona(): Promise<void> {
  const current = form.value;
  const target = editedLibraryPersona.value;
  if (!current || !target) return;
  const ok = await askConfirm({
    title: `更新人设「${target.name}」`,
    message: "把当前的人设内容写回人设库的这一套。所有绑定它的机器人和群都会改成这份内容，并立即生效。",
    confirmLabel: "更新"
  });
  if (!ok) return;
  personaLibraryBusy.value = true;
  try {
    const response = await savePersona({ ...personaFromSettings(current, target.name), id: target.id });
    savedPersonaLibrary.value = response.personas ?? [];
    if (form.value === current) form.value = selectPersona(asCustomPersona(current), response.persona);
    editedFromPersonaID.value = "";
    const synced = [
      response.bots_synced ? `${response.bots_synced} 台机器人` : "",
      response.groups_synced ? `${response.groups_synced} 个群` : ""
    ].filter(Boolean).join("、");
    toastSuccess(synced ? `已更新「${target.name}」，同步到 ${synced}` : `已更新「${target.name}」`);
    if (response.warning) toastError(response.warning);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "人设更新失败");
  } finally {
    personaLibraryBusy.value = false;
  }
}

const personaFileInput = ref<HTMLInputElement | null>(null);

function personaFileInputClick(): void {
  personaFileInput.value?.click();
}

// 单套和整库导出的是同一种文件（personas 数组里放一个还是放几个而已），
// 所以单套文件也能直接被导入，不用为它另开一条读取分支。
// 不导 id 和 updated_at：id 是本机的，导到别处只会撞车（后端也一律重新分配）。
function personaExportPayload(personas: Persona[]): string {
  return JSON.stringify(
    {
      version: PERSONA_EXPORT_VERSION,
      exported_at: new Date().toISOString(),
      personas: personas.map((persona) => ({
        name: persona.name,
        system_prompt: persona.system_prompt ?? "",
        action_description_enabled: persona.action_description_enabled ?? false,
        daypart_tone_enabled: persona.daypart_tone_enabled,
        self_reference: persona.self_reference ?? "",
        sentence_enders: persona.sentence_enders ?? ""
      }))
    },
    null,
    2
  );
}

function downloadPersonaFile(fileName: string, content: string): void {
  const url = URL.createObjectURL(new Blob([content], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  link.click();
  URL.revokeObjectURL(url);
}

// 人设名是用户随便起的，可能带 / \ : 这类在文件名里非法或有歧义的字符。
// 中日韩字符本身没问题，所以只挑掉真正危险的那几个，不做整体转拼音。
function personaFileSlug(name: string): string {
  const slug = name.replace(/[\\/:*?"<>|\u0000-\u001f]/g, "").trim();
  return slug || "persona";
}

// 导出直接用内存里那份：它就是整库，再跑一趟接口拿不到别的东西。
function exportPersonaLibrary(): void {
  downloadPersonaFile(`diana-personas-${new Date().toISOString().slice(0, 10)}.json`, personaExportPayload(personaLibrary.value));
}

function exportPersona(persona: Persona): void {
  downloadPersonaFile(`diana-persona-${personaFileSlug(persona.name)}-${new Date().toISOString().slice(0, 10)}.json`, personaExportPayload([persona]));
}

// fileToBase64 读出文件的 base64 正文。走 dataURL 再剥前缀：对二进制 PNG 和
// UTF-8 JSON 一视同仁，不用自己分块喂 btoa。
function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result).split(",", 2)[1] ?? "");
    reader.onerror = () => reject(reader.error ?? new Error("读取文件失败"));
    reader.readAsDataURL(file);
  });
}

// looksLikeCharacterCard 判断一段 JSON 是不是 SillyTavern 角色卡：有 spec 标记、
// 或带着卡特有的字段（first_mes / mes_example / data.name）。人设文件和世界书
// 文件都没有这些。
function looksLikeCharacterCard(parsed: unknown): boolean {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return false;
  const record = parsed as Record<string, any>;
  if (typeof record.spec === "string" && record.spec.startsWith("chara_card")) return true;
  if (record.first_mes !== undefined || record.mes_example !== undefined) return true;
  return Boolean(record.data && typeof record.data === "object" && typeof record.data.name === "string");
}

async function importCharacterCardFile(file: File): Promise<void> {
  const result = await importCharacterCard(await fileToBase64(file));
  savedPersonaLibrary.value = result.personas ?? [];
  if (result.nodes?.length) {
    worldBookNodes.value = result.nodes;
  }
  const notes: string[] = [];
  if (result.persona) notes.push(`已导入角色卡「${result.persona.name}」为人设${result.renamed ? "（重名已改名）" : ""}`);
  else if (result.skipped) notes.push("这张卡的人设已经在库里，跳过");
  if (result.book_imported) notes.push(`世界书并入 ${result.book_imported} 条${result.book_name ? `（${result.book_name}）` : ""}`);
  if (result.book_dropped) notes.push(`${result.book_dropped} 条无效已忽略`);
  toastSuccess(notes.join("，") || "角色卡已处理");
}

async function importPersonaFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  // 先清掉选中的文件：不清的话连续选同一个文件不会再触发 change。
  input.value = "";
  if (!file) return;

  personaLibraryBusy.value = true;
  try {
    // PNG 一定是内嵌卡，直接走角色卡通道；JSON 先解析再看长相。
    if (file.type === "image/png" || file.name.toLowerCase().endsWith(".png")) {
      await importCharacterCardFile(file);
      return;
    }
    const text = await file.text();
    // YAML 交给后端解析：品格层写成 YAML 才读得下去（有注释、有多行字符串），
    // 而前端没有 YAML 解析器，为这一件事塞一个进去不值当。
    const lowerName = file.name.toLowerCase();
    if (lowerName.endsWith(".yaml") || lowerName.endsWith(".yml")) {
      const imported = await importPersonaSource(text);
      savedPersonaLibrary.value = imported.personas ?? [];
      toastSuccess(`导入 ${imported.imported} 套`);
      return;
    }
    const parsed = JSON.parse(text) as unknown;
    if (looksLikeCharacterCard(parsed)) {
      await importCharacterCardFile(file);
      return;
    }
    // 导出文件是 {personas: [...]}，但手写或从别处拿到的可能就是个数组，
    // 甚至是单独一套。三种都收下，没必要为格式挑剔到让人回去改文件。
    const list = Array.isArray(parsed)
      ? parsed
      : Array.isArray((parsed as { personas?: unknown }).personas)
        ? (parsed as { personas: unknown[] }).personas
        : [parsed];
    const result = await importPersonas(list as Persona[]);
    savedPersonaLibrary.value = result.personas ?? [];
    const notes = [`导入 ${result.imported} 套`];
    if (result.renamed) notes.push(`${result.renamed} 套重名已改名`);
    if (result.skipped) notes.push(`${result.skipped} 套重复已跳过`);
    if (result.dropped) notes.push(`${result.dropped} 套无效已忽略`);
    toastSuccess(notes.join("，"));
    // 认不出来的风格单独说：它不算失败，导入照常成功，但那几套的语气会退回
    // 「助手」。混在上面那串数字里说，用户不会注意到自己拼错了。
    if (result.unknown_styles?.length) {
      toastError(`旧表达风格无法识别，已保留人设正文导入：${result.unknown_styles.join("、")}`);
    }
  } catch (error) {
    toastError(error instanceof SyntaxError ? "这个文件不是有效的 JSON" : error instanceof Error ? error.message : "人设导入失败");
  } finally {
    personaLibraryBusy.value = false;
  }
}

async function removePersona(persona: Persona): Promise<void> {
  if (!(await askConfirm({ title: `删除人设「${persona.name}」？`, message: "只删库里这一份。绑定它的机器人和群保留现有人设，改为自定义。", danger: true, confirmLabel: "删除" }))) {
    return;
  }
  personaLibraryBusy.value = true;
  try {
    savedPersonaLibrary.value = (await deletePersona(persona.id)).personas ?? [];
    toastSuccess(`已删除人设「${persona.name}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "人设删除失败");
  } finally {
    personaLibraryBusy.value = false;
  }
}

// ---- 世界书（世界观设定库） ----
// 树是全局一棵，节点的增删改立即落库；world_book_enabled 才是跟着机器人配置走的。

const worldBookNodes = ref<WorldBookNode[]>([]);
const worldBookBusy = ref(false);
const worldBookEditorOpen = ref(false);
const worldBookDraft = ref<WorldBookNode>({ id: "", title: "" });
const worldBookKeywordsDraft = ref("");
const worldBookSecondaryDraft = ref("");
const worldBookDraftEnabled = ref(true);
const worldBookFileInput = ref<HTMLInputElement | null>(null);

interface WorldBookRow {
  node: WorldBookNode;
  depth: number;
  path: string[];
}

// 深度优先展开，和后端注入顺序一致；悬空父节点当根处理，后端下次保存会修正。
const worldBookRows = computed<WorldBookRow[]>(() => {
  const byParent = new Map<string, WorldBookNode[]>();
  const ids = new Set(worldBookNodes.value.map((node) => node.id));
  for (const node of worldBookNodes.value) {
    const parent = node.parent_id && ids.has(node.parent_id) ? node.parent_id : "";
    byParent.set(parent, [...(byParent.get(parent) ?? []), node]);
  }
  const rows: WorldBookRow[] = [];
  const walk = (parent: string, depth: number, path: string[]): void => {
    for (const node of byParent.get(parent) ?? []) {
      rows.push({ node, depth, path });
      walk(node.id, depth + 1, [...path, node.title]);
    }
  };
  walk("", 0, []);
  return rows;
});

// 父节点候选不能选自己和自己的后代，不然一保存就成了环。
const worldBookParentOptions = computed<AppSelectOption[]>(() => {
  const editingID = worldBookDraft.value.id;
  const excluded = new Set<string>();
  if (editingID) {
    excluded.add(editingID);
    for (const row of worldBookRows.value) {
      if (row.node.parent_id && excluded.has(row.node.parent_id)) excluded.add(row.node.id);
    }
  }
  return [
    { value: "", label: "（根）" },
    ...worldBookRows.value
      .filter((row) => !excluded.has(row.node.id))
      .map((row) => ({ value: row.node.id, label: `${"　".repeat(row.depth)}${row.node.title}` }))
  ];
});

function worldBookRowDisabled(row: WorldBookRow): boolean {
  if (row.node.enabled === false) return true;
  const byID = new Map(worldBookNodes.value.map((node) => [node.id, node]));
  let parent = row.node.parent_id;
  while (parent) {
    const node = byID.get(parent);
    if (!node) break;
    if (node.enabled === false) return true;
    parent = node.parent_id;
  }
  return false;
}

function worldBookNodeSummary(row: WorldBookRow): string {
  const parts: string[] = [];
  if (row.node.enabled === false) parts.push("已停用");
  else if (worldBookRowDisabled(row)) parts.push("随父级停用");
  if (row.node.always_on) parts.push("常驻");
  else if (row.node.keywords?.length) {
    parts.push(`触发：${row.node.keywords.join("、")}`);
    if (row.node.secondary_keywords?.length) parts.push(`且需：${row.node.secondary_keywords.join("、")}`);
  }
  else if (row.node.content?.trim()) parts.push("未设注入方式，仅作目录");
  else parts.push("目录");
  return parts.join(" · ");
}

async function loadWorldBook(): Promise<void> {
  try {
    worldBookNodes.value = (await listWorldBook()).nodes ?? [];
  } catch {
    // 世界书读不出来不该挡住机器人页：没有设定集机器人照样聊天。
    worldBookNodes.value = [];
  }
}

function openWorldBookEditor(node?: WorldBookNode): void {
  worldBookDraft.value = node
    ? { ...node }
    : { id: "", title: "", parent_id: "", content: "", always_on: false };
  worldBookKeywordsDraft.value = (node?.keywords ?? []).join(",");
  worldBookSecondaryDraft.value = (node?.secondary_keywords ?? []).join(",");
  worldBookDraftEnabled.value = node?.enabled !== false;
  worldBookEditorOpen.value = true;
}

async function storeWorldBookNode(): Promise<void> {
  const draft = worldBookDraft.value;
  if (!draft.title.trim()) return;
  worldBookBusy.value = true;
  try {
    const response = await saveWorldBookNode({
      ...(draft.id ? { id: draft.id } : {}),
      parent_id: draft.parent_id || "",
      title: draft.title,
      content: draft.content ?? "",
      keywords: worldBookKeywordsDraft.value.split(/[,，]/).map((keyword) => keyword.trim()).filter(Boolean),
      secondary_keywords: worldBookSecondaryDraft.value.split(/[,，]/).map((keyword) => keyword.trim()).filter(Boolean),
      always_on: draft.always_on ?? false,
      enabled: worldBookDraftEnabled.value
    });
    worldBookNodes.value = response.nodes ?? [];
    worldBookEditorOpen.value = false;
    toastSuccess(draft.id ? `已更新设定「${response.node.title}」` : `已添加设定「${response.node.title}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "世界书保存失败");
  } finally {
    worldBookBusy.value = false;
  }
}

async function removeWorldBookNode(node: WorldBookNode): Promise<void> {
  if (!(await askConfirm({ title: `删除设定「${node.title}」？`, message: "它的子节点会接到它的父节点上，不会一起删掉。", danger: true, confirmLabel: "删除" }))) {
    return;
  }
  worldBookBusy.value = true;
  try {
    worldBookNodes.value = (await deleteWorldBookNode(node.id)).nodes ?? [];
    toastSuccess(`已删除设定「${node.title}」`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "世界书删除失败");
  } finally {
    worldBookBusy.value = false;
  }
}

function worldBookFileInputClick(): void {
  worldBookFileInput.value?.click();
}

// 不导 id 和 updated_at 的原值给人看，但父子引用要靠 id 重建，所以原样带上；
// 后端导入时会统一换新 ID 并按文件内的对照重连。
function exportWorldBook(): void {
  const content = JSON.stringify(
    {
      version: WORLD_BOOK_EXPORT_VERSION,
      exported_at: new Date().toISOString(),
      nodes: worldBookNodes.value.map((node) => ({
        id: node.id,
        parent_id: node.parent_id ?? "",
        title: node.title,
        content: node.content ?? "",
        keywords: node.keywords ?? [],
        secondary_keywords: node.secondary_keywords ?? [],
        always_on: node.always_on ?? false,
        enabled: node.enabled !== false
      }))
    },
    null,
    2
  );
  downloadPersonaFile(`diana-world-book-${new Date().toISOString().slice(0, 10)}.json`, content);
}

async function importWorldBookFile(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;

  worldBookBusy.value = true;
  try {
    const parsed = JSON.parse(await file.text()) as unknown;
    // SillyTavern 的三种文件都认：世界书文件顶层是 entries 对象，角色卡把
    // 条目埋在 data.character_book.entries 或 character_book.entries 里。
    // 认出来就把 entries 原样交给后端转换，规则只维护一份。
    const record = (parsed ?? {}) as Record<string, any>;
    const stEntries = !Array.isArray(parsed)
      ? record.entries ?? record.data?.character_book?.entries ?? record.character_book?.entries
      : undefined;
    let result: WorldBookImportResult;
    if (stEntries && typeof stEntries === "object") {
      result = await importWorldBookSillyTavern(stEntries);
    } else {
      const list = Array.isArray(parsed)
        ? parsed
        : Array.isArray(record.nodes)
          ? (record.nodes as unknown[])
          : [parsed];
      result = await importWorldBook(list as WorldBookNode[]);
    }
    worldBookNodes.value = result.nodes ?? [];
    const notes = [`导入 ${result.imported} 条设定`];
    if (result.dropped) notes.push(`${result.dropped} 条无效已忽略`);
    toastSuccess(notes.join("，"));
  } catch (error) {
    toastError(error instanceof SyntaxError ? "这个文件不是有效的 JSON" : error instanceof Error ? error.message : "世界书导入失败");
  } finally {
    worldBookBusy.value = false;
  }
}

function setParticipation(value: ParticipationPreferences | undefined): void {
  if (!form.value || !value) return;
  form.value.response_mode = "custom";
  form.value.chat_in_level = undefined;
  form.value.chat_in_enabled = undefined;
  form.value.natural_interjection_enabled = undefined;
  form.value.chat_in_threshold = undefined;
  form.value.chat_in_chance = undefined;
  form.value.participation = value;
}

// 新群默认在群管理里改，这里只是把读到的值原样带回去：保存机器人配置不该
// 顺手把它重置成默认的「新群照常工作」。
const admissionMode = computed(() => form.value?.group_admission?.mode ?? "blacklist");

const privateAdmissionModeOptions: AppSelectOption[] = [
  { value: "all", label: "所有人（默认）", hint: "任何用户的私聊都会响应" },
  { value: "owner_only", label: "仅主人", hint: "非主人的私聊静默忽略" },
  { value: "whitelist", label: "白名单", hint: "仅主人与白名单用户的私聊响应" }
];

const privateAdmissionMode = computed(() => form.value?.private_admission?.mode ?? "all");

function setPrivateAdmissionMode(mode: "all" | "owner_only" | "whitelist"): void {
  if (!form.value) {
    return;
  }
  form.value.private_admission = { ...(form.value.private_admission ?? {}), mode };
}

// 全局门槛用 null 表示「不设门槛」，和群级的「跟随全局」是不同语义，
// 所以全局表单不给「跟随」那一档。
const globalGate = computed({
  get: () => form.value?.reply_gate ?? {},
  set: (value) => {
    if (form.value) {
      form.value.reply_gate = value;
    }
  }
});

const status = computed(() => stream.status);
const profiles = computed<BotProfileConfig[]>(() => profileSet.value?.profiles ?? []);
const copiedFrom = ref<Pick<BotProfileConfig, "id" | "name" | "platform" | "connection_profile_id"> | null>(null);
const copySourceID = ref("");
const copySourceOptions = computed<AppSelectOption[]>(() => [
  { value: "", label: "选择配置来源" },
  ...profiles.value.filter((profile) => profile.id).map((profile) => ({ value: profile.id!, label: profile.name || "未命名机器人", hint: platformName(profile.platform) }))
]);
const copiedConnectionSource = computed(() => form.value?.platform === "onebot-v11" && copiedFrom.value?.platform === "onebot-v11" ? copiedFrom.value.connection_profile_id || copiedFrom.value.id : undefined);
async function copySelectedProfile(): Promise<void> {
  const source = profiles.value.find((profile) => profile.id === copySourceID.value);
  if (source) await beginCopyProfile(source);
}
async function beginCopyProfile(source: BotProfileConfig): Promise<void> {
  if (busy.value) return;
  busy.value = true;
  try {
    const defaults = await getNewBotProfileDefaults(source.platform || "onebot-v11");
    setForm(copyBotConfiguration(source, defaults));
    copiedFrom.value = { id: source.id, name: source.name, platform: source.platform, connection_profile_id: source.connection_profile_id };
    creating.value = true;
    platformPickerOpen.value = false;
    editorTab.value = "access";
    page.value = "edit";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "复制机器人配置失败");
  } finally {
    busy.value = false;
  }
}
const connectionConflict = computed(() => form.value ? findWebSocketConnectionConflict(form.value, profiles.value) : undefined);
function reuseConflictingConnection(): void {
  const source = connectionConflict.value;
  if (!form.value || !source?.id || connectionUsers(form.value).length) return;
  form.value.connection_profile_id = source.id;
}
const reusableConnections = computed(() => profiles.value.filter((profile) => profile.id && profile.platform === "onebot-v11" && !profile.connection_profile_id));
const connectionOptions = computed<AppSelectOption[]>(() => [
  { value: "", label: "独立配置连接" },
  ...reusableConnections.value.filter((profile) => profile.id !== form.value?.id).map((profile) => ({ value: profile.id!, label: `复用 · ${profile.name || "未命名机器人"}` }))
]);
function connectionAccount(profile: BotProfileConfig): string | undefined {
  return profile.connection_profile_id ? profiles.value.find((source) => source.id === profile.connection_profile_id)?.bot_account : profile.bot_account;
}
function connectionSourceName(profile: BotProfileConfig): string {
  return profiles.value.find((source) => source.id === profile.connection_profile_id)?.name || "来源不存在";
}
function connectionUsers(profile: BotProfileConfig): BotProfileConfig[] {
  if (!profile.id) return [];
  return profiles.value.filter((item) => item.connection_profile_id === profile.id);
}
async function beginCreateShared(source: BotProfileConfig): Promise<void> {
  const platform = platforms.value.find((item) => item.id === source.platform);
  if (platform) await beginCreate(platform, source.id);
}
// 正在编辑的这台机器人自己的 NoneBot 桥接状态；桥接按机器人各自一份。
const formBridge = computed(() => {
  const id = form.value?.id;
  return id ? status.value?.nonebot_bridges?.[id] : undefined;
});
const relayManagerOpen = ref(false);
const messageRelays = computed<MessageRelayPair[]>(() => profileSet.value?.message_relays ?? []);
const relaySummary = computed(() => {
  const total = messageRelays.value.length;
  if (total === 0) return "把两个会话连起来，两边的消息互相转发。还没有配置链路。";
  const active = messageRelays.value.filter((pair) => pair.enabled).length;
  return active === total ? `已配置 ${total} 条链路，全部在转发。` : `已配置 ${total} 条链路，其中 ${active} 条在转发。`;
});
// 卡片右上角的启停只动这一台机器人的 enabled，其他机器人不受影响。
const profileEnabledToggling = ref("");
async function toggleProfileEnabled(profile: BotProfileConfig, enabled: boolean): Promise<void> {
  if (!profile.id) return;
  profileEnabledToggling.value = profile.id;
  try {
    applyConfig(await saveProfileEnabled(profile.id, enabled));
    toastSuccess(enabled ? `机器人「${profile.name || "未命名"}」已启用` : `机器人「${profile.name || "未命名"}」已停用`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "机器人启停保存失败");
  } finally {
    profileEnabledToggling.value = "";
  }
}

// 页头的批量开关：全部启用 = 把每台机器人都置为启用；全部停止 = 全部停用。
// 有一台没启用时显示「全部启用」，全部启用后才显示「全部停止」。
const allProfilesEnabled = computed(() => profiles.value.length > 0 && profiles.value.every((profile) => profile.enabled));
async function toggleAllProfiles(enabled: boolean): Promise<void> {
  busy.value = true;
  try {
    applyConfig(await saveAllProfilesEnabled(enabled));
    toastSuccess(enabled ? "全部机器人已启用" : "全部机器人已停用");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "批量启停保存失败");
  } finally {
    busy.value = false;
  }
}
// 运行时的启停是整个进程一份，不分机器人：停掉就是所有启用的机器人一起断开。
async function toggleRuntime(start: boolean): Promise<void> {
  busy.value = true;
  try {
    pushStatusSnapshot(start ? await startBot() : await stopBot());
    toastSuccess(start ? "机器人已启动" : "机器人已停止");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "操作失败");
  } finally {
    busy.value = false;
  }
}
const channelStatuses = computed<readonly BotChannelStatus[]>(() => status.value?.channels ?? (status.value?.channel ? [status.value.channel] : []));
const visibleChannels = computed(() => {
  const profileID = form.value?.id;
  if (!profileID) return channelStatuses.value;
  return channelStatuses.value.filter((channel) => channel.profile_id === profileID);
});
const failedChannels = computed(() => visibleChannels.value.filter((channel) => Boolean(channel.last_error) || channelAccountUnhealthy(channel)));
const platformOptions = computed<AppSelectOption[]>(() =>
  platforms.value.map((platform) => ({
    value: platform.id,
    label: platform.name,
    hint: platform.protocol
  }))
);

function platformDefinition(id?: string): BotPlatform | undefined {
  return platforms.value.find((platform) => platform.id === id);
}

function platformName(id?: string): string {
  return platformDefinition(id)?.name ?? id ?? "未选择平台";
}

function platformProtocol(id?: string): string {
  if (platformDefinition(id)?.protocol.startsWith("onebot-v11")) {
    const source = form.value?.connection_profile_id ? profiles.value.find((profile) => profile.id === form.value?.connection_profile_id) : form.value;
    const mode = source?.onebot_transport || "reverse_ws";
    return ({ reverse_ws: "OneBot v11 反向 WebSocket", forward_ws: "OneBot v11 正向 WebSocket", http: "OneBot v11 HTTP" } as Record<string, string>)[mode] || "OneBot v11";
  }
  return platformDefinition(id)?.protocol ?? "未识别协议";
}

function platformDescription(id?: string): string {
  return platformDefinition(id)?.description ?? "请选择已安装适配器支持的平台。";
}

function profileState(profile: BotProfileConfig): { label: string; tone: string } {
  if (!profile.enabled) return { label: "未启用", tone: "idle" };
  const channel = channelStatuses.value.find((item) => item.profile_id === profile.id);
  if (channel && channelAccountUnhealthy(channel)) return { label: channelStatusLabel(channel), tone: "error" };
  if (channel?.connected) return { label: "已连接", tone: "online" };
  if (channel?.last_error) return { label: "连接失败", tone: "error" };
  if (status.value?.running) return { label: "连接中", tone: "pending" };
  return { label: "已停止", tone: "idle" };
}

function onMessageRelaysSaved(config: BotProfileConfig): void {
  applyConfig(config);
  relayManagerOpen.value = false;
  toastSuccess("消息互通链路已保存");
}

// —— 模型分配 ——
// 细分用途：不配就跟着「意图识别」那一档走。摊出来是因为这些调用的性质差得很远——
// 主动接话判定和发送前审核都能改成判断题（可以绑 TypeSafe Jev 这类只做判断的模型），
// 而记忆抽取、上下文压缩要的是文本输出，绑上去只会每次先失败一次再降级。
const purposeRoleKeys = ["background"] as const;

type RoleKey = "chat" | "vision" | "intent" | "image" | "media_parse" | (typeof purposeRoleKeys)[number];
type RoleRoute = { profile_id?: string; group?: string; model: string; provider_id?: string; model_id?: string; follow_chat?: boolean };
type RoleAssignment = RoleRoute & { fallbacks?: RoleRoute[] };
const modelRoleRows: { key: RoleKey; label: string; description: string }[] = [
  {
    key: "chat",
    label: "对话",
    description: "正式回复用的模型。其他用途在提供商一栏选「跟随对话」时，直接沿用这里的提供商、模型和后备路由，模型一栏随之锁定。"
  },
  {
    key: "vision",
    label: "视觉理解",
    description: "聊天中直接看图回答时使用。选「跟随对话」时，对话模型本身要能识图。"
  },
  {
    key: "media_parse",
    label: "媒体解析（可选）",
    description:
      "后台批量识图：历史图片和视频每一帧的描述、表情包语义简介，以及图片识别插件的看图与模型 OCR。这些调用量大、在后台排队逐张执行，" +
      "建议单独指一个便宜、快、识图稳定的视觉模型并配上后备。跟随视觉理解时，更换对话模型会连带换掉它，换成慢模型会让识图队列积压；" +
      "单独指定后只使用这一档及其后备，不再回落到视觉理解。"
  },
  {
    key: "intent",
    label: "意图识别",
    description:
      "判定当前这一轮该不该说话、说出去的这句能不能发：主动接话判定、接话质量和发送前审核。问的都是是非、单选和打分，" +
      "发请求时带着判断题表，所以这一档可以绑 TypeSafe Jev 这类只做判断的模型——更快更便宜。写字的活在「后台生成」那一档。"
  },
  {
    key: "image",
    label: "图片生成",
    description: "生成和编辑图片。选「跟随对话」时，对话模型本身必须支持出图。"
  }
];
// 后台生成是从「意图识别」里拆出来的一档。留空就跟着意图识别，行为和拆之前一样。
const purposeRoleRows: { key: RoleKey; label: string; description: string }[] = [
  {
    key: "background",
    label: "后台生成（好感度 / 长期记忆）",
    description:
      "好感度评估、长期记忆抽取与归纳、上下文摘要、语义指代、转发内容安全，以及各种提示改写。" +
      "它们都要写出成段文字，判断模型答不了；也不在回复的关键路径上，慢一点没关系。不指定时跟随对话。"
  }
];

// 细分用途默认收起：绝大多数部署只需要「意图识别」一档，13 行铺开会把这一页淹掉。
const purposeRolesOpen = ref(false);
// 收起时仍然显示已经配过的那几行，否则配完一收就找不到在哪改了。
const visibleModelRoleRows = computed(() =>
  purposeRolesOpen.value
    ? [...modelRoleRows, ...purposeRoleRows]
    : [...modelRoleRows, ...purposeRoleRows.filter((row) => roleForm.value[row.key])]
);

const llmChannels = ref<LLMConfig[]>([]);
const roleForm = ref<Partial<Record<RoleKey, RoleAssignment>>>({});

// 模型分配不止这一页能改：主人在聊天里让机器人换模型，写的是同一份机器人配置。
// savedRoleSnapshot 记着草稿出发时服务端那一版，用来分辨「这一档没动过」和
// 「两边同时在改」——前者直接跟上新值，后者只提示，不替主人决定保留哪一份。
const savedRoleSnapshot = ref("");
const modelRolesChangedElsewhere = ref(false);
const incomingModelRoles = ref<BotProfileConfig["model_roles"]>();

// roleSnapshot 按固定字段顺序拍平，保证服务端回来的那份和页面草稿能直接比。
function roleSnapshot(roles: Record<string, RoleAssignment | undefined> | undefined): string {
  const route = (item: RoleRoute): unknown[] => [item.profile_id ?? "", item.group ?? "", item.model ?? "", item.provider_id ?? "", item.model_id ?? "", item.follow_chat === true];
  return JSON.stringify(
    Object.keys(roles ?? {})
      .sort()
      .map((key) => {
        const role = roles?.[key];
        return role ? [key, route(role), (role.fallbacks ?? []).map(route)] : [key];
      })
  );
}

// orderedRoleKeys 按「模型分配」那几行的排法给用途排序，不跟数据来源走。
// 服务端的 model_roles 是个 map，序列化出来按字母排；草稿又可能在编辑途中被
// 别处的改动整份换掉，或者因为后加了一档而把新键追加在末尾。键序跟着这些走，
// 保存出去的配置就会莫名其妙换个样子，配置对比和导出全是噪音。认不出的键按
// 原样排在后面，别把以后新增的用途悄悄丢掉。
function orderedRoleKeys(roles: Partial<Record<string, unknown>>): string[] {
  const known = modelRoleRows.map((row) => row.key).filter((key) => key in roles);
  return [...known, ...Object.keys(roles).filter((key) => !known.includes(key as RoleKey))];
}

function setRoleForm(source: BotProfileConfig["model_roles"]): void {
  const incoming = source ?? {};
  const roles: typeof roleForm.value = {};
  for (const key of orderedRoleKeys(incoming)) {
    const role = incoming[key];
    roles[key as RoleKey] = {
      profile_id: role.profile_id,
      group: role.group,
      model: role.model,
      provider_id: role.provider_id,
      model_id: role.model_id,
      follow_chat: role.follow_chat,
      fallbacks: role.fallbacks?.map((fallback) => ({ ...fallback }))
    };
  }
  roleForm.value = roles;
  savedRoleSnapshot.value = roleSnapshot(roles);
  modelRolesChangedElsewhere.value = false;
}

// 生成人设时用哪个提供商和模型。undefined 表示跟随对话那一档，和「模型分配」里的
// 「跟随对话」是同一个意思，也是原来唯一的行为——想换一个更会写文案的模型来起草人设
// 以前做不到，只能先把对话那一档改掉、生成完再改回去。
const personaRoute = ref<RoleRoute | undefined>(undefined);

const personaChannelOptions = computed<AppSelectOption[]>(() => [
  { value: FOLLOW_CHAT, label: "跟随对话", hint: "用对话那一档的提供商和模型来写" },
  ...channelOptionsFor("chat")
]);

const personaChannelValue = computed(() => (personaRoute.value ? routeSelectionValue(personaRoute.value) : FOLLOW_CHAT));

const personaModelOptions = computed<AppSelectOption[]>(() =>
  personaRoute.value ? modelOptionsFor("chat", personaRoute.value) : []
);

const personaModelValue = computed(() => personaRoute.value?.model ?? "");

function setPersonaChannel(value: string): void {
  if (!value) return;
  if (value === FOLLOW_CHAT) {
    personaRoute.value = undefined;
    return;
  }
  const model = personaRoute.value?.model ?? "";
  personaRoute.value = value.startsWith(GROUP_PREFIX)
    ? { group: value.slice(GROUP_PREFIX.length), model }
    : { profile_id: value, model };
  // 换了提供商之后原来那个模型多半不在这一家里，就近挑一个能用的，别留着一个报错的组合。
  const options = personaModelOptions.value.filter((option) => option.value);
  if (!options.some((option) => option.value === model)) {
    setPersonaModel(options[0]?.value ?? "");
  }
}

function setPersonaModel(value: string): void {
  if (!personaRoute.value) return;
  if (value.includes(MODEL_PAIR_SEP)) {
    const [profileID, model] = value.split(MODEL_PAIR_SEP);
    personaRoute.value = { profile_id: profileID, model };
    return;
  }
  personaRoute.value.model = value;
}

// 下拉里分组选项用 group: 前缀编码，与单渠道的 profile id 区分。
const GROUP_PREFIX = "group:";

// 「跟随对话」是提供商一栏的一个特殊值：选中后这一档不自己绑提供商和模型，运行时
// 直接用对话那一档的绑定（含后备路由）。它以前藏在视觉理解的模型下拉里，只有那一
// 个用途有；实际上每个用途都需要——不然「我就是要跟着对话走」这件事在界面上没法表达，
// 只能靠「什么都不填」隐式回落，改了对话之后也看不出哪些用途跟着变了。
const FOLLOW_CHAT = "__follow_chat__";
const FOLLOW_VISION = "__follow_vision__";

function llmProviderLabel(provider: LLMConfig["provider"]): string {
  const labels: Record<LLMConfig["provider"], string> = {
    openai_compatible: "OpenAI 兼容",
    gemini: "Gemini",
    anthropic: "Anthropic",
    typesafe: "TypeSafe 判断模型"
  };
  return labels[provider];
}

type ModelCompatibility = "compatible" | "unknown" | "incompatible";

function normalizedModalities(values?: string[]): string[] {
  return [...new Set((values ?? []).map((value) => value.trim().toLowerCase()).filter(Boolean))];
}

function mergeModelInfo(preferred: LLMModelInfo, fallback?: LLMModelInfo): LLMModelInfo {
  return {
    ...fallback,
    ...preferred,
    input_modalities: normalizedModalities([
      ...(preferred.input_modalities ?? []),
      ...(fallback?.input_modalities ?? [])
    ]),
    output_modalities: normalizedModalities([
      ...(preferred.output_modalities ?? []),
      ...(fallback?.output_modalities ?? [])
    ])
  };
}

function profileModels(profile: LLMConfig): LLMModelInfo[] {
  const models = new Map<string, LLMModelInfo>();
  for (const model of profile.models ?? []) {
    if (!model.id) continue;
    models.set(model.id, mergeModelInfo(models.get(model.id) ?? model, model));
  }
  if (profile.model && !models.has(profile.model)) {
    models.set(profile.model, { id: profile.model });
  }

  // image_model 是 Provider 配置明确声明的生图模型；即使 /models 没返回它，
  // 也应出现在图片生成用途里。已有明确输出能力时尊重目录结果。
  const declaredImageModels = [profile.image_model, profile.group === "image" ? profile.model : undefined];
  for (const id of declaredImageModels) {
    if (!id) continue;
    const current = models.get(id);
    if (!current) {
      models.set(id, { id, output_modalities: ["image"] });
    } else if (normalizedModalities(current.output_modalities).length === 0) {
      models.set(id, { ...current, output_modalities: ["image"] });
    }
  }
  return [...models.values()];
}

function modelCompatibility(model: LLMModelInfo, role: RoleKey): ModelCompatibility {
  const input = new Set(normalizedModalities(model.input_modalities));
  const output = new Set(normalizedModalities(model.output_modalities));
  const inputKnown = input.size > 0;
  const outputKnown = output.size > 0;

  if (role === "image") {
    return !outputKnown ? "unknown" : output.has("image") ? "compatible" : "incompatible";
  }
  if (role === "chat" || role === "intent") {
    return !outputKnown ? "unknown" : output.has("text") ? "compatible" : "incompatible";
  }
  if ((inputKnown && !input.has("image")) || (outputKnown && !output.has("text"))) {
    return "incompatible";
  }
  return input.has("image") && output.has("text") ? "compatible" : "unknown";
}

function compatibilityRank(value: ModelCompatibility): number {
  if (value === "compatible") return 0;
  if (value === "unknown") return 1;
  return 2;
}

function modelCapabilityLabel(model: LLMModelInfo): string {
  const input = new Set(normalizedModalities(model.input_modalities));
  const output = new Set(normalizedModalities(model.output_modalities));
  const labels: string[] = [];
  if (output.has("text")) {
    labels.push(input.has("image") ? "文字 / 视觉理解" : "文字");
  }
  if (output.has("image")) {
    labels.push(input.has("image") ? "图片生成 / 编辑" : "图片生成");
  }
  return labels.join(" · ") || "能力待验证";
}

function modelHint(model: LLMModelInfo, compatibility: ModelCompatibility, prefix?: string): string {
  const capability = compatibility === "incompatible" ? "当前模型能力不匹配" : modelCapabilityLabel(model);
  return [prefix, capability].filter(Boolean).join(" · ");
}

function modelsForRole(profile: LLMConfig, role: RoleKey, current: RoleRoute | undefined = roleForm.value[role]): { model: LLMModelInfo; compatibility: ModelCompatibility }[] {
  const profileIsSelected = current?.group
    ? (profile.group?.trim() || "default") === current.group
    : Boolean(current?.profile_id && profile.id === current.profile_id);
  return profileModels(profile)
    .map((model) => ({ model, compatibility: modelCompatibility(model, role) }))
    .filter(
      ({ model, compatibility }) =>
        compatibility !== "incompatible" || (profileIsSelected && model.id === current?.model)
    )
    .sort((a, b) => compatibilityRank(a.compatibility) - compatibilityRank(b.compatibility));
}

function channelGroups(): { name: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const channel of llmChannels.value) {
    const group = channel.group?.trim() || "default";
    counts.set(group, (counts.get(group) ?? 0) + 1);
  }
  return [...counts.entries()]
    .filter(([, count]) => count > 1)
    .map(([name, count]) => ({ name, count }));
}

function channelOptionsFor(role: RoleKey): AppSelectOption[] {
  const base: AppSelectOption[] = [];
  if (role === "media_parse") base.push({ value: FOLLOW_VISION, label: "跟随视觉理解", hint: "不单独绑定媒体解析模型" });
  // 对话是被跟随的那一档，不能跟随自己。
  if (role !== "chat") {
    base.push({
      value: FOLLOW_CHAT,
      label: "跟随对话",
      hint:
        role === "image"
          ? "沿用对话的提供商与模型；对话模型本身要支持出图"
          : "沿用对话选定的提供商、模型和后备路由"
    });
  }
  for (const group of channelGroups()) {
    base.push({
      value: GROUP_PREFIX + group.name,
      label: `${group.name === "default" ? "默认分组" : group.name}（提供商分组）`,
      hint: `${group.count} 个提供商按顺序降级`
    });
  }
  for (const channel of llmChannels.value) {
    const selectableModels = modelsForRole(channel, role);
    base.push({
      value: channel.id ?? "",
      label: channel.name || llmProviderLabel(channel.provider),
      hint: `${llmProviderLabel(channel.provider)} · ${selectableModels.length} 个匹配模型`
    });
  }
  return base;
}

function selectedRoleProfiles(role: RoleKey, selection: RoleRoute | undefined = roleForm.value[role]): LLMConfig[] {
  if (!selection) return [];
  if (selection.group) {
    return llmChannels.value.filter((channel) => (channel.group?.trim() || "default") === selection.group);
  }
  return llmChannels.value.filter((channel) => channel.id === selection.profile_id);
}

// 未指定 Provider 时，模型下拉直接聚合所有 Provider 的模型，选中即自动
// 带出对应 Provider——不必先在左边选一次再回来选模型。
const MODEL_PAIR_SEP = "::";

function crossProviderModelOptions(role: RoleKey): AppSelectOption[] {
  const options: AppSelectOption[] = [];
  const candidates: { option: AppSelectOption; compatibility: ModelCompatibility }[] = [];
  for (const channel of llmChannels.value) {
    const channelName = channel.name || llmProviderLabel(channel.provider);
    for (const { model, compatibility } of modelsForRole(channel, role)) {
      candidates.push({
        compatibility,
        option: {
          value: `${channel.id ?? ""}${MODEL_PAIR_SEP}${model.id}`,
          label: model.name && model.name !== model.id ? `${model.name} (${model.id})` : model.id,
          hint: modelHint(model, compatibility, channelName)
        }
      });
    }
  }
  candidates.sort((a, b) => compatibilityRank(a.compatibility) - compatibilityRank(b.compatibility));
  options.push(...candidates.map(({ option }) => option));
  return options;
}

function modelOptionsFor(role: RoleKey, selection: RoleRoute | undefined = roleForm.value[role]): AppSelectOption[] {
  // 跟随对话时模型由对话那一档决定，这里没有可选项。
  if (selection?.follow_chat) return [];
  const profiles = selectedRoleProfiles(role, selection);
  if (profiles.length === 0) {
    return crossProviderModelOptions(role);
  }
  const models = new Map<string, { model: LLMModelInfo; compatibility: ModelCompatibility }>();
  for (const profile of profiles) {
    const seen = new Set<string>();
    for (const { model, compatibility } of modelsForRole(profile, role, selection)) {
      if (seen.has(model.id)) continue;
      seen.add(model.id);
      const current = models.get(model.id);
      models.set(model.id, {
        model: current ? mergeModelInfo(current.model, model) : model,
        compatibility:
          current && compatibilityRank(current.compatibility) < compatibilityRank(compatibility)
            ? current.compatibility
            : compatibility
      });
    }
  }
  const options: AppSelectOption[] = [];
	const candidates = [...models.values()].sort(
    (a, b) => compatibilityRank(a.compatibility) - compatibilityRank(b.compatibility)
  );
  for (const { model, compatibility } of candidates) {
    const providers = profiles
      .filter((profile) => profileCanRouteRoleModel(profile, role, model.id))
      .map((profile) => profile.name || llmProviderLabel(profile.provider));
    if (providers.length === 0) continue;
    options.push({
      value: model.id,
      label: model.name && model.name !== model.id ? `${model.name} (${model.id})` : model.id,
      hint: modelHint(
        model,
        compatibility,
        profiles.length > 1
          ? `${providers.length}/${profiles.length} 个提供商将参与路由：${providers.join("、")}`
          : (model.owned_by || undefined)
      )
    });
  }
  return options;
}

function mergeModelLists(preferred: LLMModelInfo[], fallback: LLMModelInfo[]): LLMModelInfo[] {
  const models = new Map<string, LLMModelInfo>();
  for (const model of fallback) {
    if (model.id) models.set(model.id, model);
  }
  for (const model of preferred) {
    if (model.id) models.set(model.id, mergeModelInfo(model, models.get(model.id)));
  }
  return [...models.values()];
}

async function refreshLLMChannelCapabilities(channels: LLMConfig[]): Promise<void> {
  const refreshed = await Promise.all(
    channels.map(async (channel) => {
      if (channel.provider === "openai_compatible" && !channel.api_key_configured && !channel.api_key) {
        return channel;
      }
      try {
        const result = await listLLMModels(channel);
        return { ...channel, models: mergeModelLists(result.models, channel.models ?? []) };
      } catch {
        return channel;
      }
    })
  );
  llmChannels.value = refreshed;
}

function roleModelValue(role: RoleKey): string {
  // 跟随对话时模型一栏是锁定的，留空让 placeholder 说明它跟着谁走。
  if (roleForm.value[role]?.follow_chat) return "";
  return roleForm.value[role]?.model ?? "";
}

function roleSelectionValue(role: RoleKey): string {
  if (role === "media_parse" && !roleForm.value[role]) return FOLLOW_VISION;
  return routeSelectionValue(roleForm.value[role]);
}

function routeSelectionValue(route?: RoleRoute): string {
  if (!route) return "";
  if (route.follow_chat) return FOLLOW_CHAT;
  return route.group ? GROUP_PREFIX + route.group : (route.profile_id ?? "");
}

function setRoleChannel(role: RoleKey, value: string): void {
  if (role === "media_parse" && value === FOLLOW_VISION) {
    delete roleForm.value[role];
    return;
  }
  if (!value) {
    return;
  }
  if (value === FOLLOW_CHAT) {
    // 跟随对话不带自己的模型和后备：这两样都从对话那一档现取，留着只会在界面上
    // 显示一份早就不生效的旧绑定。
    roleForm.value[role] = { model: "", follow_chat: true };
    return;
  }
  const current = roleForm.value[role];
  const model = current?.follow_chat ? "" : (current?.model ?? "");
  const fallbacks = current?.follow_chat ? undefined : current?.fallbacks;
  if (value.startsWith(GROUP_PREFIX)) {
    roleForm.value[role] = { group: value.slice(GROUP_PREFIX.length), model, fallbacks };
  } else {
    roleForm.value[role] = { profile_id: value, model, fallbacks };
  }
  const options = modelOptionsFor(role).filter((option) => option.value !== "");
  if (!roleModelIsSelectable(role, model)) {
    roleForm.value[role]!.model = options.find((option) => roleModelIsSelectable(role, option.value))?.value ?? "";
  }
}

function addRoleFallback(role: RoleKey): void {
  const assignment = roleForm.value[role];
  if (!assignment) return;
  assignment.fallbacks ??= [];
  assignment.fallbacks.push({ model: "" });
}

function removeRoleFallback(role: RoleKey, index: number): void {
  roleForm.value[role]?.fallbacks?.splice(index, 1);
}

// 主路由和后备路由是同一张有序列表：下标 0 是主路由，i 是后备 i。
// 拖动或方向键调整顺序，排到最上面的那条成为主路由。跟随对话时没有自己的路由可排。
const routeDrag = ref<{ role: RoleKey; from: number; over: number | null } | null>(null);

function routeReorderable(role: RoleKey): boolean {
  const assignment = roleForm.value[role];
  return !!assignment && !assignment.follow_chat && (assignment.fallbacks?.length ?? 0) > 0;
}

function moveRoleRoute(role: RoleKey, from: number, to: number): void {
  const assignment = roleForm.value[role];
  if (!assignment || assignment.follow_chat) return;
  const { fallbacks = [], ...primary } = assignment;
  const routes: RoleRoute[] = [primary, ...fallbacks];
  if (from === to || from < 0 || to < 0 || from >= routes.length || to >= routes.length) return;
  const [moved] = routes.splice(from, 1);
  routes.splice(to, 0, moved);
  const [first, ...rest] = routes;
  roleForm.value[role] = { ...first, fallbacks: rest };
}

function routeDragClasses(role: RoleKey, index: number): Record<string, boolean> {
  const drag = routeDrag.value;
  const active = drag?.role === role;
  return {
    "is-dragging": active && drag.from === index,
    "is-drop-target": active && drag.over === index && drag.from !== index
  };
}

function onRouteDragStart(role: RoleKey, index: number, event: DragEvent): void {
  routeDrag.value = { role, from: index, over: null };
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", `${role}:${index}`);
  }
}

function onRouteDragOver(role: RoleKey, index: number, event: DragEvent): void {
  const drag = routeDrag.value;
  if (!drag || drag.role !== role) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  drag.over = index;
}

function onRouteDrop(role: RoleKey, index: number, event: DragEvent): void {
  const drag = routeDrag.value;
  if (!drag || drag.role !== role) return;
  event.preventDefault();
  moveRoleRoute(role, drag.from, index);
  routeDrag.value = null;
}

function onRouteDragEnd(): void {
  routeDrag.value = null;
}

async function onRouteHandleKeydown(role: RoleKey, index: number, event: KeyboardEvent): Promise<void> {
  const delta = event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : 0;
  const count = 1 + (roleForm.value[role]?.fallbacks?.length ?? 0);
  if (!delta || index + delta < 0 || index + delta >= count) return;
  event.preventDefault();
  moveRoleRoute(role, index, index + delta);
  await nextTick();
  document.querySelector<HTMLElement>(`[data-route-handle="${role}-${index + delta}"]`)?.focus();
}

function setFallbackChannel(role: RoleKey, index: number, value: string): void {
  const route = roleForm.value[role]?.fallbacks?.[index];
  if (!route) return;
  delete route.profile_id;
  delete route.group;
  delete route.provider_id;
  delete route.model_id;
  if (value.startsWith(GROUP_PREFIX)) route.group = value.slice(GROUP_PREFIX.length);
  else route.profile_id = value;
  const options = modelOptionsFor(role, route).filter((option) => option.value !== "");
  if (!selectedRoleProfiles(role, route).some((profile) => profileCanRouteRoleModel(profile, role, route.model))) {
    route.model = options[0]?.value ?? "";
  }
}

function setFallbackModel(role: RoleKey, index: number, value: string): void {
  const route = roleForm.value[role]?.fallbacks?.[index];
  if (!route) return;
  if (value.includes(MODEL_PAIR_SEP)) {
    const [profileID, model] = value.split(MODEL_PAIR_SEP);
    route.profile_id = profileID;
    delete route.group;
    route.model = model;
    return;
  }
  route.model = value;
}

function roleModelIsSelectable(role: RoleKey, modelID: string): boolean {
  if (!modelID) return false;
  return selectedRoleProfiles(role).some((profile) => profileCanRouteRoleModel(profile, role, modelID));
}

function profileCanRouteRoleModel(profile: LLMConfig, role: RoleKey, modelID: string): boolean {
  const catalog = profile.models ?? [];
  // 没有同步到模型目录的 Provider 能力未知，运行时仍可尝试。
  if (catalog.length === 0) return true;
  return catalog.some(
    (model) => model.id === modelID && modelCompatibility(model, role) !== "incompatible"
  );
}

function setRoleModel(role: RoleKey, value: string): void {
  if (value.includes(MODEL_PAIR_SEP)) {
    // 跨 Provider 选择：一次确定 Provider 和模型。
    const [profileID, model] = value.split(MODEL_PAIR_SEP);
    roleForm.value[role] = { profile_id: profileID, model, fallbacks: roleForm.value[role]?.fallbacks };
    return;
  }
  if (!value) {
    return;
  }
  const current = roleForm.value[role];
  if (current) {
	delete current.follow_chat;
    current.model = value;
  }
}

function setForm(config: BotProfileConfig): void {
  form.value = {
    ...config,
    persona_id: config.persona_id ?? "",
    custom_persona: config.custom_persona ?? asCustomPersona(config).custom_persona,
    participation: participationFromConfig(config),
    profiles: undefined,
    // 可选布尔字段先归一化成具体值供开关绑定；少数安全行为默认关闭。
    owner_llm_config_enabled: config.owner_llm_config_enabled ?? true,
    bot_reply_loop_detection_enabled: config.bot_reply_loop_detection_enabled ?? true,
    reply_account_safety_audit_master_enabled: config.reply_account_safety_audit_master_enabled ?? true,
    natural_reply_split_enabled: config.natural_reply_split_enabled ?? true,
    reply_preserve_line_breaks: config.reply_preserve_line_breaks ?? true,
    reply_line_split_enabled: config.reply_line_split_enabled ?? false,
    typing_delay_enabled: config.typing_delay_enabled ?? false,
    social_reply_enabled: config.social_reply_enabled ?? false,
    notebook_shared_scope_enabled: config.notebook_shared_scope_enabled ?? true,
    telegram_suppress_bot_messages: config.telegram_suppress_bot_messages ?? true,
    qq_typing_enabled: config.qq_typing_enabled ?? true,
    // 后端归一化后总会回填 mode；旧配置没有该字段时按布尔开关折算。
    // 沙盒模式后端会归一化后回填；旧配置没有这个字段时按 auto 展示。
    agent_command_sandbox: config.agent_command_sandbox ?? "auto",
    agent_command_sandbox_allow_network: config.agent_command_sandbox_allow_network ?? false,
    agent_file_write_enabled: config.agent_file_write_enabled ?? false,
    agent_browser_control_enabled: config.agent_browser_control_enabled ?? false,
    agent_browser_box_disabled: config.agent_browser_box_disabled ?? false,
    reply_reference_mode: config.reply_reference_mode ?? "auto",
    model_disclosure: config.model_disclosure ?? "owner",
    repository_disclosure: config.repository_disclosure ?? "owner",
    mention_user_mode: config.mention_user_mode ?? "auto",
    markdown_to_plain: config.markdown_to_plain ?? !platformSupportsRichText(config.platform),
    error_notify_enabled: config.error_notify_enabled ?? true,
    muted_reply_pause_enabled: config.muted_reply_pause_enabled ?? true,
    muted_voice_transcription_enabled: config.muted_voice_transcription_enabled ?? true,
    muted_image_description_enabled: config.muted_image_description_enabled ?? true,
    muted_reply_judgment_enabled: config.muted_reply_judgment_enabled ?? false,
    recall_reply_auto_delete_enabled: config.recall_reply_auto_delete_enabled ?? false,
    recall_reply_auto_delete_delay_seconds: config.recall_reply_auto_delete_delay_seconds ?? defaultRecallReplyAutoDeleteDelaySeconds,
    long_term_memory_enabled: config.long_term_memory_enabled ?? true,
    debug_mode_enabled: config.debug_mode_enabled ?? false,
    cross_group_memory_enabled: config.cross_group_memory_enabled ?? false,
    cross_platform_memory_enabled: config.cross_platform_memory_enabled ?? false,
    world_book_enabled: config.world_book_enabled ?? true,
    self_note_enabled: config.self_note_enabled ?? false,
    romance_enabled: config.romance_enabled ?? false,
    mood_enabled: config.mood_enabled ?? false,
    poke_reply_enabled: config.poke_reply_enabled ?? false,
    expression_learning_enabled: config.expression_learning_enabled ?? false,
    dict_segment_enabled: config.dict_segment_enabled ?? false,
    semantic_search_enabled: config.semantic_search_enabled ?? false,
    natural_interjection_enabled: undefined,
    chat_in_level: undefined,
    chat_in_enabled: undefined,
    chat_in_threshold: undefined,
    chat_in_chance: undefined,
    response_mode: "custom",
    auto_image_description: config.auto_image_description ?? true,
    auto_video_preprocess: config.auto_video_preprocess ?? true,
    action_description_enabled: config.action_description_enabled ?? false,
    daypart_tone_enabled: config.daypart_tone_enabled ?? false,
    llm_streaming_enabled: config.llm_streaming_enabled ?? true,
    self_reference: config.self_reference ?? "",
    sentence_enders: config.sentence_enders ?? "",
    group_trigger_mode: config.group_trigger_mode ?? "smart",
    refusal_strategy: config.refusal_strategy ?? "smart",
    prompt_inject_time: config.prompt_inject_time ?? true,
    prompt_inject_plaintext_rules: config.prompt_inject_plaintext_rules ?? true,
    prompt_inject_group_sender: config.prompt_inject_group_sender ?? true,
    prompt_chinese_slang_hint: config.prompt_chinese_slang_hint ?? true
  };
  triggersDraft.value = (config.group_triggers ?? []).join(",");
  welcomeTemplatesDraft.value = (config.welcome_templates ?? []).join("\n");
  allowlistDraft.value = (config.agent_command_allowlist ?? []).join(",");
  privateAllowedUsers.value = [...(config.private_admission?.allowed_users ?? [])];
  for (const draft of Object.values(tokenDrafts)) {
    draft.value = "";
  }
  // 换一个配置档就得重新索取，别把上一档的明文状态带过来。
  tokenRevealed.value = emptyRevealState();
  setRoleForm(config.model_roles);
}

function applyConfig(config: BotProfileConfig): void {
  profileSet.value = config;
  setForm(config);
}

function splitList(raw: string): string[] {
  return raw
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter((item) => item !== "");
}

// 「恢复内置提示词」要恢复的是后端那套默认值，所以除了人设，这里一律留空：
// 保存时 BotConfig.WithDefaults 会把空字符串补成它自己的默认文案，前端不再抄一份。
// 抄过的那几份都烂掉过——排版规则停在一版没有「正文不要输出真实换行符」的旧文案，
// 主动回复提示词停在后端专门写了迁移去替换掉的 legacySingleMessageProactiveReplyPrompt，
// 点一次「恢复」等于把旧文案按回配置里。这几个字段本来也没有输入框，留空不会让人看见空白。
//
// 人设是例外：它有输入框，恢复后要当场显示出来给人看，所以前端留了一份逐字节副本
// （builtin-personas.ts 里的 defaultSystemPrompt），由测试盯着它和 Go 常量一致。
const promptDefaults = {
  system_prompt: defaultSystemPrompt,
  prompt_chinese_slang_text: "",
  prompt_plaintext_rules_text: "",
  prompt_time_template: "",
  prompt_group_sender_template: "",
  prompt_image_only_text: "",
  prompt_wake_only_text: "",
  proactive_reply_router_prompt: "",
  proactive_reply_prompt: ""
};

function openPersonaComposer(): void {
  personaComposerOpen.value = true;
  void nextTick(() => personaDraftInput.value?.focus());
}

function closePersonaComposer(): void {
  // 生成中不让关：请求还在路上，关掉之后结果会填进一个用户以为已经放弃的表单。
  if (personaBusy.value) return;
  personaComposerOpen.value = false;
}

async function runPersonaGenerate(): Promise<void> {
  const description = personaDraft.value.trim();
  if (!form.value || !description || personaBusy.value) return;
  personaBusy.value = true;
  try {
    const current = form.value.system_prompt?.trim() || "";
    // 没单独指定就跟随对话那一档，和界面上「跟随对话」那个选项对应。
    const route = personaRoute.value ?? roleForm.value.chat;
    // 传群内触发名，不是 form.name——后者是控制台用来区分多个机器人的标签
    // （「主群助手」「客服机器人」），拿它当角色名，生成出来的人设会自称「主群助手」。
    // 触发名正在编辑时以输入框里的为准，还没填过就退回控制台标签。
    const inChatName = splitList(triggersDraft.value)[0] || form.value.group_triggers?.[0]?.trim() || form.value.name;
    const result = await generatePersona(description, inChatName, current, {
      response_mode: form.value.response_mode,
      profile_id: route?.profile_id || route?.provider_id,
      group: route?.group,
      model: route?.model_id || route?.model,
      // 只有正文没被人工改过时才把卡带回去：改过的话卡和正文已经对不上，
      // 拿旧卡当基准会把用户手打的那几句悄悄改回去。
      card: personaCardMatchesPrompt(current) ? personaCard.value : null
    });
    const persona = result.persona?.trim();
    if (!persona) {
      toastError("模型没有返回可用的人设");
      return;
    }
    personaPrevious.value = current;
    personaCard.value = result.card ?? null;
    personaCardPrompt.value = persona;
    form.value.system_prompt = persona;
    personaComposerOpen.value = false;
    personaDraft.value = "";
    toastSuccess("人设已生成，确认后记得保存配置");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "人设生成失败");
  } finally {
    personaBusy.value = false;
  }
}

function undoPersonaGenerate(): void {
  if (!form.value) return;
  form.value.system_prompt = personaPrevious.value;
  personaPrevious.value = "";
  // 退回上一版之后，那张卡拼出来的正文已经不在框里了，跟着一起丢掉。
  personaCard.value = null;
  personaCardPrompt.value = "";
}

function personaCardMatchesPrompt(prompt: string): boolean {
  return Boolean(personaCard.value) && prompt.trim() === personaCardPrompt.value.trim();
}

const personaCardExportable = computed(() => personaCardMatchesPrompt(form.value?.system_prompt ?? ""));

// 导出成一张标准的 SillyTavern V2 角色卡：这个文件酒馆认，Diana 自己的角色卡
// 导入接口也认，等于生成一次就能到处用。
function exportPersonaCard(): void {
  const card = personaCard.value;
  if (!card) return;
  downloadPersonaFile(`${personaFileSlug(card.data?.name || "persona")}.card.json`, JSON.stringify(card, null, 2));
}

function resetPromptDefaults(): void {
  if (!form.value) {
    return;
  }
  Object.assign(form.value, promptDefaults, {
    prompt_inject_time: true,
    prompt_inject_plaintext_rules: true,
    prompt_inject_group_sender: true,
    prompt_chinese_slang_hint: true
  });
  toastSuccess("已恢复内置提示词，保存配置后生效");
}

async function save(): Promise<void> {
  const current = form.value;
  if (!current) {
    return;
  }
  if (!current.connection_profile_id && (!current.platform || current.platform === "onebot-v11") && current.onebot_transport !== "http" && !validWebSocketURL(current.onebot_transport === "forward_ws" ? current.onebot_ws_endpoint || "" : current.onebot_reverse_ws_endpoint)) {
    toastError("请填写有效的 ws:// 或 wss:// 连接地址");
    return;
  }
  if (connectionConflict.value) {
    editorTab.value = "access";
    toastError(`此 WebSocket 地址已由「${connectionConflict.value.name || "未命名机器人"}」使用，请选择复用或填写不同的地址`);
    return;
  }
  // 后端会拒绝「启用 + 反向 WS + 空 token」的保存；提前拦住，错误提示更贴上下文。
  if (
    !current.connection_profile_id &&
    (!current.platform || current.platform === "onebot-v11") &&
    current.enabled &&
    (!current.onebot_transport || current.onebot_transport === "reverse_ws") &&
    !current.onebot_access_token_configured &&
    !tokenDraft.value.trim()
  ) {
    toastError("反向 WebSocket 模式必须配置 Access Token，需与 OneBot v11 客户端保持一致");
    return;
  }
  // 留空 = 没配过，提交时整个字段不带上，后端按默认 5 次；填 0 才是「出错别通知」。
  const failureAlertThresholdDraft = current.recurring_failure_alert_threshold;
  const failureAlertThreshold =
    failureAlertThresholdDraft === undefined || failureAlertThresholdDraft === null || `${failureAlertThresholdDraft}`.trim() === ""
      ? undefined
      : Number(failureAlertThresholdDraft);
  if (
    failureAlertThreshold !== undefined &&
    failureAlertThreshold !== 0 &&
    (!Number.isInteger(failureAlertThreshold) || failureAlertThreshold < 1 || failureAlertThreshold > maximumRecurringFailureAlertThreshold)
  ) {
    toastError(`连续失败几次才报请输入 1 到 ${maximumRecurringFailureAlertThreshold} 之间的整数`);
    return;
  }
  const recallDeleteDelay = Number(current.recall_reply_auto_delete_delay_seconds);
  if (
    current.recall_reply_auto_delete_enabled &&
    (!Number.isInteger(recallDeleteDelay) || recallDeleteDelay < 1 || recallDeleteDelay > maximumRecallReplyAutoDeleteDelaySeconds)
  ) {
    toastError(`回复保留时间请输入 1 到 ${maximumRecallReplyAutoDeleteDelaySeconds} 秒之间的整数`);
    return;
  }
  const sendRetryError = sendRetryValidationError(current);
  if (sendRetryError) {
    toastError(sendRetryError);
    return;
  }
  for (const row of [...modelRoleRows, ...purposeRoleRows]) {
    const role = roleForm.value[row.key];
    // 细分用途和媒体解析都可以留空：留空表示跟随它所属的那一档。
    if (!role && (row.key === "media_parse" || purposeRoleKeys.includes(row.key as (typeof purposeRoleKeys)[number]))) continue;
    // 跟随对话的那几档没有自己的提供商和模型，跳过校验；对话本身没有这个选项。
    if (row.key !== "chat" && role?.follow_chat) continue;
    if (!role || (!role.profile_id && !role.group && !(role.provider_id && role.model_id))) {
      editorTab.value = "model";
      toastError(`${row.label}必须选择提供商和模型`);
      return;
    }
    if (!role.model.trim()) {
      toastError(`${row.label}模型尚未选择`);
      return;
    }
		if (!role.provider_id && !role.model_id && !roleModelIsSelectable(row.key, role.model.trim())) {
      toastError(`${row.label}模型 ${role.model.trim()} 与当前提供商配置不兼容，请重新选择`);
      return;
    }
    for (const [index, fallback] of (role.fallbacks ?? []).entries()) {
      if ((!fallback.profile_id && !fallback.group && !(fallback.provider_id && fallback.model_id)) || !fallback.model.trim()) {
        toastError(`${row.label}后备 ${index + 1} 尚未完整选择`);
        return;
      }
      if (!fallback.provider_id && !fallback.model_id && !selectedRoleProfiles(row.key, fallback).some((profile) => profileCanRouteRoleModel(profile, row.key, fallback.model.trim()))) {
        toastError(`${row.label}后备 ${index + 1} 的模型与所选提供商不兼容`);
        return;
      }
    }
  }
  busy.value = true;
  try {
    const modelRoles: BotProfileConfig["model_roles"] = {};
    for (const key of orderedRoleKeys(roleForm.value)) {
      const role = roleForm.value[key as RoleKey];
      if (key !== "chat" && role?.follow_chat) {
        modelRoles[key] = { model: "", follow_chat: true };
        continue;
      }
      if (role && (role.profile_id || role.group || (role.provider_id && role.model_id)) && role.model.trim()) {
        modelRoles[key] = {
        profile_id: role.profile_id,
        group: role.group,
        model: role.model.trim(),
        provider_id: role.provider_id,
        model_id: role.model_id,
        fallbacks: role.fallbacks?.map((fallback) => ({ ...fallback, model: fallback.model.trim() }))
      };
      }
    }
    // 草稿为空表示「没改过」，字段留空提交，后端会沿用已存的那份；填了才覆盖。
    const secrets: Record<string, string | undefined> = {};
    for (const [field, draft] of Object.entries(tokenDrafts)) {
      secrets[field] = current.connection_profile_id && (field === "onebot_access_token" || field === "onebot_http_secret") ? undefined : draft.value.trim() || undefined;
    }
    const payload: BotProfileConfig = {
      ...current,
      ...(selectedPersonaID.value === "custom" ? { persona_id: "", custom_persona: asCustomPersona(current).custom_persona } : {}),
      forward_reply_threshold: Number(current.forward_reply_threshold) || 0,
      // 数字框清空后 v-model.number 给的是空串，后端按整数解析会整份拒收。
      model_call_quota: Math.max(0, Math.round(Number(current.model_call_quota) || 0)),
      reply_sample_percent: Math.min(100, Math.max(0, Math.round(Number(current.reply_sample_percent) || 0))),
      forward_reply_chunk_threshold: Number(current.forward_reply_chunk_threshold) || 0,
      reply_merge_confidence_percent: Number(current.reply_merge_confidence_percent) || 0,
      ...sendRetryPayload(current),
      typing_delay_per_char_ms: Number(current.typing_delay_per_char_ms) || 0,
      ...secrets,
      group_triggers: splitList(triggersDraft.value),
      welcome_templates: welcomeTemplatesDraft.value
        .split("\n")
        .map((item) => item.trim())
        .filter((item) => item !== ""),
      welcome_llm_cooldown_seconds: Number(current.welcome_llm_cooldown_seconds) || 0,
      agent_command_allowlist: splitList(allowlistDraft.value),
      recurring_failure_alert_threshold: failureAlertThreshold,
      recall_reply_auto_delete_delay_seconds: Number.isInteger(recallDeleteDelay)
        ? recallDeleteDelay
        : defaultRecallReplyAutoDeleteDelaySeconds,
      group_admission: { mode: admissionMode.value },
      private_admission: {
        mode: privateAdmissionMode.value,
        allowed_users: [...privateAllowedUsers.value]
      },
      model_roles: modelRoles
    };
    const saved = await (creating.value ? createBotProfileConfig(payload) : saveBotProfileConfig(payload));
    applyConfig(saved);
    creating.value = false;
    // 档位走自己的接口，但用户看到的是同一个保存按钮：配置存好之后立刻补上，
    // 失败了单独报，不能把「机器人配置已保存」这句也一起吞掉。
    try {
      await residencyPanel.value?.applyPending();
    } catch (error) {
      toastError(error instanceof Error ? `档位没保存成功：${error.message}` : "档位没保存成功");
    }
    toastSuccess("机器人配置已保存");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    busy.value = false;
  }
}

function validWebSocketURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return (parsed.protocol === "ws:" || parsed.protocol === "wss:") && Boolean(parsed.host);
  } catch {
    return false;
  }
}


// 编辑哪台机器人只是这个页面自己的状态：直接用列表里的那台填表单，不通知服务端。
// 以前这里会先调用「切换激活」，把选中的机器人写成全局的当前机器人，影响运行时判断，
// 两个人同时开控制台还会互相覆盖。
async function editProfile(profile: BotProfileConfig): Promise<void> {
  if (!profile.id) {
    return;
  }
  setForm(profile);
  creating.value = false;
  editorTab.value = "access";
  page.value = "edit";
}

function leaveEditor(): void {
  // 放弃未保存的修改：按表单里这台机器人的 ID 找回列表里保存过的配置。
  const current = profiles.value.find((profile) => profile.id === form.value?.id);
  if (current) {
    setForm(current);
  }
  creating.value = false;
  page.value = "list";
}

async function beginCreate(platform: BotPlatform, connectionProfileID = ""): Promise<void> {
  if (busy.value) return;
  busy.value = true;
  try {
    const defaults = await getNewBotProfileDefaults(platform.id);
    copiedFrom.value = null;
    setForm({
      ...defaults,
      id: undefined,
      name: `新建 ${platform.name} 机器人`,
      connection_profile_id: connectionProfileID,
      platform: platform.id
    });
    creating.value = true;
    platformPickerOpen.value = false;
    editorTab.value = "access";
    page.value = "edit";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载机器人默认配置失败");
  } finally {
    busy.value = false;
  }
}

async function removeProfile(profile: BotProfileConfig): Promise<void> {
  if (!profile.id) {
    return;
  }
  const users = connectionUsers(profile);
  if (users.length) {
    toastError(`「${users.map((item) => item.name || "未命名机器人").join("、")}」仍在复用此连接，请先更换它们的连接来源`);
    return;
  }
  const ok = await askConfirm({
    title: "删除机器人",
    message: `确定删除「${profile.name || "未命名"}」吗？该机器人的配置会被移除，此操作不可撤销。`,
    confirmLabel: "删除",
    danger: true
  });
  if (!ok) {
    return;
  }
  busy.value = true;
  try {
    applyConfig(await deleteBotProfile(profile.id));
    toastSuccess("配置档已删除");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally {
    busy.value = false;
  }
}

async function copyEndpoint(): Promise<void> {
  const endpoint = form.value?.onebot_reverse_ws_endpoint;
  if (!endpoint) {
    return;
  }
  try {
    await navigator.clipboard.writeText(endpoint);
    toastSuccess("已复制连接地址");
  } catch {
    toastError("复制失败，请手动选择复制");
  }
}

onBeforeUnmount(() => {
  headerResizeObserver?.disconnect();
  headerResizeObserver = null;
});

async function load(): Promise<void> {
  loading.value = true;
  // 人设库和世界书单独拉，不放进下面那组 Promise.all：它们只是素材库，
  // 慢一点或者读不出来都不该拖住机器人配置本身的加载。
  void loadPersonaLibrary();
  void loadWorldBook();
  const [platformResult, botConfig, llmConfig] = await Promise.all([
    getBotPlatforms().catch(() => ({ platforms: [] as BotPlatform[] })),
    getBotProfileConfig().catch((error: unknown) => {
      toastError(error instanceof Error ? error.message : "加载配置失败");
      return null;
    }),
    getConfig().catch(() => null)
  ]);
  platforms.value = platformResult.platforms.length
    ? platformResult.platforms
    : [{ id: "onebot-v11", name: "OneBot v11", protocol: "onebot-v11", category: "qq", category_label: "QQ" }];
  if (botConfig) {
    applyConfig(botConfig);
  }
  const channels = llmConfig?.profiles ?? [];
  llmChannels.value = channels;
  // 模型清单由提供商配置页维护。机器人页只用已保存结果，不再为每个提供商
  // 自动发一次远程模型请求；账号多时这曾是页面打开后最大的网络开销。
  loading.value = false;
}

onMounted(() => {
  trackHeaderHeight();
  void load().then(openRequestedTab);
});

// 别处（运行记录里的上下文构成）跳过来时带着 ?tab=context：那边看到工具占了多少，
// 这边才是改它的地方。跳过来要落在正确的机器人上，所以按顶栏的作用域挑；没选作用域
// 又只有一台时就是它，再多就停在列表让人自己点。
function openRequestedTab(): void {
  const requested = viewQuery().get("tab") as EditorTab | null;
  if (!requested || !editorTabs.some((tab) => tab.key === requested)) return;
  const target = profiles.value.find((profile) => profile.id === botScope.value) ?? (profiles.value.length === 1 ? profiles.value[0] : undefined);
  if (!target) return;
  void editProfile(target).then(() => {
    editorTab.value = requested;
  });
}
useConfigurationRefresh(["llm"], async () => {
  const config = await getConfig();
  llmChannels.value = config.profiles ?? [];
});
useConfigurationRefresh(["bot"], async () => {
  const config = await getBotProfileConfig();
  profileSet.value = config;
  // An editor can have unsaved changes while another cached page saves data.
  if (page.value !== "edit") {
    setForm(config);
    return;
  }
  syncModelRolesWhileEditing(config);
});

// 编辑页开着的时候不能拿服务端那份覆盖整个草稿，但模型分配这一档得跟上：主人
// 多半就是刚在聊天里让机器人换完模型，再回到这一页看结果，页面停在旧值等于告诉
// 他没换成。草稿里这一档没动过就直接换成新值；动过了只挂一条提示，两边都改时
// 替谁做主都是错的。
function syncModelRolesWhileEditing(config: BotProfileConfig): void {
  const editing = form.value?.id;
  if (!editing) return;
  const latest = config.id === editing ? config : (config.profiles ?? []).find((profile) => profile.id === editing);
  if (!latest) return;
  const incoming = roleSnapshot(latest.model_roles);
  if (incoming === savedRoleSnapshot.value) return;
  if (roleSnapshot(roleForm.value) === savedRoleSnapshot.value) {
    setRoleForm(latest.model_roles);
    return;
  }
  incomingModelRoles.value = latest.model_roles;
  modelRolesChangedElsewhere.value = true;
}

// 放弃这一档的草稿，改用服务端最新的模型分配；其余草稿字段不动。
function adoptIncomingModelRoles(): void {
  setRoleForm(incomingModelRoles.value);
}

</script>
