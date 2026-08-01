<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>群管理</h1>
        <p>按群配置触发词、专属人设与插件开关；登录控制台即可直接管理</p>
      </div>
      <div class="cluster" style="gap: 8px">
        <input
          v-model="newGroupID"
          class="input"
          style="width: 160px"
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
      <div v-if="groups.length > 0" class="plugin-grid">
        <section v-for="group in groups" :key="group.group_id" class="plugin-card">
          <div class="plugin-card-head">
            <h2 class="mono">{{ group.group_id }}</h2>
            <label class="switch" :title="group.enabled ? '在本群启用' : '在本群停用'">
              <input type="checkbox" :checked="group.enabled" @change="toggleGroup(group, $event)" />
              <span class="track" aria-hidden="true"></span>
            </label>
          </div>
          <div class="plugin-card-badges">
            <span v-if="group.system_prompt" class="badge accent">专属人设</span>
            <span v-if="(group.group_triggers?.length ?? 0) > 0" class="badge">触发词 {{ group.group_triggers?.length }}</span>
            <span v-if="overrideCount(group) > 0" class="badge">插件覆盖 {{ overrideCount(group) }}</span>
            <span v-if="group.welcome_enabled" class="badge">入群欢迎</span>
          </div>
          <p class="plugin-card-desc">
            {{ group.system_prompt ? truncate(group.system_prompt, 60) : "沿用全局人设与默认行为。" }}
          </p>
          <div class="plugin-card-foot">
            <button class="btn small" type="button" @click="openEditor(group)">
              <SlidersHorizontal :size="13" aria-hidden="true" />
              设置
            </button>
          </div>
        </section>
      </div>
      <EmptyState v-else title="还没有群配置" hint="输入群号添加第一个群，配置会在机器人进群消息时生效。" />
    </div>
    <div v-else class="plugin-grid">
      <div class="skeleton" style="height: 180px"></div>
      <div class="skeleton" style="height: 180px"></div>
    </div>

    <!-- 群配置编辑弹窗 -->
    <Modal v-if="editing" :title="`群 ${editing.group_id} · 配置`" wide @close="editing = null">
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
import { onMounted, ref } from "vue";
import { Plus, Save, SlidersHorizontal } from "@lucide/vue";
import { listQQBotGroups, saveQQBotGroup, type PluginState, type QQBotGroupConfig } from "../api";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";
import { toastError, toastSuccess } from "../toast";

const groups = ref<QQBotGroupConfig[]>([]);
const plugins = ref<PluginState[]>([]);
const loaded = ref(false);
const newGroupID = ref("");
const editing = ref<QQBotGroupConfig | null>(null);
const triggersDraft = ref("");
const saving = ref(false);

function truncate(text: string, max: number): string {
  return text.length > max ? text.slice(0, max) + "…" : text;
}

function overrideCount(group: QQBotGroupConfig): number {
  return Object.keys(group.plugin_overrides ?? {}).length;
}

async function load(): Promise<void> {
  try {
    const response = await listQQBotGroups();
    groups.value = response.groups;
    plugins.value = response.plugins;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "读取群配置失败");
  } finally {
    loaded.value = true;
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
    }
  );
  newGroupID.value = "";
}

function openEditor(group: QQBotGroupConfig): void {
  // 深拷贝编辑，取消时不污染列表数据。
  editing.value = JSON.parse(JSON.stringify(group)) as QQBotGroupConfig;
  triggersDraft.value = (group.group_triggers ?? []).join(",");
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

async function toggleGroup(group: QQBotGroupConfig, event: Event): Promise<void> {
  const enabled = (event.target as HTMLInputElement).checked;
  try {
    const saved = await saveQQBotGroup({ ...group, enabled });
    upsert(saved.config);
    toastSuccess(enabled ? `群 ${group.group_id} 已启用` : `群 ${group.group_id} 已停用`);
  } catch (error) {
    (event.target as HTMLInputElement).checked = !enabled;
    toastError(error instanceof Error ? error.message : "保存失败");
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
    groups.value[index] = config;
  } else {
    groups.value.push(config);
  }
}

onMounted(load);
</script>
