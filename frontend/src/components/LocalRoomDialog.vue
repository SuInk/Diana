<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<template>
  <Modal :title="room ? '群设置' : '新建群聊'" @close="emit('close')">
    <div v-if="!room" class="segmented room-dialog-tabs" role="tablist" aria-label="建群方式">
      <button type="button" role="tab" :aria-selected="mode === 'plan'" :class="{ active: mode === 'plan' }" @click="mode = 'plan'">一句话建群</button>
      <button type="button" role="tab" :aria-selected="mode === 'pick'" :class="{ active: mode === 'pick' }" @click="mode = 'pick'">选机器人</button>
    </div>

    <template v-if="!room && mode === 'plan'">
      <div class="field">
        <label for="room-plan">想建什么样的群</label>
        <textarea
          id="room-plan"
          v-model="planText"
          class="input"
          rows="4"
          maxlength="500"
          placeholder="例如：建四个机器人：虹夏、小孤独、喜多、凉，自动生成性格，组个乐队群"
        />
        <span class="hint">会新建这几台内置机器人，按名字自动写好人设，再拉进一个新群。你在群里一发言，他们就会接话。</span>
      </div>
      <div v-if="contacts.length > 1" class="field">
        <label>模型照哪台机器人</label>
        <AppSelect v-model="template" :options="templateOptions" />
        <span class="hint">新机器人沿用它的模型分配，建好后可以在「机器人」页单独改。</span>
      </div>
    </template>

    <template v-else>
      <div class="field">
        <label for="room-name">群名</label>
        <input id="room-name" v-model="name" class="input" maxlength="40" placeholder="例如：结束乐队" />
      </div>
      <div class="field">
        <label>成员</label>
        <div class="room-members" role="group" aria-label="群成员">
          <label v-for="contact in contacts" :key="contact.id" class="room-member">
            <input v-model="members" type="checkbox" :value="contact.id" />
            <BotAvatar :name="contact.name" :src="contact.avatar" :size="26" />
            <span>{{ contact.name }}</span>
            <small>{{ contact.platform }}</small>
          </label>
        </div>
        <span class="hint">平台机器人和内置机器人都能拉进来；群里的话只在 Diana 里，不会发到平台上。</span>
      </div>
      <div class="field">
        <label for="room-turns">机器人最多连续接几条</label>
        <input id="room-turns" v-model.number="maxBotTurns" class="input" type="number" min="1" max="50" step="1" inputmode="numeric" />
        <span class="hint">你没插话时，机器人之间接到这么多条就停下等你，免得聊个没完。</span>
      </div>
    </template>

    <p v-if="error" class="room-dialog-error" role="alert">{{ error }}</p>

    <template #footer>
      <button v-if="room" class="btn danger" type="button" :disabled="busy" @click="remove">解散群</button>
      <span class="room-dialog-spacer" />
      <button class="btn" type="button" :disabled="busy" @click="emit('close')">取消</button>
      <button class="btn primary" type="button" :disabled="busy || !canSubmit" @click="submit">
        <LoaderCircle v-if="busy" :size="14" class="room-dialog-spin" aria-hidden="true" />
        {{ room ? "保存" : mode === "plan" ? (busy ? "正在建群…" : "建群") : "建群" }}
      </button>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { LoaderCircle } from "@lucide/vue";
import Modal from "./Modal.vue";
import AppSelect from "./AppSelect.vue";
import BotAvatar from "./BotAvatar.vue";
import { askConfirm } from "../lib/confirm";
import { createLocalRoom, deleteLocalRoom, LOCAL_ROOM_DEFAULT_MAX_BOT_TURNS, planLocalRoom, updateLocalRoom, type LocalRoom } from "../lib/local-rooms";

interface Contact {
  id: string;
  name: string;
  platform: string;
  avatar?: string;
}

const props = defineProps<{ room?: LocalRoom; contacts: Contact[]; defaultTemplate?: string }>();
const emit = defineEmits<{ close: []; saved: [room: LocalRoom, created: boolean]; deleted: [id: string] }>();

const mode = ref<"plan" | "pick">("plan");
const planText = ref("");
const template = ref(props.defaultTemplate || props.contacts[0]?.id || "");
const templateOptions = computed(() => props.contacts.map((contact) => ({ value: contact.id, label: contact.name, hint: contact.platform })));
const name = ref(props.room?.name ?? "");
const members = ref<string[]>([...(props.room?.members ?? [])]);
const maxBotTurns = ref(props.room?.max_bot_turns || LOCAL_ROOM_DEFAULT_MAX_BOT_TURNS);
const busy = ref(false);
const error = ref("");

const canSubmit = computed(() => (!props.room && mode.value === "plan" ? planText.value.trim().length > 0 : name.value.trim().length > 0 && members.value.length > 0));

async function submit(): Promise<void> {
  busy.value = true;
  error.value = "";
  try {
    const turns = Math.min(50, Math.max(1, Math.round(Number(maxBotTurns.value) || LOCAL_ROOM_DEFAULT_MAX_BOT_TURNS)));
    if (props.room) {
      emit("saved", await updateLocalRoom({ id: props.room.id, name: name.value.trim(), members: members.value, max_bot_turns: turns }), false);
    } else if (mode.value === "plan") {
      emit("saved", await planLocalRoom(planText.value.trim(), template.value), true);
    } else {
      emit("saved", await createLocalRoom({ name: name.value.trim(), members: members.value, max_bot_turns: turns }), true);
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}

async function remove(): Promise<void> {
  if (!props.room) return;
  const ok = await askConfirm({ title: `解散「${props.room.name}」？`, message: "群和群消息会删掉；成员机器人还在，各自记得的聊天不受影响。", confirmLabel: "解散", danger: true });
  if (!ok) return;
  busy.value = true;
  try {
    await deleteLocalRoom(props.room.id);
    emit("deleted", props.room.id);
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err);
  } finally {
    busy.value = false;
  }
}
</script>

<style scoped>
.room-dialog-tabs {
  margin-bottom: 14px;
}

.field + .field {
  margin-top: 16px;
}

.room-members {
  display: grid;
  gap: 4px;
  max-height: 240px;
  overflow: auto;
  padding: 6px;
  border: 1px solid var(--border);
  border-radius: 10px;
}

.room-member {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px;
  border-radius: 8px;
  cursor: pointer;
}

.room-member:hover {
  background: var(--surface-2);
}

.room-member small {
  margin-left: auto;
  color: var(--muted);
}

.room-dialog-error {
  margin: 8px 0 0;
  color: var(--err);
  font-size: 13px;
}

.room-dialog-spacer {
  flex: 1;
}

.room-dialog-spin {
  animation: room-dialog-spin 0.9s linear infinite;
}

@keyframes room-dialog-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
