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
          v-if="checkResult?.update_supported && checkResult.update_available"
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
          强制同步 Release
        </button>
      </div>
      <div v-if="forceConfirming" class="force-update-confirm">
        <AlertTriangle :size="17" aria-hidden="true" />
        <div class="stack" style="gap: 8px; flex: 1">
          <strong>强制同步最新稳定 Release？</strong>
          <span class="muted" style="font-size: 12.5px">这会丢弃已跟踪文件的本地修改，并重置到最新稳定 Release tag；不会绕过 Git 对象哈希校验。</span>
          <div class="cluster" style="gap: 8px">
            <button class="btn danger small" type="button" :disabled="updating" @click="forceUpdate">确认强制同步</button>
            <button class="btn ghost small" type="button" :disabled="updating" @click="forceConfirming = false">取消</button>
          </div>
        </div>
      </div>
      <p v-if="updatedHint" class="badge ok" style="align-self: flex-start">{{ updatedHint }}</p>
      <p v-if="releaseSelfUpdate" class="muted" style="font-size: 12.5px; margin: 0">
        完整 Release 包会先校验 SHA-256，再备份数据库与当前版本；切换后自动重启并执行健康检查，失败时自动恢复。
      </p>
      <p v-else-if="deploymentMode === 'release'" class="muted" style="font-size: 12.5px; margin: 0">
        Docker 镜像由 OCI digest 校验并由部署环境安装。
      </p>

      <hr class="divider" style="margin: 0" />
      <section v-if="recentReleases.length" class="stack" style="gap: 8px">
        <div class="cluster" style="justify-content: space-between">
          <h3 style="margin: 0; font-size: 14px">最近版本</h3>
          <span class="muted" style="font-size: 12.5px">
            {{ deploymentMode === "git" ? "回退后自动暂停更新" : releaseSelfUpdate ? "异常时自动回退" : "固定镜像标签后由部署环境重启" }}
          </span>
        </div>
        <ul class="recent-version-list">
          <li v-for="release in recentReleases" :key="release.tag" class="recent-version-item">
            <div class="recent-version-meta">
              <span class="cluster" style="gap: 7px">
                <a class="mono changelog-sha" :href="release.url" target="_blank" rel="noreferrer">{{ release.tag }}</a>
                <span v-if="release.tag === currentTag" class="badge ok">当前</span>
              </span>
              <span class="muted changelog-date">{{ formatDate(release.date) }}</span>
            </div>
            <div class="cluster" style="gap: 6px">
              <a
                v-if="release.checksum_available && release.checksum_url"
                class="badge ok"
                :href="release.checksum_url"
                target="_blank"
                rel="noreferrer"
                title="查看 SHA-256 清单"
              >
                SHA-256
              </a>
              <span v-else-if="deploymentMode === 'release' && !releaseSelfUpdate" class="badge ok" title="容器镜像由 OCI digest 校验">OCI digest</span>
              <button
                v-if="deploymentMode === 'git' && isOlderRelease(release.tag)"
                class="btn danger small"
                type="button"
                :disabled="updating"
                @click="rollbackTarget = release"
              >
                <History :size="13" aria-hidden="true" />
                回退
              </button>
              <button
                v-else-if="deploymentMode === 'release' && !releaseSelfUpdate && release.tag !== currentTag"
                class="btn ghost icon-only small"
                type="button"
                :title="`复制固定镜像标签 ${release.tag}`"
                :aria-label="`复制固定镜像标签 ${release.tag}`"
                @click="copyImageTag(release.tag)"
              >
                <Copy :size="14" aria-hidden="true" />
              </button>
            </div>
          </li>
        </ul>
        <div v-if="deploymentMode === 'release' && !releaseSelfUpdate" class="release-rollback-note">
          <Container :size="16" aria-hidden="true" />
          <span>回退时将部署镜像固定为 <code>ghcr.io/suink/diana:&lt;版本&gt;</code>，并暂停 Watchtower 等自动更新器；镜像拉取会校验 OCI digest。</span>
        </div>
      </section>

      <div v-if="rollbackTarget" class="rollback-confirm">
        <AlertTriangle :size="17" aria-hidden="true" />
        <div class="stack" style="gap: 8px; flex: 1">
          <strong>回退到 {{ rollbackTarget.tag }}？</strong>
          <span class="muted" style="font-size: 12.5px">这会把已跟踪代码重置到该版本，工作区有未提交修改时服务端会拒绝执行。回退后需重启服务。</span>
          <div class="cluster" style="gap: 8px">
            <button class="btn danger small" type="button" :disabled="updating" @click="rollback">确认回退</button>
            <button class="btn ghost small" type="button" :disabled="updating" @click="rollbackTarget = null">取消</button>
          </div>
        </div>
      </div>

      <hr v-if="recentReleases.length" class="divider" style="margin: 0" />
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
import { AlertTriangle, Container, Copy, Download, History, RefreshCcw, RefreshCw } from "@lucide/vue";
import Modal from "./Modal.vue";
import {
  checkForUpdate,
  getChangelog,
  getSystemVersion,
  getUpdateStatus,
  pullFromGitHub,
  rollbackSystem,
  type ChangelogEntry,
  type ReleaseEntry,
  type SystemVersion,
  type UpdateCheckResponse,
  type UpdateStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";

const emit = defineEmits<{ close: []; checked: [available: boolean] }>();

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
const rollbackTarget = ref<ReleaseEntry | null>(null);

const deploymentMode = computed(() => version.value?.deployment_mode ?? (version.value?.git_available ? "git" : "release"));
const releaseSelfUpdate = computed(() => deploymentMode.value === "release" && version.value?.update_supported === true);

const versionLabel = computed(() => {
  const label = version.value?.version_label;
  const commit = status.value?.head_commit;
  if (deploymentMode.value === "git" && label && commit && label !== commit) {
    return `${label}（${commit}）`;
  }
  return label || commit || version.value?.build_version || "—";
});

const currentTag = computed(() => {
  const raw = version.value?.version_label || version.value?.build_version || "";
  return raw.split("+")[0].split("（")[0].trim();
});

const recentReleases = computed(() => releases.value.filter((release) => !release.prerelease).slice(0, 5));

function formatDate(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  return `${date.getMonth() + 1}-${String(date.getDate()).padStart(2, "0")}`;
}

function comparableVersion(value: string): number[] | null {
  const match = value.trim().match(/^v?(\d+)\.(\d+)\.(\d+)/);
  return match ? match.slice(1).map(Number) : null;
}

function isOlderRelease(tag: string): boolean {
  const candidate = comparableVersion(tag);
  const current = comparableVersion(currentTag.value);
  if (!candidate || !current) return false;
  for (let index = 0; index < current.length; index += 1) {
    if (candidate[index] !== current[index]) return candidate[index] < current[index];
  }
  return false;
}

async function load(): Promise<void> {
  try {
    version.value = await getSystemVersion();
  } catch {
    version.value = null;
  }
  if (deploymentMode.value === "git" || version.value?.update_supported) {
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
    emit("checked", checkResult.value.update_available);
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
    const target = checkResult.value?.latest_version || result.target_commit || result.status.head_commit;
    updatedHint.value = result.updated
      ? releaseSelfUpdate.value
        ? `已校验并暂存 ${target}，服务将自动重启并执行健康检查`
        : `已更新到 ${target}，重启服务后生效`
      : "已是最新稳定版本";
    if (checkResult.value) checkResult.value.update_available = false;
    emit("checked", false);
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
    const target = checkResult.value?.latest_version || result.status.head_commit;
    updatedHint.value = `已强制同步到 ${target}，重启服务后生效`;
    forceConfirming.value = false;
    if (checkResult.value) checkResult.value.update_available = false;
    emit("checked", false);
    toastSuccess("强制更新完成");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "强制更新失败");
  } finally {
    updating.value = false;
  }
}

async function rollback(): Promise<void> {
  if (!rollbackTarget.value) return;
  updating.value = true;
  try {
    const target = rollbackTarget.value.tag;
    const response = await rollbackSystem(target);
    status.value = response.result.status;
    updatedHint.value = `已回退到 ${target}，自动更新已暂停；重启服务后生效`;
    rollbackTarget.value = null;
    toastSuccess(`已回退到 ${target}`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "版本回退失败");
  } finally {
    updating.value = false;
  }
}

async function copyImageTag(tag: string): Promise<void> {
  const image = `ghcr.io/suink/diana:${tag}`;
  try {
    await navigator.clipboard.writeText(image);
    toastSuccess(`已复制 ${image}`);
  } catch {
    toastError("复制失败，请手动复制镜像标签");
  }
}

onMounted(load);
</script>

<style scoped>
.force-update-confirm,
.rollback-confirm {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 12px;
  border: 1px solid var(--err);
  border-radius: 6px;
  color: var(--err);
  background: var(--err-soft);
}

.recent-version-list {
  display: grid;
  gap: 6px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.recent-version-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 42px;
  padding: 7px 9px;
  border: 1px solid var(--border);
  border-radius: 6px;
}

.recent-version-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex: 1;
  min-width: 0;
}

.release-rollback-note {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 9px 10px;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--muted);
  background: var(--surface-2);
  font-size: 12.5px;
  line-height: 1.55;
}

.release-rollback-note svg {
  flex: 0 0 auto;
  margin-top: 2px;
}

@media (max-width: 640px) {
  .recent-version-meta {
    align-items: flex-start;
    flex-direction: column;
    gap: 3px;
  }
}
</style>
