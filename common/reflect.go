package common

import (
	"encoding/json"
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
	SetReflectField(field, value)
}

// SetReflectField 将任意值设置到已解析的 reflect.Value 字段。
// 支持类型赋值、命名类型转换、bool↔int、float64↔int/uint、值↔指针互转及 JSON 兜底转换。
func SetReflectField(field reflect.Value, value any) {
	if value == nil {
		return
	}

	val := reflect.ValueOf(value)
	ft := field.Type()

	// 类型一致 → 直接赋值
	if val.Type().AssignableTo(ft) {
		field.Set(val)
		return
	}
	// 处理命名类型（如 type SiteVersionStatus string）
	if val.Type().ConvertibleTo(ft) {
		field.Set(val.Convert(ft))
		return
	}

	// 数值兼容：bool ↔ int(X)
	if ft.Kind() >= reflect.Int && ft.Kind() <= reflect.Int64 {
		if b, ok := value.(bool); ok {
			if b {
				field.SetInt(1)
			} else {
				field.SetInt(0)
			}
			return
		}
	}
	if ft.Kind() == reflect.Bool {
		switch v := value.(type) {
		case int, int8, int16, int32, int64:
			field.SetBool(reflect.ValueOf(v).Int() != 0)
			return
		}
	}

	// float64 → int / uint（JSON 反序列化常见场景）
	if val.Kind() == reflect.Float64 {
		switch ft.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			field.SetInt(int64(val.Float()))
			return
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			field.SetUint(uint64(val.Float()))
			return
		case reflect.Float32, reflect.Float64:
			field.SetFloat(val.Float())
			return
		}
	}

	// string → string
	if ft.Kind() == reflect.String && val.Kind() == reflect.String {
		field.SetString(val.String())
		return
	}

	// bool
	if ft.Kind() == reflect.Bool && val.Kind() == reflect.Bool {
		field.SetBool(val.Bool())
		return
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

	// 最终兜底：尝试通过 JSON 序列化转换
	if data, err := json.Marshal(value); err == nil {
		tmp := reflect.New(ft)
		if json.Unmarshal(data, tmp.Interface()) == nil {
			field.Set(tmp.Elem())
		}
	}
}
