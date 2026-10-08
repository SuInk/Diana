<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <section class="activity-heatmap stack" aria-label="活跃时段">
    <div class="activity-head">
      <h3>活跃时段</h3>
      <div class="activity-controls">
        <select v-model="groupID" class="input activity-group" aria-label="按群聊筛选">
          <option value="">全部群聊和私聊</option>
          <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
        </select>
        <div class="segmented" role="radiogroup" aria-label="活跃时段统计窗口">
          <button
            v-for="option in windowOptions"
            :key="option.id"
            type="button"
            role="radio"
            :class="{ active: selected === option.id }"
            :aria-checked="selected === option.id"
            @click="selected = option.id"
          >
            {{ option.label }}
          </button>
        </div>
      </div>
    </div>

    <LoadingSkeleton v-if="loading && !data" kind="chart" label="正在读取活跃时段" />
    <p v-else-if="error" class="hint text-err" style="margin: 0">{{ error }}</p>
    <p v-else-if="!current || current.total === 0" class="hint" style="margin: 0">{{ scopeLabel }}{{ windowLabel }}没有收到消息，画不出活跃时段。</p>
    <template v-else>
      <div class="activity-tiles">
        <div v-for="tile in tiles" :key="tile.label" class="activity-tile" :title="tile.hint">
          <span class="muted">{{ tile.label }}</span>
          <strong>{{ tile.value }}</strong>
          <span class="muted activity-tile-foot">{{ tile.foot }}</span>
        </div>
      </div>

      <div class="activity-grid-wrap">
        <div class="activity-grid" @mouseleave="pointed = null">
          <span />
          <span v-for="hour in 24" :key="`h${hour}`" class="activity-hour muted">{{ (hour - 1) % 3 === 0 ? hour - 1 : "" }}</span>
          <template v-for="(row, day) in current.cells" :key="`d${day}`">
            <span class="activity-day muted">{{ weekdayShort[day] }}</span>
            <button
              v-for="(count, hour) in row"
              :key="`c${day}-${hour}`"
              type="button"
              class="activity-cell"
              :class="{ pointed: pointed && pointed.day === day && pointed.hour === hour }"
              :style="{ background: cellColor(count) }"
              :aria-label="cellText(day, hour, count)"
              @mouseenter="pointed = { day, hour }"
              @focus="pointed = { day, hour }"
              @click="pointed = { day, hour }"
            />
          </template>
        </div>
      </div>
      <div class="activity-readout">
        <span v-if="pointedText">{{ pointedText }}</span>
        <span v-else class="muted">指向或点按格子查看具体数字</span>
        <span class="activity-legend muted" aria-hidden="true">
          少
          <i v-for="level in legendLevels" :key="level" :style="{ background: activityLevelColor(level) }" />
          多
        </span>
      </div>
    </template>

    <p v-if="current && current.total > 0 && partialNote" class="hint" style="margin: 0">{{ partialNote }}</p>
    <p class="hint" style="margin: 0">
      按消息发生时间统计收到的消息，和卡片上的「收到消息」同一口径；颜色深浅只在当前窗口内相比。{{ timezoneNote }}
    </p>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { getStatsActivity, type ActivityWindowID, type StatsActivity } from "../api";
import { activityLevel, activityLevelColor, activityTimezoneNote } from "../activity";
import { formatNumber, formatTime } from "../format";
import LoadingSkeleton from "./LoadingSkeleton.vue";

const props = defineProps<{
  profileId: string;
  /** 这台机器人加入的群，供筛选；为空时下拉框只有「全部」。 */
  groups: readonly { id: string; name: string }[];
}>();

const windowOptions: { id: ActivityWindowID; label: string }[] = [
  { id: "7d", label: "7 天" },
  { id: "28d", label: "4 周" }
];
const weekdayShort = ["一", "二", "三", "四", "五", "六", "日"];
const weekdayLong = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];
const legendLevels = [0, 1, 2, 3, 4];

const selected = ref<ActivityWindowID>("28d");
const groupID = ref("");
const data = ref<StatsActivity | null>(null);
const loading = ref(false);
const error = ref("");
const pointed = ref<{ day: number; hour: number } | null>(null);

// 切换机器人或群时先发的请求可能后返回，只认最后一次，免得选的是这个群、画的是上一个。
let requestSeq = 0;

async function load(): Promise<void> {
  const seq = ++requestSeq;
  loading.value = true;
  error.value = "";
  try {
    const result = await getStatsActivity(props.profileId, groupID.value);
    if (seq === requestSeq) data.value = result;
  } catch (err) {
    if (seq === requestSeq) error.value = err instanceof Error ? err.message : "读取活跃时段失败";
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

onMounted(load);
watch(groupID, load);
// 换了机器人，原来选的群多半不在新机器人的群列表里，退回「全部」再拉。
watch(
  () => props.profileId,
  () => {
    if (groupID.value) groupID.value = "";
    else void load();
  }
);

const current = computed(() => data.value?.windows.find((window) => window.id === selected.value) ?? null);
const windowLabel = computed(() => `最近 ${windowOptions.find((option) => option.id === selected.value)?.label ?? ""}`);
const scopeLabel = computed(() => (groupID.value ? `${props.groups.find((group) => group.id === groupID.value)?.name ?? `群 ${groupID.value}`} ` : ""));
const weeks = computed(() => Math.max(1, current.value?.weeks ?? 1));
const peak = computed(() => Math.max(0, ...(current.value?.cells.flat() ?? [0])));

function cellColor(count: number): string {
  return activityLevelColor(activityLevel(count, peak.value));
}

function perWeek(count: number): string {
  if (count <= 0) return "没有消息";
  if (weeks.value <= 1) return `${formatNumber(count)} 条`;
  const average = count / weeks.value;
  return `平均每周 ${average >= 10 ? formatNumber(Math.round(average)) : average.toFixed(1)} 条`;
}

function hourRange(hour: number): string {
  return `${hour}–${hour + 1} 时`;
}

function cellText(day: number, hour: number, count: number): string {
  const total = weeks.value > 1 && count > 0 ? `，共 ${formatNumber(count)} 条` : "";
  return `${weekdayLong[day]} ${hourRange(hour)}：${perWeek(count)}${total}`;
}

const pointedText = computed(() => {
  const cell = pointed.value;
  if (!cell || !current.value) return "";
  return cellText(cell.day, cell.hour, current.value.cells[cell.day]?.[cell.hour] ?? 0);
});

// 三张小卡回答的是「什么时候人最多、什么时候没人」：最热闹的那一格，以及把七天叠起来
// 之后一天里最热闹、最冷清的钟点——后者正好是设免打扰或主动搭话时段时要看的。
const tiles = computed(() => {
  const window = current.value;
  if (!window) return [];
  let peakDay = 0;
  let peakHour = 0;
  window.cells.forEach((row, day) =>
    row.forEach((count, hour) => {
      if (count > window.cells[peakDay][peakHour]) {
        peakDay = day;
        peakHour = hour;
      }
    })
  );
  const byHour = Array.from({ length: 24 }, (_, hour) => window.cells.reduce((sum, row) => sum + (row[hour] ?? 0), 0));
  const busiest = byHour.indexOf(Math.max(...byHour));
  const quietest = byHour.indexOf(Math.min(...byHour));
  const perDay = (count: number) => {
    if (count <= 0) return "没有消息";
    const average = count / (weeks.value * 7);
    return `平均每天 ${average >= 10 ? formatNumber(Math.round(average)) : average.toFixed(1)} 条`;
  };
  return [
    {
      label: "最热闹",
      value: `${weekdayLong[peakDay]} ${peakHour} 时`,
      foot: perWeek(window.cells[peakDay][peakHour]),
      hint: "星期几和钟点组合起来，消息最多的那一格"
    },
    { label: "每天最热闹", value: hourRange(busiest), foot: perDay(byHour[busiest]), hint: "七天叠在一起看，消息最多的钟点" },
    { label: "每天最冷清", value: hourRange(quietest), foot: perDay(byHour[quietest]), hint: "七天叠在一起看，消息最少的钟点" }
  ];
});

// 实例装上不满一个窗口时，按整窗口除出来的周均会偏低，得说出来。
const partialNote = computed(() => {
  const window = current.value;
  if (!window?.first_event_at) return "";
  const gap = new Date(window.first_event_at).getTime() - new Date(window.since).getTime();
  if (gap < 86_400_000) return "";
  return `最早一条消息在 ${formatTime(window.first_event_at)}，还没记满${windowLabel.value}，平均数会偏低。`;
});

const timezoneNote = computed(() => activityTimezoneNote(data.value?.utc_offset, "钟点"));
</script>

<style scoped>
.activity-heatmap {
  gap: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}

.activity-heatmap h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.activity-head,
.activity-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.activity-group {
  width: auto;
  max-width: 220px;
}

.activity-tiles {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}

.activity-tile {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  padding: 10px 12px;
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  font-size: 12px;
}

.activity-tile strong {
  font-size: 15px;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.activity-tile-foot {
  font-variant-numeric: tabular-nums;
}

.activity-grid-wrap {
  overflow-x: auto;
}

/* GitHub 式的小方格：格子有上限不随容器拉伸，留白比被拉成长条好读。 */
.activity-grid {
  display: grid;
  grid-template-columns: 18px repeat(24, minmax(8px, 13px));
  gap: 3px;
  width: max-content;
  max-width: 100%;
}

.activity-hour {
  width: 0;
  font-size: 10px;
  line-height: 13px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.activity-day {
  font-size: 10px;
  line-height: 1;
  align-self: center;
}

.activity-cell {
  aspect-ratio: 1;
  padding: 0;
  border: none;
  border-radius: 2px;
  cursor: pointer;
}

.activity-cell.pointed,
.activity-cell:focus-visible {
  outline: 2px solid var(--text);
  outline-offset: 1px;
}

.activity-readout {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 20px;
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
}

.activity-legend {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
}

.activity-legend i {
  width: 11px;
  height: 11px;
  border-radius: 2px;
}

@media (max-width: 560px) {
  .activity-tiles {
    gap: 6px;
  }

  .activity-tile {
    padding: 8px;
    font-size: 11px;
  }

  .activity-tile strong {
    font-size: 13px;
  }

  .activity-grid {
    gap: 2px;
  }
}
</style>
