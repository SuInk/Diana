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

    <!-- 按「改的是什么」分三档：账号与会话决定谁能进来，系统是这台服务本身，
         外观只影响你眼前这个浏览器。原先五张卡平铺成两列，既没有标题也没有边界，
         找一项设置只能一张张看过去。用和机器人配置同一套 tab，两处操作手感一致。 -->
    <nav class="editor-tabs settings-tabs" role="tablist" aria-label="设置分区">
      <button
        v-for="item in settingsTabs"
        :key="item.key"
        class="editor-tab"
        :class="{ active: tab === item.key }"
        type="button"
        role="tab"
        :aria-selected="tab === item.key"
        @click="tab = item.key"
      >
        {{ item.label }}
      </button>
    </nav>
    <p class="settings-tab-hint">{{ activeTabHint }}</p>

      <div v-show="tab === 'security'" class="settings-section-body">
          <!-- 访问安全 -->
          <section class="card">
          <div class="card-header">
            <h2>访问安全</h2>
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
                总开关是「对外 API」内置插件：未启用时外部调用一律 403，密钥可以先备好再开闸；限流等参数在「插件」页调整。
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

      <div v-show="tab === 'system'" class="settings-section-body">
        <!-- 系统更新 -->
        <section class="card">
          <div class="card-header">
            <h2>系统更新</h2>
            <SkeletonBlock v-if="loading && !systemVersion" width="90px" height="21px" />
            <span v-else class="badge">{{ deploymentMode === "git" ? "源码更新" : systemVersion?.update_supported ? "Release 自更新" : "Docker" }}</span>
            <button class="btn small ghost" type="button" :disabled="loading" title="刷新更新状态" @click="loadUpdates">
              <RefreshCw :size="14" aria-hidden="true" />
            </button>
          </div>
          <div v-if="loading && !systemVersion" class="card-body stack" style="gap: 10px" role="status" aria-label="正在加载更新状态">
            <div class="cluster" style="justify-content: space-between"><SkeletonBlock width="64px" height="20px" /><SkeletonBlock width="80px" height="20px" /></div>
            <SkeletonBlock width="85%" height="19px" />
            <SkeletonBlock height="38px" />
            <hr class="divider" style="margin: 4px 0" />
            <div class="field"><SkeletonBlock width="120px" height="20px" /><SkeletonBlock height="37px" /></div>
            <SkeletonBlock width="90px" height="30px" />
            <SkeletonBlock width="85%" height="19px" />
            <hr class="divider" style="margin: 4px 0" />
            <SkeletonBlock height="38px" />
            <SkeletonBlock width="75%" height="19px" />
          </div>
          <div v-else class="card-body stack" style="gap: 10px; font-size: 13px">
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">当前版本</span>
              <span class="cluster" style="gap: 6px">
                <span v-if="sourceBuild" class="badge warn">源码构建</span>
                <span v-if="currentVersionLabel" class="mono">{{ currentVersionLabel }}</span>
              </span>
            </div>
            <p class="muted" style="font-size: 12.5px; margin: 0">
              {{ deploymentMode === "git" ? "发现新版本时仅显示黄色提示点，确认后才会同步最新稳定 Release。" : systemVersion?.update_supported ? "Release 更新先下载并校验；安装和重启必须单独确认，默认不会自动执行。" : "控制台仅提示新版本；Docker 镜像需由部署环境手动更新。" }}
            </p>

            <template v-if="deploymentMode === 'git' && updateStatus">
              <hr class="divider" style="margin: 4px 0" />
              <div class="cluster" style="justify-content: space-between">
                <span class="muted">分支 / 提交</span>
                <span class="mono">{{ updateStatus.branch || "—" }} · {{ shortCommit }}</span>
              </div>
              <div v-if="updateStatus.dirty" class="badge warn">工作区有未提交修改，更新可能被跳过</div>
            </template>
            <button v-if="systemVersion?.update_supported" class="btn primary" type="button" :disabled="operationRunning" @click="runUpdate">
              <RefreshCw v-if="deploymentMode === 'release' && downloadReadyForLatest" :size="15" aria-hidden="true" />
              <Download v-else :size="15" aria-hidden="true" />
              {{ operationRunning ? "处理中…" : deploymentMode === "git" ? "安装最新稳定 Release" : downloadReadyForLatest ? "安装并重启" : "下载最新 Release" }}
            </button>
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
            <hr class="divider" style="margin: 4px 0" />
            <!-- Token 只影响查询版本时的 API 限额，装不装都能更新，所以放在这里
                 而不是更新面板上：更新面板要的是「点一下就更新」。 -->
            <label class="field update-token-field">
              <span class="muted">GitHub Token（可选）</span>
              <input
                v-model="githubToken"
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
            <hr class="divider" style="margin: 4px 0" />
            <button class="btn" type="button" :disabled="restarting" @click="doRestart">
              <RotateCw :size="15" aria-hidden="true" />
              {{ restarting ? "重启中，等待服务恢复…" : "重启服务" }}
            </button>
            <p class="muted" style="font-size: 12.5px; margin: 0">原地重启当前服务进程，更新拉取后需重启才生效。恢复后页面会自动刷新。</p>
          </div>
        </section>

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

      <div v-show="tab === 'appearance'" class="settings-section-body">
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
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import LoadingSkeleton from "../components/LoadingSkeleton.vue";
import SkeletonBlock from "../components/SkeletonBlock.vue";
import { Download, Eye, EyeOff, KeyRound, LogOut, RefreshCw, RotateCw } from "@lucide/vue";
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
  listOpenAPIKeys,
  createOpenAPIKey,
  revokeOpenAPIKey,
  listPlugins,
  setPluginEnabled,
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

const settingsTabs = [
  { key: "security", label: "安全", hint: "谁能打开这个控制台，以及现在有哪些设备登着。" },
  { key: "system", label: "系统", hint: "这台服务本身的版本、更新与运行信息。" },
  { key: "appearance", label: "外观", hint: "只存在你当前这个浏览器里，不会同步到其它设备，也不影响别的登录用户。" }
] as const;

const tab = ref<(typeof settingsTabs)[number]["key"]>("security");
const activeTabHint = computed(() => settingsTabs.find((item) => item.key === tab.value)?.hint ?? "");

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
const togglingPlugin = ref(false);
const openAPIPluginEnabled = computed(() => openAPIPlugin.value?.enabled === true);
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
		title: installingRelease ? "安装已下载版本并重启？" : deploymentMode.value === "release" ? "下载最新稳定版本？" : "安装最新稳定版本？",
		message: deploymentMode.value === "release"
		  ? installingRelease
			? "将备份数据库和当前版本，安装后自动重启并执行健康检查；失败时自动恢复。"
			: "只下载、校验并暂存完整 Release 包，不会安装或重启服务。"
      : "确认后才会同步到最新稳定 Release。更新完成前请勿关闭服务。",
		confirmLabel: installingRelease ? "安装并重启" : deploymentMode.value === "release" ? "下载更新" : "确认更新"
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
			? "已开始安装，服务即将重启并探活"
			: result.downloaded ? "更新已下载并通过校验，等待安装" : "已是最新，无需更新"
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
  void loadUpdates();
  void loadGitHubTokenStatus();
  void loadAuthStatus().then(() => loadSessions());
  void loadApiKeys();
  void loadOpenAPIPlugin();
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
}

.update-token-actions {
  gap: 8px;
}

/* 只有三档，铺满整行反而显得空；靠左按内容宽度排。 */
.settings-tabs {
  display: inline-flex;
  max-width: 100%;
}

.settings-tabs .editor-tab {
  flex: 0 0 auto;
}

/* 一句话说明跟着当前 tab 变：分区名进了 tab 之后，「这一档管什么」得有地方说。 */
.settings-tab-hint {
  margin: 10px 0 14px;
  font-size: 12.5px;
  color: var(--muted);
}

/* auto-fit 让只有一张卡的分区（外观）自己占满整行，右边不留空位；
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
