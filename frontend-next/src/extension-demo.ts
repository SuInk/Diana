import type { ManagedExtension } from './api';

const entries: Array<ManagedExtension & {content?:string;config?:Record<string,any>}> = [
  {kind:'skill',id:'skill:bot-protocol',name:'bot-protocol',description:'平台群操作与 Diana 回复设置',source:'builtin:bot-protocol',managed:false,enabled:true,content:'---\nname: bot-protocol\ndescription: 平台协议与回复配置\n---\n使用平台协议工具查询群信息，使用 bot_config 修改回复设置。'},
  {kind:'skill',id:'skill:daily-summary',name:'daily-summary',description:'整理讨论中的待办与结论',source:'managed',managed:true,enabled:true,content:'---\nname: daily-summary\ndescription: 整理讨论中的待办与结论\n---\n根据提供的讨论内容整理待办。'},
  {kind:'skill',id:'skill:release-notes',name:'release-notes',description:'按仓库发布记录整理更新说明',source:'managed',managed:true,enabled:true,bundled:true,content:'---\nname: release-notes\ndescription: 按仓库发布记录整理更新说明\n---\n用 browser_render 读取 releases 页面，再按模板整理。'},
  {kind:'mcp',id:'mcp:notes',name:'notes',description:'演示笔记服务',source:'https://example.com/mcp',transport:'streamable_http',managed:true,enabled:true,config:{url:'https://example.com/mcp',headers:{},env:{},enabled:true,startup_timeout_sec:20,tool_timeout_sec:60}},
];
const overrides:Record<string,Record<string,boolean>>={};
const memberAccess:Record<string,Record<string,boolean>>={};
const memberAudience:Record<string,Record<string,{min_role?:string;users:string[];groups:string[]}>>={};
// demoMCPPresets 复刻 model/agent/mcp_presets.go 的内置清单，只留界面要用的字段。
const demoMCPPresets=[{
 id:'gitea',name:'gitea',title:'Gitea',
 summary:'接入自建或公有 Gitea 的仓库、Issue 与 Pull Request。需要先自己跑一份 gitea-mcp（官方提供二进制和 Docker 镜像），Diana 不打包它的二进制。',
 docs_url:'https://gitea.com/gitea/gitea-mcp',
 transports:[
  {id:'http',label:'连接已经跑起来的 gitea-mcp',hint:'推荐：用官方 Docker 镜像跑一份 gitea-mcp（-t http），这里填它的地址。令牌配在那一侧，Diana 不经手。',fields:[
   {key:'url',label:'gitea-mcp 服务地址',placeholder:'http://127.0.0.1:8080/mcp',required:true},
   {key:'authorization',label:'Authorization 请求头',placeholder:'留空表示不带',hint:'只有给 gitea-mcp 另加了鉴权时才需要填。',secret:true}
  ]},
  {id:'stdio',label:'由 Diana 启动本机的 gitea-mcp',hint:'宿主机部署、且本机已经装了 gitea-mcp 时可用。容器部署里镜像没有这个命令，装上会连不上。',fields:[
   {key:'host',label:'Gitea 实例地址',placeholder:'https://git.example.com',required:true},
   {key:'token',label:'访问令牌',hint:'Gitea 里生成的个人访问令牌，按 MCP 环境变量存放，不回显。',required:true,secret:true},
   {key:'command',label:'可执行文件',placeholder:'gitea-mcp',hint:'留空按 gitea-mcp 处理；不在 PATH 里就填绝对路径。'}
  ]}
 ]
}];

export function extensionDemoResponse(method:string,profile:string,body:Record<string,any>){
 const operation=method==='GET'?'list':body.operation;
 if(operation==='list')return {items:entries.map(({content,config,...item})=>({...item,enabled:item.enabled&&(overrides[profile]?.[item.id]??true),...(profile?{members_enabled:memberAccess[profile]?.[item.id]??false,...(memberAudience[profile]?.[item.id]?{member_audience:memberAudience[profile][item.id]}:{})}:{})}))};
 // 预设清单和真实部署保持一致：演示里也能走一遍「选预设 → 填字段 → 添加」。
 if(operation==='presets')return {items:demoMCPPresets.map(preset=>({preset,installed:entries.some(i=>i.kind==='mcp'&&i.name===preset.name)}))};
 if(operation==='preset_save'){
  const preset=demoMCPPresets.find(p=>p.id===body.preset);if(!preset)throw Error('预设不存在');
  const transport=preset.transports.find(t=>t.id===body.transport);if(!transport)throw Error('预设没有这种接法');
  for(const field of transport.fields)if(field.required&&!String(body.values?.[field.key]??'').trim())throw Error(`请填写「${field.label}」`);
  const name=body.name||preset.name;if(entries.some(i=>i.kind==='mcp'&&i.name===name))throw Error('名称已存在');
  const url=String(body.values?.url??'');
  entries.push({kind:'mcp',id:`mcp:${name}`,name,description:preset.summary,source:url||'managed',managed:true,enabled:true,config:{},transport:body.transport==='stdio'?'stdio':'streamable_http'});
  return {ok:true};
 }
 const item=entries.find(i=>i.kind===body.kind&&i.name===body.name);
 if(operation==='read'){if(!item)throw Error('扩展不存在');return item.kind==='skill'?{content:item.content,managed:item.managed}:{config:item.config,configured_headers:[],configured_env:[]}}
 if(operation==='test')throw Error('演示模式不连接外部 MCP，请在真实部署中测试');
 if(operation==='enabled'){if(!item||!body.profile_id)throw Error('请选择机器人');(overrides[body.profile_id]??={})[item.id]=body.enabled;return {ok:true}}
 if(operation==='members'){if(!item)throw Error('扩展不存在');if(!body.profile_id)throw Error('请选择机器人');(memberAccess[body.profile_id]??={})[item.id]=body.enabled;return {ok:true}}
 if(operation==='audience'){if(!item)throw Error('扩展不存在');if(!body.profile_id)throw Error('请选择机器人');const users=(body.audience?.users||[]).filter(Boolean),groups=(body.audience?.groups||[]).filter(Boolean);const minRole=body.audience?.min_role||'';if(minRole&&minRole!=='admin')throw Error('身份门槛只支持 admin');const store=(memberAudience[body.profile_id]??={});if(users.length||groups.length||minRole)store[item.id]={...(minRole?{min_role:minRole}:{}),users,groups};else delete store[item.id];return {ok:true}}
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
