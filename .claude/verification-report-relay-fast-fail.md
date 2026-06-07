# 验证报告 - 中继快失败、健康粘性与代理客户端复用

**任务**: 中转站场景下的快失败与健康粘性增强  
**执行范围**: 第一阶段、第二阶段、第三阶段  
**日期**: 2026-06-07  
**结论**: ✅ 通过

---

## 1. 本次目标

用户主要使用质量不稳定的中转站服务，希望：

1. 通过故障转移在多个中转中找到可用渠道。
2. 找到可用渠道后通过会话粘性持续高频使用。
3. 当渠道不可用或首 token 过慢时尽快切换。
4. 偶尔使用 ChatGPT 官方 Codex，避免被中转策略误伤。

本次按稳定优先原则分三步完成：

| 阶段 | 内容 | 行为影响 |
|---|---|---|
| 第一阶段 | attempt 级可观测性增强 | 不改变默认转发行为 |
| 第二阶段 | 慢成功不刷新 sticky | 默认关闭，配置启用 |
| 第三阶段 | 自定义 channel_proxy HTTP Client 复用 | 不改变业务语义，降低连接开销 |

---

## 2. 变更文件

| 文件 | 说明 |
|---|---|
| `internal/model/log.go` | `ChannelAttempt` 新增 `first_token_time` 字段 |
| `internal/model/setting.go` | 新增 `sticky_healthy_first_token_timeout` 设置，默认 `0` |
| `internal/relay/active_requests.go` | 新增 `waiting_upstream` 活跃请求状态 |
| `internal/relay/balancer/iterator.go` | `AttemptSpan` 支持记录首 token 时间 |
| `internal/relay/type.go` | `relayAttempt` 保存 attempt span 引用 |
| `internal/relay/relay.go` | 增加 attempt 开始/成功/失败日志；实现健康 sticky 判断 |
| `internal/relay/metrics.go` | relay complete 后输出 attempts 摘要 |
| `internal/client/http.go` | 自定义代理 HTTP Client 按代理地址复用 |
| `internal/client/http_test.go` | 新增自定义代理 Client 复用测试 |

---

## 3. 关键实现说明

### 3.1 Attempt 级可观测性

新增能力：

- 每次真实上游尝试开始时输出 attempt start 日志。
- 每次成功或失败时输出 attempt 结果日志。
- relay complete 后输出 attempts summary。
- `ChannelAttempt` 持久化记录 `first_token_time`。
- 活跃请求状态新增 `waiting_upstream`，用于定位请求是否卡在等待上游响应。

### 3.2 健康粘性

新增设置：

```text
sticky_healthy_first_token_timeout
```

默认值：

```text
0
```

默认含义：

```text
关闭健康粘性检查，保持原有“成功即刷新 sticky”的行为。
```

启用后，例如设置为 `30`：

```text
当前 attempt 首 token <= 30 秒：刷新 sticky
当前 attempt 首 token > 30 秒：本次成功返回，但不刷新 sticky
非流式请求：没有首 token 语义，保持成功即刷新 sticky
```

重要修正：

- 健康粘性判断使用**当前 attempt 开始到首 token**的耗时，而不是整次 relay 的累计耗时。
- 这样避免第一通道慢失败后，第二通道实际很快却被误判为慢成功。

### 3.3 自定义代理 HTTP Client 复用

此前：

```text
GetHTTPClientCustomProxy 每次调用都新建 http.Client / Transport。
```

现在：

```text
按 trim 后的 proxyURL 缓存并复用 http.Client。
```

收益：

- 复用连接池。
- 降低代理连接和 TLS 握手成本。
- 高频调用中转站时减少连接抖动。
- 不改变渠道、代理和请求语义。

---

## 4. 本地验证

### 4.1 相关包测试

```bash
go test ./internal/client ./internal/relay/balancer ./internal/relay ./internal/server/...
```

结果：✅ 通过

### 4.2 全量 Go 测试

```bash
go test ./...
```

结果：✅ 通过

### 4.3 全量构建

```bash
go build ./...
```

结果：✅ 通过

---

## 5. 建议配置

### 5.1 中转站分组

建议从保守配置开始：

```text
mode = failover
first_token_time_out = 45
session_keep_time = 300
sticky_healthy_first_token_timeout = 30
circuit_breaker_threshold = 1 或 2
circuit_breaker_cooldown = 180
circuit_breaker_max_cooldown = 900
```

含义：

- 45 秒还没有首 token 就切换渠道。
- 30 秒内首 token 才认为是健康成功并刷新 sticky。
- 失败后快速熔断，减少坏中转反复拖慢请求。

### 5.2 官方 Codex 分组

建议和中转站分组分离：

```text
mode = failover
first_token_time_out = 90 到 120
session_keep_time = 0 或 1800
sticky_healthy_first_token_timeout = 0
circuit_breaker_threshold = 2 或 3
circuit_breaker_cooldown = 300
circuit_breaker_max_cooldown = 600
```

原因：官方 Codex 任务可能天然更慢，不宜使用中转站的激进快切策略。

---

## 6. 风险与回滚

| 风险 | 影响 | 缓解 |
|---|---|---|
| 日志量增加 | 高频请求下日志更详细 | 仅 attempt 级记录，必要时后续可改为 debug |
| 健康粘性阈值过低 | 正常慢渠道不刷新 sticky | 默认关闭；建议从 30-45 秒开始 |
| 自定义代理 client 缓存增长 | 极多不同 proxyURL 时内存占用增加 | 当前按配置型 proxyURL 使用，数量通常很小；后续可加上限 |
| 非流式请求无法按首 token 判断 | 慢成功仍可能 sticky | 当前保持兼容，建议中转站使用流式请求 |

回滚方式：

1. 将 `sticky_healthy_first_token_timeout` 设置为 `0`，立即关闭健康粘性检查。
2. 如需代码回滚，仅回退本报告”变更文件”表中的文件即可。
3. 自定义代理 Client 复用如需回滚，恢复 `GetHTTPClientCustomProxy` 每次调用 `newHTTPClientCustomProxy` 的旧逻辑即可。

---

## 7. 健康粘性配置方式

### 7.1 通过 API 配置（推荐）

```bash
# 查看当前配置
curl -H “Authorization: Bearer YOUR_TOKEN” \
  http://localhost:801/api/settings/sticky_healthy_first_token_timeout

# 设置为 30 秒（中转站建议值）
curl -X PUT \
  -H “Authorization: Bearer YOUR_TOKEN” \
  -H “Content-Type: application/json” \
  -d '{“value”:”30”}' \
  http://localhost:801/api/settings/sticky_healthy_first_token_timeout

# 关闭（恢复默认行为）
curl -X PUT \
  -H “Authorization: Bearer YOUR_TOKEN” \
  -H “Content-Type: application/json” \
  -d '{“value”:”0”}' \
  http://localhost:801/api/settings/sticky_healthy_first_token_timeout
```

### 7.2 通过 SQL 配置

```sql
-- 查看当前配置
SELECT * FROM settings WHERE key = 'sticky_healthy_first_token_timeout';

-- 设置为 30 秒
UPDATE settings 
SET value = '30' 
WHERE key = 'sticky_healthy_first_token_timeout';

-- 关闭（恢复默认）
UPDATE settings 
SET value = '0' 
WHERE key = 'sticky_healthy_first_token_timeout';
```

### 7.3 验证配置生效

重启 Octopus 后，观察成功 attempt 日志：

```text
attempt 1/2 success: channel=muyuan(23), key=40, duration=8200ms, first_token=2100ms, sticky_updated=true
```

如果 `first_token` > 阈值，会看到：

```text
slow success, skip sticky: channel=muyuan(23), first_token=35000ms, threshold=30s
attempt 1/2 success: channel=muyuan(23), key=40, duration=35500ms, first_token=35000ms, sticky_updated=false
```

**注意**：前端设置页将在下个版本补齐配置入口。

---

## 8. 后续建议

1. **前端设置页增加 `sticky_healthy_first_token_timeout` 配置项**（下个版本）。
2. 日志页展示 attempt 的 `first_token_time`。
3. 增加慢请求筛选：按 `duration`、`first_token_time`、`waiting_upstream` 查询。
4. 若仍出现非流式长时间等待，再单独设计”响应头超时”配置，不建议硬编码总超时。
5. 监控 `customProxyClients` map 大小，必要时增加 LRU 淘汰或上限。

---

## 9. 浅拷贝修复（2026-06-07 补充）

### 9.1 问题

原实现 `requestForOutboundPipeline` 只做浅拷贝：

```go
attemptRequest := *request  // 浅拷贝
```

**风险**：`llm.Request` 包含引用类型字段（如 `Stream *bool`），浅拷贝后多个 attempt 共享同一指针，如果 transformer 修改会互相污染。

### 9.2 修复

深拷贝所有引用类型字段：

```go
// 浅拷贝值类型字段
attemptRequest := *request

// 深拷贝引用类型字段
if request.Stream != nil {
	streamCopy := *request.Stream
	attemptRequest.Stream = &streamCopy
}
```

### 9.3 测试覆盖

新增 3 个测试：
- `TestRequestForOutboundPipelineDeepCopiesStreamPointer`：验证 Stream 深拷贝
- `TestRequestForOutboundPipelineHandlesNilStream`：验证 nil Stream 不崩溃
- 修改副本的 `Stream` 不污染原始请求和其他副本

### 9.4 验证结果

```bash
go test ./internal/relay -run 'TestRequestForOutboundPipeline' -v  ✅
go test ./internal/relay  ✅
go build ./...  ✅
```

---

## 10. 结论

本次三阶段改动已完成并通过本地验证：

- ✅ 可观测性增强完成。
- ✅ 慢成功不刷新 sticky 已实现，默认关闭，配置启用。
- ✅ 自定义代理 HTTP Client 已按代理地址复用。
- ✅ 相关测试、全量测试和全量构建均通过。

建议部署后先使用保守配置观察 1-2 天，再根据 attempts summary 调整首 token 超时、健康 sticky 阈值和熔断阈值。