# ccLoad 借鉴方案

## 目标

在保持 Octopus 现有个人化、轻量聚合体验的前提下，选择性吸收 ccLoad 的生产网关能力。优先补足高收益、低侵入的调度和保护机制，避免整体替换协议层、存储层或前端形态。

## 原则

- 保留 Octopus 现有组树、nested fallback、compact 策略、axonhub/llm transformer 和 React 管理端。
- 新能力默认兼容存量配置，0 或空值表示关闭。
- 先做运行时保护，再做健康度排序和复杂可观测性。
- 每个阶段都要能通过 focused tests 独立验证。

## 阶段一：渠道级保护与稳定加权

状态：✅ 已完成。

### 渠道 RPM 限制

- 在 `Channel` 增加 `rpm_limit`，0 表示不限。
- relay attempt 发往上游前使用 60 秒滑动窗口预留请求名额。
- 命中限制时记录为 skipped attempt，并继续尝试后续候选。

### 渠道并发限制

- 在 `Channel` 增加 `max_concurrency`，0 表示不限。
- attempt 生命周期内占用并发槽位，结束时释放。
- 命中限制不计为渠道失败，不触发熔断。

### 平滑加权轮询

- 保留现有 `GroupModeWeighted` 语义。
- 将随机加权排序替换为平滑加权轮询，减少短窗口内的流量抖动。
- 非正权重按 1 处理，兼容旧配置。

落地证据：渠道限制在 attempt 前预留、结束释放，限流跳过不污染健康状态；平滑加权运行态有容量、TTL 和渠道失效清理。focused tests 覆盖 RPM、并发、跳过语义、平滑比例、非正权重与运行态回收。

## 阶段二：URL 运行时择优

状态：✅ 已完成计划内的第一版内存态实现。

### 设计

- 保留 `base_urls.delay` 作为初始参考值。
- relay 成功后记录每个 channel + URL 的首字节 EWMA 延迟。
- URL 失败后设置短期指数退避冷却。
- 未探索 URL 保留一定探索概率，避免永远不用新增 URL。

### 落地边界

- 第一版只做内存态，不做持久化和 UI。
- relay 请求路径已接入运行时选择；后台模型同步和 URL 测速仍使用静态配置，不受影响。
- 后续可在渠道详情里展示 URL 运行时延迟、失败数和冷却状态。

上述 UI/持久化明确属于第一版边界之外的后续可观测性增强，不是本阶段遗留实现项。

## 阶段三：错误分类与冷却决策

状态：✅ 已完成。

### 设计

- 抽出 relay 错误分类模块，输出 `RetryKey`、`RetryChannel`、`ReturnClient`。
- 识别 `Retry-After`、SSE error、HTTP 200 soft error、1308、Gemini `RESOURCE_EXHAUSTED` 等常见形态。
- 单 Key 渠道遇到 Key 级错误时可升级为渠道级冷却，避免无意义重试。

### 落地边界

- 不一次性替换现有熔断器。
- 先把分类结果用于是否切换候选和冷却时长，再逐步扩展 UI。

落地证据：`ErrorDecision` 已统一 key 重试/渠道切换/客户端终止决策；分类器覆盖 `Retry-After`、HTTP 200 structured soft error、SSE error、Anthropic 1308/1310 与 Gemini `RESOURCE_EXHAUSTED`。流式路径会在首个错误事件写给客户端前返回可分类错误，从而仍可安全切换同渠道 key。

## 阶段四：健康度排序

状态：✅ 已完成。

### 设计

- 基于窗口内成功率、样本量和首字节延迟生成健康分。
- 只在用户开启后影响同优先级候选排序。
- 小样本降低惩罚权重，避免新渠道被误伤。

### 落地边界

- 默认关闭。
- 不覆盖用户显式 failover 优先级，只作为同层候选内排序因素。

### 已实现语义

- 健康分由窗口成功率（80%）与首字节 P95（20%）组合；有效样本数未达到 `MinSamplesForPosterior`（默认 10）时，惩罚按样本置信度收敛到中性分，避免单次失败误伤新渠道。
- `health_weighted_balancer_enabled` 默认值为 `false`；设置页提供独立开关，且只有智能健康系统与该开关同时开启时才影响调度。
- Weighted 模式只将健康分作为显式权重的乘数；关闭时保持原平滑加权比例。
- Failover 始终先按用户 `priority` 升序，只有同一优先级内才按健康分降序；关闭时保持同层输入稳定顺序。
- focused tests 覆盖小样本降权、相同成功率下的延迟差异、Weighted 开/关与 Failover 不越级约束。

## 阶段五：请求改写增强

状态：✅ 已完成。

### 设计

- 在现有 `CustomHeader`、`ParamOverride` 基础上补充 header remove/append 和 JSON path remove/override。
- 认证头保持保护，不允许规则覆盖 Authorization、x-api-key 等敏感头。

### 落地边界

- 保持现有简单表单可用，高级规则另做折叠区。
- 规则容量设置上限，避免恶意或误配置造成请求处理成本过高。

### 配置与执行顺序

- 存量 `custom_header` 仍是简单 header set，存量 `param_override` 仍是顶层 JSON merge，旧 API 与旧表单保持兼容。
- 新增有序 `header_rules`：`set`、`append`、`remove`；新增有序 `json_rewrite_rules`：`override`、`remove`。
- 执行顺序为 raw passthrough → `param_override` → JSON path rules → `custom_header` → header rules → 渠道 `user_agent`。
- `Authorization`、`Proxy-Authorization`、`x-api-key`、`x-goog-api-key`、常见 auth/access/security token 与 Cookie 类认证头在校验和运行时双重保护下均不能设置、追加或删除。

### 有限 JSON Pointer 语法

- path 必须以 `/` 开头，采用 RFC 6901 的 `~0`、`~1` 转义；禁止改写根、空片段和数组追加 `-`。
- 最多 16 层、path 最多 512 bytes、单片段最多 128 bytes；数组只允许访问已有的十进制索引，不创建中间容器、不扩展数组。
- `override` 可替换已有值或创建最终对象成员；中间路径不存在与 `remove` 目标不存在时安全 no-op。
- 每类最多 32 条规则；单个 override JSON 值最多 16 KiB、最多 16 层/1024 个值，单类规则总载荷最多 64 KiB。

### 落地证据

- 后端模型、严格 JSON API、数据库 create/patch、备份校验/克隆与 relay 高级规则均已接通；模型发现路径也执行认证头保护，serializer JSON patch 有真实 SQLite 持久化测试。
- 管理端保留简单表单，并新增独立高级折叠区、创建/更新载荷、生成契约、认证头提示和简中/繁中/英文文案。
- focused Go tests 覆盖语法边界、容量、认证头保护、header 三操作、嵌套对象/数组 JSON 改写与持久化；Vitest 覆盖前端规则规范化和认证头识别。

## 不采纳项

- 不迁移 ccLoad 静态多页前端。
- 不替换 Octopus 的 axonhub/llm 协议转换路径。
- 不整体迁移 ccLoad storage/cache 体系。
- 不默认启用健康度惩罚，避免改变用户对组策略的直觉。

## 同渠道多 Key 模型合并与故障转移

状态：✅ 已完成 Phase 1 和 Phase 2。

### Phase 1：模型同步合并

- `FetchModels` 遍历同一渠道内所有可用 key，并行请求上游模型接口。
- 按 key 配置顺序合并模型列表并去重，保证结果稳定。
- 单个 key 拉取失败不会影响其他 key；只有所有 key 都失败时才返回错误。
- Anthropic/Gemini 模型接口失败或为空时，仍保留 OpenAI-compatible `/models` 回退兼容。

### Phase 2：请求期 key 级优化

- relay 在同一渠道内生成 key 尝试序列：sticky key 优先，其余 key 按累计成本低优先。
- 当前 key 返回 401、403、429 且客户端尚未收到响应时，自动在同渠道内切换下一个 key。
- 5xx、网络错误、首字超时等仍按渠道级失败处理，避免在同一故障上浪费重试预算。
- 成功请求继续刷新会话粘性，后续同一 API key + 请求模型优先复用成功的渠道 key。
