import type {TrustedLoginApp, TrustedLoginSettings} from '../api';

export function createTrustedLoginDemo() {
 const state:TrustedLoginSettings={enabled:false,apps:[],profiles:[{id:'demo-bot',name:'Diana',platform:'onebot-v11',available:true,reason:''},{id:'demo-disabled',name:'未开启快速登录的机器人',platform:'onebot-v11',available:false,reason:'管理员快速登录未开启'}],records:[]};
 let serial=0;
 const json=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json'}});
 return (path:string,method:string,body:Record<string,unknown>):Response|null=>{
  const root='/api/auth/apps/settings';if(path!==root && !path.startsWith(root+'/'))return null;
  if(path===root){if(method==='PUT')state.enabled=Boolean(body.enabled);return json(state);}
  const rotate=path.match(/\/apps\/([^/]+)\/rotate$/);
  if(rotate){if(!state.apps.some(a=>a.id===rotate[1]))return json({error:'应用不存在'},404);return json({id:rotate[1],secret:`demo-only-not-a-real-credential-${++serial}`});}
  const id=method==='POST'?String(body.id):decodeURIComponent(path.split('/').pop() || '');
  const index=state.apps.findIndex(a=>a.id===id);
  if(method==='POST' && index>=0)return json({error:'应用标识已存在'},409);
  if(method==='PATCH' && index<0)return json({error:'应用不存在'},404);
  const app:TrustedLoginApp={id,name:String(body.name),profile_id:String(body.profile_id),disabled:Boolean(body.disabled),last_login_at:index>=0?state.apps[index].last_login_at:null};
  if(index<0)state.apps.push(app);else state.apps[index]=app;
  return json({id,secret:method==='POST'?`demo-only-not-a-real-credential-${++serial}`:''});
 };
}
export const trustedLoginDemoResponse=createTrustedLoginDemo();
