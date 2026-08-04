<div align="center">

<img src="web/public/logo.svg" alt="Octopus Logo" width="120" height="120">

### Octopus

**为个人打造的简单、美观、优雅的 LLM API 聚合与负载均衡服务**

简体中文 | [English](README.md)

</div>


## ✨ 特性

- 🔀 **多渠道聚合** - 支持接入多个 LLM 供应商渠道，统一管理
- 🔑 **多Key支持** - 单渠道支持配置多 Key
- ⚡ **智能优选** - 单渠道多端点，智能选择延迟最小的端点请求
- ⚖️ **负载均衡** - 自动分配请求，确保服务稳定高效
- 🔄 **协议互转** - 支持 OpenAI Chat / OpenAI Responses / Anthropic 三种 API 格式互相转换
- 💰 **价格同步** - 自动更新模型价格
- 🔃 **模型同步** - 自动与渠道同步可用模型列表，省心省力
- 📊 **数据统计** - 全面的请求统计、Token 消耗、费用追踪
- 🎨 **优雅界面** - 简洁美观的 Web 管理面板
- 🗄️ **多数据库支持** - 支持 SQLite、MySQL、PostgreSQL


## 🚀 快速开始

### 🐳 Docker 运行

直接运行

```bash
docker volume create octopus-data
docker run -d --name octopus --restart unless-stopped \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=192m,mode=1777 \
  --security-opt no-new-privileges:true --cap-drop ALL \
  --cpus 1.0 --memory 512m --pids-limit 256 \
  -v octopus-data:/app/data -p 8080:8080 \
  ghcr.io/linbmv/octopus:latest
```

镜像默认以 UID/GID `10001:10001` 运行，并且只允许写入 `/app/data`。推荐使用
named volume。若需 bind mount，请先执行
`install -d -m 0700 ./data && sudo chown -R 10001:10001 ./data` 完成一次性权限准备，
再将 volume 参数替换为 `-v "$(pwd)/data:/app/data"`。示例中的 CPU、内存和进程数
限制应根据实际负载调整或移除。旧版 root 镜像创建的数据也需要在升级前完成这次
所有权迁移；容器不会在每次启动时递归执行 `chown`。

**镜像通道说明**：`latest` / `latest-alpine` 与 `vX.Y.Z` 由正式 tag 发布流程产出
（带 Cosign 签名与 SBOM attestation）。`edge` / `edge-alpine` 与 `dev-<sha>` 是开发
通道镜像——dev 分支每次通过 Quality Gate 后自动构建，包含最新改动但未经签名，
适合尝鲜与回归验证，不建议生产环境使用。

或者使用 docker compose 运行

```bash
wget https://raw.githubusercontent.com/linbmv/octopus/refs/heads/dev/docker-compose.yml
docker compose up -d
```


### 📦 从 Release 下载

从 [Releases](https://github.com/linbmv/octopus/releases) 下载对应平台的二进制文件，然后运行：

```bash
./octopus start
```

### 🛠️ 源码运行

**环境要求：**
- Go 1.26.5+
- Node.js 22.12+（CI 使用 Node.js 24.x）
- pnpm 9.15.9

```bash
# 克隆项目
git clone https://github.com/linbmv/octopus.git
cd octopus
# 构建前端
cd web && pnpm install --frozen-lockfile && pnpm run build && cd ..
# 移动前端产物到 static 目录
mv web/out static/
# 启动后端服务
go run main.go start 
```

> 💡 **提示**：前端构建产物会被嵌入到 Go 二进制文件中，所以必须先构建前端再启动后端。

**开发模式**

```bash
cd web && pnpm install --frozen-lockfile && NEXT_PUBLIC_API_BASE_URL="http://127.0.0.1:8080" pnpm run dev
## 新建终端,启动后端服务
go run main.go start
## 访问前端地址
http://localhost:3000
```

### 🔐 初始管理员

空数据库首次启动时，Octopus 会创建一个管理员：

- **用户名**：`admin`
- **密码（自动化部署推荐）**：首次启动前通过 `OCTOPUS_INITIAL_ADMIN_PASSWORD` 提供 8–72 字节的有效 UTF-8 密码。该值不会写入日志，也不会创建凭据文件。
- **密码（自动回退方式）**：未提供上述变量时，Octopus 会生成 24 字符随机密码并写入 `data/initial-admin-password`；数据目录挂载在其他位置时，可用 `OCTOPUS_INITIAL_ADMIN_PASSWORD_FILE` 覆盖路径。

随机凭据文件必须是普通 `0600` 文件；程序会拒绝符号链接、非普通文件、过宽权限和无效密码内容。日志只记录文件路径，绝不记录密码。若启动在写文件后、创建数据库用户前中断，重启会安全复用该文件；程序跟踪的文件只会在强制改密成功后删除。容器部署可从宿主机挂载的数据目录读取，或执行 `docker exec octopus cat /app/data/initial-admin-password`（请按镜像调整容器内路径）。

不存在默认密码 `admin`。首次改密完成前，该账户只能访问状态和改密接口。原有 403 响应 helper 已通过 `AbortWithStatusJSON` 中止 Gin 链；该分支现额外显式调用 `c.Abort()`，仅用于防御一致性和可读性，回归测试用于锁定 handler 不执行的既有语义。使用环境变量完成首次初始化并成功改密后，应从部署配置中移除 `OCTOPUS_INITIAL_ADMIN_PASSWORD`，避免未来空库初始化时复用旧值。

### ⚠️ 部署与备份安全边界

Octopus 当前按**单应用实例**设计。调度器、粘性路由、熔断与健康状态、运行时缓存以及
请求速率/并发限制均为进程内状态。多个副本即使共享同一数据库，也可能重复执行定时
任务，并对路由、熔断和限流作出不一致判断。除非已经接入外部分布式协调、共享运行态
以及调度器 leader election，否则请只运行一个副本。

数据库导出包含渠道凭据和 Octopus 管理的 API Key；若包含 Relay 日志，还可能包含按
日志策略留存的请求/响应内容。默认导出仍为明文 JSON；提供专用密码请求头时会生成带
版本的 scrypt + AES-256-GCM 加密封装。请求头、格式、内存上限与导入方法见
[备份加密指南](docs/backup-encryption.md)。无论明文还是加密备份都应视同密钥；一旦泄露
应立即轮换其中凭据。当前备份数据版本 2 支持 dry-run 和基于 UUID 的增量
`reject`/`skip`/`replace`/`merge`；旧版数据版本 1 仍只支持恢复到空目标数据库。

当前候选版本已在 Go 1.26.5 上通过全仓 race、`go vet`、Go build/module verify、
golangci-lint 2.12.2（0 issues）、Staticcheck 2026.1、deadcode 0.48.0、
govulncheck 1.6.0、actionlint 1.7.12 和脚本语法检查。前端 frozen install、lint、
type-check、15 个 Vitest 文件/40 项测试、locale parity 和 Next.js 16.2.10 生产构建均
通过。Playwright 核心流程 1/1 通过，覆盖 bootstrap、强制改密、Cookie/CSRF、渠道、
分组、API Key、模型列表、Relay、日志和注销。

真实 SQLite、MySQL、PostgreSQL migration/CRUD/export/restore 矩阵均已通过。完整
Release 生成 8 个 Linux/Windows/macOS 压缩包，`SHA256SUMS` 严格校验通过。Alpine 和
Distroless Debian 的 arm64/amd64 镜像均通过非 root、只读 rootfs、named/bind/旧卷、
Compose 和 SIGTERM 矩阵。当前源码和四镜像的 Trivy HIGH/CRITICAL 均为 0，工作树
Gitleaks 命中为 0。

这些是本地候选版本证据，不能替代干净 tag 发布。已知外部凭据仍需吊销，完整 Git
历史中的 6 个命中已于 2026-07-17 完成正式处置（确认为上游 fork 前文档示例 key，精确 fingerprint 风险接受记录见 .gitleaksignore，全历史 fail-closed 扫描通过）；真实 tag workflow 还必须生成并独立验证
Cosign/OIDC 签名、SBOM attestation 和 provenance，之后才能生产发布。精确证据和
剩余边界见[完整审计报告](docs/project-audit-2026-07-15.md)。

**后端依赖安全：**初扫发现的 Go 1.26.4、quic-go 0.57.1、`x/net` 0.52.0 和
pgx/v5 5.6.0 可达漏洞，已通过升级到 Go 1.26.5、quic-go 0.59.1、`x/net` 0.55.0 和
pgx/v5 5.9.2 修复；同时使用 `x/crypto` 0.52.0 和 edwards25519 1.1.1。最终
govulncheck 的可达 symbol 漏洞和 imported-package 漏洞均为 0。扫描仍在 required
module `x/crypto` 层面列出其 `openpgp` package 的 GO-2026-5932，但项目既不导入该
package，也不调用漏洞代码，因此记录为不适用的模块级提示，而不是可达漏洞。

最终 `deadcode -test ./...` 也为零输出。剩余未引用的 internal 兼容层/失效 API 结果
已经逐项确认并删除；它们均无路由、接口、反射或 build tag 消费者。相关 op/helper、
handlers、health、relay 和 server race 仍全部通过。

**前端依赖安全：**pnpm 9 锁文件通过 overrides 将有漏洞的传递依赖固定为
`lodash/lodash-es 4.18.1`、`js-cookie 3.0.8`、`cosmiconfig>yaml 1.10.3`、
`mermaid 11.16.0`、`mermaid>dompurify 3.4.11`、`mermaid>uuid 11.1.1`、
`@lobehub/ui>uuid 13.0.1` 和 `next>postcss 8.5.10`；`next-intl` 精确固定为 `4.9.2`。
由于 pnpm 9/10 使用的旧 audit endpoint 在 2026 年返回 HTTP 410，本次只在 `/tmp` 复制
相同的最终 package/lock 文件，并用 pnpm 11.13.0 读取该锁文件进行审计；
`audit --prod` 以 0 退出并报告 “No known vulnerabilities found”。项目实际构建仍固定
使用 pnpm 9.15.9；此前报告的 3 个 high、23 个 moderate 和 3 个 low 均已修复。

Next.js 16.2.10 是当前固定的最新版，但自身精确固定了旧 PostCSS，因此每次升级 Next
都必须重新复核 `next>postcss` override。将来正式迁移 pnpm 11 时，应先把 overrides
迁到 `pnpm-workspace.yaml` 再生成锁文件。Recharts v2 弃用警告和 React 19 peer 警告
仍属于兼容性债务，但不是本次审计中的已知漏洞。

### 📝 配置文件

配置文件默认位于 `data/config.json`，首次启动时自动生成。

**完整配置示例：**

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8080,
    "trusted_proxies": [],
    "session_cookie_secure": "auto"
  },
  "database": {
    "type": "sqlite",
    "path": "data/data.db"
  },
  "log": {
    "level": "info",
    "format": "json"
  },
  "relay": {
    "initial_response_timeout_seconds": 120,
    "non_stream_timeout_seconds": 120,
    "stream_first_event_timeout_seconds": 120,
    "stream_idle_timeout_seconds": 120,
    "non_stream_attempt_timeout_seconds": 60,
    "stream_cold_start_first_event_timeout_seconds": 30,
    "stream_first_event_budget_seconds": 120,
    "max_json_request_bytes": 33554432,
    "max_image_request_bytes": 67108864,
    "max_non_stream_response_bytes": 67108864
  },
  "observability": {
    "metrics": {
      "enabled": true,
      "host": "127.0.0.1",
      "port": 9090,
      "bearer_token": ""
    },
    "tracing": {
      "enabled": false,
      "endpoint": "localhost:4318",
      "insecure": true,
      "sample_ratio": 0.01
    }
  }
}
```

**配置项说明：**

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `server.host` | 监听地址 | `0.0.0.0` |
| `server.port` | 服务端口 | `8080` |
| `server.trusted_proxies` | 允许提供客户端 IP 转发头的反向代理 IP/CIDR 白名单 | `[]` |
| `server.session_cookie_secure` | 管理会话 Cookie 策略：`auto` 检测直连 TLS 或可信代理 HTTPS；`always` 强制 `Secure` | `auto` |
| `database.type` | 数据库类型 | `sqlite` |
| `database.path` | 数据库连接地址 | `data/data.db` |
| `log.level` | 日志级别 | `info` |
| `log.format` | 日志编码格式（`json` 或 `console`） | `json` |
| `relay.initial_response_timeout_seconds` | 不可关闭的首响应硬上限，同时限制非流式请求跨全部重试的总时长，以及流式请求跨全部尝试等待首个语义输出的累计时长；范围 1–120 秒 | `120` |
| `relay.non_stream_timeout_seconds` | 非流式上游请求跨全部重试的配置时限（`0` 表示使用首响应硬上限，最大 86400 秒）；实际值不会超过 `initial_response_timeout_seconds` | `120` |
| `relay.stream_first_event_timeout_seconds` | 单次流式尝试从发起上游请求到首个模型语义输出的全局兜底时限（`0` 关闭该层守卫，最大 86400 秒）；`response.created` 等生命周期/控制事件不会结束守卫；分组手动值优先，其次为已有样本的自适应值，累计等待仍受首响应硬上限约束 | `120` |
| `relay.stream_idle_timeout_seconds` | 首个模型语义输出后才启动的流空闲时限；每个上游事件或原始 heartbeat 都会重置（`0` 使用首响应硬上限，配置值不会超过该硬上限） | `120` |
| `relay.non_stream_attempt_timeout_seconds` | 单次非流式尝试等待响应头的上限，仅在还有其他候选可故障转移时生效；挂死渠道会在此时限内让位，最后一个候选使用剩余的首响应总预算；该超时只切换当前请求，不写入全局 URL 冷却或熔断失败（`0` 关闭该层守卫，最大 86400 秒） | `60` |
| `relay.stream_cold_start_first_event_timeout_seconds` | 无自适应健康样本的流式尝试在仍有备选候选时等待首个语义输出的冷启动上限，用于加速故障转移收敛；有手动值或健康样本时不介入；该超时只切换当前请求，不写入全局 URL 冷却或熔断失败（`0` 关闭该层守卫，最大 86400 秒） | `30` |
| `relay.stream_first_event_budget_seconds` | 流式请求跨渠道、Key 和端点尝试等待首个语义输出的累计预算（`0` 表示使用首响应硬上限）；实际值不会超过 `initial_response_timeout_seconds` | `120` |
| `relay.max_json_request_bytes` | 解码后 JSON 请求体上限；有效范围 1 字节–1 GiB | `33554432`（32 MiB） |
| `relay.max_image_request_bytes` | 图片编辑/变体完整 multipart 请求体上限；有效范围 1 字节–1 GiB | `67108864`（64 MiB） |
| `relay.max_non_stream_response_bytes` | 解码后上游非流式响应及流式 HTTP 错误响应体上限；成功流不设总字节上限 | `67108864`（64 MiB） |
| `observability.metrics.enabled` | 是否在独立 metrics 监听器提供 Prometheus 指标 | `false` |
| `observability.metrics.host` | metrics 独立绑定地址 | `127.0.0.1` |
| `observability.metrics.port` | metrics 独立端口 | `9090` |
| `observability.metrics.bearer_token` | 可选 Bearer token（非回环绑定时必填，至少 16 字节） | 空 |
| `observability.tracing.enabled` | 是否通过 OTLP/HTTP 导出 OpenTelemetry trace | `false` |
| `observability.tracing.endpoint` | OTLP/HTTP Collector 地址 | `localhost:4318` |
| `observability.tracing.sample_ratio` | Trace 采样率（0 到 1） | `0.01` |

程序启动后会监听配置文件变化。新配置只有在完整解析并通过校验后才会原子替换当前快照；无效配置会被拒绝并继续使用旧值。`log.level`、Relay 时限/请求响应体上限和 tracing 配置会对新请求/新 attempt 即时生效；业务监听器、可信代理、会话 Cookie 策略、独立 metrics 监听器（包括启停和 token）、数据库连接及日志格式变更需要重启进程。业务端口永不提供 `/metrics`，Prometheus 应采集 `observability.metrics.host:port/metrics`。

Relay 会在解压前拒绝除空值/`identity` 以外的 `Content-Encoding`（415），防止压缩请求膨胀攻击；过大的 JSON 或图片 multipart 请求返回结构化 413。非流式响应上限作用于 Go Transport 解压后的内容。模型发现另有每页 16 MiB、跨 Key/分页累计 64 MiB 和 50,000 个唯一模型上限；价格目录响应上限为 16 MiB。首事件与空闲保护只是阶段时限，不是流式总时限：持续产生 heartbeat 的有效流可以无限期运行，已识别终止事件仍按成功处理。

内置管理界面会显式以 `auth_mode: "cookie"` 登录。服务端下发不透明的、仅限当前主机的 `HttpOnly`、`SameSite=Strict` 会话 Cookie，以及独立且绑定该会话的 CSRF Cookie；浏览器不会收到、保存或持久化管理员 JWT。所有使用 Cookie 认证的非安全方法都必须在 `X-Octopus-CSRF` 中回传 CSRF Cookie。浏览器管理界面有意限制为同源使用；CORS 白名单仍可供 Bearer/API Key 客户端跨域调用，但不允许跨域携带 Cookie。`auto` 模式仅在请求的直接来源命中 `server.trusted_proxies` 时才接受 `X-Forwarded-Proto: https`；无法可靠提供该头的部署应使用 `always`。

为保持兼容，非浏览器客户端调用 `POST /api/v1/user/login` 时不传 `auth_mode` 仍会取得 Bearer JWT，也可显式传入 `auth_mode: "bearer"`。登录响应带有 `Cache-Control: no-store`。浏览器会话保存在当前单实例进程中，最多 4096 项，过期后惰性清理，进程重启后全部失效。`POST /api/v1/user/logout` 只撤销当前浏览器会话；修改管理员密码或用户名会递增 `token_version`，从而使全部浏览器会话和 Bearer JWT 失效。

**数据库配置：**

支持三种数据库：

| 类型 | `database.type` | `database.path` 格式 |
|------|-----------------|---------------------|
| SQLite | `sqlite` | `data/data.db` |
| MySQL | `mysql` | `user:password@tcp(host:port)/dbname` |
| PostgreSQL | `postgres` | `postgresql://user:password@host:port/dbname?sslmode=disable` |

**MySQL 配置示例：**

```json
{
  "database": {
    "type": "mysql",
    "path": "root:password@tcp(127.0.0.1:3306)/octopus"
  }
}
```

**PostgreSQL 配置示例：**

```json
{
  "database": {
    "type": "postgres",
    "path": "postgresql://user:password@localhost:5432/octopus?sslmode=disable"
  }
}
```

> 💡 **提示**：MySQL 和 PostgreSQL 需要先手动创建数据库，程序会自动创建表结构。

**环境变量：**

所有配置项均可通过环境变量覆盖，格式为 `OCTOPUS_` + 配置路径（用 `_` 连接）：

| 环境变量 | 对应配置项 |
|----------|-----------|
| `OCTOPUS_SERVER_PORT` | `server.port` |
| `OCTOPUS_SERVER_HOST` | `server.host` |
| `OCTOPUS_DATABASE_TYPE` | `database.type` |
| `OCTOPUS_DATABASE_PATH` | `database.path` |
| `OCTOPUS_LOG_LEVEL` | `log.level` |
| `OCTOPUS_RELAY_INITIAL_RESPONSE_TIMEOUT_SECONDS` | `relay.initial_response_timeout_seconds` |
| `OCTOPUS_RELAY_NON_STREAM_TIMEOUT_SECONDS` | `relay.non_stream_timeout_seconds` |
| `OCTOPUS_RELAY_STREAM_FIRST_EVENT_TIMEOUT_SECONDS` | `relay.stream_first_event_timeout_seconds` |
| `OCTOPUS_RELAY_STREAM_IDLE_TIMEOUT_SECONDS` | `relay.stream_idle_timeout_seconds` |
| `OCTOPUS_RELAY_MAX_JSON_REQUEST_BYTES` | `relay.max_json_request_bytes` |
| `OCTOPUS_RELAY_MAX_IMAGE_REQUEST_BYTES` | `relay.max_image_request_bytes` |
| `OCTOPUS_RELAY_MAX_NON_STREAM_RESPONSE_BYTES` | `relay.max_non_stream_response_bytes` |
| `OCTOPUS_OBSERVABILITY_METRICS_ENABLED` | `observability.metrics.enabled` |
| `OCTOPUS_OBSERVABILITY_METRICS_HOST` | `observability.metrics.host` |
| `OCTOPUS_OBSERVABILITY_METRICS_PORT` | `observability.metrics.port` |
| `OCTOPUS_OBSERVABILITY_METRICS_BEARER_TOKEN` | `observability.metrics.bearer_token` |


## 📸 界面预览

### 🖥️ 桌面端

<div align="center">
<table>
<tr>
<td align="center"><b>首页</b></td>
<td align="center"><b>渠道</b></td>
<td align="center"><b>分组</b></td>
</tr>
<tr>
<td><img src="web/public/screenshot/desktop-home.png" alt="首页" width="400"></td>
<td><img src="web/public/screenshot/desktop-channel.png" alt="渠道" width="400"></td>
<td><img src="web/public/screenshot/desktop-group.png" alt="分组" width="400"></td>
</tr>
<tr>
<td align="center"><b>价格</b></td>
<td align="center"><b>日志</b></td>
<td align="center"><b>设置</b></td>
</tr>
<tr>
<td><img src="web/public/screenshot/desktop-price.png" alt="价格" width="400"></td>
<td><img src="web/public/screenshot/desktop-log.png" alt="日志" width="400"></td>
<td><img src="web/public/screenshot/desktop-setting.png" alt="设置" width="400"></td>
</tr>
</table>
</div>

### 📱 移动端

<div align="center">
<table>
<tr>
<td align="center"><b>首页</b></td>
<td align="center"><b>渠道</b></td>
<td align="center"><b>分组</b></td>
<td align="center"><b>价格</b></td>
<td align="center"><b>日志</b></td>
<td align="center"><b>设置</b></td>
</tr>
<tr>
<td><img src="web/public/screenshot/mobile-home.png" alt="移动端首页" width="140"></td>
<td><img src="web/public/screenshot/mobile-channel.png" alt="移动端渠道" width="140"></td>
<td><img src="web/public/screenshot/mobile-group.png" alt="移动端分组" width="140"></td>
<td><img src="web/public/screenshot/mobile-price.png" alt="移动端价格" width="140"></td>
<td><img src="web/public/screenshot/mobile-log.png" alt="移动端日志" width="140"></td>
<td><img src="web/public/screenshot/mobile-setting.png" alt="移动端设置" width="140"></td>
</tr>
</table>
</div>


## 📖 功能说明

### 📡 渠道管理

渠道是连接 LLM 供应商的基础配置单元。

**Base URL 说明：**

程序会根据渠道类型自动补全 API 路径，您只需填写基础 URL 即可：

| 渠道类型 | 自动补全路径 | 填写 URL | 完整请求地址示例 |
|----------|-------------|----------|-----------------|
| OpenAI Chat | `/chat/completions` | `https://api.openai.com/v1` | `https://api.openai.com/v1/chat/completions` |
| OpenAI Responses | `/responses` | `https://api.openai.com/v1` | `https://api.openai.com/v1/responses` |
| Codex OAuth | `/responses` | `https://chatgpt.com/backend-api/codex`（固定） | `https://chatgpt.com/backend-api/codex/responses` |
| OpenAI Images | `/images/generations`、`/images/edits`、`/images/variations` | `https://api.openai.com/v1` | `https://api.openai.com/v1/images/generations` |
| Anthropic | `/messages` | `https://api.anthropic.com/v1` | `https://api.anthropic.com/v1/messages` |
| Gemini | `/models/:model:generateContent` | `https://generativelanguage.googleapis.com/v1beta` | `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent` |

> 💡 **提示**：填写 Base URL 时无需包含具体的 API 端点路径，程序会自动处理。

Codex OAuth 渠道不是普通 OpenAI API Key 渠道。新建渠道时选择 **Codex OAuth**，导入本机
JSON 文件或将完整内容粘贴到 **Codex OAuth JSON** 字段；Octopus 会在到期前自动刷新并
原子写回轮换后的 token。附件请求格式、凭据字段兼容范围和安全边界见
[Codex OAuth 渠道使用说明](docs/codex-oauth-channel.md)。

---

### 📁 分组管理

分组用于将多个渠道聚合为一个统一的对外模型名称。

**核心概念：**

- **分组名称** 即程序对外暴露的模型名称
- 调用 API 时，将请求中的 `model` 参数设置为分组名称即可

**负载均衡模式：**

| 模式 | 说明 |
|------|------|
| 🔄 **轮询** | 每次请求依次切换到下一个渠道 |
| 🎲 **随机** | 每次请求随机选择一个可用渠道 |
| 🛡️ **故障转移** | 优先使用高优先级渠道，仅当其故障时才切换到低优先级渠道 |
| ⚖️ **加权分配** | 根据渠道设置的权重比例分配请求 |

> 💡 **示例**：创建分组名称为 `gpt-4o`，将多个供应商的 GPT-4o 渠道加入该分组，即可通过统一的 `model: gpt-4o` 访问所有渠道。

---

### 💰 价格管理

管理系统中的模型价格信息。

**数据来源：**

- 系统会定期从 [models.dev](https://github.com/sst/models.dev) 同步更新模型价格数据
- 当创建渠道时，若渠道包含的模型不在 models.dev 中，系统会自动在此页面创建该模型的价格信息,所以此页面显示的是没有从上游获取到价格的模型，用户可以手动设置价格
- 也支持手动创建 models.dev 中已存在的模型，用于自定义价格

**价格优先级：**

| 优先级 | 来源 | 说明 |
|:------:|------|------|
| 🥇 高 | 本页面 | 用户在价格管理页面设置的价格 |
| 🥈 低 | models.dev | 自动同步的默认价格 |

> 💡 **提示**：如需覆盖某个模型的默认价格，只需在价格管理页面为其设置自定义价格即可。

---

### ⚙️ 设置

系统全局配置项。

**Relay 日志正文策略：**

- `metadata`（默认）：只保存诊断信息和元数据，不保存请求/响应正文。
- `full`：保存经过脱敏和大小限制的请求/响应正文。脱敏只能降低风险，无法保证任意提示词中不含秘密。
- `disabled`：不再产生新的 Relay 日志。

除非明确需要排查正文级问题，否则应保持 `metadata`。修改策略不会删除已经落库或已经
复制进备份的历史内容。

**统计保存周期（分钟）：**

由于程序涉及大量统计项目，若每次请求都直接写入数据库会影响读写性能。因此程序采用以下策略：

- 统计数据先保存在 **内存** 中
- 按设定的周期 **定期批量写入** 数据库

> ⚠️ **重要提示**：退出程序时，请使用正常的关闭方式（如 `Ctrl+C` 或发送 `SIGTERM` 信号），以确保内存中的统计数据能正确写入数据库。**请勿使用 `kill -9` 等强制终止方式**，否则可能导致统计数据丢失。




## 🔌 客户端接入

### OpenAI SDK

```python
from openai import OpenAI
import os

client = OpenAI(   
    base_url="http://127.0.0.1:8080/v1",   
    api_key=os.environ["OCTOPUS_API_KEY"],
)
completion = client.chat.completions.create(
    model="octopus-openai",  # 填写正确的分组名称
    messages = [
        {"role": "user", "content": "Hello"},
    ],
)
print(completion.choices[0].message.content)
```

### Claude Code

编辑 `~/.claude/settings.json`

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080",
    "ANTHROPIC_AUTH_TOKEN": "<YOUR_OCTOPUS_API_KEY>",
    "API_TIMEOUT_MS": "3000000",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "ANTHROPIC_MODEL": "octopus-sonnet-4-5",
    "ANTHROPIC_SMALL_FAST_MODEL": "octopus-haiku-4-5",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "octopus-sonnet-4-5",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "octopus-sonnet-4-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "octopus-haiku-4-5"
  }
}
```

### Codex

编辑 `~/.codex/config.toml`

```toml
model = "octopus-codex" # 填写正确的分组名称

model_provider = "octopus"

[model_providers.octopus]
name = "octopus"
base_url = "http://127.0.0.1:8080/v1"
```
编辑 `~/.codex/auth.json`

```json
{
  "OPENAI_API_KEY": "<YOUR_OCTOPUS_API_KEY>"
}
```


---

## 🤝 致谢

- 🙏 [looplj/axonhub](https://github.com/looplj/axonhub) - 本项目的 LLM API 适配模块直接源自该仓库的实现
- 📊 [sst/models.dev](https://github.com/sst/models.dev) - AI 模型数据库，提供模型价格数据
- 🇨🇳 [AtomGit](https://atomgit.com/bestruirui/octopus) - 国内代码托管
