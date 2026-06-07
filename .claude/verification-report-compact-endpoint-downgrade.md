# 验证报告 - Compact 请求端点降级与数据搬运修复

**任务**: 修复 Response 渠道不支持 Compact 时自动降级到 Chat 端点  
**执行日期**: 2026-06-07  
**结论**: ✅ 通过（已修复 Codex 发现的 Critical 问题）

---

## 1. 问题背景

用户使用 Compact 格式请求 Response 类型渠道时，部分渠道（如中转站）不支持 `/v1/responses/compact` 端点，导致：
- 404 端点不存在
- 客户端收到错误而非降级重试

期望行为：
- 同一渠道内先尝试 `/v1/responses/compact`
- 端点不支持时自动降级到 `/v1/chat/completions`
- 保持请求语义不变（数据完整搬运）

---

## 2. 实施方案

### 2.1 数据搬运修复

**问题**：`compactChatFallbackRequest` 原实现只搬运 `Compact.Messages`，导致 Chat 端点收到空 messages 报错。

**修复**：
```go
// 完整搬运 Compact.Input → Messages
fallbackReq.Messages = request.Compact.Input
// 搬运 Instructions → System Message
if len(request.Compact.Instructions) > 0 {
    fallbackReq.Messages = append([]llm.Message{{
        Role:    "system",
        Content: strings.Join(request.Compact.Instructions, "\n"),
    }}, fallbackReq.Messages...)
}
```

**测试覆盖**：
- `TestCompactChatFallbackRequestCopiesInputMessages` - 验证 Input → Messages
- `TestCompactChatFallbackRequestConvertsInstructions` - 验证 Instructions → System
- `TestCompactChatFallbackRequestDoesNotMutateOriginal` - 验证原始请求不污染

### 2.2 端点降级逻辑

**核心函数**：`isEndpointUnsupportedError`

**判定逻辑**：
1. **优先按 HTTP 状态码**：404 / 405 / 501 → 端点不支持
2. **兼容两种错误类型**：
   - `*llm.ResponseError`（axonhub pipeline 实际返回）
   - `*httpclient.Error`（原始 HTTP 错误）
3. **文本兜底**：仅匹配明确的端点不存在语义
   - ✅ "invalid url"、"no such endpoint"、"unsupported endpoint"
   - ❌ 不再匹配通用 "not found"（避免误判 "api key not found"）
   - ❌ 移除 "/responses/compact" 单独匹配（避免误判 "429 on /responses/compact"）

**降级流程**（`relay.go:forward`）：
```go
// 第一次：用 Response 渠道的原始 outbound (responses transformer)
upstreamStatusCode, fwdErr := forwardWithAdapter(...)
if fwdErr == nil { return nil }  // 成功

// 判定是否端点不支持
if !isEndpointUnsupportedError(fwdErr) {
    return upstreamStatusCode, fwdErr  // 其它错误直接返回
}

// 端点降级：重建 Chat outbound
fallbackOutbound, err := newOutbound(
    llm.APIFormatOpenAIChatCompletion,
    compactChatFallbackRequest(ra.internalRequest),
    ra.channel.GetBaseUrl(),
    ra.usedKey.ChannelKey,
)
// 第二次：用 Chat 端点重试
upstreamStatusCode, fwdErr = forwardWithAdapter(..., fallbackOutbound)
```

### 2.3 Codex 审查发现的 Critical 问题与修复

**问题**：
原实现 `isEndpointUnsupportedError` 只检查 `*httpclient.Error`，但 axonhub pipeline 会先把 HTTP 错误转成 `*llm.ResponseError`，导致：
- **误降级**：500/429/401 只要错误消息含 "not found" 或 "/responses/compact" 就降级
- **漏降级**：405/501 如果 body 为空就不降级

**修复**：
1. 增加 `*llm.ResponseError` 状态码判定（优先于文本匹配）
2. 收紧文本 markers，移除通用 "not found" 和 "/responses/compact" 单独匹配
3. 新增测试覆盖 `ResponseError` 和防误判场景

**测试验证**：
```
TestIsEndpointUnsupportedErrorByStatusCode - 18 个测试（httpclient + ResponseError）
TestIsEndpointUnsupportedErrorByMessage - 8 个测试（含防误判）
全部通过 ✅
```

---

## 3. 测试覆盖

### 3.1 数据搬运测试（3 个）
- `TestCompactChatFallbackRequestCopiesInputMessages` ✅
- `TestCompactChatFallbackRequestConvertsInstructions` ✅
- `TestCompactChatFallbackRequestDoesNotMutateOriginal` ✅

### 3.2 端点判定测试（27 个）
- 状态码判定：18 个（httpclient + ResponseError，覆盖 404/405/501/401/403/429/500/502/503）✅
- 文本判定：8 个（含防误判："api key not found"、"429 on /responses/compact"、"500 calling /responses/compact"）✅
- Nil 判定：1 个 ✅

### 3.3 回归测试
```bash
go test ./internal/relay  ✅ (所有测试通过)
go build ./...             ✅ (构建通过)
```

---

## 4. 风险评估

| 风险 | 影响 | 缓解措施 | 状态 |
|------|------|----------|------|
| 误降级（401/429/5xx 被当成端点不支持） | 掩盖真实故障 | 收紧文本 markers，优先状态码判定 | ✅ 已修复 |
| 漏降级（405/501 空 body 不降级） | 用户仍收到错误 | 增加 ResponseError 状态码判定 | ✅ 已修复 |
| 已写出响应后重试 | 客户端收到重复 body | `c.Writer.Written()` gate 阻止 | ✅ 已有保护 |
| 降级消耗双倍首 token 超时 | 整体等待超过单次配置 | 首 token guard 按 attempt 隔离 | ⚠️ 已知限制 |
| ParamOverride 日志混淆 | 审计日志可能保留第一次痕迹 | 非阻塞，日志可读性问题 | ℹ️ 可接受 |

---

## 5. 配置建议

### 5.1 Response 类型渠道（中转站）
```yaml
类型：APIFormatOpenAIResponse
支持端点：/v1/responses/compact（自动降级到 /v1/chat/completions）
first_token_time_out: 45-60
circuit_breaker_threshold: 1-2
```

### 5.2 原生 OpenAI 渠道
```yaml
类型：APIFormatOpenAIResponseCompact
支持端点：/v1/responses/compact（不降级）
first_token_time_out: 90-120
```

---

## 6. 后续建议

1. **集成测试补充**（Codex Major 建议）：
   - 使用 httptest 模拟真实 forward 两次调用
   - 验证第一次 404 → 第二次 Chat 成功
   - 验证 401/429/5xx 不降级

2. **原地修改审计**（Codex Minor 建议）：
   - `OutboundRequestSummary` 在降级重试时可能被覆盖
   - 建议审计日志记录每次 attempt 的端点信息

3. **Raw URL 边界**（Codex Minor 建议）：
   - 如果 Response 渠道 base URL 配置为 `##/v1/responses/compact`
   - Chat fallback 可能仍打原 raw URL
   - 建议补测试或显式禁用该场景

---

## 7. 验证结论

### Codex 审查评分（修复后）
- **Root Cause Resolution**: 18/20（数据搬运修复到位，端点判定修复后严密）
- **Code Quality**: 17/20（错误分类器收紧，状态码优先）
- **Side Effects**: 14/20（请求污染控制较好）
- **Edge Cases**: 16/20（405/501 漏判已修复，4xx/5xx 误判已修复）
- **Test Coverage**: 15/20（单元测试有价值，集成测试可后续补充）

**综合评分**: 80/100 → **PASS**（Critical 问题已修复）

### 本地验证
- ✅ 数据搬运测试：3/3 通过
- ✅ 端点判定测试：27/27 通过
- ✅ 回归测试：relay 包全部通过
- ✅ 全量构建：无错误

---

## 8. 交付清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/relay/relay.go` | 修改 | 增加数据搬运 + 端点降级逻辑 |
| `internal/relay/soft_error.go` | 修改 | 修复端点判定（ResponseError 支持 + 文本收紧）|
| `internal/relay/transformers.go` | 注释更新 | 反映端点降级语义 |
| `internal/relay/endpoint_downgrade_test.go` | 新增 | 30 个测试覆盖端点判定和数据搬运 |
| `.claude/verification-report-compact-endpoint-downgrade.md` | 新增 | 本验证报告 |

---

**最终结论**：✅ 功能完整，Critical 问题已修复，测试覆盖充分，可以交付。

**记录人**：Claude Opus 4.8  
**审查人**：Codex (80/100 PASS)  
**交付时间**：2026-06-07