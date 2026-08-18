<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Modal title="版本与更新" wide @close="emit('close')">
    <div class="stack" style="gap: 14px">
      <header class="version-hero">
        <div class="version-hero-main">
          <span v-if="versionLabel" class="mono version-current">{{ versionLabel }}</span>
          <template v-if="!checking && !checkError && (checkResult?.update_available || switchToRelease) && checkResult?.latest_version">
            <ArrowRight class="version-arrow" :size="16" aria-hidden="true" />
            <span class="mono version-latest">{{ checkResult.latest_version }}</span>
          </template>
          <span v-if="checking" class="version-checking"><LoaderCircle class="spin" :size="13" aria-hidden="true" />检查中…</span>
          <span v-else-if="installTracking" class="badge warn">升级并验证中</span>
          <span v-else-if="checkError" class="badge err">检查失败</span>
          <span v-else-if="status?.download_ready" class="badge warn">已下载，待安装</span>
          <span v-else-if="checkResult?.update_available" class="badge accent">发现新版本</span>
          <span v-else-if="switchToRelease" class="badge accent">可切换到正式版</span>
          <span v-else-if="checkResult" class="badge ok">已是最新</span>
          <span v-else class="muted" style="font-size: 12.5px">尚未检查</span>
          <span v-if="sourceBuild" class="badge warn">源码构建</span>
        </div>
        <div class="version-hero-meta">
          <span v-if="checkResult?.latest_published_at">
            {{ checkResult.update_available || switchToRelease ? "新版本" : "" }}发布于 {{ formatDateTime(checkResult.latest_published_at) }}
          </span>
          <span v-if="checkResult?.checked_at" :title="formatDateTime(checkResult.checked_at)">
            {{ formatRelativeTime(checkResult.checked_at) }}检查过
          </span>
          <span v-if="checkResult && deploymentMode === 'git'" class="version-hero-integrity ok">
            <ShieldCheck :size="13" aria-hidden="true" />
            Git 对象哈希校验
          </span>
          <a
            v-else-if="checkResult?.checksum_available && checkResult.checksum_url"
            class="version-hero-integrity ok"
            :href="checkResult.checksum_url"
            target="_blank"
            rel="noreferrer"
            title="查看 SHA-256 清单"
          >
            <ShieldCheck :size="13" aria-hidden="true" />
            校验通过
          </a>
          <span v-else-if="checkResult" class="version-hero-integrity warn">
            <ShieldAlert :size="13" aria-hidden="true" />
            缺少 SHA-256 清单
          </span>
        </div>
        <p v-if="checkError" class="version-hero-error">{{ checkError }}</p>
      </header>

      <div v-if="operationRunning && releaseSelfUpdate && !status?.download_ready" class="release-progress" role="progressbar" aria-label="Release 下载进度" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="downloadPercent">
        <div class="release-progress-label"><span>{{ downloadPhaseLabel }}</span><strong class="mono">{{ downloadPercent }}%</strong></div>
        <div class="release-progress-track"><span :style="{ width: `${downloadPercent}%` }"></span></div>
      </div>
      <pre v-if="operationError" class="operation-error mono">{{ operationError }}</pre>
      <p v-if="updatedHint" class="update-hint"><CheckCircle2 :size="15" aria-hidden="true" />{{ updatedHint }}</p>

      <section v-if="releaseSelfUpdate && !sourceBuild" class="update-policy">
        <label class="policy-row">
          <span class="policy-copy">
            <strong>自动下载</strong>
            <span class="muted">发现新版本后自动下载完整 Release 包，校验 SHA-256 后暂存</span>
          </span>
          <span class="switch">
            <input v-model="policy.auto_download" type="checkbox" :disabled="savingPolicy" @change="persistPolicy('download')" />
            <span class="track"></span>
          </span>
        </label>
        <label class="policy-row">
          <span class="policy-copy">
            <strong>自动安装并重启</strong>
            <span class="muted">下载完成后自动备份、切换版本并重启，健康检查失败会自动恢复；开启时会一并开启自动下载</span>
          </span>
          <span class="switch">
            <input v-model="policy.auto_install" type="checkbox" :disabled="savingPolicy" @change="persistPolicy('install')" />
            <span class="track"></span>
          </span>
        </label>
      </section>

      <div class="cluster" style="gap: 8px">
        <button
          v-if="canDownloadUpdate"
          class="btn primary"
          type="button"
          :disabled="operationRunning"
          @click="downloadUpdate()"
        >
          <Download :size="14" aria-hidden="true" />
          {{ operationRunning ? "下载并校验中…" : "下载并校验" }}
        </button>
        <button v-if="releaseSelfUpdate && status?.download_ready" class="btn primary" type="button" :disabled="operationRunning" @click="confirmInstall">
          <RefreshCcw :size="14" aria-hidden="true" />
          {{ operationRunning ? "升级中…" : "升级并重启" }}
        </button>
        <button v-if="switchToRelease && !status?.download_ready" class="btn primary" type="button" :disabled="operationRunning" @click="confirmSwitchToRelease">
          <Download :size="14" aria-hidden="true" />
          {{ operationRunning ? "下载并校验中…" : `切换到正式 ${checkResult?.latest_version || "版本"}` }}
        </button>
        <button v-if="!releaseSelfUpdate && checkResult?.update_supported && checkResult.update_available" class="btn primary" type="button" :disabled="operationRunning" @click="confirmUpdate">
          <Download :size="14" aria-hidden="true" />
          {{ operationRunning ? "更新中…" : "立即更新" }}
        </button>
        <button class="btn" type="button" :disabled="checking || operationRunning" @click="check()">
          <LoaderCircle v-if="checking" class="spin" :size="14" aria-hidden="true" />
          <RefreshCw v-else :size="14" aria-hidden="true" />
          {{ checking ? "检查中…" : "检查更新" }}
        </button>
        <button
          v-if="deploymentMode === 'git'"
          class="btn ghost"
          type="button"
          :disabled="checking || operationRunning"
          @click="confirmForceSync"
        >
          <RefreshCcw :size="14" aria-hidden="true" />
          强制同步 Release
        </button>
      </div>
      <p v-if="sourceBuild" class="muted" style="font-size: 12.5px; margin: 0">
        当前二进制由源码构建，没有注入正式版本号，因此不会提示更新，也不会自动下载或安装。切换到正式版会下载完整 Release 包并校验 SHA-256，再备份数据库、替换当前二进制并重启。
      </p>
      <p v-else-if="deploymentMode === 'release' && !releaseSelfUpdate" class="muted" style="font-size: 12.5px; margin: 0">
        Docker 镜像由 OCI digest 校验并由部署环境安装。
      </p>

      <hr class="divider" style="margin: 0" />
      <section class="stack" style="gap: 8px">
        <div class="cluster" style="justify-content: space-between">
          <h3 style="margin: 0; font-size: 14px">版本历史</h3>
          <span class="cluster" style="gap: 12px">
            <span v-if="loaded && kind === 'releases' && releases.length" class="muted" style="font-size: 12.5px">
              {{ deploymentMode === "git" || releaseSelfUpdate ? "可回退最近 5 个稳定版本" : "固定镜像标签后由部署环境重启" }}
            </span>
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
          </span>
        </div>
        <p v-if="changelogError" class="muted" style="font-size: 13px; margin: 0">{{ changelogError }}</p>
        <div v-else-if="!loaded" class="skeleton" style="height: 80px"></div>

        <template v-else-if="kind === 'releases'">
          <p v-if="!releases.length" class="muted" style="font-size: 12.5px; margin: 0">暂无 Release 记录。</p>
          <ul v-else class="changelog-list release-list version-history-list" :class="{ 'with-rollback': deploymentMode === 'git' || releaseSelfUpdate }">
            <li v-for="release in releases" :key="release.tag" class="release-item">
              <div class="release-row">
                <span class="cluster" style="gap: 7px">
                  <a class="mono changelog-sha" :href="release.url" target="_blank" rel="noreferrer">{{ release.tag }}</a>
                  <span v-if="release.tag === currentTag" class="badge ok">当前</span>
                  <span v-if="release.prerelease" class="badge warn">预发布</span>
                </span>
                <span class="release-side">
                  <span class="muted changelog-date">{{ formatDateTime(release.date) }}</span>
                  <span class="release-actions">
                    <button
                      v-if="canRollbackTo(release)"
                      class="btn ghost small rollback-btn"
                      type="button"
                      :disabled="operationRunning"
                      @click="confirmRollback(release)"
                    >
                      <History :size="13" aria-hidden="true" />
                      回退
                    </button>
                    <button
                      v-else-if="deploymentMode === 'release' && !releaseSelfUpdate && !release.prerelease && release.tag !== currentTag"
                      class="btn ghost icon-only small"
                      type="button"
                      :title="`复制固定镜像标签 ${release.tag}`"
                      :aria-label="`复制固定镜像标签 ${release.tag}`"
                      @click="copyImageTag(release.tag)"
                    >
                      <Copy :size="14" aria-hidden="true" />
                    </button>
                    <a
                      v-if="release.checksum_available && release.checksum_url"
                      class="integrity-link"
                      :href="release.checksum_url"
                      target="_blank"
                      rel="noreferrer"
                      :title="`查看 ${release.tag} 的 SHA-256 清单`"
                      :aria-label="`查看 ${release.tag} 的 SHA-256 清单`"
                    >
                      <ShieldCheck :size="15" aria-hidden="true" />
                    </a>
                  </span>
                </span>
              </div>
              <p v-if="release.notes" class="muted release-notes">{{ release.notes }}</p>
            </li>
          </ul>
          <div v-if="releases.length && deploymentMode === 'release' && !releaseSelfUpdate" class="release-rollback-note">
            <Container :size="16" aria-hidden="true" />
            <span>回退时将部署镜像固定为 <code>ghcr.io/suink/diana:&lt;版本&gt;</code>，并暂停 Watchtower 等自动更新器；镜像拉取会校验 OCI digest。WebUI 不能直接重建宿主机容器。</span>
          </div>
        </template>

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
      </section>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ArrowRight, CheckCircle2, Container, Copy, Download, History, LoaderCircle, RefreshCcw, RefreshCw, ShieldAlert, ShieldCheck } from "@lucide/vue";
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
  saveUpdatePolicy,
  type ChangelogEntry,
  type ReleaseEntry,
  type SystemVersion,
  type UpdateCheckResponse,
  type UpdatePolicy,
  type UpdateStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";
import { askConfirm } from "../confirm";

const emit = defineEmits<{ close: []; checked: [available: boolean]; versionChanged: [version: SystemVersion] }>();

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
const savingPolicy = ref(false);
const policy = ref<UpdatePolicy>({ auto_download: true, auto_install: false });
const updatedHint = ref("");
const operationError = ref("");
let statusPollTimer: number | undefined;
const installTracking = ref(false);
let installTarget = "";
let installStartedAt = 0;

const deploymentMode = computed(() => version.value?.deployment_mode ?? (version.value?.git_available ? "git" : "release"));
const releaseSelfUpdate = computed(() => deploymentMode.value === "release" && version.value?.update_supported === true);
const operationRunning = computed(() => updating.value || installTracking.value || status.value?.updating === true);
const sourceBuild = computed(() => deploymentMode.value === "release"
  && (checkResult.value?.build_type ?? version.value?.build_type) === "source");
const switchToRelease = computed(() => sourceBuild.value
  && !checking.value
  && !checkError.value
  && checkResult.value?.switch_to_release_available === true);
const canDownloadUpdate = computed(() => releaseSelfUpdate.value
  && !sourceBuild.value
  && !checking.value
  && !checkError.value
  && checkResult.value?.update_supported === true
  && checkResult.value.update_available
  && checkResult.value.checksum_available
  && !status.value?.download_ready);
const downloadPercent = computed(() => Math.max(0, Math.min(100, Math.round(status.value?.download_percent ?? 0))));
const downloadPhaseLabel = computed(() => status.value?.update_phase === "extracting"
  ? "下载完成，正在校验并解压"
  : "正在下载更新包");

// 版本号还没加载出来时返回空串，模板不渲染占位符。
const versionLabel = computed(() => {
  const label = version.value?.version_label;
  const commit = status.value?.head_commit;
  if (deploymentMode.value === "git" && label && commit && label !== commit) {
    return `${label}（${commit}）`;
  }
  return label || commit || version.value?.build_version || "";
});

const currentTag = computed(() => {
  const raw = version.value?.version_label || version.value?.build_version || "";
  return raw.split("+")[0].split("（")[0].trim();
});

// 与服务端回退白名单保持一致：比当前版本旧的最近 5 个稳定 Release。
const rollbackTags = computed(() => {
  const tags = new Set<string>();
  for (const release of releases.value) {
    if (release.prerelease || !release.tag) continue;
    if (!isOlderRelease(release.tag)) continue;
    tags.add(release.tag);
    if (tags.size === 5) break;
  }
  return tags;
});

function canRollbackTo(release: ReleaseEntry): boolean {
  if (deploymentMode.value !== "git" && !releaseSelfUpdate.value) return false;
  return rollbackTags.value.has(release.tag);
}

function formatDate(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  return `${date.getMonth() + 1}-${String(date.getDate()).padStart(2, "0")}`;
}

function formatDateTime(value?: string): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(date);
}

function formatRelativeTime(value?: string): string {
  if (!value) return "";
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return "";
  const diff = Date.now() - time;
  if (diff < 60_000) return "刚刚";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return `${Math.floor(diff / 86_400_000)} 天前`;
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
  const [statusResult, changelogResult] = await Promise.allSettled([
    deploymentMode.value === "git" || version.value?.update_supported ? getUpdateStatus() : Promise.resolve(null),
    getChangelog()
  ]);
  if (statusResult.status === "fulfilled") {
    status.value = statusResult.value;
    if (status.value) applyPersistedUpdateResult(status.value);
  } else {
    status.value = null;
  }
  if (changelogResult.status === "fulfilled") {
    const changelog = changelogResult.value;
    kind.value = changelog.kind;
    entries.value = changelog.entries ?? [];
    releases.value = changelog.releases ?? [];
    repo.value = changelog.repo;
    changelogError.value = "";
    loaded.value = true;
  } else {
    changelogError.value = changelogResult.reason instanceof Error ? changelogResult.reason.message : "更新日志暂不可用";
  }
}

async function check(notify = true): Promise<void> {
  checking.value = true;
  updatedHint.value = "";
  checkError.value = "";
  try {
    checkResult.value = await checkForUpdate();
    status.value = checkResult.value.status ?? status.value;
    policy.value = checkResult.value.policy ?? policy.value;
    emit("checked", checkResult.value.update_available);
    if (notify) {
      if (checkResult.value.update_available) {
        toastSuccess(`发现新版本 ${checkResult.value.latest_version || ""}`.trim());
      } else if (switchToRelease.value) {
        toastSuccess(`当前为源码构建，可切换到正式 ${checkResult.value.latest_version || "版本"}`);
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

async function persistPolicy(changed: "download" | "install"): Promise<void> {
  if (changed === "install" && policy.value.auto_install) policy.value.auto_download = true;
  if (changed === "download" && !policy.value.auto_download) policy.value.auto_install = false;
  savingPolicy.value = true;
  try {
    policy.value = await saveUpdatePolicy(policy.value);
    toastSuccess("自动更新设置已保存");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存自动更新设置失败");
    await check(false);
  } finally {
    savingPolicy.value = false;
  }
}

async function confirmSwitchToRelease(): Promise<void> {
  const target = checkResult.value?.latest_version || "最新稳定版本";
  const confirmed = await askConfirm({
    title: `切换到正式 ${target}？`,
    message: "当前运行的是源码构建。切换会下载完整 Release 包并校验 SHA-256，安装时备份数据库和当前二进制，再重启并执行健康检查。",
    confirmLabel: "下载并校验"
  });
  if (confirmed) await downloadUpdate(true);
}

async function downloadUpdate(force = false): Promise<void> {
  if (operationRunning.value) return;
  updating.value = true;
  operationError.value = "";
  const progressTimer = window.setInterval(() => {
    void getUpdateStatus().then((value) => { status.value = value; }).catch(() => undefined);
  }, 500);
  try {
    const result = await downloadSystemUpdate(force);
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
  operationError.value = "";
  try {
    const result = await installDownloadedSystemUpdate();
    status.value = result.status;
    installTarget = result.target_commit || target;
    installStartedAt = Date.now();
    installTracking.value = true;
    updatedHint.value = `正在安装 ${result.target_commit || target}，服务即将重启`;
    toastSuccess("已开始安装并重启");
  } catch (error) {
    operationError.value = error instanceof Error ? error.message : "安装更新失败";
    toastError(operationError.value);
  } finally {
    updating.value = false;
  }
}

function applyPersistedUpdateResult(value: UpdateStatus): void {
  const target = value.last_update_version || installTarget || "目标版本";
  if (value.last_update_status === "healthy") {
    updatedHint.value = `${target} 已升级成功并通过健康检查`;
    operationError.value = "";
    return;
  }
  if (value.last_update_status === "rolled_back") {
    operationError.value = `升级 ${target} 失败，已自动恢复旧版本${value.last_update_error ? `：${value.last_update_error}` : ""}`;
    return;
  }
  if (value.last_update_status === "failed") {
    operationError.value = `升级 ${target} 失败${value.last_update_error ? `：${value.last_update_error}` : ""}`;
  }
}

async function pollInstallResult(): Promise<void> {
  try {
    const nextStatus = await getUpdateStatus();
    status.value = nextStatus;
    applyPersistedUpdateResult(nextStatus);
    if (nextStatus.last_update_status === "healthy") {
      version.value = await getSystemVersion();
      emit("versionChanged", version.value);
      checkResult.value = await checkForUpdate();
      status.value = checkResult.value.status ?? nextStatus;
      installTracking.value = false;
      emit("checked", checkResult.value.update_available);
      toastSuccess(`${nextStatus.last_update_version || installTarget || "新版本"} 升级成功`);
    } else if (nextStatus.last_update_status === "rolled_back" || nextStatus.last_update_status === "failed") {
      installTracking.value = false;
      version.value = await getSystemVersion().catch(() => version.value);
      toastError(operationError.value);
    }
  } catch {
    // 服务切换期间请求会短暂失败，保留升级中状态并继续等待新进程。
    if (installStartedAt > 0 && Date.now() - installStartedAt > 150_000) {
      installTracking.value = false;
      operationError.value = `升级 ${installTarget || "目标版本"} 后服务超过 150 秒仍未恢复，请检查 .diana-updates/last-update.log`;
      toastError(operationError.value);
    }
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

async function confirmForceSync(): Promise<void> {
  const confirmed = await askConfirm({
    title: "强制同步最新稳定 Release？",
    message: "这会丢弃已跟踪文件的本地修改，并重置到最新稳定 Release tag；不会绕过 Git 对象哈希校验。",
    confirmLabel: "强制同步",
    danger: true
  });
  if (confirmed) {
    await forceUpdate();
  }
}

async function forceUpdate(): Promise<void> {
  updating.value = true;
  try {
    const result = await pullFromGitHub(true);
    status.value = result.status;
    const target = checkResult.value?.latest_version || result.status.head_commit;
    updatedHint.value = `已强制同步到 ${target}，重启服务后生效`;
    if (checkResult.value) checkResult.value.update_available = false;
    emit("checked", false);
    toastSuccess("强制更新完成");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "强制更新失败");
  } finally {
    updating.value = false;
  }
}

async function confirmRollback(release: ReleaseEntry): Promise<void> {
  const confirmed = await askConfirm({
    title: `回退到 ${release.tag}？`,
    message: releaseSelfUpdate.value
      ? "会重新下载并校验目标完整 Release 包，安装后重启并执行健康检查；失败会自动恢复。"
      : "会把已跟踪代码重置到该版本，工作区有未提交修改时服务端会拒绝执行；回退后需重启服务。",
    confirmLabel: "确认回退",
    danger: true
  });
  if (confirmed) {
    await rollback(release.tag);
  }
}

async function rollback(target: string): Promise<void> {
  updating.value = true;
  try {
    const response = await rollbackSystem(target);
    status.value = response.result.status;
    if (releaseSelfUpdate.value && response.result.restart_required) {
      installTarget = response.result.target_commit || target;
      installStartedAt = Date.now();
      installTracking.value = true;
      updatedHint.value = `正在回退到 ${installTarget}，服务即将重启并执行健康检查`;
    } else {
      updatedHint.value = `已回退到 ${target}，重启服务后生效`;
    }
    toastSuccess(releaseSelfUpdate.value && response.result.restart_required ? `已开始回退到 ${target}` : `已回退到 ${target}`);
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

onMounted(() => {
  void load();
  void check(false);
  statusPollTimer = window.setInterval(() => {
    if (installTracking.value) {
      void pollInstallResult();
      return;
    }
    if (!operationRunning.value || !releaseSelfUpdate.value) return;
    void getUpdateStatus().then((value) => { status.value = value; }).catch(() => undefined);
  }, 1000);
});

onBeforeUnmount(() => {
  if (statusPollTimer !== undefined) window.clearInterval(statusPollTimer);
});
</script>

<style scoped>
.version-hero {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 13px 15px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-2);
}

.version-hero-main {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}

.version-current,
.version-latest {
  font-size: 19px;
  font-weight: 650;
  line-height: 1.2;
}

.version-latest {
  color: var(--accent);
}

.version-arrow {
  flex: 0 0 auto;
  color: var(--muted);
}

.version-checking {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--muted);
  font-size: 12.5px;
}

.version-hero-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 14px;
  color: var(--muted);
  font-size: 12.5px;
}

.version-hero-integrity {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.version-hero-integrity.ok {
  color: var(--ok);
}

.version-hero-integrity.warn {
  color: var(--warn);
}

a.version-hero-integrity {
  text-decoration: none;
}

a.version-hero-integrity:hover {
  text-decoration: underline;
}

.version-hero-error {
  margin: 0;
  color: var(--err);
  font-size: 12.5px;
}

.release-progress { display: grid; gap: 7px; }
.release-progress-label { display: flex; justify-content: space-between; gap: 12px; color: var(--muted); font-size: 12px; }
.release-progress-track { height: 7px; overflow: hidden; border: 1px solid var(--border); border-radius: 4px; background: var(--surface-2); }
.release-progress-track span { display: block; height: 100%; min-width: 2px; background: var(--accent); transition: width 180ms ease; }
.operation-error { margin: 0; padding: 10px; white-space: pre-wrap; color: var(--err); border: 1px solid var(--err); background: var(--err-soft); font-size: 11.5px; }

.update-hint {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0;
  padding: 9px 11px;
  border: 1px solid color-mix(in srgb, var(--ok) 40%, transparent);
  border-radius: 8px;
  background: var(--ok-soft);
  color: var(--ok);
  font-size: 12.5px;
  line-height: 1.55;
}

.update-hint svg {
  flex: 0 0 auto;
  margin-top: 1px;
}

.update-policy {
  display: flex;
  flex-direction: column;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-2);
}

.policy-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 11px 13px;
  cursor: pointer;
}

.policy-row + .policy-row {
  border-top: 1px solid var(--border);
}

.policy-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.policy-copy strong {
  font-size: 13.5px;
}

.policy-copy .muted {
  font-size: 12.5px;
  line-height: 1.5;
}

.version-history-list {
  max-height: 380px;
}

.version-history-list .release-item {
  gap: 2px;
}

.release-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}

.release-side {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: auto;
}

/* 固定操作列宽度，让各行日期对齐成一列。 */
.release-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  min-width: 60px;
}

.version-history-list.with-rollback .release-actions {
  min-width: 98px;
}

.integrity-link {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: var(--radius-sm);
  color: var(--muted);
}

.integrity-link:hover {
  color: var(--ok);
  background: var(--ok-soft);
}

.rollback-btn:hover:not(:disabled) {
  color: var(--err);
  background: var(--err-soft);
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
  .policy-row {
    align-items: flex-start;
  }

  .policy-row .switch {
    margin-top: 2px;
  }
}
</style>
