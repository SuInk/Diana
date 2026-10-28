<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- ToastHost 放在分支外：登录页也会 toast（验证码发送失败、配对过期等），
       挂在 app-shell 里的话锁定状态下根本没挂载，那些提示会全部石沉大海。 -->
  <ToastHost />
  <div v-if="booting" class="startup-loading" role="status" aria-live="polite">
    <BrandLogo class="startup-logo" />
    <span class="startup-spinner" aria-hidden="true" />
    <p>正在连接 Diana…</p>
  </div>
  <BackendDownView
    v-else-if="backendDown"
    :message="backendDownDetail"
    :retrying="backendRetrying"
    :retry-in-seconds="backendRetryDelay"
    :waiting="backendWaiting"
    :grace-left="backendGraceLeft"
    :updating="backendUpdating"
    @retry="retryBackend"
  />
  <LoginView v-else-if="!booting && locked" @success="onLoginSuccess" @unreachable="showBackendDown" />
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
          <BrandLogo class="brand-logo" />
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
      </div>

      <div v-if="scopeOptions.length > 0" class="bot-scope">
        <AppSelect
          id="app-bot-scope"
          :model-value="botScope"
          :options="scopeOptions"
          aria-label="当前机器人"
          @update:model-value="requestBotScope"
        >
          <template #footer>
            <button type="button" class="app-select-item" @click="requestBotCreate">
              <Plus :size="14" aria-hidden="true" />
              <span class="app-select-item-label"><span class="app-select-item-text">添加机器人</span></span>
            </button>
          </template>
        </AppSelect>
      </div>

      <nav class="nav-sections" aria-label="主导航">
        <div v-for="(section, index) in sections" :key="section.group?.id ?? `plain-${index}`" class="nav-section">
          <p v-if="section.group" class="nav-group-title">{{ section.group.label }}</p>
          <button
            v-for="item in section.items"
            :key="item.id"
            type="button"
            class="nav-item"
            :class="{ active: isActiveNav(item.id) }"
            :title="item.hint"
            @click="go(item.id)"
          >
            <component :is="navIcon(item.id)" :size="17" aria-hidden="true" />
            <span class="nav-label">{{ item.label }}</span>
            <span v-if="item.id === 'space' && spaceUnread" class="nav-dot" aria-label="小窝有新动静"></span>
          </button>
        </div>
      </nav>

      <div class="nav-footer">
        <button
          v-for="item in footerNavItems"
          :key="item.id"
          class="nav-action"
          :class="{ active: isActiveNav(item.id) }"
          type="button"
          :title="item.hint"
          @click="go(item.id)"
        >
          <component :is="navIcon(item.id)" :size="16" aria-hidden="true" />
          <span class="nav-label">{{ item.label }}</span>
        </button>
        <button class="nav-action" type="button" :title="themeToggleLabel" @click="cycleTheme">
          <component :is="themeIcon" :size="16" aria-hidden="true" />
          <span class="nav-label">{{ themeModeLabel }}</span>
        </button>
        <button
          v-if="versionLabel"
          class="nav-action version-action"
          type="button"
          title="查看版本与更新"
          @click="versionOpen = true"
        >
          <Tag :size="16" aria-hidden="true" />
          <span class="nav-label version-label-text">{{ versionLabel }}</span>
          <span v-if="releaseUpdateAvailable" class="version-dot" aria-label="有新版本"></span>
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
        <h1 class="topbar-title">{{ viewTitle }}</h1>
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
        <span v-if="health" class="topbar-uptime mono">已运行 {{ formatUptime(health.uptime_seconds) }}</span>
        <div class="topbar-actions">
        <!-- 管理对话是随时可能要用的求助入口（装扩展、排查问题），做成全局入口，不占侧栏。 -->
        <button
          class="btn ghost small icon-only topbar-chat"
          :class="{ active: currentView === 'admin-chat' }"
          type="button"
          :title="currentView === 'admin-chat' ? '关闭管理对话' : '管理对话：安装扩展与排查问题'"
          aria-label="管理对话"
          :aria-pressed="currentView === 'admin-chat'"
          @click="toggleAdminChat"
        >
          <MessageCircle :size="16" aria-hidden="true" />
        </button>
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

      <main class="app-content" :class="{ 'app-content-fill': fillViews.has(currentView) }">
        <section v-if="scopeReady && !unscopedViews.has(currentView)" class="scope-context" aria-label="当前平台与机器人">
          <div class="scope-identity">
            <span class="badge">{{ scopePlatform }}</span>
            <strong>{{ scopeProfile?.name || (botScope ? "未命名机器人" : "全部机器人") }}</strong>
            <span v-if="scopeProfile" class="scope-account">账号 <span class="mono">{{ scopeAccount || "未获取" }}</span></span>
            <span v-if="scopeProfile" class="scope-profile-id">Bot ID <span class="mono">{{ scopeProfile.id }}</span></span>
          </div>
          <span v-if="scopeSwitching" class="scope-loading-label" role="status">正在切换…</span>
          <div v-if="scopeSwitching" class="scope-progress" role="progressbar" aria-label="正在加载当前机器人数据"><span /></div>
        </section>
        <ViewSkeleton v-if="!scopeReady" label="正在加载机器人配置" />
        <div v-else :aria-busy="scopeSwitching" :inert="scopeSwitching" :class="{ 'scope-content-pending': scopeSwitching }">
          <KeepAlive :key="botScope" :max="VIEW_CACHE_LIMIT">
            <component :is="activeView" :key="viewKey" />
          </KeepAlive>
        </div>
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
import { useConfigurationRefresh } from "./lib/configuration-sync";
import { computed, defineAsyncComponent, KeepAlive, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { Component } from "vue";
import {
  Bot,
  Activity,
  BrainCircuit,
  CalendarClock,
  FileClock,
  Heart,
  FolderOpen,
  LayoutGrid,
  MessageCircle,
  MessagesSquare,
  LogOut,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
  PlugZap,
  Globe,
  Moon,
  Sun,
  SunMoon,
  Tag,
  BookUser,
  House,
  Newspaper,
  Users,
  Wrench
} from "@lucide/vue";
import { currentView, navItemForView, navItems, navSections, navigate, type ViewID } from "./router";
import { markSpaceSeen, refreshSpaceUnread, spaceUnread } from "./lib/space-unread";
import { ALL_PROFILES, botCreateRequested, botScope, reconcileBotScope, requestBotScope, setBotScope } from "./lib/bot-scope";
import { scopeSwitching } from "./lib/scope-transition";
import AppSelect from "./components/AppSelect.vue";
import { startEventStream, stream, pushStatusSnapshot } from "./lib/stream";
import { theme } from "./lib/theme";
import { formatUptime } from "./lib/format";
import { checkForUpdate, getBotStatus, getAuthStatus, getBotProfileConfig, getConfig, getHealth, getSystemVersion, logout, type BotProfileConfig, type HealthResponse, type SystemVersion } from "./api";
import ToastHost from "./components/ToastHost.vue";
import ConfirmHost from "./components/ConfirmHost.vue";
import { toastSuccess } from "./lib/toast";
import { channelAccountUnhealthy } from "./lib/channel-status";
import VersionModal from "./components/VersionModal.vue";
import { autoReloadAllowed, clearUpdateInstalling, markAutoReloaded, updateInstallingRecently } from "./lib/backendState";
import BackendDownView from "./views/BackendDownView.vue";
import LoginView from "./views/LoginView.vue";
import DashboardView from "./views/DashboardView.vue";
import BrandLogo from "./components/BrandLogo.vue";
import ViewSkeleton from "./components/ViewSkeleton.vue";

// 页面代码首次下载期间显示整页骨架；120ms 内下完的不画，免得缓存命中时也闪一下。
function lazyView(loader: () => Promise<{ default: Component }>): Component {
  return defineAsyncComponent({ loader, loadingComponent: ViewSkeleton, delay: 120 });
}

// 总览是默认首屏，保持同步加载；其它页面首次打开时才下载代码。异步组件引用
// 保持稳定，配合 KeepAlive 后只挂载一次，切页回来直接复用已有 DOM 和数据。
const RecordsView = lazyView(() => import("./views/RecordsView.vue"));
const AdminChatView = lazyView(() => import("./views/AdminChatView.vue"));
const ChatView = lazyView(() => import("./views/ChatView.vue"));
const TasksView = lazyView(() => import("./views/TasksView.vue"));
const SetupWizard = lazyView(() => import("./views/SetupWizard.vue"));
const LLMView = lazyView(() => import("./views/LLMView.vue"));
const AssistantView = lazyView(() => import("./views/AssistantView.vue"));
const PluginsView = lazyView(() => import("./views/PluginsView.vue"));
const BrowserBoxView = lazyView(() => import("./views/BrowserBoxView.vue"));
const GroupsView = lazyView(() => import("./views/GroupsView.vue"));
const MemoryView = lazyView(() => import("./views/MemoryView.vue"));
const HomeView = lazyView(() => import("./views/HomeView.vue"));
const SettingsView = lazyView(() => import("./views/SettingsView.vue"));
const WorkspaceView = lazyView(() => import("./views/WorkspaceView.vue"));

const VIEW_CACHE_LIMIT = 16;

const viewComponents: Record<ViewID, Component> = {
  dashboard: DashboardView,
  "admin-chat": AdminChatView,
  chat: ChatView,
  events: RecordsView,
  tasks: TasksView,
  setup: SetupWizard,
  provider: LLMView,
  bot: AssistantView,
  plugins: PluginsView,
  groups: GroupsView,
  users: MemoryView,
  notebook: MemoryView,
  "world-book": MemoryView,
  feed: HomeView,
  space: HomeView,
  "self-notes": HomeView,
  browser: BrowserBoxView,
  workspace: WorkspaceView,
  logs: RecordsView,
  favorability: RecordsView,
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
// 自更新的空窗实测约 11 秒（安装、旧进程退出、新进程接管）。宽限期给 15 秒：
// 按秒重试、只说「正在连接」，别把一次计划内的重启渲染成故障页。数到 0 还没回来
// 就整页重载一次——自更新连前端产物一起换了，重载才能拿到新版界面。
const BACKEND_GRACE_SECONDS = 15;
const BACKEND_RETRY_MIN_SECONDS = 5;
const BACKEND_RETRY_MAX_SECONDS = 60;
const locked = ref(false);
// 后端够不着时整页报错，而不是退回登录页：那样用户会拿着对的密码被告知密码不对。
const backendDown = ref(false);
const backendDownDetail = ref("");
const backendRetrying = ref(false);
const backendRetryDelay = ref(1);
// 宽限期内先按「等它回来」渲染，超时之后才升级成故障页。
const backendWaiting = ref(true);
const backendUpdating = ref(false);
// 宽限期剩余秒数，直接显示成倒计时。
const backendGraceLeft = ref(BACKEND_GRACE_SECONDS);
let backendRetryTimer = 0;
let backendDownSince = 0;
let versionBeforeOutage = "";
// 鉴权状态未知时两边都不渲染：抢先把主界面铺出来会让它的接口拿一串 401，
// 那些报错会以 toast 的形式留在随后切出来的登录页上。
const booting = ref(true);
const scopeReady = ref(false);
let sessionGeneration = 0;
let navigationGeneration = 0;
let updateStartTimer = 0;
function sessionActive(generation: number): boolean {
  return generation === sessionGeneration && !locked.value;
}
function invalidateSession(): void {
  sessionGeneration++;
  scopeReady.value = false;
  window.clearTimeout(updateStartTimer);
  window.clearInterval(updateIndicatorTimer);
}
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
  "admin-chat": "管理对话",
  chat: "聊天",
  browser: "浏览器",
  workspace: "文件",
  events: "运行记录",
  tasks: "提醒与订阅",
  setup: "配置向导",
  provider: "提供商",
  bot: "机器人",
  plugins: "插件",
  groups: "群管理",
  users: "记忆",
  notebook: "记忆",
  "world-book": "记忆",
  feed: "小窝",
  space: "小窝",
  "self-notes": "小窝",
  logs: "运行记录",
  favorability: "运行记录",
  settings: "设置"
};

const botProfiles = ref<BotProfileConfig[]>([]);
// 这几页的内容不分机器人（管理员对话、全局一本的世界书），顶上的作用域条会让人
// 以为改的是当前那台的，所以不显示。
const unscopedViews = new Set<ViewID>(["admin-chat", "chat", "world-book"]);
// 这两页自己占满一屏、输入框贴底，内容区底部不再留白，否则高度对不上会多出滚动条。
const fillViews = new Set<ViewID>(["admin-chat", "chat"]);
const scopeProfile = computed(() => botProfiles.value.find((profile) => profile.id === botScope.value));
const scopePlatform = computed(() => {
  const platform = scopeProfile.value?.platform;
  if (!platform) return botScope.value ? "未知平台" : "全部平台";
  return ({ "onebot-v11": "OneBot v11", telegram: "Telegram", qq: "QQ", weixin: "微信", imessage: "iMessage", discord: "Discord" } as Record<string, string>)[platform] || platform;
});
const scopeAccount = computed(() => {
  const channel = stream.status?.channels?.find((channel) => channel.profile_id === botScope.value);
  return channel?.self_id || scopeProfile.value?.bot_account;
});

async function loadBotProfiles(required = false): Promise<void> {
  if (locked.value || booting.value) return;
  const generation = sessionGeneration;
  try {
    const config = await getBotProfileConfig(false, required ? 10_000 : 0);
    if (!sessionActive(generation)) return;
    botProfiles.value = config.profiles?.length ? config.profiles : [config];
    reconcileBotScope(botProfiles.value);
    if (botProfiles.value.length === 1 && botProfiles.value[0]?.id && botScope.value === ALL_PROFILES) {
      setBotScope(botProfiles.value[0].id);
    }
    ensureConcreteGroupScope();
    if (required) scopeReady.value = true;
  } catch (error) {
    if (required && sessionActive(generation)) {
      showBackendDown(error instanceof Error ? error.message : "无法加载机器人配置");
    }
  }
}

// 有机器人就显示切换器：单机器人部署没有可切的，但菜单底部的「添加机器人」
// 是加第二台的入口。「全部机器人」只在真有多台时才有意义。
const scopeOptions = computed(() => {
  if (!botProfiles.value.some((profile) => profile.id)) {
    return [];
  }
  return [
    ...(currentView.value === "groups" || botProfiles.value.length < 2 ? [] : [{ value: ALL_PROFILES, label: "全部机器人" }]),
    ...botProfiles.value
      .filter((profile) => profile.id)
      .map((profile) => ({ value: profile.id ?? "", label: profile.name || profile.platform || "未命名机器人", hint: profile.platform, avatar: profile.avatar_url }))
  ];
});

function ensureConcreteGroupScope(): void {
  if (currentView.value !== "groups" || botScope.value !== ALL_PROFILES) return;
  const first = botProfiles.value.find((profile) => profile.id)?.id;
  if (first) setBotScope(first);
}
const sections = computed(() => navSections());
const footerNavItems = navItems.filter((item) => item.footer);

const viewTitle = computed(() => viewTitles[currentView.value]);

// 日志之于「记录」、笔记本之于「记忆」都是页内的一档，停在那一档时侧边栏要继续
// 亮着它所属的那一项，别让人以为自己掉出了导航。归属关系写在 navItems 的 covers
// 里，这里不再逐个视图硬编码。
function isActiveNav(id: ViewID): boolean {
  return navItemForView(currentView.value) === id;
}
const activeView = computed(() => viewComponents[currentView.value] ?? DashboardView);
const viewKey = computed(() => currentView.value);

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
    const message = first?.account_status_message || "账号状态异常，请在 OneBot 客户端中检查登录状态";
    const label = first?.account_status_known && !first.account_online ? "账号离线" : "账号状态异常";
    return { kind: "err", label, hint: `${message}；WebSocket 仍已连接。` };
  }
  const connected = channels.filter((channel) => channel.connected).length;
  if (connected === 0) {
    return { kind: "err", label: "等待通道连接", hint: "请确认 OneBot 接入端已启动，并检查连接方式、服务地址与鉴权配置。" };
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
    "admin-chat": MessageCircle,
    chat: MessagesSquare,
    events: Activity,
    tasks: CalendarClock,
    provider: BrainCircuit,
    bot: Bot,
    plugins: PlugZap,
    browser: Globe,
    groups: Users,
    users: BookUser,
    feed: Newspaper,
    space: House,
    workspace: FolderOpen,
    logs: FileClock,
    favorability: Heart,
    settings: Wrench
  };
  return icons[id] ?? LayoutGrid;
}

function go(view: ViewID): void {
  navigate(view);
  drawerOpen.value = false;
}

// 顶栏的管理对话按钮是开关：再点一下回到打开它之前的那页，直接打开的就回总览。
let viewBeforeAdminChat: ViewID = "dashboard";
watch(currentView, (view, previous) => {
  if (view === "admin-chat" && previous && previous !== "admin-chat") viewBeforeAdminChat = previous;
});
function toggleAdminChat(): void {
  go(currentView.value === "admin-chat" ? viewBeforeAdminChat : "admin-chat");
}

function requestBotCreate(): void {
  botCreateRequested.value = true;
  go("bot");
}

// 版本号只在进程重启时会变，而 SSE 断开重连正是那个信号：升级完成、手动重启、
// 容器换镜像都会走到这里。之前版本只在 bootApp 里取一次，重启后页面上还挂着旧
// 版本号，除非手动刷新。
const UPDATE_INDICATOR_INTERVAL_MS = 30 * 60 * 1000;
let updateIndicatorTimer = 0;

function refreshRuntimeVersion(): void {
  const generation = sessionGeneration;
  void getHealth().then((result) => {
    if (sessionActive(generation)) health.value = result;
  }).catch(() => undefined);
  void getSystemVersion(true).then((result) => {
    if (sessionActive(generation)) systemVersion.value = result;
  }).catch(() => undefined);
}

async function refreshUpdateIndicator(): Promise<void> {
  const generation = sessionGeneration;
  if (locked.value || booting.value || backendDown.value) return;
  try {
    const result = await checkForUpdate();
    if (sessionActive(generation)) releaseUpdateAvailable.value = result.update_available;
  } catch {
    // 更新提示失败不打扰当前操作。
  }
}

function bootApp(): void {
  invalidateSession();
  const generation = sessionGeneration;
  const initialNavigation = navigationGeneration;
  startEventStream();
  refreshRuntimeVersion();
  void loadBotProfiles(true);
  // 大模型没配、或首启播种的那个机器人还没启用，都算没走完引导。「看过」的标记
  // 记的是机器人 ID：同一个浏览器换到一台新装的 Diana（新数据库、新 ID）还会再弹。
  void Promise.all([getConfig(), getBotProfileConfig()]).then(([config, bot]) => {
    if (!sessionActive(generation) || initialNavigation !== navigationGeneration) return;
    if (config.api_key_configured && bot.enabled) return;
    const instance = bot.id || "default";
    let seen = "";
    try { seen = window.localStorage.getItem(SETUP_DISMISS_KEY) ?? ""; } catch { /* 隐私模式读不到就当没看过 */ }
    if (seen === instance) return;
    navigate("setup");
    try { window.localStorage.setItem(SETUP_DISMISS_KEY, instance); } catch { /* 写不进去只是下次再弹 */ }
  }).catch(() => undefined);
  updateStartTimer = window.setTimeout(() => void refreshUpdateIndicator(), 2500);
  updateIndicatorTimer = window.setInterval(() => void refreshUpdateIndicator(), UPDATE_INDICATOR_INTERVAL_MS);
}

async function doLogout(): Promise<void> {
  locked.value = true;
  invalidateSession();
  try {
    await logout();
  } finally {
    // 重载最简单，能确保清掉所有视图里缓存的配置数据。
    window.location.reload();
  }
}

function onLoginSuccess(): void {
  locked.value = false;
  booting.value = false;
  bootApp();
}

// 重连即重启：连上之后把版本、运行时长和更新提示一起对齐。
// 机器人页可能增删配置档，离开时重新取一次，切换器的选项不至于停在旧列表上。
// 进了小窝的任一档就算看过；离开时不清，免得刚产生的新动静被吞掉。
watch([currentView, botScope, scopeReady, locked, backendDown], ([view, scope]) => {
  if (booting.value || locked.value || backendDown.value || !scopeReady.value) return;
  if (view === "space" || view === "feed" || view === "self-notes") markSpaceSeen(scope);
  else void refreshSpaceUnread(scope).catch(() => undefined);
}, { immediate: true });

watch(currentView, (next, previous) => {
  navigationGeneration++;
  if (booting.value || locked.value || !scopeReady.value) return;
  if (next === "groups") ensureConcreteGroupScope();
  if (previous === "bot" && next !== "bot") {
    void loadBotProfiles();
  }
}, { flush: "sync" });

// 首次建连不算重连：bootApp 刚取过版本，2.5 秒后也会查更新，别再各多发一遍。
let streamConnectedBefore = false;
watch(() => stream.connected, (connected, previous) => {
  const reconnected = connected && previous === false && streamConnectedBefore;
  if (connected) streamConnectedBefore = true;
  if (reconnected &&!locked.value && !booting.value && !backendDown.value && scopeReady.value) {
    void refreshRuntimeVersion();
    void refreshUpdateIndicator();
  }
});

function showBackendDown(detail: string): void {
  invalidateSession();
  backendDownDetail.value = detail;
  backendDown.value = true;
  backendRetrying.value = false;
  backendWaiting.value = true;
  backendUpdating.value = updateInstallingRecently();
  backendRetryDelay.value = 1;
  backendGraceLeft.value = BACKEND_GRACE_SECONDS;
  backendDownSince = Date.now();
  versionBeforeOutage = systemVersion.value?.version_label ?? "";
  scheduleBackendRetry();
}

function scheduleBackendRetry(): void {
  window.clearTimeout(backendRetryTimer);
  backendRetryTimer = window.setTimeout(() => {
    void retryBackend();
  }, backendRetryDelay.value * 1000);
}

// 宽限期内每秒敲一次门，好让一次十几秒的升级重启尽快接上；超时之后改成退避，
// 后端要是关了一整天，不该让页面一直高频重试。
async function retryBackend(): Promise<void> {
  window.clearTimeout(backendRetryTimer);
  if (backendRetrying.value) return;
  backendRetrying.value = true;
  const outageVersion = versionBeforeOutage;
  const reachable = await probeAuthStatus();
  backendRetrying.value = false;
  if (reachable) {
    backendDown.value = false;
    backendWaiting.value = true;
    backendUpdating.value = false;
    clearUpdateInstalling();
    // 版本变了就说清楚刚才那下是升级重启，而不是让人以为服务出过问题。
    const generation = sessionGeneration;
    const recoveredVersion = !locked.value && outageVersion
      ? await getSystemVersion(true).catch(() => null) : null;
    if (!sessionActive(generation)) return;
    const now = recoveredVersion?.version_label ?? "";
    if (recoveredVersion) systemVersion.value = recoveredVersion;
    if (outageVersion && now && now !== outageVersion) {
      toastSuccess(`后端已更新到 ${now}，正在重新载入界面…`);
      versionBeforeOutage = "";
      // 后端换了版本，这个页面跑的却还是更新前那份 JS。前端产物的文件名带内容
      // 哈希，只有整页重载才会取到新的那份，否则用户会拿旧界面对着新后端。
      if (autoReloadAllowed()) {
        markAutoReloaded();
        window.setTimeout(() => window.location.reload(), 1200);
      }
      return;
    }
    versionBeforeOutage = "";
    return;
  }
  const elapsed = Math.floor((Date.now() - backendDownSince) / 1000);
  backendGraceLeft.value = Math.max(BACKEND_GRACE_SECONDS - elapsed, 0);
  if (backendGraceLeft.value > 0) {
    backendRetryDelay.value = 1;
    scheduleBackendRetry();
    return;
  }
  // 倒计时归零：重载一次，顺带把可能已经换过的前端产物一起取新的。
  if (backendWaiting.value && autoReloadAllowed()) {
    markAutoReloaded();
    window.location.reload();
    return;
  }
  backendWaiting.value = false;
  backendRetryDelay.value = backendRetryDelay.value < BACKEND_RETRY_MIN_SECONDS
    ? BACKEND_RETRY_MIN_SECONDS
    : Math.min(backendRetryDelay.value * 2, BACKEND_RETRY_MAX_SECONDS);
  scheduleBackendRetry();
}

// probeAuthStatus 问一次登录态：连得上就顺手把界面切到该去的地方，返回 false 表示
// 后端还是够不着。
async function probeAuthStatus(): Promise<boolean> {
  const generation = sessionGeneration;
  try {
    const auth = await getAuthStatus();
    if (generation !== sessionGeneration) return true;
    locked.value = auth.auth_required && !auth.authenticated;
    booting.value = false;
    if (!locked.value) {
      bootApp();
    }
    return true;
  } catch (err) {
    if (generation !== sessionGeneration) return true;
    backendDownDetail.value = err instanceof Error ? err.message : "连不上 Diana 后端服务";
    return false;
  }
}

onMounted(async () => {
  sidebarMedia.addEventListener("change", syncSidebarMode);
  // 会话失效时任意接口的 401 会广播这个事件，统一切回登录界面。
  window.addEventListener("diana:unauthorized", () => {
    locked.value = true;
    booting.value = false;
    backendDown.value = false;
    window.clearTimeout(backendRetryTimer);
    invalidateSession();
  });
  window.addEventListener("diana:open-version", () => {
    versionOpen.value = true;
  });
  // 这一步既是取登录态，也是探活：它失败就说明后端够不着，再往下走只会渲染一个
  // 处处报错的空壳界面。
  const reachable = await probeAuthStatus();
  booting.value = false;
  if (!reachable) {
    showBackendDown(backendDownDetail.value);
  }
});

onBeforeUnmount(() => {
  invalidateSession();
  sidebarMedia.removeEventListener("change", syncSidebarMode);
  window.clearInterval(updateIndicatorTimer);
  window.clearTimeout(backendRetryTimer);
});
useConfigurationRefresh(["bot"], () => loadBotProfiles());
useConfigurationRefresh(["bot", "llm"], async () => {
  if (locked.value || booting.value || !scopeReady.value) return;
  const generation = sessionGeneration;
  const status = await getBotStatus();
  if (sessionActive(generation)) pushStatusSnapshot(status);
});

</script>
