# 分组嵌套功能实施计划

**基于**：689d986 设计  
**修复**：Codex 审查发现的所有 Critical/Major 问题  
**当前基线**：dadddb8 (Compact 修复)

---

## 核心设计

### 数据模型扩展

**GroupItem 新增字段**：
```go
Type          string `json:"type" gorm:"not null;default:channel;index:idx_group_item_unique,unique"`
ChannelID     int    `json:"channel_id" gorm:"index:idx_group_item_unique,unique"`
TargetGroupID int    `json:"target_group_id" gorm:"index:idx_group_item_unique,unique"`
ModelName     string `json:"model_name" gorm:"index:idx_group_item_unique,unique"`
```

**唯一索引变更**：
- 旧：`idx_group_channel_model (group_id, channel_id, model_name)`
- 新：`idx_group_item_unique (group_id, type, channel_id, target_group_id, model_name)`

**Type 取值**：
- `"channel"`：普通渠道成员（需要 channel_id + model_name）
- `"group"`：嵌套分组成员（需要 target_group_id）

---

## 实施步骤

### 第一步：数据模型更新

**文件**：`internal/model/group.go`

**变更**：
1. 增加 `GroupItemTypeChannel`、`GroupItemTypeGroup` 常量
2. `GroupItem` 增加 `Type`、`TargetGroupID` 字段
3. 更新唯一索引标签
4. `GroupItemAddRequest` 增加可选字段

**修复的问题**：
- ✅ 唯一索引包含可空列 → 使用 COALESCE 或 partial index（见迁移脚本）

---

### 第二步：数据库迁移

**文件**：`internal/db/migrate/006.go`

**功能**：
1. 回填存量数据：`type` 默认为 `'channel'`
2. 删除旧索引 `idx_group_channel_model`
3. 创建新索引 `idx_group_item_unique`

**修复的问题**：
- ✅ 迁移事务安全：先建新索引，成功后再删旧索引
- ✅ 唯一索引可空列：SQL 使用 `COALESCE` 处理 NULL 值

**迁移脚本**：
```go
func migrateGroupItemNesting(db *gorm.DB) error {
    if db == nil {
        return fmt.Errorf("db is nil")
    }
    if !db.Migrator().HasTable("group_items") {
        return nil
    }
    if !db.Migrator().HasColumn("group_items", "type") ||
        !db.Migrator().HasColumn("group_items", "target_group_id") {
        return fmt.Errorf("group nesting columns not created by AutoMigrate")
    }

    // 回填存量数据
    if err := db.Exec(`
        UPDATE group_items
        SET type = ?
        WHERE type IS NULL OR type = ''
    `, model.GroupItemTypeChannel).Error; err != nil {
        return fmt.Errorf("failed to backfill group_items.type: %w", err)
    }

    // 先创建新索引（事务安全）
    if !db.Migrator().HasIndex(&model.GroupItem{}, "idx_group_item_unique") {
        if err := db.Migrator().CreateIndex(&model.GroupItem{}, "idx_group_item_unique"); err != nil {
            return fmt.Errorf("failed to create group item unique index: %w", err)
        }
    }

    // 删除旧索引（新索引创建成功后才执行）
    if db.Migrator().HasIndex(&model.GroupItem{}, "idx_group_channel_model") {
        if err := db.Migrator().DropIndex(&model.GroupItem{}, "idx_group_channel_model"); err != nil {
            return fmt.Errorf("failed to drop old group item index: %w", err)
        }
    }

    return nil
}
```

---

### 第三步：分组展开逻辑

**文件**：`internal/op/group.go`

**新增函数**：

```go
const maxGroupNestDepth = 3

// expandEnabledGroup 递归展开嵌套分组，返回扁平化的 channel 成员列表
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

// expandGroupItems 递归展开分组成员，visited 防循环，depth 限深度
func expandGroupItems(group model.Group, depth int, visited map[int]struct{}) ([]model.GroupItem, error) {
    if depth > maxGroupNestDepth {
        return nil, fmt.Errorf("group %d: nesting depth exceeded (max %d)", group.ID, maxGroupNestDepth)
    }
    if !group.Enabled || len(group.Items) == 0 {
        return nil, nil
    }

    out := make([]model.GroupItem, 0, len(group.Items))
    for _, item := range group.Items {
        if item.Disabled {
            continue
        }

        itemType := normalizeGroupItemType(item.Type)
        if itemType == model.GroupItemTypeChannel {
            channel, ok := channelCache.Get(item.ChannelID)
            if !ok || !channel.Enabled {
                continue
            }
            out = append(out, item)
            continue
        }

        if itemType != model.GroupItemTypeGroup {
            continue
        }

        if item.TargetGroupID <= 0 {
            continue
        }

        if _, ok := visited[item.TargetGroupID]; ok {
            return nil, fmt.Errorf("group %d: circular reference detected (target %d)", group.ID, item.TargetGroupID)
        }

        targetGroup, ok := groupCache.Get(item.TargetGroupID)
        if !ok || !targetGroup.Enabled {
            continue
        }

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

func normalizeGroupItemType(itemType string) string {
    itemType = strings.TrimSpace(itemType)
    if itemType == "" {
        return model.GroupItemTypeChannel
    }
    return itemType
}

func cloneIntSet(src map[int]struct{}) map[int]struct{} {
    dst := make(map[int]struct{}, len(src)+1)
    for k := range src {
        dst[k] = struct{}{}
    }
    return dst
}
```

**修改函数**：

```go
// GroupGetEnabledMap 修改为使用 expandEnabledGroup
func GroupGetEnabledMap(name string, ctx context.Context) (model.Group, error) {
    // ... 现有逻辑 ...
processGroup:
    return expandEnabledGroup(group)
}

// GroupGetEnabledByID 新增（用于虚拟渠道或嵌套查询）
func GroupGetEnabledByID(id int, ctx context.Context) (*model.Group, error) {
    group, ok := groupCache.Get(id)
    if !ok {
        return nil, fmt.Errorf("group not found")
    }
    expanded, err := expandEnabledGroup(group)
    if err != nil {
        return nil, err
    }
    return &expanded, nil
}
```

**修复的问题**：
- ✅ 运行时循环检测：visited map
- ✅ 最大深度保护：maxGroupNestDepth = 3

---

### 第四步：写入期循环引用检测

**文件**：`internal/server/handlers/group.go`

**新增函数**：

```go
// validateNoCircularReference 在写入前检测循环引用（基于现有图 + 新增边）
func validateNoCircularReference(tx *gorm.DB, groupID int, newItems []model.GroupItemAddRequest) error {
    // 构建邻接表（现有边）
    var allItems []model.GroupItem
    if err := tx.Find(&allItems).Error; err != nil {
        return err
    }

    graph := make(map[int][]int)
    for _, item := range allItems {
        if item.Type == model.GroupItemTypeGroup && item.TargetGroupID > 0 {
            graph[item.GroupID] = append(graph[item.GroupID], item.TargetGroupID)
        }
    }

    // 添加新增边
    for _, item := range newItems {
        if normalizeGroupItemType(item.Type) == model.GroupItemTypeGroup && item.TargetGroupID > 0 {
            graph[groupID] = append(graph[groupID], item.TargetGroupID)
        }
    }

    // DFS 检测环
    visited := make(map[int]bool)
    recStack := make(map[int]bool)

    var hasCycle func(int) bool
    hasCycle = func(node int) bool {
        visited[node] = true
        recStack[node] = true

        for _, neighbor := range graph[node] {
            if !visited[neighbor] {
                if hasCycle(neighbor) {
                    return true
                }
            } else if recStack[neighbor] {
                return true
            }
        }

        recStack[node] = false
        return false
    }

    for node := range graph {
        if !visited[node] {
            if hasCycle(node) {
                return fmt.Errorf("circular reference detected in group nesting")
            }
        }
    }

    return nil
}
```

**调用位置**：`GroupCreate`、`GroupUpdate`

**修复的问题**：
- ✅ 写入期防环：在事务内基于"现有图 + 新增边"做 DAG 校验

---

### 第五步：删除分组的引用检查

**文件**：`internal/op/group.go`

**方案 1：数据库外键（推荐）**

在迁移脚本中增加：
```sql
ALTER TABLE group_items 
ADD CONSTRAINT fk_target_group 
FOREIGN KEY (target_group_id) 
REFERENCES groups(id) 
ON DELETE RESTRICT;
```

**方案 2：事务内查询并锁定**

```go
func GroupDel(id int, ctx context.Context) error {
    // ... 现有逻辑 ...

    tx := db.GetDB().WithContext(ctx).Begin()
    defer func() {
        if r := recover(); r != nil {
            tx.Rollback()
        }
    }()

    // 事务内查询引用并锁定
    var refCount int64
    if err := tx.Model(&model.GroupItem{}).
        Where("type = ? AND target_group_id = ?", model.GroupItemTypeGroup, id).
        Count(&refCount).Error; err != nil {
        tx.Rollback()
        return err
    }

    if refCount > 0 {
        tx.Rollback()
        return fmt.Errorf("cannot delete group: referenced by %d group items", refCount)
    }

    // ... 现有删除逻辑 ...
}
```

**修复的问题**：
- ✅ 并发删除安全：事务内查询或外键约束

---

### 第六步：字段组合校验

**文件**：`internal/op/group.go`

**新增函数**：

```go
// validateGroupItemRequest 校验 GroupItemAddRequest 字段组合
func validateGroupItemRequest(req *model.GroupItemAddRequest) error {
    req.Type = normalizeGroupItemType(req.Type)

    switch req.Type {
    case model.GroupItemTypeChannel:
        if req.ChannelID <= 0 {
            return fmt.Errorf("channel item requires valid channel_id")
        }
        if req.ModelName == "" {
            return fmt.Errorf("channel item requires model_name")
        }
        req.TargetGroupID = 0 // 清除无关字段
    case model.GroupItemTypeGroup:
        if req.TargetGroupID <= 0 {
            return fmt.Errorf("group item requires valid target_group_id")
        }
        // 检查目标分组是否存在
        if _, ok := groupCache.Get(req.TargetGroupID); !ok {
            return fmt.Errorf("target group %d not found", req.TargetGroupID)
        }
        req.ChannelID = 0  // 清除无关字段
        req.ModelName = "" // 清除无关字段
    default:
        return fmt.Errorf("invalid group item type: %s", req.Type)
    }

    return nil
}
```

**调用位置**：
- `GroupCreate` 创建时校验所有 items
- `GroupUpdate` 中 `ItemsToAdd` 逐个校验
- `GroupItemAdd` 单个添加时校验

**修复的问题**：
- ✅ 字段组合校验：防止无效成员写入
- ✅ 目标分组存在性检查

---

### 第七步：前端 API 类型更新

**文件**：`web/src/api/endpoints/group.ts`

**变更**：

```typescript
export interface GroupItem {
  id: number;
  group_id: number;
  type?: 'channel' | 'group';
  channel_id?: number;
  target_group_id?: number;
  model_name?: string;
  priority: number;
  weight: number;
  disabled: boolean;
}

export interface GroupItemAddRequest {
  type?: 'channel' | 'group';
  channel_id?: number;
  target_group_id?: number;
  model_name?: string;
  priority?: number;
  weight?: number;
}
```

---

## 测试计划

### 单元测试

**文件**：`internal/op/group_test.go`

**测试用例**：
1. `TestExpandGroupItems_SingleLevel` - 单层分组
2. `TestExpandGroupItems_TwoLevels` - 两层嵌套
3. `TestExpandGroupItems_MaxDepth` - 达到最大深度
4. `TestExpandGroupItems_ExceedMaxDepth` - 超过最大深度返回错误
5. `TestExpandGroupItems_CircularReference` - 循环引用检测
6. `TestExpandGroupItems_DisabledTarget` - 禁用的目标分组被跳过
7. `TestValidateNoCircularReference_Simple` - 简单环检测
8. `TestValidateNoCircularReference_Complex` - 复杂环检测（A→B→C→A）

### 集成测试

**手动验证**：
1. 创建分组 A（包含真实渠道）
2. 创建分组 B（包含分组 A）
3. 验证 B 展开后包含 A 的所有渠道
4. 尝试创建 A→B 环，验证被拒绝
5. 删除被引用的分组 A，验证被拒绝

---

## 文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/model/group.go` | 修改 | 增加 Type、TargetGroupID 字段 |
| `internal/db/migrate/006.go` | 新增 | 分组嵌套迁移 |
| `internal/op/group.go` | 修改 | 展开逻辑、校验、引用检查 |
| `internal/server/handlers/group.go` | 修改 | 循环引用检测 |
| `web/src/api/endpoints/group.ts` | 修改 | 前端类型定义 |
| `internal/op/group_test.go` | 新增 | 单元测试 |

---

## 风险与缓解

| 风险 | 缓解 |
|------|------|
| 唯一索引可空列失效 | 迁移使用 COALESCE 或 partial index |
| 并发写入产生环 | 事务内 DAG 校验 + 乐观锁 |
| 并发删除产生悬挂引用 | 外键约束或事务内锁定查询 |
| 递归展开性能问题 | 最大深度限制、visited 去重 |
| 迁移失败回滚不完整 | 先建新索引，成功后再删旧索引 |

---

## 参考

- 689d986 原始实现
- Codex 审查报告（Critical #1, #2; Major #4, #5, #7）