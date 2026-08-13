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
          <button v-if="systemVersion?.update_supported" class="btn primary" type="button" :disabled="updating" @click="runUpdate">
            <RefreshCw v-if="deploymentMode === 'release' && updateStatus?.download_ready" :size="15" aria-hidden="true" />
            <Download v-else :size="15" aria-hidden="true" />
            {{ updating ? "处理中…" : deploymentMode === "git" ? "安装最新稳定 Release" : updateStatus?.download_ready ? "安装并重启" : "下载最新 Release" }}
          </button>
          <pre v-if="updateOutput" class="mono" style="margin: 0; font-size: 11.5px; white-space: pre-wrap; color: var(--muted)">{{ updateOutput }}</pre>
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
  getUpdateStatus,
	installDownloadedSystemUpdate,
  logout,
	downloadSystemUpdate,
  pullFromGitHub,
  type HealthResponse,
  type SystemVersion,
  type UpdateStatus
} from "../api";
import { accentOptions, theme } from "../theme";
import { formatTime, formatUptime } from "../format";
import { toastError, toastSuccess } from "../toast";
import { askConfirm } from "../confirm";

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
const deploymentMode = ref<"git" | "release">("release");

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
    updateStatus.value = systemVersion.value.update_supported ? await getUpdateStatus() : null;
  } catch {
    updateStatus.value = null;
  } finally {
    loading.value = false;
  }
}

async function runUpdate(): Promise<void> {
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
  updateOutput.value = "";
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
