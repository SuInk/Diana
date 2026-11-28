<!-- Copyright (c) 2025-now SuInk.
     Licensed under the Limited Redistribution License in the repository root. -->

<script setup lang="ts">
// 桌面端自己的设置：连的是哪台 Diana、本机 Diana 的数据放在哪、版本。机器人和
// 模型这些服务端设置在管理后台里改，这里不重复。
import { onMounted, ref } from 'vue'
import { FolderOpen } from '@lucide/vue'
import type { AppInfo, SessionState } from '../../../shared/types.ts'

const props = defineProps<{ session: SessionState }>()
const emit = defineEmits<{ 'use-remote': []; 'use-local': []; logout: [] }>()

const info = ref<AppInfo | null>(null)
onMounted(async () => {
  info.value = await window.diana.info()
})

const revealData = () => window.diana.revealData()
const host = () => props.session.server.replace(/^https?:\/\//, '')
</script>

<template>
  <div class="settings">
    <section class="settings-section">
      <h2>连接</h2>
      <div class="settings-card">
        <div class="settings-row">
          <div class="settings-row-text">
            <strong>{{ session.mode === 'local' ? '本机 Diana' : host() }}</strong>
            <span class="muted">{{ session.mode === 'local' ? '桌面端自带的 Diana，只在这台电脑上运行，退出 App 时一起停' : `用 ${session.username} 登录的其他服务器` }}</span>
          </div>
          <span class="status-pill"><i class="status-dot" aria-hidden="true" />{{ session.mode === 'local' ? '运行中' : '已连接' }}</span>
        </div>
        <div class="settings-row">
          <div class="settings-row-text">
            <strong>{{ session.mode === 'local' ? '连接其他服务器' : '改用本机 Diana' }}</strong>
            <span class="muted">{{ session.mode === 'local' ? '连到别处部署的 Diana；登上后本机 Diana 停掉，数据留着' : '回到桌面端自带的 Diana，不用登录' }}</span>
          </div>
          <button v-if="session.mode === 'local'" class="button" type="button" @click="emit('use-remote')">连接…</button>
          <button v-else class="button" type="button" @click="emit('use-local')">切换</button>
        </div>
        <div v-if="session.mode === 'remote'" class="settings-row">
          <div class="settings-row-text">
            <strong>退出登录</strong>
            <span class="muted">清掉保存在这台电脑上的登录状态</span>
          </div>
          <button class="button danger" type="button" @click="emit('logout')">退出登录</button>
        </div>
      </div>
    </section>

    <section v-if="session.mode === 'local' && info?.local" class="settings-section">
      <h2>本机 Diana</h2>
      <div class="settings-card">
        <div class="settings-row">
          <div class="settings-row-text">
            <strong>地址</strong>
            <span class="muted mono">127.0.0.1:{{ info.local.port }}</span>
          </div>
        </div>
        <div class="settings-row">
          <div class="settings-row-text">
            <strong>数据目录</strong>
            <span class="muted mono">{{ info.local.dataDir }}</span>
          </div>
          <button class="button" type="button" @click="revealData"><FolderOpen :size="15" aria-hidden="true" /> 打开</button>
        </div>
      </div>
    </section>

    <section class="settings-section">
      <h2>关于</h2>
      <div class="settings-card">
        <div class="settings-row">
          <div class="settings-row-text">
            <strong>Diana 桌面端</strong>
            <span class="muted">版本 {{ info?.version ?? '…' }}</span>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
