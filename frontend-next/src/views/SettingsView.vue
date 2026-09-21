<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>设置</h1>
        <p>控制台自身的配置。机器人怎么说话、回不回复，在「机器人」页里改。</p>
      </div>
    </header>

    <!-- 左侧分类菜单 + 右侧内容：和 anime-rss 的设置页同一套交互——分区收进
         侧栏分组，正文一次只显示选中的一项，长页不用来回滚。 -->
    <div class="settings-layout">
      <aside class="settings-side">
        <nav class="settings-side-nav" aria-label="设置分类">
          <div v-for="group in settingsGroups" :key="group.label" class="settings-side-group">
            <span class="settings-side-group-label">{{ group.label }}</span>
            <button
              v-for="page in group.pages"
              :key="page.key"
              type="button"
              class="settings-side-link"
              :class="{ 'settings-side-link-active': activePage === page.key }"
              :aria-current="activePage === page.key ? 'page' : undefined"
              @click="activePage = page.key"
            >
              <component :is="page.icon" :size="15" aria-hidden="true" />
              {{ page.label }}
            </button>
          </div>
        </nav>
      </aside>

      <div class="settings-content">
        <header class="settings-page-head">
          <h2>{{ activePageMeta.label }}</h2>
          <p class="settings-page-desc">{{ activePageMeta.hint }}</p>
        </header>

      <div v-show="activePage === 'security'" class="settings-section-body">
          <!-- 访问安全 -->
          <section class="card">
          <div class="card-header">
            <SkeletonBlock v-if="authLoading" width="120px" height="21px" />
            <span v-else class="badge" :class="authRequired ? 'ok' : 'warn'">{{ authRequired ? "已开启密码保护" : "未设置密码" }}</span>
          </div>
          <div v-if="authLoading" class="card-body"><LoadingSkeleton kind="form" :count="3" label="正在加载访问安全设置" /></div>
          <div v-else class="card-body form-grid">
            <p v-if="!authRequired" class="muted field wide" style="margin: 0; font-size: 13px">
              当前控制台无需登录即可访问。部署在公网或局域网前，请务必设置管理密码。
            </p>
            <div class="field">
              <label for="sec-username">管理账号</label>
              <input id="sec-username" v-model="username" class="input" placeholder="留空则沿用当前账号" autocomplete="username" />
            </div>
            <div v-if="authRequired" class="field">
              <label for="sec-current">当前密码</label>
              <div class="password-field">
                <input
                  id="sec-current"
                  v-model="currentPassword"
                  class="input"
                  :type="showCurrentPassword ? 'text' : 'password'"
                  autocomplete="current-password"
                />
                <button
                  class="password-toggle"
                  type="button"
                  :aria-label="showCurrentPassword ? '隐藏当前密码' : '显示当前密码'"
                  @click="showCurrentPassword = !showCurrentPassword"
                >
                  <EyeOff v-if="showCurrentPassword" :size="16" aria-hidden="true" />
                  <Eye v-else :size="16" aria-hidden="true" />
                </button>
              </div>
            </div>
            <div class="field">
              <label for="sec-new">{{ authRequired ? "新密码（至少 8 位）" : "设置管理密码（至少 8 位）" }}</label>
              <div class="password-field">
                <input
                  id="sec-new"
                  v-model="newPassword"
                  class="input"
                  :type="showNewPassword ? 'text' : 'password'"
                  autocomplete="new-password"
                />
                <button
                  class="password-toggle"
                  type="button"
                  :aria-label="showNewPassword ? '隐藏新密码' : '显示新密码'"
                  @click="showNewPassword = !showNewPassword"
                >
                  <EyeOff v-if="showNewPassword" :size="16" aria-hidden="true" />
                  <Eye v-else :size="16" aria-hidden="true" />
                </button>
              </div>
            </div>
            <div class="field wide cluster" style="gap: 8px">
              <button class="btn primary" type="button" :disabled="savingPassword || username.length === 0 || newPassword.length === 0" @click="saveCredentials">
                <KeyRound :size="15" aria-hidden="true" />
                {{ savingPassword ? "保存中…" : authRequired ? "更新账号与密码" : "开启密码保护" }}
              </button>
            </div>
          </div>
        </section>

      </div>

      <div v-show="activePage === 'sessions'" class="settings-section-body">
          <!-- 登录会话 -->
          <section v-if="authRequired || authLoading" class="card">
          <div class="card-header">
            <h2>登录会话</h2>
            <span class="card-sub">机器人发来异常登录提醒时，在这里把对应设备踢下线</span>
          </div>
          <div class="card-body stack">
            <LoadingSkeleton v-if="sessionsLoading && sessions.length === 0" kind="sessions" :count="2" label="正在加载登录会话" />
            <p v-else-if="sessions.length === 0" class="muted" style="margin: 0; font-size: 13px">当前没有活跃会话。</p>
            <ul v-else class="session-list">
              <li v-for="session in sessions" :key="session.id" class="session-item">
                <div class="session-main">
                  <span class="session-name">
                    {{ session.device_name || "未知设备" }}
                    <span v-if="session.current" class="badge ok">当前设备</span>
                  </span>
                  <span class="session-meta">
                    {{ session.ip_address || "IP 未知" }} · 最后活跃 {{ formatTime(session.last_seen_at) }}
                  </span>
                  <span v-if="session.user_agent" class="session-agent">{{ session.user_agent }}</span>
                </div>
                <button
                  class="btn small danger"
                  type="button"
                  :disabled="revokingID !== ''"
                  @click="revokeSession(session)"
                >
                  <LogOut :size="14" aria-hidden="true" />
                  {{ revokingID === session.id ? "处理中…" : session.current ? "退出本机" : "踢下线" }}
                </button>
              </li>
            </ul>
            <div class="cluster" style="gap: 8px">
              <button class="btn" type="button" :disabled="sessionsLoading" @click="loadSessions">
                <RefreshCw :size="14" aria-hidden="true" />
                刷新
              </button>
              <button class="btn danger" type="button" :disabled="revokingID !== '' || otherSessionCount === 0" @click="revokeOthers">
                <LogOut :size="14" aria-hidden="true" />
                登出其他 {{ otherSessionCount }} 个设备
              </button>
            </div>
          </div>
        </section>

      </div>

      <div v-show="activePage === 'openapi'" class="settings-section-body">
          <!-- 对外 API 密钥 -->
          <section class="card">
          <div class="card-header">
            <h2>对外 API</h2>
            <SkeletonBlock v-if="pluginLoading" width="90px" height="21px" />
            <span v-else class="badge" :class="openAPIPluginEnabled ? 'ok' : 'warn'">{{ openAPIPluginEnabled ? "插件已启用" : "插件未启用" }}</span>
            <span class="card-sub">让 CI、监控这类外部系统通过 HTTP 接口给机器人推送消息</span>
          </div>
          <div class="card-body stack">
            <div class="cluster" style="gap: 8px; align-items: center">
              <p class="muted" style="margin: 0; font-size: 13px; flex: 1">
                未启用时外部调用一律 403。密钥、限流和启停统一在此管理，不属于任何机器人的插件配置。
              </p>
              <button class="btn small" type="button" :disabled="togglingPlugin || openAPIPlugin === null" @click="toggleOpenAPIPlugin">
                {{ togglingPlugin ? "处理中…" : openAPIPluginEnabled ? "停用" : "启用" }}
              </button>
            </div>
            <p class="muted" style="margin: 0; font-size: 13px">
              携带 <code class="mono">Authorization: Bearer &lt;密钥&gt;</code> 调用
              <code class="mono">POST /openapi/v1/messages</code>，正文里用
              <code class="mono">group_id</code> 或 <code class="mono">user_id</code> 指定目标会话、<code class="mono">text</code> 填内容；
              多通道部署时再带上 <code class="mono">platform</code> 或 <code class="mono">profile_id</code> 指路。
              <code class="mono">GET /openapi/v1/status</code> 可探活并列出可投递的通道。
            </p>
            <div v-if="openAPIPlugin" class="stack">
              <PluginSettingField v-for="spec in openAPIPlugin.manifest.settings ?? []" :key="spec.key" :spec="spec" :form="openAPISettings" />
              <button class="btn small" type="button" :disabled="savingOpenAPISettings" @click="saveOpenAPISettings"><Save :size="14" aria-hidden="true" />保存接口参数</button>
            </div>
            <div v-if="createdToken" class="openapi-token">
              <p class="openapi-token-hint">密钥只显示这一次，请立即复制保存：</p>
              <div class="cluster" style="gap: 8px; flex-wrap: wrap">
                <code class="mono openapi-token-value">{{ createdToken }}</code>
                <button class="btn small" type="button" @click="copyCreatedToken">复制</button>
                <button class="btn small ghost" type="button" @click="createdToken = ''">我已保存</button>
              </div>
            </div>
            <LoadingSkeleton v-if="apiKeysLoading && apiKeys.length === 0" kind="sessions" :count="2" label="正在加载 API 密钥" />
            <p v-else-if="apiKeys.length === 0" class="muted" style="margin: 0; font-size: 13px">还没有密钥。创建后外部系统才能调用推送接口。</p>
            <ul v-else class="session-list">
              <li v-for="key in apiKeys" :key="key.id" class="session-item">
                <div class="session-main">
                  <span class="session-name">{{ key.name }}</span>
                  <span class="session-meta mono">{{ key.prefix }}…</span>
                  <span class="session-meta">
                    创建于 {{ formatTime(key.created_at) }}
                    · {{ key.last_used_at ? `最近使用 ${formatTime(key.last_used_at)}` : "从未使用" }}
                  </span>
                </div>
                <button
                  class="btn small danger"
                  type="button"
                  :disabled="revokingKeyID !== ''"
                  @click="revokeKey(key)"
                >
                  {{ revokingKeyID === key.id ? "处理中…" : "吊销" }}
                </button>
              </li>
            </ul>
            <div class="cluster" style="gap: 8px">
              <input
                v-model="newKeyName"
                class="input"
                placeholder="密钥用途，例如 ci-notify"
                style="max-width: 240px"
                @keyup.enter="createKey"
              />
              <button class="btn primary" type="button" :disabled="creatingKey || newKeyName.trim().length === 0" @click="createKey">
                <KeyRound :size="15" aria-hidden="true" />
                {{ creatingKey ? "创建中…" : "创建密钥" }}
              </button>
            </div>
          </div>
        </section>
      </div>


      <div v-show="activePage === 'browser-control'" class="settings-section-body">
        <section class="card">
          <div class="card-header">
            <h2>浏览器控制</h2>
            <span class="badge" :class="browserPolicy.enabled ? (browserReady ? 'ok' : 'warn') : 'warn'">
              {{ browserPolicy.enabled ? (browserReady ? "可用" : "已启用，等扩展连接") : "未启用" }}
            </span>
            <button class="btn small ghost" type="button" :disabled="browserLoading" title="刷新" aria-label="刷新浏览器控制状态" @click="loadBrowserControl">
              <RefreshCw :size="14" aria-hidden="true" />
            </button>
            <span class="card-sub">机器人操作的是你自己浏览器里的页面，带着你的登录态</span>
          </div>
          <div class="card-body stack">
            <p class="muted" style="margin: 0; font-size: 13px">
              装在浏览器里的扩展反向连到这里，只能操作下面列出的站点。默认只读；点击、输入和导航要单独打开。
              任何时候你都可以在扩展或这一页按下接管，机器人立刻停手。还需要在对应机器人的 Agent 设置里单独打开这一档。
            </p>

            <div class="field">
              <label class="switch-row">
                <input v-model="browserPolicy.enabled" type="checkbox" />
                <span>启用浏览器控制（关闭会当场断开所有已连接的扩展）</span>
              </label>
              <label class="switch-row">
                <input v-model="browserPolicy.write_enabled" type="checkbox" :disabled="!browserPolicy.enabled" />
                <span>允许写操作：点击、输入、导航。关闭时只能读取和截图</span>
              </label>
            </div>

            <div class="field">
              <label for="browser-origins">允许的来源（每行一条）</label>
              <textarea
                id="browser-origins"
                v-model="browserOriginsText"
                class="input"
                rows="2"
                placeholder="chrome-extension://abcdefghijklmnopabcdefghijklmnop"
              ></textarea>
              <p class="muted" style="margin: 0; font-size: 12.5px">
                填扩展选项页上显示的扩展 ID，写成 <code class="mono">chrome-extension://&lt;扩展 ID&gt;</code>。留空时谁都连不上。
              </p>
            </div>

            <div class="field">
              <label for="browser-allowed-hosts">可操作站点（每行一条）</label>
              <textarea
                id="browser-allowed-hosts"
                v-model="browserAllowedHostsText"
                class="input"
                rows="3"
                placeholder="example.com&#10;*.wiki.example.com"
              ></textarea>
              <p class="muted" style="margin: 0; font-size: 12.5px">
                <code class="mono">example.com</code> 只匹配这一个主机名；<code class="mono">*.example.com</code> 匹配子域但不含主域本身，
                两个都要就写两行。留空时一个站点都不允许。
              </p>
            </div>

            <div class="field">
              <label for="browser-denied-hosts">排除的站点（每行一条，优先于上面）</label>
              <textarea
                id="browser-denied-hosts"
                v-model="browserDeniedHostsText"
                class="input"
                rows="2"
                placeholder="admin.example.com"
              ></textarea>
            </div>

            <div class="cluster" style="gap: 8px; flex-wrap: wrap">
              <div class="field" style="max-width: 200px">
                <label for="browser-timeout">单条指令超时（毫秒）</label>
                <input id="browser-timeout" v-model.number="browserPolicy.command_timeout_ms" class="input" type="number" min="1000" max="120000" />
              </div>
              <div class="field" style="max-width: 200px">
                <label for="browser-rate">每分钟指令上限</label>
                <input id="browser-rate" v-model.number="browserPolicy.commands_per_minute" class="input" type="number" min="1" max="600" />
              </div>
            </div>

            <div class="cluster" style="gap: 8px">
              <button class="btn primary" type="button" :disabled="browserSaving" @click="saveBrowserPolicy">
                <Save :size="14" aria-hidden="true" />
                {{ browserSaving ? "保存中…" : "保存策略" }}
              </button>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2>控制令牌</h2>
            <span class="card-sub">扩展用它连接，只显示一次</span>
          </div>
          <div class="card-body stack">
            <div v-if="browserCreatedToken" class="openapi-token">
              <p class="openapi-token-hint">令牌只显示这一次，请立即复制并填进扩展选项页：</p>
              <div class="cluster" style="gap: 8px; flex-wrap: wrap">
                <code class="mono openapi-token-value">{{ browserCreatedToken }}</code>
                <button class="btn small" type="button" @click="copyBrowserToken">复制</button>
                <button class="btn small ghost" type="button" @click="browserCreatedToken = ''">我已保存</button>
              </div>
            </div>
            <LoadingSkeleton v-if="browserLoading && browserTokens.length === 0" kind="sessions" :count="2" label="正在加载令牌" />
            <p v-else-if="browserTokens.length === 0" class="muted" style="margin: 0; font-size: 13px">还没有令牌。签发后扩展才能连上来。</p>
            <ul v-else class="session-list">
              <li v-for="token in browserTokens" :key="token.id" class="session-item">
                <div class="session-main">
                  <span class="session-name">{{ token.name }}</span>
                  <span class="session-meta mono">{{ token.prefix }}…</span>
                  <span class="session-meta">
                    创建于 {{ formatTime(token.created_at) }}
                    · {{ token.last_used_at ? `最近使用 ${formatTime(token.last_used_at)}` : "从未使用" }}
                    · {{ token.extension_id ? `已绑定扩展 ${token.extension_id}` : "尚未绑定扩展" }}
                  </span>
                </div>
                <button class="btn small danger" type="button" :disabled="browserRevokingID !== ''" @click="revokeBrowserToken(token)">
                  {{ browserRevokingID === token.id ? "处理中…" : "吊销" }}
                </button>
              </li>
            </ul>
            <div class="cluster" style="gap: 8px">
              <input
                v-model="browserNewTokenName"
                class="input"
                placeholder="这台浏览器的用途，例如 公司台式机 Chrome"
                style="max-width: 280px"
                @keyup.enter="createBrowserToken"
              />
              <button class="btn primary" type="button" :disabled="browserCreating || browserNewTokenName.trim().length === 0" @click="createBrowserToken">
                <KeyRound :size="15" aria-hidden="true" />
                {{ browserCreating ? "签发中…" : "签发令牌" }}
              </button>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <h2>已连接的浏览器</h2>
            <span class="card-sub">接管打开时机器人一条指令都不会下发</span>
          </div>
          <div class="card-body stack">
            <p v-if="browserConnections.length === 0" class="muted" style="margin: 0; font-size: 13px">
              还没有扩展连上来。装好扩展、填上地址与令牌之后会自动出现在这里。
            </p>
            <ul v-else class="session-list">
              <li v-for="conn in browserConnections" :key="conn.id" class="session-item">
                <div class="session-main">
                  <span class="session-name">
                    {{ conn.label || conn.browser || "浏览器" }}
                    <span v-if="conn.takeover" class="badge warn">人工接管中</span>
                  </span>
                  <span class="session-meta mono">{{ conn.extension_id }}</span>
                  <span class="session-meta">
                    连接于 {{ formatTime(conn.connected_at) }}
                    · 可操作标签页 {{ conn.allowed_tabs }} 个
                    · 已下发 {{ conn.commands }} 条指令
                    <template v-if="conn.takeover_reason"> · {{ conn.takeover_reason }}</template>
                  </span>
                </div>
                <div class="cluster" style="gap: 6px">
                  <button class="btn small" type="button" @click="toggleBrowserTakeover(conn)">
                    {{ conn.takeover ? "交还控制权" : "人工接管" }}
                  </button>
                  <button class="btn small danger" type="button" @click="disconnectBrowser(conn)">断开</button>
                </div>
              </li>
            </ul>
          </div>
        </section>
      </div>

      <div v-show="activePage === 'cache'" class="settings-section-body">
        <section class="download-cache-settings">
          <div class="card-header">
            <h2>下载缓存</h2>
            <button class="btn small ghost" type="button" :disabled="cacheLoading || cacheSaving" title="刷新缓存设置" aria-label="刷新缓存设置" @click="loadCachePolicy">
              <RefreshCw :size="14" aria-hidden="true" />
            </button>
          </div>
          <form class="card-body" @submit.prevent="saveCachePolicy">
            <div v-if="cacheLoading && !cachePolicy" class="cache-policy-fields form-grid" role="status" aria-label="正在加载缓存设置">
              <div v-for="n in 2" :key="n" class="field"><SkeletonBlock width="90px" height="20px" /><SkeletonBlock height="37px" /></div>
              <div class="field wide"><SkeletonBlock width="150px" height="22px" /></div>
              <div class="field wide"><SkeletonBlock width="140px" height="38px" /></div>
            </div>
            <p v-if="cacheError" class="error" role="alert">{{ cacheError }}</p>
            <fieldset v-if="!cacheLoading || cachePolicy" class="cache-policy-fields form-grid" :disabled="cacheLoading || cacheSaving || !cachePolicy">
              <div class="field">
                <label for="cache-cleanup-mode">清理策略</label>
                <select id="cache-cleanup-mode" v-model="cacheMode" class="input">
                  <option value="days">按闲置天数清理</option>
                  <option value="capacity">仅按容量清理</option>
                  <option value="never">永不自动清理</option>
                </select>
              </div>
              <div v-if="cacheMode === 'days'" class="field">
                <label for="cache-retention-days">闲置保留天数</label>
                <input id="cache-retention-days" v-model.number="cacheDays" class="input" type="number" min="1" max="36500" step="1" required />
              </div>
              <div v-if="cacheMode === 'days'" class="field wide">
                <label class="cache-capacity-toggle">
                  <input v-model="cacheLimitEnabled" type="checkbox" />
                  同时限制缓存容量
                </label>
              </div>
              <div v-if="cacheMode === 'capacity' || (cacheMode === 'days' && cacheLimitEnabled)" class="field">
                <label for="cache-max-mb">容量上限（MiB）</label>
                <input id="cache-max-mb" v-model.number="cacheMaxMB" class="input" type="number" min="1" max="1048576" step="1" required />
              </div>
              <div class="field wide">
                <button class="btn primary" type="submit" :disabled="!cacheDraftValid || !cacheDirty">
                  <Save :size="15" aria-hidden="true" />
                  {{ cacheSaving ? "保存中…" : "保存缓存设置" }}
                </button>
              </div>
            </fieldset>
          </form>
        </section>
      </div>

      <div v-show="activePage === 'media'" class="settings-section-body">
        <section class="download-cache-settings">
          <div class="card-header"><h2>历史媒体原件</h2><button class="btn small ghost" type="button" :disabled="historyMediaLoading || historyMediaSaving" @click="loadHistoryMediaPolicy"><RefreshCw :size="14" /></button></div>
          <form class="card-body form-grid" @submit.prevent="saveHistoryMedia">
            <p v-if="historyMediaError" class="error field wide">{{ historyMediaError }}</p>
            <div class="field"><label for="history-media-days">保留天数</label><input id="history-media-days" v-model.number="historyMediaDays" class="input" type="number" min="-1" max="36500" /><span class="hint">-1 表示不按时间删除。</span></div>
            <div class="field"><label for="history-media-max">容量上限（MiB）</label><input id="history-media-max" v-model.number="historyMediaMaxMB" class="input" type="number" min="0" max="1048576" /><span class="hint">0 表示不限制容量。</span></div>
            <p class="hint field wide">清理只删除图片、视频、音频、PDF 等历史原件；聊天文字、媒体类型和已有摘要保留。删除后历史记录会显示原件不可用。</p>
            <div class="field wide"><button class="btn primary" type="submit" :disabled="historyMediaLoading || historyMediaSaving || !historyMediaValid"><Save :size="15" />{{ historyMediaSaving ? "清理中…" : "保存并立即清理" }}</button></div>
          </form>
        </section>
        <section class="download-cache-settings">
          <div class="card-header">
            <h2>媒体回源</h2>
            <button class="btn small ghost" type="button" :disabled="mediaBaseURLLoading || mediaBaseURLSaving" title="刷新媒体回源设置" aria-label="刷新媒体回源设置" @click="loadMediaBaseURL">
              <RefreshCw :size="14" aria-hidden="true" />
            </button>
          </div>
          <form class="card-body form-grid" @submit.prevent="saveMediaBaseURL">
            <p v-if="mediaBaseURLError" class="error field wide" role="alert">{{ mediaBaseURLError }}</p>
            <div class="field wide">
              <label for="media-base-url">回源基址</label>
              <input id="media-base-url" v-model="mediaBaseURL" class="input mono" placeholder="留空自动推断，例如 http://192.168.1.10:18080/media/resolver" :disabled="mediaBaseURLLoading || mediaBaseURLSaving" />
              <span class="hint">发送文件/图片时，接入端（NapCat 等）按这个地址回源拉取媒体。留空时按接入方式自动推断：反向 ws 用握手地址，正向 ws / HTTP 用接入端地址推主机名 + Diana 的 Web 端口。跨机或反向代理部署收不到文件时，把接入端实际可访问的 Diana 地址填到这里。当前生效来源：{{ mediaBaseURLSourceLabel }}。</span>
            </div>
            <div class="field wide">
              <button class="btn primary" type="submit" :disabled="mediaBaseURLLoading || mediaBaseURLSaving">
                <Save :size="15" aria-hidden="true" />
                {{ mediaBaseURLSaving ? "保存中…" : "保存回源设置" }}
              </button>
            </div>
          </form>
        </section>
      </div>

      <div v-show="activePage === 'update'" class="settings-section-body">
        <!-- 系统更新：标题和说明在正文页头已经有一份，卡片头只留状态徽标和刷新。 -->
        <section class="card">
          <div class="card-header" style="justify-content: space-between">
            <SkeletonBlock v-if="loading && !systemVersion" width="90px" height="21px" />
            <span v-else class="badge">{{ deploymentMode === "git" ? "源码更新" : systemVersion?.update_supported ? "Release 自更新" : "Docker" }}</span>
            <button class="btn small ghost" type="button" :disabled="loading" title="刷新更新状态" @click="loadUpdates">
              <RefreshCw :size="14" aria-hidden="true" />
            </button>
          </div>
          <div v-if="loading && !systemVersion" class="card-body stack" style="gap: 10px" role="status" aria-label="正在加载更新状态">
            <div class="info-row"><SkeletonBlock width="64px" height="20px" /><SkeletonBlock width="80px" height="20px" /></div>
            <SkeletonBlock width="85%" height="19px" />
            <SkeletonBlock height="38px" />
          </div>
          <div v-else class="card-body stack" style="gap: 10px; font-size: 13px">
            <div class="info-row">
              <span class="muted info-label">当前版本</span>
              <span class="info-value cluster" style="gap: 6px; justify-content: flex-end">
                <span v-if="sourceBuild" class="badge warn">源码构建</span>
                <span v-if="currentVersionLabel" class="mono">{{ currentVersionLabel }}</span>
              </span>
            </div>
            <p class="muted" style="font-size: 12.5px; margin: 0">
              {{ deploymentMode === "git" ? "发现新版本时仅显示黄色提示点，确认后才会同步最新稳定 Release。" : systemVersion?.update_supported ? "Release 更新先下载并校验；重启并安装必须单独确认，默认不会自动执行。" : "控制台仅提示新版本；Docker 镜像需由部署环境手动更新。" }}
            </p>

            <template v-if="deploymentMode === 'git' && updateStatus">
              <div class="info-row">
                <span class="muted info-label">分支 / 提交</span>
                <span class="mono info-value">{{ updateStatus.branch || "—" }} · {{ shortCommit }}</span>
              </div>
              <div v-if="updateStatus.dirty" class="badge warn">工作区有未提交修改，更新可能被跳过</div>
            </template>
            <div class="cluster">
              <button v-if="systemVersion?.update_supported" class="btn primary" type="button" :disabled="operationRunning" @click="runUpdate">
                <RefreshCw v-if="deploymentMode === 'release' && downloadReadyForLatest" :size="15" aria-hidden="true" />
                <Download v-else :size="15" aria-hidden="true" />
                {{ operationRunning ? "处理中…" : deploymentMode === "git" ? "重启并安装" : downloadReadyForLatest ? "重启并安装" : "下载最新 Release" }}
              </button>
            </div>
            <p v-if="staleDownloadedVersion" class="muted" style="font-size: 12.5px; margin: 0">
              已下载 {{ updateStatus?.downloaded_version }}，但最新版本是 {{ latestVersion }}；下次下载会替换旧安装包。
            </p>
            <div v-if="operationRunning && deploymentMode === 'release'" class="update-progress" role="progressbar" aria-label="Release 下载进度" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="updatePercent">
              <div class="update-progress-label">
                <span>{{ updatePhaseLabel }}</span>
                <strong class="mono">{{ updatePercent }}%</strong>
              </div>
              <div class="update-progress-track"><span :style="{ width: `${updatePercent}%` }"></span></div>
            </div>
            <pre v-if="updateOutput" class="mono update-output" :class="{ error: updateFailed }">{{ updateOutput }}</pre>
          </div>
        </section>

        <!-- Token 只影响查询版本时的 API 限额，装不装都能更新：独立一张卡片，不混进更新操作里。 -->
        <section class="card">
          <div class="card-header"><span class="card-sub">GitHub Token（可选）</span></div>
          <div class="card-body stack" style="gap: 10px; font-size: 13px">
            <label class="update-token-field">
              <input
                v-model="githubToken"
                class="input"
                type="password"
                autocomplete="new-password"
                :placeholder="githubTokenFromEnvironment ? '已由环境变量提供' : githubTokenConfigured ? '已配置，留空保持不变' : '提高版本查询的 API 限额'"
                :disabled="githubTokenFromEnvironment"
              />
            </label>
            <div class="cluster update-token-actions">
              <button class="btn small" type="button" :disabled="savingToken || !githubToken || githubTokenFromEnvironment" @click="persistGitHubToken(false)">保存 Token</button>
              <button v-if="githubTokenConfigured && !githubTokenFromEnvironment" class="btn ghost small" type="button" :disabled="savingToken" @click="persistGitHubToken(true)">清除</button>
            </div>
            <p class="muted" style="font-size: 12.5px; margin: 0">
              匿名查询 GitHub 版本有限额，用得频繁时容易被限流；填一个只读 Token 就够，不填也能正常更新。也可以改用环境变量
              <code>DIANA_GITHUB_TOKEN</code>。
            </p>
          </div>
        </section>

        <section class="card">
          <div class="card-header"><span class="card-sub">重启服务</span></div>
          <div class="card-body stack" style="gap: 10px; font-size: 13px">
            <div class="cluster">
              <button class="btn" type="button" :disabled="restarting" @click="doRestart">
                <RotateCw :size="15" aria-hidden="true" />
                {{ restarting ? "重启中，等待服务恢复…" : "重启服务" }}
              </button>
            </div>
            <p class="muted" style="font-size: 12.5px; margin: 0">原地重启当前服务进程，更新拉取后需重启才生效。恢复后页面会自动刷新。</p>
          </div>
        </section>

      </div>

      <div v-show="activePage === 'status'" class="settings-section-body">
        <!-- 运行状态：版本号只在「系统更新」显示一次，这里只放运行期信息。 -->
        <section class="card">
          <div class="card-header">
            <h2>运行状态</h2>
          </div>
          <div class="card-body stack" style="gap: 8px; font-size: 13px">
            <div class="info-row">
              <span class="muted info-label">运行时长</span>
              <SkeletonBlock v-if="healthLoading" width="80px" height="18px" />
              <span v-else class="info-value">{{ health ? formatUptime(health.uptime_seconds) : "—" }}</span>
            </div>
            <div class="info-row">
              <span class="muted info-label">启动时间</span>
              <SkeletonBlock v-if="healthLoading" width="140px" height="18px" />
              <span v-else class="mono info-value">{{ health ? formatTime(health.started_at) : "—" }}</span>
            </div>
          </div>
        </section>
      </div>

      <div v-show="activePage === 'theme'" class="settings-section-body">
        <!-- 主题 -->
        <section class="card">
          <div class="card-header">
            <h2>界面主题</h2>
          </div>
          <div class="card-body stack">
            <div class="field">
              <label>主题模式</label>
              <div class="segmented" role="radiogroup" aria-label="主题模式">
                <button type="button" :class="{ active: theme.mode === 'auto' }" @click="theme.mode = 'auto'">跟随系统</button>
                <button type="button" :class="{ active: theme.mode === 'light' }" @click="theme.mode = 'light'">浅色</button>
                <button type="button" :class="{ active: theme.mode === 'dark' }" @click="theme.mode = 'dark'">深色</button>
              </div>
            </div>
            <div class="field">
              <label>主题色</label>
              <div class="accent-swatches">
                <button
                  v-for="option in accentOptions"
                  :key="option.id"
                  type="button"
                  class="accent-swatch"
                  :class="{ selected: theme.accent === option.id }"
                  @click="theme.accent = option.id"
                >
                  <span class="swatch-dot" :style="{ background: option.color }" aria-hidden="true"></span>
                  {{ option.label }}
                </button>
              </div>
            </div>
          </div>
        </section>
      </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import PluginSettingField from "../components/PluginSettingField.vue";
import { Activity, Download, Eye, EyeOff, Globe, HardDriveDownload, Images, KeyRound, LogOut, MonitorSmartphone, Palette, Plug, RefreshCw, RotateCw, Save, ShieldCheck } from "@lucide/vue";
import {
  changeCredentials,
  getAuthStatus,
  listAuthSessions,
  revokeAuthSession,
  revokeOtherAuthSessions,
  getHealth,
  checkForUpdate,
  getSystemVersion,
  getUpdateGitHubToken,
  getUpdateStatus,
  saveUpdateGitHubToken,
	installDownloadedSystemUpdate,
	downloadSystemUpdate,
  pullFromGitHub,
  restartSystem,
  getMediaCachePolicy,
  saveMediaCachePolicy,
  type MediaCachePolicy,
  getHistoryMediaPolicy,
  saveHistoryMediaPolicy,
  type HistoryMediaPolicy,
  getMediaBaseURLSetting,
  saveMediaBaseURLSetting,
  type MediaBaseURLSetting,
  listOpenAPIKeys,
  createOpenAPIKey,
  revokeOpenAPIKey,
  getBrowserControlStatus,
  saveBrowserControlPolicy,
  createBrowserControlToken,
  revokeBrowserControlToken,
  setBrowserControlTakeover,
  disconnectBrowserControl,
  type BrowserControlConnection,
  type BrowserControlPolicy,
  type BrowserControlToken,
  listPlugins,
  setPluginEnabled,
  updatePluginSettings,
  type PluginState,
  type OpenAPIKey,
  type AuthSession,
  type HealthResponse,
  type SystemVersion,
  type UpdateCheckResponse,
  type UpdateStatus
} from "../api";
import { askConfirm } from "../confirm";
import { accentOptions, theme } from "../theme";
import { formatTime, formatUptime } from "../format";
import { toastError, toastSuccess } from "../toast";

// 侧栏菜单按「改的是谁的」分组：账号与安全决定谁能进来，系统是这台服务本身，
// 个性化只影响当前浏览器。正文一次只显示选中的一项。
const settingsPages = [
  { key: "security", label: "访问安全", hint: "谁能打开这个控制台：管理账号与密码保护。", icon: ShieldCheck },
  { key: "sessions", label: "登录会话", hint: "机器人发来异常登录提醒时，在这里把对应设备踢下线。", icon: MonitorSmartphone },
  { key: "openapi", label: "对外 API", hint: "让 CI、监控这类外部系统通过 HTTP 接口给机器人推送消息。", icon: Plug },
  { key: "browser-control", label: "浏览器控制", hint: "让机器人操作你自己浏览器里已授权站点的页面，随时可人工接管。", icon: Globe },
  { key: "cache", label: "下载缓存", hint: "控制下载的媒体缓存按闲置天数或容量清理。", icon: HardDriveDownload },
  { key: "media", label: "媒体与文件", hint: "历史媒体原件的保留策略，以及发送文件时接入端回源拉取媒体的地址。", icon: Images },
  { key: "update", label: "系统更新", hint: "检查、下载并安装新版本，以及原地重启服务。", icon: Download },
  { key: "status", label: "运行状态", hint: "当前服务的启动时间与运行时长。", icon: Activity },
  { key: "theme", label: "界面主题", hint: "只存在你当前这个浏览器里，不会同步到其它设备，也不影响别的登录用户。", icon: Palette }
] as const;

const settingsGroups: { label: string; pages: (typeof settingsPages)[number][] }[] = [
  { label: "账号与安全", pages: [settingsPages[0], settingsPages[1], settingsPages[2], settingsPages[3]] },
  { label: "系统", pages: [settingsPages[4], settingsPages[5], settingsPages[6], settingsPages[7]] },
  { label: "个性化", pages: [settingsPages[8]] }
];

const activePage = ref<(typeof settingsPages)[number]["key"]>("security");
const activePageMeta = computed(() => settingsPages.find((item) => item.key === activePage.value) ?? settingsPages[0]);

const cachePolicy = ref<MediaCachePolicy | null>(null);
const historyMediaDays = ref(-1);
const historyMediaMaxMB = ref(0);
const historyMediaLoading = ref(true);
const historyMediaSaving = ref(false);
const historyMediaError = ref("");
const historyMediaValid = computed(() => Number.isInteger(historyMediaDays.value) && historyMediaDays.value >= -1 && historyMediaDays.value <= 36500 && Number.isInteger(historyMediaMaxMB.value) && historyMediaMaxMB.value >= 0 && historyMediaMaxMB.value <= 1048576);
async function loadHistoryMediaPolicy() {
  historyMediaLoading.value = true; historyMediaError.value = "";
  try { const policy: HistoryMediaPolicy = await getHistoryMediaPolicy(); historyMediaDays.value = policy.retention_days; historyMediaMaxMB.value = policy.max_mb; }
  catch (error) { historyMediaError.value = error instanceof Error ? error.message : "历史媒体设置加载失败"; }
  finally { historyMediaLoading.value = false; }
}
async function saveHistoryMedia() {
  if (!historyMediaValid.value || historyMediaSaving.value) return;
  if (!(await askConfirm({
    title: "保存历史媒体策略",
    message: "保存后会立即删除超过保留天数或容量上限的历史原件。聊天文字和摘要会保留，但已删除原件无法恢复。",
    confirmLabel: "保存并清理",
    danger: true
  }))) return;
  historyMediaSaving.value = true; historyMediaError.value = "";
  try { await saveHistoryMediaPolicy({ retention_days: historyMediaDays.value, max_mb: historyMediaMaxMB.value }); toastSuccess("历史媒体策略已保存并完成清理"); }
  catch (error) { historyMediaError.value = error instanceof Error ? error.message : "历史媒体设置保存失败"; toastError(historyMediaError.value); }
  finally { historyMediaSaving.value = false; }
}
const cacheMode = ref<"days" | "capacity" | "never">("days");
// 媒体回源基址：文件/图片发送时接入端按它回源拉取媒体。留空走自动推断
// （反向 ws 按握手地址，正向 ws / HTTP 按接入端地址推主机 + 本服务 Web 端口）。
const mediaBaseURL = ref("");
const mediaBaseURLSource = ref<MediaBaseURLSetting["source"]>("auto");
const mediaBaseURLLoading = ref(true);
const mediaBaseURLSaving = ref(false);
const mediaBaseURLError = ref("");
const mediaBaseURLSourceLabel = computed(() => ({
  database: "已保存的设置",
  config: "config.yaml",
  auto: "自动推断"
})[mediaBaseURLSource.value]);
async function loadMediaBaseURL() {
  mediaBaseURLLoading.value = true; mediaBaseURLError.value = "";
  try {
    const setting = await getMediaBaseURLSetting();
    mediaBaseURL.value = setting.base_url; mediaBaseURLSource.value = setting.source;
  } catch (error) { mediaBaseURLError.value = error instanceof Error ? error.message : "媒体回源基址加载失败"; }
  finally { mediaBaseURLLoading.value = false; }
}
async function saveMediaBaseURL() {
  if (mediaBaseURLSaving.value) return;
  mediaBaseURLSaving.value = true; mediaBaseURLError.value = "";
  try {
    const setting = await saveMediaBaseURLSetting({ base_url: mediaBaseURL.value.trim() });
    mediaBaseURL.value = setting.base_url; mediaBaseURLSource.value = setting.source;
    toastSuccess("媒体回源基址已保存并生效");
  } catch (error) { mediaBaseURLError.value = error instanceof Error ? error.message : "媒体回源基址保存失败"; toastError(mediaBaseURLError.value); }
  finally { mediaBaseURLSaving.value = false; }
}
const cacheDays = ref(7);
const cacheMaxMB = ref(1024);
const cacheLimitEnabled = ref(false);
const cacheLoading = ref(true);
const cacheSaving = ref(false);
const cacheError = ref("");
const cacheDraft = computed<MediaCachePolicy>(() => ({
  retention_days: cacheMode.value === "days" ? Number(cacheDays.value) : -1,
  max_mb: cacheMode.value === "capacity" || (cacheMode.value === "days" && cacheLimitEnabled.value) ? Number(cacheMaxMB.value) : 0
}));
const cacheDraftValid = computed(() => {
  const { retention_days: days, max_mb: capacity } = cacheDraft.value;
  return (cacheMode.value !== "days" || (Number.isInteger(days) && days >= 1 && days <= 36500))
    && Number.isInteger(capacity) && capacity >= 0 && capacity <= 1048576
    && (!(cacheMode.value === "capacity" || (cacheMode.value === "days" && cacheLimitEnabled.value)) || capacity > 0);
});
const cacheDirty = computed(() => cachePolicy.value !== null && (
  cachePolicy.value.retention_days !== cacheDraft.value.retention_days || cachePolicy.value.max_mb !== cacheDraft.value.max_mb
));

function applyCachePolicy(policy: MediaCachePolicy) {
  cachePolicy.value = policy;
  cacheMode.value = policy.retention_days > 0 ? "days" : policy.max_mb > 0 ? "capacity" : "never";
  cacheDays.value = policy.retention_days > 0 ? policy.retention_days : 7;
  cacheLimitEnabled.value = policy.max_mb > 0;
  cacheMaxMB.value = policy.max_mb > 0 ? policy.max_mb : 1024;
}

async function loadCachePolicy() {
  cacheLoading.value = true;
  cacheError.value = "";
  try {
    applyCachePolicy(await getMediaCachePolicy());
  } catch (error) {
    cacheError.value = error instanceof Error ? error.message : "缓存设置加载失败";
  } finally {
    cacheLoading.value = false;
  }
}

async function saveCachePolicy() {
  if (!cacheDraftValid.value || !cacheDirty.value || cacheSaving.value || cacheLoading.value) return;
  cacheSaving.value = true;
  cacheError.value = "";
  try {
    applyCachePolicy(await saveMediaCachePolicy(cacheDraft.value));
    toastSuccess("缓存设置已保存并立即生效");
  } catch (error) {
    cacheError.value = error instanceof Error ? error.message : "缓存设置保存失败";
    toastError(cacheError.value);
  } finally {
    cacheSaving.value = false;
  }
}

const updateStatus = ref<UpdateStatus | null>(null);
const updateCheck = ref<UpdateCheckResponse | null>(null);
const systemVersion = ref<SystemVersion | null>(null);
const health = ref<HealthResponse | null>(null);
const loading = ref(true);
const authLoading = ref(true);
const healthLoading = ref(true);
const pluginLoading = ref(true);
const updating = ref(false);
const updateFailed = ref(false);
const restarting = ref(false);
const savingToken = ref(false);
const githubToken = ref("");
const githubTokenConfigured = ref(false);
const githubTokenFromEnvironment = ref(false);
const updateOutput = ref("");
const authRequired = ref(false);
const username = ref("");
const currentPassword = ref("");
const newPassword = ref("");
const showCurrentPassword = ref(false);
const showNewPassword = ref(false);
const savingPassword = ref(false);
const deploymentMode = ref<"git" | "release">("release");
const sessions = ref<AuthSession[]>([]);
const sessionsLoading = ref(true);
const revokingID = ref("");
const apiKeys = ref<OpenAPIKey[]>([]);
const apiKeysLoading = ref(true);
const creatingKey = ref(false);
const newKeyName = ref("");
const createdToken = ref("");
const revokingKeyID = ref("");
const openAPIPlugin = ref<PluginState | null>(null);
const openAPISettings = ref<Record<string, unknown>>({});
const savingOpenAPISettings = ref(false);
const togglingPlugin = ref(false);
const openAPIPluginEnabled = computed(() => openAPIPlugin.value?.enabled === true);
const browserPolicy = ref<BrowserControlPolicy>({ enabled: false, write_enabled: false, command_timeout_ms: 20000, commands_per_minute: 60 });
const browserTokens = ref<BrowserControlToken[]>([]);
const browserConnections = ref<BrowserControlConnection[]>([]);
const browserReady = ref(false);
const browserLoading = ref(true);
const browserSaving = ref(false);
const browserCreating = ref(false);
const browserRevokingID = ref("");
const browserNewTokenName = ref("");
const browserCreatedToken = ref("");
// 三个站点列表在界面上是多行文本，保存时才拆成数组：让用户一行一条地贴，
// 比逗号分隔好改，也不会因为多打一个逗号多出一条空白规则。
const browserOriginsText = ref("");
const browserAllowedHostsText = ref("");
const browserDeniedHostsText = ref("");

const OPEN_API_PLUGIN_ID = "official.open-api";
const otherSessionCount = computed(() => sessions.value.filter((item) => !item.current).length);
const operationRunning = computed(() => updating.value || updateStatus.value?.updating === true);
// 版本号还没加载出来时留空，不显示占位符。
const currentVersionLabel = computed(() => systemVersion.value?.version_label || systemVersion.value?.build_version || "");
const backendVersionLabel = computed(() => currentVersionLabel.value || health.value?.version || "");
// 源码构建不参与自动更新，只能在版本弹窗里显式切换到正式 Release。
const sourceBuild = computed(() => deploymentMode.value === "release" && systemVersion.value?.build_type === "source");
const latestVersion = computed(() => updateCheck.value?.latest_version || "");
const downloadReadyForLatest = computed(() => updateStatus.value?.download_ready === true
  && Boolean(updateStatus.value.downloaded_version)
  && (!latestVersion.value || updateStatus.value.downloaded_version === latestVersion.value));
const staleDownloadedVersion = computed(() => updateStatus.value?.download_ready === true
  && Boolean(updateStatus.value.downloaded_version)
  && Boolean(latestVersion.value)
  && updateStatus.value?.downloaded_version !== latestVersion.value);
let updateStatusPollTimer: number | undefined;

async function loadAuthStatus(): Promise<void> {
  try {
    const status = await getAuthStatus();
    authRequired.value = status.auth_required;
    username.value = status.username || "";
  } catch {
    /* 状态读取失败保持默认展示 */
  } finally {
    authLoading.value = false;
  }
}

async function saveCredentials(): Promise<void> {
  savingPassword.value = true;
  try {
    const result = await changeCredentials(currentPassword.value, username.value, newPassword.value);
    username.value = result.username;
    // 改密会清空所有旧会话，列表要跟着刷新。
    void loadSessions();
    toastSuccess(authRequired.value ? "账号与密码已更新" : "密码保护已开启");
    authRequired.value = true;
    currentPassword.value = "";
    newPassword.value = "";
    showCurrentPassword.value = false;
    showNewPassword.value = false;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存密码失败");
  } finally {
    savingPassword.value = false;
  }
}

async function loadSessions(): Promise<void> {
  if (!authRequired.value) {
    sessions.value = [];
    sessionsLoading.value = false;
    return;
  }
  sessionsLoading.value = true;
  try {
    sessions.value = (await listAuthSessions()).sessions;
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取登录会话失败");
  } finally {
    sessionsLoading.value = false;
  }
}

async function revokeSession(session: AuthSession): Promise<void> {
  const label = session.current ? "退出当前设备？" : `踢下线「${session.device_name || "未知设备"}」？`;
  if (!(await askConfirm({ title: "撤销登录会话", message: label, confirmLabel: "撤销", danger: true }))) return;
  revokingID.value = session.id;
  try {
    const result = await revokeAuthSession(session.id);
    if (result.current) {
      // 撤销的是自己，cookie 已被清掉，重载回登录页。
      window.location.reload();
      return;
    }
    toastSuccess("该设备已登出");
    await loadSessions();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "撤销会话失败");
  } finally {
    revokingID.value = "";
  }
}

async function revokeOthers(): Promise<void> {
  if (!(await askConfirm({
    title: "登出其他设备",
    message: `除当前设备外的 ${otherSessionCount.value} 个会话都会立即失效。`,
    confirmLabel: "全部登出",
    danger: true
  }))) {
    return;
  }
  revokingID.value = "others";
  try {
    const result = await revokeOtherAuthSessions();
    toastSuccess(`已登出 ${result.revoked} 个设备`);
    await loadSessions();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "登出其他设备失败");
  } finally {
    revokingID.value = "";
  }
}

async function loadOpenAPIPlugin(): Promise<void> {
  try {
    const plugins = await listPlugins();
    openAPIPlugin.value = plugins.find((item) => item.manifest.id === OPEN_API_PLUGIN_ID) ?? null;
    if (openAPIPlugin.value) openAPISettings.value = Object.fromEntries((openAPIPlugin.value.manifest.settings ?? []).map((spec) => [spec.key, openAPIPlugin.value?.settings?.[spec.key] ?? spec.default]));
  } catch {
    /* 拉不到插件状态时按未知处理，开关按钮保持禁用 */
  } finally {
    pluginLoading.value = false;
  }
}

async function toggleOpenAPIPlugin(): Promise<void> {
  if (openAPIPlugin.value === null || togglingPlugin.value) return;
  const next = !openAPIPluginEnabled.value;
  togglingPlugin.value = true;
  try {
    openAPIPlugin.value = await setPluginEnabled(OPEN_API_PLUGIN_ID, next);
    toastSuccess(next ? "对外 API 已启用" : "对外 API 已停用，外部调用将收到 403");
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换对外 API 状态失败");
  } finally {
    togglingPlugin.value = false;
  }
}

async function saveOpenAPISettings(): Promise<void> {
  savingOpenAPISettings.value = true;
  try {
    openAPIPlugin.value = await updatePluginSettings(OPEN_API_PLUGIN_ID, openAPISettings.value);
    toastSuccess("接口参数已保存");
  } catch (error) { toastError(error instanceof Error ? error.message : "保存失败"); }
  finally { savingOpenAPISettings.value = false; }
}

async function loadApiKeys(): Promise<void> {
  apiKeysLoading.value = true;
  try {
    apiKeys.value = (await listOpenAPIKeys()).keys;
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取 API 密钥失败");
  } finally {
    apiKeysLoading.value = false;
  }
}

async function createKey(): Promise<void> {
  const name = newKeyName.value.trim();
  if (name.length === 0 || creatingKey.value) return;
  creatingKey.value = true;
  try {
    const result = await createOpenAPIKey(name);
    // 明文只在这次响应里出现，先摆在页面上等用户自己复制，刷新即消失。
    createdToken.value = result.token;
    newKeyName.value = "";
    await loadApiKeys();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "创建 API 密钥失败");
  } finally {
    creatingKey.value = false;
  }
}

async function copyCreatedToken(): Promise<void> {
  try {
    await navigator.clipboard.writeText(createdToken.value);
    toastSuccess("密钥已复制");
  } catch {
    toastError("复制失败，请手动选中复制");
  }
}

async function revokeKey(key: OpenAPIKey): Promise<void> {
  if (!(await askConfirm({
    title: "吊销 API 密钥",
    message: `吊销「${key.name}」后，用它的外部系统会立即收到 401。`,
    confirmLabel: "吊销",
    danger: true
  }))) {
    return;
  }
  revokingKeyID.value = key.id;
  try {
    await revokeOpenAPIKey(key.id);
    toastSuccess("密钥已吊销");
    await loadApiKeys();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "吊销 API 密钥失败");
  } finally {
    revokingKeyID.value = "";
  }
}

function linesToList(value: string): string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
}

async function loadBrowserControl(): Promise<void> {
  browserLoading.value = true;
  try {
    const status = await getBrowserControlStatus();
    browserPolicy.value = status.policy;
    browserTokens.value = status.tokens ?? [];
    browserConnections.value = status.connections ?? [];
    browserReady.value = status.ready;
    browserOriginsText.value = (status.policy.allowed_origins ?? []).join("\n");
    browserAllowedHostsText.value = (status.policy.allowed_hosts ?? []).join("\n");
    browserDeniedHostsText.value = (status.policy.denied_hosts ?? []).join("\n");
  } catch (err) {
    toastError(err instanceof Error ? err.message : "读取浏览器控制状态失败");
  } finally {
    browserLoading.value = false;
  }
}

async function saveBrowserPolicy(): Promise<void> {
  if (browserSaving.value) return;
  const policy: BrowserControlPolicy = {
    ...browserPolicy.value,
    allowed_origins: linesToList(browserOriginsText.value),
    allowed_hosts: linesToList(browserAllowedHostsText.value),
    denied_hosts: linesToList(browserDeniedHostsText.value)
  };
  if (policy.enabled && (policy.allowed_hosts?.length ?? 0) === 0) {
    // 这不是错误配置，但它的效果是「启用了却一个站点都碰不到」，先说清楚再保存。
    if (!(await askConfirm({
      title: "没有授权任何站点",
      message: "站点白名单是空的，扩展连上来也读不了任何页面。确定就这样保存吗？",
      confirmLabel: "保存"
    }))) {
      return;
    }
  }
  browserSaving.value = true;
  try {
    const saved = await saveBrowserControlPolicy(policy);
    toastSuccess("浏览器控制策略已保存");
    browserPolicy.value = saved.policy;
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "保存浏览器控制策略失败");
  } finally {
    browserSaving.value = false;
  }
}

async function createBrowserToken(): Promise<void> {
  const name = browserNewTokenName.value.trim();
  if (name.length === 0 || browserCreating.value) return;
  browserCreating.value = true;
  try {
    const result = await createBrowserControlToken(name);
    // 明文只在这次响应里出现，摆在页面上等用户复制，刷新即消失。
    browserCreatedToken.value = result.plaintext;
    browserNewTokenName.value = "";
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "签发令牌失败");
  } finally {
    browserCreating.value = false;
  }
}

async function copyBrowserToken(): Promise<void> {
  try {
    await navigator.clipboard.writeText(browserCreatedToken.value);
    toastSuccess("令牌已复制");
  } catch {
    toastError("复制失败，请手动选中复制");
  }
}

async function revokeBrowserToken(token: BrowserControlToken): Promise<void> {
  if (!(await askConfirm({
    title: "吊销控制令牌",
    message: `吊销「${token.name}」后，用它连着的浏览器会立即断开，需要重新签发才能再连。`,
    confirmLabel: "吊销",
    danger: true
  }))) {
    return;
  }
  browserRevokingID.value = token.id;
  try {
    await revokeBrowserControlToken(token.id);
    toastSuccess("令牌已吊销");
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "吊销令牌失败");
  } finally {
    browserRevokingID.value = "";
  }
}

async function toggleBrowserTakeover(conn: BrowserControlConnection): Promise<void> {
  try {
    await setBrowserControlTakeover(conn.id, !conn.takeover, conn.takeover ? "" : "从 WebUI 接管");
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "切换接管状态失败");
  }
}

async function disconnectBrowser(conn: BrowserControlConnection): Promise<void> {
  if (!(await askConfirm({
    title: "断开浏览器连接",
    message: "断开后扩展会自动重连；要彻底停掉请关闭总开关或吊销令牌。",
    confirmLabel: "断开",
    danger: true
  }))) {
    return;
  }
  try {
    await disconnectBrowserControl(conn.id);
    await loadBrowserControl();
  } catch (err) {
    toastError(err instanceof Error ? err.message : "断开连接失败");
  }
}

const shortCommit = computed(() => {
  const commit = updateStatus.value?.head_commit;
  return commit ? commit.slice(0, 10) : "—";
});

async function loadGitHubTokenStatus(): Promise<void> {
  try {
    const status = await getUpdateGitHubToken();
    githubTokenConfigured.value = status.configured;
    githubTokenFromEnvironment.value = status.source === "environment";
  } catch {
    githubTokenConfigured.value = false;
    githubTokenFromEnvironment.value = false;
  }
}

async function persistGitHubToken(clear: boolean): Promise<void> {
  savingToken.value = true;
  try {
    const status = await saveUpdateGitHubToken(githubToken.value, clear);
    githubTokenConfigured.value = status.configured;
    githubTokenFromEnvironment.value = status.source === "environment";
    githubToken.value = "";
    toastSuccess(clear ? "GitHub Token 已清除" : "GitHub Token 已保存");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存 GitHub Token 失败");
  } finally {
    savingToken.value = false;
  }
}

async function loadUpdates(): Promise<void> {
  loading.value = true;
  try {
    const versionResult = await getSystemVersion();
    systemVersion.value = versionResult;
    deploymentMode.value = versionResult.deployment_mode;
    const [statusResult, checkResult] = versionResult.update_supported
      ? await Promise.all([getUpdateStatus().catch(() => null), checkForUpdate().catch(() => null)])
      : [null, null];
    updateCheck.value = checkResult;
    updateStatus.value = versionResult.update_supported ? (checkResult?.status ?? statusResult) : null;
  } catch {
    updateStatus.value = null;
    updateCheck.value = null;
  } finally {
    loading.value = false;
  }
}

async function runUpdate(): Promise<void> {
	if (operationRunning.value) return;
	const installingRelease = deploymentMode.value === "release" && downloadReadyForLatest.value;
  const confirmed = await askConfirm({
		title: installingRelease ? "重启并安装已下载版本？" : deploymentMode.value === "release" ? "下载最新稳定版本？" : "重启并安装最新稳定版本？",
		message: deploymentMode.value === "release"
		  ? installingRelease
			? "将备份数据库和当前版本，安装后自动重启并执行健康检查；失败时自动恢复。"
			: "只下载、校验并暂存完整 Release 包，不会安装或重启服务。"
      : "确认后才会同步到最新稳定 Release。更新完成前请勿关闭服务。",
		confirmLabel: installingRelease ? "重启并安装" : deploymentMode.value === "release" ? "下载更新" : "重启并安装"
  });
  if (!confirmed) return;
  updating.value = true;
  updateFailed.value = false;
  updateOutput.value = "";
  const progressTimer = deploymentMode.value === "release" && !installingRelease
    ? window.setInterval(() => {
        void getUpdateStatus().then((status) => { updateStatus.value = status; }).catch(() => undefined);
      }, 500)
    : undefined;
  try {
		const result = deploymentMode.value === "release"
		  ? installingRelease
			? await installDownloadedSystemUpdate()
			: await downloadSystemUpdate()
		  : await pullFromGitHub();
    updateStatus.value = result.status;
    updateOutput.value = result.output ?? "";
		toastSuccess(deploymentMode.value === "release"
		  ? installingRelease
			? "已开始重启并安装，完成后将执行健康检查"
			: result.downloaded ? "更新已下载并通过校验，等待重启并安装" : "已是最新，无需更新"
		  : result.updated ? "更新完成，重启服务后生效" : "已是最新，无需更新");
  } catch (error) {
    const message = error instanceof Error ? error.message : "更新失败";
    updateFailed.value = true;
    updateOutput.value = message;
    toastError(message);
  } finally {
    if (progressTimer !== undefined) window.clearInterval(progressTimer);
    updating.value = false;
    if (deploymentMode.value === "release") {
      await getUpdateStatus().then((status) => { updateStatus.value = status; }).catch(() => undefined);
    }
  }
}

const updatePercent = computed(() => Math.max(0, Math.min(100, Math.round(updateStatus.value?.download_percent ?? 0))));
const updatePhaseLabel = computed(() => {
  switch (updateStatus.value?.update_phase) {
    case "checksum": return "准备 → 下载校验清单";
    case "downloading": return `准备 → 下载 ${updatePercent.value}%`;
    case "extracting": return "准备 → 下载 100% → 校验 → 解压";
    case "ready": return "准备 → 下载 100% → 校验 → 解压 → 完成";
    default: return "准备更新";
  }
});

async function doRestart(): Promise<void> {
  const ok = await askConfirm({
    title: "重启服务",
    message: "服务会中断几秒，进行中的消息处理会被打断。确定重启吗？",
    confirmLabel: "重启",
    danger: true
  });
  if (!ok) {
    return;
  }
  restarting.value = true;
  const previousStart = health.value?.started_at ?? "";
  try {
    await restartSystem();
  } catch (error) {
    restarting.value = false;
    toastError(error instanceof Error ? error.message : "触发重启失败");
    return;
  }
  // 轮询健康检查，started_at 变化说明新进程已就绪；恢复后整页刷新。
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 1000));
    try {
      const current = await getHealth();
      if (current.started_at !== previousStart) {
        toastSuccess("服务已恢复");
        window.location.reload();
        return;
      }
    } catch {
      /* 服务重启期间健康检查失败属预期，继续等待 */
    }
  }
  restarting.value = false;
  toastError("等待服务恢复超时，请手动刷新页面确认状态");
}

onMounted(() => {
  void loadCachePolicy();
  void loadHistoryMediaPolicy();
  void loadMediaBaseURL();
  void loadUpdates();
  void loadGitHubTokenStatus();
  void loadAuthStatus().then(() => loadSessions());
  void loadApiKeys();
  void loadOpenAPIPlugin();
  void loadBrowserControl();
  void getHealth()
    .then((result) => {
      health.value = result;
    })
    .catch(() => {
      health.value = null;
    })
    .finally(() => { healthLoading.value = false; });
	updateStatusPollTimer = window.setInterval(() => {
		if (!operationRunning.value || deploymentMode.value !== "release") return;
		void getUpdateStatus().then((status) => { updateStatus.value = status; }).catch(() => undefined);
	}, 1000);
});

onBeforeUnmount(() => {
	if (updateStatusPollTimer !== undefined) window.clearInterval(updateStatusPollTimer);
});
</script>

<style scoped>
.update-token-field {
  display: grid;
  gap: 4px;
}

.update-token-field input {
  width: 100%;
  max-width: 360px;
}

.update-token-actions {
  gap: 8px;
}

.download-cache-settings {
  grid-column: 1 / -1;
  min-width: 0;
  border-bottom: 1px solid var(--border);
}

.cache-policy-fields {
  border: 0;
  padding: 0;
  margin: 0;
  min-width: 0;
  max-width: 640px;
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.cache-policy-fields .cache-capacity-toggle {
  display: flex;
  flex-direction: row;
  align-items: center;
  justify-content: flex-start;
  gap: 8px;
}

.cache-capacity-toggle input {
  width: auto;
  flex: 0 0 auto;
}

.cache-policy-fields .btn {
  width: fit-content;
  max-width: 100%;
  align-self: flex-start;
}

@media (max-width: 520px) {
  .cache-policy-fields { grid-template-columns: minmax(0, 1fr); }
}

.cache-policy-fields .input {
  min-width: 0;
  max-width: 100%;
}

/* 左侧分组菜单 + 右侧内容：和 anime-rss 设置页同一套两栏结构。窄屏收成单栏，
   菜单横排换行，不占纵向空间。 */
.settings-layout {
  display: grid;
  grid-template-columns: 190px minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}

.settings-side {
  position: sticky;
  /* 页头吸顶，菜单跟着停在其下方。 */
  top: calc(var(--topbar-height) + 84px);
}

.settings-side-nav {
  display: grid;
  gap: 16px;
}

.settings-side-group {
  display: grid;
  gap: 2px;
}

.settings-side-group-label {
  padding: 0 10px 5px;
  font-size: 11.5px;
  color: var(--muted);
}

.settings-side-link {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 7px 10px;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-secondary);
  font-size: 13.5px;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}

.settings-side-link:hover {
  background: var(--surface-2);
}

.settings-side-link-active,
.settings-side-link-active:hover {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}

.settings-content {
  min-width: 0;
}

/* 选中项的标题和一句话说明：分区名进了侧栏，「这一项管什么」由正文头部交代。 */
.settings-page-head {
  margin: 0 0 14px;
}

.settings-page-head h2 {
  margin: 0;
  font-size: 16px;
  font-weight: 650;
}

.settings-page-desc {
  margin: 4px 0 0;
  font-size: 12.5px;
  color: var(--muted);
}

@media (max-width: 640px) {
  .settings-layout { grid-template-columns: minmax(0, 1fr); }
  .settings-side { position: static; }
  .settings-side-nav { display: flex; flex-wrap: wrap; gap: 6px; }
  .settings-side-group { display: contents; }
  .settings-side-group-label { display: none; }
  .settings-side-link { width: auto; }
}

/* auto-fit 让只有一张卡的分区自己占满整行，右边不留空位；
   两张卡的分区并排，和原来的两列观感一致。 */
.settings-section-body {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
  gap: 16px;
  align-items: start;
}

@media (max-width: 960px) {
  .settings-section-body { grid-template-columns: minmax(0, 1fr); }
}


/* 新建密钥的一次性明文展示：要醒目（错过就再也拿不到），但不该像报错。 */
.openapi-token {
  display: grid;
  gap: 6px;
  padding: 10px;
  border: 1px solid color-mix(in srgb, var(--accent) 45%, var(--border));
  background: color-mix(in srgb, var(--accent) 8%, var(--surface-muted));
  border-radius: 6px;
}
.openapi-token-hint { margin: 0; font-size: 12.5px; color: var(--muted); }
.openapi-token-value {
  padding: 4px 8px;
  font-size: 12px;
  word-break: break-all;
  background: var(--surface-muted);
  border: 1px solid var(--border);
  border-radius: 4px;
}

.update-progress { display: grid; gap: 7px; }
.update-progress-label { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 12px; color: var(--muted); }
.update-progress-track { height: 7px; overflow: hidden; background: var(--surface-muted); border: 1px solid var(--border); border-radius: 4px; }
.update-progress-track span { display: block; height: 100%; min-width: 2px; background: var(--accent); transition: width 180ms ease; }
.update-output { margin: 0; padding: 10px; font-size: 11.5px; white-space: pre-wrap; color: var(--muted); border: 1px solid var(--border); background: var(--surface-muted); }
.update-output.error { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 45%, var(--border)); }
</style>
