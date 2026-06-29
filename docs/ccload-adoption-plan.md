# ccLoad 借鉴方案

## 目标

在保持 Octopus 现有个人化、轻量聚合体验的前提下，选择性吸收 ccLoad 的生产网关能力。优先补足高收益、低侵入的调度和保护机制，避免整体替换协议层、存储层或前端形态。

## 原则

- 保留 Octopus 现有组树、nested fallback、compact 策略、axonhub/llm transformer 和 React 管理端。
- 新能力默认兼容存量配置，0 或空值表示关闭。
- 先做运行时保护，再做健康度排序和复杂可观测性。
- 每个阶段都要能通过 focused tests 独立验证。

## 阶段一：渠道级保护与稳定加权

状态：已开始实施。

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

## 阶段二：URL 运行时择优

状态：已完成第一版内存态实现。

### 设计

- 保留 `base_urls.delay` 作为初始参考值。
- relay 成功后记录每个 channel + URL 的首字节 EWMA 延迟。
- URL 失败后设置短期指数退避冷却。
- 未探索 URL 保留一定探索概率，避免永远不用新增 URL。

### 落地边界

- 第一版只做内存态，不做持久化和 UI。
- relay 请求路径已接入运行时选择；后台模型同步和 URL 测速仍使用静态配置，不受影响。
- 后续可在渠道详情里展示 URL 运行时延迟、失败数和冷却状态。

## 阶段三：错误分类与冷却决策

### 设计

- 抽出 relay 错误分类模块，输出 `RetryKey`、`RetryChannel`、`ReturnClient`。
- 识别 `Retry-After`、SSE error、HTTP 200 soft error、1308、Gemini `RESOURCE_EXHAUSTED` 等常见形态。
- 单 Key 渠道遇到 Key 级错误时可升级为渠道级冷却，避免无意义重试。

### 落地边界

- 不一次性替换现有熔断器。
- 先把分类结果用于是否切换候选和冷却时长，再逐步扩展 UI。

## 阶段四：健康度排序

### 设计

- 基于窗口内成功率、样本量和首字节延迟生成健康分。
- 只在用户开启后影响同优先级候选排序。
- 小样本降低惩罚权重，避免新渠道被误伤。

### 落地边界

- 默认关闭。
- 不覆盖用户显式 failover 优先级，只作为同层候选内排序因素。

## 阶段五：请求改写增强

### 设计

- 在现有 `CustomHeader`、`ParamOverride` 基础上补充 header remove/append 和 JSON path remove/override。
- 认证头保持保护，不允许规则覆盖 Authorization、x-api-key 等敏感头。

### 落地边界

- 保持现有简单表单可用，高级规则另做折叠区。
- 规则容量设置上限，避免恶意或误配置造成请求处理成本过高。

## 不采纳项

- 不迁移 ccLoad 静态多页前端。
- 不替换 Octopus 的 axonhub/llm 协议转换路径。
- 不整体迁移 ccLoad storage/cache 体系。
- 不默认启用健康度惩罚，避免改变用户对组策略的直觉。
