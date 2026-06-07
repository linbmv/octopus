# 实施计划：axonhub/llm 更新与能力分阶段接入

生成时间：2026-06-02
任务类型：后端为主，前端配置联动
范围限制：本计划仅用于后续执行，不在规划阶段修改产品代码。

## 1. 最终定论

双模型一致结论：建议更新，但必须分阶段执行。

- 必须优先修复依赖可复现性：当前 `go.mod` 使用 `github.com/looplj/axonhub/llm v0.0.0` 并 `replace` 到 `../axonhub/llm`，但本机 sibling 仓库不存在。
- 建议同步上游明确 commit 的 bug fix，不建议无约束长期追踪 `unstable`。
- 不建议一次性开放上游所有新增 APIFormat/RequestType。
- 第一阶段应保持现有公开能力不扩大，先验证 chat / responses / messages / embeddings / images 不回归。
- 第二阶段再选择性接入高价值能力。

## 2. 当前证据

### 2.1 本项目现状

| 位置 | 现状 | 影响 |
|------|------|------|
| `go.mod:11` | `require github.com/looplj/axonhub/llm v0.0.0` | 版本不可审计 |
| `go.mod:117` | `replace github.com/looplj/axonhub/llm => ../axonhub/llm` | 依赖依赖本地 sibling 仓库 |
| `internal/server/server.go:56-65` | 仅注册 chat/responses/messages/embeddings/images 路由 | 当前公开入口有限 |
| `internal/relay/transformers.go:15-91` | 仅处理 chat / embedding / image 三类 request type | 新增 rerank/video/compact/completion 不能直接接入 |
| `internal/relay/relay.go:249-255` | 使用 axonhub pipeline 与 `stream.EnsureUsage()` | 更新依赖需要验证 pipeline 行为 |
| `internal/relay/relay.go:464-485` | 自定义 middleware 记录状态码和 usage | 更新依赖不能破坏此副作用 |
| `internal/helper/fetch.go:14-27` | 模型拉取仅覆盖 OpenAI/Gemini/Anthropic/Doubao | 新 provider 需要新增模型拉取策略 |
| `web/src/api/endpoints/channel.ts:8-15` | 前端只暴露 6 种 ChannelType | 新 channel 需要前后端同步 |
| `web/src/components/modules/channel/Form.tsx:253-258` | 下拉框仅展示 6 种渠道 | 不宜一次性塞入全部上游格式 |

### 2.2 上游 axonhub/llm 新增能力

上游 `unstable` 当前新增或已有：

- RequestType：`chat`、`embedding`、`rerank`、`image`、`video`、`compact`、`completion`
- APIFormat：
  - `openai/chat_completions`
  - `openai/completions`
  - `openai/responses`
  - `openai/responses_compact`
  - `openai/image_generation`
  - `openai/image_edit`
  - `openai/image_variation`
  - `openai/embeddings`
  - `openai/video`
  - `gemini/contents`
  - `anthropic/messages`
  - `aisdk/text`
  - `aisdk/datastream`
  - `gemini/embeddings`
  - `jina/rerank`
  - `jina/embeddings`
  - `ollama/chat`
  - `seedance/video`

近期重要修复：

- Anthropic server-side tool blocks 防 panic
- Responses websocket sessions
- structured response item arguments
- non-stream fallback to require-stream channels
- responses raw items pass through
- pipeline 在 llm middleware 前暴露 RawRequest
- thinking/text stream block 顺序修复
- DeepSeek reasoning disabled 行为修复
- OpenAI reasoning field 默认值调整
- 跨 provider citation / annotation 支持
- Ollama chat 支持 image parts

## 3. 总体技术方案

### 3.1 阶段划分

| 阶段 | 目标 | 是否扩大公开能力 | 是否建议本次执行 |
|------|------|------------------|------------------|
| 阶段 0 | 记录当前状态和选择上游目标 commit | 否 | 是 |
| 阶段 1 | 修复依赖基线，移除本地 replace，锁定明确版本 | 否 | 是 |
| 阶段 2 | 验证现有能力不回归 | 否 | 是 |
| 阶段 3 | 补最小后端回归测试 | 否 | 是 |
| 阶段 4 | 选择性新增 provider/APIFormat | 是 | 视验证结果执行 |
| 阶段 5 | 单独评估 compact | 是 | 建议单独小阶段 |
| 阶段 6 | 单独评估 responses websocket | 否，偏传输优化 | 建议暂缓，先做设计验证 |

### 3.2 推荐策略

1. 第一批只做“依赖更新 + 现有能力回归”。
2. 对新增 RequestType 显式保持不支持，避免误路由。
3. 第二批优先评估：
   - `ollama/chat`
   - `jina/rerank`
   - `jina/embeddings`
   - `gemini/embeddings`
   - `openai/completions`
4. `openai/responses_compact` 单独接入，因为它是独立非流式 RequestType，不是普通 responses 的简单变体。
5. `responses websocket` 暂不作为公开路由能力，而是作为上游 Responses 出站传输优化单独评估。

## 4. 详细实施步骤

## 阶段 0：基线记录与上游 commit 选择

### 目标

确认本次更新的起点和上游目标版本，保证可回滚、可审计。

### 操作

1. 记录当前工作区：

```bash
git status --short
git log --oneline -5
```

2. 选择上游目标 commit：

建议优先选择包含以下修复后的明确 commit：

- `54ffbdf`：Anthropic server-side tool blocks 修复
- `74d223c`：Responses websocket sessions
- `11a286b`：structured response item arguments
- `88c399d`：pipeline RawRequest 暴露顺序修复
- `729e285`：thinking/text stream block 顺序修复
- `c1fd96c`：cross-provider citation / annotation

选择标准：

- 必须是上游 `unstable` 的明确 commit hash
- 必须能被 `go get github.com/looplj/axonhub/llm@<commit>` 解析
- 不使用浮动 `unstable` 作为长期依赖描述

### 预期产物

- 记录目标上游 commit
- 明确回滚点：当前 HEAD `97d6a04`

## 阶段 1：修复依赖基线

### 目标

消除本地 `../axonhub/llm` 依赖，让仓库在任意机器上可复现构建。

### 文件

| 文件 | 操作 | 说明 |
|------|------|------|
| `go.mod` | 修改 | 移除 `replace github.com/looplj/axonhub/llm => ../axonhub/llm`，锁定明确 pseudo-version |
| `go.sum` | 更新 | 生成 axonhub/llm 远程校验条目 |

### 操作

```bash
go get github.com/looplj/axonhub/llm@<上游目标commit>
go mod tidy
go list -m all | rg "axonhub|looplj"
```

### 验收标准

- `go.mod` 不再依赖本地 `../axonhub/llm`
- `go.sum` 出现 `github.com/looplj/axonhub/llm` 相关校验条目
- `go list -m all` 能显示明确 axonhub/llm 版本

## 阶段 2：现有能力兼容性验证

### 目标

确认只更新 axonhub/llm 依赖后，现有公开 API 不回归。

### 涉及现有入口

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/messages`
- `/v1/embeddings`
- `/v1/images/generations`
- `/v1/images/edits`
- `/v1/images/variations`

### 重点检查

| 检查点 | 文件 |
|--------|------|
| 入站格式仍能正常解析 | `internal/relay/transformers.go` |
| 出站 channel type 选择仍正确 | `internal/relay/transformers.go` |
| pipeline 执行顺序未破坏 ParamOverride / custom header | `internal/relay/relay.go` |
| 非流式 usage 仍记录 | `internal/relay/relay.go` |
| 流式聚合仍能记录最终响应与 usage | `internal/relay/relay.go` |
| 上游错误状态码仍能进入 key 状态和熔断逻辑 | `internal/relay/relay.go` |

### 验证命令

```bash
go test ./...
go build ./...
```

如果 Go 版本或工具链缺失，必须记录失败原因，并先补齐本地验证能力。

## 阶段 3：补最小后端回归测试

### 目标

在继续新增能力前，补足最低限度的 relay 回归保护。

### 建议新增测试范围

| 测试主题 | 覆盖内容 |
|----------|----------|
| `newInbound` | OpenAI chat/responses/embedding/images、Anthropic messages |
| `newOutbound` | Chat/Embedding/Image request type 与 OpenAI/Responses/Anthropic/Gemini/Doubao channel type 的兼容性 |
| 不兼容报错 | 新增或未知 request type 不应误路由 |
| ParamOverride | JSON 请求体覆盖，multipart 图片编辑不覆盖 |
| CustomHeader | 敏感头不覆盖认证头，普通 header 可追加 |
| Stream aggregation | `AggregateStreamChunks` 聚合最终响应和 usage |
| Upstream error | `OnOutboundRawError` 能记录 HTTP 状态码 |
| Non-stream usage | `OnOutboundLlmResponse` 能记录 usage |

### 建议文件

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/relay/transformers_test.go` | 新增 | 测试 transformer 选择与兼容性 |
| `internal/relay/relay_test.go` | 新增 | 测试 middleware 副作用与聚合行为 |
| `internal/helper/fetch_test.go` | 新增 | 测试不同 channel type 的模型拉取 URL 策略 |

### 验证命令

```bash
go test ./internal/relay ./internal/helper
go test ./...
```

## 阶段 4：选择性新增 provider/APIFormat

### 目标

在依赖更新和测试稳定后，选择性开放高价值格式。

### 推荐优先级

| 优先级 | 能力 | 建议 | 理由 |
|--------|------|------|------|
| P1 | `ollama/chat` | 可评估接入 | 本地模型常见，chat 语义接近现有能力 |
| P1 | `jina/embeddings` | 可评估接入 | embeddings 已有入口，扩展成本较低 |
| P1 | `gemini/embeddings` | 可评估接入 | embeddings 已有入口，provider 扩展清晰 |
| P2 | `jina/rerank` | 单独新增 `/v1/rerank` | 新 RequestType，需要新入口和模型拉取策略 |
| P2 | `openai/completions` | 视需求接入 | completion 是旧式 API，是否需要取决于用户场景 |
| P3 | `openai/video` / `seedance/video` | 暂缓 | 涉及任务状态、文件、计费和响应语义 |
| P3 | `aisdk/text` / `aisdk/datastream` | 暂缓 | 更偏前端 SDK 流格式，现有产品入口不匹配 |

### 每新增一种 channel/APIFormat 需要同步

| 文件 | 操作 |
|------|------|
| `internal/relay/transformers.go` | 新增 inbound/outbound 映射或显式不支持分支 |
| `internal/server/server.go` | 如需要新入口则注册新路由 |
| `internal/helper/fetch.go` | 新增模型拉取策略 |
| `internal/model/channel.go` | 如需要本地自定义 channel type 则新增常量 |
| `internal/db/migrate/003.go` | 如涉及旧数据迁移则扩展迁移 |
| `web/src/api/endpoints/channel.ts` | 新增 ChannelType |
| `web/src/components/modules/channel/Form.tsx` | 分组显示新类型 |
| `web/public/locale/en.json` | 新增英文文案 |
| `web/public/locale/zh_hans.json` | 新增简体中文文案 |
| `web/public/locale/zh_hant.json` | 新增繁体中文文案 |
| `README.md` / `README_zh.md` | 更新能力说明 |

### 前端展示策略

不采用扁平长下拉，改为分组：

- 标准 OpenAI 兼容
- Anthropic / Gemini
- 本地与第三方 provider
- 工具类能力：Embeddings / Rerank
- 实验能力：Video / AI SDK Streams，默认不展示或标记实验

## 5. compact 单独评估

### 5.1 上游事实

上游 `llm/transformer/openai/responses` 目录包含：

- `compact.go`
- `compact_inbound.go`
- `compact_outbound.go`
- `compact_*_test.go`

上游常量：

- `RequestTypeCompact = "compact"`
- `APIFormatOpenAIResponseCompact = "openai/responses_compact"`

### 5.2 行为特征

compact 不是普通 Responses 的一个参数，而是独立 request type：

- 请求体对应 `CompactAPIRequest`
- 入口语义是 `POST /v1/responses/compact`
- 强制非流式：`Stream = false`
- 数据存放在 `llm.Request.Compact`，不是普通 `Messages`
- compact input 必须保持有序 message array，不能压平成纯文本
- response 需要 `llm.Response.Compact`
- stream transform 和 stream aggregation 对 compact 返回不支持

### 5.3 对 octopus 的影响

当前 `internal/relay/transformers.go` 只处理：

- `RequestTypeChat`
- `RequestTypeEmbedding`
- `RequestTypeImage`

如果直接升级依赖但不接入 compact，compact 请求不会有入口，也不会被正确路由。

### 5.4 是否纳入本次

建议：**不纳入第一阶段依赖更新；作为第二阶段单独小功能接入。**

理由：

- 它是独立 RequestType，不是无成本 bug fix
- 不支持流式，需要在 relay 中明确非流式处理
- 日志、usage、请求体记录需要确认 `Compact` 字段不会丢失
- 前端暂无 compact channel/type 概念

### 5.5 compact 接入步骤

1. 后端路由新增：

```go
v1.POST("/responses/compact", middleware.RequireJSON(), relay.Handler(llm.APIFormatOpenAIResponseCompact))
```

2. `newInbound` 新增：

```go
case llm.APIFormatOpenAIResponseCompact:
    return responses.NewCompactInboundTransformer()
```

具体构造函数名需以更新后的上游源码为准。

3. `newOutbound` 新增 `RequestTypeCompact` 分支：

```go
case llm.RequestTypeCompact:
    switch channelType {
    case llm.APIFormatOpenAIResponseCompact:
        return responses.NewCompactOutboundTransformer(baseURL, key)
    default:
        return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
    }
```

具体构造函数名需以更新后的上游源码为准。

4. 明确 compact 不支持 stream：

- 如请求带 stream，应由 inbound transformer 或 relay 返回错误
- 测试中覆盖该行为

5. 日志验证：

- `RelayMetrics.InternalRequest` 能记录 compact 请求核心字段
- `RelayMetrics.InternalResponse` 能记录 compact 响应
- usage 能通过 `OnOutboundLlmResponse` 记录

6. 前端：

- 不建议第一时间作为普通 channel type 暴露
- 如果要暴露，应放入“实验能力”分组，并标注非流式

### 5.6 compact 验证命令

```bash
go test ./internal/relay -run Compact
go test ./...
```

冒烟请求：

```bash
curl -X POST http://127.0.0.1:8080/v1/responses/compact \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <octopus-api-key>" \
  -d '{
    "model": "<model>",
    "input": [
      {"role": "user", "content": [{"type": "input_text", "text": "请压缩这段上下文"}]}
    ],
    "instructions": "保留关键事实"
  }'
```

## 6. responses websocket 单独评估

### 6.1 上游事实

上游 `llm/transformer/openai/responses/websocket_executor.go` 提供：

- `WebSocketExecutor`
- `NewWebSocketExecutor(inner pipeline.Executor)`
- `Do(ctx, request)`
- `DoStream(ctx, request)`
- `Close()`
- `TopLevelWebSocketError(chunks)`

它设置 OpenAI Beta header：

```text
OpenAI-Beta: responses_websockets=2026-02-06
```

### 6.2 行为特征

responses websocket 不是新的入站 APIFormat，也不是新的 Gin route。

它是 Responses API 的出站传输执行器：

- 将 HTTP/HTTPS URL 转换为 WS/WSS
- 将普通 Responses 请求转成 websocket payload
- 支持 `Do` 聚合为 HTTP-style response
- 支持 `DoStream` 直接返回 stream events
- 支持 session pooling
- 需要 session id 和 session scope 才能复用连接
- 可根据 `previous_response_id` 和 input 前缀进行增量复用

### 6.3 对 octopus 的影响

当前 `internal/relay/relay.go` 使用：

```go
pipeline.NewFactory(httpclient.NewHttpClientWithClient(httpClient))
```

如果要启用 websocket，需要在 Responses 出站场景中选择或包装 executor：

```go
executor := responses.NewWebSocketExecutor(httpclient.NewHttpClientWithClient(httpClient))
pipeline.NewFactory(executor)
```

具体类型和包路径需以更新后的上游源码为准。

### 6.4 是否纳入本次

建议：**不纳入第一阶段，也不和 compact 同批接入；作为独立优化实验评估。**

理由：

- 它不要求新增 Gin route，但改变出站传输路径
- 涉及连接池生命周期、session scope、上下文复用、错误驱逐策略
- 当前 octopus relay retry/failover 逻辑基于一次通道尝试和 HTTP 状态码记录，需要确认 websocket 错误如何进入 `OnOutboundRawError`、key 状态、熔断和重试
- 当前 API key / channel / sticky routing 是否适合 websocket session reuse 需要单独设计
- 连接池生命周期需要跟 server shutdown 绑定，否则可能泄露连接

### 6.5 responses websocket 接入前置问题

必须先回答：

1. session id 来源是什么？
   - 客户端 header？
   - Octopus 自己生成？
   - API key + model + channel 维度？

2. session scope 如何定义？
   - 单用户？
   - 单 API key？
   - 单 channel key？
   - 单 model？

3. 是否允许跨重试复用 websocket？
   - 如果第一次通道失败，第二个通道不能复用第一通道的 session

4. websocket 错误如何影响熔断？
   - top-level error
   - response.failed
   - response.cancelled
   - response.incomplete

5. 如何关闭 executor？
   - server shutdown 调用 `Close()`
   - channel 配置变更是否需要清理连接池

### 6.6 responses websocket 建议接入方式

1. 第一阶段只在代码中保留普通 HTTP Responses 路径。
2. 增加内部配置项控制 websocket，但默认关闭。
3. 只对 `llm.APIFormatOpenAIResponse` + `RequestTypeChat` 的 OpenAI Responses 出站启用。
4. 不影响 Anthropic/Gemini/Doubao 出站。
5. 单独编写 websocket executor 生命周期测试。
6. 冒烟通过后再考虑对用户开放配置。

### 6.7 responses websocket 验证命令

```bash
go test ./internal/relay -run WebSocket
go test ./...
```

冒烟场景：

- Responses 非流式 websocket
- Responses 流式 websocket
- 上游 top-level error
- response.failed
- 首 token 超时
- 客户端断开
- 同一 session 连续请求复用
- 不同 API key / channel key 不复用
- server shutdown 后连接关闭

## 7. 风险与缓解

| 风险 | 等级 | 缓解 |
|------|------|------|
| 移除 replace 后依赖解析失败 | Critical | 先选明确 commit，执行 `go get` 和 `go mod tidy` |
| axonhub 接口名变化导致编译失败 | Critical | 先只做依赖更新，按编译错误调整 import/构造函数 |
| pipeline 行为变化影响 usage/日志 | Major | 补 middleware 回归测试 |
| 新 RequestType 被误路由 | Major | `newOutbound` 对未知类型显式报错 |
| 前端展示超多 channel 导致 UX 混乱 | Major | 分组展示，默认只开放已验证能力 |
| compact 丢失 `Compact` 字段 | Major | 单独测试 `llm.Request.Compact` 和 `llm.Response.Compact` |
| websocket 连接池泄露 | Major | executor 生命周期绑定 server shutdown |
| 没有测试导致回归不可见 | Major | 阶段 3 必须补最小测试 |

## 8. 验证计划

### 后端基础验证

```bash
go test ./...
go build ./...
```

### 后端专项验证

```bash
go test ./internal/relay
go test ./internal/helper
```

### 前端验证

```bash
cd web
pnpm install --frozen-lockfile
pnpm lint
pnpm build
```

### 手动冒烟

```bash
curl -X POST http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <octopus-api-key>" \
  -d '{"model":"<model>","messages":[{"role":"user","content":"hello"}]}'
```

```bash
curl -X POST http://127.0.0.1:8080/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <octopus-api-key>" \
  -d '{"model":"<model>","input":"hello"}'
```

```bash
curl -X POST http://127.0.0.1:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <octopus-api-key>" \
  -d '{"model":"<embedding-model>","input":"hello"}'
```

## 9. 交付顺序建议

### PR / 变更批次 1：依赖可复现 + 现有能力回归

- 修改 `go.mod`
- 更新 `go.sum`
- 保持公开 API 不扩大
- 补基础 relay 测试
- 通过 `go test ./...` 和 `go build ./...`

### PR / 变更批次 2：选择性新增 embeddings / rerank provider

- 新增 `jina/embeddings`、`gemini/embeddings`
- 可选新增 `jina/rerank` 和 `/v1/rerank`
- 更新前端 ChannelType 和 i18n

### PR / 变更批次 3：compact

- 新增 `/v1/responses/compact`
- 新增 `RequestTypeCompact` 分支
- 补 compact 专项测试

### PR / 变更批次 4：responses websocket 实验

- 增加内部配置，默认关闭
- 接入 WebSocketExecutor
- 补连接池和错误处理测试
- 验证后再决定是否对 UI 暴露

## 10. 多模型结论记录

### Codex

- SESSION_ID：`019e86d9-175b-72e1-9830-1d44821a6a74`
- 结论：建议更新，但先修复依赖可复现性并锁定明确 commit；新增 RequestType 必须显式处理，测试不足是主要风险。

### Gemini

- SESSION_ID：`2c010648-5713-4fce-a410-1f9eb95585a4`
- 结论：不要一次性在 UI 暴露所有上游格式；建议依赖更新优先，新增能力通过分组和渐进式开放。

## 11. 最终建议

本次执行建议只做：

1. 移除本地 `replace`。
2. 锁定 axonhub/llm 明确 commit。
3. 验证现有公开 API 不回归。
4. 补 relay 最小回归测试。
5. 对新增 RequestType 保持显式不支持。

暂不直接做：

- 全量接入上游所有 provider。
- 直接开放 `video` / `seedance`。
- 默认启用 `responses websocket`。
- 把所有新格式一次性放进前端下拉框。

`compact` 建议作为依赖更新后的第一个独立新增能力候选；`responses websocket` 建议作为更晚的传输层优化实验。
