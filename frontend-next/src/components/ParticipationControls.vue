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

type RatingKey = "relevance_level" | "chat_level" | "answerability_level";
type LevelOption = { value: string; label: string; hint: string };
// 档位名后面统一带上后端的分数门槛，数字只在 participation.ts 里维护。
function levelOptions(options: LevelOption[]): LevelOption[] {
  return options.map(option => ({ ...option, label: participationLevelLabel(option.value, { label: option.label }) }));
}

const settings = [
  {
    key: "relevance_level" as const,
    label: "回应提问",
    description: "多确定是在问你，才回应。",
    options: levelOptions([
      { value: "off", label: "从不回应", hint: "不通过提问相关度触发回应；直接回复和主动闲聊仍按各自规则处理。" },
      { value: "minimal", label: "仅明显指向我", hint: "提问对象非常明确时才承接。" },
      { value: "low", label: "较谨慎", hint: "较确定对方在向机器人提问时才承接。" },
      { value: "medium", label: "适中", hint: "正常承接指向机器人的提问和追问。" },
      { value: "high", label: "较积极", hint: "更容易接住不太明确的提问与追问。" },
      { value: "extreme", label: "很积极", hint: "只有较弱的提问指向，也可能回应。" },
      { value: "always", label: "完全不限制", hint: "不设提问相关度门槛，仍需满足回答质量要求。" },
    ]),
  },
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
      { value: "always", label: "完全不限制", hint: "不设闲聊评分门槛，仍受冷却和回答质量要求限制，不是每条必回。" },
    ]),
  },
  {
    key: "answerability_level" as const,
    label: "回答质量要求",
    description: "多有把握说出有用内容，才开口。",
    options: levelOptions([
      { value: "off", label: "完全不限制", hint: "不设回答质量门槛；停止请求和重复回应限制仍生效。" },
      { value: "minimal", label: "很严格", hint: "预计回答很有意义时才放行。" },
      { value: "low", label: "较严格", hint: "对预计回答质量有较高要求。" },
      { value: "medium", label: "适中", hint: "对预计回答质量有适中的要求。" },
      { value: "high", label: "较宽松", hint: "接受更多可能有帮助的回答。" },
      { value: "extreme", label: "很宽松", hint: "只排除预计意义很小的回答。" },
    ]),
  },
];

function ratingLevel(key: RatingKey) {
  return value.value[key] ?? (key === "answerability_level" ? "medium" : preset.value === "off" ? "off" : key === "relevance_level" ? "medium" : preset.value === "max" ? "always" : preset.value);
}
function displayedLevel(key: RatingKey) {
  const level = ratingLevel(key);
  // Both legacy values bypass the quality score. Keep the stored value until edited.
  return key === "answerability_level" && level === "always" ? "off" : level;
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
        括号里是后端的评分门槛：参与度档位越高，要求的分数反而越低（“极低”最严 ≥0.90，“极高”最松 ≥0.10）。
        “回答质量要求”是三项共用的总门槛，“回应提问”和“主动闲聊”是两条独立通道——质量达标，且两条通道任意一条达标，才会开口。
      </p>
      <details class="participation-explanation">
        <summary>了解判断逻辑</summary>
        <div class="explanation-body">
          <p>先检查预计回答质量。通过后，满足“回应提问”，或满足“主动闲聊”且冷却结束，才进入回复流程。</p>
          <p>“回应提问”达标不受闲聊冷却限制。关闭其中一项，只关闭对应的接话途径；回答质量要求选“完全不限制”时不设质量门槛。</p>
          <p>需要搜索或调用工具不代表无法回答。停止请求和重复循环仍保持沉默；已经进入直接回复流程的请求不受这里的路由条件影响。</p>
          <p>参与档位越积极，评分门槛越低；回答质量要求越严格，评分门槛越高。切换档位不会重设冷却时间。</p>
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
