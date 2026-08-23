<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- ToastHost 放在分支外：登录页也会 toast（验证码发送失败、配对过期等），
       挂在 app-shell 里的话锁定状态下根本没挂载，那些提示会全部石沉大海。 -->
  <ToastHost />
  <LoginView v-if="!booting && locked" @success="onLoginSuccess" />
  <div v-else-if="!booting" class="app-shell">
    <transition name="none">
      <div v-if="drawerOpen" class="drawer-backdrop" @click="drawerOpen = false" />
    </transition>

    <aside
      id="app-sidebar"
      class="app-sidebar"
      :class="{ open: drawerOpen, collapsed: collapsed && !narrowSidebar }"
      aria-label="主导航"
    >
      <div class="sidebar-head">
        <div class="brand">
          <span class="brand-mark">
            <BotMessageSquare :size="19" aria-hidden="true" />
          </span>
          <span class="brand-name">
            <strong>Diana</strong>
            <button v-if="versionLabel" class="brand-version" type="button" title="查看版本与更新" @click="versionOpen = true">
              {{ versionLabel }}
              <span v-if="releaseUpdateAvailable" class="version-dot" aria-label="有新版本"></span>
            </button>
          </span>
        </div>
        <button
          class="btn ghost icon-only sidebar-toggle"
          type="button"
          :aria-label="sidebarToggleLabel"
          :aria-expanded="sidebarExpanded"
          aria-controls="app-sidebar"
          :title="sidebarToggleLabel"
          @click="toggleSidebar"
        >
          <component :is="sidebarToggleIcon" :size="18" aria-hidden="true" />
        </button>
      </div>

      <button
        v-for="item in navItems"
        :key="item.id"
        type="button"
        class="nav-item"
        :class="{ active: currentView === item.id }"
        :title="item.hint"
        @click="go(item.id)"
      >
        <component :is="navIcon(item.id)" :size="17" aria-hidden="true" />
        <span class="nav-label">{{ item.label }}</span>
      </button>

      <div class="nav-footer">
        <button class="nav-action" type="button" :title="themeToggleLabel" @click="cycleTheme">
          <component :is="themeIcon" :size="16" aria-hidden="true" />
          <span class="nav-label">{{ themeModeLabel }}</span>
        </button>
      </div>
    </aside>

    <div class="app-main">
      <header class="app-topbar">
        <!-- 窄屏或桌面侧栏完全隐藏时，开关放在顶栏。 -->
        <button
          class="btn ghost icon-only menu-button"
          type="button"
          :aria-label="sidebarToggleLabel"
          aria-controls="app-sidebar"
          :aria-expanded="sidebarExpanded"
          @click="toggleSidebar"
        >
          <PanelLeftOpen :size="18" aria-hidden="true" />
        </button>
        <span class="topbar-title">{{ viewTitle }}</span>
        <span class="topbar-spacer" />
        <span v-if="botSummary" class="badge" :class="botSummary.kind" :title="botSummary.hint">
          <span class="status-dot" :class="{ pulse: botSummary.kind === 'ok' }" aria-hidden="true" />
          {{ botSummary.label }}
        </span>
        <span v-if="demoMode" class="badge warn demo-mode-badge">演示模式 · 模拟数据</span>
        <span class="topbar-stream" :title="stream.connected ? '事件实时推送中' : '实时通道已断开，页面数据可能不是最新'">
          <span class="status-dot" :class="stream.connected ? 'text-ok' : 'text-err'" aria-hidden="true" />
          <span class="topbar-stream-text">{{ stream.connected ? "实时连接正常" : "实时连接已断开" }}</span>
        </span>
        <span v-if="health" class="topbar-uptime mono">{{ health.version }} · 已运行 {{ formatUptime(health.uptime_seconds) }}</span>
        <div class="topbar-actions">
        <a
          class="btn ghost small icon-only topbar-repo"
          :href="repositoryURL"
          target="_blank"
          rel="noopener noreferrer"
          :title="`GitHub 仓库 ${repositoryName}`"
          :aria-label="`GitHub 仓库 ${repositoryName}`"
        >
          <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true">
            <path
              fill="currentColor"
              d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
            />
          </svg>
        </a>
        <button class="btn ghost small topbar-logout" type="button" title="退出登录" @click="doLogout">
          <LogOut :size="15" aria-hidden="true" />
          <span class="topbar-logout-text">退出登录</span>
        </button>
        </div>
      </header>

      <main class="app-content">
        <KeepAlive :max="8">
          <component :is="activeView" :key="currentView" />
        </KeepAlive>
      </main>
    </div>

    <ConfirmHost />
    <VersionModal
      v-if="versionOpen"
      @close="versionOpen = false"
      @checked="releaseUpdateAvailable = $event"
      @version-changed="systemVersion = $event"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, KeepAlive, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { Component } from "vue";
import {
  Bot,
  BotMessageSquare,
  Activity,
  BrainCircuit,
  CalendarClock,
  FileClock,
  LayoutGrid,
  MessageCircle,
  LogOut,
  PanelLeftClose,
  PanelLeftOpen,
  PlugZap,
  Moon,
  Sun,
  SunMoon,
  UserRound,
  BookMarked,
  Users,
  Wrench
} from "@lucide/vue";
import { currentView, navItems, navigate, type ViewID } from "./router";
import { startEventStream, stream } from "./stream";
import { theme } from "./theme";
import { formatUptime } from "./format";
import { checkForUpdate, getAuthStatus, getConfig, getHealth, getSystemVersion, logout, type HealthResponse, type SystemVersion } from "./api";
import ToastHost from "./components/ToastHost.vue";
import ConfirmHost from "./components/ConfirmHost.vue";
import { toastSuccess } from "./toast";
import { channelAccountUnhealthy } from "./channel-status";
import VersionModal from "./components/VersionModal.vue";
import LoginView from "./views/LoginView.vue";
import DashboardView from "./views/DashboardView.vue";
import EventsView from "./views/EventsView.vue";
import TasksView from "./views/TasksView.vue";
import SetupWizard from "./views/SetupWizard.vue";
import LLMView from "./views/LLMView.vue";
import AssistantView from "./views/AssistantView.vue";
import PluginsView from "./views/PluginsView.vue";
import GroupsView from "./views/GroupsView.vue";
import UsersView from "./views/UsersView.vue";
import GlossaryView from "./views/GlossaryView.vue";
import LogsView from "./views/LogsView.vue";
import SettingsView from "./views/SettingsView.vue";

const viewComponents: Record<ViewID, Component> = {
  dashboard: DashboardView,
  events: EventsView,
  tasks: TasksView,
  setup: SetupWizard,
  llm: LLMView,
  bot: AssistantView,
  plugins: PluginsView,
  groups: GroupsView,
  users: UsersView,
  glossary: GlossaryView,
  logs: LogsView,
  settings: SettingsView
};

const drawerOpen = ref(false);
const demoMode = import.meta.env.VITE_DEMO_MODE === "true";
const SIDEBAR_DRAWER_QUERY = "(max-width: 960px)";
const sidebarMedia = window.matchMedia(SIDEBAR_DRAWER_QUERY);
const narrowSidebar = ref(sidebarMedia.matches);

// 侧栏收起状态记在 localStorage，刷新后保持。
const COLLAPSE_KEY = "dqb-next:sidebar-collapsed";
const collapsed = ref(window.localStorage.getItem(COLLAPSE_KEY) === "1");

function toggleCollapsed(): void {
  collapsed.value = !collapsed.value;
  window.localStorage.setItem(COLLAPSE_KEY, collapsed.value ? "1" : "0");
}

const sidebarExpanded = computed(() => (narrowSidebar.value ? drawerOpen.value : !collapsed.value));
const sidebarToggleLabel = computed(() => (sidebarExpanded.value ? "收起侧栏" : "展开侧栏"));
const sidebarToggleIcon = computed<Component>(() => (sidebarExpanded.value ? PanelLeftClose : PanelLeftOpen));

function toggleSidebar(): void {
  if (narrowSidebar.value) {
    drawerOpen.value = !drawerOpen.value;
    return;
  }
  toggleCollapsed();
}

function syncSidebarMode(event: MediaQueryListEvent): void {
  narrowSidebar.value = event.matches;
  drawerOpen.value = false;
}
const locked = ref(false);
// 鉴权状态未知时两边都不渲染：抢先把主界面铺出来会让它的接口拿一串 401，
// 那些报错会以 toast 的形式留在随后切出来的登录页上。
const booting = ref(true);
const versionOpen = ref(false);
const systemVersion = ref<SystemVersion | null>(null);
// 版本号还没加载出来时返回空串，由模板隐藏入口，不显示占位文案。
const versionLabel = computed(() => {
  const raw = systemVersion.value?.version_label || systemVersion.value?.build_version || "";
  if (!raw) return "";
  const semantic = raw.match(/^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/);
  if (!semantic) return "开发版";
  return semantic[0].startsWith("v") ? semantic[0] : `v${semantic[0]}`;
});
const releaseUpdateAvailable = ref(false);
const health = ref<HealthResponse | null>(null);
// 仓库地址来自后端 health（源码部署取 git remote），Fork 后展示的是自己的仓库。
const repositoryName = computed(() => health.value?.repository || "SuInk/Diana");
const repositoryURL = computed(
  () => health.value?.repository_url || `https://github.com/${repositoryName.value}`
);

const SETUP_DISMISS_KEY = "dqb-next:setup-seen";


const viewTitles: Record<ViewID, string> = {
  dashboard: "总览",
  events: "事件明细",
  tasks: "提醒与订阅",
  setup: "配置向导",
  llm: "LLM 配置",
  bot: "机器人",
  plugins: "插件",
  groups: "群管理",
  users: "人员",
  glossary: "词典",
  logs: "日志",
  settings: "设置"
};

const viewTitle = computed(() => viewTitles[currentView.value]);
const activeView = computed(() => viewComponents[currentView.value] ?? DashboardView);

const botSummary = computed(() => {
  const status = stream.status;
  if (!status) {
    return null;
  }
  if (!status.running) {
    return { kind: "warn", label: "机器人未启动" };
  }
  const channels = status.channels ?? [status.channel];
  const unhealthy = channels.filter(channelAccountUnhealthy);
  if (unhealthy.length > 0) {
    const first = unhealthy[0];
    const message = first?.account_status_message || "账号状态异常，请在 NapCat 中检查登录状态";
    const label = first?.account_status_known && !first.account_online ? "账号离线" : "账号状态异常";
    return { kind: "err", label, hint: `${message}；WebSocket 仍已连接。` };
  }
  const connected = channels.filter((channel) => channel.connected).length;
  if (connected === 0) {
    return { kind: "err", label: "等待通道连接", hint: "请确认 NapCat 已启动，并检查反向 WebSocket 地址与 Token。" };
  }
  return { kind: connected === channels.length ? "ok" : "warn", label: `已连接 ${connected}/${channels.length}`, hint: "机器人通道与账号状态正常。" };
});

const themeModeLabels: Record<string, string> = { auto: "跟随系统", light: "浅色", dark: "深色" };

const themeModeLabel = computed(() => themeModeLabels[theme.mode] ?? "跟随系统");
const themeToggleLabel = computed(() => `外观：${themeModeLabel.value}（点击切换）`);

function cycleTheme(): void {
  theme.mode = theme.mode === "auto" ? "light" : theme.mode === "light" ? "dark" : "auto";
  const labels: Record<string, string> = { auto: "已切换：跟随系统", light: "已切换：浅色模式", dark: "已切换：深色模式" };
  toastSuccess(labels[theme.mode]);
}

// 主题按钮图标跟随当前模式：太阳=浅色、月亮=深色、日月=跟随系统。
const themeIcon = computed<Component>(() => (theme.mode === "light" ? Sun : theme.mode === "dark" ? Moon : SunMoon));

function navIcon(id: ViewID): Component {
  const icons: Partial<Record<ViewID, Component>> = {
    dashboard: LayoutGrid,
    events: Activity,
    tasks: CalendarClock,
    llm: BrainCircuit,
    bot: Bot,
    plugins: PlugZap,
    groups: Users,
    users: UserRound,
    glossary: BookMarked,
    logs: FileClock,
    settings: Wrench
  };
  return icons[id] ?? LayoutGrid;
}

function go(view: ViewID): void {
  navigate(view);
  drawerOpen.value = false;
}

// 版本号只在进程重启时会变，而 SSE 断开重连正是那个信号：升级完成、手动重启、
// 容器换镜像都会走到这里。之前版本只在 bootApp 里取一次，重启后页面上还挂着旧
// 版本号，除非手动刷新。
const UPDATE_INDICATOR_INTERVAL_MS = 30 * 60 * 1000;
let updateIndicatorTimer = 0;

async function refreshRuntimeVersion(): Promise<void> {
  const [healthResult, versionResult] = await Promise.all([
    getHealth().catch(() => null),
    getSystemVersion(true).catch(() => null)
  ]);
  if (healthResult) health.value = healthResult;
  if (versionResult) systemVersion.value = versionResult;
}

async function refreshUpdateIndicator(): Promise<void> {
  try {
    const result = await checkForUpdate();
    releaseUpdateAvailable.value = result.update_available;
  } catch {
    // 静默检查只负责黄色提示点，网络异常不打扰控制台操作。
  }
}

// bootApp 在鉴权通过（或未开启鉴权）后加载应用数据。
async function bootApp(): Promise<void> {
  startEventStream();
  const [healthResult, versionResult, config] = await Promise.all([
    getHealth().catch(() => null),
    getSystemVersion().catch(() => null),
    getConfig().catch(() => null)
  ]);
  health.value = healthResult;
  systemVersion.value = versionResult;
  window.setTimeout(() => {
    void refreshUpdateIndicator();
  }, 2500);
  // 小黄点原来只在启动后查一次：页面挂一天，期间发了新版本也不会亮。
  // 30 分钟对齐后端 updateCheckInterval 与 Release 缓存，不会额外打 GitHub。
  window.clearInterval(updateIndicatorTimer);
  updateIndicatorTimer = window.setInterval(() => {
    void refreshUpdateIndicator();
  }, UPDATE_INDICATOR_INTERVAL_MS);
  // 首次访问且 LLM 未配置时自动进入向导；之后只在总览顶部保留一条引导。
  if (config && !config.api_key_configured && !window.localStorage.getItem(SETUP_DISMISS_KEY)) {
    navigate("setup");
    window.localStorage.setItem(SETUP_DISMISS_KEY, "1");
  }
}

async function doLogout(): Promise<void> {
  try {
    await logout();
  } finally {
    // 重载最简单，能确保清掉所有视图里缓存的配置数据。
    window.location.reload();
  }
}

function onLoginSuccess(): void {
  locked.value = false;
  void bootApp();
}

// 重连即重启：连上之后把版本、运行时长和更新提示一起对齐。
watch(() => stream.connected, (connected, previous) => {
  if (connected && previous === false && !locked.value && !booting.value) {
    void refreshRuntimeVersion();
    void refreshUpdateIndicator();
  }
});

onMounted(async () => {
  sidebarMedia.addEventListener("change", syncSidebarMode);
  // 会话失效时任意接口的 401 会广播这个事件，统一切回登录界面。
  window.addEventListener("diana:unauthorized", () => {
    locked.value = true;
  });
  window.addEventListener("diana:open-version", () => {
    versionOpen.value = true;
  });
  try {
    const auth = await getAuthStatus();
    if (auth.auth_required && !auth.authenticated) {
      locked.value = true;
      booting.value = false;
      return;
    }
  } catch {
    /* 状态接口失败按未开启鉴权处理，避免把用户锁在门外 */
  }
  booting.value = false;
  await bootApp();
});

onBeforeUnmount(() => {
  sidebarMedia.removeEventListener("change", syncSidebarMode);
  window.clearInterval(updateIndicatorTimer);
});
</script>
