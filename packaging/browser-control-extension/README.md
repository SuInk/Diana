# Diana 浏览器控制扩展

用户自己安装、自己授权的浏览器扩展。它反向连到 Diana 的 WebUI，按控制面下发的有限指令操作**已授权站点**的页面，用户随时可以接管。

完整说明见 [docs/browser-control.md](../../docs/browser-control.md)。

## 安装（开发者模式加载）

1. Chrome / Edge 打开 `chrome://extensions`，开启「开发者模式」。
2. 「加载已解压的扩展程序」，选择本目录。
3. 记下扩展 ID，填进 Diana 的「浏览器控制 → 允许的来源」，格式 `chrome-extension://<扩展 ID>`。
4. 打开扩展选项页，填 Diana 的 WebUI 地址与令牌，点「保存并连接」，再点「授权这些站点」。

## 文件

| 文件 | 作用 |
| --- | --- |
| `manifest.json` | MV3 清单。站点权限放在 `optional_host_permissions`，由用户按白名单逐批授权，不预先要 `<all_urls>` |
| `background.js` | Service Worker：连接控制面、上报标签页、执行指令、维护接管状态 |
| `policy.js` | 站点白名单与读写档位的扩展侧实现，规则与 `model/browserctl/policy.go` 一致 |
| `options.html` / `options.js` | 配置页：地址、令牌、备注、站点权限申请、接管开关、状态 |

## 边界

- 不读写 Cookie，不替用户填密码框，不执行控制面发来的任意脚本——协议里没有这类指令。
- 只上报白名单内标签页的地址和标题，其余标签页不出现在任何帧里。
- 接管打开时一条指令都不执行。
- 站点权限只申请白名单里的 origin；白名单变了要重新申请。

改 `policy.js` 的匹配规则时必须同步改 `model/browserctl/policy.go`，两侧规则不一致会让一边拦、另一边放。
