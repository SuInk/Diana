// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { contextBridge, ipcRenderer, type IpcRendererEvent } from 'electron'
import type { AppCommand, ChatFrame, DianaBridge, Material, TaskStreamFrame } from '../shared/types.ts'

const bridge: DianaBridge = {
  // 界面按平台和窗口底材决定哪里透明、标题栏按钮留多少位置，渲染前就要知道。
  appearance: ipcRenderer.sendSync('diana:appearance') as { platform: string; material: Material },
  state: () => ipcRenderer.invoke('diana:state'),
  login: (request) => ipcRenderer.invoke('diana:login', request),
  logout: () => ipcRenderer.invoke('diana:logout'),
  startLocal: () => ipcRenderer.invoke('diana:start-local'),
  showAdmin: (bounds) => ipcRenderer.invoke('diana:admin-show', bounds),
  placeAdmin: (bounds) => ipcRenderer.send('diana:admin-place', bounds),
  hideAdmin: () => ipcRenderer.send('diana:admin-hide'),
  reloadAdmin: () => ipcRenderer.send('diana:admin-reload'),
  info: () => ipcRenderer.invoke('diana:info'),
  tasks: (profile) => ipcRenderer.invoke('diana:tasks', profile),
  task: (profile, id) => ipcRenderer.invoke('diana:task', profile, id),
  createTask: (profile) => ipcRenderer.invoke('diana:task-create', profile),
  deleteTask: (id) => ipcRenderer.invoke('diana:task-delete', id),
  stopTask: (id) => ipcRenderer.invoke('diana:task-stop', id),
  runTask: (requestId, id, message, model) => ipcRenderer.invoke('diana:task-run', requestId, id, message, model),
  onTaskFrame: (listener) => {
    const handler = (_event: IpcRendererEvent, frame: TaskStreamFrame) => listener(frame)
    ipcRenderer.on('diana:task-frame', handler)
    return () => ipcRenderer.removeListener('diana:task-frame', handler)
  },
  models: () => ipcRenderer.invoke('diana:models'),
  revealData: () => ipcRenderer.send('diana:reveal-data'),
  onCommand: (listener) => {
    const handler = (_event: IpcRendererEvent, command: AppCommand) => listener(command)
    ipcRenderer.on('diana:command', handler)
    return () => ipcRenderer.removeListener('diana:command', handler)
  },
  bots: () => ipcRenderer.invoke('diana:bots'),
  history: (profile) => ipcRenderer.invoke('diana:history', profile),
  send: (requestId, profile, text) => ipcRenderer.invoke('diana:send', requestId, profile, text),
  onFrame: (listener) => {
    const handler = (_event: IpcRendererEvent, frame: ChatFrame) => listener(frame)
    ipcRenderer.on('diana:frame', handler)
    return () => ipcRenderer.removeListener('diana:frame', handler)
  },
}

contextBridge.exposeInMainWorld('diana', bridge)
