# 分组嵌套重构 - 最终需求文档

生成时间：2026-06-06  
基于：与用户的深入技术交流  
状态：需求已确认

---

## 🎯 最高准则

**用户核心诉求（优先级）**：
1. **稳定性第一** - 架构清晰、概念纯粹、无歧义
2. **性能第二** - 运行效率高、无额外开销
3. **扩展维护第三** - 代码简洁、易理解、易扩展

**决策原则**：
- ✅ 不考虑开发难度和时长
- ✅ 追求长期收益而非短期便利
- ✅ 宁可重构也不妥协于次优方案

---

## 📋 核心需求

### 目标

**将虚拟渠道实现重构为分组嵌套，利用现有分组功能实现更简洁的架构。**

**关键洞察**（来自用户）：
> "分组已经具备大部分功能，只是作为分组成员被引用而已"

---

## 🔬 当前系统分析

### 现有调度逻辑（关键发现）

```go
// 当前系统的模型匹配逻辑
GroupGetEnabledMap("claude-opus-4-8") {
    // 1. 通过分组名查找分组
    group := groupMap.Get("claude-opus-4-8")
    
    // 2. 过滤禁用成员（不过滤模型名！）
    filterEnabledGroupItems(group) {
        for item in group.Items {
            if !item.Disabled && channel.Enabled {
                enabledItems.append(item)  // ← 保留所有模型的渠道
            }
        }
    }
    
    // 3. Iterator 遍历所有启用成员
    // 包括 claude-opus-4-8 渠道和 gpt-5.5 渠道
}
```

**关键特性**：
- ✅ **成员不按模型名过滤** - 分组内可包含不同模型的渠道
- ✅ **已支持跨模型降级** - claude-opus-4-8 失败 → gpt-5.5 自动接管
- ✅ **缓存机制完善** - groupCache 自动失效和重新加载

### 用户实际使用场景

```
claude-opus-4-8 分组（当前结构）
├─ 1. claude-opus-4-8 (Anyrouter_claude)      ← 主渠道
├─ 2. claude-opus-4-8 (Linuxdo_官方公益_claude) ← 主渠道
├─ 3. gpt-5.5 (Anyrouter_codex)              ← 降级渠道
├─ 4. gpt-5.5 (Linuxdo_官方公益_codex)        ← 降级渠道
├─ 5. gpt-5.5 (Linuxdo_WONG)                 ← 降级渠道
├─ 6. gpt-5.5 (Linuxdo_阿里狗路由)           ← 降级渠道
└─ 7. gpt-5.5 (Linuxdo_Dream) [禁用]         ← 降级渠道
```

**用户反馈**：
> "gpt-5.5 经常被调用到"（证明跨模型降级已在使用）

---

## 🎯 重构目标

### 从

```
Channel (真实) + Channel (虚拟 → Group)
          ↓
    概念不纯粹、逻辑复杂
```

### 到

```
Group → [Channel, Channel, Group → [...]]
          ↓
    概念纯粹、逻辑简单
```

---

## 📊 技术方案

### 1. 数据模型改动

#### 扩展 GroupItem 类型

```go
// 现有
type GroupItem struct {
    ChannelID int    // 引用 Channel
    ModelName string
    Priority  int
    Weight    int
    Disabled  bool
}

// 重构后
type GroupItem struct {
    Type      string  // "channel" | "group" ← 新增
    ChannelID *int    // Type="channel" 时使用
    GroupID   *int    // Type="group" 时使用 ← 新增
    ModelName string  // Type="channel" 时使用（保持不变）
    Priority  int
    Weight    int
    Disabled  bool
}
```

**关键点**：
- ✅ **不需要 ModelRewrite 字段** - 保持当前调度逻辑（不过滤模型名）
- ✅ **ChannelID 和 GroupID 互斥** - 通过 Type 字段区分
- ✅ **向后兼容** - Type="channel" 时行为与现有完全一致

### 2. 调度逻辑改动

#### 递归展开嵌套分组

```go
// 新增：展开嵌套分组为扁平成员列表
func expandGroupItems(groupID int, visited map[int]bool, depth int) []model.GroupItem {
    // 循环检测
    if visited[groupID] {
        log.Warnf("circular reference detected: group %d", groupID)
        return nil
    }
    
    // 深度限制
    if depth > maxNestedDepth {
        log.Warnf("max nested depth exceeded: group %d", groupID)
        return nil
    }
    
    visited[groupID] = true
    defer func() { delete(visited, groupID) }()
    
    group := getGroup(groupID)
    var items []model.GroupItem
    
    for _, member := range group.Items {
        if member.Type == "channel" {
            // 直接添加渠道成员
            items = append(items, member)
        } else if member.Type == "group" {
            // 递归展开子分组
            subItems := expandGroupItems(member.GroupID, visited, depth+1)
            items = append(items, subItems...)
        }
    }
    
    return items
}

// 修改：GroupGetEnabledMap 使用展开后的成员
func GroupGetEnabledMap(name string, ctx context.Context) (model.Group, error) {
    group, ok := groupMap.Get(name)
    if !ok {
        return model.Group{}, fmt.Errorf("group not found")
    }
    
    // 展开嵌套分组
    visited := make(map[int]bool)
    expandedItems := expandGroupItems(group.ID, visited, 0)
    
    // 过滤禁用成员（保持现有逻辑）
    group.Items = expandedItems
    return filterEnabledGroupItems(group), nil
}
```

**关键特性**：
- ✅ **保持当前调度逻辑** - 展开后就是普通的成员列表
- ✅ **循环检测简单** - 只需检查 GroupID
- ✅ **深度限制** - maxNestedDepth = 3
- ✅ **缓存友好** - 每次请求都重新展开（实时同步）

### 3. 前端 UI 改动

#### 分组成员选择器

```tsx
// 添加成员时选择类型
<Select
    label="成员类型"
    value={memberType}
    onChange={(type) => setMemberType(type)}
>
    <Option value="channel">渠道</Option>
    <Option value="group">分组</Option>
</Select>

{memberType === "channel" ? (
    <ChannelSelect
        value={channelID}
        onChange={setChannelID}
    />
) : (
    <GroupSelect
        value={groupID}
        onChange={setGroupID}
        excludeGroups={[currentGroupID]}  // 防止自引用
    />
)}
```

#### 成员列表展示

```tsx
// 树形展示嵌套分组
<GroupMemberList>
    {members.map(member => 
        member.type === "channel" ? (
            <ChannelItem channel={member} />
        ) : (
            <NestedGroupItem group={member}>
                {/* 可展开显示子分组内容（只读预览）*/}
            </NestedGroupItem>
        )
    )}
</GroupMemberList>
```

---

## 📋 实施范围

### 包含（In Scope）

1. **删除虚拟渠道代码**
   - ❌ `Channel.IsVirtual`
   - ❌ `Channel.VirtualTargetGroupID`
   - ❌ `Channel.VirtualModelRewrite`
   - ❌ `resolveVirtualChannel()`
   - ❌ `restoreResponseModel()`
   - ❌ `virtualResolveState`

2. **添加分组嵌套支持**
   - ✅ `GroupItem.Type`
   - ✅ `GroupItem.GroupID`
   - ✅ `expandGroupItems()`
   - ✅ 循环检测（单层，只检查 GroupID）
   - ✅ 深度限制（maxNestedDepth = 3）

3. **前端 UI 改动**
   - ❌ 删除 Channel 表单的虚拟渠道区域
   - ✅ 添加 Group 表单的成员类型选择
   - ✅ 添加嵌套分组预览（树形展示）

4. **数据迁移**
   - ❌ 删除虚拟渠道数据（无生产数据，直接清空）
   - ✅ 数据库迁移：修改 group_items 表结构

### 不包含（Out of Scope）

- ❌ 成员级模型重写（Phase 2 可选）
- ❌ 超过 3 层嵌套
- ❌ 虚拟渠道向后兼容

---

## ✅ 验收标准

### 功能验收

1. ✅ 用户可以在分组中添加"子分组"作为成员
2. ✅ 子分组的渠道自动参与父分组的调度
3. ✅ 循环引用检测生效（A → B → A 被拒绝）
4. ✅ 最大嵌套深度限制生效（超过 3 层报错）
5. ✅ 实时同步：修改子分组后，父分组自动生效
6. ✅ 跨模型降级：claude-opus-4-8 失败 → gpt-5.5 自动接管

### 性能验收

1. ✅ 单请求嵌套分组展开 < 0.5ms
2. ✅ 相比虚拟渠道，P99 延迟降低 > 30%
3. ✅ 无响应模型恢复开销（~0.8ms）

### 质量验收

1. ✅ Go 后端测试覆盖率 > 80%
2. ✅ 前端 TypeScript 编译无错误
3. ✅ 所有现有测试通过
4. ✅ 新增 5 个分组嵌套测试用例全部通过

---

## 🎯 关键决策记录

### Q1: 是否需要成员级模型重写？

**决策**：❌ **不需要**

**理由**：
- 当前系统已支持跨模型调度（成员不按模型名过滤）
- 展开嵌套分组后，保持每个成员的原始模型名
- 调度器自动处理跨模型降级

### Q2: 最大嵌套深度？

**决策**：✅ **3 层**

**理由**：
- 覆盖 95% 业务场景（主 → 备 → 本地）
- 循环检测开销最小
- 符合"稳定第一"准则

### Q3: 数据迁移策略？

**决策**：✅ **清空重建**

**理由**：
- 虚拟渠道刚开发完成，无生产数据
- 直接删除虚拟渠道字段和数据

---

## 📊 代码改动预估

| 模块 | 删除 | 新增 | 净变化 |
|------|------|------|--------|
| 后端数据模型 | 30 行 | 20 行 | -10 行 |
| 后端调度器 | 200 行 | 80 行 | -120 行 |
| 前端 Channel 表单 | 150 行 | 0 行 | -150 行 |
| 前端 Group 表单 | 0 行 | 100 行 | +100 行 |
| 测试用例 | 15 个 | 5 个 | -10 个 |
| **总计** | **380 行** | **200 行** | **-180 行** ⭐ |

**代码量减少 ~30%！**

---

## ⏱️ 预计开发时间

| 里程碑 | 交付物 | 预计耗时 |
|--------|--------|----------|
| M1: 数据模型重构 | GroupItem 扩展 + 数据库迁移 | 1.5 小时 |
| M2: 后端调度器重构 | 递归展开 + 循环检测 + 删除虚拟渠道逻辑 | 3 小时 |
| M3: 前端 UI 重构 | 成员类型选择 + 树形展示 + 删除虚拟渠道 UI | 3 小时 |
| M4: 测试与验证 | 单元测试 + 集成测试 + 性能基准测试 | 2 小时 |
| M5: 文档更新 | 删除虚拟渠道文档 + 添加分组嵌套文档 | 0.5 小时 |
| **总计** | | **10 小时（1.3 天）** |

---

## 🔐 风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| 循环检测漏洞 | High | Low | 单元测试覆盖所有循环场景 + 前端预检查 |
| 性能未达预期 | Medium | Low | 基准测试验证 + 性能监控 |
| 缓存一致性问题 | Medium | Low | 复用现有 groupCache 机制 |
| 用户学习成本 | Low | Medium | 详细文档 + UI 引导 |

---

## 📚 参考文档

- [虚拟渠道用户指南](.claude/virtual-channel-user-guide.md)（待删除）
- [虚拟渠道代码审查报告](.claude/code-review-report.md)
- [增强需求文档](.claude/plan/refactor-to-nested-group-enhanced-requirements.md)

---

**需求确认完成，准备进入上下文检索和技术方案生成阶段。**
