<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div class="stack">
    <p class="hint" style="margin: 0">
      机器人必须是本群管理员才能撤回和禁言。主人和群管理员不受这些规则约束；第一次违规只警告，之后按阶梯禁言。
    </p>
    <div class="form-grid">
      <div class="field wide">
        <label class="switch">
          <input type="checkbox" :checked="gov.anti_spam_enabled ?? false" @change="patch({ anti_spam_enabled: checkedOf($event) })" />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">刷屏检测</span>
        </label>
      </div>
      <template v-if="gov.anti_spam_enabled">
        <div class="field">
          <label :for="`${idPrefix}-window`">时间窗（秒）</label>
          <input :id="`${idPrefix}-window`" class="input" inputmode="numeric" placeholder="10" :value="gov.spam_window_seconds || ''" @input="patch({ spam_window_seconds: numberOf($event) })" />
        </div>
        <div class="field">
          <label :for="`${idPrefix}-max`">窗口内最多条数</label>
          <input :id="`${idPrefix}-max`" class="input" inputmode="numeric" placeholder="8" :value="gov.spam_max_messages || ''" @input="patch({ spam_max_messages: numberOf($event) })" />
        </div>
        <div class="field">
          <label :for="`${idPrefix}-repeat`">相同内容最多次数</label>
          <input :id="`${idPrefix}-repeat`" class="input" inputmode="numeric" placeholder="4" :value="gov.spam_max_repeats || ''" @input="patch({ spam_max_repeats: numberOf($event) })" />
        </div>
        <div class="field">
          <label class="switch">
            <input type="checkbox" :checked="gov.spam_recall_enabled ?? false" @change="patch({ spam_recall_enabled: checkedOf($event) })" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">同时撤回刷屏消息</span>
          </label>
        </div>
      </template>

      <div class="field wide">
        <label class="switch">
          <input type="checkbox" :checked="gov.keyword_filter_enabled ?? false" @change="patch({ keyword_filter_enabled: checkedOf($event) })" />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">违规词拦截（命中即撤回）</span>
        </label>
      </div>
      <div v-if="gov.keyword_filter_enabled" class="field wide">
        <label :for="`${idPrefix}-rules`">违规词规则</label>
        <textarea
          :id="`${idPrefix}-rules`"
          class="textarea"
          rows="4"
          placeholder="每行一条，不区分大小写；re: 开头按正则，例如 re:加\s*[vV微]"
          :value="(gov.keyword_rules ?? []).join('\n')"
          @change="patch({ keyword_rules: linesOf($event) })"
        ></textarea>
      </div>

      <template v-if="gov.anti_spam_enabled || gov.keyword_filter_enabled">
        <div class="field">
          <label :for="`${idPrefix}-ladder`">阶梯禁言（分钟）</label>
          <input
            :id="`${idPrefix}-ladder`"
            class="input"
            placeholder="10, 60, 1440"
            :value="(gov.penalty_ladder_seconds ?? []).map((s) => Math.round(s / 60)).join(', ')"
            @change="patch({ penalty_ladder_seconds: ladderOf($event) })"
          />
          <span class="hint">第 2、3… 次违规依次禁言这么久，超出按最后一档。留空用 10 分钟、1 小时、1 天。</span>
        </div>
        <div class="field">
          <label :for="`${idPrefix}-reset`">违规记录有效期（分钟）</label>
          <input :id="`${idPrefix}-reset`" class="input" inputmode="numeric" placeholder="1440" :value="gov.strike_reset_minutes || ''" @input="patch({ strike_reset_minutes: numberOf($event) })" />
        </div>
        <div class="field wide">
          <label :for="`${idPrefix}-warning`">警告语</label>
          <input
            :id="`${idPrefix}-warning`"
            class="input"
            placeholder="{nickname}，{reason}。{penalty}。"
            :value="gov.warning_message ?? ''"
            @input="patch({ warning_message: valueOf($event) })"
          />
          <span class="hint">可用 {nickname} {user_id} {reason} {penalty} {strike}。</span>
        </div>
      </template>

      <div class="field wide">
        <label class="switch">
          <input type="checkbox" :checked="gov.member_leave_audit_enabled ?? false" @change="patch({ member_leave_audit_enabled: checkedOf($event) })" />
          <span class="track" aria-hidden="true"></span>
          <span class="switch-label">退群/踢人时私聊通知主人</span>
        </label>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { GroupGovernance } from "../api";

const props = defineProps<{ modelValue?: GroupGovernance | null; idPrefix: string }>();
const emit = defineEmits<{ "update:modelValue": [GroupGovernance] }>();

const gov = computed<GroupGovernance>(() => props.modelValue ?? {});

function patch(changes: Partial<GroupGovernance>): void {
  emit("update:modelValue", { ...gov.value, ...changes });
}

function valueOf(event: Event): string {
  return (event.target as HTMLInputElement).value;
}

function checkedOf(event: Event): boolean {
  return (event.target as HTMLInputElement).checked;
}

function numberOf(event: Event): number {
  const value = Math.round(Number(valueOf(event)));
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function linesOf(event: Event): string[] {
  return valueOf(event)
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}

// 界面按分钟填，后端按秒存：禁言没有按秒调的必要，分钟更不容易填错数量级。
function ladderOf(event: Event): number[] {
  return valueOf(event)
    .split(/[,，\s]+/)
    .map((item) => Math.round(Number(item)))
    .filter((minutes) => Number.isFinite(minutes) && minutes > 0)
    .map((minutes) => minutes * 60);
}
</script>
