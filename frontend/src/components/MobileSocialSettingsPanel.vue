<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <template v-if="tab === 'device'">
    <section class="repository-watch-manager credential-manager" data-mobile-device-panel>
      <div class="repository-watch-manager-head">
        <div>
          <h3>手机连接</h3>
          <p>插上 USB 真机即可自动识别；只勾选 Hacker News、V2EX 时无需连手机。</p>
        </div>
        <button
          class="btn small"
          type="button"
          data-mobile-settings-test
          :disabled="testingConnection"
          @click="emit('test-connection')"
        >
          <LoaderCircle v-if="testingConnection" :size="14" class="spin" aria-hidden="true" />
          <RefreshCw v-else :size="14" aria-hidden="true" />
          {{ testingConnection ? "检测中…" : "检测连接" }}
        </button>
      </div>

      <div class="repository-watch-manager-list">
        <form v-if="editingDevice" class="credential-edit" @submit.prevent="commitEditing">
          <div class="form-grid mobile-device-edit-grid">
            <div class="field wide">
              <label for="mobile-setting-backend">连接方式</label>
              <AppSelect
                id="mobile-setting-backend"
                :model-value="deviceDraft.driver_backend"
                :options="backendOptions"
                @update:model-value="deviceDraft.driver_backend = String($event ?? 'auto')"
              />
            </div>

            <template v-if="isAndroidOrAutoDraft">
              <div class="field wide">
                <label for="mobile-setting-pin">锁屏密码（选填）</label>
                <div class="credential-edit-row">
                  <input
                    id="mobile-setting-pin"
                    v-model="deviceDraft.unlock_pin"
                    class="input credential-edit-token"
                    type="password"
                    autocomplete="off"
                    :disabled="deviceDraft.clearing_pin"
                    :placeholder="pinPlaceholder"
                  />
                  <button
                    v-if="secretConfigured('unlock_pin')"
                    class="btn small ghost"
                    type="button"
                    @click="deviceDraft.clearing_pin = !deviceDraft.clearing_pin"
                  >
                    {{ deviceDraft.clearing_pin ? "撤销清除" : "清除" }}
                  </button>
                </div>
                <span class="hint">锁屏时自动点按数字键盘解锁，输错一次即停，不会连续试错。</span>
              </div>

              <div class="field wide">
                <div class="plugin-setting-switch">
                  <div class="plugin-setting-switch-text">
                    <label for="mobile-setting-stay-awake">插电保持常亮</label>
                    <span class="hint">USB 供电时不自动熄屏锁屏。</span>
                  </div>
                  <label class="switch">
                    <input id="mobile-setting-stay-awake" v-model="deviceDraft.stay_awake_usb" type="checkbox" />
                    <span class="track" aria-hidden="true"></span>
                  </label>
                </div>
              </div>

              <details class="field wide music-advanced" :open="hasAdvancedDraft">
                <summary>高级连接（无线地址 · 模拟器 · 指定序列号）</summary>
                <div class="form-grid mobile-advanced-grid">
                  <div class="field">
                    <label for="mobile-setting-adb-addr">无线 / 容器地址</label>
                    <input
                      id="mobile-setting-adb-addr"
                      v-model.trim="deviceDraft.adb_address"
                      class="input"
                      type="text"
                      placeholder="如 127.0.0.1:5555（USB 直连留空）"
                    />
                  </div>
                  <div class="field">
                    <label for="mobile-setting-device-id">指定设备 ID</label>
                    <input
                      id="mobile-setting-device-id"
                      v-model.trim="deviceDraft.device_id"
                      class="input"
                      type="text"
                      placeholder="单台设备留空即可"
                    />
                  </div>
                  <div class="field">
                    <label for="mobile-setting-vm-name">自动启动模拟器名称</label>
                    <input
                      id="mobile-setting-vm-name"
                      v-model.trim="deviceDraft.vm_name"
                      class="input"
                      type="text"
                      placeholder="如 Pixel_8_API_35（不用留空）"
                    />
                  </div>
                  <div class="field mobile-inline-switch">
                    <div class="plugin-setting-switch">
                      <div class="plugin-setting-switch-text">
                        <label for="mobile-setting-vm-headless">后台无窗口启动模拟器</label>
                      </div>
                      <label class="switch">
                        <input id="mobile-setting-vm-headless" v-model="deviceDraft.vm_headless" type="checkbox" />
                        <span class="track" aria-hidden="true"></span>
                      </label>
                    </div>
                  </div>
                </div>
              </details>
            </template>

            <template v-else-if="deviceDraft.driver_backend === 'wda'">
              <div class="field">
                <label for="mobile-setting-wda-url">WebDriverAgent 地址</label>
                <input
                  id="mobile-setting-wda-url"
                  v-model.trim="deviceDraft.wda_url"
                  class="input"
                  type="text"
                  placeholder="http://127.0.0.1:8100"
                />
              </div>
              <div class="field">
                <label for="mobile-setting-wda-id">指定设备 UDID（选填）</label>
                <input
                  id="mobile-setting-wda-id"
                  v-model.trim="deviceDraft.device_id"
                  class="input"
                  type="text"
                  placeholder="单台留空"
                />
              </div>
            </template>

            <template v-else-if="deviceDraft.driver_backend === 'simctl'">
              <div class="field">
                <label for="mobile-setting-sim-name">模拟器名称或 UDID</label>
                <input
                  id="mobile-setting-sim-name"
                  v-model.trim="deviceDraft.vm_name"
                  class="input"
                  type="text"
                  placeholder="如 iPhone 16（已开机留空）"
                />
              </div>
              <div class="field">
                <label for="mobile-setting-sim-wda">WebDriverAgent 地址（选填）</label>
                <input
                  id="mobile-setting-sim-wda"
                  v-model.trim="deviceDraft.wda_url"
                  class="input"
                  type="text"
                  placeholder="http://127.0.0.1:8100"
                />
              </div>
            </template>

            <template v-else-if="deviceDraft.driver_backend === 'mobile_mcp'">
              <div class="field wide">
                <label for="mobile-setting-mcp-cmd">MCP 启动命令</label>
                <input
                  id="mobile-setting-mcp-cmd"
                  v-model.trim="deviceDraft.mcp_command"
                  class="input"
                  type="text"
                  placeholder="npx -y @mobilenext/mobile-mcp@latest"
                />
              </div>
            </template>
          </div>

          <div class="credential-edit-foot">
            <div class="credential-edit-status">
              <span class="hint">点「完成」后记得点击弹窗底部「保存」生效。</span>
            </div>
            <div class="cluster" style="gap: 7px; flex-wrap: nowrap">
              <button class="btn small" type="button" @click="cancelEditDevice">取消</button>
              <button class="btn small primary" type="submit">完成</button>
            </div>
          </div>
        </form>

        <article v-else class="repository-watch-manager-item">
          <div class="repository-watch-manager-main">
            <div class="cluster">
              <strong>{{ primaryDeviceTitle }}</strong>
              <span class="badge" :class="connectionBadgeTone">{{ connectionBadgeLabel }}</span>
              <span class="badge">{{ backendLabel(String(form.driver_backend || 'auto')) }}</span>
              <template v-for="api in enabledAPIBadges" :key="api.source">
                <span class="badge" :class="api.ok ? 'accent' : 'warn'" :title="api.message">
                  {{ api.label }} {{ api.ok ? "正常" : "异常" }}
                </span>
              </template>
            </div>
            <div class="task-facts">
              <span v-if="showAndroidFacts">锁屏密码：{{ pinStatusLabel }}</span>
              <span v-if="showAndroidFacts && form.stay_awake_usb">插电常亮已开启</span>
              <span v-if="form.adb_address">无线地址：{{ form.adb_address }}</span>
              <span v-if="form.vm_name">模拟器：{{ form.vm_name }}</span>
              <span v-if="form.device_id">序列号：{{ form.device_id }}</span>
            </div>
            <p v-if="connectionReport?.mobile_message && connectionReport.mobile_ok" class="hint mobile-card-status-msg">
              {{ connectionReport.mobile_message }}
            </p>
          </div>
          <div class="repository-watch-manager-actions">
            <button class="btn small" type="button" data-mobile-device-edit @click="startEditDevice">
              <Pencil :size="14" aria-hidden="true" />
              编辑
            </button>
          </div>
        </article>
      </div>
    </section>

    <div class="plugin-settings-section-head plugin-settings-subsection">
      <h3>操控与识屏</h3>
    </div>
    <div class="stack plugin-settings-form">
      <PluginSettingField v-if="specOf('allow_agent_control')" :spec="specOf('allow_agent_control')!" :form="form" />
      <PluginSettingField v-if="specOf('vision_fallback')" :spec="specOf('vision_fallback')!" :form="form" />
    </div>

    <button class="btn small ghost github-settings-link" type="button" @click="emit('update:tab', 'patrol')">
      去「巡检推送」选择平台与通知群聊
      <ArrowRight :size="14" aria-hidden="true" />
    </button>
  </template>

  <template v-else-if="tab === 'patrol'">
    <section class="repository-watch-manager" data-mobile-patrol-panel>
      <div class="repository-watch-manager-head">
        <div>
          <h3>巡检内容</h3>
          <p>前 3 个平台通过手机打开对应 App 读取；Hacker News 与 V2EX 直接走公开接口。</p>
        </div>
        <button class="btn small" type="button" :disabled="runningPatrol" @click="triggerPatrolNow">
          <LoaderCircle v-if="runningPatrol" :size="14" class="spin" aria-hidden="true" />
          <Play v-else :size="14" aria-hidden="true" />
          {{ runningPatrol ? "巡检中…" : "立即巡检" }}
        </button>
      </div>

      <div class="stack plugin-settings-form">
        <PluginSettingField v-if="specOf('patrol_enabled')" :spec="specOf('patrol_enabled')!" :form="form" />

        <div class="field">
          <span id="mobile-sources-label" class="plugin-setting-group-label">巡检平台</span>
          <div class="repository-watch-scopes" role="group" aria-labelledby="mobile-sources-label">
            <label v-for="opt in sourceOptions" :key="opt.value" class="check-item">
              <input
                type="checkbox"
                :checked="sourceChecked(opt.value)"
                @change="toggleSource(opt.value, $event)"
              />
              {{ opt.label }}
            </label>
          </div>
        </div>

        <div class="form-grid mobile-two-col">
          <PluginSettingField v-if="specOf('poll_interval_minutes')" :spec="specOf('poll_interval_minutes')!" :form="form" />
          <PluginSettingField v-if="specOf('judge_model')" :spec="specOf('judge_model')!" :form="form" />
        </div>

        <PluginSettingField v-if="specOf('interest_topics')" :spec="specOf('interest_topics')!" :form="form" />
      </div>
    </section>

    <div class="plugin-settings-section-head plugin-settings-subsection">
      <h3>推送群聊与时机</h3>
      <p>不添加通知目标时，默认发往已启用本插件的活跃群。</p>
    </div>

    <div class="stack plugin-settings-form">
      <SubscriptionTargetsEditor
        :model-value="targetRows"
        :profiles="profiles"
        :default-profile="defaultProfile || profiles[0]?.id"
        :plugin-ids="['official.mobile-social']"
        @update:model-value="onTargetsChanged"
      />

      <PluginSettingField v-if="specOf('breaking_push')" :spec="specOf('breaking_push')!" :form="form" />
      <PluginSettingField v-if="specOf('casual_pool')" :spec="specOf('casual_pool')!" :form="form" />

      <details class="music-advanced" :open="false">
        <summary>
          防打扰与限流：冷却 {{ form.breaking_cooldown_minutes || 90 }} 分钟 · 每日最多 {{ form.daily_push_limit || 3 }} 条 · 免打扰 {{ form.quiet_start || "23:00" }}–{{ form.quiet_end || "08:00" }}
        </summary>
        <div class="form-grid mobile-advanced-grid">
          <PluginSettingField v-if="specOf('breaking_cooldown_minutes')" :spec="specOf('breaking_cooldown_minutes')!" :form="form" />
          <PluginSettingField v-if="specOf('daily_push_limit')" :spec="specOf('daily_push_limit')!" :form="form" />
          <PluginSettingField v-if="specOf('quiet_start')" :spec="specOf('quiet_start')!" :form="form" />
          <PluginSettingField v-if="specOf('quiet_end')" :spec="specOf('quiet_end')!" :form="form" />
          <PluginSettingField v-if="specOf('swipe_screens')" :spec="specOf('swipe_screens')!" :form="form" />
        </div>
        <PluginSettingField v-if="specOf('error_notice')" :spec="specOf('error_notice')!" :form="form" />
      </details>
    </div>
  </template>

  <template v-else>
    <section class="repository-watch-manager" data-mobile-pool-panel>
      <div class="repository-watch-manager-head">
        <div>
          <h3>候选池与最近记录</h3>
          <p>{{ poolHeaderHint }}</p>
        </div>
        <div class="cluster" style="gap: 7px; flex-wrap: nowrap">
          <button class="btn small" type="button" :disabled="loadingPool" @click="loadPoolState">
            <RefreshCw :size="14" :class="{ spin: loadingPool }" aria-hidden="true" />
            刷新
          </button>
          <button class="btn small primary" type="button" :disabled="runningPatrol" @click="triggerPatrolNow">
            <LoaderCircle v-if="runningPatrol" :size="14" class="spin" aria-hidden="true" />
            <Play v-else :size="14" aria-hidden="true" />
            {{ runningPatrol ? "巡检中…" : "立即巡检" }}
          </button>
        </div>
      </div>

      <div v-if="loadingPool && !poolState" class="repository-watch-manager-empty">
        <LoaderCircle :size="16" class="spin" aria-hidden="true" />
        正在读取候选池
      </div>

      <div v-else-if="combinedItems.length" class="repository-watch-manager-list">
        <article v-for="entry in combinedItems" :key="`${entry.pending ? 'p' : 'c'}-${entry.item.id}`" class="repository-watch-manager-item">
          <div class="repository-watch-manager-main">
            <div class="cluster">
              <span class="badge" :class="entry.pending ? 'warn' : entry.item.tier === 'breaking' ? 'accent' : ''">
                {{ entry.pending ? "待发重磅" : entry.item.tier === "breaking" ? "重磅突发" : "日常话题" }}
              </span>
              <span class="badge">{{ sourceLabel(entry.item.source) }}</span>
              <span class="hint">{{ entry.item.score }} 分</span>
              <strong>{{ entry.item.title }}</strong>
            </div>
            <p class="mobile-candidate-summary">{{ entry.item.summary }}</p>
            <div class="task-facts">
              <span>{{ formatUnixTime(entry.item.created_at) }}</span>
              <span v-if="sharedGroupCount(entry.item) > 0">已在 {{ sharedGroupCount(entry.item) }} 个群聊过</span>
              <a v-if="entry.item.url" :href="entry.item.url" target="_blank" rel="noreferrer" class="token-create-link">原帖链接</a>
            </div>
          </div>
          <div class="repository-watch-manager-actions">
            <button
              class="btn small ghost danger icon-only"
              type="button"
              title="移除此条"
              aria-label="移除此条"
              :disabled="deletingID === entry.item.id"
              @click="removeCandidate(entry.item.id)"
            >
              <Trash2 :size="14" aria-hidden="true" />
            </button>
          </div>
        </article>
      </div>

      <div v-else class="repository-watch-manager-empty repository-watch-manager-empty-guide">
        <strong>候选池里还没有热闻</strong>
        <span>开启插件或点右上角「立即巡检」即可抓取并筛选最新帖子。</span>
      </div>

      <div class="github-run-records-actions">
        <button class="btn small ghost" type="button" @click="emit('open-logs')">查看执行日志</button>
      </div>
    </section>
  </template>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { ArrowRight, LoaderCircle, Pencil, Play, RefreshCw, Trash2 } from "@lucide/vue";
import {
  deleteMobileSocialCandidate,
  getBotProfileConfig,
  getMobileSocialState,
  runMobileSocialPatrol,
  type BotProfileConfig,
  type MobileSocialCandidateItem,
  type MobileSocialConnectionReport,
  type MobileSocialStateSnapshot,
  type PluginSettingSpec,
  type RepositoryWatchTarget
} from "../api";
import { toastError, toastSuccess } from "../lib/toast";
import AppSelect from "./AppSelect.vue";
import PluginSettingField from "./PluginSettingField.vue";
import SubscriptionTargetsEditor from "./SubscriptionTargetsEditor.vue";

export type MobileSocialTab = "device" | "patrol" | "pool";

const props = defineProps<{
  tab: MobileSocialTab;
  specs: PluginSettingSpec[];
  form: Record<string, any>;
  clearSecrets: string[];
  secretConfigured: (key: string) => boolean;
  defaultProfile?: string;
  testingConnection: boolean;
  connectionReport: MobileSocialConnectionReport | null;
  buildPayload: () => Record<string, unknown>;
}>();

const emit = defineEmits<{
  "update:tab": [MobileSocialTab];
  "toggle-clear": [string];
  "test-connection": [];
  "open-logs": [];
  "runtime-updated": [];
}>();

function specOf(key: string): PluginSettingSpec | undefined {
  return props.specs.find((item) => item.key === key);
}

const backendOptions = [
  { value: "auto", label: "自动识别（推荐）" },
  { value: "adb", label: "安卓（USB / 无线 / 模拟器）" },
  { value: "wda", label: "iPhone（WebDriverAgent）" },
  { value: "simctl", label: "macOS iOS 模拟器" },
  { value: "mobile_mcp", label: "Mobile MCP" }
];

const sourceOptions = [
  { value: "x", label: "X" },
  { value: "xiaohongshu", label: "小红书" },
  { value: "heybox", label: "小黑盒" },
  { value: "hackernews", label: "Hacker News" },
  { value: "v2ex", label: "V2EX" }
];

function backendLabel(value: string): string {
  switch (value) {
    case "adb": return "安卓";
    case "wda": return "iPhone WDA";
    case "simctl": return "iOS 模拟器";
    case "mobile_mcp": return "Mobile MCP";
    default: return "自动识别";
  }
}

function sourceLabel(source: string): string {
  return sourceOptions.find((item) => item.value === source)?.label ?? source;
}

// 设备连接行内编辑：默认收起成一张摘要卡，点「编辑」才展开表单。
const editingDevice = ref(false);
const deviceDraft = reactive({
  driver_backend: "auto",
  device_id: "",
  vm_name: "",
  vm_headless: true,
  adb_address: "",
  unlock_pin: "",
  clearing_pin: false,
  stay_awake_usb: false,
  wda_url: "",
  mcp_command: "npx -y @mobilenext/mobile-mcp@latest"
});

const isAndroidOrAutoDraft = computed(() => deviceDraft.driver_backend === "auto" || deviceDraft.driver_backend === "adb");
const hasAdvancedDraft = computed(() => Boolean(deviceDraft.adb_address || deviceDraft.device_id || deviceDraft.vm_name));

const pinPlaceholder = computed(() => {
  if (deviceDraft.clearing_pin) return "保存后将清除已存密码";
  return props.secretConfigured("unlock_pin") ? "已设置 — 留空保持不变，输入则覆盖" : "如 6 位锁屏数字密码（选填）";
});

const showAndroidFacts = computed(() => {
  const backend = String(props.form.driver_backend || "auto");
  return backend === "auto" || backend === "adb";
});

const pinStatusLabel = computed(() => {
  if (props.clearSecrets.includes("unlock_pin")) return "待清除";
  if (String(props.form.unlock_pin || "").trim() || props.secretConfigured("unlock_pin")) return "已设置";
  return "未设置";
});

const selectedDevice = computed(() => {
  const list = props.connectionReport?.devices ?? [];
  return list.find((item) => item.selected) ?? list[0] ?? null;
});

const primaryDeviceTitle = computed(() => {
  if (selectedDevice.value) return selectedDevice.value.name;
  if (props.form.vm_name) return String(props.form.vm_name);
  if (props.form.adb_address) return String(props.form.adb_address);
  return "默认手机通道";
});

const connectionBadgeLabel = computed(() => {
  if (props.testingConnection) return "检测中";
  const report = props.connectionReport;
  if (!report) return "待检测";
  if (!report.needs_mobile) return "仅公开接口";
  if (selectedDevice.value?.locked) return "已锁屏";
  return report.mobile_ok ? "已连接" : "未连接";
});

const connectionBadgeTone = computed(() => {
  const report = props.connectionReport;
  if (!report || props.testingConnection) return "";
  if (!report.needs_mobile) return "accent";
  if (selectedDevice.value?.locked) return "warn";
  return report.mobile_ok ? "accent" : "warn";
});

const enabledAPIBadges = computed(() => (props.connectionReport?.api_sources ?? []).filter((item) => item.enabled));

function startEditDevice(): void {
  deviceDraft.driver_backend = String(props.form.driver_backend || "auto");
  deviceDraft.device_id = String(props.form.device_id || "");
  deviceDraft.vm_name = String(props.form.vm_name || "");
  deviceDraft.vm_headless = Boolean(props.form.vm_headless ?? true);
  deviceDraft.adb_address = String(props.form.adb_address || "");
  deviceDraft.unlock_pin = String(props.form.unlock_pin || "");
  deviceDraft.clearing_pin = props.clearSecrets.includes("unlock_pin");
  deviceDraft.stay_awake_usb = Boolean(props.form.stay_awake_usb ?? false);
  deviceDraft.wda_url = String(props.form.wda_url || "");
  deviceDraft.mcp_command = String(props.form.mcp_command || "npx -y @mobilenext/mobile-mcp@latest");
  editingDevice.value = true;
}

function cancelEditDevice(): void {
  editingDevice.value = false;
}

function commitEditing(): boolean {
  if (!editingDevice.value) return true;
  props.form.driver_backend = deviceDraft.driver_backend;
  props.form.device_id = deviceDraft.device_id.trim();
  props.form.vm_name = deviceDraft.vm_name.trim();
  props.form.vm_headless = deviceDraft.vm_headless;
  props.form.adb_address = deviceDraft.adb_address.trim();
  props.form.stay_awake_usb = deviceDraft.stay_awake_usb;
  props.form.wda_url = deviceDraft.wda_url.trim();
  props.form.mcp_command = deviceDraft.mcp_command.trim() || "npx -y @mobilenext/mobile-mcp@latest";

  const currentlyClearing = props.clearSecrets.includes("unlock_pin");
  if (deviceDraft.clearing_pin) {
    props.form.unlock_pin = "";
    if (!currentlyClearing) emit("toggle-clear", "unlock_pin");
  } else {
    if (currentlyClearing) emit("toggle-clear", "unlock_pin");
    props.form.unlock_pin = deviceDraft.unlock_pin;
  }
  editingDevice.value = false;
  emit("test-connection");
  return true;
}

function sourceChecked(source: string): boolean {
  const list = props.form.enabled_sources;
  return Array.isArray(list) && list.includes(source);
}

function toggleSource(source: string, event: Event): void {
  const checked = (event.target as HTMLInputElement).checked;
  const set = new Set<string>(Array.isArray(props.form.enabled_sources) ? props.form.enabled_sources.map(String) : []);
  if (checked) set.add(source);
  else set.delete(source);
  props.form.enabled_sources = sourceOptions.map((item) => item.value).filter((value) => set.has(value));
}

// 通知目标：复用 SubscriptionTargetsEditor，保存成换行分隔的 profile:group:id 或 profile:private:id。
const profiles = ref<BotProfileConfig[]>([]);
const targetRows = ref<RepositoryWatchTarget[]>([]);
let syncingTargets = false;

function parseTargetString(raw: string): RepositoryWatchTarget[] {
  const fallbackProfile = props.defaultProfile || profiles.value[0]?.id || "";
  return raw
    .split(/[\n,;，；]+/)
    .map((entry) => entry.trim())
    .filter(Boolean)
    .map((entry): RepositoryWatchTarget => {
      const parts = entry.split(":");
      if (parts.length >= 3 && (parts[1] === "group" || parts[1] === "private")) {
        const profileID = parts[0].trim() || fallbackProfile;
        const targetID = parts.slice(2).join(":").trim();
        return parts[1] === "private"
          ? { profile_id: profileID, destination: "private", user_id: targetID }
          : { profile_id: profileID, destination: "group", group_id: targetID };
      }
      return { profile_id: fallbackProfile, destination: "group", group_id: entry };
    });
}

function serializeTargets(rows: RepositoryWatchTarget[]): string {
  return rows
    .map((row) => {
      const id = (row.destination === "private" ? row.user_id : row.group_id)?.trim() || "";
      if (!id) return "";
      const profile = (row.profile_id || "").trim();
      if (row.destination === "private") {
        return `${profile}:private:${id}`;
      }
      return profile ? `${profile}:group:${id}` : id;
    })
    .filter(Boolean)
    .join("\n");
}

function onTargetsChanged(next: RepositoryWatchTarget[]): void {
  targetRows.value = next;
  syncingTargets = true;
  props.form.target_groups = serializeTargets(next);
  queueMicrotask(() => {
    syncingTargets = false;
  });
}

watch(
  () => String(props.form.target_groups ?? ""),
  (raw) => {
    if (syncingTargets) return;
    targetRows.value = parseTargetString(raw);
  },
  { immediate: true }
);

// 候选池与手动巡检
const poolState = ref<MobileSocialStateSnapshot | null>(null);
const loadingPool = ref(false);
const runningPatrol = ref(false);
const deletingID = ref("");

const combinedItems = computed(() => {
  const state = poolState.value;
  if (!state) return [];
  const pendingIds = new Set((state.pending_breaking ?? []).map((item) => item.id));
  const list: Array<{ item: MobileSocialCandidateItem; pending: boolean }> = [];
  for (const item of state.pending_breaking ?? []) {
    list.push({ item, pending: true });
  }
  for (const item of state.candidates ?? []) {
    if (!pendingIds.has(item.id)) list.push({ item, pending: false });
  }
  return list;
});

const poolHeaderHint = computed(() => {
  const state = poolState.value;
  if (!state) return "读取最近巡检筛选出的待发突发与日常话题候选。";
  if (state.last_error) return `最近巡检异常：${state.last_error}`;
  if (state.last_summary) {
    const at = state.last_patrol_at ? `（${formatUnixTime(state.last_patrol_at)}）` : "";
    return `最近巡检：${state.last_summary}${at}`;
  }
  return "尚未执行过巡检，可点右侧「立即巡检」试跑一轮。";
});

function sharedGroupCount(item: MobileSocialCandidateItem): number {
  return Object.keys(item.shared_groups ?? {}).length;
}

function formatUnixTime(unix: number): string {
  if (!unix) return "";
  return new Date(unix * 1000).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  });
}

async function loadPoolState(): Promise<void> {
  loadingPool.value = true;
  try {
    poolState.value = await getMobileSocialState();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "读取候选池失败");
  } finally {
    loadingPool.value = false;
  }
}

async function triggerPatrolNow(): Promise<void> {
  commitEditing();
  runningPatrol.value = true;
  try {
    const response = await runMobileSocialPatrol(props.buildPayload(), props.clearSecrets, props.defaultProfile || "");
    poolState.value = response.state;
    emit("runtime-updated");
    const r = response.result;
    toastSuccess(`巡检完成：抓取 ${r.fetched_count} 条，入选重磅 ${r.breaking_added} 条、日常话题 ${r.casual_added} 条`);
    if (props.tab !== "pool" && (r.breaking_added > 0 || r.casual_added > 0)) {
      emit("update:tab", "pool");
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "立即巡检失败");
  } finally {
    runningPatrol.value = false;
  }
}

async function removeCandidate(id: string): Promise<void> {
  deletingID.value = id;
  try {
    poolState.value = await deleteMobileSocialCandidate(id);
    emit("runtime-updated");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "移除失败");
  } finally {
    deletingID.value = "";
  }
}

watch(
  () => props.tab,
  (next) => {
    if (next === "pool") void loadPoolState();
  },
  { immediate: true }
);

onMounted(async () => {
  try {
    const config = await getBotProfileConfig();
    profiles.value = config.profiles ?? [];
    if (!syncingTargets) {
      targetRows.value = parseTargetString(String(props.form.target_groups ?? ""));
    }
  } catch {
    profiles.value = [];
  }
});

defineExpose({ commitEditing });
</script>

<style scoped>
.mobile-device-edit-grid,
.mobile-advanced-grid,
.mobile-two-col {
  gap: 12px;
}

.mobile-advanced-grid {
  margin-top: 10px;
}

.mobile-inline-switch {
  display: flex;
  align-items: center;
}

.mobile-card-status-msg {
  margin: 4px 0 0;
}

.mobile-candidate-summary {
  margin: 4px 0;
  font-size: 13px;
  color: var(--text-secondary);
}
</style>
