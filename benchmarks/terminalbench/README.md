# Diana Terminal-Bench 接入

通过 Harbor 在官方任务容器内运行 Diana 的 `model/agent.Runner`、原生工具注册表、模型适配器、协议修复和默认系统提示词。没有用另一个 Agent 替嘉然解题。

范围：单次原生 agent runner；不覆盖聊天平台、长期记忆、完整 assistant 会话层或跨轮持久化。文件与命令工具开启，浏览器关闭；隔离由任务容器提供。工作目录沿用官方镜像的 WORKDIR；原生文件工具保持工作目录边界，shell 可操作任务要求的其他路径。保留 Diana 的 32 次工具调用硬上限、60 秒单命令上限；默认每题 900 秒、32768 输出 token，是试跑预算，不能当作原始榜单配置。

## 准备

需要可用的 Docker Engine/Compose，Python 3.12+。Harbor 固定为 `0.1.44`，避免跟随最新版接口变化。

```sh
python3 -m venv .venv-benchmark
.venv-benchmark/bin/pip install -r benchmarks/terminalbench/requirements.txt
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/diana-terminalbench ./benchmarks/terminalbench
export DIANA_BENCH_BINARY=/tmp/diana-terminalbench
export DIANA_BENCH_CONFIG=/absolute/private/provider.json
export PYTHONPATH="$PWD/benchmarks/terminalbench"
```

`provider.json` 是 Diana 的 `llm.ProviderConfig` JSON（provider、model、api_key、base_url、api_format、reasoning_effort 等），权限设为 0600，不放进仓库。模型配置会传入一次性任务容器，启动前删除临时输入文件。原生 JSONL 日志记录调用、工具输出、用量、结束原因，并脱敏 API key。日志仍可能含任务数据，应按私有评测产物保管。

## 运行

先运行官方 oracle 检查环境和验证器，再使用同一任务运行嘉然：

```sh
.venv-benchmark/bin/harbor run -d terminal-bench@2.0 -a oracle --task-name '<task-name>' -n 1
.venv-benchmark/bin/harbor run -d terminal-bench@2.0 \
  --agent-import-path diana_agent:DianaAgent -m openai/gpt-5.6-sol \
  --task-name '<task-name>' -n 1
```

以固定版本 `harbor run --help` 为准选择任务。每次使用新的 jobs 目录，保存配置、数据集 revision、Diana commit、模型配置（移除密钥）、原生调用轨迹和 verifier reward。只能把官方 verifier reward 算作任务成功，不能使用模型自述。环境/oracle 失败单列，不计为嘉然解题失败。

与 Pi 比较时固定模型、提供商、推理强度、任务、时间和 token 预算，分别报告框架原生限制。少量任务只报告逐题结果；全量/重复试验完成前不要声称榜单水平。此入口明确拒绝在没有 `/.dockerenv` 的主机上运行，但该检查不是独立安全边界。

## ARM 主机注意事项

本次 Colima/QEMU 环境中，Go 1.26.6 的 amd64 二进制在 JSON 解析中出现致命 map 错误；同一最小复现的原生 arm64 二进制通过。ARM Linux 内核可直接执行静态链接的 arm64 嘉然程序，即使任务镜像为 amd64。此时将构建命令改为 `GOARCH=arm64`，仍保留官方任务镜像；报告必须标明混合架构，不将运行耗时与原生 amd64 榜单直接比较。启动付费任务前先验证 agent 二进制在选定环境中稳定运行。
