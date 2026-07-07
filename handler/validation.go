package handler

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Huey1979/gocrux/common"
	errs "github.com/Huey1979/gocrux/errors"
)

// ============================================================
// 1. 校验规则类型
// ============================================================

// FieldRule 单字段校验规则。
type FieldRule struct {
	Type      string   `json:"type" yaml:"type"`             // 期望类型：string, int, float, bool, time
	Required  bool     `json:"required" yaml:"required"`     // 是否必填
	Min       *float64 `json:"min" yaml:"min"`               // 数值下限
	Max       *float64 `json:"max" yaml:"max"`               // 数值上限
	MinLength *int     `json:"min_length" yaml:"min_length"` // 字符串最小长度
	MaxLength *int     `json:"max_length" yaml:"max_length"` // 字符串最大长度
	Enum      []string `json:"enum" yaml:"enum"`             // 枚举值
	Pattern   string   `json:"pattern" yaml:"pattern"`       // 正则
	Format    string   `json:"format" yaml:"format"`         // 内置格式：datetime/date/time/email/url/phone/ulid
}

// EndpointRules 某接口的字段级规则集。
type EndpointRules map[string]*FieldRule

// ValidateConfig 校验配置（按操作拆分）。
type ValidateConfig struct {
	Create *EndpointRules `json:"create" yaml:"create"`
	Update *EndpointRules `json:"update" yaml:"update"`
	List   *EndpointRules `json:"list" yaml:"list"`
}

// ============================================================
// 1.5 批量错误类型
// ============================================================

// BatchError 单条数据的校验错误详情。
type BatchError struct {
	Index   int    // 数据在批量请求中的位置（0-based）
	Field   string // 出错字段名（"" 表示非字段级错误，如唯一性冲突）
	Message string // 错误描述
}

// BatchErrors 批量错误集合，实现 error 接口。
type BatchErrors struct {
	Errors []BatchError
}

// Error 返回所有错误的汇总信息。
func (e *BatchErrors) Error() string {
	if len(e.Errors) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("共 %d 条数据校验失败:", len(e.Errors)))
	for i, be := range e.Errors {
		if be.Field != "" {
			sb.WriteString(fmt.Sprintf("\n  [%d] 第%d条 %s: %s", i+1, be.Index+1, be.Field, be.Message))
		} else {
			sb.WriteString(fmt.Sprintf("\n  [%d] 第%d条: %s", i+1, be.Index+1, be.Message))
		}
	}
	return sb.String()
}

// isFrameworkMetaParam 判断是否为框架控制参数（不计入业务校验）。
func isFrameworkMetaParam(key string) bool {
	switch key {
	case "page", "page_size", "order_by", "order_dir",
		"depth", "fdepth", "fstop",
		"ignore", "ignoreRef", "ignoreCascade", "ignoreAll",
		"follow_published":
		return true
	}
	return false
}

// ============================================================
// 2. 从 entity struct 自动推导字段规则
// ============================================================

// frameworkManagedCols 由框架自动管理的字段（SetID/SetDefaults/审计等），
// 禁止从 gorm not null 自动推导 Required=true，避免用户被迫传入无需关心的字段。
var frameworkManagedCols = map[string]bool{
	"is_deleted":     true, // SetDelete() 管理
	"is_current":     true, // SetDefaults() 管理
	"version_code":   true, // Service._beforeCreate 自动设 v1.0
	"version_status": true, // SetDefaults() 管理
	"version_remark": true, // 版本说明，可为空
	"created_by":     true, // 审计字段
	"created_at":     true, // 审计字段
	"updated_by":     true, // 审计字段
	"updated_at":     true, // 审计字段
	"published_by":   true, // 版本发布审计
	"published_at":   true, // 版本发布审计
}

// deriveFieldRules 从 entity struct M 反射推导默认 FieldRule。
// - 从 gorm 标签提取 db column 名 + type/size/not null 等约束
// - 从 Go 类型推导期望类型
// - ULID 类型自动 max_length=26 + format=ulid，且排除 Required
// - 框架管理字段（_ulid/is_deleted/version_*/审计字段）不设 Required
// 仅做类型约束，不做业务级必填/枚举/正则。
func deriveFieldRules[M any]() EndpointRules {
	var m M
	t := reflect.TypeOf(m)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	rules := make(EndpointRules, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		colName := gormColumn(sf)
		if colName == "" || colName == "-" {
			continue
		}

		rule := &FieldRule{}

		// 类型推导
		rule.Type = goKindToRuleType(sf.Type)

		// JSON 原生类型检测：json.RawMessage 字段自动标记 format=json
		if sf.Type == reflect.TypeOf(json.RawMessage{}) {
			rule.Type = "string"
			rule.Format = "json"
		}

		// 主键/ULID 字段：自动 max_length=26 + format=ulid，不设为 Required
		// （SetID() 自动生成主键，用户无需传入）
		isPK := strings.Contains(sf.Tag.Get("gorm"), "primaryKey")
		if isPK && strings.HasSuffix(colName, "_ulid") && rule.Type == "string" {
			rule.MaxLength = IntPtr(26)
			rule.Format = "ulid"
		} else if strings.HasSuffix(colName, "_ulid") && rule.Type == "string" {
			rule.MaxLength = IntPtr(26)
		} else {
			// gorm 标签约束（仅对非 ULID 字段生效）
			gormTag := sf.Tag.Get("gorm")
			if gormTag != "" {
				// size:N → MaxLength（仅 string 类型）
				if size := parseGormSize(gormTag); size > 0 && rule.Type == "string" {
					rule.MaxLength = IntPtr(size)
				}

				// not null → Required
				if strings.Contains(gormTag, "not null") {
					rule.Required = true
				}

				// default:NULL / default:null → 抵消 not null
				if hasGormDefaultNull(gormTag) {
					rule.Required = false
				}

				// type: 解析
				gt := parseGormType(gormTag)

				// JSON 列自动检测：type:json 或 type:jsonb
				if strings.Contains(gt, "json") {
					rule.Type = "string"
					rule.Format = "json"
				}

				// type:enum('a','b','c') → 枚举约束
				if enumVals := parseGormEnum(gt); len(enumVals) > 0 {
					rule.Enum = enumVals
				}

				// type:tinyint(1) → bool（MySQL 惯用布尔表示）
				if strings.ReplaceAll(gt, " ", "") == "tinyint(1)" {
					rule.Type = "bool"
				}

				// type:decimal(M,D) → float
				if strings.HasPrefix(gt, "decimal") {
					rule.Type = "float"
				}

				// type:text / longtext / mediumtext → 清除 MaxLength
				if isTextGormType(gt) {
					rule.MaxLength = nil
				}
			}
		}

		// 框架管理字段：永不设为 Required
		if frameworkManagedCols[colName] {
			rule.Required = false
		}

		// is_deleted 字段 → int 类型
		if colName == "is_deleted" {
			rule.Type = "int"
		}

		rules[colName] = rule
	}
	return rules
}

// gormColumn 提取 gorm tag 中的 column 名。
func gormColumn(sf reflect.StructField) string {
	tag := sf.Tag.Get("gorm")
	if tag == "" || tag == "-" {
		// 回退到 json tag
		jsonTag := sf.Tag.Get("json")
		if jsonTag != "" {
			if idx := strings.Index(jsonTag, ","); idx >= 0 {
				jsonTag = jsonTag[:idx]
			}
			return jsonTag
		}
		return common.ToSnakeCase(sf.Name)
	}
	// 解析 gorm:"column:xxx;..."
	if col := common.ExtractGormColumn(tag); col != "" {
		return col
	}
	return common.ToSnakeCase(sf.Name)
}

// rxGormEnum 匹配 gorm 标签中的 type:enum('a','b','c') 部分。
var rxGormEnum = regexp.MustCompile(`(?i)enum\s*\(\s*'([^']*)'(?:\s*,\s*'([^']*)')*\s*\)`)

// parseGormEnum 从 gorm type 字符串（如 "enum('a','b','c')"）提取枚举值列表。
func parseGormEnum(gormType string) []string {
	m := rxGormEnum.FindStringSubmatch(gormType)
	if len(m) < 2 {
		return nil
	}
	var result []string
	for _, v := range m[1:] {
		if v != "" {
			result = append(result, v)
		}
	}
	return result
}

// parseGormType 提取 gorm 标签中的 type: 值（如 "varchar(255)", "enum('a','b')"）。
func parseGormType(tag string) string {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "type:") {
			return strings.TrimPrefix(part, "type:")
		}
	}
	return ""
}

// isTextGormType 判断 gorm type 是否为 text/longtext/mediumtext 等无长度限制的文本类型。
func isTextGormType(gormType string) bool {
	s := strings.ToLower(strings.TrimSpace(gormType))
	return s == "text" || s == "longtext" || s == "mediumtext" || s == "tinytext"
}

// hasGormDefaultNull 判断 gorm 标签是否包含 default:NULL 或 default:null。
func hasGormDefaultNull(tag string) bool {
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if strings.EqualFold(part, "default:NULL") || strings.EqualFold(part, "default:null") {
			return true
		}
	}
	return false
}

// parseGormSize 解析 gorm 标签中的 size 值。
func parseGormSize(tag string) int {
	for _, part := range strings.Split(tag, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if kv[0] == "size" && len(kv) == 2 {
			if n, err := strconv.Atoi(kv[1]); err == nil {
				return n
			}
		}
	}
	return 0
}

// goKindToRuleType 将 reflect.Type 映射为规则类型名。
func goKindToRuleType(t reflect.Type) string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Bool:
		return "bool"
	case reflect.String:
		return "string"
	default:
		if t == reflect.TypeOf(interface{}(any(nil))) {
			return "any"
		}
		return "string" // 兜底
	}
}

// ============================================================
// 3. 类型转换（宽松模式）
// ============================================================

// coerceValue 尝试将 val 转为 targetType 对应的 Go 类型。
// 转换成功返回新值（可能类型已变）；失败返回 error。
// 仅做显式类型转换的字段才调用；无 Type 约束的字段跳过。
func coerceValue(field, targetType string, val any) (any, error) {
	switch targetType {
	case "string":
		return coerceToString(field, val)
	case "int":
		return coerceToInt(field, val)
	case "float":
		return coerceToFloat(field, val)
	case "bool":
		return coerceToBool(field, val)
	case "time":
		return coerceToTime(field, val)
	}
	return val, nil
}

func coerceToString(field string, val any) (any, error) {
	switch v := val.(type) {
	case string:
		return v, nil
	case float64:
		// JSON 数字：123 → "123", 123.5 → "123.5"
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), nil
		}
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case int, int8, int16, int32, int64:
		return fmt.Sprint(v), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(v), nil
	default:
		return nil, errs.ErrFieldValidation(field, "无法转为字符串类型")
	}
}

func coerceToInt(field string, val any) (any, error) {
	switch v := val.(type) {
	case float64:
		// JSON 数字：123.0 → 123, 123.5 → 报错
		if v != float64(int64(v)) {
			return nil, errs.ErrFieldValidation(field, "应为整数")
		}
		return int64(v), nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, errs.ErrFieldValidation(field, "应为整数")
		}
		return n, nil
	case int:
		return int64(v), nil
	case int8, int16, int32, int64:
		return v, nil
	case uint, uint8, uint16, uint32, uint64:
		return v, nil
	default:
		return nil, errs.ErrFieldValidation(field, "应为整数")
	}
}

func coerceToFloat(field string, val any) (any, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, errs.ErrFieldValidation(field, "应为浮点数")
		}
		return f, nil
	case int, int8, int16, int32, int64:
		return float64(reflect.ValueOf(v).Int()), nil
	case uint, uint8, uint16, uint32, uint64:
		return float64(reflect.ValueOf(v).Uint()), nil
	default:
		return nil, errs.ErrFieldValidation(field, "应为浮点数")
	}
}

func coerceToBool(field string, val any) (any, error) {
	switch v := val.(type) {
	case bool:
		return v, nil
	case string:
		s := strings.ToLower(v)
		switch s {
		case "true", "1":
			return true, nil
		case "false", "0":
			return false, nil
		default:
			return nil, errs.ErrFieldValidation(field, "应为布尔值(true/false/1/0)")
		}
	case float64:
		if v == 1 {
			return true, nil
		}
		if v == 0 {
			return false, nil
		}
		return nil, errs.ErrFieldValidation(field, "应为布尔值(true/false/1/0)")
	case int, int8, int16, int32, int64:
		return reflect.ValueOf(v).Int() == 1, nil
	default:
		return nil, errs.ErrFieldValidation(field, "应为布尔值(true/false/1/0)")
	}
}

func coerceToTime(field string, val any) (any, error) {
	switch v := val.(type) {
	case string:
		return v, nil // 保留原始字符串，由 Format 校验具体格式
	case nil:
		return nil, errs.ErrFieldValidation(field, "时间值不能为空")
	default:
		return nil, errs.ErrFieldValidation(field, "应为时间格式字符串")
	}
}

// ============================================================
// 4. 格式校验（内置常见场景）
// ============================================================

// built-in 正则
var (
	rxEmail = regexp.MustCompile(`^[\w.+\-]+@[\w\-]+(\.[\w\-]+)+$`)
	rxPhone = regexp.MustCompile(`^1[3-9]\d{9}$`)
	rxURL   = regexp.MustCompile(`^https?://[\w.\-]+(:\d+)?[\w./?=&%#\-_~]*$`)
	rxULID  = regexp.MustCompile(`(?i)^[0-9A-HJKMNP-TV-Z]{24,26}$`)
)

// timeFormats 尝试解析的常见时间格式。
var timeFormats = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05+08:00",
	"2006/01/02 15:04:05",
	"2006-01-02",
	"2006/01/02",
	"15:04:05",
}

// checkFormat 校验内置格式。
func checkFormat(field, format string, val any) error {
	s := fmt.Sprint(val)
	switch format {
	case "json":
		if !json.Valid([]byte(s)) {
			return errs.ErrFieldValidation(field, "不是有效的JSON格式")
		}
	case "datetime":
		// 尝试多种 datetime 格式
		for _, layout := range []string{
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05.000Z",
			"2006-01-02T15:04:05+08:00",
			"2006/01/02 15:04:05",
		} {
			if _, err := time.Parse(layout, s); err == nil {
				return nil
			}
		}
		return errs.ErrFieldValidation(field, "应为日期时间格式(YYYY-MM-DD HH:MM:SS)")
	case "date":
		for _, layout := range []string{"2006-01-02", "2006/01/02"} {
			if _, err := time.Parse(layout, s); err == nil {
				return nil
			}
		}
		return errs.ErrFieldValidation(field, "应为日期格式(YYYY-MM-DD)")
	case "time":
		if _, err := time.Parse("15:04:05", s); err == nil {
			return nil
		}
		return errs.ErrFieldValidation(field, "应为时间格式(HH:MM:SS)")
	case "email":
		if !rxEmail.MatchString(s) {
			return errs.ErrFieldValidation(field, "邮箱格式不正确")
		}
	case "url":
		if !rxURL.MatchString(s) {
			return errs.ErrFieldValidation(field, "URL格式不正确")
		}
	case "phone":
		if !rxPhone.MatchString(s) {
			return errs.ErrFieldValidation(field, "手机号格式不正确")
		}
	case "ulid":
		if (len(s) < 24 || len(s) > 26) || !rxULID.MatchString(s) {
			return errs.ErrFieldValidation(field, "ULID格式不正确(需24-26位Crockford base32)")
		}
	}
	return nil
}

// ============================================================
// 5. 校验执行
// ============================================================

// validateField 对单个字段按规则执行全部校验：必填 → 类型转换 → 格式 → 数值范围 → 长度 → 枚举 → 正则。
// data 中的值会在类型转换成功后原地替换。返回 nil 表示通过。
func validateField(rule *FieldRule, field string, data map[string]any) error {
	val, exists := data[field]

	// 必填检查（在转换之前，原始值判空）
	if rule.Required && (!exists || isEmpty(val)) {
		return errs.ErrFieldValidation(field, "不能为空")
	}
	if !exists || val == nil {
		return nil
	}
	// 忽略空字符串（非必填时）
	if !rule.Required && isEmpty(val) {
		return nil
	}

	// 类型转换（能转就转，不能转才报错）
	if rule.Type != "" {
		newVal, err := coerceValue(field, rule.Type, val)
		if err != nil {
			return err
		}
		data[field] = newVal // 原地替换
		val = newVal
	}

	// 格式校验（datetime/email/phone/ulid/url/…）
	if rule.Format != "" {
		if err := checkFormat(field, rule.Format, val); err != nil {
			return err
		}
	}

	// 数值范围（int/float 字段）
	if rule.Type == "int" || rule.Type == "float" {
		if err := checkNumericRange(field, rule, val); err != nil {
			return err
		}
	}

	// 字符串长度（所有类型，基于 fmt.Sprint 后长度）
	if rule.MinLength != nil || rule.MaxLength != nil {
		if err := checkStringLength(field, rule, val); err != nil {
			return err
		}
	}

	// 枚举
	if len(rule.Enum) > 0 {
		if err := checkEnum(field, rule.Enum, val); err != nil {
			return err
		}
	}

	// 正则
	if rule.Pattern != "" {
		if err := checkPattern(field, rule.Pattern, val); err != nil {
			return err
		}
	}

	return nil
}

// validateInput 按规则集校验一条记录，同时对值进行类型转换（宽松模式）。
// data 中的值会在校验成功后被原地替换为转换后的类型。
// 返回 nil 表示通过；返回 error 包含具体字段和原因。
//
// rejectUnknown 为 true 时，对 data 中那些不在 rules、非框架控制参数、非 extraAllowedFields 的 key 报错。
// extraAllowedFields 用于声明级联子表数据占位 key（如 Cascades.ChildrenField）。
func validateInput(rules EndpointRules, data map[string]any, endpoint string, rejectUnknown bool, extraAllowedFields ...string) error {
	if rejectUnknown {
		allowed := make(map[string]bool, len(extraAllowedFields))
		for _, f := range extraAllowedFields {
			allowed[f] = true
		}
		for key := range data {
			if isFrameworkMetaParam(key) {
				continue
			}
			if _, ok := rules[key]; ok {
				continue
			}
			if allowed[key] {
				continue
			}
			return errs.ErrFieldValidation(key, "无效字段")
		}
	}

	for field, rule := range rules {
		if err := validateField(rule, field, data); err != nil {
			return err
		}
	}
	return nil
}

// validateInputCollect 与 validateInput 相同，但不返回第一个错误，而是收集所有字段的校验错误。
// 返回 nil 表示全部通过；返回 *BatchErrors 包含每条数据的错误详情。
func validateInputCollect(rules EndpointRules, data map[string]any, endpoint string, index int, rejectUnknown bool, extraAllowedFields ...string) *BatchErrors {
	if rejectUnknown {
		allowed := make(map[string]bool, len(extraAllowedFields))
		for _, f := range extraAllowedFields {
			allowed[f] = true
		}
		for key := range data {
			if isFrameworkMetaParam(key) {
				continue
			}
			if _, ok := rules[key]; ok {
				continue
			}
			if allowed[key] {
				continue
			}
			return &BatchErrors{Errors: []BatchError{
				{Index: index, Field: key, Message: "无效字段"},
			}}
		}
	}

	var batchErrs *BatchErrors
	for field, rule := range rules {
		if err := validateField(rule, field, data); err != nil {
			if batchErrs == nil {
				batchErrs = &BatchErrors{}
			}
			batchErrs.Errors = append(batchErrs.Errors, BatchError{
				Index: index, Field: field, Message: err.Error(),
			})
		}
	}
	return batchErrs
}

// isEmpty 判断值是否为空（nil / 空字符串）。
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	switch x := v.(type) {
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	default:
		return false
	}
}

// checkNumericRange 数值范围检查（val 已做过类型转换）。
func checkNumericRange(field string, rule *FieldRule, val any) error {
	var f float64
	switch v := val.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int64:
		f = float64(v)
	case int:
		f = float64(v)
	case int8:
		f = float64(v)
	case int16:
		f = float64(v)
	case int32:
		f = float64(v)
	case uint, uint8, uint16, uint32, uint64:
		f = float64(reflect.ValueOf(v).Uint())
	case string:
		var err error
		f, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return nil // 类型转换已报错，这里不再重复
		}
	default:
		return nil
	}
	if rule.Min != nil && f < *rule.Min {
		return errs.ErrFieldValidation(field, fmt.Sprintf("不能小于 %v", *rule.Min))
	}
	if rule.Max != nil && f > *rule.Max {
		return errs.ErrFieldValidation(field, fmt.Sprintf("不能大于 %v", *rule.Max))
	}
	return nil
}

// checkStringLength 字符串长度检查（基于 fmt.Sprint 获取字符串表示）。
func checkStringLength(field string, rule *FieldRule, val any) error {
	s := fmt.Sprint(val)
	if rule.MinLength != nil && len(s) < *rule.MinLength {
		return errs.ErrFieldValidation(field, fmt.Sprintf("长度不能小于 %d", *rule.MinLength))
	}
	if rule.MaxLength != nil && len(s) > *rule.MaxLength {
		return errs.ErrFieldValidation(field, fmt.Sprintf("长度不能超过 %d", *rule.MaxLength))
	}
	return nil
}

// checkEnum 枚举值检查（基于 fmt.Sprint 转为字符串比较）。
func checkEnum(field string, allowed []string, val any) error {
	s := fmt.Sprint(val)
	for _, a := range allowed {
		if s == a {
			return nil
		}
	}
	return errs.ErrFieldValidation(field, fmt.Sprintf("值必须在 [%s] 中", strings.Join(allowed, ", ")))
}

// checkPattern 正则检查。
func checkPattern(field, pattern string, val any) error {
	s := fmt.Sprint(val)
	if matched, _ := regexp.MatchString(pattern, s); !matched {
		return errs.ErrFieldValidation(field, "格式不匹配")
	}
	return nil
}

// ============================================================
// 6. 配置合并：自动推导 + 用户覆盖
// ============================================================

// mergeRules 将用户配置的规则覆盖在自动推导的规则之上。
// user 中的字段替换 auto 中的同名字段；仅 user 中显式设置的属性值会覆盖。
func mergeRules(auto, user EndpointRules) EndpointRules {
	if user == nil {
		return auto
	}
	merged := make(EndpointRules, len(auto))
	for k, v := range auto {
		merged[k] = v
	}
	for k, v := range user {
		if existing, ok := merged[k]; ok {
			merged[k] = mergeFieldRule(existing, v)
		} else {
			merged[k] = v
		}
	}
	return merged
}

// mergeFieldRule 将 user 中非零值字段覆盖到 auto 上。
func mergeFieldRule(auto, user *FieldRule) *FieldRule {
	r := &FieldRule{}
	r.Type = pick(user.Type, auto.Type).(string)
	r.Required = user.Required // bool，用户优先
	r.Format = pick(user.Format, auto.Format).(string)
	r.Pattern = pick(user.Pattern, auto.Pattern).(string)
	r.Enum = pick(user.Enum, auto.Enum).([]string)
	if user.Min != nil {
		r.Min = user.Min
	} else {
		r.Min = auto.Min
	}
	if user.Max != nil {
		r.Max = user.Max
	} else {
		r.Max = auto.Max
	}
	if user.MinLength != nil {
		r.MinLength = user.MinLength
	} else {
		r.MinLength = auto.MinLength
	}
	if user.MaxLength != nil {
		r.MaxLength = user.MaxLength
	} else {
		r.MaxLength = auto.MaxLength
	}
	return r
}

// pick 选择 b（user）的零值取 a（auto）。
func pick(b, a any) any {
	switch v := b.(type) {
	case string:
		if v == "" {
			return a
		}
	case []string:
		if len(v) == 0 {
			return a
		}
	}
	return b
}

// ============================================================
// 7. List 参数内置校验
// ============================================================

// defaultListRules 返回 List 接口的框架参数默认规则。
func defaultListRules() EndpointRules {
	return EndpointRules{
		"page":      {Type: "int", Min: Float64Ptr(1)},
		"page_size": {Type: "int", Min: Float64Ptr(1), Max: Float64Ptr(100)},
		"order_dir": {Type: "string", Enum: []string{"asc", "desc"}},
		"depth":     {Type: "int", Min: Float64Ptr(1)},
	}
}

// Float64Ptr 返回 *float64，供构建 ValidateConfig 时使用。
func Float64Ptr(v float64) *float64 { return &v }

// IntPtr 返回 *int，供构建 ValidateConfig 时使用。
func IntPtr(v int) *int { return &v }

func float64Ptr(v float64) *float64 { return &v }
// cloneEndpointRules 深拷贝规则集。
func cloneEndpointRules(src EndpointRules) EndpointRules {
	dst := make(EndpointRules, len(src))
	for k, v := range src {
		dst[k] = cloneFieldRule(v)
	}
	return dst
}

// cloneFieldRule 深拷贝单字段规则。
func cloneFieldRule(r *FieldRule) *FieldRule {
	if r == nil {
		return nil
	}
	nr := &FieldRule{
		Type:     r.Type,
		Required: r.Required,
		Format:   r.Format,
		Pattern:  r.Pattern,
	}
	if r.Min != nil {
		nr.Min = Float64Ptr(*r.Min)
	}
	if r.Max != nil {
		nr.Max = Float64Ptr(*r.Max)
	}
	if r.MinLength != nil {
		nr.MinLength = IntPtr(*r.MinLength)
	}
	if r.MaxLength != nil {
		nr.MaxLength = IntPtr(*r.MaxLength)
	}
	if len(r.Enum) > 0 {
		nr.Enum = make([]string, len(r.Enum))
		copy(nr.Enum, r.Enum)
	}
	return nr
}
