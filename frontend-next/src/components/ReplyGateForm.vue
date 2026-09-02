<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="stack">
    <div v-if="allowInherit" class="field wide">
      <label class="switch">
        <input type="checkbox" :checked="custom" @change="toggleCustom" />
        <span class="track" aria-hidden="true"></span>
        <span class="switch-label">为本群单独设置回复规则</span>
      </label>
      <p class="muted" style="margin-top: 4px">关闭时完全跟随全局。打开后等级和时段由本群说了算，但三个用户名单始终是全局加本群的并集——不会因为在这里配了东西就把全局屏蔽名单丢掉。</p>
    </div>

    <div v-if="!allowInherit || custom" class="form-grid">
      <div v-if="supportsGroupLevel" class="field">
        <label :for="`${idPrefix}-level`">群等级门槛</label>
        <input
          :id="`${idPrefix}-level`"
          class="input"
          inputmode="numeric"
          :value="gate.min_group_level ?? 0"
          @input="patch({ min_group_level: numberOf($event) })"
        />
        <p class="muted" style="margin-top: 4px">0 表示不限。指群内活跃度等级（Lv.1~6），不是账号等级。</p>
      </div>

      <div v-if="supportsGroupLevel" class="field">
        <label :for="`${idPrefix}-unknown`">等级读不到时</label>
        <AppSelect
          :id="`${idPrefix}-unknown`"
          :model-value="gate.level_unknown_policy ?? 'allow'"
          :options="unknownPolicyOptions"
          @update:model-value="patch({ level_unknown_policy: $event as 'allow' | 'deny' })"
        />
        <p class="muted" style="margin-top: 4px">
          部分 OneBot 实现不提供群等级。选「拦截」会让这些实现下的群整体静音，除非确认你的实现能返回等级。
        </p>
      </div>

      <div class="field wide">
        <label class="switch">
          <input
            type="checkbox"
            :checked="gate.active_hours_enabled ?? false"
            @change="patch({ active_hours_enabled: checkedOf($event) })"
          />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">{{ allowInherit ? "限制本群回复时间" : "限制回复时间" }}</span>
        </label>
      </div>

      <template v-if="gate.active_hours_enabled">
        <div class="field">
          <label :for="`${idPrefix}-start`">开始时间</label>
          <input
            :id="`${idPrefix}-start`"
            type="time"
            class="input"
            :value="gate.active_start || '09:00'"
            @input="patch({ active_start: valueOf($event) })"
          />
        </div>
        <div class="field">
          <label :for="`${idPrefix}-end`">结束时间</label>
          <input
            :id="`${idPrefix}-end`"
            type="time"
            class="input"
            :value="gate.active_end || '23:00'"
            @input="patch({ active_end: valueOf($event) })"
          />
        </div>
        <div class="field wide">
          <p v-if="overnight" class="badge accent">跨夜时段：{{ gate.active_start }} 到<strong>次日</strong> {{ gate.active_end }}</p>
          <p v-else-if="sameTime" class="muted">开始与结束相同，视为全天开放。</p>
        </div>
        <div class="field">
          <label :for="`${idPrefix}-tz`">时区</label>
          <input
            :id="`${idPrefix}-tz`"
            class="input"
            list="reply-gate-timezones"
            placeholder="留空用服务器本地时区"
            :value="gate.timezone ?? ''"
            @input="patch({ timezone: valueOf($event) })"
          />
          <datalist id="reply-gate-timezones">
            <option value="Asia/Shanghai"></option>
            <option value="Asia/Hong_Kong"></option>
            <option value="Asia/Taipei"></option>
            <option value="Asia/Tokyo"></option>
            <option value="UTC"></option>
          </datalist>
        </div>
        <div class="field wide">
          <label :for="`${idPrefix}-quiet`">静默期提示语（留空则完全不出声）</label>
          <input
            :id="`${idPrefix}-quiet`"
            class="input"
            placeholder="现在是休息时间，白天再来找我吧"
            :value="gate.quiet_reply ?? ''"
            @input="patch({ quiet_reply: valueOf($event) })"
          />
          <p class="muted" style="margin-top: 4px">同一会话每小时最多提示一次，避免刷屏。</p>
        </div>
      </template>

      <div class="field wide">
        <label class="switch">
          <input
            type="checkbox"
            :checked="gate.owner_bypass ?? true"
            @change="patch({ owner_bypass: checkedOf($event) })"
          />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">{{ supportsGroupLevel ? "主人不受时段与等级限制" : "主人不受时段限制" }}</span>
        </label>
        <p class="muted" style="margin-top: 4px">建议保持开启，否则配置写错时你自己也会被挡在门外。</p>
      </div>

      <div class="field wide">
        <label :for="`${idPrefix}-user-admission`">{{ allowInherit ? "本群人员准入" : "人员准入" }}</label>
        <AppSelect
          :id="`${idPrefix}-user-admission`"
          :model-value="gate.user_admission ?? 'blacklist'"
          :options="userAdmissionOptions"
          @update:model-value="patch({ user_admission: $event as 'blacklist' | 'whitelist' })"
        />
        <p class="muted" style="margin-top: 4px">
          白名单是「只回名单里的人」，和下面的豁免不是一回事——豁免只是绕过{{ supportsGroupLevel ? "等级和时段" : "时段" }}门槛，人仍然要先过准入。
          主人不受白名单限制。
        </p>
      </div>

      <div v-if="(gate.user_admission ?? 'blacklist') === 'whitelist'" class="field wide">
        <label :for="`${idPrefix}-allowed`">{{ allowInherit ? "本群白名单" : "白名单" }}</label>
        <IdChipInput
          :input-id="`${idPrefix}-allowed`"
          :model-value="gate.allowed_users ?? []"
          :placeholder="`填${accountNoun.trim()}后回车`"
          :resolve-names="resolveAccountNames"
          @update:model-value="patch({ allowed_users: $event })"
        />
        <p class="muted" style="margin-top: 4px">
          只回这些账号，名单外一律不回，连静默提示也不发。留空等于谁都不回。
        </p>
      </div>

      <div class="field">
        <label :for="`${idPrefix}-exempt`">{{ allowInherit ? "本群豁免用户" : "豁免用户" }}</label>
        <IdChipInput
          :input-id="`${idPrefix}-exempt`"
          :model-value="gate.exempt_users ?? []"
          :placeholder="`填${accountNoun.trim()}后回车`"
          :resolve-names="resolveAccountNames"
          @update:model-value="patch({ exempt_users: $event })"
        />
        <p class="muted" style="margin-top: 4px">{{ supportsGroupLevel ? "无视等级与时段门槛。" : "无视时段门槛。" }}</p>
      </div>

      <div class="field">
        <label :for="`${idPrefix}-blocked`">{{ allowInherit ? "本群屏蔽账号" : "屏蔽用户" }}</label>
        <IdChipInput
          :input-id="`${idPrefix}-blocked`"
          :model-value="gate.blocked_users ?? []"
          :placeholder="`填${accountNoun.trim()}后回车`"
          :resolve-names="resolveAccountNames"
          @update:model-value="patch({ blocked_users: $event })"
        />
        <p class="muted" style="margin-top: 4px">{{ allowInherit ? "在全局屏蔽名单之外，本群额外不回复这些账号。" : "群聊和私聊都不回复。" }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import AppSelect, { type AppSelectOption } from "./AppSelect.vue";
import IdChipInput from "./IdChipInput.vue";
import { fetchAssistantUserNames, type ReplyGate } from "../api";

const props = defineProps<{
  modelValue?: ReplyGate | null;
  /** 群级表单需要「跟随全局」这一档；全局表单不需要。 */
  allowInherit?: boolean;
  idPrefix: string;
  /**
   * 群等级是 OneBot v11 独有的概念，Telegram 上没有对应字段，
   * 显示出来只会让人以为配了会生效。
   */
  supportsGroupLevel?: boolean;
}>();
const emit = defineEmits<{ "update:modelValue": [ReplyGate | null] }>();

// 账号在不同平台叫法不同，文案跟着平台走。
const accountNoun = computed(() => (props.supportsGroupLevel ? " 账号" : "用户 ID"));

const userAdmissionOptions: AppSelectOption[] = [
  { value: "blacklist", label: "黑名单（默认）", hint: "除屏蔽账号外都回" },
  { value: "whitelist", label: "白名单", hint: "只回白名单里的人" }
];

const unknownPolicyOptions: AppSelectOption[] = [
  { value: "allow", label: "放行（推荐）", hint: "读不到等级时不拦截" },
  { value: "deny", label: "拦截", hint: "读不到等级时一律不回" }
];

const custom = computed(() => props.modelValue != null);
const gate = computed<ReplyGate>(() => props.modelValue ?? {});

const overnight = computed(() => {
  const start = minutesOf(gate.value.active_start);
  const end = minutesOf(gate.value.active_end);
  return start !== null && end !== null && end < start;
});

const sameTime = computed(() => {
  const start = minutesOf(gate.value.active_start);
  const end = minutesOf(gate.value.active_end);
  return start !== null && end !== null && end === start;
});

function minutesOf(value?: string): number | null {
  if (!value) {
    return null;
  }
  const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim());
  if (!match) {
    return null;
  }
  return Number(match[1]) * 60 + Number(match[2]);
}

function patch(changes: Partial<ReplyGate>): void {
  emit("update:modelValue", { ...gate.value, ...changes });
}

function toggleCustom(event: Event): void {
  const enabled = (event.target as HTMLInputElement).checked;
  // 关闭时置 null，后端据此判断「跟随全局」。
  emit("update:modelValue", enabled ? { active_start: "09:00", active_end: "23:00" } : null);
}

function valueOf(event: Event): string {
  return (event.target as HTMLInputElement).value;
}

function checkedOf(event: Event): boolean {
  return (event.target as HTMLInputElement).checked;
}

function numberOf(event: Event): number {
  const parsed = Number.parseInt((event.target as HTMLInputElement).value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

// 名字只是回显，profile 留空即当前选中的那台机器人——和其它几处昵称回显一致。
async function resolveAccountNames(ids: string[]): Promise<Record<string, string>> {
  const response = await fetchAssistantUserNames(ids);
  return response.names ?? {};
}
</script>
