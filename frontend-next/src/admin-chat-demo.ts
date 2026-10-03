import type {AdminChatFrame, AdminChatMessage, AdminChatSession, AdminChatSummary} from './admin-chat';

const sessions = new Map<string, AdminChatSession>();
function newSession(profile: string): AdminChatSession {
  const now = new Date().toISOString();
  const session: AdminChatSession = {session_id:crypto.randomUUID(),profile_id:profile,title:'新的管理会话',created_at:now,updated_at:now,messages:[],running:false,command_allowlist:['date','uname','df'],file_write_enabled:false,sandbox:'auto',network_enabled:false};
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
  if (url.pathname.endsWith('/stop')) return json({ok:true});
  const user: AdminChatMessage = {id:crypto.randomUUID(),role:'user',content:String(body.message || ''),created_at:new Date().toISOString()};
  const reply: AdminChatMessage = {id:crypto.randomUUID(),role:'assistant',created_at:new Date().toISOString(),content:'演示模式：这段对话用于预览界面，不会执行真实安装或访问你的服务器。\n\n实际部署可以搜索所选机器人已保存的群聊记录，按群号、关键词和日期筛选；也会查询配置与错误日志，并通过确认码完成 Skill/MCP 变更。',steps:[{phase:'tool_completed',tool:'admin_diagnostics',duration_ms:8}]};
  if (!session.messages?.length) session.title=user.content.slice(0,36);
  session.updated_at=reply.created_at;
  session.messages = [...(session.messages || []), user, reply].slice(-40);
  const frames: AdminChatFrame[] = [{type:'user',message:user},{type:'progress',progress:{phase:'tool_started',tool:'admin_diagnostics'}},{type:'message',message:reply},{type:'done'}];
  return new Response(frames.map(frame=>JSON.stringify(frame)).join('\n')+'\n',{headers:{'Content-Type':'application/x-ndjson'}});
}
