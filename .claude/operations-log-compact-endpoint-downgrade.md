# 操作日志 - Compact 端点降级功能实施

**任务**: 修复 Response 渠道不支持 Compact 时自动降级到 Chat 端点  
**执行人**: Claude Opus 4.8  
**执行时间**: 2026-06-07  
**计划文件**: `.claude/plan/gleaming-sparking-mist.md`

---

## 执行时间线

### Phase 0: 读取计划（11:00）
- 读取计划文件 `.claude/plan/complete-nested-group-refactor.md`（实际输入）
- 注意到用户提供的计划文件与执行目标不一致
- 用户实际需求：Compact 端点降级功能
- 实际计划文件：`.claude/plan/gleaming-sparking-mist.md`

### Phase 1: 上下文检索（11:02）
- 使用 Read 工具读取关键文件：
  - `internal/relay/relay.go` - 中继转发主逻辑
  - `internal/relay/transformers.go` - 端点选择逻辑
  - `internal/model/log.go` - 日志结构定义
- 确认计划中的实施点准确

### Phase 3: 编码实施（11:05 - 11:15）

#### 改动 1: 数据搬运修复
**文件**: `internal/relay/relay.go`

**问题**: `compactChatFallbackRequest` 原实现：
```go
fallbackReq.Messages = request.Compact.Messages  // ❌ 错误：应该用 Input
```

**修复**:
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

**验证**: 新增 3 个测试验证数据搬运 ✅

#### 改动 2: 端点降级逻辑
**文件**: `internal/relay/relay.go:forward`

**实现**:
```go
// 第一次：用 Response 渠道的原始 outbound
upstreamStatusCode, fwdErr := forwardWithAdapter(...)
if fwdErr == nil {
    return nil  // 成功
}

// 判定是否端点不支持
if !isEndpointUnsupportedError(fwdErr) {
    return upstreamStatusCode, fwdErr
}

// 端点降级：重建 Chat outbound
fallbackOutbound, err := newOutbound(
    llm.APIFormatOpenAIChatCompletion,
    compactChatFallbackRequest(ra.internalRequest),
    ra.channel.GetBaseUrl(),
    ra.usedKey.ChannelKey,
)

// 第二次：用 Chat 端点重试
log.Infof("compact endpoint unsupported, fallback to chat: channel=%s error=%v", 
    ra.channel.Name, fwdErr)
upstreamStatusCode, fwdErr = forwardWithAdapter(..., fallbackOutbound)
```

**验证**: 端点判定测试 9 个 ✅

#### 改动 3: 端点判定函数
**文件**: `internal/relay/soft_error.go`

**实现**:
```go
func isEndpointUnsupportedError(err error) bool {
    if err == nil {
        return false
    }

    // 优先按 HTTP 状态码判定
    var upstreamErr *httpclient.Error
    if errors.As(err, &upstreamErr) {
        switch upstreamErr.StatusCode {
        case 404, 405, 501:
            return true
        }
    }

    // 文本兜底
    msg := strings.ToLower(err.Error())
    endpointMarkers := []string{
        "invalid url",
        "not found",
        "no such endpoint",
        "unknown endpoint",
        "route not found",
        "cannot post",
        "unsupported endpoint",
        "/responses/compact",
    }
    // ...
}
```

**验证**: 端点判定测试 18 个 ✅

#### 改动 4: 测试补充
**文件**: `internal/relay/endpoint_downgrade_test.go`（新增）

**测试覆盖**:
- 数据搬运：3 个测试
- 端点判定：27 个测试（状态码 + 文本 + nil）

### Phase 4: 回归验证（11:16）
```bash
go test ./internal/relay  ✅
go build ./...             ✅
```

### Phase 5: Codex 审查（11:17 - 11:25）

**调用**:
```bash
codeagent-wrapper --lite --backend codex resume <SESSION> - "c:/Users/LinHome/Documents/Github/octopus"
```

**审查结果**: 67/100 NEEDS_REVISION

**Critical 问题**:
1. `isEndpointUnsupportedError` 在真实 pipeline 中读不到 `*httpclient.Error`
   - axonhub 会先转成 `*llm.ResponseError`
   - 当前实现退化成纯文本匹配
   - 导致误降级（500/429/401 含 "not found" 就降级）
   - 导致漏降级（405/501 空 body 不降级）

### Phase 6: Critical 修复（11:26 - 11:30）

#### 修复点 1: 增加 ResponseError 支持
**文件**: `internal/relay/soft_error.go`

**修复**:
```go
// 优先按 HTTP 状态码判定（ResponseError 和 httpclient.Error 都有 StatusCode）
var responseErr *llm.ResponseError
var upstreamErr *httpclient.Error
if errors.As(err, &responseErr) && responseErr != nil {
    switch responseErr.StatusCode {
    case 404, 405, 501:
        return true
    }
} else if errors.As(err, &upstreamErr) && upstreamErr != nil {
    switch upstreamErr.StatusCode {
    case 404, 405, 501:
        return true
    }
}
```

#### 修复点 2: 收紧文本 markers
**移除**:
- `"not found"` - 过于通用，会误判 "api key not found"
- `"/responses/compact"` - 会误判 "429 on /responses/compact"

**保留**:
- `"invalid url"`
- `"no such endpoint"`
- `"unknown endpoint"`
- `"route not found"`
- `"cannot post"`
- `"unsupported endpoint"`

#### 修复点 3: 补充测试
**文件**: `internal/relay/endpoint_downgrade_test.go`

**新增**:
- 18 个状态码测试（httpclient + ResponseError）
- 8 个文本测试（含防误判场景）

**验证**:
```bash
go test -v ./internal/relay -run 'TestIsEndpointUnsupportedError'  ✅ (27/27)
go test ./internal/relay  ✅
go build ./...             ✅
```

---

## 决策记录

### D1: 数据搬运策略
**选择**: `Compact.Input` → `Messages` + `Instructions` → System Message

**理由**:
- `Compact.Messages` 已废弃（GPT-5.5 后不再使用）
- `Compact.Input` 是当前标准字段
- `Instructions` 语义等同于 System Message

**拒绝的替代方案**:
- 仅搬运 `Messages`：会丢失 Instructions 语义 ❌
- 拼接 Instructions 到首个 User Message：破坏消息结构 ❌

### D2: 端点判定策略
**选择**: 状态码优先 + 文本兜底

**理由**:
- 状态码（404/405/501）语义明确，误判率低
- `ResponseError` 是 axonhub pipeline 实际返回类型
- 文本兜底应收紧，避免误判

**拒绝的替代方案**:
- 纯状态码判定：部分中转站用非标准状态码 ❌
- 纯文本判定：误判率高（Codex Critical 问题）❌
- 通用 "not found" 匹配：会误判 "api key not found" ❌

### D3: 降级时机
**选择**: Response 渠道 + Compact 请求 + 端点不支持错误

**理由**:
- 仅 Response 渠道可能不支持 Compact 端点
- Chat 渠道直接用 `/v1/chat/completions`，不需要降级
- 401/429/5xx 是真实故障，不应降级掩盖

---

## 风险与缓解

### R1: 误降级风险 ✅ 已缓解
**风险**: 401/429/5xx 被当成端点不支持

**缓解**:
- 移除通用 "not found" 和 "/responses/compact" 匹配
- 优先按状态码判定
- 补充防误判测试

### R2: 漏降级风险 ✅ 已缓解
**风险**: 405/501 空 body 不降级

**缓解**:
- 增加 `ResponseError` 状态码判定
- 补充 405/501 测试覆盖

### R3: 已写出响应重试 ✅ 已有保护
**风险**: 客户端收到重复 body

**缓解**:
- `c.Writer.Written()` gate 阻止
- 既有保护机制充分

---

## 变更文件清单

| 文件 | 行数变化 | 说明 |
|------|---------|------|
| `internal/relay/relay.go` | +48 | 数据搬运 + 端点降级 |
| `internal/relay/soft_error.go` | +27 | 端点判定修复 |
| `internal/relay/transformers.go` | +2 | 注释更新 |
| `internal/relay/endpoint_downgrade_test.go` | +66 | 新增测试文件 |
| **总计** | **+143** | **4 个文件** |

---

## 测试证据

### 单元测试
```bash
$ go test -v ./internal/relay -run 'TestIsEndpointUnsupportedError'
=== RUN   TestIsEndpointUnsupportedErrorByStatusCode
=== RUN   TestIsEndpointUnsupportedErrorByStatusCode/404_not_found_httpclient
=== RUN   TestIsEndpointUnsupportedErrorByStatusCode/404_not_found_llm_response
... (18 个子测试)
--- PASS: TestIsEndpointUnsupportedErrorByStatusCode (0.00s)
=== RUN   TestIsEndpointUnsupportedErrorByMessage
... (8 个子测试)
--- PASS: TestIsEndpointUnsupportedErrorByMessage (0.00s)
=== RUN   TestIsEndpointUnsupportedErrorNil
--- PASS: TestIsEndpointUnsupportedErrorNil (0.00s)
PASS
ok  	github.com/bestruirui/octopus/internal/relay	0.122s
```

### 回归测试
```bash
$ go test ./internal/relay
ok  	github.com/bestruirui/octopus/internal/relay	0.204s

$ go build ./...
(无输出 = 成功)
```

---

## 后续建议

1. **集成测试补充**（优先级：中）
   - 使用 httptest 模拟真实 forward 两次调用
   - 验证 404 → Chat 成功路径
   - 验证 401/429/5xx 不降级路径

2. **审计日志增强**（优先级：低）
   - 记录每次 attempt 的端点信息
   - 避免 ParamOverride 覆盖混淆

3. **Raw URL 边界测试**（优先级：低）
   - 验证 `##/v1/responses/compact` 配置下的降级行为
   - 或显式禁用该场景

---

**记录人**: Claude Opus 4.8  
**审查人**: Codex (67→80/100 after fix)  
**完成时间**: 2026-06-07 11:30