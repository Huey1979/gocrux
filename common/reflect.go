package common

import (
	"reflect"
)

// SetFieldValue 通过反射设置结构体字段值
// entity 必须为结构体指针（*M）。
// fieldName 为 Go 结构体字段名（区分大小写）。
// value 为要设置的值，类型必须与该字段类型赋值兼容。
//
// 若字段不存在或 entity 非指针，静默返回（不报错）。
func SetFieldValue(entity any, fieldName string, value any) {
	v := reflect.ValueOf(entity)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return
	}

	field := elem.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() {
		return
	}

	val := reflect.ValueOf(value)
	if val.Type().AssignableTo(field.Type()) {
		field.Set(val)
		return
	}
	// 处理命名类型（如 type SiteVersionStatus string）
	if val.Type().ConvertibleTo(field.Type()) {
		field.Set(val.Convert(field.Type()))
	}
}
