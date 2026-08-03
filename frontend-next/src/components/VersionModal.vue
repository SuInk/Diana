<template>
  <Modal title="版本与更新" wide @close="emit('close')">
    <div class="stack" style="gap: 14px">
      <div class="version-summary">
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">当前版本</span>
          <span class="mono">{{ versionLabel }}</span>
        </div>
        <div v-if="checkResult?.latest_version" class="cluster" style="justify-content: space-between">
          <span class="muted">最新版本</span>
          <span class="mono">{{ checkResult.latest_version }}</span>
        </div>
        <div class="cluster" style="justify-content: space-between">
          <span class="muted">更新状态</span>
          <span v-if="checking" class="muted">检查中…</span>
          <span v-else-if="checkResult?.update_available" class="badge warn">发现新版本</span>
          <span v-else-if="checkResult" class="badge ok">已是最新</span>
          <span v-else class="muted">尚未检查</span>
        </div>
        <div v-if="checkResult" class="cluster" style="justify-content: space-between">
          <span class="muted">完整性校验</span>
          <span v-if="deploymentMode === 'git'" class="badge ok">Git 对象哈希</span>
          <a
            v-else-if="checkResult.checksum_available && checkResult.checksum_url"
            class="badge ok"
            :href="checkResult.checksum_url"
            target="_blank"
            rel="noreferrer"
          >
            SHA-256 可用
          </a>
          <span v-else class="badge warn">缺少 SHA-256 清单</span>
        </div>
      </div>

      <div class="cluster" style="gap: 8px">
        <button class="btn" type="button" :disabled="checking || updating" @click="check">
          <RefreshCw :size="14" aria-hidden="true" />
          {{ checking ? "检查中…" : "检查更新" }}
        </button>
        <button
          v-if="deploymentMode === 'git' && checkResult?.update_available"
          class="btn primary"
          type="button"
          :disabled="updating"
          @click="update"
        >
          <Download :size="14" aria-hidden="true" />
          {{ updating ? "更新中…" : "立即更新" }}
        </button>
        <button
          v-if="deploymentMode === 'git'"
          class="btn ghost"
          type="button"
          :disabled="checking || updating"
          @click="forceConfirming = true"
        >
          <RefreshCcw :size="14" aria-hidden="true" />
          强制更新
        </button>
      </div>
      <div v-if="forceConfirming" class="force-update-confirm">
        <AlertTriangle :size="17" aria-hidden="true" />
        <div class="stack" style="gap: 8px; flex: 1">
          <strong>强制同步远端版本？</strong>
          <span class="muted" style="font-size: 12.5px">这会丢弃已跟踪文件的本地修改，但不会绕过 Git 对象哈希校验。</span>
          <div class="cluster" style="gap: 8px">
            <button class="btn danger small" type="button" :disabled="updating" @click="forceUpdate">确认强制同步</button>
            <button class="btn ghost small" type="button" :disabled="updating" @click="forceConfirming = false">取消</button>
          </div>
        </div>
      </div>
      <p v-if="updatedHint" class="badge ok" style="align-self: flex-start">{{ updatedHint }}</p>
      <p v-if="deploymentMode === 'release'" class="muted" style="font-size: 12.5px; margin: 0">
        Release 下载必须通过 SHA-256 校验；Docker 镜像由 OCI digest 校验并由部署环境安装。
      </p>

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

        <ul v-else-if="kind === 'releases'" class="changelog-list release-list">
          <li v-for="release in releases" :key="release.tag" class="release-item">
            <div class="cluster" style="justify-content: space-between; gap: 8px">
              <span class="cluster" style="gap: 8px">
                <a class="mono changelog-sha" :href="release.url" target="_blank" rel="noreferrer">{{ release.tag }}</a>
                <strong v-if="release.name" style="font-size: 13px">{{ release.name }}</strong>
                <span v-if="release.prerelease" class="badge warn">预发布</span>
              </span>
              <span class="muted changelog-date">{{ formatDate(release.date) }}</span>
            </div>
            <p v-if="release.notes" class="muted release-notes">{{ release.notes }}</p>
          </li>
        </ul>

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
            </li>
          </ul>
        </template>
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { AlertTriangle, Download, RefreshCcw, RefreshCw } from "@lucide/vue";
import Modal from "./Modal.vue";
import {
  checkForUpdate,
  getChangelog,
  getSystemVersion,
  getUpdateStatus,
  pullFromGitHub,
  type ChangelogEntry,
  type ReleaseEntry,
  type SystemVersion,
  type UpdateCheckResponse,
  type UpdateStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";

const emit = defineEmits<{ close: [] }>();

const version = ref<SystemVersion | null>(null);
const status = ref<UpdateStatus | null>(null);
const checkResult = ref<UpdateCheckResponse | null>(null);
const kind = ref<"releases" | "commits">("commits");
const entries = ref<ChangelogEntry[]>([]);
const releases = ref<ReleaseEntry[]>([]);
const loaded = ref(false);
const repo = ref("");
const changelogError = ref("");
const checking = ref(false);
const updating = ref(false);
const updatedHint = ref("");
const forceConfirming = ref(false);

const deploymentMode = computed(() => version.value?.deployment_mode ?? (version.value?.git_available ? "git" : "release"));

const versionLabel = computed(() => {
  const label = version.value?.version_label;
  const commit = status.value?.head_commit;
  if (deploymentMode.value === "git" && label && commit && label !== commit) {
    return `${label}（${commit}）`;
  }
  return label || commit || version.value?.build_version || "—";
});

function formatDate(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  return `${date.getMonth() + 1}-${String(date.getDate()).padStart(2, "0")}`;
}

async function load(): Promise<void> {
  try {
    version.value = await getSystemVersion();
  } catch {
    version.value = null;
  }
  if (deploymentMode.value === "git") {
    try {
      status.value = await getUpdateStatus();
    } catch {
      status.value = null;
    }
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

async function check(): Promise<void> {
  checking.value = true;
  updatedHint.value = "";
  try {
    checkResult.value = await checkForUpdate();
    status.value = checkResult.value.status ?? status.value;
    if (checkResult.value.update_available) {
      toastSuccess(`发现新版本 ${checkResult.value.latest_version || ""}`.trim());
    } else {
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
    if (checkResult.value) checkResult.value.update_available = false;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "更新失败");
  } finally {
    updating.value = false;
  }
}

async function forceUpdate(): Promise<void> {
  updating.value = true;
  try {
    const result = await pullFromGitHub(true);
    status.value = result.status;
    updatedHint.value = `已强制同步到 ${result.status.head_commit}，重启服务后生效`;
    forceConfirming.value = false;
    if (checkResult.value) checkResult.value.update_available = false;
    toastSuccess("强制更新完成");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "强制更新失败");
  } finally {
    updating.value = false;
  }
}

onMounted(load);
</script>

<style scoped>
.force-update-confirm {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 12px;
  border: 1px solid var(--err);
  border-radius: 6px;
  color: var(--err);
  background: var(--err-soft);
}
</style>
