package relay

import (
	"context"
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
)

// groupPathItem 保存一次叶子选路经过的每一层分组成员。
type groupPathItem struct {
	group model.Group
	item  model.GroupItem
}

// groupLeaf 是嵌套分组最终解析出的渠道模型成员。
type groupLeaf struct {
	group model.Group
	item  model.GroupItem
	path  []groupPathItem
}

// pickGroupLeaf 按每层分组自己的模式解析到渠道模型叶子。
// 写入侧会拒绝环和超深图；visited 与深度仍作为运行时纵深保护，防止脏数据无限递归。
func pickGroupLeaf(ctx context.Context, root model.Group) (*groupLeaf, error) {
	return pickGroupLeafAt(ctx, root, 0, map[int]struct{}{})
}

func pickGroupLeafAt(ctx context.Context, group model.Group, depth int, visited map[int]struct{}) (*groupLeaf, error) {
	return pickGroupLeafAtWithSkipped(ctx, group, depth, visited, nil)
}

// pickGroupLeafSkipping is used by one request to skip a candidate that is
// temporarily ineligible because of a circuit or passive slow-recovery
// backoff. The skip is not persisted in RouteState and therefore does not turn
// a cross-request health decision into a group-member failure.
func pickGroupLeafSkipping(ctx context.Context, root model.Group, skipped map[int]struct{}) (*groupLeaf, error) {
	if skipped == nil {
		skipped = make(map[int]struct{})
	}
	return pickGroupLeafAtWithSkipped(ctx, root, 0, map[int]struct{}{}, skipped)
}

func pickGroupLeafAtWithSkipped(ctx context.Context, group model.Group, depth int, visited, externalSkipped map[int]struct{}) (*groupLeaf, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !group.Enabled {
		return nil, nil
	}
	if depth > op.MaxGroupNestDepth {
		return nil, fmt.Errorf("nested group depth exceeded (max %d)", op.MaxGroupNestDepth)
	}
	if _, exists := visited[group.ID]; exists {
		return nil, fmt.Errorf("circular group reference detected at group %d", group.ID)
	}
	nextVisited := make(map[int]struct{}, len(visited)+1)
	for id := range visited {
		nextVisited[id] = struct{}{}
	}
	nextVisited[group.ID] = struct{}{}

	// 每次上游失败都会让故障转移分组冷却当前成员；配置上暂时不可用的
	// 嵌套项只在本次展开中跳过，最多检查一次当前快照中的每个成员。
	skipped := make(map[int]struct{}, len(externalSkipped))
	for itemID := range externalSkipped {
		skipped[itemID] = struct{}{}
	}
	for checked := 0; checked < len(group.Items); checked++ {
		item := pickGroupItemSkipping(group, skipped)
		if item.ID == 0 {
			return nil, nil
		}
		switch item.Type {
		case "", model.GroupItemTypeChannelModel:
			if item.ChannelModelID == nil || item.ChannelModel == nil {
				if group.Mode == model.GroupModeManual {
					return nil, nil
				}
				recordRouteFailure(group, item.ID, group.RelayConfig.MemberMaxAttempts)
				continue
			}
			return &groupLeaf{
				group: group,
				item:  item,
				path:  []groupPathItem{{group: group, item: item}},
			}, nil

		case model.GroupItemTypeGroup:
			if item.TargetGroupID == nil {
				if group.Mode == model.GroupModeManual {
					return nil, nil
				}
				recordRouteFailure(group, item.ID, group.RelayConfig.MemberMaxAttempts)
				continue
			}
			child, err := op.GroupGetByID(*item.TargetGroupID)
			if err == nil && child.Enabled {
				var leaf *groupLeaf
				leaf, err = pickGroupLeafAtWithSkipped(ctx, child, depth+1, nextVisited, externalSkipped)
				if err == nil && leaf != nil {
					leaf.path = append([]groupPathItem{{group: group, item: item}}, leaf.path...)
					return leaf, nil
				}
			}
			if err != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if group.Mode == model.GroupModeManual {
				return nil, nil
			}
			if err == nil {
				// Disabled or temporarily exhausted child groups are configuration
				// skips, not upstream failures; do not cool the parent member.
				skipped[item.ID] = struct{}{}
				if externalSkipped != nil {
					externalSkipped[item.ID] = struct{}{}
				}
				continue
			}
			// 嵌套目标不可用时立即让出该父成员，继续父分组中的下一项。
			recordRouteFailure(group, item.ID, group.RelayConfig.MemberMaxAttempts)

		default:
			if group.Mode == model.GroupModeManual {
				return nil, nil
			}
			recordRouteFailure(group, item.ID, group.RelayConfig.MemberMaxAttempts)
		}
	}
	return nil, nil
}

func recordRouteSuccessPath(path []groupPathItem) {
	for i := len(path) - 1; i >= 0; i-- {
		recordRouteSuccess(path[i].group, path[i].item.ID)
	}
}

func releaseRouteProbePath(path []groupPathItem) {
	for i := len(path) - 1; i >= 0; i-- {
		releaseRouteProbe(path[i].group, path[i].item.ID)
	}
}

// recordRouteFailurePath records a failed leaf and, when that leaf belongs to
// a manual nested group, bubbles the failure through manual ancestors until a
// failover group can switch away from the nested member. A failover child owns
// its own retry/cooldown decisions; its parent is notified later if the child
// has no usable member left.
func recordRouteFailurePath(path []groupPathItem, failures int) bool {
	if len(path) == 0 {
		return false
	}
	leaf := path[len(path)-1]
	leafCooled := recordRouteFailure(leaf.group, leaf.item.ID, failures)
	if leaf.group.Mode != model.GroupModeManual {
		return leafCooled
	}

	for i := len(path) - 2; i >= 0; i-- {
		ancestor := path[i]
		if ancestor.group.Mode == model.GroupModeManual {
			// Manual groups have no alternate member of their own, but an
			// enclosing failover group may still need to switch away.
			recordRouteFailure(ancestor.group, ancestor.item.ID, ancestor.group.RelayConfig.MemberMaxAttempts)
			continue
		}
		return recordRouteFailure(ancestor.group, ancestor.item.ID, ancestor.group.RelayConfig.MemberMaxAttempts)
	}
	return false
}
