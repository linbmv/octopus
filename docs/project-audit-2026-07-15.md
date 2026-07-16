# Octopus（awcaio）全项目审计、修复与最终验收报告

> 初审日期：2026-07-15
> 最终复验日期：2026-07-16
> 审计对象：`/root/distrobox/ai-env/github/octopus` 当前实际工作区，包括未提交修改
> 仓库身份：`github.com/linbmv/octopus` 为当前 fork；Go module 继续使用上游路径 `github.com/bestruirui/octopus`
> 状态标记：✅ 已闭环；🟡 代码与本地证据完成、仍有外部阻断；◐ 后续产品/维护路线；❌ 未通过

## 1. 最终结论

本轮已完成当前环境内所有可执行的审计、修复和验证。项目的管理面、渠道、分组、模型映射、API Key、Relay、日志、统计、备份、健康与观测、前端、Release、Docker 和供应链门禁均已逐模块复核；高风险问题已经落实到代码、测试、工作流和文档。

项目当前可定义为：**高质量的单实例发布候选版本**。本地代码与制品门禁已经通过，但在三个外部事项完成前不得表述为“生产发布完全验收”：

1. 吊销并轮换用户曾公开提供的第三方中转凭据及旧 probe/test token。
2. 处置完整 Git 历史中的 6 个 secret 命中；历史改写需要仓库协作授权，不能擅自执行。
3. 在真实 `v*` tag GitHub Actions 中取得 Cosign keyless、GitHub OIDC provenance、SBOM attestation 和镜像 digest 的成功证据。

除上述外部事项外，原审计计划没有仍可在本工作区继续执行的 P0/P1 修复项。多实例协调和外部日志 spool 属于明确的后续产品路线，不是当前单实例版本的隐性“未修完 bug”；备份 v2 增量合并与账号能力探测均已于 2026-07-16 完成。

### 1.1 最终评分

| 维度 | 初审 | 最终 | 结论 |
|---|---:|---:|---|
| 功能完整性 | 7.0 | 9.3 | 管理与 Relay 主链路完整，真实渠道/分组/模型/CLI 与备份 v2 已验证；缺口主要是能力矩阵探测。 |
| 稳定性 | 5.5 | 9.3 | 全仓 race、三方言数据库、浏览器、跨架构容器和故障边界均有证据；部署边界仍为单实例。 |
| 安全性 | 4.5 | 8.8 | Cookie/CSRF、备份加密、资源上限、错误脱敏、secret/vulnerability gate 已完成；外部凭据和历史命中仍压低评分。 |
| 代码质量 | 6.0 | 9.0 | golangci-lint、Staticcheck、deadcode 归零，职责与错误模型明显收敛；少数 Go/React 大文件仍需持续拆分。 |
| 测试与工程化 | 5.0 | 9.5 | Go、前端、Playwright、数据库、Release、容器、Trivy、Gitleaks、SBOM 已形成完整证据链。 |
| 发布就绪度 | 4.0 | 8.6 | 本地发布候选通过；真实 tag 签名证明和外部凭据处置仍是硬门禁。 |

按 20% 功能、20% 稳定、20% 安全、15% 代码质量、15% 测试工程化、10% 发布就绪度加权，**整体评分为 9.1/10**。其中代码与单实例产品质量为 9.1/10，生产发布就绪度单列为 8.6/10。

### 1.2 计划完成度

原报告的 40 个 P0/P1/P2/P3 项目归并后：

- 38 项完全闭环，占 95%。
- 2 项代码/流程完成但受外部状态阻断，占 5%：凭据与 Git 历史处置、真实 tag 签名/证明。
- 原列为持续维护的 2 项已于 2026-07-16 完成本轮收敛：前端大组件拆分、语义级国际化质量门禁。

按“本地可执行任务”统计为 **100% 完成**；按包含外部处置的原始清单统计为 **95% 完全闭环、5% 外部阻断**。

2026-07-16 扩展路线另纳入 8 项增强：dirty build、metrics allowlist、CSP、WebAuthn、备份 v2、能力探测、日志 spool、多实例。前 6 项已完成并验证；后 2 项仍是产品/架构路线，不回算为原始 40 项缺陷。外部凭据/历史处置与真实 tag 证明仍是两个独立阻断组。

## 2. 审计范围与方法

覆盖范围：

- 根入口、Cobra CLI、启动、关闭、健康回放工具。
- 配置、热更新、数据库、迁移、数据模型和全部 `op` 业务模块。
- Relay 编排、协议转换、流、负载均衡、熔断、健康、运行时状态和错误分类。
- HTTP client、模型发现、价格同步、任务调度、metrics、tracing 与通用工具包。
- Gin 路由、handlers、管理员认证、API Key 认证、中间件、响应协议。
- Next.js API client、登录、渠道、分组、日志、设置、备份、Service Worker 与 E2E。
- CI、Release、Docker/Compose、安全扫描、SBOM、签名流程、脚本、文档和代码生成工具。

审计方法：

- 目录、符号、路由、调用链和错误出口扫描。
- 事务、缓存、并发、生命周期、超时、内存/字节上限和敏感数据人工复核。
- 故障注入、边界、race、handler、前端单元和浏览器级回归。
- 真实 SQLite、MySQL、PostgreSQL、第三方中转、Codex CLI、Claude CLI、Docker 双架构验证。
- 构建、静态分析、依赖验证、secret/vulnerability/misconfiguration 扫描和 CycloneDX SBOM。

仓库要求优先使用 fast-context MCP；当前会话未提供该 MCP 所需凭据/工具，因此使用 `rg`、编译器、测试、调用链和运行时证据完成审计。这一工具限制不影响最终门禁结果。

工作区本来就包含大量未提交修改。本报告评价当前实际文件，不把全部 diff 归因于单一轮次，也没有执行 reset、checkout、历史改写或替用户提交。

## 3. 当前架构与支持边界

```text
main / Cobra
  -> validated conf snapshot + watcher
  -> db.InitDB + versioned migrations
  -> op caches/workers + secure bootstrap
  -> relay health persistence + scheduler
  -> Gin
       /api/v1      -> Bearer JWT 或浏览器 Cookie session + CSRF -> handlers
       /v1,/v1beta  -> API Key -> relay
       /health...   -> coarse public health
       独立 listener /metrics -> optional Bearer
       embedded Next.js static export
  -> signal-driven reverse-order shutdown

relay
  -> bounded request read + inbound transform
  -> model/group lookup + nested candidates
  -> channel/key/URL + RPM/concurrency/circuit/health policy
  -> bounded non-stream or phase-guarded stream pipeline
  -> redacted public response + stats/log/health/runtime feedback
```

数据库是持久化真值；浏览器会话、Scheduler、粘性路由、熔断、健康、URL 冷却、RPM、并发限制和部分缓存仍为进程内状态。因此当前正式支持边界是 **单应用实例**。多副本部署前必须完成第 9.6 节的共享状态方案。

## 4. 最终自动化与运行证据

### 4.1 Go、静态分析与契约

| 检查 | 最终结果 |
|---|---|
| `go test -race -count=1 ./...` | ✅ 全部 Go 包通过，无 race。 |
| 最后 Relay 503 脱敏后的定向 race | ✅ `relay`、handlers、middleware、helper、price、bodylimit 全部通过。 |
| `go vet ./...` | ✅ |
| `go build -buildvcs=false ./...` | ✅ |
| `go mod verify` | ✅ `all modules verified`。 |
| golangci-lint 2.12.2 | ✅ `0 issues`。 |
| Staticcheck 2026.1 | ✅ 零输出。 |
| deadcode 0.48.0 `-test ./...` | ✅ 零输出。 |
| govulncheck 1.6.0 | ✅ 当前 module graph 最近一次扫描为 0 可达、0 imported-package；后续修改未改变依赖图。 |
| actionlint 1.7.12 | ✅ quality/release workflow 均通过。 |
| contracts/locale parity | ✅ 生成结果无漂移，三份 locale key 一致。 |
| `gofmt -d` / `git diff --check` / shell syntax | ✅ 均无输出。 |

### 4.2 前端与浏览器核心业务

| 检查 | 最终结果 |
|---|---|
| `pnpm install --frozen-lockfile --offline` | ✅ pnpm 9.15.9，锁文件一致。 |
| ESLint | ✅ |
| TypeScript | ✅ `tsc --noEmit --incremental false`。 |
| Vitest | ✅ 15 个文件、40 项测试。 |
| Next.js 生产构建 | ✅ Next 16.2.10 静态导出成功。 |
| Playwright | ✅ 1/1 passed；包含 WebAuthn 虚拟验证器注册与二因子登录，结果文件 `web/test-results/.last-run.json` 为 `passed`。 |

Playwright 覆盖的不是“只打开首页”，而是完整业务链：

- 首次启动、强制改密、重新登录、刷新恢复。
- 管理员 JWT 不进入浏览器存储；HttpOnly、SameSite=Strict Cookie 正常。
- 缺失/错误 CSRF 返回 403。
- 注册 WebAuthn 虚拟验证器；再次登录时密码因子不签发 Cookie/JWT，验证器成功后才建立管理会话。
- 创建渠道、创建分组、创建 API Key。
- `/v1/models` 只暴露允许的分组模型。
- 经真实本地 mock upstream 完成 Relay 对话、usage 与日志检查。
- 注销清除会话与 CSRF Cookie；注销后 `/api/v1/user/status` 明确为 401。

Quality E2E 直接读取同一步 Next 构建生成的 `web/out`；Release 构建将该产物复制到 `static/out`，tag E2E 再通过显式开关只读取最终打包目录。两条路径都要求对应 `index.html` 存在，既不会误测旧嵌入产物，也不会假等待 300 秒。

### 4.3 数据库方言矩阵

| 方言 | 版本/证据 | 结果 |
|---|---|---|
| SQLite | modernc SQLite，2026-07-16 备份 v2 最终树复验 | ✅ v1→v2、dry-run、四策略、ID 重映射、两类晚期回滚、CRUD、序列继续、并发删除。 |
| PostgreSQL | PostgreSQL 17.6 临时隔离容器，2026-07-16 复验 | ✅ 同一完整备份 v2 矩阵通过。 |
| MySQL | MySQL 8.0.46 临时隔离实例，2026-07-16 复验 | ✅ 同一完整备份 v2 矩阵通过；同时修复 WebAuthn 1024 字符 utf8mb4 唯一索引超过 3072 bytes 的新库阻断，改为完整 ID + SHA-256 唯一索引和 migration 014 回填。 |

数据库矩阵使用两个独立空数据库，先在 source 生成完整导出，再在 target 验证失败回滚和成功恢复，不能由 SQLite 单元测试替代。

### 4.4 真实第三方中转、渠道、分组与模型调用

2026-07-15 已使用用户指定的第三方中转完成真实联网验证。凭据只经环境变量和隔离临时目录传递，本报告不记录或复述任何密钥值。

直连第三方边界：

| 能力 | 结果 | 解释 |
|---|---|---|
| `GET /v1/models` | ✅ 200，117 个模型 ID | 目录声明不等于账号授权或工具能力。 |
| OpenAI Chat 普通对话 | ✅ | `gpt-5.4-mini` 返回预期内容。 |
| OpenAI Responses 普通对话 | ✅ | `gpt-5.4-mini` completed。 |
| Anthropic Messages 普通对话 | ✅ | Claude 模型正常结束。 |
| Anthropic 原生工具调用 | ✅ | 返回合法 `tool_use`、名称和 JSON 参数。 |
| OpenAI Chat 工具调用 | ◐ | GPT 在 required 模式未产生 tool call；同请求使用 Claude 模型成功。能力与模型/协议有关。 |
| Responses 函数工具 | ❌ 上游边界 | 第三方返回 `not implemented`，不是 Octopus 本地转换失败。 |
| 声明的 Codex 模型 | ❌ 上游授权边界 | 模型目录存在，但账号明确不支持。 |

经 Octopus 的真实链路已完成：

1. 创建 `openai/chat_completions` 渠道并写入临时上游账号。
2. 创建公开分组别名，将 item 映射到已验证的 Claude 上游模型。
3. 创建只允许该分组模型的 Octopus API Key。
4. `/v1/models` 仅返回这一公开模型。
5. 普通对话、强制工具调用、工具结果回传全部成功。
6. Relay 日志证明 `request_model_name` 为公开分组别名、`actual_model_name` 为指定上游模型，channel/attempt 一致。
7. metadata 日志策略下 request/response 正文为空，服务日志未命中精确凭据。

Codex CLI 0.144.4 与 Claude Code 2.1.207 也都只连接本地 Octopus：

- Codex CLI 普通对话 exit 0；真实产生一个 `command_execution`，执行只读 `pwd`、exit 0，并正确引用结果。
- Claude CLI 普通对话 exit 0；真实产生一个 Bash `tool_use` 和一个 `tool_result`，最终答案正确引用隔离目录。
- 统计和 Relay 日志只在本地 Octopus 增长，排除了 CLI 绕过网关直连第三方的可能。

结论：Octopus 的对话、渠道、分组、模型映射、工具回合、Codex CLI 和 Claude CLI 使用正确。第三方的 GPT/Responses 工具实现和部分 Codex 模型授权仍是供应商能力边界，不能通过本地代码伪装成“已支持”。

### 4.5 Release 与二进制

最终执行 `GOCACHE=build/go-cache-release bash scripts/build.sh release`：

- ✅ 8 个目标：Linux amd64/arm64/armv7/386，Windows amd64/386，macOS amd64/arm64。
- ✅ 8 个 ZIP 均可完整解压，Windows 包内为 `octopus.exe`。
- ✅ `SHA256SUMS` 恰有 8 项，`sha256sum --check --strict` 全通过。
- ✅ magic 验证：Linux `7f454c46`、Windows `4d5a9000`、Mach-O `cffaedfe`。
- ✅ 本机 arm64 `--help` 与 `version` exit 0，报告 Go 1.26.5、版本 `dev-6a0b324-dirty`。
- ✅ Release 构建前后 `git status --porcelain=v1 --untracked-files=all` 完全一致；受跟踪的 `static/out/README.md` 不再被构建删除。
- ✅ 临时 QEMU 卸载后，amd64 二进制在 arm64 宿主恢复为 exit 126 / `Exec format error`，证明未残留跨架构 host 修改。

这些是从当前未提交工作区生成的审计制品，不等同于可复现的正式 tag 制品；正式发布必须由干净 tag workflow 重新生成。

### 4.6 Docker、Compose 与供应链

最终构建并测试四个镜像：Alpine arm64/amd64、Distroless Debian arm64/amd64。

两套架构矩阵均通过：

- named volume 首次启动与再次启动持久化。
- 已准备 bind mount。
- root-owned 旧目录先 fail-closed 并给出可操作提示，修正 owner 后成功。
- 固定 UID/GID `10001:10001`、只读 rootfs、`cap-drop ALL`、`no-new-privileges`。
- `/tmp` 为 `192m`、`noexec,nosuid,nodev`，足以容纳 64 MiB 明文备份安全 spool。
- `/app/data` 可写、应用目录不可写、health 200、SIGTERM exit 0。
- Compose 隔离、健康等待、停止和卷清理通过。

安全证据：

| 扫描对象 | 组件数 | HIGH/CRITICAL | Secret |
|---|---:|---:|---:|
| 当前源码/部署配置 | Go 163；Web 1129 | 0 | 工作树 Gitleaks 0 |
| Alpine arm64 | 714 | 0 | 0 |
| Alpine amd64 | 714 | 0 | 0 |
| Distroless Debian arm64 | 1373 | 0 | 0 |
| Distroless Debian amd64 | 1374 | 0 | 0 |

源码 Trivy 同时报告 0 个 HIGH/CRITICAL 漏洞和 0 个高危 misconfiguration。六份 CycloneDX SBOM 均非空并通过 JSON/校验和验证。

完整 Git 历史扫描仍有 6 个命中，全部位于提交 `479454f96d2c340ca4559b1cdc3a040ff81c9a8f` 的 README/README_zh 旧行。门禁故意 fail-closed，没有用宽泛 ignore 掩盖。

## 5. P0/P1 修复落实

| 项目 | 状态 | 最终落实 |
|---|---|---|
| P0-1 进程内不安全自更新 | ✅ | 删除下载、解压、覆盖和重启实现；版本接口只读；升级交给外部部署系统。 |
| P0-2 明文 probe/历史凭据 | 🟡 | 当前工作树 0 命中，错误输出脱敏；供应商吊销和历史 6 命中仍需外部处置。 |
| P0-3 质量门禁漂移 | ✅ | 恢复并固定 Go/前端/Action 全门禁，失败不再被吞掉。 |
| P0-4 可达依赖漏洞 | ✅ | Go 1.26.5 与依赖升级；govulncheck/Trivy 当前无可达高危。 |
| P1-1 首次登录与强改密 | ✅ | 高熵 bootstrap、一次性文件安全、强改密状态机、旧会话/JWT 失效、Playwright 完整通过。 |
| P1-2 慢连接、代理与总时限 | ✅ | header/idle/header-size、trusted proxy IP/CIDR、非流式全生命周期 600s、流首事件/idle 600s。 |
| P1-3 Channel 删除错误/panic | ✅ | 事务错误与 panic 正确传播，故障注入通过。 |
| P1-4 API Key 删除一致性 | ✅ | DB/统计同事务，提交后才清双向缓存。 |
| P1-5 Channel Key dirty 标记 | ✅ | 版本 snapshot，失败或并发更新保留 dirty。 |
| P1-6 Setting 与 Scheduler | ✅ | schema 校验、原子应用、禁用后可启用、热更新测试。 |
| P1-7 regexp2 回溯 DoS | ✅ | 集中长度限制、有限 MatchTimeout、handler 与灾难性输入测试。 |
| P1-8 Relay 日志无界 | ✅ | 固定 worker/队列、缓存上限、drop/flush metrics、cursor/SSE resume/gap。 |
| P1-9 运行态全局状态 | ✅ | sticky/circuit/weight/URL/proxy client 均有 TTL、容量、清扫、主动失效和 race。 |
| P1-10 敏感日志 | ✅ | 默认 metadata、受控 redacted/full、请求/响应脱敏、客户端 key/密码/header 不落公开日志。 |
| P1-11 备份恢复与加密 | ✅ | strict JSON、全量预检、空目标、单事务；scrypt + AES-256-GCM `OCTOBKUP`，UI/API 完成。 |
| P1-12 DTO/输入/错误码 | ✅ | strict binding、集中 validator、typed domain error、500/Relay 5xx 统一脱敏。 |
| P1-13 虚假 health 运维面 | ✅ | 删除未注册运维 API，文档只描述真实 public/coarse surface。 |
| P1-14 构建部分失败仍成功 | 🟡 | 本地 Release/容器/SBOM 已通过，workflow 已接 Cosign/provenance；真实 tag OIDC 尚待外部执行。 |
| P1-15 终止流被误记取消 | ✅ | terminal event 先判成功，Codex 真实流量和 race 回归通过。 |

P0/P1 共 19 项：17 项完整闭环，2 项仅剩外部动作。

## 6. 逐模块评价

| 模块 | 评价 | 已落实 | 仍需关注 |
|---|---|---|---|
| 根入口、`cmd` | 上 | 启动错误同步返回、信号逆序关闭、health replay 收敛、无进程内自更新；本地构建显式带 `-dirty`。 | 正式发布仍必须来自 clean tag workflow。 |
| `conf` | 上 | 不可变快照、严格默认值/范围、watcher、trusted proxies、Cookie/Relay/metrics 配置。 | 未来为复杂热重载增加统一 rollback transaction。 |
| `db` / migrations | 上 | MySQL 特殊字符 DSN、迁移 registry、失败记录、migration 012 精确旧 admin 修复。 | 将三方言 service matrix 固化为 required CI。 |
| `model` | 上 | Channel/Group/API Key/Setting/backup 集中验证，长度/数量/非有限值边界。 | 可进一步拆 domain DTO 与 persistence model。 |
| `op/channel` | 上 | 原子 CRUD、Key 持久化重试、cache/runtime/proxy 主动失效。 | 多实例需发布缓存失效事件。 |
| `op/group` | 上 | CRUD 拆文件、引用校验、cycle 防护、nested fallback、稳定排序。 | 超大图可增加显式复杂度预算与基准。 |
| `op/apikey` | 上 | 双向索引一致、事务删除、单实例并发 reservation。 | 单响应仍可超过剩余额度；多实例需共享原子账本。 |
| `op/backup` | 上 | 严格恢复、故障回滚、64 MiB 边界、加密 envelope、安全 spool；v2 UUID 映射、dry-run 和四策略增量导入。 | 多实例恢复仍需全局维护锁。 |
| `op/log` | 上 | 有界 worker/cache、字符串 Snowflake ID、keyset cursor、SSE resume/gap/去重。 | 高可靠场景仍需外部 spool。 |
| `op/stats` | 上 | dirty snapshot、持久化错误重试、稳定查询拆分。 | 多实例聚合需共享原子更新。 |
| `op/user` | 上 | bootstrap 文件身份+摘要校验、bcrypt、token_version、默认 admin 迁移、WebAuthn 凭据生命周期。 | 可后续增加恢复码与更细的验证器策略。 |
| `client` | 上 | transport clone、proxy client TTL/容量/关闭、UA 边界。 | 把连接池/DNS/TLS 参数产品化并加故障注入。 |
| `helper` / model discovery | 上 | 100 页、单页 16 MiB、累计 64 MiB、50,000 唯一模型、token/regex 边界；能力探测不再把目录声明当账号授权。 | 可后续增加更多供应商专用 probe 模板。 |
| `price` | 上 | 16 MiB response ceiling、关闭错误传播、原子更新时间。 | 可增加 ETag/缓存与签名来源策略。 |
| `relay` core | 上 | 请求/响应限长、拒绝压缩炸弹、非流总预算、5xx 脱敏、attempt 统计。 | 分渠道 timeout override 与更细遥测可后续增加。 |
| `relay/stream` | 上 | 首事件优先级、raw heartbeat idle reset、terminal success、bounded log。 | 无终止标记的静默 EOF可进一步标记 incomplete。 |
| `relay/balancer` | 上 | 多模式、粘性、熔断、RPM/并发、TTL/容量、变更失效和 race。 | 状态为单实例。 |
| `relay/health` / errorclass | 上 | 三级分类、自适应 timeout、shadow、边界测试。 | 高级趋势 UI/共享 health 为后续。 |
| `server/auth` | 上 | Bearer 兼容；浏览器不透明 session、HttpOnly/Strict、绑定 CSRF、logout。 | 共享 session 仅在多实例路线需要。 |
| `server/middleware` | 上 | no-store、安全头、CORS、Request ID、可信代理、登录限流、JSON/image body ceiling。 | CSP 为兼容 UI 仍允许 inline，可渐进收紧。 |
| `server/handlers` / `resp` | 上 | strict JSON、typed error、内部 500 不泄露、管理审计、cursor handler。 | 可生成 OpenAPI 并统一非日志列表分页。 |
| `task` | 上 | 初始化完整后发布、可禁用/再启用、周期热更新、shutdown flush。 | 多实例需 leader election。 |
| `metrics` / `tracing` | 上 | 独立 metrics bind/token/IP-CIDR allowlist，非回环强制 token；拒绝/放行和 collector 可达性已有测试；OTLP middleware 可配置。 | 多实例聚合与长期指标存储由外部观测平台承担。 |
| `utils` | 上 | bodylimit、bounded cache、上下文日志、shutdown、Snowflake 和 diff 均有测试。 | 无明确垃圾/不明代码残留。 |
| Web API/auth | 上 | 管理 Cookie/CSRF、API Key-only sessionStorage、401 清理、ApiError/timeout。 | 保持不把管理员 JWT 重新引入浏览器。 |
| Web Channel/Group/Log/Setting | 中上 | 核心表单、日志 cursor/gap、加密备份 UI 和 locale 已打通。 | `log/Item.tsx`、`channel/CardContent.tsx` 等大组件继续拆纯函数/RTL。 |
| Service Worker | 上 | `/api`、`/v1`、`/v1beta`、health/metrics 和未知动态 GET 全部 network-only；v2 清旧 cache。 | 新增动态路由时保持 fail-closed 白名单。 |
| Web E2E | 上 | Quality 权威 `web/out`、Release 权威 `static/out`，缺产物立即失败，完整核心流程 1/1。 | 可扩展更多上游故障和移动端视觉回归。 |
| `static` / webtypes | 上 | 生产静态导出真实嵌入；contracts/locale 防漂移。 | 正式 tag 重新生成并校验。 |
| Build/Release | 上 | frozen install、依赖验证、失败 fail-closed、8 ZIP、SHA-256。 | dirty 本地构建标记与可复现时间戳仍可加强。 |
| Docker/Compose | 上 | 固定 digest、Alpine/Distroless、非 root、只读、卷迁移、双架构矩阵。 | 正式 registry digest 需 tag workflow 产出。 |
| CI/供应链 | 上 | Actions 固定 SHA、Gitleaks、Trivy、6 SBOM、Cosign/OIDC/provenance 流程。 | 历史 gate 未清前 tag workflow会正确失败。 |
| 文档 | 上 | Cookie、备份、health、metrics、单实例、供应链和 E2E 边界已同步。 | 继续用生成式 config/OpenAPI 降低漂移。 |

## 7. 稳定性与安全边界说明

### 7.1 Relay 资源边界

- JSON 请求默认最大 32 MiB。
- image edit/variation multipart 默认最大 64 MiB。
- 非流式上游响应默认最大 64 MiB。
- 非流式完整生命周期默认 600 秒，所有 key/channel retry 共用同一预算。
- 流式首事件默认 600 秒；优先级为分组手工值 > adaptive > 全局值。
- 首事件后 idle 默认 600 秒，上游事件与原始 SSE heartbeat 都续期。
- 成功流不设固定总字节上限，避免破坏合法长流；错误 body 仍受限。
- `gzip`/`deflate`/`zstd` 等压缩请求明确返回 415，避免解压炸弹。

### 7.2 备份安全语义

- 明文兼容导出仍存在，必须视为包含 Channel Key、Octopus API Key 和可选业务日志的高敏感文件。
- 提供密码时使用 scrypt `N=32768,r=8,p=1` 派生 AES-256-GCM key，格式以 `OCTOBKUP` 和版本字段开头。
- 加密/解密错误不区分错误密码与篡改，避免 oracle。
- 导出先写 `0600` 安全 spool，完整成功后才发送 200；不会把半个备份返回给客户端。
- 当前导出为 v2；按 UUID 支持 dry-run 与 `reject/skip/replace/merge` 增量导入。旧 v1 仍只允许空目标恢复。

### 7.3 API Key MaxCost 的准确语义

当前 reservation 已关闭单实例并发 check-then-act：同一个 API Key 的并发请求会保守预留，不会都看到同一余额后同时放行。

它不是绝对合同级硬预算：单次未知成本响应仍可能超过剩余额度；进程崩溃时 reservation 只存在于内存；多实例没有共享原子账本。对严格财务上限，应在请求前使用可证明的最大成本或共享事务账本，完成后结算差额。

### 7.4 错误与缓存泄露边界

- handlers 内部错误保留结构化服务端日志，客户端只收到通用 500。
- Relay 502/503/504 统一 `UPSTREAM_ERROR` / `Upstream service unavailable`；4xx 业务详情保留。
- 管理与 API Key dashboard 响应统一 `Cache-Control: no-store`。
- Service Worker 不接管任何认证 API、模型目录、健康或 metrics 响应，不会跨 Authorization 共享 Cache Storage。

## 8. 冗余、垃圾、未调用与不明代码结论

本轮通过引用扫描、Staticcheck、golangci-lint 和 `deadcode -test ./...` 清理了确定无消费者的内部符号、被替代的 health API、重复 stream writer、自更新实现、伪安全 confirm middleware 和过时测试。最终 deadcode 与 Staticcheck 都为零输出。

未发现仍可判定为“垃圾/不明代码”的生产符号。以下内容不是垃圾：

- `github.com/bestruirui/octopus` module path：fork 兼容需要。
- health estimator、errorclass、compact 与 runtime state：均有真实调用和测试。
- `AuditEvent` 与 active-request 状态：生产路由/运维 UI 使用。
- 加密备份复杂校验：属于 fail-closed 安全逻辑，不能为降低行数而删除。

仍有结构性维护债务，但应通过保行为拆分解决，而不是直接删除：大型 `op`/Relay 文件、部分前端 Card/Item 组件、全局 service locator 和硬编码中文。

## 9. 剩余修改方案

### 9.0 执行看板（2026-07-16 更新）

| 项目 | 当前状态 | 下一验收点 |
|---|---|---|
| 9.1 外部凭据与 Git 历史处置 | 🟡 外部权限阻断 | 供应商旧 key 失效，历史 required gate 通过 |
| 9.2 真实 tag 签名与 provenance | 🟡 外部环境阻断 | 正式 `v*` tag 的签名、SBOM 和 attestation 独立验证成功 |
| 9.3 备份 v2 | ✅ 已完成 | 五类核心实体 UUID、v1 升级、v2 导出、dry-run、四策略、显式关系/ID 重映射、三方言事务回滚与 UI 均通过 |
| 9.4 能力探测 | ✅ 已完成 | 有界 probe worker、五态结果、按账号/模型/协议/端点证据和路由/UI 闭环均通过 |
| 9.5 外部日志 spool | ◐ 待实施 | 有界 WAL、幂等重放、gap 指标 |
| 9.6 多实例路线 | ◐ 待实施 | leader/fencing、共享关键状态和三副本故障注入 |
| 9.7 CSP | ✅ 本轮完成 | `connect-src 'self'`、禁止 script attribute、限制 frame/media/manifest/worker；静态导出所需内联脚本保留并记录边界 |
| 9.7 metrics IP allowlist | ✅ 本轮完成 | IP/CIDR 校验、TCP peer 拒绝/放行、Bearer 叠加测试和运维文档已完成 |
| 9.7 管理员 2FA/WebAuthn | ✅ 本轮完成 | RP 配置、注册/删除、密码后二因子登录、凭据计数更新、浏览器 UI 和 Chromium 虚拟验证器 E2E 已通过 |
| 9.7 dirty build 标记 | ✅ 本轮完成 | clean/dirty 隔离仓库测试通过；当前工作树输出 `dev-6a0b324-dirty` |

执行顺序：先完成独立且可回滚的 dirty 标记、metrics allowlist 和 CSP，再推进 2FA、备份 v2、能力探测、日志 spool 与多实例。9.1/9.2 不以本地伪造结果替代外部证据。

### 9.1 P0：外部凭据与 Git 历史处置

状态：🟡 不能由本地代码代替供应商或仓库管理员操作。

执行方案：

1. 在第三方供应商控制台吊销本次公开凭据和旧 probe/test token，生成新 key。
2. 新 key 只进入 secret manager；不要写入 issue、聊天、README、shell history 或备份。
3. 记录吊销时间、操作者、供应商返回和受影响渠道。
4. 对历史提交 `479454…` 的 6 个命中逐个确认。
5. 经协作者授权后使用 `git filter-repo`/等价工具改写历史，强制更新受影响引用；或者为确认的假阳性建立精确 fingerprint 处置记录。禁止宽泛路径/规则 ignore。
6. 重新运行 current tree 和 `--all --full-history` Gitleaks。

验收：供应商旧 key 调用失败；新 key 正常；工作树与历史 gate 均成功，或每个保留项都有正式、精确、可审计的风险接受。

### 9.2 P0：真实 tag 签名与 provenance

状态：🟡 workflow 已实现，本地不能伪造 GitHub OIDC。

执行方案：

1. 先完成 9.1，使 release secret gate 可通过。
2. 将当前巨型工作区按 auth/backup/relay/log/frontend/supply-chain 等主题拆分提交并同行复核。
3. 从干净 commit 创建正式 `v*` tag。
4. 观察 `verify -> container-gate -> publish`，任何 job 失败都不得手工绕过。
5. 独立运行 Cosign `verify`/`verify-blob` 和 `gh attestation verify`，校验 workflow identity、OIDC issuer、source ref 和 digest。

验收：8 个 archive、6 个 SBOM、两个多架构镜像都有可验证签名/attestation；发布页面只包含同一 tag commit 产生的对象。

### 9.3 P1：备份 v2 UUID、dry-run 与增量 merge

状态：✅ 2026-07-16 完成。

修改方案：

1. 为 Channel、Key、Group、Item、API Key 等引入稳定 UUID，并增加兼容迁移。
2. v2 dump 保存 old ID、UUID 和显式关系，导入构建 `oldID -> newID` 映射。
3. 新增 dry-run：展示新增、更新、冲突、删除和不可恢复引用，不写数据库。
4. 定义按 UUID 的 `reject/skip/replace/merge` 策略，默认 reject。
5. 全部表在一个事务内恢复，提交后再刷新缓存；多实例时加维护锁。
6. 加密 envelope 继续包裹完整 v2 payload，保留 v1 只读兼容。

验收：SQLite/MySQL/PostgreSQL 对 ID 冲突、重复导入、关系映射、dry-run、晚期故障回滚和 v1->v2 兼容全部通过。

落地证据：

- Channel、Channel Key、Group、Group Item、API Key 使用稳定 UUID；migration 013 为存量数据回填并建立唯一索引。
- 当前导出为 v2，保留 source old ID、UUID 与显式 Key/Item 关系；增量导入从不复用 source 数字 ID，并重映射统计与 Relay attempt 引用。
- dry-run 与正式执行共用规划器，报告 create/update/skip/delete/conflict/unresolved；dry-run 不写库也不刷新缓存。
- `reject/skip/replace/merge` 默认 reject；replace 仅同步已导入 Channel/Group 的子项，merge 保留目标端额外子项；合并后分组图再次检查循环和深度。
- 所有写入在单事务中完成，设置阶段故障与冲突均完整回滚；提交后才刷新全部运行时缓存。
- SQLite、MySQL 8.0.46、PostgreSQL 17.6 的同一矩阵覆盖 v1→v2、重复导入、数字 ID 冲突、关系映射、dry-run 和晚期回滚；Go race、handler、Web 15 文件/40 项 Vitest、ESLint、独立 TypeScript 与 Next production build 均通过。

### 9.4 P1：账号×模型×协议能力探测

状态：✅ 已完成。第三方实测证明“模型列表存在”不等于“账号/协议/工具可用”，实现已按这一边界 fail-conservative。

落地证据：

1. 能力证据主键覆盖 channel、channel key、model、wire protocol、capability 和 endpoint fingerprint；scope fingerprint 在凭据、代理、header/JSON 改写或 User-Agent 变化后自动使旧证据失效。
2. 异步 probe worker 具有固定队列、并发、RPM、超时、TTL、单批和总成本上限；禁用时在创建付费 job 前拒绝。
3. 结果使用 `supported/unsupported/unauthorized/not_implemented/transient` 五态，并对错误类、HTTP 状态和消息做长度限制与凭据脱敏。
4. 路由只使用新鲜且匹配账号、协议、端点和请求能力的证据：支持项优先，未知/过期/瞬态保持保守 fallback，确定负项最后尝试但不被静默删除。
5. 渠道详情 UI 展示账号 remark、模型、协议、端点、探测时间、fresh/stale、五态与错误分类；API 不返回 key、scope fingerprint 或 endpoint fingerprint。
6. fake provider tests 覆盖文本可用但工具未实现、目录有但账号未授权、工具成功与伪流式响应；worker tests 覆盖成本、并发和 TTL，路由 tests 覆盖 supported/unknown/negative 排序，Vitest 防止 UI 把负项或过期证据显示为支持。

### 9.5 P1：外部日志 spool

状态：◐ 高可靠部署增强。

修改方案：定义 `LogSink` 接口，先实现本地 WAL/批量远端 sink；以现有字符串 Snowflake ID 为幂等键，明确 backpressure、磁盘配额、重试、丢弃和 gap 告警；日志正文继续执行同一脱敏策略。

验收：数据库/远端 sink 长时间故障时内存保持有界，恢复后不重复、不乱序，无法保留的数据产生可见 gap 指标。

### 9.6 P1：多实例路线

状态：◐ 当前不支持；部署必须保持单副本。

实施顺序：

1. Scheduler leader election 与 fencing token。
2. 共享浏览器 session/CSRF、API Key reservation、RPM/concurrency limiter。
3. 共享 sticky/circuit/health/URL 状态或明确按副本局部化并调整语义。
4. 数据变更 pub/sub 缓存失效。
5. 备份/恢复全局维护锁和流量 drain。
6. 跨副本日志 cursor、幂等写和顺序测试。

验收：至少 3 副本故障注入，覆盖 leader 切换、重复任务、并发预算、会话漂移、缓存陈旧和备份期间请求隔离。

### 9.7 P2：代码与前端维护

- [x] 将 `log/Item.tsx`、`channel/CardContent.tsx` 和 Setting 大组件拆为纯展示、表单状态和 API orchestration；关键纯函数与错误恢复路径已有测试覆盖。
- [x] 把用户可见的硬编码中文迁入 locale，并增加语义级自动检查，而不仅是 key parity。
- [x] 评估并收紧 CSP：连接仅允许同源、禁止脚本属性执行、禁止 frame/media，并显式约束 manifest/worker。Next.js 静态导出仍生成 hydration/theme 内联脚本与 style 属性，因此当前不能在不破坏 UI 的前提下删除 `script-src/style-src 'unsafe-inline'`；后续若改为 nonce/hash 或不再静态导出再移除。
- [x] 为 metrics 增加可选 IP/CIDR allowlist；直接 TCP peer 必须先通过 allowlist，再进行 Bearer 验证，且不信任转发头。
- [x] 为管理员增加 WebAuthn 二因子：显式 RP/HTTPS 来源配置、密码再认证注册/删除、用户验证强制、五分钟有界单次挑战、密码后第二因子登录、签名计数持久化和虚拟验证器 E2E。
- [x] 为本地 dirty build 增加 `-dirty` 标记和隔离仓库回归测试；正式 Release 保持 clean tag fail-closed。

## 10. 发布与部署要求

发布前必须同时满足：

- [x] 全仓 Go race/vet/build/mod verify 通过。
- [x] golangci-lint、Staticcheck、deadcode、actionlint 通过。
- [x] 前端 frozen install、lint、type-check、40 项 Vitest、production build 通过。
- [x] Playwright 核心业务 1/1 通过。
- [x] SQLite/MySQL/PostgreSQL 核心矩阵通过。
- [x] 8 平台 Release、ZIP、SHA256SUMS、格式和本机执行验证通过。
- [x] Alpine/Distroless、arm64/amd64 容器矩阵通过。
- [x] 源码和四镜像 Trivy/Syft 通过；当前工作树 Gitleaks 0。
- [ ] 已公开/旧凭据在供应商端吊销并轮换。
- [ ] Git 历史 6 个 secret 命中完成正式处置，required gate 成功。
- [ ] 真实 tag workflow 的 Cosign/OIDC provenance/SBOM attestation 独立验证成功。

生产部署在上述后三项完成前应停止。完成后仍需保持单实例，设置 `session_cookie_secure: "always"`（HTTPS 部署）、仅配置可信代理 CIDR、保护独立 metrics listener，并优先使用加密备份。

## 11. 最终判断

本项目已从“存在多处稳定性、安全和工程化缺口”提升为**经过全栈、数据库、浏览器、跨平台和容器证据验证的单实例发布候选**。渠道、分组和模型不是只做了静态检查：管理创建、公开模型、真实 Relay、工具回合、日志、Codex CLI 和 Claude CLI 均已实测。

当前没有仍可由本工作区自主完成的计划内 P0/P1 修复。剩余三个发布门禁都依赖供应商、Git 历史协作权限或 GitHub OIDC tag 环境；应保持 fail-closed，而不是通过忽略、伪造本地证明或反复要求用户做功能取舍来“宣布完成”。
