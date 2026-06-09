package handler

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// RequestFactory — 按操作的 Request 构造器
//
// 每个操作可能需要不同的 Request 类型，因此按操作分离。
// 各方法返回零值指针，供 json.Unmarshal 使用。
// nil 表示该操作不需要具体 Request 类型（fallback 到 MapRequest[M]）。
// ============================================================

// RequestFactory 每个操作的 Request 构造器。
type RequestFactory[M service.Record] struct {
	// Create 创建操作的 Request 构造器。
	Create func() service.CrudRequest[M]

	// Update 编辑操作的 Request 构造器（可能与 Create 相同）。
	Update func() service.CrudRequest[M]

	// List 列表查询的 Request 构造器。
	List func() any
}

// ============================================================
// MapRequest — 泛型请求适配器
//
// 将 Handler 层绑定的 map[string]any (JSON) 适配为 Service 层所需的 CrudRequest[M]。
// 当 HandlerConfig.ReqFactory 为空时使用此适配器作为兜底。
// ============================================================

// MapRequest 实现 CrudRequest[M]，内部存储原始 map 数据。
// GetID 支持 "id" / "ulid" 两种常见键名。
// MergeTo 通过 JSON 往返将 map 字段合并到实体。
// IdempotencyKey 支持幂等创建，从 data["idempotency_key"] 提取。
type MapRequest[M service.Record] struct {
	data             map[string]any
	idempotencyKey   string // 从 data["idempotency_key"] 提取并缓存
	idemKeyExtracted bool   // 是否已提取过
}

// GetID 提取主键值。
// 按优先级查找：id → ulid → ID → ULID
func (r *MapRequest[M]) GetID() any {
	for _, key := range []string{"id", "ulid", "ID", "ULID"} {
		if v, ok := r.data[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

// GetIdempotencyKey 从 data 中提取幂等键（仅提取一次）。
// 幂等键名：idempotency_key
func (r *MapRequest[M]) GetIdempotencyKey() string {
	if r.idemKeyExtracted {
		return r.idempotencyKey
	}
	r.idemKeyExtracted = true
	if v, ok := r.data["idempotency_key"]; ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			r.idempotencyKey = s
		}
	}
	return r.idempotencyKey
}

// MergeTo 将 map 中的数据合并到目标实体。
// 通过 JSON 序列化/反序列化实现字典→结构体的自动映射，
// 利用结构体的 json tag 自动处理 snake_case ↔ PascalCase 转换。
func (r *MapRequest[M]) MergeTo(target *M) error {
	return mergeByJSON(r.data, target)
}

// Validate MapRequest 无法校验 schema，始终通过。
// 具体校验应由业务 Request 类型（通过 ReqFactory 注入）的 Validate() 完成。
func (r *MapRequest[M]) Validate() error {
	return nil
}

// ============================================================
// mergeByJSON — 通过 JSON 中介实现 map → struct 合并
//
// map 中没有的字段保留 target 原有值（非零值覆盖），
// 这与 GORM Updates(map) 的行为一致。
// ============================================================

// mergeByJSON 将 map 合并到 struct，不存在的字段保留原值。
// 先读取 target 完整 JSON → 用 map 覆盖 → 反序列化回 target。
func mergeByJSON[M any](m map[string]any, target *M) error {
	// 1. 序列化当前实体
	current, err := json.Marshal(target)
	if err != nil {
		return err
	}

	// 2. 反序列化为 map
	var currentMap map[string]any
	if err := json.Unmarshal(current, &currentMap); err != nil {
		return err
	}

	// 3. 用请求 map 覆盖
	for k, v := range m {
		currentMap[k] = v
	}

	// 4. 重新序列化 & 反序列化
	merged, err := json.Marshal(currentMap)
	if err != nil {
		return err
	}
	return json.Unmarshal(merged, target)
}

// ============================================================
// mergeMapToStructFlat — 直接反射合并（备选，用于不需要 JSON tag 的场景）
// ============================================================

// mergeMapToStructFlat 通过反射直接将 map 键值对设置到 struct 对应字段。
// 支持 snake_case → PascalCase 转换和 json tag 匹配。
func mergeMapToStructFlat(target any, m map[string]any) {
	v := reflect.ValueOf(target).Elem()
	t := v.Type()

	for key, val := range m {
		field := findField(v, t, key)
		if field.IsValid() && field.CanSet() {
			setFieldValue(field, val)
		}
	}
}

// findField 在 struct 中查找与 map key 对应的字段。
// 优先按 json tag 匹配，其次按 PascalCase / snake_case 推断。
func findField(v reflect.Value, t reflect.Type, name string) reflect.Value {
	// 解引用：避免对指针值调用 FieldByName
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}

	// 1. 按 json tag 匹配
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		tagName := strings.Split(tag, ",")[0]
		if tagName == name {
			return v.Field(i)
		}
	}

	// 2. 按 PascalCase 推断（snake_case → PascalCase）
	pascal := toPascal(name)
	return v.FieldByName(pascal)
}

// toPascal snake_case → PascalCase
func toPascal(s string) string {
	parts := strings.Split(s, "_")
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// setFieldValue 将任意值设置到 reflect.Value。
// 处理 string / float64 → int / bool 等常见 JSON 反序列化类型转换。
func setFieldValue(field reflect.Value, val any) {
	if val == nil {
		return
	}

	rv := reflect.ValueOf(val)
	ft := field.Type()

	// 类型一致 → 直接赋值
	if rv.Type().AssignableTo(ft) {
		field.Set(rv)
		return
	}

	// string → string
	if ft.Kind() == reflect.String && rv.Kind() == reflect.String {
		field.SetString(rv.String())
		return
	}

	// float64 → int / int64 / uint / int8 ...
	if rv.Kind() == reflect.Float64 {
		switch ft.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			field.SetInt(int64(rv.Float()))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			field.SetUint(uint64(rv.Float()))
		case reflect.Float32, reflect.Float64:
			field.SetFloat(rv.Float())
		}
		return
	}

	// bool
	if ft.Kind() == reflect.Bool && rv.Kind() == reflect.Bool {
		field.SetBool(rv.Bool())
		return
	}

	// 最终兜底：尝试通过 JSON 序列化转换
	if data, err := json.Marshal(val); err == nil {
		tmp := reflect.New(ft)
		if json.Unmarshal(data, tmp.Interface()) == nil {
			field.Set(tmp.Elem())
		}
	}
}
