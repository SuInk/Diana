import type { ManagedExtension } from './api';

const entries: Array<ManagedExtension & {content?:string;config?:Record<string,any>}> = [
  {kind:'skill',id:'skill:bot-protocol',name:'bot-protocol',description:'平台群操作与 Diana 回复设置',source:'builtin:bot-protocol',managed:false,enabled:true,content:'---\nname: bot-protocol\ndescription: 平台协议与回复配置\n---\n使用平台协议工具查询群信息，使用 diana.bot_config 修改回复设置。'},
  {kind:'skill',id:'skill:daily-summary',name:'daily-summary',description:'整理讨论中的待办与结论',source:'managed',managed:true,enabled:true,content:'---\nname: daily-summary\ndescription: 整理讨论中的待办与结论\n---\n根据提供的讨论内容整理待办。'},
  {kind:'mcp',id:'mcp:notes',name:'notes',description:'演示笔记服务',source:'https://example.com/mcp',transport:'streamable_http',managed:true,enabled:true,config:{url:'https://example.com/mcp',headers:{},env:{},enabled:true,startup_timeout_sec:20,tool_timeout_sec:60}},
];
const overrides:Record<string,Record<string,boolean>>={};
export function extensionDemoResponse(method:string,profile:string,body:Record<string,any>){
 const operation=method==='GET'?'list':body.operation;
 if(operation==='list')return {items:entries.map(({content,config,...item})=>({...item,enabled:item.enabled&&(overrides[profile]?.[item.id]??true)}))};
 const item=entries.find(i=>i.kind===body.kind&&i.name===body.name);
 if(operation==='read'){if(!item)throw Error('扩展不存在');return item.kind==='skill'?{content:item.content,managed:item.managed}:{config:item.config,configured_headers:[],configured_env:[]}}
 if(operation==='test')throw Error('演示模式不连接外部 MCP，请在真实部署中测试');
 if(operation==='enabled'){if(!item||!body.profile_id)throw Error('请选择机器人');(overrides[body.profile_id]??={})[item.id]=body.enabled;return {ok:true}}
 if(operation==='delete'){if(!item?.managed)throw Error('只读扩展不能删除');entries.splice(entries.indexOf(item),1);return {ok:true}}
 if(operation==='save'){
  if(body.source_url)throw Error('演示模式请直接导入 SKILL.md 正文');
  const name=body.name||body.content?.match(/^name:\s*(.+)$/m)?.[1];if(!name)throw Error('请填写名称');
  if(item&&!item.managed)throw Error('只读扩展不能编辑');if(item&&!body.replace)throw Error('名称已存在');
  const next={kind:body.kind,id:`${body.kind}:${name}`,name,description:body.kind==='skill'?body.content?.match(/^description:\s*(.+)$/m)?.[1]:'',source:body.config?.url||'managed',managed:true,enabled:true,content:body.content,config:body.config,transport:body.config?.command?'stdio':'streamable_http'};
  if(item)Object.assign(item,next);else entries.push(next);return {ok:true};
 }
 throw Error('不支持的操作');
}
