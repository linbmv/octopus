# 虚拟渠道代码清理总结

**执行时间**：2026-06-07  
**执行者**：Claude Opus 4.8

---

## 清理范围

### 删除的提交（12 个）

| 提交 | 说明 |
|------|------|
| a7c39ed | 虚拟渠道后端基础设施（Day 1）|
| 1a4eb6a | 虚拟渠道 relay 核心与响应恢复（Day 2）|
| 0d232a6 | 虚拟渠道前端 API 类型（Day 3.1）|
| 4438324 | 虚拟渠道前端 UI - 表单和创建/编辑逻辑（Day 3.2）|
| 462ecf2 | 修复虚拟渠道前端 UI 的状态一致性和校验问题 |
| 798189c | 虚拟渠道 Group 组件支持（Day 3.3）|
| 3e8f225 | 日志组件支持虚拟渠道 redirect 状态（Day 3.4）|
| 40578b8 | 虚拟渠道功能文档和测试清单（Day 3.5）|
| 689d986 | 分组嵌套重构 - 后端实施（Day 3.6）|
| 1e981f5 | 分组嵌套重构 - 执行文档（Day 3.6）|
| 0681530 | Compact 请求在 OpenAI Chat 渠道上的兼容性问题（旧版本）|

**说明**：`689d986` 和 `1e981f5` 是虚拟渠道的"重构版本"（改用 GroupItem.Type + TargetGroupID），也一并删除。

### 删除的文件

**后端**：
- `internal/db/migrate/005.go` - 虚拟渠道字段迁移
- `internal/db/migrate/006.go` - 分组嵌套迁移
- `migrations/000025_group_nesting.*.sql` - SQL 迁移脚本

**前端**：
- web 组件中的虚拟渠道 UI（已还原到干净状态）

**文档**：
- `.claude/virtual-channel-*.md`
- `.claude/day3.*.md`
- `.claude/operations-log-day3.*.md`
- `.claude/verification-report-day3.*.md`
- `.claude/plan/*nested*.md`
- `.claude/plan/*refactor*.md`
- `.claude/*gemini*.md`

---

## 保留的提交（4 个）

| 提交 | 说明 |
|------|------|
| dadddb8 | compact 请求转发给 OpenAI Chat 渠道时改用标准端点 |
| 97700c2 | 移除临时调试日志 |
| ac6277d | 补充 fetch 回归测试锁住 anyrouter 兼容路径 |
| d346a22 | 记录 compact 请求路由问题排查与修复过程 |
| **5105b9f** | **中继快失败与健康粘性优化（本次会话新增）** |

---

## 新增功能（本次会话）

### 第一阶段：attempt 级可观测性增强
- ✅ attempt 开始/成功/失败日志
- ✅ 首 token 时间记录到 attempt span
- ✅ waiting_upstream 活跃请求状态

### 第二阶段：慢成功不刷新 sticky（健康粘性）
- ✅ `sticky_healthy_first_token_timeout` 配置（默认 0=关闭）
- ✅ 仅当首 token < 阈值时才刷新 sticky
- ✅ 非流式请求保持兼容

### 第三阶段：自定义代理 HTTP Client 按地址复用
- ✅ `GetHTTPClientCustomProxy` 按 proxyURL 复用
- ✅ 双重检查锁，避免并发创建

### 补充修复：Compact 请求深拷贝
- ✅ `requestForOutboundPipeline` 深拷贝 Stream 指针
- ✅ 避免多 attempt 重试时互相污染
- ✅ 补充 3 个深拷贝测试

---

## 验证结果

```bash
go test ./internal/relay -run 'TestRequestForOutboundPipeline' -v  ✅
go test ./internal/relay  ✅
go test ./internal/client ./internal/relay/balancer ./internal/server/...  ✅
go build ./...  ✅
```

---

## Git 历史对比

### 清理前（dadddb8..0681530）
- 14 个提交，包含虚拟渠道和分组嵌套
- +3464 / -127 行

### 清理后（dadddb8..5105b9f）
- 4 个提交，只保留 Compact 修复和本次优化
- +3913 / -43 行（主要是文档和测试）

---

## 下一步操作

### 推送到远程（需要强制推送）

```bash
# 查看差异
git log --oneline origin/dev..HEAD

# 强制推送（会重写远程历史）
git push --force-with-lease origin dev
```

**警告**：`--force-with-lease` 比 `--force` 更安全，如果远程有其他人推送的提交会拒绝。

### 通知协作者

如果有其他人在基于旧分支工作，需要通知他们：

```bash
git fetch origin
git reset --hard origin/dev
```

---

## 清理理由

1. **虚拟渠道功能未完成**：
   - 迁移 005 会导致启动失败（`model.Channel` 缺虚拟字段）
   - relay 主路径未接入虚拟渠道逻辑
   - 功能完全不生效

2. **分组嵌套是虚拟渠道的重构版本**：
   - 同样存在循环引用、唯一索引等问题
   - 未经充分测试和验证

3. **保持代码库干净**：
   - 删除半成品功能
   - 避免误导后续开发

---

**最终状态**：代码库已恢复到纯 Compact 修复 + 中继优化状态，无任何虚拟渠道残留。