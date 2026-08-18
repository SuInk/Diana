<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>设置</h1>
        <p>界面主题、访问安全与系统更新</p>
      </div>
    </header>

    <div class="settings-grid">
      <div class="settings-grid-main">
        <!-- 访问安全：半宽列里字段竖排，避免宽屏下挤在左侧留出大片空白。 -->
        <section class="card">
          <div class="card-header">
            <h2>访问安全</h2>
            <span class="badge" :class="authRequired ? 'ok' : 'warn'">{{ authRequired ? "已开启密码保护" : "未设置密码" }}</span>
          </div>
          <div class="card-body settings-security-form">
            <p v-if="!authRequired" class="settings-security-note">
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
              <label for="sec-new">{{ authRequired ? "新密码" : "设置管理密码" }}</label>
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
              <span class="hint">至少 8 位；留空则保持当前密码不变。</span>
            </div>
            <div class="settings-security-actions">
              <button class="btn primary" type="button" :disabled="savingPassword || username.length === 0 || newPassword.length === 0" @click="saveCredentials">
                <KeyRound :size="15" aria-hidden="true" />
                {{ savingPassword ? "保存中…" : authRequired ? "更新账号与密码" : "开启密码保护" }}
              </button>
            </div>
          </div>
        </section>

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

      <div class="settings-grid-main">
      <!-- 系统更新：检查 → 下载并校验 → 升级并重启，与左上角版本弹窗同一套流程。 -->
      <section class="card">
        <div class="card-header">
          <h2>系统更新</h2>
          <span class="badge">{{ deploymentMode === "git" ? "源码更新" : releaseSelfUpdate ? "Release 自更新" : "Docker" }}</span>
        </div>
        <div class="card-body stack settings-update-body">
          <div class="settings-info-list">
            <div class="settings-info-row">
              <span class="muted">当前版本</span>
              <span class="mono">{{ systemVersion?.version_label || systemVersion?.build_version || "—" }}</span>
            </div>
            <div v-if="checkResult?.latest_version" class="settings-info-row">
              <span class="muted">最新版本</span>
              <span class="mono">{{ checkResult.latest_version }}</span>
            </div>
            <div class="settings-info-row">
              <span class="muted">更新状态</span>
              <span v-if="checking" class="muted">检查中…</span>
              <span v-else-if="checkError" class="badge err">检查失败</span>
              <span v-else-if="updateStatus?.download_ready" class="badge warn">已下载，等待安装</span>
              <span v-else-if="checkResult?.update_available" class="badge warn">发现新版本</span>
              <span v-else-if="checkResult" class="badge ok">已是最新</span>
              <span v-else class="muted">尚未检查</span>
            </div>
            <div v-if="deploymentMode === 'git' && updateStatus" class="settings-info-row">
              <span class="muted">分支 / 提交</span>
              <span class="mono">{{ updateStatus.branch || "—" }} · {{ shortCommit }}</span>
            </div>
          </div>
          <p v-if="checkError" class="settings-update-error">{{ checkError }}</p>
          <p class="muted settings-update-hint">{{ updateHint }}</p>
          <div v-if="deploymentMode === 'git' && updateStatus?.dirty" class="badge warn">工作区有未提交修改，更新可能被跳过</div>

          <div v-if="operationRunning && releaseSelfUpdate && !updateStatus?.download_ready" class="update-progress" role="progressbar" aria-label="Release 下载进度" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="updatePercent">
            <div class="update-progress-label">
              <span>{{ updatePhaseLabel }}</span>
              <strong class="mono">{{ updatePercent }}%</strong>
            </div>
            <div class="update-progress-track"><span :style="{ width: `${updatePercent}%` }"></span></div>
          </div>
          <pre v-if="updateOutput" class="mono update-output" :class="{ error: updateFailed }">{{ updateOutput }}</pre>

          <div class="settings-update-actions">
            <button class="btn small" type="button" :disabled="checking || operationRunning" @click="checkUpdate()">
              <RefreshCw :size="14" aria-hidden="true" />
              {{ checking ? "检查中…" : "检查更新" }}
            </button>
            <button v-if="canDownloadUpdate" class="btn small primary" type="button" :disabled="operationRunning" @click="downloadUpdate">
              <Download :size="14" aria-hidden="true" />
              {{ operationRunning ? "下载并校验中…" : "下载并校验" }}
            </button>
            <button v-if="releaseSelfUpdate && updateStatus?.download_ready" class="btn small primary" type="button" :disabled="operationRunning" @click="confirmInstall">
              <RefreshCw :size="14" aria-hidden="true" />
              {{ operationRunning ? "升级中…" : "升级并重启" }}
            </button>
            <button v-if="!releaseSelfUpdate && checkResult?.update_supported && checkResult.update_available" class="btn small primary" type="button" :disabled="operationRunning" @click="confirmGitUpdate">
              <Download :size="14" aria-hidden="true" />
              {{ operationRunning ? "更新中…" : "立即更新" }}
            </button>
            <button class="btn small ghost" type="button" @click="openVersionModal">
              版本详情
            </button>
          </div>
          <div v-if="loading" class="skeleton" style="height: 72px"></div>
        </div>
      </section>

      <!-- 运行状态：版本号只在「系统更新」显示一次，这里只放运行期信息。 -->
      <section class="card">
        <div class="card-header">
          <h2>运行状态</h2>
        </div>
        <div class="card-body settings-info-list">
          <div class="settings-info-row">
            <span class="muted">运行时长</span>
            <span>{{ health ? formatUptime(health.uptime_seconds) : "—" }}</span>
          </div>
          <div class="settings-info-row">
            <span class="muted">启动时间</span>
            <span class="mono">{{ health ? formatTime(health.started_at) : "—" }}</span>
          </div>
        </div>
      </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { Download, Eye, EyeOff, KeyRound, RefreshCw } from "@lucide/vue";
import {
  changeCredentials,
  checkForUpdate,
  getAuthStatus,
  getHealth,
  getSystemVersion,
  getUpdateStatus,
  installDownloadedSystemUpdate,
  downloadSystemUpdate,
  pullFromGitHub,
  type HealthResponse,
  type SystemVersion,
  type UpdateCheckResponse,
  type UpdateStatus
} from "../api";
import { askConfirm } from "../confirm";
import { accentOptions, theme } from "../theme";
import { formatTime, formatUptime } from "../format";
import { toastError, toastSuccess } from "../toast";

const updateStatus = ref<UpdateStatus | null>(null);
const systemVersion = ref<SystemVersion | null>(null);
const health = ref<HealthResponse | null>(null);
const loading = ref(false);
const updating = ref(false);
const updateFailed = ref(false);
const updateOutput = ref("");
const checkResult = ref<UpdateCheckResponse | null>(null);
const checking = ref(false);
const checkError = ref("");
const authRequired = ref(false);
const username = ref("");
const currentPassword = ref("");
const newPassword = ref("");
const showCurrentPassword = ref(false);
const showNewPassword = ref(false);
const savingPassword = ref(false);
const deploymentMode = computed(() => systemVersion.value?.deployment_mode ?? "release");
const releaseSelfUpdate = computed(() => deploymentMode.value === "release" && systemVersion.value?.update_supported === true);
const operationRunning = computed(() => updating.value || updateStatus.value?.updating === true);
const canDownloadUpdate = computed(() =>
  releaseSelfUpdate.value
  && !checking.value
  && !checkError.value
  && checkResult.value?.update_supported === true
  && checkResult.value.update_available
  && checkResult.value.checksum_available
  && !updateStatus.value?.download_ready
);
const updateHint = computed(() => {
  if (deploymentMode.value === "git") return "发现新版本后需确认才会同步最新稳定 Release。";
  if (releaseSelfUpdate.value) return "完整 Release 包下载后先校验 SHA-256；安装时才备份、切换版本并重启，健康检查失败会自动恢复。";
  return "控制台仅提示新版本；Docker 镜像需由部署环境手动更新。";
});
let updateStatusPollTimer: number | undefined;

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

const shortCommit = computed(() => {
  const commit = updateStatus.value?.head_commit;
  return commit ? commit.slice(0, 10) : "—";
});

async function loadUpdates(): Promise<void> {
  loading.value = true;
  try {
    const [versionResult, statusResult] = await Promise.all([
      getSystemVersion(),
      getUpdateStatus().catch(() => null)
    ]);
    systemVersion.value = versionResult;
    updateStatus.value = versionResult.update_supported ? statusResult : null;
    await checkUpdate(false);
  } catch {
    updateStatus.value = null;
  } finally {
    loading.value = false;
  }
}

async function checkUpdate(notify = true): Promise<void> {
  checking.value = true;
  checkError.value = "";
  try {
    checkResult.value = await checkForUpdate();
    updateStatus.value = checkResult.value.status ?? updateStatus.value;
    if (notify) {
      toastSuccess(checkResult.value.update_available ? `发现新版本 ${checkResult.value.latest_version || ""}`.trim() : "已是最新版本");
    }
  } catch (error) {
    checkResult.value = null;
    checkError.value = error instanceof Error ? error.message : "检查更新失败";
    if (notify) toastError(checkError.value);
  } finally {
    checking.value = false;
  }
}

async function downloadUpdate(): Promise<void> {
  if (operationRunning.value) return;
  updating.value = true;
  updateFailed.value = false;
  updateOutput.value = "";
  const progressTimer = window.setInterval(() => {
    void getUpdateStatus().then((status) => { updateStatus.value = status; }).catch(() => undefined);
  }, 500);
  try {
    const result = await downloadSystemUpdate();
    updateStatus.value = result.status;
    updateOutput.value = result.output ?? "";
    toastSuccess(result.downloaded ? "更新已下载并通过校验，等待安装" : "已是最新稳定版本");
    if (checkResult.value && !result.downloaded) checkResult.value.update_available = false;
  } catch (error) {
    const message = error instanceof Error ? error.message : "下载更新失败";
    updateFailed.value = true;
    updateOutput.value = message;
    toastError(message);
  } finally {
    window.clearInterval(progressTimer);
    updating.value = false;
    await getUpdateStatus().then((status) => { updateStatus.value = status; }).catch(() => undefined);
  }
}

async function confirmInstall(): Promise<void> {
  const target = updateStatus.value?.downloaded_version || "已下载版本";
  const confirmed = await askConfirm({
    title: `安装 ${target} 并重启？`,
    message: "安装时会备份当前版本和数据库，切换后自动重启并执行健康检查；失败时自动恢复。",
    confirmLabel: "安装并重启"
  });
  if (!confirmed) return;
  updating.value = true;
  updateFailed.value = false;
  updateOutput.value = "";
  try {
    const result = await installDownloadedSystemUpdate();
    updateStatus.value = result.status;
    toastSuccess("已开始安装并重启");
  } catch (error) {
    const message = error instanceof Error ? error.message : "安装更新失败";
    updateFailed.value = true;
    updateOutput.value = message;
    toastError(message);
  } finally {
    updating.value = false;
  }
}

async function confirmGitUpdate(): Promise<void> {
  const target = checkResult.value?.latest_version || "最新稳定版本";
  const confirmed = await askConfirm({
    title: `更新到 ${target}？`,
    message: "确认后才会同步到最新稳定 Release。更新完成前请勿关闭服务。",
    confirmLabel: "确认更新"
  });
  if (!confirmed) return;
  updating.value = true;
  updateFailed.value = false;
  updateOutput.value = "";
  try {
    const result = await pullFromGitHub();
    updateStatus.value = result.status;
    updateOutput.value = result.output ?? "";
    toastSuccess(result.updated ? `已更新到 ${target}，重启服务后生效` : "已是最新，无需更新");
    if (checkResult.value) checkResult.value.update_available = false;
  } catch (error) {
    const message = error instanceof Error ? error.message : "更新失败";
    updateFailed.value = true;
    updateOutput.value = message;
    toastError(message);
  } finally {
    updating.value = false;
  }
}

function openVersionModal(): void {
  window.dispatchEvent(new CustomEvent("diana:open-version"));
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
.settings-security-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.settings-security-note {
  margin: 0;
  padding: 9px 11px;
  border: 1px solid color-mix(in srgb, var(--warn) 35%, var(--border));
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--warn) 10%, transparent);
  color: var(--text-secondary);
  font-size: 12.5px;
  line-height: 1.5;
}

.settings-security-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 2px;
}

.settings-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}

.settings-grid-main {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-width: 0;
}

.settings-update-body {
  gap: 12px;
  font-size: 13px;
}

/* 键值信息用细分隔线分行，比等距堆叠更容易横向对读。 */
.settings-info-list {
  display: flex;
  flex-direction: column;
  font-size: 13px;
}

.settings-info-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 34px;
  padding: 4px 0;
  border-bottom: 1px solid var(--border);
}

.settings-info-row:last-child {
  border-bottom: 0;
}

.settings-info-list .settings-info-row:first-child {
  padding-top: 0;
}

.settings-update-error {
  margin: 0;
  color: var(--err);
  font-size: 12.5px;
}

.settings-update-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.settings-update-body .divider {
  margin: 4px 0;
}

.update-progress { display: grid; gap: 7px; }
.update-progress-label { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 12px; color: var(--muted); }
.update-progress-track { height: 7px; overflow: hidden; background: var(--surface-muted); border: 1px solid var(--border); border-radius: 4px; }
.update-progress-track span { display: block; height: 100%; min-width: 2px; background: var(--accent); transition: width 180ms ease; }
.update-output { margin: 0; padding: 10px; font-size: 11.5px; white-space: pre-wrap; color: var(--muted); border: 1px solid var(--border); background: var(--surface-muted); }
.update-output.error { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 45%, var(--border)); }

@media (max-width: 960px) {
  .settings-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
