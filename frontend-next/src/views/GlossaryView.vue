<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div>
    <header class="view-header">
      <div class="view-title">
        <h1>词典</h1>
        <p>
          机器人自己收下的梗、黑话与内部称呼；这里可以改释义、作废记错的词条。
          <template v-if="sharedScope">当前跨群共用一本，新词条都记进全局词典。</template>
          <template v-else>当前按会话隔离，一个群记下的梗只在这个群生效；想共用可在「机器人 → 词典作用域」打开。</template>
        </p>
      </div>
      <div class="view-actions">
        <button class="btn" type="button" :disabled="scopes.length === 0" @click="openCreate">
          <Plus :size="15" aria-hidden="true" />
          新增词条
        </button>
        <button class="btn ghost" type="button" :disabled="loading" @click="reload">
          <RefreshCw :size="15" aria-hidden="true" />
          刷新
        </button>
      </div>
    </header>

    <BotScopeNotice subject="全局词典" />

    <section class="card">
      <div class="card-body" style="padding-top: 8px">
        <div class="cluster" style="padding: 8px 0 12px; gap: 10px">
          <div style="max-width: 320px; flex: 1">
            <AppSelect :model-value="scope" :options="scopeOptions" aria-label="词典范围" @update:model-value="scope = $event; reload()" />
          </div>
          <div class="input-group" style="flex: 1; max-width: 320px">
            <input v-model="query" class="input" placeholder="按词条 / 释义搜索…" @keydown.enter="reload" />
          </div>
          <button class="btn ghost small" type="button" :disabled="loading" @click="reload">搜索</button>
          <label class="cluster" style="gap: 6px; font-size: 12.5px">
            <input v-model="includeDeleted" type="checkbox" @change="reload" />
            显示已作废
          </label>
        </div>

        <div v-if="entries.length > 0">
          <article
            v-for="entry in entries"
            :key="entry.id"
            class="log-row"
            role="button"
            tabindex="0"
            :aria-label="`查看词条 ${entry.term}`"
            @click="openDetail(entry)"
            @keydown.enter="openDetail(entry)"
          >
            <div class="log-main">
              <div class="cluster" style="gap: 6px; margin-bottom: 2px">
                <strong>{{ entry.term }}</strong>
                <span v-for="alias in entry.aliases ?? []" :key="alias" class="badge">{{ alias }}</span>
                <span v-if="entry.status === 'deleted'" class="badge err">已作废</span>
              </div>
              <p class="log-detail" style="white-space: normal">{{ entry.meaning }}</p>
              <p class="log-detail">
                第 {{ entry.version }} 版 · 命中 {{ entry.usage_count }} 次
                <template v-if="entry.last_used_at"> · 最近命中 {{ formatRelative(entry.last_used_at) }}</template>
                <template v-if="entry.author_name"> · 记录人 {{ entry.author_name }}</template>
              </p>
            </div>
            <ChevronRight :size="16" class="muted" aria-hidden="true" />
          </article>
        </div>
        <EmptyState
          v-else-if="!loading"
          :title="scopes.length === 0 ? '词典还是空的' : query ? '没有匹配的词条' : '这本词典还没有条目'"
          :description="
            scopes.length === 0
              ? '群里有人解释一个梗、黑话或外号时，机器人会自己把它记下来。'
              : query
                ? '换个关键词试试。'
                : '也可以直接点「新增词条」手动教它一个。'
          "
        />
        <div v-else class="stack">
          <div class="skeleton" style="height: 48px"></div>
          <div class="skeleton" style="height: 48px"></div>
        </div>
      </div>
    </section>

    <Modal v-if="editing" :title="editingTitle" wide @close="closeEditor">
      <div class="stack" style="gap: 14px">
        <div class="field">
          <label for="glossary-term">词条</label>
          <input id="glossary-term" v-model="form.term" class="input" :disabled="!creating" placeholder="例如：带薪拉屎" />
          <span v-if="creating" class="hint">词条建好之后不能改名；改名等于换一个词，应当新建。</span>
        </div>
        <div class="field">
          <label for="glossary-meaning">释义</label>
          <textarea
            id="glossary-meaning"
            v-model="form.meaning"
            class="input"
            rows="3"
            placeholder="它在群里到底指什么，是褒是贬，谁在用"
          ></textarea>
        </div>
        <div class="field">
          <label for="glossary-aliases">别名</label>
          <input id="glossary-aliases" v-model="form.aliases" class="input" placeholder="同义的写法或缩写，用逗号分隔" />
          <span class="hint">别名和词条一样参与自动命中。</span>
        </div>
        <div class="field">
          <label for="glossary-example">例句</label>
          <input id="glossary-example" v-model="form.example" class="input" placeholder="可选：一句能体现用法的话" />
        </div>
        <div class="field">
          <label for="glossary-note">修订说明</label>
          <input id="glossary-note" v-model="form.note" class="input" placeholder="可选：这次改了什么，会记进修订记录" />
        </div>

        <div v-if="detail && !creating" class="stack" style="gap: 8px">
          <h3 class="detail-section-title">修订记录（最近 {{ detail.revisions?.length ?? 0 }} 次）</h3>
          <article v-for="revision in detail.revisions ?? []" :key="revision.version" class="memory-item">
            <div class="cluster" style="gap: 6px">
              <span class="badge">第 {{ revision.version }} 版</span>
              <span class="muted" style="font-size: 12px">{{ revision.note || "无说明" }}</span>
            </div>
            <p v-if="revision.meaning" class="memory-text" style="margin-top: 4px">{{ revision.meaning }}</p>
            <p class="log-detail">
              {{ formatTime(revision.recorded_at) }}
              <template v-if="revision.editor_name"> · {{ revision.editor_name }}</template>
            </p>
          </article>
        </div>
      </div>

      <template #footer>
        <button
          v-if="detail && detail.status === 'active' && !creating"
          class="btn danger ghost"
          type="button"
          :disabled="saving"
          @click="removeEntry"
        >
          作废
        </button>
        <button
          v-if="detail && detail.status === 'deleted' && !creating"
          class="btn ghost"
          type="button"
          :disabled="saving"
          @click="restoreEntry"
        >
          恢复
        </button>
        <button class="btn ghost" type="button" @click="closeEditor">取消</button>
        <button class="btn" type="button" :disabled="saving || !canSave" @click="save">保存</button>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import BotScopeNotice from "../components/BotScopeNotice.vue";
import { computed, onMounted, reactive, ref } from "vue";
import { ChevronRight, Plus, RefreshCw } from "@lucide/vue";
import {
  deleteGlossaryEntry,
  getGlossaryEntry,
  getBotProfileConfig,
  listBotGroups,
  listGlossary,
  restoreGlossaryEntry,
  saveGlossaryEntry,
  type GlossaryEntry,
  type GlossaryScopeSummary
} from "../api";
import { formatRelative, formatTime } from "../format";
import { askConfirm } from "../confirm";
import { toastError, toastSuccess } from "../toast";
import AppSelect from "../components/AppSelect.vue";
import EmptyState from "../components/EmptyState.vue";
import Modal from "../components/Modal.vue";

const scopes = ref<GlossaryScopeSummary[]>([]);
const entries = ref<GlossaryEntry[]>([]);
const scope = ref("");
const query = ref("");
const includeDeleted = ref(false);
const loading = ref(false);
const saving = ref(false);
const editing = ref(false);
const creating = ref(false);
const detail = ref<GlossaryEntry | null>(null);
const groupNames = ref<Record<string, string>>({});
const sharedScope = ref(false);
const profileNames = ref<Record<string, string>>({});

const form = reactive({ term: "", meaning: "", aliases: "", example: "", note: "" });

const editingTitle = computed(() => (creating.value ? "新增词条" : `词条：${form.term}`));
const canSave = computed(() => form.term.trim() !== "" && form.meaning.trim() !== "");

// 作用域键是内部格式（<配置档>:group:123 / <配置档>:private:456 / global），直接
// 摆出来没人看得懂。
//
// 排版顺序很重要：配置档 ID 是个 UUID，放在前面会把真正有用的群号挤出可视范围，
// 手机上整个下拉框只剩一串 UUID，多开几个群就没法选了。所以群名/群号排最前，
// 配置档只在确实有多个时作为后缀出现。
const scopeOptions = computed(() => scopes.value.map((item) => ({ value: item.scope_key, label: `${scopeLabel(item.scope_key)}（${item.active_count}）` })));

function scopeLabel(key: string): string {
  if (key === "global") {
    return "全局词典";
  }
  const parts = key.split(":");
  const id = parts[parts.length - 1] ?? "";
  const kind = parts[parts.length - 2] ?? "";
  if (kind !== "group" && kind !== "private") {
    return key;
  }
  const namespace = parts.slice(0, parts.length - 2).join(":");
  const head = kind === "group" ? groupLabel(id) : `私聊 ${id}`;
  if (!namespace || !multipleNamespaces.value) {
    return head;
  }
  return `${head} · ${profileLabel(namespace)}`;
}

// 有群名就用群名，群号退居括号里；查不到名字才只显示群号。
function groupLabel(groupID: string): string {
  const name = groupNames.value[groupID]?.trim();
  return name ? `${name}（${groupID}）` : `群 ${groupID}`;
}

// 配置档同样优先显示名字；名字缺失时 UUID 只留前 8 位，够区分又不占地方。
function profileLabel(profileID: string): string {
  const name = profileNames.value[profileID]?.trim();
  return name || profileID.slice(0, 8);
}

// 只有真的跨多个配置档时才把配置档摆出来。单机器人部署里它是恒定的噪音。
const multipleNamespaces = computed(() => {
  const seen = new Set<string>();
  for (const item of scopes.value) {
    const parts = item.scope_key.split(":");
    if (parts.length > 2) {
      seen.add(parts.slice(0, parts.length - 2).join(":"));
    }
  }
  return seen.size > 1;
});

// 群名和配置档名都是为了让下拉框可读，取不到就退回 ID，不阻塞词典本身。
async function loadScopeNames(): Promise<void> {
  try {
    const response = await listBotGroups();
    const names: Record<string, string> = {};
    for (const group of response.groups ?? []) {
      if (group.group_id && group.group_name) {
        names[group.group_id] = group.group_name;
      }
    }
    groupNames.value = names;
  } catch {
    // 群列表拿不到不影响词典，标签退回群号。
  }
  try {
    const config = await getBotProfileConfig();
    sharedScope.value = config.glossary_shared_scope_enabled ?? false;
    const names: Record<string, string> = {};
    for (const profile of config.profiles ?? []) {
      if (profile.id && profile.name) {
        names[profile.id] = profile.name;
      }
    }
    profileNames.value = names;
  } catch {
    // 同上，退回 UUID 前 8 位。
  }
}

async function reload(): Promise<void> {
  loading.value = true;
  try {
    const response = await listGlossary(scope.value, query.value.trim(), includeDeleted.value);
    scopes.value = response.scopes;
    scope.value = response.scope;
    entries.value = response.entries;
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载词典失败");
  } finally {
    loading.value = false;
  }
}

function resetForm(entry: GlossaryEntry | null): void {
  form.term = entry?.term ?? "";
  form.meaning = entry?.meaning ?? "";
  form.aliases = (entry?.aliases ?? []).join("，");
  form.example = entry?.example ?? "";
  form.note = "";
}

function openCreate(): void {
  creating.value = true;
  editing.value = true;
  detail.value = null;
  resetForm(null);
}

async function openDetail(entry: GlossaryEntry): Promise<void> {
  creating.value = false;
  editing.value = true;
  detail.value = entry;
  resetForm(entry);
  try {
    // 列表不带修订记录，详情才带：翻词典时最想知道的就是「什么时候被改成现在这个意思的」。
    detail.value = await getGlossaryEntry(entry.scope_key, entry.term);
  } catch (error) {
    toastError(error instanceof Error ? error.message : "加载词条详情失败");
  }
}

function closeEditor(): void {
  editing.value = false;
  creating.value = false;
  detail.value = null;
}

function parsedAliases(): string[] {
  return form.aliases
    .split(/[,，、]/)
    .map((alias) => alias.trim())
    .filter((alias) => alias !== "");
}

async function save(): Promise<void> {
  saving.value = true;
  try {
    await saveGlossaryEntry({
      scope: detail.value?.scope_key || scope.value,
      term: form.term.trim(),
      aliases: parsedAliases(),
      meaning: form.meaning.trim(),
      example: form.example.trim(),
      note: form.note.trim()
    });
    toastSuccess(creating.value ? "词条已新增" : "词条已更新");
    closeEditor();
    await reload();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "保存词条失败");
  } finally {
    saving.value = false;
  }
}

async function removeEntry(): Promise<void> {
  if (!detail.value) {
    return;
  }
  const ok = await askConfirm({
    title: "作废这条词条？",
    message: `「${detail.value.term}」不再参与自动命中。修订记录会保留，之后可以恢复。`,
    confirmLabel: "作废",
    danger: true
  });
  if (!ok) {
    return;
  }
  saving.value = true;
  try {
    await deleteGlossaryEntry(detail.value.scope_key, detail.value.term, form.note.trim());
    toastSuccess("词条已作废");
    closeEditor();
    await reload();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "作废词条失败");
  } finally {
    saving.value = false;
  }
}

async function restoreEntry(): Promise<void> {
  if (!detail.value) {
    return;
  }
  saving.value = true;
  try {
    await restoreGlossaryEntry(detail.value.scope_key, detail.value.term);
    toastSuccess("词条已恢复");
    closeEditor();
    await reload();
  } catch (error) {
    toastError(error instanceof Error ? error.message : "恢复词条失败");
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  void loadScopeNames();
  void reload();
});
</script>
