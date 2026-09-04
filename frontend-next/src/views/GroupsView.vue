<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>群管理</h1>
        <p>查看机器人已加入的全部群，并按群配置回复时间、屏蔽账号、专属人设与插件开关</p>
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
              <img v-if="group.avatar_url" :src="group.avatar_url" :alt="group.group_name || `群 ${group.group_id}`" @error="hideBrokenAvatar" />
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
            <span v-if="group.configured && group.response_mode" class="badge accent">{{ responseModeLabel(group.response_mode) }}</span>
            <span v-if="group.configured && group.reply_style" class="badge">{{ replyStyleLabel(group.reply_style) }}</span>
            <span v-if="group.configured && overrideCount(group) > 0" class="badge">插件覆盖 {{ overrideCount(group) }}</span>
            <span v-if="group.configured && group.welcome_enabled" class="badge">入群欢迎</span>
            <span v-if="group.configured && group.natural_interjection_enabled" class="badge accent">自然插话</span>
            <span v-if="group.configured && group.social_reply_enabled" class="badge accent">社交性回应</span>
            <span v-if="group.configured && group.reply_gate?.active_hours_enabled" class="badge">
              回复 {{ group.reply_gate.active_start }}–{{ group.reply_gate.active_end }}
            </span>
            <span v-if="group.configured && group.recall_reply_auto_delete_enabled" class="badge">
              撤回回复保留 {{ group.recall_reply_auto_delete_delay_seconds ?? defaultRecallReplyAutoDeleteDelaySeconds }} 秒
            </span>
            <span v-if="group.configured && blockedUserCount(group) > 0" class="badge">屏蔽 {{ blockedUserCount(group) }} 人</span>
            <span v-if="group.configured && hasOtherReplyGateRules(group)" class="badge">专属回复规则</span>
          </div>
          <p class="group-card-desc">
            {{ group.system_prompt ? truncate(group.system_prompt, 68) : group.configured ? "沿用全局人设与默认行为。" : "尚未设置群级覆盖，当前跟随全局配置。" }}
          </p>
          <div class="group-card-foot">
            <span class="muted">{{ group.enabled ? "机器人已启用" : "机器人已停用" }}</span>
            <div class="cluster" style="gap: 8px">
              <button class="btn small ghost" type="button" @click="openRelations(group, group.group_name)">
                <Share2 :size="13" aria-hidden="true" />
                关系图
              </button>
              <button class="btn small" type="button" @click="openEditor(group, group.group_name)">
                <SlidersHorizontal :size="13" aria-hidden="true" />
                配置
              </button>
            </div>
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
    <Modal
      v-if="relationsGroupID"
      :title="`${relationsGroupName || `群 ${relationsGroupID}`} · 关系图`"
      wide
      @close="closeRelations"
    >
      <div class="stack" style="gap: 12px">
        <div class="segmented" role="radiogroup" aria-label="按时间范围统计关系">
          <button
            v-for="option in relationRangeOptions"
            :key="option.value"
            type="button"
            role="radio"
            :aria-checked="relationsRange === option.value"
            :class="{ active: relationsRange === option.value }"
            @click="selectRelationsRange(option.value)"
          >
            {{ option.label }}
          </button>
        </div>
        <p v-if="relationsLoading" class="muted">正在统计…</p>
        <GroupRelationChart v-else-if="relationsGraph" :graph="relationsGraph" />
      </div>
    </Modal>

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
          <input id="group-triggers" v-model="triggersDraft" class="input" placeholder="Diana,diana" />
        </div>
        <div class="field wide">
          <label for="group-trigger-mode">本群触发词匹配（留空用全局）</label>
          <AppSelect
            id="group-trigger-mode"
            :model-value="editing.group_trigger_mode ?? ''"
            :options="groupTriggerModeOptions"
            @update:model-value="(value) => { if (editing) editing.group_trigger_mode = value as typeof editing.group_trigger_mode; }"
          />
          <span class="hint">智能档下，群里谈论机器人而不是叫它的消息不会强制回复。</span>
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
        <div class="field">
          <label for="group-response-mode">回复模式</label>
          <AppSelect
            id="group-response-mode"
            :model-value="editing.response_mode ?? ''"
            :options="groupResponseModeOptions"
            @update:model-value="(value) => { if (editing) editing.response_mode = value as typeof editing.response_mode; }"
          />
          <span class="hint">控制本群中机器人主动接话的频率。</span>
        </div>
        <div class="field">
          <label for="group-reply-style">表达风格</label>
          <AppSelect
            id="group-reply-style"
            :model-value="editing.reply_style ?? ''"
            :options="groupReplyStyleOptions"
            @update:model-value="(value) => { if (editing) editing.reply_style = value as typeof editing.reply_style; }"
          />
        </div>
        <div class="field">
          <label for="group-action-description">动作描写</label>
          <AppSelect
            id="group-action-description"
            :model-value="editing.action_description_enabled === undefined ? '' : editing.action_description_enabled ? 'on' : 'off'"
            :options="groupActionDescriptionOptions"
            @update:model-value="(value) => { if (editing) editing.action_description_enabled = value === '' ? undefined : value === 'on'; }"
          />
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
          <label for="group-history-budget">回复历史 token 预算</label>
          <input id="group-history-budget" v-model.number="editing.recent_history_token_budget" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
        </div>
        <div class="field">
          <label for="group-context">历史查询条数上限</label>
          <input id="group-context" v-model.number="editing.recent_context_limit" class="input" inputmode="numeric" />
        </div>
        <div class="field">
          <label for="group-maxcontext">单次请求上下文上限</label>
          <input id="group-maxcontext" v-model.number="editing.max_context_tokens" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
        </div>
        <div class="field">
          <label for="group-maxreply">回复上限（字符）</label>
          <input id="group-maxreply" v-model.number="editing.max_reply_chars" class="input" inputmode="numeric" />
        </div>
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.natural_reply_split_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">本群自然分条</span>
          </label>
          <span class="hint">
            按模型排的换行、以及句号边界，把一条回复分成几条发。关掉后只认模型显式写的分条标记，
            下面的「最多分几条」随之失效，「分段发送长度」和合并转发不受影响。
          </span>
        </div>
        <div class="field">
          <label for="group-maxbubbles">最多分几条</label>
          <input id="group-maxbubbles" :disabled="!editing.natural_reply_split_enabled" v-model.number="editing.reply_max_bubbles" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">分出来超过它就退回粗一档（先不按句号、再不按换行），退到底把相邻段均分成这么多条。</span>
        </div>
        <div class="field">
          <label for="group-chunk">分段发送长度</label>
          <input id="group-chunk" v-model.number="editing.direct_reply_chunk_size" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">单条聊天消息最多多少字，撞上了会在最近的标点处切开。这是硬上限，不受自然分条开关约束。</span>
        </div>
        <div class="field">
          <label for="group-forward-len">合并转发字数</label>
          <input id="group-forward-len" v-model.number="editing.forward_reply_threshold" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">正文超过这个字数改用合并转发卡片，不再逐条发。</span>
        </div>
        <div class="field">
          <label for="group-forward-chunks">合并转发块数</label>
          <input id="group-forward-chunks" v-model.number="editing.forward_reply_chunk_threshold" class="input" inputmode="numeric" placeholder="留空跟随机器人" />
          <span class="hint">切出超过这么多块也改用合并转发卡片。</span>
        </div>
        <div v-if="editing.response_mode === 'custom'" class="field">
          <label for="group-proactive-chance">主动回复采样率</label>
          <input id="group-proactive-chance" v-model.number="editing.proactive_reply_chance" class="input" type="number" min="0.05" max="1" step="0.05" />
        </div>
        <div v-if="editing.response_mode === 'custom'" class="field">
          <label for="group-proactive-threshold">主动回复置信度阈值</label>
          <input id="group-proactive-threshold" v-model.number="editing.proactive_reply_threshold" class="input" type="number" min="0.5" max="1" step="0.01" />
        </div>
        <div v-if="editing.response_mode === 'custom'" class="field wide">
          <label class="switch">
            <input v-model="editing.natural_interjection_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">本群开启自然插话模式</span>
          </label>
          <span class="hint">普通群聊只要模型能生成具体、可靠且有实质内容的回复就可以插话；关闭时使用现有置信度、概率和冷却规则。</span>
        </div>
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.social_reply_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">本群社交性回应</span>
          </label>
          <span class="hint">
            群友直接对机器人打招呼、夸奖、调侃或轻微评价（「笨笨」「你好可爱」「早」）时也回一句，哪怕没有具体问题。
            只放行冲着机器人来的那一类：别人之间的闲聊、要机器人安静、同一轮已经回过，仍然沉默。
          </span>
        </div>
        <div class="field wide">
          <label class="switch">
            <input v-model="editing.recall_reply_auto_delete_enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">本群查看撤回消息后自动撤回回复</span>
          </label>
          <span class="hint">关闭时，查看撤回记录产生的回复会一直保留。</span>
        </div>
        <div v-if="editing.recall_reply_auto_delete_enabled" class="field">
          <label for="group-recall-delete-delay">回复保留时间（秒）</label>
          <input
            id="group-recall-delete-delay"
            v-model.number="editing.recall_reply_auto_delete_delay_seconds"
            class="input"
            type="number"
            min="1"
            :max="maximumRecallReplyAutoDeleteDelaySeconds"
            step="1"
            inputmode="numeric"
          />
        </div>
        <div class="field wide">
          <label>本群回复时间与屏蔽账号</label>
          <ReplyGateForm v-model="editing.reply_gate" allow-inherit id-prefix="group-gate" :supports-group-level="supportsGroupLevel" />
        </div>
        <div class="field wide">
          <label>本群插件</label>
          <div class="row-list" style="margin-top: 6px">
            <div v-for="plugin in plugins" :key="plugin.manifest.id" class="row-item group-plugin-row">
              <div class="group-plugin-row-head">
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
              <GroupPluginSettings
                v-if="plugin.installed && plugin.manifest.settings?.length"
                :plugin="plugin"
                :model-value="settingOverridesOf(plugin.manifest.id)"
                @update:model-value="setPluginSettingOverrides(plugin.manifest.id, $event)"
              />
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
import { computed, onMounted, ref, watch } from "vue";
import { botScope } from "../bot-scope";
import { Plus, RefreshCw, Save, Search, Share2, SlidersHorizontal, Users, WifiOff } from "@lucide/vue";
import {
  getBotProfileConfig,
  getBotPlatforms,
  listBotGroups,
  saveBotGroup,
  getGroupRelations,
  type PluginState,
  type BotGroupConfig,
  type BotGroupSummary,
  type AssistantEventRange,
  type GroupRelationGraph
} from "../api";
import EmptyState from "../components/EmptyState.vue";
import GroupRelationChart from "../components/GroupRelationChart.vue";
import GroupPluginSettings from "../components/GroupPluginSettings.vue";
import AppSelect, { type AppSelectOption } from "../components/AppSelect.vue";
import Modal from "../components/Modal.vue";
import ReplyGateForm from "../components/ReplyGateForm.vue";

// 空值代表「跟随全局」，与后端把空字符串当成未覆盖的约定一致。
const groupTriggerModeOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "smart", label: "智能" },
  { value: "strict", label: "严格" },
  { value: "loose", label: "宽松" }
];

const groupResponseModeOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "quiet", label: "安静模式" },
  { value: "standard", label: "标准模式" },
  { value: "active", label: "活跃模式" },
  { value: "super_active", label: "超级活跃模式", hint: "几乎每条群消息都会尝试接话" },
  { value: "custom", label: "自定义" }
];

const groupReplyStyleOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "groupmate", label: "群友" },
  { value: "assistant", label: "助手" },
  { value: "gentle", label: "温柔" },
  { value: "lively", label: "活泼" },
  { value: "concise", label: "简洁" },
  { value: "catgirl", label: "猫娘" }
];

const groupActionDescriptionOptions: AppSelectOption[] = [
  { value: "", label: "跟随全局" },
  { value: "on", label: "开启" },
  { value: "off", label: "关闭" }
];
import { toastError, toastSuccess } from "../toast";

const groups = ref<BotGroupSummary[]>([]);
// 群等级只有 OneBot v11 有；按当前激活的机器人平台决定要不要显示这一项。
const supportsGroupLevel = ref(true);
const plugins = ref<PluginState[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
const liveAvailable = ref(false);
const syncWarning = ref("");
const searchQuery = ref("");
const newGroupID = ref("");
const relationRangeOptions: Array<{ value: AssistantEventRange; label: string }> = [
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
  { value: "all", label: "全部" }
];
const relationsGroupID = ref("");
const relationsGroupName = ref("");
const relationsRange = ref<AssistantEventRange>("7d");
const relationsGraph = ref<GroupRelationGraph | null>(null);
const relationsLoading = ref(false);

function openRelations(group: BotGroupConfig, groupName = ""): void {
  relationsGroupID.value = group.group_id;
  relationsGroupName.value = groupName;
  relationsGraph.value = null;
  void loadRelations();
}

function closeRelations(): void {
  relationsGroupID.value = "";
  relationsGraph.value = null;
}

function selectRelationsRange(value: AssistantEventRange): void {
  if (relationsRange.value === value) return;
  relationsRange.value = value;
  relationsGraph.value = null;
  void loadRelations();
}

async function loadRelations(): Promise<void> {
  const groupID = relationsGroupID.value;
  if (!groupID) return;
  relationsLoading.value = true;
  try {
    const response = await getGroupRelations(groupID, relationsRange.value);
    // 期间可能已经关掉弹窗或换了群，回来的结果不该覆盖当前状态。
    if (relationsGroupID.value !== groupID) return;
    relationsGraph.value = response.graph;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "关系图加载失败");
  } finally {
    relationsLoading.value = false;
  }
}

const editing = ref<BotGroupConfig | null>(null);
const editingGroupName = ref("");
const triggersDraft = ref("");
const saving = ref(false);
const togglingGroupID = ref("");
const defaultRecallReplyAutoDeleteEnabled = ref(false);
const defaultNaturalInterjectionEnabled = ref(false);
// 自然分条默认是开的，跟机器人配置那边的缺省一致。
const defaultNaturalReplySplitEnabled = ref(true);
const defaultSocialReplyEnabled = ref(false);
const defaultRecallReplyAutoDeleteDelaySeconds = 60;
const maximumRecallReplyAutoDeleteDelaySeconds = 60 * 60;
const defaultRecallReplyAutoDeleteDelay = ref(defaultRecallReplyAutoDeleteDelaySeconds);

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

function responseModeLabel(mode: BotGroupConfig["response_mode"]): string {
  return ({ quiet: "安静模式", standard: "标准模式", active: "活跃模式", super_active: "超级活跃模式", custom: "自定义回复" } as const)[mode as "quiet" | "standard" | "active" | "super_active" | "custom"] ?? "";
}

function replyStyleLabel(style: BotGroupConfig["reply_style"]): string {
  return ({ groupmate: "群友风格", assistant: "助手风格", gentle: "温柔风格", lively: "活泼风格", concise: "简洁风格", catgirl: "猫娘风格", roleplay: "扮演风格" } as const)[
    style as "groupmate" | "assistant" | "gentle" | "lively" | "concise" | "catgirl" | "roleplay"
  ] ?? "";
}

function overrideCount(group: BotGroupConfig): number {
  return new Set([
    ...Object.keys(group.plugin_overrides ?? {}),
    ...Object.keys(group.plugin_setting_overrides ?? {})
  ]).size;
}

function blockedUserCount(group: BotGroupConfig): number {
  return group.reply_gate?.blocked_users?.length ?? 0;
}

function hasOtherReplyGateRules(group: BotGroupConfig): boolean {
  const gate = group.reply_gate;
  if (!gate) {
    return false;
  }
  return Boolean(
    gate.user_admission === "whitelist" ||
      (gate.allowed_users?.length ?? 0) > 0 ||
    (gate.min_group_level ?? 0) > 0 ||
      (gate.exempt_users?.length ?? 0) > 0 ||
      gate.owner_bypass === false ||
      gate.quiet_reply?.trim()
  );
}

async function load(showFeedback = false): Promise<void> {
  refreshing.value = true;
  try {
    const [response, configAndPlatforms] = await Promise.all([
      listBotGroups(showFeedback, botScope.value),
      Promise.all([getBotProfileConfig(), getBotPlatforms()]).catch(() => null)
    ]);
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
    if (configAndPlatforms) {
      const [config, platformList] = configAndPlatforms;
      const active = config.profiles?.[0];
      const current = active ?? config;
      defaultRecallReplyAutoDeleteEnabled.value = current.recall_reply_auto_delete_enabled ?? false;
      defaultNaturalInterjectionEnabled.value = current.natural_interjection_enabled ?? false;
      defaultNaturalReplySplitEnabled.value = current.natural_reply_split_enabled ?? true;
      defaultSocialReplyEnabled.value = current.social_reply_enabled ?? false;
      defaultRecallReplyAutoDeleteDelay.value = current.recall_reply_auto_delete_delay_seconds ?? defaultRecallReplyAutoDeleteDelaySeconds;
      const def = platformList.platforms.find((item) => item.id === active?.platform);
      supportsGroupLevel.value = def ? def.protocol.startsWith("onebot") : true;
    } else {
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
      natural_interjection_enabled: defaultNaturalInterjectionEnabled.value,
      natural_reply_split_enabled: defaultNaturalReplySplitEnabled.value,
      social_reply_enabled: defaultSocialReplyEnabled.value,
      recall_reply_auto_delete_enabled: defaultRecallReplyAutoDeleteEnabled.value,
      recall_reply_auto_delete_delay_seconds: defaultRecallReplyAutoDeleteDelay.value,
      plugin_overrides: {},
      plugin_setting_overrides: {}
    },
    existing?.group_name
  );
  newGroupID.value = "";
}

function openEditor(group: BotGroupConfig, groupName = ""): void {
  // 深拷贝编辑，取消时不污染列表数据。
  const config = JSON.parse(JSON.stringify(groupConfigOf(group))) as BotGroupConfig;
  config.recall_reply_auto_delete_enabled ??= defaultRecallReplyAutoDeleteEnabled.value;
  config.natural_interjection_enabled ??= defaultNaturalInterjectionEnabled.value;
  config.natural_reply_split_enabled ??= defaultNaturalReplySplitEnabled.value;
  config.social_reply_enabled ??= defaultSocialReplyEnabled.value;
  config.plugin_setting_overrides ??= {};
  config.response_mode ??= "";
  config.reply_style ??= "";
  const delay = Number(config.recall_reply_auto_delete_delay_seconds);
  config.recall_reply_auto_delete_delay_seconds = Number.isInteger(delay) && delay > 0 ? delay : defaultRecallReplyAutoDeleteDelay.value;
  editing.value = config;
  editingGroupName.value = groupName;
  triggersDraft.value = (group.group_triggers ?? []).join(",");
}

function groupConfigOf(group: BotGroupConfig): BotGroupConfig {
  const summary = group as Partial<BotGroupSummary>;
  const { group_name, avatar_url, member_count, max_member_count, configured, joined, ...config } = summary;
  return config as BotGroupConfig;
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

function settingOverridesOf(pluginID: string): Record<string, unknown> {
  return editing.value?.plugin_setting_overrides?.[pluginID] ?? {};
}

function setPluginSettingOverrides(pluginID: string, values: Record<string, unknown>): void {
  if (!editing.value) {
    return;
  }
  const overrides = { ...(editing.value.plugin_setting_overrides ?? {}) };
  if (Object.keys(values).length === 0) {
    delete overrides[pluginID];
  } else {
    overrides[pluginID] = values;
  }
  editing.value.plugin_setting_overrides = overrides;
}

async function toggleGroup(group: BotGroupSummary, event: Event): Promise<void> {
  const enabled = (event.target as HTMLInputElement).checked;
  togglingGroupID.value = group.group_id;
  try {
    const saved = await saveBotGroup({ ...groupConfigOf(group), bot_profile_id: botScope.value || group.bot_profile_id, enabled });
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
  const recallDeleteDelay = Number(current.recall_reply_auto_delete_delay_seconds);
  if (
    current.recall_reply_auto_delete_enabled &&
    (!Number.isInteger(recallDeleteDelay) || recallDeleteDelay < 1 || recallDeleteDelay > maximumRecallReplyAutoDeleteDelaySeconds)
  ) {
    toastError(`回复保留时间请输入 1 到 ${maximumRecallReplyAutoDeleteDelaySeconds} 秒之间的整数`);
    return;
  }
  saving.value = true;
  try {
    const payload: BotGroupConfig = {
      ...current,
      recall_reply_auto_delete_delay_seconds: Number.isInteger(recallDeleteDelay)
        ? recallDeleteDelay
        : defaultRecallReplyAutoDeleteDelaySeconds,
      group_triggers: triggersDraft.value
        .split(/[,，]/)
        .map((item) => item.trim())
        .filter((item) => item !== "")
    };
    const saved = await saveBotGroup({ ...payload, bot_profile_id: botScope.value || payload.bot_profile_id });
    upsert(saved.config);
    editing.value = null;
    toastSuccess(`群 ${payload.group_id} 配置已保存`);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存失败");
  } finally {
    saving.value = false;
  }
}

function upsert(config: BotGroupConfig): void {
  const index = groups.value.findIndex((group) => group.group_id === config.group_id);
  if (index >= 0) {
    groups.value[index] = { ...groups.value[index], ...config, configured: true };
  } else {
    // 头像地址由后端按平台决定（QQ 直链或本机代理），前端不再自己拼；
    // 这里先留空，下一次拉取列表时补上。
    groups.value.push({
      ...config,
      configured: true,
      joined: false
    });
  }
}

// 换了机器人，群列表和每个群的配置都是另一套。
watch(botScope, () => {
  void load();
});

onMounted(() => load());
</script>
