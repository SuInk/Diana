import type { ManagedExtension } from './api';

const entries: Array<ManagedExtension & {content?:string;config?:Record<string,any>;preset?:string;preset_transport?:string;preset_values?:Record<string,string>}> = [
  {kind:'skill',id:'skill:bot-protocol',name:'bot-protocol',description:'平台群操作与 Diana 回复设置',source:'builtin:bot-protocol',managed:false,enabled:true,content:'---\nname: bot-protocol\ndescription: 平台协议与回复配置\n---\n使用平台协议工具查询群信息，使用 bot_config 修改回复设置。'},
  {kind:'skill',id:'skill:daily-summary',name:'daily-summary',description:'整理讨论中的待办与结论',source:'managed',managed:true,enabled:true,keywords:['总结','待办'],content:'---\nname: daily-summary\ndescription: 整理讨论中的待办与结论\n---\n根据提供的讨论内容整理待办。'},
  {kind:'skill',id:'skill:release-notes',name:'release-notes',description:'按仓库发布记录整理更新说明',source:'managed',managed:true,enabled:true,bundled:true,content:'---\nname: release-notes\ndescription: 按仓库发布记录整理更新说明\n---\n用 browser_render 读取 releases 页面，再按模板整理。'},
  {kind:'mcp',id:'mcp:notes',name:'notes',description:'演示笔记服务',source:'https://example.com/mcp',transport:'streamable_http',managed:true,enabled:true,config:{url:'https://example.com/mcp',headers:{},env:{},enabled:true,startup_timeout_sec:20,tool_timeout_sec:60}},
];
const overrides:Record<string,Record<string,boolean>>={};
// Skill 正文档位：键不在就是跟随默认。
const residency:Record<string,Record<string,boolean>>={};
const memberAccess:Record<string,Record<string,boolean>>={};
const memberAudience:Record<string,Record<string,{min_role?:string;users:string[];groups:string[]}>>={};
const hiddenPresets=new Set<string>();
// demoMCPPresets 复刻 model/agent/mcp_presets.go 的内置清单，只留界面要用的字段。
const demoMCPPresets=[{
 id:'gitea',name:'gitea',title:'Gitea',
 summary:'接入自建或公有 Gitea 的仓库、Issue 与 Pull Request。官方 gitea-mcp 随 Diana 一起打包，填实例地址和访问令牌就能用。',
 docs_url:'https://gitea.com/gitea/gitea-mcp',
 transports:[
  {id:'stdio',label:'用自带的 gitea-mcp',hint:'推荐：Diana 直接拉起随包发布的 gitea-mcp，令牌只存在这条 MCP 的环境变量里，不经过第三方。',verifiable:true,fields:[
   {key:'host',label:'Gitea 实例地址',placeholder:'https://git.example.com',required:true},
   {key:'token',label:'访问令牌',hint:'Gitea 里生成的个人访问令牌，按 MCP 环境变量存放，不回显。',required:true,secret:true},
   {key:'command',label:'可执行文件',placeholder:'留空用自带的那份',hint:'只有要换成自己编译或另外安装的 gitea-mcp 时才填，可填命令名或绝对路径。'}
  ]},
  {id:'http',label:'连接已经跑起来的 gitea-mcp',hint:'已经用官方 Docker 镜像或别的机器跑了一份 gitea-mcp（-t http）时填它的地址。令牌配在那一侧，Diana 不经手。',fields:[
   {key:'url',label:'gitea-mcp 服务地址',placeholder:'http://127.0.0.1:8080/mcp',required:true},
   {key:'authorization',label:'Authorization 请求头',placeholder:'留空表示不带',hint:'只有给 gitea-mcp 另加了鉴权时才需要填。',secret:true}
  ]}
 ]
},{
 id:'mcdonalds',name:'mcdonalds',title:'麦当劳中国',
 summary:'麦当劳中国官方 MCP：查门店、菜单与营养信息，领麦麦省优惠券、积分兑换，以及麦乐送、到店取餐、得来速、团餐点单。令牌等同于点单权限，能直接下单付款。',
 docs_url:'https://github.com/M-China/mcd-mcp-server',
 transports:[
  {id:'http',label:'官方远程服务',hint:'麦当劳中国托管，不用自己跑任何东西，贴上令牌就能用。仅面向中国大陆（不含港澳台），每个令牌每分钟最多 600 次请求。',verifiable:true,fields:[
   {key:'token',label:'访问令牌',hint:'在 open.mcd.cn/mcp 用手机号登录后于控制台激活。这个令牌等同于你的点单权限，能直接下单付款，所以这条服务默认只有主人能用——放开给群成员等于让别人用你的账号点餐。',required:true,secret:true},
   {key:'url',label:'服务地址',placeholder:'https://mcp.mcd.cn',hint:'官方地址已经填好，除非官方改了地址，否则不用动。'}
  ]}
 ]
},{
 id:'luckin',name:'luckin',title:'瑞幸咖啡',
 summary:'瑞幸官方 MCP：查附近门店和商品、预览价格、一句话点单与再来一单。令牌等同于点单权限，能直接下单付款。',
 docs_url:'https://open.lkcoffee.com',
 transports:[
  {id:'http',label:'官方远程服务',hint:'瑞幸托管，不用自己跑任何东西，贴上令牌就能用。',verifiable:true,fields:[
   {key:'token',label:'访问令牌',hint:'用日常点单的手机号登录 open.lkcoffee.com 自助获取。这个令牌等同于你的点单权限，能直接下单付款，所以这条服务默认只有主人能用——放开给群成员等于让别人用你的账号点单。',required:true,secret:true},
   {key:'url',label:'服务地址',placeholder:'https://gwmcp.lkcoffee.com/order/user/mcp',hint:'官方地址已经填好，除非官方改了地址，否则不用动。'}
  ]}
 ]
}];

export function extensionDemoResponse(method:string,profile:string,body:Record<string,any>){
 const operation=method==='GET'?'list':body.operation;
 if(operation==='list')return {items:entries.map(({content,config,...item})=>({...item,enabled:item.enabled&&(overrides[profile]?.[item.id]??true),...(profile?{members_enabled:memberAccess[profile]?.[item.id]??false,...(residency[profile]?.[item.id]!==undefined?{resident:residency[profile][item.id]}:{}),...(memberAudience[profile]?.[item.id]?{member_audience:memberAudience[profile][item.id]}:{})}:{})}))};
 if(operation==='residency'){
  const item=entries.find(i=>i.kind===body.kind&&i.name===body.name);if(!item)throw Error('扩展不存在');
  const values=residency[profile||body.profile_id]??={};
  if(typeof body.resident==='boolean')values[item.id]=body.resident;else delete values[item.id];
  return {ok:true};
 }
 // 预设清单和真实部署保持一致：演示里也能走一遍「选预设 → 填字段 → 添加」。
 if(operation==='presets')return {items:demoMCPPresets.map(preset=>({preset,installed:entries.some(i=>i.kind==='mcp'&&i.name===preset.name),hidden:hiddenPresets.has(preset.id)}))};
 // 「删掉」预设只是把那一行藏起来，随时能放回来。
 if(operation==='presets'&&body.action==='hide'){hiddenPresets.add(String(body.preset));return {ok:true}}
 if(operation==='presets'&&body.action==='show'){hiddenPresets.delete(String(body.preset));return {ok:true}}
 if(operation==='save'&&body.preset){
  const preset=demoMCPPresets.find(p=>p.id===body.preset);if(!preset)throw Error('预设不存在');
  const transport=preset.transports.find(t=>t.id===body.transport);if(!transport)throw Error('预设没有这种接法');
  for(const field of transport.fields)if(field.required&&!String(body.values?.[field.key]??'').trim())throw Error(`请填写「${field.label}」`);
  const name=body.name||preset.name;if(entries.some(i=>i.kind==='mcp'&&i.name===name))throw Error('名称已存在');
  const url=String(body.values?.url??'');
  // 配置照真实后端的样子拼一份，不然演示站里点开编辑是一张空表。
  const endpoint=url||String(transport.fields.find(f=>f.key==='url')?.placeholder??'');
  const config=body.transport==='stdio'
   ?{command:String(body.values?.command||'/app/gitea-mcp'),args:['-t','stdio'],env:{GITEA_HOST:String(body.values?.host??''),GITEA_ACCESS_TOKEN:''},enabled:true,startup_timeout_sec:20,tool_timeout_sec:60}
   :{url:endpoint,headers:body.values?.token?{Authorization:''}:{},enabled:true,startup_timeout_sec:20,tool_timeout_sec:60};
  entries.push({kind:'mcp',id:`mcp:${name}`,name,description:preset.summary,source:url||'managed',managed:true,enabled:true,config,preset:preset.id,preset_transport:body.transport,preset_values:Object.fromEntries(transport.fields.filter(f=>!f.secret).map(f=>[f.key,String(body.values?.[f.key]??'')])),transport:body.transport==='stdio'?'stdio':'streamable_http'});
  return {ok:true};
 }
 // 演示站不连任何外部服务，也就没法真的验令牌——照实说，不伪造一个「验过了」。
 if(operation==='verify')return {verified:false,supported:false,message:'演示模式不连接外部服务，无法检测令牌，请在真实部署中检测'};
 const item=entries.find(i=>i.kind===body.kind&&i.name===body.name);
 if(operation==='read'){if(!item)throw Error('扩展不存在');return item.kind==='skill'?{content:item.content,managed:item.managed}:{config:item.config,configured_headers:Object.keys(item.config?.headers||{}),configured_env:Object.keys(item.config?.env||{}),...(item.preset?{preset:item.preset,preset_transport:item.preset_transport,preset_values:item.preset_values}:{})}}
 if(operation==='reveal'){if(!item)throw Error('扩展不存在');return {url:item.config?.url||'',headers:item.config?.headers||{},env:item.config?.env||{},preset_values:item.preset_values||{},preset_secrets:{}}}
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
