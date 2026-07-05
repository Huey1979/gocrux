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
	for elem.Kind() == reflect.Ptr {
		elem = elem.Elem()
	}
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
		return
	}

	// 数值兼容：bool ↔ int(X)
	if field.Type().Kind() >= reflect.Int && field.Type().Kind() <= reflect.Int64 {
		if b, ok := value.(bool); ok {
			if b {
				field.SetInt(1)
			} else {
				field.SetInt(0)
			}
			return
		}
	}
	if field.Type().Kind() == reflect.Bool {
		switch v := value.(type) {
		case int, int8, int16, int32, int64:
			field.SetBool(reflect.ValueOf(v).Int() != 0)
			return
		}
	}

	// 值 → 指针：field 是指针类型，val 是值类型
	if field.Kind() == reflect.Ptr && val.Type().AssignableTo(field.Type().Elem()) {
		ptr := reflect.New(field.Type().Elem())
		ptr.Elem().Set(val)
		field.Set(ptr)
		return
	}

	// 指针 → 值：field 是值类型，val 是指针类型
	if field.Kind() != reflect.Ptr && val.Kind() == reflect.Ptr {
		if val.Elem().Type().AssignableTo(field.Type()) {
			field.Set(val.Elem())
			return
		}
	}
}
