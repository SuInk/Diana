<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <div ref="rootRef" class="app-select" :class="{ open }">
    <button :id="id" ref="triggerRef" type="button" class="app-select-trigger" :disabled="disabled" @click="toggle" @keydown="onKeydown">
      <img v-if="currentAvatar" class="app-select-avatar" :src="currentAvatar" alt="" loading="lazy" @error="hideAvatar" />
      <span class="app-select-value">{{ currentLabel }}</span>
      <ChevronDown :size="14" class="app-select-chevron" aria-hidden="true" />
    </button>
    <!-- 菜单挂到 body：留在原位的话，弹窗（.modal-body 有 overflow-y: auto）
         会把它裁掉，靠底部的下拉只能看见头一两项。 -->
    <Teleport to="body">
      <div v-if="open" ref="menuRef" class="app-select-menu" role="listbox" :style="menuStyle">
        <div v-if="searchable" class="app-select-search">
          <Search :size="13" aria-hidden="true" />
          <input
            ref="searchRef"
            v-model="keyword"
            type="text"
            :placeholder="searchPlaceholder || '搜索'"
            :aria-label="searchPlaceholder || '搜索选项'"
            @keydown.escape.stop="close"
          />
        </div>
        <p v-if="visibleGroups.length === 0" class="app-select-empty muted">没有匹配的选项</p>
        <template v-for="section in visibleGroups" :key="section.name || '_'">
        <p v-if="section.name" class="app-select-group">{{ section.name }}</p>
        <button
          v-for="option in section.options"
          :key="option.value"
          type="button"
          class="app-select-item"
          :class="{ active: option.value === modelValue }"
          role="option"
          :aria-selected="option.value === modelValue"
          @click="pick(option.value)"
        >
          <Check v-if="option.value === modelValue" :size="14" aria-hidden="true" />
          <span v-else class="app-select-item-pad" aria-hidden="true"></span>
          <img v-if="option.avatar && !brokenAvatars.has(option.avatar)" class="app-select-avatar" :src="option.avatar" alt="" loading="lazy" @error="hideAvatar" />
          <span class="app-select-item-label">
            <span class="app-select-item-text">{{ option.label }}</span>
            <small v-if="option.hint" class="muted">{{ option.hint }}</small>
          </span>
        </button>
        </template>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Check, ChevronDown, Search } from "@lucide/vue";

export interface AppSelectOption {
  value: string;
  label: string;
  hint?: string;
  /** 可选头像。取不到图时自动隐藏，不留破图占位。 */
  avatar?: string;
  /** 分段标题。相邻的同名选项归到一段，留空表示不分段。 */
  group?: string;
}

const props = defineProps<{
  modelValue: string;
  options: AppSelectOption[];
  id?: string;
  disabled?: boolean;
  /** 选项多到需要翻找时打开：菜单顶部出现搜索框，按标签和 hint 过滤。 */
  searchable?: boolean;
  searchPlaceholder?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [string] }>();

const open = ref(false);
const rootRef = ref<HTMLElement | null>(null);
const searchRef = ref<HTMLInputElement | null>(null);
const keyword = ref("");

// 过滤同时看标签和 hint：hint 里放的是群号和条数，按号找群的人不必先想起名字。
const filteredOptions = computed(() => {
  const term = keyword.value.trim().toLowerCase();
  if (!term) return props.options;
  return props.options.filter((option) => `${option.label} ${option.hint ?? ""}`.toLowerCase().includes(term));
});

// 按 group 归段，保持选项本身的顺序；没有 group 的归到无标题段。
const visibleGroups = computed(() => {
  const sections: Array<{ name: string; options: AppSelectOption[] }> = [];
  for (const option of filteredOptions.value) {
    const name = option.group ?? "";
    const last = sections[sections.length - 1];
    if (last && last.name === name) {
      last.options.push(option);
      continue;
    }
    sections.push({ name, options: [option] });
  }
  return sections;
});

// 每次打开都从空关键词开始并聚焦搜索框：留着上次的词会让人以为选项变少了。
watch(open, (value) => {
  if (!value) {
    keyword.value = "";
    return;
  }
  if (props.searchable) void nextTick(() => searchRef.value?.focus());
});
const triggerRef = ref<HTMLElement | null>(null);
const menuRef = ref<HTMLElement | null>(null);

const currentLabel = computed(() => props.options.find((option) => option.value === props.modelValue)?.label ?? props.modelValue);

// 头像来自外部图床（QQ 的 qlogo 等），加载失败很常见：记下来直接不显示，
// 让位置塌掉，好过留一个破图图标。
const brokenAvatars = ref(new Set<string>());
const currentAvatar = computed(() => {
  const avatar = props.options.find((option) => option.value === props.modelValue)?.avatar;
  return avatar && !brokenAvatars.value.has(avatar) ? avatar : "";
});

function hideAvatar(event: Event): void {
  const src = (event.target as HTMLImageElement | null)?.src;
  if (src) brokenAvatars.value = new Set(brokenAvatars.value).add(src);
}

// 菜单脱离了原来的定位父级，位置只能自己算。
const menuGap = 4;
const viewportMargin = 8;
const menuMinHeight = 132;
const menuMaxHeight = 300;
const menuStyle = ref<Record<string, string>>({});

function placeMenu(): void {
  const trigger = triggerRef.value;
  if (!trigger) return;
  const rect = trigger.getBoundingClientRect();
  const below = window.innerHeight - rect.bottom - menuGap - viewportMargin;
  const above = rect.top - menuGap - viewportMargin;
  // 菜单还没渲染时按选项数估个高度，估得够准，展开后不会再跳一次位置。
  const rendered = menuRef.value?.scrollHeight ?? 0;
  const wanted = Math.min(rendered > 0 ? rendered : props.options.length * 34 + 8, menuMaxHeight);
  // 下面装不下整份列表、而上面更宽敞时才翻上去。
  const dropUp = below < wanted && above > below;
  const room = Math.max(dropUp ? above : below, menuMinHeight);
  // 贴住触发器左沿，但不许探出视口右侧；宽度按内容撑开，长群名就不会被截断。
  const left = Math.max(viewportMargin, Math.min(rect.left, window.innerWidth - viewportMargin - rect.width));
  const style: Record<string, string> = {
    left: `${Math.round(left)}px`,
    minWidth: `${Math.round(rect.width)}px`,
    maxWidth: `${Math.round(window.innerWidth - viewportMargin - left)}px`,
    maxHeight: `${Math.round(Math.min(room, menuMaxHeight))}px`
  };
  if (dropUp) {
    style.bottom = `${Math.round(window.innerHeight - rect.top + menuGap)}px`;
  } else {
    style.top = `${Math.round(rect.bottom + menuGap)}px`;
  }
  menuStyle.value = style;
}

function close(): void {
  open.value = false;
}

function toggle(): void {
  open.value = !open.value;
}

watch(open, async (isOpen) => {
  if (!isOpen) return;
  placeMenu();
  await nextTick();
  placeMenu();
});

function pick(value: string): void {
  emit("update:modelValue", value);
  open.value = false;
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") {
    open.value = false;
    return;
  }
  // 上下键在选项间移动，无需展开也能快速切换。
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    const list = filteredOptions.value;
    const index = list.findIndex((option) => option.value === props.modelValue);
    const next = event.key === "ArrowDown" ? index + 1 : index - 1;
    if (next >= 0 && next < list.length) {
      emit("update:modelValue", list[next].value);
    }
  }
}

function onDocumentClick(event: MouseEvent): void {
  if (!open.value) return;
  const target = event.target as Node;
  // 菜单已经不在组件的 DOM 子树里了，点它也得算「点在里面」。
  if (rootRef.value?.contains(target) || menuRef.value?.contains(target)) return;
  open.value = false;
}

// 弹窗正文能滚动，触发器一动菜单就得跟着走，否则会浮在原地。
function onViewportChange(): void {
  if (open.value) placeMenu();
}

onMounted(() => {
  document.addEventListener("mousedown", onDocumentClick);
  window.addEventListener("scroll", onViewportChange, true);
  window.addEventListener("resize", onViewportChange);
});
onBeforeUnmount(() => {
  document.removeEventListener("mousedown", onDocumentClick);
  window.removeEventListener("scroll", onViewportChange, true);
  window.removeEventListener("resize", onViewportChange);
});
</script>

<style scoped>
.app-select-search {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border);
  color: var(--muted);
}

.app-select-search input {
  width: 100%;
  min-width: 0;
  border: 0;
  background: none;
  color: var(--text);
  font: inherit;
  font-size: 12px;
  outline: none;
}

.app-select-group {
  margin: 0;
  padding: 6px 10px 2px;
  color: var(--muted);
  font-size: 11px;
}

.app-select-empty {
  margin: 0;
  padding: 10px;
  font-size: 12px;
}

/* 头像只是辨识用的小图，不该把行高撑起来。 */
.app-select-avatar {
  flex: 0 0 auto;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  object-fit: cover;
  background: var(--surface-muted);
}
</style>
