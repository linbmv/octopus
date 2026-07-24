# 上游渠道自愈实施计划

> 状态：P0/P1/P2 已实现。P2 compare 诊断模式（`mode:"compare"` + `golden_sample`）已接入 API 与 UI（`GoldenSampleDiff.tsx`），置信度计算收敛到 `internal/selfheal/confidence.go`；供应商 profile 目录（`selfheal/profile/`、`selfheal/diff/`）按需合并进了 `compare.go`/`importer/`，未单独建包
> 适用范围：Octopus Go 网关、渠道 failover、能力探测、渠道配置 UI
> 方案来源：用户诉求、现有代码核查、Claude 方案和 Codex 方案的合并结果
> 版本：2026-07-24

## 1. 执行摘要

AnyRouter 当前运行正常，因此本计划不以“重新配置 AnyRouter”为目标。目标是建立一个可控的渠道自愈闭环：当上游容量故障、认证失效、WAF/客户端指纹限制、请求头变化或请求体 schema 变化发生时，Octopus 能够在后台发现异常，判断根因，给出证据和配置差异，并由管理员一键应用经过验证的补丁。

最终闭环如下：

```text
正常请求成功
    |
    v
记录脱敏的最终请求形状，形成最后成功基线
    |
    v
哨兵按渠道/模型/端点执行有界最小探测
    |
    +--> 容量、限流、网络、Key 失效 ------> 标记状态，等待恢复或切换 Key/渠道
    |
    +--> 400/schema、WAF、200 HTML、SSE 解码异常
            |
            v
        诊断变体矩阵（定向渠道，隔离生产状态）
            |
            v
        生成证据、差异和配置补丁预览
            |
            v
        管理员确认 -> 事务更新 -> 单次验证 -> 成功保留 / 失败自动回滚
```

本计划采纳双方方案的互补部分：

| 视角 | 采纳内容 | 约束 |
|---|---|---|
| Claude | “基线快照 + 后台哨兵 + 自动诊断 + 一键修复”的三层递进；先区分容量问题和协议变化；诊断自动、应用手动；诊断请求不进入 failover、熔断和普通统计 | 诊断结果不能因为一次上游抖动而改写生产配置 |
| Codex | 请求预览、单次 live 诊断、已知可用请求对比三种模式；在最终 outbound middleware 捕获；复用真实 transformer；认证头脱敏、会话 TTL、审计、Body 大小限制 | 不复制一套独立的参数处理逻辑，不保存密钥和默认用户内容 |

### 1.1 交付顺序

| 阶段 | 目标 | 默认开关 | 结果 |
|---|---|---|---|
| P0 | 统一错误证据、最终请求形状和成功基线 | 关闭 | 有可追溯事实，但不自动发诊断请求、不改配置 |
| P1 | 后台哨兵、变体诊断、补丁预览和一键应用 | 关闭，管理员逐渠道开启 | 能发现协议漂移并通过确认应用补丁 |
| P2 | cURL/HAR/CLI 黄金样本对比、供应商 profile、置信度和回滚增强 | 关闭，按需开启 | 能处理复杂 AnyRouter/CLI 指纹变化，并降低误诊 |

## 2. 问题定义与边界

### 2.1 已确认的运行事实

1. Anyrouter Claude 使用 Anthropic Messages，实际依赖 `claude-cli` User-Agent 和较长的 `anthropic-beta` 头。
2. Anyrouter Codex 使用 OpenAI Responses，实际依赖 `codex-tui/0.144.6`、`originator` 等 Codex 头，并曾使用 `raw_passthrough=1`。
3. 上游曾出现 403 User-Agent 拒绝、WAF JavaScript 质询页、500 负载上限、429/503、HTTP 200 携带 HTML、SSE 首字节解码异常等不同形态。
4. Anyrouter Codex 曾出现 246 次调用 0 成功；已知根因包含上游满载且同一分组没有备用渠道。
5. `codex_capture.py` 等外部透明代理可以捕获 CLI 的真实成功请求，但当前不是 Octopus 运行时的一部分。

### 2.2 现有代码基础

| 能力 | 当前位置 | 可复用方式 |
|---|---|---|
| 能力探测队列、并发、RPM、TTL、成本预算 | `internal/capability/worker.go`、`internal/capability/prober.go` | P0 统一探测执行边界，P1 复用调度器或抽出共享 job runner |
| 标准协议探测模板 | `internal/capability/prober.go` | 作为诊断矩阵的 baseline 模板，不作为真实 CLI 请求的唯一事实 |
| 错误级别 `key/channel/client` | `internal/relay/errorclass/classifier.go` | 保持 failover 语义；另增加诊断根因，不混淆两者 |
| HTTP 200 soft error 和 SSE error 识别 | `errorclass.ClassifyWithHeaders` | 所有探测、live 诊断和 relay 错误都经过统一入口并传递 headers |
| 熔断、半开探测和健康评分 | `internal/relay/balancer`、`internal/relay/health` | 生产请求继续使用；诊断请求必须显式绕开其状态变更 |
| 渠道 Header、UA、ParamOverride、JSON rewrite、raw passthrough | `internal/model/channel.go`、`internal/relay/attempt_options.go` | 补丁只写入已有配置面，最终请求始终走同一改写顺序 |
| 渠道 DB+缓存事务更新 | `internal/op/channel.go:ChannelUpdate` | 补丁应用使用现有事务、缓存刷新和能力证据失效机制 |
| 最终出站请求拦截点 | `internal/relay/pipeline_middleware.go` | 捕获最终 URL、Header 和 Body 形状，避免在 inbound 或 prober 中猜测 |
| 渠道探测 UI/API | `internal/server/handlers/channel.go`、`web/src/components/modules/channel` | 扩展为自愈状态、诊断会话和补丁审核界面 |
| 审计日志 | `internal/server/middleware/audit.go` | 记录创建、查看、导出、应用、回滚和拒绝操作 |

### 2.3 当前缺口

1. 能力 probe 生成的是固定标准 Body，不能证明上游接受真实客户端的 `instructions`、`input` 数组、Codex 头、Claude 缓存/思考字段或渠道当前 rewrite 后的完整 Body。
2. `internal/capability/prober.go` 有自己的状态分类逻辑，尚未完全使用带 headers 的 `ClassifyWithHeaders`，因此可能丢失 `Retry-After`、`X-RateLimit-Scope` 和 HTTP 200 soft error 的范围信息。
3. `relayPipelineMiddleware.OnOutboundRawRequest` 可以看到最终改写前后的请求，但目前只记录有界摘要，没有“成功请求基线”持久化能力。
4. 没有诊断会话、变体尝试、补丁版本和回滚快照的数据模型。
5. 现有 `ChannelUpdate` 可以更新所需字段，但还没有“基于预期 fingerprint 的并发保护”和“应用后自动验证失败回滚”语义。
6. 当前 UI 只有手动 capability probe，没有显示协议漂移、诊断证据、补丁 diff 和应用结果的界面。

### 2.4 非目标

- 不根据单次 400/403 自动修改生产配置。
- 不自动轮换、打印、导出或猜测 API Key。
- 不把诊断请求加入正常 failover、粘性会话、熔断计数、渠道健康评分、RelayLog 统计或费用统计。
- 不在第一版自动替换 Base URL、代理凭据、认证 Header、模型权限和 Key；这些变更需要人工处理。
- 不将完整用户 prompt、图片、Cookie、Authorization、代理密码写入数据库、日志、备份或前端响应。
- 不把“HTTP 200”直接当作成功；必须经过协议/soft error/SSE 完整性判断。

## 3. 目标架构

### 3.1 组件图

```text
                         +----------------------+
                         |  Channel UI/API       |
                         | status/diagnose/apply |
                         +----------+-----------+
                                    |
                           Self-healing Handler
                                    |
              +---------------------+---------------------+
              |                                           |
   +----------v----------+                       +--------v---------+
   | Sentinel Scheduler  |                       | Diagnostic API   |
   | periodic/failure    |                       | preview/live/diff |
   +----------+----------+                       +--------+---------+
              |                                           |
              +---------------------+---------------------+
                                    v
                      +-----------------------------+
                      | Diagnostic Orchestrator     |
                      | bounded matrix + classifier |
                      +---------------+-------------+
                                      |
                         +------------v------------+
                         | Shared Request Builder   |
                         | inbound/outbound rules   |
                         +------------+------------+
                                      |
                         +------------v------------+
                         | Isolated Probe Client    |
                         | one channel/key only     |
                         +------------+------------+
                                      |
                         +------------v------------+
                         | Upstream + evidence      |
                         | headers/body/status      |
                         +-------------------------+

 Normal relay path: inbound -> candidate selection -> pipeline middleware -> upstream
                                      |
                                      v
                         Request Shape Recorder -> successful baseline
```

### 3.2 三种诊断模式

| 模式 | 是否访问上游 | 用途 | 默认限制 |
|---|---:|---|---|
| 请求预览 `preview` | 否 | 显示 Octopus 最终准备发出的 URL、脱敏 Header、Body shape、rewrite 命中情况 | 不产生费用，不能证明上游接受 |
| 单次 live `live` | 是 | 在指定渠道、模型和 Key 上执行最多一次真实请求 | 管理员明确触发；不 failover、不记生产健康；5 分钟会话 TTL |
| 黄金样本对比 `compare` | 可选 | 将 CLI/cURL/HAR 脱敏请求与 Octopus 最终请求逐字段比较 | P2；样本大小、字段数和保存期限有界 |

P1 的自动诊断只使用 `preview` 和受预算约束的 `live` 变体；P2 再增加黄金样本导入，避免把外部抓包工具作为 P1 的运行时依赖。

### 3.3 共享最终请求形状

新增内部的不可变 `RequestArtifact`（名称可调整）作为 relay、capability probe 和 diagnostics 的共同产物。它应包含：

```json
{
  "method": "POST",
  "url": "https://anyrouter.top/v1/responses",
  "protocol": "openai/responses",
  "model": "gpt-5.6-sol",
  "headers": {
    "user-agent": "codex-tui/0.144.6",
    "originator": "codex-tui",
    "x-codex-beta-features": "remote_compaction_v2",
    "authorization": "[redacted]"
  },
  "body_shape": {
    "top_level_keys": ["input", "model", "stream"],
    "input": "array",
    "input[].content": "array",
    "body_bytes": 2048
  },
  "shape_sha256": "hash-of-redacted-shape",
  "rewrite": {
    "raw_passthrough": true,
    "param_override": false,
    "json_rewrite": false,
    "header_rules": true
  }
}
```

规则：

- `Authorization`、`x-api-key`、`x-goog-api-key`、Cookie、Proxy-Authorization 只保留存在/脱敏标记；不保留原值。
- Body 默认只保留结构、类型、顶层 key、数组长度和大小；用户内容替换为类型/长度桶，不对原始 prompt 做普通 SHA-256。
- 如需判断同一形状是否重复，hash 的输入必须是去除用户内容后的 canonical shape；不得对原始用户 Body 做可离线猜测的裸 hash。必要时使用实例级 HMAC，且不返回密钥。
- Header 名称统一小写排序，值按 allowlist 保留；UA、originator、Codex/Anthropic 指纹头可以保留，任意认证值都不保留。
- Body 最大捕获 64 KiB，响应最大扫描 8 KiB，完整响应最大读取仍使用现有有界限制。
- 捕获器只能在 middleware 之后看到最终请求，不新建另一条 rewrite 规则执行路径。

### 3.4 基线快照

基线不是“永远正确的配置”，而是“最近一次经过成功响应验证的请求形状”。建议新增 `ChannelBaseline` 模型/表：

| 字段 | 含义 |
|---|---|
| `id` | 主键 |
| `channel_id`、`channel_key_id` | 渠道和 Key 范围；只存 ID，不存密钥 |
| `model`、`wire_protocol`、`endpoint_fingerprint` | 请求范围 |
| `scope_fingerprint` | 复用 `CapabilityScopeFingerprint` 的思想，绑定 UA/Header/rewrite/端点/key 变化 |
| `request_shape_json` | 脱敏的 `RequestArtifact` |
| `request_hash` | 去除用户内容后的 canonical shape hash；不得是原始 prompt 的裸 hash |
| `success_evidence` | HTTP 状态、Content-Type、首个合法 SSE/响应事件、耗时 |
| `source` | `relay_success`、`manual_live`、`golden_import` |
| `captured_at`、`expires_at` | 基线有效期和清理时间 |
| `version` | 应用补丁和回滚的乐观锁版本 |

基线更新条件：

1. 正常 relay 请求经过统一错误分类并确认成功，或诊断 live 请求明确验证成功。
2. 流式请求至少收到合法的 SSE 首事件，且不是 `event: error`；不能只看 HTTP 200。
3. 同一 scope 只保留最近 N 份（建议 3），按成功率和时间选主基线。
4. 捕获失败不能影响用户请求；只记录有界 warning。
5. 渠道配置、Key、端点或协议发生变化时，旧基线变为 stale，不删除，便于比较和回滚。

### 3.5 哨兵调度

后台哨兵不是每分钟无差别打全量渠道，而是两种触发合并：

- 周期触发：默认每 30 分钟，对开启自愈且有有效模型/Key/端点的渠道执行一次最小 text probe；生产环境可配置 5 分钟至 24 小时。
- 失败触发：同一渠道/模型/端点在时间窗口内达到阈值（建议连续 3 次或 5 分钟内 3 次），且错误不是已经确认的客户端输入错误时，排入一次诊断。

调度约束：

- 使用现有 capability worker 的队列、RPM、并发、超时、成本预算，但诊断 job 使用独立命名空间和预算，不能因诊断占满 capability 队列而饿死普通运维操作。
- 每个渠道同时最多一个 sentinel，最多一个 diagnostic session；整个实例有全局并发上限。
- 失败使用指数退避和抖动；渠道被判定为 `channel` 容量问题时延长间隔，不继续试头和 Body。
- 默认关闭全局自愈；开启后仍需要每个渠道显式 `self_healing_enabled`，防止升级后突然产生上游费用。
- 诊断使用指定渠道和一个指定 Key，禁止自动选择其他渠道或进行 failover。

## 4. 故障分类决策树

### 4.1 两层分类模型

必须把两个概念分开：

1. `ErrorLevel` 是 relay 选路语义：`key`、`channel`、`client`。它决定是否轮换 Key、切换渠道或直接返回客户端。
2. `RootCause` 是自愈诊断语义：`capacity`、`rate_limit`、`auth`、`waf_or_client_fingerprint`、`protocol_drift`、`endpoint`、`network`、`decode`、`unknown`。

例如，上游因请求体 schema 变化返回 HTTP 400，生产 relay 仍可将它归为 `client` 以避免盲目切换渠道；自愈层则可以根据重复失败、与成功基线的差异将根因标为 `protocol_drift`，触发诊断。不能用诊断根因反向改变已有 failover 合同。

### 4.2 统一入口

所有上游 HTTP、HTTP 200 soft error 和 SSE error 必须调用：

```go
errorclass.ClassifyWithHeaders(statusCode, responseHeaders, boundedBody)
```

禁止 capability、diagnostics 或新 middleware 退回只按状态码硬编码。必须保留响应 headers，以识别 `Retry-After`、`X-RateLimit-Scope` 等范围信息。失败的 `ChannelAttempt`、诊断尝试和基线证据都要保存 `error_level` 与有界 `error_reason`。

P0 需要扩展这个统一入口对 LLM endpoint 的 2xx 语义：在有界 body/header 检查中识别 `text/html`、WAF/登录页特征和结构化失败 envelope，并仍由 `ClassifyWithHeaders` 返回统一分类；不能由 self-healing 单独维护一套“200 HTML 算失败”的旁路规则。SSE 解码失败也要在同一个诊断结果中保留 status、headers 和 decode reason，不能把未解码的首字节当作成功。

### 4.3 决策表

| 观测 | ErrorLevel（生产） | RootCause（自愈） | 处理 |
|---|---|---|---|
| DNS、连接、TLS、超时、代理连接失败 | `channel` | `network` | 不改 Header/Body；退避并等待恢复；超过阈值告警 |
| 429 + `Retry-After > 60s` 或 scope=global/IP | `channel` | `rate_limit` / `capacity` | 记录恢复时间，暂停该渠道；不跑变体矩阵 |
| 429 + 短 Retry-After、Key/account scope | `key` | `rate_limit` | 按既有策略冷却 Key；不改配置 |
| 500/502/503/504，正文含 overloaded、load limit、服务繁忙 | `channel` | `capacity` | 延长 sentinel；不把容量错误误判为协议漂移 |
| 401、Key quota、1308/1310、Gemini `RESOURCE_EXHAUSTED` | `key` | `auth` / `rate_limit` | 轮换 Key；不产生配置补丁 |
| 403 明确 invalid key/permission | `key` | `auth` | 轮换 Key；要求用户更换凭据 |
| 403 明确 current client、IP blocked、probe restriction | `channel` | `waf_or_client_fingerprint` | 进入低频诊断；比较 UA/指纹头；不自动换 Key |
| 403 返回 Cloudflare/WAF HTML | `channel` | `waf_or_client_fingerprint` | 按 HTML/WAF 证据诊断；不把 200/403 当业务响应 |
| 400/422 `invalid_request`、unknown field、missing required field | `client` | `protocol_drift` | 与成功基线做 shape diff；满足阈值才运行矩阵 |
| 404 endpoint/path not found | `channel` | `endpoint` | 先检查 Base URL/路径；第一版不自动改 Base URL |
| 404 明确 model_not_found | `client` | `auth` / `model_access` | 标记模型权限，不跑 Header 变体 |
| HTTP 200 + JSON error envelope / `response.failed` | 按 `ClassifyWithHeaders` | 由结构化错误决定 | 不能当成功；沿用 1308/1310、RESOURCE_EXHAUSTED 等既有兼容契约 |
| HTTP 200 + HTML/WAF/登录页 | `channel` | `waf_or_client_fingerprint` | 记录 Content-Type、HTML 指纹，触发低频诊断 |
| HTTP 200 + SSE `event:error` | 按统一分类 | `capacity` / `protocol_drift` / `auth` | 保存首个错误事件和 headers；不能只看首字节 |
| SSE 首字节非 UTF-8、gzip 未解压、Content-Encoding 异常 | `channel` | `decode` | 检查 HTTP client 解压和 Content-Type；不直接改业务 Body |
| 其他未知 4xx/5xx | 现有保守分类 | `unknown` | 只在重复且有差异证据时升级诊断 |

### 4.4 诊断触发条件

同时满足以下条件才自动进入协议/指纹诊断：

1. 同一 channel、model、endpoint scope 在窗口内连续失败达到阈值。
2. 失败分类不是明确的 Key quota、全局限流、模型不存在或网络暂态。
3. 存在最近成功基线，或存在人工导入的黄金样本；没有样本时只能执行最小 preview/live，不生成高置信度补丁。
4. 诊断预算、队列和渠道冷却条件允许。

## 5. 探测与诊断变体矩阵

### 5.1 变体生成原则

矩阵不是任意组合爆炸。每一次 live 尝试必须有：

- `variant_id`、父基线、唯一差异、模型、端点、Key ID、开始/结束时间；
- 请求的脱敏 shape、响应 status/headers、bounded body hash/分类；
- 最大尝试数（P1 建议每 session 8，硬上限 16）；
- 单次 timeout（默认 30 秒）、session deadline（默认 5 分钟）；
- 成功停止条件：HTTP/协议有效成功且与目标能力一致；失败早停条件：明确 auth、全局限流、容量持续错误。

推荐按信息增益从小到大执行：

1. 原配置 + 当前模型 + 最小 Body。
2. 原配置 + 已知成功 Body shape。
3. 仅替换 User-Agent。
4. 仅替换单个客户端指纹 Header（`originator`、Codex beta、Anthropic beta 等）。
5. 增加/删除 `stream`、`store`、`reasoning`、`include` 等协议字段。
6. 替换模型后缀（例如 `[1m]`）或模型映射；只作为 evidence，不直接自动写入模型权限。
7. 组合已验证成功的 Header 和 Body 差异。

### 5.2 AnyRouter Claude 矩阵

| 维度 | baseline | 变体 |
|---|---|---|
| URL | `/v1/messages` | 只检查规范化路径，不自动变更 Base URL |
| UA | 当前渠道 UA | `claude-cli` 与导入样本 UA |
| Headers | 当前 `anthropic-beta`、版本头 | 单独移除 beta；单独使用黄金样本 beta 集合；禁止猜认证头 |
| Body | `model`、`max_tokens`、`messages`、`stream` | 缓存字段、thinking 字段、system 结构、content block 形状逐项增加 |
| 成功信号 | JSON `content`/合法 SSE | 200 HTML、认证错误、usage quota 均为失败 |

### 5.3 AnyRouter Codex 矩阵

| 维度 | baseline | 变体 |
|---|---|---|
| URL | `/v1/responses` | 只检查规范化路径 |
| UA | `codex-tui/0.144.6` | `codex-cli`、导入 CLI 样本版本；一轮只改 UA |
| Headers | `originator`、`X-Codex-Beta-Features` 等非认证头 | 单独移除、单独替换、完整黄金集；保留 Content-Type/Accept 由共享 builder 生成 |
| Body | 完整 Responses shape | `input` 字符串 vs 数组、`instructions`、`stream`、`store`、`reasoning`、`include`、工具/上下文字段逐项变更 |
| Model | 配置模型 | `[1m]` 后缀和已知映射只生成建议，不自动改变客户端可见模型 |
| 成功信号 | JSON response 或合法 SSE 首事件 | HTTP 200 WAF HTML、500 load limit、gzip/UTF-8 异常均为失败 |

### 5.4 补丁归因

只有当“只改变一个维度”的变体成功，且原 baseline 在同一 session 失败，才将该维度标为候选补丁。组合变体只能说明组合可行，不能把所有差异都归因于某一个 Header。

建议的置信度：

```text
high   = 同一 Key、同一端点、同一模型，baseline 连续失败，单字段变体成功，重复验证成功
medium = 组合变体成功，或只有一次成功，且容量/限流信号不稳定
low    = 没有有效 baseline、只有导入样本、或上游返回不稳定 HTML/500
```

低于 `high` 的结果只能生成预览，不能显示为“已修复”；管理员仍可以明确确认应用，但 UI 必须显示证据和不确定性。

## 6. 配置补丁、应用与回滚

### 6.1 补丁允许范围

P1 允许生成的字段：

- `user_agent`；
- `custom_header` / `header_rules` 中的非认证头；
- `param_override`；
- `json_rewrite_rules`；
- `raw_passthrough`（仅在明确的黄金样本证据下建议）。

默认禁止自动生成：

- `ChannelKey`、Authorization、x-api-key、Cookie、代理凭据；
- `base_urls`、渠道代理地址、TLS 选项；
- `model` 权限、分组权重、failover 策略；
- 受保护 Header 的 set/remove/append。

### 6.2 补丁格式

```json
{
  "channel_id": 12,
  "diagnostic_session_id": "diag_01...",
  "expected_scope_fingerprint": "old-scope-hash",
  "base_channel_version": 7,
  "confidence": "high",
  "changes": [
    {
      "field": "user_agent",
      "before": "codex_cli_rs/0.144.5",
      "after": "codex-tui/0.144.6",
      "evidence_variant_ids": ["v3", "v7"]
    },
    {
      "field": "header_rules",
      "before": [],
      "after": [{"action": "set", "header_key": "originator", "header_value": "codex-tui"}],
      "evidence_variant_ids": ["v4"]
    }
  ],
  "verification": {
    "model": "gpt-5.6-sol",
    "endpoint": "endpoint-fingerprint",
    "max_live_requests": 1
  }
}
```

前端显示为人类可读 diff，但服务端必须再次校验字段 allowlist、Header 注入规则、JSON rewrite pointer、大小、数量、受保护字段和 scope fingerprint。不能信任前端传来的 `before`。

### 6.3 应用流程

```text
GET 诊断结果
  -> 服务端重新读取当前 channel 和 patch
  -> 比较 expected_scope_fingerprint/version
  -> 事务保存旧配置快照和 patch 审计记录
  -> 调用带 fingerprint/version 校验的 ChannelUpdate
  -> 通过共享 channel runtime service 刷新 channel cache、runtime URL、balancer/circuit、proxy client
  -> 失效旧 capability evidence
  -> 仅对指定 channel/key/model 执行一次验证请求
  -> ClassifyWithHeaders + 协议成功检查
       | 成功：patch=applied，保留新基线
       | 失败：事务/补偿回滚旧配置，patch=rolled_back，告警
```

应用必须是乐观并发安全的：如果用户在诊断期间手动修改了渠道，`expected_scope_fingerprint` 或 version 不匹配则返回冲突，不覆盖手动修改。回滚保存完整的旧配置 JSON，但其中的 Key 仍只能来自数据库事务快照，不能写入普通日志或前端响应。

### 6.4 一键不等于无确认

“一键修复”在交互上是一次确认，在后端仍包含：

1. 查看 diff 和证据；
2. 确认成本、目标渠道和目标模型；
3. 应用事务；
4. 自动验证；
5. 成功/失败通知和可回滚记录。

不得设计成“一次 400 自动修改生产渠道”，也不得在 live 诊断找到任意 2xx 后立即改变配置。

## 7. API、内部链路与 UI

### 7.1 建议 API

路由名称可按项目风格调整，但必须保持完整链路：route 注册 -> inbound transformer -> internal request type -> outbound transformer -> tests -> build。

| 方法 | 路径 | 作用 |
|---|---|---|
| `GET` | `/api/v1/channel/:id/self-healing` | 返回开关、当前状态、最近基线、最后错误和最近 patch 摘要 |
| `POST` | `/api/v1/channel/:id/self-healing/preview` | 不访问上游，生成当前最终请求 shape |
| `POST` | `/api/v1/channel/:id/self-healing/diagnostics` | 创建 `preview`、`live` 或 `compare` 会话；校验预算和管理员权限 |
| `GET` | `/api/v1/channel/:id/self-healing/diagnostics/:diagnostic_id` | 返回会话状态、变体结果、分类、脱敏 diff |
| `POST` | `/api/v1/channel/:id/self-healing/diagnostics/:diagnostic_id/apply` | 应用指定 patch，执行验证或返回并发冲突 |
| `POST` | `/api/v1/channel/:id/self-healing/rollback/:patch_id` | 管理员明确回滚已应用 patch |
| `GET` | `/api/v1/channel/:id/self-healing/baselines` | 查看有限数量的基线元数据和请求 shape |

所有端点继承现有 `Auth()` 和 `RequireJSON()` 约束；live、apply、rollback、compare 需要更高管理员权限并写审计日志。前端只获得脱敏结果，不能通过诊断 API 读取 Key 原文。

### 7.2 内部请求类型

建议在 `internal/selfheal` 定义：

- `SentinelRequest`：channel/model/endpoint/scope；
- `DiagnosticSession`：模式、状态、deadline、budget、trigger、actor；
- `VariantRequest`：baseline + bounded mutations；
- `DiagnosticAttempt`：artifact、响应摘要、统一分类、耗时；
- `ChannelPatch`：expected fingerprint、allowlisted changes、evidence IDs、confidence；
- `ApplyPatchRequest`：patch ID、expected version、confirm token；
- `RollbackSnapshot`：旧配置引用和状态。

### 7.3 UI 交互

渠道详情页增加一个“自愈状态”区域，不把它做成营销式页面：

- 状态：`healthy`、`watching`、`capacity`、`suspected_drift`、`diagnosing`、`patch_ready`、`rolled_back`；
- 最近成功基线的时间、模型、协议、端点 fingerprint；
- 最近失败的 ErrorLevel、RootCause、上游 status、Retry-After、有限错误原因；
- “请求预览”显示最终 Header/Body shape；
- “开始诊断”选择模式、模型、Key、最大请求数和是否保存脱敏 Body；
- 结果页显示每个 variant 的单一差异、状态、分类、证据强度；
- patch diff 使用字段级对比，并明确“容量错误不会生成配置补丁”；
- “应用补丁”二次确认目标渠道、成本和预期 fingerprint；
- 应用后显示验证结果、自动回滚原因和审计记录入口。

## 8. 数据、安全与运维约束

### 8.1 存储策略

建议新增以下表；是否拆成独立表由实现阶段决定，但不得把诊断 Body 塞进 `relay_logs`：

| 表 | 内容 | 生命周期 |
|---|---|---|
| `channel_baselines` | 成功请求 shape、哈希、scope 和成功证据 | 每 scope 保留 3 份，过期清理 |
| `diagnostic_sessions` | 模式、触发原因、状态、预算、actor、TTL | 默认 7 天元数据；Body shape 可更短 |
| `diagnostic_attempts` | variant 差异、请求/响应摘要、分类、耗时 | 默认 24 小时，最多 16 次/session |
| `channel_patches` | patch diff、置信度、状态、version、验证结果 | 长期保留审计元数据，不保留完整 secret |
| `channel_rollback_snapshots` | 应用前的 allowlisted 渠道字段快照，不含 ChannelKey | 按保留策略限权，成功 patch 至少保留最近 10 个 |

诊断证据不进入备份、普通 RelayLog、渠道统计或导出接口；备份和删除渠道时要明确清理/保留策略。

### 8.2 安全边界

- 所有 live/compare/apply/rollback 操作要求管理员认证和审计。
- 会话默认 TTL 5 分钟；到期立即删除内存中的完整捕获内容。
- Body 默认只保存 shape，完整 Body 需要显式开启且仍有 64 KiB 上限；建议 P1 不支持完整用户 Body 保存。
- 回滚快照只保存可补丁字段（UA、非认证 Header/rewrite、ParamOverride、raw 标记）和版本，不复制渠道 Key；自定义 Header 值仍按敏感配置权限保护。
- 全部认证 Header、Cookie、代理凭据强制脱敏；脱敏发生在持久化前，不依赖前端处理。
- 响应 Body 只保留有界摘要和 hash；HTML/WAF 页面不得完整回传到 UI。
- 诊断请求最多固定一个渠道、一个端点、一个 Key；不能通过 API 让用户把任意 URL 当 probe 目标，防止 SSRF。
- 继承现有 URL、Header、JSON pointer 验证；禁止 CRLF 注入、受保护 Header 改写和任意 JSON 路径扩张。
- 诊断失败不能更新 Key 冷却、熔断、健康评分、failover 权重和生产统计。

### 8.3 监控指标

至少增加：

- `self_healing_sentinel_total{channel,model,result}`；
- `self_healing_diagnostic_total{channel,root_cause,result}`；
- `self_healing_variant_total{variant_dimension,result}`；
- `self_healing_patch_total{action,status,confidence}`；
- `self_healing_probe_inflight`、队列深度、预算消耗、session age；
- `self_healing_baseline_age_seconds`；
- `self_healing_auto_rollback_total`。

日志字段使用 channel ID/name、model、endpoint fingerprint、variant ID、ErrorLevel、RootCause、bounded reason；不得包含 Key、Authorization、用户 prompt 或完整请求 Body。

## 9. 分阶段实施计划

### P0：事实采集与统一内核

**目标**：在不改变生产选路、不新增后台 live 流量的前提下，记录最终请求形状、成功基线和统一错误证据。

#### 文件级变更点

| 文件/目录 | 变更 |
|---|---|
| `internal/model/channel_baseline.go` | 新增 `ChannelBaseline`、脱敏 `RequestArtifact`、scope/version/生命周期字段和校验 |
| `internal/model/diagnostic.go` | 新增 `RootCause`、诊断状态、variant 结果、patch 状态枚举；与 `ErrorLevel` 分离 |
| `internal/db/db.go` | 将新模型加入 `AutoMigrate` |
| `internal/db/migrate/016.go`、测试 | 如当前迁移编号允许，新增索引、清理策略和旧库升级测试；不要把诊断表放进普通备份 |
| `internal/relay/request_artifact.go` | 实现 Header/Body shape、hash、敏感字段脱敏和有界读取 |
| `internal/relay/pipeline_middleware.go` | 在最终 outbound request 点生成 artifact；在成功/合法 SSE 后通知基线服务；失败不能影响 relay |
| `internal/relay/attempt_options.go` | 暴露有界 rewrite 命中摘要，确保 raw/param/header/json 顺序与正常 relay 一致 |
| `internal/relay/errorclass/classifier.go` | 提供统一的分类适配接口/有界 reason；保留 1308/1310、Gemini `RESOURCE_EXHAUSTED` 和 headers 语义 |
| `internal/capability/prober.go` | 改为传递 response headers，并调用统一分类；标准模板仍保持现有能力探测语义 |
| `internal/op/baseline.go`、`internal/op/diagnostic.go` | 新增按 scope 查询、写入、TTL 清理和版本读取操作 |
| `internal/conf/config.go` | 增加默认关闭的采集开关、TTL、body 上限和保留数，配置校验覆盖上下界 |
| `internal/model/backup.go`、`internal/op/backup.go`、`internal/op/backup_merge.go` | 明确排除基线/诊断完整 Body；必要时只导出脱敏元数据 |

#### P0 验收标准

1. AnyRouter Claude/Codex 正常 relay 成功时能产生脱敏基线，Key、Authorization、Cookie 和用户内容不落库。
2. 对 HTTP 200 HTML、HTTP 200 JSON error、SSE error、429 Retry-After、1308/1310、Gemini quota 的测试全部经过统一 `ClassifyWithHeaders`。
3. 开关关闭时没有新的上游请求；捕获或基线 DB 写失败不会让用户请求失败。
4. 既有 relay、capability、errorclass、handler 测试通过。
5. 执行 `go test ./internal/relay/... ./internal/capability/... ./internal/op/... ./internal/db/...` 和 `go build ./...`。

### P1：后台哨兵、变体诊断与补丁闭环

**目标**：实现用户真正需要的“发现故障 -> 自动诊断 -> 一键应用 -> 验证/回滚”，但保持默认关闭和人工确认。

#### 文件级变更点

| 文件/目录 | 变更 |
|---|---|
| `internal/selfheal/` | 新增 sentinel scheduler、diagnostic orchestrator、variant generator、isolated client、patch generator、apply/rollback service |
| `internal/selfheal/worker.go` | 复用现有 job queue 的边界，增加独立队列名、预算、deadline、去重和指数退避 |
| `internal/selfheal/request_builder.go` | 调用共享最终请求构建器；禁止复制 `attempt_options.go` 的 rewrite 逻辑 |
| `internal/selfheal/classifier.go` | 将 `ClassifyWithHeaders` 输出映射到 `RootCause`，保留 ErrorLevel 原值 |
| `internal/selfheal/repository.go` | session、attempt、patch、snapshot 的事务写入和有界清理 |
| `internal/op/channel.go` | 增加带 expected scope/version 的安全 patch 入口，内部仍调用 `ChannelUpdate` 的事务和缓存刷新 |
| `internal/channelstate/runtime.go` | 抽出当前 handler 中的 `invalidateChannelRuntimeState`，让普通更新、self-healing apply、enable/delete 共享 balancer/runtime URL/proxy client 失效副作用 |
| `cmd/start.go` | 注册 self-healing worker；配置变更、停止和启动失败处理与 capability worker 一致 |
| `internal/server/handlers/channel.go` 或新 handler 文件 | 注册状态、preview、diagnostic、result、apply、rollback routes；每个 route 完整串通 inbound/internal/outbound/test |
| `internal/server/middleware/audit.go` | 增加诊断创建、查看、应用、回滚、导出事件；审计详情只存脱敏 ID 和摘要 |
| `web/src/api/endpoints/channel.ts` | 增加 API 类型、query/mutation 和错误码映射 |
| `web/src/components/modules/channel/` | 自愈状态、请求预览、诊断进度、variant diff、patch 确认和回滚 UI |
| `web/src/i18n/`、前端测试 | 增加状态文案、容量/协议漂移区分、敏感字段隐藏测试 |
| `internal/conf/config.go`、设置 UI | 增加全局开关、周期、阈值、并发、每 session 最大尝试数、成本预算；默认关闭 |

#### P1 验收标准

1. 连续容量错误只显示 `capacity`/`channel`，不生成 Header/Body 补丁，且不增加变体请求。
2. 重复 400 schema、403 WAF 或 200 HTML 能创建诊断 session，并按矩阵上限执行；同一 channel 同时只有一个 session。
3. Preview 不访问上游；live 只访问指定 channel/key/endpoint，最多一次或受 session 硬上限限制，不触发 failover、熔断、健康统计和普通 RelayLog。
4. 变体结果保留 headers、status、bounded reason、shape diff 和统一 ErrorLevel/RootCause；认证信息永不返回。
5. 补丁只允许非认证 UA/header/rewrite 字段，基于 fingerprint/version 并发保护；应用后必须经过共享 runtime/circuit/proxy 失效路径再验证，失败恢复旧配置并记录 `rolled_back`。
6. UI 可以查看证据、确认 patch、看到成功/失败/回滚原因；所有敏感操作有审计记录。
7. 运行 `go test ./internal/selfheal ./internal/server/... ./internal/relay/... ./internal/capability/...`、前端相关测试和 `go build ./...`。

### P2：黄金样本、CLI 对比和供应商适配

**目标**：在 P1 闭环稳定后，支持 AnyRouter 等对客户端指纹和复杂 Body 高度敏感的渠道，减少盲目试探并提高补丁归因质量。

#### 文件级变更点

| 文件/目录 | 变更 |
|---|---|
| `internal/selfheal/importer/` | 解析脱敏 cURL、HAR、JSON；限制 URL、Header、Body 大小和协议，拒绝认证 secret 持久化 |
| `internal/selfheal/diff/` | Header、JSON shape、数组类型和字段语义 diff；输出单字段差异和置信度 |
| `internal/selfheal/profile/` | Claude Messages、Codex Responses、OpenAI Chat、Gemini 的变体 profile；profile 只描述允许的公开字段 |
| `internal/selfheal/variant.go` | 以黄金样本差异为优先级生成矩阵，避免全组合爆炸 |
| `internal/selfheal/confidence.go` | 重复验证、容量抖动、同一 Key/endpoint 约束下的置信度计算 |
| `web/src/components/modules/channel/GoldenSampleDiff.tsx` | 样本导入、字段 diff、应用建议预览；不显示敏感值 |
| `docs/`、运维脚本 | 记录 AnyRouter Claude/Codex 的脱敏回归样本和手工复现步骤，不提交真实凭据 |

#### P2 验收标准

1. 导入脱敏 Codex CLI/Claude CLI 请求后，能显示 UA、originator、beta 头、`input`/`instructions`/缓存/工具字段差异。
2. 只有单维度变体成功且重复验证通过时才给出 high confidence；组合成功显示 medium/low。
3. 导入的 URL 不能造成 SSRF；样本中认证值被拒绝或立即脱敏。
4. 已应用 patch 可以从 UI 查看审计、验证证据和回滚历史。
5. 执行完整 Go 测试、前端测试、`go build ./...`，并用本地 mock upstream 覆盖 AnyRouter 的 403、WAF 200、500 load、429、schema 400 和成功 SSE 场景。

## 10. 测试策略与验收矩阵

### 10.1 单元测试

- `RequestArtifact`：认证头脱敏、Header 排序、Body shape、64 KiB 限制、用户内容不保存。
- baseline：scope fingerprint 变化失效、同 scope 保留 N 份、成功条件和 SSE 首事件。
- classifier：HTTP、soft 2xx、SSE、headers、1308/1310、Gemini quota、WAF HTML、load error。
- variant generator：单字段优先、最大尝试数、去重、早停、禁止保护字段。
- patch validator：JSON pointer、CRLF、受保护 Header、大小/数量、expected version 冲突。
- rollback：验证失败恢复旧配置；并发手动更新时拒绝覆盖。

### 10.2 集成测试

使用本地 `httptest` upstream，不连接真实 AnyRouter：

| 场景 | 预期 |
|---|---|
| 成功 OpenAI Responses/Anthropic Messages | 写入脱敏 baseline |
| 500 load limit | RootCause=capacity，不生成 patch |
| 403 client restricted/WAF | 进入诊断，变体可区分 UA/指纹差异 |
| 200 HTML | 不记成功，保存 HTML 指纹和 bounded reason |
| 200 SSE error | ClassifyWithHeaders 生效，保存错误事件 |
| 429 Retry-After global/key | channel/key 级别与冷却语义正确 |
| 400 schema drift | 触发矩阵并生成 shape diff |
| patch apply success | DB、cache、runtime state、capability evidence 同步失效 |
| patch verification failure | 自动 rollback，用户可看到失败原因 |
| channel deleted/updated during session | queued job 丢弃或返回 version conflict，不写旧配置 |

### 10.3 API/UI 测试

新增每个 route 的 handler 测试，至少覆盖：认证、严格 JSON、channel 不存在、worker disabled、budget exceeded、敏感字段脱敏、apply conflict、rollback authorization。前端测试覆盖状态映射、diff 显示、错误/过期/回滚状态和不渲染 Key 原文。

### 10.4 全量门槛

每个涉及 Go 代码的阶段必须至少执行：

```bash
go test ./internal/relay/...
go test ./internal/server/...
go test ./internal/capability/...
go test ./internal/selfheal/...
go build ./...
```

涉及 API route 时，还要人工核对并在 PR 描述中列出：

```text
route 注册 -> inbound transformer -> internal request type
-> outbound transformer -> handler/integration tests -> go build ./...
```

文档阶段本身不声称代码已修复；实施完成后必须以实际命令输出为准。

## 11. 风险、取舍与回退策略

| 风险 | 影响 | 缓解 |
|---|---|---|
| 探测产生费用或触发 WAF | 上游封禁、成本增加 | 默认关闭；每渠道 opt-in；RPM/并发/成本/session 上限；容量错误不跑矩阵 |
| 把容量故障误判协议变化 | 错误改配置，导致更大故障 | ErrorLevel/RootCause 分离；必须重复失败和基线差异；容量状态早停 |
| 诊断请求与生产请求不一致 | 误生成补丁 | 在最终 outbound middleware 捕获；共享 builder；preview 显示 rewrite 命中 |
| 上游随机成功 | 低置信度补丁 | 同一 Key/端点重复验证；组合变体只能 medium/low；应用后自动验证和回滚 |
| WAF 依赖 IP/时间/会话 | 变体无法稳定复现 | 记录 headers、Content-Type、HTML 指纹和时间；UI 明确“无法归因”，不强行修复 |
| raw passthrough 泄露用户内容 | 隐私风险 | 只保留 shape/hash；完整 Body 默认关闭；诊断不进备份和 RelayLog |
| 配置补丁覆盖手动修改 | 生产回退或冲突 | expected fingerprint/version；事务和补偿回滚；冲突返回 409 |
| 自动新增组件影响启动 | 部署失败 | worker 默认关闭；启动注册失败明确报错；配置版本校验和独立队列 |
| 向历史备份写入新敏感数据 | 凭据泄露 | 明确排除 baseline/diagnostic Body；增加备份回归测试和清理任务 |
| classifier 逻辑分叉 | failover 行为回归 | 所有来源复用 `ClassifyWithHeaders`；覆盖 HTTP/soft 2xx/SSE/headers 测试 |

无法确认协议变更时的安全回退是：停止诊断、保持原配置、继续使用现有健康/failover 逻辑，并把证据呈现给管理员。系统宁可报告“需要人工确认”，也不能把一次不稳定的 2xx 当成正确修复。

## 12. 决策记录

1. **采用三层递进，而不是直接做自动改配置。** 基线是事实来源，哨兵负责发现，诊断负责归因，应用必须人工确认。
2. **诊断与能力探测分离但共享执行边界。** 能力探测回答“是否支持 text/stream/tool/vision”；自愈诊断回答“为什么最终请求被当前上游拒绝”。
3. **最终请求捕获点选择 outbound middleware。** 这样 `ParamOverride`、JSON rewrite、HeaderRules、UA、raw passthrough 的顺序与真实 relay 一致。
4. **统一错误分类优先于新增根因枚举。** RootCause 只能建立在带 headers 的统一 ErrorLevel 证据之上，不能另写只看状态码的分类器。
5. **一键修复只是一键确认，不是无人值守自动改生产。** 这是避免上游抖动造成配置震荡的必要边界。
6. **P2 才引入 cURL/HAR/CLI 黄金样本。** P0/P1 先让系统仅靠内部真实成功基线工作，降低解析器和 SSRF 的首版风险。

## 13. 实施开始前的立即行动项

1. 冻结并脱敏 AnyRouter Claude/Codex 的成功请求样本，只保留 Header 名称/公开指纹和 Body shape；不要把当前 token 写入仓库。
2. 确认 P0 的 DB 迁移编号、备份排除策略和基线保留期限。
3. 先实现 `RequestArtifact` 和 classifier 适配，再开始 sentinel；没有可信的最终请求形状，不应开始自动补丁。
4. 用本地 mock upstream 编写 200 HTML、500 load、429、403 WAF、400 schema、SSE error 的回归测试。
5. P0 全量测试和 `go build ./...` 通过后，才在临时实例开启 self-healing；生产先只开启 preview 和成功基线采集。
