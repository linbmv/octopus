# Reasoning Token 持久化与展示实施计划

## 背景

AxonHub 的 `llm.Usage` 已将 OpenAI Responses、Chat Completions、Anthropic
和 Gemini 的用量统一到以下字段：

```go
usage.CompletionTokensDetails.ReasoningTokens
```

Octopus 当前只持久化输入和输出总量，因此 Codex 等推理模型的输出构成无法在
调用日志、渠道统计和 API Key 统计中观察。本次迁移将 Reasoning Token 作为
一等统计字段贯通后端、备份、API 合同和前端。

## 范围与不变量

本次变更只增加用量观察能力，不改变请求或调度行为。

必须保持：

- `OutputToken` 是上游返回的完整 completion/output token 数。
- `ReasoningToken` 是 `OutputToken` 的子集，不是额外输出量。
- `TotalToken = InputToken + OutputToken`。
- `OutputCost` 继续基于完整 `OutputToken` 计算，不单独叠加推理成本。
- 历史记录无法可靠反推 Reasoning Token，数据库升级后保持为 `0`。

不得改变：

- 上游 URL、Header、JSON Body、User-Agent 或模型映射。
- 渠道和 Key 的选择、权重、粘性、重试、故障转移或熔断。
- 流式响应转发和客户端响应格式。
- Anyrouter Claude/Codex 当前生效的渠道配置和请求重写。

不纳入本次迁移：

- inbound-header 路由。
- JSON set-if-absent 或供应商专用 Body 改写。
- TLS 指纹模拟。
- Claude Code 提示词、环境或用户身份改写。

## 数据链路

```text
AxonHub llm.Usage
  -> relay.RecordUsage
  -> StatsMetrics / RelayLog
  -> total, daily, hourly, channel, API key aggregates
  -> SQLite / MySQL / PostgreSQL
  -> backup export and import
  -> existing stats and log APIs
  -> generated TypeScript contracts
  -> log, channel, API key and home views
```

现有 API route 不需要增加。`StatsMetrics` 和 `RelayLog` 的字段通过现有响应
自动公开；TypeScript 合同必须从 Go 模型重新生成，不得手工维护副本。

## 阶段 1：采集与核心模型

1. 为 `StatsMetrics` 增加 `reasoning_token`，默认值为 `0`。
2. 为 `RelayLog` 增加 `reasoning_tokens`，默认值为 `0`。
3. 在 `RecordUsage` 中读取 `CompletionTokensDetails.ReasoningTokens`。
4. 缺少明细或值为负数时保存 `0`。
5. 当推理量大于非负的输出总量时，将其限制到输出总量。
6. 将字段加入 `StatsMetrics.Add`、所有聚合更新及完成日志。
7. 保持输入/输出计费代码不变。

验收条件：非流式和流式标准化 Usage 均能记录推理量；异常明细不会产生负数
或大于输出总量的内部统计。

## 阶段 2：数据库与备份

1. 使用带 `not null;default:0` 的 GORM 字段，让 AutoMigrate 添加列。
2. 增加迁移 `015`，在 AutoMigrate 后断言五张统计表的
   `reasoning_token` 和日志表的 `reasoning_tokens` 已存在。
3. 验证有数据的旧 SQLite 表升级后原数据不丢失，新字段为 `0`。
4. 扩展 MySQL/PostgreSQL 数据库矩阵，检查列并往返非零值。
5. 备份校验拒绝负数 Reasoning Token。
6. 新备份必须保留字段；旧 v1/v2 JSON 缺少字段时按 Go 零值导入。
7. empty-target restore、merge、replace 均必须保留字段。

这是向后兼容的可选 JSON 字段扩展，不单独提升 `dbDumpVersion`。

## 阶段 3：API 合同与前端

1. 运行 `go run ./tools/webtypes > web/src/api/contracts.ts`。
2. 为 `StatsMetricsFormatted` 增加格式化后的 `reasoning_token`。
3. 更新 daily、hourly、total、channel 和 API Key 的全部 formatter。
4. 保持所有 `total_token` 表达式为 `input_token + output_token`。
5. 日志卡片仅在 `reasoning_tokens > 0` 时显示推理量。
6. 日志详情在输出面板中显示推理明细，不把它加到输出总数。
7. 渠道详情、API Key 统计和首页输出区域增加推理指标。
8. 更新所有现有语言的文案。
9. 第一阶段不增加第三条图表堆叠序列，避免改变现有趋势图语义。

## 阶段 4：测试与回归

后端：

- nil details、负数、正常值和大于输出总量的 Usage。
- 非流式和 `response.completed` 流式用量。
- `StatsMetrics.Add` 及并发渠道/API Key 聚合。
- 日志持久化和内容策略不影响数字元数据。
- SQLite 旧库升级、数据库保存/读取和备份兼容。
- MySQL 8 与 PostgreSQL 17 可选集成矩阵。

前端：

- 合同生成结果一致。
- formatter 包含推理量且总 Token 不重复计算。
- 日志推理量为正时显示，为零时不显示。
- TypeScript、ESLint、Vitest 和 Next.js 生产构建通过。

Anyrouter 回归：

- 在临时 Octopus 和临时捕获代理中分别调用 Claude 与 Codex。
- 对比改动前后的脱敏 URL、Header、JSON Body 和 User-Agent 指纹。
- 排除 request ID、时间戳等非确定字段。
- 确认渠道/Key 选择、权重、故障转移和熔断决策未改变。
- 不修改或重启现有生产 Octopus 实例。

## 完成门槛

```text
go test ./internal/relay
go test ./internal/op
go test ./internal/server/...
go test ./...
go build ./...
govulncheck ./...
trivy fs ... .
pnpm type-check
pnpm lint
pnpm test
pnpm build
```

同时要求生成合同无差异、工作树仅包含本任务文件，并在推送后核对本地
`HEAD` 与远端 `refs/heads/dev` 完全一致。
