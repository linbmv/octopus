# 🚀 Octopus 健康系统快速启动指南

## 📋 前置要求

- Go 1.26+
- 运行中的 Octopus 实例

> 健康状态接口是运维/调试接口，不作为 Web 主导航功能暴露。完整接口说明见 [health-operations-api.md](./health-operations-api.md)。

## ⚡ 快速启动（3 步）

### 1. 确认健康系统已集成

健康系统已自动集成到 relay 中，默认启用。

验证集成：
```bash
# 编译项目
go build ./...

# 验证健康包
go test ./internal/relay/health -v
```

### 2. 启动服务

健康系统会自动启动，无需额外配置。

```bash
# 启动 Octopus
./octopus serve

# 验证健康系统运行
curl http://localhost:8080/api/v1/health/status
```

### 3. 查看健康状态

```bash
# 查看所有渠道健康状态
curl http://localhost:8080/api/v1/health/status | jq

# 查看特定渠道
curl "http://localhost:8080/api/v1/health/status/channel?channel_id=1" | jq

# 查看 Prometheus 指标
curl http://localhost:8080/api/v1/health/metrics | grep octopus_health
```

---

## 🎯 完整部署流程

### Step 1: 配置健康系统

复制配置模板：
```bash
cp config/health.example.yaml config/health.yaml
```

编辑配置：
```yaml
health:
  enabled: true
  timeout:
    min: 5s
    max: 40s
    min_samples: 30
  persistence:
    enabled: true
    data_dir: ./data/health
    interval: 5m
```

### Step 2: 启动服务

```bash
# 创建数据目录
mkdir -p ./data/health

# 启动服务
./octopus serve -config config/health.yaml

# 检查日志
tail -f logs/octopus.log | grep health
```

预期输出：
```
2026-07-04T16:00:00+08:00  INFO  health/persistence.go:88  Health persistence started: interval=5m, dir=./data/health
```

### Step 3: 验证运行

#### 3.1 验证 API
```bash
# 健康状态
curl http://localhost:8080/api/v1/health/status

# 应该返回
{
  "count": 0,
  "states": []
}
```

#### 3.2 触发事件
```bash
# 发送测试请求到 relay
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

#### 3.3 查看健康状态
```bash
# 再次查询，应该有数据了
curl http://localhost:8080/api/v1/health/status | jq

# 应该返回
{
  "count": 1,
  "states": [
    {
      "channel_id": 1,
      "key_id": 100,
      "model": "gpt-4",
      "score": 1.0,
      "adaptive_timeout_ms": 20000,
      "stats": {
        "total_count": 1,
        "success_count": 1,
        "success_rate": 1.0,
        ...
      }
    }
  ]
}
```

### Step 4: 配置监控（可选）

#### 4.1 Prometheus

在 `prometheus.yml` 中添加：
```yaml
scrape_configs:
  - job_name: 'octopus-health'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: /metrics
```

#### 4.2 Grafana 仪表盘

导入仪表盘（待创建）：
- 健康度评分趋势
- 自适应超时分布
- 误杀率监控
- P95 延迟趋势

---

## 📊 监控指标

### 关键指标

#### 1. 健康度评分
```promql
# 查询所有渠道健康度
octopus_health_score

# 健康度 < 0.8 的渠道
octopus_health_score < 0.8
```

#### 2. 误杀率监控（Phase 4）
```promql
# 误杀率 = 超时数 / 成功数
rate(octopus_health_timeout_total[5m]) / rate(octopus_health_requests_success_total[5m])
```

#### 3. 自适应超时
```promql
# P95 延迟 vs 自适应超时
octopus_health_first_token_p95_ms
octopus_health_adaptive_timeout_ms
```

#### 4. CV（变异系数）
```promql
# 高抖动渠道 (CV > 0.8)
octopus_health_cv > 0.8
```

---

## 🛠️ 常见操作

### 查看健康状态

```bash
# 全部状态
curl http://localhost:8080/api/v1/health/status

# 特定渠道
curl "http://localhost:8080/api/v1/health/status/channel?channel_id=1"

# 特定 (channel, key, model)
curl "http://localhost:8080/api/v1/health/status/specific?channel_id=1&key_id=100&model=gpt-4"
```

### 管理操作

```bash
# 禁用健康系统
curl -X POST http://localhost:8080/api/v1/health/disable

# 启用健康系统
curl -X POST http://localhost:8080/api/v1/health/enable

# 重置所有状态
curl -X POST http://localhost:8080/api/v1/health/reset
```

### 持久化管理

```bash
# 查看快照文件
ls -lh ./data/health/

# 快照示例
-rw-r--r--  1 user  staff   5.1K Jul  4 16:00 health_20260704_160000.json
-rw-r--r--  1 user  staff   5.2K Jul  4 16:05 health_20260704_160500.json

# 手动触发保存（通过重启）
kill -TERM $(pgrep octopus)  # 优雅关闭会自动保存
./octopus serve              # 启动时自动加载
```

---

## 🔧 调试与故障排查

### 问题 1: 健康状态为空

**症状**: `curl /api/v1/health/status` 返回 `count: 0`

**原因**: 没有请求通过 relay

**解决**:
1. 检查 relay 是否正常工作
2. 发送测试请求
3. 查看日志确认事件记录

### 问题 2: 自适应超时不生效

**症状**: 超时始终是 20s（冷启动超时）

**原因**: 样本数不足

**解决**:
1. 检查 `min_samples` 配置（默认 30）
2. 发送更多请求累积样本
3. 查询统计：
   ```bash
   curl /api/v1/health/status | jq '.states[0].stats.total_count'
   ```

### 问题 3: 误杀率过高

**症状**: Replay 显示误杀率 > 2%

**原因**: 参数配置不合理

**解决**:
1. 增加 `min_samples` 到 50
2. 调整 `timeout_sample_weight` 到 0.7
3. 增加 `max_timeout` 到 60s
4. 重新运行 Replay 验证

### 问题 4: 持久化失败

**症状**: 日志显示 "Failed to persist health states"

**原因**: 数据目录权限不足

**解决**:
```bash
# 检查目录权限
ls -ld ./data/health

# 修复权限
chmod 755 ./data/health
```

---

## 📈 性能调优

### 1. 内存优化

健康系统内存占用：
- 每个健康状态：~10KB
- 1000 个状态：~10MB

如果内存受限：
```yaml
health:
  window_size: 30        # 降低到 30（默认 50）
  estimator:
    compression: 50      # 降低到 50（默认 100）
```

### 2. 持久化优化

如果磁盘 I/O 受限：
```yaml
persistence:
  interval: 10m          # 增加到 10 分钟（默认 5 分钟）
  max_snapshots: 5       # 减少到 5（默认 10）
```

### 3. 指标更新优化

如果 Prometheus 抓取频繁：
```yaml
metrics:
  update_interval: 60s   # 增加到 60 秒（默认 30 秒）
```

---

## ✅ 验收检查清单

### 部署验收
- [ ] 服务启动成功
- [ ] 健康系统已启用
- [ ] API 可访问
- [ ] Prometheus 指标可见

### 功能验收
- [ ] 健康状态正常记录
- [ ] 自适应超时生效
- [ ] 持久化正常工作
- [ ] 重启后状态恢复

### 性能验收
- [ ] OnEvent < 20μs
- [ ] 内存占用 < 50MB
- [ ] CPU 占用 < 2%

---

## 🚀 下一步

### Phase 4: Replay 验证
```bash
# 收集一天的日志
./scripts/collect-relay-logs.sh --start 2026-07-04 --output relay.log

# 运行 Replay
./health-replay -log relay.log -output ./replay-output

# 检查报告
cat ./replay-output/report.md
```

### Phase 5: 灰度上线
```yaml
health:
  rollout:
    enabled: true
    percentage: 1        # 从 1% 开始
    observation_mode: true
```

### Phase 6: 生产部署
- [ ] 运维文档完善
- [ ] 告警规则配置
- [ ] 灰度流程完整
- [ ] 回滚预案准备

---

## 📞 支持与反馈

- 文档: `.claude/plan/PHASE_0-6_SUMMARY.md`
- 配置: `config/health.example.yaml`
- 测试: `go test ./internal/relay/health -v`

**状态**: ✅ Phase 0-3 已完成 | 📋 Phase 4-6 待实施
