<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { SessionState } from '../../shared/types.ts'
import LoginView from './components/LoginView.vue'
import ChatShell from './components/ChatShell.vue'
import StartupView from './components/StartupView.vue'

const session = ref<SessionState | null>(null)
// 本机模式下点了「连接其他服务器」：先显示登录表单，登上了才真正切过去。
const choosingRemote = ref(false)
const startError = ref('')

onMounted(async () => {
  session.value = await window.diana.state()
  if (session.value.mode === 'local' && !session.value.loggedIn) await startLocal()
})

async function startLocal() {
  choosingRemote.value = false
  startError.value = ''
  if (session.value) session.value = { ...session.value, mode: 'local', loggedIn: false }
  const result = await window.diana.startLocal()
  if (result.ok) session.value = result.value
  else startError.value = result.error
}

function loggedIn(next: SessionState) {
  choosingRemote.value = false
  session.value = next
}

async function logout() {
  if (session.value?.mode === 'local') {
    choosingRemote.value = true
    return
  }
  await window.diana.logout()
  if (session.value) session.value = { ...session.value, loggedIn: false }
}

// 会话过期：本机的重新登一次，别处的回登录页，地址和用户名保留。
function expired() {
  if (session.value?.mode === 'local') void startLocal()
  else if (session.value) session.value = { ...session.value, loggedIn: false }
}
</script>

<template>
  <div class="titlebar-drag" aria-hidden="true" />
  <template v-if="session">
    <LoginView v-if="choosingRemote || (session.mode === 'remote' && !session.loggedIn)" :session="session" :cancellable="choosingRemote" @logged-in="loggedIn" @use-local="startLocal" @cancel="choosingRemote = false" />
    <ChatShell v-else-if="session.loggedIn" :session="session" @logout="logout" @expired="expired" />
    <StartupView v-else :error="startError" @retry="startLocal" @use-remote="choosingRemote = true" />
  </template>
</template>
