<template>
  <LoginView v-if="locked" @success="onLoginSuccess" />
  <div v-else class="app-shell">
    <transition name="none">
      <div v-if="drawerOpen" class="drawer-backdrop" @click="drawerOpen = false" />
    </transition>

    <aside class="app-sidebar" :class="{ open: drawerOpen }" aria-label="主导航">
      <div class="brand">
        <span class="brand-mark">
          <BotMessageSquare :size="19" aria-hidden="true" />
        </span>
        <span class="brand-name">
          <strong>Diana</strong>
          <button class="brand-version" type="button" title="查看版本与更新" @click="versionOpen = true">
            {{ versionLabel }}
            <span v-if="updateBehind > 0" class="version-dot" aria-label="有新版本"></span>
          </button>
        </span>
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
        <button class="nav-theme" type="button" :title="themeToggleLabel" @click="cycleTheme">
          <component :is="themeIcon" :size="15" aria-hidden="true" />
          <span class="nav-theme-text">
            <span class="nav-theme-title">外观</span>
            <span class="nav-theme-value">{{ themeModeLabel }}</span>
          </span>
        </button>
        <span class="cluster" style="gap: 6px">
          <span class="status-dot" :class="stream.connected ? 'text-ok' : 'text-err'" aria-hidden="true" />
          {{ stream.connected ? "实时连接正常" : "实时连接已断开" }}
        </span>
        <span v-if="health" class="mono">{{ health.version }} · 已运行 {{ formatUptime(health.uptime_seconds) }}</span>
      </div>
    </aside>

    <div class="app-main">
      <header class="app-topbar">
        <button class="btn ghost icon-only menu-button" type="button" aria-label="打开导航" @click="drawerOpen = true">
          <PanelLeftOpen :size="18" aria-hidden="true" />
        </button>
        <span class="topbar-title">{{ viewTitle }}</span>
        <span class="topbar-spacer" />
        <span v-if="botSummary" class="badge" :class="botSummary.kind">
          <span class="status-dot" :class="{ pulse: botSummary.kind === 'ok' }" aria-hidden="true" />
          {{ botSummary.label }}
        </span>
        <button class="btn ghost small topbar-logout" type="button" title="退出登录" @click="doLogout">
          <LogOut :size="15" aria-hidden="true" />
          退出登录
        </button>
      </header>

      <main class="app-content">
        <DashboardView v-if="currentView === 'dashboard'" />
        <SetupWizard v-else-if="currentView === 'setup'" />
        <LLMView v-else-if="currentView === 'llm'" />
        <TestChatView v-else-if="currentView === 'test'" />
        <AssistantView v-else-if="currentView === 'bot'" />
        <PluginsView v-else-if="currentView === 'plugins'" />
        <GroupsView v-else-if="currentView === 'groups'" />
        <LogsView v-else-if="currentView === 'logs'" />
        <SettingsView v-else />
      </main>
    </div>

    <ToastHost />
    <ConfirmHost />
    <VersionModal v-if="versionOpen" @close="versionOpen = false" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import type { Component } from "vue";
import {
  Bot,
  BotMessageSquare,
  BrainCircuit,
  FileClock,
  LayoutGrid,
  MessageCircle,
  LogOut,
  PanelLeftOpen,
  PlugZap,
  Moon,
  Sun,
  SunMoon,
  Users,
  Wrench
} from "@lucide/vue";
import { currentView, navItems, navigate, type ViewID } from "./router";
import { startEventStream, stream } from "./stream";
import { theme } from "./theme";
import { formatUptime } from "./format";
import { getAuthStatus, getConfig, getHealth, getSystemVersion, logout, type HealthResponse, type SystemVersion } from "./api";
import ToastHost from "./components/ToastHost.vue";
import ConfirmHost from "./components/ConfirmHost.vue";
import { toastSuccess } from "./toast";
import VersionModal from "./components/VersionModal.vue";
import LoginView from "./views/LoginView.vue";
import DashboardView from "./views/DashboardView.vue";
import SetupWizard from "./views/SetupWizard.vue";
import LLMView from "./views/LLMView.vue";
import TestChatView from "./views/TestChatView.vue";
import AssistantView from "./views/AssistantView.vue";
import PluginsView from "./views/PluginsView.vue";
import GroupsView from "./views/GroupsView.vue";
import LogsView from "./views/LogsView.vue";
import SettingsView from "./views/SettingsView.vue";

const drawerOpen = ref(false);
const locked = ref(false);
const versionOpen = ref(false);
const systemVersion = ref<SystemVersion | null>(null);
const versionLabel = computed(() => systemVersion.value?.version_label || systemVersion.value?.build_version || "控制台");
const updateBehind = computed(() => systemVersion.value?.behind ?? 0);
const health = ref<HealthResponse | null>(null);

const SETUP_DISMISS_KEY = "dqb-next:setup-seen";


const viewTitles: Record<ViewID, string> = {
  dashboard: "总览",
  setup: "配置向导",
  llm: "LLM 配置",
  test: "测试台",
  bot: "机器人",
  plugins: "插件",
  groups: "群管理",
  logs: "日志",
  settings: "设置"
};

const viewTitle = computed(() => viewTitles[currentView.value]);

const botSummary = computed(() => {
  const status = stream.status;
  if (!status) {
    return null;
  }
  if (!status.running) {
    return { kind: "warn", label: "机器人未启动" };
  }
  if (!status.channel.connected) {
    return { kind: "err", label: "等待 NapCat 连接" };
  }
  return { kind: "ok", label: `已连接 ${status.channel.self_id || ""}`.trim() };
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
    llm: BrainCircuit,
    test: MessageCircle,
    bot: Bot,
    plugins: PlugZap,
    groups: Users,
    logs: FileClock,
    settings: Wrench
  };
  return icons[id] ?? LayoutGrid;
}

function go(view: ViewID): void {
  navigate(view);
  drawerOpen.value = false;
}

// bootApp 在鉴权通过（或未开启鉴权）后加载应用数据。
async function bootApp(): Promise<void> {
  startEventStream();
  try {
    health.value = await getHealth();
  } catch {
    /* 健康接口失败不影响使用 */
  }
  try {
    systemVersion.value = await getSystemVersion();
  } catch {
    /* 版本信息失败时侧栏只显示占位 */
  }
  // 首次访问且 LLM 未配置时自动进入向导；之后只在总览顶部保留一条引导。
  try {
    const config = await getConfig();
    if (!config.api_key_configured && !window.localStorage.getItem(SETUP_DISMISS_KEY)) {
      navigate("setup");
      window.localStorage.setItem(SETUP_DISMISS_KEY, "1");
    }
  } catch {
    /* 配置读取失败时停留在总览 */
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

onMounted(async () => {
  // 会话失效时任意接口的 401 会广播这个事件，统一切回登录界面。
  window.addEventListener("diana:unauthorized", () => {
    locked.value = true;
  });
  try {
    const auth = await getAuthStatus();
    if (auth.auth_required && !auth.authenticated) {
      locked.value = true;
      return;
    }
  } catch {
    /* 状态接口失败按未开启鉴权处理，避免把用户锁在门外 */
  }
  await bootApp();
});
</script>
