<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div ref="rootRef" class="date-range-picker" :class="{ open }">
    <button
      ref="triggerRef"
      type="button"
      class="date-range-trigger"
      :aria-expanded="open"
      aria-haspopup="dialog"
      aria-label="选择自定义日期范围"
      @click="toggle"
    >
      <Calendar :size="13" class="date-range-icon" aria-hidden="true" />
      <span class="date-range-text">{{ triggerLabel }}</span>
      <ChevronDown :size="13" class="date-range-chevron" aria-hidden="true" />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="popoverRef"
        class="date-range-popover"
        role="dialog"
        aria-label="日期范围选择"
        :style="popoverStyle"
        @keydown.escape.stop="close"
      >
        <!-- 快捷区间 -->
        <div class="drp-presets">
          <button v-for="preset in presets" :key="preset.label" type="button" class="drp-preset-btn" @click="applyPreset(preset.days, preset.mode)">
            {{ preset.label }}
          </button>
        </div>

        <!-- 起止日期状态与手填输入 -->
        <div class="drp-inputs">
          <label class="drp-input-box" :class="{ active: !pickingEnd }">
            <span class="muted">开始</span>
            <input
              v-model="manualFrom"
              type="text"
              inputmode="numeric"
              placeholder="YYYY-MM-DD"
              aria-label="开始日期"
              @focus="pickingEnd = false"
              @keydown.enter.prevent="commitManual"
            />
          </label>
          <span class="muted">→</span>
          <label class="drp-input-box" :class="{ active: pickingEnd }">
            <span class="muted">结束</span>
            <input
              v-model="manualTo"
              type="text"
              inputmode="numeric"
              placeholder="YYYY-MM-DD"
              aria-label="结束日期"
              @focus="pickingEnd = true"
              @keydown.enter.prevent="commitManual"
            />
          </label>
        </div>

        <!-- 年月导航 -->
        <div class="drp-nav">
          <div class="drp-nav-side">
            <button type="button" class="drp-nav-btn" title="上一年" aria-label="上一年" @click="shiftMonth(-12)">«</button>
            <button type="button" class="drp-nav-btn" title="上一月" aria-label="上一月" @click="shiftMonth(-1)">
              <ChevronLeft :size="14" aria-hidden="true" />
            </button>
          </div>
          <strong class="drp-month-title">{{ viewYear }} 年 {{ viewMonth + 1 }} 月</strong>
          <div class="drp-nav-side">
            <button type="button" class="drp-nav-btn" title="下一月" aria-label="下一月" :disabled="isCurrentOrFutureMonth" @click="shiftMonth(1)">
              <ChevronRight :size="14" aria-hidden="true" />
            </button>
            <button type="button" class="drp-nav-btn" title="下一年" aria-label="下一年" :disabled="viewYear >= todayYear" @click="shiftMonth(12)">»</button>
          </div>
        </div>

        <!-- 星期表头与日期网格 -->
        <div class="drp-weekdays" aria-hidden="true">
          <span v-for="w in weekdays" :key="w">{{ w }}</span>
        </div>
        <div class="drp-grid" @mouseleave="hoverDate = ''">
          <button
            v-for="cell in calendarCells"
            :key="cell.iso"
            type="button"
            class="drp-day"
            :class="{
              outside: !cell.inMonth,
              today: cell.iso === todayISO,
              disabled: cell.future,
              'is-start': cell.iso === previewRange.start,
              'is-end': cell.iso === previewRange.end,
              'in-range': isBetween(cell.iso, previewRange.start, previewRange.end)
            }"
            :disabled="cell.future"
            @mouseenter="onHoverDay(cell.iso)"
            @click="onPickDay(cell.iso)"
          >
            {{ cell.day }}
          </button>
        </div>

        <!-- 底部提示与确认 -->
        <div class="drp-footer">
          <span class="muted drp-hint">{{ pickingEnd ? "请点击结束日期（可点同一天）" : "点击日期选择起点" }}</span>
          <div class="drp-footer-actions">
            <button type="button" class="btn ghost small" @click="pickToday">今天</button>
            <button type="button" class="btn primary small" @click="commitManual">确定</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Calendar, ChevronDown, ChevronLeft, ChevronRight } from "@lucide/vue";

const props = defineProps<{
  from: string;
  to: string;
  /** 递增时自动弹出日历面板（例如刚从下拉框切到「自定义日期」时立即展开）。 */
  openSignal?: number;
}>();

const emit = defineEmits<{
  "update:from": [value: string];
  "update:to": [value: string];
  change: [];
}>();

const weekdays = ["一", "二", "三", "四", "五", "六", "日"];
const presets: { label: string; days: number; mode?: "this-month" | "last-month" }[] = [
  { label: "近 7 天", days: 7 },
  { label: "近 14 天", days: 14 },
  { label: "本月", days: 0, mode: "this-month" },
  { label: "上月", days: 0, mode: "last-month" },
  { label: "近 90 天", days: 90 }
];

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

function formatISO(d: Date): string {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

function parseISO(raw: string): Date | null {
  const m = raw.trim().match(/^(\d{4})[-/](\d{1,2})[-/](\d{1,2})$/);
  if (!m) return null;
  const y = Number(m[1]);
  const mo = Number(m[2]) - 1;
  const day = Number(m[3]);
  const d = new Date(y, mo, day);
  if (d.getFullYear() !== y || d.getMonth() !== mo || d.getDate() !== day) return null;
  return d;
}

const now = new Date();
const todayYear = now.getFullYear();
const todayMonth = now.getMonth();
const todayISO = formatISO(now);

const rootRef = ref<HTMLElement | null>(null);
const triggerRef = ref<HTMLElement | null>(null);
const popoverRef = ref<HTMLElement | null>(null);
const open = ref(false);
const viewYear = ref(todayYear);
const viewMonth = ref(todayMonth);
const draftStart = ref("");
const draftEnd = ref("");
const manualFrom = ref("");
const manualTo = ref("");
const hoverDate = ref("");
const pickingEnd = ref(false);
const popoverPos = ref({ top: 0, left: 0 });

const triggerLabel = computed(() => {
  if (props.from && props.to) {
    return props.from === props.to ? props.from : `${props.from} ~ ${props.to}`;
  }
  if (props.from) return `${props.from} ~ 今天`;
  if (props.to) return `截至 ${props.to}`;
  return "选择日期范围";
});

const isCurrentOrFutureMonth = computed(
  () => viewYear.value > todayYear || (viewYear.value === todayYear && viewMonth.value >= todayMonth)
);

const popoverStyle = computed(() => ({
  top: `${popoverPos.value.top}px`,
  left: `${popoverPos.value.left}px`
}));

function syncPosition(): void {
  const trigger = triggerRef.value;
  if (!trigger) return;
  const rect = trigger.getBoundingClientRect();
  const width = 316;
  const height = 352;
  const left = Math.max(12, Math.min(rect.left, window.innerWidth - width - 12));
  const below = rect.bottom + 6;
  const top = below + height <= window.innerHeight - 8 ? below : Math.max(12, rect.top - height - 6);
  popoverPos.value = { top, left };
}

function openPopover(): void {
  draftStart.value = props.from || todayISO;
  draftEnd.value = props.to || props.from || todayISO;
  manualFrom.value = draftStart.value;
  manualTo.value = draftEnd.value;
  pickingEnd.value = false;
  hoverDate.value = "";
  const anchor = parseISO(props.to || props.from) ?? new Date();
  viewYear.value = anchor.getFullYear();
  viewMonth.value = anchor.getMonth();
  open.value = true;
  void nextTick(syncPosition);
}

function close(): void {
  open.value = false;
  pickingEnd.value = false;
  hoverDate.value = "";
}

function toggle(): void {
  if (open.value) close();
  else openPopover();
}

watch(
  () => props.openSignal,
  (next, prev) => {
    if (next && next !== prev) {
      window.setTimeout(openPopover, 0);
    }
  }
);

function shiftMonth(delta: number): void {
  const d = new Date(viewYear.value, viewMonth.value + delta, 1);
  const maxD = new Date(todayYear, todayMonth, 1);
  if (d > maxD) {
    viewYear.value = todayYear;
    viewMonth.value = todayMonth;
    return;
  }
  viewYear.value = d.getFullYear();
  viewMonth.value = d.getMonth();
}

interface CalendarCell {
  iso: string;
  day: number;
  inMonth: boolean;
  future: boolean;
}

const calendarCells = computed<CalendarCell[]>(() => {
  const first = new Date(viewYear.value, viewMonth.value, 1);
  const startOffset = (first.getDay() + 6) % 7; // 周一为 0
  const cells: CalendarCell[] = [];
  for (let i = 0; i < 42; i++) {
    const d = new Date(viewYear.value, viewMonth.value, 1 - startOffset + i);
    const iso = formatISO(d);
    cells.push({
      iso,
      day: d.getDate(),
      inMonth: d.getMonth() === viewMonth.value,
      future: iso > todayISO
    });
  }
  return cells;
});

const previewRange = computed(() => {
  if (pickingEnd.value && draftStart.value && hoverDate.value) {
    return draftStart.value <= hoverDate.value
      ? { start: draftStart.value, end: hoverDate.value }
      : { start: hoverDate.value, end: draftStart.value };
  }
  return { start: draftStart.value, end: draftEnd.value };
});

function isBetween(iso: string, start: string, end: string): boolean {
  if (!start || !end) return false;
  return iso > start && iso < end;
}

function onHoverDay(iso: string): void {
  if (pickingEnd.value) hoverDate.value = iso;
}

function applyRange(fromISO: string, toISO: string): void {
  const [start, end] = fromISO <= toISO ? [fromISO, toISO] : [toISO, fromISO];
  emit("update:from", start);
  emit("update:to", end);
  emit("change");
  close();
}

function onPickDay(iso: string): void {
  if (!pickingEnd.value) {
    draftStart.value = iso;
    draftEnd.value = iso;
    manualFrom.value = iso;
    manualTo.value = iso;
    pickingEnd.value = true;
    return;
  }
  const start = draftStart.value || iso;
  const [s, e] = start <= iso ? [start, iso] : [iso, start];
  manualFrom.value = s;
  manualTo.value = e;
  applyRange(s, e);
}

function pickToday(): void {
  applyRange(todayISO, todayISO);
}

function applyPreset(days: number, mode?: "this-month" | "last-month"): void {
  const today = new Date(todayYear, todayMonth, now.getDate());
  if (mode === "this-month") {
    const first = new Date(todayYear, todayMonth, 1);
    applyRange(formatISO(first), formatISO(today));
    return;
  }
  if (mode === "last-month") {
    const first = new Date(todayYear, todayMonth - 1, 1);
    const last = new Date(todayYear, todayMonth, 0);
    applyRange(formatISO(first), formatISO(last));
    return;
  }
  const start = new Date(today);
  start.setDate(start.getDate() - Math.max(0, days - 1));
  applyRange(formatISO(start), formatISO(today));
}

function commitManual(): void {
  const f = parseISO(manualFrom.value);
  const t = parseISO(manualTo.value);
  const startISO = f ? formatISO(f) : draftStart.value || todayISO;
  const endISO = t ? formatISO(t) : draftEnd.value || startISO;
  const clampedEnd = endISO > todayISO ? todayISO : endISO;
  const clampedStart = startISO > clampedEnd ? clampedEnd : startISO;
  applyRange(clampedStart, clampedEnd);
}

function onPointerDownOutside(event: MouseEvent): void {
  if (!open.value) return;
  const target = event.target as Node | null;
  if (!target) return;
  if (rootRef.value?.contains(target) || popoverRef.value?.contains(target)) return;
  close();
}

function onWindowLayout(): void {
  if (open.value) syncPosition();
}

onMounted(() => {
  document.addEventListener("mousedown", onPointerDownOutside);
  window.addEventListener("resize", onWindowLayout);
  window.addEventListener("scroll", onWindowLayout, true);
  if (props.openSignal) {
    window.setTimeout(openPopover, 0);
  }
});

onBeforeUnmount(() => {
  document.removeEventListener("mousedown", onPointerDownOutside);
  window.removeEventListener("resize", onWindowLayout);
  window.removeEventListener("scroll", onWindowLayout, true);
});
</script>

<style scoped>
.date-range-picker {
  display: inline-flex;
  align-items: center;
}

.date-range-trigger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
  font: inherit;
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  cursor: pointer;
  transition: border-color 0.15s ease, background 0.15s ease;
}

.date-range-trigger:hover,
.date-range-picker.open .date-range-trigger {
  border-color: var(--accent);
}

.date-range-icon,
.date-range-chevron {
  color: var(--muted);
  flex-shrink: 0;
}

.date-range-popover {
  position: fixed;
  z-index: 1100;
  width: 316px;
  padding: 12px;
  border: 1px solid var(--border-strong, var(--border));
  border-radius: var(--radius-lg);
  background: var(--surface);
  box-shadow: var(--shadow-lg, 0 14px 36px rgba(0, 0, 0, 0.28));
  display: flex;
  flex-direction: column;
  gap: 10px;
  box-sizing: border-box;
}

.drp-presets {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.drp-preset-btn {
  padding: 3px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--surface-2);
  color: var(--text-secondary);
  font: inherit;
  font-size: 11.5px;
  cursor: pointer;
  white-space: nowrap;
}

.drp-preset-btn:hover {
  border-color: var(--accent);
  color: var(--accent);
}

.drp-inputs {
  display: flex;
  align-items: center;
  gap: 6px;
}

.drp-input-box {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-size: 11.5px;
}

.drp-input-box > span {
  flex-shrink: 0;
  white-space: nowrap;
}

.drp-input-box.active {
  border-color: var(--accent);
}

.drp-input-box input {
  width: 100%;
  min-width: 0;
  border: 0;
  padding: 0;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  outline: none;
}

.drp-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.drp-nav-side {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.drp-nav-btn {
  display: inline-grid;
  place-items: center;
  width: 26px;
  height: 26px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.drp-nav-btn:hover:not(:disabled) {
  background: var(--surface-2);
  color: var(--text);
}

.drp-nav-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.drp-month-title {
  font-size: 13px;
  font-weight: 600;
}

.drp-weekdays {
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  text-align: center;
  font-size: 11px;
  color: var(--muted);
}

.drp-grid {
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  row-gap: 2px;
}

.drp-day {
  height: 32px;
  padding: 0;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--text);
  font: inherit;
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
  cursor: pointer;
  position: relative;
}

.drp-day.outside {
  color: var(--muted);
  opacity: 0.55;
}

.drp-day.disabled {
  opacity: 0.28;
  cursor: not-allowed;
}

.drp-day:hover:not(:disabled):not(.is-start):not(.is-end) {
  background: var(--surface-2);
}

.drp-day.today:not(.is-start):not(.is-end) {
  color: var(--accent);
  font-weight: 650;
}

.drp-day.in-range {
  border-radius: 0;
  background: var(--accent-soft);
  color: var(--accent-strong, var(--text));
}

.drp-day.is-start,
.drp-day.is-end {
  background: var(--accent);
  color: #fff;
  font-weight: 600;
}

.drp-day.is-start:not(.is-end) {
  border-top-right-radius: 0;
  border-bottom-right-radius: 0;
}

.drp-day.is-end:not(.is-start) {
  border-top-left-radius: 0;
  border-bottom-left-radius: 0;
}

.drp-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding-top: 6px;
  border-top: 1px solid var(--border);
}

.drp-hint {
  font-size: 11px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.drp-footer-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
</style>
