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
          <span v-else-if="checkError" class="badge err">检查失败</span>
          <span v-else-if="status?.download_ready" class="badge warn">已下载，等待安装</span>
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
		<p v-if="checkError" style="margin: 0; color: var(--err); font-size: 12.5px">{{ checkError }}</p>
      </div>
      <div v-if="operationRunning && releaseSelfUpdate && !status?.download_ready" class="release-progress" role="progressbar" aria-label="Release 下载进度" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="downloadPercent">
        <div class="release-progress-label"><span>{{ downloadPhaseLabel }}</span><strong class="mono">{{ downloadPercent }}%</strong></div>
        <div class="release-progress-track"><span :style="{ width: `${downloadPercent}%` }"></span></div>
      </div>
      <pre v-if="operationError" class="operation-error mono">{{ operationError }}</pre>

      <div class="cluster" style="gap: 8px">
        <button class="btn" type="button" :disabled="checking || operationRunning" @click="check()">
          <RefreshCw :size="14" aria-hidden="true" />
          {{ checking ? "检查中…" : "检查更新" }}
        </button>
        <button
		  v-if="releaseSelfUpdate && checkResult?.update_supported && checkResult.update_available && !status?.download_ready"
          class="btn primary"
          type="button"
          :disabled="operationRunning"
		  @click="downloadUpdate"
        >
          <Download :size="14" aria-hidden="true" />
		  {{ operationRunning ? "下载中…" : "下载更新" }}
        </button>
		<button v-if="releaseSelfUpdate && status?.download_ready" class="btn primary" type="button" :disabled="operationRunning" @click="confirmInstall">
			<RefreshCcw :size="14" aria-hidden="true" />
			{{ operationRunning ? "安装中…" : "安装并重启" }}
		</button>
		<button v-if="!releaseSelfUpdate && checkResult?.update_supported && checkResult.update_available" class="btn primary" type="button" :disabled="operationRunning" @click="confirmUpdate">
			<Download :size="14" aria-hidden="true" />
			{{ operationRunning ? "更新中…" : "立即更新" }}
		</button>
        <button
          v-if="deploymentMode === 'git'"
          class="btn ghost"
          type="button"
          :disabled="checking || operationRunning"
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
            <button class="btn danger small" type="button" :disabled="operationRunning" @click="forceUpdate">确认强制同步</button>
            <button class="btn ghost small" type="button" :disabled="operationRunning" @click="forceConfirming = false">取消</button>
          </div>
        </div>
      </div>
      <p v-if="updatedHint" class="badge ok" style="align-self: flex-start">{{ updatedHint }}</p>
      <p v-if="releaseSelfUpdate" class="muted" style="font-size: 12.5px; margin: 0">
		完整 Release 包下载后先校验 SHA-256；安装时才备份数据库、切换版本并重启，健康检查失败会自动恢复。
      </p>
      <p v-else-if="deploymentMode === 'release'" class="muted" style="font-size: 12.5px; margin: 0">
        Docker 镜像由 OCI digest 校验并由部署环境安装。
      </p>

      <hr class="divider" style="margin: 0" />
      <section v-if="recentReleases.length" class="stack" style="gap: 8px">
        <div class="cluster" style="justify-content: space-between">
          <h3 style="margin: 0; font-size: 14px">最近版本</h3>
          <span class="muted" style="font-size: 12.5px">
            {{ deploymentMode === "git" ? "支持手动回退" : releaseSelfUpdate ? "异常时自动恢复旧版本" : "固定镜像标签后由部署环境重启" }}
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
                :disabled="operationRunning"
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
            <button class="btn danger small" type="button" :disabled="operationRunning" @click="rollback">确认回退</button>
            <button class="btn ghost small" type="button" :disabled="operationRunning" @click="rollbackTarget = null">取消</button>
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
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { AlertTriangle, Container, Copy, Download, History, RefreshCcw, RefreshCw } from "@lucide/vue";
import Modal from "./Modal.vue";
import {
  checkForUpdate,
	downloadSystemUpdate,
  getChangelog,
  getSystemVersion,
  getUpdateStatus,
	installDownloadedSystemUpdate,
  pullFromGitHub,
  rollbackSystem,
	type ChangelogEntry,
  type ReleaseEntry,
  type SystemVersion,
  type UpdateCheckResponse,
	type UpdateStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";
import { askConfirm } from "../confirm";

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
const checkError = ref("");
const checking = ref(false);
const updating = ref(false);
const updatedHint = ref("");
const operationError = ref("");
const forceConfirming = ref(false);
const rollbackTarget = ref<ReleaseEntry | null>(null);
let statusPollTimer: number | undefined;

const deploymentMode = computed(() => version.value?.deployment_mode ?? (version.value?.git_available ? "git" : "release"));
const releaseSelfUpdate = computed(() => deploymentMode.value === "release" && version.value?.update_supported === true);
const operationRunning = computed(() => updating.value || status.value?.updating === true);
const downloadPercent = computed(() => Math.max(0, Math.min(100, Math.round(status.value?.download_percent ?? 0))));
const downloadPhaseLabel = computed(() => status.value?.update_phase === "extracting"
	? "准备 → 下载 100% → 校验 → 解压"
	: `准备 → 下载 ${downloadPercent.value}%`);

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

async function check(notify = true): Promise<void> {
  checking.value = true;
  updatedHint.value = "";
	checkError.value = "";
  try {
    checkResult.value = await checkForUpdate();
    status.value = checkResult.value.status ?? status.value;
    emit("checked", checkResult.value.update_available);
    if (notify) {
      if (checkResult.value.update_available) {
        toastSuccess(`发现新版本 ${checkResult.value.latest_version || ""}`.trim());
      } else {
        toastSuccess("已是最新版本");
      }
    }
  } catch (error) {
		checkResult.value = null;
		checkError.value = error instanceof Error ? error.message : "检查更新失败";
		toastError(checkError.value);
  } finally {
    checking.value = false;
  }
}

async function downloadUpdate(): Promise<void> {
	if (operationRunning.value) return;
	updating.value = true;
	operationError.value = "";
	const progressTimer = window.setInterval(() => {
		void getUpdateStatus().then((value) => { status.value = value; }).catch(() => undefined);
	}, 500);
	try {
		const result = await downloadSystemUpdate();
		status.value = result.status;
		updatedHint.value = result.status.updating
			? "更新包正在下载或处理中"
			: result.downloaded ? `${result.target_commit || "新版本"} 已下载并通过校验，等待安装` : "已是最新稳定版本";
		toastSuccess(updatedHint.value);
	} catch (error) {
		operationError.value = error instanceof Error ? error.message : "下载更新失败";
		toastError(operationError.value);
	} finally {
		window.clearInterval(progressTimer);
		updating.value = false;
		await getUpdateStatus().then((value) => { status.value = value; }).catch(() => undefined);
	}
}

async function confirmInstall(): Promise<void> {
	const target = status.value?.downloaded_version || "已下载版本";
	const confirmed = await askConfirm({title: `安装 ${target} 并重启？`, message: "安装时会备份当前版本和数据库，切换后自动重启并执行健康检查；失败时自动恢复。", confirmLabel: "安装并重启"});
	if (!confirmed) return;
	updating.value = true;
	try {
		const result = await installDownloadedSystemUpdate();
		status.value = result.status;
		updatedHint.value = `正在安装 ${result.target_commit || target}，服务即将重启`;
		toastSuccess("已开始安装并重启");
	} catch (error) {
		toastError(error instanceof Error ? error.message : "安装更新失败");
	} finally {
		updating.value = false;
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

async function confirmUpdate(): Promise<void> {
  const target = checkResult.value?.latest_version || "最新稳定版本";
  const confirmed = await askConfirm({
    title: `更新到 ${target}？`,
    message: releaseSelfUpdate.value
      ? "确认后才会下载并校验完整 Release 包、备份数据库和当前版本，再切换版本并执行健康检查。"
      : "确认后才会同步到最新稳定 Release。更新完成前请勿关闭服务。",
    confirmLabel: "确认更新"
  });
  if (confirmed) {
    await update();
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
    updatedHint.value = `已回退到 ${target}，重启服务后生效`;
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

onMounted(async () => {
  await load();
  await check(false);
	statusPollTimer = window.setInterval(() => {
		if (!operationRunning.value || !releaseSelfUpdate.value) return;
		void getUpdateStatus().then((value) => { status.value = value; }).catch(() => undefined);
	}, 1000);
});

onBeforeUnmount(() => {
	if (statusPollTimer !== undefined) window.clearInterval(statusPollTimer);
});
</script>

<style scoped>
.release-progress { display: grid; gap: 7px; }
.release-progress-label { display: flex; justify-content: space-between; gap: 12px; color: var(--muted); font-size: 12px; }
.release-progress-track { height: 7px; overflow: hidden; border: 1px solid var(--border); border-radius: 4px; background: var(--surface-2); }
.release-progress-track span { display: block; height: 100%; min-width: 2px; background: var(--accent); transition: width 180ms ease; }
.operation-error { margin: 0; padding: 10px; white-space: pre-wrap; color: var(--err); border: 1px solid var(--err); background: var(--err-soft); font-size: 11.5px; }

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
