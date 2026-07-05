# Octopus 复杂度治理与功能收敛计划

## 背景

Octopus 当前测试基线仍然健康，但项目复杂度已经开始向少数核心模块集中：

- 后端 `internal/relay/relay.go` 承担入站解析、候选选择、重试、compact fallback、raw passthrough、流式转发、指标、日志等多类职责。
- `internal/op` 以包级全局缓存和后台 flush 为主，生命周期、失败重试、测试隔离和并发边界逐渐变难。
- 前端设置、分组、渠道页面中存在大组件，表单状态、业务规则、接口调用和展示逻辑混在一起。
- 工作区近期变更跨越 compact、health、sync、export、UI 等多个主题，说明功能增长已经超过当前模块边界承载能力。

本计划目标不是重写系统，而是在保持现有功能可用的前提下，逐步降低复杂度、收敛功能入口、减少请求路径副作用，并建立后续改动的边界。

## 治理目标

1. 把高频请求路径从“业务、日志、统计、健康、策略混合执行”收敛为清晰的 relay pipeline。
2. 把后台副作用统一放入可观测、可停止、可限流的 worker 体系。
3. 把全局缓存逐步封装为 service，减少隐藏依赖和测试污染。
4. 把前端大组件拆成表单状态、业务转换、展示组件三个层次。
5. 把高级功能从“散落设置项”收敛为少数模式和预设，降低用户和维护者认知负担。

## 非目标

- 不整体替换 `axonhub/llm` 协议转换层。
- 不重写前端技术栈。
- 不一次性删除 compact fallback、health、stats、logs 等现有能力。
- 不在同一个提交里混合架构拆分、行为调整和 UI 改版。
- 不在没有迁移和回滚路径的情况下改变用户现有配置语义。

## 当前主要问题

### 1. Relay 模块职责过重

现状：

- `relay.go` 同时处理 request parse、candidate ranking、nested fallback、attempt lifecycle、stream writer、raw passthrough、compact fallback、soft error、metrics/logging。
- 每次新增上游协议、compact 策略或错误分类，都容易改动 relay 主流程。

影响：

- 变更风险高。
- 测试需要覆盖过多组合。
- 性能优化和行为修复容易互相干扰。

治理方向：

- 以接口和小结构拆分 relay 子模块，但保持对外 handler 不变。
- 先拆纯逻辑，再拆状态和副作用。

### 2. 请求路径中仍有过多副作用

现状：

- relay 保存指标、日志、健康状态、sticky、熔断状态。
- 渠道创建/更新 handler 内直接启动 5 分钟后台任务，执行价格补全、URL 延迟探测、自动分组。
- stats/log flush 已开始异步化，但 worker 生命周期和失败重试还未统一。

影响：

- 请求耗时和请求成功语义被后台副作用影响。
- 高并发时难以限制后台任务数量。
- 失败任务难以追踪和重试。

治理方向：

- 所有非必须立即完成的副作用进入统一 worker。
- handler 只提交 job，不直接开 goroutine。

### 3. 全局缓存和 init 注册过多

现状：

- route、migration、log worker、stats worker、health integration 等依赖 `init()` 或包级变量。
- `op` 包中存在 setting/channel/group/user/stats/log 等全局缓存。
- 部分 DB 操作未携带请求 context。

影响：

- 测试间状态污染风险增加。
- 生命周期不可控，shutdown 时难以 flush/stop。
- 新功能容易继续添加全局变量。

治理方向：

- 新代码禁止新增无管理的 package-level worker。
- 逐步引入 `AppServices` 或 `Runtime` 聚合关键 service。

### 4. 日志和流式响应内存风险

现状：

- 流式响应会把事件保存在 `responseEvents`，结束后聚合成日志响应体。
- 最终落库前会截断，但事件列表本身没有明确上限。
- 日志体系同时包含 DB 保存、内存缓存、SSE 推送、内容截断、导出。

影响：

- 长输出和高并发会产生内存压力。
- 日志功能承担审计、实时 UI 和调试三种目标，边界不清。

治理方向：

- 日志级别收敛为 `off`、`metadata`、`metadata_with_truncated_body`。
- 流式日志改为有限缓冲或增量聚合，不保存完整事件列表。

### 5. Health/compact/错误分类配置复杂度上升

现状：

- health/adaptive timeout/weighted balancer/slow model/sticky threshold/shadow mode 等设置项不断增加。
- compact fallback、probe、endpoint downgrade、errorclass、soft error 存在交叉。

影响：

- 用户难以判断应该开哪些设置。
- 维护者难以确认每个设置组合下的行为。

治理方向：

- 前端提供少数模式预设，后端收敛为 policy 对象。
- compact 策略使用表驱动 strategy chain，relay 主流程只执行策略结果。

### 6. 前端大组件继续膨胀

现状：

- API Key、Group Editor、Channel Form 等组件同时处理数据查询、表单状态、业务转换和渲染。
- API hooks 与 UI invalidation、业务语义耦合较深。

影响：

- 新增字段或交互时容易改动大文件。
- 组件局部测试和复用困难。

治理方向：

- 大组件拆为 `useXxxFormState`、纯转换函数、展示组件。
- API endpoint hook 保持数据访问，不承载 UI 流程。

## 功能精简与合并清单

### 必须收敛

1. Relay 日志功能
   - 合并 DB 日志、实时日志、响应体日志策略。
   - 以日志模式控制内容粒度。
   - 删除流式完整事件聚合依赖。

2. 统计保存功能
   - 合并 total/daily/hourly/channel/model/api_key 的 flush 逻辑。
   - 建立统一 dirty snapshot 和 flush worker。
   - 跨天 rollover 只提交 daily snapshot，不阻塞请求。

3. 渠道维护任务
   - 合并价格补全、URL 延迟检测、自动分组为 `ChannelMaintenanceJob`。
   - 支持同 channel job 去重。
   - 支持手动触发和后台触发复用同一入口。

4. Health 配置
   - 合并多项高级设置为预设：`off`、`shadow`、`balanced`、`aggressive`。
   - 高级设置保留但折叠，不作为默认主路径。

5. Compact fallback
   - 合并 probe、fallback、downgrade 判断为 `CompactStrategyChain`。
   - relay 只消费 strategy chain 返回的候选和错误动作。

### 应该优化

1. DB 导出
   - 稳定后只保留流式导出作为 handler 主路径。
   - 旧 `DBExportAll` 仅作为测试辅助或删除。

2. 前端日志页面
   - 历史分页和 SSE 实时日志合并为 bounded store。
   - 限制页面内最大日志条数。

3. API Key 设置
   - 拆为 Basic、Limits、ModelAccess 三块。
   - 金额、过期时间、模型权限转换逻辑移出组件。

4. Group Editor
   - 拆出 `ModelPicker`、`MemberListEditor`、`GroupFormState`。
   - 排序、权重、自动添加逻辑独立测试。

5. 错误分类
   - 统一 `errorclass`、`soft_error`、compact downgrade 的结果结构。
   - 输出标准动作：`return_client`、`retry_key`、`retry_channel`、`downgrade_strategy`。

### 可以降级或做开关

1. 内置自更新
   - 如果部署主要依赖 Docker/GitHub release，可默认关闭。
   - 保留命令式入口，不放在主流程。

2. 低频图像接口
   - `images/edits`、`images/variations` 若使用率低，可降低维护优先级。
   - 保留协议转发，但不让其阻塞 relay 主流程重构。

3. 高级 health 细粒度设置
   - 前端折叠到 Advanced。
   - 默认只展示模式选择。

## 分阶段实施计划

### Phase 0：冻结与基线

目标：停止继续扩大复杂度，建立可回滚基线。

任务：

1. 整理当前未提交改动，按主题拆分为独立提交：
   - compact 稳定性。
   - health/sync 改动。
   - 第二阶段性能优化。
   - 前端设置 UI。
2. 每个提交必须通过：
   - `go test ./...`
   - `pnpm --dir web lint`
3. 新增 `docs/complexity-remediation-plan.md` 作为治理准则。
4. 临时规则：
   - 不新增设置项，除非附带默认模式和迁移说明。
   - 不新增无生命周期管理的 goroutine。
   - 不在 `relay.go` 增加新的大块功能。

验收标准：

- 工作区可以按主题 cleanly commit。
- 任一主题可单独 revert。
- CI/test 基线稳定。

建议提交：

- `docs: add complexity remediation plan`

### Phase 1：请求路径减负

目标：先解决稳定性和性能风险，不做大规模重构。

任务：

1. 流式日志有限缓冲
   - 将 `responseEvents` 改为 bounded buffer 或 streaming aggregator。
   - 保证最大内存与 `MaxRelayLogContentBytes` 或独立上限相关。
   - 超限时记录 `[truncated]`，不中断转发。

2. Relay 日志模式
   - 新增内部日志策略枚举，不急于暴露 UI。
   - 当前配置映射到 `metadata_with_truncated_body` 或 `off`。
   - 日志内容生成只依赖策略。

3. Channel maintenance job
   - 从 channel handler 中移除直接 goroutine。
   - 新增 job 提交入口，先用内存队列和同 channel 去重。
   - 失败只记录日志，不影响 create/update 响应。

4. DB context 清理
   - `SettingSetString`、`SettingSetInt`、`UserChangePassword`、`UserChangeUsername` 增加 context 版本或统一从调用方传入。
   - 保留旧函数作为兼容 wrapper。

验收标准：

- 长流式输出内存不随 token 数无界增长。
- 创建/更新渠道不会直接启动无管理 goroutine。
- `go test ./...` 通过。

建议提交：

- `fix(relay): bound stream log aggregation`
- `refactor(channel): move maintenance work to managed job`
- `refactor(op): pass context through settings and user updates`

### Phase 2：Worker 生命周期统一

目标：把 log flush、stats save、channel maintenance、model sync 等后台任务纳入统一管理。

任务：

1. 新增 `internal/runtime` 或 `internal/task/worker`：
   - `Start(ctx)`
   - `Stop(ctx)`
   - `Submit(job)`
   - 并发上限
   - 队列深度
   - 失败计数
2. log flush worker 接入统一 worker。
3. stats save worker 接入统一 worker。
4. channel maintenance worker 接入统一 worker。
5. shutdown 时调用 flush：
   - stats dirty snapshot。
   - relay log cache。
   - channel key dirty cache。

验收标准：

- 后台 goroutine 都能被 context 停止。
- 关键 flush 在 shutdown 中有明确入口和超时。
- worker 队列满时有明确降级策略。

建议提交：

- `feat(runtime): add managed worker lifecycle`
- `refactor(op): move async flushers to managed workers`

### Phase 3：Relay 模块拆分

目标：降低 `relay.go` 单文件和单模块变更风险。

拆分目标：

1. `request.go`
   - parse inbound request。
   - supported models check。
2. `runner.go`
   - `relayRun` lifecycle。
   - attempt loop。
3. `attempt.go`
   - `relayAttempt` prepare/run/finalize。
4. `stream_writer.go`
   - SSE write、first token guard、stream aggregation。
5. `raw_passthrough.go`
   - raw body patch、stream_options patch。
6. `compact_chain.go`
   - compact strategy chain。
7. `metrics_log.go`
   - metrics save、relay log build。

实施原则：

- 第一轮只移动代码，不改行为。
- 每次移动后跑 focused tests。
- 对外入口 `Handler(inboundType)` 保持不变。

验收标准：

- `relay.go` 降到 500 行以内。
- 每个子文件职责单一。
- 现有 relay 测试不降级。

建议提交：

- `refactor(relay): split request and runner`
- `refactor(relay): split stream writer and raw passthrough`
- `refactor(relay): split metrics and compact chain`

### Phase 4：Policy 收敛

目标：把 health、compact、errorclass 从散落判断收敛为策略对象。

任务：

1. Health policy
   - 定义 `HealthPolicy`。
   - 从 settings 一次性构建 policy。
   - relay/balancer 只读 policy。
2. Compact policy
   - 定义 `CompactPolicy` 和 strategy chain。
   - probe 只更新 strategy 状态，不直接污染 relay 主流程。
3. Error decision
   - 统一 errorclass 和 soft error 输出。
   - 决策结果包含：
     - error level。
     - retry key/channel。
     - client response。
     - compact downgrade action。

验收标准：

- relay 主流程不直接识别大量状态码和错误字符串。
- health 设置读取集中在一个地方。
- compact fallback 行为由表驱动测试覆盖。

建议提交：

- `refactor(health): introduce health policy`
- `refactor(compact): use strategy chain`
- `refactor(relay): unify error decisions`

### Phase 5：op 全局状态收敛

目标：减少 package-level mutable state。

任务：

1. 定义 `Services`：
   - `Settings`
   - `Users`
   - `Channels`
   - `Groups`
   - `Stats`
   - `RelayLogs`
2. 先对 user/settings 做 service wrapper。
3. 再对 channel/group cache 做 service wrapper。
4. 保留旧 `op.Xxx` 函数作为薄 wrapper，逐步迁移调用点。
5. 测试使用 test services，减少全局状态清理。

验收标准：

- 新测试不依赖包级全局 cache reset。
- user/settings 并发访问有锁或不可变快照。
- 旧 API 调用保持兼容。

建议提交：

- `refactor(op): introduce settings service`
- `refactor(op): introduce user service`
- `refactor(op): wrap channel and group caches`

### Phase 6：前端组件收敛

目标：降低 UI 修改成本。

任务：

1. API Key 页面
   - `useAPIKeyFormState`
   - `APIKeyBasicFields`
   - `APIKeyLimitFields`
   - `APIKeyModelAccess`
2. Group Editor
   - `useGroupEditorState`
   - `ModelPicker`
   - `MemberListEditor`
   - `WeightControls`
3. Log 页面
   - bounded log store。
   - SSE reconnect/backoff。
4. Setting 页面
   - health 使用模式预设。
   - advanced settings 折叠。

验收标准：

- 单个组件文件尽量低于 350 行。
- 表单转换函数有单元测试或至少纯函数测试。
- UI 行为不变。

建议提交：

- `refactor(web): split api key settings form`
- `refactor(web): split group editor`
- `refactor(web): bound live log state`

## 质量门槛

每个阶段必须满足：

1. `go test ./...`
2. `pnpm --dir web lint`
3. `git diff --check`
4. 影响 relay 的改动必须包含 focused relay tests。
5. 影响导入/导出、日志、stats 的改动必须包含边界测试。
6. 任何后台 worker 必须有：
   - context 停止。
   - 队列满策略。
   - 错误日志。
   - shutdown flush 或明确说明不需要。

## 提交与发布纪律

1. 一个提交只做一个主题。
2. 不把 UI 改版和后端行为改动混在一起。
3. 不把纯移动代码和行为变化混在一起。
4. 每个提交说明：
   - 解决什么问题。
   - 行为是否变化。
   - 如何验证。
   - 如何回滚。
5. 对外行为变化必须更新 README 或 docs。

## 风险与回滚

### 风险：relay 拆分引入行为回归

缓解：

- 先纯移动，后改行为。
- 保持 `Handler` 入口和测试不变。
- 每次只拆一个职责。

回滚：

- 单提交 revert。

### 风险：worker 异步化导致数据延迟或丢失

缓解：

- 保留周期 flush。
- shutdown flush。
- 队列满时对关键任务使用合并信号，不丢最后状态。

回滚：

- worker 接口保留同步 fallback。

### 风险：配置预设改变用户直觉

缓解：

- 旧设置继续生效。
- 预设只是 UI 和 policy builder 层映射。
- advanced 保留原始设置。

回滚：

- 前端隐藏预设，后端保留 settings 读取。

### 风险：前端拆分产生 UI 回归

缓解：

- 不改布局，只拆逻辑。
- 每次只拆一个组件区域。
- 保持现有 API hook 返回结构。

回滚：

- 单组件提交 revert。

## 优先级排序

P0：

1. 当前未提交改动拆分提交。
2. 流式日志 bounded buffer。
3. channel maintenance job 管理化。
4. worker 生命周期统一设计。

P1：

1. relay 文件拆分。
2. stats/log flush 接入 worker。
3. health policy 收敛。
4. compact strategy chain。

P2：

1. op services 化。
2. 前端 API Key / Group Editor 拆分。
3. 日志前端 bounded store。
4. 设置页高级项折叠。

P3：

1. 内置自更新降级为可选。
2. 低频图像接口维护优先级降低。
3. 旧导出函数清理。

## 工作区未提交改动审核

本节记录当前工作区未提交改动的主题划分、收益、风险和纳入整改计划的处理方式。目标是避免把多个方向的修复混成一个不可回滚的大提交。

### A. Compact 主动探测默认关闭

涉及文件：

- `internal/helper/compact_probe.go`
- `internal/model/setting.go`
- `internal/model/setting_test.go`
- `internal/task/sync.go`
- `web/src/api/endpoints/setting.ts`
- `web/src/components/modules/setting/LLMSync.tsx`
- `web/public/locale/*.json`

改动摘要：

- 新增 `compact_strategy_probe_enabled` 设置项，默认 `false`。
- 模型同步阶段的 compact strategy probe 受该开关控制。
- probe 请求内容从简单 `hello` / `Reply with ok` 改为更接近真实 handoff summary 的低 token 请求。
- 前端 LLM Sync 设置页增加开关和说明文案。

价值：

- 降低同步任务向上游发送探测内容导致风控、封禁或误判的概率。
- 保留真实请求中的 compact fallback，不直接牺牲功能可用性。
- 把“主动探测”从默认行为改为显式选择，更符合生产网关的最小副作用原则。

风险：

- 行为变化：默认不再主动探测 compact 策略，首次真实 compact 请求可能多一次 fallback 成本。
- 新增设置项会继续扩大 settings 面积，和“设置项收敛”目标存在张力。
- 前端 toggle 当前是乐观更新，保存失败时需要补回滚或错误提示一致性。

整改纳入：

- 作为独立提交合入，不与 relay/errorclass 或 worker 改动混合。
- 提交前补充验证：
  - 默认设置包含 `compact_strategy_probe_enabled=false`。
  - 设置校验只接受 bool。
  - probe disabled 时 `syncCompactGroupStrategies` 不执行上游探测。
  - probe enabled 时仍保持原有探测路径。
- Phase 4 中将其并入 `CompactPolicy`，避免后续继续散落读取 setting。

建议提交：

- `feat(compact): make strategy probes opt-in`

### B. 错误分类与请求取消处理

涉及文件：

- `internal/relay/errorclass/classifier.go`
- `internal/relay/errorclass/classifier_test.go`
- `internal/relay/relay.go`
- `internal/relay/relay_test.go`

改动摘要：

- 403 根据响应体识别 client/IP/probe restriction，升级为 Channel 级错误。
- 429 根据响应体识别 service unavailable/global limit，升级为 Channel 级错误。
- 503 识别 `no_available_account` / 无可用账号，升级为 Channel 级错误。
- relay attempt 中请求 context 已取消时，不再继续记录 runtime URL failure 或 key status 更新。

价值：

- 避免同渠道多 key 在明显渠道/客户端限制下反复重试，减少无意义请求。
- 客户端断开或请求超时不再污染上游健康、URL 选择和 key 状态。
- 与之前 compact 自动压缩失败日志中的“探测触发上游限制”问题相关，方向正确。

风险：

- 基于响应体字符串分类存在误判空间，尤其是不同上游对 403/429 的文案不一致。
- 429 从 Key 级升级为 Channel 级时，可能减少同渠道其他 key 的重试机会。
- classifier 继续扩张会变成另一个规则堆积点。

整改纳入：

- 作为独立稳定性提交合入。
- 合入前补充表驱动测试覆盖：
  - key quota / insufficient balance 仍是 Key 级。
  - client restricted / probe abuse 是 Channel 级。
  - generic 429 仍是 Key 级。
  - service unavailable body 是 Channel 级。
- Phase 4 将 `errorclass`、`soft_error`、compact downgrade 合并成统一 `ErrorDecision`，防止规则散落。

建议提交：

- `fix(relay): classify client-restricted upstream errors as channel failures`
- `fix(relay): ignore request cancellations for upstream health mutation`

### C. 第二阶段性能优化

涉及文件：

- `internal/op/log.go`
- `internal/op/stats.go`
- `internal/op/backup.go`
- `internal/server/handlers/setting.go`
- `internal/op/backup_test.go`
- `internal/server/handlers/setting_test.go`
- `internal/server/middleware/validate_test.go`

改动摘要：

- relay log 达阈值后改为 signal 后台 flush，不在 `RelayLogAdd` 内同步写库。
- stats daily rollover 改为 signal 后台保存旧 daily snapshot。
- DB export 改为逐表流式 JSON 输出，不再一次性构建完整 dump。
- 补充 body limit 测试和流式导出兼容性测试。

价值：

- 明显降低请求路径同步 DB 写入风险。
- 大型导出不再把完整日志/统计加载到内存。
- body limit 有测试保护，降低大 body 导致内存压力的回归风险。

风险：

- `log.go` 和 `stats.go` 新增 `init()` 后台 worker，当前没有统一 shutdown、flush 和生命周期管理。
- `statsSaveSignal` 满时会额外启动 goroutine 等待发送，DB 持续阻塞时可能积累 goroutine。
- relay log async flush 失败后只记录错误，重试依赖下一次 signal 或周期任务。
- export handler 已写出 200 后若中途失败，只能得到不完整 JSON；服务端不能再返回结构化错误。
- 旧 `DBExportAll` 仍保留，导出实现出现双路径。

整改纳入：

- 作为独立性能提交合入，但必须在提交说明里声明当前 worker 生命周期限制。
- Phase 2 必须优先接管这些 worker：
  - log flush worker。
  - stats save worker。
  - channel maintenance worker。
- `statsSaveSignal` 满时不允许无限开 goroutine，改为合并 pending snapshot 或记录 dropped/merged 指标。
- export 流式失败纳入文档：客户端以 JSON parse failure 识别下载失败，服务端记录 error。
- Phase 3/P3 稳定后删除或降级旧 `DBExportAll`。

建议提交：

- `perf(op): move relay log and stats persistence off request path`
- `perf(backup): stream database exports`
- `test(server): cover request body limits`

### D. 前端 LLM Sync 设置改动

涉及文件：

- `web/src/components/modules/setting/LLMSync.tsx`
- `web/src/api/endpoints/setting.ts`
- `web/public/locale/*.json`

改动摘要：

- 在 LLM Sync 设置区域增加 compact probe 开关。
- 增加三语言文案。
- 新增 `CompactStrategyProbeEnabled` setting key。

价值：

- 用户能明确控制主动探测是否启用。
- 文案说明真实请求 fallback 不受影响，降低误解。

风险：

- `LLMSync.tsx` 继续承载更多设置项，和前端设置组件膨胀问题一致。
- toggle 保存失败时本地状态没有回滚，可能造成 UI 与实际设置短暂不一致。
- 设置页继续按功能散落开关，后续需要整合到 compact/advanced policy。

整改纳入：

- 可随 A 主题提交，不单独混入其它后端改动。
- Phase 6 拆分设置页时，将 compact probe 放入 compact/advanced 区域。
- 补 UI 行为：保存失败时回滚 switch 或重新拉取 settings。

建议提交：

- 与 `feat(compact): make strategy probes opt-in` 同提交，或单独 `feat(web): expose compact probe setting`。

### E. 工作区提交拆分建议

当前未提交改动应拆为以下提交序列：

1. `feat(compact): make strategy probes opt-in`
   - 包含 compact probe setting、task gate、probe prompt、前端开关和 locale。
   - 不包含 errorclass、export、stats/log worker。

2. `fix(relay): refine upstream error classification`
   - 包含 403/429/503 分类和测试。
   - 不包含 request cancellation 行为。

3. `fix(relay): avoid health mutation after request cancellation`
   - 包含 `isRequestContextCanceled` 和对应测试。
   - 便于独立回滚。

4. `perf(op): move persistence work off request path`
   - 包含 log flush 和 stats save 异步化。
   - 提交说明标注“后续 Phase 2 接入 managed worker”。

5. `perf(backup): stream database exports`
   - 包含 `DBExportAllStream`、handler 调整和 export 测试。

6. `test(server): cover body size limits`
   - 包含 body limit 测试。

7. `docs: add complexity remediation plan`
   - 包含本计划书。

### F. 合入前检查清单

每个提交合入前执行：

- `go test ./...`
- `pnpm --dir web lint`
- `git diff --check`

额外检查：

- Compact probe opt-in：
  - 手动确认默认 disabled。
  - 手动确认真实 compact fallback 不依赖主动 probe。

- Error classification：
  - 检查近期 relay 日志中的 403/429/503 文案，确认分类样本覆盖真实情况。

- Async persistence：
  - 压测或小脚本验证连续请求下 goroutine 数不异常增长。
  - 验证 shutdown/SaveCache 仍能 flush 缓存。

- Streaming export：
  - 导出包含 logs/stats 的 dump 后重新 import 到临时 DB。
  - 大日志表导出时观察内存峰值。

## 建议执行节奏

第一周：

- 完成 Phase 0。
- 完成流式日志 bounded buffer。
- 完成 channel maintenance job 管理化。

第二周：

- 完成 worker lifecycle。
- 接入 log/stats/channel maintenance。
- 开始 relay 纯拆分。

第三周：

- 完成 relay 拆分。
- 收敛 health policy 和 compact strategy chain。

第四周：

- 开始 op service wrapper。
- 拆前端 API Key 和 Group Editor。

## 当前立即行动项

1. 将当前工作区改动按主题拆开，不再继续叠加新功能。
2. 合入第二阶段优化前，确认它只包含：
   - log flush 异步化。
   - stats save 异步化。
   - export streaming。
   - body limit tests。
3. 开始 P0 第一项代码改动：限制流式日志内存。
4. 建立 worker lifecycle 草案，再迁移现有异步 flush。

## 成功标准

短期成功：

- relay 长流式响应内存有明确上限。
- handler 不再直接开无管理长任务。
- 当前未提交改动按主题提交。

中期成功：

- `relay.go` 降到 500 行以内。
- 后台任务全部有生命周期管理。
- health/compact/error decision 有统一 policy/strategy 入口。

长期成功：

- 新功能能落在明确模块里，不再自然流向 `relay.go` 或大前端组件。
- 单个变更能被单独测试、单独回滚。
- 项目复杂度增长速度低于功能增长速度。
