<script setup lang="ts">
/**
 * 凭据输入框：默认掩码显示，点眼睛才向后端索取明文。
 *
 * 所有平台的密钥都是同一套交互——留空表示沿用已存的那份，填了才覆盖——所以
 * 抽成一个组件，避免每个平台各抄一遍眼睛按钮和占位文案的判断。
 */
import { Eye, EyeOff } from "@lucide/vue";

defineProps<{
  /** 输入框 id，供 label 的 for 关联。 */
  id: string;
  label: string;
  /** 后端已存有这个凭据；决定占位文案说「留空沿用」还是给填写指引。 */
  configured?: boolean;
  /** 未配置时的占位文案，通常写「去哪里拿这个值」。 */
  placeholder?: string;
  hint?: string;
  revealed?: boolean;
  busy?: boolean;
}>();

const model = defineModel<string>({ required: true });

const emit = defineEmits<{ (event: "toggle-reveal"): void }>();
</script>

<template>
  <div class="field">
    <label :for="id">{{ label }}</label>
    <div class="input-group">
      <input
        :id="id"
        v-model="model"
        class="input"
        :type="revealed ? 'text' : 'password'"
        autocomplete="off"
        :placeholder="configured ? '已配置 — 留空沿用，填写则覆盖' : placeholder"
      />
      <button
        class="btn icon-only"
        type="button"
        :disabled="busy"
        :aria-label="revealed ? `隐藏${label}` : `查看${label}`"
        @click="emit('toggle-reveal')"
      >
        <EyeOff v-if="revealed" :size="14" aria-hidden="true" />
        <Eye v-else :size="14" aria-hidden="true" />
      </button>
    </div>
    <span v-if="hint" class="hint">{{ hint }}</span>
  </div>
</template>
