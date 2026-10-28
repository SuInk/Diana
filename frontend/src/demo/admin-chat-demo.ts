import type {AdminChatFrame, AdminChatMessage, AdminChatSession, AdminChatSummary} from '../lib/admin-chat';

const sessions = new Map<string, AdminChatSession>();
// 演示回复分几秒流出来，好在界面上看到排队、打断和停止。
const stopRequested = new Set<string>();
// 回复按 Markdown 显示，演示内容把常见写法都带上。
const demoReply = [
  '**演示模式**：这段对话只用于预览界面，不会执行真实安装，也不会访问你的服务器。',
  '',
  '### 实际部署能做什么',
  '1. **搜索群聊记录**：按群名、群号、关键词和日期筛选，例如「产品讨论（演示）」群最近 7 天。',
  '2. **排查问题**：查询配置、错误日志和消息处理事件。',
  '   - 不回复时先看 `decision_reason`',
  '   - 连接错误看 OneBot 状态',
  '3. **管理扩展**：通过确认码完成 Skill/MCP 变更。',
  '',
  '| 群 | 消息数 |',
  '|---|--:|',
  '| 产品讨论（演示） | 186 |',
  '| 日常交流（演示） | 74 |',
  '',
  '```mermaid',
  'flowchart LR',
  '  A[群消息] --> B{要回复吗}',
  '  B -- 是 --> C[生成回复] --> D[发送前审核] --> E[发出]',
  '  B -- 否 --> F[只记录]',
  '```',
  '',
  '```bash',
  'docker logs diana --since 1h | grep ERROR',
  '```',
  '',
  '> 群聊内容只供分析，不会当作操作指令。详见 https://github.com/SuInk/diana。',
].join('\n');
function newSession(profile: string): AdminChatSession {
  const now = new Date().toISOString();
  const session: AdminChatSession = {session_id:crypto.randomUUID(),profile_id:profile,title:'新的管理会话',created_at:now,updated_at:now,messages:[],running:false,command_enabled:true,file_write_enabled:false,sandbox:'auto',network_enabled:false};
  sessions.set(session.session_id,session);
  return session;
}
function summary(session: AdminChatSession): AdminChatSummary {
  const messages=session.messages || [];
  return {session_id:session.session_id,profile_id:session.profile_id,title:session.title || '新的管理会话',preview:messages[messages.length-1]?.content || '还没有消息',created_at:session.created_at!,updated_at:session.updated_at!,running:session.running,message_count:messages.length};
}
export function adminChatDemoResponse(url: URL, method: string, body: Record<string, unknown>): Response | undefined {
  if (!url.pathname.startsWith('/api/assistant/admin-chat')) return;
  const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), {status, headers:{'Content-Type':'application/json'}});
  const profile = url.searchParams.get('profile') || '';
  if (url.pathname.endsWith('/sessions')) {
    if (method === 'GET') return json({sessions:[...sessions.values()].filter(s=>s.profile_id===profile).map(summary).sort((a,b)=>b.updated_at.localeCompare(a.updated_at))});
    if (method === 'POST') {const session=newSession(String(body.profile || ''));return json({session_id:session.session_id,profile_id:session.profile_id},201);}
    return json({error:'不支持的操作'},405);
  }
  if (method === 'GET') {
    const requested=url.searchParams.get('session_id');
    let session = requested ? sessions.get(requested) : [...sessions.values()].filter(s=>s.profile_id===profile).sort((a,b)=>(b.updated_at || '').localeCompare(a.updated_at || ''))[0];
    if (requested && (!session || session.profile_id!==profile)) return json({error:'对话不存在'},404);
    if (!session) session=newSession(profile);
    return json(session);
  }
  const id = method === 'POST' && url.pathname.endsWith('/admin-chat') ? String(body.session_id || '') : url.pathname.split('/')[4];
  const session = [...sessions.values()].find(session => session.session_id === id);
  if (!session) return json({error:'对话不存在'},404);
  if (method === 'DELETE') {session.messages=[];session.title='新的管理会话';session.updated_at=new Date().toISOString();return json({ok:true});}
  if (url.pathname.endsWith('/stop')) {if (session.running) stopRequested.add(session.session_id);return json({ok:true});}
  const user: AdminChatMessage = {id:crypto.randomUUID(),role:'user',content:String(body.message || ''),created_at:new Date().toISOString()};
  const reply: AdminChatMessage = {id:crypto.randomUUID(),role:'assistant',created_at:new Date().toISOString(),content:demoReply,steps:[{phase:'tool_completed',tool:'admin_diagnostics',duration_ms:8}]};
  if (!session.messages?.length) session.title=user.content.slice(0,36);
  session.messages = [...(session.messages || []), user].slice(-40);
  session.running = true;
  const encoder = new TextEncoder();
  const line = (frame: AdminChatFrame) => encoder.encode(JSON.stringify(frame)+'\n');
  const stream = new ReadableStream<Uint8Array>({async start(controller) {
    controller.enqueue(line({type:'user',message:user}));
    controller.enqueue(line({type:'progress',progress:{phase:'tool_started',tool:'admin_diagnostics'}}));
    for (let i = 0; i < 30 && !stopRequested.has(session.session_id); i++) await new Promise(resolve => setTimeout(resolve, 100));
    const stopped = stopRequested.delete(session.session_id);
    const final = stopped ? {...reply, content:'已停止。', error:true, steps:[]} : reply;
    final.created_at = new Date().toISOString();
    session.updated_at = final.created_at;
    session.messages = [...(session.messages || []), final].slice(-40);
    session.running = false;
    controller.enqueue(line({type:'message',message:final}));
    controller.enqueue(line({type:'done'}));
    controller.close();
  }});
  return new Response(stream,{headers:{'Content-Type':'application/x-ndjson'}});
}
