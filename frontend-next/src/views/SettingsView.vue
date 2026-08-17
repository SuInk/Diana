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
          <span class="badge">{{ deploymentMode === "git" ? "源码更新" : systemVersion?.update_supported ? "Release 自更新" : "Docker" }}</span>
          <button class="btn small ghost" type="button" :disabled="loading" title="刷新更新状态" @click="loadUpdates">
            <RefreshCw :size="14" aria-hidden="true" />
          </button>
        </div>
        <div class="card-body stack" style="gap: 10px; font-size: 13px">
          <div class="cluster" style="justify-content: space-between">
            <span class="muted">当前版本</span>
            <span class="mono">{{ systemVersion?.version_label || systemVersion?.build_version || "—" }}</span>
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
            <RefreshCw v-if="deploymentMode === 'release' && updateStatus?.download_ready" :size="15" aria-hidden="true" />
            <Download v-else :size="15" aria-hidden="true" />
            {{ operationRunning ? "处理中…" : deploymentMode === "git" ? "安装最新稳定 Release" : updateStatus?.download_ready ? "安装并重启" : "下载最新 Release" }}
          </button>
          <div v-if="operationRunning && deploymentMode === 'release'" class="update-progress" role="progressbar" aria-label="Release 下载进度" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="updatePercent">
            <div class="update-progress-label">
              <span>{{ updatePhaseLabel }}</span>
              <strong class="mono">{{ updatePercent }}%</strong>
            </div>
            <div class="update-progress-track"><span :style="{ width: `${updatePercent}%` }"></span></div>
          </div>
          <pre v-if="updateOutput" class="mono update-output" :class="{ error: updateFailed }">{{ updateOutput }}</pre>
          <hr class="divider" style="margin: 4px 0" />
          <button class="btn" type="button" :disabled="restarting" @click="doRestart">
            <RotateCw :size="15" aria-hidden="true" />
            {{ restarting ? "重启中，等待服务恢复…" : "重启服务" }}
          </button>
          <p class="muted" style="font-size: 12.5px; margin: 0">原地重启当前服务进程，更新拉取后需重启才生效。恢复后页面会自动刷新。</p>
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
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { Download, Eye, EyeOff, KeyRound, LogOut, RefreshCw, RotateCw } from "@lucide/vue";
import {
  changeCredentials,
  getAuthStatus,
  getHealth,
  getSystemVersion,
  getUpdateStatus,
	installDownloadedSystemUpdate,
  logout,
	downloadSystemUpdate,
  pullFromGitHub,
  restartSystem,
  type HealthResponse,
  type SystemVersion,
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
const restarting = ref(false);
const updateOutput = ref("");
const authRequired = ref(false);
const username = ref("");
const currentPassword = ref("");
const newPassword = ref("");
const showCurrentPassword = ref(false);
const showNewPassword = ref(false);
const savingPassword = ref(false);
const deploymentMode = ref<"git" | "release">("release");
const operationRunning = computed(() => updating.value || updateStatus.value?.updating === true);
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
    const [versionResult, statusResult] = await Promise.all([
      getSystemVersion(),
      getUpdateStatus().catch(() => null)
    ]);
    systemVersion.value = versionResult;
    deploymentMode.value = versionResult.deployment_mode;
    updateStatus.value = versionResult.update_supported ? statusResult : null;
  } catch {
    updateStatus.value = null;
  } finally {
    loading.value = false;
  }
}

async function runUpdate(): Promise<void> {
	if (operationRunning.value) return;
	const installingRelease = deploymentMode.value === "release" && updateStatus.value?.download_ready;
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
.update-progress { display: grid; gap: 7px; }
.update-progress-label { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 12px; color: var(--muted); }
.update-progress-track { height: 7px; overflow: hidden; background: var(--surface-muted); border: 1px solid var(--border); border-radius: 4px; }
.update-progress-track span { display: block; height: 100%; min-width: 2px; background: var(--accent); transition: width 180ms ease; }
.update-output { margin: 0; padding: 10px; font-size: 11.5px; white-space: pre-wrap; color: var(--muted); border: 1px solid var(--border); background: var(--surface-muted); }
.update-output.error { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 45%, var(--border)); }
</style>
