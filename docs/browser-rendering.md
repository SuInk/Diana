# Chromium / Chrome 浏览器依赖

网页渲染统一使用 Chromium / Google Chrome。插件不再下载或回退到 Obscura；此前手动安装的 Obscura 文件不会被自动删除。

## 插件安装

在「插件 → 网页渲染 → 依赖管理」中安装并刷新状态：

- Linux：通过 apk、apt、dnf/yum 或 pacman 安装 `chromium`。
- macOS：通过 Homebrew 安装 Google Chrome。Homebrew 的 Chromium cask 已被禁用，因此不提供该失效安装路径。
- Windows：通过 winget 或 Chocolatey 安装 Google Chrome。

已有 Chrome/Chromium 时直接复用。包管理器安装需要对应系统权限；没有包管理器或权限不足时，会显示原因，可手动安装后刷新。安装成功后同时验证本地截图与网页沙箱/CDP，而非只检查版本号。源码或原生部署应以普通用户运行浏览器；中文字体可在插件依赖管理中一键下载；实际出图时也会按文字补齐缺失的多语言字体，无需管理员权限，详见[字体下载与多语言出图](fonts.md)。

## Docker

官方运行镜像预装 Chromium、fontconfig 和 Noto CJK 字体，并以 UID 10001 运行 Diana。仓库的 `docker-compose.yml` 默认拉取预构建镜像，已配置专用 seccomp 规则。首次在部署目录执行一键脚本，自动下载 Compose 文件与 `scripts/docker/chromium-seccomp.json` 并启动（需已安装并启动 Docker，含 Compose v2）：

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

以后在同一目录更新：

```sh
docker compose pull && docker compose up -d
```

从克隆的仓库本地构建：

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

已有 `docker run --name diana` 部署迁移到 Compose 时，先确认 Compose 使用原来的 `data/`、`logs/` 和可选配置文件路径，再停止并删除旧容器（保留宿主机数据目录），以免同名容器冲突。

seccomp 配置必须保存在宿主机，Docker 在创建容器时读取它；它不是挂载给应用的配置文件。旧版尚未包含预装浏览器，需要升级到包含浏览器的镜像。

默认 Docker seccomp 会阻止 Chromium 创建其沙箱所需的命名空间，表现为 `Operation not permitted` 或 CDP 启动失败。项目配置保留默认拒绝策略，只在 Moby 默认配置基础上额外允许 `clone`、`setns`、`unshare`。不需要 `--privileged`、`SYS_ADMIN` 或 `seccomp=unconfined`，网页渲染也不添加 `--no-sandbox`。宿主机另有 AppArmor 或用户命名空间禁令时仍需按管理员策略处理，插件探测会显示实际失败原因。

本地 HTML 截图与外部网页读取走不同启动路径，因此截图成功不代表网页沙箱可用；依赖页现已分别验证两者。所有渲染使用临时浏览器配置，不读取用户日常浏览器登录态。

## seccomp 配置来源

`scripts/docker/chromium-seccomp.json` 基于 [Moby profiles 默认 seccomp](https://github.com/moby/profiles/blob/245180c51918481c0525424b3ee025d2b435d46c/seccomp/default.json)，上游提交 `245180c51918481c0525424b3ee025d2b435d46c`。修改仅为末尾增加上述三个调用的允许规则；`clone3` 继续沿用上游限制。原始 Apache-2.0 许可保存在 `scripts/docker/LICENSE.moby-profiles`。更新上游基线时须保留许可证并重新测试 Linux amd64/arm64 沙箱启动。
