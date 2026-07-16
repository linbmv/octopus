# Octopus 健康与可观测性快速指南

本文只描述当前程序中已经注册、可以实际访问的能力。

Octopus 的渠道健康评分、自适应首 token 超时和健康快照属于 relay 内部运行机制。当前版本没有 `/api/v1/health/*` 运维 API，也不读取独立的 `health.yaml`；相关参数只能通过管理端已有的设置项控制。

## 启动

环境要求与主 README 一致：Go 1.26.5+；构建前端时使用 Node.js 22.12+ 和 pnpm 9.15.9。

```bash
go build -o octopus .
./octopus start
```

若使用非默认配置文件：

```bash
./octopus start --config /path/to/config.json
```

CLI 子命令是 `start`，配置文件是主 JSON 配置；不存在 `serve` 子命令或单独的 health YAML 配置。

## 实际可用端点

| 端点 | 用途 | 成功状态 |
|---|---|---:|
| `GET /health` | 综合检查进程和数据库；数据库不可用时返回 degraded | 200 / 503 |
| `GET /ready` | Kubernetes/readiness 检查 | 200 / 503 |
| `GET /readiness` | `/ready` 的兼容别名 | 200 / 503 |
| `GET /liveness` | 进程存活检查，不探测数据库 | 200 |
| 独立监听器 `GET /metrics` | Prometheus 指标；仅在 `observability.metrics.enabled=true` 时启动 | 200 / 401 / 404 |

快速验证：

```bash
curl --fail-with-body http://127.0.0.1:8080/health
curl --fail-with-body http://127.0.0.1:8080/ready
curl --fail-with-body http://127.0.0.1:8080/liveness
curl --fail-with-body http://127.0.0.1:9090/metrics
```

`/health`、`/ready`、`/readiness` 和 `/liveness` 使用业务监听器；它们只返回粗粒度状态，不返回底层数据库错误。`/metrics` 不在业务端口注册，默认只在 `127.0.0.1:9090` 的独立监听器提供。

## 主配置示例

Metrics 使用主配置文件 `data/config.json` 中的字段：

```json
{
  "observability": {
    "metrics": {
      "enabled": true,
      "host": "127.0.0.1",
      "port": 9090,
      "bearer_token": ""
    }
  }
}
```

健康状态持久化由程序按当前内置策略管理，目录是 `data/health`。不要创建 `config/health.yaml`：当前配置 schema 不会读取它。

## Prometheus 示例

同主机 Prometheus 可直接抓取默认回环监听器：

```yaml
scrape_configs:
  - job_name: octopus
    metrics_path: /metrics
    static_configs:
      - targets: ['127.0.0.1:9090']
```

若 Prometheus 位于另一容器/主机，必须把 `observability.metrics.host` 改为可达地址，并配置至少 16 字节的 `bearer_token`；非回环且无 token 的配置会在启动前被拒绝。Prometheus 对应配置示例：

```yaml
authorization:
  type: Bearer
  credentials: '<same token>'
```

metrics 的启停、绑定地址和 token 变更需要重启；配置 watcher 会明确记录该要求。

## 渠道健康机制

渠道健康数据会在 relay 请求成功或失败后更新，并影响自适应超时及加权调度。可以在管理端设置中调整已经公开的 `smart_health_*`、`health_*` 相关选项。当前没有受支持的 HTTP 接口用于列出、重置、启用或禁用单个健康状态。

如需排查：

1. 先确认 `/health` 和 `/ready` 返回成功。
2. 检查标准输出或容器日志；项目当前不写 `logs/octopus.log`。
3. 确认请求确实经过 `/v1` 或 `/v1beta` relay 路由。
4. 使用 `go test ./internal/relay/health ./internal/relay` 验证健康算法与集成。

Replay 工具位于 `cmd/health-replay`；其实际输出报告名为 `replay-report.md`。仓库没有 `scripts/collect-relay-logs.sh`，调用方应自行提供符合工具输入格式的日志。

## 当前限制

- 没有 `/api/v1/health/status`、`reset`、`enable`、`disable` 或 `metrics` 路由。
- 没有可热加载的 health YAML schema。
- 没有 Web 健康诊断页面。
- 当前没有 metrics IP allowlist；非回环部署除 Bearer token 外仍应叠加防火墙或网络策略。

新增运维 API 时，必须同时实现 Gin 路由、JWT/运维鉴权、配置 schema、路由测试和本文档；不能只保留未注册的 handler。
