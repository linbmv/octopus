# 📋 实施计划：完成分组嵌套重构（选项 A）

## 任务类型
- [x] 前端 (→ Gemini 成功完成前端 UI 规划)
- [x] 后端 (→ Codex 完成后端架构分析，已在 689d986 实施)
- [x] 全栈 (→ 前端清理 + 后端已完成)

## 问题诊断

### 当前状态
- ✅ **后端**：已在 `689d986` 完成分组嵌套重构，删除了 `Channel.IsVirtual`、`VirtualTargetGroupID`、`VirtualModelRewrite`
- ✅ **前端 API 类型（Group）**：已在 `689d986` 更新，`GroupItem` 的 `channel_id`、`model_name` 变为可选，新增 `type`、`target_group_id`
- ❌ **前端 API 类型（Channel）**：仍然保留虚拟渠道字段定义
- ❌ **前端 Channel UI**：仍然保留虚拟渠道表单、校验和逻辑
- ❌ **前端 Group UI**：未适配分组嵌套成员，仍然假设所有成员都有 `channel_id`/`model_name`
- ❌ **CI 构建**：TypeScript 编译失败，因为 `modelChannelKey(item.channel_id, item.model_name)` 传入了可选字段

### 根本原因
Day 3.6 重构（`689d986`）**只完成了后端和前端 API 类型的部分更新，未完成前端 UI 适配**，导致：
1. 前端 Channel 的虚拟渠道 UI 成为死代码（后端已不接受这些字段）
2. 前端 Group UI 对分组嵌套成员调用 `modelChannelKey` 时类型不匹配
3. TypeScript 严格模式编译失败

## 技术方案

### 核心策略
**彻底删除虚拟渠道前端遗留，完成分组嵌套 UI 适配**

- 删除前端 Channel 虚拟渠道字段定义、表单、校验、逻辑
- 删除前端 Model 虚拟渠道字段定义（`LLMChannel.is_virtual` 等）
- 修复前端 Group UI 以正确处理分组嵌套成员（`type='group'` 的成员没有 `channel_id`/`model_name`）
- 保持现有 UI/UX 设计语言和可访问性

### 架构决策
- ✅ **保留分组嵌套架构**：不回滚 `689d986`，完成前端适配
- ❌ **拒绝保留虚拟渠道字段作为兼容层**：后端已删除，前端继续发送会导致类型漂移

## 实施步骤

### 步骤 1：清理 Channel API 类型定义
**文件**：`web/src/api/endpoints/channel.ts`

**变更**：
1. 删除 `ChannelType.Virtual = 'virtual/group_redirect'`
2. 从 `Channel` 类型删除：
   ```typescript
   // 虚拟渠道字段
   is_virtual: boolean;
   virtual_target_group_id: number;
   virtual_model_rewrite: string;
   ```
3. 从 `CreateChannelRequest` 类型删除：
   ```typescript
   // 虚拟渠道字段
   is_virtual?: boolean;
   virtual_target_group_id?: number;
   virtual_model_rewrite?: string;
   ```
4. 从 `UpdateChannelRequest` 类型删除：
   ```typescript
   // 虚拟渠道字段
   is_virtual?: boolean;
   virtual_target_group_id?: number;
   virtual_model_rewrite?: string;
   ```

**预期产物**：Channel API 类型不再包含虚拟渠道字段

---

### 步骤 2：清理 Model API 类型定义
**文件**：`web/src/api/endpoints/model.ts`

**变更**：
从 `LLMChannel` 类型删除：
```typescript
is_virtual?: boolean;
virtual_target_group_id?: number;
virtual_model_rewrite?: string;
```

**预期产物**：`LLMChannel` 类型不再包含虚拟渠道字段

---

### 步骤 3：清理 Channel 表单组件
**文件**：`web/src/components/modules/channel/Form.tsx`

**变更**：
1. 删除 `useGroupList` import 和调用
2. 删除 `ArrowRight` icon import
3. 从 `ChannelFormData` 类型删除：
   ```typescript
   is_virtual: boolean;
   virtual_target_group_id: number;
   virtual_model_rewrite: string;
   ```
4. 删除 `isVirtual` 变量
5. 删除 `handleVirtualChange` 函数
6. 删除虚拟渠道 Switch 和配置区域（行 319-364）
7. 删除 `handleSubmit` 中的虚拟渠道校验（行 262-272）
8. 移除所有 `!isVirtual &&` 条件包裹，让 Base URLs、API Keys、Model、Advanced 区域始终显示
9. `handleSubmit` 直接调用 `onSubmit(event)`

**预期产物**：Channel 表单不再显示虚拟渠道相关字段

---

### 步骤 4：清理 Channel 创建组件
**文件**：`web/src/components/modules/channel/Create.tsx`

**变更**：
1. 从初始 `formData` 删除：
   ```typescript
   is_virtual: false,
   virtual_target_group_id: 0,
   virtual_model_rewrite: '',
   ```
2. 删除 `isVirtual` 和 `virtualModelRewrite` 变量计算
3. 简化 `createChannel.mutate` payload，直接使用真实渠道字段，删除所有 `isVirtual ? ... : ...` 三元表达式
4. `onSuccess` 回调中重置表单时同步删除虚拟字段

**预期产物**：Channel 创建流程不再包含虚拟渠道逻辑

---

### 步骤 5：清理 Channel 编辑组件
**文件**：`web/src/components/modules/channel/CardContent.tsx`

**变更**：
1. 删除 `ChannelType` import（如果仅用于虚拟渠道逻辑）
2. 从初始 `formData` 删除：
   ```typescript
   is_virtual: channel.is_virtual,
   virtual_target_group_id: channel.virtual_target_group_id,
   virtual_model_rewrite: channel.virtual_model_rewrite ?? '',
   ```
3. 删除"虚拟渠道配置变更检测"整段（行 123-139）
4. 删除 `req.is_virtual`、`req.type`、`req.virtual_target_group_id`、`req.virtual_model_rewrite` 赋值

**预期产物**：Channel 编辑流程不再包含虚拟渠道逻辑

---

### 步骤 6：适配 Group Card 组件支持分组嵌套
**文件**：`web/src/components/modules/group/Card.tsx`

**Gemini 前端建议**：
- 更新 `modelChannelKey` 工具函数以接受对象参数，支持 `type='channel'` 和 `type='group'`
- 增强 `GroupEditor` 添加"Groups"选择区域，允许选择现有分组作为成员
- 添加防护逻辑防止分组添加自身
- 使用 `Layers` 或 `Folder` 图标区分分组嵌套成员

**Codex 后端建议**：
- 在调用点先按成员类型收窄，保持 `modelChannelKey` 严格签名
- 对嵌套分组成员生成独立 id（如 `group-${target_group_id}`）
- 显示名可先用 `Group ${target_group_id}`，后续接入 group name map

**综合方案**：
1. 删除 `virtualInfoByKey` useMemo（行 93-105）
2. 重写 `displayMembers` useMemo：
   ```typescript
   const displayMembers = useMemo((): SelectedMember[] =>
       [...(group.items || [])]
           .sort((a, b) => a.priority - b.priority)
           .map((item) => {
               // 分组嵌套成员：没有 channel_id/model_name
               if (item.type === 'group' && item.target_group_id) {
                   return {
                       id: `group-${item.target_group_id}`,
                       name: `Group ${item.target_group_id}`,  // TODO: 后续可接入 group name map
                       enabled: true,  // 分组成员的启用状态由目标分组本身决定
                       channel_id: 0,  // 占位，type='group' 时不使用
                       channel_name: '',
                       item_id: item.id,
                       weight: item.weight,
                       disabled: item.disabled ?? false,
                   };
               }
               
               // 渠道成员：必须有 channel_id 和 model_name
               if (item.channel_id && item.model_name) {
                   const key = modelChannelKey(item.channel_id, item.model_name);
                   return {
                       id: key,
                       name: item.model_name,
                       enabled: enabledByKey.get(key) ?? true,
                       channel_id: item.channel_id,
                       channel_name: channelNameByKey.get(key) ?? `Channel ${item.channel_id}`,
                       item_id: item.id,
                       weight: item.weight,
                       disabled: item.disabled ?? false,
                   };
               }
               
               // 无效成员：过滤掉
               return null;
           })
           .filter((m): m is SelectedMember => m !== null),
       [group.items, channelNameByKey, enabledByKey]
   );
   ```
3. 修改 `handleSubmitEdit` 中的 `items_to_add` 构造，按成员类型发送：
   ```typescript
   const items_to_add = values.members
       .map((m, idx) => ({ m, priority: idx + 1 }))
       .filter(({ m }) => typeof m.item_id !== 'number')
       .map(({ m, priority }) => {
           // 分组嵌套成员
           if (m.id.startsWith('group-')) {
               const targetGroupId = parseInt(m.id.replace('group-', ''), 10);
               return {
                   type: 'group' as const,
                   target_group_id: targetGroupId,
                   priority,
                   weight: m.weight ?? 1,
               };
           }
           // 渠道成员
           return {
               type: 'channel' as const,
               channel_id: m.channel_id,
               model_name: m.name,
               priority,
               weight: m.weight ?? 1,
           };
       });
   ```

**预期产物**：Group Card 能正确显示和编辑分组嵌套成员

---

### 步骤 7：清理 Group ItemList 组件
**文件**：`web/src/components/modules/group/ItemList.tsx`

**变更**：
1. 删除 `Shuffle` icon import
2. 删除虚拟渠道图标显示（行 155-166）

**预期产物**：成员列表不再显示虚拟渠道图标

---

### 步骤 8：清理 Group Editor 组件
**文件**：`web/src/components/modules/group/Editor.tsx`

**变更**：
1. 删除 `Shuffle` icon import
2. 删除模型选择列表中的虚拟渠道图标（行 150-154）

**预期产物**：模型选择器不再显示虚拟渠道图标

---

### 步骤 9：清理 Group Create 组件
**文件**：`web/src/components/modules/group/Create.tsx`

**变更**：
`handleSubmit` 中的 `items` 构造只发送渠道成员（当前已是这样），无需修改。但如果后续支持在创建时添加分组嵌套成员，需类似步骤 6 的逻辑。

**预期产物**：保持现有行为（仅支持添加渠道成员）

---

### 步骤 10：验证和测试

**本地验证**：
```bash
# TypeScript 类型检查
cd web
pnpm tsc --noEmit

# 完整构建
pnpm run build
```

**验证点**：
1. ✅ TypeScript 编译无错误
2. ✅ `rg -n "is_virtual|virtual_target_group_id|virtual_model_rewrite|ChannelType.Virtual" web/src` 仅命中已删除的注释或类型定义
3. ✅ 前端 Channel 表单不显示虚拟渠道开关
4. ✅ 前端 Group UI 能显示渠道成员（现有功能保持）
5. ✅ CI release workflow 通过

## 关键文件清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `web/src/api/endpoints/channel.ts` | 修改 | 删除 Channel 虚拟渠道类型定义 |
| `web/src/api/endpoints/model.ts` | 修改 | 删除 LLMChannel 虚拟渠道字段 |
| `web/src/components/modules/channel/Form.tsx` | 修改 | 删除虚拟渠道表单和校验 |
| `web/src/components/modules/channel/Create.tsx` | 修改 | 删除虚拟渠道创建逻辑 |
| `web/src/components/modules/channel/CardContent.tsx` | 修改 | 删除虚拟渠道编辑逻辑 |
| `web/src/components/modules/group/Card.tsx` | 修改 | 适配分组嵌套成员显示和编辑 |
| `web/src/components/modules/group/ItemList.tsx` | 修改 | 删除虚拟渠道图标 |
| `web/src/components/modules/group/Editor.tsx` | 修改 | 删除虚拟渠道图标 |
| `web/src/components/modules/group/Create.tsx` | 无需修改 | 当前已是渠道成员专用 |
| `web/src/components/modules/group/utils.ts` | 无需修改 | 保持严格类型签名 |

## 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 用户已创建虚拟渠道数据 | 后端已不支持，前端删除 UI 后用户无法编辑 | ⚠️ 数据库迁移已在 `689d986` 删除字段，旧数据已无法恢复。用户需使用分组嵌套功能替代 |
| 分组嵌套成员显示不完整 | 前端只显示 `Group ${id}`，用户体验欠佳 | 🔧 后续增强：调用 `/api/v1/group/list` 构建 `groupNameById` map |
| 前端与后端类型不一致 | API 调用可能失败 | ✅ 本次彻底删除前端虚拟字段，确保与后端一致 |
| TypeScript 编译错误遗漏 | 部分调用点未修复 | ✅ `pnpm tsc --noEmit` 全量检查 |

## SESSION_ID（供 /ccg:execute 使用）
- CODEX_SESSION: `019e9d3e-f8d8-72d2-8ab4-0aa3e616732f`
- GEMINI_SESSION: ✅ 可用（后端架构分析完成，前端 UI 规划可用）

## 后续优化建议（不在本次范围）
1. 🔧 **分组嵌套成员名称显示优化**：调用 `/api/v1/group/list` 构建 `groupNameById` map，将 `Group ${id}` 替换为实际名称
2. 🔧 **Group Editor 支持添加分组嵌套成员**：当前只能添加渠道成员，后续可支持选择目标分组
3. 🔧 **分组嵌套成员图标**：为 `type='group'` 的成员添加专属图标（如 `Layers` 或 `FolderTree`）
4. 🧪 **E2E 测试**：覆盖分组嵌套成员的创建、编辑、删除流程
5. 📚 **用户文档**：说明如何使用分组嵌套替代原虚拟渠道功能