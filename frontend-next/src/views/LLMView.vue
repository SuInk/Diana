<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>LLM 配置</h1>
        <p>管理 Provider、凭据、分组与可用模型；机器人按用途选择 Provider 和模型</p>
      </div>
      <div class="view-actions">
        <button class="btn" type="button" @click="exportProfiles">
          <Download :size="15" aria-hidden="true" />
          导出
        </button>
        <button class="btn" type="button" @click="importInputClick">
          <Upload :size="15" aria-hidden="true" />
          导入
        </button>
        <input ref="importInput" type="file" accept="application/json" style="display: none" @change="importProfiles" />
        <button class="btn primary" type="button" @click="startCreate">
          <Plus :size="15" aria-hidden="true" />
          新建配置
        </button>
      </div>
    </header>

    <div class="stack">
      <!-- 配置列表 -->
      <section class="card">
        <div class="card-header">
          <h2>配置列表</h2>
          <span v-if="profileSet" class="badge">{{ profiles.length }} 套</span>
        </div>
        <div class="card-body stack" style="gap: 14px">
          <div v-for="section in groupedProfiles" :key="section.group" class="stack" style="gap: 6px">
            <div class="group-header">
              <span>{{ groupLabel(section.group) }}</span>
              <span class="muted" style="font-size: 12px">
                {{ section.items.length }} 个 Provider · {{ sectionModelCount(section.items) }} 个模型 · 顺序即降级优先级
              </span>
            </div>
            <div class="row-list">
            <div
              v-for="profile in section.items"
              :key="profile.id ?? profile.name ?? profile.model"
              class="row-item"
              :class="{ selected: editingID === profile.id }"
            >
              <div class="row-main">
                <div class="row-title">
                  {{ profile.name || profile.model }}
                </div>
                <div class="row-sub">
                  {{ providerLabel(profile.provider) }} · 默认 {{ profile.model || "未选择" }} · {{ profileModelCount(profile) }} 个模型
                  <template v-if="profile.description"> · {{ profile.description }}</template>
                </div>
              </div>
              <div class="row-actions">
                <span class="cluster" style="gap: 2px" title="组内顺序即失败降级优先级">
                  <button class="btn small ghost icon-only" type="button" :disabled="busy || !canMove(profile, -1)" @click="move(profile, -1)">
                    <ChevronUp :size="13" aria-hidden="true" />
                  </button>
                  <button class="btn small ghost icon-only" type="button" :disabled="busy || !canMove(profile, 1)" @click="move(profile, 1)">
                    <ChevronDown :size="13" aria-hidden="true" />
                  </button>
                </span>
                <button class="btn small ghost" type="button" title="测试这套配置" @click="openTest(profile)">
                  <Send :size="13" aria-hidden="true" />
                </button>
                <button class="btn small ghost" type="button" @click="startEdit(profile)">
                  <Pencil :size="13" aria-hidden="true" />
                </button>
                <button class="btn small ghost" type="button" :disabled="busy" @click="clone(profile)" title="克隆">
                  <Copy :size="13" aria-hidden="true" />
                </button>
                <button
                  class="btn small ghost danger"
                  type="button"
                  :disabled="busy || profiles.length <= 1"
                  title="删除"
                  @click="remove(profile)"
                >
                  <Trash2 :size="13" aria-hidden="true" />
                </button>
              </div>
            </div>
            </div>
          </div>
          <EmptyState v-if="profiles.length === 0" title="还没有 LLM 配置" hint="点击右上角「新建配置」开始" />
        </div>
      </section>

    </div>

    <!-- 单配置连通测试弹窗 -->
    <Modal v-if="testTarget" :title="`测试 · ${testTarget.name || testTarget.model}`" @close="testTarget = null">
      <div class="stack" style="gap: 10px">
        <p class="muted" style="margin: 0; font-size: 12.5px">
          {{ providerLabel(testTarget.provider) }} · {{ testTarget.model }} · {{ groupLabel(groupOf(testTarget)) }}
        </p>
        <textarea v-model="testMessage" class="textarea" rows="3" placeholder="输入一句话测试连通…"></textarea>
        <button class="btn primary" type="button" :disabled="busy || !testMessage.trim()" @click="runTest">
          <Send :size="15" aria-hidden="true" />
          {{ busy ? "请求中…" : "发送测试" }}
        </button>
        <div v-if="testReply" class="event-reply" style="margin: 0">{{ testReply }}</div>
        <p v-if="testUsage" class="muted" style="font-size: 12px; margin: 0">{{ testUsage }}</p>
      </div>
    </Modal>

    <!-- 编辑弹窗 -->
    <Modal v-if="editorOpen" :title="editingID ? '编辑配置' : '新建配置'" wide @close="editorOpen = false">
      <div class="form-grid">
        <div class="field">
          <label for="llm-name">名称</label>
          <input id="llm-name" v-model="form.name" class="input" placeholder="例如 主力 · GPT" />
        </div>
        <div class="field">
          <label for="llm-channel-group">渠道分组（可选）</label>
          <input id="llm-channel-group" v-model="form.group" class="input" placeholder="例如 主力、备用" />
          <span class="hint">同分组的渠道自动轮换降级；机器人页可把用途绑到整个分组。</span>
        </div>
        <div class="field">
          <label for="llm-service">服务平台</label>
          <AppSelect
            id="llm-service"
            :model-value="selectedService"
            :options="serviceOptions"
            @update:model-value="applyServicePreset"
          />
        </div>
        <div class="field">
          <label for="llm-api-style">接口模式</label>
          <AppSelect
            id="llm-api-style"
            v-model="form.api_style"
            :options="[
              { value: 'chat_completions', label: 'Chat Completions' },
              { value: 'responses', label: 'Responses API' }
            ]"
          />
          <span class="hint">平台预设会选择推荐模式，也可按服务实际支持情况切换。</span>
        </div>
        <div class="field wide">
          <label for="llm-baseurl">API 地址</label>
          <input id="llm-baseurl" v-model="form.base_url" class="input mono" placeholder="https://api.example.com/v1" />
          <span class="hint">{{ selectedPreset?.hint }}；请填写完整 API 根地址，包括服务要求的 `/v1` 等路径。</span>
        </div>
        <div class="field wide">
          <label for="llm-apikey">API Key</label>
          <div class="input-group">
            <input
              id="llm-apikey"
              v-model="form.api_key"
              class="input"
              :type="showKey ? 'text' : 'password'"
              autocomplete="off"
              :placeholder="editingConfigured ? '留空表示沿用已保存的 Key' : '粘贴 API Key'"
            />
            <button class="btn icon-only" type="button" :aria-label="showKey ? '隐藏 Key' : '显示 Key'" @click="showKey = !showKey">
              <EyeOff v-if="showKey" :size="14" aria-hidden="true" />
              <Eye v-else :size="14" aria-hidden="true" />
            </button>
          </div>
        </div>
        <div class="field wide">
          <label for="llm-model">默认模型（可选）</label>
          <div ref="modelFieldRef" class="input-group model-picker-anchor">
            <input
              id="llm-model"
              v-model="form.model"
              class="input"
              placeholder="留空将自动获取模型列表"
              autocomplete="off"
              @focus="openModelPicker"
              @input="openModelPicker"
              @keydown="onModelKeydown"
            />
            <button class="btn" type="button" :disabled="modelsLoading" @click="loadModels(false)">
              <RefreshCw :size="14" aria-hidden="true" />
              {{ modelsLoading ? "获取中…" : modelOptions.length > 0 ? "刷新列表" : "获取模型列表" }}
            </button>
            <div v-if="modelPickerOpen && modelOptions.length > 0" class="model-picker">
              <div class="model-picker-meta">
                <span>
                  共 {{ modelOptions.length }} 个模型<template v-if="form.model.trim() && filteredModels.length < modelOptions.length"
                    >，匹配 {{ filteredModels.length }} 个</template
                  >
                </span>
                <button class="btn ghost small" type="button" @click="modelPickerOpen = false">收起</button>
              </div>
              <p v-if="form.model.trim() && filteredModels.length === 0" class="model-picker-empty">
                没有包含「{{ form.model.trim() }}」的模型，已显示全部
              </p>
              <ul class="model-picker-list">
                <li v-for="model in displayModels" :key="model.id">
                  <button
                    type="button"
                    class="model-picker-item"
                    :class="{ active: model.id === form.model }"
                    @mousedown.prevent="pickModel(model.id)"
                  >
                    {{ model.id }}
                  </button>
                </li>
              </ul>
            </div>
          </div>
          <span v-if="modelOptions.length > 0" class="hint">
            已保存 {{ modelOptions.length }} 个可用模型；这里仅选择默认项，机器人页可为不同用途选择同一 Provider 下的其他模型。
          </span>
          <span v-else class="hint">填写 API Key 后获取完整模型列表；不选择默认项时保存会采用列表第一项。</span>
          <details v-if="modelsError" class="request-error" open>
            <summary>模型列表获取失败，查看完整错误</summary>
            <pre>{{ modelsError }}</pre>
          </details>
        </div>
        <div class="field">
          <label for="llm-temp">Temperature（可选）</label>
          <input id="llm-temp" v-model="form.temperature" class="input" inputmode="decimal" placeholder="0.7" />
        </div>
        <div class="field">
          <label for="llm-maxtokens">最大输出 Token</label>
          <input id="llm-maxtokens" v-model="form.max_output_tokens" class="input" inputmode="numeric" placeholder="1024" />
        </div>
        <div class="field">
          <label for="llm-timeout">超时（毫秒）</label>
          <input id="llm-timeout" v-model="form.timeout_ms" class="input" inputmode="numeric" placeholder="30000" />
        </div>
        <div v-if="form.provider === 'openai_compatible'" class="field">
          <label for="llm-ua">User-Agent（可选）</label>
          <input id="llm-ua" v-model="form.user_agent" class="input" placeholder="codex-cli/0.142.0" />
        </div>
        <div class="field wide">
          <label for="llm-desc">备注（可选）</label>
          <input id="llm-desc" v-model="form.description" class="input" placeholder="这套配置的用途" />
        </div>
      </div>
      <template #footer>
        <button class="btn ghost" type="button" @click="editorOpen = false">取消</button>
        <button class="btn primary" type="button" :disabled="busy" @click="save">
          <Save :size="15" aria-hidden="true" />
          保存
        </button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ChevronDown, ChevronUp, Copy, Download, Eye, EyeOff, Pencil, Plus, RefreshCw, Save, Send, Trash2, Upload } from "@lucide/vue";
import {
  cloneConfigProfile,
  deleteConfigProfile,
  exportConfig,
  reorderConfigProfiles,
  getConfig,
  importConfigProfiles,
  listLLMModels,
  saveConfig,
  testLLM,
  type LLMConfig,
  type LLMModelInfo,
  type Provider
} from "../api";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import Modal from "../components/Modal.vue";
import AppSelect from "../components/AppSelect.vue";
import EmptyState from "../components/EmptyState.vue";
import { detectLLMService, llmServicePresets } from "../llm-presets";

interface LLMFormState {
  name: string;
  provider: Provider;
  api_style: "responses" | "chat_completions";
  group: string;
  model: string;
  base_url: string;
  api_key: string;
  user_agent: string;
  description: string;
  temperature: string;
  max_output_tokens: string;
  timeout_ms: string;
}

const emptyForm: LLMFormState = {
  name: "",
  provider: "openai_compatible",
  api_style: "responses",
  group: "default",
  model: "",
  base_url: "",
  api_key: "",
  user_agent: "",
  description: "",
  temperature: "",
  max_output_tokens: "",
  timeout_ms: ""
};

const profileSet = ref<LLMConfig | null>(null);
const busy = ref(false);
const editorOpen = ref(false);
const editingID = ref<string | undefined>(undefined);
const editingConfigured = ref(false);
const showKey = ref(false);
const form = ref<LLMFormState>({ ...emptyForm });
const selectedService = ref("openai");
const modelOptions = ref<LLMModelInfo[]>([]);
const modelsLoading = ref(false);
const modelsError = ref("");
const modelPickerOpen = ref(false);
const modelFieldRef = ref<HTMLElement | null>(null);
const importInput = ref<HTMLInputElement | null>(null);

// 按子串（而不是浏览器 datalist 的前缀规则）筛选模型，输入任意片段都能命中。
const filteredModels = computed<LLMModelInfo[]>(() => {
  const keyword = form.value.model.trim().toLowerCase();
  if (!keyword) {
    return modelOptions.value;
  }
  // 输入框里已经是完整模型 ID（选完/填完）时不再过滤：
  // 重新打开或刷新列表仍能浏览全部模型换选，控件不因已有内容失效。
  if (modelOptions.value.some((model) => model.id.toLowerCase() === keyword)) {
    return modelOptions.value;
  }
  return modelOptions.value.filter((model) => model.id.toLowerCase().includes(keyword));
});

// 筛选空手时兜底显示全部，保证拉取结果永远可浏览、可点选。
const displayModels = computed<LLMModelInfo[]>(() =>
  filteredModels.value.length > 0 ? filteredModels.value : modelOptions.value
);

const testMessage = ref("");
const testTarget = ref<LLMConfig | null>(null);
const testReply = ref("");
const testUsage = ref("");
const serviceOptions = llmServicePresets.map((preset) => ({
  value: preset.id,
  label: preset.label,
  hint: preset.hint
}));
const selectedPreset = computed(() => llmServicePresets.find((preset) => preset.id === selectedService.value));

function applyServicePreset(id: string): void {
  const preset = llmServicePresets.find((item) => item.id === id);
  if (!preset) return;
  selectedService.value = id;
  form.value.provider = preset.provider;
  form.value.api_style = preset.apiStyle;
  form.value.base_url = preset.baseURL;
  form.value.model = preset.model;
  modelOptions.value = [];
  modelsError.value = "";
  modelPickerOpen.value = false;
}

const profiles = computed<LLMConfig[]>(() => profileSet.value?.profiles ?? []);

function providerLabel(provider: Provider): string {
  const labels: Record<Provider, string> = {
    openai_compatible: "OpenAI 兼容",
    gemini: "Gemini",
    anthropic: "Anthropic"
  };
  return labels[provider];
}

async function reload(): Promise<void> {
  profileSet.value = await getConfig();
}

function startCreate(): void {
  editingID.value = undefined;
  editingConfigured.value = false;
  form.value = { ...emptyForm };
  selectedService.value = "openai";
  applyServicePreset("openai");
  modelOptions.value = [];
  modelsError.value = "";
  editorOpen.value = true;
}

const groupLabels: Record<string, string> = {
  default: "默认分组"
};

const groupOrder = ["default", "vision", "intent", "image"];

// 配置列表按用途分节展示；已知用途按固定顺序排前，自定义分组跟在后面。
const groupedProfiles = computed<{ group: string; items: LLMConfig[] }[]>(() => {
  const sections = new Map<string, LLMConfig[]>();
  for (const profile of profiles.value) {
    const group = groupOf(profile);
    if (!sections.has(group)) {
      sections.set(group, []);
    }
    sections.get(group)!.push(profile);
  }
  const keys = [...sections.keys()].sort((a, b) => {
    const ia = groupOrder.indexOf(a);
    const ib = groupOrder.indexOf(b);
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
  });
  return keys.map((group) => ({ group, items: sections.get(group)! }));
});

function groupOf(profile: LLMConfig): string {
  return profile.group?.trim() || "default";
}

function groupLabel(group: string): string {
  return groupLabels[group] ?? group;
}

function profileModelCount(profile: LLMConfig): number {
  const ids = new Set((profile.models ?? []).map((model) => model.id).filter(Boolean));
  if (profile.model) ids.add(profile.model);
  return ids.size;
}

function sectionModelCount(items: LLMConfig[]): number {
  const ids = new Set<string>();
  for (const profile of items) {
    for (const model of profile.models ?? []) ids.add(model.id);
    if (profile.model) ids.add(profile.model);
  }
  return ids.size;
}

function defaultTestMessage(profile: LLMConfig): string {
  return groupOf(profile) === "image" ? "生成小猫" : "hi";
}

// 同用途组内上下移动；组内顺序就是失败降级的优先级。
function sameGroupSiblings(profile: LLMConfig): LLMConfig[] {
  return profiles.value.filter((item) => groupOf(item) === groupOf(profile));
}

function canMove(profile: LLMConfig, delta: number): boolean {
  const siblings = sameGroupSiblings(profile);
  const index = siblings.findIndex((item) => item.id === profile.id);
  const target = index + delta;
  return index >= 0 && target >= 0 && target < siblings.length;
}

async function move(profile: LLMConfig, delta: number): Promise<void> {
  const siblings = sameGroupSiblings(profile);
  const index = siblings.findIndex((item) => item.id === profile.id);
  const target = index + delta;
  if (index < 0 || target < 0 || target >= siblings.length) {
    return;
  }
  // 在全量列表里交换两个同组条目的位置，然后按新顺序提交。
  const ids = profiles.value.map((item) => item.id ?? "");
  const from = ids.indexOf(profile.id ?? "");
  const to = ids.indexOf(siblings[target].id ?? "");
  [ids[from], ids[to]] = [ids[to], ids[from]];
  busy.value = true;
  try {
    profileSet.value = await reorderConfigProfiles(ids);
    toastSuccess("优先级已调整");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "调整失败");
  } finally {
    busy.value = false;
  }
}

function startEdit(profile: LLMConfig): void {
  editingID.value = profile.id;
  editingConfigured.value = Boolean(profile.api_key_configured);
  form.value = {
    name: profile.name ?? "",
    provider: profile.provider,
    api_style: profile.api_style ?? "responses",
    group: profile.group === "default" ? "" : (profile.group ?? ""),
    model: profile.model,
    base_url: profile.base_url ?? "",
    api_key: "",
    user_agent: profile.user_agent ?? "",
    description: profile.description ?? "",
    temperature: profile.temperature === null || profile.temperature === undefined ? "" : String(profile.temperature),
    max_output_tokens: profile.max_output_tokens ? String(profile.max_output_tokens) : "",
    timeout_ms: profile.timeout_ms ? String(profile.timeout_ms) : ""
  };
  selectedService.value = detectLLMService(profile.base_url);
  modelOptions.value = [...(profile.models ?? [])];
  modelsError.value = "";
  editorOpen.value = true;
}

function formToPayload(): LLMConfig {
  const payload: LLMConfig = {
    id: editingID.value,
    name: form.value.name.trim() || undefined,
    provider: form.value.provider,
    api_style: form.value.api_style,
    group: form.value.group.trim() || "default",
    model: form.value.model.trim(),
    base_url: form.value.base_url.trim() || undefined,
    api_key: form.value.api_key.trim() || undefined,
    models: modelOptions.value,
    user_agent: form.value.user_agent.trim() || undefined,
    description: form.value.description.trim() || undefined
  };
  const temperature = form.value.temperature.trim();
  if (temperature !== "" && !Number.isNaN(Number(temperature))) {
    payload.temperature = Number(temperature);
  }
  const maxTokens = form.value.max_output_tokens.trim();
  if (maxTokens !== "" && !Number.isNaN(Number(maxTokens))) {
    payload.max_output_tokens = Number(maxTokens);
  }
  const timeout = form.value.timeout_ms.trim();
  if (timeout !== "" && !Number.isNaN(Number(timeout))) {
    payload.timeout_ms = Number(timeout);
  }
  return payload;
}

async function save(): Promise<void> {
  if (!form.value.model.trim()) {
    const resolved = await loadModels(true);
    if (!resolved) return;
  }
  busy.value = true;
  try {
    const editingProfileID = editingID.value;
    const saved = await saveConfig(formToPayload());
    profileSet.value = saved;
    editorOpen.value = false;
    const savedProfile =
      saved.profiles?.find((profile) => profile.id === editingProfileID) ??
      saved.profiles?.find((profile) => profile.id === saved.active_profile_id) ??
      saved;
    openTest(savedProfile);
    toastSuccess("配置已保存，请完成连通测试");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    busy.value = false;
  }
}


async function clone(profile: LLMConfig): Promise<void> {
  if (!profile.id) {
    return;
  }
  busy.value = true;
  try {
    profileSet.value = await cloneConfigProfile(profile.id);
    toastSuccess("已克隆配置");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "克隆失败");
  } finally {
    busy.value = false;
  }
}

async function remove(profile: LLMConfig): Promise<void> {
  if (!profile.id) {
    return;
  }
  const ok = await askConfirm({
    title: "删除 LLM 配置",
    message: `确定删除「${profile.name || profile.model}」吗？此操作不可撤销。`,
    confirmLabel: "删除",
    danger: true
  });
  if (!ok) {
    return;
  }
  busy.value = true;
  try {
    profileSet.value = await deleteConfigProfile(profile.id);
    toastSuccess("配置已删除");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "删除失败");
  } finally {
    busy.value = false;
  }
}

async function loadModels(selectFirst: boolean): Promise<boolean> {
  if (modelsLoading.value) return false;
  modelsError.value = "";
  modelsLoading.value = true;
  try {
    const payload = formToPayload();
    const result = await listLLMModels(payload);
    modelOptions.value = result.models;
    if (result.models.length === 0) {
      toastError("该 Provider 未返回模型列表");
      return false;
    } else {
      if (selectFirst && !form.value.model.trim()) {
        form.value.model = result.models[0].id;
      }
      // 拉取成功立刻展开列表，无论输入框里是否已有文字。
      modelPickerOpen.value = !selectFirst;
    }
    return true;
  } catch (error) {
    modelsError.value = error instanceof Error ? error.message : "拉取模型失败";
    toastError("模型列表获取失败，完整信息已显示在模型字段下方");
    return false;
  } finally {
    modelsLoading.value = false;
  }
}

function openModelPicker(): void {
  if (modelOptions.value.length > 0) {
    modelPickerOpen.value = true;
  } else if (!form.value.model.trim() && (form.value.api_key.trim() || editingConfigured.value)) {
    void loadModels(false);
  }
}

function pickModel(id: string): void {
  form.value.model = id;
  modelPickerOpen.value = false;
}

// Esc 在列表展开时只收起列表，不触发 Modal 的关闭。
function onModelKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape" && modelPickerOpen.value) {
    event.stopPropagation();
    modelPickerOpen.value = false;
  }
}

// 点击模型字段以外的区域时收起列表。
function onDocumentPointerDown(event: PointerEvent): void {
  if (!modelPickerOpen.value) {
    return;
  }
  if (modelFieldRef.value && !modelFieldRef.value.contains(event.target as Node)) {
    modelPickerOpen.value = false;
  }
}

function openTest(profile: LLMConfig): void {
  testTarget.value = profile;
  testMessage.value = defaultTestMessage(profile);
  testReply.value = "";
  testUsage.value = "";
}

async function runTest(): Promise<void> {
  const target = testTarget.value;
  busy.value = true;
  testReply.value = "";
  testUsage.value = "";
  try {
    // 带上目标配置（含 id），后端会自动复用该配置已保存的 API Key，无需先激活。
    const result = await testLLM(testMessage.value.trim(), target ?? undefined);
    testReply.value = result.text;
    if (result.usage) {
      testUsage.value = `输入 ${result.usage.input_tokens ?? 0} / 输出 ${result.usage.output_tokens ?? 0} tokens`;
    }
  } catch (error) {
    toastError(error instanceof Error ? error.message : "测试失败");
  } finally {
    busy.value = false;
  }
}

async function exportProfiles(): Promise<void> {
  try {
    const data = await exportConfig();
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `diana-llm-profiles-${new Date().toISOString().slice(0, 10)}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
    toastSuccess("已导出（包含密钥，请妥善保管）");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "导出失败");
  }
}

function importInputClick(): void {
  importInput.value?.click();
}

async function importProfiles(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) {
    return;
  }
  try {
    const text = await file.text();
    const parsed = JSON.parse(text) as LLMConfig;
    profileSet.value = await importConfigProfiles({
      active_profile_id: parsed.active_profile_id,
      profiles: parsed.profiles
    });
    toastSuccess("配置已导入");
  } catch (error) {
    toastError(error instanceof Error ? error.message : "导入失败：文件格式不正确");
  }
}

onMounted(() => {
  document.addEventListener("pointerdown", onDocumentPointerDown);
  void reload().catch((error: unknown) => {
    toastError(error instanceof Error ? error.message : "加载配置失败");
  });
});

onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", onDocumentPointerDown);
});
</script>
