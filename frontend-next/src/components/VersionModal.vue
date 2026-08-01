<template>
  <Modal title="版本与更新" wide @close="emit('close')">
    <div class="stack" style="gap: 14px">
      <!-- 当前版本 -->
      <div class="version-summary">
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">当前版本</span>
          <span class="mono">{{ versionLabel }}</span>
        </div>
        <div v-if="status?.branch" class="cluster" style="justify-content: space-between">
          <span class="muted">分支</span>
          <span class="mono">{{ status.branch }}</span>
        </div>
        <div v-if="status?.head_subject" class="cluster" style="justify-content: space-between; gap: 12px">
          <span class="muted">提交说明</span>
          <span style="text-align: right; max-width: 70%">{{ status.head_subject }}</span>
        </div>
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">更新状态</span>
          <span v-if="checking" class="muted">检查中…</span>
          <span v-else-if="(status?.behind ?? 0) > 0" class="badge warn">落后 {{ status?.behind }} 个提交</span>
          <span v-else-if="status" class="badge ok">已是最新</span>
          <span v-else class="muted">—</span>
        </div>
      </div>

      <div class="cluster" style="gap: 8px">
        <button class="btn" type="button" :disabled="checking || updating" @click="check">
          <RefreshCw :size="14" aria-hidden="true" />
          {{ checking ? "检查中…" : "检查更新" }}
        </button>
        <button
          v-if="(status?.behind ?? 0) > 0"
          class="btn primary"
          type="button"
          :disabled="updating"
          @click="update"
        >
          <Download :size="14" aria-hidden="true" />
          {{ updating ? "更新中…" : "立即更新" }}
        </button>
      </div>
      <p v-if="updatedHint" class="badge ok" style="align-self: flex-start">{{ updatedHint }}</p>

      <!-- 自动更新 -->
      <hr class="divider" style="margin: 0" />
      <div class="stack" style="gap: 10px">
        <label class="switch">
          <input v-model="autoEnabled" type="checkbox" @change="saveAuto" />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">自动更新（后台定时拉取新版本）</span>
        </label>
        <div v-if="autoEnabled" class="cluster" style="gap: 8px; align-items: center">
          <label class="muted" for="auto-interval" style="font-size: 13px">检查间隔</label>
          <input
            id="auto-interval"
            v-model.number="autoInterval"
            class="input"
            style="width: 90px"
            inputmode="numeric"
            @change="saveAuto"
          />
          <span class="muted" style="font-size: 13px">分钟（10–1440）</span>
        </div>
        <p v-if="lastAutoRun" class="muted" style="font-size: 12.5px; margin: 0">
          上次自动更新：{{ lastAutoRun }}
        </p>
        <p class="muted" style="font-size: 12.5px; margin: 0">拉取到新代码后需重启服务生效；Docker 部署请改用镜像更新。</p>
      </div>

      <!-- 更新日志 -->
      <hr class="divider" style="margin: 0" />
      <div class="stack" style="gap: 8px">
        <div class="cluster" style="justify-content: space-between">
          <h3 style="margin: 0; font-size: 14px">更新日志</h3>
          <a
            v-if="repo"
            class="muted"
            style="font-size: 12.5px"
            :href="`https://github.com/${repo}/${kind === 'releases' ? 'releases' : 'commits'}`"
            target="_blank"
            rel="noreferrer"
          >
            在 GitHub 查看全部
          </a>
        </div>
        <p v-if="changelogError" class="muted" style="font-size: 13px; margin: 0">{{ changelogError }}</p>
        <div v-else-if="!loaded" class="skeleton" style="height: 80px"></div>

        <!-- Release 视图 -->
        <ul v-else-if="kind === 'releases'" class="changelog-list release-list">
          <li v-for="release in releases" :key="release.tag" class="release-item">
            <div class="cluster" style="justify-content: space-between; gap: 8px">
              <span class="cluster" style="gap: 8px">
                <a class="mono changelog-sha" :href="release.url" target="_blank" rel="noreferrer">{{ release.tag }}</a>
                <strong v-if="release.name" style="font-size: 13px">{{ release.name }}</strong>
                <span v-if="release.prerelease" class="badge warn">预发布</span>
              </span>
              <span class="cluster" style="gap: 8px">
                <span class="muted changelog-date">{{ formatDate(release.date) }}</span>
                <template v-if="pendingRef === release.tag">
                  <button class="btn small danger" type="button" :disabled="rollingBack" @click="confirmRollback">
                    {{ rollingBack ? "回退中…" : "确认回退" }}
                  </button>
                  <button class="btn small ghost" type="button" @click="pendingRef = ''">取消</button>
                </template>
                <button v-else class="btn small ghost" type="button" @click="pendingRef = release.tag">回退</button>
              </span>
            </div>
            <p v-if="release.notes" class="muted release-notes">{{ release.notes }}</p>
          </li>
        </ul>

        <!-- 提交记录回退视图 -->
        <template v-else>
          <p class="muted" style="font-size: 12px; margin: 0">仓库尚未发布正式 Release，暂以提交记录代替。</p>
          <ul class="changelog-list">
            <li v-for="entry in entries" :key="entry.sha" class="changelog-item">
              <a class="mono changelog-sha" :href="entry.url" target="_blank" rel="noreferrer">{{ entry.short }}</a>
              <span class="changelog-message" :class="{ current: entry.short === status?.head_commit }">
                {{ entry.message }}
                <span v-if="entry.short === status?.head_commit" class="badge ok" style="margin-left: 6px">当前</span>
              </span>
              <span class="muted changelog-date">{{ formatDate(entry.date) }}</span>
              <template v-if="entry.short !== status?.head_commit">
                <template v-if="pendingRef === entry.short">
                  <button class="btn small danger" type="button" :disabled="rollingBack" @click="confirmRollback">
                    {{ rollingBack ? "回退中…" : "确认" }}
                  </button>
                  <button class="btn small ghost" type="button" @click="pendingRef = ''">取消</button>
                </template>
                <button v-else class="btn small ghost" type="button" @click="pendingRef = entry.short">回退</button>
              </template>
            </li>
          </ul>
        </template>
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Download, RefreshCw } from "@lucide/vue";
import Modal from "./Modal.vue";
import {
  checkForUpdate,
  getChangelog,
  getSystemVersion,
  getUpdateSettings,
  getUpdateStatus,
  pullFromGitHub,
  rollbackSystem,
  saveUpdateSettings,
  type ChangelogEntry,
  type ReleaseEntry,
  type SystemVersion,
  type UpdateStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";

const emit = defineEmits<{ close: [] }>();

const version = ref<SystemVersion | null>(null);
const status = ref<UpdateStatus | null>(null);
const kind = ref<"releases" | "commits">("commits");
const entries = ref<ChangelogEntry[]>([]);
const releases = ref<ReleaseEntry[]>([]);
const loaded = ref(false);
const repo = ref("");
const changelogError = ref("");
const checking = ref(false);
const updating = ref(false);
const rollingBack = ref(false);
const pendingRef = ref("");
const updatedHint = ref("");
const autoEnabled = ref(false);
const autoInterval = ref(60);
const lastAutoRun = ref("");

const versionLabel = computed(() => {
  const label = version.value?.version_label;
  const commit = status.value?.head_commit;
  if (label && commit && label !== commit) {
    // 语义化版本加提交短号，弹窗里保留精确信息。
    return `${label}（${commit}）`;
  }
  return label || commit || version.value?.build_version || "—";
});

function formatDate(value?: string): string {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  return `${date.getMonth() + 1}-${String(date.getDate()).padStart(2, "0")}`;
}

async function load(): Promise<void> {
  try {
    version.value = await getSystemVersion();
  } catch {
    version.value = null;
  }
  try {
    status.value = await getUpdateStatus();
  } catch {
    status.value = null;
  }
  try {
    const settings = await getUpdateSettings();
    autoEnabled.value = settings.settings.auto_update_enabled;
    autoInterval.value = settings.settings.interval_minutes;
    if (settings.last_run_at) {
      const at = new Date(settings.last_run_at).toLocaleString();
      lastAutoRun.value = settings.last_error ? `${at}（失败：${settings.last_error}）` : `${at}（${settings.last_result}）`;
    }
  } catch {
    /* 自动更新不可用时保持默认展示 */
  }
  try {
    const changelog = await getChangelog();
    kind.value = changelog.kind;
    entries.value = changelog.entries ?? [];
    releases.value = changelog.releases ?? [];
    repo.value = changelog.repo;
    changelogError.value = "";
    loaded.value = true;
  } catch (error) {
    changelogError.value = error instanceof Error ? error.message : "更新日志暂不可用";
  }
}

async function confirmRollback(): Promise<void> {
  const target = pendingRef.value;
  if (!target) {
    return;
  }
  rollingBack.value = true;
  try {
    const response = await rollbackSystem(target);
    status.value = response.result.status;
    pendingRef.value = "";
    if (response.auto_update_disabled) {
      autoEnabled.value = false;
      toastSuccess(`已回退到 ${response.result.status.head_commit}，自动更新已暂停，重启服务后生效`);
    } else {
      toastSuccess(`已回退到 ${response.result.status.head_commit}，重启服务后生效`);
    }
    updatedHint.value = "";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "回退失败");
  } finally {
    rollingBack.value = false;
  }
}

async function check(): Promise<void> {
  checking.value = true;
  updatedHint.value = "";
  try {
    status.value = await checkForUpdate();
    if ((status.value.behind ?? 0) === 0) {
      toastSuccess("已是最新版本");
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "检查更新失败");
  } finally {
    checking.value = false;
  }
}

async function update(): Promise<void> {
  updating.value = true;
  try {
    const result = await pullFromGitHub();
    status.value = result.status;
    updatedHint.value = result.updated ? `已更新到 ${result.status.head_commit}，重启服务后生效` : "没有可用更新";
  } catch (error) {
    toastError(error instanceof Error ? error.message : "更新失败");
  } finally {
    updating.value = false;
  }
}

async function saveAuto(): Promise<void> {
  try {
    const saved = await saveUpdateSettings({
      auto_update_enabled: autoEnabled.value,
      interval_minutes: autoInterval.value
    });
    autoEnabled.value = saved.settings.auto_update_enabled;
    autoInterval.value = saved.settings.interval_minutes;
    toastSuccess(autoEnabled.value ? `自动更新已开启，每 ${autoInterval.value} 分钟检查` : "自动更新已关闭");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存自动更新设置失败");
  }
}

onMounted(load);
</script>
