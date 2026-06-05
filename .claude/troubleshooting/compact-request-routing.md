# Compact 请求路由失败问题排查与修复

## 问题现象

**时间**：2026-06-05

**症状**：
- Claude Code 使用 `gpt-5.5-openai-compact` 模型请求失败
- 错误信息：`No available channel for model gpt-5.5-openai-compact under group default (distributor)`
- octopus 日志显示已匹配到 `gpt-5.5` 分组，模型名也改写成了 `gpt-5.5`
- 中转站（muyuan.do、wzw.pp.ua、anyrouter.top）却报错说找不到 `gpt-5.5-openai-compact`

## 排查过程

### 1. 初步假设（❌ 错误）

怀疑 octopus 模型名改写没生效，导致中转站收到 `gpt-5.5-openai-compact`。

**验证方法**：
- 检查分组后缀回退逻辑（`internal/op/group.go:48-56`）✅ 正常
- 检查成员模型名改写（`internal/relay/relay.go:167`）✅ 正常
- 检查 RawPassthrough 影响（`internal/relay/relay.go:419-468`）✅ compact 请求不触发

### 2. 添加调试日志

在 `relay.go:640` 添加 `outbound raw request` 日志，打印实际发出的请求体。

**关键发现**：
```
outbound raw request to Linuxdo_WONG: 
  url=https://wzw.pp.ua/v1/responses/compact    ← 问题在这里！
  body={"model":"gpt-5.5",...}                  ← 模型名是对的
```

**实锤**：octopus 转发的模型名确实是 `gpt-5.5`（正确），但**端点是 `/v1/responses/compact`**（错误）。

### 3. 根本原因

**中转站不支持 `/v1/responses/compact` 端点！**

验证：
```bash
# 测试 1：标准端点 + gpt-5.5
curl https://muyuan.do/v1/chat/completions -d '{"model":"gpt-5.5",...}'
→ ✅ 200 成功

# 测试 2：compact 端点 + gpt-5.5
curl https://muyuan.do/v1/responses/compact -d '{"model":"gpt-5.5",...}'
→ ❌ 503/400 端点不存在或不支持
```

中转站（OpenAI 兼容 API）只支持标准 OpenAI 端点：
- `/v1/chat/completions` ✅
- `/v1/embeddings` ✅
- `/v1/images/generations` ✅

不支持 OpenAI 内部端点：
- `/v1/responses` ❌
- `/v1/responses/compact` ❌

### 4. 为什么会发 `/v1/responses/compact`？

**请求链路**：
```
Claude Code → octopus /v1/responses/compact
    ↓
octopus 解析：APIFormatOpenAIResponseCompact
    ↓
newOutbound(OpenAIChatCompletion, RequestTypeCompact, ...)
    ↓
transformers.go:78-86 选择 responses.NewOutboundTransformer
    ↓
发往 /v1/responses/compact ❌
```

**问题代码**（修复前）：
```go
case llm.RequestTypeCompact:
    switch channelType {
    case llm.APIFormatOpenAIChatCompletion,    // ← OpenAI Chat 渠道
         llm.APIFormatOpenAIResponse,
         llm.APIFormatOpenAIResponseCompact:
        return responses.NewOutboundTransformer(baseURL, key)  // ← 都用 responses transformer
```

**逻辑错误**：无论渠道类型，compact 请求都用 responses transformer，导致发往 `/v1/responses/compact`。

## 修复方案

**Commit**: `dadddb8` (2026-06-05)

**修改文件**: `internal/relay/transformers.go:78-90`

**修复逻辑**：compact 请求按渠道类型选择正确的端点：

```go
case llm.RequestTypeCompact:
    switch channelType {
    case llm.APIFormatOpenAIChatCompletion:
        // OpenAI Chat 渠道：用标准 chat 端点（中转站支持）
        return openai.NewOutboundTransformer(baseURL, key)  // → /v1/chat/completions ✅
    case llm.APIFormatOpenAIResponse,
         llm.APIFormatOpenAIResponseCompact:
        // 原生支持 responses API 的渠道：用 responses 端点
        return responses.NewOutboundTransformer(baseURL, key)  // → /v1/responses/compact ✅
    default:
        return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
    }
```

**效果**：
- 客户端发 `/v1/responses/compact` 请求（带 `gpt-5.5-openai-compact` 模型名）
- octopus 后缀回退 → 匹配 `gpt-5.5` 分组
- octopus 转发时：
  - 模型名改写成 `gpt-5.5` ✅
  - 端点改成 `/v1/chat/completions` ✅
- 中转站收到标准 OpenAI 请求 → 成功 ✅

## 测试验证

### 单元测试
```bash
go test ./internal/relay -run TestNewOutboundKeepsCurrentCompactCompatibility
→ PASS (覆盖 OpenAIChatCompletion、OpenAIResponse、OpenAIResponseCompact 三种渠道)
```

### 实际日志对比

**修复前（失败）**：
```
url=https://wzw.pp.ua/v1/responses/compact body={"model":"gpt-5.5",...}
→ 503 Service Unavailable
```

**修复后（成功）**：
```
url=https://wzw.pp.ua/v1/chat/completions body={"model":"gpt-5.5",...}
→ 200 OK
```

## 配置建议

### 分组配置（简化版）

只需要按模型版本建分组（**不需要为后缀建多个分组**）：

```
分组 gpt-5.5
  └─ 匹配正则：^gpt-5\.5(?:-mini)?.*
  └─ 成员：
     - Anyrouter_codex (gpt-5.5)
     - Linuxdo_WONG (gpt-5.5)
     - muyuan_codex (gpt-5.5)
     ...（所有成员配置基础名 gpt-5.5）

分组 claude-opus-4-8
  └─ 匹配正则：.*opus.*4.*8.*
  └─ 成员：
     - Anyrouter_claude (claude-opus-4-8)
     - Linuxdo_claude (claude-opus-4-8)
     ...
```

**自动支持的变体**（后缀回退机制）：
- `gpt-5.5` → 匹配 `gpt-5.5` 分组 ✅
- `gpt-5.5-openai` → 回退到 `gpt-5.5` 分组 ✅
- `gpt-5.5-openai-compact` → 回退到 `gpt-5.5` 分组 ✅
- `gpt-5.5-thinking` → 回退到 `gpt-5.5` 分组 ✅
- `claude-opus-4-8-anthropic` → 回退到 `claude-opus-4-8` 分组 ✅

### 渠道配置

**OpenAI Chat 兼容渠道**（中转站）：
- 类型：`APIFormatOpenAIChatCompletion`
- 支持端点：`/v1/chat/completions`
- 后缀回退：✅ 生效（客户端 `gpt-5.5-openai-compact` → 转发 `gpt-5.5`）
- compact 请求：✅ 自动转成标准端点

**原生 OpenAI 官方渠道**：
- 类型：`APIFormatOpenAIResponse` 或 `APIFormatOpenAIResponseCompact`
- 支持端点：`/v1/responses/compact`
- compact 请求：✅ 保持原端点

## 相关 Commit

| Commit | 日期 | 说明 |
|--------|------|------|
| `ac6e886` | 2026-06-02 | 初次实现模型名后缀自动回退功能 |
| `5012f68` | 2026-06-02 | 允许 OpenAI Chat 渠道承接 Compact 请求（但端点仍错误）|
| `fbd346c` | 2026-06-05 | 扩展后缀回退支持更多后缀（11 种）|
| `1c054fd` | 2026-06-05 | 添加调试日志用于排查（临时）|
| `dadddb8` | 2026-06-05 | **修复 compact 请求端点路由错误**（最终修复）|

## 经验教训

1. **端点兼容性和模型名是两个独立问题**：
   - 模型名改写正确 ≠ 请求一定成功
   - 还要确保端点被上游支持

2. **中转站通常只支持标准 OpenAI 端点**：
   - `/v1/chat/completions`、`/v1/embeddings` 等
   - 不支持 OpenAI 内部端点（`/v1/responses/*`）

3. **调试方法**：
   - 分离排查：先确认模型名（body），再确认端点（URL）
   - 日志验证：打印完整的 `url + body` 才能定位问题
   - 对比测试：curl 直接请求中转站，对比 octopus 转发结果

4. **后缀回退的意义**：
   - 让用户只需配置基础模型名（`gpt-5.5`）
   - 自动支持所有 API 格式变体（`-openai`、`-compact`、`-anthropic` 等）
   - 避免为每个后缀建重复分组

## 参考资料

- OpenAI API 文档：https://platform.openai.com/docs/api-reference
- axonhub/llm transformer 设计：`github.com/looplj/axonhub/llm/transformer`
- octopus 后缀回退实现：`internal/op/group.go:86-101`
- octopus 端点路由逻辑：`internal/relay/transformers.go:42-120`

---

**记录人**：Claude Opus 4.8 (1M context)  
**记录时间**：2026-06-06  
**协作者**：@sglinhome
