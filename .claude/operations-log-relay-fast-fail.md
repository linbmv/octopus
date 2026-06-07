# Octopus 中继快失败与健康粘性优化 - 操作日志

**任务代号**: relay-fast-fail-healthy-sticky  
**开始时间**: 2026-06-07  
**负责人**: Claude Code  
**目标**: 中转站场景下的快失败与健康粘性增强（第一、第二阶段）

---

## 改动范围

### 第一阶段：可观测性增强（不改变转发行为）
1. 增加 attempt 级阶段日志
2. 增强 active request 状态
3. 记录首 token 用时到 attempt
4. relay complete 日志增加 attempts 摘要

### 第二阶段：健康粘性（默认关闭，通过配置启用）
1. 新增全局设置 `sticky_healthy_first_token_timeout`
2. 慢成功不刷新 sticky 逻辑
3. 默认值为 0（关闭该特性，保持现有行为）

---

## 第一阶段：可观测性增强

### 1. 增强 ChannelAttempt 结构

**文件**: `internal/model/log.go`

**改动**:
```go
type ChannelAttempt struct {
    ChannelID        int           `json:"channel_id"`
    ChannelKeyID     int           `json:"channel_key_id,omitempty"`
    ChannelKeyRemark string        `json:"channel_key_remark,omitempty"`
    ChannelName      string        `json:"channel_name"`
    ModelName        string        `json:"model_name"`
    AttemptNum       int           `json:"attempt_num"`
    Status           AttemptStatus `json:"status"`
    Duration         int           `json:"duration"` // 已有字段
    Sticky           bool          `json:"sticky,omitempty"`
    Msg              string        `json:"msg,omitempty"`
    
    // 新增字段（第一阶段）
    FirstTokenTime   int           `json:"first_token_time,omitempty"` // 首token用时(ms)，仅成功且流式时有值
}
```

**理由**: 记录首 token 用时到 attempt，便于后续判断"健康成功"。

---

### 2. 增强活跃请求状态

**文件**: `internal/relay/active_requests.go`

**改动**:
```go
type ActiveRequestState string

const (
    StateForwarding        ActiveRequestState = "forwarding"         // 正在转发（已有）
    StateWaitingUpstream   ActiveRequestState = "waiting_upstream"   // 新增：等待上游响应
    StateStreaming         ActiveRequestState = "streaming"          // 流式传输中（已有）
    StateDone              ActiveRequestState = "done"               // 已完成（已有）
)
```

**调用点**:
- `forward()` 开始时：`UpdateState(trackingID, StateWaitingUpstream)`
- 首 token 到达时：保持现有 `UpdateState(trackingID, StateStreaming)`

**理由**: 更细的状态有助于诊断慢请求卡在哪里。

---

### 3. 增加 attempt 阶段日志

**文件**: `internal/relay/relay.go`

**新增日志点**:

1. **attempt 开始**（`relayAttempt.run()` 开头）:
```go
log.Infof("attempt %d/%d start: channel=%s(%d), key=%d, sticky=%t, model=%s",
    ra.iter.Index()+1, ra.iter.Len(),
    ra.channel.Name, ra.channel.ID, ra.usedKey.ID, ra.iter.IsSticky(), ra.metrics.ActualModel)
```

2. **attempt 成功**（成功分支）:
```go
log.Infof("attempt %d/%d success: channel=%s(%d), key=%d, duration=%dms, first_token=%dms",
    ra.iter.Index()+1, ra.iter.Len(),
    ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
    span.Duration().Milliseconds(), firstTokenMs)
```

3. **attempt 失败**（失败分支）:
```go
log.Warnf("attempt %d/%d failed: channel=%s(%d), key=%d, duration=%dms, error=%v",
    ra.iter.Index()+1, ra.iter.Len(),
    ra.channel.Name, ra.channel.ID, ra.usedKey.ID,
    span.Duration().Milliseconds(), fwdErr)
```

**理由**: 每个 attempt 的开始/结束/耗时/首token清晰可见，便于排查慢请求。

---

### 4. relay complete 日志增加 attempts 摘要

**文件**: `internal/relay/metrics.go`

**改动**: 在 `Save()` 方法的 `log.Infof` 后增加一段 attempts 摘要：

```go
log.Infof("relay complete: ...")
// 新增：
if len(attempts) > 0 {
    log.Infof("  attempts summary:")
    for _, a := range attempts {
        log.Infof("    #%d: channel=%s(%d), key=%d, status=%s, duration=%dms, sticky=%t, msg=%s",
            a.AttemptNum, a.ChannelName, a.ChannelID, a.ChannelKeyID, a.Status, a.Duration, a.Sticky, a.Msg)
    }
}
```

**理由**: 一次 relay 的所有 attempt 决策一目了然。

---

## 第二阶段：健康粘性

### 1. 新增全局设置

**文件**: `internal/model/setting.go`

**改动**:
```go
const (
    ...
    SettingKeyStickyHealthyFirstTokenTimeout SettingKey = "sticky_healthy_first_token_timeout" // 粘性健康首token阈值（秒），0=关闭
)

func DefaultSettings() []Setting {
    ...
    {Key: SettingKeyStickyHealthyFirstTokenTimeout, Value: "0"}, // 默认关闭
}

func (s *Setting) Validate() error {
    switch s.Key {
    case ..., SettingKeyStickyHealthyFirstTokenTimeout:
        _, err := strconv.Atoi(s.Value)
        ...
    }
}
```

**理由**: 默认值为 0，保持现有"只要成功就粘住"的行为；设置为 >0 后启用"慢成功不粘"。

---

### 2. 慢成功不刷新 sticky 逻辑

**文件**: `internal/relay/relay.go`

**改动位置**: `relayAttempt.run()` 成功分支，当前是：

```go
if fwdErr == nil {
    ...
    span.End(dbmodel.AttemptSuccess, "")
    ...
    balancer.RecordSuccess(...)
    balancer.SetSticky(...)  // <-- 这里需要加条件判断
    return false, nil
}
```

**改为**:

```go
if fwdErr == nil {
    ...
    span.End(dbmodel.AttemptSuccess, "")
    ...
    balancer.RecordSuccess(...)
    
    // 慢成功不刷新 sticky（第二阶段）
    shouldSticky := true
    healthyTimeout, err := op.SettingGetInt(dbmodel.SettingKeyStickyHealthyFirstTokenTimeout)
    if err == nil && healthyTimeout > 0 {
        // 启用健康粘性：只有首 token 足够快才刷新 sticky
        firstTokenMs := int64(0)
        if !ra.metrics.FirstTokenTime.IsZero() {
            firstTokenMs = ra.metrics.FirstTokenTime.Sub(ra.metrics.StartTime).Milliseconds()
        }
        if firstTokenMs > int64(healthyTimeout)*1000 {
            shouldSticky = false
            log.Infof("slow success, skip sticky: channel=%s(%d), first_token=%dms, threshold=%ds",
                ra.channel.Name, ra.channel.ID, firstTokenMs, healthyTimeout)
        }
    }
    
    if shouldSticky {
        balancer.SetSticky(ra.metrics.APIKeyID, ra.metrics.RequestModel, ra.channel.ID, ra.usedKey.ID, ra.metrics.ActualModel)
    }
    
    return false, nil
}
```

**理由**: 
- 默认 `healthyTimeout = 0` 时，`shouldSticky` 始终为 `true`，保持现有行为；
- 设置 `sticky_healthy_first_token_timeout = 30` 后，首 token 超过 30 秒的成功不会刷新 sticky，避免慢中转被长期粘住。

---

### 3. 记录首 token 用时到 attempt

**文件**: `internal/relay/balancer/iterator.go`

**改动**: `AttemptSpan` 结构和 `End()` 方法：

```go
type AttemptSpan struct {
    attempt        model.ChannelAttempt
    startTime      time.Time
    iter           *Iterator
    ended          bool
    firstTokenTime *time.Time  // 新增：记录首token时间
}

// RecordFirstToken 记录首 token 时间
func (s *AttemptSpan) RecordFirstToken(t time.Time) {
    if s.firstTokenTime == nil {
        s.firstTokenTime = &t
    }
}

// End 结束尝试：设置状态，自动计算耗时，追加到 Iterator
func (s *AttemptSpan) End(status model.AttemptStatus, msg string) {
    if s.ended {
        return
    }
    s.ended = true
    s.attempt.Status = status
    s.attempt.Duration = int(time.Since(s.startTime).Milliseconds())
    s.attempt.Msg = msg
    
    // 新增：记录首 token 用时
    if s.firstTokenTime != nil {
        s.attempt.FirstTokenTime = int(s.firstTokenTime.Sub(s.startTime).Milliseconds())
    }
    
    s.iter.attempts = append(s.iter.attempts, s.attempt)
}
```

**调用点**: `relay.go` 的 `writeStream()` 中，首 token 到达时：

```go
if firstToken {
    ra.metrics.FirstTokenTime = time.Now()
    span.RecordFirstToken(time.Now())  // 新增
    firstToken = false
    ...
}
```

**理由**: 把首 token 用时记录到 attempt，日志和慢成功判断都能用到。

---

## 改动文件清单

| 文件 | 改动类型 | 说明 |
|---|---|---|
| `internal/model/setting.go` | 新增设置 | 添加 `sticky_healthy_first_token_timeout` |
| `internal/model/log.go` | 新增字段 | `ChannelAttempt.FirstTokenTime` |
| `internal/relay/active_requests.go` | 新增状态 | `StateWaitingUpstream` |
| `internal/relay/balancer/iterator.go` | 新增方法 | `AttemptSpan.RecordFirstToken()` |
| `internal/relay/relay.go` | 增强日志+逻辑 | attempt 日志、慢成功不 sticky |
| `internal/relay/metrics.go` | 增强日志 | relay complete 后输出 attempts 摘要 |

---

## 默认行为兼容性

### 第一阶段

- ✅ 只增加日志和字段，不改变转发逻辑
- ✅ 新增字段 `FirstTokenTime` 有 `omitempty`，不影响现有日志解析
- ✅ 新增状态 `StateWaitingUpstream` 不影响现有活跃请求查询

### 第二阶段

- ✅ 新设置默认值为 `0`，保持"只要成功就粘住"的现有行为
- ✅ 只有用户明确设置 `sticky_healthy_first_token_timeout > 0` 后才启用慢成功不粘
- ✅ 不影响非流式请求（首 token 时间为 0，不会触发慢成功判断）

---

## 验证标准

### 第一阶段

1. `go test ./internal/relay` 通过
2. `go test ./internal/relay/balancer` 通过
3. `go build ./...` 成功
4. 日志能看到 attempt 开始/结束/耗时/首token
5. 活跃请求接口能返回 `waiting_upstream` 状态

### 第二阶段

1. 设置默认保持 `0`，成功请求仍然刷新 sticky
2. 设置为 `30` 后，首 token > 30s 的成功不刷新 sticky
3. 日志能看到 `slow success, skip sticky` 消息
4. 熔断和 failover 逻辑不受影响

---

## 后续建议

1. 前端可选增加：展示 attempt 的首 token 用时
2. 监控可选增加：统计慢成功不粘的次数
3. 文档补充：说明 `sticky_healthy_first_token_timeout` 的推荐值（30-45秒）
4. 缓存自定义代理 HTTP client（第三阶段，单独任务）

---

## 风险与缓解

| 风险 | 缓解措施 |
|---|---|
| 新日志过多影响性能 | 只在 attempt 级别记录，频率不高 |
| 慢成功不粘误伤正常慢模型 | 默认关闭，用户可按场景调整阈值 |
| 首 token 时间计算不准 | 复用现有 `FirstTokenTime` 字段，已验证 |
| 非流式请求没有首 token | 判断时检查 `IsZero()`，不会误触发 |

---

## 实施检查点

- [x] 上下文核对完成
- [ ] 代码改动实施
- [ ] 本地编译测试
- [ ] 补充单元测试
- [ ] 生成验证报告