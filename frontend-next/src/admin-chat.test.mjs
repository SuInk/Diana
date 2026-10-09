import assert from 'node:assert/strict';
import {test} from 'node:test';
import {filterAdminChatSessions,readAdminChatFrames} from './admin-chat.ts';
import {adminChatDemoResponse} from './admin-chat-demo.ts';

test('admin stream handles split JSON and multibyte characters', async () => {
  const encoded = new TextEncoder().encode(JSON.stringify({type:'message',message:{id:'1',role:'assistant',content:'安装完成'}})+'\n'+JSON.stringify({type:'heartbeat'})+'\n'+JSON.stringify({type:'done'}));
  const stream = new ReadableStream({start(controller){ for(let i=0;i<encoded.length;i++)controller.enqueue(encoded.slice(i,i+1));controller.close(); }});
  const frames=[];await readAdminChatFrames(stream,frame=>frames.push(frame));
  assert.equal(frames[0].message.content,'安装完成');assert.equal(frames.at(-1).type,'done');
});
test('admin stream rejects an interrupted response and cancels malformed streams', async () => {
  const incomplete = new ReadableStream({start(controller){controller.enqueue(new TextEncoder().encode('{"type":"progress","progress":{"phase":"started"}}\n'));controller.close();}});
  await assert.rejects(readAdminChatFrames(incomplete,()=>{}),/连接中断/);
  let cancelled=false;
  const malformed=new ReadableStream({start(controller){controller.enqueue(new TextEncoder().encode('invalid\n'));},cancel(){cancelled=true;}});
  await assert.rejects(readAdminChatFrames(malformed,()=>{}));assert.equal(cancelled,true);
});

test('conversation search matches titles and previews without changing server order', () => {
  const rows=[{session_id:'1',title:'MCP 安装失败',preview:'检查认证设置'},{session_id:'2',title:'群聊诊断',preview:'MCP 连接正常'},{session_id:'3',title:'日志检查',preview:'暂无错误'}];
  assert.deepEqual(filterAdminChatSessions(rows,' mcp ').map(s=>s.session_id),['1','2']);
  assert.deepEqual(filterAdminChatSessions(rows,'认证').map(s=>s.session_id),['1']);
  assert.equal(filterAdminChatSessions(rows,'不存在').length,0);
  assert.deepEqual(filterAdminChatSessions(rows,''),rows);
});

test('demo new conversations preserve independent histories and enforce profile binding', async () => {
  const call=(path,method='GET',body={})=>adminChatDemoResponse(new URL(`https://demo.invalid/api/assistant/admin-chat${path}`),method,body);
  const profile=`test-${crypto.randomUUID()}`;
  const first=await call('/sessions','POST',{profile}).json(),second=await call('/sessions','POST',{profile}).json();
  assert.notEqual(first.session_id,second.session_id);
  await call('','POST',{session_id:first.session_id,message:'测试 MCP 连接'}).text();
  const original=await call(`?profile=${profile}&session_id=${first.session_id}`).json();
  const blank=await call(`?profile=${profile}&session_id=${second.session_id}`).json();
  assert.equal(original.messages.length,2);assert.equal(blank.messages.length,0);
  assert.equal(call(`?profile=wrong&session_id=${first.session_id}`).status,404);
  const list=await call(`/sessions?profile=${profile}`).json();
  assert.equal(list.sessions.length,2);assert.equal(list.sessions.find(s=>s.session_id===first.session_id).title,'测试 MCP 连接');
});

// Execute the view's queue against Vue's real scheduler and a failing transport.
// A source-only assertion would miss the idle watcher immediately retrying.
import {readFileSync} from 'node:fs';
import ts from 'typescript';
import {ref, computed, watch, nextTick, effectScope} from 'vue';

test('failed queued sends pause until explicit resume and retain their original session', async () => {
  const source = readFileSync(new URL('./views/AdminChatView.vue', import.meta.url), 'utf8');
  const queueSource = source.slice(source.indexOf('interface QueuedMessage'), source.indexOf('async function searchHistory'));
  const code = ts.transpileModule(queueSource, {compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
  let attempts = 0, rejectSend;
  const session = ref({session_id:'first',profile_id:''});
  const sending=ref(false), loading=ref(false), remoteRunning=ref(false);
  const transport = () => { attempts++; return new Promise((_,reject)=>{rejectSend=reject;}); };
  const scope=effectScope();
  const setup = new Function('ref','computed','watch','session','sending','loading','remoteRunning','sendAdminChat', `
    const busy=computed(()=>sending.value||remoteRunning.value);
    const selectedProfile=ref(''), draft=ref(''), error=ref(''), progress=ref(null), followTranscript=ref(false), selectedModel=ref(''), stopping=ref(false), messages=ref([]);
    let active=true,controller;
    const splitModel=()=>[''],scroll=async()=>{},load=async()=>{},stop=async()=>{};
    ${code}
    return {queue,queuePaused,drain,resumeQueue};
  `);
  const q=scope.run(()=>setup(ref,computed,watch,session,sending,loading,remoteRunning,transport));
  try {
    q.queue.value.push({id:'a',sessionID:'first',text:'run once'});
    q.drain(); await nextTick();
    session.value={session_id:'second',profile_id:''};
    rejectSend(new Error('offline')); await new Promise(resolve=>setImmediate(resolve));
    assert.equal(attempts,1);
    assert.equal(q.queuePaused.value,true);
    assert.equal(q.queue.value[0].sessionID,'first');
    session.value={session_id:'first',profile_id:''};
    q.drain(); await nextTick(); assert.equal(attempts,1);
    q.resumeQueue(); await nextTick(); assert.equal(attempts,2);
    rejectSend(new Error('still offline')); await new Promise(resolve=>setImmediate(resolve));
    assert.equal(attempts,2);
  } finally { scope.stop(); }
});
