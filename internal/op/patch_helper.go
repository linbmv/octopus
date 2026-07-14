package op

import (
	"reflect"
)

// PatchHelper 通用 PATCH 字段更新助手，用于减少重复的 if nil 判断逻辑
type PatchHelper struct {
	selectFields []string
	updates      map[string]interface{}
}

// NewPatchHelper 创建新的 PATCH 助手
func NewPatchHelper() *PatchHelper {
	return &PatchHelper{
		selectFields: make([]string, 0, 20),
		updates:      make(map[string]interface{}),
	}
}

// ApplyField 应用单个字段更新（指针类型）
// fieldName: 数据库字段名（snake_case）
// value: 指针类型的值，nil 表示不更新
func (p *PatchHelper) ApplyField(fieldName string, value interface{}) {
	if value == nil {
		return
	}

	// 使用反射获取指针指向的实际值
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Ptr {
		// 非指针类型直接使用
		p.selectFields = append(p.selectFields, fieldName)
		p.updates[fieldName] = value
		return
	}

	if rv.IsNil() {
		return
	}

	// 解引用指针
	p.selectFields = append(p.selectFields, fieldName)
	p.updates[fieldName] = rv.Elem().Interface()
}

// SelectFields 返回需要更新的字段列表
func (p *PatchHelper) SelectFields() []string {
	return p.selectFields
}

// Updates 返回更新的字段和值映射
func (p *PatchHelper) Updates() map[string]interface{} {
	return p.updates
}

// HasUpdates 判断是否有字段需要更新
func (p *PatchHelper) HasUpdates() bool {
	return len(p.selectFields) > 0
}
