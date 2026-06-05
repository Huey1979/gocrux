package main

import (
	"fmt"
	"regexp"
	"strings"
)

// ============================================================
// 命名工具
// ============================================================

// toPascal 将 snake_case 转为 PascalCase
func toPascal(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// singularize 简单单数化（处理常见复数后缀）
func singularize(s string) string {
	if strings.HasSuffix(s, "ies") && len(s) > 4 { // categories → category
		return s[:len(s)-3] + "y"
	}
	if strings.HasSuffix(s, "ses") && len(s) > 4 { // classes → class
		return s[:len(s)-2]
	}
	if strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 2 {
		return s[:len(s)-1]
	}
	return s
}

// ============================================================
// MySQL 类型 → Go 类型映射
// ============================================================

type goTypeInfo struct {
	GoType   string // 如 string, int, time.Time
	IsTime   bool   // 是否需要 import "time"
	Nullable bool   // 是否需要指针类型
}

// mapColumnType 将 MySQL 列类型映射为 Go 类型
func mapColumnType(col ColumnInfo) goTypeInfo {
	t := strings.ToLower(col.Type)

	// tinyint(1) → bool
	if matched, _ := regexp.MatchString(`^tinyint\(1\)`, t); matched {
		return goTypeInfo{GoType: "bool"}
	}
	if strings.HasPrefix(t, "tinyint") {
		return goTypeInfo{GoType: "int8", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "smallint") {
		return goTypeInfo{GoType: "int16", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "mediumint") {
		return goTypeInfo{GoType: "int", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "int") {
		return goTypeInfo{GoType: "int", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "bigint") {
		return goTypeInfo{GoType: "int64", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "float") || strings.HasPrefix(t, "double") || strings.HasPrefix(t, "decimal") {
		return goTypeInfo{GoType: "float64", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "varchar") || strings.HasPrefix(t, "char") ||
		strings.HasPrefix(t, "text") || strings.HasPrefix(t, "longtext") ||
		strings.HasPrefix(t, "mediumtext") || strings.HasPrefix(t, "tinytext") {
		return goTypeInfo{GoType: "string", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "datetime") || strings.HasPrefix(t, "timestamp") || strings.HasPrefix(t, "date") {
		return goTypeInfo{GoType: "time.Time", IsTime: true, Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "json") {
		return goTypeInfo{GoType: "string", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "enum") || strings.HasPrefix(t, "set") {
		return goTypeInfo{GoType: "string", Nullable: col.IsNullable}
	}
	if strings.HasPrefix(t, "bit") {
		return goTypeInfo{GoType: "bool", Nullable: col.IsNullable}
	}
	// 默认
	return goTypeInfo{GoType: "string", Nullable: col.IsNullable}
}

// isFrameworkField 判断是否为框架约定字段（始终非指针）
func isFrameworkField(name string) bool {
	framework := map[string]bool{
		"created_at": true,
		"updated_at": true,
		"created_by": true,
		"updated_by": true,
		"deleted_at": true,
		"is_deleted": true,
	}
	return framework[name]
}

// goTypeName 返回最终的 Go 类型名（含 * 前缀表示指针）。
// cfg 用于判断列是否为框架字段（含映射别名），以决定是否为指针类型。
func goTypeName(col ColumnInfo, cfg *FieldConfig) string {
	info := mapColumnType(col)
	t := info.GoType

	// is_deleted 字段统一使用 int8（与 heims 约定一致：tinyint(1)→int8 而非 bool）
	isFramework := isFrameworkField(col.Name)
	if cfg != nil {
		if cfg.isFrameworkColumn(col.Name) {
			isFramework = true
			if cfg.FieldMapping.ReverseLookup(col.Name) == "is_deleted" {
				t = "int8"
			}
		}
	} else if col.Name == "is_deleted" {
		// 无 config 时的 fallback：is_deleted 用 int8
		t = "int8"
	}

	if info.Nullable && !isFramework {
		return "*" + t
	}
	return t
}

// ============================================================
// GORM Tag 生成
// ============================================================

// buildGormTag 生成 gorm tag
func buildGormTag(col ColumnInfo) string {
	parts := []string{fmt.Sprintf("column:%s", col.Name)}

	// 主键
	if col.Key == "PRI" {
		parts = append(parts, "primaryKey")
	}
	// 自增
	if strings.Contains(col.Extra, "auto_increment") {
		parts = append(parts, "autoIncrement")
	}
	// 唯一索引
	if col.Key == "UNI" {
		parts = append(parts, "uniqueIndex")
	} else if col.Key == "MUL" {
		parts = append(parts, "index")
	}
	// 非空
	if !col.IsNullable {
		parts = append(parts, "not null")
	}
	// 类型限定（varchar/char → size, text/json → type）
	t := strings.ToLower(col.Type)
	if matched, _ := regexp.MatchString(`^(varchar|char)\((\d+)\)`, t); matched {
		re := regexp.MustCompile(`\((\d+)\)`)
		m := re.FindStringSubmatch(t)
		if len(m) > 1 {
			parts = append(parts, fmt.Sprintf("size:%s", m[1]))
		}
	} else if strings.HasPrefix(t, "text") || strings.HasPrefix(t, "longtext") ||
		strings.HasPrefix(t, "mediumtext") || strings.HasPrefix(t, "tinytext") {
		parts = append(parts, "type:text")
	} else if strings.HasPrefix(t, "json") {
		parts = append(parts, "type:json")
	}
	// 默认值
	if col.Default.Valid {
		parts = append(parts, fmt.Sprintf("default:%s", col.Default.String))
	}

	return strings.Join(parts, ";")
}

// ============================================================
// Entity struct 生成
// ============================================================

// generateEntity 生成实体定义文件内容。cfg 用于字段映射和类型判定。
func generateEntity(table *TableInfo, cfg *FieldConfig) string {
	structName := toPascal(singularize(table.Name))
	var sb strings.Builder

	// 注释
	sb.WriteString("package entity\n\n")
	sb.WriteString("import (\n")
	if hasTimeColumn(table) {
		sb.WriteString("\t\"time\"\n\n")
	}
	sb.WriteString("\t\"github.com/Huey1979/gocrux/common\"\n")
	sb.WriteString(")\n\n")

	// struct 注释
	comment := table.Comment
	if comment == "" {
		comment = table.Name
	}
	sb.WriteString(fmt.Sprintf("// %s %s\n", structName, comment))
	sb.WriteString(fmt.Sprintf("type %s struct {\n", structName))

	for _, col := range table.Columns {
		fieldName := toPascal(col.Name)
		gt := goTypeName(col, cfg)
		gormTag := buildGormTag(col)

		colComment := col.Comment
		line := fmt.Sprintf("\t%s\t%s\t`gorm:\"%s\" json:\"%s\"`", fieldName, gt, gormTag, col.Name)
		if colComment != "" {
			line += fmt.Sprintf("\t// %s", colComment)
		}
		sb.WriteString(line + "\n")
	}

	sb.WriteString("}\n\n")

	// TableName
	sb.WriteString(fmt.Sprintf("func (%s) TableName() string {\n", structName))
	sb.WriteString(fmt.Sprintf("\treturn \"%s\"\n", table.Name))
	sb.WriteString("}\n\n")

	// Record 接口实现
	sb.WriteString(generateRecordImpl(table, structName, cfg))

	return sb.String()
}

// generateRecordImpl 生成 Record 接口实现。
// cfg 用于将框架字段名映射到数据库列名，从而正确检测字段存在性。
func generateRecordImpl(table *TableInfo, structName string, cfg *FieldConfig) string {
	var sb strings.Builder

	pkCol := getPKColumn(table)
	pkField := toPascal(pkCol.Name)
	pkDBName := pkCol.Name
	isAutoInc := strings.Contains(pkCol.Extra, "auto_increment")
	isULID := strings.HasSuffix(pkCol.Name, "_ulid") || strings.HasSuffix(pkCol.Name, "_ulid")

	// 解析映射后的列名
	isDelCol := "is_deleted"
	delAtCol := "deleted_at"
	createdAtCol := "created_at"
	createdByCol := "created_by"
	updatedAtCol := "updated_at"
	updatedByCol := "updated_by"
	if cfg != nil {
		isDelCol = cfg.FieldMapping.Resolve("is_deleted")
		delAtCol = cfg.FieldMapping.Resolve("deleted_at")
		createdAtCol = cfg.FieldMapping.Resolve("created_at")
		createdByCol = cfg.FieldMapping.Resolve("created_by")
		updatedAtCol = cfg.FieldMapping.Resolve("updated_at")
		updatedByCol = cfg.FieldMapping.Resolve("updated_by")
	}

	// ---- SetDefaults ----
	sb.WriteString(fmt.Sprintf("func (m *%s) SetDefaults() {\n", structName))
	for _, col := range table.Columns {
		if col.Default.Valid && col.Default.String != "" {
			fieldName := toPascal(col.Name)
			defVal := formatDefaultValue(col, col.Default.String)
			sb.WriteString(fmt.Sprintf("\tif m.%s == %s {\n\t\tm.%s = %s\n\t}\n",
				fieldName, zeroValue(col), fieldName, defVal))
		}
	}
	sb.WriteString("}\n\n")

	// ---- SetID ----
	sb.WriteString(fmt.Sprintf("func (m *%s) SetID() {\n", structName))
	if isAutoInc {
		sb.WriteString(fmt.Sprintf("\t// %s 自增，由数据库生成\n", pkDBName))
	} else if isULID {
		sb.WriteString(fmt.Sprintf("\tif m.%s == \"\" {\n\t\tm.%s = common.NewULID()\n\t}\n", pkField, pkField))
	} else {
		// 默认为 ULID 模式
		sb.WriteString(fmt.Sprintf("\tif m.%s == \"\" {\n\t\tm.%s = common.NewULID()\n\t}\n", pkField, pkField))
	}
	sb.WriteString("}\n\n")

	// ---- SetCreatedAt ----
	if hasColumn(table, createdAtCol) {
		fieldName := toPascal(createdAtCol)
		sb.WriteString(fmt.Sprintf("func (m *%s) SetCreatedAt(t time.Time) { m.%s = t }\n", structName, fieldName))
	} else {
		sb.WriteString(fmt.Sprintf("func (m *%s) SetCreatedAt(t time.Time) {}\n", structName))
	}
	sb.WriteString("\n")

	// ---- SetCreatedBy ----
	if hasColumn(table, createdByCol) {
		fieldName := toPascal(createdByCol)
		sb.WriteString(fmt.Sprintf("func (m *%s) SetCreatedBy(uid string) { m.%s = uid }\n", structName, fieldName))
	} else {
		sb.WriteString(fmt.Sprintf("func (m *%s) SetCreatedBy(uid string) {}\n", structName))
	}
	sb.WriteString("\n")

	// ---- SetUpdatedAt ----
	if hasColumn(table, updatedAtCol) {
		fieldName := toPascal(updatedAtCol)
		sb.WriteString(fmt.Sprintf("func (m *%s) SetUpdatedAt(t time.Time) { m.%s = t }\n", structName, fieldName))
	} else {
		sb.WriteString(fmt.Sprintf("func (m *%s) SetUpdatedAt(t time.Time) {}\n", structName))
	}
	sb.WriteString("\n")

	// ---- SetUpdatedBy ----
	if hasColumn(table, updatedByCol) {
		fieldName := toPascal(updatedByCol)
		sb.WriteString(fmt.Sprintf("func (m *%s) SetUpdatedBy(uid string) { m.%s = uid }\n", structName, fieldName))
	} else {
		sb.WriteString(fmt.Sprintf("func (m *%s) SetUpdatedBy(uid string) {}\n", structName))
	}
	sb.WriteString("\n")

	// ---- SupportsDraft ----
	hasDraft := hasColumn(table, "version_status") || hasColumn(table, "is_draft")
	if hasDraft {
		sb.WriteString(fmt.Sprintf("func (m *%s) SupportsDraft() bool { return true }\n", structName))
	} else {
		sb.WriteString(fmt.Sprintf("func (m *%s) SupportsDraft() bool { return false }\n", structName))
	}
	sb.WriteString("\n")

	// ---- SetDelete ----
	if hasColumn(table, isDelCol) {
		fieldName := toPascal(isDelCol)
		sb.WriteString(fmt.Sprintf("func (m *%s) SetDelete() bool {\n", structName))
		// 使用 int8 赋值 1（与 heims 约定一致，tinyint(1) 在 ORM 中映射为 int8）
		sb.WriteString(fmt.Sprintf("\tm.%s = 1\n\treturn true\n}\n", fieldName))
	} else if hasColumn(table, delAtCol) {
		fieldName := toPascal(delAtCol)
		sb.WriteString(fmt.Sprintf("func (m *%s) SetDelete() bool {\n", structName))
		sb.WriteString(fmt.Sprintf("\tm.%s = time.Now()\n\treturn true\n}\n", fieldName))
	} else {
		sb.WriteString(fmt.Sprintf("func (m *%s) SetDelete() bool { return false }\n", structName))
	}
	sb.WriteString("\n")

	// ---- PKField ----
	sb.WriteString(fmt.Sprintf("func (m *%s) PKField() string { return \"%s\" }\n", structName, pkDBName))
	sb.WriteString("\n")

	// ---- SelfFKField ----
	// 检测自关联外键（parent_ulid 或 parent_id 列）
	selfFK := ""
	if hasColumn(table, "parent_ulid") {
		selfFK = "parent_ulid"
	} else if hasColumn(table, "parent_id") {
		selfFK = "parent_id"
	}
	sb.WriteString(fmt.Sprintf("func (m *%s) SelfFKField() string { return \"%s\" }\n", structName, selfFK))

	return sb.String()
}

// ============================================================
// Blueprint 生成
// ============================================================

// generateBlueprint 生成注册蓝图文件内容
func generateBlueprint(table *TableInfo) string {
	structName := toPascal(singularize(table.Name))
	regKey := singularize(table.Name) // 注册表 key
	routePrefix := strings.ReplaceAll(table.Name, "_", "-")
	comment := table.Comment
	if comment == "" {
		comment = table.Name
	}

	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`package blueprints

import (
	"github.com/Huey1979/gocrux/handler"
	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"

	"github.com/gin-gonic/gin"
)

// Register%s 注册 %s 的完整 CRUD 路由。
//
// 使用方式:
//
//	blues := NewBlueprints(serviceReg, handlerReg)
//	blues.Register%s(apiGroup)
//
// 同时将以下代码加入 cmd/main.go 的 bootstrap.Migrate() 调用:
//
//	&entity.%s{},
func (b *Blueprints) Register%s(r *gin.RouterGroup) {
	repo := repository.NewCRUDRepository[entity.%s]()
	svc := service.NewGenericService(repo, service.Config[entity.%s]{
		EntityName:             "%s",
		EnableUniqueValidation: true,
		EnableOpLog:            true,
	})

	b.ServiceReg.Register("%s", svc)

	gh := handler.NewGenericHandler[entity.%s](b.ServiceReg, "%s",
		handler.HandlerConfig{PathPrefix: "/api/v1/%s"},
	)
	gh.RegisterRoutes(r)

	b.HandlerReg.Register("%s", gh)
}
`,
		structName, comment,
		structName, structName,
		structName, structName,
		structName, comment,
		regKey,
		structName, regKey, routePrefix,
		regKey,
	))

	return sb.String()
}

// ============================================================
// 辅助函数
// ============================================================

func hasTimeColumn(table *TableInfo) bool {
	for _, col := range table.Columns {
		info := mapColumnType(col)
		if info.IsTime {
			return true
		}
	}
	return false
}

func hasColumn(table *TableInfo, name string) bool {
	for _, col := range table.Columns {
		if col.Name == name {
			return true
		}
	}
	return false
}

func getPKColumn(table *TableInfo) ColumnInfo {
	for _, col := range table.Columns {
		if col.Key == "PRI" {
			return col
		}
	}
	// 无主键时返回第一列
	if len(table.Columns) > 0 {
		return table.Columns[0]
	}
	return ColumnInfo{Name: "id", Type: "int", Extra: "auto_increment"}
}

func formatDefaultValue(col ColumnInfo, def string) string {
	info := mapColumnType(col)
	if info.GoType == "string" {
		return fmt.Sprintf("%q", def)
	}
	if info.GoType == "bool" {
		if def == "1" || strings.ToLower(def) == "true" {
			return "true"
		}
		return "false"
	}
	if info.GoType == "time.Time" {
		// time.Time 的默认值通常是 CURRENT_TIMESTAMP，在 SetDefaults 中跳过
		return def
	}
	// 数值类型
	return def
}

func zeroValue(col ColumnInfo) string {
	info := mapColumnType(col)
	switch info.GoType {
	case "string":
		return `""`
	case "int", "int8", "int16", "int64", "float64":
		return "0"
	case "bool":
		return "false"
	case "time.Time":
		return "time.Time{}"
	default:
		return "nil"
	}
}
