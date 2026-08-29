<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Modal title="版本与更新" wide @close="emit('close')">
    <div class="stack" style="gap: 14px">
      <header class="version-hero">
        <div class="version-hero-main">
          <span v-if="versionLabel" class="mono version-current">{{ versionLabel }}</span>
          <!-- 检查中不清空已知的目标版本：抹掉再显示回来只会让这一行闪一下，
               上一次的结论在新结果回来之前仍然成立。 -->
          <template v-if="!checkError && (checkResult?.update_available || switchToRelease) && checkResult?.latest_version">
            <ArrowRight class="version-arrow" :size="16" aria-hidden="true" />
            <span class="mono version-latest">{{ checkResult.latest_version }}</span>
          </template>
          <span v-if="checking" class="version-checking"><LoaderCircle class="spin" :size="13" aria-hidden="true" />检查中…</span>
          <span v-else-if="installTracking" class="badge warn">升级并验证中</span>
          <span v-else-if="checkError" class="badge err">检查失败</span>
          <span v-else-if="status?.restart_required" class="badge warn">等待重启</span>
          <span v-else-if="status?.download_ready" class="badge warn">已下载，待安装</span>
          <span v-else-if="operationRunning" class="badge warn">正在下载并校验</span>
          <span v-else-if="checkResult?.update_available" class="badge accent">发现新版本</span>
          <span v-else-if="switchToRelease" class="badge accent">可切换到正式版</span>
          <!-- 「升不了级」不能渲染成「已是最新」：两者都没有可点的按钮，但含义相反。 -->
          <span v-else-if="updateUnsupported" class="badge err">不支持自更新</span>
          <span v-else-if="checkResult" class="badge ok">已是最新</span>
          <span v-else class="muted" style="font-size: 12.5px">尚未检查</span>
          <span v-if="sourceBuild" class="badge warn">源码构建</span>
        </div>
        <p v-if="updateUnsupportedReason" class="update-unsupported-note">{{ updateUnsupportedReason }}</p>
        <div class="version-hero-meta">
          <span v-if="checkResult?.latest_published_at">
            {{ checkResult.update_available || switchToRelease ? "新版本" : "" }}发布于 {{ formatDateTime(checkResult.latest_published_at) }}
          </span>
          <span v-if="checkResult?.checked_at && !checking" :title="formatDateTime(checkResult.checked_at)">
            {{ formatRelativeTime(checkResult.checked_at) }}检查过
          </span>
          <span v-if="checkResult && deploymentMode === 'git'" class="version-hero-integrity ok">
            <ShieldCheck :size="13" aria-hidden="true" />
            Git 对象哈希校验
          </span>
          <span
            v-else-if="checkResult?.checksum_available"
            class="version-hero-integrity ok"
            title="该 Release 带 SHA-256 清单，安装流程会在解包前逐一校验"
          >
            <ShieldCheck :size="13" aria-hidden="true" />
            校验通过
          </span>
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
      <!-- 国内直连 GitHub 常常卡在几十 KB/s，这里挑一条快的下载线路。 -->
      <div v-if="releaseSelfUpdate && !sourceBuild" class="mirror-bar">
        <label class="mirror-field">
          <span>下载加速</span>
          <select v-model="mirrorMode" :disabled="savingPolicy" @change="persistPolicy('mirror')">
            <option value="auto">自动（实测挑最快的线路）</option>
            <option value="direct">直连 GitHub</option>
            <option v-for="mirror in mirrors" :key="mirror.base_url" :value="mirror.base_url">{{ mirror.name }}</option>
          </select>
        </label>
        <button class="btn ghost small" type="button" :disabled="testingMirrors" @click="runMirrorTest">
          <RefreshCw :size="14" aria-hidden="true" />
          {{ testingMirrors ? "测速中…" : "测速" }}
        </button>
        <p class="mirror-hint">测速会真的拉一段安装包下来算速率（只测握手最快的几条），握手快不代表下载快；直连够快就直接走直连。加速只用于下载安装包，校验清单始终直连，安装前都要对上 SHA-256。</p>
        <ul v-if="mirrorProbe.length" class="mirror-results">
          <li v-for="result in mirrorProbe" :key="result.name" :class="{ ok: result.ok }">
            <span class="mirror-name">{{ result.name }}</span>
            <template v-if="result.ok">
              <span v-if="result.speed_kbps" class="mono mirror-speed">{{ formatSpeed(result.speed_kbps) }}</span>
              <span class="mono mirror-latency">{{ result.latency_ms }} ms</span>
            </template>
            <span v-else class="mirror-error">{{ result.error || "不可用" }}</span>
          </li>
        </ul>
      </div>

      <!-- 开关在左，操作按钮靠右，窄屏自动换行。 -->
      <div class="update-bar">
        <template v-if="releaseSelfUpdate && !sourceBuild">
          <label class="policy-toggle" title="发现新版本后自动下载完整 Release 包，校验 SHA-256 后暂存">
            <span>自动下载</span>
            <span class="switch">
              <input v-model="policy.auto_download" type="checkbox" :disabled="savingPolicy" @change="persistPolicy('download')" />
              <span class="track"></span>
            </span>
          </label>
          <label class="policy-toggle" title="下载完成后自动备份、切换版本并重启，健康检查失败会自动恢复；开启时会一并开启自动下载">
            <span>自动安装并重启</span>
            <span class="switch">
              <input v-model="policy.auto_install" type="checkbox" :disabled="savingPolicy" @change="persistPolicy('install')" />
              <span class="track"></span>
            </span>
          </label>
        </template>

        <span class="update-bar-gap"></span>

        <button
          v-if="canDownloadUpdate"
          class="btn primary small"
          type="button"
          :disabled="operationRunning || checking"
          @click="downloadUpdate()"
        >
          <Download :size="14" aria-hidden="true" />
          {{ operationRunning ? "下载并校验中…" : "下载并校验" }}
        </button>
        <button
          v-if="releaseSelfUpdate && (status?.download_ready || installTracking || status?.restart_required)"
          class="btn primary small"
          type="button"
          :disabled="operationRunning"
          @click="confirmInstall"
        >
          <RefreshCcw :size="14" aria-hidden="true" />
          {{ installTracking ? "升级并重启中…" : operationRunning ? "升级中…" : "升级并重启" }}
        </button>
        <button v-if="switchToRelease && !status?.download_ready" class="btn primary small" type="button" :disabled="operationRunning" @click="confirmSwitchToRelease">
          <Download :size="14" aria-hidden="true" />
          {{ operationRunning ? "下载并校验中…" : `切换到正式 ${checkResult?.latest_version || "版本"}` }}
        </button>
        <button v-if="!releaseSelfUpdate && checkResult?.update_supported && checkResult.update_available" class="btn primary small" type="button" :disabled="operationRunning" @click="confirmUpdate">
          <Download :size="14" aria-hidden="true" />
          {{ operationRunning ? "更新中…" : "立即更新" }}
        </button>
        <button class="btn small" type="button" :disabled="checking || operationRunning" @click="check()">
          <LoaderCircle v-if="checking" class="spin" :size="14" aria-hidden="true" />
          <RefreshCw v-else :size="14" aria-hidden="true" />
          {{ checking ? "检查中…" : "检查更新" }}
        </button>
        <button
          v-if="deploymentMode === 'git'"
          class="btn ghost small"
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
                  </span>
                </span>
              </div>
              <template v-if="releaseNoteLines(release).length">
                <p
                  :ref="(el) => registerNoteElement(release.tag, el as HTMLElement | null)"
                  class="muted release-notes"
                  :class="{ expanded: expandedNotes.has(release.tag) }"
                >
                  <template v-for="(line, index) in releaseNoteLines(release)" :key="index">
                    <span :class="line.heading ? 'release-notes-heading' : undefined">{{ line.text }}</span>
                    <br v-if="index < releaseNoteLines(release).length - 1" />
                  </template>
                </p>
                <button
                  v-if="overflowingNotes.has(release.tag)"
                  class="release-notes-toggle"
                  type="button"
                  @click="toggleNotes(release.tag)"
                >
                  <ChevronDown :size="13" :class="{ flipped: expandedNotes.has(release.tag) }" aria-hidden="true" />
                  {{ expandedNotes.has(release.tag) ? "收起" : "展开更新说明" }}
                </button>
              </template>
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
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { ArrowRight, ChevronDown, Container, Copy, Download, History, LoaderCircle, RefreshCcw, RefreshCw, ShieldAlert, ShieldCheck } from "@lucide/vue";
import Modal from "./Modal.vue";
import {
  checkForUpdate,
  downloadSystemUpdate,
  getChangelog,
  getSystemVersion,
  getUpdateStatus,
  installDownloadedSystemUpdate,
  getUpdateMirrors,
  pullFromGitHub,
  rollbackSystem,
  saveUpdatePolicy,
  testUpdateMirrors,
  type ChangelogEntry,
  type GitHubMirror,
  type GitHubMirrorProbe,
  type ReleaseEntry,
  type SystemVersion,
  type UpdateCheckResponse,
  type UpdatePolicy,
  type UpdateStatus
} from "../api";
import { toastError, toastSuccess } from "../toast";
import { askConfirm } from "../confirm";
import { markUpdateInstalling } from "../backendState";

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
const policy = ref<UpdatePolicy>({ auto_download: true, auto_install: false, github_mirror: "auto" });
const mirrors = ref<GitHubMirror[]>([]);
const mirrorProbe = ref<GitHubMirrorProbe[]>([]);
const testingMirrors = ref(false);
const operationError = ref("");
let statusPollTimer: number | undefined;
const installTracking = ref(false);
let installTarget = "";
let installStartedAt = 0;

const deploymentMode = computed(() => version.value?.deployment_mode ?? (version.value?.git_available ? "git" : "release"));
const releaseSelfUpdate = computed(() => deploymentMode.value === "release" && version.value?.update_supported === true);
const operationRunning = computed(() => updating.value || installTracking.value || status.value?.updating === true);
// 不支持自更新时，界面必须明说原因：显示「已是最新」会让人以为自己已经升过了，
// 直到某天发现版本号停在几个月前。
const updateUnsupported = computed(() => checkResult.value
  ? checkResult.value.update_supported === false
  : version.value?.update_supported === false);
const updateUnsupportedReason = computed(() => {
  if (!updateUnsupported.value) return "";
  return checkResult.value?.update_unsupported_reason
    || version.value?.update_unsupported_reason
    || "当前部署不支持自更新，需要手动更换新版本。";
});
const sourceBuild = computed(() => deploymentMode.value === "release"
  && (checkResult.value?.build_type ?? version.value?.build_type) === "source");
const switchToRelease = computed(() => sourceBuild.value
  && !checking.value
  && !checkError.value
  && checkResult.value?.switch_to_release_available === true);
const canDownloadUpdate = computed(() => releaseSelfUpdate.value
  && !sourceBuild.value
  && !checkError.value
  && checkResult.value?.update_supported === true
  && checkResult.value.update_available
  && checkResult.value.checksum_available
  && !status.value?.download_ready
  // 安装一开始后端就把 download_ready 清掉了（包已交给 helper），但新版本还没起来，
  // update_available 仍是 true。不排除 installTracking 的话，用户刚点完「升级并重启」，
  // 按钮就当场变回「下载并校验」，等超时解锁后还能真的再下一遍。
  && !installTracking.value
  && !status.value?.restart_required);
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
  checkError.value = "";
  try {
    checkResult.value = await checkForUpdate();
    status.value = checkResult.value.status ?? status.value;
    if (status.value?.last_update_status) applyPersistedUpdateResult(status.value);
    policy.value = checkResult.value.policy ?? policy.value;
    emit("checked", checkResult.value.update_available);
    if (notify) {
      if (checkResult.value.update_available) {
        toastSuccess(`发现新版本 ${checkResult.value.latest_version || ""}`.trim());
      } else if (switchToRelease.value) {
        toastSuccess(`当前为源码构建，可切换到正式 ${checkResult.value.latest_version || "版本"}`);
      } else if (updateUnsupported.value) {
        const latest = checkResult.value.latest_version;
        toastError(latest && latest !== checkResult.value.current_version
          ? `最新版本是 ${latest}，但${updateUnsupportedReason.value}`
          : updateUnsupportedReason.value);
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

// mirrorMode 单独包一层：后端允许空值（按 auto 处理），下拉框需要一个确定的值。
const mirrorMode = computed({
  get: () => policy.value.github_mirror || "auto",
  set: (value: string) => { policy.value.github_mirror = value; }
});

async function persistPolicy(changed: "download" | "install" | "mirror"): Promise<void> {
  if (changed === "install" && policy.value.auto_install) policy.value.auto_download = true;
  if (changed === "download" && !policy.value.auto_download) policy.value.auto_install = false;
  savingPolicy.value = true;
  try {
    policy.value = await saveUpdatePolicy(policy.value);
    toastSuccess(changed === "mirror" ? "下载加速设置已保存" : "自动更新设置已保存");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存自动更新设置失败");
    await check(false);
  } finally {
    savingPolicy.value = false;
  }
}

async function loadMirrors(): Promise<void> {
  try {
    const status = await getUpdateMirrors();
    mirrors.value = status.mirrors ?? [];
    mirrorProbe.value = status.last_probe ?? [];
    if (status.mode) policy.value.github_mirror = status.mode;
  } catch {
    // 线路列表拿不到不影响更新本身，界面退回只有「自动 / 直连」两项。
    mirrors.value = [];
  }
}

// formatSpeed 把后端的 KiB/s 显示成人能读的速率。0 表示样本太小没测出速度，
// 那种情况下模板不会走到这里，只显示握手耗时。
function formatSpeed(kbps: number): string {
  if (kbps >= 1024) return `${(kbps / 1024).toFixed(1)} MB/s`;
  return `${kbps} KB/s`;
}

async function runMirrorTest(): Promise<void> {
  testingMirrors.value = true;
  try {
    const status = await testUpdateMirrors();
    mirrorProbe.value = status.last_probe ?? [];
    mirrors.value = status.mirrors ?? mirrors.value;
    const usable = mirrorProbe.value.filter((item) => item.ok).length;
    toastSuccess(usable > 0 ? `实测完成，${usable} 条线路可用` : "实测完成，暂时没有可用线路");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "线路测速失败");
  } finally {
    testingMirrors.value = false;
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
    toastSuccess(result.status.updating
      ? "更新包正在下载或处理中"
      : result.downloaded ? `${result.target_commit || "新版本"} 已下载并通过校验，等待安装` : "已是最新稳定版本");
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
    // 记一笔「正在升级」：接下来旧进程会退出，页面这边的内存标记撑不过那一下，
    // 断线期间要靠它把「正在重启」和「后端挂了」区分开。
    markUpdateInstalling();
    toastSuccess(`已开始安装 ${result.target_commit || target} 并重启`);
  } catch (error) {
    operationError.value = error instanceof Error ? error.message : "安装更新失败";
    toastError(operationError.value);
  } finally {
    updating.value = false;
  }
}

// GitHub 的 Release 正文是 Markdown。之前直接当纯文本渲染，再按像素高度硬裁，
// 结果是「## 新增功能」这种裸标记露在外面，而且经常正好裁在标题那一行——标题
// 留着，它底下的内容全被切掉，等于什么都没说。这里先把标记洗掉，再按行数截断。
type ReleaseNoteLine = { text: string; heading: boolean };

const notesCache = new Map<string, ReleaseNoteLine[]>();
const expandedNotes = ref<Set<string>>(new Set());

function releaseNoteLines(release: ReleaseEntry): ReleaseNoteLine[] {
  const cached = notesCache.get(release.tag);
  if (cached) return cached;
  const raw = (release.notes ?? "").trim();
  const lines: ReleaseNoteLine[] = [];
  for (const rawLine of raw.replace(/<!--[\s\S]*?-->/g, "").split("\n")) {
    const heading = /^\s*#{1,6}\s+/.test(rawLine);
    const text = rawLine
      .replace(/^\s*#{1,6}\s+/, "")
      .replace(/^\s*[-*+]\s+/, "· ")
      .replace(/^\s*\d+\.\s+/, "· ")
      .replace(/^\s*>\s?/, "")
      .replace(/\*\*([^*]+)\*\*/g, "$1")
      .replace(/`([^`]+)`/g, "$1")
      .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
      .trim();
    if (text) lines.push({ text, heading });
  }
  notesCache.set(release.tag, lines);
  return lines;
}

// 是否需要「展开」按钮必须按渲染后的真实高度判断，不能数逻辑行数：窄屏下一
// 条要点会折成两三行，三条逻辑行照样被截断，按行数判断就永远不给展开入口，
// 内容直接看不到了。
const noteElements = new Map<string, HTMLElement>();
const overflowingNotes = ref<Set<string>>(new Set());

function registerNoteElement(tag: string, el: HTMLElement | null): void {
  if (el) noteElements.set(tag, el);
  else noteElements.delete(tag);
}

function measureNoteOverflow(): void {
  const next = new Set<string>();
  noteElements.forEach((el, tag) => {
    // 展开状态下高度已经撑满，测不出溢出；保留原判定，免得按钮闪一下就没了。
    if (expandedNotes.value.has(tag) || el.scrollHeight - el.clientHeight > 2) next.add(tag);
  });
  overflowingNotes.value = next;
}

function toggleNotes(tag: string): void {
  const next = new Set(expandedNotes.value);
  if (!next.delete(tag)) next.add(tag);
  expandedNotes.value = next;
}

// 服务端持久化的升级结果是一条历史记录，不等于「刚刚升级成功」。成功那条没有
// 阅读价值——版本号就写在上面，升级完还挂一条绿横幅只是占地方，所以只在失败和
// 回退时留下痕迹。
function applyPersistedUpdateResult(value: UpdateStatus): void {
  const target = value.last_update_version || installTarget || "目标版本";
  if (value.last_update_status === "healthy") {
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
      // 装完必须拿真版本号：缓存里那个还是升级前的。
      version.value = await getSystemVersion(true);
      emit("versionChanged", version.value);
      checkResult.value = await checkForUpdate();
      status.value = checkResult.value.status ?? nextStatus;
      installTracking.value = false;
      emit("checked", checkResult.value.update_available);
      toastSuccess(`${nextStatus.last_update_version || installTarget || "新版本"} 升级成功`);
    } else if (nextStatus.last_update_status === "rolled_back" || nextStatus.last_update_status === "failed") {
      installTracking.value = false;
      version.value = await getSystemVersion(true).catch(() => version.value);
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
    toastSuccess(result.updated
      ? releaseSelfUpdate.value
        ? `已校验并暂存 ${target}，服务将自动重启并执行健康检查`
        : `已更新到 ${target}，重启服务后生效`
      : "已是最新稳定版本");
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
    if (checkResult.value) checkResult.value.update_available = false;
    emit("checked", false);
    toastSuccess(`已强制同步到 ${target}，重启服务后生效`);
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
  void loadMirrors();
  window.addEventListener("resize", measureNoteOverflow);
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
  window.removeEventListener("resize", measureNoteOverflow);
});

// 列表渲染完再量一次；展开/收起之后也要重量，否则收起时按钮会消失。
watch([releases, expandedNotes], () => {
  void nextTick(measureNoteOverflow);
}, { deep: false });
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

.update-unsupported-note {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--err);
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

.update-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px 14px;
}

/* 把按钮推到行尾；窄屏换行后这个占位符不占高度。 */
.update-bar-gap {
  flex: 1 1 auto;
}


.update-hint svg {
  flex: 0 0 auto;
}

.mirror-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 12px;
  margin-top: 10px;
}

.mirror-field {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-muted);
}

.mirror-field select {
  max-width: 260px;
  padding: 4px 8px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 13px;
}

.mirror-hint {
  flex-basis: 100%;
  margin: 0;
  font-size: 12px;
  color: var(--text-muted);
}

.mirror-results {
  flex-basis: 100%;
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  margin: 0;
  padding: 0;
  list-style: none;
  font-size: 12px;
}

.mirror-results li {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--border);
  color: var(--text-muted);
}

.mirror-results li.ok {
  border-color: color-mix(in srgb, var(--accent) 45%, transparent);
  color: var(--text);
}

/* 速度是这里真正要看的数字，握手耗时只是旁证，压暗一档避免抢读。 */
.mirror-speed {
  font-weight: 600;
  color: var(--accent);
}

.mirror-latency {
  color: var(--text-muted);
}

.mirror-error {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.policy-toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
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

/* 窄屏放不下 tag + 徽章 + 日期时，让日期落到下一行的左边而不是右边——
   space-between 会把它甩到最右侧，看着像另一条记录的开头。 */
@media (max-width: 560px) {
  .release-side {
    margin-left: 0;
    width: 100%;
    justify-content: space-between;
  }
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
  /* 换行之后按钮另起一行，占满宽度更好按。 */
  .update-bar-gap {
    flex-basis: 100%;
  }
}
</style>
