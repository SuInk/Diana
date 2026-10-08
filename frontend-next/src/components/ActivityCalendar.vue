<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <!-- 照 GitHub 贡献图的做法：固定近一年、格子大小不变，窄了横向滚动并停在最近这头；
       月份在上，只标一三五；悬停浮出当天读数；底部左边说口径，右边是图例。 -->
  <section class="activity-calendar" aria-label="每日消息量">
    <LoadingSkeleton v-if="loading && !data" kind="chart" label="正在读取每日消息量" />
    <p v-else-if="error" class="hint text-err" style="margin: 0">{{ error }}</p>
    <template v-else-if="data">
      <div ref="scroller" class="calendar-scroll">
        <div
          class="calendar-grid"
          :style="{ gridTemplateColumns: `${labelWidth}px repeat(${columns.length}, ${cellSize}px)` }"
          @mouseleave="pointed = null"
        >
          <span />
          <span v-for="(week, index) in columns" :key="`m${index}`" class="calendar-month muted">{{ week.month ? `${week.month}月` : "" }}</span>
          <template v-for="weekday in 7" :key="`r${weekday}`">
            <span class="calendar-weekday muted">{{ weekday % 2 === 1 && weekday < 7 ? weekdayShort[weekday - 1] : "" }}</span>
            <template v-for="week in columns" :key="`${week.days[weekday - 1].date}`">
              <button
                v-if="week.days[weekday - 1].count !== null"
                type="button"
                class="calendar-cell"
                :style="{ background: activityLevelColor(activityLevel(week.days[weekday - 1].count ?? 0, peak)) }"
                :aria-label="dayText(week.days[weekday - 1].date, week.days[weekday - 1].count ?? 0)"
                @mouseenter="point($event, week.days[weekday - 1].date, week.days[weekday - 1].count ?? 0)"
                @focus="point($event, week.days[weekday - 1].date, week.days[weekday - 1].count ?? 0)"
                @click="point($event, week.days[weekday - 1].date, week.days[weekday - 1].count ?? 0)"
                @blur="pointed = null"
              />
              <span v-else />
            </template>
          </template>
        </div>
      </div>
      <div class="calendar-footer">
        <span class="muted calendar-footnote">{{ footnote }}</span>
        <span class="calendar-legend muted" aria-hidden="true">
          少
          <i v-for="level in legendLevels" :key="level" :style="{ background: activityLevelColor(level) }" />
          多
        </span>
      </div>
      <Teleport to="body">
        <div v-if="pointed" class="calendar-tooltip" role="tooltip" :style="{ left: `${pointed.x}px`, top: `${pointed.y}px` }">{{ pointed.text }}</div>
      </Teleport>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { getStatsActivity, type StatsActivity } from "../api";
import { activityLevel, activityLevelColor, activityTimezoneNote, buildActivityCalendar } from "../activity";
import { formatNumber } from "../format";
import LoadingSkeleton from "./LoadingSkeleton.vue";

const props = defineProps<{ profileId: string }>();
const emit = defineEmits<{ caption: [text: string] }>();

const weekdayShort = ["一", "二", "三", "四", "五", "六", "日"];
const weekdayLong = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];
const legendLevels = [0, 1, 2, 3, 4];
// GitHub 的尺寸：10px 格子、3px 间距，53 周正好一年。
const cellSize = 10;
const labelWidth = 22;
const weeks = 53;

const scroller = ref<HTMLElement | null>(null);
const data = ref<StatsActivity | null>(null);
const loading = ref(false);
const error = ref("");
const pointed = ref<{ text: string; x: number; y: number } | null>(null);

let requestSeq = 0;

async function load(): Promise<void> {
  const seq = ++requestSeq;
  loading.value = true;
  error.value = "";
  try {
    const result = await getStatsActivity(props.profileId);
    if (seq !== requestSeq) return;
    data.value = result;
    // 窄的时候横向滚动，先露出最近这头：看日历多半是想知道这几天怎么样。
    await nextTick();
    if (scroller.value) scroller.value.scrollLeft = scroller.value.scrollWidth;
  } catch (err) {
    if (seq === requestSeq) error.value = err instanceof Error ? err.message : "读取每日消息量失败";
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

watch(() => props.profileId, load, { immediate: true });
onBeforeUnmount(() => {
  pointed.value = null;
  emit("caption", "");
});

const columns = computed(() => (data.value ? buildActivityCalendar(data.value.today, data.value.days, weeks) : []));
const visible = computed(() => columns.value.flatMap((week) => week.days).filter((cell) => cell.count !== null));
const peak = computed(() => Math.max(0, ...visible.value.map((cell) => cell.count ?? 0)));

function dayText(date: string, count: number): string {
  const [, month, day] = date.split("-").map(Number);
  const weekday = weekdayLong[(new Date(`${date}T00:00:00Z`).getUTCDay() + 6) % 7];
  return `${month}月${day}日 ${weekday} · ${count > 0 ? `${formatNumber(count)} 条消息` : "没有消息"}`;
}

// 提示挂到 body 上按视口定位：卡片和滚动容器都会裁掉溢出的内容，放在里面会被切。
function point(event: Event, date: string, count: number): void {
  const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
  pointed.value = { text: dayText(date, count), x: rect.left + rect.width / 2, y: rect.top };
}

// 和 GitHub 一样，标题本身就是汇总。
const caption = computed(() => {
  if (!data.value) return "";
  const total = visible.value.reduce((sum, cell) => sum + (cell.count ?? 0), 0);
  return `近一年 ${formatNumber(total)} 条消息`;
});
watch(caption, (text) => emit("caption", text), { immediate: true });

const footnote = computed(() => activityTimezoneNote(data.value?.utc_offset, "日期") || "按收到消息的日期统计");
</script>

<style scoped>
.activity-calendar {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

/* 滚动条藏起来：Windows 上它会占一截高度，卡片就和 24 小时视图对不齐了。触控板和
   手指照样能横向滑动，打开时已经停在最近这头。 */
.calendar-scroll {
  overflow-x: auto;
  scrollbar-width: none;
}

.calendar-scroll::-webkit-scrollbar {
  display: none;
}

.calendar-grid {
  display: grid;
  grid-template-rows: 15px repeat(7, 10px);
  gap: 3px;
  width: max-content;
}

.calendar-month {
  width: 0;
  font-size: 11px;
  line-height: 15px;
  white-space: nowrap;
}

.calendar-weekday {
  font-size: 10px;
  line-height: 10px;
}

.calendar-cell {
  width: 10px;
  height: 10px;
  padding: 0;
  border: none;
  border-radius: 2px;
  /* GitHub 给每格描一圈很淡的边，空格子在深色底上也认得出是格子。 */
  box-shadow: inset 0 0 0 1px rgba(127, 127, 127, 0.08);
  cursor: pointer;
}

.calendar-cell:focus-visible {
  outline: 2px solid var(--text);
  outline-offset: 1px;
}

.calendar-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 11.5px;
}

.calendar-footnote {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.calendar-legend {
  display: inline-flex;
  flex-shrink: 0;
  align-items: center;
  gap: 3px;
}

.calendar-legend i {
  width: 10px;
  height: 10px;
  border-radius: 2px;
}

.calendar-tooltip {
  position: fixed;
  z-index: 1000;
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--text);
  color: var(--surface);
  font-size: 12px;
  white-space: nowrap;
  pointer-events: none;
  transform: translate(-50%, calc(-100% - 6px));
}
</style>
