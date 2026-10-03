export interface AdminChatProgress {
  phase: string;
  tool?: string;
  duration_ms?: number;
  failed?: boolean;
}
export interface AdminChatMessage {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  error?: boolean;
  steps?: AdminChatProgress[];
  created_at?: string;
}
export interface AdminChatSummary {
  session_id: string;
  profile_id: string;
  title: string;
  preview: string;
  updated_at: string;
  created_at: string;
  running: boolean;
  message_count: number;
}
export interface AdminChatSession {
  session_id: string;
  profile_id: string;
  messages: AdminChatMessage[] | null;
  running: boolean;
  agent_mode?: 'standard' | 'safe';
  command_allowlist: string[] | null;
  file_write_enabled: boolean;
  sandbox: string;
  network_enabled: boolean;
  title?: string;
  updated_at?: string;
  created_at?: string;
}
export type AdminChatFrame =
  | {type: 'user' | 'message'; message: AdminChatMessage}
  | {type: 'progress'; progress: AdminChatProgress}
  | {type: 'done' | 'heartbeat'};

async function chatRequest(path: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(`/api/assistant/admin-chat${path}`, {
    ...init, headers: {'Content-Type': 'application/json', ...init?.headers},
    credentials: 'same-origin', cache: 'no-store'
  });
  if (!response.ok) {
    if (response.status === 401) window.dispatchEvent(new CustomEvent('diana:unauthorized'));
    const data = await response.json().catch(() => ({}));
    throw new Error(data.error || `请求失败（${response.status}）`);
  }
  return response;
}
export async function getAdminChat(profile: string, signal?: AbortSignal, id = ''): Promise<AdminChatSession> {
  const params = new URLSearchParams({profile});
  if (id) params.set('session_id', id);
  return (await chatRequest(`?${params}`, {signal})).json();
}
export async function listAdminChatSessions(profile: string, signal?: AbortSignal): Promise<AdminChatSummary[]> {
  return (await (await chatRequest(`/sessions?profile=${encodeURIComponent(profile)}`, {signal})).json()).sessions;
}
export async function createAdminChatSession(profile: string): Promise<{session_id:string;profile_id:string}> {
  return (await chatRequest('/sessions', {method:'POST',body:JSON.stringify({profile})})).json();
}
export function filterAdminChatSessions(sessions: AdminChatSummary[], search: string): AdminChatSummary[] {
  const term = search.trim().toLocaleLowerCase();
  return sessions.filter(session => !term || `${session.title}\n${session.preview}`.toLocaleLowerCase().includes(term));
}
export async function stopAdminChat(id: string): Promise<void> {
  await chatRequest(`/${encodeURIComponent(id)}/stop`, {method: 'POST'});
}
export async function clearAdminChat(id: string): Promise<void> {
  await chatRequest(`/${encodeURIComponent(id)}`, {method: 'DELETE'});
}

// HTTP chunks can split a JSON line or a multibyte character. A completed HTTP
// response without the terminal frame is an interrupted run, not success.
export async function readAdminChatFrames(body: ReadableStream<Uint8Array>, receive: (frame: AdminChatFrame) => void): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let pending = '';
  let done = false;
  const consume = (line: string) => {
    if (!line.trim()) return;
    const frame = JSON.parse(line) as AdminChatFrame;
    if (!['user', 'message', 'progress', 'done', 'heartbeat'].includes(frame.type)) throw new Error('对话响应格式错误');
    receive(frame);
    if (frame.type === 'done') done = true;
  };
  try {
    while (true) {
      const chunk = await reader.read();
      pending += decoder.decode(chunk.value, {stream: !chunk.done});
      let newline: number;
      while ((newline = pending.indexOf('\n')) >= 0) {
        consume(pending.slice(0, newline));
        pending = pending.slice(newline + 1);
      }
      if (pending.length > 256 * 1024) throw new Error('对话响应过大');
      if (chunk.done) break;
    }
    consume(pending);
    if (!done) throw new Error('对话连接中断，请刷新查看已经完成的操作');
  } finally {
    if (!done) await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export async function sendAdminChat(id: string, message: string, signal: AbortSignal, receive: (frame: AdminChatFrame) => void): Promise<void> {
  const response = await chatRequest('', {method: 'POST', body: JSON.stringify({session_id: id, message}), signal});
  if (!response.body) throw new Error('浏览器不支持流式对话');
  await readAdminChatFrames(response.body, receive);
}
