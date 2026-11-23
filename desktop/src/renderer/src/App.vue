<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { SessionState } from '../../shared/types.ts'
import LoginView from './components/LoginView.vue'
import ChatShell from './components/ChatShell.vue'

const session = ref<SessionState | null>(null)

onMounted(async () => {
  session.value = await window.diana.state()
})

function loggedIn(next: SessionState) {
  session.value = next
}

async function logout() {
  await window.diana.logout()
  if (session.value) session.value = { ...session.value, loggedIn: false }
}

// 会话过期时回登录页，地址和用户名保留。
function expired() {
  if (session.value) session.value = { ...session.value, loggedIn: false }
}
</script>

<template>
  <div class="titlebar-drag" aria-hidden="true" />
  <template v-if="session">
    <ChatShell v-if="session.loggedIn" :session="session" @logout="logout" @expired="expired" />
    <LoginView v-else :session="session" @logged-in="loggedIn" />
  </template>
</template>
