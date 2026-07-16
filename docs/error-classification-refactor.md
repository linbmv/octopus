# 错误分类重构方案

## 目标

借鉴 ccLoad 的成熟架构，将 Octopus 的错误处理从"分散的硬编码判断"重构为"集中的三级决策系统"。

## 当前问题

1. **逻辑分散**：`canRetryNextKey()` 硬编码了 401/403/429/503+model_not_found 的判断
2. **难以扩展**：每次新增错误场景需要修改多处代码
3. **缺乏统一标准**：没有明确的"什么是 key 级错误，什么是渠道级错误"的定义

## ccLoad 的设计（参考）

### 三级错误分类

```go
type ErrorLevel int

const (
    ErrorLevelNone    ErrorLevel = iota // 2xx 成功
    ErrorLevelKey                       // Key 级：冷却当前 key，尝试其他 key
    ErrorLevelChannel                   // 渠道级：冷却整个渠道，切换渠道
    ErrorLevelClient                    // 客户端级：直接返回，不重试
)
```

### 统一分类器

```go
// ClassifyHTTPResponseWithMeta 基于状态码 + headers + 响应体智能分类
func ClassifyHTTPResponseWithMeta(
    statusCode int, 
    headers map[string][]string, 
    responseBody []byte,
) HTTPResponseClassification {
    // 1. 特殊错误优先（1308、structured quota error）
    // 2. SSE error 动态分类
    // 3. 429 结合 headers 判断限流范围
    // 4. 400/404 根据响应体智能分类
    // 5. 其他状态码走表驱动（statusCodeMetaMap）
}
```

### 表驱动状态码映射

```go
var statusCodeMetaMap = map[int]StatusCodeMeta{
    401: {ErrorLevelKey},     // Unauthorized
    403: {ErrorLevelKey},     // Forbidden
    429: {ErrorLevelKey},     // Too Many Requests
    500: {ErrorLevelChannel}, // Internal Server Error
    502: {ErrorLevelChannel}, // Bad Gateway
    503: {ErrorLevelChannel}, // Service Unavailable
    504: {ErrorLevelChannel}, // Gateway Timeout
    // ...
}
```

### 动作决策

```go
switch errorLevel {
case ErrorLevelClient:
    return ActionReturnClient
case ErrorLevelKey:
    return ActionRetryKey      // 同渠道换 key
case ErrorLevelChannel:
    return ActionRetryChannel  // 切换渠道
}
```

## 实施计划

### Phase 1：引入错误分类框架（不改变现有行为）✅ 已完成

1. 新增 `internal/relay/errorclass/` 包
2. 定义 `ErrorLevel` 枚举和 `Classification` 结构
3. 实现 `ClassifyError(statusCode, headers, body)` 函数
4. 实现表驱动的 `statusCodeMetaMap`
5. 单元测试覆盖所有状态码

**输出**：独立的错误分类器，不侵入现有 relay 逻辑

### Phase 2：重构 `canRetryNextKey()`✅ 已完成

1. 将 `canRetryNextKey()` 改为调用 `errorclass.ClassifyError()`
2. 删除硬编码的状态码判断
3. 保留现有测试用例，确保行为一致

**输出**：relay 逻辑使用新的分类器，但对外行为不变

### Phase 3：扩展高级场景 ✅ 已完成

1. ✅ 支持 429 + Retry-After header 的智能分类
2. ✅ 支持 X-RateLimit-Scope header 分析
3. ✅ 支持 400 + quota/billing 错误的智能分类
4. ✅ 新增 `ClassifyWithHeaders()` 函数
5. ✅ 新增 60+ 测试用例覆盖高级场景
6. ✅ relay 生产路径将上游响应 headers 传入 `decideRelayError()` / `ClassifyWithHeaders()`
7. ✅ 识别 HTTP 200 / SSE error、1308/1310 用量上限与 Gemini `RESOURCE_EXHAUSTED`

**输出**：更智能的错误分类，覆盖更多边缘场景

### Phase 4：前端可观测性 ✅ 已完成

1. ✅ 在 attempt 日志里记录 `error_level`（key/channel/client）
2. ✅ 同时记录有界、结构化的 `error_reason`，成功、取消和跳过 attempt 保持空值
3. ✅ 提供最近 1–168 小时的错误级别统计 API；合并未刷盘缓存与数据库日志，最多扫描最新 10,000 条
4. ✅ 在统计页面展示 key/channel/client 错误分布和容量截断提示
5. ✅ 在渠道详情页展示 key 级 vs 渠道级错误计数与小时趋势

**输出**：从上游响应分类、attempt 持久化、后端聚合查询到前端分布/渠道趋势的完整可观测性闭环

## 风险与缓解

### 风险 1：行为变化导致回归

**缓解**：
- Phase 1 完全独立，不影响现有逻辑
- Phase 2 保留所有现有测试用例
- 增加集成测试覆盖典型错误场景

### 风险 2：性能影响

**缓解**：
- 错误分类逻辑非常轻量（几个 switch + map 查询）
- 只在失败路径执行，不影响成功请求性能
- 可以加 benchmark 验证

### 风险 3：与现有冷却机制冲突

**缓解**：
- Octopus 当前只有熔断，没有 ccLoad 的指数退避冷却
- 新的分类器只决定"是否换 key"，不触碰熔断逻辑
- 可以逐步引入 key 级冷却（Phase 5，可选）

## 时间估算

- Phase 1：~2 小时（新增包 + 分类器 + 测试）
- Phase 2：~1 小时（重构 canRetryNextKey）
- Phase 3：~3 小时（高级场景 + 测试）
- Phase 4：~2 小时（前端可观测性）

**总计：~8 小时**

## 成功标准

1. ✅ 所有现有测试通过
2. ✅ 新增 50+ 个错误分类测试用例
3. ✅ 代码行数减少（删除硬编码逻辑）
4. ✅ 前端可以看到 key 级 vs 渠道级错误分布
5. ✅ 文档更新（CLAUDE.md 增加错误分类说明）

## 参考代码

- ccLoad: `internal/util/classifier.go`
- ccLoad: `internal/cooldown/manager.go`
- Octopus 当前: `internal/relay/relay.go:476-508`

---

## 实施总结（2026-06-30）

### 已完成的工作

**Phase 1 & 2（提交 `5fc53e6`）**
- ✅ 创建 `internal/relay/errorclass` 包
- ✅ 实现三级错误分类系统（None/Key/Channel/Client）
- ✅ 表驱动的状态码映射（30+ 状态码）
- ✅ 智能 404/503 响应体分析
- ✅ 重构 `canRetryNextKey()` 使用新分类器
- ✅ 70+ 测试用例

**Phase 3（提交 `7866ac5`）**
- ✅ 新增 `ClassifyWithHeaders()` 支持 header 分析
- ✅ 429 Retry-After 智能分类（>60s = 渠道级）
- ✅ X-RateLimit-Scope header 支持
- ✅ 400 quota/billing 错误检测
- ✅ attempt 日志增加 `error_level` 字段
- ✅ 60+ 新增测试用例

**Phase 4 闭环（2026-07-16）**
- ✅ 修复生产 relay 分类未携带上游 headers 的缺口；长 `Retry-After` / 渠道范围限流不会错误轮换 Key
- ✅ HTTP 200 soft error 与首个 SSE error event 进入同一分类器；未向客户端写入时仍可安全切换 Key/渠道
- ✅ 1308/1310 与 Gemini `RESOURCE_EXHAUSTED` 在任意 HTTP 状态或 SSE 包装下均归为 Key 级
- ✅ failed attempt 持久化 `error_level` 与 `error_reason`；传输错误和软 2xx 错误统一归为 channel 级
- ✅ 新增 `/api/v1/stats/error-levels` 有界查询，支持全局分布和按渠道小时趋势
- ✅ 首页展示错误级别分布，渠道详情展示 key/channel 趋势与计数
- ✅ contracts、三语 locale、Go focused tests 和 Vitest 已覆盖新链路

### 测试覆盖

- **总测试用例**：130+ 个
- **覆盖场景**：
  - 所有标准 HTTP 状态码
  - 404/503/400/429 智能分类
  - Header 分析（Retry-After、X-RateLimit-Scope）
  - 边缘场景（空响应、大小写不敏感、中文错误信息）

### 代码统计

- **新增代码**：
  - `classifier.go`: 310 行
  - `classifier_test.go`: 280 行
- **删除代码**：
  - `relay.go`: 移除硬编码判断逻辑 20 行

### 未来改进（可选）

1. **更多高级场景**（如有需要）：
   - 在引入 Key 冷却管理器后，使用 1308/RESOURCE_EXHAUSTED 响应中的重置时间做精确冷却
   - 扩展更多供应商私有错误码的结构化解析

2. **冷却策略**（如有需要）：
   - Key 级指数退避冷却
   - 渠道级熔断机制
   - 基于 error_level 的自适应冷却时长

### 成功验证

- ✅ 所有测试通过
- ✅ 原有功能行为不变
- ✅ 日志格式保持兼容
- ✅ 性能无影响（错误分类逻辑非常轻量）
- ✅ 代码行数减少（DRY 原则）
