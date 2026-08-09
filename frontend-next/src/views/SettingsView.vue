<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>设置</h1>
        <p>界面主题、访问安全与系统更新</p>
      </div>
    </header>

    <!-- 访问安全 -->
    <section class="card" style="margin-bottom: 16px">
      <div class="card-header">
        <h2>访问安全</h2>
        <span class="badge" :class="authRequired ? 'ok' : 'warn'">{{ authRequired ? "已开启密码保护" : "未设置密码" }}</span>
      </div>
      <div class="card-body form-grid">
        <p v-if="!authRequired" class="muted field wide" style="margin: 0; font-size: 13px">
          当前控制台无需登录即可访问。部署在公网或局域网前，请务必设置管理密码。
        </p>
        <div class="field">
          <label for="sec-username">管理账号</label>
          <input id="sec-username" v-model="username" class="input" placeholder="diana#账号" autocomplete="username" />
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
          <button v-if="authRequired" class="btn ghost" type="button" @click="doLogout">
            <LogOut :size="15" aria-hidden="true" />
            退出登录
          </button>
        </div>
      </div>
    </section>

    <div class="grid-2">
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

      <!-- 系统更新 -->
      <section class="card">
        <div class="card-header">
          <h2>系统更新</h2>
          <span class="badge">{{ deploymentMode === "git" ? "源码更新" : "Release / Docker" }}</span>
          <button class="btn small ghost" type="button" :disabled="loading" title="刷新更新状态" @click="loadUpdates">
            <RefreshCw :size="14" aria-hidden="true" />
          </button>
        </div>
        <div class="card-body stack" style="gap: 10px; font-size: 13px">
          <div class="cluster" style="justify-content: space-between">
            <span class="muted">当前版本</span>
            <span class="mono">{{ systemVersion?.version_label || systemVersion?.build_version || "—" }}</span>
          </div>
          <label class="switch">
            <input v-model="autoEnabled" type="checkbox" @change="saveAutoSettings" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">自动更新</span>
          </label>
          <div v-if="autoEnabled" class="field" style="max-width: 220px">
            <label for="settings-update-interval">检查间隔（分钟）</label>
            <input
              id="settings-update-interval"
              v-model.number="autoInterval"
              class="input"
              type="number"
              min="10"
              max="1440"
              inputmode="numeric"
              @change="saveAutoSettings"
            />
          </div>
          <p class="muted" style="font-size: 12.5px; margin: 0">
            {{ deploymentMode === "git" ? "后台检查 GitHub 并快进拉取更新，默认每 30 分钟执行。" : "后台检查 Release；Docker 镜像由部署环境的更新器自动安装。" }}
          </p>
          <p v-if="lastAutoRun" class="muted" style="font-size: 12.5px; margin: 0">上次检查：{{ lastAutoRun }}</p>

          <template v-if="deploymentMode === 'git' && updateStatus">
            <hr class="divider" style="margin: 4px 0" />
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">分支 / 提交</span>
              <span class="mono">{{ updateStatus.branch || "—" }} · {{ shortCommit }}</span>
            </div>
            <div v-if="updateStatus.dirty" class="badge warn">工作区有未提交修改，更新可能被跳过</div>
            <button class="btn primary" type="button" :disabled="updating" @click="runUpdate">
              <Download :size="15" aria-hidden="true" />
              {{ updating ? "更新中…" : "从 GitHub 拉取更新" }}
            </button>
            <pre v-if="updateOutput" class="mono" style="margin: 0; font-size: 11.5px; white-space: pre-wrap; color: var(--muted)">{{ updateOutput }}</pre>
          </template>
          <div v-if="loading" class="skeleton" style="height: 72px"></div>
        </div>
      </section>
    </div>

    <!-- 关于 -->
    <section class="card" style="margin-top: 16px">
      <div class="card-header">
        <h2>关于</h2>
      </div>
      <div class="card-body stack" style="gap: 8px; font-size: 13px">
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">后端版本</span>
          <span class="mono">{{ systemVersion?.version_label || systemVersion?.build_version || health?.version || "—" }}</span>
        </div>
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">运行时长</span>
          <span>{{ health ? formatUptime(health.uptime_seconds) : "—" }}</span>
        </div>
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">启动时间</span>
          <span>{{ health ? formatTime(health.started_at) : "—" }}</span>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Download, Eye, EyeOff, KeyRound, LogOut, RefreshCw } from "@lucide/vue";
import {
  changeCredentials,
  getAuthStatus,
  getHealth,
  getSystemVersion,
  getUpdateSettings,
  getUpdateStatus,
  logout,
  pullFromGitHub,
  saveUpdateSettings,
  type HealthResponse,
  type SystemVersion,
  type UpdateStatus
} from "../api";
import { accentOptions, theme } from "../theme";
import { formatTime, formatUptime } from "../format";
import { toastError, toastSuccess } from "../toast";

const updateStatus = ref<UpdateStatus | null>(null);
const systemVersion = ref<SystemVersion | null>(null);
const health = ref<HealthResponse | null>(null);
const loading = ref(false);
const updating = ref(false);
const updateOutput = ref("");
const authRequired = ref(false);
const username = ref("");
const currentPassword = ref("");
const newPassword = ref("");
const showCurrentPassword = ref(false);
const showNewPassword = ref(false);
const savingPassword = ref(false);
const autoEnabled = ref(true);
const autoInterval = ref(30);
const deploymentMode = ref<"git" | "release">("release");
const lastAutoRun = ref("");

async function loadAuthStatus(): Promise<void> {
  try {
    const status = await getAuthStatus();
    authRequired.value = status.auth_required;
    username.value = status.username || "";
  } catch {
    /* 状态读取失败保持默认展示 */
  }
}

async function saveCredentials(): Promise<void> {
  savingPassword.value = true;
  try {
    const result = await changeCredentials(currentPassword.value, username.value, newPassword.value);
    username.value = result.username;
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

async function doLogout(): Promise<void> {
  try {
    await logout();
  } finally {
    window.location.reload();
  }
}

const shortCommit = computed(() => {
  const commit = updateStatus.value?.head_commit;
  return commit ? commit.slice(0, 10) : "—";
});

async function loadUpdates(): Promise<void> {
  loading.value = true;
  try {
    systemVersion.value = await getSystemVersion();
    deploymentMode.value = systemVersion.value.deployment_mode;
    const settings = await getUpdateSettings();
    autoEnabled.value = settings.settings.auto_update_enabled;
    autoInterval.value = settings.settings.interval_minutes;
    deploymentMode.value = settings.deployment_mode;
    if (settings.last_run_at) {
      const at = new Date(settings.last_run_at).toLocaleString();
      lastAutoRun.value = settings.last_error ? `${at}（失败：${settings.last_error}）` : `${at}（${settings.last_result || "完成"}）`;
    } else {
      lastAutoRun.value = "";
    }
    updateStatus.value = deploymentMode.value === "git" ? await getUpdateStatus() : null;
  } catch {
    updateStatus.value = null;
  } finally {
    loading.value = false;
  }
}

async function saveAutoSettings(): Promise<void> {
  try {
    const saved = await saveUpdateSettings({
      auto_update_enabled: autoEnabled.value,
      interval_minutes: autoInterval.value
    });
    autoEnabled.value = saved.settings.auto_update_enabled;
    autoInterval.value = saved.settings.interval_minutes;
    deploymentMode.value = saved.deployment_mode;
    toastSuccess(autoEnabled.value ? `自动更新已开启，每 ${autoInterval.value} 分钟检查` : "自动更新已关闭");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存自动更新设置失败");
  }
}

async function runUpdate(): Promise<void> {
  updating.value = true;
  updateOutput.value = "";
  try {
    const result = await pullFromGitHub();
    updateStatus.value = result.status;
    updateOutput.value = result.output ?? "";
    toastSuccess(result.updated ? "更新完成，重启服务后生效" : "已是最新，无需更新");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "更新失败");
  } finally {
    updating.value = false;
  }
}

onMounted(() => {
  void loadUpdates();
  void loadAuthStatus();
  void getHealth()
    .then((result) => {
      health.value = result;
    })
    .catch(() => {
      health.value = null;
    });
});
</script>
