# render_assets

`diana.render` 出图时用到的第三方资源。截图沙箱没有网络，页面必须自包含，
所以这些文件是内嵌进二进制的（`//go:embed`），不是运行时下载的。

## mermaid.min.js

- 版本：mermaid 11.4.1
- 来源：`https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js`
- 许可：MIT（打包内含 DOMPurify 3.2.1，Apache-2.0 / MPL-2.0，二者都允许再分发）
- 体积：约 2.5 MB，全部计入二进制

这个包末尾会 `globalThis.mermaid = ...`，所以用普通 `<script>` 引入即可，
不需要 `type="module"`。初始化在 `render_image_html.go` 的 `mermaidBootstrap`，
固定用 `securityLevel: "strict"`：禁掉 `click` 指令，标签里的 HTML 也会被转义。

### 升级

换版本时把新文件放在同一路径覆盖，并核对两件事：

1. 末尾仍然把 mermaid 挂到 `globalThis`（换成纯 ESM 的话要改引入方式）
2. `TestBuildRenderPageIsSelfContained` 仍然通过 —— 它会检查页面里没有外链、
   并且 `securityLevel` 确实是 strict
