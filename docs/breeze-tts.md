# Breeze 语音合成

在「提供商」添加 OpenAI 兼容类型的配置档，地址填 `https://api.breeze.blue/v1`，API Key 填 Breeze 控制台生成的密钥。这里只借用配置档存储地址和密钥，合成使用 Breeze 协议，认证头是 `xi-api-key`。

在「提供商 → 编辑配置 → 语音服务」打开「语音合成参数」，在这里配置：

- 在提供商模型列表中手动添加 Breeze 的模型 ID，例如 `breeze-tts-2`；可用模型以账号的模型列表为准。
- 接口选「按地址自动」即可识别 `breeze.blue` 及其子域名；自建代理地址需手选「Breeze」。
- 音色填写 Breeze 的 `voice_id`，例如群里示例的 `voc_kgj4fsfnqmf4`；需确认该音色对自己的账号可用。
- 格式默认 MP3，也可以选 WAV。界面不提供裸 PCM，避免聊天端无法播放。
- 语速可填 `0.5`–`2.0`，默认 `1.0`。语气说明和语言（如 `zh`、`en`）可选，语气说明应与输入语言一致。

机器人「模型 → 语音合成」只需选择该提供商和模型，再设置发送模式、单次字数上限。各后备提供商使用自己的语音参数。语音识别参数也在提供商的「语音服务」中单独设置，不与语音合成共用。

旧机器人插槽参数继续兼容，直到对应提供商启用了该语音用途的参数；启用后，即使字段留空也使用提供商默认值，不重新继承旧音色或协议。关闭提供商的语音参数会恢复沿用旧插槽参数。内置免费服务与本机 Whisper 仍在机器人页配置。

普通语音消息和通话都请求 `POST /v1/text-to-speech/{voice_id}/stream`，显式传入 `output_format=mp3`（或选择的格式）。普通消息读取完整音频后发送，通话逐块下发，不等待整句合成完成。Breeze 流式输出为 24 kHz 单声道；底层接口也接受 `pcm`，通话会将其改为 MP3。这里没有接入 Breeze 的跨轮次 WebSocket 协议。

Breeze HTTP 单次输入默认最多 1,000 字符，获批账号可更高（最多 2,000）；Diana 语音消息的默认字数上限仍为 500。本适配不自动拆分超长请求，不支持 ElevenLabs 的 `mp3_44100_128` 等格式名。服务端错误沿用现有媒体调用重试与路由后备机制，通话交出音频后不会换路由从头重念。

协议依据：[HTTP 流式合成](https://docs.breezeblue.ai/api-reference/text-to-speech/stream-text-to-speech)、[流式音频格式](https://docs.breezeblue.ai/concepts/streaming)、[语速与表达控制](https://docs.breezeblue.ai/guides/text-to-speech)。
