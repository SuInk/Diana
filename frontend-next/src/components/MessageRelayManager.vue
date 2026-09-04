<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Modal title="消息互通" wide @close="emit('close')">
    <div class="stack relay-manager">
      <p class="hint relay-intro">
        一条链路连接两个会话，两边的消息互相转发。需要更多会话联通时就多加几条链路，
        比如 A 群 ↔ B 群、A 群 ↔ Telegram 群。
      </p>

      <article v-for="(pair, index) in draft" :key="pair.id || index" class="relay-card">
        <header class="relay-card-head">
          <input v-model="pair.name" class="input relay-name" type="text" placeholder="链路名称（可留空）" />
          <label class="switch relay-switch">
            <input v-model="pair.enabled" type="checkbox" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">{{ pair.enabled ? "转发中" : "已停用" }}</span>
          </label>
          <button class="btn ghost icon-only small danger" type="button" aria-label="删除链路" @click="removePair(index)">
            <Trash2 :size="15" aria-hidden="true" />
          </button>
        </header>

        <div class="relay-ends">
          <div v-for="side in [0, 1]" :key="side" class="relay-end">
            <span class="relay-end-title">{{ side === 0 ? "一端" : "另一端" }}</span>
            <div class="field">
              <label>机器人</label>
              <AppSelect
                :model-value="pair.endpoints[side].profile_id"
                :options="profileOptions"
                @update:model-value="selectProfile(pair, side, String($event))"
              />
            </div>
            <div class="field">
              <label>会话类型</label>
              <AppSelect
                :model-value="pair.endpoints[side].kind"
                :options="kindOptions"
                @update:model-value="selectKind(pair, side, String($event))"
              />
            </div>
            <div class="field">
              <label>{{ pair.endpoints[side].kind === "group" ? "群聊" : "用户 ID" }}</label>
              <AppSelect
                v-if="pair.endpoints[side].kind === 'group' && groupOptions(pair.endpoints[side].profile_id).length"
                :model-value="pair.endpoints[side].target_id"
                :options="groupOptions(pair.endpoints[side].profile_id)"
                @update:model-value="pair.endpoints[side].target_id = String($event)"
              />
              <input
                v-else
                v-model="pair.endpoints[side].target_id"
                class="input"
                type="text"
                :placeholder="pair.endpoints[side].kind === 'group' ? '群号 / Chat ID' : '用户 ID / Chat ID'"
              />
            </div>
          </div>
        </div>

        <p v-if="pairProblem(pair)" class="hint relay-problem">{{ pairProblem(pair) }}</p>
      </article>

      <EmptyState v-if="!draft.length" title="还没有互通链路" hint="添加一条链路，把两个会话连起来。" />

      <button class="btn small relay-add" type="button" @click="addPair">
        <Plus :size="14" aria-hidden="true" />
        添加链路
      </button>
    </div>

    <template #footer>
      <button class="btn ghost" type="button" :disabled="saving" @click="emit('close')">取消</button>
      <button class="btn primary" type="button" :disabled="saving || Boolean(blockingProblem)" @click="save">
        {{ saving ? "保存中…" : "保存" }}
      </button>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { Plus, Trash2 } from "@lucide/vue";
import Modal from "./Modal.vue";
import AppSelect from "./AppSelect.vue";
import EmptyState from "./EmptyState.vue";
import {
  listBotGroups,
  saveMessageRelays,
  type BotGroupSummary,
  type BotProfileConfig,
  type MessageRelayKind,
  type MessageRelayPair
} from "../api";

const props = defineProps<{ profiles: BotProfileConfig[]; relays: MessageRelayPair[] }>();
const emit = defineEmits<{ close: []; saved: [BotProfileConfig] }>();

const saving = ref(false);
const groups = ref<Record<string, BotGroupSummary[]>>({});
// 编辑期间用一份草稿：取消就整份丢掉，不用逐字段回滚。
const draft = reactive<MessageRelayPair[]>(props.relays.map(clonePair));

const kindOptions = [
  { value: "group", label: "群聊" },
  { value: "private", label: "私聊" }
];
const profileOptions = computed(() =>
  props.profiles
    .filter((profile) => profile.id)
    .map((profile) => ({ value: String(profile.id), label: `${profile.name || profile.id} · ${platformLabel(profile.platform)}` }))
);

onMounted(async () => {
  await Promise.all(
    props.profiles
      .filter((profile) => profile.id)
      .map(async (profile) => {
        const id = String(profile.id);
        try {
          groups.value[id] = (await listBotGroups(false, id)).groups ?? [];
        } catch {
          groups.value[id] = [];
        }
      })
  );
});

function clonePair(pair: MessageRelayPair): MessageRelayPair {
  const endpoints = [0, 1].map((side) => ({
    profile_id: pair.endpoints?.[side]?.profile_id ?? "",
    platform: pair.endpoints?.[side]?.platform ?? "",
    kind: (pair.endpoints?.[side]?.kind ?? "group") as MessageRelayKind,
    target_id: pair.endpoints?.[side]?.target_id ?? ""
  }));
  return { id: pair.id, name: pair.name ?? "", enabled: pair.enabled, endpoints };
}

function addPair(): void {
  const first = props.profiles.find((profile) => profile.id);
  const endpoint = () => ({ profile_id: String(first?.id ?? ""), platform: String(first?.platform ?? ""), kind: "group" as MessageRelayKind, target_id: "" });
  draft.push({ id: "", name: "", enabled: true, endpoints: [endpoint(), endpoint()] });
}

function removePair(index: number): void {
  draft.splice(index, 1);
}

function selectProfile(pair: MessageRelayPair, side: number, profileID: string): void {
  const profile = props.profiles.find((item) => item.id === profileID);
  pair.endpoints[side].profile_id = profileID;
  pair.endpoints[side].platform = String(profile?.platform ?? "");
  // 换了机器人，原来那个群号在新机器人上多半不存在。
  pair.endpoints[side].target_id = "";
}

function selectKind(pair: MessageRelayPair, side: number, kind: string): void {
  pair.endpoints[side].kind = kind === "private" ? "private" : "group";
  pair.endpoints[side].target_id = "";
}

function groupOptions(profileID: string) {
  return (groups.value[profileID] ?? [])
    .filter((group) => group.joined)
    .map((group) => ({ value: group.group_id, label: group.group_name ? `${group.group_name} · ${group.group_id}` : group.group_id }));
}

function endpointKey(endpoint: MessageRelayPair["endpoints"][number]): string {
  return [endpoint.profile_id, endpoint.kind, endpoint.target_id].join("|");
}

// 提交前就把说不通的链路指出来，别等后端默默丢掉再让人猜哪条没生效。
function pairProblem(pair: MessageRelayPair): string {
  const [a, b] = pair.endpoints;
  if (!a.profile_id || !a.target_id || !b.profile_id || !b.target_id) return "两端都要选好机器人和会话，这条链路才会生效。";
  if (endpointKey(a) === endpointKey(b)) return "两端指向了同一个会话，那样只会把消息转回原处。";
  return "";
}

const blockingProblem = computed(() => draft.find((pair) => pairProblem(pair)) !== undefined);

function platformLabel(platform?: string): string {
  return (
    ({ "onebot-v11": "QQ", telegram: "Telegram", "qq-official": "QQ 官方", dingtalk: "钉钉", feishu: "飞书", wecom: "企业微信" } as Record<string, string>)[
      String(platform ?? "")
    ] ?? String(platform ?? "未选择平台")
  );
}

async function save(): Promise<void> {
  saving.value = true;
  try {
    emit("saved", await saveMessageRelays(draft.map(clonePair)));
  } finally {
    saving.value = false;
  }
}
</script>

<style scoped>
.relay-intro {
  margin: 0;
}

.relay-card {
  display: grid;
  gap: 12px;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
}

.relay-card-head {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 10px;
}

.relay-name {
  min-width: 0;
}

.relay-switch {
  white-space: nowrap;
}

/* 两端并排，中间那条线让「这是一对」一眼看得出来。 */
.relay-ends {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
}

.relay-end {
  display: grid;
  gap: 8px;
  min-width: 0;
  padding-left: 12px;
  border-left: 2px solid var(--border);
}

.relay-end-title {
  color: var(--muted);
  font-size: 12px;
}

.relay-problem {
  margin: 0;
  color: var(--warning);
}

.relay-add {
  justify-self: start;
}

@media (max-width: 720px) {
  .relay-ends {
    grid-template-columns: 1fr;
  }

  .relay-card-head {
    grid-template-columns: 1fr auto;
  }

  .relay-name {
    grid-column: 1 / -1;
  }
}
</style>
