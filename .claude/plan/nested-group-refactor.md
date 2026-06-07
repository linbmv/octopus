# 分组嵌套重构实施计划

生成时间：2026-06-06  
预计耗时：10 小时（1.3 天）  
状态：待执行

---

## 🎯 任务类型

- [x] 后端 (→ Codex)
- [ ] 前端 (→ Claude)
- [x] 全栈 (→ 并行)

---

## 📋 执行摘要

**目标**：将虚拟渠道实现重构为分组嵌套，实现概念纯粹、性能最优、易维护的架构。

**核心变更**：
- ❌ 删除：`Channel.IsVirtual`、`resolveVirtualChannel`、`restoreResponseModel`（~300 行）
- ✅ 新增：`GroupItem.Type="group"`、`expandGroupItems` 递归展开（~80 行）
- ✅ 保持：当前调度逻辑（成员不按模型名过滤，支持跨模型降级）

**质量指标**：
- 稳定性：概念纯粹（Group 引用 Group）
- 性能：递归展开 < 0.5ms，消除响应模型恢复开销（~0.8ms）
- 可维护性：代码量减少 ~30%（-180 行）

---

## 🔑 技术方案（基于 Codex 分析）

### 核心设计决策

**1. 在 `op.GroupGetEnabledMap` 层完成递归展开**
- relay/balancer 只处理扁平 `[]GroupItem`（真实渠道）
- 无需理解树结构，现有排序、权重、failover、sticky 逻辑可复用
- groupCache 自动失效机制保证实时同步

**2. 保持当前调度逻辑**
- `groupMap.Get(modelName)` 通过模型名查找入口分组
- `filterEnabledGroupItems` 只过滤禁用成员，不过滤模型名
- 支持跨模型降级（claude-opus-4-8 失败 → gpt-5.5 自动接管）

**3. 数据模型调整**
```go
type GroupItem struct {
    GroupID       int    // 父分组 ID（保持不变）
    Type          string // "channel" | "group"（新增）
    ChannelID     int    // Type="channel" 时使用
    TargetGroupID int    // Type="group" 时使用（新增）
    ModelName     string // Type="channel" 时使用
    Priority      int
    Weight        int
    Disabled      bool
}
```

**关键点**：
- ✅ `TargetGroupID` 表示引用的目标分组（避免与父分组 `GroupID` 冲突）
- ✅ `Type=""` 视为 `"channel"`（兼容旧数据）
- ✅ 最大嵌套深度 3 层

---

## 📝 实施步骤

### 🗄️ 里程碑 M1：数据模型重构（1.5 小时）

#### 步骤 1.1：扩展 GroupItem 模型

**文件**：`internal/model/group.go`

**操作**：
1. 添加常量定义
2. 扩展 GroupItem 结构
3. 更新请求结构

**伪代码**：
```go
// 新增常量
const (
    GroupItemTypeChannel = "channel"
    GroupItemTypeGroup   = "group"
)

// 修改 GroupItem
type GroupItem struct {
    ID            int    `json:"id" gorm:"primaryKey"`
    GroupID       int    `json:"group_id" gorm:"not null;index"`
    Type          string `json:"type" gorm:"not null;default:channel;index"`
    ChannelID     int    `json:"channel_id" gorm:"index"`
    TargetGroupID int    `json:"target_group_id" gorm:"index"`
    ModelName     string `json:"model_name"`
    Priority      int    `json:"priority"`
    Weight        int    `json:"weight"`
    Disabled      bool   `json:"disabled" gorm:"not null;default:false"`
}

// 更新请求结构
type GroupItemAddRequest struct {
    Type          string `json:"type,omitempty"`
    ChannelID     int    `json:"channel_id,omitempty"`
    TargetGroupID int    `json:"target_group_id,omitempty"`
    ModelName     string `json:"model_name,omitempty"`
    Priority      int    `json:"priority,omitempty"`
    Weight        int    `json:"weight,omitempty"`
}
```

**验证**：
- `go build ./internal/model` 通过
- 类型定义无冲突

#### 步骤 1.2：数据库迁移脚本

**文件**：`migrations/000025_group_nesting.up.sql`（新建）

```sql
-- 添加新字段
ALTER TABLE group_items ADD COLUMN type VARCHAR(20) NOT NULL DEFAULT 'channel';
ALTER TABLE group_items ADD COLUMN target_group_id INT;
ALTER TABLE group_items ADD INDEX idx_type (type);
ALTER TABLE group_items ADD INDEX idx_target_group_id (target_group_id);

-- 删除旧的唯一索引
ALTER TABLE group_items DROP INDEX idx_group_channel_model;

-- 添加新的索引（channel 成员仍需唯一性）
CREATE UNIQUE INDEX idx_group_channel_model 
ON group_items (group_id, channel_id, model_name) 
WHERE type = 'channel';
```

**回滚脚本**：`migrations/000025_group_nesting.down.sql`（新建）

```sql
DROP INDEX idx_group_channel_model ON group_items;
CREATE UNIQUE INDEX idx_group_channel_model 
ON group_items (group_id, channel_id, model_name);

ALTER TABLE group_items DROP INDEX idx_target_group_id;
ALTER TABLE group_items DROP INDEX idx_type;
ALTER TABLE group_items DROP COLUMN target_group_id;
ALTER TABLE group_items DROP COLUMN type;
```

**验证**：
- 在测试数据库执行迁移脚本
- 检查字段和索引创建成功

---

### ⚙️ 里程碑 M2：后端调度器重构（3 小时）

#### 步骤 2.1：请求验证逻辑

**文件**：`internal/server/handlers/group.go`

**操作**：添加 GroupItem 类型验证

**伪代码**：
```go
func normalizeGroupItemRequest(req model.GroupItemAddRequest, groupID int) (model.GroupItem, error) {
    itemType := req.Type
    if itemType == "" {
        itemType = model.GroupItemTypeChannel
    }
    
    item := model.GroupItem{
        GroupID:  groupID,
        Type:     itemType,
        Priority: req.Priority,
        Weight:   req.Weight,
    }
    
    switch itemType {
    case model.GroupItemTypeChannel:
        if req.ChannelID <= 0 {
            return item, errors.New("channel_id required for channel type")
        }
        if req.ModelName == "" {
            return item, errors.New("model_name required for channel type")
        }
        item.ChannelID = req.ChannelID
        item.ModelName = req.ModelName
        
    case model.GroupItemTypeGroup:
        if req.TargetGroupID <= 0 {
            return item, errors.New("target_group_id required for group type")
        }
        if req.TargetGroupID == groupID {
            return item, errors.New("cannot reference self")
        }
        item.TargetGroupID = req.TargetGroupID
        
    default:
        return item, fmt.Errorf("invalid type: %s", itemType)
    }
    
    return item, nil
}
```

**验证**：
- 添加单元测试覆盖所有验证分支

#### 步骤 2.2：递归展开核心逻辑

**文件**：`internal/op/group.go`

**操作**：添加 `expandGroupItems` 函数

**伪代码**：
```go
const maxGroupNestDepth = 3

// 修改 GroupGetEnabledMap 入口
func GroupGetEnabledMap(name string, ctx context.Context) (model.Group, error) {
    group, ok := groupMap.Get(name)
    if !ok {
        // 尝试后备查找
        fallbackName := stripModelSuffix(name)
        if fallbackName != name {
            group, ok = groupMap.Get(fallbackName)
            if !ok {
                return model.Group{}, fmt.Errorf("group not found")
            }
        } else {
            return model.Group{}, fmt.Errorf("group not found")
        }
    }
    
    // 展开嵌套分组
    return expandEnabledGroup(group)
}

// 新增：展开嵌套分组入口
func expandEnabledGroup(group model.Group) (model.Group, error) {
    if !group.Enabled {
        group.Items = nil
        return group, nil
    }
    
    visited := map[int]struct{}{group.ID: {}}
    items, err := expandGroupItems(group, 0, visited)
    if err != nil {
        return model.Group{}, err
    }
    
    group.Items = items
    return group, nil
}

// 新增：递归展开逻辑
func expandGroupItems(group model.Group, depth int, visited map[int]struct{}) ([]model.GroupItem, error) {
    if depth > maxGroupNestDepth {
        return nil, fmt.Errorf("group %d: nesting depth exceeded (max %d)", group.ID, maxGroupNestDepth)
    }
    
    out := make([]model.GroupItem, 0, len(group.Items))
    
    for _, item := range group.Items {
        if item.Disabled {
            continue
        }
        
        // 处理渠道成员
        itemType := item.Type
        if itemType == "" {
            itemType = model.GroupItemTypeChannel
        }
        
        if itemType == model.GroupItemTypeChannel {
            // 检查渠道是否启用
            channel, ok := channelCache.Get(item.ChannelID)
            if !ok || !channel.Enabled {
                continue
            }
            out = append(out, item)
            continue
        }
        
        // 处理分组成员
        if itemType != model.GroupItemTypeGroup {
            log.Warnf("unknown group item type: %s", itemType)
            continue
        }
        
        // 循环检测
        if _, ok := visited[item.TargetGroupID]; ok {
            return nil, fmt.Errorf("group %d: circular reference detected (target %d)", group.ID, item.TargetGroupID)
        }
        
        // 获取目标分组
        targetGroup, ok := groupCache.Get(item.TargetGroupID)
        if !ok {
            log.Warnf("group %d: target group %d not found", group.ID, item.TargetGroupID)
            continue
        }
        
        if !targetGroup.Enabled {
            continue
        }
        
        // 递归展开子分组
        nextVisited := cloneIntSet(visited)
        nextVisited[item.TargetGroupID] = struct{}{}
        
        childItems, err := expandGroupItems(targetGroup, depth+1, nextVisited)
        if err != nil {
            return nil, err
        }
        
        out = append(out, childItems...)
    }
    
    return out, nil
}

// 工具函数
func cloneIntSet(src map[int]struct{}) map[int]struct{} {
    dst := make(map[int]struct{}, len(src)+1)
    for k := range src {
        dst[k] = struct{}{}
    }
    return dst
}
```

**关键特性**：
- ✅ 最大深度 3 层（入口为 0，递归到 3 停止）
- ✅ 循环检测（visited map 记录访问过的 GroupID）
- ✅ 禁用过滤（分组、成员、渠道均检查 Enabled 状态）
- ✅ 实时同步（每次从 groupCache 获取最新数据）

**验证**：
- 添加单元测试 `internal/op/group_test.go`：
  - `TestExpandGroupItems_OneLevel` - 单层嵌套
  - `TestExpandGroupItems_ThreeLevels` - 最大深度
  - `TestExpandGroupItems_CircularSelf` - A → A
  - `TestExpandGroupItems_CircularTwo` - A → B → A
  - `TestExpandGroupItems_DisabledFiltering` - 禁用过滤

#### 步骤 2.3：删除虚拟渠道逻辑

**文件**：`internal/relay/relay.go`

**操作**：删除虚拟渠道相关代码

**删除内容**：
- `const maxVirtualRedirectDepth = 3`
- `const maxVirtualResolveIterations = 50`
- `var errVirtualResolveIterationsExceeded`
- `type virtualResolveState struct {...}`
- `func newVirtualResolveState(...) {...}`
- `func (s virtualResolveState) next(...) {...}`
- `func (r *relayRun) resolveVirtualChannel(...) {...}`
- `func (ra *relayAttempt) restoreResponseBodyModel(...) {...}`
- `func (ra *relayAttempt) restoreStreamEventModel(...) {...}`

**简化后的代码**：
```go
func (r *relayRun) prepareAttempt() (*relayAttempt, error) {
    item := r.iter.Item()
    return r.resolveGroupItem(item, r.iter.IsSticky(), r.iter.StickyKeyID())
}

func (r *relayRun) resolveGroupItem(
    item dbmodel.GroupItem,
    sticky bool,
    stickyKeyID int,
) (*relayAttempt, error) {
    channel, err := op.ChannelGet(item.ChannelID, r.c.Request.Context())
    if err != nil {
        log.Warnf("failed to get channel %d: %v", item.ChannelID, err)
        msg := fmt.Sprintf("channel not found: %v", err)
        r.iter.SkipFor(item, sticky, item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), msg)
        return nil, err
    }
    
    return r.buildRealAttempt(channel, item, sticky, stickyKeyID)
}

func (r *relayRun) buildRealAttempt(
    channel *dbmodel.Channel,
    item dbmodel.GroupItem,
    sticky bool,
    stickyKeyID int,
) (*relayAttempt, error) {
    if !channel.Enabled {
        r.iter.SkipFor(item, sticky, channel.ID, 0, channel.Name, "channel disabled")
        return nil, nil
    }
    
    // ... 现有 buildRealAttempt 逻辑保持不变
}
```

**验证**：
- `go build ./internal/relay` 通过
- 删除虚拟渠道相关测试用例

#### 步骤 2.4：删除保护检查更新

**文件**：`internal/op/group.go`

**操作**：更新分组删除保护逻辑

```go
// 删除旧的虚拟渠道引用检查
- // 检查是否有虚拟渠道引用此分组
- for _, ch := range channelCache.GetAll() {
-     if ch.IsVirtual && ch.VirtualTargetGroupID == id {
-         return fmt.Errorf("cannot delete: referenced by virtual channel %s", ch.Name)
-     }
- }

// 新增分组成员引用检查
+ // 检查是否有其他分组成员引用此分组
+ refs := findReferencingGroupItems(id)
+ if len(refs) > 0 {
+     groupNames := make([]string, 0, len(refs))
+     for _, gid := range refs {
+         if g, ok := groupCache.Get(gid); ok {
+             groupNames = append(groupNames, g.Name)
+         }
+     }
+     return fmt.Errorf("cannot delete: referenced by groups: %s", strings.Join(groupNames, ", "))
+ }

// 新增辅助函数
func findReferencingGroupItems(targetGroupID int) []int {
    refGroups := make(map[int]struct{})
    for _, group := range groupCache.GetAll() {
        for _, item := range group.Items {
            if item.Type == model.GroupItemTypeGroup && item.TargetGroupID == targetGroupID {
                refGroups[group.ID] = struct{}{}
            }
        }
    }
    result := make([]int, 0, len(refGroups))
    for gid := range refGroups {
        result = append(result, gid)
    }
    return result
}
```

**验证**：
- 添加测试：尝试删除被引用的分组应失败

---

### 🎨 里程碑 M3：前端 UI 重构（3 小时）

#### 步骤 3.1：删除 Channel 虚拟渠道 UI

**文件**：`web/src/components/modules/channel/Form.tsx`

**操作**：删除虚拟渠道相关代码

**删除内容**：
- 虚拟渠道开关（Switch）
- `handleVirtualChange` 函数
- `isVirtual` 状态
- 虚拟渠道配置区域（目标分组、模型过滤）
- 条件渲染逻辑

**简化后**：Form 只保留普通渠道配置（Base URL、API Key、模型等）

**验证**：
- `npm run build` 通过
- TypeScript 无错误

#### 步骤 3.2：添加 Group 成员类型选择

**文件**：`web/src/components/modules/group/Editor.tsx`

**操作**：添加成员类型选择 UI

**新增组件**（伪代码）：
```tsx
// 成员添加表单
<Dialog open={addDialogOpen} onOpenChange={setAddDialogOpen}>
    <DialogContent>
        <DialogTitle>添加成员</DialogTitle>
        
        {/* 成员类型选择 */}
        <Select value={memberType} onValueChange={setMemberType}>
            <SelectTrigger>
                <SelectValue placeholder="选择成员类型" />
            </SelectTrigger>
            <SelectContent>
                <SelectItem value="channel">渠道</SelectItem>
                <SelectItem value="group">分组</SelectItem>
            </SelectContent>
        </Select>
        
        {/* 渠道选择（memberType === "channel"）*/}
        {memberType === "channel" && (
            <>
                <ChannelSelect
                    value={selectedChannelID}
                    onChange={setSelectedChannelID}
                />
                <Input
                    label="模型名称"
                    value={modelName}
                    onChange={(e) => setModelName(e.target.value)}
                    placeholder="如 gpt-4, claude-opus-4-8"
                />
            </>
        )}
        
        {/* 分组选择（memberType === "group"）*/}
        {memberType === "group" && (
            <GroupSelect
                value={selectedGroupID}
                onChange={setSelectedGroupID}
                excludeGroups={[currentGroupID]}  // 防止自引用
                hint="选择要嵌套的分组"
            />
        )}
        
        {/* 优先级和权重 */}
        <Input type="number" label="优先级" value={priority} />
        <Input type="number" label="权重" value={weight} />
        
        <Button onClick={handleAddMember}>添加</Button>
    </DialogContent>
</Dialog>
```

**API 调用**：
```tsx
const handleAddMember = async () => {
    const payload = {
        type: memberType,
        ...(memberType === "channel" ? {
            channel_id: selectedChannelID,
            model_name: modelName,
        } : {
            target_group_id: selectedGroupID,
        }),
        priority,
        weight,
    };
    
    await addGroupItem(currentGroupID, payload);
    setAddDialogOpen(false);
    refetch();
};
```

**验证**：
- UI 正确显示类型选择
- 提交时 payload 结构正确

#### 步骤 3.3：更新成员列表展示

**文件**：`web/src/components/modules/group/ItemList.tsx`

**操作**：支持显示嵌套分组成员

**新增组件**（伪代码）：
```tsx
<GroupMemberList>
    {members.map(member => (
        member.type === "channel" || !member.type ? (
            <ChannelMemberItem key={member.id} member={member} />
        ) : (
            <NestedGroupMemberItem key={member.id} member={member} />
        )
    ))}
</GroupMemberList>

// 嵌套分组成员显示
function NestedGroupMemberItem({ member }) {
    const { data: targetGroup } = useGroupQuery(member.target_group_id);
    
    return (
        <div className="flex items-center gap-2 p-3 border rounded-lg">
            <Layers className="size-5 text-purple-500" />  {/* 嵌套图标 */}
            <div className="flex-1">
                <div className="font-medium">{targetGroup?.name || `分组 ${member.target_group_id}`}</div>
                <div className="text-xs text-muted-foreground">
                    嵌套分组 · 优先级 {member.priority} · 权重 {member.weight}
                </div>
            </div>
            <Button variant="ghost" size="icon" onClick={() => handleRemove(member.id)}>
                <X className="size-4" />
            </Button>
        </div>
    );
}
```

**验证**：
- 渠道和分组成员正确区分显示
- 嵌套分组显示目标分组名称

#### 步骤 3.4：更新 API 类型定义

**文件**：`web/src/api/endpoints/group.ts`

**操作**：扩展 GroupItem 类型

```tsx
export interface GroupItem {
    id: number;
    group_id: number;
    type?: "channel" | "group";  // 新增
    channel_id?: number;
    target_group_id?: number;     // 新增
    model_name?: string;
    priority: number;
    weight: number;
    disabled: boolean;
}

export interface GroupItemAddRequest {
    type?: "channel" | "group";  // 新增
    channel_id?: number;
    target_group_id?: number;     // 新增
    model_name?: string;
    priority: number;
    weight: number;
}
```

**验证**：
- TypeScript 编译通过
- 类型定义与后端一致

---

### 🧪 里程碑 M4：测试与验证（2 小时）

#### 步骤 4.1：单元测试

**新增测试文件**：`internal/op/group_test.go`

测试用例：
1. `TestExpandGroupItems_ChannelOnly` - 只包含渠道成员
2. `TestExpandGroupItems_OneLevel` - 单层嵌套（A → B）
3. `TestExpandGroupItems_TwoLevels` - 两层嵌套（A → B → C）
4. `TestExpandGroupItems_ThreeLevels` - 最大深度（A → B → C → D）
5. `TestExpandGroupItems_ExceedDepth` - 超过深度限制应报错
6. `TestExpandGroupItems_CircularSelf` - 自引用（A → A）
7. `TestExpandGroupItems_CircularTwo` - 两层循环（A → B → A）
8. `TestExpandGroupItems_DisabledGroup` - 禁用分组被过滤
9. `TestExpandGroupItems_DisabledChannel` - 禁用渠道被过滤
10. `TestExpandGroupItems_MixedMembers` - 混合渠道和分组成员

**运行**：
```bash
go test ./internal/op -run TestExpandGroupItems -v
```

#### 步骤 4.2：集成测试

**测试场景**：
1. 创建嵌套分组结构（claude-opus-4-8 → gpt-5.5）
2. 发起请求 `claude-opus-4-8`
3. 验证调度流程：
   - 尝试 claude-opus-4-8 渠道
   - 失败后尝试 gpt-5.5 渠道
   - 成功返回 `{"model": "gpt-5.5"}`（无模型恢复）
4. 修改 gpt-5.5 分组（添加/删除渠道）
5. 再次请求，验证实时同步生效

**运行**：
```bash
go test ./internal/relay -run TestNestedGroupScheduling -v
```

#### 步骤 4.3：性能基准测试

**测试文件**：`internal/op/group_bench_test.go`

```go
func BenchmarkExpandGroupItems(b *testing.B) {
    // 准备测试数据：3 层嵌套，每层 5 个成员
    setupNestedGroups()
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = expandEnabledGroup(rootGroup)
    }
}
```

**目标**：
- 单次展开 < 0.5ms
- 内存分配最小化

**运行**：
```bash
go test ./internal/op -bench=BenchmarkExpandGroupItems -benchmem
```

---

### 📚 里程碑 M5：文档更新（0.5 小时）

#### 步骤 5.1：删除虚拟渠道文档

**删除文件**：
- `.claude/virtual-channel-user-guide.md`
- `.claude/virtual-channel-summary.md`
- `.claude/integration-test-checklist.md`（虚拟渠道相关部分）

#### 步骤 5.2：添加分组嵌套文档

**新建文件**：`.claude/nested-group-user-guide.md`

**内容大纲**：
1. 概述：分组嵌套功能介绍
2. 快速开始：如何添加嵌套分组
3. 典型场景：
   - 主备降级（claude-opus-4-8 → gpt-5.5）
   - 多层降级（主 → 备 → 本地）
4. 配置详解：成员类型、优先级、权重
5. 监控和调试：日志查看、问题排查
6. 最佳实践：命名规范、深度限制、循环避免

#### 步骤 5.3：更新 API 文档

更新分组 API 文档，说明：
- `GroupItem.type` 字段
- `GroupItem.target_group_id` 字段
- 嵌套分组的创建和管理

---

## 📊 关键文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/model/group.go` | 修改 | 扩展 GroupItem 结构（Type, TargetGroupID） |
| `internal/op/group.go` | 修改 | 添加 expandGroupItems 递归展开逻辑 |
| `internal/op/group_test.go` | 新增 | 单元测试（10 个用例） |
| `internal/relay/relay.go` | 删除 | 删除虚拟渠道逻辑（~200 行） |
| `internal/server/handlers/group.go` | 修改 | 添加 GroupItem 类型验证 |
| `web/src/components/modules/channel/Form.tsx` | 删除 | 删除虚拟渠道 UI（~150 行） |
| `web/src/components/modules/group/Editor.tsx` | 修改 | 添加成员类型选择 |
| `web/src/components/modules/group/ItemList.tsx` | 修改 | 支持嵌套分组显示 |
| `web/src/api/endpoints/group.ts` | 修改 | 扩展 GroupItem 类型定义 |
| `migrations/000025_group_nesting.up.sql` | 新增 | 数据库迁移脚本 |
| `migrations/000025_group_nesting.down.sql` | 新增 | 数据库回滚脚本 |

---

## ⚠️ 风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| 循环检测漏洞 | High | Low | 单元测试覆盖所有循环场景 + 前端预检查 |
| 性能未达预期 | Medium | Low | 基准测试验证 + 监控 |
| 数据迁移失败 | High | Low | 提供回滚脚本 + 测试环境验证 |
| 用户学习成本 | Low | Medium | 详细文档 + UI 引导提示 |

---

## 📈 验收标准

### 功能验收
- ✅ 用户可以在分组中添加"子分组"作为成员
- ✅ 子分组的渠道自动参与父分组的调度
- ✅ 循环引用检测生效（A → B → A 被拒绝）
- ✅ 最大嵌套深度限制生效（超过 3 层报错）
- ✅ 实时同步：修改子分组后，父分组自动生效
- ✅ 跨模型降级：claude-opus-4-8 失败 → gpt-5.5 自动接管

### 性能验收
- ✅ 单请求嵌套分组展开 < 0.5ms
- ✅ 无响应模型恢复开销（~0.8ms）
- ✅ P99 延迟降低 > 30%

### 质量验收
- ✅ Go 后端测试覆盖率 > 80%
- ✅ 前端 TypeScript 编译无错误
- ✅ 所有现有测试通过
- ✅ 新增 10 个分组嵌套测试用例全部通过

---

## 🔑 SESSION_ID（供 /ccg:execute 使用）

- **CODEX_SESSION**: `019e9bce-3dc3-7220-acc1-b385b6ea1c7a`
- **GEMINI_SESSION**: （调用失败，无 SESSION_ID）

---

## 📝 备注

**Codex 分析亮点**：
- ✅ 识别了 `GroupID` 字段冲突，建议使用 `TargetGroupID`
- ✅ 提出在 `op.GroupGetEnabledMap` 层完成展开（架构清晰）
- ✅ 强调保持当前调度逻辑（不过滤模型名）
- ✅ 提供了完整的伪代码和测试策略

**Gemini 调用失败原因**：
- 模型 `gemini-3.1-pro-preview` 不存在（404 错误）
- 前端 UI 部分由 Claude 基于项目经验补充

---

**计划生成完成时间**：2026-06-06  
**预计总耗时**：10 小时（1.3 天）

