<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>群管理</h1>
        <p>查看机器人已加入的全部群，并按群配置触发词、专属人设与插件开关</p>
      </div>
      <div class="group-manual-add">
        <input
          v-model="newGroupID"
          class="input"
          inputmode="numeric"
          placeholder="群号"
          @keydown.enter="addGroup"
        />
        <button class="btn primary" type="button" :disabled="newGroupID.trim().length === 0" @click="addGroup">
          <Plus :size="15" aria-hidden="true" />
          添加群配置
        </button>
      </div>
    </header>

    <div v-if="loaded">
      <div class="group-list-toolbar">
        <div class="group-list-summary">
          <strong>{{ liveAvailable ? joinedCount : groups.length }}</strong>
          <span>{{ liveAvailable ? "个已加入群" : "个已保存配置" }}</span>
          <span class="muted">· {{ configuredCount }} 个独立配置</span>
        </div>
        <div class="group-list-actions">
          <label class="group-search">
            <Search :size="15" aria-hidden="true" />
            <input v-model="searchQuery" type="search" placeholder="搜索群名或群号" aria-label="搜索群名或群号" />
          </label>
          <button class="btn" type="button" :disabled="refreshing" title="从机器人同步最新群列表" @click="load(true)">
            <RefreshCw :size="14" :class="{ spin: refreshing }" aria-hidden="true" />
            {{ refreshing ? "同步中…" : "刷新群列表" }}
          </button>
        </div>
      </div>

      <div v-if="syncWarning" class="group-sync-warning" role="status">
        <WifiOff :size="16" aria-hidden="true" />
        <span>{{ syncWarning }}</span>
      </div>

      <div v-if="filteredGroups.length > 0" class="group-grid">
        <section v-for="group in filteredGroups" :key="group.group_id" class="group-card">
          <div class="group-card-head">
            <div class="group-identity">
              <img :src="group.avatar_url || groupAvatarURL(group.group_id)" :alt="group.group_name || `群 ${group.group_id}`" @error="hideBrokenAvatar" />
              <div class="group-identity-copy">
                <h2>{{ group.group_name || `群 ${group.group_id}` }}</h2>
                <span class="mono">{{ group.group_id }}</span>
              </div>
            </div>
            <label class="switch" :title="group.enabled ? '在本群启用' : '在本群停用'">
              <input
                type="checkbox"
                :checked="group.enabled"
                :disabled="togglingGroupID === group.group_id"
                @change="toggleGroup(group, $event)"
              />
              <span class="track" aria-hidden="true"></span>
            </label>
          </div>
          <div class="group-card-badges">
            <span v-if="liveAvailable" class="badge" :class="{ accent: group.joined }">{{ group.joined ? "已加入" : "当前未加入" }}</span>
            <span class="badge" :class="{ accent: group.configured }">{{ group.configured ? "已配置" : "跟随全局" }}</span>
            <span v-if="group.member_count" class="badge">
              <Users :size="12" aria-hidden="true" />
              {{ group.member_count }}<template v-if="group.max_member_count"> / {{ group.max_member_count }}</template>
            </span>
            <span v-if="group.configured && group.system_prompt" class="badge">专属人设</span>
            <span v-if="group.configured && overrideCount(group) > 0" class="badge">插件覆盖 {{ overrideCount(group) }}</span>
            <span v-if="group.configured && group.welcome_enabled" class="badge">入群欢迎</span>
            <span v-if="group.configured && group.reply_gate" class="badge">专属准入</span>
          </div>
          <p class="group-card-desc">
            {{ group.system_prompt ? truncate(group.system_prompt, 68) : group.configured ? "沿用全局人设与默认行为。" : "尚未设置群级覆盖，当前跟随全局配置。" }}
          </p>
          <div class="group-card-foot">
            <span class="muted">{{ group.enabled ? "机器人已启用" : "机器人已停用" }}</span>
            <button class="btn small" type="button" @click="openEditor(group, group.group_name)">
              <SlidersHorizontal :size="13" aria-hidden="true" />
              配置
            </button>
          </div>
        </section>
      </div>
      <EmptyState v-else-if="groups.length > 0" title="没有匹配的群" hint="换一个群名或群号搜索。" />
      <EmptyState
        v-else-if="syncWarning"
        title="暂时无法读取群列表"
        hint="机器人连接后点击刷新群列表；也可以先输入群号创建配置。"
      />
      <EmptyState v-else title="机器人还没有加入群" hint="机器人加入群后会自动显示在这里，也可以输入群号预先创建配置。" />
    </div>
    <div v-else class="group-grid">
      <div class="skeleton" style="height: 180px"></div>
      <div class="skeleton" style="height: 180px"></div>
    </div>

    <!-- 群配置编辑弹窗 -->
    <Modal v-if="editing" :title="`${editingGroupName || `群 ${editing.group_id}`} · 配置`" wide @close="editing = null">
      <div class="form-grid">
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">在本群启用机器人</span>
          </label>
        </div>
        <div class="field wide">
          <label for="group-triggers">本群触发词（逗号分隔，留空用全局）</label>
          <input id="group-triggers" v-model="triggersDraft" class="input" placeholder="嘉然,然然" />
        </div>
        <div class="field wide">
          <label for="group-prompt">本群专属人设（留空用全局系统提示词）</label>
          <textarea
            id="group-prompt"
            v-model="editing.system_prompt"
            class="textarea"
            rows="3"
            placeholder="同一个机器人可以在不同群扮演不同角色"
          ></textarea>
        </div>
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.welcome_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">开启入群欢迎</span>
          </label>
        </div>
        <div v-if="editing.welcome_enabled" class="field wide">
          <label for="group-welcome">欢迎语</label>
          <textarea id="group-welcome" v-model="editing.welcome_message" class="textarea" rows="2"></textarea>
        </div>
        <div class="field">
          <label for="group-context">上下文条数</label>
          <input id="group-context" v-model.number="editing.recent_context_limit" class="input" inputmode="numeric" />
        </div>
        <div class="field">
          <label for="group-maxreply">回复上限（字符）</label>
          <input id="group-maxreply" v-model.number="editing.max_reply_chars" class="input" inputmode="numeric" />
        </div>
        <div class="field wide">
          <label>本群准入条件</label>
          <ReplyGateForm v-model="editing.reply_gate" allow-inherit id-prefix="group-gate" :supports-group-level="supportsGroupLevel" />
        </div>
        <div class="field wide">
          <label>本群插件开关（未设置跟随全局）</label>
          <div class="row-list" style="margin-top: 6px">
            <div v-for="plugin in plugins" :key="plugin.manifest.id" class="row-item">
              <div class="row-main">
                <div class="row-title">{{ plugin.manifest.name }}</div>
                <div class="row-sub">全局：{{ plugin.enabled ? "已启用" : "已停用" }}</div>
              </div>
              <div class="segmented">
                <button type="button" :class="{ active: overrideOf(plugin.manifest.id) === undefined }" @click="setOverride(plugin.manifest.id, undefined)">
                  跟随
                </button>
                <button type="button" :class="{ active: overrideOf(plugin.manifest.id) === true }" @click="setOverride(plugin.manifest.id, true)">
                  开
                </button>
                <button type="button" :class="{ active: overrideOf(plugin.manifest.id) === false }" @click="setOverride(plugin.manifest.id, false)">
                  关
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
      <template #footer>
        <button class="btn ghost" type="button" @click="editing = null">取消</button>
        <button class="btn primary" type="button" :disabled="saving" @click="saveEditing">
          <Save :size="15" aria-hidden="true" />
          {{ saving ? "保存中…" : "保存" }}
        </button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { Plus, RefreshCw, Save, Search, SlidersHorizontal, Users, WifiOff } from "@lucide/vue";
import {
  getQQBotConfig,
  getQQBotPlatforms,
  listQQBotGroups,
  saveQQBotGroup,
  type PluginState,
  type QQBotGroupConfig,
  type QQBotGroupSummary
} from "../api";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";
import ReplyGateForm from "../components/ReplyGateForm.vue";
import { toastError, toastSuccess } from "../toast";

const groups = ref<QQBotGroupSummary[]>([]);
// 群等级只有 QQ 有；按当前激活的机器人平台决定要不要显示这一项。
const supportsGroupLevel = ref(true);
const plugins = ref<PluginState[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
const liveAvailable = ref(false);
const syncWarning = ref("");
const searchQuery = ref("");
const newGroupID = ref("");
const editing = ref<QQBotGroupConfig | null>(null);
const editingGroupName = ref("");
const triggersDraft = ref("");
const saving = ref(false);
const togglingGroupID = ref("");

const filteredGroups = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase();
  if (!query) {
    return groups.value;
  }
  return groups.value.filter((group) => group.group_id.includes(query) || group.group_name?.toLocaleLowerCase().includes(query));
});
const joinedCount = computed(() => groups.value.filter((group) => group.joined).length);
const configuredCount = computed(() => groups.value.filter((group) => group.configured).length);

function truncate(text: string, max: number): string {
  return text.length > max ? text.slice(0, max) + "…" : text;
}

function overrideCount(group: QQBotGroupConfig): number {
  return Object.keys(group.plugin_overrides ?? {}).length;
}

async function load(showFeedback = false): Promise<void> {
  refreshing.value = true;
  try {
    const response = await listQQBotGroups();
    groups.value = response.groups;
    plugins.value = response.plugins;
    liveAvailable.value = response.live_available;
    syncWarning.value = response.warning ?? "";
    if (showFeedback) {
      if (response.live_available) {
        toastSuccess(`已同步 ${response.groups.filter((group) => group.joined).length} 个群`);
      } else {
        toastError(response.warning ?? "暂时无法同步群列表");
      }
    }
    try {
      const [config, platformList] = await Promise.all([getQQBotConfig(), getQQBotPlatforms()]);
      const active = config.profiles?.find((item) => item.id === config.active_profile_id) ?? config.profiles?.[0];
      const def = platformList.platforms.find((item) => item.id === active?.platform);
      supportsGroupLevel.value = def ? def.protocol.startsWith("onebot") : true;
    } catch {
      // 拿不到平台信息时保守地把等级门槛显示出来。
      supportsGroupLevel.value = true;
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "读取群配置失败");
  } finally {
    loaded.value = true;
    refreshing.value = false;
  }
}

function addGroup(): void {
  const groupID = newGroupID.value.trim();
  if (!/^\d{5,12}$/.test(groupID)) {
    toastError("请输入正确的群号");
    return;
  }
  const existing = groups.value.find((group) => group.group_id === groupID);
  openEditor(
    existing ?? {
      group_id: groupID,
      enabled: true,
      group_triggers: [],
      plugin_overrides: {}
    },
    existing?.group_name
  );
  newGroupID.value = "";
}

function openEditor(group: QQBotGroupConfig, groupName = ""): void {
  // 深拷贝编辑，取消时不污染列表数据。
  editing.value = JSON.parse(JSON.stringify(groupConfigOf(group))) as QQBotGroupConfig;
  editingGroupName.value = groupName;
  triggersDraft.value = (group.group_triggers ?? []).join(",");
}

function groupConfigOf(group: QQBotGroupConfig): QQBotGroupConfig {
  const summary = group as Partial<QQBotGroupSummary>;
  const { group_name, avatar_url, member_count, max_member_count, configured, joined, ...config } = summary;
  return config as QQBotGroupConfig;
}

function groupAvatarURL(groupID: string): string {
  const encoded = encodeURIComponent(groupID.trim());
  return encoded ? `https://p.qlogo.cn/gh/${encoded}/${encoded}/640` : "";
}

function hideBrokenAvatar(event: Event): void {
  (event.currentTarget as HTMLImageElement).hidden = true;
}

function overrideOf(pluginID: string): boolean | undefined {
  return editing.value?.plugin_overrides?.[pluginID];
}

function setOverride(pluginID: string, value: boolean | undefined): void {
  if (!editing.value) {
    return;
  }
  const overrides = { ...(editing.value.plugin_overrides ?? {}) };
  if (value === undefined) {
    delete overrides[pluginID];
  } else {
    overrides[pluginID] = value;
  }
  editing.value.plugin_overrides = overrides;
}

async function toggleGroup(group: QQBotGroupSummary, event: Event): Promise<void> {
  const enabled = (event.target as HTMLInputElement).checked;
  togglingGroupID.value = group.group_id;
  try {
    const saved = await saveQQBotGroup({ ...groupConfigOf(group), enabled });
    upsert(saved.config);
    toastSuccess(enabled ? `群 ${group.group_id} 已启用` : `群 ${group.group_id} 已停用`);
  } catch (error) {
    (event.target as HTMLInputElement).checked = !enabled;
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    togglingGroupID.value = "";
  }
}

async function saveEditing(): Promise<void> {
  const current = editing.value;
  if (!current) {
    return;
  }
  saving.value = true;
  try {
    const payload: QQBotGroupConfig = {
      ...current,
      group_triggers: triggersDraft.value
        .split(/[,，]/)
        .map((item) => item.trim())
        .filter((item) => item !== "")
    };
    const saved = await saveQQBotGroup(payload);
    upsert(saved.config);
    editing.value = null;
    toastSuccess(`群 ${payload.group_id} 配置已保存`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    saving.value = false;
  }
}

function upsert(config: QQBotGroupConfig): void {
  const index = groups.value.findIndex((group) => group.group_id === config.group_id);
  if (index >= 0) {
    groups.value[index] = { ...groups.value[index], ...config, configured: true };
  } else {
    groups.value.push({
      ...config,
      avatar_url: groupAvatarURL(config.group_id),
      configured: true,
      joined: false
    });
  }
}

onMounted(() => load());
</script>
