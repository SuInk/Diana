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
        <div v-if="authRequired" class="field">
          <label for="sec-current">当前密码</label>
          <input id="sec-current" v-model="currentPassword" class="input" type="password" autocomplete="current-password" />
        </div>
        <div class="field">
          <label for="sec-new">{{ authRequired ? "新密码（至少 8 位）" : "设置管理密码（至少 8 位）" }}</label>
          <input id="sec-new" v-model="newPassword" class="input" type="password" autocomplete="new-password" />
        </div>
        <div class="field wide cluster" style="gap: 8px">
          <button class="btn primary" type="button" :disabled="savingPassword || newPassword.length === 0" @click="savePassword">
            <KeyRound :size="15" aria-hidden="true" />
            {{ savingPassword ? "保存中…" : authRequired ? "修改密码" : "开启密码保护" }}
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
          <button class="btn small ghost" type="button" :disabled="loading" @click="loadStatus">
            <RefreshCw :size="14" aria-hidden="true" />
          </button>
        </div>
        <div class="card-body stack" style="gap: 10px; font-size: 13px">
          <template v-if="updateStatus">
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">当前分支</span>
              <span class="mono">{{ updateStatus.branch || "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">当前提交</span>
              <span class="mono">{{ shortCommit }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between; gap: 12px">
              <span class="muted">提交说明</span>
              <span style="text-align: right; max-width: 65%">{{ updateStatus.head_subject || "—" }}</span>
            </div>
            <div class="cluster" style="justify-content: space-between">
              <span class="muted">与远端差异</span>
              <span>
                <span v-if="(updateStatus.behind ?? 0) > 0" class="badge warn">落后 {{ updateStatus.behind }} 个提交</span>
                <span v-else class="badge ok">已是最新</span>
              </span>
            </div>
            <div v-if="updateStatus.dirty" class="badge warn">工作区有未提交修改，更新可能被跳过</div>
            <hr class="divider" style="margin: 4px 0" />
            <button class="btn primary" type="button" :disabled="updating" @click="runUpdate">
              <Download :size="15" aria-hidden="true" />
              {{ updating ? "更新中…" : "从 GitHub 拉取更新" }}
            </button>
            <pre v-if="updateOutput" class="mono" style="margin: 0; font-size: 11.5px; white-space: pre-wrap; color: var(--muted)">{{ updateOutput }}</pre>
          </template>
          <p v-else-if="!loading" class="muted">无法读取更新状态（可能不是 git 部署）。</p>
          <div v-else class="skeleton" style="height: 120px"></div>
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
          <span class="mono">{{ health?.version ?? "—" }}</span>
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
import { Download, KeyRound, LogOut, RefreshCw } from "@lucide/vue";
import {
  changePassword,
  getAuthStatus,
  getHealth,
  getUpdateStatus,
  logout,
  pullFromGitHub,
  type HealthResponse,
  type UpdateStatus
} from "../api";
import { accentOptions, theme } from "../theme";
import { formatTime, formatUptime } from "../format";
import { toastError, toastSuccess } from "../toast";

const updateStatus = ref<UpdateStatus | null>(null);
const health = ref<HealthResponse | null>(null);
const loading = ref(false);
const updating = ref(false);
const updateOutput = ref("");
const authRequired = ref(false);
const currentPassword = ref("");
const newPassword = ref("");
const savingPassword = ref(false);

async function loadAuthStatus(): Promise<void> {
  try {
    authRequired.value = (await getAuthStatus()).auth_required;
  } catch {
    /* 状态读取失败保持默认展示 */
  }
}

async function savePassword(): Promise<void> {
  savingPassword.value = true;
  try {
    await changePassword(currentPassword.value, newPassword.value);
    toastSuccess(authRequired.value ? "密码已更新" : "密码保护已开启");
    authRequired.value = true;
    currentPassword.value = "";
    newPassword.value = "";
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

async function loadStatus(): Promise<void> {
  loading.value = true;
  try {
    updateStatus.value = await getUpdateStatus();
  } catch {
    updateStatus.value = null;
  } finally {
    loading.value = false;
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
  void loadStatus();
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
