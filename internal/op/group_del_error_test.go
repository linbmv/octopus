package op

import (
	"context"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

// TestGroupDelReferencedErrorMessage 验证删除被引用的分组时错误信息是否友好清晰
func TestGroupDelReferencedErrorMessage(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	// 创建 child 分组
	child := &model.Group{Name: "child-group", Enabled: true, Mode: 1}
	if err := GroupCreate(child, ctx); err != nil {
		t.Fatalf("创建 child 分组失败: %v", err)
	}
	childID := child.ID

	// 创建 parent 分组并引用 child
	parent := &model.Group{Name: "parent-group", Enabled: true, Mode: 1}
	if err := GroupCreate(parent, ctx); err != nil {
		t.Fatalf("创建 parent 分组失败: %v", err)
	}

	// 通过 GroupUpdate 添加 item 引用
	updateReq := &model.GroupUpdateRequest{
		ID: parent.ID,
		ItemsToAdd: []model.GroupItemAddRequest{
			{
				Type:          model.GroupItemTypeGroup,
				TargetGroupID: childID,
				Priority:      1,
			},
		},
	}
	if _, err := GroupUpdate(updateReq, ctx); err != nil {
		t.Fatalf("添加 parent->child 引用失败: %v", err)
	}
	parentID := parent.ID

	// 尝试删除 child（被 parent 引用）
	err := GroupDel(childID, ctx)
	if err == nil {
		t.Fatal("删除被引用的分组应该失败，但成功了")
	}

	// 验证错误信息包含引用者的名称
	errMsg := err.Error()
	if !strings.Contains(errMsg, "parent-group") {
		t.Errorf("错误信息应包含引用者名称 'parent-group'，实际: %s", errMsg)
	}
	if !strings.Contains(errMsg, "无法删除") {
		t.Errorf("错误信息应包含 '无法删除'，实际: %s", errMsg)
	}
	if !strings.Contains(errMsg, "引用") {
		t.Errorf("错误信息应包含 '引用'，实际: %s", errMsg)
	}

	// 验证删除 parent 后可以删除 child
	if err := GroupDel(parentID, ctx); err != nil {
		t.Fatalf("删除 parent 失败: %v", err)
	}
	if err := GroupDel(childID, ctx); err != nil {
		t.Fatalf("删除 child 失败（parent 已删）: %v", err)
	}
}

// TestGroupDelMultipleReferencersErrorMessage 验证多个分组引用时的错误信息
func TestGroupDelMultipleReferencersErrorMessage(t *testing.T) {
	initTestDB(t)
	ctx := context.Background()

	// 创建一个被多个分组引用的 child
	child := &model.Group{Name: "shared-child", Enabled: true, Mode: 1}
	if err := GroupCreate(child, ctx); err != nil {
		t.Fatalf("创建 child 失败: %v", err)
	}
	childID := child.ID

	// 创建两个 parent，都引用同一个 child
	for i := 1; i <= 2; i++ {
		parent := &model.Group{
			Name:    strings.Join([]string{"parent", string(rune('0' + i))}, "-"),
			Enabled: true,
			Mode:    1,
		}
		if err := GroupCreate(parent, ctx); err != nil {
			t.Fatalf("创建 parent-%d 失败: %v", i, err)
		}

		updateReq := &model.GroupUpdateRequest{
			ID: parent.ID,
			ItemsToAdd: []model.GroupItemAddRequest{
				{
					Type:          model.GroupItemTypeGroup,
					TargetGroupID: childID,
					Priority:      1,
				},
			},
		}
		if _, err := GroupUpdate(updateReq, ctx); err != nil {
			t.Fatalf("添加 parent-%d->child 引用失败: %v", i, err)
		}
	}

	// 尝试删除 child
	err := GroupDel(childID, ctx)
	if err == nil {
		t.Fatal("删除被多个分组引用的 child 应该失败")
	}

	errMsg := err.Error()
	// 应该提到被 2 个分组引用
	if !strings.Contains(errMsg, "2") && !strings.Contains(errMsg, "两") {
		t.Errorf("错误信息应提到引用数量，实际: %s", errMsg)
	}
	// 应该列出所有引用者
	if !strings.Contains(errMsg, "parent-1") || !strings.Contains(errMsg, "parent-2") {
		t.Errorf("错误信息应列出所有引用者，实际: %s", errMsg)
	}
}
