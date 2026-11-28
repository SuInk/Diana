// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 主进程和界面共用的数据结构。字段名照服务端 WebUI 聊天接口（webui/web_chat.go），
// 和手机客户端 mobile/lib/api/models.dart 是同一套。

export interface Bot {
  id: string
  name: string
  platform: string
  avatar?: string
}

export interface ChatQuote {
  message_id?: string
  sender_name?: string
  text?: string
}

export interface ChatMessage {
  id: string
  /** user 或 assistant */
  role: string
  /** call 是一条通话记录，居中显示成提示 */
  kind?: string
  text: string
  quote?: ChatQuote
  images?: string[]
  /** Unix 秒 */
  at: number
}

export interface ChatInfo {
  bot_id: string
  bot_name: string
  platform: string
  /** 非空时聊不了，内容是原因 */
  problem?: string
  owner_avatar_url?: string
}

export interface ChatHistory {
  info: ChatInfo
  messages: ChatMessage[]
}

/** local：桌面端自己带的本机 Diana；remote：连别处部署的 Diana。 */
export type ServerMode = 'local' | 'remote'

export interface SessionState {
  mode: ServerMode
  server: string
  username: string
  loggedIn: boolean
}

// ---------- 任务：服务端管理对话接口（webui/admin_chat.go），字段名照它 ----------

/** 一步执行记录。summary 是服务端挑出的路径、命令、网址这类参数，已脱敏。 */
export interface TaskStep {
  phase: string
  tool?: string
  summary?: string
  duration_ms?: number
  failed?: boolean
  error?: string
}

export interface TaskMessage {
  id: string
  /** user 或 assistant */
  role: string
  content: string
  error?: boolean
  steps?: TaskStep[]
  created_at?: string
}

export interface TaskSummary {
  session_id: string
  profile_id: string
  title: string
  preview: string
  updated_at: string
  created_at: string
  running: boolean
  message_count: number
}

export interface TaskSession {
  session_id: string
  profile_id: string
  title?: string
  messages: TaskMessage[]
  running: boolean
  agent_mode?: 'standard' | 'safe'
  command_enabled: boolean
  file_write_enabled: boolean
  network_enabled: boolean
}

export type TaskFrame =
  | { type: 'user' | 'message'; message: TaskMessage }
  | { type: 'progress'; progress: TaskStep }

/** 发任务时推给界面的一帧；done 是这次发送结束。 */
export type TaskStreamFrame = { requestId: string } & (TaskFrame | { type: 'done'; error?: string })

export interface ModelOption {
  provider: string
  model: string
  label: string
  hint: string
}

/** 窗口底材：macOS 磨砂、Windows 11 Mica，或纯色。 */
export type Material = 'vibrancy' | 'mica' | 'none'

/** 应用菜单发给界面的命令。 */
export type AppCommand = 'new-task' | 'chat' | 'admin' | 'settings' | 'sidebar' | 'reload'

/** 界面里管理后台占位区域的位置，窗口内容坐标。 */
export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

export interface AppInfo {
  version: string
  /** 本机 Diana 的端口和数据目录；没在跑时为空 */
  local?: { port: number; dataDir: string }
}

/** 服务器证书不受系统信任时，主进程把指纹交给界面问用户。 */
export interface UntrustedCertificate {
  host: string
  fingerprint: string
}

export type ApiResult<T> =
  | { ok: true; value: T }
  | { ok: false; error: string; unauthorized?: boolean; certificate?: UntrustedCertificate }

export interface LoginRequest {
  server: string
  username: string
  password: string
  /** 用户核对过、愿意信任的证书指纹 */
  trustFingerprint?: string
}

/** 发消息时流式推给界面的一帧。 */
export type ChatFrame =
  | { requestId: string; type: 'message'; message: ChatMessage }
  | { requestId: string; type: 'done'; error?: string }

export interface DianaBridge {
  /** mac / win / linux，和窗口底材 */
  appearance: { platform: string; material: Material }
  state(): Promise<SessionState>
  login(request: LoginRequest): Promise<ApiResult<SessionState>>
  logout(): Promise<void>
  /** 拉起本机 Diana 并自动登录，切回本机模式也走这里。 */
  startLocal(): Promise<ApiResult<SessionState>>
  /** 在主面板的占位区域里显示当前服务器的管理后台（WebUI），沿用这里的登录。 */
  showAdmin(bounds: Rect): Promise<ApiResult<null>>
  placeAdmin(bounds: Rect): void
  hideAdmin(): void
  reloadAdmin(): void
  info(): Promise<AppInfo>
  /** 在访达 / 资源管理器里打开本机 Diana 的数据目录。 */
  revealData(): void
  onCommand(listener: (command: AppCommand) => void): () => void
  tasks(profile: string): Promise<ApiResult<TaskSummary[]>>
  task(profile: string, id?: string): Promise<ApiResult<TaskSession>>
  createTask(profile: string): Promise<ApiResult<string>>
  deleteTask(id: string): Promise<ApiResult<null>>
  stopTask(id: string): Promise<ApiResult<null>>
  /** 执行步骤和回复从 onTaskFrame 推出来，最后一帧是 done。 */
  runTask(requestId: string, id: string, message: string, model?: ModelOption): Promise<ApiResult<null>>
  onTaskFrame(listener: (frame: TaskStreamFrame) => void): () => void
  models(): Promise<ApiResult<ModelOption[]>>
  bots(): Promise<ApiResult<Bot[]>>
  history(profile: string): Promise<ApiResult<ChatHistory>>
  /** 回复按条从 onFrame 推出来，最后一帧是 done。 */
  send(requestId: string, profile: string, text: string): Promise<ApiResult<null>>
  onFrame(listener: (frame: ChatFrame) => void): () => void
}
