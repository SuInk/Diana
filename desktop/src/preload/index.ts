// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

import { contextBridge, ipcRenderer, type IpcRendererEvent } from 'electron'
import type { ChatFrame, DianaBridge } from '../shared/types.ts'

const bridge: DianaBridge = {
  state: () => ipcRenderer.invoke('diana:state'),
  login: (request) => ipcRenderer.invoke('diana:login', request),
  logout: () => ipcRenderer.invoke('diana:logout'),
  startLocal: () => ipcRenderer.invoke('diana:start-local'),
  openAdmin: () => ipcRenderer.invoke('diana:open-admin'),
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
