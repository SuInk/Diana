<template>
  <div class="stack">
    <div v-if="allowInherit" class="field wide">
      <label class="switch">
        <input type="checkbox" :checked="custom" @change="toggleCustom" />
        <span class="track" aria-hidden="true"></span>
        <span class="switch-label">为本群单独设置准入条件</span>
      </label>
      <p class="muted" style="margin-top: 4px">关闭时跟随全局准入设置。</p>
    </div>

    <div v-if="!allowInherit || custom" class="form-grid">
      <div class="field">
        <label :for="`${idPrefix}-level`">QQ 群等级门槛</label>
        <input
          :id="`${idPrefix}-level`"
          class="input"
          inputmode="numeric"
          :value="gate.min_group_level ?? 0"
          @input="patch({ min_group_level: numberOf($event) })"
        />
        <p class="muted" style="margin-top: 4px">0 表示不限。指群内活跃度等级（Lv.1~6），不是 QQ 账号等级。</p>
      </div>

      <div class="field">
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
          <span class="switch-label">限制回复时段</span>
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
          <span class="switch-label">主人不受时段与等级限制</span>
        </label>
        <p class="muted" style="margin-top: 4px">建议保持开启，否则配置写错时你自己也会被挡在门外。</p>
      </div>

      <div class="field">
        <label :for="`${idPrefix}-exempt`">豁免用户（逗号分隔 QQ 号）</label>
        <input
          :id="`${idPrefix}-exempt`"
          class="input"
          :value="(gate.exempt_users ?? []).join(',')"
          @input="patch({ exempt_users: listOf($event) })"
        />
        <p class="muted" style="margin-top: 4px">无视等级与时段门槛。</p>
      </div>

      <div class="field">
        <label :for="`${idPrefix}-blocked`">屏蔽用户（逗号分隔 QQ 号）</label>
        <input
          :id="`${idPrefix}-blocked`"
          class="input"
          :value="(gate.blocked_users ?? []).join(',')"
          @input="patch({ blocked_users: listOf($event) })"
        />
        <p class="muted" style="margin-top: 4px">群聊和私聊都不回复。</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import AppSelect, { type AppSelectOption } from "./AppSelect.vue";
import type { ReplyGate } from "../api";

const props = defineProps<{
  modelValue?: ReplyGate | null;
  /** 群级表单需要「跟随全局」这一档；全局表单不需要。 */
  allowInherit?: boolean;
  idPrefix: string;
}>();
const emit = defineEmits<{ "update:modelValue": [ReplyGate | null] }>();

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

function listOf(event: Event): string[] {
  return (event.target as HTMLInputElement).value
    .split(/[,，\s]+/)
    .map((item) => item.trim())
    .filter((item) => item !== "");
}
</script>
