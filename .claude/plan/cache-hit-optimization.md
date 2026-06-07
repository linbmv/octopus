# 实施计划：提升上游 prompt cache 命中率

生成时间：2026-06-03
任务类型：后端为主
范围：第二步「OpenAI Chat 同格式 raw passthrough」（带开关/灰度/回滚）+ sticky actual-model 校验
稳定优先：所有改动局部、可回滚、默认关闭或零行为风险，逐步验证。

## 1. 背景与已验证事实

目标是降低上游 prompt cache miss。日志现象：单请求 input_token 高达 21 万却频繁未命中缓存。

已通过 axonhub 源码取证（版本 `v0.0.0-20260602054907-23b062cf877c`）：

1. 出站 `transformer/openai/outbound.go:190` 用 `json.Marshal(oaiReq)` **重新序列化** body。
2. `httpclient/utils.go` 的 `MergeInboundRequest` 只合并 header/query，body 一律采用 outbound 重序列化结果。
3. OpenAI `Request` / `Message` 是**强类型 struct，无 catch-all 字段**，且 `Message` 没有 `cache_control` 字段。

结论：客户端原始 body 经 `unmarshal → 强类型 struct → marshal` 往返后会**字段重排、丢弃未知顶层字段、丢弃 cache_control 标记**。上游 prompt cache 依赖前缀字节完全一致，任何重排/丢字段都会导致 miss。这是命中率低的根因，第二步有真实收益。

## 2. 两项改动概述

| 改动 | 风险 | 默认状态 | 回滚方式 |
|------|------|----------|----------|
| A. sticky actual-model 校验 | 极低（纯内存，逻辑收紧） | 直接启用 | 改回单 ChannelID 匹配 |
| B. OpenAI Chat raw passthrough | 中（改出站 body 来源） | **开关默认关** | 关开关即恢复 |

A 先做（本批交付），B 后做（本计划仅设计，待 A 验证缓存观测链路后再实施）。

## 3. 改动 A：sticky actual-model 校验（最小风险，先做）

### 3.1 问题

`internal/relay/balancer/session.go` 的 `SessionEntry` 只存 `ChannelID + ChannelKeyID`，`iterator.go` sticky 命中只按 `ChannelID` 匹配。

真实风险场景：一个渠道同时服务多个 model（日志中 Linuxdo 系渠道既跑 gpt-5.5 又跑 claude-opus-4-8）。sticky 命中渠道后，若该渠道这次被选了**不同 actual model**，prompt cache 同样 miss。这与第二步同属「提升缓存命中」目标。

### 3.2 设计

会话条目增加成功时的 actual model；命中时要求 actual model 一致才复用 sticky 渠道+key，否则视为未命中走正常调度。

`SetSticky` 写入的 actual model = `ra.metrics.ActualModel`（即本次成功 attempt 的 `item.ModelName`，relay.go:164 已设置）。

### 3.3 文件改动

| 文件 | 操作 |
|------|------|
| `internal/relay/balancer/session.go` | `SessionEntry` 加 `ModelName string`；`SetSticky` 增加 `actualModel string` 参数并写入 |
| `internal/relay/balancer/iterator.go` | `NewIterator` 命中 sticky 后，比对 `sticky.ModelName == item.ModelName`，不一致则不置 sticky |
| `internal/relay/relay.go` | `SetSticky(...)` 调用增传 `ra.metrics.ActualModel` |
| `internal/relay/balancer/iterator_test.go` | 现有 `SetSticky` 调用补 actualModel 参数 |
| `internal/relay/balancer/session_test.go` | 新增：actual-model 一致命中 / 不一致不命中 |

### 3.4 关键边界

- sticky 候选要校验的是 candidates 中该 ChannelID 对应 item 的 `ModelName`。一个渠道在同一分组内可能有多个 model item（多条 GroupItem），需匹配 model 一致的那条。
- 若同渠道存在 model 一致的 item，复用其 key；否则不置 sticky。
- TTL 过期逻辑不变。

### 3.5 验收

- `go test ./internal/relay/...` 全绿。
- 新增测试覆盖：同渠道同 model 命中；同渠道不同 model 不命中；跨渠道不命中（已有）。

## 4. 改动 B：OpenAI Chat raw passthrough（带开关/灰度/回滚，后做）

### 4.1 触发条件（全部满足才走旁路）

- 入站 APIFormat == `openai/chat_completions`
- 出站 channel.Type == `openai/chat_completions`
- 非 multipart（Content-Type 为 application/json）
- 开关开启（按渠道或全局配置，默认关）

不满足任一条件 → 回退现有 axonhub 全转换路径。

### 4.2 旁路行为

- body = 客户端原始 raw body，仅用 `jsonpatch.PatchModel` 替换顶层 model（已实现，[internal/utils/jsonpatch/model.go]）。
- 仍由 octopus 控制 auth/header（复用现有 relay 中间件路径）。
- **必须保留的现有副作用**（否则静默改变行为）：
  - `ParamOverride`（relay.go applyChannelRequestOptions）
  - `CustomHeader`
  - 软错误检测（isSoftError）
  - usage 记录 / 日志 / sticky / 熔断

### 4.3 待解决的设计问题（实施前回答）

1. 开关粒度：全局 setting 还是 per-channel 字段？建议 per-channel，便于灰度单渠道。
2. raw 旁路与 axonhub pipeline 的关系：是在 forward 里分流（不进 pipeline），还是用一个 passthrough outbound transformer？后者更能复用现有中间件副作用，建议优先评估。
3. ParamOverride 与 raw body 的叠加顺序：patch model 后再叠加 ParamOverride，需保证仍是合法 JSON 且不破坏缓存前缀（ParamOverride 通常改尾部参数，影响小，但需测试）。
4. 流式 usage：raw 透传后响应仍走现有聚合逻辑，需确认 usage 记录不受影响。

### 4.4 文件改动（预估，待 4.3 定稿）

| 文件 | 操作 |
|------|------|
| `internal/model/channel.go` | 如选 per-channel 开关，加字段（默认关，AutoMigrate 加列）|
| `internal/relay/transformers.go` 或新增 passthrough transformer | raw 旁路选择逻辑 |
| `internal/relay/relay.go` | 触发条件判定 + patch model 接入 |
| 前端 channel 表单 + i18n | 如 per-channel 开关需 UI |

### 4.5 灰度与回滚

- 默认关：上线后行为与现状完全一致。
- 灰度：对单个 OpenAI Chat 渠道开启，观察缓存命中。
- 回滚：关开关即恢复 axonhub 全转换路径，无数据迁移、无不可逆变更。

## 5. 验证假设的方法（第三步作为 B 的验收线）

观测设施已就绪：
- 后端 `internal/relay/metrics.go` 按 `cached_tokens`/`write_cached_tokens` 计 cache_read/cache_write 成本。
- 前端 `web/src/lib/usage-cache-tokens.ts` 从响应解析缓存 token 并在日志展示。

验证步骤：
1. 选一个 OpenAI Chat 渠道，记录开启前一段时间的 `cached_tokens` / 命中率 / 成本基线。
2. 对该渠道开启 raw passthrough 开关。
3. 用相同/相似的多轮对话请求观察 `cached_tokens` 是否显著上升（建议判据：命中率提升 ≥30%）。
4. 同时确认无回归：响应正确、usage 正常、无新的软错误/上游兼容报错。
5. 若无明显收益或出现兼容问题 → 立即关开关回滚。

## 6. 第四步零风险复制（验证假设后）

一旦改动 B 在 OpenAI Chat 上用真实流量证明「raw passthrough 显著提升缓存命中且无回归」，扩展到 OpenAI Responses 是**复用同一套机制的零新增风险复制**：

- 复用同一个开关机制（per-channel）、同一个 `jsonpatch.PatchModel`（顶层 model patch 对 Responses 同样适用）、同一套触发条件判定（只是 APIFormat 换成 `openai/responses`）、同一套回滚方式。
- 唯一差异：触发条件的 APIFormat 匹配值，以及 Responses 特有字段（`previous_response_id`、`input` 数组）的往返失真面更大——但 raw 透传恰恰**绕过**了往返，所以 Responses 反而更受益。
- 复制清单：
  1. 触发条件增加 `入站==出站==openai/responses` 分支。
  2. 复用 PatchModel（无需改）。
  3. 复用开关字段（无需改）。
  4. 补 Responses 专项测试 + 灰度观察。
- 不要做：把 Chat 和 Responses 一次性同批接入；先用 Chat 验证假设，再复制。

## 7. 不触碰（稳定优先边界）

- 全套自研 transformer
- provider 框架重构
- key 轮询策略
- 一次支持多个协议

## 8. 交付顺序

1. 本批：改动 A（sticky actual-model 校验）+ 测试 + 本地验证。
2. 下一批：改动 B 设计定稿（回答 4.3）→ 实施 → 灰度观察（第三步验收）。
3. 再下一批：第四步 Responses 零风险复制（B 验证通过后）。
