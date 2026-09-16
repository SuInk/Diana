<script setup lang="ts">
import { computed, useId } from "vue";
import AppSelect from "./AppSelect.vue";
import { defaultParticipationCooldownSeconds, participationLevelLabel, participationPreset, participationPresetName, type ParticipationPreferences } from "../participation";

const props = defineProps<{ modelValue?: ParticipationPreferences; level?: string; inheritable?: boolean; inheritedValue?: ParticipationPreferences }>();
const emit = defineEmits<{ "update:modelValue": [value: ParticipationPreferences | undefined] }>();
const id = useId();
const value = computed(() => props.modelValue ?? (props.level ? participationPreset(props.level) : props.inheritedValue ?? participationPreset("low")));
const inherited = computed(() => props.inheritable && !props.modelValue && !props.level);
const cooldownSeconds = computed(() => value.value.cooldown_seconds ?? defaultParticipationCooldownSeconds);
const preset = computed(() => participationPresetName(value.value));

type RatingKey = "chat_level";
type LevelOption = { value: string; label: string; hint: string };
// 档位名后面统一带上后端的分数门槛，数字只在 participation.ts 里维护。
function levelOptions(options: LevelOption[]): LevelOption[] {
  return options.map(option => ({ ...option, label: participationLevelLabel(option.value, { label: option.label }) }));
}

const settings = [
  {
    key: "chat_level" as const,
    label: "主动闲聊",
    description: "没人叫你时，多愿意插话。",
    options: levelOptions([
      { value: "off", label: "从不闲聊", hint: "不主动插话，回应提问仍可独立生效。" },
      { value: "minimal", label: "很少插话", hint: "非常适合参与时才开口。" },
      { value: "low", label: "偶尔接话", hint: "比较适合参与时才接一句。" },
      { value: "medium", label: "适度参与", hint: "有合适的话就自然加入。" },
      { value: "high", label: "积极参与", hint: "更容易参与分享和闲聊。" },
      { value: "extreme", label: "频繁参与", hint: "较弱的接话机会也可能开口。" },
      { value: "always", label: "完全不限制", hint: "不设闲聊评分门槛，仍受冷却和发言占比限制，不是每条必回。" },
    ]),
  },
];

function ratingLevel(key: RatingKey) {
  return value.value[key] ?? (preset.value === "off" ? "off" : preset.value === "max" ? "always" : preset.value);
}
// 回应提问只有开关：明确在跟机器人说话就回。旧配置里的七档名称除 off 以外都算打开。
const relevanceEnabled = computed(() => {
  const stored = value.value.relevance_level;
  if (stored === undefined) return preset.value !== "off";
  return stored !== "off";
});
function setRelevanceEnabled(enabled: boolean) {
  emit("update:modelValue", { ...value.value, relevance_level: enabled ? "on" : "off" });
}
function displayedLevel(key: RatingKey) {
  return ratingLevel(key);
}
function setRatingLevel(key: RatingKey, level: string) {
  emit("update:modelValue", { ...value.value, [key]: level });
}
function updateCooldown(event: Event) {
  const input = event.target as HTMLInputElement;
  if (input.value === "" || !Number.isFinite(input.valueAsNumber)) return;
  const next = Math.max(0, Math.min(3600, Math.round(input.valueAsNumber)));
  input.value = String(next);
  emit("update:modelValue", { ...value.value, cooldown_seconds: next });
}
function restoreCooldown(event: Event) {
  (event.target as HTMLInputElement).value = String(cooldownSeconds.value);
}
</script>

<template>
  <div class="participation-controls">
    <div v-if="inheritable" class="configuration-source">
      <div class="setting-copy">
        <label :for="id + '-source'">配置来源</label>
        <p v-if="inherited" class="setting-help">使用所属机器人的接话设置。</p>
      </div>
      <AppSelect :id="id + '-source'" aria-label="配置来源" :model-value="inherited ? 'inherit' : 'custom'" :options="[{value:'inherit',label:'跟随机器人'},{value:'custom',label:'本群设置'}]" @update:model-value="emit('update:modelValue', $event === 'inherit' ? undefined : {...value})" />
    </div>
    <template v-if="!inherited">
      <div class="participation-fields">
        <section class="participation-setting">
          <div class="setting-copy">
            <label :for="id + '-relevance'">回应提问</label>
            <p class="setting-help">明确在跟你说话时回应。关掉之后只靠主动闲聊开口。</p>
          </div>
          <label class="switch relevance-switch">
            <input :id="id + '-relevance'" type="checkbox" :checked="relevanceEnabled" @change="setRelevanceEnabled(($event.target as HTMLInputElement).checked)" />
            <span class="track" aria-hidden="true"></span>
            <span class="switch-label">{{ relevanceEnabled ? "开启" : "关闭" }}</span>
          </label>
        </section>
        <section v-for="setting in settings" :key="setting.key" class="participation-setting">
          <div class="setting-copy">
            <label :for="id + '-' + setting.key">{{ setting.label }}</label>
            <p class="setting-help">{{ setting.description }}</p>
          </div>
          <AppSelect :id="id + '-' + setting.key" :aria-label="setting.label" :model-value="displayedLevel(setting.key)" :options="setting.options" @update:model-value="setRatingLevel(setting.key, $event)" />
          <div v-if="setting.key === 'chat_level'" class="field wide cooldown-setting">
            <label :for="id + '-cooldown'">闲聊冷却时间（秒）</label>
            <input :id="id + '-cooldown'" class="input" type="number" inputmode="numeric" min="0" max="3600" step="1" aria-label="主动闲聊冷却秒数" :aria-describedby="id + '-cooldown-help'" :value="cooldownSeconds" @input="updateCooldown" @blur="restoreCooldown" />
            <span :id="id + '-cooldown-help'" class="hint">主动闲聊的最短间隔，默认 30 秒；填 0 不限制。</span>
          </div>
        </section>
      </div>
      <p class="hint participation-gate-hint">
        闲聊档位括号里是后端的评分门槛：档位越积极，要求的分数越低（“很少插话”最严 ≥0.90，“频繁参与”最松 ≥0.10）。
        “回应提问”和“主动闲聊”是两条独立通道，任意一条达标就会开口。
      </p>
      <details class="participation-explanation">
        <summary>了解判断逻辑</summary>
        <div class="explanation-body">
          <p>满足“回应提问”，或满足“主动闲聊”且冷却结束，就进入回复流程。</p>
          <p>“回应提问”达标不受闲聊冷却限制。关闭其中一项，只关闭对应的接话途径。</p>
          <p>问某个群友本人才知道的事（去不去、做没做）不算在问你。附和、接梗算正常闲聊；原样复读、给没依据的说法编理由，闲聊分会很低。</p>
          <p>需要搜索或调用工具不代表无法回答。停止请求和重复循环仍保持沉默；已经进入直接回复流程的请求不受这里的路由条件影响。</p>
          <p>回应提问只判断是或否：模型认定明确在跟你说话（@、叫你的名字、接着你的话说），就回应，不打分。只有主动闲聊按分数和档位判断，档位越积极，评分门槛越低。切换档位不会重设冷却时间。</p>
        </div>
      </details>
    </template>
  </div>
</template>

<style scoped>
.participation-controls { container-type: inline-size; display: grid; gap: 12px; min-width: 0; }
.participation-fields { display: grid; min-width: 0; }
/* 档位名后面要跟得下「（≥0.90，最严）」，220px 会把门槛截成省略号。 */
.participation-setting, .configuration-source { display: grid; grid-template-columns: minmax(0, 1fr) 260px; align-items: center; gap: 12px 24px; min-width: 0; }
.participation-setting { padding: 14px 0; border-bottom: 1px solid var(--border); }
.participation-setting:first-child { padding-top: 4px; }
.participation-setting:last-child { border-bottom: 0; }
.setting-copy { display: grid; gap: 4px; min-width: 0; }
.setting-copy > label { display: block; margin: 0; color: var(--text); font-size: 14px; font-weight: 600; line-height: 1.5; }
.participation-setting > .app-select, .configuration-source > .app-select { width: 260px; max-width: 100%; min-width: 0; }
.relevance-switch { justify-self: start; }
.setting-help { margin: 0; color: var(--muted); font-size: 12px; line-height: 1.6; overflow-wrap: anywhere; }
.cooldown-setting { margin-top: 8px; }
/* .hint 只在 .field 里有样式，这行提示不在表单项内，颜色字号得自己写齐。 */
.participation-gate-hint { margin: 0; max-width: 900px; color: var(--muted); font-size: 12px; line-height: 1.7; }
.participation-explanation { border-top: 1px solid var(--border); padding-top: 12px; min-width: 0; }
.participation-explanation summary { width: fit-content; cursor: pointer; color: var(--muted); font-size: 13px; line-height: 1.6; }
.participation-explanation summary:hover { color: var(--text); }
.participation-explanation summary:focus-visible { outline: 2px solid var(--accent); outline-offset: 4px; }
.explanation-body { max-width: 900px; color: var(--muted); font-size: 12px; line-height: 1.7; }
.explanation-body p { margin: 10px 0 0; }
@container (max-width: 520px) {
  .participation-setting, .configuration-source { grid-template-columns: minmax(0, 1fr); gap: 8px; }
  .participation-setting > .app-select, .configuration-source > .app-select { width: 100%; }
}
</style>
