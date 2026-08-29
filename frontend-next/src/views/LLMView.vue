<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

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

      <LLMOAuthPanel @changed="onOAuthProvidersChanged" />

    </div>

    <!-- 单配置连通测试弹窗 -->
    <Modal v-if="testTarget" :title="`测试 · ${testTarget.name || testTarget.model}`" @close="testTarget = null">
      <div class="stack" style="gap: 10px">
        <p class="muted" style="margin: 0; font-size: 12.5px">
          {{ providerLabel(testTarget.provider) }} · {{ testTarget.model }} · {{ groupLabel(groupOf(testTarget)) }}
        </p>
        <textarea
          v-model="testMessage"
          class="textarea"
          rows="3"
          :placeholder="isImageTest ? '描述要生成的测试图片…' : '输入一句话测试连通…'"
        ></textarea>
        <button class="btn primary" type="button" :disabled="busy || !testMessage.trim()" @click="runTest">
          <ImageIcon v-if="isImageTest" :size="15" aria-hidden="true" />
          <Send v-else :size="15" aria-hidden="true" />
          {{ busy ? (isImageTest ? "生成中…" : "请求中…") : (isImageTest ? "生成测试图片" : "发送测试") }}
        </button>
        <div v-if="testImages.length > 0" class="llm-test-images" aria-live="polite">
          <img
            v-for="(image, index) in testImages"
            :key="`${index}-${image.slice(0, 48)}`"
            :src="image"
            :alt="`生图测试结果 ${index + 1}`"
          />
        </div>
        <div v-if="testReply" class="event-reply" style="margin: 0">{{ testReply }}</div>
        <p v-if="testUsage" class="muted" style="font-size: 12px; margin: 0">{{ testUsage }}</p>
      </div>
    </Modal>

    <!-- 编辑弹窗 -->
    <Modal v-if="editorOpen" :title="editingID ? '编辑配置' : '新建配置'" wide @close="closeEditor">
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
          <label for="llm-credential-mode">凭据方式</label>
          <AppSelect
            id="llm-credential-mode"
            v-model="credentialMode"
            :options="credentialModeOptions"
          />
          <span class="hint">
            授权登录的令牌会在过期前自动续期；下拉里只列出「授权登录」区域已经登录过的提供商。
          </span>
        </div>
        <div v-if="credentialMode === 'api_key'" class="field wide">
          <label for="llm-apikey">API Key</label>
          <div class="input-group">
            <input
              id="llm-apikey"
              v-model="form.api_key"
              class="input"
              :type="showKey ? 'text' : 'password'"
              autocomplete="off"
              :placeholder="apiKeyPlaceholder"
            />
            <button class="btn icon-only" type="button" :aria-label="showKey ? '隐藏 Key' : '显示 Key'" @click="showKey = !showKey">
              <EyeOff v-if="showKey" :size="14" aria-hidden="true" />
              <Eye v-else :size="14" aria-hidden="true" />
            </button>
          </div>
        </div>
        <template v-else>
          <div class="field wide">
            <label for="llm-oauth-provider">授权提供商</label>
            <AppSelect
              id="llm-oauth-provider"
              v-model="form.oauth_provider"
              :options="oauthProviderOptions"
              placeholder="选择一个已登录的提供商"
            />
            <span v-if="oauthProviderOptions.length === 0" class="hint">
              还没有登录过任何提供商，先到下面的「授权登录」里登录一次。
            </span>
            <span v-else-if="selectedOAuthStatus?.expired && !selectedOAuthStatus?.refreshable" class="hint">
              这个提供商的登录已过期且无法自动续期，需要重新登录。
            </span>
          </div>
          <!-- 授权登录时 API Key 变成可选兜底：续期失败时还能靠它继续说话，
               比整个配置档一起哑掉好。 -->
          <div class="field wide">
            <label for="llm-apikey">备用 API Key（可选）</label>
            <div class="input-group">
              <input
                id="llm-apikey"
                v-model="form.api_key"
                class="input"
                :type="showKey ? 'text' : 'password'"
                autocomplete="off"
                :placeholder="apiKeyPlaceholder"
              />
              <button class="btn icon-only" type="button" :aria-label="showKey ? '隐藏 Key' : '显示 Key'" @click="showKey = !showKey">
                <EyeOff v-if="showKey" :size="14" aria-hidden="true" />
                <Eye v-else :size="14" aria-hidden="true" />
              </button>
            </div>
            <span class="hint">填了的话，授权令牌续期失败时会自动回落到它。</span>
          </div>
        </template>
        <div class="field wide model-config-field">
          <div class="model-sync-row">
            <div class="model-sync-copy">
              <span class="model-sync-title">模型列表</span>
              <span v-if="modelOptions.length > 0" class="hint">当前有 {{ modelOptions.length }} 个可用模型，可同步刷新或手动补充。</span>
              <span v-else class="hint">从服务同步模型列表，也可手动添加中转或自建模型 ID。</span>
            </div>
            <button class="btn" type="button" :disabled="modelsLoading" @click="loadModels(false)">
              <RefreshCw :size="14" aria-hidden="true" />
              {{ modelsLoading ? "同步中…" : "同步模型列表" }}
            </button>
          </div>
          <!-- 中转和自建 endpoint 常常不实现 /models，拉不到时得能手填，
               否则机器人页的「模型分配」只有默认模型一个可选。 -->
          <div class="model-manual">
            <div class="input-group">
              <input
                v-model="manualModelDraft"
                class="input"
                placeholder="手动添加模型 ID，多个用逗号或换行分隔"
                autocomplete="off"
                @keydown.enter.prevent="addManualModels"
              />
              <button class="btn" type="button" :disabled="manualModelDraft.trim() === ''" @click="addManualModels">
                <Plus :size="14" aria-hidden="true" />
                添加
              </button>
            </div>
            <div v-if="modelOptions.length > 0" class="model-chips">
              <span v-for="model in modelOptions" :key="model.id" class="model-chip">
                <span class="model-chip-id">{{ model.id }}</span>
                <button type="button" class="model-chip-remove" :title="`移除 ${model.id}`" @click="removeModel(model.id)">
                  <X :size="12" aria-hidden="true" />
                </button>
              </span>
            </div>
          </div>
          <details v-if="modelsError" class="request-error" open>
            <summary>模型列表获取失败，查看完整错误</summary>
            <pre>{{ modelsError }}</pre>
          </details>
          <div class="model-default-field">
            <label for="llm-model">默认模型（可选）</label>
            <div ref="modelFieldRef" class="model-picker-anchor">
              <input
                id="llm-model"
                v-model="form.model"
                class="input"
                placeholder="填写默认模型 ID，或从已同步列表中选择"
                autocomplete="off"
                @focus="openModelPicker"
                @input="openModelPicker"
                @keydown="onModelKeydown"
              />
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
              输入可筛选同步结果；留空保存时采用列表第一项，机器人页仍可为不同用途选择其他模型。
            </span>
            <span v-else class="hint">可直接填写模型 ID；留空时会先尝试同步模型列表。</span>
          </div>
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
          <label for="llm-window">模型上下文窗口</label>
          <input id="llm-window" v-model="form.context_window_tokens" class="input" inputmode="numeric" placeholder="跟随模型" />
          <span class="hint">
            只填你想强制覆盖的值。{{ effectiveContextHint }}
            <template v-if="contextWindowBindings.length > 0">在用这套配置的用途：</template>
          </span>
          <ul v-if="contextWindowBindings.length > 0" class="hint context-binding-list">
            <li v-for="line in contextWindowBindings" :key="line">{{ line }}</li>
          </ul>
        </div>
        <div class="field">
          <label for="llm-maxcontext">单次请求上下文上限</label>
          <input id="llm-maxcontext" v-model="form.max_context_tokens" class="input" inputmode="numeric" placeholder="跟随窗口" />
          <span class="hint">
            {{ effectiveMaxContextHint }}近期历史、长期记忆等预算都按它按比例分配，调小可以省钱，调大能记住更多对话。
          </span>
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
        <button class="btn ghost" type="button" @click="closeEditor">取消</button>
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
import LLMOAuthPanel from "../components/LLMOAuthPanel.vue";
import { ChevronDown, ChevronUp, Copy, Download, Eye, EyeOff, Image as ImageIcon, Pencil, Plus, RefreshCw, Save, Send, Trash2, Upload, X } from "@lucide/vue";
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
  testLLMImage,
  type LLMConfig,
  type LLMModelInfo,
  type Provider,
  type LLMOAuthStatus
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
  oauth_provider: string;
  user_agent: string;
  description: string;
  temperature: string;
  context_window_tokens: string;
  max_context_tokens: string;
  max_output_tokens: string;
}

const emptyForm: LLMFormState = {
  name: "",
  provider: "openai_compatible",
  api_style: "responses",
  group: "default",
  model: "",
  base_url: "",
  api_key: "",
  oauth_provider: "",
  user_agent: "",
  description: "",
  temperature: "",
  context_window_tokens: "",
  max_context_tokens: "",
  max_output_tokens: ""
};

const profileSet = ref<LLMConfig | null>(null);
const busy = ref(false);
const editorOpen = ref(false);
const editingID = ref<string | undefined>(undefined);
const editingConfigured = ref(false);
const editingKeyPreview = ref("");
// 正在编辑的那份配置的服务端回显，只用来读「当前实际生效的窗口」这类只读字段。
const editingProfile = ref<LLMConfig | null>(null);
const showKey = ref(false);
const form = ref<LLMFormState>({ ...emptyForm });
const selectedService = ref("openai");
const modelOptions = ref<LLMModelInfo[]>([]);
const manualModelDraft = ref("");
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
const testImages = ref<string[]>([]);
const isImageTest = computed(() => testTarget.value !== null && groupOf(testTarget.value) === "image");
const serviceOptions = llmServicePresets.map((preset) => ({
  value: preset.id,
  label: preset.label,
  hint: preset.hint
}));
const selectedPreset = computed(() => llmServicePresets.find((preset) => preset.id === selectedService.value));
// 凭据方式由配置档里有没有绑 OAuth 提供商推导，不额外存一个字段：
// 多存一个就有「两处不一致」的可能，而这里没有任何信息是推不出来的。
const credentialMode = computed<"api_key" | "oauth">({
  get: () => (form.value.oauth_provider ? "oauth" : "api_key"),
  set: (value) => {
    if (value === "api_key") {
      form.value.oauth_provider = "";
      return;
    }
    if (!form.value.oauth_provider) {
      form.value.oauth_provider = oauthProviderOptions.value[0]?.value ?? "";
    }
  }
});

const credentialModeOptions = [
  { value: "api_key", label: "API Key" },
  { value: "oauth", label: "授权登录" }
];

const oauthStatuses = ref<LLMOAuthStatus[]>([]);

// 只列已经登录过的：没登录的选了也用不了，让它出现在下拉里只会制造一次失败调用。
const oauthProviderOptions = computed(() =>
  oauthStatuses.value
    .filter((status) => status.logged_in)
    .map((status) => ({ value: status.provider.key, label: status.provider.label }))
);

const selectedOAuthStatus = computed(() =>
  oauthStatuses.value.find((status) => status.provider.key === form.value.oauth_provider)
);

function onOAuthProvidersChanged(next: LLMOAuthStatus[]) {
  oauthStatuses.value = next;
  // 绑着的提供商被退出登录或删掉后，把配置档上的引用一起清掉，
  // 否则这份配置会一直以「还没有登录」失败，而界面上看不出哪里不对。
  if (form.value.oauth_provider && !next.some((status) => status.provider.key === form.value.oauth_provider && status.logged_in)) {
    form.value.oauth_provider = "";
  }
}

const apiKeyPlaceholder = computed(() => {
  if (!editingConfigured.value) return "粘贴 API Key";
  return editingKeyPreview.value ? `已保存 ${editingKeyPreview.value}，留空则沿用` : "留空表示沿用已保存的 Key";
});

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
  editingKeyPreview.value = "";
  editingProfile.value = null;
  form.value = { ...emptyForm };
  selectedService.value = "openai";
  applyServicePreset("openai");
  modelOptions.value = [];
  modelsError.value = "";
  editorOpen.value = true;
}

const groupLabels: Record<string, string> = {
  default: "默认分组",
  embedding: "向量嵌入（语义检索）"
};

const groupOrder = ["default", "vision", "intent", "image", "embedding"];

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
  editingKeyPreview.value = profile.api_key_preview ?? "";
  editingProfile.value = profile;
  form.value = {
    name: profile.name ?? "",
    provider: profile.provider,
    api_style: profile.api_style ?? "responses",
    group: profile.group === "default" ? "" : (profile.group ?? ""),
    model: profile.model,
    base_url: profile.base_url ?? "",
    api_key: "",
    oauth_provider: profile.oauth_provider ?? "",
    user_agent: profile.user_agent ?? "",
    description: profile.description ?? "",
    temperature: profile.temperature === null || profile.temperature === undefined ? "" : String(profile.temperature),
    context_window_tokens: profile.context_window_tokens ? String(profile.context_window_tokens) : "",
    max_context_tokens: profile.max_context_tokens ? String(profile.max_context_tokens) : "",
    max_output_tokens: profile.max_output_tokens ? String(profile.max_output_tokens) : ""
  };
  selectedService.value = detectLLMService(profile.base_url);
  modelOptions.value = [...(profile.models ?? [])];
  modelsError.value = "";
  editorOpen.value = true;
}

// optionalTokenInput 把输入框翻译成「用户覆盖值」：留空是 0（自动），填了才是数字。
function optionalTokenInput(raw: string): number {
  const value = raw.trim();
  if (value === "" || Number.isNaN(Number(value))) {
    return 0;
  }
  return Number(value);
}



// 编辑器里这两个框留空是常态，所以要如实说明「留空时到底用多少、这个数哪来的」，
// 而不是把推断值预填进输入框冒充用户设置。
// 窗口只认手填：不填就是兜底值，不再按模型清单或模型名去猜。清单里的数只作参考。
const effectiveContextHint = computed(() => {
  const profile = editingProfile.value;
  const window = profile?.effective_context_window_tokens;
  if (profile?.context_window_source === "user" && window) {
    return `当前生效 ${window.toLocaleString("en-US")}，这套配置统一用它。`;
  }
  const fallback = window ? window.toLocaleString("en-US") : "内置兜底值";
  const reference = profile?.catalog_context_window_tokens
    ? `模型清单里 ${profile.model} 写的是 ${profile.catalog_context_window_tokens.toLocaleString("en-US")}，可以照着填。`
    : "";
  return `留空按 ${fallback} 计算，不会自动去猜模型的真实窗口。${reference}`;
});

// 这套配置被哪些用途在用：改窗口会一起影响它们，所以列出来。
const contextWindowBindings = computed(() =>
  (editingProfile.value?.role_bindings ?? []).map((binding) => {
    const owner = binding.bot_name ? `${binding.role_label}（${binding.bot_name}）` : binding.role_label;
    return `${owner}：${binding.model}`;
  })
);

const effectiveMaxContextHint = computed(() => {
  const budget = editingProfile.value?.effective_max_context_tokens;
  if (!budget) {
    return "留空即用满窗口。";
  }
  return `留空即用满窗口，当前为 ${budget.toLocaleString("en-US")}。`;
});

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
    oauth_provider: form.value.oauth_provider.trim() || undefined,
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
  // 这两个字段必须每次都提交：留空表示「改回按模型自动推断」，省略掉的话后端
  // 会当成「这个客户端没提交」而保留旧值，于是填过的数字永远删不掉。
  payload.context_window_tokens = optionalTokenInput(form.value.context_window_tokens);
  payload.max_context_tokens = optionalTokenInput(form.value.max_context_tokens);
  return payload;
}

// closeEditor 关闭编辑弹窗，并把「正在编辑哪一条」一起复位。
//
// 列表项的绿色边框绑的是 editingID：它表达的是「这条正在编辑中」。以前三个关闭
// 路径（保存成功、取消、右上角叉）都只把 editorOpen 置 false，editingID 留在原地，
// 于是编辑过的那条会一直亮着边框，直到点「新建配置」或刷新页面。更糟的是这个
// 高亮很容易被读成「当前启用的是这套」——那是完全不同的意思。
function closeEditor(): void {
  editorOpen.value = false;
  editingID.value = undefined;
  editingProfile.value = null;
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
    closeEditor();
    const savedProfile =
      saved.profiles?.find((profile) => profile.id === editingProfileID) ??
      saved.profiles?.[0] ??
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
    // 合并而不是覆盖：手动补充的模型不该被一次刷新冲掉。
    const merged = [...result.models];
    const fetched = new Set(result.models.map((model) => model.id));
    for (const model of modelOptions.value) {
      if (!fetched.has(model.id)) merged.push(model);
    }
    modelOptions.value = merged;
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

/** 手动补充模型：拉不到列表时也能让机器人页有多个模型可分配。 */
function addManualModels(): void {
  const ids = manualModelDraft.value
    .split(/[,，\s]+/)
    .map((item) => item.trim())
    .filter((item) => item !== "");
  if (ids.length === 0) {
    return;
  }
  const existing = new Set(modelOptions.value.map((model) => model.id));
  let added = 0;
  for (const id of ids) {
    if (existing.has(id)) continue;
    existing.add(id);
    modelOptions.value.push({ id });
    added++;
  }
  // 还没有默认模型时，第一个手填的就当默认。
  if (!form.value?.model.trim() && modelOptions.value.length > 0 && form.value) {
    form.value.model = modelOptions.value[0].id;
  }
  manualModelDraft.value = "";
  toastSuccess(added > 0 ? `已添加 ${added} 个模型` : "这些模型已在列表里");
}

function removeModel(id: string): void {
  modelOptions.value = modelOptions.value.filter((model) => model.id !== id);
  if (form.value?.model === id) {
    form.value.model = modelOptions.value[0]?.id ?? "";
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
  testImages.value = [];
}

async function runTest(): Promise<void> {
  const target = testTarget.value;
  busy.value = true;
  testReply.value = "";
  testUsage.value = "";
  testImages.value = [];
  try {
    // 带上目标配置（含 id），后端会自动复用该配置已保存的 API Key，无需先激活。
    if (target && groupOf(target) === "image") {
      const result = await testLLMImage(testMessage.value.trim(), target);
      testImages.value = result.images;
      testUsage.value = `${result.model || target.model} · 已生成 ${result.images.length} 张`;
      return;
    }
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

<style scoped>
/* 模型分配引用列表：跟在 hint 后面的一小段列表，排版继承 hint 的字号和颜色。 */
.context-binding-list {
  margin: 2px 0 0;
  padding-left: 16px;
  list-style: disc;
}
</style>
