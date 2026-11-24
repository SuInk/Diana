<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
import { ref } from 'vue'
import { ArrowLeft, LoaderCircle, ShieldAlert } from '@lucide/vue'
import type { SessionState, UntrustedCertificate } from '../../../shared/types.ts'

const props = defineProps<{ session: SessionState; cancellable?: boolean }>()
const emit = defineEmits<{ 'logged-in': [SessionState]; 'use-local': []; cancel: [] }>()

const server = ref(props.session.server)
const username = ref(props.session.username)
const password = ref('')
const busy = ref(false)
const error = ref('')
// 服务器证书系统不认（自签、内网 CA）时让用户核对指纹，信任后只认这一张。
const certificate = ref<UntrustedCertificate | null>(null)

async function submit(trustFingerprint?: string) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  const result = await window.diana.login({ server: server.value, username: username.value, password: password.value, trustFingerprint })
  busy.value = false
  if (result.ok) {
    password.value = ''
    certificate.value = null
    emit('logged-in', result.value)
    return
  }
  if (result.certificate) {
    certificate.value = result.certificate
    return
  }
  error.value = result.error
}
</script>

<template>
  <main class="login">
    <form class="login-form" @submit.prevent="submit()">
      <div class="login-brand">
        <span class="login-logo" aria-hidden="true">D</span>
        <div>
          <h1>Diana</h1>
          <p class="muted">连接其他服务器上的 Diana，用它的 WebUI 管理员账号登录</p>
        </div>
      </div>
      <label class="field">
        <span>服务器地址</span>
        <input id="login-server" v-model="server" class="input" placeholder="http://192.168.1.10:18080" autocomplete="url" required autofocus />
      </label>
      <label class="field">
        <span>用户名</span>
        <input id="login-username" v-model="username" class="input" autocomplete="username" required />
      </label>
      <label class="field">
        <span>密码</span>
        <input id="login-password" v-model="password" class="input" type="password" autocomplete="current-password" required />
      </label>
      <p v-if="error" class="form-error" role="alert">{{ error }}</p>
      <button class="button primary" type="submit" :disabled="busy">
        <LoaderCircle v-if="busy" :size="16" class="spin" aria-hidden="true" />
        {{ busy ? '正在登录…' : '登录' }}
      </button>
      <p class="login-note muted">密码不保存；登录状态加密存在本机钥匙串里。</p>
      <button v-if="cancellable" class="button link" type="button" @click="emit('cancel')"><ArrowLeft :size="14" aria-hidden="true" /> 返回</button>
      <button v-else id="use-local" class="button link" type="button" @click="emit('use-local')">改用本机 Diana</button>
    </form>

    <div v-if="certificate" class="dialog-backdrop" role="dialog" aria-modal="true" aria-labelledby="cert-title">
      <div class="dialog">
        <h2 id="cert-title"><ShieldAlert :size="18" aria-hidden="true" /> 证书不受系统信任</h2>
        <p>{{ certificate.host }} 用的证书不是系统认可的机构签发的，常见于自签证书或内网 CA。核对下面的指纹和服务器上的一致再信任，之后只认这一张，换证书会再问。</p>
        <code class="fingerprint">{{ certificate.fingerprint }}</code>
        <div class="dialog-actions">
          <button class="button" type="button" @click="certificate = null">取消</button>
          <button class="button primary" type="button" :disabled="busy" @click="submit(certificate.fingerprint)">信任并登录</button>
        </div>
      </div>
    </div>
  </main>
</template>
