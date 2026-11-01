<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="card own-room" aria-label="可视化小窝">
    <div class="room-heading">
      <div>
        <p class="room-eyebrow">一个可以慢下来的地方</p>
        <h2>{{ name }}的小窝</h2>
      </div>
      <div class="room-actions">
        <span v-if="mode === 'auto'" class="room-following">正在跟随 · Agent 驱动</span>
        <button class="btn ghost small" type="button" :disabled="!enabled" @click="mode === 'auto' ? takeControl() : followAgent()">{{ mode === 'auto' ? '接管展示' : '交还给 Agent' }}</button>
      </div>
    </div>

    <div class="room-content">
      <div class="room-stage">
        <RoomStudio :key="`${profile}:${mode}`" :profile="profile" :pose="pose" :paused="paused" :can-edit="mode === 'preview'" />
        <div class="room-caption" aria-hidden="true"><span class="room-dot" :class="{ live: (activity.source === 'runtime' || activity.source === 'agent') && mode === 'auto' }"></span>{{ mode === "auto" ? "鲸鱼小窝 · 活动由 Agent 驱动" : "手动展示 · 后台 Agent 仍在运行" }}</div>
        <div v-if="!enabled" class="room-disabled"><Moon :size="22" aria-hidden="true" /><span>小窝还没开，房间等你回来</span></div>
      </div>

      <div class="room-footer">
        <div class="room-status" role="status" aria-live="polite">
          <span class="room-source" :class="{ preview: mode !== 'auto' }">{{ sourceLabel }}</span>
          <strong>{{ label }}</strong>
          <p>{{ detail }}</p>
        </div>
        <div class="room-controls">
          <div v-if="mode === 'preview'" class="room-poses" role="group" aria-label="手动选择动作">
            <button v-for="option in poses" :key="option.value" class="btn small" :class="{ selected: previewPose === option.value }" type="button" :aria-pressed="previewPose === option.value" @click="previewPose = option.value">
              <component :is="option.icon" :size="14" aria-hidden="true" />{{ option.label }}
            </button>
          </div>
          <button v-if="mode === 'preview'" class="btn ghost small" type="button" :aria-pressed="motionOff" @click="motionOff = !motionOff">
            <Pause v-if="!motionOff" :size="14" aria-hidden="true" /><Play v-else :size="14" aria-hidden="true" />{{ motionOff ? '启用动画' : '减少动画' }}
          </button>
          <button v-else class="room-feed-link" type="button" @click="navigate('feed')">去看看她的动态 <ArrowUpRight :size="14" aria-hidden="true" /></button>
        </div>
      </div>
      <p class="room-note">房间联动回复生成和其中的同步工具，不代表全部后台任务或消息投递结果。动作本身不操作电脑，不额外调用模型。<span v-if="reducedMotion">已跟随系统减少动态效果。</span></p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from "vue";
import { ArrowUpRight, Laptop, Moon, Pause, Play, Smartphone } from "@lucide/vue";
import type { RoomState } from "../api";
import { stream } from "../lib/stream";
import { roomActivity, type RoomPose } from "../lib/room-activity";
import { navigate } from "../router";
import RoomStudio from "./RoomStudio.vue";

const props = defineProps<{ profile: string; enabled: boolean; initial?: RoomState }>();
const motionOff = ref(false);
const hidden = ref(document.hidden);
const active = ref(true);
const reducedMotion = ref(false);
const mode = ref<"auto" | "preview">("auto");
const previewPose = ref<RoomPose>("rest");
const now = ref(Date.now());
let tick: ReturnType<typeof setInterval> | undefined;
let media: MediaQueryList | undefined;

const poses = [
  { value: "rest", label: "歇一会儿", icon: Moon },
  { value: "phone", label: "看看手机", icon: Smartphone },
  { value: "desk", label: "去书桌前", icon: Laptop }
] as const;
const activity = computed(() => roomActivity({ profile: props.profile, enabled: props.enabled, connected: stream.connected, status: stream.status, initial: props.initial, now: now.value }));
const name = computed(() => (stream.status?.channels ?? (stream.status ? [stream.status.channel] : [])).find((channel) => channel.profile_id === props.profile)?.name || "她");
const pose = computed(() => mode.value === "preview" && props.enabled ? previewPose.value : activity.value.pose);
const sourceLabel = computed(() => mode.value === "preview" ? "手动展示" : import.meta.env.VITE_DEMO_MODE === "true" ? "模拟数据" : activity.value.source === "runtime" ? "真实任务" : activity.value.source === "agent" ? "Agent 动作" : "房间状态");
const label = computed(() => mode.value === "preview" ? poses.find((option) => option.value === previewPose.value)!.label : activity.value.label);
const detail = computed(() => mode.value === "preview" ? "已接管当前画面，不跟随新活动；后台 Agent 仍在运行。交还后立即恢复最新状态，不会发送消息或操作电脑。" : activity.value.detail);
const paused = computed(() => motionOff.value || reducedMotion.value || hidden.value || !active.value || (mode.value === "auto" && activity.value.source === "unavailable"));

function syncClock(): void {
  clearInterval(tick);
  now.value = Date.now();
  if (active.value && !hidden.value) tick = setInterval(() => { now.value = Date.now(); }, 5000);
}
function visibilityChanged(): void { hidden.value = document.hidden; syncClock(); }
function motionChanged(): void { reducedMotion.value = media?.matches ?? false; }
function followAgent(): void { mode.value = "auto"; motionOff.value = false; previewPose.value = "rest"; now.value = Date.now(); }
function takeControl(): void { previewPose.value = activity.value.pose; mode.value = "preview"; }
watch(() => [props.profile, props.enabled], followAgent);
onMounted(() => {
  media = window.matchMedia("(prefers-reduced-motion: reduce)");
  motionChanged();
  media.addEventListener("change", motionChanged);
  document.addEventListener("visibilitychange", visibilityChanged);
  syncClock();
});
onActivated(() => { active.value = true; followAgent(); syncClock(); });
onDeactivated(() => { active.value = false; followAgent(); clearInterval(tick); });
onBeforeUnmount(() => {
  clearInterval(tick);
  media?.removeEventListener("change", motionChanged);
  document.removeEventListener("visibilitychange", visibilityChanged);
});
</script>

<style scoped>
.own-room { overflow: hidden; }
.room-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 20px 24px; }
.room-eyebrow { margin: 0 0 4px; font-size: 11px; color: var(--muted); letter-spacing: .12em; }
.room-heading h2 { margin: 0; font-size: 19px; font-weight: 650; overflow-wrap: anywhere; }
.room-following { color: var(--accent); font-size: 12px; align-self: center; }
.room-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; flex-shrink: 0; }
.room-stage { position: relative; background: #20364b; }
.room-stage:has(.room-editor) .room-caption { display: none; }
.room-caption { position: absolute; left: 20px; bottom: 14px; display: flex; align-items: center; gap: 8px; padding: 7px 12px; background: #102937c9; color: #e1e5da; border: 1px solid #b1c5c526; border-radius: 20px; font-size: 11px; }
.room-dot { width: 6px; height: 6px; border-radius: 50%; background: #cbb895; }
.room-dot.live { background: #a1d0b4; }
.room-disabled { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; gap: 10px; color: #f4ead8; background: #12283c99; backdrop-filter: blur(3px); font-size: 14px; }
.room-footer { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; padding: 20px 24px 12px; }
.room-status { flex: 1; min-width: 0; }
.room-status strong { display: inline; font-size: 14px; }
.room-status p { margin: 9px 0 0; font-size: 12px; color: var(--muted); line-height: 1.7; max-width: 490px; }
.room-source { display: inline-block; margin: 0 8px 4px 0; padding: 3px 7px; border: 1px solid var(--border); border-radius: 4px; font-size: 10px; color: var(--muted); }
.room-source.preview { color: #886528; background: #e9bf6320; border-color: #bc974c50; }
.room-controls { display: flex; flex-direction: column; align-items: flex-end; gap: 10px; flex-shrink: 0; }
.room-poses { display: flex; flex-wrap: wrap; gap: 5px; }
.room-poses .selected { background: var(--surface-2); border-color: var(--muted); }
.room-feed-link { display: inline-flex; align-items: center; gap: 4px; background: none; border: none; color: var(--muted); font: inherit; font-size: 12px; cursor: pointer; padding: 4px 0; }
.room-note { margin: 0; padding: 0 24px 18px; font-size: 11px; color: var(--muted); line-height: 1.7; }
@media (max-width: 700px) {
  .room-heading { padding: 16px; align-items: flex-start; }
  .room-heading h2 { font-size: 16px; }
  .room-actions { flex-direction: column; gap: 2px; }
  .room-footer { flex-direction: column; padding: 16px; gap: 16px; }
  .room-controls { align-items: flex-start; width: 100%; }
  .room-caption { left: 10px; bottom: 8px; font-size: 9px; padding: 4px 8px; }
  .room-note { padding: 0 16px 16px; }
}
</style>
